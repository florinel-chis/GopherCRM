import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Login } from './Login';
import { permissionsOf } from '@/test/permissions';

const mockGetRegistrationStatus = vi.fn();
const mockStartOIDC = vi.fn();

vi.mock('@/api/endpoints', () => ({
  authApi: {
    getRegistrationStatus: () => mockGetRegistrationStatus(),
    startOIDC: () => mockStartOIDC(),
  },
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: null,
    permissions: permissionsOf(null),
    isLoading: false,
    isAuthenticated: false,
    login: vi.fn(),
    loginWithSSO: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

const renderLogin = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Login />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('Login sign-up link (gated by security.allow_public_registration)', () => {
  beforeEach(() => {
    mockGetRegistrationStatus.mockReset();
  });

  it('shows the Sign Up link once the probe reports registration open', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: true });
    renderLogin();

    expect(await screen.findByRole('link', { name: /sign up/i })).toBeInTheDocument();
  });

  it('hides the Sign Up link when registration is disabled', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: false });
    renderLogin();

    await waitFor(() => expect(mockGetRegistrationStatus).toHaveBeenCalled());
    expect(screen.queryByRole('link', { name: /sign up/i })).not.toBeInTheDocument();
    // The rest of the page is untouched by the gate.
    expect(screen.getByRole('link', { name: /forgot password/i })).toBeInTheDocument();
  });

  it('hides the Sign Up link when the probe fails (the backend would refuse anyway)', async () => {
    mockGetRegistrationStatus.mockRejectedValue(new Error('network down'));
    renderLogin();

    await waitFor(() => expect(mockGetRegistrationStatus).toHaveBeenCalled());
    expect(screen.queryByRole('link', { name: /sign up/i })).not.toBeInTheDocument();
  });
});

const SSO_BUTTON = { name: 'Sign in with single sign-on' };

const expectPasswordForm = () => {
  expect(screen.getByLabelText(/email address/i)).toBeInTheDocument();
  expect(screen.getByLabelText(/^password/i)).toBeInTheDocument();
  expect(screen.getByRole('button', { name: 'Sign In' })).toBeInTheDocument();
  expect(screen.getByRole('link', { name: /forgot password/i })).toBeInTheDocument();
};

describe('Login single sign-on button (gated by sso_enabled)', () => {
  const assign = vi.fn();
  const originalLocation = window.location;

  beforeEach(() => {
    mockGetRegistrationStatus.mockReset();
    mockStartOIDC.mockReset();
    assign.mockReset();
    Object.defineProperty(window, 'location', {
      configurable: true,
      value: { ...originalLocation, assign },
    });
    return () => {
      Object.defineProperty(window, 'location', { configurable: true, value: originalLocation });
    };
  });

  it('shows the button when the probe reports single sign-on on, next to the password form', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: false, sso_enabled: true });
    renderLogin();

    expect(await screen.findByRole('button', SSO_BUTTON)).toBeInTheDocument();
    expectPasswordForm();
  });

  it.each([
    ['sso_enabled false', () => mockGetRegistrationStatus.mockResolvedValue({ enabled: true, sso_enabled: false })],
    ['a missing sso_enabled', () => mockGetRegistrationStatus.mockResolvedValue({ enabled: true })],
    ['a failed probe', () => mockGetRegistrationStatus.mockRejectedValue(new Error('network down'))],
  ])('hides the button for %s', async (_, arrange) => {
    arrange();
    renderLogin();

    await waitFor(() => expect(mockGetRegistrationStatus).toHaveBeenCalled());
    // Let the probe settle before asserting the absence.
    await act(async () => {});
    expect(screen.queryByRole('button', SSO_BUTTON)).not.toBeInTheDocument();
    expectPasswordForm();
  });

  it('starts the sign-in once and sends the browser to the authorization URL', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: false, sso_enabled: true });
    mockStartOIDC.mockResolvedValue({ authorization_url: 'https://issuer.example/authorize?x=1' });
    renderLogin();

    await userEvent.click(await screen.findByRole('button', SSO_BUTTON));

    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://issuer.example/authorize?x=1'));
    expect(mockStartOIDC).toHaveBeenCalledTimes(1);
    expect(assign).toHaveBeenCalledTimes(1);
  });

  it('shows the unavailable message and stays on the page when the start fails', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: false, sso_enabled: true });
    mockStartOIDC.mockRejectedValue(Object.assign(new Error('503'), { response: { status: 503, data: {} } }));
    renderLogin();

    await userEvent.click(await screen.findByRole('button', SSO_BUTTON));

    expect(
      await screen.findByText('Single sign-on is unavailable. Sign in with your password.')
    ).toBeInTheDocument();
    expect(assign).not.toHaveBeenCalled();
    expect(screen.getByRole('button', SSO_BUTTON)).toBeEnabled();
    expectPasswordForm();
  });
});
