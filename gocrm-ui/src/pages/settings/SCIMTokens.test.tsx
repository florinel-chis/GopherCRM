import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { render, screen, waitFor, within } from '@testing-library/react';
import { Component as SCIMTokens } from './SCIMTokens';
import { scimTokensApi, type SCIMToken } from '@/api/endpoints/scimTokens';
import { formatDate, DATE_FALLBACK } from '@/utils/date';

const showSuccess = vi.fn();
const showError = vi.fn();

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError, showWarning: vi.fn(), showInfo: vi.fn() }),
}));

vi.mock('@/api/endpoints/scimTokens', () => ({
  scimTokensApi: {
    list: vi.fn(),
    create: vi.fn(),
    revoke: vi.fn(),
  },
}));

const activeToken: SCIMToken = {
  id: 7,
  name: 'Entra ID',
  created_at: '2026-11-18T09:00:00Z',
  last_used_at: null,
  revoked_at: null,
};

const revokedToken: SCIMToken = {
  id: 4,
  name: 'Okta (old)',
  created_at: '2026-10-01T08:00:00Z',
  last_used_at: '2026-10-15T12:30:00Z',
  revoked_at: '2026-11-01T16:45:00Z',
};

const plaintext = `scim_${'0123456789abcdef'.repeat(4)}`;

// A real QueryClient the test can inspect: the token must not reach the query
// or the mutation cache.
const renderPage = () => {
  const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  render(
    <QueryClientProvider client={queryClient}>
      <SCIMTokens />
    </QueryClientProvider>,
  );
  return queryClient;
};

const rowOf = (name: string) => {
  const row = screen.getByText(name).closest('tr');
  if (!row) {
    throw new Error(`no row for ${name}`);
  }
  return within(row);
};

describe('SCIMTokens', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    vi.mocked(scimTokensApi.list).mockResolvedValue([activeToken, revokedToken]);
  });

  it('lists the tokens with their dates through formatDate and keeps a revoked one listed', async () => {
    renderPage();
    await screen.findByText('Entra ID');

    expect(screen.getByRole('heading', { name: 'SCIM tokens' })).toBeInTheDocument();

    const active = rowOf('Entra ID');
    expect(active.getByText(formatDate(activeToken.created_at, 'PPp'))).toBeInTheDocument();
    // Never used and not revoked: both cells show the fallback.
    expect(active.getAllByText(DATE_FALLBACK)).toHaveLength(2);
    expect(active.getByRole('button', { name: 'Revoke Entra ID' })).toBeEnabled();

    const revoked = rowOf('Okta (old)');
    expect(revoked.getByText(formatDate(revokedToken.created_at, 'PPp'))).toBeInTheDocument();
    expect(revoked.getByText(formatDate(revokedToken.last_used_at, 'PPp'))).toBeInTheDocument();
    expect(revoked.getByText(formatDate(revokedToken.revoked_at, 'PPp'))).toBeInTheDocument();
    expect(revoked.queryByText(DATE_FALLBACK)).not.toBeInTheDocument();
    expect(revoked.getByRole('button', { name: 'Revoke Okta (old)' })).toBeDisabled();
  });

  it('shows an empty state when there are no tokens', async () => {
    vi.mocked(scimTokensApi.list).mockResolvedValue([]);
    renderPage();
    expect(await screen.findByText('There are no SCIM tokens yet.')).toBeInTheDocument();
  });

  it('shows the server message when the list fails', async () => {
    vi.mocked(scimTokensApi.list).mockRejectedValue({
      response: { data: { message: 'Insufficient permissions' } },
    });
    renderPage();
    expect(await screen.findByText('Insufficient permissions')).toBeInTheDocument();
    expect(screen.queryByText('There are no SCIM tokens yet.')).not.toBeInTheDocument();
  });

  it('creates a token, shows it once with the warning, and drops it after Done', async () => {
    const user = userEvent.setup();
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
    vi.mocked(scimTokensApi.create).mockResolvedValue({
      token: plaintext,
      scim_token: { ...activeToken, id: 9, name: 'Google' },
    });
    const queryClient = renderPage();
    await screen.findByText('Entra ID');
    expect(scimTokensApi.list).toHaveBeenCalledTimes(1);

    await user.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create SCIM token' });
    await user.type(within(dialog).getByLabelText(/Name/), '  Google  ');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(scimTokensApi.create).toHaveBeenCalledWith('Google'));

    const reveal = await screen.findByRole('dialog', { name: 'Your new SCIM token' });
    expect(screen.getAllByDisplayValue(plaintext)).toHaveLength(1);
    expect(screen.getByTestId('scim-token-value')).toHaveValue(plaintext);
    expect(
      within(reveal).getByText(/shown only once and cannot be retrieved again/),
    ).toBeInTheDocument();

    await user.click(within(reveal).getByRole('button', { name: 'Copy SCIM token' }));
    expect(writeText).toHaveBeenCalledWith(plaintext);
    // The snackbar confirms the copy without carrying the token.
    expect(JSON.stringify(showSuccess.mock.calls)).not.toContain(plaintext);

    await user.click(within(reveal).getByRole('button', { name: 'Done' }));

    expect(screen.queryByDisplayValue(plaintext)).toBeNull();
    expect(screen.queryByText(plaintext)).toBeNull();
    expect(document.body.innerHTML).not.toContain(plaintext);
    await waitFor(() => expect(scimTokensApi.list).toHaveBeenCalledTimes(2));

    // Nowhere else: not in the query or the mutation cache, not in storage.
    const queryData = queryClient.getQueryCache().getAll().map((q) => q.state.data);
    const mutationData = queryClient.getMutationCache().getAll().map((m) => m.state);
    expect(JSON.stringify(queryData)).not.toContain(plaintext);
    expect(JSON.stringify(mutationData)).not.toContain(plaintext);
    expect(JSON.stringify({ ...localStorage })).not.toContain(plaintext);
    expect(JSON.stringify({ ...sessionStorage })).not.toContain(plaintext);
    expect(JSON.stringify([showSuccess.mock.calls, showError.mock.calls])).not.toContain(plaintext);
  });

  it('refuses a blank name in the form and shows a server 400 in the dialog', async () => {
    const user = userEvent.setup();
    vi.mocked(scimTokensApi.create).mockRejectedValue({
      response: {
        status: 400,
        data: {
          message: 'Validation failed',
          details: { name: 'must be 1 to 100 characters after trimming' },
        },
      },
    });
    renderPage();
    await screen.findByText('Entra ID');

    await user.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create SCIM token' });
    await user.type(within(dialog).getByLabelText(/Name/), '   ');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(await within(dialog).findByText('Name is required')).toBeInTheDocument();
    expect(scimTokensApi.create).not.toHaveBeenCalled();

    await user.type(within(dialog).getByLabelText(/Name/), 'Okta');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(
      await within(dialog).findByText(
        'Validation failed: name must be 1 to 100 characters after trimming',
      ),
    ).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Your new SCIM token' })).not.toBeInTheDocument();
  });

  it('shows only the server message when a create is rate limited', async () => {
    const user = userEvent.setup();
    vi.mocked(scimTokensApi.create).mockRejectedValue({
      response: {
        status: 429,
        data: {
          code: 'TOO_MANY_REQUESTS',
          message: 'Too many requests. Please try again later.',
          details: { retry_after: '60s' },
        },
      },
    });
    renderPage();
    await screen.findByText('Entra ID');

    await user.click(screen.getByRole('button', { name: 'Create token' }));
    const dialog = await screen.findByRole('dialog', { name: 'Create SCIM token' });
    await user.type(within(dialog).getByLabelText(/Name/), 'Okta');
    await user.click(within(dialog).getByRole('button', { name: 'Create' }));
    expect(
      await within(dialog).findByText('Too many requests. Please try again later.'),
    ).toBeInTheDocument();
    expect(screen.queryByText(/retry_after/)).toBeNull();
  });

  describe('revoke', () => {
    it('revokes the row only after Confirm', async () => {
      const user = userEvent.setup();
      vi.mocked(scimTokensApi.revoke).mockResolvedValue({
        ...activeToken,
        revoked_at: '2026-11-20T10:00:00Z',
      });
      renderPage();
      await screen.findByText('Entra ID');

      await user.click(screen.getByRole('button', { name: 'Revoke Entra ID' }));
      const confirm = await screen.findByRole('dialog', { name: 'Revoke SCIM token' });
      expect(within(confirm).getByText(/will be refused at once/)).toBeInTheDocument();
      expect(scimTokensApi.revoke).not.toHaveBeenCalled();

      await user.click(within(confirm).getByRole('button', { name: 'Revoke' }));
      await waitFor(() => expect(scimTokensApi.revoke).toHaveBeenCalledTimes(1));
      expect(scimTokensApi.revoke).toHaveBeenCalledWith(activeToken.id);
      await waitFor(() => expect(scimTokensApi.list).toHaveBeenCalledTimes(2));
    });

    it('calls nothing when the confirmation is cancelled', async () => {
      const user = userEvent.setup();
      renderPage();
      await screen.findByText('Entra ID');

      await user.click(screen.getByRole('button', { name: 'Revoke Entra ID' }));
      const confirm = await screen.findByRole('dialog', { name: 'Revoke SCIM token' });
      await user.click(within(confirm).getByRole('button', { name: 'Cancel' }));

      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Revoke SCIM token' })).not.toBeInTheDocument(),
      );
      expect(scimTokensApi.revoke).not.toHaveBeenCalled();
    });
  });
});
