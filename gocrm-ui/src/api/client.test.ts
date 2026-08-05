import { describe, it, expect, beforeEach } from 'vitest';
import { apiClient, TOKEN_KEY, REFRESH_TOKEN_KEY } from './client';

// Regression tests for token storage: tokens must only be written to
// localStorage when the user opted into "remember me" (persist=true);
// otherwise they belong in sessionStorage so the session ends with the tab.
describe('ApiClient token storage', () => {
  beforeEach(() => {
    localStorage.clear();
    sessionStorage.clear();
  });

  describe('setToken', () => {
    it('stores the token in localStorage (not sessionStorage) when persist=true', () => {
      apiClient.setToken('persistent-token', true);

      expect(localStorage.getItem(TOKEN_KEY)).toBe('persistent-token');
      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    });

    it('stores the token in sessionStorage only when persist=false (regression: previously always wrote localStorage)', () => {
      apiClient.setToken('session-token', false);

      expect(sessionStorage.getItem(TOKEN_KEY)).toBe('session-token');
      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
    });

    it('defaults to persist=true when the persist argument is omitted', () => {
      apiClient.setToken('default-token');

      expect(localStorage.getItem(TOKEN_KEY)).toBe('default-token');
      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
    });

    it('removes the stale localStorage copy when switching persist true -> false', () => {
      apiClient.setToken('remembered', true);
      apiClient.setToken('not-remembered', false);

      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(sessionStorage.getItem(TOKEN_KEY)).toBe('not-remembered');
      expect(apiClient.getToken()).toBe('not-remembered');
    });

    it('removes the stale sessionStorage copy when switching persist false -> true', () => {
      apiClient.setToken('not-remembered', false);
      apiClient.setToken('remembered', true);

      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(localStorage.getItem(TOKEN_KEY)).toBe('remembered');
      expect(apiClient.getToken()).toBe('remembered');
    });
  });

  describe('getToken', () => {
    it('prefers sessionStorage when both storages hold a token', () => {
      localStorage.setItem(TOKEN_KEY, 'local-token');
      sessionStorage.setItem(TOKEN_KEY, 'session-token');

      expect(apiClient.getToken()).toBe('session-token');
    });

    it('falls back to localStorage when sessionStorage is empty', () => {
      localStorage.setItem(TOKEN_KEY, 'local-token');

      expect(apiClient.getToken()).toBe('local-token');
    });

    it('returns null when no token is stored anywhere', () => {
      expect(apiClient.getToken()).toBeNull();
    });
  });

  describe('setRefreshToken / getRefreshToken', () => {
    it('stores the refresh token in localStorage when persist=true', () => {
      apiClient.setRefreshToken('refresh-persistent', true);

      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-persistent');
      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    });

    it('stores the refresh token in sessionStorage only when persist=false', () => {
      apiClient.setRefreshToken('refresh-session', false);

      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-session');
      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    });

    it('defaults to persist=true when the persist argument is omitted', () => {
      apiClient.setRefreshToken('refresh-default');

      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('refresh-default');
      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
    });

    it('removes the copy in the other storage when the persist choice changes', () => {
      apiClient.setRefreshToken('first', true);
      apiClient.setRefreshToken('second', false);

      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBe('second');

      apiClient.setRefreshToken('third', true);

      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBe('third');
    });

    it('getRefreshToken prefers sessionStorage when both storages hold a token', () => {
      localStorage.setItem(REFRESH_TOKEN_KEY, 'local-refresh');
      sessionStorage.setItem(REFRESH_TOKEN_KEY, 'session-refresh');

      expect(apiClient.getRefreshToken()).toBe('session-refresh');
    });
  });

  describe('clearTokens', () => {
    it('removes access and refresh tokens from BOTH storages', () => {
      localStorage.setItem(TOKEN_KEY, 'local-token');
      sessionStorage.setItem(TOKEN_KEY, 'session-token');
      localStorage.setItem(REFRESH_TOKEN_KEY, 'local-refresh');
      sessionStorage.setItem(REFRESH_TOKEN_KEY, 'session-refresh');

      apiClient.clearTokens();

      expect(localStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(sessionStorage.getItem(TOKEN_KEY)).toBeNull();
      expect(localStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      expect(sessionStorage.getItem(REFRESH_TOKEN_KEY)).toBeNull();
      expect(apiClient.getToken()).toBeNull();
      expect(apiClient.getRefreshToken()).toBeNull();
    });
  });
});
