import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import type { RouteObject } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

// Regression tests for the admin routes bug: routes used to define BOTH a
// static `element` (a hardcoded placeholder like <div>Users List (Admin only)</div>)
// AND `lazy`. A static element always wins over `lazy`, so the real page
// component never rendered. Admin routes now live under a pathless
// ProtectedRoute+Outlet layout route and declare only `lazy`.

// react-router's data router constructs `new Request(url, { signal })` using
// the global (undici) Request but a jsdom AbortController signal, which undici
// rejects ("Expected signal to be an instance of AbortSignal"). No real fetch
// happens during these tests, so drop the foreign signal.
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

let authUser: User | null = null;

const authValue = () => ({
  user: authUser,
  isLoading: false,
  isAuthenticated: authUser !== null,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => authValue(),
}));

// UserList imports useAuth from the context module directly.
vi.mock('@/contexts/AuthContext', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/contexts/AuthContext')>();
  return {
    ...actual,
    useAuth: () => authValue(),
  };
});

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({
    showSuccess: vi.fn(),
    showError: vi.fn(),
    showWarning: vi.fn(),
    showInfo: vi.fn(),
  }),
}));

// Keep the shell light: the layout chrome is irrelevant here, the routed
// page is what is under test.
vi.mock('@/layouts/MainLayout', async () => {
  const { Outlet } = await import('react-router-dom');
  return {
    MainLayout: () => <Outlet />,
  };
});

vi.mock('@/api/endpoints/users', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/users')>();
  return {
    ...actual,
    usersApi: {
      ...actual.usersApi,
      getUsers: vi.fn().mockResolvedValue({
        data: [],
        total: 0,
        page: 1,
        limit: 10,
        total_pages: 0,
      }),
    },
  };
});

import { router } from './index';

const renderAt = (path: string) => {
  const memoryRouter = createMemoryRouter(
    router.routes as unknown as RouteObject[],
    { initialEntries: [path] }
  );
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={memoryRouter} />
    </QueryClientProvider>
  );
};

describe('admin routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  it('renders the REAL UserList component at /users for an admin, not the static placeholder', async () => {
    authUser = createMockUser({ role: 'admin' });

    renderAt('/users');

    // The real lazy-loaded UserList page. The generous timeout covers the dynamic
    // import of the page module and its MUI dependencies, which can take well over
    // the 1s default when the whole suite is competing for workers.
    expect(
      await screen.findByRole('heading', { name: 'Users' }, { timeout: 15000 })
    ).toBeInTheDocument();
    expect(
      screen.getByRole('button', { name: /add user/i })
    ).toBeInTheDocument();

    // The old hardcoded placeholder must be gone.
    expect(
      screen.queryByText('Users List (Admin only)')
    ).not.toBeInTheDocument();
  }, 20000);

  it('blocks non-admin users from /users via the pathless ProtectedRoute layout', async () => {
    authUser = createMockUser({ role: 'sales' });

    renderAt('/users');

    expect(await screen.findByText('Access Denied')).toBeInTheDocument();
    expect(
      screen.queryByRole('heading', { name: 'Users' })
    ).not.toBeInTheDocument();
  });

  it('no route in the config defines BOTH a static element and lazy (the invariant behind the bug)', () => {
    const offenders: string[] = [];

    const walk = (routes: RouteObject[], prefix: string) => {
      for (const route of routes) {
        const id = `${prefix}/${route.path ?? '(pathless)'}`;
        const hasElement =
          (route as { element?: unknown }).element !== undefined;
        const hasLazy = (route as { lazy?: unknown }).lazy !== undefined;
        if (hasElement && hasLazy) {
          offenders.push(id);
        }
        if (route.children) {
          walk(route.children, id);
        }
      }
    };

    walk(router.routes as unknown as RouteObject[], '');

    expect(offenders).toEqual([]);
  });
});
