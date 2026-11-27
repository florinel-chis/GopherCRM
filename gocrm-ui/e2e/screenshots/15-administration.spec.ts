import { test, expect, request as playwrightRequest, type APIRequestContext, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Documentation captures for the administration screens under Settings
 * (docs/reference/features.md sections 7b, 9b, 9c, 9d and 9e): roles and the role editor,
 * webhooks and their delivery log, single sign-on, the sign-in and role
 * audits, and SCIM tokens.
 *
 * Every record is seeded through the API with a fixed name and only when
 * missing; nothing is deleted, saved, revoked or sent from the screens.
 *   - Webhook endpoints live under the reserved `.invalid` top-level domain,
 *     which never resolves, so no delivery leaves the machine. They are
 *     created through the API, so the one-time signing-secret dialog never
 *     opens; the secret in the API answer is discarded. "Send test" is never
 *     clicked.
 *   - A lead with a fixed email gives the delivery log its rows.
 *   - A sales user with a fixed email gives the role audit a row when it
 *     would otherwise be empty.
 *   - The SCIM token is created through the API, so the one-time token dialog
 *     never opens; the token in the API answer is discarded.
 *   - The role editor and the SSO screen are captured as loaded, not saved.
 */
test.describe.configure({ mode: 'serial' });

const AREA = 'administration';
const LOADED = { timeout: 30 * 1000 };

const CRM_SYNC_URL = 'https://crm-sync.invalid/hooks/leads';
const REPORTING_URL = 'https://reporting.invalid/hooks/deals';
const WEBHOOK_LEAD_EMAIL = 'ioana.dumitru@northwind-bakery.example';
const AUDIT_USER_EMAIL = 'radu.stan@gophercrm.example';
// A demo account of the disposable capture database, never used to sign in.
const AUDIT_USER_PASSWORD = 'Demo!Sales2026x';
const SCIM_TOKEN_NAME = 'Entra ID provisioning';

let salesRoleId = 0;

async function apiLogin(api: APIRequestContext): Promise<Record<string, string>> {
  const login = await api.post(`${API_BASE_URL}/auth/login`, {
    data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
  });
  expect(login.status()).toBe(200);
  return { Authorization: `Bearer ${(await login.json()).data.token}` };
}

/** A list answer's rows: a bare array, or the one array inside a wrapper such as `{ leads: [...] }`. */
function rowsOf<T>(data: unknown): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === 'object') {
    const inner = Object.values(data).find(Array.isArray);
    if (inner) return inner as T[];
  }
  return [];
}

async function list<T>(api: APIRequestContext, headers: Record<string, string>, path: string, params = {}) {
  const response = await api.get(`${API_BASE_URL}${path}`, { headers, params });
  expect(response.status(), `GET ${path}`).toBe(200);
  return rowsOf<T>((await response.json()).data);
}

async function post(api: APIRequestContext, headers: Record<string, string>, path: string, data: unknown) {
  const response = await api.post(`${API_BASE_URL}${path}`, { headers, data });
  expect(response.status(), `POST ${path}`).toBeLessThan(300);
  return (await response.json()).data;
}

async function seedData(): Promise<void> {
  const api = await playwrightRequest.newContext();
  try {
    const headers = await apiLogin(api);
    const me = await api.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const adminId: number = (await me.json()).data.id;

    const roles = await list<{ id: number; system_key?: string }>(api, headers, '/roles', { limit: 100 });
    const sales = roles.find((role) => role.system_key === 'sales');
    expect(sales, 'the built-in sales role').toBeDefined();
    salesRoleId = (sales as { id: number }).id;

    // Webhook endpoints: one active, one inactive. The create answer carries
    // the signing secret once; it is not read or kept.
    const endpoints = await list<{ id: number; url: string }>(api, headers, '/webhooks/endpoints', { limit: 100 });
    let crmSync = endpoints.find((row) => row.url === CRM_SYNC_URL);
    if (!crmSync) {
      crmSync = await post(api, headers, '/webhooks/endpoints', {
        url: CRM_SYNC_URL,
        event_types: ['lead.created', 'lead.updated'],
        is_active: true,
      });
    }
    if (!endpoints.some((row) => row.url === REPORTING_URL)) {
      await post(api, headers, '/webhooks/endpoints', {
        url: REPORTING_URL,
        event_types: ['deal.created', 'deal.updated'],
        is_active: false,
      });
    }

    // Delivery rows: a lead created (and, on a later run with an empty log,
    // updated) while the active endpoint listens.
    const deliveries = await list(api, headers, '/webhooks/deliveries', {
      endpoint_id: (crmSync as { id: number }).id,
    });
    if (deliveries.length === 0) {
      const leads = await list<{ id: number; email: string }>(api, headers, '/leads', {
        search: WEBHOOK_LEAD_EMAIL,
        limit: 100,
      });
      const lead = leads.find((row) => row.email === WEBHOOK_LEAD_EMAIL);
      if (!lead) {
        const created = await post(api, headers, '/leads', {
          first_name: 'Ioana',
          last_name: 'Dumitru',
          email: WEBHOOK_LEAD_EMAIL,
          company: 'Northwind Bakery',
          status: 'new',
          source: 'Website',
          owner_id: adminId,
        });
        const updated = await api.put(`${API_BASE_URL}/leads/${created.id}`, {
          headers,
          data: { status: 'contacted' },
        });
        expect(updated.status(), 'PUT lead').toBe(200);
      } else {
        const updated = await api.put(`${API_BASE_URL}/leads/${lead.id}`, {
          headers,
          data: { position: `Purchasing lead ${Date.now() % 1000}` },
        });
        expect(updated.status(), 'PUT lead').toBe(200);
      }
      await expect
        .poll(async () => (await list(api, headers, '/webhooks/deliveries', { endpoint_id: (crmSync as { id: number }).id })).length, LOADED)
        .toBeGreaterThan(0);
    }

    // The role audit: a user's creation is one row.
    const audit = await list(api, headers, '/roles/audit', { limit: 1 });
    if (audit.length === 0) {
      const users = await list<{ email: string }>(api, headers, '/users', { limit: 100 });
      if (!users.some((user) => user.email === AUDIT_USER_EMAIL)) {
        await post(api, headers, '/users', {
          email: AUDIT_USER_EMAIL,
          password: AUDIT_USER_PASSWORD,
          first_name: 'Radu',
          last_name: 'Stan',
          role: 'sales',
        });
      }
    }

    // A SCIM token; the create answer carries the token once and is not read.
    const tokens = await list<{ name: string; revoked_at: string | null }>(api, headers, '/settings/scim/tokens');
    if (!tokens.some((token) => token.name === SCIM_TOKEN_NAME && token.revoked_at === null)) {
      const created = await api.post(`${API_BASE_URL}/settings/scim/tokens`, {
        headers,
        data: { name: SCIM_TOKEN_NAME },
      });
      expect(created.status(), 'POST /settings/scim/tokens').toBe(201);
    }
  } finally {
    await api.dispose();
  }
}

/** Only the permanent drawer is visible at desktop widths; the temporary one is display:none. */
function visibleNavButton(page: Page, label: string) {
  return page.locator('nav [role="button"]:visible', { hasText: label });
}

/** Opens the collapsed "Settings" group in the sidebar so captures show the section context. */
async function expandSettingsNav(page: Page): Promise<void> {
  const rolesItem = visibleNavButton(page, 'Roles');
  if (await rolesItem.count() === 0) {
    await visibleNavButton(page, 'Settings').click();
  }
  await expect(rolesItem.first()).toBeVisible();
}

test.describe('Screenshots - Administration', () => {
  test.beforeAll(async () => {
    test.setTimeout(120 * 1000);
    await seedData();
  });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(90 * 1000);
    await ensureAdminLoggedIn(page);
  });

  test('roles list', async ({ page }) => {
    await page.goto('/settings/roles');
    await expect(page.getByRole('heading', { name: 'Roles', level: 4 })).toBeVisible(LOADED);
    await expect(page.getByRole('button', { name: 'Clone Sales', exact: true })).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '01-roles', { fullPage: true });
  });

  test('role editor of a built-in role', async ({ page }) => {
    await page.goto(`/settings/roles/${salesRoleId}`);
    await expect(page.getByRole('heading', { name: 'Role', level: 4, exact: true })).toBeVisible(LOADED);
    await expect(page.getByText('A system role cannot be changed.', { exact: false })).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '02-role-editor', { fullPage: true });
  });

  test('webhook endpoints', async ({ page }) => {
    await page.goto('/settings/webhooks');
    await expect(page.getByRole('heading', { name: 'Webhooks', level: 4 })).toBeVisible(LOADED);
    const table = page.getByRole('table', { name: 'Webhook endpoints' });
    await expect(table.getByRole('row').filter({ hasText: CRM_SYNC_URL })).toBeVisible(LOADED);
    await expect(table.getByRole('row').filter({ hasText: REPORTING_URL })).toBeVisible(LOADED);
    await expect(page.getByRole('dialog', { name: 'Signing secret' })).toHaveCount(0);
    await expandSettingsNav(page);

    await capture(page, AREA, '03-webhooks');
  });

  test('webhook delivery log', async ({ page }) => {
    await page.goto('/settings/webhooks');
    await expect(page.getByRole('heading', { name: 'Webhooks', level: 4 })).toBeVisible(LOADED);
    await page.getByRole('tab', { name: 'Delivery log' }).click();
    await expect(page.getByRole('table', { name: 'Webhook deliveries' })).toBeVisible(LOADED);
    await expect(page.getByTestId('webhook-delivery-row').first()).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '04-webhook-deliveries');
  });

  test('single sign-on settings', async ({ page }) => {
    await page.goto('/settings/sso');
    await expect(page.getByRole('heading', { name: 'Single sign-on', level: 1 })).toBeVisible(LOADED);
    await expect(page.getByLabel('Issuer URL', { exact: true })).toBeVisible(LOADED);
    await expandSettingsNav(page);

    // The default state, as loaded; nothing is typed or saved.
    await capture(page, AREA, '05-sso', { fullPage: true });
  });

  test('sign-in audit', async ({ page }) => {
    await page.goto('/settings/sign-in-audit');
    await expect(page.getByRole('heading', { name: 'Sign-in audit', level: 1 })).toBeVisible(LOADED);
    await expect(page.getByTestId('sign-in-audit-row').first()).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '06-sign-in-audit');
  });

  test('role audit', async ({ page }) => {
    await page.goto('/settings/role-audit');
    await expect(page.getByRole('heading', { name: 'Role audit', level: 1 })).toBeVisible(LOADED);
    await expect(page.getByTestId('role-audit-row').first()).toBeVisible(LOADED);
    await expandSettingsNav(page);

    await capture(page, AREA, '07-role-audit');
  });

  test('scim tokens', async ({ page }) => {
    await page.goto('/settings/scim-tokens');
    await expect(page.getByRole('heading', { name: 'SCIM tokens', level: 4 })).toBeVisible(LOADED);
    await expect(page.getByTestId('scim-token-row').filter({ hasText: SCIM_TOKEN_NAME }).first()).toBeVisible(LOADED);
    // The list only: no token dialog is open.
    await expect(page.getByRole('dialog', { name: 'Your new SCIM token' })).toHaveCount(0);
    await expandSettingsNav(page);

    await capture(page, AREA, '08-scim-tokens');
  });
});
