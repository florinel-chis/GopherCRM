import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import React from 'react';
import { renderHook, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { useProductName } from './useProductName';
import { ConfigurationProvider } from '@/contexts/ConfigurationContext';
import {
  ConfigurationContext,
  type ConfigurationContextType,
} from '@/contexts/configuration-context';
import { configurationsApi } from '@/api/endpoints';
import type { Configuration } from '@/api/endpoints/configurations';
import { createMockUser } from '@/test/factories';
import type { User } from '@/types';

const mockUseAuth = vi.fn();

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

vi.mock('@/api/endpoints', () => ({
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const authState = (user: User | null) => ({
  user,
  isLoading: false,
  isAuthenticated: !!user,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

const entry = (
  key: string,
  value: string,
  type: Configuration['type'] = 'string',
): Configuration => ({
  id: 0,
  key,
  value,
  type,
  category: 'general',
  description: '',
  default_value: '',
  is_system: true,
  is_read_only: false,
  valid_values: '',
  created_at: '2026-11-10T00:00:00Z',
  updated_at: '2026-11-10T00:00:00Z',
});

// A context value as the provider would hand it out. getCompanyName answers
// with a different name on purpose: the hook must never use it.
const contextValue = (configurations: Configuration[]): ConfigurationContextType => ({
  configurations,
  isLoading: false,
  error: null,
  getLeadConversion: () => ({ status: 'unavailable' }),
  getCompanyName: () => 'NOT FROM getCompanyName',
  getPrimaryColor: () => '#1976d2',
  refreshConfigurations: vi.fn(async () => {}),
});

const withConfigurations = (configurations: Configuration[]) => {
  const value = contextValue(configurations);
  const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
    <ConfigurationContext.Provider value={value}>{children}</ConfigurationContext.Provider>
  );
  return Wrapper;
};

describe('useProductName', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv('VITE_APP_NAME', 'Build CRM');
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('returns the stored general.company_name once the configuration is loaded', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1 })));
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([
      entry('general.company_name', 'Acme CRM'),
    ]);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    const Wrapper: React.FC<{ children: React.ReactNode }> = ({ children }) => (
      <QueryClientProvider client={queryClient}>
        <ConfigurationProvider>{children}</ConfigurationProvider>
      </QueryClientProvider>
    );

    const { result } = renderHook(() => useProductName(), { wrapper: Wrapper });
    // Before the load the build-time name is shown.
    expect(result.current).toBe('Build CRM');
    await waitFor(() => expect(result.current).toBe('Acme CRM'));

    // Any non-blank value wins exactly as stored, the seeded default included.
    for (const stored of ['GopherCRM', '  Padded Name  ']) {
      const { result: direct } = renderHook(() => useProductName(), {
        wrapper: withConfigurations([entry('general.company_name', stored)]),
      });
      expect(direct.current).toBe(stored);
    }
  });

  it('returns VITE_APP_NAME when no company name is stored', () => {
    const { result } = renderHook(() => useProductName(), {
      wrapper: withConfigurations([entry('ui.theme.primary_color', '#ff5722')]),
    });
    expect(result.current).toBe('Build CRM');

    const { result: empty } = renderHook(() => useProductName(), {
      wrapper: withConfigurations([]),
    });
    expect(empty.current).toBe('Build CRM');
  });

  it('returns VITE_APP_NAME when the stored company name is blank', () => {
    for (const stored of ['', '   ', '\t\n']) {
      const { result } = renderHook(() => useProductName(), {
        wrapper: withConfigurations([entry('general.company_name', stored)]),
      });
      expect(result.current).toBe('Build CRM');
    }
  });

  it('returns VITE_APP_NAME when the stored company name is not a string', () => {
    const notStrings: Configuration[] = [
      entry('general.company_name', '42', 'integer'),
      entry('general.company_name', '4.2', 'float'),
      entry('general.company_name', 'true', 'boolean'),
      entry('general.company_name', '{"name":"Acme"}', 'json'),
      entry('general.company_name', '["Acme"]', 'array'),
      entry('general.company_name', 'not json', 'json'),
    ];
    for (const config of notStrings) {
      const { result } = renderHook(() => useProductName(), {
        wrapper: withConfigurations([config]),
      });
      expect(result.current).toBe('Build CRM');
    }
  });

  it('returns VITE_APP_NAME outside a ConfigurationProvider', () => {
    const { result } = renderHook(() => useProductName());
    expect(result.current).toBe('Build CRM');
  });

  it('returns GoCRM when no company name is stored and VITE_APP_NAME is empty', () => {
    for (const buildTime of ['', '   ']) {
      vi.stubEnv('VITE_APP_NAME', buildTime);
      const { result } = renderHook(() => useProductName(), {
        wrapper: withConfigurations([]),
      });
      expect(result.current).toBe('GoCRM');

      const { result: outside } = renderHook(() => useProductName());
      expect(outside.current).toBe('GoCRM');
    }
  });
});
