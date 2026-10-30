import { createContext } from 'react';
import type { Configuration, LeadConversionSettings } from '@/api/endpoints/configurations';

export interface ConfigurationContextType {
  /** The UI-safe configuration of the signed-in user; empty without a session. */
  configurations: Configuration[];
  /** True while the session is being restored or its configuration loads. */
  isLoading: boolean;
  /** Why the last load failed; the getters then serve their defaults. */
  error: string | null;
  getLeadConversionStatuses: () => string[];
  getLeadConversionSettings: () => LeadConversionSettings;
  getCompanyName: () => string;
  getPrimaryColor: () => string;
  /** Re-reads the configuration (for instance after an admin saved a setting). */
  refreshConfigurations: () => Promise<void>;
}

/**
 * Every cached copy of GET /configurations/ui starts with this key, so one
 * invalidation refreshes the provider's copy and any page-level query of the
 * same list.
 */
export const UI_CONFIGURATIONS_KEY = ['configurations', 'ui'] as const;

export const ConfigurationContext = createContext<ConfigurationContextType | undefined>(undefined);
