import { describe, it, expect, vi, beforeEach } from 'vitest';
import React from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { ConfigurationProvider } from './ConfigurationContext';
import { AuthContext, type AuthContextType } from './auth-context';
import { AuthProvider } from './AuthContext';
import { useConfiguration } from '@/hooks/useConfiguration';
import { useAuth } from '@/hooks/useAuth';
import { useUIConfigurationsQuery } from '@/hooks/useUIConfigurations';
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

vi.mock('@/api/endpoints', async () => {
  // The barrel hands out the mocked configurations module above.
  const configurations = await import('@/api/endpoints/configurations');
  return {
    configurationsApi: configurations.configurationsApi,
    authApi: {
      getCurrentUser: vi.fn(),
      login: vi.fn(),
      register: vi.fn(),
      logout: vi.fn(),
    },
  };
});

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
  const conversion = config.getLeadConversion();
  const settings = conversion.status === 'ready' ? conversion.settings : null;
  return (
    <div>
      <span data-testid="loading">{String(config.isLoading)}</span>
      <span data-testid="error">{config.error ?? ''}</span>
      <span data-testid="company">{config.getCompanyName()}</span>
      <span data-testid="color">{config.getPrimaryColor()}</span>
      <span data-testid="conversion">{conversion.status}</span>
      <span data-testid="statuses">{settings ? settings.allowedStatuses.join(',') : '-'}</span>
      <span data-testid="require-notes">{settings ? String(settings.requireNotes) : '-'}</span>
      <span data-testid="auto-assign">{settings ? String(settings.autoAssignOwner) : '-'}</span>
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
    // No rules are invented without a session.
    expect(text('conversion')).toBe('unavailable');
    expect(text('statuses')).toBe('-');
    expect(text('require-notes')).toBe('-');
    // Give any stray effect a chance to fire.
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });

  it('makes no request while the session is still being restored', async () => {
    renderWithAuth(authValue(null, true));
    expect(text('loading')).toBe('true');
    expect(text('conversion')).toBe('loading');
    await act(async () => {});
    expect(configurationsApi.getUIConfigurations).not.toHaveBeenCalled();
  });

  it('serves the configured values of a signed-in user', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(accountA);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    expect(text('loading')).toBe('true');
    expect(text('conversion')).toBe('loading');
    expect(text('require-notes')).toBe('-');
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(text('loading')).toBe('false');
    expect(text('conversion')).toBe('ready');
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
    expect(text('conversion')).toBe('unavailable');
    expect(text('require-notes')).toBe('-');
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
    expect(text('conversion')).toBe('loading');
    expect(text('require-notes')).toBe('-');
    expect(text('loading')).toBe('true');

    await act(async () => resolveB(accountB));
    await waitFor(() => expect(text('company')).toBe('Bravo CRM'));
    // Account B's list carries no conversion entries.
    expect(text('conversion')).toBe('unavailable');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(2);
  });

  it('reports a failed fetch: display defaults, conversion rules unavailable', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockRejectedValue(new Error('Server unavailable'));
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('error')).toBe('Server unavailable'));
    expect(text('loading')).toBe('false');
    expect(text('company')).toBe('GopherCRM');
    expect(text('conversion')).toBe('unavailable');
    expect(text('statuses')).toBe('-');
  });

  it('keeps display fallbacks per entry but never invents a malformed rule', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([
      entry('leads.conversion.allowed_statuses', 'not json', 'array'),
      entry('leads.conversion.require_notes', 'false', 'boolean'),
      entry('ui.theme.primary_color', '#00ff00'),
    ]);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('color')).toBe('#00ff00'));
    expect(text('conversion')).toBe('unavailable');
  });

  it.each([
    ['allowed statuses', 'leads.conversion.allowed_statuses'],
    ['notes rule', 'leads.conversion.require_notes'],
  ])('treats the rules as unavailable when the server left out the %s', async (_name, key) => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(
      accountA.filter((config) => config.key !== key),
    );
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(text('conversion')).toBe('unavailable');
    expect(text('require-notes')).toBe('-');
  });

  it('claims no owner assignment when that entry is missing', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(
      accountA.filter((config) => config.key !== 'leads.conversion.auto_assign_owner'),
    );
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('conversion')).toBe('ready'));
    expect(text('auto-assign')).toBe('false');
    expect(text('require-notes')).toBe('true');
  });

  it('treats a failed refresh over loaded values as unavailable', async () => {
    vi.mocked(configurationsApi.getUIConfigurations)
      .mockResolvedValueOnce(accountA)
      .mockRejectedValueOnce(new Error('Server unavailable'));
    renderWithAuth(authValue(createMockUser({ id: 1 })));
    await waitFor(() => expect(text('conversion')).toBe('ready'));

    await act(async () => {
      screen.getByRole('button', { name: 'refresh' }).click();
    });
    await waitFor(() => expect(text('conversion')).toBe('unavailable'));
  });

  it('honours an empty allowed-status list: nothing is convertible', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([
      entry('leads.conversion.allowed_statuses', '[]', 'array'),
      entry('leads.conversion.require_notes', 'false', 'boolean'),
      entry('general.company_name', 'Acme CRM'),
    ]);
    renderWithAuth(authValue(createMockUser({ id: 1 })));

    await waitFor(() => expect(text('company')).toBe('Acme CRM'));
    expect(text('conversion')).toBe('ready');
    expect(text('statuses')).toBe('');
  });

  it('shares one request with the other readers of the list', async () => {
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue(accountA);
    const CurrencyReader: React.FC = () => {
      const { data } = useUIConfigurationsQuery();
      return <span data-testid="shared">{data ? 'loaded' : 'pending'}</span>;
    };
    const queryClient = newQueryClient();
    render(
      <QueryClientProvider client={queryClient}>
        <AuthContext.Provider value={authValue(createMockUser({ id: 1 }))}>
          <ConfigurationProvider>
            <Probe />
            <CurrencyReader />
          </ConfigurationProvider>
        </AuthContext.Provider>
      </QueryClientProvider>,
    );
    await waitFor(() => expect(text('shared')).toBe('loaded'));
    expect(text('company')).toBe('Acme CRM');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(1);
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
    const { login, logout, refreshUser } = useAuth();
    return (
      <>
        <button onClick={() => void login({ email: 'a@example.com', password: 'x' })}>sign in</button>
        <button onClick={() => void login({ email: 'b@example.com', password: 'x' })}>sign in as b</button>
        <button onClick={() => void logout()}>sign out</button>
        <button onClick={() => void refreshUser()}>reload user</button>
      </>
    );
  };

  const renderApp = () =>
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

  const loginAnswer = (id: number) =>
    ({
      token: `access-${id}`,
      refresh_token: `refresh-${id}`,
      user: createMockUser({ id, email: `user${id}@example.com` }),
    }) as Awaited<ReturnType<typeof authApi.login>>;

  const click = async (name: string) => {
    await act(async () => {
      screen.getByRole('button', { name }).click();
    });
  };

  it("loads the next account's values when another account signs in without signing out", async () => {
    vi.mocked(authApi.login).mockImplementation(async (data) =>
      loginAnswer(data.email === 'b@example.com' ? 2 : 1),
    );
    vi.mocked(configurationsApi.getUIConfigurations).mockImplementation(async () =>
      session.token === 'access-2' ? accountB : accountA,
    );
    renderApp();

    await click('sign in');
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));

    await click('sign in as b');
    await waitFor(() => expect(text('company')).toBe('Bravo CRM'));
    expect(text('loading')).toBe('false');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(2);
  });

  it('loads the values of the account a session reload reports', async () => {
    vi.mocked(authApi.login).mockResolvedValue(loginAnswer(1));
    vi.mocked(configurationsApi.getUIConfigurations)
      .mockResolvedValueOnce(accountA)
      .mockResolvedValueOnce(accountB);
    renderApp();

    await click('sign in');
    await waitFor(() => expect(text('company')).toBe('Acme CRM'));

    // Another tab replaced the stored session with another account.
    session.token = 'access-2';
    vi.mocked(authApi.getCurrentUser).mockResolvedValue(createMockUser({ id: 2, email: 'b@example.com' }));
    await click('reload user');
    await waitFor(() => expect(text('company')).toBe('Bravo CRM'));
    expect(text('loading')).toBe('false');
  });

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
    expect(text('conversion')).toBe('unavailable');
    expect(configurationsApi.getUIConfigurations).toHaveBeenCalledTimes(1);
  });
});
