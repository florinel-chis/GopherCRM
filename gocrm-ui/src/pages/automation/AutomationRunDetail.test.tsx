import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as AutomationRunDetail } from './AutomationRunDetail';
import { Component as AutomationRunList } from './AutomationRunList';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { automationApi } from '@/api/endpoints';
import { createMockAutomationRun, createMockAutomationRunStep, createMockUser } from '@/test/factories';
import { useParams } from 'react-router-dom';
import { formatDate } from '@/utils/date';
import type { AutomationRunStatus, User } from '@/types';
import { AUTOMATION_DATETIME } from './automationFormat';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    getRun: vi.fn(),
    cancelRun: vi.fn(),
    listRuns: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
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
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

describe('AutomationRunDetail', () => {
  const notifyStep = createMockAutomationRunStep({
    id: 1,
    run_id: 9,
    position: 1,
    step_type: 'notify',
    outcome: 'succeeded',
    executed_at: '2026-10-28T09:00:00Z',
    details: { recipient_id: 3, notification_id: 11 },
  });
  const emailStep = createMockAutomationRunStep({
    id: 2,
    run_id: 9,
    position: 2,
    step_type: 'send_email',
    outcome: 'failed',
    error_code: 'mail_failed',
    executed_at: '2026-10-28T09:01:00Z',
    details: { recipient_id: 3 },
  });
  const waitStep = createMockAutomationRunStep({
    id: 3,
    run_id: 9,
    position: 3,
    step_type: 'wait',
    outcome: 'waiting',
    executed_at: '2026-10-28T09:02:00Z',
    details: { resume_at: '2026-11-01T08:00:00Z' },
  });
  const run = createMockAutomationRun({
    id: 9,
    status: 'waiting',
    rule_id: 3,
    rule_version: 2,
    entity_type: 'deal',
    entity_id: 7,
    next_position: 4,
    attempts: 1,
    resume_at: '2026-11-01T08:00:00Z',
    steps: [notifyStep, emailStep, waitStep],
  });

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useParams).mockReturnValue({ id: '9' });
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(automationApi.getRun).mockResolvedValue(run);
  });

  it('the detail shows step rows with outcome, time and details', async () => {
    render(<AutomationRunDetail />);

    expect(await screen.findByRole('heading', { name: 'Run #9' })).toBeInTheDocument();
    expect(automationApi.getRun).toHaveBeenCalledWith(9);
    expect(screen.getAllByTestId('run-status-chip')[0]).toHaveTextContent('Waiting');
    expect(screen.getByRole('link', { name: 'Rule #3 · version 2' })).toHaveAttribute(
      'href',
      '/automation/rules/3/edit'
    );
    expect(screen.getByRole('link', { name: 'Deal #7' })).toHaveAttribute('href', '/deals/7');

    const log = screen.getByRole('table', { name: 'Step log' });
    const rows = within(log).getAllByTestId('run-step-row');
    expect(rows).toHaveLength(3);

    expect(within(rows[0]).getByText('#1')).toBeInTheDocument();
    expect(within(rows[0]).getByText('Notify')).toBeInTheDocument();
    expect(within(rows[0]).getByTestId('outcome-chip')).toHaveTextContent('Succeeded');
    expect(within(rows[0]).getByText(formatDate(notifyStep.executed_at, AUTOMATION_DATETIME))).toBeInTheDocument();
    expect(within(rows[0]).getByText('Recipient:')).toBeInTheDocument();
    expect(within(rows[0]).getByText('User #3')).toBeInTheDocument();
    expect(within(rows[0]).getByText('Notification:')).toBeInTheDocument();

    expect(within(rows[1]).getByText('Send email')).toBeInTheDocument();
    expect(within(rows[1]).getByTestId('outcome-chip')).toHaveTextContent('Failed');
    expect(within(rows[1]).getByText('Mail failed')).toBeInTheDocument();

    expect(within(rows[2]).getByText('Wait')).toBeInTheDocument();
    expect(within(rows[2]).getByTestId('outcome-chip')).toHaveTextContent('Waiting');
    expect(within(rows[2]).getByText('Resumes:')).toBeInTheDocument();
  });

  it('Cancel is visible for admin only on pending, waiting and running runs', async () => {
    const statuses: AutomationRunStatus[] = ['pending', 'running', 'waiting', 'completed', 'failed', 'cancelled'];
    for (const status of statuses) {
      vi.mocked(automationApi.getRun).mockResolvedValue({ ...run, status });
      const view = render(<AutomationRunDetail />);
      await screen.findByRole('heading', { name: 'Run #9' });
      const button = screen.queryByRole('button', { name: 'Cancel run' });
      if (status === 'pending' || status === 'running' || status === 'waiting') {
        expect(button, status).toBeInTheDocument();
      } else {
        expect(button, status).not.toBeInTheDocument();
      }
      view.unmount();
    }

    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    vi.mocked(automationApi.getRun).mockResolvedValue({ ...run, status: 'waiting' });
    render(<AutomationRunDetail />);
    await screen.findByRole('heading', { name: 'Run #9' });
    expect(screen.queryByRole('button', { name: 'Cancel run' })).not.toBeInTheDocument();
    // Sales sees the rule as plain text: the rule editor is admin-only.
    expect(screen.queryByRole('link', { name: 'Rule #3 · version 2' })).not.toBeInTheDocument();
    expect(screen.getByText('Rule #3 · version 2')).toBeInTheDocument();
  });

  it('cancels after confirmation and keeps the step log from the cancel response', async () => {
    vi.mocked(automationApi.getRun).mockResolvedValue({ ...run, steps: [notifyStep] });
    vi.mocked(automationApi.cancelRun).mockResolvedValue({
      ...run,
      status: 'cancelled',
      cancel_reason: 'manual',
      finished_at: '2026-10-29T12:00:00Z',
      steps: [notifyStep, emailStep],
    });
    render(<AutomationRunDetail />);
    await screen.findByRole('heading', { name: 'Run #9' });
    expect(screen.getAllByTestId('run-step-row')).toHaveLength(1);

    fireEvent.click(screen.getByRole('button', { name: 'Cancel run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel run' });
    expect(within(dialog).getByText('The run stops before its next step. This cannot be undone.')).toBeInTheDocument();
    expect(automationApi.cancelRun).not.toHaveBeenCalled();
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel run' }));

    await waitFor(() => expect(automationApi.cancelRun).toHaveBeenCalledWith(9));
    await waitFor(() => expect(showSuccess).toHaveBeenCalledWith('Run cancelled'));
    await waitFor(() => expect(screen.getAllByTestId('run-status-chip')[0]).toHaveTextContent('Cancelled'));
    expect(screen.getAllByTestId('run-step-row')).toHaveLength(2);
    expect(screen.getByText('Cancelled manually')).toBeInTheDocument();
    // Once the confirmation has closed, no Cancel button is left.
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel run' })).not.toBeInTheDocument());
    // The stored detail came from the cancel response, not a refetch.
    expect(automationApi.getRun).toHaveBeenCalledTimes(1);
  });

  it('shows the server message when the run finished meanwhile (409)', async () => {
    const message = 'run 9 is completed: invalid status transition';
    vi.mocked(automationApi.cancelRun).mockRejectedValue(apiError(409, 'INVALID_STATUS_TRANSITION', message));
    render(<AutomationRunDetail />);
    await screen.findByRole('heading', { name: 'Run #9' });

    vi.mocked(automationApi.getRun).mockResolvedValue({ ...run, status: 'completed', finished_at: '2026-10-29T11:00:00Z' });
    fireEvent.click(screen.getByRole('button', { name: 'Cancel run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel run' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel run' }));

    await waitFor(() => expect(showError).toHaveBeenCalledWith(message));
    // The run is re-read, so the page shows where it really ended.
    await waitFor(() => expect(automationApi.getRun).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getAllByTestId('run-status-chip')[0]).toHaveTextContent('Completed'));
    await waitFor(() => expect(screen.queryByRole('button', { name: 'Cancel run' })).not.toBeInTheDocument());
  });

  it('refreshes a cached run list after a 409 under production defaults', async () => {
    window.history.replaceState({}, '', '/automation/runs');
    const client = createProductionQueryClient();
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [run], total: 1 });

    // The list was visited first and is cached for five minutes.
    const list = render(withClient(client, <AutomationRunList />));
    const row = (await screen.findByText('#9')).closest('tr') as HTMLElement;
    expect(within(row).getByTestId('run-status-chip')).toHaveTextContent('Waiting');
    list.unmount();

    const finished = { ...run, status: 'completed' as const, finished_at: '2026-10-29T11:00:00Z' };
    vi.mocked(automationApi.cancelRun).mockRejectedValue(
      apiError(409, 'INVALID_STATUS_TRANSITION', 'run 9 is completed: invalid status transition')
    );
    const detail = render(withClient(client, <AutomationRunDetail />));
    await screen.findByRole('heading', { name: 'Run #9' });
    vi.mocked(automationApi.getRun).mockResolvedValue(finished);
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [finished], total: 1 });
    fireEvent.click(screen.getByRole('button', { name: 'Cancel run' }));
    const dialog = await screen.findByRole('dialog', { name: 'Cancel run' });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Cancel run' }));
    await waitFor(() => expect(showError).toHaveBeenCalled());
    await waitFor(() => expect(screen.getAllByTestId('run-status-chip')[0]).toHaveTextContent('Completed'));
    detail.unmount();

    // Back on the list: the conflict marked it stale, so it shows the end state.
    render(withClient(client, <AutomationRunList />));
    await waitFor(async () => {
      const again = (await screen.findByText('#9')).closest('tr') as HTMLElement;
      expect(within(again).getByTestId('run-status-chip')).toHaveTextContent('Completed');
    });
  });

  it('shows the server message and a Retry button when the run cannot be loaded', async () => {
    vi.mocked(automationApi.getRun).mockRejectedValueOnce(apiError(500, 'INTERNAL_ERROR', 'database unavailable'));
    render(<AutomationRunDetail />);

    const alert = await screen.findByRole('alert');
    expect(alert).toHaveTextContent('database unavailable');
    expect(screen.queryByText('Run not found')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('heading', { name: 'Run #9' })).toBeInTheDocument();
    expect(automationApi.getRun).toHaveBeenCalledTimes(2);
  });

  it('shows Run not found on a 404', async () => {
    vi.mocked(automationApi.getRun).mockRejectedValue(apiError(404, 'NOT_FOUND', 'automation run 9 not found'));
    render(<AutomationRunDetail />);

    expect(await screen.findByText('Run not found')).toBeInTheDocument();
    expect(screen.getByRole('link', { name: 'Back to runs' })).toHaveAttribute('href', '/automation/runs');
  });
});
