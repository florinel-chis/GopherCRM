import { test, expect, type Page } from '@playwright/test';
import { faker } from '@faker-js/faker';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { TasksPage } from '../pages/tasks.page';
import { generateTaskData } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

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
    await tasksPage.editTask(originalTaskData.title);

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
    await expect(tasksPage.taskRow(taskData.title)).toHaveCount(1);
    await expect(tasksPage.tableRows).toHaveCount(1);

    await tasksPage.deleteTask(taskData.title);

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

  // Seeds a task owned by the signed-in admin through the API. Create always
  // yields status `pending`; another status is reached by an update.
  const seedTask = async (page: Page, title: string, options: { priority?: string; status?: string } = {}) => {
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    const headers = { Authorization: `Bearer ${token}` };
    const me = await page.request.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status(), 'current user').toBe(200);
    const created = await page.request.post(`${API_BASE_URL}/tasks`, {
      headers,
      data: { title, priority: options.priority ?? 'medium', assigned_to_id: (await me.json()).data.id },
    });
    expect(created.status(), 'seed task').toBe(201);
    if (options.status) {
      const moved = await page.request.put(`${API_BASE_URL}/tasks/${(await created.json()).data.id}`, {
        headers,
        data: { status: options.status },
      });
      expect(moved.status(), 'seed task status').toBe(200);
    }
  };

  // fixme: GET /tasks ignores the status parameter, so the Status dropdown narrows nothing (#427).
  test.fixme('admin can filter tasks by status (#427)', async ({ page }) => {
    const stamp = `${Date.now()}${faker.string.alphanumeric(6)}`;
    const pendingTitle = `${stamp} still pending`;
    const completedTitle = `${stamp} already completed`;
    await seedTask(page, pendingTitle);
    await seedTask(page, completedTitle, { status: 'completed' });

    await tasksPage.goto();
    await tasksPage.searchTasks(stamp);
    await expect(tasksPage.taskRow(pendingTitle)).toHaveCount(1);
    await expect(tasksPage.taskRow(completedTitle)).toHaveCount(1);

    await tasksPage.filterByStatus('completed');
    await expect(tasksPage.taskRow(completedTitle)).toBeVisible();
    await expect(tasksPage.taskRow(pendingTitle)).toHaveCount(0);
  });

  // fixme: GET /tasks ignores the priority parameter, so the Priority dropdown narrows nothing (#427).
  test.fixme('admin can filter tasks by priority (#427)', async ({ page }) => {
    const stamp = `${Date.now()}${faker.string.alphanumeric(6)}`;
    const lowTitle = `${stamp} can wait`;
    const highTitle = `${stamp} cannot wait`;
    await seedTask(page, lowTitle, { priority: 'low' });
    await seedTask(page, highTitle, { priority: 'high' });

    await tasksPage.goto();
    await tasksPage.searchTasks(stamp);
    await expect(tasksPage.taskRow(lowTitle)).toHaveCount(1);
    await expect(tasksPage.taskRow(highTitle)).toHaveCount(1);

    await tasksPage.filterByPriority('high');
    await expect(tasksPage.taskRow(highTitle)).toBeVisible();
    await expect(tasksPage.taskRow(lowTitle)).toHaveCount(0);
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
