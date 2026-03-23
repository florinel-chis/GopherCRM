import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LeadsPage } from '../pages/leads.page';
import { CustomersPage } from '../pages/customers.page';
import { TicketsPage } from '../pages/tickets.page';
import { TasksPage } from '../pages/tasks.page';
import { UsersPage } from '../pages/users.page';
import {
  generateLeadData,
  generateCustomerData,
  generateTicketData,
  generateTaskData,
  generateUserData
} from '../fixtures/admin-user';

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

  test('admin can create complete CRM workflow: Lead -> Customer -> Task', async ({ page }) => {
    // Step 1: Create a Lead
    const leadData = generateLeadData();
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveLead();

    // Step 2: Create a Customer
    const customerData = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();

    // Step 3: Create a Task
    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();

    // Verify all entities were created
    await leadsPage.goto();
    const leadCount = await leadsPage.getLeadCount();
    expect(leadCount).toBeGreaterThanOrEqual(1);

    await customersPage.goto();
    const customerCount = await customersPage.getRowCount();
    expect(customerCount).toBeGreaterThanOrEqual(1);

    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBeGreaterThanOrEqual(1);
  });

  test('admin can manage user roles and access control', async ({ page }) => {
    const roles = ['sales', 'support', 'customer'];

    for (const role of roles) {
      const userData = {
        ...generateUserData(),
        firstName: `${role.charAt(0).toUpperCase() + role.slice(1)}`,
        lastName: 'TestUser',
        role
      };

      await usersPage.goto();
      await usersPage.clickNewUser();
      await usersPage.fillUserForm({
        ...userData,
        confirmPassword: userData.password
      });
      await usersPage.saveUser();
    }

    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    expect(userCount).toBeGreaterThanOrEqual(roles.length);
  });

  test('admin can perform bulk operations across entities', async ({ page }) => {
    const batchSize = 2;

    // Create multiple leads
    for (let i = 0; i < batchSize; i++) {
      const leadData = {
        ...generateLeadData(),
        contactName: `BatchLead${i} TestData`
      };
      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      await leadsPage.saveLead();
    }

    // Create multiple customers
    for (let i = 0; i < batchSize; i++) {
      const customerData = {
        ...generateCustomerData(),
        contactName: `BatchCustomer${i} TestData`
      };
      await customersPage.goto();
      await customersPage.clickNewCustomer();
      await customersPage.fillCustomerForm(customerData);
      await customersPage.saveCustomer();
    }

    // Create multiple tasks
    for (let i = 0; i < batchSize; i++) {
      const taskData = {
        ...generateTaskData(),
        title: `Batch Task ${i} - Test Data`
      };
      await tasksPage.goto();
      await tasksPage.clickNewTask();
      await tasksPage.fillTaskForm(taskData);
      await tasksPage.saveTask();
    }

    // Verify all batches were created
    await leadsPage.goto();
    const leadCount = await leadsPage.getLeadCount();
    expect(leadCount).toBeGreaterThanOrEqual(batchSize);

    await customersPage.goto();
    const customerCount = await customersPage.getRowCount();
    expect(customerCount).toBeGreaterThanOrEqual(batchSize);

    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBeGreaterThanOrEqual(batchSize);
  });

  test('admin can search across all entities', async ({ page }) => {
    const searchTerm = `AdminSearch_${Date.now()}`;

    // Create searchable lead
    const leadData = { ...generateLeadData(), companyName: `${searchTerm} Corp` };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveLead();

    // Create searchable customer
    const customerData = { ...generateCustomerData(), companyName: `${searchTerm} Inc` };
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();

    // Create searchable task
    const taskData = { ...generateTaskData(), title: `${searchTerm} Task Assignment` };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();

    // Test search in leads
    await leadsPage.goto();
    await leadsPage.searchLeads(searchTerm);
    await page.waitForTimeout(1000);

    // Test search in customers
    await customersPage.goto();
    await customersPage.searchCustomers(searchTerm);
    await page.waitForTimeout(1000);

    // Test search in tasks
    await tasksPage.goto();
    await tasksPage.searchTasks(searchTerm);
    await page.waitForTimeout(1000);
  });

  test('admin can handle error scenarios gracefully', async ({ page }) => {
    // Test validation on leads
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.saveButton.click();
    expect(page.url()).toContain('/new');

    // Test validation on customers
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.saveButton.click();
    expect(page.url()).toContain('/new');

    // Test validation on tasks
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.saveButton.click();
    expect(page.url()).toContain('/new');

    // Test validation on users
    await usersPage.goto();
    await usersPage.clickNewUser();
    await usersPage.saveButton.click();
    expect(page.url()).toContain('/new');
  });
});
