import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Login } from './Login';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import { authApi, configurationsApi } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/api/endpoints', () => ({
  authApi: { getRegistrationStatus: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const authState = (user: User | null) => ({
  user,
  isLoading: false,
  isAuthenticated: !!user,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

// The real ConfigurationProvider, so the configuration request (or its
// absence) is the one the application makes.
const renderLogin = () =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter>
        <ConfigurationProvider>
          <Login />
        </ConfigurationProvider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

describe('Login product name', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv('VITE_APP_NAME', 'Build CRM');
    vi.mocked(authApi.getRegistrationStatus).mockResolvedValue({ enabled: false });
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('shows the build-time name when the configuration request fails', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1 })));
    vi.mocked(configurationsApi.getUIConfigurations).mockRejectedValue(
      new Error('Server unavailable'),
    );
    renderLogin();

    await waitFor(() => expect(configurationsApi.getUIConfigurations).toHaveBeenCalled());
    // Let the rejected query settle.
    await act(async () => {});

    expect(screen.getByText('Welcome to Build CRM')).toBeInTheDocument();
    expect(screen.queryByRole('alert')).not.toBeInTheDocument();
    expect(screen.queryByText(/Server unavailable/)).not.toBeInTheDocument();
  });

  it('shows the build-time name on the first render while signed out without requesting the configuration', async () => {
    mockUseAuth.mockReturnValue(authState(null));
    renderLogin();

    // Synchronously after the first render: no later swap (no flash).
    expect(screen.getByText('Welcome to Build CRM')).toBeInTheDocument();

    await waitFor(() => expect(authApi.getRegistrationStatus).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.getByText('Welcome to Build CRM')).toBeInTheDocument();
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });
});
