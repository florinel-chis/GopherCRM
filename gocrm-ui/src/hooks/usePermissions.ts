import { useCallback, useMemo } from 'react';
import { useAuth } from '@/hooks/useAuth';
import type { Permission } from '@/types';

export type PermissionScope = Permission['scope'];

export interface PermissionChecks {
  /** True when the key is held; a list is any-of. */
  can: (key: string | string[]) => boolean;
  /** The scope the key is held at, or null when it is not held. */
  scopeOf: (key: string) => PermissionScope | null;
  /**
   * The per-record rule: scope all covers every record, scope own only the
   * records whose owner (or assignee) is the signed-in user.
   */
  canOnRecord: (key: string, ownerId: number | null | undefined) => boolean;
}

/**
 * The permission checks of the signed-in user, from the list GET /users/me
 * returned. The role name never decides: a missing or empty list grants
 * nothing, whatever the role is called.
 */
export const usePermissions = (): PermissionChecks => {
  const { user, permissions } = useAuth();
  const userId = user?.id;

  const scopes = useMemo(() => {
    const map = new Map<string, PermissionScope>();
    for (const permission of permissions ?? []) {
      // A key listed at both scopes counts at the wider one.
      if (map.get(permission.key) !== 'all') {
        map.set(permission.key, permission.scope);
      }
    }
    return map;
  }, [permissions]);

  const can = useCallback(
    (key: string | string[]) => (Array.isArray(key) ? key : [key]).some((k) => scopes.has(k)),
    [scopes],
  );

  const scopeOf = useCallback((key: string) => scopes.get(key) ?? null, [scopes]);

  const canOnRecord = useCallback(
    (key: string, ownerId: number | null | undefined) => {
      const scope = scopes.get(key);
      if (scope === 'all') return true;
      if (scope === 'own') return userId !== undefined && ownerId === userId;
      return false;
    },
    [scopes, userId],
  );

  return { can, scopeOf, canOnRecord };
};
