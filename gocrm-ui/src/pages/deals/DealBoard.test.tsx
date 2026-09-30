import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { AxiosError, AxiosHeaders } from 'axios';
import { useNavigate } from 'react-router-dom';
import { Component as DealBoard } from './DealBoard';
import { dealsApi, type DealFilters } from '@/api/endpoints';
import { createMockCompany, createMockCustomer, createMockDeal, createMockUser } from '@/test/factories';
import type { Deal, DealPipeline, DealStage, User } from '@/types';
import { formatDealAmount } from './dealFormat';
import { DEAL_VIEW_STORAGE_KEY } from './dealView';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  dealsApi: {
    getDeals: vi.fn(),
    getPipeline: vi.fn(),
    changeStage: vi.fn(),
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

// Server-error and refetch assertions get a longer budget under suite load.
const SLOW = { timeout: 3000 };

const acmeDeal = createMockDeal({
  id: 1,
  title: 'Website redesign',
  stage: 'qualification',
  amount_cents: 1250000,
  currency: 'EUR',
  expected_close_date: '2099-12-15',
  company_id: 7,
  company: createMockCompany({ id: 7, name: 'Acme Widgets' }),
  owner: createMockUser({ first_name: 'Ana', last_name: 'Pop' }),
});
const boltDeal = createMockDeal({
  id: 2,
  title: 'Support renewal',
  stage: 'negotiation',
  amount_cents: 99900,
  currency: 'USD',
  probability: 70,
  // Long past while open: flagged.
  expected_close_date: '2020-01-31',
  customer_id: 3,
  customer: createMockCustomer({ id: 3, company_name: 'Bolt Robotics' }),
});
const wonDeal = createMockDeal({
  id: 3,
  title: 'Closed in spring',
  stage: 'won',
  probability: 100,
  expected_close_date: '2020-02-01',
  closed_at: '2020-02-01T10:00:00Z',
});

const pipeline: DealPipeline = {
  stages: [
    {
      stage: 'qualification',
      count: 2,
      totals: [
        { currency: 'EUR', amount_cents: 1250000, weighted_cents: 125000 },
        { currency: 'USD', amount_cents: 50000, weighted_cents: 5000 },
      ],
    },
    { stage: 'proposal', count: 0, totals: [] },
    { stage: 'negotiation', count: 1, totals: [{ currency: 'USD', amount_cents: 99900, weighted_cents: 69930 }] },
    { stage: 'won', count: 4, totals: [{ currency: 'EUR', amount_cents: 880000, weighted_cents: 880000 }] },
    { stage: 'lost', count: 1, totals: [{ currency: 'EUR', amount_cents: 10000, weighted_cents: 0 }] },
  ],
};

let dealsByStage: Record<DealStage, { deals: Deal[]; total: number }>;

const column = (stage: DealStage) => screen.getByTestId(`deal-board-column-${stage}`);
const header = (stage: DealStage) => within(column(stage)).getByTestId('deal-board-column-header');

const callsForStage = (stage: DealStage) =>
  vi.mocked(dealsApi.getDeals).mock.calls.filter(([filters]) => (filters as DealFilters | undefined)?.stage === stage)
    .length;

const openCardMenu = async (title: string) => {
  fireEvent.click(await screen.findByRole('button', { name: `Deal actions for ${title}` }));
};

describe('DealBoard', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.removeItem(DEAL_VIEW_STORAGE_KEY);
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    dealsByStage = {
      qualification: { deals: [acmeDeal], total: 1 },
      proposal: { deals: [], total: 0 },
      negotiation: { deals: [boltDeal], total: 1 },
      won: { deals: [wonDeal], total: 1 },
      lost: { deals: [], total: 0 },
    };
    vi.mocked(dealsApi.getDeals).mockImplementation(async (filters?: DealFilters) =>
      dealsByStage[filters?.stage as DealStage]
    );
    vi.mocked(dealsApi.getPipeline).mockResolvedValue(pipeline);
  });

  it('renders the five stage columns in order with their counts and totals', async () => {
    render(<DealBoard />);

    expect(screen.getByRole('heading', { name: 'Deal pipeline' })).toBeInTheDocument();
    const regions = screen.getAllByRole('region');
    expect(regions.map((region) => region.getAttribute('data-testid'))).toEqual([
      'deal-board-column-qualification',
      'deal-board-column-proposal',
      'deal-board-column-negotiation',
      'deal-board-column-won',
      'deal-board-column-lost',
    ]);
    expect(screen.getByRole('region', { name: 'Qualification' })).toBe(column('qualification'));

    await waitFor(() =>
      expect(within(header('qualification')).getByTestId('deal-board-count')).toHaveTextContent('2 deals')
    );
    // One total per currency, never summed across them; weighted on open stages.
    const qualificationTotals = within(header('qualification')).getAllByTestId('deal-board-total');
    expect(qualificationTotals.map((total) => total.getAttribute('data-currency'))).toEqual(['EUR', 'USD']);
    expect(qualificationTotals[0]).toHaveTextContent(formatDealAmount(1250000, 'EUR'));
    expect(qualificationTotals[0]).toHaveTextContent(`Weighted ${formatDealAmount(125000, 'EUR')}`);
    expect(qualificationTotals[1]).toHaveTextContent(formatDealAmount(50000, 'USD'));
    expect(within(header('proposal')).getByTestId('deal-board-count')).toHaveTextContent('0 deals');
    expect(within(header('proposal')).queryByTestId('deal-board-total')).not.toBeInTheDocument();
    expect(within(header('negotiation')).getByTestId('deal-board-count')).toHaveTextContent('1 deal');

    // Closed columns: count and amount, no weighted line.
    expect(within(header('won')).getByTestId('deal-board-count')).toHaveTextContent('4 deals');
    expect(within(header('won')).getByTestId('deal-board-total')).toHaveTextContent(formatDealAmount(880000, 'EUR'));
    expect(within(header('won')).queryByText(/Weighted/)).not.toBeInTheDocument();
    expect(within(header('lost')).getByTestId('deal-board-count')).toHaveTextContent('1 deal');

    expect(await within(column('proposal')).findByText('No deals')).toBeInTheDocument();
  });

  it('loads each open column sorted by expected close and leaves the closed columns unloaded', async () => {
    render(<DealBoard />);

    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenCalledTimes(3));
    for (const stage of ['qualification', 'proposal', 'negotiation'] as const) {
      expect(dealsApi.getDeals).toHaveBeenCalledWith({
        stage,
        limit: 100,
        sort_by: 'expected_close_date',
        sort_order: 'asc',
      });
    }
    expect(callsForStage('won')).toBe(0);
    expect(callsForStage('lost')).toBe(0);
    expect(dealsApi.getPipeline).toHaveBeenCalledTimes(1);
  });

  it('shows a card with title, account, amount, owner initials and a past-due flag on open deals', async () => {
    render(<DealBoard />);

    const acme = (await within(column('qualification')).findByText('Website redesign')).closest(
      '[data-testid="deal-board-card"]'
    ) as HTMLElement;
    expect(within(acme).getByRole('link', { name: 'Website redesign' })).toHaveAttribute('href', '/deals/1');
    expect(within(acme).getByText('Acme Widgets')).toBeInTheDocument();
    expect(within(acme).getByTestId('deal-board-card-amount')).toHaveTextContent(formatDealAmount(1250000, 'EUR'));
    expect(within(acme).getByRole('img', { name: 'Owner Ana Pop' })).toHaveTextContent('AP');
    expect(within(acme).getByText('Dec 15')).toBeInTheDocument();
    expect(within(acme).queryByTestId('deal-past-due')).not.toBeInTheDocument();

    const bolt = (await within(column('negotiation')).findByText('Support renewal')).closest(
      '[data-testid="deal-board-card"]'
    ) as HTMLElement;
    expect(within(bolt).getByText('Bolt Robotics')).toBeInTheDocument();
    expect(within(bolt).getByTestId('deal-past-due')).toHaveTextContent('Jan 31');
  });

  it('keeps won and lost collapsed until their header is clicked', async () => {
    render(<DealBoard />);

    const wonToggle = screen.getByRole('button', { name: 'Won' });
    expect(wonToggle).toHaveAttribute('aria-expanded', 'false');
    expect(screen.getByRole('button', { name: 'Lost' })).toHaveAttribute('aria-expanded', 'false');
    expect(screen.queryByText('Closed in spring')).not.toBeInTheDocument();

    fireEvent.click(wonToggle);

    expect(wonToggle).toHaveAttribute('aria-expanded', 'true');
    expect(await within(column('won')).findByText('Closed in spring')).toBeInTheDocument();
    expect(dealsApi.getDeals).toHaveBeenCalledWith({
      stage: 'won',
      limit: 100,
      sort_by: 'closed_at',
      sort_order: 'desc',
    });
    // The lost column stays closed and unloaded.
    expect(callsForStage('lost')).toBe(0);
    // A closed deal is not flagged past due.
    expect(within(column('won')).queryByTestId('deal-past-due')).not.toBeInTheDocument();

    fireEvent.click(wonToggle);
    expect(wonToggle).toHaveAttribute('aria-expanded', 'false');
    await waitFor(() => expect(screen.queryByText('Closed in spring')).not.toBeInTheDocument());
  });

  it('moves a card to Proposal from its menu and refetches both columns and the pipeline', async () => {
    vi.mocked(dealsApi.changeStage).mockResolvedValue({ ...acmeDeal, stage: 'proposal' });
    render(<DealBoard />);
    await within(column('qualification')).findByText('Website redesign');
    await waitFor(() => expect(dealsApi.getDeals).toHaveBeenCalledTimes(3));
    const qualificationBefore = callsForStage('qualification');
    const proposalBefore = callsForStage('proposal');

    await openCardMenu('Website redesign');
    const menu = await screen.findByRole('menu');
    // The current stage is not offered.
    expect(within(menu).queryByRole('menuitem', { name: 'Move to Qualification' })).not.toBeInTheDocument();
    expect(within(menu).getByRole('menuitem', { name: 'Open' })).toBeInTheDocument();

    // The server now has the deal under Proposal.
    dealsByStage.qualification = { deals: [], total: 0 };
    dealsByStage.proposal = { deals: [{ ...acmeDeal, stage: 'proposal' }], total: 1 };
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Move to Proposal' }));

    await waitFor(() => expect(dealsApi.changeStage).toHaveBeenCalledWith(1, { stage: 'proposal' }));
    await waitFor(() => {
      expect(callsForStage('qualification')).toBeGreaterThan(qualificationBefore);
      expect(callsForStage('proposal')).toBeGreaterThan(proposalBefore);
      expect(dealsApi.getPipeline).toHaveBeenCalledTimes(2);
    }, SLOW);
    expect(await within(column('proposal')).findByText('Website redesign', {}, SLOW)).toBeInTheDocument();
    expect(within(column('qualification')).queryByText('Website redesign')).not.toBeInTheDocument();
    expect(showSuccess).toHaveBeenCalledWith('Moved "Website redesign" to Proposal');
  });

  it('asks for a reason before moving a card to Lost and sends it', async () => {
    vi.mocked(dealsApi.changeStage).mockResolvedValue({ ...boltDeal, stage: 'lost', lost_reason: 'Budget cut' });
    render(<DealBoard />);

    await openCardMenu('Support renewal');
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Move to Lost' }));

    const dialog = await screen.findByRole('dialog', { name: 'Mark deal as lost' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));
    expect(within(dialog).getByText('A reason is required to mark the deal as lost')).toBeInTheDocument();
    expect(dealsApi.changeStage).not.toHaveBeenCalled();

    fireEvent.change(within(dialog).getByLabelText(/^Lost reason/), { target: { value: '  Budget cut ' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));

    await waitFor(() =>
      expect(dealsApi.changeStage).toHaveBeenCalledWith(2, { stage: 'lost', lost_reason: 'Budget cut' })
    );
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Mark deal as lost' })).not.toBeInTheDocument());
    await waitFor(() => expect(dealsApi.getPipeline).toHaveBeenCalledTimes(2), SLOW);
  });

  it('cancelling the lost dialog moves nothing', async () => {
    render(<DealBoard />);

    await openCardMenu('Support renewal');
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Move to Lost' }));
    const dialog = await screen.findByRole('dialog', { name: 'Mark deal as lost' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Mark deal as lost' })).not.toBeInTheDocument());
    expect(dealsApi.changeStage).not.toHaveBeenCalled();
  });

  it('shows the server message when a move fails', async () => {
    vi.mocked(dealsApi.changeStage).mockRejectedValue(
      new AxiosError('Request failed', 'ERR_BAD_REQUEST', undefined, undefined, {
        status: 403,
        statusText: 'Forbidden',
        headers: {},
        config: { headers: new AxiosHeaders() },
        data: { message: 'You can only change your own deals' },
      })
    );
    render(<DealBoard />);

    await openCardMenu('Website redesign');
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Move to Negotiation' }));

    await waitFor(() => expect(dealsApi.changeStage).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(showError).toHaveBeenCalledWith('You can only change your own deals'), SLOW);
  });

  it('opens the detail page from the card menu', async () => {
    render(<DealBoard />);

    await openCardMenu('Website redesign');
    fireEvent.click(await screen.findByRole('menuitem', { name: 'Open' }));

    expect(mockNavigate).toHaveBeenCalledWith('/deals/1');
  });

  it('links to the filtered list when a column holds more than one page', async () => {
    dealsByStage.proposal = {
      deals: Array.from({ length: 100 }, (_, index) =>
        createMockDeal({ id: 1000 + index, title: `Bulk deal ${index}`, stage: 'proposal' })
      ),
      total: 130,
    };
    render(<DealBoard />);

    const more = await within(column('proposal')).findByRole('link', { name: '+30 more in the list' }, SLOW);
    expect(more).toHaveAttribute('href', '/deals?stage=proposal');
    // A column that fits in one page has no such link.
    expect(within(column('qualification')).queryByText(/more in the list/)).not.toBeInTheDocument();
  });

  it('switches to the list and remembers the choice', async () => {
    render(<DealBoard />);

    expect(screen.getByRole('button', { name: 'Board' })).toHaveAttribute('aria-pressed', 'true');
    fireEvent.click(screen.getByRole('button', { name: 'List' }));

    expect(mockNavigate).toHaveBeenCalledWith('/deals');
    expect(localStorage.getItem(DEAL_VIEW_STORAGE_KEY)).toBe('list');
  });
});
