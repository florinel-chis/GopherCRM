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
    // Backend returns a bare APIKey[] (unwrapped from the envelope by the
    // client interceptor); pagination is synthesized client-side.
    const response = await api.get<any>('/api-keys', { params: filters });
    const apiKeys = Array.isArray(response.data) ? response.data : [];
    return {
      data: apiKeys.map(transformAPIKeyFromBackend),
      total: apiKeys.length,
      page: filters?.page || 1,
      limit: filters?.limit || 10,
      total_pages: Math.ceil(apiKeys.length / (filters?.limit || 10)),
    };
  },

  getAPIKey: async (id: number): Promise<APIKey> => {
    const response = await api.get<any>(`/api-keys/${id}`);
    return transformAPIKeyFromBackend(response.data);
  },

  createAPIKey: async (data: CreateAPIKeyData): Promise<GeneratedAPIKey> => {
    // Backend returns { key, api_key }: the plaintext key (shown exactly
    // once) alongside the stored key record.
    const response = await api.post<any>('/api-keys', data);
    const { key, api_key } = response.data;
    return { ...transformAPIKeyFromBackend(api_key), key };
  },

  updateAPIKey: async (id: number, data: UpdateAPIKeyData): Promise<APIKey> => {
    const response = await api.put<any>(`/api-keys/${id}`, data);
    return transformAPIKeyFromBackend(response.data);
  },

  // DELETE /api-keys/{id} revokes the key (marks it inactive). There is no
  // hard-delete route, so revoke and delete are the same operation.
  revokeAPIKey: async (id: number): Promise<void> => {
    await api.delete(`/api-keys/${id}`);
  },

  deleteAPIKey: async (id: number): Promise<void> => {
    await api.delete(`/api-keys/${id}`);
  },
};
