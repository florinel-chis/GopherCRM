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

  const loadUser = useCallback(async () => {
    try {
      const token = apiClient.getToken();
      if (!token) {
        setIsLoading(false);
        return;
      }

      const userData = await authApi.getCurrentUser();
      setUser(userData);
    } catch (error) {
      // Only clear tokens on auth errors (401). Other errors (429 rate limit,
      // network issues) should not log the user out.
      const status = getHTTPStatus(error);
      if (status === 401 || status === 403) {
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
    try {
      const response = await authApi.login(data);
      const persist = Boolean(data.remember_me);
      apiClient.setToken(response.token, persist);
      if (response.refresh_token) {
        apiClient.setRefreshToken(response.refresh_token, persist);
      }
      queryClient?.clear();
      setUser(response.user);
    } catch (error) {
      console.error('Login failed:', error);
      throw error;
    }
  }, [queryClient]);

  const register = useCallback(async (data: RegisterRequest) => {
    try {
      const response = await authApi.register(data);
      // Registration returns token and user, so set them directly
      apiClient.setToken(response.token);
      if (response.refresh_token) {
        apiClient.setRefreshToken(response.refresh_token);
      }
      queryClient?.clear();
      setUser(response.user);
    } catch (error) {
      console.error('Registration failed:', error);
      throw error;
    }
  }, [queryClient]);

  const logout = useCallback(async () => {
    try {
      await authApi.logout();
    } catch (error) {
      console.error('Logout error:', error);
    } finally {
      apiClient.clearTokens();
      queryClient?.clear();
      setUser(null);
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