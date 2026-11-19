import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render } from '@testing-library/react';
import { createMemoryRouter, RouterProvider } from 'react-router-dom';
import type { RouteObject } from 'react-router-dom';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

// The route table against custom permission lists. The pages themselves are
// replaced by markers: what is under test is which route renders its page
// and which one redirects.

// See index.test.tsx: the data router builds a Request with a jsdom signal
// that undici rejects; no request is made here, so the signal is dropped.
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

const auth = vi.hoisted(() => ({ permissions: [] as Array<{ key: string; scope: 'all' | 'own' }> }));

vi.mock('@/hooks/useAuth', async () => {
  const { createMockUser: mockUser } = await import('@/test/factories');
  // An admin by name: only the permission list may open a route.
  const user = mockUser({ id: 1, role: 'admin' });
  return {
    useAuth: () => ({
      user,
      permissions: auth.permissions,
      isLoading: false,
      isAuthenticated: true,
      login: vi.fn(),
      register: vi.fn(),
      logout: vi.fn(),
      refreshUser: vi.fn(),
    }),
  };
});

vi.mock('@/layouts/MainLayout', async () => {
  const { Outlet } = await import('react-router-dom');
  return { MainLayout: () => <Outlet /> };
});

vi.mock('@/pages/Dashboard', () => ({ Dashboard: () => <div>Dashboard Page</div> }));
vi.mock('@/pages/settings/Profile', () => ({ Component: () => <div>Profile Page</div> }));
vi.mock('@/pages/settings/APIKeys', () => ({ Component: () => <div>API Keys Page</div> }));
vi.mock('@/pages/tickets/TicketList', () => ({ Component: () => <div>Ticket List Page</div> }));
vi.mock('@/pages/tickets/TicketDetail', () => ({ Component: () => <div>Ticket Detail Page</div> }));
vi.mock('@/pages/tasks/TaskList', () => ({ Component: () => <div>Task List Page</div> }));
vi.mock('@/pages/tasks/TaskForm', () => ({ Component: () => <div>Task Form Page</div> }));
vi.mock('@/pages/tasks/TaskDetail', () => ({ Component: () => <div>Task Detail Page</div> }));
vi.mock('@/pages/labels/LabelList', () => ({ Component: () => <div>Label List Page</div> }));
vi.mock('@/pages/customers/CustomerList', () => ({ Component: () => <div>Customer List Page</div> }));
vi.mock('@/pages/customers/CustomerDetail', () => ({ Component: () => <div>Customer Detail Page</div> }));
vi.mock('@/pages/customers/CustomerForm', () => ({ Component: () => <div>Customer Form Page</div> }));
vi.mock('@/pages/settings/RoleList', () => ({ Component: () => <div>Role List Page</div> }));
vi.mock('@/pages/settings/RoleEditor', () => ({ Component: () => <div>Role Editor Page</div> }));
vi.mock('@/pages/settings/Webhooks', () => ({ Component: () => <div>Webhooks Page</div> }));
vi.mock('@/pages/settings/SSOSettings', () => ({ Component: () => <div>SSO Settings Page</div> }));

import { router } from './index';

const signedInWith = (permissions: Permission[]) => {
  auth.permissions = permissions;
};

const renderAt = (path: string) => {
  const memoryRouter = createMemoryRouter(router.routes as unknown as RouteObject[], {
    initialEntries: [path],
  });
  return { ...render(<RouterProvider router={memoryRouter} />), memoryRouter };
};

/** Renders the path and waits for the page marker or the Unauthorized page. */
const landOn = async (path: string, marker: string) => {
  const view = renderAt(path);
  const found = await view.findByText(marker, {}, { timeout: 15000 });
  expect(found).toBeInTheDocument();
  const pathname = view.memoryRouter.state.location.pathname;
  view.unmount();
  return pathname;
};

describe('routes', () => {
  beforeEach(() => {
    signedInWith([]);
  });

  it('redirect a custom role without the key from tickets, tasks and labels and leave the listed routes unguarded', async () => {
    // A custom role that holds real keys, but none of the tickets, tasks,
    // labels or customers ones.
    signedInWith(customPermissions('leads.view', 'deals.view', 'notifications.use'));

    for (const path of [
      '/tickets',
      '/tickets/1',
      '/tasks',
      '/tasks/new',
      '/tasks/1',
      '/tasks/1/edit',
      '/labels',
      '/customers',
      '/customers/1',
      '/customers/1/edit',
    ]) {
      expect(await landOn(path, 'Access Denied'), path).toBe('/unauthorized');
    }

    // No key at all: the three routes without a guard still render.
    signedInWith([]);
    expect(await landOn('/', 'Dashboard Page')).toBe('/');
    expect(await landOn('/settings/profile', 'Profile Page')).toBe('/settings/profile');
    expect(await landOn('/settings/api-keys', 'API Keys Page')).toBe('/settings/api-keys');

    // The read key of a customer login opens the ticket list and detail.
    signedInWith(customPermissions('tickets.view_own_customer'));
    expect(await landOn('/tickets', 'Ticket List Page')).toBe('/tickets');
    expect(await landOn('/tickets/1', 'Ticket Detail Page')).toBe('/tickets/1');
  }, 120000);

  it('open each guarded page for a caller holding exactly its key', async () => {
    const cases: Array<[path: string, key: string, marker: string]> = [
      ['/tickets', 'tickets.view', 'Ticket List Page'],
      ['/tasks', 'tasks.view', 'Task List Page'],
      ['/tasks/new', 'tasks.view', 'Task Form Page'],
      ['/tasks/1', 'tasks.view', 'Task Detail Page'],
      ['/tasks/1/edit', 'tasks.view', 'Task Form Page'],
      ['/labels', 'labels.view', 'Label List Page'],
      ['/customers', 'customers.view', 'Customer List Page'],
      ['/customers/1', 'customers.view', 'Customer Detail Page'],
      ['/customers/1/edit', 'customers.update', 'Customer Form Page'],
    ];
    for (const [path, key, marker] of cases) {
      signedInWith(customPermissions(key));
      expect(await landOn(path, marker), path).toBe(path);
    }

    // The read key does not open the edit form.
    signedInWith(customPermissions('customers.view'));
    expect(await landOn('/customers/1/edit', 'Access Denied')).toBe('/unauthorized');
  }, 120000);

  it('open the role list and a role with roles.view and the new-role form only with roles.manage', async () => {
    signedInWith(customPermissions('roles.view'));
    expect(await landOn('/settings/roles', 'Role List Page')).toBe('/settings/roles');
    expect(await landOn('/settings/roles/7', 'Role Editor Page')).toBe('/settings/roles/7');
    // "new" is its own route, not an id of the read route.
    expect(await landOn('/settings/roles/new', 'Access Denied')).toBe('/unauthorized');

    signedInWith(customPermissions('roles.view', 'roles.manage'));
    expect(await landOn('/settings/roles/new', 'Role Editor Page')).toBe('/settings/roles/new');

    // Other keys, the settings one included, open none of the three.
    signedInWith(customPermissions('settings.view', 'users.view', 'users.assign_role'));
    for (const path of ['/settings/roles', '/settings/roles/7', '/settings/roles/new']) {
      expect(await landOn(path, 'Access Denied'), path).toBe('/unauthorized');
    }
  }, 120000);

  it('open the webhooks screen only with webhooks.manage', async () => {
    signedInWith(customPermissions('webhooks.manage'));
    expect(await landOn('/settings/webhooks', 'Webhooks Page')).toBe('/settings/webhooks');

    // The settings key and the role-managing key do not stand in for it.
    signedInWith(customPermissions('settings.view', 'roles.manage'));
    expect(await landOn('/settings/webhooks', 'Access Denied')).toBe('/unauthorized');

    signedInWith([]);
    expect(await landOn('/settings/webhooks', 'Access Denied')).toBe('/unauthorized');
  }, 120000);

  it('open the single sign-on screen only with settings.sso_manage', async () => {
    signedInWith(customPermissions('settings.sso_manage'));
    expect(await landOn('/settings/sso', 'SSO Settings Page')).toBe('/settings/sso');

    // The settings read key does not stand in for it.
    signedInWith(customPermissions('settings.view'));
    expect(await landOn('/settings/sso', 'Access Denied')).toBe('/unauthorized');
  }, 120000);
});
