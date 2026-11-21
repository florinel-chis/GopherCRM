import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { Component as UserForm } from './UserForm';
import { rolesApi, usersApi } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import { customPermissions } from '@/test/permissions';
import type { User } from '@/types';

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
const mockRefreshUser = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const SIGNED_IN_ID = 1;

const ownAccount = createMockUser({
  id: SIGNED_IN_ID,
  email: 'me@example.com',
  first_name: 'Mia',
  last_name: 'Myself',
  role: 'sales',
  is_active: true,
});
const otherAccount = createMockUser({
  id: 5,
  email: 'other@example.com',
  first_name: 'Otto',
  last_name: 'Other',
  role: 'sales',
  is_active: true,
});

const renameTo = (value: string) =>
  fireEvent.change(screen.getByLabelText(/^First Name/), { target: { value } });
const save = () => fireEvent.click(screen.getByRole('button', { name: 'Update User' }));

describe('UserForm: reloading the signed-in user after a save', () => {
  const mockNavigate = vi.fn();

  const openEdit = async (account: User) => {
    vi.mocked(useParams).mockReturnValue({ id: String(account.id) });
    vi.mocked(usersApi.getUser).mockResolvedValue(account);
    render(<UserForm />);
    await screen.findByRole('button', { name: 'Update User' });
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockRefreshUser.mockResolvedValue(undefined);
    // No users.assign_role: the role picker and GET /roles stay out of the way.
    mockUseAuth.mockReturnValue({
      user: createMockUser({ id: SIGNED_IN_ID, email: ownAccount.email, role: 'sales' }),
      permissions: customPermissions('users.update'),
      isLoading: false,
      isAuthenticated: true,
      login: vi.fn(),
      register: vi.fn(),
      logout: vi.fn(),
      refreshUser: mockRefreshUser,
    });
    vi.mocked(rolesApi.getRoles).mockResolvedValue([]);
    vi.mocked(usersApi.updateUser).mockResolvedValue(createMockUser({ id: SIGNED_IN_ID }));
  });

  it('saving your own account reloads the signed-in user once', async () => {
    await openEdit(ownAccount);
    renameTo('Renamed');
    save();

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/users'));
    expect(usersApi.updateUser).toHaveBeenCalledTimes(1);
    expect(vi.mocked(usersApi.updateUser).mock.calls[0][0]).toBe(SIGNED_IN_ID);
    expect(mockRefreshUser).toHaveBeenCalledTimes(1);
    expect(mockRefreshUser.mock.invocationCallOrder[0]).toBeGreaterThan(
      vi.mocked(usersApi.updateUser).mock.invocationCallOrder[0],
    );
  });

  it('saving another account does not reload the signed-in user', async () => {
    vi.mocked(usersApi.updateUser).mockResolvedValue(otherAccount);
    await openEdit(otherAccount);
    renameTo('Renamed');
    save();

    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/users'));
    expect(vi.mocked(usersApi.updateUser).mock.calls[0][0]).toBe(otherAccount.id);
    expect(mockRefreshUser).not.toHaveBeenCalled();
  });

  it('a refused save of your own account does not reload the signed-in user', async () => {
    vi.mocked(usersApi.updateUser).mockRejectedValue({
      response: { status: 409, data: { code: 'LAST_ADMIN', message: 'server says LAST_ADMIN' } },
    });
    await openEdit(ownAccount);
    renameTo('Renamed');
    save();

    expect(
      await screen.findByText(
        'The last active administrator cannot be given another role or be deactivated.',
      ),
    ).toBeInTheDocument();
    expect(usersApi.updateUser).toHaveBeenCalledTimes(1);
    expect(mockRefreshUser).not.toHaveBeenCalled();
    expect(mockNavigate).not.toHaveBeenCalled();
  });
});
