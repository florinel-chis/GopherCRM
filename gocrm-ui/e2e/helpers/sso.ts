/// <reference types="node" />
import { readFileSync } from 'node:fs';
import { expect, request, type APIRequestContext, type Page } from '@playwright/test';
import { API_BASE_URL, UI_BASE_URL } from './env';
import { testAdminCredentials } from '../fixtures/admin-user';

/**
 * Admin API helpers for sso.spec.ts. Everything goes through the HTTP API, so
 * the spec runs unchanged on MySQL, MariaDB and SQLite.
 *
 * `make e2e` (scripts/e2e/run.sh) starts the OpenID Connect test issuer
 * (scripts/e2e/oidc-issuer) and exports its origin, client id and per-run
 * client secret, plus the backend log the password-reset check reads.
 */
export const OIDC_ISSUER_URL = process.env.E2E_OIDC_ISSUER_URL ?? '';
export const OIDC_CLIENT_ID = process.env.E2E_OIDC_CLIENT_ID ?? '';
export const OIDC_CLIENT_SECRET = process.env.E2E_OIDC_CLIENT_SECRET ?? '';
export const BACKEND_LOG = process.env.E2E_BACKEND_LOG ?? '';
/** The one redirect URI the test issuer accepts: the SPA's callback page. */
export const OIDC_REDIRECT_URI = `${UI_BASE_URL}/auth/callback`;

export const SSO_FAILED_MESSAGE = 'Single sign-on failed. Try again or sign in with your password.';

export interface SSORoleMapping {
  claim: 'groups' | 'roles';
  value: string;
  role_id: number;
}

export interface SSOSettingsBody {
  enabled: boolean;
  allow_password: boolean;
  issuer_url: string;
  client_id: string;
  scopes: string[];
  role_mapping: SSORoleMapping[];
  client_secret?: string;
  confirm_issuer_change?: boolean;
}

export interface UserRow {
  id: number;
  email: string;
  role: string;
  role_id?: number;
  is_active: boolean;
}

/** A user-scoped API client: a fresh request context plus the bearer token. */
export class APIClient {
  private constructor(
    readonly context: APIRequestContext,
    readonly token: string,
  ) {}

  /** Signs in by password through the API (a fresh session every call). */
  static async login(email: string, password: string): Promise<APIClient> {
    const context = await request.newContext();
    const response = await context.post(`${API_BASE_URL}/auth/login`, {
      data: { email, password },
    });
    if (!response.ok()) {
      const text = await response.text();
      await context.dispose();
      throw new Error(`login of ${email} failed (${response.status()}): ${text}`);
    }
    const { data } = await response.json();
    return new APIClient(context, data.token as string);
  }

  /** The seeded break-glass admin (fixtures/admin-user.ts). */
  static admin(): Promise<APIClient> {
    return APIClient.login(testAdminCredentials.email, testAdminCredentials.password);
  }

  static async withToken(token: string): Promise<APIClient> {
    return new APIClient(await request.newContext(), token);
  }

  private get headers() {
    return { Authorization: `Bearer ${this.token}` };
  }

  get(path: string, params?: Record<string, string | number>) {
    return this.context.get(`${API_BASE_URL}${path}`, { headers: this.headers, params });
  }

  post(path: string, data?: unknown) {
    return this.context.post(`${API_BASE_URL}${path}`, { headers: this.headers, data });
  }

  put(path: string, data: unknown) {
    return this.context.put(`${API_BASE_URL}${path}`, { headers: this.headers, data });
  }

  delete(path: string) {
    return this.context.delete(`${API_BASE_URL}${path}`, { headers: this.headers });
  }

  dispose() {
    return this.context.dispose();
  }

  async putSSO(body: SSOSettingsBody): Promise<void> {
    const response = await this.put('/settings/sso', body);
    expect(response.status(), `PUT /settings/sso: ${await response.text()}`).toBe(200);
  }

  async getSSO(): Promise<SSOSettingsBody & { client_secret_set: boolean }> {
    const response = await this.get('/settings/sso');
    expect(response.status()).toBe(200);
    return (await response.json()).data;
  }

  /** Role id by system key (admin, sales, ...), read from the roles API. */
  async roleId(systemKey: string): Promise<{ id: number; name: string }> {
    const response = await this.get('/roles', { limit: 100 });
    expect(response.ok()).toBeTruthy();
    const rows: Array<{ id: number; name: string; system_key: string | null }> =
      (await response.json()).data ?? [];
    const role = rows.find((row) => row.system_key === systemKey);
    if (!role) throw new Error(`no system role ${systemKey}`);
    return { id: role.id, name: role.name };
  }

  /** Every live user whose email equals this one in any letter case. */
  async usersByEmail(email: string): Promise<UserRow[]> {
    const response = await this.get('/users', { search: email, limit: 100 });
    expect(response.ok()).toBeTruthy();
    const rows: UserRow[] = (await response.json()).data ?? [];
    return rows.filter((row) => row.email.toLowerCase() === email.toLowerCase());
  }

  async userById(id: number): Promise<UserRow> {
    const response = await this.get(`/users/${id}`);
    expect(response.ok(), `GET /users/${id}`).toBeTruthy();
    return (await response.json()).data;
  }

  async activeAdmins(): Promise<UserRow[]> {
    const response = await this.get('/users', { role: 'admin', limit: 100 });
    expect(response.ok()).toBeTruthy();
    const rows: UserRow[] = (await response.json()).data ?? [];
    return rows.filter((row) => row.is_active);
  }

  async createUser(email: string, password: string, role: string): Promise<UserRow> {
    const response = await this.post('/users', {
      email,
      password,
      first_name: 'E2E',
      last_name: 'Local',
      role,
    });
    expect(response.status(), `POST /users: ${await response.text()}`).toBe(201);
    return (await response.json()).data;
  }

  async setActive(id: number, active: boolean): Promise<void> {
    const response = await this.post(`/users/${id}/${active ? 'activate' : 'deactivate'}`);
    expect(
      response.ok(),
      `${active ? 'activate' : 'deactivate'} user ${id}: ${await response.text()}`,
    ).toBeTruthy();
  }

  async deleteUser(id: number): Promise<void> {
    const response = await this.delete(`/users/${id}`);
    expect([200, 204, 404], `delete user ${id}`).toContain(response.status());
  }
}

/** The settings that switch single sign-on off and forget the test issuer. */
export const SSO_OFF: SSOSettingsBody = {
  enabled: false,
  allow_password: false,
  issuer_url: '',
  client_id: '',
  scopes: [],
  role_mapping: [],
  client_secret: '',
  // Sign-ins of the suite link accounts under the test issuer, so clearing it
  // is an issuer change that needs the confirmation.
  confirm_issuer_change: true,
};

/** The public status the login page reads. */
export async function ssoEnabledStatus(): Promise<boolean> {
  const context = await request.newContext();
  try {
    const response = await context.get(`${API_BASE_URL}/auth/registration`);
    expect(response.ok()).toBeTruthy();
    return (await response.json()).data.sso_enabled === true;
  } finally {
    await context.dispose();
  }
}

/** The access token of the page's stored session (`gophercrm_session`). */
export async function sessionToken(page: Page): Promise<string | null> {
  return page.evaluate(() => {
    const raw =
      sessionStorage.getItem('gophercrm_session') ?? localStorage.getItem('gophercrm_session');
    if (!raw) return null;
    try {
      const record = JSON.parse(raw) as { access?: unknown };
      return typeof record.access === 'string' ? record.access : null;
    } catch {
      return null;
    }
  });
}

/**
 * Lines of the backend log that record a password-reset mail to this address
 * (the log mailer: `to` plus the redacted link). Lines are counted only when
 * they carry both the message and the address, because other specs in the
 * same run may request resets for other accounts.
 */
export function passwordResetMailLines(email: string): number {
  if (!BACKEND_LOG) throw new Error('E2E_BACKEND_LOG is not set (run through make e2e)');
  return readFileSync(BACKEND_LOG, 'utf8')
    .split('\n')
    .filter(
      (line) =>
        line.includes('password reset email logged instead of sent') && line.includes(email),
    ).length;
}

/** A unique, lower-case email and subject for one identity of the spec. */
export function uniqueIdentity(label: string): { subject: string; email: string } {
  const suffix = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;
  return { subject: `e2e-sso-${label}-${suffix}`, email: `e2e-sso-${label}-${suffix}@example.com` };
}
