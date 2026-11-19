import React, { useState, useCallback, useEffect, useContext, useRef } from 'react';
import type { ReactNode } from 'react';
import { QueryClientContext } from '@tanstack/react-query';
import type { User, CurrentUser, Permission, LoginRequest, RegisterRequest } from '@/types';
import { authApi } from '@/api/endpoints';
import { apiClient } from '@/api/client';
import { AuthContext, type AuthContextType } from './auth-context';
import { getHTTPStatus } from '@/utils/apiError';

interface PublishedSession {
  user: User | null;
  permissions: Permission[];
}

const SIGNED_OUT: PublishedSession = { user: null, permissions: [] };

interface AuthProviderProps {
  children: ReactNode;
}

export const AuthProvider: React.FC<AuthProviderProps> = ({ children }) => {
  const queryClient = useContext(QueryClientContext);
  // The user and its permissions live in one state value: a render never
  // sees the new user with the permission list of the previous one.
  const [session, setSession] = useState<PublishedSession>(SIGNED_OUT);
  const [isLoading, setIsLoading] = useState(true);
  const userIdRef = useRef<number | undefined>(undefined);

  // Publishes the signed-in user. A different account (or none) drops every
  // cached query first, before anything renders for the new user: clearing
  // afterwards, from an effect, removed the queries the new account's
  // screens had already started and left them waiting forever.
  // The permissions are those of the /users/me answer; an answer without
  // the list, and a sign-out, publish none.
  const publishUser = useCallback(
    (next: CurrentUser | null) => {
      if (userIdRef.current !== undefined && userIdRef.current !== next?.id) {
        queryClient?.clear();
      }
      userIdRef.current = next?.id;
      setSession(next ? { user: next, permissions: next.permissions ?? [] } : SIGNED_OUT);
    },
    [queryClient],
  );

  // Sign-in attempts are ordered: only the most recently started one may
  // install its session, whatever order the responses arrive in.
  const signInAttemptRef = useRef(0);

  const loadUser = useCallback(async () => {
    // The session this lookup is for; another tab may replace the stored
    // session without this tab's generation changing.
    const session = apiClient.captureSession();
    try {
      const token = apiClient.getToken();
      if (!token) {
        setIsLoading(false);
        return;
      }

      const userData = await authApi.getCurrentUser();
      // A sign-out or another sign-in happened while /users/me was in flight:
      // this answer belongs to a session that is no longer the current one.
      if (!apiClient.isCurrentSession(session)) {
        return;
      }
      publishUser(userData);
    } catch (error) {
      // Only clear tokens on auth errors (401). Other errors (429 rate limit,
      // network issues) should not log the user out. A late failure of an
      // earlier session must not clear the one that replaced it.
      const status = getHTTPStatus(error);
      if ((status === 401 || status === 403) && apiClient.isCurrentSession(session)) {
        apiClient.clearTokens();
        queryClient?.clear();
      }
    } finally {
      setIsLoading(false);
    }
  }, [queryClient, publishUser]);

  useEffect(() => {
    loadUser();
  }, [loadUser]);

  // An administrator may change the role or its permissions while the tab is
  // in the background: ask again when the window regains focus. Unlike
  // loadUser this never touches isLoading — the guard would swap the page for
  // the spinner and drop whatever the user had typed.
  const refetchOnFocus = useCallback(async () => {
    if (!apiClient.getToken()) {
      return;
    }
    const current = apiClient.captureSession();
    try {
      const userData = await authApi.getCurrentUser();
      // A late answer must not overwrite a newer sign-in or bring back a
      // signed-out user.
      if (!apiClient.isCurrentSession(current)) {
        return;
      }
      publishUser(userData);
    } catch (error) {
      if (!apiClient.isCurrentSession(current)) {
        return;
      }
      // A refused session ends here, user included: on mount no user is
      // published yet, on focus one is. Any other failure (429, network)
      // keeps what is published.
      const status = getHTTPStatus(error);
      if (status === 401 || status === 403) {
        apiClient.clearTokens();
        queryClient?.clear();
        publishUser(null);
      }
    }
  }, [queryClient, publishUser]);

  useEffect(() => {
    const onFocus = () => {
      void refetchOnFocus();
    };
    window.addEventListener('focus', onFocus);
    return () => window.removeEventListener('focus', onFocus);
  }, [refetchOnFocus]);

  // The second half of a sign-in: the login and registration answers carry
  // the user but not its permissions, so /users/me is asked before anything
  // is published. The caller navigates when the promise resolves; resolving
  // earlier would send a deep link to /unauthorized. A failure ends the new
  // session, so no half-signed-in state is left behind.
  const publishSignedIn = useCallback(
    async (attempt: number) => {
      const started = apiClient.captureSession();
      const isCurrent = () =>
        attempt === signInAttemptRef.current && apiClient.isCurrentSession(started);
      let userData: CurrentUser;
      try {
        userData = await authApi.getCurrentUser();
      } catch (error) {
        if (!isCurrent()) {
          return;
        }
        apiClient.clearTokens();
        queryClient?.clear();
        publishUser(null);
        throw error;
      }
      if (!isCurrent()) {
        return;
      }
      publishUser(userData);
    },
    [queryClient, publishUser],
  );

  const login = useCallback(async (data: LoginRequest) => {
    const attempt = ++signInAttemptRef.current;
    try {
      const response = await authApi.login(data);
      if (attempt !== signInAttemptRef.current) {
        return;
      }
      apiClient.startSession(response.token, response.refresh_token, Boolean(data.remember_me));
      queryClient?.clear();
      await publishSignedIn(attempt);
    } catch (error) {
      console.error('Login failed:', error);
      throw error;
    }
  }, [queryClient, publishSignedIn]);

  // OpenID Connect sign-in, finished by the callback page. Like a password
  // login without "Remember me": no remember-me choice survives the redirect
  // to the issuer, so the session lives in sessionStorage.
  const loginWithSSO = useCallback(async (code: string, state: string) => {
    const attempt = ++signInAttemptRef.current;
    const response = await authApi.oidcCallback(code, state);
    if (attempt !== signInAttemptRef.current) {
      return;
    }
    apiClient.startSession(response.token, response.refresh_token, false);
    queryClient?.clear();
    await publishSignedIn(attempt);
  }, [queryClient, publishSignedIn]);

  const register = useCallback(async (data: RegisterRequest) => {
    const attempt = ++signInAttemptRef.current;
    try {
      const response = await authApi.register(data);
      if (attempt !== signInAttemptRef.current) {
        return;
      }
      // Registration returns the tokens; the user and its permissions come
      // from /users/me.
      apiClient.startSession(response.token, response.refresh_token, true);
      queryClient?.clear();
      await publishSignedIn(attempt);
    } catch (error) {
      console.error('Registration failed:', error);
      throw error;
    }
  }, [queryClient, publishSignedIn]);

  const logout = useCallback(async () => {
    // The stored session ends at once — no refresh or replay can use or
    // store tokens for it any more — and the UI signs out immediately. The
    // server-side revocation of every refresh session follows; it is
    // authenticated with the access token captured before the tokens were
    // dropped.
    signInAttemptRef.current += 1;
    const revoked = apiClient.endSession((accessToken) => authApi.logout(accessToken));
    queryClient?.clear();
    publishUser(null);
    try {
      await revoked;
    } catch (error) {
      console.error('Logout error:', error);
    }
  }, [queryClient, publishUser]);

  const refreshUser = useCallback(async () => {
    await loadUser();
  }, [loadUser]);

  const value: AuthContextType = {
    user: session.user,
    permissions: session.permissions,
    isLoading,
    isAuthenticated: !!session.user,
    login,
    loginWithSSO,
    register,
    logout,
    refreshUser,
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};