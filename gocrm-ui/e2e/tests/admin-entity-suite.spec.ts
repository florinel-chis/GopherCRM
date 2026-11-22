import { test, expect, type Locator, type Response } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LOADED } from '../helpers/api';
import { recordCreates, submitEmptyForm } from '../helpers/forms';
import { LeadsPage } from '../pages/leads.page';
import { CustomersPage } from '../pages/customers.page';
import { TicketsPage } from '../pages/tickets.page';
import { TasksPage } from '../pages/tasks.page';
import { UsersPage } from '../pages/users.page';
import {
  generateLeadData,
  generateCustomerData,
  generateTaskData,
  generateUserData
} from '../fixtures/admin-user';

// The three tests that walk several entity forms (the CRM workflow, the batch
// creation and the cross-entity search) take 60-100 s locally and about 1.7x
// that on a CI runner, far over the default 30 s. They get a budget sized for
// the slowest measured environment instead of passing only on a retry.
const MULTI_ENTITY_TEST_TIMEOUT_MS = 180_000;

/** Asserts a create answered 201. */
function expectCreated(response: Response) {
  expect(response.status(), `POST ${new URL(response.url()).pathname}`).toBe(201);
}

/**
 * Asserts the searched list shows exactly the one row carrying `uniqueText`:
 * the row itself first (so an unfiltered page cannot pass), then the count.
 */
async function expectOnlyRow(rows: Locator, uniqueText: string) {
  await expect(rows.filter({ hasText: uniqueText })).toHaveCount(1, LOADED);
  await expect(rows).toHaveCount(1);
}

test.describe('Admin - Complete Entity Management Suite', () => {
  let adminAuth: AdminAuthHelper;
  let leadsPage: LeadsPage;
  let customersPage: CustomersPage;
  let ticketsPage: TicketsPage;
  let tasksPage: TasksPage;
  let usersPage: UsersPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    leadsPage = new LeadsPage(page);
    customersPage = new CustomersPage(page);
    ticketsPage = new TicketsPage(page);
    tasksPage = new TasksPage(page);
    usersPage = new UsersPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can navigate between all entity pages', async ({ page }) => {
    const entities = [
      { pageObj: leadsPage, name: 'Leads', path: '/leads' },
      { pageObj: customersPage, name: 'Customers', path: '/customers' },
      { pageObj: ticketsPage, name: 'Tickets', path: '/tickets' },
      { pageObj: tasksPage, name: 'Tasks', path: '/tasks' },
      { pageObj: usersPage, name: 'Users', path: '/users' }
    ];

    for (const entity of entities) {
      await entity.pageObj.goto();
      await expect(entity.pageObj.pageTitle).toBeVisible();
      expect(page.url()).toContain(entity.path);
    }
  });

  test('admin can create complete CRM workflow: Lead -> Customer -> Task', async () => {
    test.setTimeout(MULTI_ENTITY_TEST_TIMEOUT_MS);
    const stamp = Date.now();

    // Step 1: Create a Lead
    const leadData = { ...generateLeadData(), companyName: `WorkflowLead_${stamp}` };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    expectCreated(await leadsPage.saveAndWaitForResponse());

    // Step 2: Create a Customer
    const customerData = { ...generateCustomerData(), companyName: `WorkflowCustomer_${stamp}` };
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    expectCreated(await customersPage.saveAndWaitForResponse());

    // Step 3: Create a Task
    const taskData = { ...generateTaskData(), title: `Workflow Task ${stamp}` };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    expectCreated(await tasksPage.saveAndWaitForResponse());

    // Each list finds the record this test created.
    await leadsPage.goto();
    await leadsPage.searchLeads(leadData.companyName);
    await expectOnlyRow(leadsPage.tableRows, leadData.companyName);

    await customersPage.goto();
    await customersPage.searchCustomers(customerData.companyName);
    await expectOnlyRow(customersPage.tableRows, customerData.companyName);

    await tasksPage.goto();
    await tasksPage.searchTasks(taskData.title);
    await expectOnlyRow(tasksPage.tableRows, taskData.title);
  });

  test('admin can create a user for each role', async () => {
    const roles = ['sales', 'support', 'customer'];

    for (const role of roles) {
      const roleLabel = role.charAt(0).toUpperCase() + role.slice(1);
      const userData = {
        ...generateUserData(),
        firstName: roleLabel,
        lastName: 'TestUser',
        role
      };

      await usersPage.goto();
      await usersPage.clickNewUser();
      await usersPage.fillUserForm({
        ...userData,
        confirmPassword: userData.password
      });
      const created = await usersPage.saveAndWaitForResponse();
      expectCreated(created);
      expect((await created.json()).data.role).toBe(role);

      // The list shows the user with the role it was given.
      await usersPage.goto();
      await usersPage.searchUsers(userData.email);
      await expectOnlyRow(usersPage.tableRows, userData.email);
      await expect(
        usersPage.tableRows.first().getByRole('cell', { name: roleLabel, exact: true })
      ).toBeVisible();
    }
  });

  test('admin can create several records of each entity', async () => {
    test.setTimeout(MULTI_ENTITY_TEST_TIMEOUT_MS);
    const batchSize = 2;
    // Contact names repeat across runs, so every record is found by a value
    // stamped for this run.
    const stamp = Date.now();

    // Create multiple leads
    const leadCompanies: string[] = [];
    for (let i = 0; i < batchSize; i++) {
      const leadData = {
        ...generateLeadData(),
        companyName: `BatchLeadCo_${stamp}_${i}`,
        contactName: `BatchLead${i} TestData`
      };
      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      expectCreated(await leadsPage.saveAndWaitForResponse());
      leadCompanies.push(leadData.companyName);
    }

    // Create multiple customers
    const customerCompanies: string[] = [];
    for (let i = 0; i < batchSize; i++) {
      const customerData = {
        ...generateCustomerData(),
        companyName: `BatchCustomerCo_${stamp}_${i}`,
        contactName: `BatchCustomer${i} TestData`
      };
      await customersPage.goto();
      await customersPage.clickNewCustomer();
      await customersPage.fillCustomerForm(customerData);
      expectCreated(await customersPage.saveAndWaitForResponse());
      customerCompanies.push(customerData.companyName);
    }

    // Create multiple tasks
    const taskTitles: string[] = [];
    for (let i = 0; i < batchSize; i++) {
      const taskData = {
        ...generateTaskData(),
        title: `Batch Task ${stamp}_${i} - Test Data`
      };
      await tasksPage.goto();
      await tasksPage.clickNewTask();
      await tasksPage.fillTaskForm(taskData);
      expectCreated(await tasksPage.saveAndWaitForResponse());
      taskTitles.push(taskData.title);
    }

    // Every record of every batch is in its list.
    await leadsPage.goto();
    for (const company of leadCompanies) {
      await leadsPage.searchLeads(company);
      await expectOnlyRow(leadsPage.tableRows, company);
    }

    await customersPage.goto();
    for (const company of customerCompanies) {
      await customersPage.searchCustomers(company);
      await expectOnlyRow(customersPage.tableRows, company);
    }

    await tasksPage.goto();
    for (const title of taskTitles) {
      await tasksPage.searchTasks(title);
      await expectOnlyRow(tasksPage.tableRows, title);
    }
  });

  test('admin can search across all entities', async () => {
    test.setTimeout(MULTI_ENTITY_TEST_TIMEOUT_MS);
    const searchTerm = `AdminSearch_${Date.now()}`;

    // Create searchable lead
    const leadData = { ...generateLeadData(), companyName: `${searchTerm} Corp` };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    expectCreated(await leadsPage.saveAndWaitForResponse());

    // Create searchable customer
    const customerData = { ...generateCustomerData(), companyName: `${searchTerm} Inc` };
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    expectCreated(await customersPage.saveAndWaitForResponse());

    // Create searchable task
    const taskData = { ...generateTaskData(), title: `${searchTerm} Task Assignment` };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    expectCreated(await tasksPage.saveAndWaitForResponse());

    // Test search in leads
    await leadsPage.goto();
    await leadsPage.searchLeads(searchTerm);
    await expectOnlyRow(leadsPage.tableRows, searchTerm);

    // Test search in customers
    await customersPage.goto();
    await customersPage.searchCustomers(searchTerm);
    await expectOnlyRow(customersPage.tableRows, searchTerm);

    // Test search in tasks
    await tasksPage.goto();
    await tasksPage.searchTasks(searchTerm);
    await expectOnlyRow(tasksPage.tableRows, searchTerm);
  });

  test('admin can handle error scenarios gracefully', async ({ page }) => {
    // Each form, submitted empty, shows its field message and sends nothing.
    const leadCreates = recordCreates(page, '/leads');
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await submitEmptyForm(page, leadsPage.companyNameInput, leadsPage.saveButton);
    await expect(page.getByText('Company name is required', { exact: true })).toBeVisible();
    expect(leadCreates).toHaveLength(0);
    expect(page.url()).toContain('/new');

    const customerCreates = recordCreates(page, '/customers');
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await submitEmptyForm(page, customersPage.companyNameInput, customersPage.saveButton);
    await expect(page.getByText('Company name is required', { exact: true })).toBeVisible();
    expect(customerCreates).toHaveLength(0);
    expect(page.url()).toContain('/new');

    const taskCreates = recordCreates(page, '/tasks');
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await submitEmptyForm(page, tasksPage.titleInput, tasksPage.saveButton);
    await expect(page.getByText('Title is required', { exact: true })).toBeVisible();
    expect(taskCreates).toHaveLength(0);
    expect(page.url()).toContain('/new');

    const userCreates = recordCreates(page, '/users');
    await usersPage.goto();
    await usersPage.clickNewUser();
    await submitEmptyForm(page, usersPage.emailInput, usersPage.saveButton);
    await expect(page.getByText('First name is required', { exact: true })).toBeVisible();
    expect(userCreates).toHaveLength(0);
    expect(page.url()).toContain('/new');
  });
});
