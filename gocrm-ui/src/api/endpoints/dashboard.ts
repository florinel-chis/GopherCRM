import { api } from '../client';
import type { DashboardStats, Lead, Task, Ticket } from '@/types';

export interface Activity {
  id: string;
  type: 'lead_created' | 'lead_converted' | 'ticket_created' | 'ticket_resolved' | 'task_completed';
  title: string;
  description: string;
  user: {
    id: number;
    username: string;
    first_name: string;
    last_name: string;
  };
  created_at: string;
  metadata?: Record<string, any>;
}

export interface ChartData {
  labels: string[];
  datasets: {
    label: string;
    data: number[];
    backgroundColor?: string;
    borderColor?: string;
  }[];
}

export const dashboardApi = {
  getStats: async (): Promise<DashboardStats> => {
    const response = await api.get<DashboardStats>('/dashboard/stats');
    return response.data;
  },

  getRecentActivities: async (limit: number = 10): Promise<Activity[]> => {
    const response = await api.get<Activity[]>('/dashboard/activities', { params: { limit } });
    return response.data;
  },

  getLeadsByStatus: async (): Promise<ChartData> => {
    const response = await api.get<ChartData>('/dashboard/leads-by-status');
    return response.data;
  },

  getTicketsByPriority: async (): Promise<ChartData> => {
    const response = await api.get<ChartData>('/dashboard/tickets-by-priority');
    return response.data;
  },

  getTasksByStatus: async (): Promise<ChartData> => {
    const response = await api.get<ChartData>('/dashboard/tasks-by-status');
    return response.data;
  },

  getSalesPerformance: async (period: 'week' | 'month' | 'quarter' | 'year' = 'month'): Promise<ChartData> => {
    const response = await api.get<ChartData>('/dashboard/sales-performance', { params: { period } });
    return response.data;
  },

  getUpcomingTasks: async (limit: number = 5): Promise<Task[]> => {
    const response = await api.get<Task[]>('/dashboard/upcoming-tasks', { params: { limit } });
    return response.data;
  },

  getRecentTickets: async (limit: number = 5): Promise<Ticket[]> => {
    const response = await api.get<Ticket[]>('/dashboard/recent-tickets', { params: { limit } });
    return response.data;
  },

  getNewLeads: async (limit: number = 5): Promise<Lead[]> => {
    const response = await api.get<Lead[]>('/dashboard/new-leads', { params: { limit } });
    return response.data;
  },
};