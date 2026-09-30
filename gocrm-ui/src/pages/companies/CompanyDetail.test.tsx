import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as CompanyDetail } from './CompanyDetail';
import { companiesApi, dealsApi } from '@/api/endpoints';
import { createMockCompany, createMockCustomer, createMockDeal, createMockLead, createMockUser } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';
import type { User } from '@/types';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return {
    ...actual,
    useNavigate: vi.fn(),
    useParams: vi.fn(),
  };
});

vi.mock('@/api/endpoints', () => ({
  companiesApi: {
    getCompany: vi.fn(),
    getCompanyCustomers: vi.fn(),
    getCompanyLeads: vi.fn(),
    deleteCompany: vi.fn(),
  },
  dealsApi: {
    getCompanyDeals: vi.fn(),
  },
}));

const showError = vi.fn();
const showSuccess = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
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

describe('CompanyDetail', () => {
  const mockNavigate = vi.fn();
  const company = createMockCompany({
    id: 7,
    name: 'Acme Widgets',
    domain: 'acme.example',
    website: 'https://www.acme.example',
    industry: 'Manufacturing',
    employee_range: '51-200',
    notes: 'Key account',
    customer_count: 2,
    lead_count: 1,
    owner: createMockUser({ first_name: 'Ana', last_name: 'Pop' }),
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '7' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(companiesApi.getCompany).mockResolvedValue(company);
    vi.mocked(companiesApi.getCompanyCustomers).mockResolvedValue({
      customers: [
        createMockCustomer({ id: 11, contact_name: 'Jane Smith', email: 'jane@acme.example' }),
        createMockCustomer({ id: 12, contact_name: 'Joe Bloggs', email: 'joe@acme.example' }),
      ],
      total: 2,
    });
    vi.mocked(dealsApi.getCompanyDeals).mockResolvedValue({
      deals: [createMockDeal({ id: 9, title: 'Website redesign', stage: 'proposal', amount_cents: 1250000, currency: 'EUR' })],
      total: 1,
    });
    vi.mocked(companiesApi.getCompanyLeads).mockResolvedValue({
      leads: [createMockLead({ id: 21, contact_name: 'Ion Ionescu', email: 'ion@acme.example', status: 'qualified' })],
      total: 1,
    });
  });

  it('shows the company fields, the owner and the address', async () => {
    render(<CompanyDetail />);

    expect(await screen.findByRole('heading', { name: 'Acme Widgets' })).toBeInTheDocument();
    expect(screen.getByText('acme.example')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'https://www.acme.example' })).toHaveAttribute(
      'href',
      'https://www.acme.example'
    );
    expect(screen.getByText('Manufacturing')).toBeInTheDocument();
    expect(screen.getByText('51-200 employees')).toBeInTheDocument();
    expect(screen.getByText('Ana Pop')).toBeInTheDocument();
    expect(screen.getByText('Key account')).toBeInTheDocument();
    expect(screen.getByText(/1 Foundry Lane/)).toBeInTheDocument();
  });

  it('lists the linked customers with links and the count in the heading', async () => {
    render(<CompanyDetail />);

    expect(await screen.findByRole('heading', { name: 'Customers (2)' })).toBeInTheDocument();
    await waitFor(() => {
      expect(companiesApi.getCompanyCustomers).toHaveBeenCalledWith(7, { offset: 0, limit: 5 });
    });
    expect(await screen.findByRole('link', { name: 'Jane Smith' })).toHaveAttribute('href', '/customers/11');
    expect(screen.getByRole('link', { name: 'Joe Bloggs' })).toHaveAttribute('href', '/customers/12');
  });

  it('lists the linked leads for an admin', async () => {
    render(<CompanyDetail />);

    expect(await screen.findByRole('heading', { name: 'Leads (1)' })).toBeInTheDocument();
    await waitFor(() => {
      expect(companiesApi.getCompanyLeads).toHaveBeenCalledWith(7, { offset: 0, limit: 5 });
    });
    expect(await screen.findByRole('link', { name: 'Ion Ionescu' })).toHaveAttribute('href', '/leads/21');
    expect(screen.getByText('qualified')).toBeInTheDocument();
  });

  it('pages the customers table through the sub-resource endpoint', async () => {
    vi.mocked(companiesApi.getCompanyCustomers).mockResolvedValue({
      customers: [createMockCustomer({ id: 11, contact_name: 'Jane Smith' })],
      total: 12,
    });

    render(<CompanyDetail />);
    const section = (await screen.findByRole('heading', { name: 'Customers (2)' })).closest('section') as HTMLElement;

    fireEvent.click(within(section).getByRole('button', { name: /next page/i }));

    await waitFor(() => {
      expect(companiesApi.getCompanyCustomers).toHaveBeenLastCalledWith(7, { offset: 5, limit: 5 });
    });
  });

  it('hides the leads section and the write actions for a support user', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));

    render(<CompanyDetail />);

    expect(await screen.findByRole('heading', { name: 'Acme Widgets' })).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: /^Leads/ })).not.toBeInTheDocument();
    expect(companiesApi.getCompanyLeads).not.toHaveBeenCalled();
    expect(screen.queryByRole('button', { name: 'Edit' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete company' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'New Customer' })).not.toBeInTheDocument();
  });

  it('lets sales edit and add a customer but not delete', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));

    render(<CompanyDetail />);

    expect(await screen.findByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'New Customer' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete company' })).not.toBeInTheDocument();
  });

  it('prefills the customer form with the company id from New Customer', async () => {
    render(<CompanyDetail />);

    fireEvent.click(await screen.findByRole('button', { name: 'New Customer' }));

    expect(mockNavigate).toHaveBeenCalledWith('/customers/new?company_id=7');
  });

  it('deletes the company after confirmation and returns to the list', async () => {
    vi.mocked(companiesApi.deleteCompany).mockResolvedValue(undefined);

    render(<CompanyDetail />);

    fireEvent.click(await screen.findByRole('button', { name: 'Delete company' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Company' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(companiesApi.deleteCompany).toHaveBeenCalledWith(7));
    expect(mockNavigate).toHaveBeenCalledWith('/companies');
    expect(showSuccess).toHaveBeenCalledWith('Company deleted successfully');
  });

  it('shows empty states when nothing is linked', async () => {
    vi.mocked(companiesApi.getCompanyCustomers).mockResolvedValue({ customers: [], total: 0 });
    vi.mocked(companiesApi.getCompanyLeads).mockResolvedValue({ leads: [], total: 0 });

    render(<CompanyDetail />);

    expect(await screen.findByText('No customers linked to this company')).toBeInTheDocument();
    expect(await screen.findByText('No leads linked to this company')).toBeInTheDocument();
  });
});

describe('CompanyDetail deals section', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '7' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(companiesApi.getCompany).mockResolvedValue(createMockCompany({ id: 7, name: 'Acme Widgets' }));
    vi.mocked(companiesApi.getCompanyCustomers).mockResolvedValue({ customers: [], total: 0 });
    vi.mocked(companiesApi.getCompanyLeads).mockResolvedValue({ leads: [], total: 0 });
    vi.mocked(dealsApi.getCompanyDeals).mockResolvedValue({
      deals: [createMockDeal({ id: 9, title: 'Website redesign', stage: 'proposal', amount_cents: 1250000, currency: 'EUR' })],
      total: 1,
    });
  });

  it('lists the company deals from the sub-resource endpoint with a link to each', async () => {
    render(<CompanyDetail />);

    const section = await screen.findByRole('region', { name: /Deals/ });
    expect(dealsApi.getCompanyDeals).toHaveBeenCalledWith(7, { offset: 0, limit: 5 });
    expect(await within(section).findByRole('link', { name: 'Website redesign' })).toHaveAttribute('href', '/deals/9');
    expect(within(section).getByTestId('deal-stage-chip')).toHaveTextContent('Proposal');
    expect(within(section).getByRole('heading', { name: 'Deals (1)' })).toBeInTheDocument();
  });

  it('opens a prefilled deal form from "New Deal"', async () => {
    render(<CompanyDetail />);

    const section = await screen.findByRole('region', { name: /Deals/ });
    fireEvent.click(within(section).getByRole('button', { name: 'New Deal' }));

    expect(mockNavigate).toHaveBeenCalledWith('/deals/new?company_id=7');
  });

  it('hides the deals section from support', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 3, role: 'support' })));
    render(<CompanyDetail />);

    await screen.findByRole('heading', { name: 'Acme Widgets' });
    expect(screen.queryByRole('region', { name: /Deals/ })).not.toBeInTheDocument();
    expect(dealsApi.getCompanyDeals).not.toHaveBeenCalled();
  });
});
