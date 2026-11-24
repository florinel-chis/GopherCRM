import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Documentation captures for embeddable forms (FEATURES.md section 10b): the
 * form list, the builder (the edit page of one form, never saved) and the
 * form's detail page with its embed snippet and one submission.
 *
 * Two forms are seeded through the API with fixed names and created only when
 * missing. The published one gets one submission through the public API,
 * the way an embedding site sends it, only while it has none, so repeated
 * runs against the same database do not pile up rows. Nothing is deleted.
 */
test.describe.configure({ mode: 'serial' });

const LOADED = { timeout: 30 * 1000 };
// A page of an embedding site; the demo form allows any origin.
const SITE_ORIGIN = 'https://harbor-logistics.example';
// The service files a submission younger than three seconds as spam.
const TIME_TRAP_MS = 3500;

const CONTACT_FORM = 'Website contact form';
const WEBINAR_FORM = 'Webinar registration';
const SUBMITTER_EMAIL = 'elena.marin@harbor-logistics.example';

const CONTACT_FIELDS = [
  { name: 'first_name', label: 'First name', type: 'text', required: true },
  { name: 'last_name', label: 'Last name', type: 'text', required: true },
  { name: 'email', label: 'Work email', type: 'email', required: true },
  { name: 'company', label: 'Company', type: 'text', required: false },
  { name: 'message', label: 'How can we help?', type: 'textarea', required: false },
];

let contactFormId = 0;

async function seedData(): Promise<number> {
  const api: APIRequestContext = await playwrightRequest.newContext();
  try {
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    const me = await api.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const ownerId: number = (await me.json()).data.id;

    const listed = await api.get(`${API_BASE_URL}/forms`, { headers, params: { limit: 100 } });
    expect(listed.status()).toBe(200);
    const forms: Array<{ id: number; name: string; public_id: string }> = (await listed.json()).data ?? [];

    const ensureForm = async (name: string, data: Record<string, unknown>) => {
      const existing = forms.find((form) => form.name === name);
      if (existing) return existing;
      const created = await api.post(`${API_BASE_URL}/forms`, { headers, data: { name, ...data } });
      expect(created.status(), 'POST /forms').toBe(201);
      return (await created.json()).data as { id: number; public_id: string };
    };

    const contact = await ensureForm(CONTACT_FORM, {
      description: 'The contact form on the public website. Every submission becomes a lead.',
      status: 'published',
      submit_action: 'message',
      thank_you_message: 'Thank you, we will get back to you within one business day.',
      consent_text: 'I agree to be contacted about my enquiry.',
      create_lead: true,
      default_owner_id: ownerId,
      fields: CONTACT_FIELDS,
    });
    await ensureForm(WEBINAR_FORM, {
      description: 'Sign-up for the quarterly product webinar.',
      status: 'draft',
      submit_action: 'message',
      thank_you_message: 'You are registered. The joining link follows by email.',
      create_lead: false,
      default_owner_id: ownerId,
      fields: [
        { name: 'name', label: 'Full name', type: 'text', required: true },
        { name: 'email', label: 'Email', type: 'email', required: true },
      ],
    });

    const submissions = await api.get(`${API_BASE_URL}/forms/${contact.id}/submissions`, { headers });
    expect(submissions.status()).toBe(200);
    if (((await submissions.json()).data ?? []).length === 0) {
      const definition = await api.get(`${API_BASE_URL}/forms/public/${contact.public_id}`, {
        headers: { Origin: SITE_ORIGIN },
      });
      expect(definition.status(), 'public definition').toBe(200);
      const { challenge } = (await definition.json()).data;
      // Wait out the time trap, or the submission is filed as spam.
      await new Promise((resolve) => setTimeout(resolve, TIME_TRAP_MS));
      const submitted = await api.post(`${API_BASE_URL}/forms/public/${contact.public_id}/submissions`, {
        headers: { Origin: SITE_ORIGIN },
        data: {
          values: {
            first_name: 'Elena',
            last_name: 'Marin',
            email: SUBMITTER_EMAIL,
            company: 'Harbor Logistics',
            message: 'We would like a demo for our three depots next month.',
          },
          consent: true,
          challenge,
          page_url: `${SITE_ORIGIN}/contact`,
        },
      });
      expect(submitted.status(), 'public submission').toBeLessThan(300);
    }
    return contact.id;
  } finally {
    await api.dispose();
  }
}

test.describe('Screenshots - Forms', () => {
  test.beforeAll(async () => {
    test.setTimeout(120 * 1000);
    contactFormId = await seedData();
  });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(90 * 1000);
    await ensureAdminLoggedIn(page);
  });

  test('forms list', async ({ page }) => {
    await page.goto('/forms');
    await expect(page.getByRole('heading', { name: 'Forms', level: 4 })).toBeVisible(LOADED);
    const rows = page.locator('table tbody tr');
    await expect(rows.filter({ hasText: CONTACT_FORM }).first()).toBeVisible(LOADED);
    await expect(rows.filter({ hasText: WEBINAR_FORM }).first()).toBeVisible(LOADED);

    await capture(page, 'forms', '01-list', { fullPage: true });
  });

  test('form builder', async ({ page }) => {
    await page.goto(`/forms/${contactFormId}/edit`);
    await expect(page.getByRole('heading', { name: 'Edit Form' })).toBeVisible(LOADED);
    await expect(page.getByLabel(/^Name/).first()).toHaveValue(CONTACT_FORM, LOADED);

    // Captured as loaded and never saved.
    await capture(page, 'forms', '02-builder', { fullPage: true });
  });

  test('form detail with a submission', async ({ page }) => {
    await page.goto(`/forms/${contactFormId}`);
    await expect(page.getByRole('heading', { name: CONTACT_FORM, level: 4 })).toBeVisible(LOADED);
    await expect(page.getByTestId('embed-snippet')).toBeVisible(LOADED);
    await expect(page.getByText(SUBMITTER_EMAIL).first()).toBeVisible(LOADED);

    await capture(page, 'forms', '03-detail', { fullPage: true });
  });
});
