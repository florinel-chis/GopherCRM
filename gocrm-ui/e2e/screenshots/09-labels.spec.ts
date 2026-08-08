import { test, expect, type Page } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { generateTaskData } from '../fixtures/admin-user';
import { LabelsPage } from '../pages/labels.page';
import { TasksPage } from '../pages/tasks.page';

/**
 * Documentation captures for task labels: the management screen, its editor
 * dialog, the labels field on the task form, the list filtered by a label and
 * the delete confirmation.
 *
 * Unlike the other areas, the records here have fixed names. Labels are unique
 * by name and are hard-deleted rather than soft-deleted, so generating a new
 * name on every run would leave a growing pile of run-scoped labels on the very
 * screen being photographed. Instead the suite creates each label only when it
 * is missing, which keeps the captures stable and the run repeatable.
 */
test.describe.configure({ mode: 'serial' });

interface LabelFixture {
  name: string;
  color: string;
}

const labels: LabelFixture[] = [
  { name: 'Onboarding', color: '#1F77B4' },
  { name: 'Escalation', color: '#D62728' },
  { name: 'Follow-up', color: '#2CA02C' },
];

/** The label the task capture and the filtered-list capture are built around. */
const featured = labels[0];

/** Title of the labelled task, so the filtered list is never empty. */
const labelledTaskTitle = 'Prepare onboarding pack';

/** Creates a label through the dialog unless it is already there. */
async function ensureLabel(labelsPage: LabelsPage, label: LabelFixture): Promise<void> {
  await labelsPage.goto();
  if ((await labelsPage.row(label.name).count()) > 0) {
    return;
  }

  await labelsPage.openCreateDialog();
  await labelsPage.fillEditor({ name: label.name, color: label.color, useSwatch: true });

  const response = await labelsPage.submitEditor('POST');
  expect(response.status()).toBe(201);
  await expect(labelsPage.editorDialog).toBeHidden();
}

/** Creates the labelled task unless a previous run already made it. */
async function ensureLabelledTask(page: Page, tasksPage: TasksPage): Promise<void> {
  await tasksPage.goto();
  await tasksPage.searchTasks(labelledTaskTitle);
  if ((await tasksPage.taskRow(labelledTaskTitle).count()) > 0) {
    return;
  }

  await tasksPage.goto();
  await tasksPage.clickNewTask();
  await tasksPage.fillTaskForm({ ...generateTaskData(), title: labelledTaskTitle });
  await tasksPage.attachLabel(featured.name);
  await tasksPage.attachLabel(labels[2].name);

  const response = await tasksPage.saveAndWaitForResponse();
  expect(response.status()).toBe(201);
  await page.waitForURL('**/tasks', { timeout: 15000 });
}

test.describe('Screenshots - Labels', () => {
  let labelsPage: LabelsPage;
  let tasksPage: TasksPage;

  test.beforeEach(async ({ page }) => {
    labelsPage = new LabelsPage(page);
    tasksPage = new TasksPage(page);
    await ensureAdminLoggedIn(page);
  });

  test('01 - labels list', async ({ page }) => {
    for (const label of labels) {
      await ensureLabel(labelsPage, label);
    }

    await labelsPage.goto();
    await expect(labelsPage.labelsTable).toBeVisible();
    await expect(labelsPage.row(featured.name)).toBeVisible();

    await capture(page, 'labels', '01-list', { fullPage: true });
  });

  test('02 - create label dialog', async ({ page }) => {
    await labelsPage.goto();
    await labelsPage.openCreateDialog();
    // Filled but never submitted, so the capture cannot collide with the
    // unique name index on a re-run.
    await labelsPage.fillEditor({ name: 'Renewal', color: '#9467BD', useSwatch: true });

    await expect(labelsPage.nameInput).toHaveValue('Renewal');
    await expect(labelsPage.colorInput).toHaveValue('#9467BD');

    await capture(page, 'labels', '02-create-dialog');

    await labelsPage.editorCancelButton.click();
    await expect(labelsPage.editorDialog).toBeHidden();
  });

  test('03 - labels on the task form', async ({ page }) => {
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm({ ...generateTaskData(), title: 'Draft the rollout plan' });
    await tasksPage.attachLabel(featured.name);
    await tasksPage.attachLabel(labels[1].name);

    await expect(tasksPage.selectedLabelChips.filter({ hasText: featured.name })).toBeVisible();
    await expect(tasksPage.selectedLabelChips.filter({ hasText: labels[1].name })).toBeVisible();

    // Not submitted: the form itself is the subject.
    await capture(page, 'labels', '03-task-form');
  });

  test('04 - task list filtered by a label', async ({ page }) => {
    await ensureLabelledTask(page, tasksPage);

    await tasksPage.goto();
    await tasksPage.filterByLabel(featured.name);

    await expect(tasksPage.activeLabelFilter).toBeVisible();
    await expect(tasksPage.taskRow(labelledTaskTitle)).toBeVisible();

    await capture(page, 'labels', '04-list-filtered', { fullPage: true });
  });

  test('05 - delete label confirmation', async ({ page }) => {
    await labelsPage.goto();
    await labelsPage.openDeleteDialog(featured.name);

    await expect(labelsPage.deleteDialog).toContainText(featured.name);
    await expect(labelsPage.deleteDialog).toContainText('removed from');

    await capture(page, 'labels', '05-delete-confirm');

    // Never confirm: deleting a label detaches it from every task at once.
    await labelsPage.cancelDelete();
  });
});
