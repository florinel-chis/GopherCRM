import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen } from '@testing-library/react';
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
let authLoading = false;

// The auth mock is an external store so a test can move a mounted tree from
// "loading" to "authenticated" the way the real provider does after the
// session check resolves.
let authVersion = 0;
const authListeners = new Set<() => void>();
const subscribeAuth = (listener: () => void) => {
  authListeners.add(listener);
  return () => {
    authListeners.delete(listener);
  };
};
const setAuth = (user: User | null, loading: boolean) => {
  authUser = user;
  authLoading = loading;
  authVersion += 1;
  authListeners.forEach((listener) => listener());
};

const authValue = () => ({
  user: authUser,
  isLoading: authLoading,
  isAuthenticated: !authLoading && authUser !== null,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

vi.mock('@/hooks/useAuth', async () => {
  const { useSyncExternalStore } = await import('react');
  return {
    useAuth: () => {
      useSyncExternalStore(subscribeAuth, () => authVersion);
      return authValue();
    },
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

vi.mock('@/api/endpoints/customers', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/customers')>();
  return {
    ...actual,
    customersApi: {
      ...actual.customersApi,
      getCustomers: vi.fn().mockResolvedValue({
        data: [],
        total: 0,
        page: 1,
        limit: 10,
        total_pages: 0,
      }),
    },
  };
});

// The lead list reads the conversion statuses from the configuration
// provider, which the real app mounts above the router.
vi.mock('@/hooks/useConfiguration', () => ({
  useConfiguration: () => ({
    getLeadConversionStatuses: () => ['qualified'],
    getLeadConversionSettings: () => ({
      allowedStatuses: ['qualified'],
      requireNotes: false,
      autoAssignOwner: false,
    }),
    refreshConfigurations: async () => {},
  }),
}));

vi.mock('@/api/endpoints/leads', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/leads')>();
  return {
    ...actual,
    leadsApi: {
      ...actual.leadsApi,
      getLeads: vi.fn().mockResolvedValue({
        data: [],
        total: 0,
        page: 1,
        limit: 10,
        total_pages: 0,
      }),
    },
  };
});

vi.mock('@/api/endpoints/companies', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/companies')>();
  return {
    ...actual,
    companiesApi: {
      ...actual.companiesApi,
      getCompanies: vi.fn().mockResolvedValue({ companies: [], total: 0 }),
    },
  };
});

vi.mock('@/api/endpoints/deals', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/deals')>();
  return {
    ...actual,
    dealsApi: {
      ...actual.dealsApi,
      getDeals: vi.fn().mockResolvedValue({ deals: [], total: 0 }),
      getPipeline: vi.fn().mockResolvedValue({ stages: [] }),
    },
  };
});

vi.mock('@/api/endpoints/products', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/products')>();
  return {
    ...actual,
    productsApi: {
      ...actual.productsApi,
      getProducts: vi.fn().mockResolvedValue({ products: [], total: 0 }),
    },
  };
});

vi.mock('@/api/endpoints/quotes', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/quotes')>();
  return {
    ...actual,
    quotesApi: {
      ...actual.quotesApi,
      getQuotes: vi.fn().mockResolvedValue({ quotes: [], total: 0 }),
      getQuote: vi.fn().mockResolvedValue({
        id: 1,
        number: 'Q-000001',
        title: 'Routed quote',
        status: 'draft',
        currency: 'EUR',
        subtotal_cents: 0,
        discount_cents: 0,
        total_cents: 0,
        valid_until: null,
        notes: '',
        sent_at: null,
        closed_at: null,
        last_emailed_at: null,
        owner_id: 1,
        lines: [],
        created_at: '2026-10-26T08:00:00Z',
        updated_at: '2026-10-26T08:00:00Z',
      }),
      getHistory: vi.fn().mockResolvedValue([]),
    },
  };
});

vi.mock('@/api/endpoints/tickets', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/tickets')>();
  return {
    ...actual,
    ticketsApi: {
      ...actual.ticketsApi,
      getTicket: vi.fn().mockResolvedValue(null),
      getTickets: vi.fn().mockResolvedValue({
        data: [],
        total: 0,
        page: 1,
        limit: 10,
        total_pages: 0,
      }),
    },
  };
});

vi.mock('@/api/endpoints/automation', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/automation')>();
  return {
    ...actual,
    automationApi: {
      ...actual.automationApi,
      listRules: vi.fn().mockResolvedValue({ rules: [], total: 0 }),
      listRuns: vi.fn().mockResolvedValue({ runs: [], total: 0 }),
    },
  };
});

vi.mock('@/api/endpoints/notifications', async (importOriginal) => {
  const actual = await importOriginal<typeof import('@/api/endpoints/notifications')>();
  return {
    ...actual,
    notificationsApi: {
      ...actual.notificationsApi,
      list: vi.fn().mockResolvedValue({ notifications: [], total: 0 }),
      unreadCount: vi.fn().mockResolvedValue(0),
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

// Ticket writes are support/admin only on the API. The forms therefore sit under
// their own pathless ProtectedRoute layout so a deep link cannot reach them.
describe('ticket routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  it.each(['/tickets/new', '/tickets/1/edit'])(
    'blocks a sales user from %s',
    async (path) => {
      authUser = createMockUser({ role: 'sales' });

      renderAt(path);

      expect(await screen.findByText('Access Denied')).toBeInTheDocument();
      expect(
        screen.queryByRole('heading', { name: /ticket/i })
      ).not.toBeInTheDocument();
    }
  );

  it.each(['admin', 'support'] as const)(
    'lets a %s user reach the ticket create form',
    async (role) => {
      authUser = createMockUser({ role });

      renderAt('/tickets/new');

      expect(
        await screen.findByRole(
          'heading',
          { name: 'Create New Ticket' },
          { timeout: 15000 }
        )
      ).toBeInTheDocument();
      expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
    },
    20000
  );

  it('leaves the ticket list reachable for a sales user', async () => {
    authUser = createMockUser({ role: 'sales' });

    renderAt('/tickets');

    expect(
      await screen.findByRole('heading', { name: 'Tickets' }, { timeout: 15000 })
    ).toBeInTheDocument();
    expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
  }, 20000);
});

// Companies are admin/sales/support on the API; writes are admin/sales. The
// list and detail sit under one pathless guard, the forms under a nested,
// narrower one, so a support deep link to the form is refused.
describe('company routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  it.each(['admin', 'sales', 'support'] as const)(
    'lets a %s user reach the company list',
    async (role) => {
      authUser = createMockUser({ role });

      renderAt('/companies');

      expect(
        await screen.findByRole('heading', { name: 'Companies' }, { timeout: 15000 })
      ).toBeInTheDocument();
      expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
    },
    20000
  );

  it.each(['/companies', '/companies/1', '/companies/new', '/companies/1/edit'])(
    'blocks a customer user from %s',
    async (path) => {
      authUser = createMockUser({ role: 'customer' });

      renderAt(path);

      expect(await screen.findByText('Access Denied')).toBeInTheDocument();
    }
  );

  it.each(['/companies/new', '/companies/1/edit'])(
    'blocks a support user from %s',
    async (path) => {
      authUser = createMockUser({ role: 'support' });

      renderAt(path);

      expect(await screen.findByText('Access Denied')).toBeInTheDocument();
      expect(
        screen.queryByRole('heading', { name: /company/i })
      ).not.toBeInTheDocument();
    }
  );

  it('lets a sales user reach the company create form', async () => {
    authUser = createMockUser({ role: 'sales' });

    renderAt('/companies/new');

    expect(
      await screen.findByRole(
        'heading',
        { name: 'Create New Company' },
        { timeout: 15000 }
      )
    ).toBeInTheDocument();
    expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
  }, 20000);
});

// Deals are admin and sales only on the API, so the whole /deals group sits
// under one pathless guard: support and customer get "Access Denied" on every
// path, including deep links to the form.
describe('deal routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  it.each(['admin', 'sales'] as const)(
    'lets a %s user reach the deal list',
    async (role) => {
      authUser = createMockUser({ role });

      renderAt('/deals');

      expect(
        await screen.findByRole('heading', { name: 'Deals' }, { timeout: 15000 })
      ).toBeInTheDocument();
      expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
    },
    20000
  );

  it('lets a sales user reach the deal create form', async () => {
    authUser = createMockUser({ role: 'sales' });

    renderAt('/deals/new');

    expect(
      await screen.findByRole('heading', { name: 'Create New Deal' }, { timeout: 15000 })
    ).toBeInTheDocument();
    expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
  }, 20000);

  it.each(['admin', 'sales'] as const)(
    'lets a %s user reach the deal board, not the detail route',
    async (role) => {
      authUser = createMockUser({ role });

      renderAt('/deals/board');

      expect(
        await screen.findByRole('heading', { name: 'Deal pipeline' }, { timeout: 15000 })
      ).toBeInTheDocument();
      expect(screen.queryByText('Access Denied')).not.toBeInTheDocument();
      expect(screen.queryByText('Deal not found')).not.toBeInTheDocument();
    },
    20000
  );

  const dealPaths = ['/deals', '/deals/board', '/deals/new', '/deals/1', '/deals/1/edit'];

  it.each(dealPaths)('blocks a support user from %s', async (path) => {
    authUser = createMockUser({ role: 'support' });

    renderAt(path);

    expect(await screen.findByText('Access Denied')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /deal/i })).not.toBeInTheDocument();
  });

  it.each(dealPaths)('blocks a customer user from %s', async (path) => {
    authUser = createMockUser({ role: 'customer' });

    renderAt(path);

    expect(await screen.findByText('Access Denied')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /deal/i })).not.toBeInTheDocument();
  });
});

// Products and quotes share the deal guard: admin and sales only. The product
// form routes sit under a nested admin-only guard, so a sales deep link to
// them is refused like a support or customer one. The two cases below are
// single tests on purpose: the runner verifies them by their exact names.
describe('products and quotes routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  const guardedPaths = ['/products', '/products/new', '/quotes', '/quotes/1', '/quotes/1/print'];

  it('redirect support and customer users away from /products, /products/new, /quotes, /quotes/1 and /quotes/1/print', async () => {
    for (const role of ['support', 'customer'] as const) {
      for (const path of guardedPaths) {
        authUser = createMockUser({ role });

        const view = renderAt(path);

        // The data router resolves the lazy page module before the guard
        // renders, hence the generous wait on the first visit of each path.
        expect(await view.findByText('Access Denied', {}, { timeout: 15000 })).toBeInTheDocument();
        expect(view.queryByRole('heading', { name: /product|quote/i })).not.toBeInTheDocument();
        view.unmount();
      }
    }
  }, 90000);

  it('render the list and quote pages for admin and sales users and redirect sales away from /products/new and /products/1/edit', async () => {
    const expectedHeadings: Array<[string, string | RegExp]> = [
      ['/products', 'Products'],
      ['/quotes', 'Quotes'],
      ['/quotes/1', 'Q-000001'],
      ['/quotes/1/print', 'Quote Q-000001'],
    ];

    for (const role of ['admin', 'sales'] as const) {
      for (const [path, heading] of expectedHeadings) {
        authUser = createMockUser({ role });

        const view = renderAt(path);

        // The generous timeout covers the dynamic import of each page module.
        expect(await view.findByRole('heading', { name: heading }, { timeout: 15000 })).toBeInTheDocument();
        expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
        view.unmount();
      }
    }

    for (const path of ['/products/new', '/products/1/edit']) {
      authUser = createMockUser({ role: 'sales' });

      const view = renderAt(path);

      expect(await view.findByText('Access Denied', {}, { timeout: 15000 })).toBeInTheDocument();
      expect(view.queryByRole('heading', { name: /product/i })).not.toBeInTheDocument();
      view.unmount();
    }

    authUser = createMockUser({ role: 'admin' });
    const adminForm = renderAt('/products/new');
    expect(
      await adminForm.findByRole('heading', { name: 'Create New Product' }, { timeout: 15000 })
    ).toBeInTheDocument();
    expect(adminForm.queryByText('Access Denied')).not.toBeInTheDocument();
  }, 90000);
});

// Rules and the dry run are admin-only on the API; the run history is admin
// and sales; notifications are every staff role.
describe('automation and notification routes', () => {
  beforeEach(() => {
    authUser = null;
  });

  const denied = async (path: string) => {
    const view = renderAt(path);
    // The data router resolves the lazy page module before the guard renders.
    expect(await view.findByText('Access Denied', {}, { timeout: 15000 })).toBeInTheDocument();
    view.unmount();
  };

  it('redirect sales, support and customer users away from /automation/rules, /automation/rules/new and /automation/rules/1/edit', async () => {
    for (const role of ['sales', 'support', 'customer'] as const) {
      for (const path of ['/automation/rules', '/automation/rules/new', '/automation/rules/1/edit']) {
        authUser = createMockUser({ role });
        await denied(path);
      }
    }
  }, 90000);

  it('redirect support and customer users away from /automation/runs and /automation/runs/1', async () => {
    for (const role of ['support', 'customer'] as const) {
      for (const path of ['/automation/runs', '/automation/runs/1']) {
        authUser = createMockUser({ role });
        await denied(path);
      }
    }
  }, 90000);

  it('redirect customer users away from /notifications', async () => {
    authUser = createMockUser({ role: 'customer' });
    await denied('/notifications');
  }, 30000);

  it('render the rule list for admin, the run list for admin and sales and the notifications page for support', async () => {
    const cases: Array<[string, string, string]> = [
      ['admin', '/automation/rules', 'Automation rules'],
      ['admin', '/automation/runs', 'Automation runs'],
      ['sales', '/automation/runs', 'Automation runs'],
      ['support', '/notifications', 'Notifications'],
    ];
    for (const [role, path, heading] of cases) {
      authUser = createMockUser({ role: role as 'admin' | 'sales' | 'support' });
      const view = renderAt(path);
      expect(await view.findByRole('heading', { name: heading }, { timeout: 15000 })).toBeInTheDocument();
      expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
      view.unmount();
    }
  }, 90000);
});

// Leads are admin and sales only on the API (the whole /leads group is
// RequireRole(admin, sales)), and so is POST /customers. Customer list, detail
// and edit stay open to every authenticated role: the API scopes those reads
// and updates itself.
describe('lead and customer creation routes', () => {
  beforeEach(() => {
    setAuth(null, false);
  });

  const leadPaths = ['/leads', '/leads/new', '/leads/1', '/leads/1/edit'];

  it('redirect support and customer users away from every lead route and /customers/new', async () => {
    for (const role of ['support', 'customer'] as const) {
      for (const path of [...leadPaths, '/customers/new']) {
        setAuth(createMockUser({ role }), false);

        const view = renderAt(path);

        // The data router resolves the lazy page module before the guard renders.
        expect(await view.findByText('Access Denied', {}, { timeout: 15000 })).toBeInTheDocument();
        expect(view.queryByRole('heading', { name: /lead|customer/i })).not.toBeInTheDocument();
        view.unmount();
      }
    }
  }, 90000);

  it('render the lead list, the lead form and the customer create form for admin and sales users', async () => {
    const expectedHeadings: Array<[string, string]> = [
      ['/leads', 'Leads'],
      ['/leads/new', 'Create New Lead'],
      ['/customers/new', 'Create New Customer'],
    ];

    for (const role of ['admin', 'sales'] as const) {
      for (const [path, heading] of expectedHeadings) {
        setAuth(createMockUser({ role }), false);

        const view = renderAt(path);

        // The generous timeout covers the dynamic import of each page module.
        expect(await view.findByRole('heading', { name: heading }, { timeout: 15000 })).toBeInTheDocument();
        expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
        view.unmount();
      }
    }
  }, 90000);

  it('keep the customer list reachable for support and customer users', async () => {
    for (const role of ['support', 'customer'] as const) {
      setAuth(createMockUser({ role }), false);

      const view = renderAt('/customers');

      expect(await view.findByRole('heading', { name: 'Customers' }, { timeout: 15000 })).toBeInTheDocument();
      expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
      view.unmount();
    }
  }, 60000);

  it('hold a deep link on the spinner while the session loads, then render the lead list for sales', async () => {
    setAuth(null, true);

    const view = renderAt('/leads');

    expect(await view.findByRole('progressbar', {}, { timeout: 15000 })).toBeInTheDocument();
    expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
    expect(view.queryByRole('heading', { name: 'Leads' })).not.toBeInTheDocument();

    act(() => setAuth(createMockUser({ role: 'sales' }), false));

    expect(await view.findByRole('heading', { name: 'Leads' }, { timeout: 15000 })).toBeInTheDocument();
    expect(view.queryByText('Access Denied')).not.toBeInTheDocument();
    view.unmount();
  }, 30000);

  it('hold a deep link on the spinner while the session loads, then redirect a support user away from /customers/new', async () => {
    setAuth(null, true);

    const view = renderAt('/customers/new');

    expect(await view.findByRole('progressbar', {}, { timeout: 15000 })).toBeInTheDocument();
    expect(view.queryByRole('heading', { name: 'Create New Customer' })).not.toBeInTheDocument();

    act(() => setAuth(createMockUser({ role: 'support' }), false));

    expect(await view.findByText('Access Denied', {}, { timeout: 15000 })).toBeInTheDocument();
    expect(view.queryByRole('heading', { name: 'Create New Customer' })).not.toBeInTheDocument();
    view.unmount();
  }, 30000);
});
