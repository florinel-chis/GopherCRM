import { test, expect } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { LeadsPage } from '../pages/leads.page';
import { TasksPage } from '../pages/tasks.page';
import { generateLeadData, generateTaskData } from '../fixtures/admin-user';

/**
 * Documentation captures for the dashboard and the two standalone status
 * screens (access denied, 404).
 */
test.describe.configure({ mode: 'serial' });

test.describe('Screenshots - Dashboard', () => {
  test.beforeEach(async ({ page }) => {
    await ensureAdminLoggedIn(page);
  });

  test('dashboard overview', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    const tasksPage = new TasksPage(page);

    // Seed a little activity so the stat cards, the chart and the
    // "Upcoming Tasks" panel are not empty in the published image.
    for (let i = 0; i < 2; i++) {
      const leadData = generateLeadData();
      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      const response = await leadsPage.saveAndWaitForResponse();
      expect(response.status()).toBe(201);
    }

    const taskData = generateTaskData();
    await tasksPage.goto();
    await tasksPage.clickNewTask();
    await tasksPage.fillTaskForm(taskData);
    // The form labels the assignee "(Optional)", but the backend requires
    // assigned_to_id — pick the first user so the POST succeeds.
    await page.getByLabel('Assign To (Optional)').click();
    const assigneeOption = page.locator('li[role="option"]').first();
    await assigneeOption.waitFor({ state: 'visible' });
    await assigneeOption.click();
    const taskResponse = await tasksPage.saveAndWaitForResponse();
    expect(taskResponse.status()).toBe(201);

    await page.goto('/');
    await page.waitForURL('/');
    await expect(page.locator('h4:has-text("Dashboard")')).toBeVisible();

    // Every stat card has rendered its value: the loading skeletons are gone
    // and the five titles are on screen with a numeric value beside them.
    await expect(page.locator('.MuiSkeleton-root')).toHaveCount(0, { timeout: 20000 });
    for (const title of [
      'Total Leads',
      'Total Customers',
      'Open Tickets',
      'Pending Tasks',
      'Conversion Rate',
    ]) {
      await expect(page.getByText(title, { exact: true }).first()).toBeVisible();
    }
    // The stat value is a Typography variant="h4" rendered as a <div>.
    await expect(page.locator('.MuiCard-root .MuiTypography-h4').first()).not.toBeEmpty();

    // The sales chart draws once its data arrives.
    await expect(page.locator('.recharts-surface').first()).toBeVisible({ timeout: 20000 });
    await expect(page.getByText('Quick Actions')).toBeVisible();
    await expect(page.getByText('Upcoming Tasks')).toBeVisible();

    await capture(page, 'dashboard', '01-overview', { fullPage: true });
  });

  test('access denied screen', async ({ page }) => {
    await page.goto('/unauthorized');
    await page.waitForURL('**/unauthorized');

    await expect(page.getByRole('heading', { name: 'Access Denied' })).toBeVisible();
    await expect(
      page.getByText("You don't have permission to access this page")
    ).toBeVisible();

    await capture(page, 'misc', '01-unauthorized');
  });

  test('not found screen', async ({ page }) => {
    await page.goto('/no-such-page');
    await page.waitForURL('**/no-such-page');

    await expect(page.getByRole('heading', { name: '404' })).toBeVisible();
    await expect(page.getByRole('heading', { name: 'Page Not Found' })).toBeVisible();

    await capture(page, 'misc', '02-not-found');
  });
});
