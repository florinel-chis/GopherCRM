import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { render, screen, waitFor, within, requireElement, fireEvent } from '@/test/test-utils';
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

/** A local date `days` from today, as the date input takes it (`yyyy-MM-dd`). */
const localDate = (days: number, years = 0): string => {
  const date = new Date();
  date.setFullYear(date.getFullYear() + years);
  date.setDate(date.getDate() + days);
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
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
    vi.mocked(apiKeysApi.getAPIKeys).mockResolvedValue(listing([activeKey, revokedKey]));
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  /** Creates a key through the form and waits for the reveal dialog. */
  const revealNewKey = async (user: ReturnType<typeof userEvent.setup>) => {
    vi.mocked(apiKeysApi.createAPIKey).mockResolvedValue({
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
    return screen.findByTestId('generated-api-key');
  };

  it('lists the caller own keys with status and dates', async () => {
    render(<APIKeys />);

    expect(await screen.findByText('CI pipeline')).toBeInTheDocument();
    expect(screen.getByText('Old laptop')).toBeInTheDocument();
    expect(screen.getByText('gcrm_ab…')).toBeInTheDocument();
    expect(screen.getByText('Active')).toBeInTheDocument();
    expect(screen.getByText('Inactive')).toBeInTheDocument();
    // A key that has never been used renders the em-dash fallback.
    const revokedRow = requireElement(screen.getByText('Old laptop').closest('tr'));
    expect(within(revokedRow).getAllByText('—').length).toBeGreaterThan(0);
  });

  it('reports a failed list request instead of claiming the account has no keys', async () => {
    vi.mocked(apiKeysApi.getAPIKeys).mockRejectedValue({
      response: { status: 500, data: { code: 'INTERNAL_ERROR', message: 'Internal server error' } },
    });

    render(<APIKeys />);

    expect(await screen.findByText('Internal server error')).toBeInTheDocument();
    expect(screen.queryByText(/no api keys yet/i)).not.toBeInTheDocument();
  });

  it('falls back to a generic message when a failed list carries no server message', async () => {
    vi.mocked(apiKeysApi.getAPIKeys).mockRejectedValue(new Error('Network Error'));

    render(<APIKeys />);

    expect(await screen.findByText(/failed to load api keys/i)).toBeInTheDocument();
    expect(screen.queryByText(/no api keys yet/i)).not.toBeInTheDocument();
  });

  it('falls back to its own message when the server message is not text', async () => {
    vi.mocked(apiKeysApi.getAPIKeys).mockRejectedValue({
      response: { status: 500, data: { message: {} } },
    });

    render(<APIKeys />);

    expect(
      await screen.findByText('Failed to load API keys. Please try again.'),
    ).toBeInTheDocument();
  });

  it('shows the plaintext key exactly once after creation and refreshes the list', async () => {
    const user = userEvent.setup();
    vi.mocked(apiKeysApi.createAPIKey).mockResolvedValue({
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

    vi.mocked(apiKeysApi.getAPIKeys).mockResolvedValue(
      listing([activeKey, revokedKey, { ...activeKey, id: 3, name: 'Zapier sync' }])
    );
    await user.click(screen.getByRole('button', { name: /done/i }));

    await waitFor(() => {
      expect(screen.queryByTestId('generated-api-key')).not.toBeInTheDocument();
    });
    expect(await screen.findByText('Zapier sync')).toBeInTheDocument();
  });

  it('keeps the new key on screen through Escape and a click outside, until Done', async () => {
    const user = userEvent.setup();
    const revealed = await revealNewKey(user);
    expect(revealed).toHaveValue('gcrm_plaintext_key_value');

    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Your new API key' }), {
      key: 'Escape',
      code: 'Escape',
    });
    const backdrop = requireElement(document.querySelector('.MuiBackdrop-root'));
    fireEvent.mouseDown(backdrop);
    fireEvent.click(backdrop);

    // Give a closing transition the chance to run before asserting it did not.
    await new Promise((resolve) => setTimeout(resolve, 50));
    expect(screen.getByTestId('generated-api-key')).toHaveValue('gcrm_plaintext_key_value');

    await user.click(screen.getByRole('button', { name: /done/i }));
    await waitFor(() => {
      expect(screen.queryByTestId('generated-api-key')).not.toBeInTheDocument();
    });
  });

  it('reports a failed copy when the browser has no clipboard', async () => {
    const user = userEvent.setup();
    await revealNewKey(user);
    vi.stubGlobal('navigator', { ...navigator, clipboard: undefined });

    await user.click(screen.getByRole('button', { name: 'Copy API key' }));

    await waitFor(() => {
      expect(showError).toHaveBeenCalledWith(
        'Could not copy to the clipboard. Select and copy it manually.',
      );
    });
    expect(showSuccess).not.toHaveBeenCalledWith('API key copied to clipboard');
  });

  it('sends the chosen expiry as an end-of-day RFC3339 timestamp', async () => {
    const user = userEvent.setup();
    vi.mocked(apiKeysApi.createAPIKey).mockResolvedValue({ ...activeKey, id: 4, key: 'k' });

    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    await user.type(await screen.findByLabelText(/^name/i), 'Expiring key');
    const chosen = localDate(30);
    await user.type(screen.getByLabelText(/expires on/i), chosen);
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    await waitFor(() => {
      expect(apiKeysApi.createAPIKey).toHaveBeenCalledWith({
        name: 'Expiring key',
        expires_at: new Date(`${chosen}T23:59:59`).toISOString(),
      });
    });
  });

  it('explains the one-year limit and offers at most one year from today', async () => {
    const user = userEvent.setup();
    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    const expiry = await screen.findByLabelText(/expires on/i);
    expect(
      screen.getByText('Optional. Leave empty for a key that expires in one year; at most one year from today.')
    ).toBeInTheDocument();
    expect(expiry).toHaveAttribute('max', localDate(-1, 1));
  });

  it('refuses an expiry later than one year from today without sending a request', async () => {
    const user = userEvent.setup();
    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    await user.type(await screen.findByLabelText(/^name/i), 'Long key');
    await user.type(screen.getByLabelText(/expires on/i), localDate(0, 1));
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    expect(await screen.findByText('Expiry can be at most one year from today')).toBeInTheDocument();
    expect(apiKeysApi.createAPIKey).not.toHaveBeenCalled();
  });

  it('shows the sign-in-again refusal in the open create dialog', async () => {
    const user = userEvent.setup();
    const message =
      'Sign in again to create an API key: creating a key needs a sign-in within the last 15 minutes';
    vi.mocked(apiKeysApi.createAPIKey).mockRejectedValue({
      response: { status: 403, data: { code: 'REAUTHENTICATION_REQUIRED', message } },
    });

    render(<APIKeys />);
    await screen.findByText('CI pipeline');

    await user.click(screen.getByRole('button', { name: /create api key/i }));
    await user.type(await screen.findByLabelText(/^name/i), 'Stale session key');
    await user.click(screen.getByRole('button', { name: /^create$/i }));

    const dialog = await screen.findByRole('dialog', { name: /create api key/i });
    expect(await within(dialog).findByText(message)).toBeInTheDocument();
    expect(within(dialog).getByLabelText(/^name/i)).toBeInTheDocument();
    expect(screen.queryByTestId('generated-api-key')).not.toBeInTheDocument();
  });

  it('revokes a key after confirmation', async () => {
    const user = userEvent.setup();
    vi.mocked(apiKeysApi.revokeAPIKey).mockResolvedValue(undefined);

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
