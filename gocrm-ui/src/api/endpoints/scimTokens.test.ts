import { describe, it, expect, vi, beforeEach } from 'vitest';
import { scimTokensApi } from './scimTokens';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    delete: vi.fn(),
  },
}));

const stored = {
  id: 3,
  name: 'Entra ID',
  created_at: '2026-11-20T09:00:00Z',
  last_used_at: null,
  revoked_at: null,
};

describe('scimTokensApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists the tokens with GET /settings/scim/tokens', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [stored] });
    expect(await scimTokensApi.list()).toEqual([stored]);
    expect(api.get).toHaveBeenCalledWith('/settings/scim/tokens');
  });

  it('answers an empty list when the data is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: null });
    expect(await scimTokensApi.list()).toEqual([]);
  });

  it('creates a token with POST /settings/scim/tokens and the name as the body', async () => {
    const created = { token: `scim_${'a'.repeat(64)}`, scim_token: stored };
    vi.mocked(api.post).mockResolvedValue({ data: created });
    expect(await scimTokensApi.create('Entra ID')).toEqual(created);
    expect(api.post).toHaveBeenCalledWith('/settings/scim/tokens', { name: 'Entra ID' });
  });

  it('revokes a token with DELETE /settings/scim/tokens/{id}', async () => {
    const revoked = { ...stored, revoked_at: '2026-11-21T09:00:00Z' };
    vi.mocked(api.delete).mockResolvedValue({ data: revoked });
    expect(await scimTokensApi.revoke(3)).toEqual(revoked);
    expect(api.delete).toHaveBeenCalledWith('/settings/scim/tokens/3');
  });
});
