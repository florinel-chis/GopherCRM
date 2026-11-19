import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { faker } from '@faker-js/faker';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { TicketsPage } from '../pages/tickets.page';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

// Every case that reads or changes a ticket seeds its own through the API
// first and addresses it by a subject no other record carries, so the result
// does not depend on what else is in the list. A ticket needs a customer: the
// cases that only need a ticket share the one seeded in beforeAll, the two
// that drive the create form seed their own and pick it in the Customer field.
test.describe('Admin - Tickets Management', () => {
  let adminAuth: AdminAuthHelper;
  let ticketsPage: TicketsPage;
  let api: APIRequestContext;
  let headers: Record<string, string>;
  let sharedCustomerId = 0;

  // A value no other record carries; safe inside a LIKE search.
  const uniqueToken = () => `${Date.now()}${faker.string.alphanumeric(6)}`;

  const seedCustomer = async (company: string): Promise<number> => {
    const created = await api.post(`${API_BASE_URL}/customers`, {
      headers,
      data: {
        first_name: 'Ticket',
        last_name: 'Requester',
        email: `cust_${uniqueToken()}@example.com`,
        company,
      },
    });
    expect(created.status(), 'seed customer').toBe(201);
    return (await created.json()).data.id;
  };

  // Create always yields status `open`; another status is reached by an update.
  const seedTicket = async (
    title: string,
    options: { priority?: string; status?: string; customerId?: number } = {}
  ): Promise<number> => {
    const created = await api.post(`${API_BASE_URL}/tickets`, {
      headers,
      data: {
        title,
        description: `Seeded for the browser case "${title}"`,
        priority: options.priority ?? 'medium',
        customer_id: options.customerId ?? sharedCustomerId,
      },
    });
    expect(created.status(), 'seed ticket').toBe(201);
    const id: number = (await created.json()).data.id;
    if (options.status) {
      const moved = await api.put(`${API_BASE_URL}/tickets/${id}`, {
        headers,
        data: { status: options.status },
      });
      expect(moved.status(), 'seed ticket status').toBe(200);
    }
    return id;
  };

  const fetchTicket = (id: number) => api.get(`${API_BASE_URL}/tickets/${id}`, { headers });

  test.beforeAll(async () => {
    api = await playwrightRequest.newContext();
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    sharedCustomerId = await seedCustomer(`E2E Tickets Shared ${uniqueToken()}`);
  });

  test.afterAll(async () => {
    await api.dispose();
  });

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    ticketsPage = new TicketsPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view tickets list page', async () => {
    await ticketsPage.goto();

    await expect(ticketsPage.pageTitle).toBeVisible();
    await expect(ticketsPage.newTicketButton).toBeVisible();
    await expect(ticketsPage.ticketsTable).toBeVisible();
  });

  test('admin can create a new ticket successfully', async ({ page }) => {
    const token = uniqueToken();
    const company = `E2E Tickets Co ${token}`;
    const subject = `E2E created ticket ${token}`;
    await seedCustomer(company);

    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();
    await ticketsPage.fillTicketForm({
      subject,
      description: 'Created through the ticket form.',
      priority: 'high',
    });
    await ticketsPage.selectAutocompleteOption('Customer', company);

    const response = await ticketsPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    await expect(page).toHaveURL(/\/tickets$/);
    await expect(await ticketsPage.rowMatching(subject)).toBeVisible();
  });

  test('admin can edit an existing ticket', async ({ page }) => {
    const token = uniqueToken();
    const subject = `E2E edit ticket ${token}`;
    const updatedSubject = `E2E edited ticket ${token}`;
    const ticketId = await seedTicket(subject, { priority: 'low' });

    await ticketsPage.goto();
    await ticketsPage.editTicket(subject);
    await expect(ticketsPage.titleInput).toHaveValue(subject);
    await ticketsPage.titleInput.fill(updatedSubject);
    await ticketsPage.selectMuiOption('priority', 'high');

    const response = await ticketsPage.saveAndWaitForUpdate(ticketId);
    expect(response.status()).toBe(200);
    await expect(page).toHaveURL(/\/tickets$/);
    await expect(await ticketsPage.rowMatching(updatedSubject)).toBeVisible();
  });

  test('admin can view ticket details', async ({ page }) => {
    const subject = `E2E view ticket ${uniqueToken()}`;
    const ticketId = await seedTicket(subject);

    await ticketsPage.goto();
    await ticketsPage.viewTicket(subject);

    await expect(page).toHaveURL(new RegExp(`/tickets/${ticketId}$`));
    await expect(page.getByText(subject).first()).toBeVisible();
  });

  test('admin can delete a ticket', async () => {
    const subject = `E2E delete ticket ${uniqueToken()}`;
    const ticketId = await seedTicket(subject);

    await ticketsPage.goto();
    await ticketsPage.deleteTicket(subject);

    // The old row stays rendered until the refetch lands, so the check retries.
    await expect(ticketsPage.tableRows.filter({ hasText: subject })).toHaveCount(0);
    expect((await fetchTicket(ticketId)).status()).toBe(404);
  });

  test('admin can search tickets', async () => {
    const token = uniqueToken();
    await seedTicket(`E2E search ticket ${token}`);

    await ticketsPage.goto();
    await ticketsPage.searchTickets(token);

    await expect(ticketsPage.tableRows.filter({ hasText: token })).toHaveCount(1);
    await expect(ticketsPage.tableRows).toHaveCount(1);
  });

  test('admin can filter tickets by status', async () => {
    const token = uniqueToken();
    const openSubject = `${token} stays open`;
    const resolvedSubject = `${token} was resolved`;
    await seedTicket(openSubject);
    await seedTicket(resolvedSubject, { status: 'resolved' });
    const openRow = ticketsPage.tableRows.filter({ hasText: openSubject });
    const resolvedRow = ticketsPage.tableRows.filter({ hasText: resolvedSubject });

    await ticketsPage.goto();
    await ticketsPage.searchTickets(token);
    await expect(openRow).toHaveCount(1);
    await expect(resolvedRow).toHaveCount(1);

    await ticketsPage.filterByStatus('resolved');
    await expect(resolvedRow).toBeVisible();
    await expect(openRow).toHaveCount(0);
  });

  test('admin can filter tickets by priority', async () => {
    const token = uniqueToken();
    const lowSubject = `${token} can wait`;
    const urgentSubject = `${token} cannot wait`;
    await seedTicket(lowSubject, { priority: 'low' });
    await seedTicket(urgentSubject, { priority: 'urgent' });
    const lowRow = ticketsPage.tableRows.filter({ hasText: lowSubject });
    const urgentRow = ticketsPage.tableRows.filter({ hasText: urgentSubject });

    await ticketsPage.goto();
    await ticketsPage.searchTickets(token);
    await expect(lowRow).toHaveCount(1);
    await expect(urgentRow).toHaveCount(1);

    await ticketsPage.filterByPriority('urgent');
    await expect(urgentRow).toBeVisible();
    await expect(lowRow).toHaveCount(0);
  });

  test('admin sees validation errors for invalid ticket data', async ({ page }) => {
    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();

    // Try to save without filling required fields
    await ticketsPage.saveButton.click();

    // Should stay on form page
    expect(page.url()).toContain('/tickets/new');
  });

  test('admin can handle ticket form cancellation', async ({ page }) => {
    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();

    await ticketsPage.titleInput.fill('Test Ticket to Cancel');
    await ticketsPage.cancelButton.click();

    await page.waitForURL('**/tickets', { timeout: 10000 });
    expect(page.url()).not.toContain('/new');
  });

  test('admin can navigate between tickets efficiently', async ({ page }) => {
    const subject = `E2E navigate ticket ${uniqueToken()}`;
    const ticketId = await seedTicket(subject);

    await ticketsPage.goto();
    await ticketsPage.viewTicket(subject);
    await expect(page).toHaveURL(new RegExp(`/tickets/${ticketId}$`));

    await page.goBack();
    await expect(ticketsPage.ticketsTable).toBeVisible();

    await ticketsPage.editTicket(subject);
    await expect(page).toHaveURL(new RegExp(`/tickets/${ticketId}/edit$`));

    await ticketsPage.cancelButton.click();
    await expect(ticketsPage.ticketsTable).toBeVisible();
    await expect(page).toHaveURL(/\/tickets$/);
  });

  test('admin can update ticket status', async () => {
    const subject = `E2E status ticket ${uniqueToken()}`;
    const ticketId = await seedTicket(subject);

    await ticketsPage.goto();
    await ticketsPage.editTicket(subject);
    await expect(ticketsPage.titleInput).toHaveValue(subject);
    await ticketsPage.selectMuiOption('status', 'resolved');

    const response = await ticketsPage.saveAndWaitForUpdate(ticketId);
    expect(response.status()).toBe(200);
    const stored = await fetchTicket(ticketId);
    expect(stored.status()).toBe(200);
    expect((await stored.json()).data.status).toBe('resolved');
  });

  test('admin can update ticket priority', async () => {
    const subject = `E2E priority ticket ${uniqueToken()}`;
    const ticketId = await seedTicket(subject, { priority: 'low' });

    await ticketsPage.goto();
    await ticketsPage.editTicket(subject);
    await expect(ticketsPage.titleInput).toHaveValue(subject);
    await ticketsPage.selectMuiOption('priority', 'urgent');

    const response = await ticketsPage.saveAndWaitForUpdate(ticketId);
    expect(response.status()).toBe(200);
    const stored = await fetchTicket(ticketId);
    expect(stored.status()).toBe(200);
    expect((await stored.json()).data.priority).toBe('urgent');
  });

  test('admin can handle ticket with long description', async () => {
    const token = uniqueToken();
    const company = `E2E Tickets Co ${token}`;
    const longDescription = faker.lorem.paragraphs(10);
    await seedCustomer(company);

    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();
    await ticketsPage.fillTicketForm({
      subject: `E2E long description ticket ${token}`,
      description: longDescription,
      priority: 'medium',
    });
    await ticketsPage.selectAutocompleteOption('Customer', company);

    const response = await ticketsPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    const ticketId: number = (await response.json()).data.id;
    const stored = await fetchTicket(ticketId);
    expect(stored.status()).toBe(200);
    expect((await stored.json()).data.description).toBe(longDescription);
  });
});
