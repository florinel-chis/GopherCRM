import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import { toListResult, withoutUnset, type ListResult } from '../list';

// Outbound webhooks: the endpoint registry and the delivery log. Every route
// needs webhooks.manage.

// The event types an endpoint may subscribe to. This is the one list the
// screens read; webhooks.test.ts holds it equal to the API specification.
export const WEBHOOK_EVENT_TYPES = [
  'company.created',
  'company.deleted',
  'company.updated',
  'customer.created',
  'customer.deleted',
  'customer.updated',
  'deal.created',
  'deal.deleted',
  'deal.updated',
  'lead.created',
  'lead.deleted',
  'lead.updated',
  'task.created',
  'task.deleted',
  'task.updated',
  'ticket.created',
  'ticket.deleted',
  'ticket.updated',
] as const;

export type WebhookEventType = (typeof WEBHOOK_EVENT_TYPES)[number];

export const WEBHOOK_DELIVERY_STATUSES = ['pending', 'sending', 'succeeded', 'failed'] as const;

export type WebhookDeliveryStatus = (typeof WEBHOOK_DELIVERY_STATUSES)[number];

export interface WebhookEndpoint {
  id: number;
  created_at: string;
  updated_at: string;
  url: string;
  event_types: string[];
  is_active: boolean;
}

// The answer of a create; the secret is also returned once by the API's rotate-secret.
export interface WebhookEndpointCreated extends WebhookEndpoint {
  secret: string;
}

export interface WebhookEndpointInput {
  url: string;
  event_types: string[];
  is_active?: boolean;
}

// An absent field keeps the stored value; at least one must be present.
export type WebhookEndpointPatch = Partial<WebhookEndpointInput>;

export interface WebhookDelivery {
  id: number;
  endpoint_id: number;
  event_id: string;
  event_type: string;
  record_id: number;
  status: WebhookDeliveryStatus;
  attempts: number;
  last_attempt_at: string | null;
  last_http_status: number | null;
  next_attempt_at: string | null;
  error_code: string;
  occurred_at: string;
  created_at: string;
  updated_at: string;
}

export type WebhookEndpointSortColumn = 'id' | 'created_at' | 'updated_at';

export interface WebhookEndpointListParams {
  offset: number;
  limit: number;
  // Without sort_by the API orders by id ascending.
  sort_by?: WebhookEndpointSortColumn;
  sort_order?: 'asc' | 'desc';
}

export interface WebhookDeliveryListParams {
  endpoint_id?: number;
  status?: WebhookDeliveryStatus;
  event_type?: WebhookEventType;
  offset: number;
  limit: number;
}

export const webhooksApi = {
  listEndpoints: async (
    params: WebhookEndpointListParams,
  ): Promise<ListResult<WebhookEndpoint>> => {
    const response = (await api.get<WebhookEndpoint[]>('/webhooks/endpoints', {
      params: withoutUnset(params),
    })) as ApiResponseWithMeta<WebhookEndpoint[]>;
    return toListResult(response);
  },

  // 201 with the signing secret, which no later request can read again.
  createEndpoint: async (data: WebhookEndpointInput): Promise<WebhookEndpointCreated> => {
    const response = await api.post<WebhookEndpointCreated>('/webhooks/endpoints', data);
    return response.data;
  },

  getEndpoint: async (id: number): Promise<WebhookEndpoint> => {
    const response = await api.get<WebhookEndpoint>(`/webhooks/endpoints/${id}`);
    return response.data;
  },

  updateEndpoint: async (id: number, patch: WebhookEndpointPatch): Promise<WebhookEndpoint> => {
    const response = await api.put<WebhookEndpoint>(`/webhooks/endpoints/${id}`, patch);
    return response.data;
  },

  // 204 with no body; the endpoint's delivery rows are deleted with it.
  deleteEndpoint: async (id: number): Promise<void> => {
    await api.delete(`/webhooks/endpoints/${id}`);
  },

  // Newest first (id descending); the order is the API's.
  listDeliveries: async (
    params: WebhookDeliveryListParams,
  ): Promise<ListResult<WebhookDelivery>> => {
    const response = (await api.get<WebhookDelivery[]>('/webhooks/deliveries', {
      params: withoutUnset(params),
    })) as ApiResponseWithMeta<WebhookDelivery[]>;
    return toListResult(response);
  },
};
