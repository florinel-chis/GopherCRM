import { describe, it, expect, vi, beforeEach } from 'vitest';
import { companiesApi } from './companies';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendCompany = {
  id: 7,
  name: 'Acme Widgets',
  domain: 'acme.example',
  website: 'https://www.acme.example',
  industry: 'Manufacturing',
  employee_range: '51-200',
  phone: '+40 21 555 0100',
  address: '1 Foundry Lane',
  city: 'Cluj',
  state: 'CJ',
  country: 'Romania',
  postal_code: '400001',
  notes: '',
  owner_id: 1,
  created_at: '2026-09-29T08:00:00Z',
  updated_at: '2026-09-29T08:00:00Z',
};

describe('companiesApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists companies with offset/limit/search/sort params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [backendCompany],
      meta: { total: 42, page: 1, per_page: 20 },
    });

    const result = await companiesApi.getCompanies({
      offset: 20,
      limit: 20,
      search: 'acme',
      sort_by: 'name',
      sort_order: 'desc',
    });

    expect(api.get).toHaveBeenCalledWith('/companies', {
      params: { offset: 20, limit: 20, search: 'acme', sort_by: 'name', sort_order: 'desc' },
    });
    expect(result.companies).toEqual([backendCompany]);
    expect(result.total).toBe(42);
  });

  it('falls back to the page length when the response carries no meta', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendCompany, { ...backendCompany, id: 8 }] });

    const result = await companiesApi.getCompanies();

    expect(result.total).toBe(2);
  });

  it('returns an empty list when the payload is not an array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: undefined });

    await expect(companiesApi.getCompanies()).resolves.toEqual({ companies: [], total: 0 });
  });

  it('fetches one company at /companies/{id} with its owner and counts', async () => {
    const detail = {
      ...backendCompany,
      owner: { id: 1, first_name: 'Ana', last_name: 'Pop' },
      customer_count: 3,
      lead_count: 5,
    };
    vi.mocked(api.get).mockResolvedValue({ data: detail });

    const company = await companiesApi.getCompany(7);

    expect(api.get).toHaveBeenCalledWith('/companies/7');
    expect(company.customer_count).toBe(3);
    expect(company.lead_count).toBe(5);
    expect(company.owner?.first_name).toBe('Ana');
  });

  it('creates a company with the body as given', async () => {
    vi.mocked(api.post).mockResolvedValue({ data: backendCompany });

    const created = await companiesApi.createCompany({
      name: 'Acme Widgets',
      domain: 'acme.example',
      employee_range: '51-200',
      owner_id: 1,
    });

    expect(api.post).toHaveBeenCalledWith('/companies', {
      name: 'Acme Widgets',
      domain: 'acme.example',
      employee_range: '51-200',
      owner_id: 1,
    });
    expect(created.id).toBe(7);
  });

  it('propagates a 409 duplicate-domain rejection to the caller', async () => {
    const conflict = Object.assign(new Error('conflict'), {
      response: {
        status: 409,
        data: { code: 'DUPLICATE_DOMAIN', message: 'A company with this domain already exists' },
      },
    });
    vi.mocked(api.post).mockRejectedValue(conflict);

    await expect(
      companiesApi.createCompany({ name: 'Acme Widgets', domain: 'acme.example' })
    ).rejects.toBe(conflict);
  });

  it('updates a company at /companies/{id} with PUT', async () => {
    vi.mocked(api.put).mockResolvedValue({ data: { ...backendCompany, industry: 'Robotics' } });

    const updated = await companiesApi.updateCompany(7, { name: 'Acme Widgets', industry: 'Robotics' });

    expect(api.put).toHaveBeenCalledWith('/companies/7', { name: 'Acme Widgets', industry: 'Robotics' });
    expect(updated.industry).toBe('Robotics');
  });

  it('deletes a company at /companies/{id}', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: undefined });

    await companiesApi.deleteCompany(7);

    expect(api.delete).toHaveBeenCalledWith('/companies/7');
  });

  it('lists the customers of a company and maps the raw rows to the frontend shape', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [
        {
          id: 11,
          first_name: 'Ana',
          last_name: 'Pop',
          email: 'ana@acme.example',
          company: 'Acme Widgets',
          company_id: 7,
        },
      ],
      meta: { total: 1 },
    });

    const result = await companiesApi.getCompanyCustomers(7, { offset: 0, limit: 5 });

    expect(api.get).toHaveBeenCalledWith('/companies/7/customers', {
      params: { offset: 0, limit: 5 },
    });
    expect(result.total).toBe(1);
    expect(result.customers[0].contact_name).toBe('Ana Pop');
    expect(result.customers[0].company_name).toBe('Acme Widgets');
    expect(result.customers[0].company_id).toBe(7);
  });

  it('lists the leads of a company and maps the raw rows to the frontend shape', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [
        {
          id: 21,
          first_name: 'Ion',
          last_name: 'Ionescu',
          email: 'ion@acme.example',
          company: 'Acme Widgets',
          status: 'new',
          company_id: 7,
        },
      ],
      meta: { total: 9 },
    });

    const result = await companiesApi.getCompanyLeads(7, { offset: 5, limit: 5 });

    expect(api.get).toHaveBeenCalledWith('/companies/7/leads', {
      params: { offset: 5, limit: 5 },
    });
    expect(result.total).toBe(9);
    expect(result.leads[0].contact_name).toBe('Ion Ionescu');
    expect(result.leads[0].company_name).toBe('Acme Widgets');
  });
});
