import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { Component as UserForm } from './UserForm';
import { rolesApi, usersApi, type Role } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { Permission, User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  usersApi: {
    getUser: vi.fn(),
    createUser: vi.fn(),
    updateUser: vi.fn(),
  },
  rolesApi: {
    getRoles: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

// The signed-in user is a customer by name in every case: only the
// permission list may show the picker.
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

const systemRole = (id: number, key: string, name: string): Role => ({
  id,
  name,
  description: '',
  system_key: key,
  is_system: true,
  user_count: 1,
  permissions: [],
});
const customRole = (id: number, name: string): Role => ({
  id,
  name,
  description: '',
  system_key: null,
  is_system: false,
  user_count: 0,
  permissions: [],
});

const roles: Role[] = [
  systemRole(1, 'admin', 'Admin'),
  systemRole(2, 'sales', 'Sales'),
  systemRole(3, 'support', 'Support'),
  systemRole(4, 'customer', 'Customer'),
  customRole(7, 'Field Sales'),
  customRole(8, 'Back office'),
];

const apiError = (status: number, code: string) => ({
  response: { status, data: { code, message: `server says ${code}` } },
});

const picker = () => screen.getByRole('combobox', { name: /Role/ });

const openPicker = async () => {
  fireEvent.mouseDown(picker());
  return within(await screen.findByRole('listbox'));
};

const pick = async (label: string) => {
  const list = await openPicker();
  fireEvent.click(list.getByRole('option', { name: label }));
  await waitFor(() => expect(picker()).toHaveTextContent(label));
};

const fill = (label: RegExp, value: string) =>
  fireEvent.change(screen.getByLabelText(label), { target: { value } });

const fillNewUser = () => {
  fill(/^Email/, 'new.user@example.com');
  fill(/^Password/, 'Str0ng!Passw0rd');
  fill(/^Confirm Password/, 'Str0ng!Passw0rd');
  fill(/^First Name/, 'Nia');
  fill(/^Last Name/, 'Newcomer');
};

describe('UserForm', () => {
  const mockNavigate = vi.fn();
  const errorSpy = vi.spyOn(console, 'error');
  const warnSpy = vi.spyOn(console, 'warn');

  const openNew = async () => {
    vi.mocked(useParams).mockReturnValue({});
    render(<UserForm />);
    await screen.findByRole('button', { name: 'Create User' });
  };

  const openEdit = async (account: User) => {
    vi.mocked(useParams).mockReturnValue({ id: String(account.id) });
    vi.mocked(usersApi.getUser).mockResolvedValue(account);
    render(<UserForm />);
    await screen.findByRole('button', { name: 'Update User' });
  };

  const renameAndSave = async () => {
    fill(/^First Name/, 'Renamed');
    fireEvent.click(screen.getByRole('button', { name: 'Update User' }));
    await waitFor(() => expect(usersApi.updateUser).toHaveBeenCalledTimes(1));
    return vi.mocked(usersApi.updateUser).mock.calls[0];
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    signedInWith(customPermissions('users.create', 'users.update', 'users.assign_role'));
    vi.mocked(rolesApi.getRoles).mockResolvedValue(roles);
    vi.mocked(usersApi.createUser).mockResolvedValue(createMockUser({ id: 50 }));
    vi.mocked(usersApi.updateUser).mockResolvedValue(createMockUser({ id: 5 }));
  });

  afterEach(() => {
    // A select whose value has no option (MUI "out-of-range value") would mean
    // the picker was shown before its options or lost the stored role.
    for (const spy of [errorSpy, warnSpy]) {
      for (const call of spy.mock.calls) {
        expect(String(call[0])).not.toContain('out-of-range');
      }
    }
  });

  it('role picker sends role custom and role_id for a custom role', async () => {
    await openNew();

    // The options are the roles of GET /roles, by name; sales is the default.
    expect(picker()).toHaveTextContent('Sales');
    const list = await openPicker();
    expect(list.getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Admin', 'Sales', 'Support', 'Customer', 'Field Sales', 'Back office',
    ]);
    fireEvent.click(list.getByRole('option', { name: 'Field Sales' }));
    await waitFor(() => expect(picker()).toHaveTextContent('Field Sales'));

    fillNewUser();
    fireEvent.click(screen.getByRole('button', { name: 'Create User' }));

    await waitFor(() => expect(usersApi.createUser).toHaveBeenCalledTimes(1));
    expect(vi.mocked(usersApi.createUser).mock.calls[0][0]).toEqual({
      email: 'new.user@example.com',
      password: 'Str0ng!Passw0rd',
      first_name: 'Nia',
      last_name: 'Newcomer',
      role: 'custom',
      role_id: 7,
    });
  });

  it('role picker sends the system role name without role_id', async () => {
    await openNew();

    await pick('Support');
    fillNewUser();
    fireEvent.click(screen.getByRole('button', { name: 'Create User' }));

    await waitFor(() => expect(usersApi.createUser).toHaveBeenCalledTimes(1));
    const body = vi.mocked(usersApi.createUser).mock.calls[0][0];
    expect(body.role).toBe('support');
    expect('role_id' in body).toBe(false);
  });

  it('role picker option values are the system key or custom:<id>', async () => {
    await openNew();

    // The browser page object picks a role by li[data-value="sales"].
    const list = await openPicker();
    expect(list.getByRole('option', { name: 'Sales' })).toHaveAttribute('data-value', 'sales');
    expect(list.getByRole('option', { name: 'Customer' })).toHaveAttribute('data-value', 'customer');
    expect(list.getByRole('option', { name: 'Field Sales' })).toHaveAttribute('data-value', 'custom:7');
  });

  it('keeps a custom role when only the name is edited', async () => {
    await openEdit(createMockUser({ id: 5, role: 'custom', role_id: 7 }));
    expect(picker()).toHaveTextContent('Field Sales');

    const [id, body] = await renameAndSave();
    expect(id).toBe(5);
    expect(body.first_name).toBe('Renamed');
    expect(body.role).toBe('custom');
    expect(body.role_id).toBe(7);
  });

  it('keeps a system role when only the name is edited', async () => {
    await openEdit(createMockUser({ id: 5, role: 'sales' }));
    expect(picker()).toHaveTextContent('Sales');

    const [, body] = await renameAndSave();
    expect(body.role).toBe('sales');
    expect('role_id' in body).toBe(false);
  });

  it('keeps a system role other than the create default when only the name is edited', async () => {
    await openEdit(createMockUser({ id: 5, role: 'support' }));
    expect(picker()).toHaveTextContent('Support');

    const [, body] = await renameAndSave();
    expect(body.role).toBe('support');
    expect('role_id' in body).toBe(false);
  });

  it('keeps a custom role that is missing from the role list', async () => {
    await openEdit(createMockUser({ id: 5, role: 'custom', role_id: 250 }));
    // The stored role is shown, not a blank field and not another role.
    expect(picker()).toHaveTextContent('Role #250');

    const [, body] = await renameAndSave();
    expect(body.role).toBe('custom');
    expect(body.role_id).toBe(250);
  });

  it('drops role_id when a custom-role user is moved to a system role', async () => {
    await openEdit(createMockUser({ id: 5, role: 'custom', role_id: 7 }));

    await pick('Sales');
    fireEvent.click(screen.getByRole('button', { name: 'Update User' }));

    await waitFor(() => expect(usersApi.updateUser).toHaveBeenCalledTimes(1));
    const body = vi.mocked(usersApi.updateUser).mock.calls[0][1];
    expect(body.role).toBe('sales');
    expect('role_id' in body).toBe(false);
  });

  it('does not show the edit form before the roles arrive', async () => {
    let deliver: (value: Role[]) => void = () => {};
    vi.mocked(rolesApi.getRoles).mockReturnValue(
      new Promise<Role[]>((resolve) => {
        deliver = resolve;
      }),
    );
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(usersApi.getUser).mockResolvedValue(
      createMockUser({ id: 5, role: 'custom', role_id: 7 }),
    );
    render(<UserForm />);

    await waitFor(() => expect(usersApi.getUser).toHaveBeenCalled());
    await waitFor(() => expect(rolesApi.getRoles).toHaveBeenCalled());
    // The user is there, the roles are not: nothing to submit yet.
    await Promise.resolve();
    expect(screen.queryByRole('button', { name: 'Update User' })).not.toBeInTheDocument();

    deliver(roles);
    await screen.findByRole('button', { name: 'Update User' });
    expect(picker()).toHaveTextContent('Field Sales');
  });

  it('shows an error instead of the form when the roles cannot be loaded', async () => {
    vi.mocked(rolesApi.getRoles).mockRejectedValue(apiError(500, 'INTERNAL_ERROR'));
    vi.mocked(useParams).mockReturnValue({});
    render(<UserForm />);

    expect(await screen.findByText(/The roles could not be loaded/)).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Create User' })).not.toBeInTheDocument();
  });

  it('shows no picker, reads no roles and creates a customer without users.assign_role', async () => {
    signedInWith(customPermissions('users.create', 'users.update'));
    await openNew();

    expect(screen.queryByRole('combobox', { name: /Role/ })).not.toBeInTheDocument();
    expect(screen.getByText('New account is created as a customer')).toBeInTheDocument();

    fillNewUser();
    fireEvent.click(screen.getByRole('button', { name: 'Create User' }));

    await waitFor(() => expect(usersApi.createUser).toHaveBeenCalledTimes(1));
    const body = vi.mocked(usersApi.createUser).mock.calls[0][0];
    expect(body.role).toBe('customer');
    expect('role_id' in body).toBe(false);
    expect(rolesApi.getRoles).not.toHaveBeenCalled();
  });

  it('sends neither role nor role_id on update without users.assign_role', async () => {
    signedInWith(customPermissions('users.create', 'users.update'));
    await openEdit(createMockUser({ id: 5, role: 'custom', role_id: 7 }));

    expect(screen.queryByRole('combobox', { name: /Role/ })).not.toBeInTheDocument();
    expect(screen.queryByText('New account is created as a customer')).not.toBeInTheDocument();

    const [, body] = await renameAndSave();
    expect(body.first_name).toBe('Renamed');
    expect('role' in body).toBe(false);
    expect('role_id' in body).toBe(false);
    expect(rolesApi.getRoles).not.toHaveBeenCalled();
  });

  it.each([
    ['LAST_ADMIN', 'The last active administrator cannot be given another role or be deactivated.'],
    ['CUSTOMER_LOGIN_ROLE', 'A customer login cannot be given a custom role.'],
    [
      'ROLE_ESCALATION',
      'You cannot edit an account or give a role whose permissions are above your own.',
    ],
  ])('shows the sentence of %s in the form on update', async (code, sentence) => {
    vi.mocked(usersApi.updateUser).mockRejectedValue(apiError(409, code));
    await openEdit(createMockUser({ id: 5, role: 'admin' }));

    await renameAndSave();

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(sentence);
    // In the form itself, not only in a snackbar.
    expect(alert.closest('form')).not.toBeNull();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it.each([
    ['LAST_ADMIN', 'The last active administrator cannot be given another role or be deactivated.'],
    ['CUSTOMER_LOGIN_ROLE', 'A customer login cannot be given a custom role.'],
    [
      'ROLE_ESCALATION',
      'You cannot edit an account or give a role whose permissions are above your own.',
    ],
  ])('shows the sentence of %s in the form on create', async (code, sentence) => {
    vi.mocked(usersApi.createUser).mockRejectedValue(apiError(403, code));
    await openNew();

    fillNewUser();
    fireEvent.click(screen.getByRole('button', { name: 'Create User' }));

    await waitFor(() => expect(usersApi.createUser).toHaveBeenCalledTimes(1));
    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent(sentence);
    expect(alert.closest('form')).not.toBeNull();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('offers a customer user the system roles and no custom role', async () => {
    await openEdit(createMockUser({ id: 5, role: 'customer' }));
    expect(picker()).toHaveTextContent('Customer');

    const list = await openPicker();
    // A customer login can still be moved to a system role.
    expect(list.getAllByRole('option').map((option) => option.textContent)).toEqual([
      'Admin', 'Sales', 'Support', 'Customer',
    ]);
    expect(list.queryByRole('option', { name: 'Field Sales' })).not.toBeInTheDocument();
  });
});
