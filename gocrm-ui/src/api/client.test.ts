import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import type { AxiosAdapter, InternalAxiosRequestConfig } from 'axios';
import { api, apiClient, SESSION_KEY, TOKEN_KEY, REFRESH_TOKEN_KEY } from './client';
import { authApi } from './endpoints/auth';

// Regression tests for token storage: a session goes to localStorage only when
// the user opted into "remember me" (persist=true); otherwise it belongs in
// sessionStorage so the session ends with the tab. The session is one record
// (#184); the legacy keys are only mirrors (see client.sessionRecord.test.ts).
describe('ApiClient token storage', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  describe('startSession', () => {
    it('stores the session in localStorage (not sessionStorage) when persist=true', () => {
      apiClient.startSession('persistent-token', 'persistent-refresh', true);

      expect(localStorage.getItem(SESSION_KEY)).not.toBeNull();
      expect(localStorage.getItem(TOKEN_KEY)).toBe('persistent-token');
      expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(apiClient.getToken()).toBe('persistent-token');
      expect(apiClient.getRefreshToken()).toBe('persistent-refresh');
    });

    it('stores the session in sessionStorage only when persist=false (regression: previously always wrote localStorage)', () => {
      apiClient.startSession('session-token', 'session-refresh', false);

      expect(sessionStorage.getItem(TOKEN_KEY)).toBe('session-token');
      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('session-refresh');
      expect(localStorage.getItem(SESSION_KEY)).toBeNull();
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    });

    it('removes the remembered session when switching persist true -> false', () => {
      apiClient.startSession('remembered', 'r1', true);
      apiClient.startSession('not-remembered', 'r2', false);

      expect(localStorage.getItem(SESSION_KEY)).toBeNull();
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      expect(apiClient.getToken()).toBe('not-remembered');
      expect(apiClient.getRefreshToken()).toBe('r2');
    });

    it('removes the tab session when switching persist false -> true', () => {
      apiClient.startSession('not-remembered', 'r1', false);
      apiClient.startSession('remembered', 'r2', true);

      expect(sessionStorage.getItem(SESSION_KEY)).toBeNull();
      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(apiClient.getToken()).toBe('remembered');
    });
  });

  describe('getToken', () => {
    it('returns null when no session is stored anywhere', () => {
      expect(apiClient.getToken()).toBeNull();
      expect(apiClient.getRefreshToken()).toBeNull();
    });
  });

  describe('clearTokens', () => {
    it('removes the record and both legacy keys from BOTH storages', () => {
      apiClient.startSession('local-token', 'local-refresh', true);
      sessionStorage.setItem(SESSION_KEY, 'not even json');
      sessionStorage.setItem(TOKEN_KEY, 'session-token');
      sessionStorage.setItem(REFRESH_TOKEN_KEY, 'session-refresh');

      apiClient.clearTokens();

      for (const storage of [localStorage, sessionStorage]) {
        expect(storage.getItem(SESSION_KEY)).toBeNull();
        expect(storage.getItem(TOKEN_KEY)).toBeNull();
        expect(storage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      }
      expect(apiClient.getToken()).toBeNull();
      expect(apiClient.getRefreshToken()).toBeNull();
    });
  });

  describe('single sign-on logout', () => {
    const originalAdapter = api.defaults.adapter;
    let sent: { url?: string; data?: unknown; authorization?: unknown }[] = [];
    let answer: Record<string, unknown> = { message: 'Logged out successfully' };

    beforeEach(() => {
      sent = [];
      answer = { message: 'Logged out successfully' };
      const adapter: AxiosAdapter = async (config: InternalAxiosRequestConfig) => {
        sent.push({ url: config.url, data: config.data, authorization: config.headers?.Authorization });
        return { data: { success: true, data: answer }, status: 200, statusText: 'OK', headers: {}, config };
      };
      api.defaults.adapter = adapter;
    });
    afterEach(() => {
      api.defaults.adapter = originalAdapter;
    });

    it('startSession keeps the id token of an SSO sign-in in the record', () => {
      apiClient.startSession('sso-access', 'sso-refresh', false, 'sso-id-token');
      expect(JSON.parse(sessionStorage.getItem(SESSION_KEY) ?? '{}')).toMatchObject({ idToken: 'sso-id-token' });
      apiClient.startSession('pw-access', 'pw-refresh', false);
      expect(JSON.parse(sessionStorage.getItem(SESSION_KEY) ?? '{}')).not.toHaveProperty('idToken');
    });

    it('posts {id_token_hint} for an SSO session and returns end_session_url', async () => {
      answer = { message: 'Logged out successfully', end_session_url: 'https://idp.test/logout?id_token_hint=h' };
      await expect(authApi.logout('access-1', 'id-token-1')).resolves.toBe('https://idp.test/logout?id_token_hint=h');
      expect(sent).toHaveLength(1);
      expect(sent[0].url).toBe('/auth/logout');
      expect(JSON.parse(String(sent[0].data))).toEqual({ id_token_hint: 'id-token-1' });
      expect(sent[0].authorization).toBe('Bearer access-1');
    });

    it('posts no body for a password session and returns no URL', async () => {
      await expect(authApi.logout('access-2')).resolves.toBeUndefined();
      expect(sent).toHaveLength(1);
      expect(sent[0].data).toBeUndefined();
    });

    it('an SSO session ends through endSession with its id token as the hint', async () => {
      apiClient.startSession('sso-access', 'sso-refresh', false, 'sso-id-token');
      answer = { message: 'Logged out successfully', end_session_url: 'https://idp.test/logout' };
      await expect(apiClient.endSession((token, idToken) => authApi.logout(token, idToken))).resolves.toBe(
        'https://idp.test/logout'
      );
      expect(JSON.parse(String(sent[0].data))).toEqual({ id_token_hint: 'sso-id-token' });
      expect(apiClient.getToken()).toBeNull();
    });
  });
});
