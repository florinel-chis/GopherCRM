import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter } from 'react-router-dom';
import { Register } from './Register';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import { authApi, configurationsApi } from '@/api/endpoints';

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

vi.mock('@/api/endpoints', () => ({
  authApi: { getRegistrationStatus: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

describe('Register product name', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv('VITE_APP_NAME', 'Build CRM');
    vi.mocked(authApi.getRegistrationStatus).mockResolvedValue({ enabled: true });
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('shows the build-time name on the first render while signed out without requesting the configuration', async () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    // Registration is known to be open, so the form renders on the first pass.
    queryClient.setQueryData(['registration-status'], { enabled: true });

    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter>
          <ConfigurationProvider>
            <Register />
          </ConfigurationProvider>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    // Synchronously after the first render: no later swap (no flash).
    expect(screen.getByText('Create your Build CRM account')).toBeInTheDocument();

    await waitFor(() => expect(authApi.getRegistrationStatus).toHaveBeenCalled());
    await act(async () => {});
    expect(screen.getByText('Create your Build CRM account')).toBeInTheDocument();
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });
});
