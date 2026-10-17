import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { ForgotPassword } from './ForgotPassword';

const mockRequestPasswordReset = vi.fn();

vi.mock('@/api/endpoints', () => ({
  authApi: {
    requestPasswordReset: (email: string) => mockRequestPasswordReset(email),
  },
}));

const renderForgotPassword = () => {
  return render(
    <MemoryRouter>
      <ForgotPassword />
    </MemoryRouter>,
  );
};

describe('ForgotPassword component', () => {
  beforeEach(() => {
    mockRequestPasswordReset.mockReset();
  });

  it('renders email input and submit button', () => {
    renderForgotPassword();
    expect(screen.getByRole('heading', { name: /forgot password/i })).toBeInTheDocument();
    expect(screen.getByLabelText(/email address/i)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /send reset link/i })).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /sign in/i })).toBeInTheDocument();
  });

  it('validates email field on submission', async () => {
    const user = userEvent.setup();
    renderForgotPassword();

    await user.click(screen.getByRole('button', { name: /send reset link/i }));
    expect(await screen.findByText(/invalid email address/i)).toBeInTheDocument();
    expect(mockRequestPasswordReset).not.toHaveBeenCalled();
  });

  it('submits successfully and displays confirmation message', async () => {
    mockRequestPasswordReset.mockResolvedValue(undefined);
    const user = userEvent.setup();
    renderForgotPassword();

    await user.type(screen.getByLabelText(/email address/i), 'user@example.com');
    await user.click(screen.getByRole('button', { name: /send reset link/i }));

    await waitFor(() => {
      expect(mockRequestPasswordReset).toHaveBeenCalledWith('user@example.com');
    });

    expect(await screen.findByText(/instructions have been sent/i)).toBeInTheDocument();
    expect(screen.getByRole('link', { name: /back to sign in/i })).toBeInTheDocument();
  });

  it('displays API error on failure', async () => {
    mockRequestPasswordReset.mockRejectedValue({
      response: { data: { message: 'Too many reset attempts' } },
    });
    const user = userEvent.setup();
    renderForgotPassword();

    await user.type(screen.getByLabelText(/email address/i), 'user@example.com');
    await user.click(screen.getByRole('button', { name: /send reset link/i }));

    expect(await screen.findByText(/too many reset attempts/i)).toBeInTheDocument();
  });
});
