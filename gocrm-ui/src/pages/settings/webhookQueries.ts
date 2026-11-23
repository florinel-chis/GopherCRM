import {
  useMutation,
  useMutationState,
  useQueryClient,
  type MutationKey,
} from '@tanstack/react-query';
import {
  webhooksApi,
  type WebhookDelivery,
  type WebhookDeliveryListParams,
  type WebhookEndpointListParams,
  type WebhookPingResult,
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

// Mutation keys of the per-row actions. Several rows can run at once, so the
// pages read the pending ids of every running mutation of a key instead of the
// one mutation an observer last started.
export const webhookMutationKeys = {
  testEndpoint: ['webhooks', 'test-endpoint'] as const,
  redeliverDelivery: ['webhooks', 'redeliver-delivery'] as const,
};

// The outcome handlers of a per-row action. They sit in the mutation options,
// which run for every request, so each request reports its own outcome even
// when another row was pressed before it settled (the options passed to
// mutate() are kept only for the latest call).
export interface WebhookMutationHandlers<TData> {
  onSuccess?: (data: TData, id: number) => void;
  onError?: (failure: unknown, id: number) => void;
}

// The ids of every running mutation of a key.
export const usePendingWebhookIds = (mutationKey: MutationKey): ReadonlySet<number> => {
  const ids = useMutationState({
    filters: { mutationKey, status: 'pending' },
    select: (mutation) => mutation.state.variables as number,
  });
  return new Set(ids);
};

// Sends a test ping. The ping writes nothing (no endpoint field and no
// delivery row), so nothing is invalidated.
export const useTestWebhookEndpoint = (handlers: WebhookMutationHandlers<WebhookPingResult> = {}) =>
  useMutation({
    mutationKey: webhookMutationKeys.testEndpoint,
    mutationFn: (id: number) => webhooksApi.testEndpoint(id),
    onSuccess: (result, id) => handlers.onSuccess?.(result, id),
    onError: (failure, id) => handlers.onError?.(failure, id),
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
export const useRedeliverWebhookDelivery = (
  handlers: WebhookMutationHandlers<WebhookDelivery> = {},
) => {
  const queryClient = useQueryClient();
  return useMutation({
    mutationKey: webhookMutationKeys.redeliverDelivery,
    mutationFn: (id: number) => webhooksApi.redeliverDelivery(id),
    onSuccess: (delivery, id) => {
      void queryClient.invalidateQueries({ queryKey: webhookKeys.all });
      handlers.onSuccess?.(delivery, id);
    },
    onError: (failure, id) => handlers.onError?.(failure, id),
  });
};
