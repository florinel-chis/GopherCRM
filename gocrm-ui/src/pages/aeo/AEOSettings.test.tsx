import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { AxiosError } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as AEOSettings } from './AEOSettings';
import { aeoApi } from '@/api/endpoints/aeo';
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

const axiosErrorWithStatus = (status: number, message?: string): AxiosError => {
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
});
