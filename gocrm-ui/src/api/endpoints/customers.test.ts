import { beforeEach, describe, expect, it, vi } from 'vitest';
import { api } from '../client';
import { customersApi, type CreateCustomerData } from './customers';

vi.mock('../client', () => ({ api: { get: vi.fn(), post: vi.fn(), put: vi.fn() } }));

const backendCustomer = {
  id: 7,
  first_name: 'Jane',
  last_name: 'Smith',
  company: 'Example',
  website: 'https://example.com/about',
};
const createData: CreateCustomerData = {
  contact_name: 'Jane Smith',
  company_name: 'Example',
  email: 'jane@example.com',
  phone: '123',
  address: '',
  city: '',
  state: '',
  country: '',
  postal_code: '',
  website: backendCustomer.website,
};

describe('customer websites', () => {
  beforeEach(() => vi.clearAllMocks());

  it('sends the website when creating and preserves it in the response', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendCustomer });
    const customer = await customersApi.createCustomer(createData);
    expect(api.post).toHaveBeenCalledWith(
      '/customers',
      expect.objectContaining({ website: createData.website }),
    );
    expect(customer.website).toBe(createData.website);
  });

  it.each(['https://updated.example', ''])(
    'sends an explicit website update %j',
    async (website) => {
      vi.mocked(api.put).mockResolvedValue({ data: { ...backendCustomer, website } });
      const customer = await customersApi.updateCustomer(7, { website });
      expect(api.put).toHaveBeenCalledWith('/customers/7', { website });
      expect(customer.website).toBe(website);
    },
  );

  it('omits website when only another field changes', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: backendCustomer });
    await customersApi.updateCustomer(7, { phone: '456' });
    expect(api.put).toHaveBeenCalledWith('/customers/7', { phone: '456' });
  });

  it('keeps websites on detail and list reads', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: backendCustomer });
    expect((await customersApi.getCustomer(7)).website).toBe(backendCustomer.website);
    vi.mocked(api.get).mockResolvedValueOnce({ data: { customers: [backendCustomer], total: 1 } });
    expect((await customersApi.getCustomers()).data[0].website).toBe(backendCustomer.website);
  });

  it('defaults to empty for responses from older servers', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { ...backendCustomer, website: undefined } });
    expect((await customersApi.getCustomer(7)).website).toBe('');
  });
});

// The customer API has no industry, revenue, employee count or activation
// field (the Company record owns industry and size). Reads must not invent
// them, or the UI shows defaults the server never stored.
describe('customer read model', () => {
  beforeEach(() => vi.clearAllMocks());

  it('does not fabricate industry, annual revenue, employee count or active status', async () => {
    vi.mocked(api.get).mockResolvedValueOnce({ data: backendCustomer });
    const customer = await customersApi.getCustomer(7);
    for (const field of ['industry', 'annual_revenue', 'employee_count', 'is_active']) {
      expect(customer).not.toHaveProperty(field);
    }
  });
});
