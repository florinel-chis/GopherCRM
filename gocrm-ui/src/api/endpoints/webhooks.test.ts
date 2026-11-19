import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { webhooksApi, WEBHOOK_DELIVERY_STATUSES, WEBHOOK_EVENT_TYPES } from './webhooks';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const repoRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  '..',
  '..',
  '..',
  '..',
);
const swagger = JSON.parse(readFileSync(path.join(repoRoot, 'api', 'swagger.json'), 'utf8')) as {
  definitions: Record<
    string,
    { properties: Record<string, { enum?: string[]; items?: { enum?: string[] } }> }
  >;
};

const endpoint = {
  id: 4,
  created_at: '2026-11-20T09:30:00Z',
  updated_at: '2026-11-20T09:30:00Z',
  url: 'https://hooks.example.com/gophercrm',
  event_types: ['lead.created'],
  is_active: true,
};

const delivery = {
  id: 91,
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
};

describe('webhook lists against the API specification', () => {
  it('WEBHOOK_EVENT_TYPES equals the event_types enum of the create request', () => {
    const documented =
      swagger.definitions['internal_handler.WebhookEndpointCreateRequest'].properties.event_types
        .items?.enum ?? [];

    expect(documented.length).toBeGreaterThan(0);
    expect(new Set(WEBHOOK_EVENT_TYPES).size).toBe(WEBHOOK_EVENT_TYPES.length);
    expect([...WEBHOOK_EVENT_TYPES].sort()).toEqual([...documented].sort());
  });

  it('WEBHOOK_DELIVERY_STATUSES equals the status enum of a delivery', () => {
    const documented =
      swagger.definitions['github_com_florinel-chis_gophercrm_internal_models.WebhookDelivery'].properties.status.enum ?? [];

    expect(documented.length).toBeGreaterThan(0);
    expect([...WEBHOOK_DELIVERY_STATUSES].sort()).toEqual([...documented].sort());
  });
});

describe('webhooksApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists endpoints with the page and the sort and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [endpoint], meta: { total: 37 } });

    const result = await webhooksApi.listEndpoints({
      offset: 20,
      limit: 20,
      sort_by: 'id',
      sort_order: 'desc',
    });

    expect(api.get).toHaveBeenCalledWith('/webhooks/endpoints', {
      params: { offset: 20, limit: 20, sort_by: 'id', sort_order: 'desc' },
    });
    expect(result).toEqual({ rows: [endpoint], total: 37 });
  });

  it('lists endpoints without a sort when none is asked for', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [endpoint] });

    const result = await webhooksApi.listEndpoints({ offset: 0, limit: 100 });

    expect(api.get).toHaveBeenCalledWith('/webhooks/endpoints', {
      params: { offset: 0, limit: 100 },
    });
    // Without meta the total falls back to the rows received.
    expect(result.total).toBe(1);
  });

  it('answers an empty page when the list is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: null });

    expect(await webhooksApi.listEndpoints({ offset: 0, limit: 20 })).toEqual({
      rows: [],
      total: 0,
    });
  });

  it('creates an endpoint with POST and returns the answer with its secret', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: { ...endpoint, secret: 'whsec_once' } });
    const input = { url: endpoint.url, event_types: ['lead.created'], is_active: true };

    const result = await webhooksApi.createEndpoint(input);

    expect(api.post).toHaveBeenCalledWith('/webhooks/endpoints', input);
    expect(result.secret).toBe('whsec_once');
    expect(result.id).toBe(4);
  });

  it('reads one endpoint', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: endpoint });

    expect(await webhooksApi.getEndpoint(4)).toEqual(endpoint);
    expect(api.get).toHaveBeenCalledWith('/webhooks/endpoints/4');
  });

  it('updates an endpoint with PUT and sends exactly the patch', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...endpoint, is_active: false } });

    const result = await webhooksApi.updateEndpoint(4, { is_active: false });

    expect(api.put).toHaveBeenCalledWith('/webhooks/endpoints/4', { is_active: false });
    expect(result.is_active).toBe(false);
  });

  it('deletes an endpoint', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await expect(webhooksApi.deleteEndpoint(4)).resolves.toBeUndefined();
    expect(api.delete).toHaveBeenCalledWith('/webhooks/endpoints/4');
  });

  it('lists deliveries with every filter and the page, in the order received', async () => {
    const newer = { ...delivery, id: 92, event_type: 'lead.updated' };
    vi.mocked(api.get).mockResolvedValue({ data: [newer, delivery], meta: { total: 2 } });

    const result = await webhooksApi.listDeliveries({
      endpoint_id: 4,
      status: 'failed',
      event_type: 'lead.updated',
      offset: 40,
      limit: 20,
    });

    expect(api.get).toHaveBeenCalledWith('/webhooks/deliveries', {
      params: {
        endpoint_id: 4,
        status: 'failed',
        event_type: 'lead.updated',
        offset: 40,
        limit: 20,
      },
    });
    expect(result.rows.map((row) => row.id)).toEqual([92, 91]);
    expect(result.total).toBe(2);
  });

  it('leaves unset delivery filters out of the request', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [], meta: { total: 0 } });

    await webhooksApi.listDeliveries({
      endpoint_id: undefined,
      status: undefined,
      event_type: undefined,
      offset: 0,
      limit: 20,
    });

    const [, config] = vi.mocked(api.get).mock.calls[0];
    expect(config).toEqual({ params: { offset: 0, limit: 20 } });
    expect(Object.keys((config as { params: object }).params)).toEqual(['offset', 'limit']);
  });
});
