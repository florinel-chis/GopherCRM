import { test, expect } from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

// leads.notes is a TEXT column: 65,535 bytes on MySQL, unbounded on SQLite.
// A larger value through the lead API used to reach the insert and answer 500.
test.describe('Leads API - notes and their column', () => {
  test('notes larger than the column are a 400, notes that fit are stored', async ({ request }) => {
    const login = await request.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    // An admin creating a lead must name its owner; the admin owns these.
    const me = await request.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const ownerId: number = (await me.json()).data.id;
    const stamp = Date.now();

    const tooLarge = await request.post(`${API_BASE_URL}/leads`, {
      headers,
      data: { first_name: 'Notes', last_name: `TooLarge ${stamp}`, email: `notes_large_${stamp}@example.com`, owner_id: ownerId, notes: 'x'.repeat(65536) },
    });
    expect(tooLarge.status()).toBe(400);

    const fits = await request.post(`${API_BASE_URL}/leads`, {
      headers,
      data: { first_name: 'Notes', last_name: `Fits ${stamp}`, email: `notes_fit_${stamp}@example.com`, owner_id: ownerId, notes: 'x'.repeat(65535) },
    });
    expect(fits.status()).toBe(201);
    const lead = (await fits.json()).data;
    expect(lead.notes.length).toBe(65535);

    // Remove only the lead this test created.
    await request.delete(`${API_BASE_URL}/leads/${lead.id}`, { headers });
  });
});
