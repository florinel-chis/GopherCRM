import { Page } from '@playwright/test';

export class LeadsPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Leads")');
  }

  get newLeadButton() {
    return this.page.locator('button:has-text("Add Lead")');
  }

  get leadsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form view — match actual input[name] attributes in LeadForm.tsx
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

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  // Helper method for Material-UI Select components
  async selectMuiOption(fieldName: string, value: string) {
    // Click on the select trigger to open dropdown
    const selectField = this.page.locator(`[name="${fieldName}"]`).locator('..');
    await selectField.click();

    // Wait for dropdown to open and select the option
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${value}"]`);
    await option.click();

    // Wait for dropdown to close
    await this.page.waitForTimeout(300);
  }

  // Actions
  async goto() {
    await this.page.goto('/leads');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewLead() {
    await this.newLeadButton.click();
    await this.page.waitForURL('**/leads/new');
    await this.page.waitForLoadState('networkidle');
  }

  async fillLeadForm(leadData: {
    companyName: string;
    contactName: string;
    email: string;
    phone?: string;
    source?: string;
    status?: string;
    notes?: string;
  }) {
    await this.companyNameInput.fill(leadData.companyName);
    await this.contactNameInput.fill(leadData.contactName);
    await this.emailInput.fill(leadData.email);

    if (leadData.phone) {
      await this.phoneInput.fill(leadData.phone);
    }

    if (leadData.source) {
      await this.selectMuiOption('source', leadData.source);
    }

    if (leadData.status) {
      await this.selectMuiOption('status', leadData.status);
    }

    if (leadData.notes) {
      await this.notesTextarea.fill(leadData.notes);
    }
  }

  async saveLead() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/leads') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  async editLead(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const editBtn = row.locator('[data-testid="EditIcon"]').first();
    if (await editBtn.isVisible()) {
      await editBtn.click();
    } else {
      await row.locator('button').nth(1).click();
    }
    await this.page.waitForURL('**/leads/**/edit');
  }

  async viewLead(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const viewBtn = row.locator('[data-testid="VisibilityIcon"]').first();
    if (await viewBtn.isVisible()) {
      await viewBtn.click();
    } else {
      await row.locator('button').nth(0).click();
    }
    await this.page.waitForURL('**/leads/**');
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

  async deleteLead(rowIndex: number = 0) {
    const initialCount = await this.getLeadCount();

    await this.clickDeleteOnRow(rowIndex);

    // Set up response listener before clicking confirm
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/leads') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.page.waitForLoadState('networkidle');

    // Wait for count to decrease
    let attempts = 0;
    while (attempts < 10) {
      const currentCount = await this.getLeadCount();
      if (currentCount < initialCount) break;
      await this.page.waitForTimeout(500);
      attempts++;
    }
  }

  async searchLeads(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByStatus(status: string) {
    // MUI Select filter on list page
    const filterSelect = this.page.locator('[role="combobox"]').first();
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${status}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async getLeadCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
  }

  async getLeadData(rowIndex: number = 0): Promise<{
    companyName: string;
    contactName: string;
    email: string;
    phone: string;
    status: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');

    return {
      companyName: await cells.nth(0).textContent() || '',
      contactName: await cells.nth(1).textContent() || '',
      email: await cells.nth(2).textContent() || '',
      phone: await cells.nth(3).textContent() || '',
      status: await cells.nth(4).textContent() || '',
    };
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

  async getSuccessMessage(): Promise<string | null> {
    const alert = this.page.locator('.MuiAlert-message');
    try {
      await alert.waitFor({ state: 'visible', timeout: 5000 });
      return await alert.textContent();
    } catch {
      return null;
    }
  }
}
