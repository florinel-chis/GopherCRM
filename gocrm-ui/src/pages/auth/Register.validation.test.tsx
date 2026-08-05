import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { Register } from './Register';

// The registerSchema is module-private, so the password rules are exercised
// through the rendered form: type a password, submit, assert the zod message
// surfaced as the field's helper text.
//
// The schema must match the backend rules in internal/utils/password.go:
// min 10 chars, uppercase, lowercase, digit AND special character.

const mockRegister = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: null,
    isLoading: false,
    isAuthenticated: false,
    login: vi.fn(),
    register: mockRegister,
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

const MESSAGES = {
  min: 'Password must be at least 10 characters',
  upper: 'Password must contain at least one uppercase letter',
  lower: 'Password must contain at least one lowercase letter',
  digit: 'Password must contain at least one number',
  special: 'Password must contain at least one special character',
};

const renderForm = () => render(
  <MemoryRouter>
    <Register />
  </MemoryRouter>
);

const fillForm = (password: string) => {
  fireEvent.change(screen.getByLabelText(/first name/i), {
    target: { value: 'Jane' },
  });
  fireEvent.change(screen.getByLabelText(/last name/i), {
    target: { value: 'Doe' },
  });
  fireEvent.change(screen.getByLabelText(/email address/i), {
    target: { value: 'jane.doe@example.com' },
  });
  fireEvent.change(screen.getByLabelText(/^Password/), {
    target: { value: password },
  });
  fireEvent.change(screen.getByLabelText(/^Confirm Password/), {
    target: { value: password },
  });
};

const submit = () => {
  fireEvent.click(screen.getByRole('button', { name: /sign up/i }));
};

describe('Register password validation (must match backend internal/utils/password.go)', () => {
  beforeEach(() => {
    mockRegister.mockReset();
    mockRegister.mockResolvedValue(undefined);
  });

  it('rejects "Password1" (regression: 9 chars, no special char — previously accepted)', async () => {
    renderForm();
    fillForm('Password1');
    submit();

    expect(
      await screen.findByText(MESSAGES.min)
    ).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it('accepts "Password1!" and submits the registration', async () => {
    renderForm();
    fillForm('Password1!');
    submit();

    await waitFor(() => {
      expect(mockRegister).toHaveBeenCalledTimes(1);
    });
    expect(mockRegister).toHaveBeenCalledWith(
      expect.objectContaining({ password: 'Password1!' })
    );
    expect(screen.queryByText(MESSAGES.min)).not.toBeInTheDocument();
    expect(screen.queryByText(MESSAGES.special)).not.toBeInTheDocument();
  });

  it('rejects passwords shorter than 10 characters with the length message', async () => {
    renderForm();
    fillForm('Abc1!efgh'); // 9 chars, every other rule satisfied
    submit();

    expect(await screen.findByText(MESSAGES.min)).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it('rejects passwords without an uppercase letter', async () => {
    renderForm();
    fillForm('abcdef123!@'); // 11 chars, lower + digit + special, no upper
    submit();

    expect(await screen.findByText(MESSAGES.upper)).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it('rejects passwords without a lowercase letter', async () => {
    renderForm();
    fillForm('ABCDEF123!@'); // 11 chars, upper + digit + special, no lower
    submit();

    expect(await screen.findByText(MESSAGES.lower)).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it('rejects passwords without a digit', async () => {
    renderForm();
    fillForm('Abcdefghi!!'); // 11 chars, upper + lower + special, no digit
    submit();

    expect(await screen.findByText(MESSAGES.digit)).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });

  it('rejects passwords without a special character', async () => {
    renderForm();
    fillForm('Abcdefgh123'); // 11 chars, upper + lower + digit, no special
    submit();

    expect(await screen.findByText(MESSAGES.special)).toBeInTheDocument();
    expect(mockRegister).not.toHaveBeenCalled();
  });
});
