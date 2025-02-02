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

import { getSafeUrlFromTagValue } from '../libs/build_utils';

export interface BuildTagRowProps {
  readonly tagKey: string;
  readonly tagValue: string;
}

/**
 * Renders a build tag row, include linkify support for some build tags.
 */
export function BuildTagRow({ tagKey, tagValue }: BuildTagRowProps) {
  const url = getSafeUrlFromTagValue(tagValue);

  return (
    <tr>
      <td>{tagKey}:</td>
      <td css={{ clear: 'both', overflowWrap: 'anywhere' }}>
        {url ? (
          <a href={url} target="_blank">
            {tagValue}
          </a>
        ) : (
          <>{tagValue}</>
        )}
      </td>
    </tr>
  );
}
