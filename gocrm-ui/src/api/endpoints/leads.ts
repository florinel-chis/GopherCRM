import { api } from '../client';
import type { BackendLead, Lead, PaginationParams, PaginatedResponse } from '@/types';

export interface LeadFilters extends PaginationParams {
  status?: string;
  classification?: string;
  source?: string;
  owner_id?: number;
  search?: string;
}

export interface CreateLeadData {
  company_name: string;
  contact_name: string;
  email: string;
  phone: string;
  status: Lead['status'];
  source: string;
  notes?: string;
  owner_id?: number;
  // Link to a Company record. On update, 0 clears the link and an absent
  // field keeps it (the scalar counterpart of the task label rule).
  company_id?: number;
}

export type UpdateLeadData = Partial<CreateLeadData>;

type LeadWriteData = Omit<CreateLeadData, 'company_name' | 'contact_name'> & {
  first_name: string;
  last_name: string;
  company: string;
};

interface LeadsResponse {
  leads: BackendLead[];
  total: number;
}

// Helper function to transform backend lead to frontend format. Exported for
// the company sub-resource endpoint, which returns the same raw rows.
export const transformLeadFromBackend = (backendLead: BackendLead): Lead => {
  return {
    ...backendLead,
    company_name: backendLead.company || '',
    contact_name: `${backendLead.first_name || ''} ${backendLead.last_name || ''}`.trim(),
  };
};

export const leadsApi = {
  getLeads: async (filters?: LeadFilters): Promise<PaginatedResponse<Lead>> => {
    const response = await api.get<LeadsResponse>('/leads', { params: filters });
    // Backend returns { leads: [...], total: number }
    const leads = response.data.leads || [];
    return {
      data: leads.map(transformLeadFromBackend),
      total: response.data.total || 0,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil((response.data.total || 0) / (filters?.limit || 10)),
    };
  },

  getLead: async (id: number): Promise<Lead> => {
    const response = await api.get<BackendLead>(`/leads/${id}`);
    return transformLeadFromBackend(response.data);
  },

  createLead: async (data: CreateLeadData): Promise<Lead> => {
    // Transform frontend data to match backend expectations
    const [firstName, ...lastNameParts] = data.contact_name.split(' ');
    const transformedData: LeadWriteData = {
      first_name: firstName || '',
      last_name: lastNameParts.join(' ') || '',
      email: data.email,
      phone: data.phone,
      company: data.company_name,
      status: data.status,
      source: data.source,
      notes: data.notes || '',
    };
    
    // Include owner_id if provided
    if (data.owner_id !== undefined) {
      transformedData.owner_id = data.owner_id;
    }
    if (data.company_id !== undefined) {
      transformedData.company_id = data.company_id;
    }
    
    const response = await api.post<BackendLead>('/leads', transformedData);
    return transformLeadFromBackend(response.data);
  },

  updateLead: async (id: number, data: UpdateLeadData): Promise<Lead> => {
    // Transform frontend data to match backend expectations
    const transformedData: Partial<LeadWriteData> = {};
    
    if (data.contact_name !== undefined) {
      const [firstName, ...lastNameParts] = data.contact_name.split(' ');
      transformedData.first_name = firstName || '';
      transformedData.last_name = lastNameParts.join(' ') || '';
    }
    
    if (data.company_name !== undefined) transformedData.company = data.company_name;
    if (data.email !== undefined) transformedData.email = data.email;
    if (data.phone !== undefined) transformedData.phone = data.phone;
    if (data.status !== undefined) transformedData.status = data.status;
    if (data.source !== undefined) transformedData.source = data.source;
    if (data.notes !== undefined) transformedData.notes = data.notes;
    if (data.company_id !== undefined) transformedData.company_id = data.company_id;
    
    const response = await api.put<BackendLead>(`/leads/${id}`, transformedData);
    return transformLeadFromBackend(response.data);
  },

  deleteLead: async (id: number): Promise<void> => {
    await api.delete(`/leads/${id}`);
  },

  convertLead: async (id: number, data?: { company_name?: string; website?: string; address?: string; notes?: string }): Promise<{ customer_id: number }> => {
    const response = await api.post<{ customer_id: number }>(`/leads/${id}/convert`, data || {});
    return response.data;
  },

  assignLead: async (id: number, userId: number): Promise<Lead> => {
    const response = await api.post<BackendLead>(`/leads/${id}/assign`, { user_id: userId });
    return transformLeadFromBackend(response.data);
  },

  bulkUpdateStatus: async (leadIds: number[], status: Lead['status']): Promise<void> => {
    await api.post('/leads/bulk/status', { lead_ids: leadIds, status });
  },
};
