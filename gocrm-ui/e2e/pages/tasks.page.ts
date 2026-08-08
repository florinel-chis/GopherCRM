import { Locator, Page } from '@playwright/test';

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

  /**
   * Fills the create/edit form.
   *
   * The assignee is picked even though the field reads "Assign To (Optional)":
   * the API binds `assigned_to_id` as required and answers 400 without it, so a
   * task the form considers complete is not one the backend accepts. Pass
   * `assignee: false` to exercise that rejection deliberately.
   */
  async fillTaskForm(taskData: {
    title: string;
    description?: string;
    priority?: string;
    status?: string;
    dueDate?: string;
    assignee?: boolean;
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

    if (taskData.assignee !== false) {
      await this.selectFirstAssignee();
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

  /**
   * The delete confirmation. Addressed by its accessible name, not by
   * `[role="dialog"]`: the collapsed navigation Drawer also reports that role,
   * so the bare selector matches two elements and trips strict mode.
   */
  get deleteDialog() {
    return this.page.getByRole('dialog', { name: 'Delete Task' });
  }

  async confirmDelete() {
    await this.deleteDialog.waitFor({ state: 'visible' });
    await this.deleteDialog.getByRole('button', { name: 'Delete' }).click();
  }

  /**
   * Deletes a row and waits for the list to settle.
   *
   * The row count is deliberately not used as the completion signal: the list
   * is paginated, so on a full page the row that was deleted is simply replaced
   * by the next record and the count never drops.
   */
  async deleteTask(rowIndex: number = 0) {
    await this.clickDeleteOnRow(rowIndex);

    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/tasks') && response.request().method() === 'DELETE'
    );

    await this.confirmDelete();

    const response = await responsePromise;
    if (response.status() !== 200 && response.status() !== 204) {
      throw new Error(`Delete failed with status ${response.status()}`);
    }

    await this.deleteDialog.waitFor({ state: 'hidden' });
    await this.page.waitForLoadState('networkidle');
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

  // Column order in the list: title, labels, status, priority, assignee,
  // due date, created, actions.
  async getTaskData(rowIndex: number = 0): Promise<{
    title: string;
    labels: string;
    status: string;
    priority: string;
    dueDate: string;
  }> {
    const row = this.tableRows.nth(rowIndex);
    const cells = row.locator('td');

    return {
      title: await cells.nth(0).textContent() || '',
      labels: await cells.nth(1).textContent() || '',
      status: await cells.nth(2).textContent() || '',
      priority: await cells.nth(3).textContent() || '',
      dueDate: await cells.nth(5).textContent() || '',
    };
  }

  // --- Labels --------------------------------------------------------------
  //
  // The create/edit form carries a multi-select Autocomplete of labels that can
  // also mint a new one inline; the list carries a single-select label filter
  // plus clickable chips in the labels column.

  /** The multi-select labels field on the task form. */
  get labelsField() {
    return this.page.getByRole('combobox', { name: 'Labels', exact: true });
  }

  /** The single-select label filter above the task list. */
  get labelFilterField() {
    return this.page.getByRole('combobox', { name: 'Label', exact: true });
  }

  /** The chip echoing the active label filter, with its clear affordance. */
  get activeLabelFilter() {
    return this.page.locator('[data-testid="active-label-filter"]');
  }

  /** Chips currently selected inside the task form's labels field. */
  get selectedLabelChips() {
    return this.page.locator('.MuiAutocomplete-root .MuiChip-root');
  }

  /** The list row for a task, matched on its (run-unique) title. */
  taskRow(title: string): Locator {
    return this.tableRows.filter({ hasText: title });
  }

  /** A label chip inside a task's row in the list. */
  labelChipInRow(taskTitle: string, labelName: string): Locator {
    return this.taskRow(taskTitle).locator('.MuiChip-root').filter({ hasText: labelName });
  }

  /**
   * The assignee picker is written "(Optional)" but the API requires
   * assigned_to_id, so every created task needs one.
   */
  async selectFirstAssignee() {
    await this.page.getByLabel('Assign To (Optional)').click();
    const option = this.page.locator('li[role="option"]').first();
    await option.waitFor({ state: 'visible' });
    await option.click();
  }

  /** Attaches an existing label to the task being edited. */
  async attachLabel(name: string) {
    await this.labelsField.click();
    await this.labelsField.fill(name);
    const option = this.page.getByRole('option', { name, exact: true });
    await option.waitFor({ state: 'visible' });
    await option.click();
    // Close the dropdown so the next field is not covered by the listbox.
    await this.page.keyboard.press('Escape');
  }

  /**
   * Creates a label from inside the task form via the synthetic `Add "x"`
   * option, and returns the POST /labels response it triggers.
   */
  async createLabelInline(name: string) {
    await this.labelsField.click();
    await this.labelsField.fill(name);
    const option = this.page.getByRole('option', { name: `Add "${name}"`, exact: true });
    await option.waitFor({ state: 'visible' });

    const responsePromise = this.page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith('/labels') &&
        response.request().method() === 'POST'
    );
    await option.click();
    const response = await responsePromise;
    await this.page.keyboard.press('Escape');
    return response;
  }

  /** Picks a label in the list filter and returns the refetched task list. */
  async filterByLabel(name: string) {
    await this.labelFilterField.click();
    await this.labelFilterField.fill(name);
    const option = this.page.getByRole('option', { name, exact: true });
    await option.waitFor({ state: 'visible' });

    const responsePromise = this.waitForTaskListRequest();
    await option.click();
    return await responsePromise;
  }

  /**
   * Clears the active label filter through the chip's delete affordance.
   *
   * Deliberately does not wait for a task request: clearing returns the query
   * to a key that was already fetched when the page loaded, so the cached
   * unfiltered page is reused and no HTTP call is made. Assert the rendered
   * outcome instead.
   */
  async clearLabelFilter() {
    await this.activeLabelFilter.locator('.MuiChip-deleteIcon').click();
    await this.activeLabelFilter.waitFor({ state: 'hidden' });
  }

  /** Clicks a label chip inside a task row, which filters the list by it. */
  async clickLabelChipInRow(taskTitle: string, labelName: string) {
    const responsePromise = this.waitForTaskListRequest();
    await this.labelChipInRow(taskTitle, labelName).click();
    return await responsePromise;
  }

  /** The next GET /tasks list request (not a single-task fetch). */
  private waitForTaskListRequest() {
    return this.page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith('/tasks') &&
        response.request().method() === 'GET'
    );
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
