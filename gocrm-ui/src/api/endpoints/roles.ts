import { api } from '../client';
import type { Permission } from '@/types';

// A role as the role API returns it: one of the four system roles or a custom
// role, with its permissions and the number of live users on it.
export interface Role {
  id: number;
  name: string;
  description: string;
  // admin, sales, support or customer on a system role; null on a custom role.
  system_key: string | null;
  is_system: boolean;
  user_count: number;
  permissions: Permission[];
}

// One node of GET /permissions/catalogue. The API marks both a group and a
// permission only a system role holds as not selectable; a group is the node
// that has children.
export interface CatalogueNode {
  key: string;
  title: string;
  // ['all', 'own'] for a permission that can be limited to own records, empty
  // for one that is always all and for a group.
  scopes: string[];
  selectable: boolean;
  children: CatalogueNode[];
}

export interface RoleData {
  name: string;
  // Always sent: PUT /roles/{id} keeps the stored value of an omitted
  // description or permissions field.
  description: string;
  permissions: Permission[];
}

export interface CloneRoleData {
  name: string;
}

// The largest page GET /roles serves.
const ROLE_PAGE_LIMIT = 100;

export const rolesApi = {
  getRoles: async (): Promise<Role[]> => {
    const response = await api.get<Role[]>('/roles', { params: { limit: ROLE_PAGE_LIMIT } });
    return Array.isArray(response.data) ? response.data : [];
  },

  getRole: async (id: number): Promise<Role> => {
    const response = await api.get<Role>(`/roles/${id}`);
    return response.data;
  },

  createRole: async (data: RoleData): Promise<Role> => {
    const response = await api.post<Role>('/roles', data);
    return response.data;
  },

  updateRole: async (id: number, data: RoleData): Promise<Role> => {
    const response = await api.put<Role>(`/roles/${id}`, data);
    return response.data;
  },

  deleteRole: async (id: number): Promise<void> => {
    await api.delete(`/roles/${id}`);
  },

  cloneRole: async (id: number, data: CloneRoleData): Promise<Role> => {
    const response = await api.post<Role>(`/roles/${id}/clone`, data);
    return response.data;
  },

  getCatalogue: async (): Promise<CatalogueNode[]> => {
    const response = await api.get<CatalogueNode[]>('/permissions/catalogue');
    return Array.isArray(response.data) ? response.data : [];
  },
};
