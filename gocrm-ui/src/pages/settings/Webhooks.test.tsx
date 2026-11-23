import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { Component as Webhooks } from './Webhooks';
import { webhooksApi, type WebhookEndpoint, type WebhookPingResult } from '@/api/endpoints';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { formatDate } from '@/utils/date';

vi.mock('@/api/endpoints', async () => {
  const actual = await vi.importActual<typeof import('@/api/endpoints/webhooks')>(
    '@/api/endpoints/webhooks',
  );
  return {
    WEBHOOK_EVENT_TYPES: actual.WEBHOOK_EVENT_TYPES,
    WEBHOOK_DELIVERY_STATUSES: actual.WEBHOOK_DELIVERY_STATUSES,
    webhooksApi: {
      listEndpoints: vi.fn(),
      createEndpoint: vi.fn(),
      updateEndpoint: vi.fn(),
      deleteEndpoint: vi.fn(),
      testEndpoint: vi.fn(),
      rotateSecret: vi.fn(),
      listDeliveries: vi.fn(),
    },
  };
});

const SECRET = 'whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw';

const active: WebhookEndpoint = {
  id: 4,
  created_at: '2026-11-20T09:30:00Z',
  updated_at: '2026-11-20T09:30:00Z',
  url: 'https://a.example.com/hook',
  event_types: ['lead.created', 'ticket.updated'],
  is_active: true,
};
const inactive: WebhookEndpoint = {
  ...active,
  id: 5,
  created_at: '2026-11-21T09:30:00Z',
  url: 'https://b.example.com/hook',
  event_types: ['customer.created'],
  is_active: false,
};

const rowOf = async (url: string) => {
  const row = (await screen.findByText(url)).closest('tr');
  if (!row) throw new Error(`no row for ${url}`);
  return within(row);
};

const ROTATED_SECRET = 'whsec_Rot4tedS3cretValueForTheTest0001';
const PREVIOUS_EXPIRES_AT = '2026-11-22T09:30:00Z';

const pingResult = (overrides: Partial<WebhookPingResult>): WebhookPingResult => ({
  event_id: 'evt_4f1c2a9be07d4c4fa1c3d2e5f6a7b8c9',
  outcome: 'succeeded',
  http_status: 200,
  error_code: null,
  ...overrides,
});

const storageDump = () => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]);

describe('Webhooks', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({ rows: [inactive, active], total: 2 });
    vi.mocked(webhooksApi.listDeliveries).mockResolvedValue({ rows: [], total: 0 });
  });

  it('lists the URL, the events, the active flag and the created date of each endpoint', async () => {
    render(<Webhooks />);

    const table = await screen.findByRole('table', { name: 'Webhook endpoints' });
    expect(within(table).getAllByRole('columnheader').map((cell) => cell.textContent)).toEqual([
      'URL',
      'Events',
      'Status',
      'Created',
      'Actions',
    ]);

    const first = await rowOf(active.url);
    expect(first.getByText('lead.created')).toBeInTheDocument();
    expect(first.getByText('ticket.updated')).toBeInTheDocument();
    expect(first.getByText('Active')).toBeInTheDocument();
    expect(first.getByText(formatDate(active.created_at, 'PP'))).toBeInTheDocument();
    expect(first.getByRole('button', { name: `Deactivate ${active.url}` })).toBeInTheDocument();

    const second = await rowOf(inactive.url);
    expect(second.getByText('customer.created')).toBeInTheDocument();
    expect(second.getByText('Inactive')).toBeInTheDocument();
    expect(second.getByRole('button', { name: `Activate ${inactive.url}` })).toBeInTheDocument();

    // Newest first, so a new endpoint is on the first page.
    expect(webhooksApi.listEndpoints).toHaveBeenCalledWith({
      offset: 0,
      limit: 20,
      sort_by: 'id',
      sort_order: 'desc',
    });
  });

  it('pages the endpoint list with the total of meta', async () => {
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({ rows: [active], total: 41 });
    render(<Webhooks />);
    await rowOf(active.url);

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));

    await waitFor(() =>
      expect(webhooksApi.listEndpoints).toHaveBeenLastCalledWith({
        offset: 20,
        limit: 20,
        sort_by: 'id',
        sort_order: 'desc',
      }),
    );
  });

  it('toggles an endpoint with a PUT that carries only is_active and reloads the list', async () => {
    vi.mocked(webhooksApi.updateEndpoint).mockResolvedValue({ ...active, is_active: false });
    render(withClient(createProductionQueryClient(), <Webhooks />));

    const row = await rowOf(active.url);
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({
      rows: [inactive, { ...active, is_active: false }],
      total: 2,
    });
    fireEvent.click(row.getByRole('button', { name: `Deactivate ${active.url}` }));

    await waitFor(() =>
      expect(webhooksApi.updateEndpoint).toHaveBeenCalledWith(4, { is_active: false }),
    );
    expect(await row.findByRole('button', { name: `Activate ${active.url}` })).toBeInTheDocument();

    // And back: the inactive endpoint is sent true.
    vi.mocked(webhooksApi.updateEndpoint).mockResolvedValue({ ...inactive, is_active: true });
    const other = await rowOf(inactive.url);
    fireEvent.click(other.getByRole('button', { name: `Activate ${inactive.url}` }));
    await waitFor(() =>
      expect(webhooksApi.updateEndpoint).toHaveBeenLastCalledWith(5, { is_active: true }),
    );
  });

  it('deletes only after a confirmation that names the delivery log', async () => {
    vi.mocked(webhooksApi.deleteEndpoint).mockResolvedValue(undefined);
    render(withClient(createProductionQueryClient(), <Webhooks />));

    const row = await rowOf(active.url);
    fireEvent.click(row.getByRole('button', { name: `Delete ${active.url}` }));

    const dialog = await screen.findByRole('dialog', { name: 'Delete webhook endpoint' });
    expect(dialog).toHaveTextContent(active.url);
    expect(dialog).toHaveTextContent('Its delivery log is removed with it');
    expect(webhooksApi.deleteEndpoint).not.toHaveBeenCalled();

    // Cancel leaves the endpoint alone.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: 'Delete webhook endpoint' })).not.toBeInTheDocument(),
    );
    expect(webhooksApi.deleteEndpoint).not.toHaveBeenCalled();

    fireEvent.click(row.getByRole('button', { name: `Delete ${active.url}` }));
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({ rows: [inactive], total: 1 });
    fireEvent.click(
      within(await screen.findByRole('dialog', { name: 'Delete webhook endpoint' })).getByRole(
        'button',
        { name: 'Delete' },
      ),
    );

    await waitFor(() => expect(webhooksApi.deleteEndpoint).toHaveBeenCalledWith(4));
    await waitFor(() => expect(screen.queryByText(active.url)).not.toBeInTheDocument());
    expect(screen.getByText(inactive.url)).toBeInTheDocument();
  });

  it('moves to the last existing page when the only row of a later page is deleted', async () => {
    const firstPage: WebhookEndpoint[] = Array.from({ length: 20 }, (_unused, index) => ({
      ...active,
      id: 100 - index,
      url: `https://page-one-${index}.example.com/hook`,
    }));
    const lonely: WebhookEndpoint = { ...active, id: 1, url: 'https://lonely.example.com/hook' };
    const sorted = { sort_by: 'id', sort_order: 'desc' } as const;

    // The server's answers by offset, before and after the delete resolves.
    let deleted = false;
    const offsetsAfterDelete: number[] = [];
    vi.mocked(webhooksApi.deleteEndpoint).mockImplementation(async () => {
      deleted = true;
    });
    vi.mocked(webhooksApi.listEndpoints).mockImplementation(async (params) => {
      const offset = params?.offset ?? 0;
      if (deleted) offsetsAfterDelete.push(offset);
      const total = deleted ? 20 : 21;
      if (offset === 0) return { rows: firstPage, total };
      return { rows: deleted ? [] : [lonely], total };
    });
    render(withClient(createProductionQueryClient(), <Webhooks />));
    await rowOf(firstPage[0].url);

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    const row = await rowOf(lonely.url);
    expect(screen.getByText(/^21.21 of 21$/)).toBeInTheDocument();

    fireEvent.click(row.getByRole('button', { name: `Delete ${lonely.url}` }));
    fireEvent.click(
      within(await screen.findByRole('dialog', { name: 'Delete webhook endpoint' })).getByRole(
        'button',
        { name: 'Delete' },
      ),
    );
    await waitFor(() => expect(webhooksApi.deleteEndpoint).toHaveBeenCalledWith(1));

    // The emptied second page is fetched, then the first page, which is the
    // last one left.
    await waitFor(() => expect(offsetsAfterDelete).toContain(20));
    await waitFor(() =>
      expect(webhooksApi.listEndpoints).toHaveBeenLastCalledWith({
        offset: 0,
        limit: 20,
        ...sorted,
      }),
    );
    await waitFor(() => expect(screen.getByText(/^1.20 of 20$/)).toBeInTheDocument());
    expect(screen.getByText(firstPage[0].url)).toBeInTheDocument();
    expect(screen.queryByText(lonely.url)).not.toBeInTheDocument();
    expect(screen.queryByText('No webhook endpoints yet.')).not.toBeInTheDocument();
  });

  it('shows the secret of a new endpoint once and keeps it nowhere after the dialog closes', async () => {
    const created: WebhookEndpoint = {
      ...active,
      id: 6,
      url: 'https://new.example.com/hook',
      event_types: ['lead.created'],
    };
    vi.mocked(webhooksApi.createEndpoint).mockResolvedValue({ ...created, secret: SECRET });
    const queryClient = createProductionQueryClient();
    render(withClient(queryClient, <Webhooks />));
    await rowOf(active.url);

    fireEvent.click(screen.getByRole('button', { name: 'Add endpoint' }));
    fireEvent.change(await screen.findByRole('textbox', { name: 'URL' }), {
      target: { value: created.url },
    });
    fireEvent.click(screen.getByRole('checkbox', { name: 'lead.created' }));
    // The list after the create: fetched again, never patched with the answer.
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({
      rows: [created, inactive, active],
      total: 3,
    });
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() =>
      expect(webhooksApi.createEndpoint).toHaveBeenCalledWith({
        url: created.url,
        event_types: ['lead.created'],
        is_active: true,
      }),
    );

    // Shown once, with the copy control and the notice.
    expect(await screen.findByTestId('webhook-secret')).toHaveValue(SECRET);
    expect(screen.getByRole('button', { name: 'Copy signing secret' })).toBeInTheDocument();
    expect(screen.getByText(/cannot be shown again/i)).toBeInTheDocument();
    expect(screen.queryByRole('textbox', { name: 'URL' })).not.toBeInTheDocument();
    // Even while it is on screen it is in no storage and no cache.
    expect(storageDump()).not.toContain('whsec_');

    fireEvent.click(screen.getByRole('button', { name: 'Done' }));

    await waitFor(() => expect(screen.queryByTestId('webhook-secret')).not.toBeInTheDocument());
    expect(await screen.findByText(created.url)).toBeInTheDocument();
    expect(webhooksApi.listEndpoints).toHaveBeenCalledTimes(2);

    // Gone from the document (input values included), both storages and both
    // caches of the query client.
    expect(document.body.innerHTML).not.toContain('whsec_');
    expect(
      Array.from(document.querySelectorAll('input, textarea')).map(
        (field) => (field as HTMLInputElement).value,
      ).join('\n'),
    ).not.toContain('whsec_');
    expect(storageDump()).not.toContain('whsec_');
    expect(queryClient.getQueryCache().getAll().length).toBeGreaterThan(0);
    expect(
      JSON.stringify(queryClient.getQueryCache().getAll().map((query) => query.state.data)),
    ).not.toContain('whsec_');
    expect(
      JSON.stringify(
        queryClient.getMutationCache().getAll().map((mutation) => [
          mutation.state.data,
          mutation.state.variables,
        ]),
      ),
    ).not.toContain('whsec_');

    // Reopening the form or the other tab brings nothing back.
    fireEvent.click(screen.getByRole('button', { name: 'Add endpoint' }));
    await screen.findByRole('textbox', { name: 'URL' });
    expect(document.body.innerHTML).not.toContain('whsec_');
  });

  it('reloads the list after an edit', async () => {
    const updated = { ...active, event_types: ['lead.created'] };
    vi.mocked(webhooksApi.updateEndpoint).mockResolvedValue(updated);
    render(withClient(createProductionQueryClient(), <Webhooks />));

    const row = await rowOf(active.url);
    fireEvent.click(row.getByRole('button', { name: `Edit ${active.url}` }));
    fireEvent.click(await screen.findByRole('checkbox', { name: 'ticket.updated' }));
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({ rows: [inactive, updated], total: 2 });
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() =>
      expect(webhooksApi.updateEndpoint).toHaveBeenCalledWith(4, {
        url: active.url,
        event_types: ['lead.created'],
        is_active: true,
      }),
    );
    await waitFor(() => expect(row.queryByText('ticket.updated')).not.toBeInTheDocument());
    expect(screen.queryByRole('textbox', { name: 'URL' })).not.toBeInTheDocument();
  });

  it('shows the delivery log on its tab', async () => {
    render(<Webhooks />);
    await rowOf(active.url);

    fireEvent.click(screen.getByRole('tab', { name: 'Delivery log' }));

    expect(await screen.findByRole('table', { name: 'Webhook deliveries' })).toBeInTheDocument();
    expect(screen.queryByRole('table', { name: 'Webhook endpoints' })).not.toBeInTheDocument();
    expect(webhooksApi.listDeliveries).toHaveBeenCalled();
  });

  it('shows the server message when the endpoints cannot be loaded', async () => {
    vi.mocked(webhooksApi.listEndpoints).mockRejectedValue({
      response: { status: 500, data: { code: 'INTERNAL_ERROR', message: 'Internal server error' } },
    });
    render(<Webhooks />);

    expect(await screen.findByRole('alert')).toHaveTextContent('Internal server error');
    expect(screen.queryByText('No webhook endpoints yet.')).not.toBeInTheDocument();
  });

  it('keeps the current rows on screen while the next page loads', async () => {
    const nextPage: WebhookEndpoint = { ...active, id: 2, url: 'https://next.example.com/hook' };
    let release: (value: { rows: WebhookEndpoint[]; total: number }) => void = () => {};
    vi.mocked(webhooksApi.listEndpoints).mockImplementation((params) =>
      params?.offset === 20
        ? new Promise((resolve) => {
            release = resolve;
          })
        : Promise.resolve({ rows: [inactive, active], total: 25 }),
    );
    render(<Webhooks />);
    await screen.findByText(inactive.url);

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(webhooksApi.listEndpoints).toHaveBeenCalledWith(
        expect.objectContaining({ offset: 20, limit: 20 }),
      ),
    );
    expect(screen.getByText(inactive.url)).toBeInTheDocument();
    expect(screen.getByText(active.url)).toBeInTheDocument();
    expect(screen.queryByText('Loading...')).not.toBeInTheDocument();

    release({ rows: [nextPage], total: 25 });
    expect(await screen.findByText(nextPage.url)).toBeInTheDocument();
    expect(screen.queryByText(inactive.url)).not.toBeInTheDocument();
  });

  describe('test ping', () => {
    const ping = async () => {
      render(<Webhooks />);
      const row = await rowOf(active.url);
      fireEvent.click(row.getByRole('button', { name: `Send test ping to ${active.url}` }));
      await waitFor(() => expect(webhooksApi.testEndpoint).toHaveBeenCalledWith(4));
    };

    it('reports a succeeded ping with its HTTP status', async () => {
      vi.mocked(webhooksApi.testEndpoint).mockResolvedValue(pingResult({}));
      await ping();

      expect(await screen.findByText('Test ping succeeded (HTTP 200)')).toBeInTheDocument();
      expect(webhooksApi.testEndpoint).toHaveBeenCalledTimes(1);
    });

    it('reports a failed ping with its error code and HTTP status', async () => {
      vi.mocked(webhooksApi.testEndpoint).mockResolvedValue(
        pingResult({ outcome: 'failed', http_status: 500, error_code: 'http_status' }),
      );
      await ping();

      expect(await screen.findByText('Test ping failed: http_status (HTTP 500)')).toBeInTheDocument();
    });

    it('reports a ping that got no answer', async () => {
      vi.mocked(webhooksApi.testEndpoint).mockResolvedValue(
        pingResult({ outcome: 'failed', http_status: null, error_code: 'destination_refused' }),
      );
      await ping();

      expect(
        await screen.findByText('Test ping failed: destination_refused (no response)'),
      ).toBeInTheDocument();
    });

    it('shows the server message when the ping request fails', async () => {
      vi.mocked(webhooksApi.testEndpoint).mockRejectedValue({
        response: { status: 404, data: { code: 'NOT_FOUND', message: 'Webhook endpoint not found' } },
      });
      await ping();

      expect(await screen.findByText('Webhook endpoint not found')).toBeInTheDocument();
    });

    it('disables the ping button of the row while its request runs', async () => {
      let release: (value: WebhookPingResult) => void = () => {};
      vi.mocked(webhooksApi.testEndpoint).mockImplementation(
        () =>
          new Promise((resolve) => {
            release = resolve;
          }),
      );
      await ping();
      const row = await rowOf(active.url);
      const button = row.getByRole('button', { name: `Send test ping to ${active.url}` });

      await waitFor(() => expect(button).toBeDisabled());
      release(pingResult({}));
      await waitFor(() => expect(button).toBeEnabled());
    });

    it('reports each ping of two rows and keeps the first disabled until its own ping ends', async () => {
      const releases = new Map<number, (value: WebhookPingResult) => void>();
      vi.mocked(webhooksApi.testEndpoint).mockImplementation(
        (id) =>
          new Promise((resolve) => {
            releases.set(id, resolve);
          }),
      );
      render(<Webhooks />);
      const first = await rowOf(active.url);
      const second = await rowOf(inactive.url);
      const firstButton = first.getByRole('button', { name: `Send test ping to ${active.url}` });
      const secondButton = second.getByRole('button', {
        name: `Send test ping to ${inactive.url}`,
      });

      fireEvent.click(firstButton);
      await waitFor(() => expect(webhooksApi.testEndpoint).toHaveBeenCalledWith(4));
      fireEvent.click(secondButton);
      await waitFor(() => expect(webhooksApi.testEndpoint).toHaveBeenCalledWith(5));

      // The first row stays disabled while its own ping runs, though the
      // second row started a ping after it.
      await waitFor(() => expect(secondButton).toBeDisabled());
      expect(firstButton).toBeDisabled();

      releases.get(4)?.(
        pingResult({ outcome: 'failed', http_status: null, error_code: 'destination_refused' }),
      );
      expect(
        await screen.findByText('Test ping failed: destination_refused (no response)'),
      ).toBeInTheDocument();
      await waitFor(() => expect(firstButton).toBeEnabled());
      expect(secondButton).toBeDisabled();

      releases.get(5)?.(pingResult({ http_status: 204 }));
      expect(await screen.findByText('Test ping succeeded (HTTP 204)')).toBeInTheDocument();
      await waitFor(() => expect(secondButton).toBeEnabled());
      expect(webhooksApi.testEndpoint).toHaveBeenCalledTimes(2);
    });
  });

  describe('rotate secret', () => {
    const openConfirmation = async () => {
      const row = await rowOf(active.url);
      fireEvent.click(row.getByRole('button', { name: `Rotate secret of ${active.url}` }));
      return screen.findByRole('dialog', { name: 'Rotate signing secret' });
    };

    it('sends nothing when the confirmation is cancelled', async () => {
      render(<Webhooks />);

      const dialog = await openConfirmation();
      expect(dialog).toHaveTextContent(active.url);
      expect(dialog).toHaveTextContent('24 hours');
      fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'Rotate signing secret' })).not.toBeInTheDocument(),
      );
      expect(webhooksApi.rotateSecret).not.toHaveBeenCalled();
    });

    it('shows the new secret once with the end of the previous one and keeps it nowhere after Done', async () => {
      const consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((method) =>
        vi.spyOn(console, method),
      );
      vi.mocked(webhooksApi.rotateSecret).mockResolvedValue({
        ...active,
        secret: ROTATED_SECRET,
        previous_secret_expires_at: PREVIOUS_EXPIRES_AT,
      });
      const queryClient = createProductionQueryClient();
      render(withClient(queryClient, <Webhooks />));

      const confirmation = await openConfirmation();
      fireEvent.click(within(confirmation).getByRole('button', { name: 'Rotate' }));

      await waitFor(() => expect(webhooksApi.rotateSecret).toHaveBeenCalledWith(4));
      const dialog = await screen.findByRole('dialog', { name: 'New signing secret' });
      expect(within(dialog).getByTestId('webhook-rotated-secret')).toHaveValue(ROTATED_SECRET);
      expect(within(dialog).getByRole('button', { name: 'Copy signing secret' })).toBeInTheDocument();
      expect(dialog).toHaveTextContent('cannot be shown again');
      expect(dialog).toHaveTextContent(
        `The previous secret keeps verifying until ${formatDate(PREVIOUS_EXPIRES_AT, 'PPp')}.`,
      );
      // The list is fetched again, never patched with the answer.
      await waitFor(() => expect(webhooksApi.listEndpoints).toHaveBeenCalledTimes(2));
      expect(storageDump()).not.toContain(ROTATED_SECRET);

      fireEvent.click(within(dialog).getByRole('button', { name: 'Done' }));
      await waitFor(() =>
        expect(screen.queryByRole('dialog', { name: 'New signing secret' })).not.toBeInTheDocument(),
      );

      expect(document.body.innerHTML).not.toContain(ROTATED_SECRET);
      expect(
        Array.from(document.querySelectorAll('input, textarea'))
          .map((field) => (field as HTMLInputElement).value)
          .join('\n'),
      ).not.toContain(ROTATED_SECRET);
      expect(storageDump()).not.toContain(ROTATED_SECRET);
      expect(queryClient.getQueryCache().getAll().length).toBeGreaterThan(0);
      expect(
        JSON.stringify(queryClient.getQueryCache().getAll().map((query) => query.state.data)),
      ).not.toContain(ROTATED_SECRET);
      expect(
        JSON.stringify(
          queryClient.getMutationCache().getAll().map((mutation) => [
            mutation.state.data,
            mutation.state.variables,
          ]),
        ),
      ).not.toContain(ROTATED_SECRET);
      for (const spy of consoleSpies) {
        expect(JSON.stringify(spy.mock.calls.map((call) => call.map(String)))).not.toContain(
          ROTATED_SECRET,
        );
        spy.mockRestore();
      }
    });

    it('shows the server message when the rotation fails', async () => {
      vi.mocked(webhooksApi.rotateSecret).mockRejectedValue({
        response: { status: 404, data: { code: 'NOT_FOUND', message: 'Webhook endpoint not found' } },
      });
      render(<Webhooks />);

      fireEvent.click(within(await openConfirmation()).getByRole('button', { name: 'Rotate' }));

      expect(await screen.findByText('Webhook endpoint not found')).toBeInTheDocument();
      expect(webhooksApi.rotateSecret).toHaveBeenCalledWith(4);
      expect(screen.queryByRole('dialog', { name: 'New signing secret' })).not.toBeInTheDocument();
    });
  });
});
