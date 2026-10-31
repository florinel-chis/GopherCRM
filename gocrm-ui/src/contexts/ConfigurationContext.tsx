import React, { useMemo } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import {
  configurationsApi,
  type Configuration,
  type LeadConversionSettings,
} from '@/api/endpoints/configurations';
import {
  ConfigurationContext,
  UI_CONFIGURATIONS_KEY,
  type ConfigurationContextType,
  type LeadConversionState,
} from './configuration-context';
import { useAuth } from '@/hooks/useAuth';
import { useUIConfigurationsQuery } from '@/hooks/useUIConfigurations';
import { getErrorMessage } from '@/utils/apiError';

interface ConfigurationProviderProps {
  children: React.ReactNode;
}

// Display values fall back to built-in defaults while nothing is loaded,
// without a session, after a failed load, or when an entry is absent or
// malformed. Policy (the lead-conversion rules) never does: see
// LeadConversionState.
const DEFAULT_COMPANY_NAME = 'GopherCRM';
const DEFAULT_PRIMARY_COLOR = '#1976d2';

const NO_CONFIGURATIONS: Configuration[] = [];

const isStringArray = (value: unknown): value is string[] =>
  Array.isArray(value) && value.every((item) => typeof item === 'string');

export const ConfigurationProvider: React.FC<ConfigurationProviderProps> = ({ children }) => {
  const { user, isLoading: authLoading } = useAuth();
  const queryClient = useQueryClient();
  const signedIn = user !== null && user !== undefined;
  const query = useUIConfigurationsQuery();

  const configurations = signedIn && query.data ? query.data : NO_CONFIGURATIONS;
  const isLoading = authLoading || (signedIn && query.isPending);
  const failed = signedIn && query.isError;
  const error = failed ? getErrorMessage(query.error, 'Failed to load configurations') : null;

  const value = useMemo<ConfigurationContextType>(() => {
    const find = (key: string) => configurations.find((config) => config.key === key);

    const getString = (key: string, fallback: string): string => {
      const config = find(key);
      const parsed = config ? configurationsApi.getValue(config) : undefined;
      return typeof parsed === 'string' && parsed.trim() !== '' ? parsed : fallback;
    };

    // A boolean entry, or undefined when it is absent or not a boolean.
    const readBoolean = (key: string): boolean | undefined => {
      const config = find(key);
      if (!config || config.type !== 'boolean' || (config.value !== 'true' && config.value !== 'false')) {
        return undefined;
      }
      return config.value === 'true';
    };

    const getLeadConversion = (): LeadConversionState => {
      if (isLoading) return { status: 'loading' };
      // A failed load (also a failed refresh over older values) is not
      // trusted: the rules may have changed since.
      if (!signedIn || failed) return { status: 'unavailable' };

      const statusesEntry = find('leads.conversion.allowed_statuses');
      const statuses = statusesEntry ? configurationsApi.getValue(statusesEntry) : undefined;
      const requireNotes = readBoolean('leads.conversion.require_notes');
      if (!isStringArray(statuses) || requireNotes === undefined) {
        return { status: 'unavailable' };
      }
      return {
        status: 'ready',
        settings: {
          // An empty list is a deliberate choice: nothing is convertible.
          allowedStatuses: statuses as LeadConversionSettings['allowedStatuses'],
          requireNotes,
          // Only drives a hint in the conversion dialog; unknown says nothing.
          autoAssignOwner: readBoolean('leads.conversion.auto_assign_owner') ?? false,
        },
      };
    };

    return {
      configurations,
      isLoading,
      error,
      getLeadConversion,
      getCompanyName: () => getString('general.company_name', DEFAULT_COMPANY_NAME),
      getPrimaryColor: () => getString('ui.theme.primary_color', DEFAULT_PRIMARY_COLOR),
      refreshConfigurations: async () => {
        await queryClient.invalidateQueries({ queryKey: UI_CONFIGURATIONS_KEY });
      },
    };
  }, [configurations, isLoading, error, failed, signedIn, queryClient]);

  return <ConfigurationContext.Provider value={value}>{children}</ConfigurationContext.Provider>;
};
