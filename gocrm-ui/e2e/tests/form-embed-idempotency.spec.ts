import {
  test,
  expect,
  request as playwrightRequest,
  type APIRequestContext,
  type Page,
  type Route,
} from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * The embed script's idempotency key (#341), executed for real: the hosted
 * view (GET /forms/public/{key}/view) loads the served embed.js, which renders
 * the form against the real API. Each test intercepts the submission POSTs only
 * to read their bodies and to fail one of them, so the visitor resends from the
 * same render.
 *
 * The form is a published create_ticket form (double opt-in), so every
 * submission is stored pending with its idempotency_key. One ticket per key
 * after confirmation is proven by TestHostedFormResubmissionWithSameKeyOpensOneTicket
 * (internal/handler): this run uses the log-only mailer and cannot read the
 * confirmation link.
 */

// The service files a submission younger than three seconds as spam (and a
// spam row stores no key).
const TIME_TRAP_MS = 3500;
const GENERIC_ERROR = 'Something went wrong. Please try again.';
const DETERMINISTIC_KEY = '000102030405060708090a0b0c0d0e0f';
const HEX_KEY = /^[0-9a-f]{32}$/;

type SubmitBody = Record<string, unknown> & { idempotency_key?: string };

interface StoredSubmission {
  email: string;
  status: string;
  idempotency_key: string;
}

test.describe('Public forms - embed script idempotency key', () => {
  let api: APIRequestContext;
  let headers: Record<string, string>;
  let formId = 0;
  let publicId = '';

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status(), 'admin login').toBe(200);
    headers = { Authorization: `Bearer ${(await login.json()).data.token}` };

    const created = await api.post(`${API_BASE_URL}/forms`, {
      headers,
      data: {
        name: `E2E embed key ${Date.now()}`,
        status: 'published',
        submit_action: 'message',
        create_lead: false,
        create_ticket: true,
        double_opt_in: true,
        fields: [{ name: 'email', label: 'Email', type: 'email', required: true }],
      },
    });
    expect(created.status(), 'POST /forms').toBe(201);
    const form = (await created.json()).data;
    expect(form.create_ticket).toBe(true);
    formId = form.id;
    publicId = form.public_id;
  });

  test.afterAll(async () => {
    if (formId) {
      const deleted = await api.delete(`${API_BASE_URL}/forms/${formId}`, { headers });
      expect(deleted.status(), 'form cleanup').toBeLessThan(300);
    }
    await api.dispose();
  });

  const viewURL = () => `${API_BASE_URL}/forms/public/${publicId}/view`;
  const submissionsPattern = () => `**/forms/public/${publicId}/submissions`;

  /** Serves the hosted view without its data-idempotency-key, optionally with
   * the embed tag repeated, so the script must find its own key. */
  async function stripHostedKey(page: Page, copies = 1) {
    await page.route(viewURL(), async (route) => {
      const response = await route.fetch();
      let html = (await response.text()).replace(/ data-idempotency-key="[^"]*"/g, '');
      const tag = html.match(/<script src="[^"]*embed\.js"[^>]*><\/script>/);
      expect(tag, 'the hosted view carries the embed tag').not.toBeNull();
      if (!tag) throw new Error('the hosted view carries no embed tag');
      const tagHtml = tag[0];
      html = html.replace(tagHtml, Array(copies).fill(tagHtml).join('\n'));
      await route.fulfill({ response, body: html });
    });
  }

  /** Records every submission body; the n-th POST (0-based) is handled by
   * `handlers[n]` when given, otherwise passed through to the server. */
  async function captureSubmissions(page: Page, handlers: Array<(route: Route) => Promise<void>> = []) {
    const bodies: SubmitBody[] = [];
    await page.route(submissionsPattern(), async (route) => {
      if (route.request().method() !== 'POST') {
        await route.continue();
        return;
      }
      const index = bodies.length;
      bodies.push(route.request().postDataJSON() as SubmitBody);
      const handler = handlers[index];
      if (handler) {
        await handler(route);
      } else {
        await route.continue();
      }
    });
    return bodies;
  }

  async function openForm(page: Page, count = 1) {
    await page.goto(viewURL());
    await expect(page.locator('.gcrm-form input[name="email"]')).toHaveCount(count);
    // Wait out the time trap of the challenge the script fetched.
    await page.waitForTimeout(TIME_TRAP_MS);
  }

  async function storedSubmissions(email: string): Promise<StoredSubmission[]> {
    const response = await api.get(`${API_BASE_URL}/forms/${formId}/submissions?limit=100`, { headers });
    expect(response.status()).toBe(200);
    return ((await response.json()).data as StoredSubmission[]).filter((row) => row.email === email);
  }

  test('the hosted view key is sent on the resend after a 500 and stored on both pending rows', async ({
    page,
  }) => {
    const email = `embed_key_hosted_${Date.now()}@example.com`;
    const bodies = await captureSubmissions(page, [
      // The first submission reaches the server, but the page is told it failed.
      async (route) => {
        await route.fetch();
        await route.fulfill({
          status: 500,
          contentType: 'application/json',
          body: JSON.stringify({ success: false, error: { code: 'INTERNAL_ERROR', message: 'boom' } }),
        });
      },
    ]);

    await page.goto(viewURL());
    const hostedKey = await page.locator('script[data-form-key]').getAttribute('data-idempotency-key');
    expect(hostedKey).toMatch(HEX_KEY);
    await expect(page.locator('.gcrm-form input[name="email"]')).toHaveCount(1);
    await page.waitForTimeout(TIME_TRAP_MS);

    await page.locator('.gcrm-form input[name="email"]').fill(email);
    await page.locator('.gcrm-form button[type="submit"]').click();
    await expect(page.locator('.gcrm-form-error')).toHaveText(GENERIC_ERROR);
    await expect(page.locator('.gcrm-form button[type="submit"]')).toBeEnabled();

    await page.locator('.gcrm-form button[type="submit"]').click();
    await expect(page.locator('.gcrm-success')).toBeVisible();

    expect(bodies).toHaveLength(2);
    expect(bodies[0].idempotency_key).toBe(hostedKey);
    expect(bodies[1].idempotency_key).toBe(hostedKey);

    const rows = await storedSubmissions(email);
    expect(rows).toHaveLength(2);
    for (const row of rows) {
      expect(row.status).toBe('pending');
      expect(row.idempotency_key).toBe(hostedKey);
    }
  });

  test('without the attribute the script draws one key and resends it after an aborted POST', async ({
    page,
  }) => {
    await page.addInitScript(() => {
      Crypto.prototype.getRandomValues = function <T extends ArrayBufferView | null>(array: T): T {
        const bytes = array as unknown as Uint8Array;
        for (let i = 0; i < bytes.length; i++) bytes[i] = i;
        return array;
      };
    });
    await stripHostedKey(page);
    const bodies = await captureSubmissions(page, [(route) => route.abort('failed')]);

    await openForm(page);
    await expect(page.locator('script[data-idempotency-key]')).toHaveCount(0);
    const email = `embed_key_generated_${Date.now()}@example.com`;
    await page.locator('.gcrm-form input[name="email"]').fill(email);
    await page.locator('.gcrm-form button[type="submit"]').click();
    await expect(page.locator('.gcrm-form-error')).toHaveText(GENERIC_ERROR);

    await page.locator('.gcrm-form button[type="submit"]').click();
    await expect(page.locator('.gcrm-success')).toBeVisible();

    expect(bodies).toHaveLength(2);
    for (const body of bodies) {
      expect(body.idempotency_key).toBe(DETERMINISTIC_KEY);
      expect(body.idempotency_key).toHaveLength(32);
    }
    const rows = await storedSubmissions(email);
    expect(rows).toHaveLength(1);
    expect(rows[0].idempotency_key).toBe(DETERMINISTIC_KEY);
  });

  test('two embed tags on one page send distinct keys', async ({ page }) => {
    await stripHostedKey(page, 2);
    const bodies = await captureSubmissions(page);

    await openForm(page, 2);
    const stamp = Date.now();
    for (let i = 0; i < 2; i++) {
      const form = page.locator('.gcrm-form').nth(i);
      await form.locator('input[name="email"]').fill(`embed_key_tag${i}_${stamp}@example.com`);
      await form.locator('button[type="submit"]').click();
      await expect(form.locator('.gcrm-success')).toBeVisible();
    }

    expect(bodies).toHaveLength(2);
    expect(bodies[0].idempotency_key).toMatch(HEX_KEY);
    expect(bodies[1].idempotency_key).toMatch(HEX_KEY);
    expect(bodies[0].idempotency_key).not.toBe(bodies[1].idempotency_key);
  });

  test('a render without any key source sends no idempotency_key and is accepted', async ({ page }) => {
    await page.addInitScript(() => {
      Crypto.prototype.getRandomValues = function () {
        throw new Error('unavailable');
      };
    });
    await stripHostedKey(page);
    const bodies = await captureSubmissions(page);

    await openForm(page);
    const answer = page.waitForResponse(
      (response) =>
        response.url().endsWith(`/forms/public/${publicId}/submissions`) &&
        response.request().method() === 'POST'
    );
    await page.locator('.gcrm-form input[name="email"]').fill(`embed_key_none_${Date.now()}@example.com`);
    await page.locator('.gcrm-form button[type="submit"]').click();
    expect((await answer).status()).toBe(200);
    await expect(page.locator('.gcrm-success')).toBeVisible();

    expect(bodies).toHaveLength(1);
    expect(Object.prototype.hasOwnProperty.call(bodies[0], 'idempotency_key')).toBe(false);
  });
});
