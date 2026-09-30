import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, fireEvent, within } from '@/test/test-utils';
import { Component as CustomerDetail } from './CustomerDetail';
import { customersApi, dealsApi, ticketsApi } from '@/api/endpoints';
import { createMockCustomer, createMockCompany, createMockDeal, createMockUser } from '@/test/factories';
import type { User } from '@/types';
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
  dealsApi: {
    getCustomerDeals: vi.fn(),
  },
}));

const mockUseAuth = vi.fn();
vi.mock('@/hooks/useAuth', () => ({
  useAuth: () => mockUseAuth(),
}));

const authState = (user: User) => ({
  user,
  isLoading: false,
  isAuthenticated: true,
  login: vi.fn(),
  register: vi.fn(),
  logout: vi.fn(),
  refreshUser: vi.fn(),
});

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
}));

describe('CustomerDetail company', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(ticketsApi.getTickets).mockResolvedValue({ data: [], total: 0, page: 1, limit: 10, total_pages: 0 });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(dealsApi.getCustomerDeals).mockResolvedValue({ deals: [], total: 0 });
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

describe('CustomerDetail deals section', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(ticketsApi.getTickets).mockResolvedValue({ data: [], total: 0, page: 1, limit: 10, total_pages: 0 });
    vi.mocked(customersApi.getCustomer).mockResolvedValue(createMockCustomer({ id: 5, company_name: 'Bolt Robotics' }));
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'sales' })));
    vi.mocked(dealsApi.getCustomerDeals).mockResolvedValue({
      deals: [createMockDeal({ id: 10, title: 'Support renewal', stage: 'negotiation', amount_cents: 99900, currency: 'USD' })],
      total: 1,
    });
  });

  it('lists the customer deals with a link to each and a prefilled "New Deal"', async () => {
    render(<CustomerDetail />);

    const section = await screen.findByRole('region', { name: /Deals/ });
    expect(dealsApi.getCustomerDeals).toHaveBeenCalledWith(5, { offset: 0, limit: 5 });
    expect(await within(section).findByRole('link', { name: 'Support renewal' })).toHaveAttribute('href', '/deals/10');
    expect(within(section).getByTestId('deal-stage-chip')).toHaveTextContent('Negotiation');

    fireEvent.click(within(section).getByRole('button', { name: 'New Deal' }));
    expect(mockNavigate).toHaveBeenCalledWith('/deals/new?customer_id=5');
  });

  it('says so when the customer has no deals', async () => {
    vi.mocked(dealsApi.getCustomerDeals).mockResolvedValue({ deals: [], total: 0 });
    render(<CustomerDetail />);

    const section = await screen.findByRole('region', { name: /Deals/ });
    expect(within(section).getByText('No deals for this customer')).toBeInTheDocument();
  });

  it('hides the deals section from support', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));
    render(<CustomerDetail />);

    await screen.findByRole('heading', { level: 4, name: 'Bolt Robotics' });
    expect(screen.queryByRole('region', { name: /Deals/ })).not.toBeInTheDocument();
    expect(dealsApi.getCustomerDeals).not.toHaveBeenCalled();
  });
});
