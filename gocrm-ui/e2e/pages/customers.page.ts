import { Page } from '@playwright/test';

export class CustomersPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Customers")');
  }

  get newCustomerButton() {
    return this.page.locator('button:has-text("Add Customer")');
  }

  get customersTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form — match actual input[name] attributes in CustomerForm.tsx
  get companyNameInput() {
    return this.page.locator('input[name="company_name"]');
  }

  get contactNameInput() {
    return this.page.locator('input[name="contact_name"]');
  }

  get emailInput() {
    return this.page.locator('input[name="email"]');
  }

  get phoneInput() {
    return this.page.locator('input[name="phone"]');
  }

  get industryInput() {
    return this.page.locator('input[name="industry"]');
  }

  get websiteInput() {
    return this.page.locator('input[name="website"]');
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

  get postalCodeInput() {
    return this.page.locator('input[name="postal_code"]');
  }

  get countryInput() {
    return this.page.locator('input[name="country"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  // Actions
  async goto() {
    await this.page.goto('/customers');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewCustomer() {
    await this.newCustomerButton.click();
    await this.page.waitForURL('**/customers/new');
  }

  async fillCustomerForm(data: {
    companyName: string;
    contactName: string;
    email: string;
    phone?: string;
    industry?: string;
    website?: string;
    address?: string;
    city?: string;
    state?: string;
    postalCode?: string;
    country?: string;
    notes?: string;
  }) {
    await this.companyNameInput.fill(data.companyName);
    await this.contactNameInput.fill(data.contactName);
    await this.emailInput.fill(data.email);
    if (data.phone) await this.phoneInput.fill(data.phone);
    if (data.industry) await this.industryInput.fill(data.industry);
    if (data.website) await this.websiteInput.fill(data.website);
    if (data.address) await this.addressInput.fill(data.address);
    if (data.city) await this.cityInput.fill(data.city);
    if (data.state) await this.stateInput.fill(data.state);
    if (data.postalCode) await this.postalCodeInput.fill(data.postalCode);
    if (data.country) await this.countryInput.fill(data.country);
    if (data.notes) await this.notesTextarea.fill(data.notes);
  }

  async saveCustomer() {
    await this.saveButton.click();
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/customers') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  async clickEditOnRow(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    // DataTable has icon buttons for view/edit/delete
    const editBtn = row.locator('[data-testid="EditIcon"]').first();
    if (await editBtn.isVisible()) {
      await editBtn.click();
    } else {
      // Fallback: click the edit icon button
      await row.locator('button').nth(1).click(); // 0=view, 1=edit, 2=delete
    }
    await this.page.waitForURL('**/customers/**/edit');
  }

  async clickViewOnRow(rowIndex: number = 0) {
    await this.tableRows.nth(rowIndex).click();
  }

  async clickDeleteOnRow(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const deleteBtn = row.locator('[data-testid="DeleteIcon"]').first();
    if (await deleteBtn.isVisible()) {
      await deleteBtn.click();
    } else {
      await row.locator('button').last().click();
    }
  }

  async confirmDelete() {
    const dialog = this.page.locator('[role="dialog"]');
    await dialog.waitFor({ state: 'visible' });
    await dialog.locator('button:has-text("Delete")').click();
  }

  async searchCustomers(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async getRowCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
  }

  async getErrorMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    try {
      await alert.waitFor({ state: 'visible', timeout: 5000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}
