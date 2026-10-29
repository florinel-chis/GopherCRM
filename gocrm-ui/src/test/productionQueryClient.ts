import { QueryClient } from '@tanstack/react-query';
import { APP_QUERY_DEFAULTS } from '@/api/queryDefaults';

/**
 * A query client with the application's own defaults (5-minute staleTime, no
 * retries, no focus or reconnect refetch). Use it for anything that depends on
 * invalidation, polling or refetching: the shared test wrapper's client only
 * disables retries and would let a missing invalidation pass. Render through
 * `withClient(client, ...)` from `./dealQueryCache`.
 */
export const createProductionQueryClient = (): QueryClient =>
  new QueryClient({ defaultOptions: APP_QUERY_DEFAULTS });
