import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';

// The role audit log: GET /roles/audit, guarded by roles.audit_view.
// roleAudit.test.ts holds the two lists below equal to the action and
// auth_method enums of the API specification.

export const ROLE_AUDIT_ACTIONS = [
  'role_created',
  'role_updated',
  'role_deleted',
  'user_role_changed',
  'user_activated',
  'user_deactivated',
  'user_erased',
  'user_email_changed',
] as const;

export const ROLE_AUDIT_METHODS = ['bearer', 'api_key', 'oidc', 'scim', 'migration'] as const;

export type RoleAuditAction = (typeof ROLE_AUDIT_ACTIONS)[number];
export type RoleAuditMethod = (typeof ROLE_AUDIT_METHODS)[number];

/** One row: the stored columns, nothing joined. */
export interface RoleAuditEntry {
  id: number;
  created_at: string;
  /** Null for a SCIM write or a data step. */
  actor_user_id: number | null;
  auth_method: string;
  action: string;
  /** Null when a user received a system role, and on an account row. */
  role_id: number | null;
  /** Empty on an account row. */
  role_name: string;
  /** Null on a role change. */
  target_user_id: number | null;
  before_state: unknown;
  after_state: unknown;
}

export interface RoleAuditListParams {
  offset: number;
  limit: number;
}

export interface RoleAuditListResult {
  rows: RoleAuditEntry[];
  total: number;
}

export const roleAuditApi = {
  // Newest first (created_at, then id, descending); the order is the API's.
  list: async ({ offset, limit }: RoleAuditListParams): Promise<RoleAuditListResult> => {
    const response = (await api.get<RoleAuditEntry[]>('/roles/audit', {
      params: { offset, limit },
    })) as ApiResponseWithMeta<RoleAuditEntry[]>;
    const rows = Array.isArray(response.data) ? response.data : [];
    return { rows, total: response.meta?.total ?? rows.length };
  },
};
