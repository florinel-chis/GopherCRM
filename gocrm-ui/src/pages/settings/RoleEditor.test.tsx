import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { Component as RoleEditor } from './RoleEditor';
import { rolesApi, type CatalogueNode, type Role } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  rolesApi: {
    getRole: vi.fn(),
    getCatalogue: vi.fn(),
    createRole: vi.fn(),
    updateRole: vi.fn(),
    cloneRole: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

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

// The shapes the API sends: a group and a system-only permission are both
// selectable: false; a permission that is always all has no scopes.
const catalogue: CatalogueNode[] = [
  {
    key: 'tickets',
    title: 'Tickets',
    scopes: [],
    selectable: false,
    children: [
      { key: 'tickets.create', title: 'Create tickets', scopes: [], selectable: true, children: [] },
      {
        key: 'tickets.view',
        title: 'View tickets',
        scopes: ['all', 'own'],
        selectable: true,
        children: [],
      },
      {
        key: 'tickets.view_own_customer',
        title: 'View the tickets of the own customer record',
        scopes: [],
        selectable: false,
        children: [],
      },
    ],
  },
  {
    key: 'labels',
    title: 'Labels',
    scopes: [],
    selectable: false,
    children: [
      { key: 'labels.view', title: 'View labels', scopes: [], selectable: true, children: [] },
    ],
  },
  {
    // Nothing a custom role can hold: the group is not shown.
    key: 'portal',
    title: 'Customer portal',
    scopes: [],
    selectable: false,
    children: [
      { key: 'portal.use', title: 'Use the portal', scopes: [], selectable: false, children: [] },
    ],
  },
];

const customRole: Role = {
  id: 9,
  name: 'Field Sales',
  description: 'Sales in the field',
  system_key: null,
  is_system: false,
  user_count: 2,
  permissions: [
    { key: 'labels.view', scope: 'all' },
    { key: 'tickets.view', scope: 'own' },
  ],
};

const apiError = (status: number, code: string, details?: Record<string, unknown>) => ({
  response: { status, data: { code, message: `server says ${code}`, details } },
});

const box = (name: string) => screen.getByRole('checkbox', { name });
const nameField = () => screen.getByRole('textbox', { name: /^Name/ });

describe('RoleEditor', () => {
  const mockNavigate = vi.fn();

  const openNew = async () => {
    vi.mocked(useParams).mockReturnValue({});
    render(<RoleEditor />);
    await screen.findByRole('checkbox', { name: 'Tickets' });
  };

  const openExisting = async (stored: Role) => {
    vi.mocked(useParams).mockReturnValue({ id: String(stored.id) });
    vi.mocked(rolesApi.getRole).mockResolvedValue(stored);
    render(<RoleEditor />);
    await screen.findByRole('checkbox', { name: 'Tickets' });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    signedInWith(customPermissions('roles.view', 'roles.manage'));
    vi.mocked(rolesApi.getCatalogue).mockResolvedValue(catalogue);
    vi.mocked(rolesApi.createRole).mockResolvedValue({ ...customRole, id: 20 });
    vi.mocked(rolesApi.updateRole).mockResolvedValue(customRole);
  });

  it('ticking a parent ticks every selectable child and sends their keys', async () => {
    await openNew();

    // The group is a parent although the API marks it selectable: false; the
    // childless system-only permission and the group holding nothing else
    // are not offered.
    expect(box('Tickets')).toBeInTheDocument();
    expect(box('Create tickets')).toBeInTheDocument();
    expect(box('View tickets')).toBeInTheDocument();
    expect(
      screen.queryByRole('checkbox', { name: 'View the tickets of the own customer record' }),
    ).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: 'Customer portal' })).not.toBeInTheDocument();
    expect(screen.queryByRole('checkbox', { name: 'Use the portal' })).not.toBeInTheDocument();

    fireEvent.click(box('Tickets'));
    expect(box('Tickets')).toBeChecked();
    expect(box('Create tickets')).toBeChecked();
    expect(box('View tickets')).toBeChecked();
    expect(box('View labels')).not.toBeChecked();

    // Only the permission that can be limited to own records has a selector;
    // a newly ticked one starts at All.
    expect(screen.queryByRole('combobox', { name: 'Scope of Create tickets' })).not.toBeInTheDocument();
    const scope = screen.getByRole('combobox', { name: 'Scope of View tickets' });
    expect(scope).toHaveValue('all');
    fireEvent.change(scope, { target: { value: 'own' } });

    fireEvent.change(nameField(), { target: { value: 'Ticket desk' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(rolesApi.createRole).toHaveBeenCalledTimes(1));
    expect(vi.mocked(rolesApi.createRole).mock.calls[0][0]).toEqual({
      name: 'Ticket desk',
      description: '',
      permissions: [
        { key: 'tickets.create', scope: 'all' },
        { key: 'tickets.view', scope: 'own' },
      ],
    });
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/settings/roles'));
  });

  it('shows the parent indeterminate while only some children are ticked', async () => {
    await openNew();

    fireEvent.click(box('Create tickets'));
    expect(box('Tickets')).not.toBeChecked();
    expect(box('Tickets')).toHaveAttribute('data-indeterminate', 'true');

    fireEvent.click(box('View tickets'));
    expect(box('Tickets')).toBeChecked();
    expect(box('Tickets')).toHaveAttribute('data-indeterminate', 'false');

    // Unticking the parent clears every child.
    fireEvent.click(box('Tickets'));
    expect(box('Create tickets')).not.toBeChecked();
    expect(box('View tickets')).not.toBeChecked();
    expect(box('Tickets')).toHaveAttribute('data-indeterminate', 'false');
  });

  it('sends an empty description and an empty permissions array on update', async () => {
    await openExisting(customRole);

    // The stored state: name, description, both permissions and the scope.
    expect(nameField()).toHaveValue('Field Sales');
    expect(box('View labels')).toBeChecked();
    expect(box('View tickets')).toBeChecked();
    expect(screen.getByRole('combobox', { name: 'Scope of View tickets' })).toHaveValue('own');

    fireEvent.change(screen.getByRole('textbox', { name: 'Description' }), { target: { value: '' } });
    fireEvent.click(box('View labels'));
    fireEvent.click(box('View tickets'));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(rolesApi.updateRole).toHaveBeenCalledTimes(1));
    const [id, body] = vi.mocked(rolesApi.updateRole).mock.calls[0];
    expect(id).toBe(9);
    expect('description' in body).toBe(true);
    expect('permissions' in body).toBe(true);
    expect(body).toEqual({ name: 'Field Sales', description: '', permissions: [] });
  });

  it('shows ROLE_NAME_TAKEN on the name field', async () => {
    vi.mocked(rolesApi.createRole).mockRejectedValue(apiError(409, 'ROLE_NAME_TAKEN'));
    await openNew();

    fireEvent.change(nameField(), { target: { value: 'Sales' } });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(nameField()).toHaveAttribute('aria-invalid', 'true'));
    expect(nameField()).toHaveAccessibleDescription('Another role already has this name');
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('names the refused keys of INVALID_PERMISSION at the permission tree', async () => {
    vi.mocked(rolesApi.createRole).mockRejectedValue(
      apiError(400, 'INVALID_PERMISSION', { keys: ['tickets.create'] }),
    );
    await openNew();

    fireEvent.change(nameField(), { target: { value: 'Ticket desk' } });
    fireEvent.click(box('Create tickets'));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent(
      'These permissions cannot be saved: tickets.create',
    );
    expect(nameField()).toHaveAttribute('aria-invalid', 'false');
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('does not save a role without a name', async () => {
    await openNew();

    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(nameField()).toHaveAccessibleDescription('Name is required'));
    expect(rolesApi.createRole).not.toHaveBeenCalled();
  });

  it('renders a system role read-only with Clone and no Save', async () => {
    vi.mocked(rolesApi.cloneRole).mockResolvedValue({ ...customRole, id: 21 });
    await openExisting({
      ...customRole,
      id: 2,
      // A system role that does not carry a system name.
      name: 'Field crew',
      system_key: 'sales',
      is_system: true,
    });

    expect(nameField()).toBeDisabled();
    expect(screen.getByRole('textbox', { name: 'Description' })).toBeDisabled();
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toBeDisabled();
    }
    expect(screen.getByRole('combobox', { name: 'Scope of View tickets' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    // The next-request notice belongs to a role that can be changed.
    expect(screen.queryByText(/on their next request/)).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Clone' }));
    const dialog = await screen.findByRole('dialog', { name: 'Clone role' });
    expect(dialog).toBeInTheDocument();
  });

  it('renders a custom role read-only, without Clone, for a caller lacking roles.manage', async () => {
    signedInWith(customPermissions('roles.view'));
    await openExisting(customRole);

    expect(nameField()).toBeDisabled();
    for (const checkbox of screen.getAllByRole('checkbox')) {
      expect(checkbox).toBeDisabled();
    }
    expect(screen.queryByRole('button', { name: 'Save' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Clone' })).not.toBeInTheDocument();
  });

  it('tells that a change to a custom role applies on the holders next request', async () => {
    await openExisting(customRole);

    expect(
      screen.getByText('A change applies to the users holding this role on their next request.'),
    ).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Save' })).toBeInTheDocument();
  });
});
