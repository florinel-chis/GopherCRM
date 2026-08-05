import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { renderHook, act } from '@testing-library/react';
import { AuthProvider } from './AuthContext';
import { useAuth } from '@/hooks/useAuth';
import { authApi } from '@/api/endpoints';
import { apiClient } from '@/api/client';

// Mock the API modules
vi.mock('@/api/endpoints', () => ({
  authApi: {
    getCurrentUser: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
  },
}));

vi.mock('@/api/client', () => ({
  apiClient: {
    getToken: vi.fn(),
    setToken: vi.fn(),
    setRefreshToken: vi.fn(),
    clearTokens: vi.fn(),
  },
  TOKEN_KEY: 'gophercrm_token',
  REFRESH_TOKEN_KEY: 'gophercrm_refresh_token',
}));

const mockUser = {
  id: 1,
  email: 'test@example.com',
  first_name: 'Test',
  last_name: 'User',
  role: 'admin' as const,
  is_active: true,
  created_at: '2024-01-01',
  updated_at: '2024-01-01',
};

describe('AuthContext', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
  });

  it('provides auth context to children', () => {
    render(
      <AuthProvider>
        <div>Test Child</div>
      </AuthProvider>
    );
    
    expect(screen.getByText('Test Child')).toBeInTheDocument();
  });

  it('loads user on mount when token exists', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue('test-token');
    vi.mocked(authApi.getCurrentUser).mockResolvedValue(mockUser);

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    expect(result.current.isLoading).toBe(true);

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
      expect(result.current.user).toEqual(mockUser);
      expect(result.current.isAuthenticated).toBe(true);
    });
  });

  it('does not load user when no token exists', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue(null);

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
      expect(result.current.user).toBe(null);
      expect(result.current.isAuthenticated).toBe(false);
    });

    expect(authApi.getCurrentUser).not.toHaveBeenCalled();
  });

  it('handles login successfully', async () => {
    const loginResponse = {
      token: 'test-token',
      refresh_token: 'test-refresh-token',
      user: mockUser,
    };

    vi.mocked(authApi.login).mockResolvedValue(loginResponse);

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await act(async () => {
      await result.current.login({
        email: 'test@example.com',
        password: 'password',
        remember_me: true,
      });
    });

    expect(apiClient.setToken).toHaveBeenCalledWith('test-token', true);
    expect(apiClient.setRefreshToken).toHaveBeenCalledWith('test-refresh-token', true);
    expect(result.current.user).toEqual(mockUser);
    expect(localStorage.getItem('remember_me')).toBe('true');
  });

  it('stores both tokens as session-only when remember_me is not set', async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'test-token',
      refresh_token: 'test-refresh-token',
      user: mockUser,
    });

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await act(async () => {
      await result.current.login({
        email: 'test@example.com',
        password: 'password',
      });
    });

    expect(apiClient.setToken).toHaveBeenCalledWith('test-token', false);
    expect(apiClient.setRefreshToken).toHaveBeenCalledWith('test-refresh-token', false);
    expect(localStorage.getItem('remember_me')).toBeNull();
  });

  it('does not store a refresh token when the login response omits it', async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'test-token',
      user: mockUser,
    });

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await act(async () => {
      await result.current.login({
        email: 'test@example.com',
        password: 'password',
        remember_me: true,
      });
    });

    expect(apiClient.setToken).toHaveBeenCalledWith('test-token', true);
    expect(apiClient.setRefreshToken).not.toHaveBeenCalled();
  });

  it('handles logout', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue('test-token');
    vi.mocked(authApi.getCurrentUser).mockResolvedValue(mockUser);

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await waitFor(() => {
      expect(result.current.user).toEqual(mockUser);
    });

    await act(async () => {
      await result.current.logout();
    });

    expect(authApi.logout).toHaveBeenCalled();
    // clearTokens wipes access AND refresh tokens from both storages, and the
    // server must be told first, while the access token is still available.
    expect(apiClient.clearTokens).toHaveBeenCalled();
    const logoutOrder = vi.mocked(authApi.logout).mock.invocationCallOrder[0];
    const clearOrder = vi.mocked(apiClient.clearTokens).mock.invocationCallOrder[0];
    expect(logoutOrder).toBeLessThan(clearOrder);
    expect(result.current.user).toBe(null);
    expect(result.current.isAuthenticated).toBe(false);
  });

  it('handles registration', async () => {
    vi.mocked(authApi.register).mockResolvedValue({
      token: 'test-token',
      user: mockUser,
    });
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'test-token',
      user: mockUser,
    });

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await act(async () => {
      await result.current.register({
        email: 'new@example.com',
        password: 'password',
        first_name: 'New',
        last_name: 'User',
      });
    });

    expect(authApi.register).toHaveBeenCalled();
    expect(apiClient.setToken).toHaveBeenCalledWith('test-token');
  });

  it('throws error when useAuth is used outside AuthProvider', () => {
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => {});
    
    const TestComponent = () => {
      useAuth();
      return null;
    };
    
    expect(() => {
      render(<TestComponent />);
    }).toThrow('useAuth must be used within an AuthProvider');
    
    consoleError.mockRestore();
  });
});