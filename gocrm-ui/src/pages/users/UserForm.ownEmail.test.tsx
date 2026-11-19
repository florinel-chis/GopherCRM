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
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const SIGNED_IN_ID = 1;
const OWN_PASSWORD = 'Curr3nt!Passw0rd';

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

const emailField = () => screen.getByLabelText(/^Email/);
const passwordField = () => screen.queryByLabelText(/^Current password/);
const typeEmail = (value: string) => fireEvent.change(emailField(), { target: { value } });
const save = () => fireEvent.click(screen.getByRole('button', { name: 'Update User' }));

describe('UserForm: changing your own email', () => {
  const mockNavigate = vi.fn();

  const openEdit = async (account: User) => {
    vi.mocked(useParams).mockReturnValue({ id: String(account.id) });
    vi.mocked(usersApi.getUser).mockResolvedValue(account);
    render(<UserForm />);
    await screen.findByRole('button', { name: 'Update User' });
  };

  const savedBody = async () => {
    await waitFor(() => expect(usersApi.updateUser).toHaveBeenCalledTimes(1));
    return vi.mocked(usersApi.updateUser).mock.calls[0];
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    // No users.assign_role: the role picker and GET /roles stay out of the way.
    mockUseAuth.mockReturnValue({
      user: createMockUser({ id: SIGNED_IN_ID, email: ownAccount.email, role: 'sales' }),
      permissions: customPermissions('users.update'),
      isLoading: false,
      isAuthenticated: true,
      login: vi.fn(),
      register: vi.fn(),
      logout: vi.fn(),
      refreshUser: vi.fn(),
    });
    vi.mocked(rolesApi.getRoles).mockResolvedValue([]);
    vi.mocked(usersApi.updateUser).mockResolvedValue(createMockUser({ id: SIGNED_IN_ID }));
  });

  it('own account with a changed email asks for the current password and sends it', async () => {
    await openEdit(ownAccount);
    expect(passwordField()).not.toBeInTheDocument();

    typeEmail('me.renamed@example.com');

    const field = await screen.findByLabelText(/^Current password/);
    expect(field).toBeRequired();
    expect(field).toHaveAttribute('type', 'password');
    expect(field).toHaveAttribute('name', 'current_password');
    expect(field).toHaveAttribute('autocomplete', 'current-password');

    fireEvent.change(field, { target: { value: OWN_PASSWORD } });
    save();

    const [id, body] = await savedBody();
    expect(id).toBe(SIGNED_IN_ID);
    expect(body).toEqual({
      email: 'me.renamed@example.com',
      first_name: 'Mia',
      last_name: 'Myself',
      is_active: true,
      current_password: OWN_PASSWORD,
    });
  });

  it('own account with a changed email is not sent without the current password', async () => {
    await openEdit(ownAccount);
    typeEmail('me.renamed@example.com');
    const field = await screen.findByLabelText(/^Current password/);

    // The Update button: the required field stops the submit in the browser.
    save();
    expect(field).toBeInvalid();
    expect(usersApi.updateUser).not.toHaveBeenCalled();

    // A submit that gets past that check is stopped by the form itself.
    fireEvent.submit(field.closest('form') as HTMLFormElement);
    expect(
      await screen.findByText('Enter your current password to change your email'),
    ).toBeInTheDocument();
    expect(usersApi.updateUser).not.toHaveBeenCalled();
  });

  it('own account with the loaded email shows no field and sends no current_password', async () => {
    await openEdit(ownAccount);
    fireEvent.change(screen.getByLabelText(/^First Name/), { target: { value: 'Renamed' } });

    expect(passwordField()).not.toBeInTheDocument();
    save();

    const [, body] = await savedBody();
    expect(body).toEqual({
      email: 'me@example.com',
      first_name: 'Renamed',
      last_name: 'Myself',
      is_active: true,
    });
    expect(body).not.toHaveProperty('current_password');
  });

  it('own account whose email is typed back drops the field and the typed password', async () => {
    await openEdit(ownAccount);
    typeEmail('me.renamed@example.com');
    fireEvent.change(await screen.findByLabelText(/^Current password/), {
      target: { value: OWN_PASSWORD },
    });

    typeEmail('me@example.com');
    await waitFor(() => expect(passwordField()).not.toBeInTheDocument());
    save();

    const [, body] = await savedBody();
    expect(body).not.toHaveProperty('current_password');
    expect(JSON.stringify(body)).not.toContain(OWN_PASSWORD);

    // Changing the email again asks again: the earlier value is gone.
    typeEmail('me.again@example.com');
    expect(await screen.findByLabelText(/^Current password/)).toHaveValue('');
  });

  it('another account with a changed email shows no field and sends no current_password', async () => {
    await openEdit(otherAccount);

    typeEmail('other.renamed@example.com');

    expect(passwordField()).not.toBeInTheDocument();
    save();

    const [id, body] = await savedBody();
    expect(id).toBe(otherAccount.id);
    expect(body).toEqual({
      email: 'other.renamed@example.com',
      first_name: 'Otto',
      last_name: 'Other',
      is_active: true,
    });
    expect(body).not.toHaveProperty('current_password');
  });

  it.each([
    ['INVALID_CURRENT_PASSWORD', 'The current password is incorrect'],
    ['CURRENT_PASSWORD_REQUIRED', 'Changing your email requires your current password'],
  ])('a 400 %s keeps the form open and shows the server message', async (code, message) => {
    vi.mocked(usersApi.updateUser).mockRejectedValue({
      response: { status: 400, data: { code, message } },
    });
    await openEdit(ownAccount);
    typeEmail('me.renamed@example.com');
    fireEvent.change(await screen.findByLabelText(/^Current password/), {
      target: { value: 'Wr0ng!Passw0rd' },
    });

    save();

    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
    expect(screen.getByRole('button', { name: 'Update User' })).toBeInTheDocument();
    expect(emailField()).toHaveValue('me.renamed@example.com');
  });
});
