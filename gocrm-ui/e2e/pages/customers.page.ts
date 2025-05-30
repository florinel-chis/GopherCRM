import { Page, Locator } from '@playwright/test';

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
    return this.page.locator('button:has-text("New Customer")');
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

  // Locators for form view
  get firstNameInput() {
    return this.page.locator('input[name="firstName"]');
  }

  get lastNameInput() {
    return this.page.locator('input[name="lastName"]');
  }

  get emailInput() {
    return this.page.locator('input[name="email"]');
  }

  get phoneInput() {
    return this.page.locator('input[name="phone"]');
  }

  get companyInput() {
    return this.page.locator('input[name="company"]');
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

  get zipCodeInput() {
    return this.page.locator('input[name="zipCode"]');
  }

  get countryInput() {
    return this.page.locator('input[name="country"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button:has-text("Save")');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  get deleteButton() {
    return this.page.locator('button:has-text("Delete")');
  }

  get confirmDeleteButton() {
    return this.page.locator('button:has-text("Delete"):visible');
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

  async fillCustomerForm(customerData: {
    firstName: string;
    lastName: string;
    email: string;
    phone?: string;
    company?: string;
    address?: string;
    city?: string;
    state?: string;
    zipCode?: string;
    country?: string;
    notes?: string;
  }) {
    await this.firstNameInput.fill(customerData.firstName);
    await this.lastNameInput.fill(customerData.lastName);
    await this.emailInput.fill(customerData.email);
    
    if (customerData.phone) {
      await this.phoneInput.fill(customerData.phone);
    }
    
    if (customerData.company) {
      await this.companyInput.fill(customerData.company);
    }
    
    if (customerData.address) {
      await this.addressInput.fill(customerData.address);
    }
    
    if (customerData.city) {
      await this.cityInput.fill(customerData.city);
    }
    
    if (customerData.state) {
      await this.stateInput.fill(customerData.state);
    }
    
    if (customerData.zipCode) {
      await this.zipCodeInput.fill(customerData.zipCode);
    }
    
    if (customerData.country) {
      await this.countryInput.fill(customerData.country);
    }
    
    if (customerData.notes) {
      await this.notesTextarea.fill(customerData.notes);
    }
  }

  async saveCustomer() {
    await this.saveButton.click();
    await this.page.waitForURL('**/customers/**');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/customers') && response.request().method() === 'POST'
    );
    await this.saveCustomer();
    return await responsePromise;
  }

  async editCustomer(rowIndex: number = 0) {
    const editButton = this.tableRows.nth(rowIndex).locator('button:has-text("Edit")');
    await editButton.click();
    await this.page.waitForURL('**/customers/**/edit');
  }

  async viewCustomer(rowIndex: number = 0) {
    const viewButton = this.tableRows.nth(rowIndex).locator('button:has-text("View")');
    await viewButton.click();
    await this.page.waitForURL('**/customers/**');
  }

  async deleteCustomer(rowIndex: number = 0) {
    const deleteButton = this.tableRows.nth(rowIndex).locator('button:has-text("Delete")');
    await deleteButton.click();
    
    await this.confirmDeleteButton.waitFor({ state: 'visible' });
    await this.confirmDeleteButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/customers') && response.request().method() === 'DELETE'
    );
  }

  async searchCustomers(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async getCustomerCount(): Promise<number> {
    await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
    return await this.tableRows.count();
  }

  async getCustomerData(rowIndex: number = 0): Promise<{
    firstName: string;
    lastName: string;
    email: string;
    company: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');
    
    return {
      firstName: await cells.nth(0).textContent() || '',
      lastName: await cells.nth(1).textContent() || '',
      email: await cells.nth(2).textContent() || '',
      company: await cells.nth(3).textContent() || '',
    };
  }

  async getErrorMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    
    try {
      await alert.waitFor({ state: 'visible', timeout: 2000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }

  async getSuccessMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    
    try {
      await alert.waitFor({ state: 'visible', timeout: 2000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}