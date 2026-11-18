import type { Permission, User } from '@/types';
import { systemRolePermissions, type SystemRole } from '@/permissions/systemRoleFixture';

const NONE: Permission[] = [];

/** The permissions GET /users/me lists for a system role (the same array on every call). */
export const permissionsForRole = (role: SystemRole): Permission[] => systemRolePermissions[role];

/** The permissions of a mocked signed-in user: those of its system role, none when signed out. */
export const permissionsOf = (user: Pick<User, 'role'> | null | undefined): Permission[] =>
  user ? (systemRolePermissions[user.role as SystemRole] ?? NONE) : NONE;

/** A custom role: every listed key at scope all unless a scope is given. */
export const customPermissions = (
  ...keys: Array<string | [key: string, scope: Permission['scope']]>
): Permission[] =>
  keys.map((entry) =>
    typeof entry === 'string' ? { key: entry, scope: 'all' } : { key: entry[0], scope: entry[1] },
  );

/** The `can` of a system role, for helpers that take the check as an argument; no role holds nothing. */
export const canAs =
  (role: SystemRole | undefined) =>
  (key: string): boolean =>
    (role ? systemRolePermissions[role] : NONE).some((permission) => permission.key === key);
