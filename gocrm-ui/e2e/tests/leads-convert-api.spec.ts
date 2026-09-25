import { test, expect } from '@playwright/test';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

// A lead may carry only a phone number, but every customer needs a unique
// email (customers.email is unique and not null). Converting a phone-only lead
// used to create a customer with an empty email, and the second such
// conversion hit the unique index and answered 500. A lead whose email already
// belongs to a customer answered 500 as well.
const NO_EMAIL_MESSAGE =
  'This lead has no email address. Add one before converting it: every customer needs an email.';

test.describe('Leads API - conversion needs a free email address', () => {
  test('phone-only leads are refused with 400; an email taken by a customer is a 409', async ({ request }) => {
    const login = await request.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    const me = await request.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const ownerId: number = (await me.json()).data.id;
    const stamp = Date.now();
    const leads: number[] = [];
    const customers: number[] = [];

    try {
      // Two in a row: the second is the one that used to reach the unique index.
      for (const n of [1, 2]) {
        const created = await request.post(`${API_BASE_URL}/leads`, {
          headers,
          data: {
            first_name: 'Phone',
            last_name: `Only ${n} ${stamp}`,
            phone: `+40 700 ${stamp % 1000000} ${n}`,
            owner_id: ownerId,
            status: 'qualified',
          },
        });
        expect(created.status()).toBe(201);
        const lead = (await created.json()).data;
        leads.push(lead.id);

        const convert = await request.post(`${API_BASE_URL}/leads/${lead.id}/convert`, {
          headers,
          data: { company_name: 'Acme' },
        });
        expect(convert.status(), `conversion ${n}`).toBe(400);
        expect((await convert.json()).error.message).toBe(NO_EMAIL_MESSAGE);

        const unchanged = await request.get(`${API_BASE_URL}/leads/${lead.id}`, { headers });
        expect((await unchanged.json()).data.status, 'a refused conversion leaves the lead as it was').toBe(
          'qualified'
        );
      }

      const email = `convert_taken_${stamp}@example.com`;
      const customer = await request.post(`${API_BASE_URL}/customers`, {
        headers,
        data: { first_name: 'Existing', last_name: `Customer ${stamp}`, email },
      });
      expect(customer.status()).toBe(201);
      customers.push((await customer.json()).data.id);

      const lead = await request.post(`${API_BASE_URL}/leads`, {
        headers,
        data: { first_name: 'Second', last_name: `Contact ${stamp}`, email, owner_id: ownerId, status: 'qualified' },
      });
      expect(lead.status()).toBe(201);
      const leadId: number = (await lead.json()).data.id;
      leads.push(leadId);

      const conflict = await request.post(`${API_BASE_URL}/leads/${leadId}/convert`, {
        headers,
        data: { company_name: 'Acme' },
      });
      expect(conflict.status()).toBe(409);
    } finally {
      // Remove only the records this test created, whatever happened above.
      for (const id of leads) {
        await request.delete(`${API_BASE_URL}/leads/${id}`, { headers });
      }
      for (const id of customers) {
        await request.delete(`${API_BASE_URL}/customers/${id}`, { headers });
      }
    }
  });
});
