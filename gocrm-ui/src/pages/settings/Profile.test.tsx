import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen, waitFor } from '@/test/test-utils';
import { Component as Profile } from './Profile';
import { authApi } from '@/api/endpoints';
import type { User } from '@/types';

const showSuccess = vi.fn();
const showError = vi.fn();

// Hoisted so the vi.mock factories below — which vitest lifts above every
// top-level const — can reference it.
const { adminUser } = vi.hoisted(() => ({
  adminUser: {
    id: 1,
    email: 'test-admin@gocrm.test',
    first_name: 'Test',
    last_name: 'Admin',
    role: 'admin',
    is_active: true,
    created_at: '2026-01-15T10:00:00Z',
    updated_at: '2026-01-15T10:00:00Z',
  } as User,
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: adminUser,
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

vi.mock('@/api/endpoints', () => ({
  authApi: {
    changePassword: vi.fn(),
    getCurrentUser: vi.fn().mockResolvedValue(adminUser),
  },
}));

const fillPasswordForm = async (current: string) => {
  const user = userEvent.setup();
  await user.type(screen.getByLabelText(/^current password/i), current);
  await user.type(screen.getByLabelText(/^new password/i), 'NewStrongPass1!');
  await user.type(screen.getByLabelText(/^confirm new password/i), 'NewStrongPass1!');
  await user.click(screen.getByRole('button', { name: /change password/i }));
};

describe('Profile', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('renders the signed-in user details', () => {
    render(<Profile />);

    expect(screen.getByText('Test Admin')).toBeInTheDocument();
    expect(screen.getByText('test-admin@gocrm.test')).toBeInTheDocument();
    expect(screen.getByText('admin')).toBeInTheDocument();
    // Member since, formatted from created_at.
    expect(screen.getByText(/Jan 15, 2026/)).toBeInTheDocument();
  });

  it('changes the password and reports that other sessions were signed out', async () => {
    vi.mocked(authApi.changePassword).mockResolvedValue({ renewed: true });

    render(<Profile />);
    await fillPasswordForm('OldStrongPass1!');

    await waitFor(() => {
      expect(authApi.changePassword).toHaveBeenCalledWith('OldStrongPass1!', 'NewStrongPass1!');
    });

    const message = await screen.findByText(/You stay signed in here/i);
    expect(message).toHaveTextContent(/other sessions can no longer renew themselves/i);
    // No blocklist: the copy must not promise an immediate sign-out elsewhere.
    expect(message).toHaveTextContent(/access they already hold ends when it expires/i);
    expect(message).not.toHaveTextContent(/signed out immediately/i);
    expect(showSuccess).toHaveBeenCalledWith('Password changed successfully');
  });

  it('explains that every session must sign in again when no session was renewed', async () => {
    vi.mocked(authApi.changePassword).mockResolvedValue({ renewed: false });

    render(<Profile />);
    await fillPasswordForm('OldStrongPass1!');

    expect(
      await screen.findByText(/Every session, including this one, will need to sign in again/i)
    ).toBeInTheDocument();
  });

  it('tells the user to sign in again when the session changed during the request', async () => {
    vi.mocked(authApi.changePassword).mockRejectedValue({
      response: {
        status: 409,
        data: {
          code: 'SESSION_STALE',
          message: 'Your session is no longer current. Sign in again, then change your password.',
        },
      },
    });

    render(<Profile />);
    await fillPasswordForm('OldStrongPass1!');

    expect(await screen.findByText(/Sign in again, then change your password/i)).toBeInTheDocument();
    expect(showSuccess).not.toHaveBeenCalled();
  });

  it('surfaces the server message when the current password is wrong', async () => {
    // Shape after the client's error interceptor has peeled the envelope:
    // response.data IS the inner `error` object.
    vi.mocked(authApi.changePassword).mockRejectedValue({
      response: {
        status: 400,
        data: { code: 'BAD_REQUEST', message: 'The current password is incorrect' },
      },
    });

    render(<Profile />);
    await fillPasswordForm('WrongPass1!');

    expect(await screen.findByText('The current password is incorrect')).toBeInTheDocument();
    expect(showSuccess).not.toHaveBeenCalled();
  });

  it('rejects a new password that fails the complexity policy', async () => {
    const user = userEvent.setup();
    render(<Profile />);

    await user.type(screen.getByLabelText(/^current password/i), 'OldStrongPass1!');
    await user.type(screen.getByLabelText(/^new password/i), 'short');
    await user.type(screen.getByLabelText(/^confirm new password/i), 'short');
    await user.click(screen.getByRole('button', { name: /change password/i }));

    expect(await screen.findByText('Password must be at least 10 characters')).toBeInTheDocument();
    expect(authApi.changePassword).not.toHaveBeenCalled();
  });
});
