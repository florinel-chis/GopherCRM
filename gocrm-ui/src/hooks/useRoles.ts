import { useQuery } from '@tanstack/react-query';
import { rolesApi } from '@/api/endpoints';

/** The role list's query key. A role write invalidates it, and every reader of the list follows. */
export const rolesQueryKey = ['roles'] as const;

/** The role list, from the one query every screen that offers roles reads. */
export const useRoles = (options: { enabled?: boolean } = {}) =>
  useQuery({
    queryKey: rolesQueryKey,
    queryFn: rolesApi.getRoles,
    enabled: options.enabled ?? true,
  });
