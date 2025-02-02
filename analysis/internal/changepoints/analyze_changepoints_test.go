// Copyright 2023 The LUCI Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package changepoints

import (
	"context"
	"fmt"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"go.chromium.org/luci/analysis/internal/changepoints/bqexporter"
	"go.chromium.org/luci/analysis/internal/changepoints/inputbuffer"
	"go.chromium.org/luci/analysis/internal/changepoints/sources"
	tu "go.chromium.org/luci/analysis/internal/changepoints/testutil"
	tvbr "go.chromium.org/luci/analysis/internal/changepoints/testvariantbranch"
	"go.chromium.org/luci/analysis/internal/config"
	controlpb "go.chromium.org/luci/analysis/internal/ingestion/control/proto"
	spanutil "go.chromium.org/luci/analysis/internal/span"
	"go.chromium.org/luci/analysis/internal/tasks/taskspb"
	"go.chromium.org/luci/analysis/internal/testutil"
	pb "go.chromium.org/luci/analysis/proto/v1"
	"go.chromium.org/luci/gae/impl/memory"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"
	"go.chromium.org/luci/server/span"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/google/go-cmp/cmp"
	. "github.com/smartystreets/goconvey/convey"
)

type Invocation struct {
	Project              string
	InvocationID         string
	IngestedInvocationID string
}

func TestAnalyzeChangePoint(t *testing.T) {
	exporter, _ := fakeExporter()
	Convey(`Can batch result`, t, func() {
		ctx := newContext(t)
		payload := tu.SamplePayload()
		sourcesMap := tu.SampleSourcesMap(10)
		// 900 test variants should result in 5 batches (1000 each, last one has 500).
		tvs := testVariants(4500)
		err := Analyze(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)

		// Check that there are 5 checkpoints created.
		So(countCheckPoint(ctx), ShouldEqual, 5)
	})

	Convey(`Can skip batch`, t, func() {
		ctx := newContext(t)
		payload := tu.SamplePayload()
		sourcesMap := tu.SampleSourcesMap(10)
		tvs := testVariants(100)
		err := analyzeSingleBatch(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)
		So(countCheckPoint(ctx), ShouldEqual, 1)

		// Analyze the batch again should not throw an error.
		err = analyzeSingleBatch(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)
		So(countCheckPoint(ctx), ShouldEqual, 1)
	})

	Convey(`No commit position should skip`, t, func() {
		ctx := newContext(t)
		payload := tu.SamplePayload()
		sourcesMap := map[string]*rdbpb.Sources{
			"sources_id": {
				GitilesCommit: &rdbpb.GitilesCommit{
					Host:    "host",
					Project: "proj",
					Ref:     "ref",
				},
			},
		}
		tvs := testVariants(100)
		err := Analyze(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)
		So(countCheckPoint(ctx), ShouldEqual, 0)
		So(verdictCounter.Get(ctx, "chromium", "skipped_no_commit_data"), ShouldEqual, 100)
	})

	Convey(`Filter test variant`, t, func() {
		ctx := newContext(t)
		payload := &taskspb.IngestTestResults{
			Build: &controlpb.BuildResult{
				Project: "chromium",
			},
		}

		sourcesMap := map[string]*rdbpb.Sources{
			"sources_id": {
				GitilesCommit: &rdbpb.GitilesCommit{
					Host:     "host",
					Project:  "proj",
					Ref:      "ref",
					Position: 10,
				},
			},
			"sources_id_2": {
				GitilesCommit: &rdbpb.GitilesCommit{
					Host:     "host_2",
					Project:  "proj_2",
					Ref:      "ref_2",
					Position: 10,
				},
				IsDirty: true,
			},
		}
		tvs := []*rdbpb.TestVariant{
			{
				// All skip.
				TestId: "1",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-1/tests/abc",
							Status: rdbpb.TestStatus_SKIP,
						},
					},
				},
				SourcesId: "sources_id",
			},
			{
				// Duplicate.
				TestId: "2",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-2/tests/abc",
							Status: rdbpb.TestStatus_PASS,
						},
					},
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-2/tests/abc",
							Status: rdbpb.TestStatus_FAIL,
						},
					},
				},
				SourcesId: "sources_id",
			},
			{
				// OK.
				TestId: "3",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-3/tests/abc",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id",
			},
			{
				// No source ID.
				TestId: "4",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-4/tests/abc",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id_1",
			},
			{
				// Source is dirty.
				TestId: "5",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-5/tests/abc",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id_2",
			},
		}
		duplicateMap := map[string]bool{
			"inv-2": true,
		}
		tvs, err := filterTestVariants(ctx, tvs, payload, duplicateMap, sourcesMap)
		So(err, ShouldBeNil)
		So(len(tvs), ShouldEqual, 1)
		So(tvs[0].TestId, ShouldEqual, "3")
		So(verdictCounter.Get(ctx, "chromium", "skipped_no_source"), ShouldEqual, 1)
		So(verdictCounter.Get(ctx, "chromium", "skipped_no_commit_data"), ShouldEqual, 1)
		So(verdictCounter.Get(ctx, "chromium", "skipped_all_skipped_or_duplicate"), ShouldEqual, 2)
	})

	Convey(`Filter test variant with failed presubmit`, t, func() {
		ctx := newContext(t)
		payload := &taskspb.IngestTestResults{
			Build: &controlpb.BuildResult{
				Project: "chromium",
			},
			PresubmitRun: &controlpb.PresubmitResult{
				Status: pb.PresubmitRunStatus_PRESUBMIT_RUN_STATUS_FAILED,
				Mode:   pb.PresubmitRunMode_FULL_RUN,
			},
		}

		sourcesMap := tu.SampleSourcesMap(10)
		sourcesMap["sources_id"].Changelists = []*rdbpb.GerritChange{
			{
				Host:     "host",
				Project:  "proj",
				Patchset: 1,
				Change:   12345,
			},
		}
		tvs := []*rdbpb.TestVariant{
			{
				TestId: "1",
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/inv-1/tests/abc",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id",
			},
		}
		duplicateMap := map[string]bool{}
		tvs, err := filterTestVariants(ctx, tvs, payload, duplicateMap, sourcesMap)
		So(err, ShouldBeNil)
		So(len(tvs), ShouldEqual, 0)
		So(verdictCounter.Get(ctx, "chromium", "skipped_unsubmitted_code"), ShouldEqual, 1)
	})
}

func TestAnalyzeSingleBatch(t *testing.T) {
	exporter, client := fakeExporter()
	Convey(`Analyze batch with empty buffer`, t, func() {
		ctx := newContext(t)
		payload := tu.SamplePayload()
		sourcesMap := tu.SampleSourcesMap(10)
		tvs := []*rdbpb.TestVariant{
			{
				TestId:      "test_1",
				VariantHash: "hash_1",
				Variant: &rdbpb.Variant{
					Def: map[string]string{
						"k": "v",
					},
				},
				Status: rdbpb.TestVariantStatus_EXPECTED,
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/abc/tests/xyz",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id",
			},
			{
				TestId:      "test_2",
				VariantHash: "hash_2",
				Variant: &rdbpb.Variant{
					Def: map[string]string{
						"k": "v",
					},
				},
				Status: rdbpb.TestVariantStatus_UNEXPECTED,
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/def/tests/xyz",
							Status: rdbpb.TestStatus_CRASH,
						},
					},
				},
				SourcesId: "sources_id",
			},
		}

		err := analyzeSingleBatch(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)
		So(countCheckPoint(ctx), ShouldEqual, 1)

		// Check invocations.
		invs := fetchInvocations(ctx)
		So(invs, ShouldResemble, []Invocation{
			{
				Project:              "chromium",
				InvocationID:         "abc",
				IngestedInvocationID: "build-1234",
			},
			{
				Project:              "chromium",
				InvocationID:         "def",
				IngestedInvocationID: "build-1234",
			},
		})

		// Check test variant branch.
		tvbs, err := FetchTestVariantBranches(ctx)
		So(err, ShouldBeNil)
		So(len(tvbs), ShouldEqual, 2)

		// Use diff here to compare both protobuf and non-protobuf.
		diff := cmp.Diff(tvbs[0], &tvbr.TestVariantBranch{
			Project:     "chromium",
			TestID:      "test_1",
			VariantHash: "hash_1",
			RefHash:     sources.RefHash(sourcesMap["sources_id"]),
			Variant: &pb.Variant{
				Def: map[string]string{
					"k": "v",
				},
			},
			SourceRef: &pb.SourceRef{
				System: &pb.SourceRef_Gitiles{
					Gitiles: &pb.GitilesRef{
						Host:    "host",
						Project: "proj",
						Ref:     "ref",
					},
				},
			},
			InputBuffer: &inputbuffer.Buffer{
				HotBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{
						{
							CommitPosition:   10,
							IsSimpleExpected: true,
							Hour:             payload.PartitionTime.AsTime(),
						},
					},
				},
				ColdBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{},
				},
				HotBufferCapacity:  inputbuffer.DefaultHotBufferCapacity,
				ColdBufferCapacity: inputbuffer.DefaultColdBufferCapacity,
			},
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		diff = cmp.Diff(tvbs[1], &tvbr.TestVariantBranch{
			Project:     "chromium",
			TestID:      "test_2",
			VariantHash: "hash_2",
			RefHash:     sources.RefHash(sourcesMap["sources_id"]),
			Variant: &pb.Variant{
				Def: map[string]string{
					"k": "v",
				},
			},
			SourceRef: &pb.SourceRef{
				System: &pb.SourceRef_Gitiles{
					Gitiles: &pb.GitilesRef{
						Host:    "host",
						Project: "proj",
						Ref:     "ref",
					},
				},
			},
			InputBuffer: &inputbuffer.Buffer{
				HotBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{
						{
							CommitPosition:   10,
							IsSimpleExpected: false,
							Hour:             payload.PartitionTime.AsTime(),
							Details: inputbuffer.VerdictDetails{
								IsExonerated: false,
								Runs: []inputbuffer.Run{
									{
										UnexpectedResultCount: 1,
									},
								},
							},
						},
					},
				},
				ColdBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{},
				},
				HotBufferCapacity:  inputbuffer.DefaultHotBufferCapacity,
				ColdBufferCapacity: inputbuffer.DefaultColdBufferCapacity,
			},
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		So(len(client.Insertions), ShouldEqual, 2)
		So(verdictCounter.Get(ctx, "chromium", "ingested"), ShouldEqual, 2)
	})

	Convey(`Analyze batch run analysis got change point`, t, func() {
		ctx := newContext(t)
		exporter, client := fakeExporter()
		// Store some existing data in spanner first.
		payload := tu.SamplePayload()
		sourcesMap := tu.SampleSourcesMap(10)

		// Set up the verdicts in spanner.
		positions := make([]int, 2000)
		total := make([]int, 2000)
		hasUnexpected := make([]int, 2000)
		for i := 0; i < 2000; i++ {
			positions[i] = i + 1
			total[i] = 1
			if i >= 100 {
				hasUnexpected[i] = 1
			}
		}
		vs := inputbuffer.Verdicts(positions, total, hasUnexpected)
		tvb := &tvbr.TestVariantBranch{
			IsNew:       true,
			Project:     "chromium",
			TestID:      "test_1",
			VariantHash: "hash_1",
			SourceRef:   sources.SourceRef(sourcesMap["sources_id"]),
			RefHash:     sources.RefHash(sourcesMap["sources_id"]),
			Variant: &pb.Variant{
				Def: map[string]string{
					"k": "v",
				},
			},
			InputBuffer: &inputbuffer.Buffer{
				HotBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{},
				},
				ColdBuffer: inputbuffer.History{
					Verdicts: vs,
				},
				IsColdBufferDirty: true,
			},
		}
		mutation, err := tvb.ToMutation()
		So(err, ShouldBeNil)
		testutil.MustApply(ctx, mutation)

		tvs := []*rdbpb.TestVariant{
			{
				TestId:      "test_1",
				VariantHash: "hash_1",
				Status:      rdbpb.TestVariantStatus_EXPECTED,
				Results: []*rdbpb.TestResultBundle{
					{
						Result: &rdbpb.TestResult{
							Name:   "invocations/abc/tests/xyz",
							Status: rdbpb.TestStatus_PASS,
						},
					},
				},
				SourcesId: "sources_id",
			},
		}

		err = analyzeSingleBatch(ctx, tvs, payload, sourcesMap, exporter)
		So(err, ShouldBeNil)
		So(countCheckPoint(ctx), ShouldEqual, 1)

		// Check invocations.
		invs := fetchInvocations(ctx)
		So(invs, ShouldResemble, []Invocation{
			{
				Project:              "chromium",
				InvocationID:         "abc",
				IngestedInvocationID: "build-1234",
			},
		})

		// Check test variant branch.
		tvbs, err := FetchTestVariantBranches(ctx)
		So(err, ShouldBeNil)
		So(len(tvbs), ShouldEqual, 1)
		tvb = tvbs[0]

		// Use diff here to compare both protobuf and non-protobuf.
		diff := cmp.Diff(tvb, &tvbr.TestVariantBranch{
			Project:     "chromium",
			TestID:      "test_1",
			VariantHash: "hash_1",
			RefHash:     sources.RefHash(sourcesMap["sources_id"]),
			Variant: &pb.Variant{
				Def: map[string]string{
					"k": "v",
				},
			},
			SourceRef: &pb.SourceRef{
				System: &pb.SourceRef_Gitiles{
					Gitiles: &pb.GitilesRef{
						Host:    "host",
						Project: "proj",
						Ref:     "ref",
					},
				},
			},
			InputBuffer: &inputbuffer.Buffer{
				HotBuffer: inputbuffer.History{
					Verdicts: []inputbuffer.PositionVerdict{},
				},
				ColdBuffer: inputbuffer.History{
					Verdicts: vs[100:],
				},
				HotBufferCapacity:  inputbuffer.DefaultHotBufferCapacity,
				ColdBufferCapacity: inputbuffer.DefaultColdBufferCapacity,
			},
			FinalizingSegment: &pb.Segment{
				State:                        pb.SegmentState_FINALIZING,
				HasStartChangepoint:          true,
				StartPosition:                101,
				StartHour:                    timestamppb.New(time.Unix(101*3600, 0)),
				FinalizedCounts:              &pb.Counts{},
				StartPositionLowerBound_99Th: 100,
				StartPositionUpperBound_99Th: 101,
			},
			FinalizedSegments: &pb.Segments{
				Segments: []*pb.Segment{
					{
						State:               pb.SegmentState_FINALIZED,
						HasStartChangepoint: false,
						StartPosition:       1,
						StartHour:           timestamppb.New(time.Unix(3600, 0)),
						EndPosition:         100,
						EndHour:             timestamppb.New(time.Unix(100*3600, 0)),
						FinalizedCounts: &pb.Counts{
							TotalResults:  101,
							TotalRuns:     101,
							TotalVerdicts: 101,
						},
					},
				},
			},
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")
		So(len(client.Insertions), ShouldEqual, 1)
		So(verdictCounter.Get(ctx, "chromium", "ingested"), ShouldEqual, 1)
	})
}

func TestOutOfOrderVerdict(t *testing.T) {
	Convey("Out of order verdict", t, func() {
		sourcesMap := tu.SampleSourcesMap(10)
		sources := sourcesMap["sources_id"]
		Convey("No test variant branch", func() {
			So(isOutOfOrderAndShouldBeDiscarded(nil, sources), ShouldBeFalse)
		})

		Convey("No finalizing or finalized segment", func() {
			tvb := &tvbr.TestVariantBranch{}
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
		})

		Convey("Have finalizing segments", func() {
			tvb := finalizingTvbWithPositions([]int{1}, []int{})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
			tvb = finalizingTvbWithPositions([]int{}, []int{1})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
			tvb = finalizingTvbWithPositions([]int{8, 13}, []int{7, 9})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
			tvb = finalizingTvbWithPositions([]int{11, 15}, []int{6, 8})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
			tvb = finalizingTvbWithPositions([]int{11, 15}, []int{10, 16})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeFalse)
			tvb = finalizingTvbWithPositions([]int{11, 15}, []int{12, 16})
			So(isOutOfOrderAndShouldBeDiscarded(tvb, sources), ShouldBeTrue)
		})
	})
}

func countCheckPoint(ctx context.Context) int {
	st := spanner.NewStatement(`
			SELECT *
			FROM TestVariantBranchCheckPoint
		`)
	it := span.Query(span.Single(ctx), st)
	count := 0
	err := it.Do(func(r *spanner.Row) error {
		count++
		return nil
	})
	So(err, ShouldBeNil)
	return count
}

func fetchInvocations(ctx context.Context) []Invocation {
	st := spanner.NewStatement(`
			SELECT Project, InvocationID, IngestedInvocationID
			FROM Invocations
			ORDER BY InvocationID
		`)
	it := span.Query(span.Single(ctx), st)
	results := []Invocation{}
	err := it.Do(func(r *spanner.Row) error {
		var b spanutil.Buffer
		inv := Invocation{}
		err := b.FromSpanner(r, &inv.Project, &inv.InvocationID, &inv.IngestedInvocationID)
		if err != nil {
			return err
		}
		results = append(results, inv)
		return nil
	})
	So(err, ShouldBeNil)
	return results
}

func testVariants(n int) []*rdbpb.TestVariant {
	tvs := make([]*rdbpb.TestVariant, n)
	for i := 0; i < n; i++ {
		tvs[i] = &rdbpb.TestVariant{
			TestId:      fmt.Sprintf("test_%d", i),
			VariantHash: fmt.Sprintf("hash_%d", i),
			SourcesId:   "sources_id",
		}
	}
	return tvs
}

func finalizingTvbWithPositions(hotPositions []int, coldPositions []int) *tvbr.TestVariantBranch {
	tvb := &tvbr.TestVariantBranch{
		FinalizingSegment: &pb.Segment{},
		InputBuffer:       &inputbuffer.Buffer{},
	}
	for _, pos := range hotPositions {
		tvb.InputBuffer.HotBuffer.Verdicts = append(tvb.InputBuffer.HotBuffer.Verdicts, inputbuffer.PositionVerdict{
			CommitPosition: pos,
		})
	}

	for _, pos := range coldPositions {
		tvb.InputBuffer.ColdBuffer.Verdicts = append(tvb.InputBuffer.ColdBuffer.Verdicts, inputbuffer.PositionVerdict{
			CommitPosition: pos,
		})
	}
	return tvb
}

func newContext(t *testing.T) context.Context {
	ctx := memory.Use(testutil.IntegrationTestContext(t))
	So(config.SetTestConfig(ctx, tu.TestConfig()), ShouldBeNil)
	return ctx
}

func fakeExporter() (*bqexporter.Exporter, *bqexporter.FakeClient) {
	client := bqexporter.NewFakeClient()
	exporter := bqexporter.NewExporter(client)
	return exporter, client
}
