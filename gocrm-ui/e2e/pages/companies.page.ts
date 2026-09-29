import { Page, Locator, expect } from '@playwright/test';

export interface CompanyFormData {
  name: string;
  domain?: string;
  website?: string;
  industry?: string;
  employeeRange?: '1-10' | '11-50' | '51-200' | '201-500' | '501-1000' | '1000+';
  phone?: string;
  address?: string;
  city?: string;
  state?: string;
  country?: string;
  postalCode?: string;
  notes?: string;
}

/**
 * Values unique to one run. The domain is what `rowMatching` searches for: it
 * is unique among live companies on the API, so it identifies exactly one row.
 *
 * The API accepts only letters, digits, dots and hyphens in a domain (the
 * hostname rule), so the domain and website get a stamp without underscores.
 */
export function generateCompanyData(): Required<CompanyFormData> {
  const stamp = `${Date.now()}_${Math.random().toString(36).slice(2, 6)}`;
  const hostStamp = stamp.replace(/_/g, '-');
  return {
    name: `E2E Company ${stamp}`,
    domain: `e2e-${hostStamp}.example`,
    website: `https://www.e2e-${hostStamp}.example`,
    industry: `Industry ${stamp}`,
    employeeRange: '51-200',
    phone: '+40 21 555 0100',
    address: '1 Foundry Lane',
    city: 'Cluj',
    state: 'CJ',
    country: 'Romania',
    postalCode: '400001',
    notes: `Created by the e2e suite (${stamp})`,
  };
}

export class CompaniesPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // List view
  get pageTitle() {
    return this.page.locator('h4:has-text("Companies")');
  }

  get newCompanyButton() {
    return this.page.getByRole('button', { name: 'New Company' });
  }

  get companiesTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Form — match the input[name] attributes in CompanyForm.tsx
  get nameInput() {
    return this.page.locator('input[name="name"]');
  }

  get domainInput() {
    return this.page.locator('input[name="domain"]');
  }

  get websiteInput() {
    return this.page.locator('input[name="website"]');
  }

  get industryInput() {
    return this.page.locator('input[name="industry"]');
  }

  get employeeRangeSelect() {
    return this.page.getByRole('combobox', { name: /Employees/ });
  }

  get phoneInput() {
    return this.page.locator('input[name="phone"]');
  }

  get addressInput() {
    return this.page.locator('input[name="address"]');
  }

  get cityInput() {
    return this.page.locator('input[name="city"]');
  }

  get stateInput() {
    return this.page.locator('input[name="state"]');
  }

  get countryInput() {
    return this.page.locator('input[name="country"]');
  }

  get postalCodeInput() {
    return this.page.locator('input[name="postal_code"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.getByRole('button', { name: 'Cancel' });
  }

  /** The helper line under the domain field; carries the 409 message. */
  get domainHelperText() {
    return this.page
      .locator('.MuiFormControl-root', { has: this.page.locator('input[name="domain"]') })
      .locator('.MuiFormHelperText-root');
  }

  // Detail view
  detailHeading(name: string) {
    return this.page.getByRole('heading', { level: 4, name });
  }

  get customersSection() {
    return this.page.locator('section[aria-labelledby="company-customers-heading"]');
  }

  get leadsSection() {
    return this.page.locator('section[aria-labelledby="company-leads-heading"]');
  }

  get detailEditButton() {
    return this.page.getByRole('button', { name: 'Edit' });
  }

  get detailDeleteButton() {
    return this.page.getByRole('button', { name: 'Delete company' });
  }

  // Actions
  async goto() {
    await this.page.goto('/companies');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewCompany() {
    await this.newCompanyButton.click();
    await this.page.waitForURL('**/companies/new');
  }

  async fillCompanyForm(data: CompanyFormData) {
    await this.nameInput.fill(data.name);
    if (data.domain !== undefined) await this.domainInput.fill(data.domain);
    if (data.website !== undefined) await this.websiteInput.fill(data.website);
    if (data.industry !== undefined) await this.industryInput.fill(data.industry);
    if (data.employeeRange) {
      await this.employeeRangeSelect.click();
      await this.page.getByRole('option', { name: data.employeeRange, exact: true }).click();
    }
    if (data.phone !== undefined) await this.phoneInput.fill(data.phone);
    if (data.address !== undefined) await this.addressInput.fill(data.address);
    if (data.city !== undefined) await this.cityInput.fill(data.city);
    if (data.state !== undefined) await this.stateInput.fill(data.state);
    if (data.country !== undefined) await this.countryInput.fill(data.country);
    if (data.postalCode !== undefined) await this.postalCodeInput.fill(data.postalCode);
    if (data.notes !== undefined) await this.notesTextarea.fill(data.notes);
  }

  /** Submits the form and returns the POST or PUT /companies response. */
  async saveAndWaitForResponse(method: 'POST' | 'PUT' = 'POST') {
    const responsePromise = this.page.waitForResponse(
      (response) =>
        /\/companies(\/\d+)?$/.test(new URL(response.url()).pathname) &&
        response.request().method() === method
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  /** Creates a company through the UI and returns its id from the detail URL. */
  async createCompany(data: CompanyFormData): Promise<number> {
    await this.goto();
    await this.clickNewCompany();
    await this.fillCompanyForm(data);
    const response = await this.saveAndWaitForResponse('POST');
    expect(response.status()).toBe(201);
    await this.page.waitForURL(/\/companies\/\d+$/);
    const match = this.page.url().match(/\/companies\/(\d+)$/);
    return Number(match?.[1]);
  }

  /**
   * Narrows the list to one company and returns its row.
   *
   * Row position is not stable: the list is paginated and server-sorted, and
   * every run appends rows. Tests pass the company's domain, which is unique
   * among live companies, and get the matching row.
   */
  async rowMatching(uniqueText: string): Promise<Locator> {
    await this.searchCompanies(uniqueText);
    const row = this.tableRows.filter({ hasText: uniqueText }).first();
    await expect(row).toBeVisible({ timeout: 10000 });
    return row;
  }

  private async clickRowAction(row: Locator, iconTestId: string) {
    const button = row.locator(`[data-testid="${iconTestId}"]`).first();
    await expect(button).toBeVisible({ timeout: 10000 });
    await button.click();
  }

  async clickEditOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'EditIcon');
    await this.page.waitForURL('**/companies/**/edit');
  }

  async clickViewOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'VisibilityIcon');
    await this.page.waitForURL(/\/companies\/\d+$/);
  }

  async clickDeleteOnRowMatching(uniqueText: string) {
    await this.clickRowAction(await this.rowMatching(uniqueText), 'DeleteIcon');
  }

  /**
   * Addressed by its accessible name, not by `[role="dialog"]`: the navigation
   * Drawer also reports that role and would trip strict mode.
   */
  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete Company' });
  }

  async confirmDelete() {
    await this.deleteDialog.waitFor({ state: 'visible' });
    await this.deleteDialog.getByRole('button', { name: 'Delete' }).click();
  }

  async searchCompanies(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  /**
   * Picks a company in the "Company (linked)" autocomplete of the lead or
   * customer form: type a fragment (the domain is unique), then click the
   * option, which reads "Name (domain)".
   */
  async pickLinkedCompany(typed: string, optionLabel: string) {
    const input = this.page.getByLabel('Company (linked)');
    await input.click();
    await input.fill(typed);
    await this.page.getByRole('option', { name: optionLabel, exact: true }).click();
    await expect(input).toHaveValue(optionLabel);
  }
}
