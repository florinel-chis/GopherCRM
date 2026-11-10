import { api } from '../client';
import type { BackendTask, Task, PaginationParams, PaginatedResponse } from '@/types';

export interface TaskFilters extends PaginationParams {
  status?: string;
  priority?: string;
  assigned_to?: number;
  created_by?: number;
  due_date_from?: string;
  due_date_to?: string;
  search?: string;
  // Restricts the list to tasks carrying this label.
  label_id?: number;
}

export interface CreateTaskData {
  title: string;
  description: string;
  priority: 'low' | 'medium' | 'high';
  due_date: string;
  assigned_to?: number;
  label_ids?: number[];
  // The deal to link (#240). Sent only when a deal was picked; the deal must be
  // live and visible to the caller (closed deals are allowed).
  deal_id?: number;
}

export interface UpdateTaskData {
  title?: string;
  description?: string;
  status?: 'pending' | 'in_progress' | 'completed' | 'cancelled';
  priority?: 'low' | 'medium' | 'high';
  due_date?: string;
  assigned_to?: number;
  // Replaces the whole label set; omit the field to leave labels untouched.
  label_ids?: number[];
  // Omit to keep the stored link, an id to replace it, 0 to clear it. The API
  // answers 200 and keeps a link the caller cannot see when asked to clear it,
  // so callers re-read the task instead of trusting the response for the link.
  deal_id?: number;
}

export interface DealTasksParams {
  offset?: number;
  limit?: number;
}

export interface DealTasksResult {
  tasks: Task[];
  total: number;
}

interface TasksResponse {
  tasks: BackendTask[];
  total: number;
}

type UpdateTaskRequest = UpdateTaskData & { assigned_to_id?: number };

// Helper function to transform backend task to frontend format
const transformTaskFromBackend = (backendTask: BackendTask): Task => {
  return {
    ...backendTask,
    // Frontend uses 'assigned_to' as number, backend uses 'assigned_to_id'
    assigned_to: backendTask.assigned_to_id,
    // Add fields that frontend expects
    created_by: backendTask.assigned_to_id, // Default to assignee since backend doesn't track creator
    // Labels come back as a nested array; normalize the absent case to [] so
    // consumers can render without a null check.
    labels: Array.isArray(backendTask.labels) ? backendTask.labels : [],
    completed_at: backendTask.status === 'completed' ? backendTask.updated_at : undefined,
  };
};

export const tasksApi = {
  getTasks: async (filters?: TaskFilters): Promise<PaginatedResponse<Task>> => {
    const response = await api.get<TasksResponse>('/tasks', { params: filters });
    // Backend returns { tasks: [...], total: number }
    const tasks = response.data.tasks || [];
    return {
      data: tasks.map(transformTaskFromBackend),
      total: response.data.total || 0,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil((response.data.total || 0) / (filters?.limit || 10)),
    };
  },

  getTask: async (id: number): Promise<Task> => {
    const response = await api.get<BackendTask>(`/tasks/${id}`);
    return transformTaskFromBackend(response.data);
  },

  createTask: async (data: CreateTaskData): Promise<Task> => {
    // Transform frontend data to backend format
    const transformedData = {
      ...data,
      assigned_to_id: data.assigned_to,
    };
    delete transformedData.assigned_to;
    // deal_id passes through unchanged when given; an undefined one is not sent.
    if (transformedData.deal_id === undefined) {
      delete transformedData.deal_id;
    }

    const response = await api.post<BackendTask>('/tasks', transformedData);
    return transformTaskFromBackend(response.data);
  },

  updateTask: async (id: number, data: UpdateTaskData): Promise<Task> => {
    // Transform frontend data to backend format
    const transformedData: UpdateTaskRequest = { ...data };
    if (data.assigned_to !== undefined) {
      transformedData.assigned_to_id = data.assigned_to;
      delete transformedData.assigned_to;
    }
    // An absent deal_id keeps the stored link, so an undefined one must not
    // reach the body as a key; 0 (clear) and an id pass through unchanged.
    if (transformedData.deal_id === undefined) {
      delete transformedData.deal_id;
    }

    const response = await api.put<BackendTask>(`/tasks/${id}`, transformedData);
    return transformTaskFromBackend(response.data);
  },

  deleteTask: async (id: number): Promise<void> => {
    await api.delete(`/tasks/${id}`);
  },

  assignTask: async (id: number, userId: number): Promise<Task> => {
    const response = await api.post<BackendTask>(`/tasks/${id}/assign`, { user_id: userId });
    return transformTaskFromBackend(response.data);
  },

  completeTask: async (id: number): Promise<Task> => {
    const response = await api.post<BackendTask>(`/tasks/${id}/complete`);
    return transformTaskFromBackend(response.data);
  },

  bulkUpdateStatus: async (taskIds: number[], status: Task['status']): Promise<void> => {
    await api.post('/tasks/bulk/status', { task_ids: taskIds, status });
  },

  getUpcomingTasks: async (days: number = 7): Promise<Task[]> => {
    const response = await api.get<BackendTask[] | Pick<TasksResponse, 'tasks'>>('/tasks/upcoming', {
      params: { days },
    });
    // This endpoint might return an array directly or wrapped
    const tasks = Array.isArray(response.data) ? response.data : (response.data.tasks || []);
    return tasks.map(transformTaskFromBackend);
  },

  // GET /deals/:id/tasks (admin any deal; sales only a deal it owns, and only
  // the tasks assigned to it, which `total` counts as well).
  getDealTasks: async (dealId: number, params?: DealTasksParams): Promise<DealTasksResult> => {
    const response = await api.get<TasksResponse>(`/deals/${dealId}/tasks`, { params });
    const tasks = Array.isArray(response.data?.tasks) ? response.data.tasks : [];
    return {
      tasks: tasks.map(transformTaskFromBackend),
      total: response.data?.total ?? tasks.length,
    };
  },
};
