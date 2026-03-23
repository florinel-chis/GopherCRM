import { Page } from '@playwright/test';

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

  // Locators for form view — match actual input[name] attributes in TicketForm.tsx
  get titleInput() {
    return this.page.locator('input[name="subject"]');
  }

  get descriptionTextarea() {
    return this.page.locator('textarea[name="description"]');
  }

  get saveButton() {
    return this.page.locator('button[type="submit"]');
  }

  get cancelButton() {
    return this.page.locator('button:has-text("Cancel")');
  }

  // Helper method for Material-UI Select components
  async selectMuiOption(fieldName: string, value: string) {
    const selectField = this.page.locator(`[name="${fieldName}"]`).locator('..');
    await selectField.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${value}"]`);
    await option.click();
    await this.page.waitForTimeout(300);
  }

  // Helper for MUI Autocomplete fields (customer, agent)
  async selectAutocompleteOption(label: string, optionText: string) {
    const autocomplete = this.page.locator(`label:has-text("${label}")`).locator('..').locator('input');
    await autocomplete.click();
    await autocomplete.fill(optionText);
    await this.page.waitForTimeout(500);
    const option = this.page.locator('[role="listbox"] [role="option"]').filter({ hasText: optionText }).first();
    await option.click();
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
  }) {
    await this.titleInput.fill(ticketData.subject);
    await this.descriptionTextarea.fill(ticketData.description);

    if (ticketData.priority) {
      await this.selectMuiOption('priority', ticketData.priority);
    }

    if (ticketData.status) {
      await this.selectMuiOption('status', ticketData.status);
    }
  }

  async saveTicket() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tickets') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  async editTicket(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const editBtn = row.locator('[data-testid="EditIcon"]').first();
    if (await editBtn.isVisible()) {
      await editBtn.click();
    } else {
      await row.locator('button').nth(1).click();
    }
    await this.page.waitForURL('**/tickets/**/edit');
  }

  async viewTicket(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const viewBtn = row.locator('[data-testid="VisibilityIcon"]').first();
    if (await viewBtn.isVisible()) {
      await viewBtn.click();
    } else {
      await row.locator('button').nth(0).click();
    }
    await this.page.waitForURL('**/tickets/**');
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

  async deleteTicket(rowIndex: number = 0) {
    const initialCount = await this.getTicketCount();

    await this.clickDeleteOnRow(rowIndex);

    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tickets') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.page.waitForLoadState('networkidle');
  }

  async searchTickets(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByStatus(status: string) {
    const filterSelect = this.page.locator('[role="combobox"]').first();
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${status}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async filterByPriority(priority: string) {
    const filterSelect = this.page.locator('[role="combobox"]').nth(1);
    await filterSelect.click();
    await this.page.waitForTimeout(500);
    const option = this.page.locator(`li[data-value="${priority}"]`);
    await option.click();
    await this.page.waitForTimeout(500);
  }

  async getTicketCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
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
