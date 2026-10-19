import { api } from '../client';
import type { BackendCustomer, Customer, PaginationParams, PaginatedResponse } from '@/types';

export interface CustomerFilters extends PaginationParams {
  owner_id?: number;
  city?: string;
  state?: string;
  country?: string;
  search?: string;
}

export interface CreateCustomerData {
  website?: string;
  company_name: string;
  contact_name: string;
  email: string;
  phone: string;
  address: string;
  city: string;
  state: string;
  country: string;
  postal_code: string;
  notes?: string;
  // Link to a Company record. On update, 0 clears the link and an absent
  // field keeps it (the scalar counterpart of the task label rule).
  company_id?: number;
}

export type UpdateCustomerData = Partial<CreateCustomerData>;

type CustomerWriteData = Omit<CreateCustomerData, 'company_name' | 'contact_name'> & {
  first_name: string;
  last_name: string;
  company: string;
};

interface CustomersResponse {
  customers: BackendCustomer[];
  total: number;
}

// Helper function to transform backend customer to frontend format. Exported
// for the company sub-resource endpoint, which returns the same raw rows.
export const transformCustomerFromBackend = (backendCustomer: BackendCustomer): Customer => {
  return {
    ...backendCustomer,
    company_name: backendCustomer.company || '',
    contact_name: `${backendCustomer.first_name || ''} ${backendCustomer.last_name || ''}`.trim(),
    // Provide defaults for optional and UI-only fields.
    total_revenue: 0,
    is_active: true,
    website: backendCustomer.website ?? '',
    industry: '',
    annual_revenue: 0,
    employee_count: 0,
  };
};

export const customersApi = {
  getCustomers: async (filters?: CustomerFilters): Promise<PaginatedResponse<Customer>> => {
    const response = await api.get<CustomersResponse>('/customers', { params: filters });
    // Backend returns { customers: [...], total: number }
    const customers = response.data.customers || [];
    return {
      data: customers.map(transformCustomerFromBackend),
      total: response.data.total || 0,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil((response.data.total || 0) / (filters?.limit || 10)),
    };
  },

  getCustomer: async (id: number): Promise<Customer> => {
    const response = await api.get<BackendCustomer>(`/customers/${id}`);
    return transformCustomerFromBackend(response.data);
  },

  createCustomer: async (data: CreateCustomerData): Promise<Customer> => {
    // Transform frontend data to backend format
    const [firstName, ...lastNameParts] = data.contact_name.split(' ');
    const transformedData = {
      first_name: firstName || '',
      last_name: lastNameParts.join(' ') || '',
      email: data.email,
      phone: data.phone,
      company: data.company_name,
      website: data.website,
      address: data.address,
      city: data.city,
      state: data.state,
      country: data.country,
      postal_code: data.postal_code,
      notes: data.notes || '',
      ...(data.company_id !== undefined && { company_id: data.company_id }),
    };
    const response = await api.post<BackendCustomer>('/customers', transformedData);
    return transformCustomerFromBackend(response.data);
  },

  updateCustomer: async (id: number, data: UpdateCustomerData): Promise<Customer> => {
    // Transform frontend data to backend format
    const transformedData: Partial<CustomerWriteData> = {};

    if (data.contact_name !== undefined) {
      const [firstName, ...lastNameParts] = data.contact_name.split(' ');
      transformedData.first_name = firstName || '';
      transformedData.last_name = lastNameParts.join(' ') || '';
    }

    if (data.company_name !== undefined) transformedData.company = data.company_name;
    if (data.website !== undefined) transformedData.website = data.website;
    if (data.email !== undefined) transformedData.email = data.email;
    if (data.phone !== undefined) transformedData.phone = data.phone;
    if (data.address !== undefined) transformedData.address = data.address;
    if (data.city !== undefined) transformedData.city = data.city;
    if (data.state !== undefined) transformedData.state = data.state;
    if (data.country !== undefined) transformedData.country = data.country;
    if (data.postal_code !== undefined) transformedData.postal_code = data.postal_code;
    if (data.notes !== undefined) transformedData.notes = data.notes;
    if (data.company_id !== undefined) transformedData.company_id = data.company_id;

    const response = await api.put<BackendCustomer>(`/customers/${id}`, transformedData);
    return transformCustomerFromBackend(response.data);
  },

  deleteCustomer: async (id: number): Promise<void> => {
    await api.delete(`/customers/${id}`);
  },

  assignCustomer: async (id: number, userId: number): Promise<Customer> => {
    const response = await api.post<BackendCustomer>(`/customers/${id}/assign`, {
      user_id: userId,
    });
    return transformCustomerFromBackend(response.data);
  },

  exportCustomers: async (filters?: CustomerFilters): Promise<Blob> => {
    // The export endpoint only supports search, sort_by and sort_order;
    // other list filters are ignored server-side, so don't send them.
    const response = await api.get('/customers/export', {
      params: {
        search: filters?.search,
        sort_by: filters?.sort_by,
        sort_order: filters?.sort_order,
      },
      responseType: 'blob',
    });
    return response.data;
  },
};
