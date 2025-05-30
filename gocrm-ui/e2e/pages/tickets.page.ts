import { Page, Locator } from '@playwright/test';

export class TicketsPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Tickets")');
  }

  get newTicketButton() {
    return this.page.locator('button:has-text("Create Ticket")');
  }

  get ticketsTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  get statusFilter() {
    return this.page.locator('[role="combobox"]').first();
  }

  get priorityFilter() {
    return this.page.locator('[role="combobox"]').nth(1);
  }

  // Locators for form view
  get titleInput() {
    return this.page.locator('input[name="subject"]');
  }

  get descriptionTextarea() {
    return this.page.locator('textarea[name="description"]');
  }

  get prioritySelect() {
    return this.page.locator('[name="priority"]');
  }

  get statusSelect() {
    return this.page.locator('[name="status"]');
  }

  get customerSelect() {
    return this.page.locator('[name="customer_id"]');
  }

  get assigneeSelect() {
    return this.page.locator('[name="assignee_id"]');
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
    await this.page.goto('/tickets');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewTicket() {
    await this.newTicketButton.click();
    await this.page.waitForURL('**/tickets/new');
    await this.page.waitForLoadState('networkidle');
  }

  async fillTicketForm(ticketData: {
    subject: string;
    description: string;
    priority?: string;
    status?: string;
    customer_id?: number;
    assignee_id?: number;
  }) {
    await this.titleInput.fill(ticketData.subject);
    await this.descriptionTextarea.fill(ticketData.description);
    
    if (ticketData.priority) {
      await this.selectMuiOption('priority', ticketData.priority);
    }
    
    if (ticketData.status) {
      await this.selectMuiOption('status', ticketData.status);
    }
    
    if (ticketData.customer_id) {
      await this.selectMuiOption('customer_id', ticketData.customer_id.toString());
    }
    
    if (ticketData.assignee_id) {
      await this.selectMuiOption('assignee_id', ticketData.assignee_id.toString());
    }
  }

  async saveTicket() {
    await this.saveButton.click();
    // Wait for the page to finish loading
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/tickets') && response.request().method() === 'POST'
    );
    await this.saveTicket();
    return await responsePromise;
  }

  async editTicket(rowIndex: number = 0) {
    const editButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="EditIcon"])');
    await editButton.click();
    await this.page.waitForURL('**/tickets/**/edit');
  }

  async viewTicket(rowIndex: number = 0) {
    const viewButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="VisibilityIcon"])');
    await viewButton.click();
    await this.page.waitForURL('**/tickets/**');
  }

  async deleteTicket(rowIndex: number = 0) {
    // Get initial count for reference
    const initialCount = await this.getTicketCount();
    
    const deleteButton = this.tableRows.nth(rowIndex).locator('button:has(svg[data-testid="DeleteIcon"])');
    await deleteButton.click();
    
    // Wait for confirmation dialog
    await this.confirmDeleteButton.waitFor({ state: 'visible' });
    
    // Set up response listener before clicking confirm
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/tickets') && response.request().method() === 'DELETE'
    );
    
    await this.confirmDeleteButton.click();
    
    // Wait for delete response
    const response = await responsePromise;
    
    // Verify delete was successful
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }
    
    // Wait for table to refresh
    await this.page.waitForLoadState('networkidle');
  }

  async searchTickets(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500); // Wait for search debounce
  }

  async filterByStatus(status: string) {
    // Click on the Material-UI Select
    await this.statusFilter.click();
    
    // Wait for dropdown to open and select the option
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${status}"]`);
    await option.click();
    
    // Wait for filter to apply
    await this.page.waitForTimeout(500);
  }

  async filterByPriority(priority: string) {
    // Click on the Material-UI Select
    await this.priorityFilter.click();
    
    // Wait for dropdown to open and select the option
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${priority}"]`);
    await option.click();
    
    // Wait for filter to apply
    await this.page.waitForTimeout(500);
  }

  async getTicketCount(): Promise<number> {
    await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
    return await this.tableRows.count();
  }

  async getTicketData(rowIndex: number = 0): Promise<{
    subject: string;
    customer: string;
    status: string;
    priority: string;
    assignee: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');
    
    return {
      subject: await cells.nth(0).textContent() || '',
      customer: await cells.nth(1).textContent() || '',
      status: await cells.nth(2).textContent() || '',
      priority: await cells.nth(3).textContent() || '',
      assignee: await cells.nth(4).textContent() || '',
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