import { api } from '../client';
import type { Lead } from '@/types';

export interface Configuration {
  id: number;
  key: string;
  value: string;
  type: 'string' | 'boolean' | 'integer' | 'float' | 'json' | 'array';
  category: 'general' | 'leads' | 'customers' | 'tickets' | 'tasks' | 'deals' | 'security' | 'integration' | 'ui';
  description: string;
  default_value: string;
  is_system: boolean;
  is_read_only: boolean;
  // Sensitive rows are never echoed back: the API replaces `value` with an
  // empty string and reports whether something is stored through `is_set`.
  is_sensitive?: boolean;
  is_set?: boolean;
  valid_values: string;
  created_at: string;
  updated_at: string;
}

// GET /configurations/ui carries three synthetic, read-only lead-conversion
// entries with the values POST /leads/:id/convert enforces: the allowed
// statuses (type 'array', a JSON list of lead statuses), whether conversion
// notes are required and whether the lead owner becomes the customer's
// assigned user (both type 'boolean', 'true' or 'false'). An entry whose
// setting could not be read is absent; the server still enforces the rule.
export type LeadConversionConfigKey =
  | 'leads.conversion.allowed_statuses'
  | 'leads.conversion.require_notes'
  | 'leads.conversion.auto_assign_owner';

// The parsed form of those three entries.
export interface LeadConversionSettings {
  allowedStatuses: Lead['status'][];
  requireNotes: boolean;
  autoAssignOwner: boolean;
}

/**
 * Why these settings refuse to convert a lead with this status, or null when
 * they allow it. The one place the UI applies the allowed-statuses rule: the
 * pages use it to offer the action and to stop an open dialog whose lead the
 * (re-read) settings no longer allow.
 */
export const leadConversionRefusal = (
  settings: LeadConversionSettings,
  status: Lead['status'],
): string | null => {
  if (settings.allowedStatuses.includes(status)) return null;
  return status === 'converted'
    ? 'This lead has already been converted.'
    : `The conversion settings do not allow converting a lead with status "${status}".`;
};

export interface ConfigurationFilters {
  category?: string;
}

export type ConfigurationValue =
  | string
  | number
  | boolean
  | null
  | ConfigurationValue[]
  | { [key: string]: ConfigurationValue };

export interface SetConfigurationData {
  value: Exclude<ConfigurationValue, null>;
}

interface ConfigurationsResponse {
  configurations: Configuration[];
}

export const configurationsApi = {
  // Get UI-safe configurations (available to all authenticated users)
  getUIConfigurations: async (): Promise<Configuration[]> => {
    const response = await api.get<ConfigurationsResponse>('/configurations/ui');
    return response.data.configurations || [];
  },

  // Admin-only endpoints
  getAll: async (): Promise<Configuration[]> => {
    const response = await api.get<ConfigurationsResponse>('/configurations');
    return response.data.configurations || [];
  },

  getByCategory: async (category: string): Promise<Configuration[]> => {
    const response = await api.get<ConfigurationsResponse>(`/configurations/category/${category}`);
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
  getValue: (config: Configuration): unknown => {
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
};
