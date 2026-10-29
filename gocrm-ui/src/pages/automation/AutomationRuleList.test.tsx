import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render as plainRender } from '@testing-library/react';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { MemoryRouter, Outlet, Route, Routes, useNavigate } from 'react-router-dom';
import { QueryClientProvider } from '@tanstack/react-query';
import { automationApi, configurationsApi, dealsApi } from '@/api/endpoints';
import { ProtectedRoute } from '@/components/ProtectedRoute';
import { createMockAutomationRule, createMockDeal, createMockDryRunReport, createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { formatDate } from '@/utils/date';
import type { User } from '@/types';
import { AUTOMATION_DATETIME } from './automationFormat';
import { afterRuleCreated } from './automationQueries';
import { Component as AutomationRuleList } from './AutomationRuleList';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    listRules: vi.fn(),
    enableRule: vi.fn(),
    disableRule: vi.fn(),
    deleteRule: vi.fn(),
    dryRun: vi.fn(),
    dryRunDraft: vi.fn(),
  },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const showSuccess = vi.fn();
const showError = vi.fn();
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
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_RESPONSE', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const rowOf = async (text: string) => {
  const cell = await screen.findByText(text);
  return cell.closest('tr') as HTMLElement;
};

const welcome = createMockAutomationRule({
  id: 3,
  name: 'Welcome new leads',
  entity_type: 'lead',
  trigger_type: 'lead_status_changed',
  trigger: { hour_utc: 0, to: 'qualified' },
  enabled: false,
  version: 2,
  updated_at: '2026-10-28T08:30:00Z',
});
const chase = createMockAutomationRule({
  id: 5,
  name: 'Chase proposals',
  entity_type: 'deal',
  trigger_type: 'schedule',
  trigger: { hour_utc: 8 },
  enabled: true,
  version: 4,
});

describe('AutomationRuleList', () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'admin' })));
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [welcome, chase], total: 2 });
  });

  it('renders name, entity, trigger, enabled switch, version and updated time from a mocked list response', async () => {
    render(<AutomationRuleList />);

    const first = await rowOf('Welcome new leads');
    expect(screen.getByRole('heading', { name: 'Automation rules' })).toBeInTheDocument();
    expect(within(first).getByText('Lead')).toBeInTheDocument();
    expect(within(first).getByText('Lead status changed → qualified')).toBeInTheDocument();
    expect(within(first).getByLabelText('Welcome new leads enabled')).not.toBeChecked();
    expect(within(first).getByText('2')).toBeInTheDocument();
    expect(within(first).getByText(formatDate('2026-10-28T08:30:00Z', AUTOMATION_DATETIME))).toBeInTheDocument();

    const second = await rowOf('Chase proposals');
    expect(within(second).getByText('Deal')).toBeInTheDocument();
    expect(within(second).getByText('Schedule, daily at 08:00 UTC')).toBeInTheDocument();
    expect(within(second).getByLabelText('Chase proposals enabled')).toBeChecked();

    expect(automationApi.listRules).toHaveBeenCalledWith({
      offset: 0,
      limit: 10,
      sort_by: undefined,
      sort_order: undefined,
    });
    fireEvent.click(screen.getByRole('button', { name: 'New rule' }));
    expect(mockNavigate).toHaveBeenCalledWith('/automation/rules/new');
  });

  it('pages with Next and page size: offset follows the zero-based table page and a size change resets to the first page', async () => {
    const page1 = Array.from({ length: 10 }, (_, i) => createMockAutomationRule({ id: i + 1, name: `Page one rule ${i + 1}` }));
    const page2 = Array.from({ length: 10 }, (_, i) => createMockAutomationRule({ id: i + 11, name: `Page two rule ${i + 1}` }));
    vi.mocked(automationApi.listRules).mockImplementation(async (filters) => ({
      rules: (filters?.offset ?? 0) === 10 ? page2 : page1,
      total: 25,
    }));
    render(<AutomationRuleList />);
    await rowOf('Page one rule 1');

    fireEvent.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() =>
      expect(automationApi.listRules).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 10, limit: 10 })),
    );
    expect(await screen.findByText('Page two rule 1')).toBeInTheDocument();

    fireEvent.mouseDown(screen.getByRole('combobox', { name: /rows per page/i }));
    fireEvent.click(await screen.findByRole('option', { name: '25' }));
    await waitFor(() =>
      expect(automationApi.listRules).toHaveBeenLastCalledWith(expect.objectContaining({ offset: 0, limit: 25 })),
    );
    expect(await screen.findByText('Page one rule 1')).toBeInTheDocument();
  });

  it('sorts server-side on the allowed columns and never on Tools', async () => {
    render(<AutomationRuleList />);
    await rowOf('Welcome new leads');

    const expected: Array<[RegExp, string]> = [
      [/^Name/, 'name'],
      [/^Entity/, 'entity_type'],
      [/^Trigger/, 'trigger_type'],
      [/^Enabled/, 'enabled'],
      [/^Version/, 'version'],
      [/^Updated/, 'updated_at'],
    ];
    for (const [label, column] of expected) {
      fireEvent.click(await screen.findByRole('button', { name: label }));
      await waitFor(() =>
        expect(automationApi.listRules).toHaveBeenLastCalledWith(
          expect.objectContaining({ sort_by: column, sort_order: 'asc', offset: 0 }),
        ),
      );
      await rowOf('Welcome new leads');
    }

    fireEvent.click(screen.getByRole('button', { name: /^Updated/ }));
    await waitFor(() =>
      expect(automationApi.listRules).toHaveBeenLastCalledWith(
        expect.objectContaining({ sort_by: 'updated_at', sort_order: 'desc' }),
      ),
    );
    await rowOf('Welcome new leads');
    expect(screen.getByRole('columnheader', { name: 'Tools' })).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Tools/ })).not.toBeInTheDocument();
  });

  it('enabling calls the enable endpoint and refreshes the list', async () => {
    vi.mocked(automationApi.enableRule).mockResolvedValue({ ...welcome, enabled: true });
    render(<AutomationRuleList />);
    const row = await rowOf('Welcome new leads');
    expect(automationApi.listRules).toHaveBeenCalledTimes(1);

    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [{ ...welcome, enabled: true }, chase], total: 2 });
    fireEvent.click(within(row).getByLabelText('Welcome new leads enabled'));

    await waitFor(() => expect(automationApi.enableRule).toHaveBeenCalledWith(3));
    await waitFor(() => expect(automationApi.listRules).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByLabelText('Welcome new leads enabled')).toBeChecked());
    expect(showSuccess).toHaveBeenCalledWith('Rule enabled');
    expect(screen.queryByRole('dialog', { name: 'Disable rule' })).not.toBeInTheDocument();
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('disabling asks for confirmation, then calls the disable endpoint and refreshes the list', async () => {
    vi.mocked(automationApi.disableRule).mockResolvedValue({ ...chase, enabled: false });
    render(<AutomationRuleList />);
    const row = await rowOf('Chase proposals');

    fireEvent.click(within(row).getByLabelText('Chase proposals enabled'));
    const confirm = await screen.findByRole('dialog', { name: 'Disable rule' });
    expect(confirm).toHaveTextContent('Disabling "Chase proposals" cancels its pending, waiting and running runs.');
    expect(automationApi.disableRule).not.toHaveBeenCalled();

    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [welcome, { ...chase, enabled: false }], total: 2 });
    fireEvent.click(within(confirm).getByRole('button', { name: 'Disable' }));

    await waitFor(() => expect(automationApi.disableRule).toHaveBeenCalledWith(5));
    await waitFor(() => expect(automationApi.listRules).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(screen.getByLabelText('Chase proposals enabled')).not.toBeChecked());
    expect(showSuccess).toHaveBeenCalledWith('Rule disabled');
  });

  it('deleting asks for confirmation, then calls the delete endpoint and refreshes the list', async () => {
    vi.mocked(automationApi.deleteRule).mockResolvedValue(undefined);
    render(<AutomationRuleList />);
    const row = await rowOf('Welcome new leads');

    fireEvent.click(within(row).getByRole('button', { name: 'Delete rule' }));
    const confirm = await screen.findByRole('dialog', { name: 'Delete rule' });
    expect(confirm).toHaveTextContent(
      'Deleting "Welcome new leads" cancels its pending, waiting and running runs. Runs already recorded stay.',
    );
    expect(mockNavigate).not.toHaveBeenCalled();

    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [chase], total: 1 });
    fireEvent.click(within(confirm).getByRole('button', { name: 'Delete' }));

    await waitFor(() => expect(automationApi.deleteRule).toHaveBeenCalledWith(3));
    await waitFor(() => expect(screen.queryByText('Welcome new leads')).not.toBeInTheDocument());
    expect(showSuccess).toHaveBeenCalledWith('Rule deleted');
  });

  it('opens the dry-run dialog from the Tools column without opening the editor', async () => {
    render(<AutomationRuleList />);
    const row = await rowOf('Chase proposals');

    fireEvent.click(within(row).getByRole('button', { name: 'Dry run' }));

    const dialog = await screen.findByRole('dialog', { name: 'Dry run: Chase proposals' });
    expect(within(dialog).getByLabelText('Record type')).toHaveValue('Deal');
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it("the list's dry run simulates the saved version", async () => {
    const deal = createMockDeal({ id: 21, title: 'Closed big one', stage: 'won' });
    vi.mocked(dealsApi.getDeals).mockResolvedValue({ deals: [deal], total: 1 });
    vi.mocked(automationApi.dryRun).mockResolvedValue(
      createMockDryRunReport({ rule_id: 7, entity_type: 'deal', entity_id: 21, steps: [] }),
    );
    render(<AutomationRuleList />);
    const row = await rowOf('Chase proposals');
    fireEvent.click(within(row).getByRole('button', { name: 'Dry run' }));
    await screen.findByRole('dialog', { name: 'Dry run: Chase proposals' });

    await pickAutocompleteOption('Deal', 'Closed big one (Won)');
    fireEvent.click(screen.getByRole('button', { name: 'Run dry run' }));

    await waitFor(() =>
      expect(automationApi.dryRun).toHaveBeenCalledWith(5, { entity_type: 'deal', entity_id: 21 }),
    );
    expect(automationApi.dryRunDraft).not.toHaveBeenCalled();
  });

  it('opens the editor from a row click and from the table open button', async () => {
    render(<AutomationRuleList />);
    const row = await rowOf('Welcome new leads');

    fireEvent.click(within(row).getByText('Lead status changed → qualified'));
    expect(mockNavigate).toHaveBeenLastCalledWith('/automation/rules/3/edit');
    expect(mockNavigate).toHaveBeenCalledTimes(1);

    // The table's own unlabelled open button in its Actions cell.
    const cells = within(row).getAllByRole('cell');
    fireEvent.click(within(cells[cells.length - 1]).getByRole('button'));
    expect(mockNavigate).toHaveBeenCalledTimes(2);
    expect(mockNavigate).toHaveBeenLastCalledWith('/automation/rules/3/edit');
  });

  it('shows a newly created rule when returning to a list cached under production defaults', async () => {
    const client = createProductionQueryClient();
    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [welcome], total: 1 });
    const { unmount } = render(withClient(client, <AutomationRuleList />));
    await rowOf('Welcome new leads');
    unmount();

    const created = createMockAutomationRule({ id: 9, name: 'Brand new rule' });
    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [welcome, created], total: 2 });
    await afterRuleCreated(client, created);

    render(withClient(client, <AutomationRuleList />));
    expect(await screen.findByText('Brand new rule')).toBeInTheDocument();
    expect(automationApi.listRules).toHaveBeenCalledTimes(2);
  });

  it('a sales user on the route renders the unauthorized redirect', async () => {
    mockUseAuth.mockReturnValue(authState(createMockUser({ role: 'sales' })));
    plainRender(
      <QueryClientProvider client={createProductionQueryClient()}>
        <MemoryRouter initialEntries={['/automation/rules']}>
          <Routes>
            <Route path="/unauthorized" element={<div>Unauthorized Page</div>} />
            <Route
              element={
                <ProtectedRoute requiredRole="admin">
                  <Outlet />
                </ProtectedRoute>
              }
            >
              <Route path="/automation/rules" Component={AutomationRuleList} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    expect(await screen.findByText('Unauthorized Page')).toBeInTheDocument();
    expect(screen.queryByRole('heading', { name: 'Automation rules' })).not.toBeInTheDocument();
    expect(automationApi.listRules).not.toHaveBeenCalled();
  });

  it('a failed list read shows the server message and a Retry that refetches', async () => {
    vi.mocked(automationApi.listRules)
      .mockRejectedValueOnce(apiError(500, 'INTERNAL_ERROR', 'Database unavailable'))
      .mockResolvedValueOnce({ rules: [welcome], total: 1 });
    render(<AutomationRuleList />);

    const alert = await screen.findByTestId('rule-list-error');
    expect(alert).toHaveTextContent('Database unavailable');
    expect(screen.queryByText('No automation rules yet.')).not.toBeInTheDocument();

    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByText('Welcome new leads')).toBeInTheDocument();
    expect(screen.queryByTestId('rule-list-error')).not.toBeInTheDocument();
    expect(automationApi.listRules).toHaveBeenCalledTimes(2);
  });

  it('an empty list says there are no rules', async () => {
    vi.mocked(automationApi.listRules).mockResolvedValue({ rules: [], total: 0 });
    render(<AutomationRuleList />);

    expect(await screen.findByText('No automation rules yet.')).toBeInTheDocument();
    expect(screen.queryByTestId('rule-list-error')).not.toBeInTheDocument();
  });

  it('offers a named Edit control per rule in the Tools column', async () => {
    render(<AutomationRuleList />);
    const row = await rowOf('Chase proposals');

    fireEvent.click(within(row).getByRole('button', { name: 'Edit Chase proposals' }));
    expect(mockNavigate).toHaveBeenCalledTimes(1);
    expect(mockNavigate).toHaveBeenCalledWith('/automation/rules/5/edit');
    expect(screen.getByRole('button', { name: 'Edit Welcome new leads' })).toBeInTheDocument();
  });
});
