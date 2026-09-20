import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen, waitFor, within } from '@/test/test-utils';
import { Component as APIKeys } from './APIKeys';
import { apiKeysApi } from '@/api/endpoints/apikeys';
import type { APIKey } from '@/types';

const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/apikeys', () => ({
  apiKeysApi: {
    getAPIKeys: vi.fn(),
    getAPIKey: vi.fn(),
    createAPIKey: vi.fn(),
    updateAPIKey: vi.fn(),
    revokeAPIKey: vi.fn(),
    deleteAPIKey: vi.fn(),
  },
}));

const activeKey: APIKey = {
  id: 1,
  name: 'CI pipeline',
  prefix: 'gcrm_ab',
  user_id: 1,
  is_active: true,
  created_at: '2026-02-01T09:00:00Z',
  expires_at: '2027-02-01T09:00:00Z',
  last_used_at: '2026-02-10T12:00:00Z',
};

const revokedKey: APIKey = {
  id: 2,
  name: 'Old laptop',
  prefix: 'gcrm_cd',
  user_id: 1,
  is_active: false,
  created_at: '2025-11-01T09:00:00Z',
};

const listing = (keys: APIKey[]) => ({
  data: keys,
  total: keys.length,
  page: 1,
  limit: 10,
  total_pages: 1,
});

describe('APIKeys', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    (apiKeysApi.getAPIKeys as any).mockResolvedValue(listing([activeKey, revokedKey]));
  });

  it('lists the caller own keys with status and dates', async () => {
    render(<APIKeys />);

    expect(await screen.findByText('CI pipeline')).toBeInTheDocument();
    expect(screen.getByText('Old laptop')).toBeInTheDocument();
    expect(screen.getByText('gcrm_ab…')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Inactive')).toBeInTheDocument();
    // A key that has never been used renders the em-dash fallback.
    const revokedRow = screen.getByText('Old laptop').closest('tr')!;
    expect(within(revokedRow).getAllByText('—').length).toBeGreaterThan(0);
  });

  it('reports a failed list request instead of claiming the account has no keys', async () => {
    (apiKeysApi.getAPIKeys as any).mockRejectedValue({
      response: { status: 500, data: { code: 'INTERNAL_ERROR', message: 'Internal server error' } },
    });

    render(<APIKeys />);

    expect(await screen.findByText('Internal server error')).toBeInTheDocument();
    expect(screen.queryByText(/no api keys yet/i)).not.toBeInTheDocument();
  });

  it('falls back to a generic message when a failed list carries no server message', async () => {
    (apiKeysApi.getAPIKeys as any).mockRejectedValue(new Error('Network Error'));

    render(<APIKeys />);

    expect(await screen.findByText(/failed to load api keys/i)).toBeInTheDocument();
    expect(screen.queryByText(/no api keys yet/i)).not.toBeInTheDocument();
  });

  it('shows the plaintext key exactly once after creation and refreshes the list', async () => {
    const user = userEvent.setup();
    (apiKeysApi.createAPIKey as any).mockResolvedValue({
      ...activeKey,
      id: 3,
      name: 'Zapier sync',
      key: 'gcrm_plaintext_key_value',
    });

    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    await user.type(await screen.findByLabelText(/^name/i), 'Zapier sync');
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => {
      expect(apiKeysApi.createAPIKey).toHaveBeenCalledWith({
        name: 'Zapier sync',
        expires_at: undefined,
      });
    });

    const revealed = await screen.findByTestId('generated-api-key');
    expect(revealed).toHaveValue('gcrm_plaintext_key_value');
    expect(screen.getByText(/never be shown again/i)).toBeInTheDocument();

    (apiKeysApi.getAPIKeys as any).mockResolvedValue(
      listing([activeKey, revokedKey, { ...activeKey, id: 3, name: 'Zapier sync' }])
    );
    await user.click(screen.getByRole('button', { name: /done/i }));

    await waitFor(() => {
      expect(screen.queryByTestId('generated-api-key')).not.toBeInTheDocument();
    });
    expect(await screen.findByText('Zapier sync')).toBeInTheDocument();
  });

  it('sends the chosen expiry as an end-of-day RFC3339 timestamp', async () => {
    const user = userEvent.setup();
    (apiKeysApi.createAPIKey as any).mockResolvedValue({ ...activeKey, id: 4, key: 'k' });

    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    await user.type(await screen.findByLabelText(/^name/i), 'Expiring key');
    await user.type(screen.getByLabelText(/expires on/i), '2027-12-31');
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => {
      expect(apiKeysApi.createAPIKey).toHaveBeenCalledWith({
        name: 'Expiring key',
        expires_at: new Date('2027-12-31T23:59:59').toISOString(),
      });
    });
  });

  it('revokes a key after confirmation', async () => {
    const user = userEvent.setup();
    (apiKeysApi.revokeAPIKey as any).mockResolvedValue(undefined);

    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: 'Revoke CI pipeline' }));

    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/cannot be undone/i)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: /^revoke$/i }));

    await waitFor(() => {
      expect(apiKeysApi.revokeAPIKey).toHaveBeenCalledWith(1);
    });
    expect(showSuccess).toHaveBeenCalledWith('API key revoked');
  });

  it('does not offer revoke on an already inactive key', async () => {
    render(<APIKeys />);
    await screen.findByText('Old laptop');

    expect(screen.getByRole('button', { name: 'Revoke Old laptop' })).toBeDisabled();
  });
});
