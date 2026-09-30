import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen } from '@/test/test-utils';
import { Component as CustomerDetail } from './CustomerDetail';
import { customersApi, ticketsApi } from '@/api/endpoints';
import { createMockCustomer, createMockCompany } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  customersApi: {
    getCustomer: vi.fn(),
    deleteCustomer: vi.fn(),
  },
  ticketsApi: {
    getTickets: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}));

describe('CustomerDetail company', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(ticketsApi.getTickets).mockResolvedValue({ data: [], total: 0, page: 1, limit: 10, total_pages: 0 });
  });

  it('links to the company record when the customer is linked', async () => {
    vi.mocked(customersApi.getCustomer).mockResolvedValue(
      createMockCustomer({
        company_name: 'Acme (as typed)',
        company_id: 7,
        company_record: createMockCompany({ id: 7, name: 'Acme Widgets' }),
      })
    );

    render(<CustomerDetail />);

    expect(await screen.findByRole('link', { name: 'Acme Widgets' })).toHaveAttribute('href', '/companies/7');
  });

  it('shows the free-text company when there is no link', async () => {
    vi.mocked(customersApi.getCustomer).mockResolvedValue(createMockCustomer({ company_name: 'Loose Text Ltd' }));

    render(<CustomerDetail />);

    expect((await screen.findAllByText('Loose Text Ltd')).length).toBeGreaterThan(0);
    expect(screen.queryByRole('link', { name: 'Loose Text Ltd' })).not.toBeInTheDocument();
  });
});
