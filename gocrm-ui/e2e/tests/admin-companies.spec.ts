import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { CompaniesPage, generateCompanyData } from '../pages/companies.page';
import { CustomersPage } from '../pages/customers.page';
import { generateCustomerData } from '../fixtures/admin-user';

test.describe('Admin - Companies', () => {
  let adminAuth: AdminAuthHelper;
  let companiesPage: CompaniesPage;
  let customersPage: CustomersPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    companiesPage = new CompaniesPage(page);
    customersPage = new CustomersPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  /** Creates a customer linked to `company` through the customer form. */
  async function createLinkedCustomer(company: ReturnType<typeof generateCompanyData>) {
    const customer = generateCustomerData();
    await customersPage.goto();
    await customersPage.clickNewCustomer();
    await customersPage.fillCustomerForm(customer);
    await companiesPage.pickLinkedCompany(company.domain, `${company.name} (${company.domain})`);
    const response = await customersPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    return customer;
  }

  test('admin can view the companies list page', async () => {
    await companiesPage.goto();
    await expect(companiesPage.pageTitle).toBeVisible();
    await expect(companiesPage.newCompanyButton).toBeVisible();
    await expect(companiesPage.companiesTable).toBeVisible();
  });

  test('admin can create a company with all fields and sees them on the detail page', async ({ page }) => {
    const data = generateCompanyData();

    await companiesPage.goto();
    await companiesPage.clickNewCompany();
    await companiesPage.fillCompanyForm(data);
    const response = await companiesPage.saveAndWaitForResponse('POST');
    expect(response.status()).toBe(201);

    await page.waitForURL(/\/companies\/\d+$/);
    await expect(companiesPage.detailHeading(data.name)).toBeVisible();
    await expect(page.getByText(data.domain, { exact: true })).toBeVisible();
    await expect(page.getByRole('link', { name: data.website })).toHaveAttribute('href', data.website);
    await expect(page.getByText(data.industry, { exact: true })).toBeVisible();
    await expect(page.getByText(`${data.employeeRange} employees`)).toBeVisible();
    await expect(page.getByText(data.address)).toBeVisible();
    await expect(page.getByText(data.notes)).toBeVisible();
    // A fresh company has nothing linked yet.
    await expect(companiesPage.customersSection).toContainText('No customers linked to this company');
    await expect(companiesPage.leadsSection).toContainText('No leads linked to this company');
  });

  test('validation errors keep the form open', async ({ page }) => {
    await companiesPage.goto();
    await companiesPage.clickNewCompany();

    await companiesPage.websiteInput.fill('not a url');
    await companiesPage.saveButton.click();

    await expect(page.getByText('Name is required')).toBeVisible();
    await expect(page.getByText('Website must be an http(s) URL')).toBeVisible();
    expect(page.url()).toContain('/companies/new');
  });

  test('a duplicate domain is refused with the server message on the domain field', async ({ page }) => {
    const original = generateCompanyData();
    await companiesPage.createCompany(original);

    const duplicate = { ...generateCompanyData(), domain: original.domain };
    await companiesPage.goto();
    await companiesPage.clickNewCompany();
    await companiesPage.fillCompanyForm(duplicate);
    const hintBefore = await companiesPage.domainHelperText.textContent();

    const response = await companiesPage.saveAndWaitForResponse('POST');
    expect(response.status()).toBe(409);

    await expect(companiesPage.domainHelperText).toHaveClass(/Mui-error/);
    await expect(companiesPage.domainHelperText).not.toHaveText(hintBefore ?? '');
    await expect(companiesPage.domainHelperText).not.toBeEmpty();
    expect(page.url()).toContain('/companies/new');
  });

  test('admin can edit a company', async ({ page }) => {
    const data = generateCompanyData();
    await companiesPage.createCompany(data);

    await companiesPage.goto();
    await companiesPage.clickEditOnRowMatching(data.domain);
    await expect(companiesPage.nameInput).toHaveValue(data.name);

    const industry = `Robotics ${Date.now()}`;
    await companiesPage.industryInput.fill(industry);
    const response = await companiesPage.saveAndWaitForResponse('PUT');
    expect(response.status()).toBe(200);

    await page.waitForURL(/\/companies\/\d+$/);
    await expect(companiesPage.detailHeading(data.name)).toBeVisible();
    await expect(page.getByText(industry, { exact: true })).toBeVisible();
  });

  test('admin can search companies', async () => {
    const data = generateCompanyData();
    await companiesPage.createCompany(data);

    await companiesPage.goto();
    await companiesPage.searchCompanies(data.name);

    await expect(companiesPage.tableRows.filter({ hasText: data.domain })).toHaveCount(1);
    await expect(companiesPage.tableRows).toHaveCount(1);
  });

  test('linking a customer to a company shows the link on both detail pages', async ({ page }) => {
    const company = generateCompanyData();
    const companyId = await companiesPage.createCompany(company);

    const customer = await createLinkedCustomer(company);

    // Customer detail: the linked record wins over the free text.
    await customersPage.goto();
    await customersPage.clickViewOnRowMatching(customer.email);
    const companyLink = page.getByRole('link', { name: company.name });
    await expect(companyLink).toHaveAttribute('href', `/companies/${companyId}`);

    // Company detail: the customer appears in the Customers section.
    await companyLink.click();
    await page.waitForURL(`**/companies/${companyId}`);
    await expect(companiesPage.detailHeading(company.name)).toBeVisible();
    await expect(
      companiesPage.customersSection.getByRole('link', { name: customer.contactName })
    ).toBeVisible();
    await expect(companiesPage.customersSection).toContainText(customer.email);
  });

  test('deleting a company leaves the customer with its text company only', async ({ page }) => {
    const company = generateCompanyData();
    await companiesPage.createCompany(company);
    const customer = await createLinkedCustomer(company);

    await companiesPage.goto();
    await companiesPage.clickDeleteOnRowMatching(company.domain);
    await companiesPage.confirmDelete();
    // The list is still narrowed to the domain, so the row must disappear.
    await expect(companiesPage.tableRows.filter({ hasText: company.domain })).toHaveCount(0);

    await customersPage.goto();
    await customersPage.clickViewOnRowMatching(customer.email);
    await expect(page.getByRole('heading', { level: 4, name: customer.companyName })).toBeVisible();
    // The link is gone and the free-text company is shown as plain text.
    await expect(page.getByRole('link', { name: company.name })).toHaveCount(0);
    await expect(page.getByText(customer.companyName, { exact: true }).first()).toBeVisible();
    await expect(page.getByText(company.name, { exact: true })).toHaveCount(0);
  });
});
