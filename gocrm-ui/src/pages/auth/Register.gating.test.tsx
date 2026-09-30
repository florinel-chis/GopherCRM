import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Register } from './Register';

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

const renderRegister = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={queryClient}>
      <MemoryRouter>
        <Register />
      </MemoryRouter>
    </QueryClientProvider>
  );
};

describe('Register page gating (security.allow_public_registration)', () => {
  beforeEach(() => {
    mockGetRegistrationStatus.mockReset();
  });

  it('renders the form when registration is open', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: true });
    renderRegister();

    expect(await screen.findByLabelText(/email address/i)).toBeInTheDocument();
    expect(screen.queryByText(/registration is disabled/i)).not.toBeInTheDocument();
  });

  it('renders the disabled notice instead of the form when registration is off', async () => {
    mockGetRegistrationStatus.mockResolvedValue({ enabled: false });
    renderRegister();

    expect(await screen.findByText(/registration is disabled/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/email address/i)).not.toBeInTheDocument();
    expect(screen.getByRole('link', { name: /back to sign in/i })).toBeInTheDocument();
  });

  it('renders the disabled notice when the probe fails', async () => {
    mockGetRegistrationStatus.mockRejectedValue(new Error('network down'));
    renderRegister();

    expect(await screen.findByText(/registration is disabled/i)).toBeInTheDocument();
    expect(screen.queryByLabelText(/email address/i)).not.toBeInTheDocument();
  });
});
