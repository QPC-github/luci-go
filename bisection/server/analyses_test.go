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

package server

import (
	"context"
	"testing"

	"go.chromium.org/luci/bisection/model"
	pb "go.chromium.org/luci/bisection/proto/v1"
	"go.chromium.org/luci/bisection/util/testutil"

	"github.com/google/go-cmp/cmp"
	. "github.com/smartystreets/goconvey/convey"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	buildbucketpb "go.chromium.org/luci/buildbucket/proto"
	"go.chromium.org/luci/gae/impl/memory"
	"go.chromium.org/luci/gae/service/datastore"
	"go.chromium.org/luci/server/secrets"
)

func TestQueryAnalysis(t *testing.T) {
	t.Parallel()
	server := &AnalysesServer{}
	c := memory.Use(context.Background())
	testutil.UpdateIndices(c)
	datastore.GetTestable(c).AutoIndex(true)

	Convey("No BuildFailure Info", t, func() {
		req := &pb.QueryAnalysisRequest{}
		_, err := server.QueryAnalysis(c, req)
		So(err, ShouldNotBeNil)
		So(status.Convert(err).Code(), ShouldEqual, codes.InvalidArgument)
	})

	Convey("No bbid", t, func() {
		req := &pb.QueryAnalysisRequest{BuildFailure: &pb.BuildFailure{}}
		_, err := server.QueryAnalysis(c, req)
		So(err, ShouldNotBeNil)
		So(status.Convert(err).Code(), ShouldEqual, codes.InvalidArgument)
	})

	Convey("Unsupported step", t, func() {
		req := &pb.QueryAnalysisRequest{
			BuildFailure: &pb.BuildFailure{
				FailedStepName: "some step",
				Bbid:           123,
			},
		}
		_, err := server.QueryAnalysis(c, req)
		So(err, ShouldNotBeNil)
		So(status.Convert(err).Code(), ShouldEqual, codes.Unimplemented)
	})

	Convey("No analysis found", t, func() {
		req := &pb.QueryAnalysisRequest{
			BuildFailure: &pb.BuildFailure{
				FailedStepName: "compile",
				Bbid:           123,
			},
		}
		_, err := server.QueryAnalysis(c, req)
		So(err, ShouldNotBeNil)
		So(status.Convert(err).Code(), ShouldEqual, codes.NotFound)
	})

	Convey("Analysis found", t, func() {
		// Prepares datastore
		failedBuild := &model.LuciFailedBuild{
			Id: 123,
			LuciBuild: model.LuciBuild{
				Project: "chromium/test",
				Bucket:  "ci",
				Builder: "android",
			},
			BuildFailureType: pb.BuildFailureType_COMPILE,
		}
		So(datastore.Put(c, failedBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		compileFailure := &model.CompileFailure{
			Id:    123,
			Build: datastore.KeyForObj(c, failedBuild),
		}
		So(datastore.Put(c, compileFailure), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		suspect := &model.Suspect{
			GitilesCommit: buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Id:      "commit5",
			},
			ReviewUrl:   "http://this/is/review/url",
			ReviewTitle: "This is review title",
			ActionDetails: model.ActionDetails{
				RevertURL:         "https://this/is/revert/review/url",
				IsRevertCreated:   true,
				RevertCreateTime:  (&timestamppb.Timestamp{Seconds: 100}).AsTime(),
				IsRevertCommitted: true,
				RevertCommitTime:  (&timestamppb.Timestamp{Seconds: 200}).AsTime(),
			},
			VerificationStatus: model.SuspectVerificationStatus_ConfirmedCulprit,
			Score:              100,
			Justification:      "Justification",
		}
		So(datastore.Put(c, suspect), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		compileFailureAnalysis := &model.CompileFailureAnalysis{
			CompileFailure:     datastore.KeyForObj(c, compileFailure),
			FirstFailedBuildId: 119,
			InitialRegressionRange: &pb.RegressionRange{
				LastPassed: &buildbucketpb.GitilesCommit{
					Host:    "host1",
					Project: "proj1",
					Ref:     "ref",
					Id:      "commit9",
				},
				FirstFailed: &buildbucketpb.GitilesCommit{
					Host:    "host1",
					Project: "proj1",
					Ref:     "ref",
					Id:      "commit0",
				},
			},
		}
		So(datastore.Put(c, compileFailureAnalysis), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		heuristicAnalysis := &model.CompileHeuristicAnalysis{
			ParentAnalysis: datastore.KeyForObj(c, compileFailureAnalysis),
		}
		So(datastore.Put(c, heuristicAnalysis), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Create nth section analysis
		nsa := &model.CompileNthSectionAnalysis{
			ParentAnalysis: datastore.KeyForObj(c, compileFailureAnalysis),
			Status:         pb.AnalysisStatus_FOUND,
			RunStatus:      pb.AnalysisRunStatus_ENDED,
			StartTime:      (&timestamppb.Timestamp{Seconds: 100}).AsTime(),
			EndTime:        (&timestamppb.Timestamp{Seconds: 102}).AsTime(),
			BlameList:      testutil.CreateBlamelist(10),
		}
		So(datastore.Put(c, nsa), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Create suspect for nthsection
		nthSectionSuspect := &model.Suspect{
			GitilesCommit: buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Id:      "commit6",
			},
			ReviewUrl:          "http://this/is/review/url1",
			ReviewTitle:        "This is review title1",
			VerificationStatus: model.SuspectVerificationStatus_Vindicated,
			ParentAnalysis:     datastore.KeyForObj(c, nsa),
		}
		So(datastore.Put(c, nthSectionSuspect), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Add culprit verification rerun build for suspect
		suspectRerunBuild := &model.CompileRerunBuild{
			Id: 8877665544332211,
			LuciBuild: model.LuciBuild{
				BuildId:    8877665544332211,
				Project:    "chromium",
				Bucket:     "findit",
				Builder:    "luci-bisection-single-revision",
				CreateTime: (&timestamppb.Timestamp{Seconds: 100}).AsTime(),
				StartTime:  (&timestamppb.Timestamp{Seconds: 101}).AsTime(),
				EndTime:    (&timestamppb.Timestamp{Seconds: 102}).AsTime(),
				Status:     buildbucketpb.Status_FAILURE,
				GitilesCommit: buildbucketpb.GitilesCommit{
					Host:    "host1",
					Project: "proj1",
					Ref:     "ref",
					Id:      "commit5",
				},
			},
		}
		So(datastore.Put(c, suspectRerunBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Add culprit verification single rerun for suspect
		suspectSingleRerun := &model.SingleRerun{
			Analysis:   datastore.KeyForObj(c, compileFailureAnalysis),
			Suspect:    datastore.KeyForObj(c, suspect),
			RerunBuild: datastore.KeyForObj(c, suspectRerunBuild),
			Status:     pb.RerunStatus_RERUN_STATUS_FAILED,
			GitilesCommit: buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit5",
			},
			CreateTime: (&timestamppb.Timestamp{Seconds: 100}).AsTime(),
			StartTime:  (&timestamppb.Timestamp{Seconds: 101}).AsTime(),
			EndTime:    (&timestamppb.Timestamp{Seconds: 102}).AsTime(),
			Type:       model.RerunBuildType_CulpritVerification,
			Priority:   100,
		}
		So(datastore.Put(c, suspectSingleRerun), ShouldBeNil)

		// Add culprit verification rerun build for parent of suspect
		parentRerunBuild := &model.CompileRerunBuild{
			Id: 7766554433221100,
			LuciBuild: model.LuciBuild{
				BuildId:    7766554433221100,
				Project:    "chromium",
				Bucket:     "findit",
				Builder:    "luci-bisection-single-revision",
				CreateTime: (&timestamppb.Timestamp{Seconds: 200}).AsTime(),
				StartTime:  (&timestamppb.Timestamp{Seconds: 201}).AsTime(),
				EndTime:    (&timestamppb.Timestamp{Seconds: 202}).AsTime(),
				Status:     buildbucketpb.Status_SUCCESS,
				GitilesCommit: buildbucketpb.GitilesCommit{
					Host:    "host1",
					Project: "proj1",
					Ref:     "ref",
					Id:      "commit6",
				},
			},
		}
		So(datastore.Put(c, parentRerunBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Add culprit verification single rerun for parent of suspect
		parentSingleRerun := &model.SingleRerun{
			Analysis:   datastore.KeyForObj(c, compileFailureAnalysis),
			Suspect:    datastore.KeyForObj(c, suspect),
			RerunBuild: datastore.KeyForObj(c, parentRerunBuild),
			Status:     pb.RerunStatus_RERUN_STATUS_PASSED,
			GitilesCommit: buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit6",
			},
			CreateTime: (&timestamppb.Timestamp{Seconds: 200}).AsTime(),
			StartTime:  (&timestamppb.Timestamp{Seconds: 201}).AsTime(),
			EndTime:    (&timestamppb.Timestamp{Seconds: 202}).AsTime(),
			Type:       model.RerunBuildType_CulpritVerification,
			Priority:   100,
		}
		So(datastore.Put(c, parentSingleRerun), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// Update suspect's culprit verification results
		suspect.ParentAnalysis = datastore.KeyForObj(c, heuristicAnalysis)
		suspect.VerificationStatus = model.SuspectVerificationStatus_ConfirmedCulprit
		suspect.SuspectRerunBuild = datastore.KeyForObj(c, suspectRerunBuild)
		suspect.ParentRerunBuild = datastore.KeyForObj(c, parentRerunBuild)
		So(datastore.Put(c, suspect), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		compileFailureAnalysis.VerifiedCulprits = []*datastore.Key{
			datastore.KeyForObj(c, suspect),
		}
		So(datastore.Put(c, compileFailureAnalysis), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		// nth section rerun
		rrBuild := &model.CompileRerunBuild{
			Id: 800999000,
		}
		So(datastore.Put(c, rrBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		nthSectionRerun := &model.SingleRerun{
			Analysis:           datastore.KeyForObj(c, compileFailureAnalysis),
			NthSectionAnalysis: datastore.KeyForObj(c, nsa),
			Status:             pb.RerunStatus_RERUN_STATUS_PASSED,
			GitilesCommit: buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit8",
			},
			RerunBuild: datastore.KeyForObj(c, rrBuild),
			CreateTime: (&timestamppb.Timestamp{Seconds: 300}).AsTime(),
			StartTime:  (&timestamppb.Timestamp{Seconds: 301}).AsTime(),
			EndTime:    (&timestamppb.Timestamp{Seconds: 302}).AsTime(),
			Type:       model.RerunBuildType_NthSection,
			Priority:   100,
		}

		So(datastore.Put(c, nthSectionRerun), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		req := &pb.QueryAnalysisRequest{
			BuildFailure: &pb.BuildFailure{
				FailedStepName: "compile",
				Bbid:           123,
			},
		}

		res, err := server.QueryAnalysis(c, req)
		So(err, ShouldBeNil)
		So(len(res.Analyses), ShouldEqual, 1)

		analysis := res.Analyses[0]
		So(analysis.Builder, ShouldResemble, &buildbucketpb.BuilderID{
			Project: "chromium/test",
			Bucket:  "ci",
			Builder: "android",
		})
		So(analysis.BuildFailureType, ShouldEqual, pb.BuildFailureType_COMPILE)
		So(len(analysis.Culprits), ShouldEqual, 1)
		So(proto.Equal(analysis.Culprits[0], &pb.Culprit{
			Commit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Id:      "commit5",
			},
			ReviewUrl:   "http://this/is/review/url",
			ReviewTitle: "This is review title",
			CulpritAction: []*pb.CulpritAction{
				{
					ActionType:  pb.CulpritActionType_CULPRIT_AUTO_REVERTED,
					RevertClUrl: "https://this/is/revert/review/url",
					ActionTime:  &timestamppb.Timestamp{Seconds: 200},
				},
			},
			VerificationDetails: &pb.SuspectVerificationDetails{
				Status: string(model.SuspectVerificationStatus_ConfirmedCulprit),
				SuspectRerun: &pb.SingleRerun{
					Bbid:      8877665544332211,
					StartTime: &timestamppb.Timestamp{Seconds: 101},
					EndTime:   &timestamppb.Timestamp{Seconds: 102},
					RerunResult: &pb.RerunResult{
						RerunStatus: pb.RerunStatus_RERUN_STATUS_FAILED,
					},
					Commit: &buildbucketpb.GitilesCommit{
						Host:    "host1",
						Project: "proj1",
						Ref:     "ref",
						Id:      "commit5",
					},
				},
				ParentRerun: &pb.SingleRerun{
					Bbid:      7766554433221100,
					StartTime: &timestamppb.Timestamp{Seconds: 201},
					EndTime:   &timestamppb.Timestamp{Seconds: 202},
					RerunResult: &pb.RerunResult{
						RerunStatus: pb.RerunStatus_RERUN_STATUS_PASSED,
					},
					Commit: &buildbucketpb.GitilesCommit{
						Host:    "host1",
						Project: "proj1",
						Ref:     "ref",
						Id:      "commit6",
					},
				},
			},
		}), ShouldBeTrue)

		So(len(analysis.HeuristicResult.Suspects), ShouldEqual, 1)
		So(proto.Equal(analysis.HeuristicResult.Suspects[0], &pb.HeuristicSuspect{
			GitilesCommit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Id:      "commit5",
			},
			ReviewUrl:       "http://this/is/review/url",
			ReviewTitle:     "This is review title",
			Score:           100,
			Justification:   "Justification",
			ConfidenceLevel: pb.SuspectConfidenceLevel_HIGH,
			VerificationDetails: &pb.SuspectVerificationDetails{
				Status: string(model.SuspectVerificationStatus_ConfirmedCulprit),
				SuspectRerun: &pb.SingleRerun{
					Bbid:      8877665544332211,
					StartTime: &timestamppb.Timestamp{Seconds: 101},
					EndTime:   &timestamppb.Timestamp{Seconds: 102},
					RerunResult: &pb.RerunResult{
						RerunStatus: pb.RerunStatus_RERUN_STATUS_FAILED,
					},
					Commit: &buildbucketpb.GitilesCommit{
						Host:    "host1",
						Project: "proj1",
						Ref:     "ref",
						Id:      "commit5",
					},
				},
				ParentRerun: &pb.SingleRerun{
					Bbid:      7766554433221100,
					StartTime: &timestamppb.Timestamp{Seconds: 201},
					EndTime:   &timestamppb.Timestamp{Seconds: 202},
					RerunResult: &pb.RerunResult{
						RerunStatus: pb.RerunStatus_RERUN_STATUS_PASSED,
					},
					Commit: &buildbucketpb.GitilesCommit{
						Host:    "host1",
						Project: "proj1",
						Ref:     "ref",
						Id:      "commit6",
					},
				},
			},
		}), ShouldBeTrue)

		nthSectionResult := analysis.NthSectionResult
		So(nthSectionResult, ShouldNotBeNil)
		So(proto.Equal(nthSectionResult.StartTime, &timestamppb.Timestamp{Seconds: 100}), ShouldBeTrue)
		So(proto.Equal(nthSectionResult.EndTime, &timestamppb.Timestamp{Seconds: 102}), ShouldBeTrue)
		So(nthSectionResult.Status, ShouldEqual, pb.AnalysisStatus_FOUND)
		diff := cmp.Diff(nthSectionResult.Suspect, &pb.NthSectionSuspect{
			GitilesCommit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Id:      "commit6",
			},
			ReviewUrl:   "http://this/is/review/url1",
			ReviewTitle: "This is review title1",
			VerificationDetails: &pb.SuspectVerificationDetails{
				Status: string(model.SuspectVerificationStatus_Vindicated),
			},
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		diff = cmp.Diff(nthSectionResult.RemainingNthSectionRange, &pb.RegressionRange{
			LastPassed: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit5",
			},
			FirstFailed: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit5",
			},
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		So(len(nthSectionResult.Reruns), ShouldEqual, 3)

		diff = cmp.Diff(nthSectionResult.Reruns[0], &pb.SingleRerun{
			Bbid:      8877665544332211,
			StartTime: &timestamppb.Timestamp{Seconds: 101},
			EndTime:   &timestamppb.Timestamp{Seconds: 102},
			RerunResult: &pb.RerunResult{
				RerunStatus: pb.RerunStatus_RERUN_STATUS_FAILED,
			},
			Commit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit5",
			},
			Index: "5",
			Type:  "Culprit Verification",
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		diff = cmp.Diff(nthSectionResult.Reruns[1], &pb.SingleRerun{
			Bbid:      7766554433221100,
			StartTime: &timestamppb.Timestamp{Seconds: 201},
			EndTime:   &timestamppb.Timestamp{Seconds: 202},
			RerunResult: &pb.RerunResult{
				RerunStatus: pb.RerunStatus_RERUN_STATUS_PASSED,
			},
			Commit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit6",
			},
			Index: "6",
			Type:  "Culprit Verification",
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		diff = cmp.Diff(nthSectionResult.Reruns[2], &pb.SingleRerun{
			Bbid:      800999000,
			StartTime: &timestamppb.Timestamp{Seconds: 301},
			EndTime:   &timestamppb.Timestamp{Seconds: 302},
			RerunResult: &pb.RerunResult{
				RerunStatus: pb.RerunStatus_RERUN_STATUS_PASSED,
			},
			Commit: &buildbucketpb.GitilesCommit{
				Host:    "host1",
				Project: "proj1",
				Ref:     "ref",
				Id:      "commit8",
			},
			Index: "8",
			Type:  "NthSection",
		}, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")

		diff = cmp.Diff(nthSectionResult.BlameList, nsa.BlameList, cmp.Comparer(proto.Equal))
		So(diff, ShouldEqual, "")
	})

	Convey("Analysis found for a similar failure", t, func() {
		// Prepares datastore
		basedFailedBuild := &model.LuciFailedBuild{
			Id: 122,
		}
		So(datastore.Put(c, basedFailedBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		basedCompileFailure := &model.CompileFailure{
			Id:    122,
			Build: datastore.KeyForObj(c, basedFailedBuild),
		}
		So(datastore.Put(c, basedCompileFailure), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		failedBuild := &model.LuciFailedBuild{
			Id: 123,
		}
		So(datastore.Put(c, failedBuild), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		compileFailure := &model.CompileFailure{
			Id:               123,
			Build:            datastore.KeyForObj(c, failedBuild),
			MergedFailureKey: datastore.KeyForObj(c, basedCompileFailure),
		}
		So(datastore.Put(c, compileFailure), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		compileFailureAnalysis := &model.CompileFailureAnalysis{
			CompileFailure: datastore.KeyForObj(c, basedCompileFailure),
		}
		So(datastore.Put(c, compileFailureAnalysis), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		req := &pb.QueryAnalysisRequest{
			BuildFailure: &pb.BuildFailure{
				FailedStepName: "compile",
				Bbid:           123,
			},
		}

		res, err := server.QueryAnalysis(c, req)
		So(err, ShouldBeNil)
		So(len(res.Analyses), ShouldEqual, 1)
	})

}

func TestListAnalyses(t *testing.T) {
	t.Parallel()
	server := &AnalysesServer{}

	Convey("List existing analyses", t, func() {
		// Set up context and AEAD so that page tokens can be generated
		c := memory.Use(context.Background())
		c = secrets.GeneratePrimaryTinkAEADForTest(c)

		// Prepares datastore
		failureAnalysis1 := &model.CompileFailureAnalysis{
			Id:         1,
			CreateTime: (&timestamppb.Timestamp{Seconds: 100}).AsTime(),
		}
		failureAnalysis2 := &model.CompileFailureAnalysis{
			Id:         2,
			CreateTime: (&timestamppb.Timestamp{Seconds: 102}).AsTime(),
		}
		failureAnalysis3 := &model.CompileFailureAnalysis{
			Id:         3,
			CreateTime: (&timestamppb.Timestamp{Seconds: 101}).AsTime(),
		}
		failureAnalysis4 := &model.CompileFailureAnalysis{
			Id:         4,
			CreateTime: (&timestamppb.Timestamp{Seconds: 103}).AsTime(),
		}
		So(datastore.Put(c, failureAnalysis1), ShouldBeNil)
		So(datastore.Put(c, failureAnalysis2), ShouldBeNil)
		So(datastore.Put(c, failureAnalysis3), ShouldBeNil)
		So(datastore.Put(c, failureAnalysis4), ShouldBeNil)
		datastore.GetTestable(c).CatchupIndexes()

		Convey("Invalid page size", func() {
			req := &pb.ListAnalysesRequest{
				PageSize: -5,
			}
			_, err := server.ListAnalyses(c, req)
			So(err, ShouldNotBeNil)
			So(status.Convert(err).Code(), ShouldEqual, codes.InvalidArgument)
		})

		Convey("Specifying page size is optional", func() {
			req := &pb.ListAnalysesRequest{}
			res, err := server.ListAnalyses(c, req)
			So(err, ShouldBeNil)
			So(len(res.Analyses), ShouldEqual, 4)

			Convey("Next page token is empty if there are no more analyses", func() {
				So(res.NextPageToken, ShouldEqual, "")
			})
		})

		Convey("Response is limited by the page size", func() {
			req := &pb.ListAnalysesRequest{
				PageSize: 3,
			}
			res, err := server.ListAnalyses(c, req)
			So(err, ShouldBeNil)
			So(len(res.Analyses), ShouldEqual, req.PageSize)
			So(res.NextPageToken, ShouldNotEqual, "")

			Convey("Returned analyses are sorted correctly", func() {
				So(res.Analyses[0].AnalysisId, ShouldEqual, 4)
				So(res.Analyses[1].AnalysisId, ShouldEqual, 2)
				So(res.Analyses[2].AnalysisId, ShouldEqual, 3)

				Convey("Page token will get the next page of analyses", func() {
					req = &pb.ListAnalysesRequest{
						PageSize:  3,
						PageToken: res.NextPageToken,
					}
					res, err = server.ListAnalyses(c, req)
					So(err, ShouldBeNil)
					So(len(res.Analyses), ShouldEqual, 1)
					So(res.Analyses[0].AnalysisId, ShouldEqual, 1)
				})
			})
		})
	})
}
