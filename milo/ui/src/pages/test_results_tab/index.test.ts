// Copyright 2021 The LUCI Authors.
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

import { afterEach, beforeEach, expect, jest } from '@jest/globals';
import { aTimeout, fixture, fixtureCleanup } from '@open-wc/testing-helpers';
import { html, LitElement } from 'lit';
import { customElement } from 'lit/decorators.js';
import { computed } from 'mobx';
import { destroy } from 'mobx-state-tree';

import '.';
import { ANONYMOUS_IDENTITY } from '../../libs/auth_state';
import { CacheOption } from '../../libs/cached_fn';
import { provider } from '../../libs/context';
import { timeout } from '../../libs/utils';
import { QueryTestVariantsRequest, QueryTestVariantsResponse, TestVariantStatus } from '../../services/resultdb';
import { provideStore, Store, StoreInstance } from '../../store';
import { provideInvocationState } from '../../store/invocation_state';
import { TestResultsTabElement } from '.';

const variant1 = {
  testId: 'a',
  variant: { def: { key1: 'val1' } },
  variantHash: 'key1:val1',
  status: TestVariantStatus.UNEXPECTED,
};

const variant2 = {
  testId: 'a',
  variant: { def: { key1: 'val2' } },
  variantHash: 'key1:val2',
  status: TestVariantStatus.UNEXPECTED,
};

const variant3 = {
  testId: 'a',
  variant: { def: { key1: 'val3' } },
  variantHash: 'key1:val3',
  status: TestVariantStatus.UNEXPECTED,
};

const variant4 = {
  testId: 'b',
  variant: { def: { key1: 'val2' } },
  variantHash: 'key1:val2',
  status: TestVariantStatus.FLAKY,
};

const variant5 = {
  testId: 'c',
  variant: { def: { key1: 'val2', key2: 'val1' } },
  variantHash: 'key1:val2|key2:val1',
  status: TestVariantStatus.FLAKY,
};

@customElement('milo-test-context-provider')
@provider
class ContextProvider extends LitElement {
  @provideStore()
  store!: StoreInstance;

  @provideInvocationState()
  @computed
  get invocationState() {
    return this.store.invocationPage.invocation;
  }
}

describe('TestResultsTab', () => {
  let store: StoreInstance;

  afterEach(() => {
    fixtureCleanup();
    destroy(store);
  });

  it('should load the first page of test variants when connected', async () => {
    store = Store.create({
      authState: { value: { identity: ANONYMOUS_IDENTITY } },
      invocationPage: { invocationId: 'invocation-id' },
    });
    const queryTestVariantsStub = jest.spyOn(store.services.resultDb!, 'queryTestVariants');
    queryTestVariantsStub.mockResolvedValueOnce({
      testVariants: [variant1, variant2, variant3],
      nextPageToken: 'next',
    });
    queryTestVariantsStub.mockResolvedValueOnce({ testVariants: [variant4, variant5] });
    const getInvocationStub = jest.spyOn(store.services.resultDb!, 'getInvocation');
    getInvocationStub.mockReturnValue(Promise.race([]));

    const provider = await fixture<ContextProvider>(html`
      <milo-test-context-provider .store=${store}>
        <milo-test-results-tab></milo-test-results-tab>
      </milo-test-context-provider>
    `);
    const tab = provider.querySelector<TestResultsTabElement>('milo-test-results-tab')!;

    expect(queryTestVariantsStub.mock.calls.length).toStrictEqual(1);

    tab.disconnectedCallback();
    tab.connectedCallback();

    await aTimeout(0);
    expect(store.invocationPage.invocation.testLoader?.isLoading).toStrictEqual(false);
    expect(queryTestVariantsStub.mock.calls.length).toStrictEqual(1);
  });
});

describe('loadNextTestVariants', () => {
  let queryTestVariantsStub: jest.SpiedFunction<
    (req: QueryTestVariantsRequest, cacheOpt?: CacheOption) => Promise<QueryTestVariantsResponse>
  >;
  let store: StoreInstance;
  let tab: TestResultsTabElement;

  beforeEach(async () => {
    jest.useFakeTimers();
    store = Store.create({
      authState: { value: { identity: ANONYMOUS_IDENTITY } },
      invocationPage: { invocationId: 'invocation-id' },
    });

    queryTestVariantsStub = jest.spyOn(store.services.resultDb!, 'queryTestVariants');
    queryTestVariantsStub.mockImplementationOnce(async () => {
      await timeout(100);
      return { testVariants: [variant1], nextPageToken: 'next0' };
    });
    queryTestVariantsStub.mockImplementationOnce(async () => {
      await timeout(100);
      return { testVariants: [variant2], nextPageToken: 'next1' };
    });

    const getInvocationStub = jest.spyOn(store.services.resultDb!, 'getInvocation');
    getInvocationStub.mockReturnValue(Promise.race([]));

    const provider = await fixture<ContextProvider>(html`
      <milo-test-context-provider .store=${store}>
        <milo-test-results-tab></milo-test-results-tab>
      </milo-test-context-provider>
    `);
    tab = provider.querySelector<TestResultsTabElement>('milo-test-results-tab')!;
  });

  afterEach(() => {
    fixtureCleanup();
    destroy(store);
    const url = `${window.location.protocol}//${window.location.host}${window.location.pathname}`;
    window.history.replaceState(null, '', url);
    jest.useRealTimers();
  });

  it('should trigger automatic loading when visiting the tab for the first time', async () => {
    expect(store.invocationPage.invocation.testLoader?.isLoading).toStrictEqual(true);
    expect(queryTestVariantsStub.mock.calls.length).toStrictEqual(1);

    await jest.runOnlyPendingTimersAsync();
    expect(store.invocationPage.invocation.testLoader?.isLoading).toStrictEqual(false);
    expect(queryTestVariantsStub.mock.calls.length).toStrictEqual(1);

    // Disconnect, then reload the tab.
    tab.disconnectedCallback();
    await fixture<ContextProvider>(html`
      <milo-test-context-provider .store=${store}>
        <milo-test-results-tab></milo-test-results-tab>
      </milo-test-context-provider>
    `);

    // Should not trigger loading agin.
    expect(store.invocationPage.invocation.testLoader?.isLoading).toStrictEqual(false);
    expect(queryTestVariantsStub.mock.calls.length).toStrictEqual(1);
  });
});
