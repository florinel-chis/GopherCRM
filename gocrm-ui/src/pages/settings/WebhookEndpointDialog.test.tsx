import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@/test/test-utils';
import { WebhookEndpointDialog } from './WebhookEndpointDialog';
import { webhooksApi, WEBHOOK_EVENT_TYPES, type WebhookEndpoint } from '@/api/endpoints';

vi.mock('@/api/endpoints', async () => {
  const actual = await vi.importActual<typeof import('@/api/endpoints/webhooks')>(
    '@/api/endpoints/webhooks',
  );
  return {
    WEBHOOK_EVENT_TYPES: actual.WEBHOOK_EVENT_TYPES,
    WEBHOOK_DELIVERY_STATUSES: actual.WEBHOOK_DELIVERY_STATUSES,
    webhooksApi: {
      createEndpoint: vi.fn(),
      updateEndpoint: vi.fn(),
    },
  };
});

// The rule text of the server (webhook.WebhookURLRule).
const URL_RULE =
  'must be an https URL of at most 2048 characters, without credentials, query string or fragment, whose host is not a private, loopback, link-local or metadata address';

const stored: WebhookEndpoint = {
  id: 4,
  created_at: '2026-11-20T09:30:00Z',
  updated_at: '2026-11-20T09:30:00Z',
  url: 'https://hooks.example.com/gophercrm',
  event_types: ['lead.created'],
  is_active: true,
};

// The shape the axios interceptor leaves: the error object of the envelope,
// flat under response.data.
const validationError = (details: Record<string, string>) => ({
  response: { status: 400, data: { code: 'VALIDATION_ERROR', message: 'Validation failed', details } },
});

const setup = (endpoint: WebhookEndpoint | null = null) => {
  const handlers = { onClose: vi.fn(), onCreated: vi.fn(), onUpdated: vi.fn() };
  render(<WebhookEndpointDialog open endpoint={endpoint} {...handlers} />);
  return handlers;
};

const typeURL = (value: string) =>
  fireEvent.change(screen.getByRole('textbox', { name: 'URL' }), { target: { value } });

const tick = (eventType: string) =>
  fireEvent.click(screen.getByRole('checkbox', { name: eventType }));

describe('WebhookEndpointDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('offers one checkbox per entry of the shared event list and no other', () => {
    setup();

    const events = within(screen.getByRole('group', { name: 'Events' }));
    expect(events.getAllByRole('checkbox')).toHaveLength(WEBHOOK_EVENT_TYPES.length);
    for (const eventType of WEBHOOK_EVENT_TYPES) {
      expect(events.getByRole('checkbox', { name: eventType })).not.toBeChecked();
    }
    expect(screen.getByRole('switch', { name: 'Active' })).toBeChecked();
  });

  it('offers the deleted events', () => {
    setup();

    const events = within(screen.getByRole('group', { name: 'Events' }));
    for (const eventType of ['lead.deleted', 'customer.deleted', 'ticket.deleted']) {
      expect(events.getByRole('checkbox', { name: eventType })).not.toBeChecked();
    }
  });

  it('offers the deal events', () => {
    setup();

    const events = within(screen.getByRole('group', { name: 'Events' }));
    for (const eventType of ['deal.created', 'deal.updated', 'deal.deleted']) {
      expect(events.getByRole('checkbox', { name: eventType })).not.toBeChecked();
    }
  });

  it('creates with the URL, the ticked events and the active flag and hands the answer over', async () => {
    const created = { ...stored, event_types: ['lead.created', 'ticket.updated'], secret: 'whsec_once' };
    vi.mocked(webhooksApi.createEndpoint).mockResolvedValue(created);
    const handlers = setup();

    typeURL('  https://hooks.example.com/gophercrm  ');
    // Ticked against the list order: the body follows the list.
    tick('ticket.updated');
    tick('lead.created');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    await waitFor(() => expect(handlers.onCreated).toHaveBeenCalledWith(created));
    expect(webhooksApi.createEndpoint).toHaveBeenCalledWith({
      url: 'https://hooks.example.com/gophercrm',
      event_types: ['lead.created', 'ticket.updated'],
      is_active: true,
    });
    expect(webhooksApi.updateEndpoint).not.toHaveBeenCalled();
  });

  it('starts an edit from the stored values and sends url, event_types and is_active', async () => {
    const updated = { ...stored, event_types: ['lead.created', 'lead.updated'], is_active: false };
    vi.mocked(webhooksApi.updateEndpoint).mockResolvedValue(updated);
    const handlers = setup(stored);

    expect(screen.getByRole('dialog', { name: 'Edit webhook endpoint' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'URL' })).toHaveValue(stored.url);
    expect(screen.getByRole('checkbox', { name: 'lead.created' })).toBeChecked();
    expect(screen.getByRole('checkbox', { name: 'lead.updated' })).not.toBeChecked();

    tick('lead.updated');
    fireEvent.click(screen.getByRole('switch', { name: 'Active' }));
    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    await waitFor(() => expect(handlers.onUpdated).toHaveBeenCalledWith(updated));
    expect(webhooksApi.updateEndpoint).toHaveBeenCalledWith(4, {
      url: stored.url,
      event_types: ['lead.created', 'lead.updated'],
      is_active: false,
    });
    expect(webhooksApi.createEndpoint).not.toHaveBeenCalled();
  });

  it('asks for a URL and an event before sending anything', async () => {
    const handlers = setup();

    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(await screen.findByText('Enter the URL of the endpoint')).toBeInTheDocument();
    expect(screen.getByText('Select at least one event')).toBeInTheDocument();
    expect(webhooksApi.createEndpoint).not.toHaveBeenCalled();
    expect(handlers.onCreated).not.toHaveBeenCalled();
  });

  it.each([
    ['a plain http URL', 'http://hooks.example.com/x'],
    ['a URL with credentials', 'https://user:pw@hooks.example.com/x'],
    ['a URL with a query string', 'https://hooks.example.com/x?a=1'],
  ])('sends %s to the server and shows its refusal with the form still open', async (_name, url) => {
    vi.mocked(webhooksApi.createEndpoint).mockRejectedValue(validationError({ url: URL_RULE }));
    const handlers = setup();

    typeURL(url);
    tick('lead.created');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    // The client did not judge the URL: the request went out unchanged.
    await waitFor(() =>
      expect(webhooksApi.createEndpoint).toHaveBeenCalledWith({
        url,
        event_types: ['lead.created'],
        is_active: true,
      }),
    );
    expect(await screen.findByRole('alert')).toHaveTextContent('Validation failed');
    expect(screen.getByText(URL_RULE)).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'URL' })).toHaveAttribute('aria-invalid', 'true');

    expect(screen.getByRole('dialog', { name: 'Add webhook endpoint' })).toBeInTheDocument();
    expect(screen.getByRole('textbox', { name: 'URL' })).toHaveValue(url);
    expect(handlers.onCreated).not.toHaveBeenCalled();
    expect(handlers.onClose).not.toHaveBeenCalled();
    await waitFor(() => expect(screen.getByRole('button', { name: 'Create' })).toBeEnabled());
  });

  it('shows the event_types detail of a refusal under the checkboxes', async () => {
    const rule = 'must list 1 to 12 known event types';
    vi.mocked(webhooksApi.updateEndpoint).mockRejectedValue(validationError({ event_types: rule }));
    setup(stored);

    fireEvent.click(screen.getByRole('button', { name: 'Save' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('Validation failed');
    expect(within(screen.getByRole('group', { name: 'Events' })).getByText(rule)).toBeInTheDocument();
  });

  it('falls back to a generic message when the failure carries none', async () => {
    vi.mocked(webhooksApi.createEndpoint).mockRejectedValue(new Error('Network Error'));
    setup();

    typeURL('https://hooks.example.com/x');
    tick('lead.created');
    fireEvent.click(screen.getByRole('button', { name: 'Create' }));

    expect(await screen.findByRole('alert')).toHaveTextContent('The endpoint could not be saved.');
  });

  it('mounts no form while closed and reopens from the stored values', () => {
    const handlers = { onClose: vi.fn(), onCreated: vi.fn(), onUpdated: vi.fn() };
    const view = render(<WebhookEndpointDialog open={false} endpoint={null} {...handlers} />);
    expect(screen.queryByRole('textbox', { name: 'URL' })).not.toBeInTheDocument();

    view.rerender(<WebhookEndpointDialog open endpoint={null} {...handlers} />);
    typeURL('https://draft.example.com/x');
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
    expect(handlers.onClose).toHaveBeenCalledTimes(1);
  });
});
