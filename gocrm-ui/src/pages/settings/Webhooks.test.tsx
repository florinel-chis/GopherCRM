import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { Component as Webhooks } from './Webhooks';
import { webhooksApi, type WebhookEndpoint } from '@/api/endpoints';
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
});
