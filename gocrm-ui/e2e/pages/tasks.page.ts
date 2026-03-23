import { Page } from '@playwright/test';

export class TasksPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  // Locators for list view
  get pageTitle() {
    return this.page.locator('h4:has-text("Tasks")');
  }

  get newTaskButton() {
    return this.page.locator('button:has-text("Create Task")');
  }

  get tasksTable() {
    return this.page.locator('table');
  }

  get tableRows() {
    return this.page.locator('table tbody tr');
  }

  get searchInput() {
    return this.page.locator('input[placeholder*="Search"]');
  }

  // Locators for form view — match actual input[name] attributes in TaskForm.tsx
  get titleInput() {
    return this.page.locator('input[name="title"]');
  }

  get descriptionTextarea() {
    return this.page.locator('textarea[name="description"]');
  }

  // due_date is a date input rendered by FormDatePicker
  get dueDateInput() {
    return this.page.locator('input[name="due_date"]');
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

  // Actions
  async goto() {
    await this.page.goto('/tasks');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewTask() {
    await this.newTaskButton.click();
    await this.page.waitForURL('**/tasks/new');
    await this.page.waitForLoadState('networkidle');
  }

  async fillTaskForm(taskData: {
    title: string;
    description?: string;
    priority?: string;
    status?: string;
    dueDate?: string;
  }) {
    await this.titleInput.fill(taskData.title);

    if (taskData.description) {
      await this.descriptionTextarea.fill(taskData.description);
    }

    if (taskData.priority) {
      await this.selectMuiOption('priority', taskData.priority);
    }

    if (taskData.status) {
      await this.selectMuiOption('status', taskData.status);
    }

    if (taskData.dueDate) {
      await this.dueDateInput.fill(taskData.dueDate);
    }
  }

  async saveTask() {
    await this.saveButton.click();
    await this.page.waitForLoadState('networkidle');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tasks') && response.request().method() === 'POST'
    );
    await this.saveButton.click();
    return await responsePromise;
  }

  async editTask(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const editBtn = row.locator('[data-testid="EditIcon"]').first();
    if (await editBtn.isVisible()) {
      await editBtn.click();
    } else {
      await row.locator('button').nth(1).click();
    }
    await this.page.waitForURL('**/tasks/**/edit');
  }

  async viewTask(rowIndex: number = 0) {
    const row = this.tableRows.nth(rowIndex);
    const viewBtn = row.locator('[data-testid="VisibilityIcon"]').first();
    if (await viewBtn.isVisible()) {
      await viewBtn.click();
    } else {
      await row.locator('button').nth(0).click();
    }
    await this.page.waitForURL('**/tasks/**');
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

  async deleteTask(rowIndex: number = 0) {
    const initialCount = await this.getTaskCount();

    await this.clickDeleteOnRow(rowIndex);

    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tasks') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.page.waitForLoadState('networkidle');

    let attempts = 0;
    while (attempts < 10) {
      const currentCount = await this.getTaskCount();
      if (currentCount < initialCount) break;
      await this.page.waitForTimeout(500);
      attempts++;
    }
  }

  async searchTasks(searchTerm: string) {
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

  async getTaskCount(): Promise<number> {
    try {
      await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
      return await this.tableRows.count();
    } catch {
      return 0;
    }
  }

  async getTaskData(rowIndex: number = 0): Promise<{
    title: string;
    status: string;
    priority: string;
    dueDate: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');

    return {
      title: await cells.nth(0).textContent() || '',
      status: await cells.nth(1).textContent() || '',
      priority: await cells.nth(2).textContent() || '',
      dueDate: await cells.nth(3).textContent() || '',
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
