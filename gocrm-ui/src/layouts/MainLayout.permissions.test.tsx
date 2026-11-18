import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import userEvent from '@testing-library/user-event';
import { MainLayout } from './MainLayout';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

const mockUseAuth = vi.fn();
vi.mock('@/components/layout/NotificationBell', () => ({
  NotificationBell: () => <div data-testid="notification-bell" />,
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

// The role name is the same in every case and is one that saw Tickets before
// the conversion or not, as the case needs: only the list may decide.
const signedInWith = (role: 'admin' | 'customer', permissions: Permission[]) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: 1, role }),
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

// The permanent drawer is the navigation a desktop viewer sees; entries are
// looked up by text (see MainLayout.test.tsx).
const navigation = () => within(screen.getByTestId('app-navigation'));

describe('MainLayout permission gating', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('shows Tickets to a caller holding tickets.view', () => {
    signedInWith('customer', customPermissions('tickets.view'));
    const held = render(<MainLayout />);
    expect(navigation().getByText('Tickets')).toBeInTheDocument();
    held.unmount();

    // Without the key the entry is hidden, whatever the role is called.
    signedInWith('admin', customPermissions('leads.view', 'tasks.view'));
    render(<MainLayout />);
    expect(navigation().queryByText('Tickets')).not.toBeInTheDocument();
    expect(navigation().getByText('Leads')).toBeInTheDocument();
  });

  it('shows only the entries without a key to a caller with no permissions', async () => {
    signedInWith('admin', []);
    render(<MainLayout />);

    expect(navigation().getByText('Dashboard')).toBeInTheDocument();
    for (const title of [
      'Leads', 'Deals', 'Products', 'Quotes', 'Automation', 'Customers', 'Companies',
      'Tickets', 'Tasks', 'Labels', 'Forms', 'AEO', 'Users',
    ]) {
      expect(navigation().queryByText(title)).not.toBeInTheDocument();
    }

    await userEvent.click(navigation().getByText('Settings'));
    expect(navigation().getByText('Profile')).toBeInTheDocument();
    expect(navigation().getByText('API Keys')).toBeInTheDocument();
    expect(navigation().queryByText('Configuration')).not.toBeInTheDocument();
    expect(navigation().queryByText('Roles')).not.toBeInTheDocument();
  });

  it('shows Roles under Settings only to a caller holding roles.view', async () => {
    signedInWith('customer', customPermissions('roles.view'));
    const held = render(<MainLayout />);
    await userEvent.click(navigation().getByText('Settings'));
    expect(navigation().getByText('Roles')).toBeInTheDocument();
    expect(navigation().queryByText('Configuration')).not.toBeInTheDocument();
    held.unmount();

    // The settings key and the managing key alone do not show the entry.
    signedInWith('admin', customPermissions('settings.view', 'roles.manage'));
    render(<MainLayout />);
    await userEvent.click(navigation().getByText('Settings'));
    expect(navigation().getByText('Configuration')).toBeInTheDocument();
    expect(navigation().queryByText('Roles')).not.toBeInTheDocument();
  });

  it('shows the Automation group with only the child whose key is held', async () => {
    signedInWith('customer', customPermissions('automation.rules_manage'));
    render(<MainLayout />);

    await userEvent.click(navigation().getByText('Automation'));
    expect(navigation().getByText('Rules')).toBeInTheDocument();
    expect(navigation().queryByText('Runs')).not.toBeInTheDocument();
  });
});
