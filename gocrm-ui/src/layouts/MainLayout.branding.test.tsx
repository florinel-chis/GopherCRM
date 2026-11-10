import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { MainLayout } from './MainLayout';
import {
  ConfigurationContext,
  type ConfigurationContextType,
} from '@/contexts/configuration-context';
import type { Configuration } from '@/api/endpoints/configurations';
import { createMockUser } from '@/test/factories';

vi.mock('@/components/layout/NotificationBell', () => ({
  NotificationBell: () => <div data-testid="notification-bell" />,
}));

vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => ({
    user: createMockUser({ role: 'admin' }),
    isLoading: false,
    isAuthenticated: true,
    login: vi.fn(),
    register: vi.fn(),
    logout: vi.fn(),
    refreshUser: vi.fn(),
  }),
}));

const companyName = (value: string): Configuration => ({
  id: 1,
  key: 'general.company_name',
  value,
  type: 'string',
  category: 'general',
  description: '',
  default_value: 'GopherCRM',
  is_system: true,
  is_read_only: false,
  valid_values: '',
  created_at: '2026-11-10T00:00:00Z',
  updated_at: '2026-11-10T00:00:00Z',
});

const contextValue = (configurations: Configuration[]): ConfigurationContextType => ({
  configurations,
  isLoading: false,
  error: null,
  getLeadConversion: () => ({ status: 'unavailable' }),
  getCompanyName: () => 'NOT FROM getCompanyName',
  getPrimaryColor: () => '#1976d2',
  refreshConfigurations: vi.fn(async () => {}),
});

const renderLayout = (configurations: Configuration[]) =>
  render(
    <MemoryRouter>
      <ConfigurationContext.Provider value={contextValue(configurations)}>
        <MainLayout />
      </ConfigurationContext.Provider>
    </MemoryRouter>,
  );

// The permanent drawer is what a desktop viewer sees; the temporary one is
// kept mounted for mobile and would double the title. The title is the first
// toolbar of the drawer paper (jsdom ignores the `sm` media query, so text
// queries are used rather than role queries).
const drawerTitle = () => {
  const toolbar = screen
    .getByTestId('app-navigation')
    .querySelector('.MuiToolbar-root .MuiTypography-root');
  return toolbar?.textContent;
};

describe('MainLayout product name', () => {
  beforeEach(() => {
    vi.stubEnv('VITE_APP_NAME', 'Build CRM');
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('shows the stored company name in the drawer', () => {
    renderLayout([companyName('Acme CRM')]);
    expect(drawerTitle()).toBe('Acme CRM');
    expect(within(screen.getByTestId('app-navigation')).getByText('Acme CRM')).toBeInTheDocument();
    expect(screen.queryByText('Build CRM')).not.toBeInTheDocument();
  });

  it('shows VITE_APP_NAME when no company name is stored', () => {
    renderLayout([]);
    expect(drawerTitle()).toBe('Build CRM');
  });

  it('shows GoCRM when no company name is stored and VITE_APP_NAME is empty', () => {
    vi.stubEnv('VITE_APP_NAME', '');
    renderLayout([companyName('   ')]);
    expect(drawerTitle()).toBe('GoCRM');
  });
});
