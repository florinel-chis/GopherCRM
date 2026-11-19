import { api } from '../client';

// Single sign-on (OpenID Connect) settings, behind settings.sso_manage. The
// client secret is write-only: the API reports only whether one is set.

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
  sources: SSOSettingsSources;
}

// PUT /settings/sso is a full replacement. client_secret absent keeps the
// stored secret, "" clears it, any other value replaces it.
export interface SSOSettingsUpdate {
  enabled: boolean;
  allow_password: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string[];
  role_mapping: SSORoleMapping[];
  client_secret?: string;
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
