import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as DealList } from './DealList';
import { dealsApi } from '@/api/endpoints';
import { createMockCompany, createMockCustomer, createMockDeal, createMockUser } from '@/test/factories';
import { useNavigate } from 'react-router-dom';
import type { User } from '@/types';
import { formatDealAmount } from './dealFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  dealsApi: {
    getDeals: vi.fn(),
    deleteDeal: vi.fn(),
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

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

describe('DealList', () => {
  const mockNavigate = vi.fn();
  const acmeDeal = createMockDeal({
    id: 9,
    title: 'Website redesign',
    stage: 'proposal',
    amount_cents: 1250000,
    currency: 'EUR',
    probability: 40,
    expected_close_date: '2099-12-15',
    company_id: 7,
    company: createMockCompany({ id: 7, name: 'Acme Widgets' }),
    owner: createMockUser({ first_name: 'Ana', last_name: 'Pop' }),
  });
  const customerDeal = createMockDeal({
    id: 10,
    title: 'Support renewal',
    stage: 'negotiation',
    amount_cents: 99900,
    currency: 'USD',
    probability: 70,
    // Long past, and the stage is open: shown as past due.
    expected_close_date: '2020-01-31',
    customer_id: 3,
    customer: createMockCustomer({ id: 3, company_name: 'Bolt Robotics', contact_name: 'Jane Smith' }),
  });
  const wonDeal = createMockDeal({
    id: 11,
    title: 'Closed last year',
    stage: 'won',
    probability: 100,
    // Past as well, but closed: not past due.
    expected_close_date: '2020-02-01',
    closed_at: '2020-02-01T10:00:00Z',
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(dealsApi.getDeals).mockResolvedValue({ deals: [acmeDeal, customerDeal, wonDeal], total: 3 });
  });

  it('requests the first page with offset/limit and no stage or open filter', async () => {
    render(<DealList />);

    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenCalledTimes(1));
    expect(dealsApi.getDeals).toHaveBeenCalledWith({
      offset: 0,
      limit: 10,
      search: undefined,
      stage: undefined,
      open: undefined,
      sort_by: undefined,
      sort_order: undefined,
    });
  });

  it('renders title, company or customer, stage chip, formatted amount, probability, date and owner', async () => {
    render(<DealList />);

    const acme = await rowOf('Website redesign');
    expect(within(acme).getByText('Acme Widgets')).toBeInTheDocument();
    expect(within(acme).getByTestId('deal-stage-chip')).toHaveTextContent('Proposal');
    expect(within(acme).getByText(formatDealAmount(1250000, 'EUR'))).toBeInTheDocument();
    expect(within(acme).getByText('40%')).toBeInTheDocument();
    expect(within(acme).getByText('Dec 15, 2099')).toBeInTheDocument();
    expect(within(acme).getByText('Ana Pop')).toBeInTheDocument();

    // No company: the customer's company name stands in.
    const bolt = await rowOf('Support renewal');
    expect(within(bolt).getByText('Bolt Robotics')).toBeInTheDocument();
    expect(within(bolt).getByText(formatDealAmount(99900, 'USD'))).toBeInTheDocument();
  });

  it('marks a past expected close as past due only while the deal is open', async () => {
    render(<DealList />);

    const open = await rowOf('Support renewal');
    expect(within(open).getByTestId('deal-past-due')).toHaveTextContent('Jan 31, 2020');

    const won = await rowOf('Closed last year');
    expect(within(won).queryByTestId('deal-past-due')).not.toBeInTheDocument();
    expect(within(won).getByText('Feb 01, 2020')).toBeInTheDocument();
  });

  it('sends the stage filter server-side', async () => {
    render(<DealList />);
    await rowOf('Website redesign');

    fireEvent.mouseDown(screen.getByRole('combobox', { name: /Stage/ }));
    fireEvent.click(await screen.findByRole('option', { name: 'Proposal' }));

    await waitFor(() =>
      expect(dealsApi.getDeals).toHaveBeenLastCalledWith(expect.objectContaining({ stage: 'proposal', offset: 0 }))
    );
  });

  it('sends open=true when "Open only" is switched on and drops it when switched off', async () => {
    render(<DealList />);
    await rowOf('Website redesign');

    fireEvent.click(screen.getByLabelText('Open only'));
    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenLastCalledWith(expect.objectContaining({ open: true })));

    // The list re-mounts while the new page loads, so the switch is looked up again.
    await rowOf('Website redesign');
    fireEvent.click(screen.getByLabelText('Open only'));
    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenLastCalledWith(expect.objectContaining({ open: undefined })));
  });

  it('sends the search text server-side', async () => {
    render(<DealList />);
    await rowOf('Website redesign');

    fireEvent.change(screen.getByLabelText('Search deals'), { target: { value: 'renewal' } });

    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenLastCalledWith(expect.objectContaining({ search: 'renewal' })));
  });

  it('sorts by an allowlisted column and offers no sort on the account and owner columns', async () => {
    render(<DealList />);
    await rowOf('Website redesign');

    fireEvent.click(screen.getByRole('button', { name: /^Amount/ }));
    await waitFor(() =>
      expect(dealsApi.getDeals).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'amount_cents', sort_order: 'asc' })
      )
    );

    expect(screen.queryByRole('button', { name: /Company \/ Customer/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Owner/ })).not.toBeInTheDocument();
  });

  it('lets an admin create and delete', async () => {
    vi.mocked(dealsApi.deleteDeal).mockResolvedValue();
    render(<DealList />);

    fireEvent.click(await screen.findByRole('button', { name: 'New Deal' }));
    expect(mockNavigate).toHaveBeenCalledWith('/deals/new');

    const row = await rowOf('Website redesign');
    fireEvent.click(row.querySelector('button svg[data-testid="DeleteIcon"]')?.parentElement as HTMLElement);
    const dialog = await screen.findByRole('dialog', { name: 'Delete Deal' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(dealsApi.deleteDeal).toHaveBeenCalledWith(9));
    await waitFor(() => expect(showSuccess).toHaveBeenCalledWith('Deal deleted successfully'));
  });

  it('lets sales create and edit but not delete', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    render(<DealList />);

    expect(await screen.findByRole('button', { name: 'New Deal' })).toBeInTheDocument();
    const row = await rowOf('Website redesign');
    expect(row.querySelector('svg[data-testid="EditIcon"]')).not.toBeNull();
    expect(row.querySelector('svg[data-testid="DeleteIcon"]')).toBeNull();
  });

  it('opens the detail page from a row', async () => {
    render(<DealList />);

    fireEvent.click(await screen.findByText('Website redesign'));

    expect(mockNavigate).toHaveBeenCalledWith('/deals/9');
  });
});
