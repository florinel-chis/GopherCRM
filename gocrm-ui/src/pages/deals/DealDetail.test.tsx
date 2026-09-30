import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as DealDetail } from './DealDetail';
import { dealsApi } from '@/api/endpoints';
import {
  createMockCompany,
  createMockCustomer,
  createMockDeal,
  createMockDealStageChange,
  createMockLead,
  createMockUser,
} from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';
import type { User } from '@/types';
import { formatDealAmount } from './dealFormat';
import { DASHBOARD_PIPELINE_KEY, clientWithDashboardPipeline, isInvalidated, withClient } from '@/test/dealQueryCache';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  dealsApi: {
    getDeal: vi.fn(),
    getDealHistory: vi.fn(),
    changeStage: vi.fn(),
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

const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Bad Request',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const stageSelect = () => screen.getByRole('combobox', { name: /^Stage/ });
const pickStage = async (label: string) => {
  fireEvent.mouseDown(stageSelect());
  fireEvent.click(await screen.findByRole('option', { name: label }));
};

describe('DealDetail', () => {
  const mockNavigate = vi.fn();
  const deal = createMockDeal({
    id: 9,
    title: 'Website redesign',
    stage: 'proposal',
    amount_cents: 1250000,
    currency: 'EUR',
    probability: 40,
    expected_close_date: '2099-12-15',
    source: 'referral',
    notes: 'Key account',
    company_id: 7,
    company: createMockCompany({ id: 7, name: 'Acme Widgets' }),
    customer_id: 3,
    customer: createMockCustomer({ id: 3, contact_name: 'Jane Smith' }),
    lead_id: 5,
    lead: createMockLead({ id: 5, contact_name: 'Ion Ionescu' }),
    owner: createMockUser({ first_name: 'Ana', last_name: 'Pop' }),
  });
  const history = [
    createMockDealStageChange({ id: 1, from_stage: null, to_stage: 'qualification' }),
    createMockDealStageChange({ id: 2, from_stage: 'qualification', to_stage: 'proposal', changed_at: '2026-09-30T09:00:00Z' }),
  ];

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({ id: '9' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(dealsApi.getDeal).mockResolvedValue(deal);
    vi.mocked(dealsApi.getDealHistory).mockResolvedValue(history);
  });

  it('renders the fields, the links and the history timeline', async () => {
    render(<DealDetail />);

    expect(await screen.findByRole('heading', { name: 'Website redesign' })).toBeInTheDocument();
    expect(screen.getByTestId('deal-stage-chip')).toHaveTextContent('Proposal');
    expect(screen.getByTestId('deal-amount')).toHaveTextContent(formatDealAmount(1250000, 'EUR'));
    expect(screen.getByText('40%')).toBeInTheDocument();
    expect(screen.getByText('Dec 15, 2099')).toBeInTheDocument();
    expect(screen.queryByTestId('deal-past-due')).not.toBeInTheDocument();
    expect(screen.getByText('Ana Pop')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Acme Widgets' })).toHaveAttribute('href', '/companies/7');
    expect(screen.getByRole('link', { name: 'Jane Smith' })).toHaveAttribute('href', '/customers/3');
    expect(screen.getByRole('link', { name: 'Ion Ionescu' })).toHaveAttribute('href', '/leads/5');
    expect(screen.getByText('referral')).toBeInTheDocument();
    expect(screen.getByText('Key account')).toBeInTheDocument();
    // Open deal: nothing closed yet.
    expect(screen.queryByTestId('deal-closed-at')).not.toBeInTheDocument();
    expect(screen.queryByTestId('deal-lost-reason')).not.toBeInTheDocument();

    const rows = within(await screen.findByRole('list', { name: 'Stage history' })).getAllByTestId('deal-history-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Created in Qualification');
    expect(rows[1]).toHaveTextContent('Qualification to Proposal');
  });

  it('flags a past expected close on an open deal', async () => {
    vi.mocked(dealsApi.getDeal).mockResolvedValue({ ...deal, expected_close_date: '2020-01-31' });
    render(<DealDetail />);

    expect(await screen.findByTestId('deal-past-due')).toHaveTextContent('Jan 31, 2020 (past due)');
  });

  it('shows closed_at on a won deal and the reason on a lost one, with no past-due flag', async () => {
    vi.mocked(dealsApi.getDeal).mockResolvedValue({
      ...deal,
      stage: 'lost',
      probability: 0,
      expected_close_date: '2020-01-31',
      closed_at: '2026-10-01T10:00:00Z',
      lost_reason: 'Budget cut',
    });
    render(<DealDetail />);

    expect(await screen.findByTestId('deal-closed-at')).toHaveTextContent('Oct 01, 2026');
    expect(screen.getByTestId('deal-lost-reason')).toHaveTextContent('Budget cut');
    expect(screen.queryByTestId('deal-past-due')).not.toBeInTheDocument();
  });

  it('moves to another open stage straight away and refreshes the history', async () => {
    const moved = { ...deal, stage: 'negotiation' as const, probability: 70 };
    vi.mocked(dealsApi.changeStage).mockResolvedValue(moved);
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Negotiation');

    await waitFor(() => expect(dealsApi.changeStage).toHaveBeenCalledWith(9, { stage: 'negotiation' }));
    await waitFor(() => expect(screen.getByTestId('deal-stage-chip')).toHaveTextContent('Negotiation'));
    await waitFor(() => expect(dealsApi.getDealHistory).toHaveBeenCalledTimes(2));
    expect(showSuccess).toHaveBeenCalledWith('Stage updated');
  });

  it('marks the dashboard pipeline and the deal lists stale after a stage change', async () => {
    vi.mocked(dealsApi.changeStage).mockResolvedValue({ ...deal, stage: 'negotiation' as const, probability: 70 });
    const client = clientWithDashboardPipeline();
    client.setQueryData(['deals', 'pipeline'], { stages: [] });
    render(withClient(client, <DealDetail />));
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Negotiation');

    await waitFor(() => expect(isInvalidated(client, DASHBOARD_PIPELINE_KEY)).toBe(true));
    expect(isInvalidated(client, ['deals', 'pipeline'])).toBe(true);
  });

  it('marks the dashboard pipeline stale after a delete', async () => {
    vi.mocked(dealsApi.deleteDeal).mockResolvedValue();
    const client = clientWithDashboardPipeline();
    render(withClient(client, <DealDetail />));
    await screen.findByRole('heading', { name: 'Website redesign' });

    fireEvent.click(screen.getByRole('button', { name: 'Delete deal' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Deal' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(isInvalidated(client, DASHBOARD_PIPELINE_KEY)).toBe(true));
  });

  it('does nothing when the current stage is picked again', async () => {
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Proposal');

    expect(dealsApi.changeStage).not.toHaveBeenCalled();
  });

  it('asks for a reason before marking the deal lost and sends it', async () => {
    vi.mocked(dealsApi.changeStage).mockResolvedValue({
      ...deal,
      stage: 'lost',
      probability: 0,
      lost_reason: 'Budget cut',
      closed_at: '2026-10-01T10:00:00Z',
    });
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Lost');
    const dialog = await screen.findByRole('dialog', { name: 'Mark deal as lost' });

    // Empty reason: refused locally.
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));
    expect(within(dialog).getByText('A reason is required to mark the deal as lost')).toBeInTheDocument();
    expect(dealsApi.changeStage).not.toHaveBeenCalled();

    fireEvent.change(within(dialog).getByLabelText(/^Lost reason/), { target: { value: 'Budget cut' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));

    await waitFor(() => expect(dealsApi.changeStage).toHaveBeenCalledWith(9, { stage: 'lost', lost_reason: 'Budget cut' }));
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Mark deal as lost' })).not.toBeInTheDocument());
    expect(await screen.findByTestId('deal-lost-reason')).toHaveTextContent('Budget cut');
    expect(screen.getByTestId('deal-closed-at')).toBeInTheDocument();
  });

  it('cancelling the lost dialog changes nothing', async () => {
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Lost');
    const dialog = await screen.findByRole('dialog', { name: 'Mark deal as lost' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel' }));

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Mark deal as lost' })).not.toBeInTheDocument());
    expect(dealsApi.changeStage).not.toHaveBeenCalled();
    expect(screen.getByTestId('deal-stage-chip')).toHaveTextContent('Proposal');
  });

  it('shows the server message when the stage change is refused', async () => {
    vi.mocked(dealsApi.changeStage).mockRejectedValue(apiError(400, 'VALIDATION_ERROR', 'stage is invalid'));
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    await pickStage('Won');

    await waitFor(() => expect(dealsApi.changeStage).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(showError).toHaveBeenCalledWith('stage is invalid'), { timeout: 3000 });
  });

  it('lets an admin delete and returns to the list', async () => {
    vi.mocked(dealsApi.deleteDeal).mockResolvedValue();
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    fireEvent.click(screen.getByRole('button', { name: 'Delete deal' }));
    const dialog = await screen.findByRole('dialog', { name: 'Delete Deal' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(dealsApi.deleteDeal).toHaveBeenCalledWith(9));
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/deals'));
  });

  it('gives sales the stage selector and edit but no delete', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    expect(stageSelect()).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Edit' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Delete deal' })).not.toBeInTheDocument();
  });

  it('opens the edit form', async () => {
    render(<DealDetail />);
    await screen.findByRole('heading', { name: 'Website redesign' });

    fireEvent.click(screen.getByRole('button', { name: 'Edit' }));

    expect(mockNavigate).toHaveBeenCalledWith('/deals/9/edit');
  });

  it('says so when the deal cannot be loaded (another user\'s deal for sales)', async () => {
    vi.mocked(dealsApi.getDeal).mockRejectedValue(apiError(404, 'NOT_FOUND', 'deal not found'));
    render(<DealDetail />);

    expect(await screen.findByText('Deal not found')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Back to deals' }));
    expect(mockNavigate).toHaveBeenCalledWith('/deals');
  });
});
