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
    
    // Ensure admin is logged in
    await adminAuth.ensureAdminLoggedIn();
  });

  test.afterEach(async ({ page }) => {
    // Cleanup - logout after each test
    await adminAuth.logout();
  });

  test('admin can view customers list page', async ({ page }) => {
    await customersPage.goto();
    
    // Verify page loads correctly
    await expect(customersPage.pageTitle).toBeVisible();
    await expect(customersPage.newCustomerButton).toBeVisible();
    
    // Verify we can see the table (even if empty)
    await expect(customersPage.customersTable).toBeVisible();
  });

  test('admin can create a new customer successfully', async ({ page }) => {
    const customerData = generateCustomerData();
    
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    // Fill the customer form
    await customersPage.fillCustomerForm(customerData);
    
    // Save and wait for response
    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify redirect to customers list or detail
    expect(page.url()).toContain('/customers');
    
    // Verify success message
    const successMessage = await customersPage.getSuccessMessage();
    expect(successMessage).toBeTruthy();
  });

  test('admin can edit an existing customer', async ({ page }) => {
    // First create a customer
    const originalCustomerData = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(originalCustomerData);
    await customersPage.saveCustomer();
    
    // Go back to customers list
    await customersPage.goto();
    
    // Edit the first customer
    await customersPage.editCustomer(0);
    
    // Modify the customer data
    const updatedCustomerData = {
      ...originalCustomerData,
      firstName: 'UpdatedFirst',
      lastName: 'UpdatedLast',
      company: 'Updated Company Inc.'
    };
    
    await customersPage.fillCustomerForm(updatedCustomerData);
    await customersPage.saveCustomer();
    
    // Verify the update
    await customersPage.goto();
    const customerData = await customersPage.getCustomerData(0);
    expect(customerData.firstName).toBe('UpdatedFirst');
    expect(customerData.lastName).toBe('UpdatedLast');
    expect(customerData.company).toBe('Updated Company Inc.');
  });

  test('admin can view customer details', async ({ page }) => {
    // Create a customer first
    const customerData = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();
    
    // Go back to customers list and view the customer
    await customersPage.goto();
    await customersPage.viewCustomer(0);
    
    // Verify we're on the customer detail page
    expect(page.url()).toMatch(/\/customers\/\d+$/);
    
    // Verify customer information is displayed
    await expect(page.locator(`text=${customerData.firstName}`)).toBeVisible();
    await expect(page.locator(`text=${customerData.lastName}`)).toBeVisible();
    await expect(page.locator(`text=${customerData.email}`)).toBeVisible();
  });

  test('admin can delete a customer', async ({ page }) => {
    // Create a customer first
    const customerData = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();
    
    // Go back to customers list
    await customersPage.goto();
    const initialCount = await customersPage.getCustomerCount();
    
    // Delete the customer
    await customersPage.deleteCustomer(0);
    
    // Verify customer is removed
    await page.waitForTimeout(1000); // Wait for table to update
    const finalCount = await customersPage.getCustomerCount();
    expect(finalCount).toBe(initialCount - 1);
  });

  test('admin can search customers', async ({ page }) => {
    // Create multiple customers with distinct data
    const customer1Data = { ...generateCustomerData(), firstName: 'SearchCustomer1', company: 'SearchCompany1' };
    const customer2Data = { ...generateCustomerData(), firstName: 'SearchCustomer2', company: 'SearchCompany2' };
    
    // Create first customer
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customer1Data);
    await customersPage.saveCustomer();
    
    // Create second customer
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customer2Data);
    await customersPage.saveCustomer();
    
    // Test search functionality
    await customersPage.goto();
    await customersPage.searchCustomers('SearchCustomer1');
    
    // Should find the first customer
    const searchResults = await customersPage.getCustomerCount();
    expect(searchResults).toBeGreaterThanOrEqual(1);
    
    const firstResult = await customersPage.getCustomerData(0);
    expect(firstResult.firstName).toContain('SearchCustomer1');
  });

  test('admin can create customer with full address information', async ({ page }) => {
    const completeCustomerData = generateCustomerData();
    
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    // Fill all fields including address information
    await customersPage.fillCustomerForm(completeCustomerData);
    
    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify all data was saved by viewing the customer
    await customersPage.goto();
    await customersPage.viewCustomer(0);
    
    // Check that address fields are displayed
    await expect(page.locator(`text=${completeCustomerData.address}`)).toBeVisible();
    await expect(page.locator(`text=${completeCustomerData.city}`)).toBeVisible();
    await expect(page.locator(`text=${completeCustomerData.state}`)).toBeVisible();
    await expect(page.locator(`text=${completeCustomerData.zipCode}`)).toBeVisible();
  });

  test('admin sees validation errors for invalid customer data', async ({ page }) => {
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    // Try to save without required fields
    await customersPage.saveCustomer();
    
    // Should show validation errors or prevent submission
    const currentUrl = page.url();
    expect(currentUrl).toContain('/customers/new'); // Should stay on form page
  });

  test('admin can handle customer form cancellation', async ({ page }) => {
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    // Fill some data
    const customerData = generateCustomerData();
    await customersPage.firstNameInput.fill(customerData.firstName);
    await customersPage.lastNameInput.fill(customerData.lastName);
    
    // Cancel the form
    await customersPage.cancelButton.click();
    
    // Should return to customers list
    expect(page.url()).toContain('/customers');
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create customer with minimal required data', async ({ page }) => {
    const minimalCustomerData = {
      firstName: 'Minimal',
      lastName: 'Customer',
      email: `minimal_${Date.now()}@example.com`
    };
    
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    // Fill only required fields
    await customersPage.fillCustomerForm(minimalCustomerData);
    
    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    
    // Verify customer was created
    await customersPage.goto();
    const customerData = await customersPage.getCustomerData(0);
    expect(customerData.firstName).toBe('Minimal');
    expect(customerData.lastName).toBe('Customer');
    expect(customerData.email).toBe(minimalCustomerData.email);
  });

  test('admin can navigate between customers efficiently', async ({ page }) => {
    // Create multiple customers
    for (let i = 0; i < 3; i++) {
      const customerData = { ...generateCustomerData(), firstName: `TestCustomer${i}` };
      await customersPage.goto();
      await customersPage.clickNewCustomer();
      await customersPage.fillCustomerForm(customerData);
      await customersPage.saveCustomer();
    }
    
    // Navigate to customers list
    await customersPage.goto();
    
    // View first customer
    await customersPage.viewCustomer(0);
    const firstCustomerUrl = page.url();
    
    // Go back to list
    await customersPage.goto();
    
    // View second customer
    await customersPage.viewCustomer(1);
    const secondCustomerUrl = page.url();
    
    // Verify different customers have different URLs
    expect(firstCustomerUrl).not.toBe(secondCustomerUrl);
    
    // Both should be valid customer detail URLs
    expect(firstCustomerUrl).toMatch(/\/customers\/\d+$/);
    expect(secondCustomerUrl).toMatch(/\/customers\/\d+$/);
  });

  test('admin can handle duplicate customer email validation', async ({ page }) => {
    const customerData = generateCustomerData();
    
    // Create first customer
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customerData);
    await customersPage.saveCustomer();
    
    // Try to create second customer with same email
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    
    const duplicateCustomerData = {
      ...generateCustomerData(),
      email: customerData.email // Same email
    };
    
    await customersPage.fillCustomerForm(duplicateCustomerData);
    await customersPage.saveCustomer();
    
    // Should show error message or prevent creation
    const errorMessage = await customersPage.getErrorMessage();
    if (errorMessage) {
      expect(errorMessage.toLowerCase()).toContain('email');
    } else {
      // If no error message, should stay on form page
      expect(page.url()).toContain('/customers/new');
    }
  });
});