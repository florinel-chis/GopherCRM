import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';

// The sign-in audit log (#455): GET /audit/sign-ins, guarded by
// audit.sign_ins_view. The three lists below are the only values the API
// accepts as filters; signInAudit.test.ts holds them equal to the API
// specification.

export const SIGN_IN_AUDIT_METHODS = [
  'api_key',
  'logout',
  'oidc',
  'password',
  'password_change',
  'refresh',
  'reset_confirm',
  'reset_request',
  'scim',
] as const;

export const SIGN_IN_AUDIT_OUTCOMES = ['refused', 'success'] as const;

export const SIGN_IN_AUDIT_REASONS = [
  'account_erased',
  'account_inactive',
  'account_locked',
  'account_unknown',
  'binding_mismatch',
  'binding_missing',
  'claims_invalid',
  'customer_login_role',
  'discovery_failed',
  'email_missing',
  'email_taken',
  'group_overage',
  'groups_malformed',
  'groups_unavailable',
  'id_token_invalid',
  'id_token_missing',
  'identity_mismatch',
  'identity_taken',
  'internal_error',
  'invalid_credentials',
  'issuer_changed',
  'issuer_invalid',
  'key_expired',
  'key_invalid',
  'key_revoked',
  'last_admin',
  'no_mapping_match',
  'nonce_mismatch',
  'not_configured',
  'password_changed',
  'role_missing',
  'roles_malformed',
  'session_stale',
  'sso_account',
  'sso_disabled',
  'state_invalid',
  'subject_invalid',
  'token_exchange_failed',
  'token_invalid',
  'token_missing',
  'token_reuse',
  'token_revoked',
  'unavailable',
  'userinfo_subject_mismatch',
] as const;

export type SignInAuditMethod = (typeof SIGN_IN_AUDIT_METHODS)[number];
export type SignInAuditOutcome = (typeof SIGN_IN_AUDIT_OUTCOMES)[number];
export type SignInAuditReason = (typeof SIGN_IN_AUDIT_REASONS)[number];

/** One row: the stored columns, nothing joined. */
export interface SignInAuditEntry {
  id: number;
  created_at: string;
  method: SignInAuditMethod;
  outcome: SignInAuditOutcome;
  /** Empty on success. */
  reason: string;
  user_id: number | null;
  attempt_id: number | null;
  request_id: string;
  subject_ref: string | null;
  claim_roles: string[] | null;
  claim_groups: string[] | null;
  client_network: string;
  user_agent_family: string;
  /**
   * 1, or for an api_key refusal the refusals of that reason, account and
   * network in that UTC minute; the row keeps the first refusal's request id
   * and time.
   */
  attempts: number;
}

export interface SignInAuditListParams {
  method?: SignInAuditMethod;
  outcome?: SignInAuditOutcome;
  reason?: SignInAuditReason;
  user_id?: number;
  /** RFC 3339, inclusive. */
  from?: string;
  /** RFC 3339, exclusive. */
  to?: string;
  offset: number;
  limit: number;
}

export interface SignInAuditListResult {
  rows: SignInAuditEntry[];
  total: number;
}

export const signInAuditApi = {
  // Newest first (created_at, then id, descending); the order is the API's.
  // Only the filters that are set are sent, with offset and limit.
  list: async (params: SignInAuditListParams): Promise<SignInAuditListResult> => {
    const query = Object.fromEntries(
      Object.entries(params).filter(([, value]) => value !== undefined && value !== ''),
    );
    const response = (await api.get<SignInAuditEntry[]>('/audit/sign-ins', {
      params: query,
    })) as ApiResponseWithMeta<SignInAuditEntry[]>;
    const rows = Array.isArray(response.data) ? response.data : [];
    return { rows, total: response.meta?.total ?? rows.length };
  },
};
