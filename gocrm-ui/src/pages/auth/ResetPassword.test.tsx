import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ResetPassword } from './ResetPassword';

const mockResetPassword = vi.fn();

vi.mock('@/api/endpoints', () => ({
  authApi: {
    resetPassword: (token: string, newPassword: string) => mockResetPassword(token, newPassword),
  },
}));

const renderResetPassword = (initialUrl = '/reset-password?token=valid-token-123') => {
  return render(
    <MemoryRouter initialEntries={[initialUrl]}>
      <ResetPassword />
    </MemoryRouter>,
  );
};

describe('ResetPassword component', () => {
  beforeEach(() => {
    mockResetPassword.mockReset();
  });

  it('renders warning when token is missing', () => {
    renderResetPassword('/reset-password');
    expect(screen.getByText(/invalid or missing reset token/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /request reset link/i })).toBeInTheDocument();
  });

  it('renders password inputs and submit button when token is present', () => {
    renderResetPassword();
    expect(screen.getByRole('heading', { name: /reset password/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/^new password/i)).toBeInTheDocument();
    expect(screen.getByLabelText(/confirm new password/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^reset password/i })).toBeInTheDocument();
  });

  it('enforces password policy on submit', async () => {
    const user = userEvent.setup();
    renderResetPassword();

    // Type a weak password (under 10 chars)
    await user.type(screen.getByLabelText(/^new password/i), 'Weak1!');
    await user.type(screen.getByLabelText(/confirm new password/i), 'Weak1!');
    await user.click(screen.getByRole('button', { name: /^reset password/i }));

    expect(await screen.findByText(/at least 10 characters/i)).toBeInTheDocument();
    expect(mockResetPassword).not.toHaveBeenCalled();
  });

  it('validates password confirmation match', async () => {
    const user = userEvent.setup();
    renderResetPassword();

    await user.type(screen.getByLabelText(/^new password/i), 'ValidPass123!');
    await user.type(screen.getByLabelText(/confirm new password/i), 'DifferentPass123!');
    await user.click(screen.getByRole('button', { name: /^reset password/i }));

    expect(await screen.findByText(/passwords don't match/i)).toBeInTheDocument();
    expect(mockResetPassword).not.toHaveBeenCalled();
  });

  it('submits valid reset request and displays success screen', async () => {
    mockResetPassword.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderResetPassword();

    await user.type(screen.getByLabelText(/^new password/i), 'ValidPass123!');
    await user.type(screen.getByLabelText(/confirm new password/i), 'ValidPass123!');
    await user.click(screen.getByRole('button', { name: /^reset password/i }));

    await waitFor(() => {
      expect(mockResetPassword).toHaveBeenCalledWith('valid-token-123', 'ValidPass123!');
    });

    expect(await screen.findByText(/password has been successfully reset/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /sign in/i })).toBeInTheDocument();
  });
});
