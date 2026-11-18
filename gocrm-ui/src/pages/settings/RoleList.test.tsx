import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate } from 'react-router-dom';
import { Component as RoleList } from './RoleList';
import { rolesApi, type Role } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  rolesApi: {
    getRoles: vi.fn(),
    deleteRole: vi.fn(),
    cloneRole: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

// The signed-in user is a customer by name in every case: only the
// permission list may show an action.
const signedInWith = (permissions: Permission[]) =>
  mockUseAuth.mockReturnValue({
    user: createMockUser({ id: 1, role: 'customer' }),
    permissions,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  });

// A custom role that carries a system role's name and a system role with an
// unusual one: is_system decides, never the name.
const systemRole: Role = {
  id: 2,
  name: 'Field crew',
  description: '',
  system_key: 'sales',
  is_system: true,
  user_count: 4,
  permissions: customPermissions('leads.view', 'deals.view', 'tasks.view'),
};
const customRole: Role = {
  id: 9,
  name: 'Sales',
  description: 'A custom role',
  system_key: null,
  is_system: false,
  user_count: 3,
  permissions: customPermissions('leads.view'),
};

const apiError = (status: number, code: string, details?: Record<string, unknown>) => ({
  response: { status, data: { code, message: `server says ${code}`, details } },
});

const rowOf = async (name: string) => {
  const row = (await screen.findByRole('link', { name })).closest('tr');
  if (!row) throw new Error(`no row for ${name}`);
  return within(row);
};

describe('RoleList', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    signedInWith(customPermissions('roles.view', 'roles.manage'));
    vi.mocked(rolesApi.getRoles).mockResolvedValue([systemRole, customRole]);
  });

  it('offers only Clone on a system role', async () => {
    render(<RoleList />);

    const system = await rowOf('Field crew');
    expect(system.getByText('System')).toBeInTheDocument();
    expect(system.getByRole('button', { name: 'Clone Field crew' })).toBeInTheDocument();
    expect(system.queryByRole('button', { name: /^Edit / })).not.toBeInTheDocument();
    expect(system.queryByRole('button', { name: /^Delete / })).not.toBeInTheDocument();
    // User count and permission count.
    expect(system.getByText('4')).toBeInTheDocument();
    expect(system.getByText('3')).toBeInTheDocument();

    const custom = await rowOf('Sales');
    expect(custom.getByText('Custom')).toBeInTheDocument();
    expect(custom.getByRole('button', { name: 'Edit Sales' })).toBeInTheDocument();
    expect(custom.getByRole('button', { name: 'Clone Sales' })).toBeInTheDocument();
    expect(custom.getByRole('button', { name: 'Delete Sales' })).toBeInTheDocument();

    expect(screen.getByRole('button', { name: 'New role' })).toBeInTheDocument();
    fireEvent.click(custom.getByRole('button', { name: 'Edit Sales' }));
    expect(mockNavigate).toHaveBeenCalledWith('/settings/roles/9');
  });

  it('shows no actions without roles.manage', async () => {
    signedInWith(customPermissions('roles.view'));
    render(<RoleList />);

    const system = await rowOf('Field crew');
    const custom = await rowOf('Sales');
    for (const row of [system, custom]) {
      expect(row.queryByRole('button')).not.toBeInTheDocument();
    }
    expect(screen.queryByRole('button', { name: 'New role' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^(Clone|Edit|Delete)/ })).not.toBeInTheDocument();
    expect(screen.queryByText('Actions')).not.toBeInTheDocument();
    // The name still opens the read-only editor.
    expect(screen.getByRole('link', { name: 'Sales' })).toHaveAttribute('href', '/settings/roles/9');
  });

  it('keeps the delete confirmation open and shows the ROLE_IN_USE user count', async () => {
    vi.mocked(rolesApi.deleteRole).mockRejectedValue(
      apiError(409, 'ROLE_IN_USE', { user_count: 3 }),
    );
    render(<RoleList />);

    fireEvent.click((await rowOf('Sales')).getByRole('button', { name: 'Delete Sales' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete role' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(rolesApi.deleteRole).toHaveBeenCalledWith(9));
    expect(await within(dialog).findByText(/Still assigned to 3 users/)).toBeInTheDocument();
    expect(screen.getByRole('dialog', { name: 'Delete role' })).toBeInTheDocument();
  });

  it('refuses an empty clone name and posts the typed name', async () => {
    vi.mocked(rolesApi.cloneRole).mockResolvedValue({ ...customRole, id: 12, name: 'Field Sales' });
    render(<RoleList />);

    fireEvent.click((await rowOf('Field crew')).getByRole('button', { name: 'Clone Field crew' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone role' });
    const submit = within(dialog).getByRole('button', { name: 'Clone' });
    const nameField = within(dialog).getByRole('textbox', { name: /Name of the new role/ });

    // Empty, and blank: the button is off and submitting the form posts nothing.
    expect(submit).toBeDisabled();
    fireEvent.change(nameField, { target: { value: '   ' } });
    expect(submit).toBeDisabled();
    const form = nameField.closest('form');
    if (!form) throw new Error('the clone dialog has no form');
    fireEvent.submit(form);
    expect(rolesApi.cloneRole).not.toHaveBeenCalled();

    fireEvent.change(nameField, { target: { value: 'Field Sales' } });
    fireEvent.click(submit);

    await waitFor(() => expect(rolesApi.cloneRole).toHaveBeenCalledWith(2, { name: 'Field Sales' }));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/settings/roles/12'));
  });

  it('shows ROLE_NAME_TAKEN on the clone name field and keeps the dialog open', async () => {
    vi.mocked(rolesApi.cloneRole).mockRejectedValue(apiError(409, 'ROLE_NAME_TAKEN'));
    render(<RoleList />);

    fireEvent.click((await rowOf('Field crew')).getByRole('button', { name: 'Clone Field crew' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone role' });
    const nameField = within(dialog).getByRole('textbox', { name: /Name of the new role/ });
    fireEvent.change(nameField, { target: { value: 'Sales' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Clone' }));

    await waitFor(() => expect(nameField).toHaveAttribute('aria-invalid', 'true'));
    expect(nameField).toHaveAccessibleDescription('Another role already has this name');
    expect(screen.getByRole('dialog', { name: 'Clone role' })).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
