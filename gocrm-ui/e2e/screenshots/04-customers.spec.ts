import { test, expect, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { generateCustomerData } from '../fixtures/admin-user';
import { CustomersPage } from '../pages/customers.page';

/**
 * Documentation captures for the Customers area.
 *
 * Every record shown is created through the UI inside this file with faker
 * data, so the images never depend on what an earlier run left in the
 * database. The delete dialog is opened, captured and then cancelled —
 * deleting a customer is an irreversible erasure, not a soft delete.
 */

type CustomerData = ReturnType<typeof generateCustomerData>;

interface CreatedCustomer {
  id: number;
  data: CustomerData;
}

/** Customer reused by the detail, edit and delete captures. */
let showcase: CreatedCustomer | null = null;

/** Creates one customer through the form and returns its id and field values. */
async function createCustomer(page: Page): Promise<CreatedCustomer> {
  const customersPage = new CustomersPage(page);
  const data = generateCustomerData();

  await customersPage.goto();
  await customersPage.clickNewCustomer();
  await customersPage.fillCustomerForm(data);

  const response = await customersPage.saveAndWaitForResponse();
  expect(response.status()).toBe(201);

  const body = await response.json();
  const id = Number(body?.data?.id);
  expect(Number.isFinite(id)).toBeTruthy();

  // The form navigates back to the list once the mutation resolves.
  await page.waitForURL(/\/customers$/, { timeout: 15000 });

  return { id, data };
}

/** Returns the shared customer, creating it if an earlier test has not. */
async function ensureShowcaseCustomer(page: Page): Promise<CreatedCustomer> {
  if (!showcase) {
    showcase = await createCustomer(page);
  }
  return showcase;
}

test.describe.configure({ mode: 'serial' });

test.describe('Screenshots - Customers', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('customer list with several records', async ({ page }) => {
    // Three creations plus a login do not fit the default per-test budget.
    test.slow();

    const customersPage = new CustomersPage(page);

    for (let i = 0; i < 3; i++) {
      await createCustomer(page);
    }

    await customersPage.goto();
    await expect(customersPage.customersTable).toBeVisible();
    await expect(customersPage.tableRows.nth(2)).toBeVisible();
    expect(await customersPage.tableRows.count()).toBeGreaterThanOrEqual(3);

    await capture(page, 'customers', '01-list', { fullPage: true });
  });

  test('create customer form filled in', async ({ page }) => {
    const customersPage = new CustomersPage(page);
    const data = generateCustomerData();

    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await expect(page.locator('h4:has-text("Create New Customer")')).toBeVisible();

    await customersPage.fillCustomerForm(data);
    await expect(customersPage.companyNameInput).toHaveValue(data.companyName);
    await expect(customersPage.emailInput).toHaveValue(data.email);

    // Captured deliberately before submitting — the form is the subject.
    await capture(page, 'customers', '02-create', { fullPage: true });
  });

  test('customer detail page', async ({ page }) => {
    const customer = await ensureShowcaseCustomer(page);

    await page.goto(`/customers/${customer.id}`);
    await expect(
      page.locator('h4', { hasText: customer.data.companyName })
    ).toBeVisible();
    await expect(page.getByText(customer.data.email).first()).toBeVisible();

    await capture(page, 'customers', '03-detail', { fullPage: true });
  });

  test('customer edit form with loaded values', async ({ page }) => {
    const customer = await ensureShowcaseCustomer(page);
    const customersPage = new CustomersPage(page);

    await page.goto(`/customers/${customer.id}/edit`);
    await expect(page.locator('h4:has-text("Edit Customer")')).toBeVisible();
    await expect(customersPage.companyNameInput).toHaveValue(customer.data.companyName);
    await expect(customersPage.emailInput).toHaveValue(customer.data.email);

    await capture(page, 'customers', '04-edit', { fullPage: true });
  });

  test('delete confirmation dialog', async ({ page }) => {
    const customer = await ensureShowcaseCustomer(page);
    const customersPage = new CustomersPage(page);

    await customersPage.goto();

    // Search rather than trusting the record to sit on the first page.
    await customersPage.searchInput.fill(customer.data.companyName);
    const row = customersPage.tableRows.filter({ hasText: customer.data.companyName });
    await expect(row.first()).toBeVisible({ timeout: 15000 });

    await row.first().locator('[data-testid="DeleteIcon"]').first().click();

    const dialog = page.getByRole('dialog', { name: 'Delete Customer' });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Delete Customer');
    await expect(dialog).toContainText(customer.data.companyName);

    await capture(page, 'customers', '05-delete-confirm');

    // Always cancel: confirming would erase the record permanently.
    await dialog.locator('button:has-text("Cancel")').click();
    await expect(dialog).toBeHidden();
    await expect(row.first()).toBeVisible();
  });
});
