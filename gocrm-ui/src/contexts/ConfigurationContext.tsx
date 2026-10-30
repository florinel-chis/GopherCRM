import React, { useMemo } from 'react';
import { useQuery, useQueryClient } from '@tanstack/react-query';
import {
  configurationsApi,
  type Configuration,
  type LeadConversionSettings,
} from '@/api/endpoints/configurations';
import {
  ConfigurationContext,
  UI_CONFIGURATIONS_KEY,
  type ConfigurationContextType,
} from './configuration-context';
import { useAuth } from '@/hooks/useAuth';
import { getErrorMessage } from '@/utils/apiError';

interface ConfigurationProviderProps {
  children: React.ReactNode;
}

// The provider's copy is keyed by the signed-in user: a sign-in, sign-out or
// account switch moves to another cache entry, so one account's values can
// never be served to the next.
const uiConfigurationsKey = (userId: number) => [...UI_CONFIGURATIONS_KEY, { userId }] as const;

// The defaults the UI falls back to while nothing is loaded, without a
// session, after a failed load, or when an entry is absent or malformed. The
// conversion defaults are the settings' own seeded defaults for the statuses
// and the notes; owner assignment is only mentioned when the server says it
// is on. The server enforces the real rules either way.
const DEFAULT_COMPANY_NAME = 'GopherCRM';
const DEFAULT_PRIMARY_COLOR = '#1976d2';
const DEFAULT_CONVERSION_STATUSES: LeadConversionSettings['allowedStatuses'] = ['qualified'];

const NO_CONFIGURATIONS: Configuration[] = [];

const isStringArray = (value: unknown): value is string[] =>
  Array.isArray(value) && value.every((item) => typeof item === 'string');

export const ConfigurationProvider: React.FC<ConfigurationProviderProps> = ({ children }) => {
  const { user, isLoading: authLoading } = useAuth();
  const queryClient = useQueryClient();
  const userId = user?.id;

  // Only an established session asks: on the login and registration pages
  // there is no user, so no unauthenticated request (and no refresh attempt
  // or redirect it could trigger) is ever made.
  const query = useQuery({
    queryKey: uiConfigurationsKey(userId ?? 0),
    queryFn: () => configurationsApi.getUIConfigurations(),
    enabled: userId !== undefined,
  });

  const configurations = userId !== undefined && query.data ? query.data : NO_CONFIGURATIONS;
  const isLoading = authLoading || (userId !== undefined && query.isPending);
  const error =
    userId !== undefined && query.isError
      ? getErrorMessage(query.error, 'Failed to load configurations')
      : null;

  const value = useMemo<ConfigurationContextType>(() => {
    const find = (key: string) => configurations.find((config) => config.key === key);

    const getString = (key: string, fallback: string): string => {
      const config = find(key);
      const parsed = config ? configurationsApi.getValue(config) : undefined;
      return typeof parsed === 'string' && parsed.trim() !== '' ? parsed : fallback;
    };

    const getBoolean = (key: string, fallback: boolean): boolean => {
      const config = find(key);
      if (!config || config.type !== 'boolean') return fallback;
      return configurationsApi.getValue(config) === true;
    };

    const getLeadConversionStatuses = (): LeadConversionSettings['allowedStatuses'] => {
      const config = find('leads.conversion.allowed_statuses');
      const parsed = config ? configurationsApi.getValue(config) : undefined;
      // An empty list is a deliberate choice (nothing is convertible) and is
      // kept; only an absent or malformed entry falls back.
      return isStringArray(parsed)
        ? (parsed as LeadConversionSettings['allowedStatuses'])
        : DEFAULT_CONVERSION_STATUSES;
    };

    return {
      configurations,
      isLoading,
      error,
      getLeadConversionStatuses,
      getLeadConversionSettings: () => ({
        allowedStatuses: getLeadConversionStatuses(),
        requireNotes: getBoolean('leads.conversion.require_notes', false),
        autoAssignOwner: getBoolean('leads.conversion.auto_assign_owner', false),
      }),
      getCompanyName: () => getString('general.company_name', DEFAULT_COMPANY_NAME),
      getPrimaryColor: () => getString('ui.theme.primary_color', DEFAULT_PRIMARY_COLOR),
      refreshConfigurations: async () => {
        await queryClient.invalidateQueries({ queryKey: UI_CONFIGURATIONS_KEY });
      },
    };
  }, [configurations, isLoading, error, queryClient]);

  return <ConfigurationContext.Provider value={value}>{children}</ConfigurationContext.Provider>;
};
