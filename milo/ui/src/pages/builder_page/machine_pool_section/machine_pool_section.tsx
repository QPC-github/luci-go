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

import { GrpcError, RpcCode } from '@chopsui/prpc-client';
import styled from '@emotion/styled';
import { Alert, AlertTitle, CircularProgress, Link } from '@mui/material';
import { useQuery } from '@tanstack/react-query';
import { useState } from 'react';

import { useAuthState, useGetAccessToken } from '../../../components/auth_state_provider';
import { ExpandableEntry, ExpandableEntryBody, ExpandableEntryHeader } from '../../../components/expandable_entry';
import { PrpcClientExt } from '../../../libs/prpc_client_ext';
import { StringPair } from '../../../services/common';
import { BotsRequest, BotsService, BotStatus, getBotStatus } from '../../../services/swarming';
import { BotStatusTable } from './bot_status_table';
import { BotTable } from './bot_table';

const PAGE_SIZE = 1000;

function useMachinePool(swarmingHost: string, req: BotsRequest) {
  const { identity } = useAuthState();
  const getAccessToken = useGetAccessToken();
  return useQuery({
    queryKey: [identity, swarmingHost, BotsService.SERVICE, 'ListBots', req],
    queryFn: async () => {
      const botsService = new BotsService(new PrpcClientExt({ host: swarmingHost }, getAccessToken));
      const res = await botsService.ListBots(req);
      const bots = res.items?.filter((b) => !b.deleted) || [];

      // TODO(weiweilin): We do not iterate over all pages because that could
      // potentially be very slow and expensive. As a result, the stats is not
      // accurate when there are multiple pages. We should use a `GetStats` RPC
      // when it becomes available.
      const stats = {
        [BotStatus.Idle]: 0,
        [BotStatus.Busy]: 0,
        [BotStatus.Quarantined]: 0,
        [BotStatus.Dead]: 0,
        // Delete bots have been filtered out. Declare it regardless to pass
        // type checking.
        [BotStatus.Deleted]: 0,
      };
      for (const bot of bots) {
        const status = getBotStatus(bot);
        stats[status]++;
      }
      return {
        bots,
        stats,
        hasNextPage: Boolean(res.cursor),
      };
    },
  });
}

const ErrorDisplay = styled.pre({
  whiteSpace: 'pre-wrap',
  overflowWrap: 'break-word',
});

export interface MachinePoolSectionProps {
  readonly swarmingHost: string;
  readonly dimensions: readonly StringPair[];
}

export function MachinePoolSection({ swarmingHost, dimensions }: MachinePoolSectionProps) {
  const [botListExpanded, setBotListExpanded] = useState(false);

  const { data, error, isError, isSuccess, isLoading } = useMachinePool(swarmingHost, {
    limit: PAGE_SIZE,
    dimensions,
  });

  const isPermissionError =
    isError &&
    error instanceof GrpcError &&
    [RpcCode.NOT_FOUND, RpcCode.PERMISSION_DENIED, RpcCode.UNAUTHENTICATED].includes(error.code);
  if (isError && !isPermissionError) {
    throw error;
  }

  return (
    <>
      <h3>
        <Link href={swarmingHost}>Machine Pool</Link>
        {isSuccess && <> ({data.hasNextPage ? `first ${PAGE_SIZE} bots` : data.bots.length})</>}
      </h3>
      {isLoading && <CircularProgress />}
      {isPermissionError && (
        <Alert severity="warning">
          <AlertTitle>You've no permission to view the machine pool</AlertTitle>
          <ErrorDisplay>{`Original Error:\n${error.message}`}</ErrorDisplay>
        </Alert>
      )}
      {isSuccess && (
        <>
          <BotStatusTable stats={data.stats} totalBots={data.bots.length} />
          <ExpandableEntry expanded={botListExpanded}>
            <ExpandableEntryHeader onToggle={setBotListExpanded}>
              Bots: {data.hasNextPage ? `first ${PAGE_SIZE} bots` : data.bots.length}
            </ExpandableEntryHeader>
            <ExpandableEntryBody ruler="none">
              <BotTable swarmingHost={swarmingHost} bots={data.bots} />
            </ExpandableEntryBody>
          </ExpandableEntry>
        </>
      )}
    </>
  );
}
