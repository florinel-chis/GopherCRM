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
  'quote.created',
  'quote.deleted',
  'quote.updated',
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

// The answer of a create; a new secret is also returned once by rotateSecret.
export interface WebhookEndpointCreated extends WebhookEndpoint {
  secret: string;
}

// The answer of rotateSecret: the new signing secret, shown once. Until
// previous_secret_expires_at every request also carries a signature under the
// replaced secret.
export interface WebhookEndpointSecretRotated extends WebhookEndpoint {
  secret: string;
  previous_secret_expires_at: string | null;
}

// The outcome and failure codes of a test ping. webhooks.test.ts holds both
// lists equal to the API specification.
export const WEBHOOK_PING_OUTCOMES = ['succeeded', 'failed'] as const;

export type WebhookPingOutcome = (typeof WEBHOOK_PING_OUTCOMES)[number];

export const WEBHOOK_PING_ERROR_CODES = [
  'http_status',
  'redirect',
  'timeout',
  'connection_failed',
  'destination_refused',
  'secret_unavailable',
] as const;

export type WebhookPingErrorCode = (typeof WEBHOOK_PING_ERROR_CODES)[number];

// The answer of a test ping. Nothing is written to the delivery log.
export interface WebhookPingResult {
  event_id: string;
  outcome: WebhookPingOutcome;
  // Null when no answer was received.
  http_status: number | null;
  // Null after a success.
  error_code: WebhookPingErrorCode | null;
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

  // Sends one signed ping event now and answers its outcome; a failed ping is
  // still a 200. Nothing is written to the delivery log.
  testEndpoint: async (id: number): Promise<WebhookPingResult> => {
    const response = await api.post<WebhookPingResult>(`/webhooks/endpoints/${id}/test`);
    return response.data;
  },

  // Replaces the signing secret and returns the new one, which no later
  // request can read again.
  rotateSecret: async (id: number): Promise<WebhookEndpointSecretRotated> => {
    const response = await api.post<WebhookEndpointSecretRotated>(
      `/webhooks/endpoints/${id}/rotate-secret`,
    );
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

  // Queues a succeeded or failed delivery to be sent again; 409 while it is
  // pending or being sent, or when its endpoint is deleted or inactive.
  redeliverDelivery: async (id: number): Promise<WebhookDelivery> => {
    const response = await api.post<WebhookDelivery>(`/webhooks/deliveries/${id}/redeliver`);
    return response.data;
  },
};
