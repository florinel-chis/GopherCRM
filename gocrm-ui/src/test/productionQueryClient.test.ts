import { describe, it, expect } from 'vitest';
import { createProductionQueryClient } from './productionQueryClient';
import { APP_QUERY_DEFAULTS } from '@/api/queryDefaults';

describe('createProductionQueryClient', () => {
  it('uses the application query defaults', () => {
    const client = createProductionQueryClient();

    expect(client.getDefaultOptions()).toEqual(APP_QUERY_DEFAULTS);
    expect(client.getDefaultOptions().queries).toMatchObject({
      staleTime: 300000,
      retry: false,
      refetchOnWindowFocus: false,
      refetchOnReconnect: false,
    });
  });

  it('returns an independent cache on every call', () => {
    const first = createProductionQueryClient();
    first.setQueryData(['k'], 1);

    expect(createProductionQueryClient().getQueryData(['k'])).toBeUndefined();
  });
});
