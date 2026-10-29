import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Documentation captures for follow-up automation and notifications
 * (FEATURES.md 4e.17): the rule list, the rule form, the dry-run dialog, the
 * run list and a run's step log, the Automation section on a lead, the
 * notification bell's menu and the notifications page.
 *
 * Three rules are seeded through the API with fixed names, each created only
 * when missing so repeated runs against one database do not pile them up on
 * the list being captured. Only "New lead follow-up" is enabled: two leads
 * created after it start runs that notify the owner (the admin) and then wait
 * two days, so the run list, the step log, the lead section and the bell all
 * have real data. Runs start on a worker tick; export
 * AUTOMATION_POLL_INTERVAL_SECONDS=2 for the backend to keep the wait short.
 * A backend started with AUTOMATION_ENABLED=false fails the run-dependent
 * captures on purpose.
 */
test.describe.configure({ mode: 'serial' });

const AREA = 'automation';
const DAY_MINUTES = 24 * 60;

const RULE_A = 'New lead follow-up';
const RULE_B = 'Proposal sent: check in after three days';
const RULE_C = 'Daily nudge for stale contacted leads';

interface RuleSeed {
  name: string;
  description: string;
  entity_type: 'lead' | 'deal';
  trigger_type: string;
  trigger: Record<string, unknown>;
  enabled: boolean;
  steps: Array<{ step_type: string; config: Record<string, unknown> }>;
}

const RULES: RuleSeed[] = [
  {
    name: RULE_A,
    description: 'Tells the owner about every new lead and books a follow-up call two days later.',
    entity_type: 'lead',
    trigger_type: 'lead_created',
    trigger: {},
    enabled: true,
    steps: [
      {
        step_type: 'notify',
        config: {
          recipient: 'owner',
          title: 'New lead {{lead.first_name}} {{lead.last_name}}',
          body: 'A new lead from {{lead.company}} is waiting for a first call.',
        },
      },
      { step_type: 'wait', config: { duration_minutes: 2 * DAY_MINUTES } },
      {
        step_type: 'create_task',
        config: {
          recipient: 'owner',
          title: 'Follow up lead {{lead.id}}',
          description: 'Call the lead and qualify the opportunity.',
          priority: 'high',
          due_in_days: 1,
        },
      },
    ],
  },
  {
    name: RULE_B,
    description: 'Emails the owner a reminder when a deal has sat in proposal for three days.',
    entity_type: 'deal',
    trigger_type: 'deal_stage_changed',
    trigger: { to: 'proposal' },
    enabled: false,
    steps: [
      { step_type: 'wait', config: { duration_minutes: 3 * DAY_MINUTES } },
      {
        step_type: 'condition',
        config: { field: 'stage', operator: 'eq', value: 'proposal', else: 'stop' },
      },
      {
        step_type: 'send_email',
        config: {
          recipient: 'owner',
          subject: 'Check in on {{deal.title}}',
          body: 'The proposal for {{deal.title}} went out three days ago. Time for a follow-up call.',
        },
      },
    ],
  },
  {
    name: RULE_C,
    description: 'Every morning, a task for each contacted lead nobody has touched for two weeks.',
    entity_type: 'lead',
    trigger_type: 'schedule',
    trigger: { hour_utc: 8, filters: { status: 'contacted' }, stale_days: 14 },
    enabled: false,
    steps: [
      {
        step_type: 'create_task',
        config: {
          recipient: 'owner',
          title: 'Re-engage lead {{lead.id}}',
          priority: 'medium',
          due_in_days: 2,
        },
      },
    ],
  },
];

interface Seeded {
  rules: Map<string, number>;
  leadId: number;
  leadLabel: string;
  runId: number;
}

let seeded: Seeded | null = null;

async function apiLogin(): Promise<{ api: APIRequestContext; headers: Record<string, string> }> {
  const api = await playwrightRequest.newContext();
  const login = await api.post(`${API_BASE_URL}/auth/login`, {
    data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
  });
  expect(login.status()).toBe(200);
  return { api, headers: { Authorization: `Bearer ${(await login.json()).data.token}` } };
}

async function post(api: APIRequestContext, headers: Record<string, string>, path: string, data: unknown) {
  const response = await api.post(`${API_BASE_URL}${path}`, { headers, data });
  expect(response.status(), `POST ${path}`).toBeLessThan(300);
  return (await response.json()).data;
}

/** Creates the rules that are missing, and returns every rule's id by name. */
async function seedRules(api: APIRequestContext, headers: Record<string, string>) {
  const found = await api.get(`${API_BASE_URL}/automation/rules`, { headers, params: { limit: 100 } });
  expect(found.status()).toBe(200);
  const rows: Array<{ id: number; name: string; enabled: boolean }> = (await found.json()).data ?? [];
  const ids = new Map<string, number>();
  for (const rule of RULES) {
    const existing = rows.find((row) => row.name === rule.name);
    if (existing) {
      if (rule.enabled && !existing.enabled) {
        await post(api, headers, `/automation/rules/${existing.id}/enable`, {});
      }
      ids.set(rule.name, existing.id);
      continue;
    }
    const created = await post(api, headers, '/automation/rules', rule);
    ids.set(rule.name, created.id);
  }
  return ids;
}

/** Waits for the worker to run a lead's run up to its wait step. */
async function waitingRunOf(api: APIRequestContext, headers: Record<string, string>, leadId: number) {
  let runId = 0;
  await expect
    .poll(
      async () => {
        const runs = await api.get(`${API_BASE_URL}/automation/runs`, {
          headers,
          params: { entity_type: 'lead', entity_id: leadId },
        });
        expect(runs.status()).toBe(200);
        const rows: Array<{ id: number; status: string }> = (await runs.json()).data ?? [];
        const waiting = rows.find((row) => row.status === 'waiting');
        runId = waiting?.id ?? 0;
        return waiting?.status ?? rows.map((row) => row.status).join(',');
      },
      { timeout: 120_000, intervals: [1_000, 2_000] }
    )
    .toBe('waiting');
  return runId;
}

async function seedData(): Promise<Seeded> {
  const { api, headers } = await apiLogin();
  try {
    const rules = await seedRules(api, headers);
    const me = await api.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const ownerId: number = (await me.json()).data.id;

    // Lead emails carry the run's timestamp; the leads are created after the
    // follow-up rule is enabled, so each one starts a run.
    const stamp = Date.now();
    const lead = (firstName: string, lastName: string, company: string, position: string) =>
      post(api, headers, '/leads', {
        first_name: firstName,
        last_name: lastName,
        email: `${firstName.toLowerCase()}.${lastName.toLowerCase()}.${stamp}@example.com`,
        company,
        position,
        source: 'website',
        status: 'new',
        owner_id: ownerId,
      });
    const second = await lead('Elena', 'Popescu', 'Danube Freight', 'Procurement Manager');
    const main = await lead('Andrei', 'Marin', 'Northwind Retail', 'Head of Operations');

    await waitingRunOf(api, headers, second.id);
    const runId = await waitingRunOf(api, headers, main.id);

    return { rules, leadId: main.id, leadLabel: 'Andrei Marin (Northwind Retail)', runId };
  } finally {
    await api.dispose();
  }
}

async function ensureSeeded(): Promise<Seeded> {
  if (!seeded) {
    seeded = await seedData();
  }
  return seeded;
}

function ruleId(data: Seeded, name: string): number {
  const id = data.rules.get(name);
  expect(id, name).toBeDefined();
  return id as number;
}

test.describe('Screenshots - Follow-up automation', () => {
  test.beforeAll(async () => {
    test.setTimeout(180_000);
    await ensureSeeded();
  });

  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('01-rules-list', async ({ page }) => {
    await page.goto('/automation/rules');
    await expect(page.getByRole('heading', { name: 'Automation rules' })).toBeVisible();

    const rows = page.locator('table tbody tr');
    for (const rule of RULES) {
      const row = rows.filter({ hasText: rule.name }).first();
      await expect(row).toBeVisible();
      const enabledSwitch = row.getByLabel(`${rule.name} enabled`);
      if (rule.enabled) {
        await expect(enabledSwitch).toBeChecked();
      } else {
        await expect(enabledSwitch).not.toBeChecked();
      }
    }

    await capture(page, AREA, '01-rules-list', { fullPage: true });
  });

  test('02-rule-form', async ({ page }) => {
    const data = await ensureSeeded();
    await page.goto(`/automation/rules/${ruleId(data, RULE_B)}/edit`);
    await expect(page.getByRole('heading', { name: 'Edit automation rule' })).toBeVisible();
    await expect(page.locator('input[name="name"]')).toHaveValue(RULE_B);
    await expect(page.getByRole('group', { name: /^Step \d/ })).toHaveCount(3);
    await expect(page.getByRole('group', { name: 'Step 2' }).getByRole('combobox', { name: 'Field' })).toHaveText(
      'Stage'
    );
    await expect(page.getByTestId('dry-run-draft-notice')).toBeVisible();

    await capture(page, AREA, '02-rule-form', { fullPage: true });
  });

  test('03-dry-run-dialog', async ({ page }) => {
    const data = await ensureSeeded();
    await page.goto('/automation/rules');
    const row = page.locator('table tbody tr').filter({ hasText: RULE_A }).first();
    await expect(row).toBeVisible();
    await row.getByRole('button', { name: 'Dry run' }).click();

    const dialog = page.getByRole('dialog', { name: `Dry run: ${RULE_A}` });
    await expect(dialog).toBeVisible();
    const leadInput = dialog.getByRole('combobox', { name: 'Lead' });
    await leadInput.click();
    await leadInput.fill('Andrei Marin');
    await page.getByRole('option', { name: data.leadLabel }).first().click();
    await expect(leadInput).toHaveValue(data.leadLabel);

    const dryRun = page.waitForResponse(
      (response) =>
        /\/automation\/rules\/\d+\/dry-run$/.test(new URL(response.url()).pathname) &&
        response.request().method() === 'POST'
    );
    await dialog.getByRole('button', { name: 'Run dry run' }).click();
    expect((await dryRun).status()).toBe(200);
    await expect(dialog.getByTestId('dry-run-step')).toHaveCount(3);

    await capture(page, AREA, '03-dry-run-dialog');
    await dialog.getByRole('button', { name: 'Close' }).click();
    await expect(dialog).toBeHidden();
  });

  test('04-runs-list', async ({ page }) => {
    await ensureSeeded();
    await page.goto('/automation/runs');
    await expect(page.getByRole('heading', { name: 'Automation runs' })).toBeVisible();
    const rows = page.locator('table tbody tr');
    await expect(rows.getByTestId('run-status-chip').filter({ hasText: 'Waiting' }).first()).toBeVisible();
    expect(await rows.count()).toBeGreaterThanOrEqual(2);

    await capture(page, AREA, '04-runs-list', { fullPage: true });
  });

  test('05-run-detail', async ({ page }) => {
    const data = await ensureSeeded();
    await page.goto(`/automation/runs/${data.runId}`);
    await expect(page.getByRole('heading', { name: `Run #${data.runId}` })).toBeVisible();
    await expect(page.getByTestId('run-status-chip')).toHaveText('Waiting');
    const steps = page.getByTestId('run-step-row');
    await expect(steps).toHaveCount(2);
    await expect(steps.nth(0)).toContainText('Notify');
    await expect(steps.nth(0).getByTestId('outcome-chip')).toHaveText('Succeeded');
    await expect(steps.nth(1)).toContainText('Wait');
    await expect(steps.nth(1).getByTestId('outcome-chip')).toHaveText('Waiting');
    await expect(page.getByRole('button', { name: 'Cancel run' })).toBeVisible();

    await capture(page, AREA, '05-run-detail', { fullPage: true });
  });

  test('06-lead-automation-section', async ({ page }) => {
    const data = await ensureSeeded();
    await page.goto(`/leads/${data.leadId}`);
    const section = page.locator('section[aria-labelledby="lead-automation-heading"]');
    await expect(section).toBeVisible();
    await expect(section.getByTestId('record-run-row')).toHaveCount(1);
    await expect(section.getByTestId('run-status-chip')).toHaveText('Waiting');

    await capture(page, AREA, '06-lead-automation-section', { fullPage: true });
  });

  test('07-notification-bell-menu', async ({ page }) => {
    await ensureSeeded();
    await page.goto('/');
    const bell = page.getByTestId('notification-bell');
    await expect(bell).not.toHaveAttribute('data-unread', '0');
    await bell.click();
    const menu = page.getByRole('menu', { name: 'Notifications' });
    await expect(menu).toBeVisible();
    await expect(menu.getByTestId('bell-notification').filter({ hasText: 'New lead Andrei Marin' })).toBeVisible();

    await capture(page, AREA, '07-notification-bell-menu');
    await page.keyboard.press('Escape');
    await expect(menu).toBeHidden();
  });

  test('08-notifications-page', async ({ page }) => {
    await ensureSeeded();
    await page.goto('/notifications');
    await expect(page.getByRole('heading', { name: 'Notifications' })).toBeVisible();
    const rows = page.getByTestId('notification-row');
    await expect(rows.filter({ hasText: 'New lead Andrei Marin' }).first()).toBeVisible();
    await expect(rows.filter({ hasText: 'New lead Elena Popescu' }).first()).toBeVisible();

    await capture(page, AREA, '08-notifications-page', { fullPage: true });
  });
});
