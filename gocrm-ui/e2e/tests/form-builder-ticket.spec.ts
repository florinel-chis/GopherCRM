import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';
import { apiResponse } from '../helpers/api';

/**
 * The form builder's submission action (#335) against the real API: the seeded
 * admin builds a form with "Create a support ticket", the POST answer carries
 * create_ticket and double_opt_in, FormDetail shows "Creates tickets: yes", and
 * reopening the editor and saving unchanged keeps create_ticket true (read back
 * through the API). Before #335 the builder sent no create_ticket, so a save
 * turned ticket intake off.
 *
 * The form stays a draft, so it is never served publicly, and it is deleted
 * through the API afterwards.
 */

test.describe('Admin - Form builder ticket action', () => {
  const formName = `E2E ticket form ${Date.now()}`;

  let api: APIRequestContext;
  let headers: Record<string, string>;
  let formId = 0;

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
  });

  test.afterAll(async () => {
    if (formId) {
      const deleted = await api.delete(`${API_BASE_URL}/forms/${formId}`, { headers });
      expect(deleted.status(), 'form cleanup').toBeLessThan(300);
    }
    await api.dispose();
  });

  test.beforeEach(async ({ page }) => {
    await new AdminAuthHelper(page).ensureAdminLoggedIn();
  });

  test('admin creates a ticket form in the builder and an unchanged update keeps create_ticket', async ({
    page,
  }, testInfo) => {
    // 1. Create: the ticket action switches double opt-in on and hides the owner.
    await page.goto('/forms/new');
    await expect(page.getByRole('heading', { name: 'Create Form' })).toBeVisible();
    await page.locator('input[name="name"]').fill(formName);

    await expect(page.getByRole('heading', { name: 'Submission action' })).toBeVisible();
    await expect(page.getByRole('radio', { name: 'Create a lead' })).toBeChecked();
    await page.getByRole('radio', { name: 'Create a support ticket' }).check();

    const optIn = page.getByLabel('Require email confirmation before a lead or ticket is created');
    await expect(optIn).toBeChecked();
    await expect(page.getByTestId('lead-action-settings')).toBeHidden();
    await expect(page.getByText(/A confirmed submission opens a ticket/)).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('form-builder-ticket-create.png'), fullPage: true });

    const created = apiResponse(page, /\/forms$/, 'POST');
    await page.getByRole('button', { name: 'Create Form' }).click();
    const createResponse = await created;
    expect(createResponse.status(), 'POST /forms').toBe(201);
    const stored = (await createResponse.json()).data;
    formId = stored.id;
    expect(stored.create_ticket).toBe(true);
    expect(stored.create_lead).toBe(false);
    expect(stored.double_opt_in).toBe(true);

    // 2. Detail shows the flag.
    await page.waitForURL(`**/forms/${formId}`);
    await expect(page.getByText('Creates tickets: yes')).toBeVisible();
    await expect(page.getByText('Creates leads: no')).toBeVisible();
    await page.screenshot({ path: testInfo.outputPath('form-builder-ticket-detail.png'), fullPage: true });

    // 3. Edit without changes: the action loads and the update keeps the flag.
    await page.goto(`/forms/${formId}/edit`);
    await expect(page.getByRole('heading', { name: 'Edit Form' })).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(formName);
    await expect(page.getByRole('radio', { name: 'Create a support ticket' })).toBeChecked();
    await page.screenshot({ path: testInfo.outputPath('form-builder-ticket-edit.png'), fullPage: true });

    const updated = apiResponse(page, new RegExp(`/forms/${formId}$`), 'PUT');
    await page.getByRole('button', { name: 'Update Form' }).click();
    const updateResponse = await updated;
    expect(updateResponse.status(), 'PUT /forms/:id').toBe(200);
    expect(updateResponse.request().postDataJSON()).toMatchObject({
      create_ticket: true,
      create_lead: false,
      double_opt_in: true,
      default_owner_id: 0,
    });

    const readBack = await api.get(`${API_BASE_URL}/forms/${formId}`, { headers });
    expect(readBack.status()).toBe(200);
    const after = (await readBack.json()).data;
    expect(after.create_ticket).toBe(true);
    expect(after.create_lead).toBe(false);
    expect(after.double_opt_in).toBe(true);
  });
});
