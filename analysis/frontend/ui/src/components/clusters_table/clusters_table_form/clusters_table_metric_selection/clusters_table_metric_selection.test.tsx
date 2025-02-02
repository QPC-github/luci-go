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

import '@testing-library/jest-dom';

import {
  fireEvent,
  screen,
} from '@testing-library/react';

import { renderWithRouter } from '@/testing_tools/libs/mock_router';
import { getMockMetricsList } from '@/testing_tools/mocks/metrics_mock';

import { ClusterTableContextWrapper } from '@/components/clusters_table/clusters_table_context';

import ClustersTableMetricSelection from './clusters_table_metric_selection';

describe('Test ClusterTableMetricSelection component', () => {
  it('given a list of metrics, should display select items', async () => {
    const metrics = getMockMetricsList();
    renderWithRouter(
        <ClusterTableContextWrapper metrics={metrics}>
          <ClustersTableMetricSelection />
        </ClusterTableContextWrapper>,
    );

    await (screen.findAllByText('Metrics'));

    await fireEvent.mouseDown(screen.getByRole('button'));

    metrics.forEach(((metric) => expect(screen.getByText(metric.humanReadableName)).toBeInTheDocument()));
  });

  it('given a list of selected metrics, then should be values of the list', async () => {
    const metrics = getMockMetricsList();

    const selectedMetrics = [metrics[0].metricId, metrics[1].metricId];
    renderWithRouter(
        <ClusterTableContextWrapper metrics={metrics}>
          <ClustersTableMetricSelection />
        </ClusterTableContextWrapper>,
        `/?selectedMetrics=${selectedMetrics.join(',')}`,
    );

    await (screen.findAllByText('Metrics'));
    expect(screen.getByTestId('clusters-table-metrics-selection')).toHaveValue(selectedMetrics.join(','));
  });
});
