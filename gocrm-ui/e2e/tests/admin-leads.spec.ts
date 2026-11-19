import { test, expect } from '@playwright/test';
import { AdminAuthHelper } from '../helpers/admin-auth';
import { LeadsPage } from '../pages/leads.page';
import { generateLeadData } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';
import { faker } from '@faker-js/faker';

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
    await leadsPage.editLead(originalLeadData.companyName);

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

    await leadsPage.deleteLead(leadData.companyName);
    // The delete invalidates the narrowed query; the old row stays rendered
    // until the refetch lands, so this retrying check proves it is gone.
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

  // fixme: GET /leads ignores the status parameter, so the Status dropdown narrows nothing (#426).
  test.fixme('admin can filter leads by status (#426)', async ({ page }) => {
    const stamp = `${Date.now()}${faker.string.alphanumeric(6)}`;
    const contactedCompany = `${stamp} Contacted Co`;
    const qualifiedCompany = `${stamp} Qualified Co`;
    const token = await page.evaluate(() => localStorage.getItem('gophercrm_token'));
    const headers = { Authorization: `Bearer ${token}` };
    // An admin has to name the owner of a lead it creates; it owns these two itself.
    const me = await page.request.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status(), 'current user').toBe(200);
    const ownerId: number = (await me.json()).data.id;
    for (const [company, status] of [
      [contactedCompany, 'contacted'],
      [qualifiedCompany, 'qualified'],
    ]) {
      const created = await page.request.post(`${API_BASE_URL}/leads`, {
        headers,
        data: {
          first_name: 'Filter',
          last_name: 'Lead',
          email: `lead_${stamp}_${status}@example.com`,
          company,
          status,
          owner_id: ownerId,
        },
      });
      expect(created.status(), `seed ${status} lead`).toBe(201);
    }
    const contactedRow = leadsPage.tableRows.filter({ hasText: contactedCompany });
    const qualifiedRow = leadsPage.tableRows.filter({ hasText: qualifiedCompany });

    await leadsPage.goto();
    await leadsPage.searchLeads(stamp);
    await expect(contactedRow).toHaveCount(1);
    await expect(qualifiedRow).toHaveCount(1);

    await leadsPage.filterByStatus('qualified');
    await expect(qualifiedRow).toBeVisible();
    await expect(contactedRow).toHaveCount(0);
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

  // #398: an emptied optional text field is sent as "" and the API stores it.
  test('admin clears the phone and notes of a lead and they stay empty after a reload', async ({ page }) => {
    // The 30 s waits after each navigation need more than the default test timeout.
    test.setTimeout(120000);
    const company = `E2E Clear ${Date.now()}`;
    const leadData = {
      ...generateLeadData(),
      companyName: company,
      phone: '+44 20 7946 0001',
      notes: 'Clear me: demo booked for Tuesday.',
    };

    await leadsPage.goto();
    await leadsPage.clickNewLead();
    await leadsPage.fillLeadForm(leadData);
    const created = await leadsPage.saveAndWaitForResponse();
    expect(created.status()).toBe(201);
    const leadId = (await created.json())?.data?.id;
    expect(leadId).toBeTruthy();

    // The edit form is filled by a reset once the lead query resolves, so
    // wait for the stored values before touching the fields.
    await page.goto(`/leads/${leadId}/edit`);
    await expect(leadsPage.companyNameInput).toHaveValue(company, { timeout: 30000 });
    await expect(leadsPage.phoneInput).toHaveValue(leadData.phone);
    await expect(leadsPage.notesTextarea).toHaveValue(leadData.notes);

    await leadsPage.phoneInput.clear();
    await leadsPage.notesTextarea.clear();

    const updatePromise = page.waitForResponse(
      response =>
        new URL(response.url()).pathname.endsWith(`/leads/${leadId}`) &&
        response.request().method() === 'PUT'
    );
    await leadsPage.saveButton.click();
    const updated = await updatePromise;
    expect(updated.status()).toBe(200);
    const sent = updated.request().postDataJSON();
    expect(sent.phone).toBe('');
    expect(sent.notes).toBe('');
    await page.waitForURL(/\/leads(?!.*edit)/, { timeout: 30000 });

    // Open the edit form again and reload it, so the values come from the
    // server and not from a cached query. The inputs are briefly empty
    // before the reset: the unique company proves the stored lead is on
    // screen before the two cleared fields are judged.
    await page.goto(`/leads/${leadId}/edit`);
    await page.reload();
    await expect(leadsPage.companyNameInput).toHaveValue(company, { timeout: 30000 });
    await expect(leadsPage.emailInput).toHaveValue(leadData.email);
    await expect(leadsPage.phoneInput).toHaveValue('');
    await expect(leadsPage.notesTextarea).toHaveValue('');
  });

  test('admin can navigate between leads efficiently', async ({ page }) => {
    // Two leads of this test's own. Neither company name contains the other:
    // the list search and the row filter both match substrings.
    const stamp = Date.now();
    const leads: { companyName: string; id: number }[] = [];
    for (const letter of ['A', 'B']) {
      // The generated two-word contact name is kept: the API answers 400 to
      // a contact without a last name.
      const leadData = { ...generateLeadData(), companyName: `E2E Nav ${letter} ${stamp}` };
      await leadsPage.goto();
      await leadsPage.clickNewLead();
      await leadsPage.fillLeadForm(leadData);
      const response = await leadsPage.saveAndWaitForResponse();
      expect(response.status()).toBe(201);
      const id = (await response.json())?.data?.id;
      expect(typeof id).toBe('number');
      leads.push({ companyName: leadData.companyName, id });
    }
    expect(leads[0].id).not.toBe(leads[1].id);

    // Each detail page opened from the list is the lead that was created.
    for (const lead of leads) {
      await leadsPage.goto();
      await leadsPage.viewLead(lead.companyName);
      await expect(page).toHaveURL(new RegExp(`/leads/${lead.id}$`));
    }
  });
});
