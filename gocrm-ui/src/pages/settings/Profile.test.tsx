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
    (authApi.changePassword as any).mockResolvedValue(undefined);

    render(<Profile />);
    await fillPasswordForm('OldStrongPass1!');

    await waitFor(() => {
      expect(authApi.changePassword).toHaveBeenCalledWith('OldStrongPass1!', 'NewStrongPass1!');
    });

    expect(await screen.findByText(/other sessions have been signed out/i)).toBeInTheDocument();
    expect(showSuccess).toHaveBeenCalledWith('Password changed successfully');
  });

  it('surfaces the server message when the current password is wrong', async () => {
    // Shape after the client's error interceptor has peeled the envelope:
    // response.data IS the inner `error` object.
    (authApi.changePassword as any).mockRejectedValue({
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
