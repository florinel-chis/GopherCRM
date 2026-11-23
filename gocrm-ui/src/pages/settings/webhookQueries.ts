import { useMutation, useQueryClient } from '@tanstack/react-query';
import {
  webhooksApi,
  type WebhookDeliveryListParams,
  type WebhookEndpointListParams,
} from '@/api/endpoints';

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

// The part of a rotation answer the page keeps, in component state only.
export interface RotatedWebhookSecret {
  secret: string;
  previous_secret_expires_at: string | null;
}

// Sends a test ping. The ping writes nothing (no endpoint field and no
// delivery row), so nothing is invalidated.
export const useTestWebhookEndpoint = () =>
  useMutation({
    mutationFn: (id: number) => webhooksApi.testEndpoint(id),
  });

// Rotates the signing secret of an endpoint. The new secret is handed to
// onRotated and the mutation itself resolves with nothing, so the secret never
// sits in the mutation cache; the endpoint list is fetched again instead of
// being patched with the answer.
export const useRotateWebhookSecret = (onRotated: (rotated: RotatedWebhookSecret) => void) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: async (id: number): Promise<void> => {
      const { secret, previous_secret_expires_at } = await webhooksApi.rotateSecret(id);
      onRotated({ secret, previous_secret_expires_at });
    },
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webhookKeys.all });
    },
  });
};

// Queues a logged delivery again; the log refetches through the invalidation.
export const useRedeliverWebhookDelivery = () => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (id: number) => webhooksApi.redeliverDelivery(id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: webhookKeys.all });
    },
  });
};
