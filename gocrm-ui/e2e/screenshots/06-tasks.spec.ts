import { test, expect, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { generateTaskData } from '../fixtures/admin-user';
import { TasksPage } from '../pages/tasks.page';

/**
 * Documentation captures for the Tasks area.
 *
 * Every test creates the records it needs through the UI, so the suite is
 * self-sufficient against an empty database. Runs serially — the whole
 * screenshot suite shares one backend.
 */
test.describe.configure({ mode: 'serial' });

type TaskData = ReturnType<typeof generateTaskData>;

interface CreatedTask {
  id: number;
  title: string;
}

/** Task created for the detail capture, reused by the edit capture. */
let sharedTask: CreatedTask | null = null;

/**
 * Walks the create form and returns the identifier the API assigned, so later
 * navigation can address the record directly instead of hunting for its row
 * (the list is not sorted newest-first, so a fresh record may sit on page 2).
 */
async function createTask(
  page: Page,
  tasksPage: TasksPage,
  data: TaskData
): Promise<CreatedTask> {
  await tasksPage.goto();
  await tasksPage.clickNewTask();
  // fillTaskForm picks an assignee: the field reads "(Optional)" but the API
  // requires assigned_to_id.
  await tasksPage.fillTaskForm(data);

  const response = await tasksPage.saveAndWaitForResponse();
  expect(response.status()).toBe(201);

  const body = await response.json();
  const id = body?.data?.id as number;
  expect(id).toBeTruthy();

  await page.waitForURL('**/tasks', { timeout: 15000 });
  await page.waitForLoadState('networkidle');

  return { id, title: data.title };
}

test.describe('Screenshots - Tasks', () => {
  let tasksPage: TasksPage;

  test.beforeEach(async ({ page }) => {
    tasksPage = new TasksPage(page);
    await ensureAdminLoggedIn(page);
  });

  test('01 - tasks list', async ({ page }) => {
    for (let i = 0; i < 3; i++) {
      await createTask(page, tasksPage, generateTaskData());
    }

    await tasksPage.goto();
    await expect(tasksPage.tasksTable).toBeVisible();
    // At least three rows on screen before the shutter opens.
    await tasksPage.tableRows.nth(2).waitFor({ state: 'visible' });

    await capture(page, 'tasks', '01-list', { fullPage: true });
  });

  test('02 - create task form', async ({ page }) => {
    const data = generateTaskData();

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(data);

    await expect(tasksPage.titleInput).toHaveValue(data.title);
    await expect(tasksPage.descriptionTextarea).toHaveValue(data.description);
    await expect(tasksPage.dueDateInput).toHaveValue(data.dueDate);

    await capture(page, 'tasks', '02-create');
  });

  test('03 - task detail', async ({ page }) => {
    sharedTask = await createTask(page, tasksPage, generateTaskData());

    await page.goto(`/tasks/${sharedTask.id}`);
    await page.waitForLoadState('networkidle');
    await expect(page.getByRole('heading', { name: sharedTask.title })).toBeVisible();

    await capture(page, 'tasks', '03-detail', { fullPage: true });
  });

  test('04 - edit task form', async ({ page }) => {
    const task = sharedTask ?? (await createTask(page, tasksPage, generateTaskData()));
    sharedTask = task;

    await page.goto(`/tasks/${task.id}/edit`);
    await page.waitForLoadState('networkidle');

    await expect(page.getByRole('heading', { name: 'Edit Task' })).toBeVisible();
    // The form is populated asynchronously once the record arrives.
    await expect(tasksPage.titleInput).toHaveValue(task.title);

    await capture(page, 'tasks', '04-edit');
  });

  test('05 - delete confirmation', async ({ page }) => {
    await createTask(page, tasksPage, generateTaskData());
    await createTask(page, tasksPage, generateTaskData());

    await tasksPage.goto();
    await tasksPage.tableRows.first().waitFor({ state: 'visible' });

    await tasksPage.clickDeleteOnRow(0);

    const dialog = page.getByRole('dialog', { name: 'Delete Task' });
    await expect(dialog).toBeVisible();
    await expect(dialog.getByText('Delete Task')).toBeVisible();

    await capture(page, 'tasks', '05-delete-confirm');

    // Never confirm: deletion is irreversible erasure.
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toBeHidden();
  });
});
