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

import { MiloLink } from '../../../../components/link';
import { getBuildbucketLink } from '../../../../libs/build_utils';

export interface BuildbucketRowProps {
  readonly buildId: string;
}

export function BuildbucketRow({ buildId }: BuildbucketRowProps) {
  return (
    <tr>
      <td>Buildbucket ID:</td>
      <td>
        <MiloLink link={getBuildbucketLink(CONFIGS.BUILDBUCKET.HOST, buildId)} target="_blank" />
      </td>
    </tr>
  );
}
