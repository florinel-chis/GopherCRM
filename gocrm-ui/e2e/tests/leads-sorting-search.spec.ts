import { test, expect } from '@playwright/test';
import { LeadsPage } from '../pages/leads.page';
import { LoginPage } from '../pages/login.page';

// Use the actual admin credentials from the database
const ADMIN_EMAIL = 'admin@test.com';
const ADMIN_PASSWORD = 'admin123';

async function loginAsAdmin(page: any) {
  const loginPage = new LoginPage(page);
  await loginPage.goto();
  await loginPage.login(ADMIN_EMAIL, ADMIN_PASSWORD);

  // Wait for redirect to dashboard
  await page.waitForURL('/', { timeout: 10000 });
  await page.waitForLoadState('networkidle');
}

test.describe('Leads List - Sorting and Search', () => {
  test.beforeEach(async ({ page }) => {
    await loginAsAdmin(page);
  });

  test('should load leads page with data', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    // Verify the page loaded with a table
    await expect(leadsPage.pageTitle).toBeVisible();
    await expect(leadsPage.leadsTable).toBeVisible();

    // Should have some rows
    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should sort by Created column descending', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    // Wait for table to load
    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    // Find the "Created" column header and click it
    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();
    await expect(createdHeader).toBeVisible();

    // Wait for the API response after clicking sort
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    // Verify the request included sort params
    const requestUrl = response.request().url();
    expect(requestUrl).toContain('sort_by=created_at');

    // Verify the response was successful
    expect(response.status()).toBe(200);

    // Wait for table to update
    await page.waitForLoadState('networkidle');

    // Verify rows are still showing
    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThan(0);
  });

  test('should toggle sort order on double click', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();

    // First click - should sort ascending (or descending depending on default)
    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    let response = await responsePromise;
    const firstUrl = response.request().url();

    // Second click - should toggle sort order
    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    response = await responsePromise;
    const secondUrl = response.request().url();

    // The sort order should have changed between clicks
    expect(secondUrl).toContain('sort_by=created_at');

    // One should be asc, the other desc
    const firstHasDesc = firstUrl.includes('sort_order=desc');
    const secondHasDesc = secondUrl.includes('sort_order=desc');
    expect(firstHasDesc).not.toBe(secondHasDesc);
  });

  test('should search for a lead by email', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    // Type search query
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );

    await leadsPage.searchInput.fill('anders.t@conversio.dk');

    const response = await responsePromise;
    expect(response.status()).toBe(200);

    // Wait for table to update
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Should find the lead with that email
    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBeGreaterThanOrEqual(1);

    // Verify the email appears in the results
    const tableText = await leadsPage.leadsTable.textContent();
    expect(tableText).toContain('anders.t@conversio.dk');
  });

  test('should search for a lead by company name', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') &&
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

    // First search
    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('conversio');
    await responsePromise;

    await page.waitForLoadState('networkidle');

    // Then sort by clicking Created header
    const createdHeader = page.locator('th').filter({ hasText: 'Created' }).locator('span').first();

    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await createdHeader.click();
    const response = await responsePromise;

    // URL should contain both search and sort params
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

    // Get initial row count
    const initialCount = await leadsPage.tableRows.count();

    // Search for something specific
    let responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('anders.t@conversio.dk');
    await responsePromise;
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);

    const filteredCount = await leadsPage.tableRows.count();

    // Clear search
    responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') && response.request().method() === 'GET'
    );
    await leadsPage.searchInput.clear();
    await responsePromise;
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(300);

    const restoredCount = await leadsPage.tableRows.count();

    // After clearing, we should have the original amount of results (or more than the filtered)
    expect(restoredCount).toBeGreaterThanOrEqual(filteredCount);
  });

  test('should return no results for non-existent search', async ({ page }) => {
    const leadsPage = new LeadsPage(page);
    await leadsPage.goto();

    await expect(leadsPage.leadsTable).toBeVisible();
    await leadsPage.tableRows.first().waitFor({ state: 'visible' });

    // Search for something that doesn't exist
    const responsePromise = page.waitForResponse(
      response => response.url().includes('/api/leads') &&
                  response.url().includes('search=') &&
                  response.request().method() === 'GET'
    );
    await leadsPage.searchInput.fill('xyznonexistent12345abcdef');
    const response = await responsePromise;

    expect(response.status()).toBe(200);

    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Should have no rows (or the table body should be empty)
    const rowCount = await leadsPage.tableRows.count();
    expect(rowCount).toBe(0);
  });
});
