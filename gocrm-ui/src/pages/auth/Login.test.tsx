import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Login } from './Login';

const mockGetRegistrationStatus = vi.fn();

vi.mock('@/api/endpoints', () => ({
  authApi: {
    getRegistrationStatus: () => mockGetRegistrationStatus(),
  },
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: null,
    isLoading: false,
    isAuthenticated: false,
    login: vi.fn(),
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
