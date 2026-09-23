import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { TasksPage } from '../pages/tasks.page';
import { generateTaskData } from '../fixtures/admin-user';

test.describe('Admin - Tasks Management', () => {
  let adminAuth: AdminAuthHelper;
  let tasksPage: TasksPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    tasksPage = new TasksPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view tasks list page', async () => {
    await tasksPage.goto();

    await expect(tasksPage.pageTitle).toBeVisible();
    await expect(tasksPage.newTaskButton).toBeVisible();
    await expect(tasksPage.tasksTable).toBeVisible();
  });

  test('admin can create a new task successfully', async ({ page }) => {
    const taskData = generateTaskData();

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);

    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    expect(page.url()).toContain('/tasks');
  });

  test('admin can edit an existing task', async ({ page }) => {
    // Create a task first
    // Edit only the task this test created (the list is not newest-first).
    const stamp = Date.now();
    const originalTaskData = { ...generateTaskData(), title: `EditTask_${stamp}` };
    const updatedTitle = `EditedTask_${stamp}`;
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(originalTaskData);
    await tasksPage.saveAndWaitForResponse();

    await tasksPage.goto();
    await tasksPage.searchTasks(originalTaskData.title);
    await expect(tasksPage.tableRows).toHaveCount(1);
    await tasksPage.editTask(0);

    await tasksPage.titleInput.clear();
    await tasksPage.titleInput.fill(updatedTitle);
    await tasksPage.saveButton.click();

    await page.waitForURL(/\/tasks(?!.*edit)/, { timeout: 10000 });
    await tasksPage.goto();
    await tasksPage.searchTasks(updatedTitle);
    await expect(tasksPage.taskRow(updatedTitle)).toHaveCount(1);
  });

  test('admin can view task details', async ({ page }) => {
    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);

    const response = await tasksPage.saveAndWaitForResponse();
    const responseBody = await response.json();
    const taskId = responseBody?.data?.id;

    await page.goto(`/tasks/${taskId}`);
    await page.waitForLoadState('networkidle');
    expect(page.url()).toMatch(/\/tasks\/\d+$/);
    await expect(page.getByText(taskData.title).first()).toBeVisible();
  });

  test('admin can delete a task', async () => {
    // Create a task first
    const taskData = { ...generateTaskData(), title: `DeleteTask_${Date.now()}` };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);

    const created = await tasksPage.saveAndWaitForResponse();
    expect(created.status()).toBe(201);

    // Narrow the list to the task this test created — a spec may only delete
    // its own records, and the list is not ordered newest-first.
    await tasksPage.goto();
    await tasksPage.searchTasks(taskData.title);
    await expect(tasksPage.taskRow(taskData.title)).toBeVisible();

    await tasksPage.deleteTask(0);

    await expect(tasksPage.taskRow(taskData.title)).toHaveCount(0);
  });

  test('admin can search tasks', async () => {
    const taskData = { ...generateTaskData(), title: `SearchTask_${Date.now()}` };

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveAndWaitForResponse();

    await tasksPage.goto();
    await tasksPage.searchTasks(taskData.title);
    await expect(tasksPage.taskRow(taskData.title)).toHaveCount(1);
    await expect(tasksPage.tableRows).toHaveCount(1);
  });

  // fixme: asserted `count >= 0`, which cannot fail. The filter itself is broken: the
  // backend ignores the parameter (catalog TC-XCUT-038). Once it works, create a task with a
  // known value and check the filter keeps it and drops it for another value.
  test.fixme('admin can filter tasks by status', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.filterByStatus('pending');
    await page.waitForTimeout(1000);

    const filteredCount = await tasksPage.getTaskCount();
    expect(filteredCount).toBeGreaterThanOrEqual(0);
  });

  // fixme: asserted `count >= 0`, which cannot fail. The filter itself is broken: the
  // backend ignores the parameter (catalog TC-XCUT-038). Once it works, create a task with a
  // known value and check the filter keeps it and drops it for another value.
  test.fixme('admin can filter tasks by priority', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.filterByPriority('high');
    await page.waitForTimeout(1000);

    const filteredCount = await tasksPage.getTaskCount();
    expect(filteredCount).toBeGreaterThanOrEqual(0);
  });

  test('admin sees validation errors for invalid task data', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.clickNewTask();

    // Try to save without required fields — clear the title which has default empty
    await tasksPage.saveButton.click();

    // Should stay on form page
    expect(page.url()).toContain('/tasks/new');
  });

  test('admin can handle task form cancellation', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.clickNewTask();

    await tasksPage.titleInput.fill('Task to Cancel');
    await tasksPage.cancelButton.click();

    await page.waitForURL('**/tasks', { timeout: 10000 });
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create task with minimal required data', async () => {
    const minimalTaskData = {
      title: `MinTask_${Date.now()}`,
      description: 'Basic task description'
    };

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(minimalTaskData);

    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
  });

  test('admin can create tasks with different priorities', async () => {
    // Three full create round-trips through the UI, each with an assignee
    // lookup, do not fit the default per-test budget.
    test.slow();

    const priorities = ['low', 'medium', 'high'];

    for (const priority of priorities) {
      const taskData = {
        ...generateTaskData(),
        title: `${priority.charAt(0).toUpperCase() + priority.slice(1)} Priority Task ${Date.now()}`,
        priority
      };

      await tasksPage.goto();
      await tasksPage.clickNewTask();
      await tasksPage.fillTaskForm(taskData);
      await tasksPage.saveTask();
    }

    await tasksPage.goto();
    const taskCount = await tasksPage.getTaskCount();
    expect(taskCount).toBeGreaterThanOrEqual(priorities.length);
  });

  test('admin can track task progress through status changes', async ({ page }) => {
    // Create a new task
    const taskData = { ...generateTaskData(), status: 'pending' };
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);

    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    const taskId = (await response.json())?.data?.id;

    // Edit the task this test created, addressed by id. Editing whichever task
    // happens to sit in row 0 picks an arbitrary record from a shared database,
    // and a completed one cannot be moved to another status at all.
    await page.goto(`/tasks/${taskId}/edit`);
    await page.waitForLoadState('networkidle');
    await expect(tasksPage.titleInput).toHaveValue(taskData.title);

    await tasksPage.selectMuiOption('status', 'in_progress');
    await tasksPage.saveButton.click();

    await page.waitForURL(/\/tasks(?!.*edit)/, { timeout: 10000 });
  });
});
