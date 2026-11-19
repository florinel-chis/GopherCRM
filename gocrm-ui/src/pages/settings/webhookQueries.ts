import type { WebhookDeliveryListParams, WebhookEndpointListParams } from '@/api/endpoints';

// Query keys of the webhook screens. Everything sits under `all`, so one
// invalidation after a write refreshes the endpoint list, the endpoint filter
// and the delivery log (deleting an endpoint removes its deliveries too).
// The signing secret is never part of a cached value: it is held in the
// page's component state only.
export const webhookKeys = {
  all: ['webhooks'] as const,
  endpoints: (params: WebhookEndpointListParams) => ['webhooks', 'endpoints', params] as const,
  endpointOptions: () => ['webhooks', 'endpoint-options'] as const,
  deliveries: (params: WebhookDeliveryListParams) => ['webhooks', 'deliveries', params] as const,
};
