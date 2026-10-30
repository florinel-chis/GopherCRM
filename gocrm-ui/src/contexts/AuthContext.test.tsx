import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { renderHook, act } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
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

// The session generation the mocked client reports; tests bump it to stand
// for a sign-out or sign-in that happened while a request was in flight.
const session = vi.hoisted(() => ({ generation: 0 }));

vi.mock('@/api/client', () => ({
  apiClient: {
    getToken: vi.fn(),
    setToken: vi.fn(),
    setRefreshToken: vi.fn(),
    clearTokens: vi.fn(),
    startSession: vi.fn(),
    endSession: vi.fn(),
    getSessionGeneration: vi.fn(() => session.generation),
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
    session.generation = 0;
    vi.mocked(apiClient.getSessionGeneration).mockImplementation(() => session.generation);
    vi.mocked(apiClient.endSession).mockImplementation(async (revoke) => {
      session.generation += 1;
      await revoke('captured-access-token');
    });
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

    expect(apiClient.startSession).toHaveBeenCalledWith('test-token', 'test-refresh-token', true);
    expect(result.current.user).toEqual(mockUser);
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

    expect(apiClient.startSession).toHaveBeenCalledWith('test-token', 'test-refresh-token', false);
  });

  it('starts the session without a refresh token when the login response omits it', async () => {
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

    // startSession replaces both keys, so no predecessor refresh token
    // survives next to the new access token (storage covered in client tests).
    expect(apiClient.startSession).toHaveBeenCalledWith('test-token', undefined, true);
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

    // endSession drops the stored tokens first and then revokes on the
    // server with the access token it captured before dropping them.
    expect(apiClient.endSession).toHaveBeenCalledTimes(1);
    expect(authApi.logout).toHaveBeenCalledWith('captured-access-token');
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
    expect(apiClient.startSession).toHaveBeenCalledWith('test-token', undefined, true);
  });

  it('clears query cache on logout when QueryClientProvider is present', async () => {
    const queryClient = new QueryClient();
    const clearSpy = vi.spyOn(queryClient, 'clear');
    vi.mocked(apiClient.getToken).mockReturnValue('test-token');
    vi.mocked(authApi.getCurrentUser).mockResolvedValue(mockUser);

    const wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
      <QueryClientProvider client={queryClient}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    );

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.user).toEqual(mockUser);
    });

    clearSpy.mockClear();

    await act(async () => {
      await result.current.logout();
    });

    expect(clearSpy).toHaveBeenCalled();
  });

  it('clears query cache on login when QueryClientProvider is present', async () => {
    const queryClient = new QueryClient();
    const clearSpy = vi.spyOn(queryClient, 'clear');
    vi.mocked(apiClient.getToken).mockReturnValue(null);
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'test-token',
      user: mockUser,
    });

    const wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
      <QueryClientProvider client={queryClient}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    );

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    clearSpy.mockClear();

    await act(async () => {
      await result.current.login({
        email: 'test@example.com',
        password: 'password',
      });
    });

    expect(clearSpy).toHaveBeenCalled();
  });

  it('clears query cache on 401/403 auth error during loadUser', async () => {
    const queryClient = new QueryClient();
    const clearSpy = vi.spyOn(queryClient, 'clear');
    const authErr = Object.assign(new Error('Unauthorized'), {
      response: { status: 401 },
    });
    vi.mocked(apiClient.getToken).mockReturnValue('expired-token');
    vi.mocked(authApi.getCurrentUser).mockRejectedValue(authErr);

    const wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
      <QueryClientProvider client={queryClient}>
        <AuthProvider>{children}</AuthProvider>
      </QueryClientProvider>
    );

    const { result } = renderHook(() => useAuth(), { wrapper });

    await waitFor(() => {
      expect(result.current.isLoading).toBe(false);
    });

    expect(clearSpy).toHaveBeenCalled();
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
  it('signs out immediately even while the server-side revocation is still pending', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue('test-token');
    vi.mocked(authApi.getCurrentUser).mockResolvedValue(mockUser);
    let finishRevocation: () => void = () => {};
    vi.mocked(apiClient.endSession).mockImplementation(
      () => new Promise<void>((resolve) => { finishRevocation = resolve; })
    );

    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider });
    await waitFor(() => expect(result.current.user).toEqual(mockUser));

    let pending: Promise<void> = Promise.resolve();
    act(() => {
      pending = result.current.logout();
    });
    await waitFor(() => expect(result.current.user).toBe(null));

    await act(async () => {
      finishRevocation();
      await pending;
    });
    expect(result.current.isAuthenticated).toBe(false);
  });

  it('ignores a /users/me answer that arrives after the session changed', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue('old-session-token');
    let answer: (user: typeof mockUser) => void = () => {};
    vi.mocked(authApi.getCurrentUser).mockImplementation(
      () => new Promise((resolve) => { answer = resolve; })
    );

    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider });

    // The session ends (sign-out elsewhere in the app) before the answer lands.
    session.generation += 1;
    await act(async () => {
      answer(mockUser);
    });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.user).toBe(null);
  });

  it('a late 401 from /users/me does not clear the session that replaced it', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue('old-session-token');
    let fail: (error: unknown) => void = () => {};
    vi.mocked(authApi.getCurrentUser).mockImplementation(
      () => new Promise((_, reject) => { fail = reject; })
    );

    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider });

    session.generation += 1; // a new sign-in replaced the session
    await act(async () => {
      fail(Object.assign(new Error('Unauthorized'), { response: { status: 401 } }));
    });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(apiClient.clearTokens).not.toHaveBeenCalled();
  });

  it('a slower earlier sign-in cannot replace a later one', async () => {
    vi.mocked(apiClient.getToken).mockReturnValue(null);
    const otherUser = { ...mockUser, id: 2, email: 'other@example.com' };
    let answerFirst: (value: { token: string; refresh_token: string; user: typeof mockUser }) => void = () => {};
    vi.mocked(authApi.login)
      .mockImplementationOnce(() => new Promise((resolve) => { answerFirst = resolve; }))
      .mockResolvedValueOnce({ token: 'second-token', refresh_token: 'second-refresh', user: otherUser });

    const { result } = renderHook(() => useAuth(), { wrapper: AuthProvider });
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    let first: Promise<void> = Promise.resolve();
    await act(async () => {
      first = result.current.login({ email: 'test@example.com', password: 'password' });
      await result.current.login({ email: 'other@example.com', password: 'password' });
    });
    await act(async () => {
      answerFirst({ token: 'first-token', refresh_token: 'first-refresh', user: mockUser });
      await first;
    });

    expect(apiClient.startSession).toHaveBeenCalledTimes(1);
    expect(apiClient.startSession).toHaveBeenCalledWith('second-token', 'second-refresh', false);
    expect(result.current.user).toEqual(otherUser);
  });
});
