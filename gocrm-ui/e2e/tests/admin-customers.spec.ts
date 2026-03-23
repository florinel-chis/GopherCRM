import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { CustomersPage } from '../pages/customers.page';
import { generateCustomerData } from '../fixtures/admin-user';

test.describe('Admin - Customers Management', () => {
  let adminAuth: AdminAuthHelper;
  let customersPage: CustomersPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    customersPage = new CustomersPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view customers list page', async ({ page }) => {
    await customersPage.goto();
    await expect(customersPage.pageTitle).toBeVisible();
    await expect(customersPage.newCustomerButton).toBeVisible();
    await expect(customersPage.customersTable).toBeVisible();
  });

  test('admin can create a new customer successfully', async ({ page }) => {
    const data = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);

    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    expect(page.url()).toContain('/customers');
  });

  test('admin can edit an existing customer', async ({ page }) => {
    // Create a customer first
    const data = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    // Go back and edit
    await customersPage.goto();
    await customersPage.clickEditOnRow(0);

    // Update company name
    await customersPage.companyNameInput.clear();
    await customersPage.companyNameInput.fill('Updated Company Inc.');
    await customersPage.saveButton.click();

    // Verify we navigated away from the edit page
    await page.waitForURL(/\/customers(?!.*edit)/, { timeout: 10000 });
  });

  test('admin can view customer details', async ({ page }) => {
    const data = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    await customersPage.goto();
    await customersPage.clickViewOnRow(0);
    expect(page.url()).toMatch(/\/customers\/\d+$/);
  });

  test('admin can delete a customer', async ({ page }) => {
    const data = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    await customersPage.goto();
    const initialCount = await customersPage.getRowCount();
    expect(initialCount).toBeGreaterThan(0);

    await customersPage.clickDeleteOnRow(0);
    await customersPage.confirmDelete();
    await page.waitForTimeout(1000);
  });

  test('admin can search customers', async ({ page }) => {
    // Create a customer with a unique name
    const data = { ...generateCustomerData(), companyName: `SearchTestCo_${Date.now()}` };
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    await customersPage.goto();
    await customersPage.searchCustomers(data.companyName);
    await page.waitForTimeout(1000);
  });

  test('admin sees validation errors for invalid customer data', async ({ page }) => {
    await customersPage.goto();
    await customersPage.clickNewCustomer();

    // Try to save without required fields
    await customersPage.saveButton.click();

    // Should stay on form page
    expect(page.url()).toContain('/customers/new');
  });

  test('admin can handle customer form cancellation', async ({ page }) => {
    await customersPage.goto();
    await customersPage.clickNewCustomer();

    await customersPage.companyNameInput.fill('Test Company');
    await customersPage.cancelButton.click();

    await page.waitForURL('**/customers', { timeout: 10000 });
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create customer with minimal required data', async ({ page }) => {
    const minimalData = {
      companyName: `MinCo_${Date.now()}`,
      contactName: 'Min Contact',
      email: `min_${Date.now()}@example.com`,
      phone: '555-0100',
    };

    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(minimalData);

    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
  });

  test('admin can handle duplicate customer email validation', async ({ page }) => {
    const data = generateCustomerData();

    // Create first customer
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    // Try same email
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    const dupData = { ...generateCustomerData(), email: data.email };
    await customersPage.fillCustomerForm(dupData);
    await customersPage.saveButton.click();

    // Should show error or stay on form
    await page.waitForTimeout(2000);
    const url = page.url();
    const error = await customersPage.getErrorMessage();
    expect(url.includes('/new') || (error && error.toLowerCase().includes('email'))).toBeTruthy();
  });
});
