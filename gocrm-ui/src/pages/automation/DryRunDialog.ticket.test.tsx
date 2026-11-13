import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { automationApi, dealsApi, leadsApi, ticketsApi, type AutomationRuleInput } from '@/api/endpoints';
import { createMockAutomationRule, createMockDryRunReport, createMockTicket } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import { DryRunDialog } from './DryRunDialog';

vi.mock('@/api/endpoints', () => ({
  automationApi: { dryRun: vi.fn(), dryRunDraft: vi.fn() },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
  ticketsApi: { getTickets: vi.fn(), getTicket: vi.fn() },
}));

// ticketsApi maps the API's title to subject.
const ticket = createMockTicket({ id: 42, subject: 'Printer jams on floor 2', status: 'in_progress' });
const OPTION = 'T-000042 · Printer jams on floor 2';

const payload: AutomationRuleInput = {
  name: 'Resolved tickets',
  description: '',
  entity_type: 'ticket',
  trigger_type: 'ticket_status_changed',
  trigger: { from: 'open', to: 'resolved' },
  steps: [{ step_type: 'notify', config: { title: 'Ticket {{ticket.number}} resolved', recipient: 'owner' } }],
};

const dialog = () => screen.getByRole('dialog', { name: /^Dry run/ });
const runButton = () => within(dialog()).getByRole('button', { name: 'Run dry run' });

describe('DryRunDialog', () => {
  describe('ticket rules', () => {
    beforeEach(() => {
      vi.clearAllMocks();
      vi.mocked(ticketsApi.getTickets).mockResolvedValue({
        data: [ticket],
        total: 1,
        page: 1,
        limit: 20,
        total_pages: 1,
      });
      vi.mocked(ticketsApi.getTicket).mockResolvedValue(ticket);
    });

    it('a ticket rule dry run searches tickets and sends entity_type ticket', async () => {
      vi.mocked(automationApi.dryRunDraft).mockResolvedValue(
        createMockDryRunReport({ rule_id: 0, rule_version: 0, draft: true, entity_type: 'ticket', entity_id: 42 }),
      );
      render(<DryRunDialog draft={{ name: 'Resolved tickets', entity_type: 'ticket', payload }} open onClose={vi.fn()} />);

      const recordType = within(dialog()).getByLabelText('Record type');
      expect(recordType).toHaveValue('Ticket');
      expect(recordType).toHaveAttribute('readonly');
      expect(within(dialog()).queryByLabelText('Lead')).not.toBeInTheDocument();
      expect(within(dialog()).queryByLabelText('Deal')).not.toBeInTheDocument();

      const input = await pickAutocompleteOption('Ticket', OPTION);
      expect(input).toHaveValue('T-000042 · Printer jams on floor 2');
      expect(ticketsApi.getTickets).toHaveBeenCalledWith({ search: undefined, limit: 20 });
      expect(leadsApi.getLeads).not.toHaveBeenCalled();
      expect(dealsApi.getDeals).not.toHaveBeenCalled();

      fireEvent.click(runButton());
      await waitFor(() =>
        expect(automationApi.dryRunDraft).toHaveBeenCalledWith({ rule: payload, entity_type: 'ticket', entity_id: 42 }),
      );
      expect(automationApi.dryRun).not.toHaveBeenCalled();
      expect(await within(dialog()).findAllByTestId('dry-run-step')).toHaveLength(1);
    });

    it('a saved ticket rule dry run sends entity_type ticket', async () => {
      const rule = createMockAutomationRule({
        id: 9,
        name: 'Saved ticket rule',
        entity_type: 'ticket',
        trigger_type: 'ticket_created',
      });
      vi.mocked(automationApi.dryRun).mockResolvedValue(
        createMockDryRunReport({ rule_id: 9, entity_type: 'ticket', entity_id: 42 }),
      );
      render(<DryRunDialog rule={rule} open onClose={vi.fn()} />);

      expect(screen.getByRole('dialog', { name: 'Dry run: Saved ticket rule' })).toBeInTheDocument();
      expect(within(dialog()).getByLabelText('Record type')).toHaveValue('Ticket');

      await pickAutocompleteOption('Ticket', OPTION);
      fireEvent.click(runButton());

      await waitFor(() => expect(automationApi.dryRun).toHaveBeenCalledWith(9, { entity_type: 'ticket', entity_id: 42 }));
      expect(automationApi.dryRunDraft).not.toHaveBeenCalled();
      expect(await within(dialog()).findAllByTestId('dry-run-step')).toHaveLength(1);
    });
  });
});
