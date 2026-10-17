import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type { User, PaginationParams, PaginatedResponse } from '@/types';

export interface UserFilters extends PaginationParams {
  role?: string;
  is_active?: boolean;
  search?: string;
}

export interface CreateUserData {
  email: string;
  password: string;
  first_name: string;
  last_name: string;
  role: 'admin' | 'sales' | 'support' | 'customer';
}

export interface UpdateUserData {
  email?: string;
  first_name?: string;
  last_name?: string;
  role?: 'admin' | 'sales' | 'support' | 'customer';
  is_active?: boolean;
}

// Helper function to transform backend user to frontend format
const transformUserFromBackend = (backendUser: User): User => {
  return {
    ...backendUser,
    // Ensure last_login_at is included if available
    last_login_at: backendUser.last_login_at,
  };
};

export const usersApi = {
  getUsers: async (filters?: UserFilters): Promise<PaginatedResponse<User>> => {
    const response = (await api.get<User[]>('/users', { params: filters })) as ApiResponseWithMeta<
      User[]
    >;
    const users = Array.isArray(response.data) ? response.data : [];
    const total = response.meta?.total ?? users.length;

    return {
      data: users.map(transformUserFromBackend),
      total: total,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil(total / (filters?.limit || 10)),
    };
  },

  getUser: async (id: number): Promise<User> => {
    const response = await api.get<User>(`/users/${id}`);
    return transformUserFromBackend(response.data);
  },

  createUser: async (data: CreateUserData): Promise<User> => {
    const response = await api.post<User>('/users', data);
    return transformUserFromBackend(response.data);
  },

  updateUser: async (id: number, data: UpdateUserData): Promise<User> => {
    const response = await api.put<User>(`/users/${id}`, data);
    return transformUserFromBackend(response.data);
  },

  deleteUser: async (id: number): Promise<void> => {
    await api.delete(`/users/${id}`);
  },

  activateUser: async (id: number): Promise<User> => {
    const response = await api.post<User>(`/users/${id}/activate`);
    return transformUserFromBackend(response.data);
  },

  deactivateUser: async (id: number): Promise<User> => {
    const response = await api.post<User>(`/users/${id}/deactivate`);
    return transformUserFromBackend(response.data);
  },
};
