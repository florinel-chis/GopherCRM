import React from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { AuthProvider } from '@/contexts/AuthContext';
import { apiClient } from '@/api/client';
import { AuthCallback, SSO_CALLBACK_FAILED_MESSAGE } from './AuthCallback';

// See routes/index.test.tsx: the data router builds a Request with a jsdom
// signal that undici rejects; no real fetch happens, so the signal is dropped.
const NativeRequest = globalThis.Request;
class TestRequest extends NativeRequest {
  constructor(input: RequestInfo | URL, init?: RequestInit) {
    if (init && 'signal' in init) {
      const rest: RequestInit = { ...init };
      delete rest.signal;
      super(input, rest);
    } else {
      super(input, init);
    }
  }
}
vi.stubGlobal('Request', TestRequest);

// The real AuthProvider and session store; only the two API calls of an
// OpenID Connect sign-in are stubbed.
const mockOIDCCallback = vi.fn();
const mockGetCurrentUser = vi.fn();

vi.mock('@/api/endpoints', () => ({
  authApi: {
    oidcCallback: (code: string, state: string) => mockOIDCCallback(code, state),
    getCurrentUser: () => mockGetCurrentUser(),
    logout: vi.fn(),
  },
}));

const ACCESS = 'sso-access-token-value';
const REFRESH = 'sso-refresh-token-value';

const httpError = (status: number) =>
  Object.assign(new Error(`HTTP ${status}`), { response: { status, data: { code: 'X', message: 'refused' } } });

const renderCallback = (search: string) => {
  const memoryRouter = createMemoryRouter(
    [
      { path: '/auth/callback', element: <AuthCallback /> },
      { path: '/', element: <div>Home Page</div> },
      { path: '/login', element: <div>Login Page</div> },
    ],
    { initialEntries: [`/auth/callback${search}`] },
  );
  // StrictMode runs the effects twice: the single-use code must still be
  // posted once.
  const view = render(
    <React.StrictMode>
      <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
        <AuthProvider>
          <RouterProvider router={memoryRouter} />
        </AuthProvider>
      </QueryClientProvider>
    </React.StrictMode>,
  );
  return { ...view, memoryRouter };
};

const dump = (storage: Storage) =>
  Array.from({ length: storage.length }, (_, i) => {
    const key = storage.key(i) ?? '';
    return `${key}=${storage.getItem(key) ?? ''}`;
  }).join('\n');

const expectNoStoredSession = () => {
  expect(apiClient.getToken()).toBeNull();
  expect(dump(sessionStorage)).not.toContain(ACCESS);
  expect(dump(localStorage)).not.toContain(ACCESS);
};

describe('AuthCallback', () => {
  beforeEach(() => {
    mockOIDCCallback.mockReset();
    mockGetCurrentUser.mockReset();
    apiClient.clearTokens();
    sessionStorage.clear();
    localStorage.clear();
  });

  it('posts code and state once, drops them from the address and starts a session-storage session', async () => {
    mockOIDCCallback.mockResolvedValue({ token: ACCESS, refresh_token: REFRESH, user: { id: 7 } });
    mockGetCurrentUser.mockResolvedValue({ id: 7, email: 'sso@example.com', role: 'sales', permissions: [] });
    const { memoryRouter, container } = renderCallback('?code=c&state=s');

    expect(await screen.findByText('Home Page')).toBeInTheDocument();
    expect(memoryRouter.state.location.pathname).toBe('/');
    expect(mockOIDCCallback).toHaveBeenCalledTimes(1);
    expect(mockOIDCCallback).toHaveBeenCalledWith('c', 's');

    expect(apiClient.getToken()).toBe(ACCESS);
    expect(apiClient.getRefreshToken()).toBe(REFRESH);
    const sessionDump = dump(sessionStorage);
    expect(sessionDump).toContain(ACCESS);
    expect(sessionDump).toContain(REFRESH);
    // Session-only: no token persists in localStorage.
    expect(dump(localStorage)).not.toContain(ACCESS);
    expect(dump(localStorage)).not.toContain(REFRESH);
    expect(container.innerHTML).not.toContain(ACCESS);
  });

  it('removes the query from the address with a replace navigation while the request is pending', async () => {
    mockOIDCCallback.mockReturnValue(new Promise(() => {}));
    const { memoryRouter, container } = renderCallback('?code=secret-code&state=secret-state');

    await waitFor(() => expect(memoryRouter.state.location.search).toBe(''));
    expect(memoryRouter.state.historyAction).toBe('REPLACE');
    expect(memoryRouter.state.location.pathname).toBe('/auth/callback');
    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(container.innerHTML).not.toContain('secret-code');
    expect(container.innerHTML).not.toContain('secret-state');
    expect(mockOIDCCallback).toHaveBeenCalledTimes(1);
  });

  it.each([401, 503])('shows the generic failure and a link to sign in on a %s answer', async (status) => {
    mockOIDCCallback.mockRejectedValue(httpError(status));
    renderCallback('?code=c&state=s');

    expect(await screen.findByText(SSO_CALLBACK_FAILED_MESSAGE)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to sign in' })).toHaveAttribute('href', '/login');
    expect(mockOIDCCallback).toHaveBeenCalledTimes(1);
    expectNoStoredSession();
  });

  it('shows the generic failure when the session cannot be confirmed afterwards', async () => {
    mockOIDCCallback.mockResolvedValue({ token: ACCESS, refresh_token: REFRESH, user: { id: 7 } });
    mockGetCurrentUser.mockRejectedValue(httpError(401));
    renderCallback('?code=c&state=s');

    expect(await screen.findByText(SSO_CALLBACK_FAILED_MESSAGE)).toBeInTheDocument();
    expectNoStoredSession();
  });

  it.each([
    ['a missing code', '?state=s'],
    ['a missing state', '?code=c'],
    ['no parameters', ''],
    ['an error parameter', '?error=access_denied&code=c&state=s'],
  ])('shows the generic failure without any request for %s', async (_, search) => {
    const { memoryRouter } = renderCallback(search);

    expect(await screen.findByText(SSO_CALLBACK_FAILED_MESSAGE)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to sign in' })).toHaveAttribute('href', '/login');
    await waitFor(() => expect(memoryRouter.state.location.search).toBe(''));
    expect(mockOIDCCallback).not.toHaveBeenCalled();
    expectNoStoredSession();
  });
});
