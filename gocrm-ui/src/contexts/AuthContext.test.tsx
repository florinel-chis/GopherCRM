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
  TOKEN_KEY: 'gocrm_token',
  REFRESH_TOKEN_KEY: 'gocrm_refresh_token',
}));

const mockUser = {
  id: 1,
  username: 'testuser',
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
        username: 'testuser',
        password: 'password',
        remember_me: true,
      });
    });

    expect(apiClient.setToken).toHaveBeenCalledWith('test-token');
    expect(apiClient.setRefreshToken).toHaveBeenCalledWith('test-refresh-token');
    expect(result.current.user).toEqual(mockUser);
    expect(localStorage.getItem('remember_me')).toBe('true');
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
    expect(apiClient.clearTokens).toHaveBeenCalled();
    expect(result.current.user).toBe(null);
    expect(result.current.isAuthenticated).toBe(false);
  });

  it('handles registration', async () => {
    vi.mocked(authApi.register).mockResolvedValue(mockUser);
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'test-token',
      user: mockUser,
    });

    const { result } = renderHook(() => useAuth(), {
      wrapper: AuthProvider,
    });

    await act(async () => {
      await result.current.register({
        username: 'newuser',
        email: 'new@example.com',
        password: 'password',
        first_name: 'New',
        last_name: 'User',
      });
    });

    expect(authApi.register).toHaveBeenCalled();
    expect(authApi.login).toHaveBeenCalledWith({
      username: 'newuser',
      password: 'password',
    });
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