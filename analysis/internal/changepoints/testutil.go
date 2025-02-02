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

	"cloud.google.com/go/spanner"
	tvbr "go.chromium.org/luci/analysis/internal/changepoints/testvariantbranch"
	"go.chromium.org/luci/server/span"
)

func FetchTestVariantBranches(ctx context.Context) ([]*tvbr.TestVariantBranch, error) {
	st := spanner.NewStatement(`
			SELECT Project, TestId, VariantHash, RefHash, Variant, SourceRef, HotInputBuffer, ColdInputBuffer, FinalizingSegment, FinalizedSegments
			FROM TestVariantBranch
			ORDER BY TestId
		`)
	it := span.Query(span.Single(ctx), st)
	results := []*tvbr.TestVariantBranch{}
	err := it.Do(func(r *spanner.Row) error {
		tvb, err := tvbr.SpannerRowToTestVariantBranch(r)
		if err != nil {
			return err
		}
		results = append(results, tvb)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
}
