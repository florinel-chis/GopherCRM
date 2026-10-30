import { describe, it, expect, vi, beforeEach } from 'vitest';
import React from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConfigurationProvider } from './ConfigurationContext';
import { AuthContext, type AuthContextType } from './auth-context';
import { AuthProvider } from './AuthContext';
import { useConfiguration } from '@/hooks/useConfiguration';
import { useAuth } from '@/hooks/useAuth';
import { configurationsApi, type Configuration } from '@/api/endpoints/configurations';
import { authApi } from '@/api/endpoints';
import { createMockUser } from '@/test/factories';
import { APP_QUERY_DEFAULTS } from '@/api/queryDefaults';

vi.mock('@/api/endpoints/configurations', async () => {
  const actual = await vi.importActual<typeof import('@/api/endpoints/configurations')>(
    '@/api/endpoints/configurations',
  );
  return {
    ...actual,
    configurationsApi: { ...actual.configurationsApi, getUIConfigurations: vi.fn() },
  };
});

vi.mock('@/api/endpoints', () => ({
  authApi: {
    getCurrentUser: vi.fn(),
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
  },
}));

const session = vi.hoisted(() => ({ generation: 0, token: null as string | null }));

vi.mock('@/api/client', () => ({
  apiClient: {
    getToken: vi.fn(() => session.token),
    clearTokens: vi.fn(),
    startSession: vi.fn((token: string) => {
      session.generation += 1;
      session.token = token;
    }),
    endSession: vi.fn(async (revoke: (token: string) => Promise<void>) => {
      session.generation += 1;
      session.token = null;
      await revoke('captured-access-token');
    }),
    captureSession: vi.fn(() => ({ generation: session.generation, subject: null, refreshToken: null })),
    isCurrentSession: vi.fn((snapshot: { generation: number }) => snapshot.generation === session.generation),
  },
  TOKEN_KEY: 'gophercrm_token',
  REFRESH_TOKEN_KEY: 'gophercrm_refresh_token',
}));

const entry = (key: string, value: string, type: Configuration['type'] = 'string'): Configuration => ({
  id: 0,
  key,
  value,
  type,
  category: 'ui',
  description: '',
  default_value: '',
  is_system: true,
  is_read_only: true,
  valid_values: '',
  created_at: '2026-10-30T00:00:00Z',
  updated_at: '2026-10-30T00:00:00Z',
});

const accountA = [
  entry('general.company_name', 'Acme CRM'),
  entry('ui.theme.primary_color', '#ff5722'),
  entry('leads.conversion.allowed_statuses', '["contacted","qualified"]', 'array'),
  entry('leads.conversion.require_notes', 'true', 'boolean'),
  entry('leads.conversion.auto_assign_owner', 'true', 'boolean'),
];

const accountB = [entry('general.company_name', 'Bravo CRM')];

// Renders what the provider currently exposes.
const Probe: React.FC = () => {
  const config = useConfiguration();
  const conversion = config.getLeadConversionSettings();
  return (
    <div>
      <span data-testid="loading">{String(config.isLoading)}</span>
      <span data-testid="error">{config.error ?? ''}</span>
      <span data-testid="company">{config.getCompanyName()}</span>
      <span data-testid="color">{config.getPrimaryColor()}</span>
      <span data-testid="statuses">{config.getLeadConversionStatuses().join(',')}</span>
      <span data-testid="require-notes">{String(conversion.requireNotes)}</span>
      <span data-testid="auto-assign">{String(conversion.autoAssignOwner)}</span>
      <button onClick={() => void config.refreshConfigurations()}>refresh</button>
    </div>
  );
};

const authValue = (user: AuthContextType['user'], isLoading = false): AuthContextType => ({
  user,
  isLoading,
  isAuthenticated: !!user,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const newQueryClient = () => new QueryClient({ defaultOptions: APP_QUERY_DEFAULTS });

// The provider under a controllable auth state, so a test can sign in, sign
// out or switch accounts by re-rendering.
const renderWithAuth = (auth: AuthContextType, queryClient = newQueryClient()) => {
  const tree = (value: AuthContextType) => (
    <QueryClientProvider client={queryClient}>
      <AuthContext.Provider value={value}>
        <ConfigurationProvider>
          <Probe />
        </ConfigurationProvider>
      </AuthContext.Provider>
    </QueryClientProvider>
  );
  const utils = render(tree(auth));
  return { ...utils, setAuth: (value: AuthContextType) => utils.rerender(tree(value)) };
};

const text = (id: string) => screen.getByTestId(id).textContent;

describe('ConfigurationProvider', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.generation = 0;
    session.token = null;
  });

  it('makes no request and serves the defaults without a session', async () => {
    renderWithAuth(authValue(null));

    expect(text('loading')).toBe('false');
    expect(text('company')).toBe('GopherCRM');
    expect(text('color')).toBe('#1976d2');
    expect(text('statuses')).toBe('qualified');
    expect(text('require-notes')).toBe('false');
    expect(text('auto-assign')).toBe('false');
    // Give any stray effect a chance to fire.
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });

  it('makes no request while the session is still being restored', async () => {
    renderWithAuth(authValue(null, true));
    expect(text('loading')).toBe('true');
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });

  it('serves the configured values of a signed-in user', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(accountA);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    expect(text('loading')).toBe('true');
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(text('loading')).toBe('false');
    expect(text('color')).toBe('#ff5722');
    expect(text('statuses')).toBe('contacted,qualified');
    expect(text('require-notes')).toBe('true');
    expect(text('auto-assign')).toBe('true');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(1);
  });

  it('loads on sign-in and clears on sign-out', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(accountA);
    const { setAuth } = renderWithAuth(authValue(null));
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();

    setAuth(authValue(createMockUser({ id: 1 })));
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));

    setAuth(authValue(null));
    expect(text('company')).toBe('GopherCRM');
    expect(text('statuses')).toBe('qualified');
    expect(text('require-notes')).toBe('false');
    expect(text('loading')).toBe('false');
  });

  it("refetches on an account switch and never shows the previous account's values", async () => {
    let resolveB: (value: Configuration[]) => void = () => {};
    vi.mocked(configurationsApi.getUIConfigurations)
      .mockResolvedValueOnce(accountA)
      .mockImplementationOnce(() => new Promise((resolve) => { resolveB = resolve; }));
    const { setAuth } = renderWithAuth(authValue(createMockUser({ id: 1 })));
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));

    setAuth(authValue(createMockUser({ id: 2, email: 'b@example.com' })));
    // While B's values load, A's are gone: defaults and a loading flag.
    expect(text('company')).toBe('GopherCRM');
    expect(text('require-notes')).toBe('false');
    expect(text('loading')).toBe('true');

    await act(async () => resolveB(accountB));
    await waitFor(() => expect(text('company')).toBe('Bravo CRM'));
    expect(text('statuses')).toBe('qualified');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(2);
  });

  it('falls back to the defaults and reports the error when the fetch fails', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockRejectedValue(new Error('Server unavailable'));
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('error')).toBe('Server unavailable'));
    expect(text('loading')).toBe('false');
    expect(text('company')).toBe('GopherCRM');
    expect(text('statuses')).toBe('qualified');
  });

  it('falls back per entry when a value is malformed', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([
      entry('leads.conversion.allowed_statuses', 'not json', 'array'),
      entry('ui.theme.primary_color', '#00ff00'),
    ]);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('color')).toBe('#00ff00'));
    expect(text('statuses')).toBe('qualified');
  });

  it('honours an empty allowed-status list: nothing is convertible', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([
      entry('leads.conversion.allowed_statuses', '[]', 'array'),
      entry('general.company_name', 'Acme CRM'),
    ]);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(text('statuses')).toBe('');
  });

  it('refetches on an explicit refresh, even inside the cache lifetime', async () => {
    vi.mocked(configurationsApi.getUIConfigurations)
      .mockResolvedValueOnce(accountA)
      .mockResolvedValueOnce([entry('general.company_name', 'Renamed CRM')]);
    renderWithAuth(authValue(createMockUser({ id: 1 })));
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));

    await act(async () => {
      screen.getByRole('button', { name: 'refresh' }).click();
    });
    await waitFor(() => expect(text('company')).toBe('Renamed CRM'));
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(2);
  });
});

// The same transitions driven by the real AuthProvider: sign-in installs the
// session and the configuration follows; sign-out drops it.
describe('ConfigurationProvider under AuthProvider', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    session.generation = 0;
    session.token = null;
  });

  const Controls: React.FC = () => {
    const { login, logout } = useAuth();
    return (
      <>
        <button onClick={() => void login({ email: 'a@example.com', password: 'x' })}>sign in</button>
        <button onClick={() => void logout()}>sign out</button>
      </>
    );
  };

  it('loads after sign-in and clears after sign-out, with no request before', async () => {
    vi.mocked(authApi.login).mockResolvedValue({
      token: 'access',
      refresh_token: 'refresh',
      user: createMockUser({ id: 1 }),
    } as Awaited<ReturnType<typeof authApi.login>>);
    vi.mocked(authApi.logout).mockResolvedValue(undefined);
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(accountA);

    render(
      <QueryClientProvider client={newQueryClient()}>
        <AuthProvider>
          <ConfigurationProvider>
            <Controls />
            <Probe />
          </ConfigurationProvider>
        </AuthProvider>
      </QueryClientProvider>,
    );
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
    expect(text('company')).toBe('GopherCRM');

    await act(async () => {
      screen.getByRole('button', { name: 'sign in' }).click();
    });
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(1);

    await act(async () => {
      screen.getByRole('button', { name: 'sign out' }).click();
    });
    await waitFor(() => expect(text('company')).toBe('GopherCRM'));
    expect(text('require-notes')).toBe('false');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(1);
  });
});
