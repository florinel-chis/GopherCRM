import { describe, it, expect, vi, beforeEach } from 'vitest';
import { act, render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { automationKeys } from './automationQueries';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { AutomationRunsSection } from './AutomationRunsSection';
import { Component as LeadDetail } from '@/pages/leads/LeadDetail';
import { Component as DealDetail } from '@/pages/deals/DealDetail';
import { automationApi, dealsApi, leadsApi, quotesApi } from '@/api/endpoints';
import { createMockAutomationRun, createMockDeal, createMockLead, createMockUser } from '@/test/factories';
import { useNavigate, useParams } from 'react-router-dom';
import { formatDate } from '@/utils/date';
import type { User } from '@/types';
import { AUTOMATION_DATETIME } from './automationFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    listRuns: vi.fn(),
  },
  leadsApi: {
    getLead: vi.fn(),
    deleteLead: vi.fn(),
    convertLead: vi.fn(),
  },
  dealsApi: {
    getDeal: vi.fn(),
    getDealHistory: vi.fn(),
    changeStage: vi.fn(),
    deleteDeal: vi.fn(),
  },
  quotesApi: {
    getDealQuotes: vi.fn(),
  },
  customersApi: {
    getCustomers: vi.fn(),
    getCustomer: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
  },
}));

vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess: vi.fn(), showError: vi.fn() }),
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

const asRole = (role: User['role']) => mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role })));

describe('AutomationRunsSection', () => {
  const waiting = createMockAutomationRun({
    id: 9,
    status: 'waiting',
    rule_id: 3,
    rule_version: 2,
    entity_type: 'lead',
    entity_id: 5,
    resume_at: '2026-11-01T08:00:00Z',
    created_at: '2026-10-28T09:00:00Z',
  });
  const failed = createMockAutomationRun({
    id: 10,
    status: 'failed',
    rule_id: 4,
    rule_version: 1,
    entity_type: 'lead',
    entity_id: 5,
    error_code: 'step_failed',
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    asRole('admin');
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [waiting, failed], total: 7 });
  });

  it('lists the record runs through entity_type and entity_id', async () => {
    render(<AutomationRunsSection entityType="lead" entityId={5} />);

    const region = await screen.findByRole('region', { name: 'Automation (7)' });
    expect(automationApi.listRuns).toHaveBeenCalledWith({ entity_type: 'lead', entity_id: 5, offset: 0, limit: 5 });

    const rows = within(region).getAllByTestId('record-run-row');
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByRole('link', { name: '#9' })).toHaveAttribute('href', '/automation/runs/9');
    expect(within(rows[0]).getByText('Rule #3 · v2')).toBeInTheDocument();
    expect(within(rows[0]).getByTestId('run-status-chip')).toHaveTextContent('Waiting');
    expect(within(rows[0]).getByText(formatDate(waiting.resume_at, AUTOMATION_DATETIME))).toBeInTheDocument();
    expect(within(rows[0]).getByText(formatDate(waiting.created_at, AUTOMATION_DATETIME))).toBeInTheDocument();
    expect(within(rows[1]).getByTestId('run-status-chip')).toHaveTextContent('Failed');
    expect(within(rows[1]).getByText('—')).toBeInTheDocument();

    // Server-side paging through the same filters.
    fireEvent.click(within(region).getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith({ entity_type: 'lead', entity_id: 5, offset: 5, limit: 5 })
    );
  });

  it('shows the empty text for a record without runs', async () => {
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [], total: 0 });
    render(<AutomationRunsSection entityType="deal" entityId={9} />);

    expect(await screen.findByText('No automation runs for this deal')).toBeInTheDocument();
    expect(automationApi.listRuns).toHaveBeenCalledWith({ entity_type: 'deal', entity_id: 9, offset: 0, limit: 5 });
  });

  it('shows the server message and a Retry button when the runs cannot be loaded', async () => {
    vi.mocked(automationApi.listRuns).mockRejectedValueOnce(
      new AxiosError('Request failed with status code 500', 'ERR_BAD_RESPONSE', undefined, undefined, {
        status: 500,
        statusText: 'Internal Server Error',
        headers: {},
        config: {} as InternalAxiosRequestConfig,
        data: { code: 'INTERNAL_ERROR', message: 'database unavailable', details: null },
      } as AxiosResponse)
    );
    render(<AutomationRunsSection entityType="lead" entityId={5} />);

    const region = await screen.findByRole('region', { name: /^Automation/ });
    expect(await within(region).findByRole('alert')).toHaveTextContent('database unavailable');
    expect(within(region).queryByText(/Loading runs/)).not.toBeInTheDocument();
    expect(within(region).queryByText('No automation runs for this lead')).not.toBeInTheDocument();

    fireEvent.click(within(region).getByRole('button', { name: 'Retry' }));
    expect(await within(region).findAllByTestId('record-run-row')).toHaveLength(2);
    expect(automationApi.listRuns).toHaveBeenCalledTimes(2);
  });

  it('keeps the loaded runs and shows the alert when a refetch fails', async () => {
    const client = createProductionQueryClient();
    render(withClient(client, <AutomationRunsSection entityType="lead" entityId={5} />));
    const region = await screen.findByRole('region', { name: /^Automation/ });
    await within(region).findAllByTestId('record-run-row');

    vi.mocked(automationApi.listRuns).mockRejectedValueOnce(
      new AxiosError('Request failed with status code 500', 'ERR_BAD_RESPONSE', undefined, undefined, {
        status: 500,
        statusText: 'Internal Server Error',
        headers: {},
        config: {} as InternalAxiosRequestConfig,
        data: { code: 'INTERNAL_ERROR', message: 'database unavailable', details: null },
      } as AxiosResponse)
    );
    await act(async () => {
      await client.refetchQueries({ queryKey: automationKeys.runs });
    });

    expect(await within(region).findByRole('alert')).toHaveTextContent('database unavailable');
    expect(within(region).getAllByTestId('record-run-row')).toHaveLength(2);
    expect(within(region).getByRole('button', { name: /next page/i })).toBeInTheDocument();
  });

  it('renders nothing for support and customer users', async () => {
    for (const role of ['support', 'customer'] as const) {
      asRole(role);
      const view = render(<AutomationRunsSection entityType="lead" entityId={5} />);
      expect(screen.queryByRole('region', { name: /^Automation/ })).not.toBeInTheDocument();
      view.unmount();
    }
    // Sales sees the section (the API narrows it to its own records).
    asRole('sales');
    render(<AutomationRunsSection entityType="lead" entityId={5} />);
    expect(await screen.findByRole('region', { name: /^Automation/ })).toBeInTheDocument();
    expect(automationApi.listRuns).toHaveBeenCalledTimes(1);
  });

  it('links View all to the filtered run list', async () => {
    render(<AutomationRunsSection entityType="deal" entityId={9} />);

    const region = await screen.findByRole('region', { name: /^Automation/ });
    expect(within(region).getByRole('link', { name: 'View all' })).toHaveAttribute(
      'href',
      '/automation/runs?entity_type=deal&entity_id=9'
    );
  });
});

describe('AutomationRunsSection on the detail pages', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(vi.fn());
    asRole('admin');
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [], total: 0 });
  });

  it('LeadDetail renders the section', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '5' });
    vi.mocked(leadsApi.getLead).mockResolvedValue(createMockLead({ id: 5 }));
    render(<LeadDetail />);

    const region = await screen.findByRole('region', { name: /^Automation/ });
    expect(await within(region).findByText('No automation runs for this lead')).toBeInTheDocument();
    expect(automationApi.listRuns).toHaveBeenCalledWith(expect.objectContaining({ entity_type: 'lead', entity_id: 5 }));
  });

  it('DealDetail renders the section', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '9' });
    vi.mocked(dealsApi.getDeal).mockResolvedValue(createMockDeal({ id: 9, title: 'Website redesign' }));
    vi.mocked(dealsApi.getDealHistory).mockResolvedValue([]);
    vi.mocked(quotesApi.getDealQuotes).mockResolvedValue({ quotes: [], total: 0 });
    render(<DealDetail />);

    const region = await screen.findByRole('region', { name: /^Automation/ });
    expect(await within(region).findByText('No automation runs for this deal')).toBeInTheDocument();
    expect(automationApi.listRuns).toHaveBeenCalledWith(expect.objectContaining({ entity_type: 'deal', entity_id: 9 }));
  });
});
