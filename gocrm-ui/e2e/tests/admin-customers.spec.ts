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

  test('admin can view customers list page', async () => {
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

    // Go back and edit THE customer just created. Editing row 0 instead picked
    // whichever customer sorted first, and saving it failed whenever its stored
    // data violated the edit form's zod rules (blank phone, non-URL website).
    await customersPage.goto();
    await customersPage.clickEditOnRowMatching(data.email);

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
    await customersPage.clickViewOnRowMatching(data.email);
    expect(page.url()).toMatch(/\/customers\/\d+$/);
  });

  test('admin can delete a customer', async () => {
    const data = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    await customersPage.saveAndWaitForResponse();

    await customersPage.goto();
    const initialCount = await customersPage.getRowCount();
    expect(initialCount).toBeGreaterThan(0);

    await customersPage.clickDeleteOnRowMatching(data.email);
    await customersPage.confirmDelete();

    // Assert the row is actually gone instead of sleeping and calling it done.
    // The list is still narrowed to this customer's email by the search
    // `clickDeleteOnRowMatching` ran, so the successful delete invalidates the
    // query and the filtered result set must empty out. `toHaveCount` retries,
    // which covers the refetch without a fixed wait.
    await expect(customersPage.tableRows.filter({ hasText: data.email })).toHaveCount(0);
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

  test('admin can create customer with minimal required data', async () => {
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

  // TC-CUST-018, browser half (#270, #398): an emptied optional text field is
  // sent as "" and the API stores it. Phone is required by the form, so the
  // test clears Notes and City.
  test('admin clears the notes and city of a customer and they stay empty after a reload', async ({ page }) => {
    // The 30 s waits after each navigation need more than the default test timeout.
    test.setTimeout(120000);
    const company = `E2E Clear ${Date.now()}`;
    const data = {
      ...generateCustomerData(),
      companyName: company,
      phone: '555-0100',
      city: 'Cluj-Napoca',
      notes: 'Clear me: met at the fair.',
    };

    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(data);
    const created = await customersPage.saveAndWaitForResponse();
    expect(created.status()).toBe(201);
    const customerId = (await created.json())?.data?.id;
    expect(customerId).toBeTruthy();

    // The edit form is filled by a reset once the customer query resolves,
    // so wait for the stored values before touching the fields.
    await page.goto(`/customers/${customerId}/edit`);
    await expect(customersPage.companyNameInput).toHaveValue(company, { timeout: 30000 });
    await expect(customersPage.cityInput).toHaveValue(data.city);
    await expect(customersPage.notesTextarea).toHaveValue(data.notes);

    await customersPage.cityInput.clear();
    await customersPage.notesTextarea.clear();

    const updatePromise = page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith(`/customers/${customerId}`) &&
        response.request().method() === 'PUT'
    );
    await customersPage.saveButton.click();
    const updated = await updatePromise;
    expect(updated.status()).toBe(200);
    const sent = updated.request().postDataJSON();
    expect(sent.city).toBe('');
    expect(sent.notes).toBe('');
    await page.waitForURL(/\/customers(?!.*edit)/, { timeout: 30000 });

    // Open the edit form again and reload it, so the values come from the
    // server. The inputs are briefly empty before the reset: the unique
    // company and the email prove the stored customer is on screen before
    // the two cleared fields are judged.
    await page.goto(`/customers/${customerId}/edit`);
    await page.reload();
    await expect(customersPage.companyNameInput).toHaveValue(company, { timeout: 30000 });
    await expect(customersPage.emailInput).toHaveValue(data.email);
    await expect(customersPage.cityInput).toHaveValue('');
    await expect(customersPage.notesTextarea).toHaveValue('');
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
