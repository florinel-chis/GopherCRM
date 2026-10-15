import { createContext } from 'react';
import type { Configuration } from '@/api/endpoints/configurations';

export interface ConfigurationContextType {
  configurations: Configuration[];
  isLoading: boolean;
  error: string | null;
  getLeadConversionStatuses: () => string[];
  getCompanyName: () => string;
  getPrimaryColor: () => string;
  refreshConfigurations: () => Promise<void>;
}

export const ConfigurationContext = createContext<ConfigurationContextType | undefined>(undefined);
