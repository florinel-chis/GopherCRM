import { test, expect, Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { LeadsPage } from '../pages/leads.page';
import { generateLeadData } from '../fixtures/admin-user';

/**
 * Documentation captures for the Leads area.
 *
 * Every test creates the records it needs through the UI, so the suite is
 * self-sufficient against an empty database. The lead used by the detail and
 * edit captures is created once and shared through module scope, which is why
 * the file runs serially.
 */
test.describe.configure({ mode: 'serial' });

type LeadData = ReturnType<typeof generateLeadData>;

let sharedLead: { id: number; data: LeadData } | null = null;

/** Creates a lead through the UI and returns its id together with the data used. */
async function createLead(page: Page, overrides: Partial<LeadData> = {}) {
  const leadsPage = new LeadsPage(page);
  const data: LeadData = { ...generateLeadData(), ...overrides };

  await leadsPage.goto();
  await leadsPage.clickNewLead();
  await leadsPage.fillLeadForm(data);

  const response = await leadsPage.saveAndWaitForResponse();
  expect(response.status()).toBe(201);

  const body = await response.json();
  const id = Number(body?.data?.id);
  expect(Number.isFinite(id)).toBe(true);

  await page.waitForURL('**/leads', { timeout: 15000 });
  return { id, data };
}

test.describe('Screenshots - Leads', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('01-list', async ({ page }) => {
    const leadsPage = new LeadsPage(page);

    for (let i = 0; i < 3; i++) {
      await createLead(page);
    }

    await leadsPage.goto();
    await expect(leadsPage.leadsTable).toBeVisible();
    await expect(leadsPage.tableRows.nth(2)).toBeVisible();

    await capture(page, 'leads', '01-list', { fullPage: true });
  });

  test('02-create', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    const data = generateLeadData();

    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await expect(page).toHaveURL(/\/leads\/new$/);

    await leadsPage.fillLeadForm(data);
    await expect(leadsPage.companyNameInput).toHaveValue(data.companyName);
    await expect(leadsPage.emailInput).toHaveValue(data.email);

    // Captured before submitting so the form shows the filled-in state.
    await capture(page, 'leads', '02-create');
  });

  test('03-detail', async ({ page }) => {
    sharedLead = await createLead(page);

    await page.goto(`/leads/${sharedLead.id}`);
    await expect(page).toHaveURL(/\/leads\/\d+$/);
    await expect(page.getByText(sharedLead.data.companyName).first()).toBeVisible();
    await expect(page.getByRole('tab', { name: 'Details' })).toBeVisible();

    await capture(page, 'leads', '03-detail', { fullPage: true });
  });

  test('04-edit', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    if (!sharedLead) {
      sharedLead = await createLead(page);
    }

    await page.goto(`/leads/${sharedLead.id}/edit`);
    await expect(page).toHaveURL(/\/leads\/\d+\/edit$/);
    await expect(leadsPage.companyNameInput).toHaveValue(sharedLead.data.companyName);
    await expect(leadsPage.contactNameInput).toHaveValue(sharedLead.data.contactName);

    await capture(page, 'leads', '04-edit');
  });

  test('05-delete-confirm', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    const { data } = await createLead(page);

    await leadsPage.goto();
    await expect(leadsPage.tableRows.first()).toBeVisible();

    // The list is not sorted newest-first, so narrow it down to the fresh lead
    // instead of relying on it landing on the first page.
    await leadsPage.searchInput.fill(data.companyName);

    const row = leadsPage.tableRows.filter({ hasText: data.companyName }).first();
    await expect(row).toBeVisible();
    await row.locator('[data-testid="DeleteIcon"]').first().click();

    const dialog = page.getByRole('dialog', { name: 'Delete Lead' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('Delete Lead')).toBeVisible();

    await capture(page, 'leads', '05-delete-confirm');

    // Never confirm: deleting a lead is an irreversible erasure.
    await dialog.locator('button:has-text("Cancel")').click();
    await expect(dialog).toBeHidden();
  });

  test('06-search', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    const { data } = await createLead(page);

    await leadsPage.goto();
    await expect(leadsPage.tableRows.first()).toBeVisible();

    await leadsPage.searchInput.fill(data.companyName);

    const matchingRow = leadsPage.tableRows.filter({ hasText: data.companyName }).first();
    await expect(matchingRow).toBeVisible();
    await expect(leadsPage.searchInput).toHaveValue(data.companyName);

    await capture(page, 'leads', '06-search');
  });
});
