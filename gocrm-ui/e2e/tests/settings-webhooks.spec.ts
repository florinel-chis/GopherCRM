import {
  test,
  expect,
  request as playwrightRequest,
  type APIRequestContext,
  type Page,
} from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';
import { apiResponse, LOADED } from '../helpers/api';

/**
 * Settings > Webhooks, end to end: an admin registers an endpoint, sees its
 * signing secret once, edits and deletes it, rotates a signing secret, sends a
 * test ping, and reads the delivery log with its filters.
 *
 * Every endpoint but one lives under the reserved `.invalid` top-level domain,
 * which never resolves, so no delivery leaves the machine: the rows stay
 * `pending` (briefly `sending`) and are retried against nothing. The ping
 * endpoint is `https://localhost/...`, registered inactive (so it gets no
 * delivery rows): the create check judges only IP literals, and the egress
 * guard of the ping refuses the loopback address when it dials, so the ping
 * fails with destination_refused and nothing is sent.
 *
 * Safe to retry against the same database: the endpoints of an earlier
 * attempt are deleted first, through the API, and again afterwards. Deleting
 * an endpoint removes its delivery rows, so the log starts empty as long as
 * no other endpoint is registered (no other spec registers one).
 */
test.describe.configure({ mode: 'serial' });

const HOST = 'e2e-hooks.invalid';
const URL_PREFIX = `https://${HOST}/`;
const LIFECYCLE_URL = `${URL_PREFIX}a`;
const LOG_URL_A = `${URL_PREFIX}log-a`;
const LOG_URL_B = `${URL_PREFIX}log-b`;
const ROTATE_URL = `${URL_PREFIX}rotate`;
const PING_URL = 'https://localhost/e2e-hooks-ping';

interface EndpointRow {
  id: number;
  url: string;
  event_types: string[];
  is_active: boolean;
}

const ENDPOINTS = /\/webhooks\/endpoints$/;
const ENDPOINT = /\/webhooks\/endpoints\/\d+$/;
const DELIVERIES = /\/webhooks\/deliveries$/;
const ROTATE = /\/webhooks\/endpoints\/\d+\/rotate-secret$/;
const PING = /\/webhooks\/endpoints\/\d+\/test$/;

// The first assertion after a login, a navigation or a request that loads
// data: the default 5 s is too short on a loaded machine.

test.describe('Admin - Settings > Webhooks', () => {
  let api: APIRequestContext;
  let headers: Record<string, string>;
  let adminId = 0;
  let leadId = 0;

  const ownEndpoints = async (): Promise<EndpointRow[]> => {
    const listed = await api.get(`${API_BASE_URL}/webhooks/endpoints`, {
      headers,
      params: { limit: 100 },
    });
    expect(listed.status()).toBe(200);
    const rows: EndpointRow[] = (await listed.json()).data ?? [];
    return rows.filter((row) => row.url.includes(`//${HOST}/`) || row.url === PING_URL);
  };

  const removeOwnEndpoints = async () => {
    for (const row of await ownEndpoints()) {
      const removed = await api.delete(`${API_BASE_URL}/webhooks/endpoints/${row.id}`, { headers });
      expect(removed.status(), `delete endpoint ${row.id}`).toBe(204);
    }
  };

  const registerEndpoint = async (
    url: string,
    eventTypes: string[],
    isActive = true,
  ): Promise<EndpointRow & { secret: string }> => {
    const created = await api.post(`${API_BASE_URL}/webhooks/endpoints`, {
      headers,
      data: { url, event_types: eventTypes, is_active: isActive },
    });
    expect(created.status()).toBe(201);
    return (await created.json()).data;
  };

  // No signing secret in the page text, a field or either storage.
  const expectNoSecret = async (page: Page) => {
    await expect(page.locator('body')).not.toContainText('whsec_');
    const kept = await page.evaluate(() =>
      JSON.stringify({
        html: document.documentElement.outerHTML.includes('whsec_'),
        fields: Array.from(document.querySelectorAll('input, textarea')).some((field) =>
          (field as HTMLInputElement).value.includes('whsec_'),
        ),
        local: JSON.stringify({ ...localStorage }).includes('whsec_'),
        session: JSON.stringify({ ...sessionStorage }).includes('whsec_'),
      }),
    );
    expect(JSON.parse(kept)).toEqual({ html: false, fields: false, local: false, session: false });
  };

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    const me = await api.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    adminId = (await me.json()).data.id;
    await removeOwnEndpoints();
  });

  test.afterAll(async () => {
    await removeOwnEndpoints();
    if (leadId) {
      const removed = await api.delete(`${API_BASE_URL}/leads/${leadId}`, { headers });
      expect(removed.status(), 'lead cleanup').toBeLessThan(300);
    }
    await api.dispose();
  });

  test.beforeEach(async ({ page }) => {
    await new AdminAuthHelper(page).ensureAdminLoggedIn();
  });

  test('admin registers an endpoint, sees the secret once, edits and deletes it', async ({ page }) => {
    test.setTimeout(90 * 1000);
    await removeOwnEndpoints();

    const row = page.getByRole('row').filter({ hasText: LIFECYCLE_URL });

    // 1. Through the navigation: Settings > Webhooks.
    await page.goto('/');
    const navigation = page.getByTestId('app-navigation');
    await expect(navigation.getByText('Settings', { exact: true })).toBeVisible(LOADED);
    await navigation.getByText('Settings', { exact: true }).click();
    await navigation.getByText('Webhooks', { exact: true }).click();
    await expect(page).toHaveURL(/\/settings\/webhooks$/, LOADED);
    await expect(page.getByRole('heading', { name: 'Webhooks', level: 4 })).toBeVisible();
    await expect(page.getByRole('table', { name: 'Webhook endpoints' })).toBeVisible(LOADED);

    // 2. A URL that is valid in everything but the scheme: the server refuses
    //    it, the form shows the refusal and stays open, nothing is created.
    await page.getByRole('button', { name: 'Add endpoint' }).click();
    const form = page.getByRole('dialog', { name: 'Add webhook endpoint' });
    await expect(form).toBeVisible();
    await form.getByRole('textbox', { name: 'URL' }).fill(`http://${HOST}/a`);
    await form.getByRole('checkbox', { name: 'lead.created' }).check();
    const refused = apiResponse(page, ENDPOINTS, 'POST');
    await form.getByRole('button', { name: 'Create' }).click();
    expect((await refused).status()).toBe(400);
    await expect(form.getByRole('alert')).toContainText('Validation failed', LOADED);
    await expect(form.getByText(/must be an https URL/)).toBeVisible();
    await expect(form.getByRole('textbox', { name: 'URL' })).toHaveValue(`http://${HOST}/a`);
    expect(await ownEndpoints()).toHaveLength(0);

    // 3. The same form with https: created, and the secret dialog opens.
    await form.getByRole('textbox', { name: 'URL' }).fill(LIFECYCLE_URL);
    const created = apiResponse(page, ENDPOINTS, 'POST');
    await form.getByRole('button', { name: 'Create' }).click();
    expect((await created).status()).toBe(201);

    const secretDialog = page.getByRole('dialog', { name: 'Signing secret' });
    await expect(secretDialog).toBeVisible(LOADED);
    await expect(secretDialog.getByTestId('webhook-secret')).toHaveValue(/^whsec_.{20,}$/);
    await expect(secretDialog.getByRole('button', { name: 'Copy signing secret' })).toBeVisible();
    await expect(secretDialog.getByText(/cannot be shown again/i)).toBeVisible();
    await secretDialog.getByRole('button', { name: 'Done' }).click();
    await expect(secretDialog).toBeHidden();

    // 4. After a reload the endpoint is listed and the secret is nowhere: not
    //    in the page text, not in a field, not in either storage.
    await expectNoSecret(page);
    await page.reload();
    await expect(row).toBeVisible(LOADED);
    await expect(row).toContainText('lead.created');
    await expect(row).not.toContainText('lead.updated');
    await expect(row.getByText('Active', { exact: true })).toBeVisible();
    await expectNoSecret(page);

    // 5. Edit: one more event and inactive; both survive a reload.
    await row.getByRole('button', { name: `Edit ${LIFECYCLE_URL}` }).click();
    const editForm = page.getByRole('dialog', { name: 'Edit webhook endpoint' });
    await expect(editForm.getByRole('textbox', { name: 'URL' })).toHaveValue(LIFECYCLE_URL);
    await expect(editForm.getByRole('checkbox', { name: 'lead.created' })).toBeChecked();
    await editForm.getByRole('checkbox', { name: 'lead.updated' }).check();
    await editForm.getByRole('switch', { name: 'Active' }).uncheck();
    const updated = apiResponse(page, ENDPOINT, 'PUT');
    await editForm.getByRole('button', { name: 'Save' }).click();
    expect((await updated).status()).toBe(200);
    await expect(editForm).toBeHidden();

    await page.reload();
    await expect(row).toBeVisible(LOADED);
    await expect(row).toContainText('lead.created');
    await expect(row).toContainText('lead.updated');
    await expect(row.getByText('Inactive', { exact: true })).toBeVisible();
    await expectNoSecret(page);
    const stored = await ownEndpoints();
    expect(stored).toHaveLength(1);
    expect(stored[0]).toMatchObject({
      url: LIFECYCLE_URL,
      event_types: ['lead.created', 'lead.updated'],
      is_active: false,
    });

    // 6. Delete: the confirmation names the delivery log; gone after a reload.
    await row.getByRole('button', { name: `Delete ${LIFECYCLE_URL}` }).click();
    const confirm = page.getByRole('dialog', { name: 'Delete webhook endpoint' });
    await expect(confirm).toContainText('Its delivery log is removed with it');
    const deleted = apiResponse(page, ENDPOINT, 'DELETE');
    await confirm.getByRole('button', { name: 'Delete' }).click();
    expect((await deleted).status()).toBe(204);

    await page.reload();
    await expect(page.getByRole('table', { name: 'Webhook endpoints' })).toBeVisible(LOADED);
    await expect(page.getByText('No webhook endpoints yet.')).toBeVisible(LOADED);
    await expect(row).toHaveCount(0);
    expect(await ownEndpoints()).toHaveLength(0);
  });

  test('the delivery log lists the events newest first and narrows by endpoint, event type and status', async ({
    page,
  }) => {
    test.setTimeout(90 * 1000);
    await removeOwnEndpoints();

    // Both endpoints exist before the lead does. A hears lead.created only,
    // B both events: the create makes two rows, the update one.
    const endpointA = await registerEndpoint(LOG_URL_A, ['lead.created']);
    await registerEndpoint(LOG_URL_B, ['lead.created', 'lead.updated']);

    const stamp = `${Date.now()}`;
    const lead = await api.post(`${API_BASE_URL}/leads`, {
      headers,
      data: {
        first_name: 'Webhook',
        last_name: `E2E ${stamp}`,
        email: `webhook.e2e.${stamp}@example.com`,
        owner_id: adminId,
        status: 'new',
      },
    });
    expect(lead.status()).toBe(201);
    leadId = (await lead.json()).data.id;
    // A stored value really changes: an unchanged update emits no event.
    const renamed = await api.put(`${API_BASE_URL}/leads/${leadId}`, {
      headers,
      data: { first_name: 'Webhook renamed' },
    });
    expect(renamed.status()).toBe(200);

    const rows = page.getByTestId('webhook-delivery-row');
    const choose = async (selectId: string, option: string | RegExp) => {
      await page.locator(`#${selectId}`).click();
      await page.getByRole('option', { name: option }).click();
    };
    // Picks a filter value and returns the query of the request it caused.
    const pick = async (selectId: string, option: string | RegExp, param: string) => {
      const listed = page.waitForResponse((response) => {
        const url = new URL(response.url());
        return DELIVERIES.test(url.pathname) && response.status() === 200 && url.searchParams.has(param);
      });
      await choose(selectId, option);
      return new URL((await listed).url()).searchParams;
    };

    await page.goto('/settings/webhooks');
    await page.getByRole('tab', { name: 'Delivery log' }).click();
    const table = page.getByRole('table', { name: 'Webhook deliveries' });
    await expect(table).toBeVisible(LOADED);
    await expect(table.getByRole('columnheader')).toHaveText([
      'Event type',
      'Record ID',
      'Status',
      'Attempts',
      'Last attempt',
      'Last response code',
      'Actions',
    ]);

    // Three rows, the update (the newest) first; every row is of this lead.
    await expect(rows).toHaveCount(3, LOADED);
    await expect(rows.nth(0).getByRole('cell').nth(0)).toHaveText('lead.updated');
    await expect(rows.nth(1).getByRole('cell').nth(0)).toHaveText('lead.created');
    await expect(rows.nth(2).getByRole('cell').nth(0)).toHaveText('lead.created');
    for (const index of [0, 1, 2]) {
      await expect(rows.nth(index).getByRole('cell').nth(1)).toHaveText(String(leadId));
    }

    // Endpoint A: its one lead.created row.
    let sent = await pick('webhook-delivery-endpoint', new RegExp(`${LOG_URL_A}$`), 'endpoint_id');
    expect(sent.get('endpoint_id')).toBe(String(endpointA.id));
    await expect(rows).toHaveCount(1, LOADED);
    await expect(rows.nth(0).getByRole('cell').nth(0)).toHaveText('lead.created');

    // Event type lead.updated over every endpoint: the one row of B.
    await choose('webhook-delivery-endpoint', 'All endpoints');
    await expect(rows).toHaveCount(3, LOADED);
    sent = await pick('webhook-delivery-event', 'lead.updated', 'event_type');
    expect(sent.get('event_type')).toBe('lead.updated');
    expect(sent.has('endpoint_id')).toBe(false);
    await expect(rows).toHaveCount(1, LOADED);
    await expect(rows.nth(0).getByRole('cell').nth(0)).toHaveText('lead.updated');

    // Status succeeded: nothing can succeed against a host that never
    // resolves, so the log is empty.
    await choose('webhook-delivery-event', 'All event types');
    await expect(rows).toHaveCount(3, LOADED);
    sent = await pick('webhook-delivery-status', 'succeeded', 'status');
    expect(sent.get('status')).toBe('succeeded');
    expect(sent.has('event_type')).toBe(false);
    await expect(page.getByText('No deliveries match these filters.')).toBeVisible(LOADED);
    await expect(rows).toHaveCount(0);
  });

  test('admin rotates a signing secret and sees the new one once', async ({ page }) => {
    test.setTimeout(90 * 1000);
    await removeOwnEndpoints();
    const endpoint = await registerEndpoint(ROTATE_URL, ['lead.created']);
    expect(endpoint.secret).toMatch(/^whsec_.{20,}$/);

    const rotatePosts: string[] = [];
    page.on('request', (request) => {
      if (request.method() === 'POST' && ROTATE.test(new URL(request.url()).pathname)) {
        rotatePosts.push(request.url());
      }
    });

    await page.goto('/settings/webhooks');
    const row = page.getByRole('row').filter({ hasText: ROTATE_URL });
    await expect(row).toBeVisible(LOADED);

    // 1. Cancel: the confirmation names the 24-hour window and sends nothing.
    await row.getByRole('button', { name: `Rotate secret of ${ROTATE_URL}` }).click();
    const confirm = page.getByRole('dialog', { name: 'Rotate signing secret' });
    await expect(confirm).toContainText('24 hours');
    await expect(confirm).toContainText(ROTATE_URL);
    await confirm.getByRole('button', { name: 'Cancel' }).click();
    await expect(confirm).toBeHidden();
    expect(rotatePosts).toHaveLength(0);

    // 2. Rotate: a new secret, shown once, with the end of the previous one.
    await row.getByRole('button', { name: `Rotate secret of ${ROTATE_URL}` }).click();
    const rotated = apiResponse(page, ROTATE, 'POST');
    await confirm.getByRole('button', { name: 'Rotate' }).click();
    expect((await rotated).status()).toBe(200);
    expect(rotatePosts).toHaveLength(1);

    const dialog = page.getByRole('dialog', { name: 'New signing secret' });
    await expect(dialog).toBeVisible(LOADED);
    const field = dialog.getByTestId('webhook-rotated-secret');
    await expect(field).toHaveValue(/^whsec_.{20,}$/);
    expect(await field.inputValue()).not.toBe(endpoint.secret);
    await expect(dialog.getByRole('button', { name: 'Copy signing secret' })).toBeVisible();
    await expect(dialog.getByText(/cannot be shown again/i)).toBeVisible();
    await expect(dialog.getByText(/The previous secret keeps verifying until .+\./)).toBeVisible();

    // 3. Done: the secret is nowhere, also after a reload.
    await dialog.getByRole('button', { name: 'Done' }).click();
    await expect(dialog).toBeHidden();
    await expectNoSecret(page);
    await page.reload();
    await expect(row).toBeVisible(LOADED);
    await expectNoSecret(page);

    // A later test never inherits an active subscriber.
    await removeOwnEndpoints();
  });

  test('admin sends a test ping that the egress guard refuses', async ({ page }) => {
    test.setTimeout(90 * 1000);
    await removeOwnEndpoints();
    // Inactive: no event is ever enqueued for it, so the log stays unchanged.
    await registerEndpoint(PING_URL, ['lead.created'], false);

    await page.goto('/settings/webhooks');
    const row = page.getByRole('row').filter({ hasText: PING_URL });
    await expect(row).toBeVisible(LOADED);

    const pinged = apiResponse(page, PING, 'POST');
    await row.getByRole('button', { name: `Send test ping to ${PING_URL}` }).click();
    const response = await pinged;
    expect(response.status()).toBe(200);
    expect((await response.json()).data).toMatchObject({
      outcome: 'failed',
      error_code: 'destination_refused',
      http_status: null,
    });
    await expect(page.getByText('Test ping failed: destination_refused (no response)')).toBeVisible(
      LOADED,
    );

    await removeOwnEndpoints();
  });
});
