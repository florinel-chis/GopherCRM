import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AxiosError } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as AEOSettings } from './AEOSettings';
import { aeoApi } from '@/api/endpoints/aeo';
import { configurationsApi } from '@/api/endpoints/configurations';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();
const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/aeo', () => ({
  aeoApi: {
    getProfile: vi.fn(),
    saveProfile: vi.fn(),
    getProviders: vi.fn(),
    createRun: vi.fn(),
  },
}));

vi.mock('@/api/endpoints/configurations', () => ({
  configurationsApi: {
    getByCategory: vi.fn(),
    set: vi.fn(),
    getUIConfigurations: vi.fn(),
    getValue: vi.fn(),
  },
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const profile = {
  id: 1,
  brand_name: 'Acme',
  description: 'CRM for gophers',
  brand_aliases: ['Acme Inc'],
  owned_domains: ['acme.com'],
  competitors: [{ name: 'Globex', aliases: ['Globex Corp'], domain: 'globex.com' }],
};

const providers = [
  { name: 'anthropic', model: 'claude-opus-5', configured: true },
  { name: 'openai', model: 'gpt-4o-mini', configured: false },
];

const sensitiveConfig = (key: string, isSet: boolean) => ({
  id: 1,
  key,
  value: '',
  type: 'string',
  category: 'integration',
  description: '',
  default_value: '',
  is_system: true,
  is_read_only: false,
  is_sensitive: true,
  is_set: isSet,
  valid_values: '',
  created_at: '2026-08-12T00:00:00Z',
  updated_at: '2026-08-12T00:00:00Z',
});

const integrationConfigs = [
  sensitiveConfig('integration.aeo.anthropic_api_key', true),
  sensitiveConfig('integration.aeo.openai_api_key', false),
  sensitiveConfig('integration.aeo.gemini_api_key', false),
  sensitiveConfig('integration.aeo.moonshot_api_key', false),
  sensitiveConfig('integration.aeo.perplexity_api_key', false),
  {
    ...sensitiveConfig('integration.aeo.generation_engine', true),
    is_sensitive: false,
    value: 'anthropic',
    default_value: 'anthropic',
    valid_values: '["anthropic", "openai", "gemini", "kimi", "perplexity"]',
  },
];

const axiosErrorWithStatus =(status: number, message?: string): AxiosError => {
  const error = new AxiosError('request failed');
  error.response = {
    status,
    statusText: '',
    data: message ? { message } : {},
    headers: {},
    config: { headers: {} as never },
  } as never;
  return error;
};

describe('AEOSettings', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    (aeoApi.getProfile as any).mockResolvedValue(profile);
    (aeoApi.getProviders as any).mockResolvedValue(providers);
    (configurationsApi.getByCategory as any).mockResolvedValue(integrationConfigs);
    (configurationsApi.set as any).mockResolvedValue(
      sensitiveConfig('integration.aeo.anthropic_api_key', true)
    );
  });

  afterEach(() => {
    vi.restoreAllMocks();
  });

  it('shows the loading state while the profile is fetched', () => {
    (aeoApi.getProfile as any).mockImplementation(() => new Promise(() => {}));

    render(<AEOSettings />);

    expect(screen.getByTestId('loading')).toBeInTheDocument();
  });

  it('renders an error alert when the profile request fails', async () => {
    (aeoApi.getProfile as any).mockRejectedValue(new Error('boom'));

    render(<AEOSettings />);

    expect(
      await screen.findByText(/Failed to load the AEO brand profile/)
    ).toBeInTheDocument();
  });

  it('populates the form from the saved profile', async () => {
    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Brand name/)).toHaveValue('Acme');
    });

    expect(screen.getByLabelText(/^Business description/)).toHaveValue('CRM for gophers');
    expect(screen.getByText('Acme Inc')).toBeInTheDocument();
    expect(screen.getByText('acme.com')).toBeInTheDocument();
    expect(screen.getByLabelText(/^Competitor 1 name/)).toHaveValue('Globex');
    expect(screen.getByLabelText(/^Competitor 1 domain/)).toHaveValue('globex.com');
    expect(screen.getByText('Globex Corp')).toBeInTheDocument();
  });

  it('renders a status chip per answer engine', async () => {
    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByTestId('provider-chip-anthropic')).toBeInTheDocument();
    });

    expect(screen.getByTestId('provider-chip-anthropic')).toHaveTextContent(
      'anthropic: claude-opus-5'
    );
    expect(screen.getByTestId('provider-chip-openai')).toHaveTextContent('openai: gpt-4o-mini');
    expect(screen.getByText(/1 of 2 engines configured/)).toBeInTheDocument();
  });

  it('tells the operator when the profile has not been configured yet', async () => {
    (aeoApi.getProfile as any).mockResolvedValue(null);

    render(<AEOSettings />);

    expect(
      await screen.findByText(/No brand profile configured yet/)
    ).toBeInTheDocument();
    expect(screen.getByLabelText(/^Brand name/)).toHaveValue('');
  });

  it('reports an empty engine list as the no-data state', async () => {
    (aeoApi.getProviders as any).mockResolvedValue([]);

    render(<AEOSettings />);

    expect(
      await screen.findByText(/No AEO data yet — no answer engines/)
    ).toBeInTheDocument();
  });

  it('saves an added alias and a new competitor', async () => {
    (aeoApi.saveProfile as any).mockResolvedValue(profile);

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Brand name/)).toHaveValue('Acme');
    });

    fireEvent.change(screen.getByLabelText(/^Brand aliases/), {
      target: { value: 'Acme Software' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Add Brand aliases' }));

    await waitFor(() => {
      expect(screen.getByText('Acme Software')).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /save profile/i }));

    await waitFor(() => {
      expect(aeoApi.saveProfile).toHaveBeenCalledWith({
        brand_name: 'Acme',
        description: 'CRM for gophers',
        brand_aliases: ['Acme Inc', 'Acme Software'],
        owned_domains: ['acme.com'],
        competitors: [{ name: 'Globex', domain: 'globex.com', aliases: ['Globex Corp'] }],
      });
    });
  });

  it('refuses to save without a brand name', async () => {
    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Brand name/)).toHaveValue('Acme');
    });

    fireEvent.change(screen.getByLabelText(/^Brand name/), { target: { value: '' } });
    fireEvent.click(screen.getByRole('button', { name: /save profile/i }));

    expect(await screen.findByText('Brand name is required')).toBeInTheDocument();
    expect(aeoApi.saveProfile).not.toHaveBeenCalled();
  });

  it('drops a removed competitor from the payload', async () => {
    (aeoApi.saveProfile as any).mockResolvedValue(profile);

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Competitor 1 name/)).toHaveValue('Globex');
    });

    fireEvent.click(screen.getByRole('button', { name: 'Remove competitor 1' }));
    fireEvent.click(screen.getByRole('button', { name: /save profile/i }));

    await waitFor(() => {
      expect(aeoApi.saveProfile).toHaveBeenCalledWith(
        expect.objectContaining({ competitors: [] })
      );
    });
  });

  it('starts a run from the Run now button', async () => {
    (aeoApi.createRun as any).mockResolvedValue({ id: 3, status: 'running' });

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /run now/i }));

    await waitFor(() => {
      expect(aeoApi.createRun).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(showSuccess).toHaveBeenCalledWith('AEO run started');
    });
  });

  // 409 is the overlap guard (or a missing profile); the server wording is the
  // most useful thing to show.
  it('surfaces the 409 conflict when a run is already in progress', async () => {
    (aeoApi.createRun as any).mockRejectedValue(
      axiosErrorWithStatus(409, 'an AEO run is already in progress')
    );

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /run now/i }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('an AEO run is already in progress');
    });
  });

  it('falls back to a generic conflict message when the server sends none', async () => {
    (aeoApi.createRun as any).mockRejectedValue(axiosErrorWithStatus(409));

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /run now/i }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('An AEO run is already in progress');
    });
  });

  it('reports a 503 when no provider key is configured', async () => {
    (aeoApi.createRun as any).mockRejectedValue(axiosErrorWithStatus(503));

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /run now/i }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith('No AEO providers are configured');
    });
  });

  it.each(['admin', 'sales'] as const)('offers Save and Run now to the %s role', async (role) => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByRole('button', { name: /run now/i })).toBeInTheDocument();
    });
    expect(screen.getByRole('button', { name: /save profile/i })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Add competitor' })).toBeInTheDocument();
  });

  it('renders read-only for the support role', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'support' })));

    render(<AEOSettings />);

    await waitFor(() => {
      expect(screen.getByLabelText(/^Brand name/)).toHaveValue('Acme');
    });

    expect(screen.queryByRole('button', { name: /run now/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /save profile/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Add competitor' })).not.toBeInTheDocument();
    expect(screen.getByLabelText(/^Brand name/)).toBeDisabled();
    // A read-only viewer must not be able to strip a chip either.
    const aliasChip = screen.getByText('Acme Inc').closest('.MuiChip-root') as HTMLElement;
    expect(within(aliasChip).queryByTestId('CancelIcon')).not.toBeInTheDocument();
  });

  describe('API keys card', () => {
    it.each(['sales', 'support'] as const)('stays hidden for the %s role', async (role) => {
      mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role })));

      render(<AEOSettings />);

      await waitFor(() => {
        expect(screen.getByLabelText(/^Brand name/)).toHaveValue('Acme');
      });

      expect(screen.queryByRole('heading', { name: 'API keys' })).not.toBeInTheDocument();
      expect(screen.queryByLabelText(/^Anthropic API key/)).not.toBeInTheDocument();
      expect(configurationsApi.getByCategory).not.toHaveBeenCalled();
    });

    it('renders one row per provider with the stored state', async () => {
      render(<AEOSettings />);

      expect(await screen.findByRole('heading', { name: 'API keys' })).toBeInTheDocument();

      await waitFor(() => {
        expect(screen.getByTestId('api-key-chip-anthropic')).toHaveTextContent('Configured');
      });

      for (const name of ['openai', 'gemini', 'kimi', 'perplexity']) {
        expect(screen.getByTestId(`api-key-chip-${name}`)).toHaveTextContent('Not configured');
      }

      const input = screen.getByLabelText(/^Anthropic API key/);
      expect(input).toHaveAttribute('type', 'password');
      expect(input).toHaveAttribute('autocomplete', 'new-password');
      expect(input).toHaveValue('');
      // Nothing is configured yet, so there is nothing to clear.
      expect(screen.getByRole('button', { name: 'Clear Gemini API key' })).toBeDisabled();
    });

    it('shows the stored generation engine and saves a new selection', async () => {
      render(<AEOSettings />);

      const select = await screen.findByTestId('generation-engine-select');
      expect(select).toHaveValue('anthropic');

      fireEvent.change(select, { target: { value: 'gemini' } });

      await waitFor(() => {
        expect(configurationsApi.set).toHaveBeenCalledWith('integration.aeo.generation_engine', {
          value: 'gemini',
        });
      });
    });

    it('sends the typed key once and clears the input afterwards', async () => {
      render(<AEOSettings />);

      const input = await screen.findByLabelText(/^Gemini API key/);
      fireEvent.change(input, { target: { value: 'top-secret-value' } });
      fireEvent.click(screen.getByRole('button', { name: 'Save Gemini API key' }));

      await waitFor(() => {
        expect(configurationsApi.set).toHaveBeenCalledTimes(1);
      });
      expect(configurationsApi.set).toHaveBeenCalledWith('integration.aeo.gemini_api_key', {
        value: 'top-secret-value',
      });

      await waitFor(() => {
        expect(screen.getByLabelText(/^Gemini API key/)).toHaveValue('');
      });
      expect(screen.queryByDisplayValue('top-secret-value')).not.toBeInTheDocument();
      expect(showSuccess).toHaveBeenCalledWith('API key saved');
    });

    it('refreshes the provider roster after a save', async () => {
      render(<AEOSettings />);

      const input = await screen.findByLabelText(/^Gemini API key/);
      fireEvent.change(input, { target: { value: 'another-secret' } });

      const callsBefore = (aeoApi.getProviders as any).mock.calls.length;
      fireEvent.click(screen.getByRole('button', { name: 'Save Gemini API key' }));

      await waitFor(() => {
        expect((aeoApi.getProviders as any).mock.calls.length).toBeGreaterThan(callsBefore);
      });
      await waitFor(() => {
        expect(configurationsApi.getByCategory).toHaveBeenCalledTimes(2);
      });
    });

    it('clears a configured key with an empty value', async () => {
      render(<AEOSettings />);

      const clear = await screen.findByRole('button', { name: 'Clear Anthropic API key' });
      fireEvent.click(clear);

      await waitFor(() => {
        expect(configurationsApi.set).toHaveBeenCalledWith(
          'integration.aeo.anthropic_api_key',
          { value: '' }
        );
      });
      await waitFor(() => {
        expect(showSuccess).toHaveBeenCalledWith('API key cleared');
      });
    });

    it('reports a failed save', async () => {
      (configurationsApi.set as any).mockRejectedValue(new Error('nope'));

      render(<AEOSettings />);

      const input = await screen.findByLabelText(/^Kimi API key/);
      fireEvent.change(input, { target: { value: 'secret' } });
      fireEvent.click(screen.getByRole('button', { name: 'Save Kimi API key' }));

      await waitFor(() => {
        expect(showError).toHaveBeenCalledWith('Failed to save the API key');
      });
    });

    it('surfaces a failed configuration load', async () => {
      (configurationsApi.getByCategory as any).mockRejectedValue(new Error('boom'));

      render(<AEOSettings />);

      expect(await screen.findByText('Failed to load the stored API keys')).toBeInTheDocument();
    });
  });
});
