import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LabelsPage } from '../pages/labels.page';
import { TasksPage } from '../pages/tasks.page';
import { generateTaskData } from '../fixtures/admin-user';

/**
 * Task labels, end to end: the management screen at /labels, attaching labels
 * to tasks (including creating one inline from the task form), the chips in the
 * task list and on the task detail page, the two ways of filtering by label,
 * and the rename / recolour / delete lifecycle.
 *
 * Runs serially with shared state: the label created by the first test is the
 * subject of the later ones. Names are scoped to the run so a re-run cannot
 * collide with its own leftovers on the unique name index — and the spec
 * removes everything it created, since labels are hard-deleted and would
 * otherwise pile up on the management screen and in the documentation captures.
 */
test.describe.configure({ mode: 'serial' });

/** Unique per run; short enough that names stay inside the 50-char limit. */
const runId = `${Date.now().toString(36)}${Math.random().toString(36).slice(2, 6)}`;

const labelNames = {
  attached: `e2e-attached-${runId}`,
  renamed: `e2e-renamed-${runId}`,
  inline: `e2e-inline-${runId}`,
};

/** Palette presets, picked through the swatch row rather than typed. */
const colors = {
  initial: '#D62728',
  recolored: '#2CA02C',
};

/** The task the `attached` label is put on, and the one the inline label lands on. */
let attachedTask: { id: number; title: string } | null = null;
let inlineTask: { id: number; title: string } | null = null;

test.describe('Task Labels', () => {
  let adminAuth: AdminAuthHelper;
  let labelsPage: LabelsPage;
  let tasksPage: TasksPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    labelsPage = new LabelsPage(page);
    tasksPage = new TasksPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can create a label from the labels management page', async () => {
    await labelsPage.goto();
    await expect(labelsPage.createLabelButton).toBeVisible();

    await labelsPage.openCreateDialog();
    await labelsPage.fillEditor({
      name: labelNames.attached,
      color: colors.initial,
      useSwatch: true,
    });

    const response = await labelsPage.submitEditor('POST');
    expect(response.status()).toBe(201);

    await expect(labelsPage.editorDialog).toBeHidden();
    await expect(labelsPage.row(labelNames.attached)).toBeVisible();
    expect(await labelsPage.colorOf(labelNames.attached)).toBe(colors.initial);
    // Nothing carries it yet.
    expect(await labelsPage.taskCountOf(labelNames.attached)).toBe(0);
  });

  test('a label name that already exists is rejected', async () => {
    await labelsPage.goto();
    await labelsPage.openCreateDialog();
    // Same name, different colour: the clash is on the name alone.
    await labelsPage.fillEditor({
      name: labelNames.attached,
      color: colors.recolored,
      useSwatch: true,
    });

    const response = await labelsPage.submitEditor('POST');
    expect(response.status()).toBe(409);
    expect(await labelsPage.snackbarMessage()).toContain('already exists');

    // The dialog stays open with the input intact so the name can be corrected.
    await expect(labelsPage.editorDialog).toBeVisible();
    await expect(labelsPage.nameInput).toHaveValue(labelNames.attached);

    await labelsPage.editorCancelButton.click();
    await expect(labelsPage.editorDialog).toBeHidden();

    // Nothing was written: the label is still listed exactly once.
    await labelsPage.goto();
    await expect(labelsPage.row(labelNames.attached)).toHaveCount(1);
  });

  test('admin can attach an existing label to a new task', async ({ page }) => {
    const taskData = generateTaskData();

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    await tasksPage.attachLabel(labelNames.attached);

    await expect(tasksPage.selectedLabelChips.filter({ hasText: labelNames.attached })).toBeVisible();

    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);

    const body = await response.json();
    attachedTask = { id: body?.data?.id, title: taskData.title };
    expect(attachedTask.id).toBeTruthy();
    // The created task comes back with its labels resolved, not just the ids.
    expect(body?.data?.labels?.map((label: { name: string }) => label.name)).toContain(
      labelNames.attached
    );

    await page.waitForURL('**/tasks', { timeout: 15000 });

    // The management screen now counts the task against the label.
    await labelsPage.goto();
    expect(await labelsPage.taskCountOf(labelNames.attached)).toBe(1);
  });

  test('label chips appear in the task list and on the task detail page', async ({ page }) => {
    expect(attachedTask).not.toBeNull();
    const task = attachedTask!;

    // The list is not sorted newest-first, so search for the task rather than
    // assuming it landed on page one.
    await tasksPage.goto();
    await tasksPage.searchTasks(task.title);
    await expect(tasksPage.taskRow(task.title)).toBeVisible();
    await expect(tasksPage.labelChipInRow(task.title, labelNames.attached)).toBeVisible();

    await page.goto(`/tasks/${task.id}`);
    await page.waitForLoadState('networkidle');
    await expect(page.getByRole('heading', { name: task.title })).toBeVisible();
    await expect(page.locator('.MuiChip-root').filter({ hasText: labelNames.attached })).toBeVisible();
  });

  test('clicking a label chip in the list filters the tasks by that label', async () => {
    expect(attachedTask).not.toBeNull();
    const task = attachedTask!;

    await tasksPage.goto();
    await tasksPage.searchTasks(task.title);
    await expect(tasksPage.labelChipInRow(task.title, labelNames.attached)).toBeVisible();

    const response = await tasksPage.clickLabelChipInRow(task.title, labelNames.attached);
    expect(new URL(response.url()).searchParams.get('label_id')).toBeTruthy();

    // The filter is echoed back as a chip with a clear affordance.
    await expect(tasksPage.activeLabelFilter).toBeVisible();
    await expect(tasksPage.activeLabelFilter).toHaveText(labelNames.attached);

    // The server applies label_id INSTEAD of search, so the UI drops the search
    // term and disables the box rather than leaving it claiming a narrowing
    // that is not being applied.
    await expect(tasksPage.searchInput).toHaveValue('');
    await expect(tasksPage.searchInput).toBeDisabled();

    // Every row now carries the label — including the task that was searched
    // for. The result set is all labelled tasks rather than the intersection
    // with the search term.
    await expect(tasksPage.taskRow(task.title)).toBeVisible();
    const rows = await tasksPage.tableRows.count();
    expect(rows).toBeGreaterThan(0);
    for (let i = 0; i < rows; i++) {
      await expect(tasksPage.tableRows.nth(i)).toContainText(labelNames.attached);
    }
  });

  test('the label filter dropdown narrows the list and can be cleared', async () => {
    expect(attachedTask).not.toBeNull();
    const task = attachedTask!;

    await tasksPage.goto();
    const filtered = await tasksPage.filterByLabel(labelNames.attached);
    expect(new URL(filtered.url()).searchParams.get('label_id')).toBeTruthy();

    await expect(tasksPage.taskRow(task.title)).toBeVisible();
    await expect(tasksPage.activeLabelFilter).toBeVisible();

    // Clearing goes back to a query key that was already fetched on page load,
    // so the cached unfiltered page is reused and no request is issued — the
    // observable effect is in the rendered controls, not on the wire.
    await tasksPage.clearLabelFilter();
    await expect(tasksPage.activeLabelFilter).toBeHidden();
    await expect(tasksPage.labelFilterField).toHaveValue('');
    // Search becomes usable again once nothing overrides it.
    await expect(tasksPage.searchInput).toBeEnabled();
  });

  test('admin can create a label inline from the task form', async ({ page }) => {
    const taskData = generateTaskData();

    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);

    const created = await tasksPage.createLabelInline(labelNames.inline);
    expect(created.status()).toBe(201);

    // The new label is selected straight away, without a trip to /labels.
    await expect(tasksPage.selectedLabelChips.filter({ hasText: labelNames.inline })).toBeVisible();

    const response = await tasksPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);

    const body = await response.json();
    inlineTask = { id: body?.data?.id, title: taskData.title };
    expect(body?.data?.labels?.map((label: { name: string }) => label.name)).toContain(
      labelNames.inline
    );

    await page.waitForURL('**/tasks', { timeout: 15000 });

    await labelsPage.goto();
    await expect(labelsPage.row(labelNames.inline)).toBeVisible();
    expect(await labelsPage.taskCountOf(labelNames.inline)).toBe(1);
  });

  test('admin can rename and recolour a label', async ({ page }) => {
    expect(attachedTask).not.toBeNull();
    const task = attachedTask!;

    await labelsPage.goto();
    await labelsPage.openEditDialog(labelNames.attached);
    await expect(labelsPage.nameInput).toHaveValue(labelNames.attached);

    await labelsPage.fillEditor({ name: labelNames.renamed });
    await labelsPage.fillEditor({ color: colors.recolored, useSwatch: true });

    const response = await labelsPage.submitEditor('PUT');
    expect(response.status()).toBe(200);

    await expect(labelsPage.editorDialog).toBeHidden();
    await expect(labelsPage.row(labelNames.renamed)).toBeVisible();
    expect(await labelsPage.colorOf(labelNames.renamed)).toBe(colors.recolored);
    // Renaming does not detach anything.
    expect(await labelsPage.taskCountOf(labelNames.renamed)).toBe(1);

    // The task carries the label by reference, so the detail page follows.
    await page.goto(`/tasks/${task.id}`);
    await page.waitForLoadState('networkidle');
    await expect(page.locator('.MuiChip-root').filter({ hasText: labelNames.renamed })).toBeVisible();
    await expect(page.locator('.MuiChip-root').filter({ hasText: labelNames.attached })).toHaveCount(0);
  });

  test('deleting a label detaches it from the tasks that carry it', async ({ page }) => {
    expect(attachedTask).not.toBeNull();
    const task = attachedTask!;

    await labelsPage.goto();
    await labelsPage.openDeleteDialog(labelNames.renamed);
    // The dialog states the blast radius before anything is destroyed.
    await expect(labelsPage.deleteDialog).toContainText('removed from 1 task');

    const response = await labelsPage.confirmDelete();
    expect(response.status()).toBe(204);

    await expect(labelsPage.row(labelNames.renamed)).toHaveCount(0);

    // The task survives; only the chip is gone.
    await page.goto(`/tasks/${task.id}`);
    await page.waitForLoadState('networkidle');
    await expect(page.getByRole('heading', { name: task.title })).toBeVisible();
    await expect(page.locator('.MuiChip-root').filter({ hasText: labelNames.renamed })).toHaveCount(0);
  });

  test('editing a task replaces its label set', async ({ page }) => {
    expect(inlineTask).not.toBeNull();
    const task = inlineTask!;

    await page.goto(`/tasks/${task.id}/edit`);
    await page.waitForLoadState('networkidle');
    await expect(tasksPage.titleInput).toHaveValue(task.title);

    const chip = tasksPage.selectedLabelChips.filter({ hasText: labelNames.inline });
    await expect(chip).toBeVisible();
    await chip.locator('.MuiChip-deleteIcon').click();
    await expect(chip).toHaveCount(0);

    const responsePromise = page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith(`/tasks/${task.id}`) &&
        response.request().method() === 'PUT'
    );
    await tasksPage.saveButton.click();
    const response = await responsePromise;
    expect(response.status()).toBe(200);
    expect((await response.json())?.data?.labels ?? []).toEqual([]);

    await page.goto(`/tasks/${task.id}`);
    await page.waitForLoadState('networkidle');
    await expect(page.locator('.MuiChip-root').filter({ hasText: labelNames.inline })).toHaveCount(0);
  });

  test('a label no task carries can be deleted', async () => {
    await labelsPage.goto();
    // The previous test cleared the only task that used it.
    expect(await labelsPage.taskCountOf(labelNames.inline)).toBe(0);

    await labelsPage.openDeleteDialog(labelNames.inline);
    await expect(labelsPage.deleteDialog).toContainText('removed from 0 tasks');

    const response = await labelsPage.confirmDelete();
    expect(response.status()).toBe(204);

    await expect(labelsPage.row(labelNames.inline)).toHaveCount(0);
  });
});
