import { api } from '../client';
import type { APIKey, PaginationParams, PaginatedResponse } from '@/types';

export interface APIKeyFilters extends PaginationParams {
  user_id?: number;
  is_active?: boolean;
}

export interface CreateAPIKeyData {
  name: string;
  expires_at?: string;
}

export interface UpdateAPIKeyData {
  name?: string;
  is_active?: boolean;
}

export interface GeneratedAPIKey extends APIKey {
  key: string; // Plain text key, only returned on creation
}

// Helper function to transform backend API key to frontend format
const transformAPIKeyFromBackend = (backendKey: any): APIKey => {
  // Remove sensitive fields that shouldn't be exposed
  const { key_hash, ...safeKey } = backendKey;
  return safeKey;
};

export const apiKeysApi = {
  getAPIKeys: async (filters?: APIKeyFilters): Promise<PaginatedResponse<APIKey>> => {
    const response = await api.get<any>('/apikeys', { params: filters });
    // Backend returns { api_keys: [...] } and uses direct JSON response
    const apiKeys = response.data.api_keys || [];
    return {
      data: apiKeys.map(transformAPIKeyFromBackend),
      total: apiKeys.length, // Backend doesn't provide total in handler
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil(apiKeys.length / (filters?.limit || 10)),
    };
  },

  getAPIKey: async (id: number): Promise<APIKey> => {
    const response = await api.get<any>(`/apikeys/${id}`);
    return transformAPIKeyFromBackend(response.data);
  },

  createAPIKey: async (data: CreateAPIKeyData): Promise<GeneratedAPIKey> => {
    const response = await api.post<any>('/apikeys', data);
    // For creation, we keep the plain text key that's returned
    return response.data;
  },

  updateAPIKey: async (id: number, data: UpdateAPIKeyData): Promise<APIKey> => {
    const response = await api.put<any>(`/apikeys/${id}`, data);
    return transformAPIKeyFromBackend(response.data);
  },

  revokeAPIKey: async (id: number): Promise<void> => {
    await api.post(`/apikeys/${id}/revoke`);
  },

  deleteAPIKey: async (id: number): Promise<void> => {
    await api.delete(`/apikeys/${id}`);
  },
};