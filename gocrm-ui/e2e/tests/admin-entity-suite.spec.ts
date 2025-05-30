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

  test.beforeAll(async ({ browser }) => {
    // This test suite demonstrates a complete CRM workflow
    console.log('Starting comprehensive admin entity test suite...');
  });

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    leadsPage = new LeadsPage(page);
    customersPage = new CustomersPage(page);
    ticketsPage = new TicketsPage(page);
    tasksPage = new TasksPage(page);
    usersPage = new UsersPage(page);
    
    // Ensure admin is logged in
    await adminAuth.ensureAdminLoggedIn();
  });

  test.afterEach(async ({ page }) => {
    // Cleanup - logout after each test
    await adminAuth.logout();
  });

  test('admin can navigate between all entity pages', async ({ page }) => {
    // Test navigation to each entity page
    const entities = [
      { page: leadsPage, name: 'Leads', path: '/leads' },
      { page: customersPage, name: 'Customers', path: '/customers' },
      { page: ticketsPage, name: 'Tickets', path: '/tickets' },
      { page: tasksPage, name: 'Tasks', path: '/tasks' },
      { page: usersPage, name: 'Users', path: '/users' }
    ];

    for (const entity of entities) {
      await entity.page.goto();
      await expect(entity.page.pageTitle).toBeVisible();
      expect(page.url()).toContain(entity.path);
      console.log(`✓ Successfully navigated to ${entity.name} page`);
    }
  });

  test('admin can create complete CRM workflow: Lead → Customer → Ticket → Task', async ({ page }) => {
    // Step 1: Create a Lead
    const leadData = generateLeadData();
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveLead();
    
    console.log('✓ Created lead:', leadData.firstName, leadData.lastName);

    // Step 2: Convert Lead to Customer (simulated by creating customer with same info)
    const customerData = {
      firstName: leadData.firstName,
      lastName: leadData.lastName,
      email: leadData.email,
      phone: leadData.phone,
      company: leadData.company,
      address: '123 Main St',
      city: 'Anytown',
      state: 'CA',
      zipCode: '12345',
      country: 'USA',
      notes: `Converted from lead: ${leadData.notes}`
    };

    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();
    
    console.log('✓ Converted to customer');

    // Step 3: Create a Support Ticket for the Customer
    const ticketData = {
      title: `Support ticket for ${customerData.firstName} ${customerData.lastName}`,
      description: 'Customer needs assistance with onboarding process',
      priority: 'medium',
      status: 'open',
      category: 'Support'
    };

    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();
    await ticketsPage.fillTicketForm(ticketData);
    await ticketsPage.saveTicket();
    
    console.log('✓ Created support ticket');

    // Step 4: Create a Task to handle the Ticket
    const taskData = {
      title: `Follow up with ${customerData.firstName} ${customerData.lastName}`,
      description: 'Complete customer onboarding and resolve support ticket',
      priority: 'medium',
      status: 'pending',
      dueDate: new Date(Date.now() + 7 * 24 * 60 * 60 * 1000).toISOString().split('T')[0] // 7 days from now
    };

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();
    
    console.log('✓ Created follow-up task');

    // Verify all entities were created successfully
    await leadsPage.goto();
    const leadCount = await leadsPage.getLeadCount();
    expect(leadCount).toBeGreaterThanOrEqual(1);

    await customersPage.goto();
    const customerCount = await customersPage.getCustomerCount();
    expect(customerCount).toBeGreaterThanOrEqual(1);

    await ticketsPage.goto();
    const ticketCount = await ticketsPage.getTicketCount();
    expect(ticketCount).toBeGreaterThanOrEqual(1);

    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBeGreaterThanOrEqual(1);

    console.log('✓ Complete CRM workflow verified');
  });

  test('admin can manage user roles and access control', async ({ page }) => {
    // Create users with different roles
    const roles = ['sales', 'support', 'customer'];
    const createdUsers = [];

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
      
      createdUsers.push(userData);
      console.log(`✓ Created ${role} user:`, userData.firstName, userData.lastName);
    }

    // Verify all users were created
    await usersPage.goto();
    const userCount = await usersPage.getUserCount();
    expect(userCount).toBeGreaterThanOrEqual(roles.length);

    // Test role filtering
    for (const role of roles) {
      await usersPage.filterByRole(role);
      const filteredCount = await usersPage.getUserCount();
      expect(filteredCount).toBeGreaterThanOrEqual(1);
      
      // Verify filtered users have correct role
      for (let i = 0; i < Math.min(filteredCount, 3); i++) {
        const userData = await usersPage.getUserData(i);
        expect(userData.role.toLowerCase()).toContain(role);
      }
      console.log(`✓ Role filter for ${role} working correctly`);
    }
  });

  test('admin can perform bulk operations across entities', async ({ page }) => {
    // Create multiple records for each entity
    const batchSize = 3;

    // Create multiple leads
    for (let i = 0; i < batchSize; i++) {
      const leadData = {
        ...generateLeadData(),
        firstName: `BatchLead${i}`,
        lastName: 'TestData'
      };

      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      await leadsPage.saveLead();
    }
    console.log(`✓ Created ${batchSize} test leads`);

    // Create multiple customers
    for (let i = 0; i < batchSize; i++) {
      const customerData = {
        ...generateCustomerData(),
        firstName: `BatchCustomer${i}`,
        lastName: 'TestData'
      };

      await customersPage.goto();
      await customersPage.clickNewCustomer();
      await customersPage.fillCustomerForm(customerData);
      await customersPage.saveCustomer();
    }
    console.log(`✓ Created ${batchSize} test customers`);

    // Create multiple tickets
    for (let i = 0; i < batchSize; i++) {
      const ticketData = {
        ...generateTicketData(),
        title: `Batch Ticket ${i} - Test Data`
      };

      await ticketsPage.goto();
      await ticketsPage.clickNewTicket();
      await ticketsPage.fillTicketForm(ticketData);
      await ticketsPage.saveTicket();
    }
    console.log(`✓ Created ${batchSize} test tickets`);

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
    console.log(`✓ Created ${batchSize} test tasks`);

    // Verify all batches were created
    await leadsPage.goto();
    const leadCount = await leadsPage.getLeadCount();
    expect(leadCount).toBeGreaterThanOrEqual(batchSize);

    await customersPage.goto();
    const customerCount = await customersPage.getCustomerCount();
    expect(customerCount).toBeGreaterThanOrEqual(batchSize);

    await ticketsPage.goto();
    const ticketCount = await ticketsPage.getTicketCount();
    expect(ticketCount).toBeGreaterThanOrEqual(batchSize);

    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBeGreaterThanOrEqual(batchSize);

    console.log('✓ All batch operations completed successfully');
  });

  test('admin can search across all entities', async ({ page }) => {
    const searchTerm = 'AdminSearchTest';

    // Create searchable records in each entity
    const leadData = { ...generateLeadData(), firstName: searchTerm, lastName: 'Lead' };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveLead();

    const customerData = { ...generateCustomerData(), firstName: searchTerm, lastName: 'Customer' };
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();

    const ticketData = { ...generateTicketData(), title: `${searchTerm} Ticket Issue` };
    await ticketsPage.goto();
    await ticketsPage.clickNewTicket();
    await ticketsPage.fillTicketForm(ticketData);
    await ticketsPage.saveTicket();

    const taskData = { ...generateTaskData(), title: `${searchTerm} Task Assignment` };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveTask();

    // Test search functionality in each entity
    await leadsPage.goto();
    await leadsPage.searchLeads(searchTerm);
    const leadResults = await leadsPage.getLeadCount();
    expect(leadResults).toBeGreaterThanOrEqual(1);
    console.log(`✓ Found ${leadResults} leads matching "${searchTerm}"`);

    await customersPage.goto();
    await customersPage.searchCustomers(searchTerm);
    const customerResults = await customersPage.getCustomerCount();
    expect(customerResults).toBeGreaterThanOrEqual(1);
    console.log(`✓ Found ${customerResults} customers matching "${searchTerm}"`);

    await ticketsPage.goto();
    await ticketsPage.searchTickets(searchTerm);
    const ticketResults = await ticketsPage.getTicketCount();
    expect(ticketResults).toBeGreaterThanOrEqual(1);
    console.log(`✓ Found ${ticketResults} tickets matching "${searchTerm}"`);

    await tasksPage.goto();
    await tasksPage.searchTasks(searchTerm);
    const taskResults = await tasksPage.getTaskCount();
    expect(taskResults).toBeGreaterThanOrEqual(1);
    console.log(`✓ Found ${taskResults} tasks matching "${searchTerm}"`);
  });

  test('admin can handle error scenarios gracefully', async ({ page }) => {
    // Test invalid data handling across entities
    const entities = [
      { page: leadsPage, path: '/leads' },
      { page: customersPage, path: '/customers' },
      { page: ticketsPage, path: '/tickets' },
      { page: tasksPage, path: '/tasks' },
      { page: usersPage, path: '/users' }
    ];

    for (const entity of entities) {
      // Try to save without required fields
      await entity.page.goto();
      
      if (entity.page.newLeadButton) await entity.page.clickNewLead();
      else if (entity.page.newCustomerButton) await entity.page.clickNewCustomer();
      else if (entity.page.newTicketButton) await entity.page.clickNewTicket();
      else if (entity.page.newTaskButton) await entity.page.clickNewTask();
      else if (entity.page.newUserButton) await entity.page.clickNewUser();

      // Try to save without filling required fields
      if (entity.page.saveButton) {
        await entity.page.saveButton.click();
        
        // Should stay on form page due to validation
        expect(page.url()).toContain('/new');
        console.log(`✓ ${entity.path} properly validates required fields`);
      }
    }
  });
});