import { Page, Locator } from '@playwright/test';

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

  get filterSelect() {
    return this.page.locator('[role="combobox"]').first();
  }

  // Locators for form view
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

  get sourceSelect() {
    return this.page.locator('[name="source"]');
  }

  get statusSelect() {
    return this.page.locator('[name="status"]');
  }

  get notesTextarea() {
    return this.page.locator('textarea[name="notes"]');
  }

  get saveButton() {
    return this.page.locator('button:has-text("Create"), button:has-text("Update")');
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

  // Helper method for Material-UI Select components
  async selectMuiOption(fieldName: string, value: string) {
    // Click on the select field to open dropdown
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
    // Wait for the page to finish loading
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'POST'
    );
    await this.saveLead();
    return await responsePromise;
  }

  async editLead(rowIndex: number = 0) {
    const editButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="EditIcon"])');
    await editButton.click();
    await this.page.waitForURL('**/leads/**/edit');
  }

  async viewLead(rowIndex: number = 0) {
    const viewButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="VisibilityIcon"])');
    await viewButton.click();
    await this.page.waitForURL('**/leads/**');
  }

  async deleteLead(rowIndex: number = 0) {
    // Get initial count for reference
    const initialCount = await this.getLeadCount();
    
    const deleteButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="DeleteIcon"])');
    await deleteButton.click();
    
    // Wait for confirmation dialog
    await this.confirmDeleteButton.waitFor({ state: 'visible' });
    
    // Set up response listener before clicking confirm
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'DELETE'
    );
    
    await this.confirmDeleteButton.click();
    
    // Wait for delete response
    const response = await responsePromise;
    
    // Verify delete was successful
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }
    
    // Wait for table to refresh and count to change
    await this.page.waitForLoadState('networkidle');
    
    // Wait for the row count to actually decrease
    let attempts = 0;
    while (attempts < 10) {
      const currentCount = await this.getLeadCount();
      if (currentCount < initialCount) {
        break;
      }
      await this.page.waitForTimeout(500);
      attempts++;
    }
  }

  async searchLeads(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500); // Wait for search debounce
  }

  async filterByStatus(status: string) {
    // Click on the Material-UI Select
    await this.filterSelect.click();
    
    // Wait for dropdown to open and select the option
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${status}"]`);
    await option.click();
    
    // Wait for filter to apply
    await this.page.waitForTimeout(500);
  }

  async getLeadCount(): Promise<number> {
    await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
    return await this.tableRows.count();
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