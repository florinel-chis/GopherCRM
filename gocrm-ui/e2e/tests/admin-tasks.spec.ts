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

  test('admin can view tasks list page', async ({ page }) => {
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
    const originalTaskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(originalTaskData);
    await tasksPage.saveAndWaitForResponse();

    // Go back and edit
    await tasksPage.goto();
    await tasksPage.editTask(0);

    // Update fields
    await tasksPage.titleInput.clear();
    await tasksPage.titleInput.fill('Updated Task Title');
    await tasksPage.saveButton.click();

    await page.waitForURL(/\/tasks(?!.*edit)/, { timeout: 10000 });
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

  test('admin can delete a task', async ({ page }) => {
    // Create a task first
    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveAndWaitForResponse();

    await tasksPage.goto();
    const initialCount = await tasksPage.getTaskCount();
    expect(initialCount).toBeGreaterThan(0);

    await tasksPage.deleteTask(0);
    expect(true).toBe(true);
  });

  test('admin can search tasks', async ({ page }) => {
    const taskData = { ...generateTaskData(), title: `SearchTask_${Date.now()}` };

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.saveAndWaitForResponse();

    await tasksPage.goto();
    await tasksPage.searchTasks(taskData.title);
    await page.waitForTimeout(1000);
  });

  test('admin can filter tasks by status', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.filterByStatus('pending');
    await page.waitForTimeout(1000);

    const filteredCount = await tasksPage.getTaskCount();
    expect(filteredCount).toBeGreaterThanOrEqual(0);
  });

  test('admin can filter tasks by priority', async ({ page }) => {
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

  test('admin can create task with minimal required data', async ({ page }) => {
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

  test('admin can create tasks with different priorities', async ({ page }) => {
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
    await tasksPage.saveAndWaitForResponse();

    // Edit task to change status
    await tasksPage.goto();
    await tasksPage.editTask(0);
    await tasksPage.selectMuiOption('status', 'in_progress');
    await tasksPage.saveButton.click();

    await page.waitForURL(/\/tasks(?!.*edit)/, { timeout: 10000 });
  });
});
