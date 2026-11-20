import { api } from '../client';

// The SCIM provider tokens (#468, screen #490): GET, POST and DELETE under
// /settings/scim/tokens, guarded by settings.scim_manage.

/** One stored token: neither the token nor its hash is ever returned again. */
export interface SCIMToken {
  id: number;
  name: string;
  created_at: string;
  /** The last successful SCIM authentication (best-effort); null when never used. */
  last_used_at: string | null;
  /** Null while the token is usable. */
  revoked_at: string | null;
}

/** The create answer: `token` is shown in this answer only. */
export interface CreatedSCIMToken {
  token: string;
  scim_token: SCIMToken;
}

export const scimTokensApi = {
  // Every token, revoked ones included, newest first (the API's order).
  list: async (): Promise<SCIMToken[]> => {
    const response = await api.get<SCIMToken[]>('/settings/scim/tokens');
    return Array.isArray(response.data) ? response.data : [];
  },

  create: async (name: string): Promise<CreatedSCIMToken> => {
    const response = await api.post<CreatedSCIMToken>('/settings/scim/tokens', { name });
    return response.data;
  },

  // Stamps revoked_at; the row is kept.
  revoke: async (id: number): Promise<SCIMToken> => {
    const response = await api.delete<SCIMToken>(`/settings/scim/tokens/${id}`);
    return response.data;
  },
};
