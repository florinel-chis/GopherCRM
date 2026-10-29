import { test, expect, request as playwrightRequest, type APIRequestContext, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * The admin flow through the follow-up automation screens: build a disabled
 * lead_created rule with a condition and a notify step in the rule form, find
 * it in the rule list, dry-run it on a lead and check that the dry run
 * notified nobody (the bell's unread count is unchanged).
 *
 * `make e2e` starts from an empty database, so the lead the dry run uses is
 * created here through the API and removed again afterwards, together with
 * the rule. The rule is saved disabled, so the automation worker running
 * inside the e2e backend never starts a run for it.
 */

const apiResponse = (page: Page, pathPattern: RegExp, method: 'POST' | 'PUT' | 'GET' | 'DELETE') =>
  page.waitForResponse(
    (response) => pathPattern.test(new URL(response.url()).pathname) && response.request().method() === method
  );

// The bell polls the count once a minute and the test timeout is 30 s, so
// every wait for it is registered before the navigation that triggers it.
const unreadCount = (page: Page) =>
  page.waitForResponse(
    (response) =>
      new URL(response.url()).pathname.endsWith('/notifications/unread-count') &&
      response.request().method() === 'GET' &&
      response.status() === 200
  );

test.describe('Admin - Automation rules', () => {
  const stamp = `${Date.now()}`;
  const ruleName = `E2E rule ${stamp}`;
  const leadLabel = `Automation E2E ${stamp}`;

  let api: APIRequestContext;
  let headers: Record<string, string>;
  let adminId = 0;
  let leadId = 0;
  let ruleId = 0;

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

    const lead = await api.post(`${API_BASE_URL}/leads`, {
      headers,
      data: {
        first_name: 'Automation',
        last_name: `E2E ${stamp}`,
        email: `automation.e2e.${stamp}@example.com`,
        owner_id: adminId,
        status: 'new',
      },
    });
    expect(lead.status()).toBe(201);
    leadId = (await lead.json()).data.id;
  });

  test.afterAll(async () => {
    if (ruleId) {
      const deleted = await api.delete(`${API_BASE_URL}/automation/rules/${ruleId}`, { headers });
      expect(deleted.status(), 'rule cleanup').toBeLessThan(300);
    }
    if (leadId) {
      const deleted = await api.delete(`${API_BASE_URL}/leads/${leadId}`, { headers });
      expect(deleted.status(), 'lead cleanup').toBeLessThan(300);
    }
    await api.dispose();
  });

  test.beforeEach(async ({ page }) => {
    await new AdminAuthHelper(page).ensureAdminLoggedIn();
  });

  test('admin creates a disabled lead_created rule with a condition and a notify step, dry-runs it on a lead and the unread count stays the same', async ({
    page,
  }) => {
    const bell = page.getByTestId('notification-bell');

    // 1. The unread baseline, from the response the bell itself renders.
    const firstCount = unreadCount(page);
    await page.goto('/');
    const before = (await (await firstCount).json()).data.unread as number;
    await expect(bell).toHaveAttribute('data-unread', String(before));

    // 2. The rule form: a lead_created trigger.
    await page.goto('/automation/rules/new');
    await expect(page.getByRole('heading', { name: 'New automation rule' })).toBeVisible();
    await page.locator('input[name="name"]').fill(ruleName);
    await expect(page.getByRole('combobox', { name: 'Entity type' })).toHaveText('Lead');
    await page.getByRole('combobox', { name: 'Trigger' }).click();
    await page.getByRole('option', { name: 'Lead created' }).click();
    await expect(page.getByRole('combobox', { name: 'Trigger' })).toHaveText('Lead created');

    // 3. Step 1: condition status = new, stop the run otherwise.
    await page.getByRole('button', { name: 'Add step' }).click();
    await page.getByRole('menuitem', { name: 'Condition' }).click();
    const step1 = page.getByRole('group', { name: 'Step 1' });
    await expect(step1).toBeVisible();
    await step1.getByRole('combobox', { name: 'Field' }).click();
    await page.getByRole('option', { name: 'Status' }).click();
    await step1.getByRole('combobox', { name: 'Operator' }).click();
    await page.getByRole('option', { name: 'equals', exact: true }).click();
    await step1.getByRole('combobox', { name: 'Value' }).click();
    await page.getByRole('option', { name: 'New', exact: true }).click();
    await expect(step1.getByRole('combobox', { name: 'Value' })).toHaveText('New');
    await expect(step1.getByRole('combobox', { name: 'If false' })).toHaveText('Stop the run');

    // 4. Step 2: notify the record owner.
    await page.getByRole('button', { name: 'Add step' }).click();
    await page.getByRole('menuitem', { name: 'Notify' }).click();
    const step2 = page.getByRole('group', { name: 'Step 2' });
    await expect(step2).toBeVisible();
    await expect(step2.getByRole('combobox', { name: 'Notify' })).toHaveText('Record owner');
    await step2.getByLabel('Title').fill('New lead {{lead.id}}');

    // 5. Enabled stays off; the rule is created disabled.
    await expect(page.getByRole('switch', { name: 'Enabled', exact: true })).not.toBeChecked();
    const created = apiResponse(page, /\/automation\/rules$/, 'POST');
    await page.getByRole('button', { name: 'Create rule' }).click();
    const createdResponse = await created;
    expect(createdResponse.status()).toBe(201);
    expect(createdResponse.request().postDataJSON()).toMatchObject({
      enabled: false,
      trigger_type: 'lead_created',
      steps: [{ step_type: 'condition' }, { step_type: 'notify' }],
    });
    ruleId = (await createdResponse.json()).data.id;
    expect(ruleId).toBeGreaterThan(0);
    await page.waitForURL(/\/automation\/rules$/);

    // 6. The list row, its switch off, and the dry run from the Tools column.
    const row = page.locator('table tbody tr').filter({ hasText: ruleName });
    await expect(row).toHaveCount(1);
    await expect(row.getByLabel(`${ruleName} enabled`)).not.toBeChecked();
    await row.getByRole('button', { name: 'Dry run' }).click();

    const dialog = page.getByRole('dialog', { name: /^Dry run/ });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByTestId('dry-run-notice')).toBeVisible();
    const leadInput = dialog.getByRole('combobox', { name: 'Lead' });
    await leadInput.click();
    await leadInput.fill(stamp);
    await page.getByRole('option', { name: leadLabel }).click();
    await expect(leadInput).toHaveValue(leadLabel);

    const dry = apiResponse(page, /\/automation\/rules\/\d+\/dry-run$/, 'POST');
    await dialog.getByRole('button', { name: 'Run dry run' }).click();
    expect((await dry).status()).toBe(200);

    const steps = dialog.getByTestId('dry-run-step');
    await expect(steps).toHaveCount(2);
    await expect(steps.nth(0)).toContainText('Condition');
    await expect(steps.nth(0).getByTestId('outcome-chip')).toHaveText('Succeeded');
    await expect(steps.nth(0)).toContainText('Continue');
    await expect(steps.nth(1)).toContainText('Notify');
    await expect(steps.nth(1).getByTestId('outcome-chip')).toHaveText('Succeeded');
    await expect(steps.nth(1)).toContainText(`User #${adminId}`);

    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();

    // 7. The dry run notified nobody: the count is where it started.
    const secondCount = unreadCount(page);
    await page.goto('/');
    expect((await (await secondCount).json()).data.unread).toBe(before);
    await expect(bell).toHaveAttribute('data-unread', String(before));

    const apiCount = await api.get(`${API_BASE_URL}/notifications/unread-count`, { headers });
    expect(apiCount.status()).toBe(200);
    expect((await apiCount.json()).data.unread).toBe(before);
  });
});
