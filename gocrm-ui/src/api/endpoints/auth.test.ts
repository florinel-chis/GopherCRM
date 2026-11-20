import { describe, it, expect, vi, beforeEach } from 'vitest';
import { authApi } from './auth';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
  },
  apiClient: {
    changePassword: vi.fn(),
  },
}));

describe('authApi single sign-on calls', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('starts a sign-in with credentials, so the browser keeps the sign-in cookie', async () => {
    const answer = { authorization_url: 'https://idp.example.test/authorize?state=s' };
    vi.mocked(api.post).mockResolvedValue({ data: answer });
    expect(await authApi.startOIDC()).toEqual(answer);
    expect(api.post).toHaveBeenCalledTimes(1);
    expect(api.post).toHaveBeenCalledWith('/auth/oidc/start', undefined, { withCredentials: true });
  });

  it('finishes a sign-in with credentials, so the browser sends the sign-in cookie back', async () => {
    const session = { token: 'jwt', refresh_token: 'refresh', user: { id: 1 } };
    vi.mocked(api.post).mockResolvedValue({ data: session });
    expect(await authApi.oidcCallback('the-code', 'the-state')).toEqual(session);
    expect(api.post).toHaveBeenCalledTimes(1);
    expect(api.post).toHaveBeenCalledWith(
      '/auth/oidc/callback',
      { code: 'the-code', state: 'the-state' },
      { withCredentials: true }
    );
  });

  it('signs in with a password without a request config', async () => {
    const session = { token: 'jwt', refresh_token: 'refresh', user: { id: 1 } };
    vi.mocked(api.post).mockResolvedValue({ data: session });
    await authApi.login({ email: 'user@example.test', password: 'secret' });
    expect(api.post).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.post).mock.calls[0]).toHaveLength(2);
    expect(api.post).toHaveBeenCalledWith('/auth/login', { email: 'user@example.test', password: 'secret' });
  });
});
