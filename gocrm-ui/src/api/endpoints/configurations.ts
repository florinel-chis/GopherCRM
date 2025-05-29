import { api } from '../client';

export interface Configuration {
  id: number;
  key: string;
  value: string;
  type: 'string' | 'boolean' | 'integer' | 'float' | 'json' | 'array';
  category: 'general' | 'leads' | 'customers' | 'tickets' | 'tasks' | 'security' | 'integration' | 'ui';
  description: string;
  default_value: string;
  is_system: boolean;
  is_read_only: boolean;
  valid_values: string;
  created_at: string;
  updated_at: string;
}

export interface ConfigurationFilters {
  category?: string;
}

export interface SetConfigurationData {
  value: any;
}

export const configurationsApi = {
  // Get UI-safe configurations (available to all authenticated users)
  getUIConfigurations: async (): Promise<Configuration[]> => {
    const response = await api.get<any>('/configurations/ui');
    return response.data.configurations || [];
  },

  // Admin-only endpoints
  getAll: async (): Promise<Configuration[]> => {
    const response = await api.get<any>('/configurations');
    return response.data.configurations || [];
  },

  getByCategory: async (category: string): Promise<Configuration[]> => {
    const response = await api.get<any>(`/configurations/category/${category}`);
    return response.data.configurations || [];
  },

  getByKey: async (key: string): Promise<Configuration> => {
    const response = await api.get<Configuration>(`/configurations/${key}`);
    return response.data;
  },

  set: async (key: string, data: SetConfigurationData): Promise<Configuration> => {
    const response = await api.put<Configuration>(`/configurations/${key}`, data);
    return response.data;
  },

  reset: async (key: string): Promise<Configuration> => {
    const response = await api.post<Configuration>(`/configurations/${key}/reset`);
    return response.data;
  },

  // Helper methods to get parsed values
  getValue: (config: Configuration) => {
    switch (config.type) {
      case 'boolean':
        return config.value === 'true';
      case 'integer':
        return parseInt(config.value, 10);
      case 'float':
        return parseFloat(config.value);
      case 'json':
      case 'array':
        try {
          return JSON.parse(config.value);
        } catch {
          return null;
        }
      default:
        return config.value;
    }
  },

  // Specific configuration getters
  getLeadConversionStatuses: async (): Promise<string[]> => {
    try {
      const configs = await configurationsApi.getUIConfigurations();
      const config = configs.find(c => c.key === 'leads.conversion.allowed_statuses');
      if (config) {
        return configurationsApi.getValue(config) as string[];
      }
    } catch (error) {
      console.warn('Failed to get lead conversion statuses from config:', error);
    }
    // Fallback to default
    return ['qualified'];
  },

  getCompanyName: async (): Promise<string> => {
    try {
      const configs = await configurationsApi.getUIConfigurations();
      const config = configs.find(c => c.key === 'general.company_name');
      if (config) {
        return configurationsApi.getValue(config) as string;
      }
    } catch (error) {
      console.warn('Failed to get company name from config:', error);
    }
    // Fallback to default
    return 'GoCRM';
  },

  getPrimaryColor: async (): Promise<string> => {
    try {
      const configs = await configurationsApi.getUIConfigurations();
      const config = configs.find(c => c.key === 'ui.theme.primary_color');
      if (config) {
        return configurationsApi.getValue(config) as string;
      }
    } catch (error) {
      console.warn('Failed to get primary color from config:', error);
    }
    // Fallback to default
    return '#1976d2';
  },
};