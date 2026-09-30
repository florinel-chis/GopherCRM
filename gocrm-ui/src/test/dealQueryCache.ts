import { createElement, type ReactElement } from 'react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';

// The two pipeline caches a deal change must mark stale. Neither has an
// observer on the pages under test except the board's own pipeline, so an
// invalidation shows up as isInvalidated on the cached entry.
export const DASHBOARD_PIPELINE_KEY = ['dashboard', 'pipeline'] as const;

/**
 * A query client that already holds a dashboard pipeline, as it would after a
 * visit to the dashboard, for tests that check a deal change invalidates it.
 * Render the page inside `withClient(client, ...)`: the inner provider wins
 * over the one the shared test wrapper installs.
 */
export const clientWithDashboardPipeline = (): QueryClient => {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  client.setQueryData(DASHBOARD_PIPELINE_KEY, { stages: [], won_this_month: { count: 0, totals: [] } });
  return client;
};

export const withClient = (client: QueryClient, ui: ReactElement): ReactElement =>
  createElement(QueryClientProvider, { client }, ui);

export const isInvalidated = (client: QueryClient, key: readonly unknown[]): boolean =>
  client.getQueryState(key)?.isInvalidated ?? false;
