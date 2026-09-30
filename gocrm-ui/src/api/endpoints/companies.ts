import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type { Company, CompanyEmployeeRange, Customer, Lead } from '@/types';
import { transformCustomerFromBackend } from './customers';
import { transformLeadFromBackend } from './leads';

// GET /companies takes offset/limit (not page/per_page), a free-text search
// over name, domain, industry and city, and a sort column from the allowlist
// name | domain | industry | created_at | updated_at.
export interface CompanyFilters {
  offset?: number;
  limit?: number;
  search?: string;
  sort_by?: 'name' | 'domain' | 'industry' | 'created_at' | 'updated_at';
  sort_order?: 'asc' | 'desc';
}

export interface CompanyListResult {
  companies: Company[];
  total: number;
}

export interface CompanyCustomersResult {
  customers: Customer[];
  total: number;
}

export interface CompanyLeadsResult {
  leads: Lead[];
  total: number;
}

export interface SubResourceParams {
  offset?: number;
  limit?: number;
}

export interface CreateCompanyData {
  name: string;
  domain?: string;
  website?: string;
  industry?: string;
  employee_range?: CompanyEmployeeRange;
  phone?: string;
  address?: string;
  city?: string;
  state?: string;
  country?: string;
  postal_code?: string;
  notes?: string;
  owner_id?: number;
}

// PUT /companies/:id is a full update: the same body as create.
export type UpdateCompanyData = CreateCompanyData;

export const companiesApi = {
  // The array is the envelope payload; the pagination totals only exist in
  // `meta`, which the client interceptor re-attaches to the response.
  getCompanies: async (filters?: CompanyFilters): Promise<CompanyListResult> => {
    const response = (await api.get<Company[]>('/companies', {
      params: filters,
    })) as ApiResponseWithMeta<Company[]>;
    const companies = Array.isArray(response.data) ? response.data : [];
    return { companies, total: response.meta?.total ?? companies.length };
  },

  // Carries `owner`, `customer_count` and `lead_count` on top of the fields.
  getCompany: async (id: number): Promise<Company> => {
    const response = await api.get<Company>(`/companies/${id}`);
    return response.data;
  },

  // A domain already used by a live company is rejected with 409; callers
  // surface the server's message on the domain field.
  createCompany: async (data: CreateCompanyData): Promise<Company> => {
    const response = await api.post<Company>('/companies', data);
    return response.data;
  },

  updateCompany: async (id: number, data: UpdateCompanyData): Promise<Company> => {
    const response = await api.put<Company>(`/companies/${id}`, data);
    return response.data;
  },

  // Admin only. The server clears `company_id` on the linked leads and
  // customers in the same transaction, so their detail pages fall back to the
  // free-text company afterwards.
  deleteCompany: async (id: number): Promise<void> => {
    await api.delete(`/companies/${id}`);
  },

  getCompanyCustomers: async (
    id: number,
    params?: SubResourceParams
  ): Promise<CompanyCustomersResult> => {
    const response = (await api.get<unknown[]>(`/companies/${id}/customers`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    const rows = Array.isArray(response.data) ? response.data : [];
    const customers = rows.map(transformCustomerFromBackend);
    return { customers, total: response.meta?.total ?? customers.length };
  },

  // Admin and sales only (support gets 403); sales sees the leads it owns.
  getCompanyLeads: async (id: number, params?: SubResourceParams): Promise<CompanyLeadsResult> => {
    const response = (await api.get<unknown[]>(`/companies/${id}/leads`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    const rows = Array.isArray(response.data) ? response.data : [];
    const leads = rows.map(transformLeadFromBackend);
    return { leads, total: response.meta?.total ?? leads.length };
  },
};
