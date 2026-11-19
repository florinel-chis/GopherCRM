import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { WebhookDeliveryLog } from './WebhookDeliveryLog';
import { webhooksApi, type WebhookDelivery, type WebhookEndpoint } from '@/api/endpoints';
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
      listDeliveries: vi.fn(),
    },
  };
});

const endpointA: WebhookEndpoint = {
  id: 4,
  created_at: '2026-11-20T09:30:00Z',
  updated_at: '2026-11-20T09:30:00Z',
  url: 'https://a.example.com/hook',
  event_types: ['lead.created'],
  is_active: true,
};
const endpointB: WebhookEndpoint = { ...endpointA, id: 5, url: 'https://b.example.com/hook' };

const delivery = (overrides: Partial<WebhookDelivery>): WebhookDelivery => ({
  id: 1,
  endpoint_id: 4,
  event_id: 'evt_4f1c2a9be07d4c4fa1c3d2e5f6a7b8c9',
  event_type: 'lead.created',
  record_id: 42,
  status: 'pending',
  attempts: 0,
  last_attempt_at: null,
  last_http_status: null,
  next_attempt_at: '2026-11-20T09:31:00Z',
  error_code: '',
  occurred_at: '2026-11-20T09:30:00Z',
  created_at: '2026-11-20T09:30:00Z',
  updated_at: '2026-11-20T09:30:00Z',
  ...overrides,
});

// Not sorted by any column a client could pick: ids 7, 9, 8, statuses and
// event types mixed. The table must keep exactly this order.
const rows: WebhookDelivery[] = [
  delivery({
    id: 7,
    event_type: 'ticket.updated',
    record_id: 300,
    status: 'failed',
    attempts: 10,
    last_attempt_at: '2026-11-21T12:00:00Z',
    last_http_status: 503,
    error_code: 'http_status',
  }),
  delivery({ id: 9, event_type: 'customer.created', record_id: 100 }),
  delivery({
    id: 8,
    event_type: 'lead.updated',
    record_id: 200,
    status: 'succeeded',
    attempts: 1,
    last_attempt_at: '2026-11-20T10:00:00Z',
    last_http_status: 204,
  }),
];

const lastParams = () => vi.mocked(webhooksApi.listDeliveries).mock.calls.at(-1)?.[0];

const pickSelect = async (name: string, option: string) => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name }));
  fireEvent.click(await screen.findByRole('option', { name: option }));
};

const dataRows = () => screen.getAllByTestId('webhook-delivery-row');

describe('WebhookDeliveryLog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(webhooksApi.listEndpoints).mockResolvedValue({ rows: [endpointB, endpointA], total: 2 });
    vi.mocked(webhooksApi.listDeliveries).mockResolvedValue({ rows, total: 45 });
  });

  it('shows the six columns of each delivery in the order the API returned', async () => {
    render(<WebhookDeliveryLog />);
    await screen.findByText('ticket.updated');

    const table = screen.getByRole('table', { name: 'Webhook deliveries' });
    expect(within(table).getAllByRole('columnheader').map((cell) => cell.textContent)).toEqual([
      'Event type',
      'Record ID',
      'Status',
      'Attempts',
      'Last attempt',
      'Last response code',
    ]);

    const cells = dataRows().map((row) =>
      within(row)
        .getAllByRole('cell')
        .map((cell) => cell.textContent),
    );
    expect(cells).toEqual([
      ['ticket.updated', '300', 'failed', '10', formatDate('2026-11-21T12:00:00Z', 'PPp'), '503'],
      // No attempt yet: the date fallback and a dash for the response code.
      ['customer.created', '100', 'pending', '0', formatDate(null, 'PPp'), '-'],
      ['lead.updated', '200', 'succeeded', '1', formatDate('2026-11-20T10:00:00Z', 'PPp'), '204'],
    ]);
    // Nothing beyond the API fields: no URL and no event id in the table.
    expect(table.textContent).not.toContain('example.com');
    expect(table.textContent).not.toContain('evt_');
  });

  it('asks for the first page without any filter', async () => {
    render(<WebhookDeliveryLog />);
    await screen.findByText('ticket.updated');

    expect(lastParams()).toEqual({
      endpoint_id: undefined,
      status: undefined,
      event_type: undefined,
      offset: 0,
      limit: 20,
    });
    // The endpoint filter is fed by one page of 100 endpoints.
    expect(webhooksApi.listEndpoints).toHaveBeenCalledWith({
      offset: 0,
      limit: 100,
      sort_by: 'id',
      sort_order: 'desc',
    });
  });

  it('sends each filter, and All takes it out again', async () => {
    render(<WebhookDeliveryLog />);
    await screen.findByText('ticket.updated');

    await pickSelect('Endpoint', '#4 https://a.example.com/hook');
    await waitFor(() => expect(lastParams()).toMatchObject({ endpoint_id: 4 }));

    await pickSelect('Status', 'failed');
    await waitFor(() => expect(lastParams()).toMatchObject({ endpoint_id: 4, status: 'failed' }));

    await pickSelect('Event type', 'lead.updated');
    await waitFor(() =>
      expect(lastParams()).toEqual({
        endpoint_id: 4,
        status: 'failed',
        event_type: 'lead.updated',
        offset: 0,
        limit: 20,
      }),
    );

    await pickSelect('Endpoint', 'All endpoints');
    await pickSelect('Status', 'All statuses');
    await pickSelect('Event type', 'All event types');
    await waitFor(() =>
      expect(lastParams()).toEqual({
        endpoint_id: undefined,
        status: undefined,
        event_type: undefined,
        offset: 0,
        limit: 20,
      }),
    );
  });

  it('pages with the total of meta and goes back to the first page when a filter changes', async () => {
    render(<WebhookDeliveryLog />);
    await screen.findByText('ticket.updated');
    expect(screen.getByText(/of 45/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(lastParams()).toMatchObject({ offset: 20, limit: 20 }));
    await screen.findByText(/21.40 of 45/);

    await pickSelect('Status', 'pending');
    await waitFor(() => expect(lastParams()).toMatchObject({ status: 'pending', offset: 0, limit: 20 }));
  });

  it('moves to the last existing page when a later page comes back empty', async () => {
    // Rows are pruned between the fetches: the third page is gone, and the
    // total says the second page is now the last.
    let pruned = false;
    vi.mocked(webhooksApi.listDeliveries).mockImplementation(async (params) => {
      if (params?.offset === 40) {
        pruned = true;
        return { rows: [], total: 25 };
      }
      return { rows, total: pruned ? 25 : 45 };
    });
    render(<WebhookDeliveryLog />);
    await screen.findByText(/^1.20 of 45$/);

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await screen.findByText(/^21.40 of 45$/);
    expect(dataRows()).toHaveLength(3);

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(webhooksApi.listDeliveries).toHaveBeenCalledWith(
        expect.objectContaining({ offset: 40, limit: 20 }),
      ),
    );

    // Not the first page: the last one that exists.
    await waitFor(() => expect(lastParams()).toMatchObject({ offset: 20, limit: 20 }));
    await waitFor(() => expect(screen.getByText(/^21.25 of 25$/)).toBeInTheDocument());
    expect(dataRows()).toHaveLength(3);
    expect(screen.queryByText('No deliveries match these filters.')).not.toBeInTheDocument();
  });

  it('shows the empty state when no delivery matches', async () => {
    vi.mocked(webhooksApi.listDeliveries).mockResolvedValue({ rows: [], total: 0 });
    render(<WebhookDeliveryLog />);

    expect(await screen.findByText('No deliveries match these filters.')).toBeInTheDocument();
    expect(screen.queryAllByTestId('webhook-delivery-row')).toHaveLength(0);
  });

  it('shows the server message when the log cannot be loaded', async () => {
    vi.mocked(webhooksApi.listDeliveries).mockRejectedValue({
      response: { status: 400, data: { code: 'VALIDATION_ERROR', message: 'Validation failed' } },
    });
    render(<WebhookDeliveryLog />);

    expect(await screen.findByRole('alert')).toHaveTextContent('Validation failed');
  });
});
