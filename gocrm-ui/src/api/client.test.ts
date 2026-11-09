import { describe, it, expect, beforeEach } from 'vitest';
import { apiClient, SESSION_KEY, TOKEN_KEY, REFRESH_TOKEN_KEY } from './client';

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
});
