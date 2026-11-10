import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { Component as QuotePrint } from './QuotePrint';
import { quotesApi } from '@/api/endpoints';
import {
  ConfigurationContext,
  type ConfigurationContextType,
} from '@/contexts/configuration-context';
import type { Configuration } from '@/api/endpoints/configurations';
import { createMockQuote } from '@/test/factories';

vi.mock('@/api/endpoints', () => ({
  quotesApi: { getQuote: vi.fn() },
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

const renderPrint = (configurations: Configuration[]) =>
  render(
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>
      <MemoryRouter initialEntries={['/quotes/42/print']}>
        <ConfigurationContext.Provider value={contextValue(configurations)}>
          <Routes>
            <Route path="/quotes/:id/print" element={<QuotePrint />} />
          </Routes>
        </ConfigurationContext.Provider>
      </MemoryRouter>
    </QueryClientProvider>,
  );

const heading = async () => {
  const element = await screen.findByRole('heading', { level: 1 });
  expect(element).toHaveAttribute('id', 'quote-print-heading');
  return element;
};

describe('QuotePrint product name', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.stubEnv('VITE_APP_NAME', 'Build CRM');
    vi.mocked(quotesApi.getQuote).mockResolvedValue(
      createMockQuote({ id: 42, number: 'Q-000042', title: 'Website redesign quote' }),
    );
  });

  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('shows the stored company name in the heading', async () => {
    renderPrint([companyName('Acme CRM')]);
    expect(await heading()).toHaveTextContent(/^Acme CRM$/);
  });

  it('shows VITE_APP_NAME when no company name is stored', async () => {
    renderPrint([]);
    expect(await heading()).toHaveTextContent(/^Build CRM$/);
  });

  it('shows GoCRM when no company name is stored and VITE_APP_NAME is empty', async () => {
    vi.stubEnv('VITE_APP_NAME', '');
    renderPrint([]);
    expect(await heading()).toHaveTextContent(/^GoCRM$/);
  });
});
