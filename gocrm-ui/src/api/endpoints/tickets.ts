import { api } from '../client';
import type { Ticket, PaginationParams, PaginatedResponse } from '@/types';

export interface TicketFilters extends PaginationParams {
  status?: string;
  priority?: string;
  customer_id?: number;
  assigned_to?: number;
  created_by?: number;
  search?: string;
}

export interface CreateTicketData {
  subject: string;
  description: string;
  status: 'open' | 'in_progress' | 'resolved' | 'closed';
  priority: 'low' | 'medium' | 'high' | 'urgent';
  customer_id: number;
  assigned_to_id?: number;
}

export interface UpdateTicketData {
  subject?: string;
  description?: string;
  status?: 'open' | 'in_progress' | 'resolved' | 'closed';
  priority?: 'low' | 'medium' | 'high' | 'urgent';
  assigned_to_id?: number;
}

export interface TicketComment {
  id: number;
  ticket_id: number;
  user_id: number;
  comment: string;
  created_at: string;
  user?: {
    id: number;
    username: string;
    first_name: string;
    last_name: string;
  };
}

// Helper function to transform backend ticket to frontend format
const transformTicketFromBackend = (backendTicket: any): Ticket => {
  return {
    ...backendTicket,
    subject: backendTicket.title || '', // Backend uses 'title', frontend expects 'subject'
    // Add fields that frontend expects but backend doesn't provide
    created_by_id: backendTicket.customer_id, // Assuming customer created the ticket
    created_by: backendTicket.customer,
    comments: [], // Backend doesn't have comments yet
    closed_at: backendTicket.status === 'closed' ? backendTicket.updated_at : undefined,
  };
};

export const ticketsApi = {
  getTickets: async (filters?: TicketFilters): Promise<PaginatedResponse<Ticket>> => {
    const response = await api.get<any>('/tickets', { params: filters });
    // Backend returns { tickets: [...], total: number }
    const tickets = response.data.tickets || [];
    return {
      data: tickets.map(transformTicketFromBackend),
      total: response.data.total || 0,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil((response.data.total || 0) / (filters?.limit || 10)),
    };
  },

  getTicket: async (id: number): Promise<Ticket> => {
    const response = await api.get<any>(`/tickets/${id}`);
    return transformTicketFromBackend(response.data);
  },

  createTicket: async (data: CreateTicketData): Promise<Ticket> => {
    // Transform frontend data to backend format
    const transformedData = {
      title: data.subject, // Frontend uses 'subject', backend expects 'title'
      description: data.description,
      status: data.status,
      priority: data.priority,
      customer_id: data.customer_id,
      assigned_to_id: data.assigned_to_id,
    };
    const response = await api.post<any>('/tickets', transformedData);
    return transformTicketFromBackend(response.data);
  },

  updateTicket: async (id: number, data: UpdateTicketData): Promise<Ticket> => {
    // Transform frontend data to backend format
    const transformedData: any = {};
    if (data.subject !== undefined) transformedData.title = data.subject;
    if (data.description !== undefined) transformedData.description = data.description;
    if (data.status !== undefined) transformedData.status = data.status;
    if (data.priority !== undefined) transformedData.priority = data.priority;
    if (data.assigned_to_id !== undefined) transformedData.assigned_to_id = data.assigned_to_id;
    
    const response = await api.put<any>(`/tickets/${id}`, transformedData);
    return transformTicketFromBackend(response.data);
  },

  deleteTicket: async (id: number): Promise<void> => {
    await api.delete(`/tickets/${id}`);
  },

  assignTicket: async (id: number, userId: number): Promise<Ticket> => {
    const response = await api.post<any>(`/tickets/${id}/assign`, { user_id: userId });
    return transformTicketFromBackend(response.data);
  },

  getTicketComments: async (id: number): Promise<TicketComment[]> => {
    const response = await api.get<TicketComment[]>(`/tickets/${id}/comments`);
    return response.data;
  },

  addTicketComment: async (id: number, comment: string): Promise<TicketComment> => {
    const response = await api.post<TicketComment>(`/tickets/${id}/comments`, { comment });
    return response.data;
  },

  addComment: async (id: number, data: { content: string }): Promise<TicketComment> => {
    const response = await api.post<TicketComment>(`/tickets/${id}/comments`, data);
    return response.data;
  },

  bulkUpdateStatus: async (ticketIds: number[], status: Ticket['status']): Promise<void> => {
    await api.post('/tickets/bulk/status', { ticket_ids: ticketIds, status });
  },
};