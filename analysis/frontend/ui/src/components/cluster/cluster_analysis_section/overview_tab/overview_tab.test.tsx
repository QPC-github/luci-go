// Copyright 2022 The LUCI Authors.
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

import '@testing-library/jest-dom';

import fetchMock from 'fetch-mock-jest';

import {
  screen,
  waitFor,
} from '@testing-library/react';

import { ClusterContextProvider } from '@/components/cluster/cluster_context';
import { QueryClusterHistoryResponse } from '@/services/cluster';
import { renderTabWithRouterAndClient } from '@/testing_tools/libs/render_tab';
import { mockFetchAuthState } from '@/testing_tools/mocks/authstate_mock';
import {
  getMockCluster,
  mockGetCluster,
  mockQueryHistory,
} from '@/testing_tools/mocks/cluster_mock';
import { mockFetchMetrics } from '@/testing_tools/mocks/metrics_mock';
import {
  createMockProjectConfigWithThresholds,
  mockFetchProjectConfig,
} from '@/testing_tools/mocks/projects_mock';

import OverviewTab from './overview_tab';

// Mock the window.ResizeObserver that is needed by recharts.
class ResizeObserver {
  // eslint-disable-next-line @typescript-eslint/no-empty-function
  observe() { }
  // eslint-disable-next-line @typescript-eslint/no-empty-function
  unobserve() { }
  // eslint-disable-next-line @typescript-eslint/no-empty-function
  disconnect() { }
}
window.ResizeObserver = ResizeObserver;

describe('Test OverviewTab component', () => {
  beforeEach(() => {
    mockFetchAuthState();
    mockFetchMetrics();
  });

  afterEach(() => {
    fetchMock.mockClear();
    fetchMock.reset();
  });

  it('given a project and cluster ID, should recommend priority and show cluster history for that cluster', async () => {
    const project = 'chrome';
    const algorithm = 'rules'
    const id = '123456';
    const mockCluster = getMockCluster(id, project, algorithm);

    mockGetCluster(project, algorithm, id, mockCluster);
    const mockConfig = createMockProjectConfigWithThresholds();
    mockFetchProjectConfig(mockConfig);

    const history: QueryClusterHistoryResponse = {
      days: [{
        date: '2023-02-16',
        metrics: {
          'human-cls-failed-presubmit': 10,
          'critical-failures-exonerated': 20,
          'test-runs-failed': 100,
        }
      }]
    };
    mockQueryHistory(history);

    renderTabWithRouterAndClient(
      <ClusterContextProvider
        project={project}
        clusterAlgorithm={algorithm}
        clusterId={id}>
        <OverviewTab value='test' />
      </ClusterContextProvider>,
    );

    await waitFor(() => {
      expect(screen.getByTestId('recommended-priority-summary')).toBeInTheDocument();
      expect(screen.getByTestId('history-chart')).toBeInTheDocument();
    });
  });
});
