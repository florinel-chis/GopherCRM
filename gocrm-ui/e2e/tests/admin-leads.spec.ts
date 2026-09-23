import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LeadsPage } from '../pages/leads.page';
import { generateLeadData } from '../fixtures/admin-user';

test.describe('Admin - Leads Management', () => {
  let adminAuth: AdminAuthHelper;
  let leadsPage: LeadsPage;

  test.beforeEach(async ({ page }) => {
    adminAuth = new AdminAuthHelper(page);
    leadsPage = new LeadsPage(page);
    await adminAuth.ensureAdminLoggedIn();
  });

  test('admin can view leads list page', async () => {
    await leadsPage.goto();

    await expect(leadsPage.pageTitle).toBeVisible();
    await expect(leadsPage.newLeadButton).toBeVisible();
    await expect(leadsPage.leadsTable).toBeVisible();
  });

  test('admin can create a new lead successfully', async ({ page }) => {
    const leadData = generateLeadData();

    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);

    const response = await leadsPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);
    expect(page.url()).toContain('/leads');
  });

  test('admin can edit an existing lead', async ({ page }) => {
    // Edit only the lead this test created: the list is not ordered
    // newest-first, so row 0 may belong to someone else.
    const stamp = Date.now();
    const originalLeadData = { ...generateLeadData(), companyName: `E2E Edit ${stamp}` };
    const updatedCompany = `E2E Edited ${stamp}`;
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(originalLeadData);
    await leadsPage.saveAndWaitForResponse();

    await leadsPage.goto();
    await leadsPage.searchLeads(originalLeadData.companyName);
    await expect(leadsPage.tableRows).toHaveCount(1);
    await leadsPage.editLead(0);

    await leadsPage.companyNameInput.clear();
    await leadsPage.companyNameInput.fill(updatedCompany);
    await leadsPage.contactNameInput.clear();
    await leadsPage.contactNameInput.fill('Updated Contact');
    await leadsPage.saveButton.click();

    await page.waitForURL(/\/leads(?!.*edit)/, { timeout: 10000 });
    await leadsPage.goto();
    await leadsPage.searchLeads(updatedCompany);
    await expect(page.locator('table tbody tr', { hasText: updatedCompany })).toHaveCount(1);
  });

  test('admin can view lead details', async ({ page }) => {
    // Create a lead first
    const leadData = generateLeadData();
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);

    const response = await leadsPage.saveAndWaitForResponse();
    const responseBody = await response.json();
    const leadId = responseBody?.data?.id;

    // Navigate directly to the lead detail page
    await page.goto(`/leads/${leadId}`);
    await page.waitForLoadState('networkidle');

    expect(page.url()).toMatch(/\/leads\/\d+$/);
    await expect(page.getByText(leadData.companyName).first()).toBeVisible();
  });

  test('admin can delete a lead', async ({ page }) => {
    // Deleting a lead is an irreversible erasure, so the test only ever
    // deletes the lead it created: a unique company name narrows the list to
    // that one row before the delete.
    const leadData = { ...generateLeadData(), companyName: `E2E Delete ${Date.now()}` };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveAndWaitForResponse();

    const ownRow = page.locator('table tbody tr', { hasText: leadData.companyName });
    await leadsPage.goto();
    await leadsPage.searchLeads(leadData.companyName);
    await expect(ownRow).toHaveCount(1);
    await expect(leadsPage.tableRows).toHaveCount(1);

    await leadsPage.deleteLead(0);

    await leadsPage.goto();
    await leadsPage.searchLeads(leadData.companyName);
    await expect(ownRow).toHaveCount(0);
  });

  test('admin can search leads', async ({ page }) => {
    // Create a lead with a unique name
    const leadData = { ...generateLeadData(), companyName: `SearchLeadCo_${Date.now()}` };
    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    await leadsPage.saveAndWaitForResponse();

    await leadsPage.goto();
    await leadsPage.searchLeads(leadData.companyName);
    await expect(page.locator('table tbody tr', { hasText: leadData.companyName })).toHaveCount(1);
    await expect(leadsPage.tableRows).toHaveCount(1);
  });

  // fixme: asserted `count >= 0`, which cannot fail. The filter itself is broken: the
  // backend ignores the parameter (catalog TC-XCUT-038). Once it works, create a lead with a
  // known status and check the filter keeps it and drops it for another status.
  test.fixme('admin can filter leads by status', async ({ page }) => {
    await leadsPage.goto();

    // Apply status filter (should complete without errors)
    await leadsPage.filterByStatus('new');
    await page.waitForTimeout(1000);

    const filteredCount = await leadsPage.getLeadCount();
    expect(filteredCount).toBeGreaterThanOrEqual(0);
  });

  test('admin sees validation errors for invalid lead data', async ({ page }) => {
    await leadsPage.goto();
    await leadsPage.clickNewLead();

    // Try to save without required fields
    await leadsPage.saveButton.click();

    // Should stay on form page
    expect(page.url()).toContain('/leads/new');
  });

  test('admin can handle lead form cancellation', async ({ page }) => {
    await leadsPage.goto();
    await leadsPage.clickNewLead();

    await leadsPage.companyNameInput.fill('Test Company');
    await leadsPage.cancelButton.click();

    await page.waitForURL('**/leads', { timeout: 10000 });
    expect(page.url()).not.toContain('/new');
  });

  test('admin can create lead with all optional fields', async ({ page }) => {
    const completeLeadData = generateLeadData();

    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(completeLeadData);

    const response = await leadsPage.saveAndWaitForResponse();
    expect(response.status()).toBe(201);

    const responseBody = await response.json();
    const leadId = responseBody?.data?.id;
    await page.goto(`/leads/${leadId}`);
    await page.waitForLoadState('networkidle');

    await expect(page.getByText(completeLeadData.companyName).first()).toBeVisible();
  });

  test('admin can navigate between leads efficiently', async ({ page }) => {
    // Create multiple leads
    for (let i = 0; i < 2; i++) {
      const leadData = { ...generateLeadData(), contactName: `TestLead${i}` };
      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      await leadsPage.saveLead();
    }

    await leadsPage.goto();
    await leadsPage.viewLead(0);
    const firstLeadUrl = page.url();

    await leadsPage.goto();
    await leadsPage.viewLead(1);
    const secondLeadUrl = page.url();

    expect(firstLeadUrl).not.toBe(secondLeadUrl);
    expect(firstLeadUrl).toMatch(/\/leads\/\d+$/);
    expect(secondLeadUrl).toMatch(/\/leads\/\d+$/);
  });
});
