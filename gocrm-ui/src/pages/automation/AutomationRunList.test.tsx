import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { Component as AutomationRunList } from './AutomationRunList';
import { automationApi } from '@/api/endpoints';
import { createMockAutomationRule, createMockAutomationRun, createMockUser } from '@/test/factories';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { useNavigate, Link as RouterLink } from 'react-router-dom';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { formatDate } from '@/utils/date';
import type { User } from '@/types';
import { AUTOMATION_DATETIME } from './automationFormat';
import { afterRuleChanged } from './automationQueries';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    listRuns: vi.fn(),
    listRules: vi.fn(),
  },
  configurationsApi: {
    getUIConfigurations: vi.fn(),
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

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

const pickSelect = async (name: RegExp, option: string) => {
  fireEvent.mouseDown(screen.getByRole('combobox', { name }));
  fireEvent.click(await screen.findByRole('option', { name: option }));
};

const enterNumber = (label: string, value: string) => {
  const input = screen.getByLabelText(label);
  fireEvent.change(input, { target: { value } });
  fireEvent.keyDown(input, { key: 'Enter' });
};

const serverError = (message: string) =>
  new AxiosError('Request failed with status code 500', 'ERR_BAD_RESPONSE', undefined, undefined, {
    status: 500,
    statusText: 'Internal Server Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code: 'INTERNAL_ERROR', message, details: null },
  } as AxiosResponse);

describe('AutomationRunList', () => {
  const mockNavigate = vi.fn();
  const waiting = createMockAutomationRun({
    id: 9,
    status: 'waiting',
    rule_id: 3,
    rule_version: 2,
    entity_type: 'lead',
    entity_id: 5,
    resume_at: '2026-11-01T08:00:00Z',
    created_at: '2026-10-28T09:00:00Z',
    updated_at: '2026-10-29T10:30:00Z',
  });
  const completed = createMockAutomationRun({
    id: 10,
    status: 'completed',
    rule_id: 4,
    rule_version: 1,
    entity_type: 'deal',
    entity_id: 7,
    finished_at: '2026-10-28T09:05:00Z',
  });

  beforeEach(() => {
    vi.clearAllMocks();
    window.history.replaceState({}, '', '/automation/runs');
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 1, role: 'admin' })));
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [waiting, completed], total: 2 });
    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [], total: 0 });
  });

  it('renders status, rule, record link, resume time and timestamps from a mocked list response', async () => {
    render(<AutomationRunList />);

    expect(screen.getByRole('heading', { name: 'Automation runs' })).toBeInTheDocument();

    const first = await rowOf('#9');
    expect(within(first).getByTestId('run-status-chip')).toHaveTextContent('Waiting');
    expect(within(first).getByText('Rule #3 · v2')).toBeInTheDocument();
    expect(within(first).getByRole('link', { name: 'Lead #5' })).toHaveAttribute('href', '/leads/5');
    expect(within(first).getByText(formatDate(waiting.resume_at, AUTOMATION_DATETIME))).toBeInTheDocument();
    expect(within(first).getByText(formatDate(waiting.created_at, AUTOMATION_DATETIME))).toBeInTheDocument();
    expect(within(first).getByText(formatDate(waiting.updated_at, AUTOMATION_DATETIME))).toBeInTheDocument();

    const second = await rowOf('#10');
    expect(within(second).getByTestId('run-status-chip')).toHaveTextContent('Completed');
    expect(within(second).getByText('Rule #4 · v1')).toBeInTheDocument();
    expect(within(second).getByRole('link', { name: 'Deal #7' })).toHaveAttribute('href', '/deals/7');
    // No resume time on a finished run.
    expect(within(second).getByText('—')).toBeInTheDocument();

    // The record link opens the record, not the run.
    fireEvent.click(within(first).getByRole('link', { name: 'Lead #5' }));
    expect(mockNavigate).not.toHaveBeenCalled();
    fireEvent.click(within(first).getByText('Rule #3 · v2'));
    expect(mockNavigate).toHaveBeenCalledWith('/automation/runs/9');
  });

  it('list filters build the query: status, rule, entity type and record id', async () => {
    render(<AutomationRunList />);
    await rowOf('#9');
    expect(automationApi.listRuns).toHaveBeenCalledWith({
      offset: 0,
      limit: 10,
      status: undefined,
      rule_id: undefined,
      entity_type: undefined,
      entity_id: undefined,
      sort_by: undefined,
      sort_order: undefined,
    });

    // The record id only makes sense once the entity type is known.
    expect(screen.getByLabelText('Record ID')).toBeDisabled();

    await pickSelect(/^Status/, 'Waiting');
    enterNumber('Rule ID', '3');
    await pickSelect(/^Entity/, 'Lead');
    expect(screen.getByLabelText('Record ID')).toBeEnabled();
    const record = screen.getByLabelText('Record ID');
    fireEvent.change(record, { target: { value: '5' } });
    fireEvent.blur(record);

    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith({
        offset: 0,
        limit: 10,
        status: 'waiting',
        rule_id: 3,
        entity_type: 'lead',
        entity_id: 5,
        sort_by: undefined,
        sort_order: undefined,
      })
    );

    // A non-positive rule id is not sent.
    enterNumber('Rule ID', '-2');
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(expect.objectContaining({ rule_id: undefined }))
    );
  });

  it('lets a sales user filter by rule id without requesting the rule list', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ id: 2, role: 'sales' })));
    render(<AutomationRunList />);
    await rowOf('#9');

    expect(screen.queryByLabelText('Rule')).not.toBeInTheDocument();

    enterNumber('Rule ID', '7');
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(expect.objectContaining({ rule_id: 7, offset: 0 }))
    );
    expect(automationApi.listRules).not.toHaveBeenCalled();
  });

  it('lets an admin pick a rule by name from later rule pages', async () => {
    const pageOne = Array.from({ length: 100 }, (_, i) =>
      createMockAutomationRule({ id: i + 1, name: `Rule ${String(i).padStart(3, '0')}` })
    );
    const pageTwo = [
      createMockAutomationRule({ id: 205, name: 'Quarterly check' }),
      ...Array.from({ length: 19 }, (_, i) => createMockAutomationRule({ id: 300 + i, name: `Spare ${i}` })),
    ];
    vi.mocked(automationApi.listRules).mockImplementation(async (filters) =>
      filters?.offset === 100 ? { rules: pageTwo, total: 120 } : { rules: pageOne, total: 120 }
    );
    const ofQuarterly = createMockAutomationRun({ id: 11, rule_id: 205, rule_version: 1, entity_id: 8 });
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [ofQuarterly], total: 1 });

    render(<AutomationRunList />);
    await rowOf('#11');
    // Nothing is fetched before the picker opens; the row falls back to the id.
    expect(automationApi.listRules).not.toHaveBeenCalled();
    expect(screen.getByText('Rule #205 · v1')).toBeInTheDocument();

    await pickAutocompleteOption('Rule', 'Quarterly check (#205)');

    expect(automationApi.listRules).toHaveBeenCalledWith({ offset: 0, limit: 100, sort_by: 'name', sort_order: 'asc' });
    expect(automationApi.listRules).toHaveBeenCalledWith({ offset: 100, limit: 100, sort_by: 'name', sort_order: 'asc' });
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(expect.objectContaining({ rule_id: 205, offset: 0 }))
    );
    expect(screen.getByLabelText('Rule ID')).toHaveValue(205);
    // Names the picker loaded label the rows.
    expect(await screen.findByText('Quarterly check · v1')).toBeInTheDocument();
  });

  it('reads its initial filters from the URL and writes changes back', async () => {
    window.history.replaceState({}, '', '/automation/runs?status=failed&rule_id=4&entity_type=deal&entity_id=7');
    render(<AutomationRunList />);

    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenCalledWith(
        expect.objectContaining({ offset: 0, status: 'failed', rule_id: 4, entity_type: 'deal', entity_id: 7 })
      )
    );
    expect(screen.getByLabelText('Rule ID')).toHaveValue(4);
    expect(screen.getByLabelText('Record ID')).toHaveValue(7);

    await pickSelect(/^Status/, 'Completed');
    await waitFor(() => expect(new URLSearchParams(window.location.search).get('status')).toBe('completed'));
    const params = new URLSearchParams(window.location.search);
    expect(params.get('rule_id')).toBe('4');
    expect(params.get('entity_type')).toBe('deal');
    expect(params.get('entity_id')).toBe('7');

    // Clearing the entity drops the record id from the query and the URL.
    await pickSelect(/^Entity/, 'All');
    await waitFor(() => expect(new URLSearchParams(window.location.search).has('entity_type')).toBe(false));
    expect(new URLSearchParams(window.location.search).has('entity_id')).toBe(false);
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(
        expect.objectContaining({ status: 'completed', rule_id: 4, entity_type: undefined, entity_id: undefined })
      )
    );
  });

  it('follows a navigation to another filter set without remounting', async () => {
    window.history.replaceState({}, '', '/automation/runs?status=failed&rule_id=4');
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [waiting, completed], total: 25 });
    render(
      <>
        <RouterLink to="/automation/runs">All runs</RouterLink>
        <RouterLink to="/automation/runs?entity_type=deal&entity_id=7">Deal 7 runs</RouterLink>
        <AutomationRunList />
      </>
    );
    await rowOf('#9');
    expect(automationApi.listRuns).toHaveBeenLastCalledWith(
      expect.objectContaining({ offset: 0, status: 'failed', rule_id: 4 })
    );
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 10, status: 'failed' }))
    );
    await rowOf('#9');

    // The nav link to the bare list clears every filter and starts at page 1.
    fireEvent.click(screen.getByRole('link', { name: 'All runs' }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith({
        offset: 0,
        limit: 10,
        status: undefined,
        rule_id: undefined,
        entity_type: undefined,
        entity_id: undefined,
        sort_by: undefined,
        sort_order: undefined,
      })
    );
    expect(screen.getByLabelText('Rule ID')).toHaveValue(null);
    expect(screen.getByRole('combobox', { name: /^Status/ })).toHaveTextContent('All statuses');

    fireEvent.click(screen.getByRole('link', { name: 'Deal 7 runs' }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(
        expect.objectContaining({ offset: 0, entity_type: 'deal', entity_id: 7, status: undefined })
      )
    );
    expect(screen.getByLabelText('Record ID')).toHaveValue(7);
  });

  it('gives every row a named link to its run', async () => {
    render(<AutomationRunList />);
    await rowOf('#9');

    expect(screen.getByRole('link', { name: 'Run #9' })).toHaveAttribute('href', '/automation/runs/9');
    expect(screen.getByRole('link', { name: 'Run #10' })).toHaveAttribute('href', '/automation/runs/10');
  });

  it('shows the server message and a Retry button when the runs cannot be loaded', async () => {
    vi.mocked(automationApi.listRuns).mockRejectedValueOnce(serverError('database unavailable'));
    render(<AutomationRunList />);

    expect(await screen.findByRole('alert')).toHaveTextContent('database unavailable');
    expect(screen.queryByRole('table')).not.toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    await rowOf('#9');
    expect(automationApi.listRuns).toHaveBeenCalledTimes(2);
  });

  it('shows why the rule picker is empty and retries it', async () => {
    vi.mocked(automationApi.listRules).mockRejectedValueOnce(serverError('rules unavailable'));
    render(<AutomationRunList />);
    await rowOf('#9');

    const input = screen.getByLabelText('Rule');
    input.focus();
    fireEvent.mouseDown(input);
    // Under the field and in the empty option list.
    expect((await screen.findAllByText(/Rules could not be loaded: rules unavailable/)).length).toBeGreaterThan(0);

    vi.mocked(automationApi.listRules).mockResolvedValue({
      rules: [createMockAutomationRule({ id: 205, name: 'Quarterly check' })],
      total: 1,
    });
    fireEvent.click(screen.getByRole('button', { name: 'Retry loading rules' }));
    await waitFor(() => expect(automationApi.listRules).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.queryAllByText(/rules unavailable/)).toHaveLength(0));
    // The open list now offers the rule.
    fireEvent.click(await screen.findByRole('option', { name: 'Quarterly check (#205)' }));
    await waitFor(() => expect(screen.getByLabelText('Rule ID')).toHaveValue(205));
  });

  it('pages and sorts server-side on the allowed columns only', async () => {
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [waiting, completed], total: 25 });
    render(<AutomationRunList />);
    await rowOf('#9');

    fireEvent.click(screen.getByRole('button', { name: /^Resumes/ }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'resume_at', sort_order: 'asc', offset: 0 })
      )
    );

    await rowOf('#9');
    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(
        expect.objectContaining({ offset: 10, limit: 10, sort_by: 'resume_at' })
      )
    );

    // A filter change goes back to the first page.
    await rowOf('#9');
    await pickSelect(/^Status/, 'Failed');
    await waitFor(() =>
      expect(automationApi.listRuns).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 0, status: 'failed' }))
    );

    expect(screen.getByRole('button', { name: /^Run/ })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: /^Created/ })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Rule/ })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Record/ })).not.toBeInTheDocument();
  });

  it('shows a run cancelled by a rule change when returning to a list cached under production defaults', async () => {
    const client = createProductionQueryClient();
    vi.mocked(automationApi.listRuns).mockResolvedValue({ runs: [waiting], total: 1 });

    const first = render(withClient(client, <AutomationRunList />));
    expect(within(await rowOf('#9')).getByTestId('run-status-chip')).toHaveTextContent('Waiting');
    first.unmount();

    vi.mocked(automationApi.listRuns).mockResolvedValue({
      runs: [{ ...waiting, status: 'cancelled', cancel_reason: 'rule_changed' }],
      total: 1,
    });
    await afterRuleChanged(client, createMockAutomationRule({ id: 3, version: 3 }));

    render(withClient(client, <AutomationRunList />));
    await waitFor(async () =>
      expect(within(await rowOf('#9')).getByTestId('run-status-chip')).toHaveTextContent('Cancelled')
    );
  });
});
