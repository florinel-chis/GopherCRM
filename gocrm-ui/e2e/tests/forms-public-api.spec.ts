import { test, expect, type APIRequestContext } from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

// The public forms API at the level a third-party site uses it, run against
// MySQL. Lead-mapped values are stored in sized varchar columns that MySQL
// enforces and SQLite ignores: before the column limits, an over-long phone or
// email reached the insert and the public endpoint answered 500.

// A foreign page, as on an embedding site; the test form allows any origin.
const SITE_ORIGIN = 'https://e2e-site.example';
// The service rejects a challenge younger than three seconds as a bot.
const TIME_TRAP_MS = 3500;

interface PublishedForm {
  id: number;
  publicId: string;
}

async function adminToken(request: APIRequestContext): Promise<string> {
  const response = await request.post(`${API_BASE_URL}/auth/login`, {
    data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
  });
  expect(response.status(), 'admin login').toBe(200);
  return (await response.json()).data.token;
}

const DEFAULT_FIELDS = [
  { name: 'first_name', label: 'First name', type: 'text', max_length: 1000 },
  { name: 'phone', label: 'Phone', type: 'text', max_length: 1000 },
  { name: 'email', label: 'Email', type: 'email', required: true },
  { name: 'message', label: 'Message', type: 'textarea' },
];

async function createForm(
  request: APIRequestContext,
  token: string,
  fields: Record<string, unknown>[] = DEFAULT_FIELDS
): Promise<PublishedForm> {
  const auth = { Authorization: `Bearer ${token}` };
  const me = await request.get(`${API_BASE_URL}/users/me`, { headers: auth });
  expect(me.status(), 'GET /users/me').toBe(200);
  const ownerId = (await me.json()).data.id;

  const response = await request.post(`${API_BASE_URL}/forms`, {
    headers: auth,
    data: {
      name: `E2E column limits ${Date.now()}`,
      status: 'published',
      submit_action: 'message',
      create_lead: true,
      default_owner_id: ownerId,
      fields,
    },
  });
  expect(response.status(), 'POST /forms').toBe(201);
  const body = await response.json();
  return { id: body.data.id, publicId: body.data.public_id };
}

async function freshChallenge(request: APIRequestContext, publicId: string) {
  const response = await request.get(`${API_BASE_URL}/forms/public/${publicId}`, {
    headers: { Origin: SITE_ORIGIN },
  });
  expect(response.status(), 'public definition').toBe(200);
  const definition = (await response.json()).data;
  // Wait out the time trap, or the submission is filed as spam.
  await new Promise((resolve) => setTimeout(resolve, TIME_TRAP_MS));
  return definition;
}

async function submit(
  request: APIRequestContext,
  publicId: string,
  challenge: string,
  values: Record<string, string>
) {
  return request.post(`${API_BASE_URL}/forms/public/${publicId}/submissions`, {
    headers: { Origin: SITE_ORIGIN },
    data: { values, challenge, page_url: `${SITE_ORIGIN}/contact` },
  });
}

async function findSubmission(request: APIRequestContext, token: string, formId: number, email: string) {
  const response = await request.get(`${API_BASE_URL}/forms/${formId}/submissions`, {
    headers: { Authorization: `Bearer ${token}` },
  });
  expect(response.status()).toBe(200);
  return (await response.json()).data.find((submission: { email: string }) => submission.email === email);
}

test.describe('Public forms API - values and their columns', () => {
  let token: string;
  let form: PublishedForm;
  // Leads created by the test's submissions, removed with the form.
  let leadIds: number[] = [];

  test.beforeEach(async ({ request }) => {
    leadIds = [];
    token = await adminToken(request);
    form = await createForm(request, token);
  });

  test.afterEach(async ({ request }) => {
    // Only the form this test created is removed.
    const headers = { Authorization: `Bearer ${token}` };
    for (const id of leadIds) {
      await request.delete(`${API_BASE_URL}/leads/${id}`, { headers });
    }
    if (form) {
      await request.delete(`${API_BASE_URL}/forms/${form.id}`, { headers });
    }
  });

  test('a value wider than its column is a 400 field error, not a 500', async ({ request }) => {
    const definition = await freshChallenge(request, form.publicId);

    const limits = Object.fromEntries(
      definition.fields.map((field: { name: string; max_length: number }) => [field.name, field.max_length])
    );
    expect(limits.phone, 'the published definition states the enforced limit').toBe(50);
    expect(limits.email).toBe(255);

    const widePhone = await submit(request, form.publicId, definition.challenge, {
      email: `wide_phone_${Date.now()}@example.com`,
      phone: '1'.repeat(51),
    });
    expect(widePhone.status()).toBe(400);
    const phoneError = await widePhone.json();
    expect(JSON.stringify(phoneError.error), 'the error names the field').toContain('phone');

    const wideEmail = await submit(request, form.publicId, definition.challenge, {
      email: `${'a'.repeat(250)}@example.com`,
    });
    expect(wideEmail.status()).toBe(400);
  });

  test('values that exactly fill their columns are accepted and stored intact', async ({ request }) => {
    const definition = await freshChallenge(request, form.publicId);
    const email = `fill_${Date.now()}@example.com`;
    const firstName = 'ä'.repeat(100); // counted in characters, as MySQL does
    const phone = '2'.repeat(50);

    const response = await submit(request, form.publicId, definition.challenge, {
      email,
      first_name: firstName,
      phone,
      message: 'x'.repeat(1000),
    });
    expect(response.status()).toBe(200);

    const submissions = await request.get(`${API_BASE_URL}/forms/${form.id}/submissions`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(submissions.status()).toBe(200);
    const stored = (await submissions.json()).data.find(
      (submission: { email: string }) => submission.email === email
    );
    expect(stored?.status, 'the submission is received, not filed as spam').toBe('received');
    expect(stored?.lead_id).toBeTruthy();
    leadIds.push(stored.lead_id);

    const lead = await request.get(`${API_BASE_URL}/leads/${stored.lead_id}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(lead.status()).toBe(200);
    const leadData = (await lead.json()).data;
    expect(leadData.first_name).toBe(firstName);
    expect(leadData.phone).toBe(phone);
  });
});

// Large submissions against the TEXT columns (65,535 bytes on MySQL):
// form_submissions.data holds the JSON-encoded values, in which `<` and `&`
// become six-byte escapes, and a lead's notes gain one block per submission
// from the same address. Either used to overflow and answer 500.
test.describe('Public forms API - large submissions', () => {
  let token: string;
  let form: PublishedForm;
  // Leads created by the test's submissions, removed with the form.
  let leadIds: number[] = [];

  test.beforeEach(async ({ request }) => {
    leadIds = [];
    token = await adminToken(request);
    form = await createForm(request, token, [
      { name: 'email', label: 'Email', type: 'email', required: true },
      { name: 'ref', label: 'Reference', type: 'text' },
      { name: 'message', label: 'Message', type: 'textarea', max_length: 10000 },
      { name: 'details', label: 'Details', type: 'textarea', max_length: 10000 },
    ]);
  });

  test.afterEach(async ({ request }) => {
    const headers = { Authorization: `Bearer ${token}` };
    for (const id of leadIds) {
      await request.delete(`${API_BASE_URL}/leads/${id}`, { headers });
    }
    if (form) {
      await request.delete(`${API_BASE_URL}/forms/${form.id}`, { headers });
    }
  });

  test('markup-heavy values are accepted and stored intact', async ({ request }) => {
    const definition = await freshChallenge(request, form.publicId);
    const email = `markup_${Date.now()}@example.com`;
    // About 12 KB on the wire, about 72 KB once JSON-escaped for storage.
    const message = '<'.repeat(10000);
    const details = '&'.repeat(2000);

    const response = await submit(request, form.publicId, definition.challenge, { email, message, details });
    expect(response.status()).toBe(200);

    const stored = await findSubmission(request, token, form.id, email);
    expect(stored?.status).toBe('received');
    if (stored?.lead_id) leadIds.push(stored.lead_id);
    expect(stored?.data?.message).toBe(message);
    expect(stored?.data?.details).toBe(details);
  });

  test('repeated large submissions from one address keep the lead notes within the column', async ({ request }) => {
    const definition = await freshChallenge(request, form.publicId);
    const email = `notes_${Date.now()}@example.com`;
    const message = '😀'.repeat(5000); // 5,000 characters, about 20 KB of UTF-8

    for (let i = 1; i <= 5; i++) {
      const response = await submit(request, form.publicId, definition.challenge, { email, ref: `sub-${i}`, message });
      expect(response.status(), `submission ${i}`).toBe(200);
    }

    const stored = await findSubmission(request, token, form.id, email);
    expect(stored?.lead_id).toBeTruthy();
    leadIds.push(stored.lead_id);
    const lead = await request.get(`${API_BASE_URL}/leads/${stored.lead_id}`, {
      headers: { Authorization: `Bearer ${token}` },
    });
    expect(lead.status()).toBe(200);
    const notes: string = (await lead.json()).data.notes;
    expect(Buffer.byteLength(notes, 'utf8')).toBeLessThanOrEqual(65535);
    expect(notes, 'the newest submission is always kept').toContain('sub-5');
  });

  // A lead created by a form starts with a form block, so staff notes added
  // later follow it. Large public submissions from the lead's address must
  // never push those notes out.
  test('staff notes added after a form block survive repeated large submissions', async ({ request }) => {
    const headers = { Authorization: `Bearer ${token}` };
    const definition = await freshChallenge(request, form.publicId);
    const email = `staffnotes_${Date.now()}@example.com`;

    const first = await submit(request, form.publicId, definition.challenge, { email, ref: 'sub-0', message: 'hello' });
    expect(first.status()).toBe(200);
    const stored = await findSubmission(request, token, form.id, email);
    expect(stored?.lead_id).toBeTruthy();
    leadIds.push(stored.lead_id);

    const staffNote = 'Staff: called on Monday, wants a demo';
    const before = await request.get(`${API_BASE_URL}/leads/${stored.lead_id}`, { headers });
    const created: string = (await before.json()).data.notes;
    const updated = await request.put(`${API_BASE_URL}/leads/${stored.lead_id}`, {
      headers,
      data: { notes: `${created}\n\n${staffNote}` },
    });
    expect(updated.status()).toBe(200);

    const message = '😀'.repeat(5000); // about 20 KB of UTF-8 each
    for (let i = 1; i <= 5; i++) {
      const response = await submit(request, form.publicId, definition.challenge, { email, ref: `sub-${i}`, message });
      expect(response.status(), `submission ${i}`).toBe(200);
    }

    const lead = await request.get(`${API_BASE_URL}/leads/${stored.lead_id}`, { headers });
    const notes: string = (await lead.json()).data.notes;
    expect(Buffer.byteLength(notes, 'utf8')).toBeLessThanOrEqual(65535);
    expect(notes, 'the staff note survives').toContain(staffNote);
    expect(notes, 'the newest submission is kept').toContain('sub-5');
    expect(notes, 'older form blocks are what gets trimmed').not.toContain('Reference: sub-1');
  });
});
