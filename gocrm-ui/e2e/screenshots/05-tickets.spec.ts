import { test, expect, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { generateCustomerData, generateTicketData } from '../fixtures/admin-user';
import { CustomersPage } from '../pages/customers.page';
import { TicketsPage } from '../pages/tickets.page';

/**
 * Documentation captures for the ticket screens.
 *
 * Tickets cannot exist without a customer, so the first test seeds one through
 * the UI and every ticket in the file hangs off it. The tests run serially and
 * share the ids they produce; each one still falls back to creating what it
 * needs so a single capture can be re-run on its own.
 */
test.describe.configure({ mode: 'serial' });

/** Company name of the customer seeded for this file. */
let customerName = '';
/** The ticket the detail, edit and delete captures point at. */
let ticketId = '';
let ticketSubject = '';

/** The ticket form's customer picker (an MUI Autocomplete, not a select). */
function customerPicker(page: Page) {
  return page
    .locator('.MuiAutocomplete-root:has(label:has-text("Customer"))')
    .locator('input')
    .first();
}

/**
 * Picks a customer on the ticket form.
 *
 * The form loads only the first 100 customers, so a freshly created company may
 * not be among the options on a well-used database. When the preferred name has
 * no match the picker falls back to whichever customer is offered first — the
 * captures only need a valid selection, not a specific one.
 */
async function selectCustomer(page: Page, preferredName: string): Promise<void> {
  const input = customerPicker(page);
  await input.click();
  await input.fill(preferredName);

  const firstOption = page.locator('ul[role="listbox"] li[role="option"]').first();
  try {
    await firstOption.waitFor({ state: 'visible', timeout: 3000 });
  } catch {
    await input.fill('');
    await firstOption.waitFor({ state: 'visible', timeout: 10000 });
  }

  await firstOption.click();
  await expect(input).not.toHaveValue('');
}

/** Creates a customer through the UI and returns its company name. */
async function createCustomer(page: Page): Promise<string> {
  const customersPage = new CustomersPage(page);
  const data = generateCustomerData();

  await page.goto('/customers/new');
  await customersPage.companyNameInput.waitFor({ state: 'visible' });
  await customersPage.fillCustomerForm(data);

  const response = await customersPage.saveAndWaitForResponse();
  expect(response.ok()).toBeTruthy();
  await page.waitForURL('**/customers');

  return data.companyName;
}

/** Creates a ticket through the UI and returns its id and subject. */
async function createTicket(
  page: Page,
  company: string
): Promise<{ id: string; subject: string }> {
  const ticketsPage = new TicketsPage(page);
  const data = generateTicketData();

  await page.goto('/tickets/new');
  await ticketsPage.titleInput.waitFor({ state: 'visible' });
  await ticketsPage.fillTicketForm({
    subject: data.subject,
    description: data.description,
    priority: data.priority,
    status: data.status,
  });
  await selectCustomer(page, company);

  const response = await ticketsPage.saveAndWaitForResponse();
  expect(response.ok()).toBeTruthy();

  const body = await response.json();
  const id = String(body?.data?.id ?? '');
  expect(id).not.toBe('');

  await page.waitForURL('**/tickets');
  return { id, subject: data.subject };
}

/** Makes sure the shared customer and ticket exist, creating them if needed. */
async function ensureSeedData(page: Page): Promise<void> {
  if (!customerName) {
    customerName = await createCustomer(page);
  }
  if (!ticketId) {
    const ticket = await createTicket(page, customerName);
    ticketId = ticket.id;
    ticketSubject = ticket.subject;
  }
}

test.describe('Screenshots - Tickets', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('01 - ticket list', async ({ page }) => {
    // Seeding a customer plus three tickets through the UI is slow.
    test.setTimeout(180 * 1000);

    const ticketsPage = new TicketsPage(page);

    customerName = await createCustomer(page);
    await createTicket(page, customerName);
    await createTicket(page, customerName);
    const latest = await createTicket(page, customerName);
    ticketId = latest.id;
    ticketSubject = latest.subject;

    await ticketsPage.goto();

    // Wait for real rows rather than the loading skeletons: the first column
    // renders the ticket number as "#<id>".
    await expect(ticketsPage.tableRows.nth(1)).toBeVisible();
    await expect(ticketsPage.tableRows.first().locator('td').first()).toContainText('#');

    await capture(page, 'tickets', '01-list', { fullPage: true });
  });

  test('02 - create ticket form', async ({ page }) => {
    const ticketsPage = new TicketsPage(page);
    const data = generateTicketData();

    if (!customerName) {
      customerName = await createCustomer(page);
    }

    await page.goto('/tickets/new');
    await expect(page.locator('h4:has-text("Create New Ticket")')).toBeVisible();
    await ticketsPage.fillTicketForm({
      subject: data.subject,
      description: data.description,
      priority: data.priority,
      status: data.status,
    });
    await selectCustomer(page, customerName);

    // Move focus off the picker so the capture shows a settled form.
    await page.locator('h6:has-text("Ticket Information")').click();
    await expect(ticketsPage.titleInput).toHaveValue(data.subject);

    // Deliberately not submitted — the capture documents the filled form.
    await capture(page, 'tickets', '02-create');
  });

  test('03 - ticket detail', async ({ page }) => {
    test.setTimeout(120 * 1000);
    await ensureSeedData(page);

    await page.goto(`/tickets/${ticketId}`);
    await expect(page.locator(`h4:has-text("Ticket #${ticketId}")`)).toBeVisible();
    await expect(page.locator('h6:has-text("Description")')).toBeVisible();

    await capture(page, 'tickets', '03-detail', { fullPage: true });
  });

  test('04 - edit ticket form', async ({ page }) => {
    test.setTimeout(120 * 1000);
    await ensureSeedData(page);

    const ticketsPage = new TicketsPage(page);

    await page.goto(`/tickets/${ticketId}/edit`);
    await expect(page.locator('h4:has-text("Edit Ticket")')).toBeVisible();
    await expect(ticketsPage.titleInput).toHaveValue(ticketSubject);

    await capture(page, 'tickets', '04-edit');
  });

  test('05 - delete confirmation', async ({ page }) => {
    test.setTimeout(120 * 1000);
    await ensureSeedData(page);

    await page.goto(`/tickets/${ticketId}`);
    await expect(page.locator(`h4:has-text("Ticket #${ticketId}")`)).toBeVisible();

    await page.locator('[data-testid="DeleteIcon"]').first().click();

    const dialog = page.getByRole('dialog', { name: 'Delete Ticket' });
    await expect(dialog).toBeVisible();
    await expect(dialog).toContainText('Delete Ticket');

    await capture(page, 'tickets', '05-delete-confirm');

    // Never confirm: deleting is irreversible erasure.
    await dialog.locator('button:has-text("Cancel")').click();
    await expect(dialog).toBeHidden();
  });
});
