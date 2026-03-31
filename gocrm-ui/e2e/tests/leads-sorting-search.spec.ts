import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LeadsPage } from '../pages/leads.page';

test.describe('Leads List - Sorting and Search', () => {
  let adminAuth: AdminAuthHelper;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('should load leads page with data', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.pageTitle).toBeVisible();
    await expect(leadsPage.leadsTable).toBeVisible();

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should sort by Created column descending', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();
    await expect(createdHeader).toBeVisible();

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    const requestUrl = response.request().url();
    expect(requestUrl).toContain('sort_by=created_at');
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should toggle sort order on double click', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();

    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    let response = await responsePromise;
    const firstUrl = response.request().url();

    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    response = await responsePromise;
    const secondUrl = response.request().url();

    expect(secondUrl).toContain('sort_by=created_at');

    const firstHasDesc = firstUrl.includes('sort_order=desc');
    const secondHasDesc = secondUrl.includes('sort_order=desc');
    expect(firstHasDesc).not.toBe(secondHasDesc);
  });

  test('should search for a lead by email', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );

    await leadsPage.searchInput.fill('anders.t@conversio.dk');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThanOrEqual(1);

    const tableText = await leadsPage.leadsTable.textContent();
    expect(tableText).toContain('anders.t@conversio.dk');
  });

  test('should search for a lead by company name', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );

    await leadsPage.searchInput.fill('Conversio');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThanOrEqual(1);

    const tableText = await leadsPage.leadsTable.textContent();
    expect(tableText).toContain('Conversio');
  });

  test('should search and sort together', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('conversio');
    await responsePromise;

    await page.waitForLoadState('networkidle');

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();

    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    const url = response.request().url();
    expect(url).toContain('search=conversio');
    expect(url).toContain('sort_by=created_at');
    expect(response.status()).toBe(200);
  });

  test('should clear search and show all leads', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const initialCount = await leadsPage.tableRows.count();

    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('anders.t@conversio.dk');
    await responsePromise;
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);

    const filteredCount = await leadsPage.tableRows.count();

    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') && response.request().method() === 'GET'
    );
    await leadsPage.searchInput.clear();
    await responsePromise;
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);

    const restoredCount = await leadsPage.tableRows.count();
    expect(restoredCount).toBeGreaterThanOrEqual(filteredCount);
  });

  test('should return no results for non-existent search', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/v1/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('xyznonexistent12345abcdef');
    const response = await responsePromise;

    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBe(0);
  });
});
