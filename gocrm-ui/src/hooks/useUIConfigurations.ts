import { useQuery } from '@tanstack/react-query';
import { configurationsApi } from '@/api/endpoints';
import { UI_CONFIGURATIONS_KEY } from '@/contexts/configuration-context';
import { useAuth } from '@/hooks/useAuth';

/**
 * The cache key of the signed-in user's copy of GET /configurations/ui. A
 * sign-in, sign-out or account switch moves to another entry, so one
 * account's values are never served to the next.
 */
export const uiConfigurationsQueryKey = (userId: number) =>
  [...UI_CONFIGURATIONS_KEY, { userId }] as const;

/**
 * The one authenticated query of GET /configurations/ui, shared by the
 * configuration provider, the deal form and the default-currency hook. It
 * only runs for an established session: on the sign-in and registration
 * pages no request (and so no refresh attempt or redirect) is made.
 */
export const useUIConfigurationsQuery = (enabled = true) => {
  const { user } = useAuth();
  const userId = user?.id;
  return useQuery({
    queryKey: uiConfigurationsQueryKey(userId ?? 0),
    queryFn: () => configurationsApi.getUIConfigurations(),
    enabled: enabled && userId !== undefined,
  });
};
