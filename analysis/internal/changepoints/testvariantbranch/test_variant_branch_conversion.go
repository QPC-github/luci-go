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

package testvariantbranch

import (
	"sort"

	"go.chromium.org/luci/common/errors"
	rdbpb "go.chromium.org/luci/resultdb/proto/v1"

	"go.chromium.org/luci/analysis/internal/changepoints/inputbuffer"
	"go.chromium.org/luci/analysis/internal/changepoints/sources"
	"go.chromium.org/luci/analysis/internal/ingestion/resultdb"
	"go.chromium.org/luci/analysis/internal/tasks/taskspb"
)

func ToPositionVerdict(tv *rdbpb.TestVariant, payload *taskspb.IngestTestResults, duplicateMap map[string]bool, src *rdbpb.Sources) (inputbuffer.PositionVerdict, error) {
	// It may be enough to check the condition status == expected, given that
	// an expected verdict should have only one expected run.
	// However, we also check the length of the result just to be certain.
	isSimpleExpected := (tv.Status == rdbpb.TestVariantStatus_EXPECTED && len(tv.Results) == 1)

	verdict := inputbuffer.PositionVerdict{
		CommitPosition:   sources.CommitPosition(src),
		IsSimpleExpected: isSimpleExpected,
		Hour:             payload.PartitionTime.AsTime(),
	}

	// Add verdict details only if verdict is not simple.
	if !isSimpleExpected {
		vd, err := toVerdictDetails(tv, duplicateMap)
		if err != nil {
			return inputbuffer.PositionVerdict{}, errors.Annotate(err, "to verdict details").Err()
		}
		verdict.Details = vd
	}
	return verdict, nil
}

// toVerdictDetails converts a test variant to verdict details.
// The runs in verdict details are ordered by:
// - IsDuplicate, in which non-duplicate runs come first, then
// - UnexpectedCount, descendingly, then
// - ExpectedCount, descendingly.
func toVerdictDetails(tv *rdbpb.TestVariant, duplicateMap map[string]bool) (inputbuffer.VerdictDetails, error) {
	isExonerated := (tv.Status == rdbpb.TestVariantStatus_EXONERATED)
	vd := inputbuffer.VerdictDetails{
		IsExonerated: isExonerated,
	}
	// runData maps invocation name to run data.
	runData := map[string]*inputbuffer.Run{}
	for _, r := range tv.Results {
		tr := r.GetResult()
		invocationName, err := resultdb.InvocationFromTestResultName(tr.Name)
		if err != nil {
			return vd, errors.Annotate(err, "invocation from test result name").Err()
		}
		if _, ok := runData[invocationName]; !ok {
			_, isDuplicate := duplicateMap[invocationName]
			runData[invocationName] = &inputbuffer.Run{
				IsDuplicate: isDuplicate,
			}
		}
		if tr.Expected {
			runData[invocationName].ExpectedResultCount++
		} else {
			runData[invocationName].UnexpectedResultCount++
		}
	}

	vd.Runs = make([]inputbuffer.Run, len(runData))
	i := 0
	for _, run := range runData {
		vd.Runs[i] = *run
		i++
	}
	// Sort the run to make a fixed order.
	// Sort by duplicate (non-duplicate first), then by unexpected count (desc),
	// then by expected count.
	sort.Slice(vd.Runs, func(i, j int) bool {
		if !vd.Runs[i].IsDuplicate && vd.Runs[j].IsDuplicate {
			return true
		}
		if vd.Runs[i].IsDuplicate && !vd.Runs[j].IsDuplicate {
			return false
		}
		if vd.Runs[i].UnexpectedResultCount < vd.Runs[j].UnexpectedResultCount {
			return false
		}
		if vd.Runs[i].UnexpectedResultCount > vd.Runs[j].UnexpectedResultCount {
			return true
		}
		return vd.Runs[i].ExpectedResultCount > vd.Runs[j].ExpectedResultCount
	})
	return vd, nil
}
