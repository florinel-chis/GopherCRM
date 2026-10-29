import type { DefaultOptions } from '@tanstack/react-query';

// The application's query defaults, shared with the tests that check cache
// behaviour under production settings (createProductionQueryClient) so the
// two cannot drift.
export const APP_QUERY_DEFAULTS: DefaultOptions = {
  queries: {
    staleTime: 1000 * 60 * 5, // 5 minutes
    retry: false, // Disable retries to prevent loading loops
    refetchOnWindowFocus: false,
    refetchOnReconnect: false,
  },
};
