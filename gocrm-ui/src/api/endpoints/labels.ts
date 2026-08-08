import { api } from '../client';
import type { Label } from '@/types';

export interface CreateLabelData {
  name: string;
  color: string;
}

export interface UpdateLabelData {
  name?: string;
  color?: string;
}

export const labelsApi = {
  // GET /labels returns a bare Label[] (the client interceptor peels the
  // { success, data } envelope), ordered by name, each carrying task_count.
  getLabels: async (): Promise<Label[]> => {
    const response = await api.get<Label[]>('/labels');
    return Array.isArray(response.data) ? response.data : [];
  },

  // Duplicate names are rejected with 409 by the API; callers surface that.
  createLabel: async (data: CreateLabelData): Promise<Label> => {
    const response = await api.post<Label>('/labels', data);
    return response.data;
  },

  updateLabel: async (id: number, data: UpdateLabelData): Promise<Label> => {
    const response = await api.put<Label>(`/labels/${id}`, data);
    return response.data;
  },

  // Labels are hard-deleted and detached from every task in the same
  // transaction, so a delete always changes the tasks that carried it.
  deleteLabel: async (id: number): Promise<void> => {
    await api.delete(`/labels/${id}`);
  },
};
