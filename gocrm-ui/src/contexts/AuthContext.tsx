import React, { useState, useCallback, useEffect, useContext, useRef } from 'react';
import type { ReactNode } from 'react';
import { QueryClientContext } from '@tanstack/react-query';
import type { User, LoginRequest, RegisterRequest } from '@/types';
import { authApi } from '@/api/endpoints';
import { apiClient } from '@/api/client';
import { AuthContext, type AuthContextType } from './auth-context';
import { getHTTPStatus } from '@/utils/apiError';

interface AuthProviderProps {
  children: ReactNode;
}

export const AuthProvider: React.FC<AuthProviderProps> = ({ children }) => {
  const queryClient = useContext(QueryClientContext);
  const [user, setUser] = useState<User | null>(null);
  const [isLoading, setIsLoading] = useState(true);
  const prevUserIdRef = useRef<number | undefined>(undefined);

  // Sign-in attempts are ordered: only the most recently started one may
  // install its session, whatever order the responses arrive in.
  const signInAttemptRef = useRef(0);

  const loadUser = useCallback(async () => {
    const generation = apiClient.getSessionGeneration();
    try {
      const token = apiClient.getToken();
      if (!token) {
        setIsLoading(false);
        return;
      }

      const userData = await authApi.getCurrentUser();
      // A sign-out or another sign-in happened while /users/me was in flight:
      // this answer belongs to a session that is no longer the current one.
      if (generation !== apiClient.getSessionGeneration()) {
        return;
      }
      setUser(userData);
    } catch (error) {
      // Only clear tokens on auth errors (401). Other errors (429 rate limit,
      // network issues) should not log the user out. A late failure of an
      // earlier session must not clear the one that replaced it.
      const status = getHTTPStatus(error);
      if ((status === 401 || status === 403) && generation === apiClient.getSessionGeneration()) {
        apiClient.clearTokens();
        queryClient?.clear();
      }
    } finally {
      setIsLoading(false);
    }
  }, [queryClient]);

  useEffect(() => {
    loadUser();
  }, [loadUser]);

  useEffect(() => {
    if (prevUserIdRef.current !== undefined && prevUserIdRef.current !== user?.id) {
      queryClient?.clear();
    }
    prevUserIdRef.current = user?.id;
  }, [user?.id, queryClient]);

  const login = useCallback(async (data: LoginRequest) => {
    const attempt = ++signInAttemptRef.current;
    try {
      const response = await authApi.login(data);
      if (attempt !== signInAttemptRef.current) {
        return;
      }
      apiClient.startSession(response.token, response.refresh_token, Boolean(data.remember_me));
      queryClient?.clear();
      setUser(response.user);
    } catch (error) {
      console.error('Login failed:', error);
      throw error;
    }
  }, [queryClient]);

  const register = useCallback(async (data: RegisterRequest) => {
    const attempt = ++signInAttemptRef.current;
    try {
      const response = await authApi.register(data);
      if (attempt !== signInAttemptRef.current) {
        return;
      }
      // Registration returns token and user, so set them directly
      apiClient.startSession(response.token, response.refresh_token, true);
      queryClient?.clear();
      setUser(response.user);
    } catch (error) {
      console.error('Registration failed:', error);
      throw error;
    }
  }, [queryClient]);

  const logout = useCallback(async () => {
    // The stored session ends at once — no refresh or replay can use or
    // store tokens for it any more — and the UI signs out immediately. The
    // server-side revocation of every refresh session follows; it is
    // authenticated with the access token captured before the tokens were
    // dropped.
    signInAttemptRef.current += 1;
    const revoked = apiClient.endSession((accessToken) => authApi.logout(accessToken));
    queryClient?.clear();
    setUser(null);
    try {
      await revoked;
    } catch (error) {
      console.error('Logout error:', error);
    }
  }, [queryClient]);

  const refreshUser = useCallback(async () => {
    await loadUser();
  }, [loadUser]);

  const value: AuthContextType = {
    user,
    isLoading,
    isAuthenticated: !!user,
    login,
    register,
    logout,
    refreshUser,
  };

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
};