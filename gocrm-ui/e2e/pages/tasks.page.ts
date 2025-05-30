import { Page, Locator } from '@playwright/test';

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
    return this.page.locator('button:has-text("New Task")');
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

  get statusFilter() {
    return this.page.locator('select[name="status"]');
  }

  get priorityFilter() {
    return this.page.locator('select[name="priority"]');
  }

  // Locators for form view
  get titleInput() {
    return this.page.locator('input[name="title"]');
  }

  get descriptionTextarea() {
    return this.page.locator('textarea[name="description"]');
  }

  get prioritySelect() {
    return this.page.locator('select[name="priority"]');
  }

  get statusSelect() {
    return this.page.locator('select[name="status"]');
  }

  get dueDateInput() {
    return this.page.locator('input[name="dueDate"]');
  }

  get assignedToSelect() {
    return this.page.locator('select[name="assignedTo"]');
  }

  get relatedToSelect() {
    return this.page.locator('select[name="relatedTo"]');
  }

  get relatedIdSelect() {
    return this.page.locator('select[name="relatedId"]');
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
    await this.page.goto('/tasks');
    await this.page.waitForLoadState('networkidle');
    await this.pageTitle.waitFor({ state: 'visible' });
  }

  async clickNewTask() {
    await this.newTaskButton.click();
    await this.page.waitForURL('**/tasks/new');
  }

  async fillTaskForm(taskData: {
    title: string;
    description: string;
    priority?: string;
    status?: string;
    dueDate?: string;
    assignedTo?: string;
    relatedTo?: string;
    relatedId?: string;
  }) {
    await this.titleInput.fill(taskData.title);
    await this.descriptionTextarea.fill(taskData.description);
    
    if (taskData.priority) {
      await this.prioritySelect.selectOption(taskData.priority);
    }
    
    if (taskData.status) {
      await this.statusSelect.selectOption(taskData.status);
    }
    
    if (taskData.dueDate) {
      await this.dueDateInput.fill(taskData.dueDate);
    }
    
    if (taskData.assignedTo) {
      await this.assignedToSelect.selectOption(taskData.assignedTo);
    }
    
    if (taskData.relatedTo) {
      await this.relatedToSelect.selectOption(taskData.relatedTo);
    }
    
    if (taskData.relatedId) {
      await this.relatedIdSelect.selectOption(taskData.relatedId);
    }
  }

  async saveTask() {
    await this.saveButton.click();
    await this.page.waitForURL('**/tasks/**');
  }

  async saveAndWaitForResponse() {
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/tasks') && response.request().method() === 'POST'
    );
    await this.saveTask();
    return await responsePromise;
  }

  async editTask(rowIndex: number = 0) {
    const editButton = this.tableRows.nth(rowIndex).locator('button:has-text("Edit")');
    await editButton.click();
    await this.page.waitForURL('**/tasks/**/edit');
  }

  async viewTask(rowIndex: number = 0) {
    const viewButton = this.tableRows.nth(rowIndex).locator('button:has-text("View")');
    await viewButton.click();
    await this.page.waitForURL('**/tasks/**');
  }

  async deleteTask(rowIndex: number = 0) {
    const deleteButton = this.tableRows.nth(rowIndex).locator('button:has-text("Delete")');
    await deleteButton.click();
    
    await this.confirmDeleteButton.waitFor({ state: 'visible' });
    await this.confirmDeleteButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/tasks') && response.request().method() === 'DELETE'
    );
  }

  async searchTasks(searchTerm: string) {
    await this.searchInput.fill(searchTerm);
    await this.page.waitForTimeout(500);
  }

  async filterByStatus(status: string) {
    await this.statusFilter.selectOption(status);
    await this.page.waitForTimeout(500);
  }

  async filterByPriority(priority: string) {
    await this.priorityFilter.selectOption(priority);
    await this.page.waitForTimeout(500);
  }

  async getTaskCount(): Promise<number> {
    await this.tableRows.first().waitFor({ state: 'visible', timeout: 5000 });
    return await this.tableRows.count();
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

  async markTaskComplete(rowIndex: number = 0) {
    const completeButton = this.tableRows.nth(rowIndex).locator('button:has-text("Complete")');
    await completeButton.click();
    
    await this.page.waitForResponse(
      response => response.url().includes('/api/tasks') && response.request().method() === 'PUT'
    );
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