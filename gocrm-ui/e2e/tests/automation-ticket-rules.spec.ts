import { test, expect, request as playwrightRequest, type APIRequestContext, type Page } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Ticket rules in the rule form (#312) against the real API: the seeded admin
 * builds a disabled ticket_status_changed rule (Open → Resolved) with a notify
 * step titled with {{ticket.number}}, the POST answer is the stored rule, the
 * edit page loads every field of it, and Save changes sends a PUT body whose
 * entity_type, trigger_type, trigger and steps equal the stored ones.
 *
 * The rule stays disabled, so the automation worker inside the e2e backend
 * never starts a run for it, and it is deleted through the API afterwards.
 */

const apiResponse = (page: Page, pathPattern: RegExp, method: 'POST' | 'PUT' | 'GET' | 'DELETE') =>
  page.waitForResponse(
    (response) => pathPattern.test(new URL(response.url()).pathname) && response.request().method() === method
  );

interface StoredStep {
  position: number;
  step_type: string;
  config: Record<string, unknown>;
}

interface StoredRule {
  id: number;
  entity_type: string;
  trigger_type: string;
  enabled: boolean;
  trigger: Record<string, unknown>;
  steps: StoredStep[];
}

// The steps as a rule body carries them: in position order, type and config only.
const asBodySteps = (steps: StoredStep[]) =>
  [...steps].sort((a, b) => a.position - b.position).map(({ step_type, config }) => ({ step_type, config }));

test.describe('Admin - Automation ticket rules', () => {
  const stamp = `${Date.now()}`;
  const ruleName = `E2E ticket rule ${stamp}`;
  const stepTitle = 'Ticket {{ticket.number}} resolved';

  let api: APIRequestContext;
  let headers: Record<string, string>;
  let ruleId = 0;

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
  });

  test.afterAll(async () => {
    if (ruleId) {
      const deleted = await api.delete(`${API_BASE_URL}/automation/rules/${ruleId}`, { headers });
      expect(deleted.status(), 'rule cleanup').toBeLessThan(300);
    }
    await api.dispose();
  });

  test.beforeEach(async ({ page }) => {
    await new AdminAuthHelper(page).ensureAdminLoggedIn();
  });

  test('admin creates a disabled ticket_status_changed rule in the form and the saved rule round-trips through the API', async ({
    page,
  }) => {
    // 1. The rule form: entity Ticket, trigger Ticket status changed, Open → Resolved.
    await page.goto('/automation/rules/new');
    await expect(page.getByRole('heading', { name: 'New automation rule' })).toBeVisible();
    await page.locator('input[name="name"]').fill(ruleName);

    await page.getByRole('combobox', { name: 'Entity type' }).click();
    await page.getByRole('option', { name: 'Ticket', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'Entity type' })).toHaveText('Ticket');

    await page.getByRole('combobox', { name: 'Trigger' }).click();
    await page.getByRole('option', { name: 'Ticket status changed', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'Trigger' })).toHaveText('Ticket status changed');

    await page.getByRole('combobox', { name: 'From status' }).click();
    await page.getByRole('option', { name: 'Open', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'From status' })).toHaveText('Open');
    await page.getByRole('combobox', { name: 'To status' }).click();
    await page.getByRole('option', { name: 'Resolved', exact: true }).click();
    await expect(page.getByRole('combobox', { name: 'To status' })).toHaveText('Resolved');

    // 2. Step 1: notify the ticket's assignee, titled with the ticket number.
    await page.getByRole('button', { name: 'Add step' }).click();
    await page.getByRole('menuitem', { name: 'Notify' }).click();
    const step1 = page.getByRole('group', { name: 'Step 1' });
    await expect(step1).toBeVisible();
    await expect(step1.getByText(/\{\{ticket\.number\}\}/).first()).toBeVisible();
    await step1.getByLabel('Title').fill(stepTitle);

    // 3. Created disabled; the 201 answer is the stored ticket rule.
    await expect(page.getByRole('switch', { name: 'Enabled', exact: true })).not.toBeChecked();
    const created = apiResponse(page, /\/automation\/rules$/, 'POST');
    await page.getByRole('button', { name: 'Create rule' }).click();
    const createdResponse = await created;
    expect(createdResponse.status()).toBe(201);
    const sent = createdResponse.request().postDataJSON();
    expect(sent).toEqual({
      name: ruleName,
      description: '',
      entity_type: 'ticket',
      trigger_type: 'ticket_status_changed',
      trigger: { from: 'open', to: 'resolved' },
      enabled: false,
      steps: [{ step_type: 'notify', config: { title: stepTitle, recipient: 'owner' } }],
    });
    const stored = (await createdResponse.json()).data as StoredRule;
    ruleId = stored.id;
    expect(ruleId).toBeGreaterThan(0);
    expect(stored).toMatchObject({
      entity_type: 'ticket',
      trigger_type: 'ticket_status_changed',
      enabled: false,
      trigger: { from: 'open', to: 'resolved' },
    });
    expect(stored.trigger.filters).toBeUndefined();
    expect(asBodySteps(stored.steps)).toEqual([
      { step_type: 'notify', config: { title: stepTitle, recipient: 'owner' } },
    ]);
    await page.waitForURL(/\/automation\/rules$/);

    // 4. The edit page loads every field of the stored rule.
    const loaded = apiResponse(page, new RegExp(`/automation/rules/${ruleId}$`), 'GET');
    await page.goto(`/automation/rules/${ruleId}/edit`);
    expect((await loaded).status()).toBe(200);
    await expect(page.getByRole('heading', { name: 'Edit automation rule' })).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(ruleName);
    await expect(page.getByRole('combobox', { name: 'Entity type' })).toHaveText('Ticket');
    await expect(page.getByRole('combobox', { name: 'Trigger' })).toHaveText('Ticket status changed');
    await expect(page.getByRole('combobox', { name: 'From status' })).toHaveText('Open');
    await expect(page.getByRole('combobox', { name: 'To status' })).toHaveText('Resolved');
    await expect(page.getByRole('group', { name: 'Step 1' }).getByLabel('Title')).toHaveValue(stepTitle);

    // 5. Save changes: the PUT body is the stored rule, and the 200 keeps it.
    const saved = apiResponse(page, new RegExp(`/automation/rules/${ruleId}$`), 'PUT');
    await page.getByRole('button', { name: 'Save changes' }).click();
    await page.getByRole('dialog', { name: 'Save rule changes?' }).getByRole('button', { name: 'Save' }).click();
    const savedResponse = await saved;
    expect(savedResponse.status()).toBe(200);

    const put = savedResponse.request().postDataJSON();
    expect(put.entity_type).toBe(stored.entity_type);
    expect(put.trigger_type).toBe(stored.trigger_type);
    // The API answers hour_utc on every trigger (0 for an event trigger) and
    // the form leaves it out of an event trigger; with that default the body's
    // trigger is the stored one.
    expect(put.trigger).toEqual({ from: 'open', to: 'resolved' });
    expect({ hour_utc: 0, ...put.trigger }).toEqual(stored.trigger);
    expect(put.steps).toEqual(asBodySteps(stored.steps));
    expect(put).not.toHaveProperty('enabled');

    const updated = (await savedResponse.json()).data as StoredRule;
    expect(updated.id).toBe(ruleId);
    expect(updated.entity_type).toBe('ticket');
    expect(updated.trigger_type).toBe('ticket_status_changed');
    expect(updated.trigger).toEqual(stored.trigger);
    expect(asBodySteps(updated.steps)).toEqual(asBodySteps(stored.steps));
    expect(updated.enabled).toBe(false);
  });
});
