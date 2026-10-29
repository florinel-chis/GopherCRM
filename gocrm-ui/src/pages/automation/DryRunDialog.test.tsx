import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { automationApi, dealsApi, leadsApi } from '@/api/endpoints';
import {
  createMockAutomationRule,
  createMockDeal,
  createMockDryRunReport,
  createMockLead,
} from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { formatDate } from '@/utils/date';
import { AUTOMATION_DATETIME } from './automationFormat';
import { DryRunDialog } from './DryRunDialog';

vi.mock('@/api/endpoints', () => ({
  automationApi: { dryRun: vi.fn() },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
}));

const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Error',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const leadRule = createMockAutomationRule({ id: 4, name: 'Welcome new leads', entity_type: 'lead', version: 3 });
const dealRule = createMockAutomationRule({
  id: 6,
  name: 'Chase proposals',
  entity_type: 'deal',
  trigger_type: 'deal_stage_changed',
});
const lead = createMockLead({ id: 5, contact_name: 'Ion Ionescu', company_name: 'Bolt Robotics' });

const report = createMockDryRunReport({
  rule_id: 4,
  rule_version: 3,
  entity_id: 5,
  simulated_start: '2026-10-28T09:00:00Z',
  simulated_end: '2026-10-30T09:00:00Z',
  steps: [
    {
      position: 1,
      step_type: 'wait',
      outcome: 'waiting',
      simulated_at: '2026-10-28T09:00:00Z',
      details: { resume_at: '2026-10-30T09:00:00Z' },
    },
    {
      position: 2,
      step_type: 'condition',
      outcome: 'succeeded',
      simulated_at: '2026-10-30T09:00:00Z',
      details: { result: true, branch: 'next' },
    },
    {
      position: 3,
      step_type: 'notify',
      outcome: 'skipped',
      error_code: 'recipient_unavailable',
      simulated_at: '2026-10-30T09:00:00Z',
      details: { recipient_id: 2 },
    },
  ],
});

const dialog = () => screen.getByRole('dialog', { name: /^Dry run/ });
const runButton = () => within(dialog()).getByRole('button', { name: 'Run dry run' });

describe('DryRunDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(leadsApi.getLeads).mockResolvedValue({ data: [lead], total: 1, page: 1, limit: 20, total_pages: 1 });
  });

  it('a mocked report renders every step row and the no-side-effects notice', async () => {
    vi.mocked(automationApi.dryRun).mockResolvedValue(report);
    render(<DryRunDialog rule={leadRule} open onClose={vi.fn()} />);

    expect(screen.getByRole('dialog', { name: 'Dry run: Welcome new leads' })).toBeInTheDocument();
    expect(within(dialog()).getByTestId('dry-run-notice')).toHaveTextContent(
      'A dry run writes, sends and notifies nothing: no task, email, notification, owner change or run is created.',
    );
    expect(runButton()).toBeDisabled();

    await pickAutocompleteOption('Lead', 'Ion Ionescu (Bolt Robotics)');
    fireEvent.click(runButton());

    await waitFor(() => expect(within(dialog()).getAllByTestId('dry-run-step')).toHaveLength(3));
    expect(automationApi.dryRun).toHaveBeenCalledWith(4, { entity_type: 'lead', entity_id: 5 });

    const [wait, condition, notify] = within(dialog()).getAllByTestId('dry-run-step');
    expect(wait).toHaveTextContent('#1');
    expect(wait).toHaveTextContent('Wait');
    expect(within(wait).getByTestId('outcome-chip')).toHaveTextContent('Waiting');
    expect(wait).toHaveTextContent(`Resumes: ${formatDate('2026-10-30T09:00:00Z', AUTOMATION_DATETIME)}`);
    expect(within(condition).getByTestId('outcome-chip')).toHaveTextContent('Succeeded');
    expect(condition).toHaveTextContent('Branch: Continue');
    expect(within(notify).getByTestId('outcome-chip')).toHaveTextContent('Skipped');
    expect(notify).toHaveTextContent('Recipient unavailable');
    expect(notify).toHaveTextContent(formatDate('2026-10-30T09:00:00Z', AUTOMATION_DATETIME));
    expect(within(dialog()).getByText(
      `Simulated from ${formatDate('2026-10-28T09:00:00Z', AUTOMATION_DATETIME)} to ${formatDate(
        '2026-10-30T09:00:00Z',
        AUTOMATION_DATETIME,
      )} · rule version 3`,
    )).toBeInTheDocument();
    expect(within(dialog()).getByTestId('dry-run-notice')).toBeInTheDocument();
  });

  it('fixes the record type to the rule entity type and offers deals of every stage', async () => {
    const won = createMockDeal({ id: 21, title: 'Closed big one', stage: 'won' });
    const open = createMockDeal({ id: 22, title: 'Still talking', stage: 'proposal' });
    vi.mocked(dealsApi.getDeals).mockResolvedValue({ deals: [won, open], total: 2 });
    vi.mocked(automationApi.dryRun).mockResolvedValue(
      createMockDryRunReport({ rule_id: 6, entity_type: 'deal', entity_id: 21, steps: [] }),
    );
    render(<DryRunDialog rule={dealRule} open onClose={vi.fn()} />);

    const recordType = within(dialog()).getByLabelText('Record type');
    expect(recordType).toHaveValue('Deal');
    expect(recordType).toHaveAttribute('readonly');
    expect(screen.queryByLabelText('Lead')).not.toBeInTheDocument();

    await pickAutocompleteOption('Deal', 'Closed big one (Won)');
    expect(dealsApi.getDeals).toHaveBeenCalledWith({ search: undefined, limit: 20 });

    fireEvent.click(runButton());
    await waitFor(() => expect(automationApi.dryRun).toHaveBeenCalledWith(6, { entity_type: 'deal', entity_id: 21 }));
    expect(await within(dialog()).findByText('No step would run.')).toBeInTheDocument();
  });

  it('a 404 shows the error inline', async () => {
    vi.mocked(automationApi.dryRun)
      .mockResolvedValueOnce(report)
      .mockRejectedValueOnce(apiError(404, 'NOT_FOUND', 'lead 5 not found'));
    render(<DryRunDialog rule={leadRule} open onClose={vi.fn()} />);
    await pickAutocompleteOption('Lead', 'Ion Ionescu (Bolt Robotics)');

    fireEvent.click(runButton());
    await waitFor(() => expect(within(dialog()).getAllByTestId('dry-run-step')).toHaveLength(3));

    fireEvent.click(runButton());
    expect(await within(dialog()).findByTestId('dry-run-error')).toHaveTextContent('lead 5 not found');
    expect(within(dialog()).queryAllByTestId('dry-run-step')).toHaveLength(0);
  });

  it('a 400 shows the error inline', async () => {
    vi.mocked(automationApi.dryRun).mockRejectedValue(
      apiError(400, 'INVALID_INPUT', 'rule 4 is a lead rule, not a deal rule: validation failed'),
    );
    const onClose = vi.fn();
    render(<DryRunDialog rule={leadRule} open onClose={onClose} />);
    await pickAutocompleteOption('Lead', 'Ion Ionescu (Bolt Robotics)');

    fireEvent.click(runButton());
    expect(await within(dialog()).findByTestId('dry-run-error')).toHaveTextContent(
      'rule 4 is a lead rule, not a deal rule: validation failed',
    );

    fireEvent.click(within(dialog()).getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalled();
  });
});
