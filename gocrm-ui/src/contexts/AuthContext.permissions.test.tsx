import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';
import { AuthProvider } from './AuthContext';
import { useAuth } from '@/hooks/useAuth';
import { usePermissions } from '@/hooks/usePermissions';
import { authApi } from '@/api/endpoints';
import { apiClient } from '@/api/client';
import type { CurrentUser, Permission } from '@/types';

vi.mock('@/api/endpoints', () => ({
  authApi: {
    getCurrentUser: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
  },
}));

// A stand-in for the stored session: the token and the generation the real
// client keeps. startSession, clearTokens and endSession move both the way
// the client does, so the provider's stale-answer guards are exercised.
const stored = vi.hoisted(() => ({ generation: 0, token: null as string | null }));

vi.mock('@/api/client', () => ({
  apiClient: {
    getToken: vi.fn(() => stored.token),
    clearTokens: vi.fn(() => {
      stored.generation += 1;
      stored.token = null;
    }),
    startSession: vi.fn((token: string) => {
      stored.generation += 1;
      stored.token = token;
    }),
    endSession: vi.fn(async (revoke: (accessToken: string) => Promise<void>) => {
      const access = stored.token;
      stored.generation += 1;
      stored.token = null;
      if (access) await revoke(access);
    }),
    getSessionGeneration: vi.fn(() => stored.generation),
    captureSession: vi.fn(() => ({ generation: stored.generation, id: null, subject: null, refreshToken: null })),
    isCurrentSession: vi.fn((snapshot: { generation: number }) => snapshot.generation === stored.generation),
  },
}));

const user = {
  id: 1,
  email: 'test@example.com',
  first_name: 'Test',
  last_name: 'User',
  role: 'sales' as const,
  is_active: true,
  created_at: '2024-01-01',
  updated_at: '2024-01-01',
};

const otherUser = { ...user, id: 2, email: 'other@example.com' };

const FIRST: Permission[] = [
  { key: 'leads.view', scope: 'own' },
  { key: 'tickets.view', scope: 'all' },
];
const CHANGED: Permission[] = [{ key: 'tickets.view', scope: 'own' }];

const me = (permissions: Permission[] | undefined, base = user): CurrentUser => ({
  ...base,
  role_name: 'Sales',
  permissions,
});

const httpError = (status: number) => ({ response: { status, data: {} } });

/** A promise the test settles by hand, to hold a /users/me answer in flight. */
const deferred = <T,>() => {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
};

/** Mounts the provider and records every published value. */
const mount = () => {
  const published: Array<ReturnType<typeof useAuth>> = [];
  const hook = renderHook(
    () => {
      const value = useAuth();
      published.push(value);
      return value;
    },
    { wrapper: AuthProvider },
  );
  return { ...hook, published };
};

/** Mounts with a stored token and waits for the session restore to publish. */
const mountSignedIn = async (permissions: Permission[] = FIRST) => {
  stored.token = 'stored-token';
  vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(permissions));
  const mounted = mount();
  await waitFor(() => expect(mounted.result.current.isLoading).toBe(false));
  expect(mounted.result.current.permissions).toEqual(permissions);
  return mounted;
};

const focusWindow = () => {
  act(() => {
    window.dispatchEvent(new Event('focus'));
  });
};

describe('AuthProvider permissions', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(authApi.getCurrentUser).mockReset();
    vi.mocked(authApi.logout).mockResolvedValue(undefined);
    stored.generation = 0;
    stored.token = null;
  });

  it('publishes the permissions of /users/me on session restore', async () => {
    stored.token = 'stored-token';
    const answer = deferred<CurrentUser>();
    vi.mocked(authApi.getCurrentUser).mockReturnValueOnce(answer.promise);

    const { result, published } = mount();

    // Nothing is published while the answer is in flight.
    expect(result.current.isLoading).toBe(true);
    expect(result.current.user).toBeNull();
    expect(result.current.permissions).toEqual([]);

    await act(async () => {
      answer.resolve(me(FIRST));
    });

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.user?.id).toBe(user.id);
    expect(result.current.permissions).toEqual(FIRST);

    // The list arrives together with the user, and loading ends only with it:
    // no render shows the user, or a finished load, with an empty list.
    for (const value of published) {
      if (value.user || !value.isLoading) {
        expect(value.permissions).toEqual(FIRST);
      }
    }
  });

  it('publishes an empty list when /users/me carries no permissions', async () => {
    stored.token = 'stored-token';
    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(undefined));

    const { result } = mount();

    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(result.current.user?.id).toBe(user.id);
    expect(result.current.permissions).toEqual([]);
  });

  it('publishes the permissions of /users/me after a login', async () => {
    vi.mocked(authApi.login).mockResolvedValue({ token: 'new-token', refresh_token: 'r', user });
    const answer = deferred<CurrentUser>();
    vi.mocked(authApi.getCurrentUser).mockReturnValueOnce(answer.promise);

    const { result, published } = mount();
    await waitFor(() => expect(result.current.isLoading).toBe(false));
    expect(authApi.getCurrentUser).not.toHaveBeenCalled();

    let settled = false;
    let pending!: Promise<void>;
    act(() => {
      pending = result.current.login({ email: 'test@example.com', password: 'password' }).then(() => {
        settled = true;
      });
    });
    await waitFor(() => expect(authApi.getCurrentUser).toHaveBeenCalledTimes(1));

    // The session is stored, but the login has neither resolved nor published
    // the user of the login answer: the page would navigate without a list.
    expect(apiClient.startSession).toHaveBeenCalledWith('new-token', 'r', false);
    expect(settled).toBe(false);
    expect(result.current.user).toBeNull();

    await act(async () => {
      answer.resolve(me(FIRST));
      await pending;
    });

    expect(settled).toBe(true);
    expect(result.current.user?.id).toBe(user.id);
    expect(result.current.permissions).toEqual(FIRST);
    for (const value of published) {
      if (value.user) expect(value.permissions).toEqual(FIRST);
    }
  });

  it('ends the session and rejects the login when /users/me fails after it', async () => {
    vi.mocked(authApi.login).mockResolvedValue({ token: 'new-token', refresh_token: 'r', user });
    vi.mocked(authApi.getCurrentUser).mockRejectedValueOnce(httpError(500));
    vi.spyOn(console, 'error').mockImplementation(() => undefined);

    const { result } = mount();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    let failure: unknown;
    await act(async () => {
      await result.current.login({ email: 'test@example.com', password: 'password' }).catch((error) => {
        failure = error;
      });
    });

    expect(failure).toEqual(httpError(500));
    expect(stored.token).toBeNull();
    expect(result.current.user).toBeNull();
    expect(result.current.permissions).toEqual([]);
  });

  it('publishes the permissions of /users/me after registration', async () => {
    vi.mocked(authApi.register).mockResolvedValue({ token: 'new-token', refresh_token: 'r', user });
    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(CHANGED));

    const { result, published } = mount();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    await act(async () => {
      await result.current.register({
        email: 'test@example.com',
        password: 'password',
        first_name: 'Test',
        last_name: 'User',
      });
    });

    expect(apiClient.startSession).toHaveBeenCalledWith('new-token', 'r', true);
    expect(authApi.getCurrentUser).toHaveBeenCalledTimes(1);
    expect(result.current.user?.id).toBe(user.id);
    expect(result.current.permissions).toEqual(CHANGED);
    for (const value of published) {
      if (value.user) expect(value.permissions).toEqual(CHANGED);
    }
  });

  it('refetches /users/me when the window regains focus', async () => {
    const { result, published } = await mountSignedIn(FIRST);
    const before = published.length;
    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(CHANGED));

    focusWindow();

    await waitFor(() => expect(result.current.permissions).toEqual(CHANGED));
    expect(authApi.getCurrentUser).toHaveBeenCalledTimes(2);
    expect(result.current.user?.id).toBe(user.id);
    // The guard never saw a loading state: the page stays mounted.
    expect(published.length).toBeGreaterThan(before);
    for (const value of published.slice(before)) {
      expect(value.isLoading).toBe(false);
    }
  });

  it('refreshUser publishes the permission list /users/me now answers', async () => {
    stored.token = 'stored-token';
    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(FIRST));
    const { result } = renderHook(() => ({ auth: useAuth(), checks: usePermissions() }), {
      wrapper: AuthProvider,
    });
    await waitFor(() => expect(result.current.auth.isLoading).toBe(false));
    expect(result.current.auth.permissions).toEqual(FIRST);
    expect(result.current.checks.can('leads.view')).toBe(true);
    expect(result.current.checks.scopeOf('tickets.view')).toBe('all');

    vi.mocked(authApi.getCurrentUser).mockResolvedValueOnce(me(CHANGED));
    await act(async () => {
      await result.current.auth.refreshUser();
    });

    expect(authApi.getCurrentUser).toHaveBeenCalledTimes(2);
    expect(result.current.auth.user?.id).toBe(user.id);
    expect(result.current.auth.permissions).toEqual(CHANGED);
    expect(result.current.checks.can('leads.view')).toBe(false);
    expect(result.current.checks.scopeOf('tickets.view')).toBe('own');
  });

  it('does not ask /users/me on focus without a stored token', async () => {
    const { result } = mount();
    await waitFor(() => expect(result.current.isLoading).toBe(false));

    focusWindow();
    await act(async () => {});

    expect(authApi.getCurrentUser).not.toHaveBeenCalled();
  });

  it('stops listening for focus when the provider unmounts', async () => {
    const { unmount } = await mountSignedIn(FIRST);
    unmount();

    focusWindow();

    expect(authApi.getCurrentUser).toHaveBeenCalledTimes(1);
  });

  it('signs the user out when the focus refetch is refused', async () => {
    for (const status of [401, 403]) {
      vi.mocked(authApi.getCurrentUser).mockReset();
      vi.mocked(apiClient.clearTokens).mockClear();
      const { result, unmount } = await mountSignedIn(FIRST);
      vi.mocked(authApi.getCurrentUser).mockRejectedValueOnce(httpError(status));

      focusWindow();

      await waitFor(() => expect(result.current.user).toBeNull());
      expect(result.current.permissions).toEqual([]);
      expect(result.current.isAuthenticated).toBe(false);
      expect(apiClient.clearTokens).toHaveBeenCalledTimes(1);
      expect(stored.token).toBeNull();
      unmount();
    }
  });

  it('keeps the user and the permissions when the focus refetch fails for another reason', async () => {
    const { result } = await mountSignedIn(FIRST);

    for (const failure of [httpError(429), new Error('Network Error')]) {
      vi.mocked(authApi.getCurrentUser).mockRejectedValueOnce(failure);
      const calls = vi.mocked(authApi.getCurrentUser).mock.calls.length;

      focusWindow();
      await waitFor(() => expect(authApi.getCurrentUser).toHaveBeenCalledTimes(calls + 1));
      await act(async () => {});

      expect(result.current.user?.id).toBe(user.id);
      expect(result.current.permissions).toEqual(FIRST);
      expect(apiClient.clearTokens).not.toHaveBeenCalled();
      expect(stored.token).toBe('stored-token');
    }
  });

  it('ignores a focus answer that belongs to an earlier session', async () => {
    // A logout while the refetch is in flight: the late answer must not bring
    // the user back.
    const first = await mountSignedIn(FIRST);
    const lateAnswer = deferred<CurrentUser>();
    vi.mocked(authApi.getCurrentUser).mockReturnValueOnce(lateAnswer.promise);

    focusWindow();
    await waitFor(() => expect(authApi.getCurrentUser).toHaveBeenCalledTimes(2));

    await act(async () => {
      await first.result.current.logout();
    });
    expect(first.result.current.user).toBeNull();

    await act(async () => {
      lateAnswer.resolve(me(CHANGED));
    });

    expect(first.result.current.user).toBeNull();
    expect(first.result.current.permissions).toEqual([]);
    first.unmount();

    // A newer sign-in while the refetch is in flight: the late answer of the
    // earlier session must not overwrite it, and neither must a late refusal.
    vi.mocked(authApi.getCurrentUser).mockReset();
    vi.mocked(apiClient.clearTokens).mockClear();
    const second = await mountSignedIn(FIRST);
    const staleAnswer = deferred<CurrentUser>();
    const staleRefusal = deferred<CurrentUser>();
    vi.mocked(authApi.getCurrentUser)
      .mockReturnValueOnce(staleAnswer.promise)
      .mockReturnValueOnce(staleRefusal.promise)
      .mockResolvedValueOnce(me(CHANGED, otherUser));
    vi.mocked(authApi.login).mockResolvedValue({ token: 'newer-token', refresh_token: 'r', user: otherUser });

    focusWindow();
    focusWindow();
    await waitFor(() => expect(authApi.getCurrentUser).toHaveBeenCalledTimes(3));

    await act(async () => {
      await second.result.current.login({ email: 'other@example.com', password: 'password' });
    });
    expect(second.result.current.user?.id).toBe(otherUser.id);
    expect(second.result.current.permissions).toEqual(CHANGED);
    vi.mocked(apiClient.clearTokens).mockClear();

    await act(async () => {
      staleAnswer.resolve(me(FIRST));
      staleRefusal.reject(httpError(401));
    });

    expect(second.result.current.user?.id).toBe(otherUser.id);
    expect(second.result.current.permissions).toEqual(CHANGED);
    expect(apiClient.clearTokens).not.toHaveBeenCalled();
    expect(stored.token).toBe('newer-token');
  });
});
