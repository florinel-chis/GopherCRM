import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as UserList } from './UserList';
import { usersApi, type UserFilters } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  usersApi: {
    getUsers: vi.fn(),
    deleteUser: vi.fn(),
    activateUser: vi.fn(),
    deactivateUser: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const signedInAs = (user: User) =>
  mockUseAuth.mockReturnValue({
    user,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

// The per-row overflow menu carries the one user action the row icons do not:
// activation and deactivation (admin only on the API). It must act on the
// row whose button was pressed, whatever the current order of the table.
describe('UserList row actions', () => {
  const mockNavigate = vi.fn();
  const me = createMockUser({
    id: 1,
    first_name: 'Ada',
    last_name: 'Admin',
    role: 'admin',
    is_active: true,
  });
  const users = [
    me,
    createMockUser({
      id: 2,
      first_name: 'Ben',
      last_name: 'Active',
      role: 'sales',
      is_active: true,
    }),
    createMockUser({
      id: 3,
      first_name: 'Cara',
      last_name: 'Dormant',
      role: 'support',
      is_active: false,
    }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    signedInAs(me);
    vi.mocked(usersApi.getUsers).mockImplementation(async (filters?: UserFilters) => {
      const data = filters?.sort_by ? [...users].reverse() : users;
      return { data, total: data.length, page: 1, limit: 10, total_pages: 1 };
    });
    vi.mocked(usersApi.activateUser).mockResolvedValue(users[2]);
    vi.mocked(usersApi.deactivateUser).mockResolvedValue(users[1]);
  });

  const openMenu = async (name: string) => {
    fireEvent.click(screen.getByRole('button', { name: `More actions for ${name}` }));
    return screen.findByRole('menu', { name: `More actions for ${name}` });
  };

  it('deactivates the chosen active user after the table was re-sorted', async () => {
    render(<UserList />);
    await screen.findByText('Ben Active');

    fireEvent.click(screen.getByRole('button', { name: 'Name' }));
    await waitFor(() => expect(screen.getAllByRole('row')[1]).toHaveTextContent('Cara Dormant'));

    const menu = await openMenu('Ben Active');
    expect(within(menu).queryByRole('menuitem', { name: 'Activate' })).not.toBeInTheDocument();
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Deactivate' }));

    await waitFor(() => expect(usersApi.deactivateUser).toHaveBeenCalledWith(2));
    expect(usersApi.activateUser).not.toHaveBeenCalled();
    // The menu item must not fall through to the row's own click.
    expect(mockNavigate).not.toHaveBeenCalledWith('/users/2');
  });

  it('activates the chosen inactive user', async () => {
    render(<UserList />);
    await screen.findByText('Cara Dormant');

    const menu = await openMenu('Cara Dormant');
    fireEvent.click(within(menu).getByRole('menuitem', { name: 'Activate' }));

    await waitFor(() => expect(usersApi.activateUser).toHaveBeenCalledWith(3));
    expect(usersApi.deactivateUser).not.toHaveBeenCalled();
  });

  // The API refuses self-deactivation ("You cannot deactivate your own account").
  it('does not let an admin deactivate their own account', async () => {
    render(<UserList />);
    await screen.findByText('Ada Admin');

    const menu = await openMenu('Ada Admin');
    const item = within(menu).getByRole('menuitem', { name: 'Deactivate' });
    expect(item).toHaveAttribute('aria-disabled', 'true');
    fireEvent.click(item);
    expect(usersApi.deactivateUser).not.toHaveBeenCalled();
  });

  it('has no row-less overflow button in the table toolbar', async () => {
    render(<UserList />);
    await screen.findByText('Ada Admin');

    for (const icon of screen.queryAllByTestId('MoreVertIcon')) {
      expect(icon.closest('button')).toHaveAccessibleName(/^More actions for /);
    }
    expect(screen.getAllByRole('button', { name: /^More actions for / })).toHaveLength(3);
  });

  it('shows no overflow menu to a non-admin', async () => {
    signedInAs(createMockUser({ id: 9, role: 'sales' }));
    render(<UserList />);
    await screen.findByText('Ada Admin');

    expect(screen.queryByTestId('MoreVertIcon')).not.toBeInTheDocument();
  });
});
