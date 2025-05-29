import React, { createContext, useContext, useEffect, useState } from 'react';
import { configurationsApi, type Configuration } from '@/api/endpoints/configurations';

interface ConfigurationContextType {
  configurations: Configuration[];
  isLoading: boolean;
  error: string | null;
  getLeadConversionStatuses: () => string[];
  getCompanyName: () => string;
  getPrimaryColor: () => string;
  refreshConfigurations: () => Promise<void>;
}

const ConfigurationContext = createContext<ConfigurationContextType | undefined>(undefined);

export const useConfiguration = () => {
  const context = useContext(ConfigurationContext);
  if (context === undefined) {
    throw new Error('useConfiguration must be used within a ConfigurationProvider');
  }
  return context;
};

interface ConfigurationProviderProps {
  children: React.ReactNode;
}

export const ConfigurationProvider: React.FC<ConfigurationProviderProps> = ({ children }) => {
  const [configurations, setConfigurations] = useState<Configuration[]>([]);
  const [isLoading, setIsLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);

  const loadConfigurations = async () => {
    try {
      setIsLoading(true);
      setError(null);
      const configs = await configurationsApi.getUIConfigurations();
      setConfigurations(configs);
    } catch (err: any) {
      console.error('Failed to load configurations:', err);
      setError(err.message || 'Failed to load configurations');
      // Set empty array as fallback
      setConfigurations([]);
    } finally {
      setIsLoading(false);
    }
  };

  useEffect(() => {
    // Temporarily disabled to isolate loading issues
    // loadConfigurations();
    setIsLoading(false);
  }, []);

  const getConfigValue = <T,>(key: string, defaultValue: T): T => {
    const config = configurations.find(c => c.key === key);
    if (!config) return defaultValue;

    try {
      return configurationsApi.getValue(config) as T;
    } catch {
      return defaultValue;
    }
  };

  const getLeadConversionStatuses = (): string[] => {
    return getConfigValue('leads.conversion.allowed_statuses', ['qualified']);
  };

  const getCompanyName = (): string => {
    return getConfigValue('general.company_name', 'GopherCRM');
  };

  const getPrimaryColor = (): string => {
    return getConfigValue('ui.theme.primary_color', '#1976d2');
  };

  const refreshConfigurations = async () => {
    await loadConfigurations();
  };

  const value: ConfigurationContextType = {
    configurations,
    isLoading,
    error,
    getLeadConversionStatuses,
    getCompanyName,
    getPrimaryColor,
    refreshConfigurations,
  };

  return (
    <ConfigurationContext.Provider value={value}>
      {children}
    </ConfigurationContext.Provider>
  );
};