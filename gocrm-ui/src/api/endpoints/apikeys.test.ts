import { describe, it, expect, vi, beforeEach } from 'vitest';
import { apiKeysApi } from './apikeys';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendKey = {
  id: 1,
  name: 'CI key',
  key_hash: 'should-never-leak',
  user_id: 7,
  is_active: true,
  created_at: '2026-08-01T10:00:00Z',
};

describe('apiKeysApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists keys from the bare array at /api-keys and strips key_hash', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendKey] });

    const result = await apiKeysApi.getAPIKeys();

    expect(api.get).toHaveBeenCalledWith('/api-keys', { params: undefined });
    expect(result.data).toHaveLength(1);
    expect(result.data[0].name).toBe('CI key');
    expect(result.data[0]).not.toHaveProperty('key_hash');
    expect(result.total).toBe(1);
  });

  it('returns an empty page when the list response is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    const result = await apiKeysApi.getAPIKeys();

    expect(result.data).toEqual([]);
    expect(result.total).toBe(0);
  });

  it('reshapes the { key, api_key } creation response into a GeneratedAPIKey', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: { key: 'plaintext-once', api_key: backendKey },
    });

    const result = await apiKeysApi.createAPIKey({ name: 'CI key' });

    expect(api.post).toHaveBeenCalledWith('/api-keys', { name: 'CI key' });
    expect(result.key).toBe('plaintext-once');
    expect(result.name).toBe('CI key');
    expect(result).not.toHaveProperty('key_hash');
  });

  it('fetches and updates a single key at /api-keys/{id}', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: backendKey });
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendKey, is_active: false } });

    const fetched = await apiKeysApi.getAPIKey(1);
    expect(api.get).toHaveBeenCalledWith('/api-keys/1');
    expect(fetched.id).toBe(1);

    const updated = await apiKeysApi.updateAPIKey(1, { is_active: false });
    expect(api.put).toHaveBeenCalledWith('/api-keys/1', { is_active: false });
    expect(updated.is_active).toBe(false);
  });

  it('maps both revoke and delete onto DELETE /api-keys/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await apiKeysApi.revokeAPIKey(3);
    await apiKeysApi.deleteAPIKey(3);

    expect(api.delete).toHaveBeenCalledTimes(2);
    expect(api.delete).toHaveBeenNthCalledWith(1, '/api-keys/3');
    expect(api.delete).toHaveBeenNthCalledWith(2, '/api-keys/3');
  });
});
