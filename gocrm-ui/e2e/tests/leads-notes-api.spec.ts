import { test, expect } from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

// leads.notes is a TEXT column: 65,535 bytes on MySQL/MariaDB, unbounded on
// SQLite. A larger value through the lead API used to reach the database and
// answer 500.
const NOTES_MAX_BYTES = 65535;
const TOO_LONG_MESSAGE = `Notes are too long (at most ${NOTES_MAX_BYTES} bytes)`;

test.describe('Leads API - notes and their column', () => {
  test('notes larger than the column are a 400 on create and update; notes that fit are stored intact', async ({
    request,
  }) => {
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
    const created: number[] = [];

    try {
      const tooLarge = await request.post(`${API_BASE_URL}/leads`, {
        headers,
        data: {
          first_name: 'Notes',
          last_name: `TooLarge ${stamp}`,
          email: `notes_large_${stamp}@example.com`,
          owner_id: ownerId,
          notes: 'x'.repeat(NOTES_MAX_BYTES + 1),
        },
      });
      expect(tooLarge.status()).toBe(400);
      expect((await tooLarge.json()).error.message).toBe(TOO_LONG_MESSAGE);

      const notes = 'x'.repeat(NOTES_MAX_BYTES);
      const fits = await request.post(`${API_BASE_URL}/leads`, {
        headers,
        data: {
          first_name: 'Notes',
          last_name: `Fits ${stamp}`,
          email: `notes_fit_${stamp}@example.com`,
          owner_id: ownerId,
          notes,
        },
      });
      expect(fits.status()).toBe(201);
      const lead = (await fits.json()).data;
      created.push(lead.id);
      expect(lead.notes).toBe(notes);

      const tooLargeUpdate = await request.put(`${API_BASE_URL}/leads/${lead.id}`, {
        headers,
        data: { notes: 'y'.repeat(NOTES_MAX_BYTES + 1) },
      });
      expect(tooLargeUpdate.status()).toBe(400);
      expect((await tooLargeUpdate.json()).error.message).toBe(TOO_LONG_MESSAGE);

      const unchanged = await request.get(`${API_BASE_URL}/leads/${lead.id}`, { headers });
      expect((await unchanged.json()).data.notes, 'a rejected update leaves the notes as they were').toBe(notes);
    } finally {
      // Remove only the leads this test created, whatever happened above.
      for (const id of created) {
        await request.delete(`${API_BASE_URL}/leads/${id}`, { headers });
      }
    }
  });
});
