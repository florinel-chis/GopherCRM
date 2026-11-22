import { api } from '../client';

// Single sign-on (OpenID Connect) settings, behind settings.sso_manage. The
// client secret, the client certificate and its private key are write-only:
// the API reports only whether one is set (and the certificate's thumbprint
// and expiry).

export type SSOClaim = 'groups' | 'roles';

export type SSOSettingSource = 'settings' | 'env' | 'default';

// One row of the role mapping. Rows are evaluated in order; the first row
// whose claim carries the value decides the role.
export interface SSORoleMapping {
  claim: SSOClaim;
  value: string;
  role_id: number;
}

export interface SSOSettingsSources {
  enabled: SSOSettingSource;
  allow_password: SSOSettingSource;
  issuer_url: SSOSettingSource;
  client_id: SSOSettingSource;
  client_secret: SSOSettingSource;
  scopes: SSOSettingSource;
  role_mapping: SSOSettingSource;
  // The certificate has no environment variable: settings or default.
  client_certificate: SSOSettingSource;
}

// allow_password: whether accounts linked to single sign-on may also use
// password sign-in and password reset (default false).
export interface SSOSettings {
  enabled: boolean;
  allow_password: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string[];
  role_mapping: SSORoleMapping[];
  client_secret_set: boolean;
  // While a client certificate is set, the code exchange authenticates with a
  // signed client assertion instead of the client secret. The thumbprint is
  // the uppercase hex SHA-1; expires_at is RFC 3339. Both are "" when unset.
  client_certificate_set: boolean;
  client_certificate_thumbprint: string;
  client_certificate_expires_at: string;
  sources: SSOSettingsSources;
}

// PUT /settings/sso is a full replacement. client_secret absent keeps the
// stored secret, "" clears it, any other value replaces it.
// client_certificate and client_private_key (PEM) go together: both absent
// keep the stored pair, both "" clear it, both set replace it.
// confirm_issuer_change true confirms a save that changes the issuer in force
// while accounts are linked to the current one; without it such a save is
// answered 409 ISSUER_CHANGE_UNCONFIRMED with details.affected_links.
export interface SSOSettingsUpdate {
  enabled: boolean;
  allow_password: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string[];
  role_mapping: SSORoleMapping[];
  client_secret?: string;
  client_certificate?: string;
  client_private_key?: string;
  confirm_issuer_change?: boolean;
}

export const ssoSettingsApi = {
  get: async (): Promise<SSOSettings> => {
    const response = await api.get<SSOSettings>('/settings/sso');
    return response.data;
  },

  update: async (body: SSOSettingsUpdate): Promise<SSOSettings> => {
    const response = await api.put<SSOSettings>('/settings/sso', body);
    return response.data;
  },
};
