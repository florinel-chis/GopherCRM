import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { automationApi, configurationsApi, usersApi } from '@/api/endpoints';
import { createMockAutomationRule, createMockUser } from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import type {
  AutomationRule,
  AutomationStep,
  AutomationStepConfig,
  AutomationStepType,
  AutomationTrigger,
} from '@/types';
import { Component as AutomationRuleForm } from './AutomationRuleForm';

vi.mock('react-router-dom', async () => {
  const actual = await vi.importActual('react-router-dom');
  return { ...actual, useNavigate: vi.fn(), useParams: vi.fn() };
});

vi.mock('@/api/endpoints', () => ({
  automationApi: {
    getRule: vi.fn(),
    createRule: vi.fn(),
    updateRule: vi.fn(),
    dryRun: vi.fn(),
    dryRunDraft: vi.fn(),
  },
  usersApi: { getUsers: vi.fn(), getUser: vi.fn() },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
  ticketsApi: { getTickets: vi.fn(), getTicket: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const showSuccess = vi.fn();
const showError = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

// A rule refusal as the API sends it: the first refusal's message and every
// refused field in error.details.fields.
const fieldsError = (
  message: string,
  fields: Array<{ field: string; code: string; message: string }>,
  code = 'INVALID_INPUT',
) =>
  new AxiosError('Request failed with status code 400', 'ERR_BAD_REQUEST', undefined, undefined, {
    status: 400,
    statusText: 'Bad Request',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: { fields } },
  } as AxiosResponse);

const steps = (...items: Array<[AutomationStepType, AutomationStepConfig]>): AutomationStep[] =>
  items.map(([step_type, config], index) => ({
    id: index + 1,
    rule_id: 3,
    position: index + 1,
    step_type,
    created_at: '2026-11-13T08:00:00Z',
    config,
  }));

const storedRule = (overrides: Partial<AutomationRule>) =>
  createMockAutomationRule({ id: 3, name: 'Stored ticket rule', version: 2, enabled: false, ...overrides });

// The API always answers hour_utc (0 for an event trigger) and the form never
// sends it for one; with that default the sent trigger is the stored one.
const asStored = (trigger: Partial<AutomationTrigger>): AutomationTrigger => ({ hour_utc: 0, ...trigger });

const group = (n: number) => screen.getByRole('group', { name: `Step ${n}` });
const field = (container: HTMLElement, name: string) => within(container).getByRole('textbox', { name });
const type = (element: HTMLElement, value: string) => fireEvent.change(element, { target: { value } });
const combobox = (container: HTMLElement, label: string) =>
  within(container).getByRole('combobox', { name: new RegExp(`^${label}`) });

const choose = async (container: HTMLElement, label: string, option: string) => {
  fireEvent.mouseDown(combobox(container, label));
  fireEvent.click(await screen.findByRole('option', { name: option }));
};

// The labels a select offers, read by opening it and closing it again.
const selectOptions = async (container: HTMLElement, label: string): Promise<string[]> => {
  fireEvent.mouseDown(combobox(container, label));
  const listbox = await screen.findByRole('listbox');
  const labels = within(listbox)
    .getAllByRole('option')
    .map((option) => option.textContent ?? '');
  fireEvent.keyDown(listbox, { key: 'Escape' });
  await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
  return labels;
};

// The values a status / priority value input suggests.
const valueSuggestions = async (container: HTMLElement): Promise<string[]> => {
  const input = within(container).getByRole('combobox', { name: 'Value' });
  input.focus();
  fireEvent.mouseDown(input);
  const listbox = await screen.findByRole('listbox');
  const labels = within(listbox)
    .getAllByRole('option')
    .map((option) => option.textContent ?? '');
  fireEvent.keyDown(input, { key: 'Escape' });
  await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
  return labels;
};

const chooseValue = async (container: HTMLElement, option: string) => {
  const input = within(container).getByRole('combobox', { name: 'Value' });
  input.focus();
  fireEvent.mouseDown(input);
  fireEvent.click(await screen.findByRole('option', { name: option }));
  await waitFor(() => expect(input).toHaveValue(option));
};

const addStep = async (label: string) => {
  fireEvent.click(screen.getByRole('button', { name: 'Add step' }));
  fireEvent.click(await screen.findByRole('menuitem', { name: label }));
};

const submitCreate = () => fireEvent.click(screen.getByRole('button', { name: 'Create rule' }));

const renderEdit = async (rule: AutomationRule) => {
  vi.mocked(useParams).mockReturnValue({ id: String(rule.id) });
  vi.mocked(automationApi.getRule).mockResolvedValue(rule);
  const view = render(<AutomationRuleForm />);
  await screen.findByRole('heading', { name: 'Edit automation rule' });
  await waitFor(() => expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue(rule.name));
  return view;
};

const saveAndConfirm = async () => {
  fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
  const dialog = await screen.findByRole('dialog', { name: 'Save rule changes?' });
  fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
};

const TICKET_PLACEHOLDERS =
  'Placeholders: {{ticket.id}}, {{ticket.number}}, {{ticket.status}}, {{ticket.priority}}, ' +
  '{{owner.first_name}}, {{owner.last_name}}, {{owner.email}}';
const TICKET_TASK_PLACEHOLDERS =
  'Placeholders: {{ticket.id}}, {{ticket.number}}, {{ticket.status}}, {{ticket.priority}}';

const sam = createMockUser({ id: 7, first_name: 'Sam', last_name: 'Seller', role: 'sales' });

// Each test drives many MUI selects; give them room under full-suite load.
describe('AutomationRuleForm', { timeout: 20_000 }, () => {
  const mockNavigate = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(useNavigate).mockReturnValue(mockNavigate);
    vi.mocked(useParams).mockReturnValue({});
    vi.mocked(configurationsApi.getUIConfigurations).mockResolvedValue([]);
    vi.mocked(automationApi.createRule).mockImplementation(async (data) =>
      createMockAutomationRule({ id: 50, name: data.name }),
    );
    vi.mocked(usersApi.getUsers).mockResolvedValue({ data: [sam], total: 1, page: 1, limit: 100, total_pages: 1 });
    vi.mocked(usersApi.getUser).mockResolvedValue(sam);
  });

  describe('ticket rules', () => {
    it('an admin creates a ticket status change rule and the API receives a ticket rule', async () => {
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Urgent ticket resolved');
      await choose(document.body, 'Entity type', 'Ticket');
      await choose(document.body, 'Trigger', 'Ticket status changed');
      await choose(document.body, 'From status', 'Open');
      await choose(document.body, 'To status', 'Resolved');

      await addStep('Condition');
      await choose(group(1), 'Field', 'Priority');
      await choose(group(1), 'Operator', 'equals');
      await chooseValue(group(1), 'urgent');

      await addStep('Notify');
      type(field(group(2), 'Title'), 'Ticket {{ticket.number}} resolved');

      submitCreate();

      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      const body = vi.mocked(automationApi.createRule).mock.calls[0][0];
      expect(body).toEqual({
        name: 'Urgent ticket resolved',
        description: '',
        entity_type: 'ticket',
        trigger_type: 'ticket_status_changed',
        trigger: { from: 'open', to: 'resolved' },
        enabled: false,
        steps: [
          { step_type: 'condition', config: { field: 'priority', operator: 'eq', value: 'urgent', else: 'stop' } },
          { step_type: 'notify', config: { title: 'Ticket {{ticket.number}} resolved', recipient: 'owner' } },
        ],
      });
      // No deal stage and no stage filter anywhere in the request.
      expect(body.trigger.filters).toBeUndefined();
      expect(JSON.stringify(body)).not.toMatch(/stage|qualification|proposal|negotiation/);
      await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/automation/rules'));
      expect(showSuccess).toHaveBeenCalledWith('Rule created');
    });

    it('a ticket schedule rule is created with a ticket status filter', async () => {
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Stale tickets in progress');
      await choose(document.body, 'Entity type', 'Ticket');
      await choose(document.body, 'Trigger', 'Schedule');
      type(screen.getByRole('spinbutton', { name: 'Hour (UTC)' }), '8');
      await choose(document.body, 'Only status', 'In progress');
      await addStep('Notify');
      type(field(group(1), 'Title'), 'Ticket {{ticket.number}} is waiting');

      submitCreate();
      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      const body = vi.mocked(automationApi.createRule).mock.calls[0][0];
      expect(body.entity_type).toBe('ticket');
      expect(body.trigger_type).toBe('schedule');
      expect(body.trigger).toEqual({ hour_utc: 8, filters: { ticket_status: 'in_progress' } });
      expect(body.trigger.filters).toEqual({ ticket_status: 'in_progress' });
    });

    it('editing a stored ticket rule loads every field and saves it unchanged', async () => {
      // A schedule rule with filters.ticket_status and every step type.
      const schedule = storedRule({
        entity_type: 'ticket',
        trigger_type: 'schedule',
        trigger: { hour_utc: 7, stale_days: 3, filters: { ticket_status: 'resolved' } },
        steps: steps(
          ['wait', { duration_minutes: 120 }],
          ['condition', { field: 'status', operator: 'eq', value: 'resolved', else: 'stop' }],
          ['condition', { field: 'customer_id', operator: 'gt', value: 4, else: 'stop' }],
          [
            'create_task',
            { title: 'Check {{ticket.number}}', priority: 'high', due_in_days: 1, recipient: 'owner' },
          ],
          ['assign_owner', { user_id: 7 }],
          ['send_email', { subject: '{{ticket.number}} closed?', body: 'Status {{ticket.status}}', recipient: 'owner' }],
          ['notify', { title: 'Ticket {{ticket.number}} is resolved', recipient: 'owner' }],
        ),
      });
      vi.mocked(automationApi.updateRule).mockResolvedValue({ ...schedule, version: 3 });
      const { unmount } = await renderEdit(schedule);

      expect(combobox(document.body, 'Entity type')).toHaveTextContent('Ticket');
      expect(combobox(document.body, 'Trigger')).toHaveTextContent('Schedule');
      expect(combobox(document.body, 'Only status')).toHaveTextContent('Resolved');
      expect(screen.getByRole('spinbutton', { name: 'Hour (UTC)' })).toHaveValue(7);
      expect(screen.getByRole('spinbutton', { name: 'Not updated for (days)' })).toHaveValue(3);
      expect(screen.getAllByRole('group', { name: /^Step \d+$/ })).toHaveLength(7);
      expect(within(group(1)).getByRole('spinbutton', { name: 'Duration' })).toHaveValue(2);
      expect(combobox(group(2), 'Field')).toHaveTextContent('Status');
      expect(within(group(2)).getByRole('combobox', { name: 'Value' })).toHaveValue('resolved');
      expect(combobox(group(3), 'Field')).toHaveTextContent('Customer ID');
      expect(field(group(3), 'Value')).toHaveValue('4');
      expect(field(group(4), 'Title')).toHaveValue('Check {{ticket.number}}');
      await waitFor(() => expect(within(group(5)).getByLabelText('New owner')).toHaveValue('Sam Seller (sales)'));
      expect(field(group(6), 'Subject')).toHaveValue('{{ticket.number}} closed?');
      expect(field(group(7), 'Title')).toHaveValue('Ticket {{ticket.number}} is resolved');

      await saveAndConfirm();
      await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
      const [id, body] = vi.mocked(automationApi.updateRule).mock.calls[0];
      expect(id).toBe(3);
      expect(body.entity_type).toBe(schedule.entity_type);
      expect(body.trigger_type).toBe(schedule.trigger_type);
      expect(body.trigger).toEqual(schedule.trigger);
      expect(body.steps).toEqual(schedule.steps.map(({ step_type, config }) => ({ step_type, config })));
      expect('enabled' in body).toBe(false);
      unmount();

      // A ticket_status_changed rule with from / to, as the create above stores it.
      vi.mocked(automationApi.updateRule).mockClear();
      const change = storedRule({
        entity_type: 'ticket',
        trigger_type: 'ticket_status_changed',
        trigger: { hour_utc: 0, from: 'open', to: 'resolved' },
        steps: steps(['notify', { title: 'Ticket {{ticket.number}} resolved', recipient: 'owner' }]),
      });
      vi.mocked(automationApi.updateRule).mockResolvedValue({ ...change, version: 3 });
      await renderEdit(change);

      expect(combobox(document.body, 'Entity type')).toHaveTextContent('Ticket');
      expect(combobox(document.body, 'Trigger')).toHaveTextContent('Ticket status changed');
      expect(combobox(document.body, 'From status')).toHaveTextContent('Open');
      expect(combobox(document.body, 'To status')).toHaveTextContent('Resolved');
      expect(field(group(1), 'Title')).toHaveValue('Ticket {{ticket.number}} resolved');

      await saveAndConfirm();
      await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
      const [, changeBody] = vi.mocked(automationApi.updateRule).mock.calls[0];
      expect(changeBody.entity_type).toBe('ticket');
      expect(changeBody.trigger_type).toBe('ticket_status_changed');
      expect(changeBody.trigger).toEqual({ from: 'open', to: 'resolved' });
      expect(asStored(changeBody.trigger)).toEqual(change.trigger);
      expect(changeBody.steps).toEqual(change.steps.map(({ step_type, config }) => ({ step_type, config })));
    });

    it('API field errors on a ticket rule show on their fields', async () => {
      vi.mocked(automationApi.createRule).mockRejectedValueOnce(
        fieldsError(
          'filters.ticket_status "in_progress" is not a ticket status: validation failed',
          [
            {
              field: 'trigger.filters.ticket_status',
              code: 'invalid_value',
              message: 'filters.ticket_status "in_progress" is not a ticket status',
            },
            {
              field: 'steps[0].config.user_id',
              code: 'unknown_reference',
              message: 'the user is not a support or admin user',
            },
            { field: 'entity_type', code: 'invalid_value', message: 'entity_type must be lead, deal or ticket' },
          ],
          'INVALID_REFERENCE',
        ),
      );
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Hand tickets over');
      await choose(document.body, 'Entity type', 'Ticket');
      await choose(document.body, 'Trigger', 'Schedule');
      await choose(document.body, 'Only status', 'In progress');
      await addStep('Assign owner');
      await pickAutocompleteOption('New owner', 'Sam Seller (sales)');
      await addStep('Notify');
      type(field(group(2), 'Title'), 'Ticket {{ticket.number}} handed over');
      submitCreate();

      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      expect(vi.mocked(automationApi.createRule).mock.calls[0][0].trigger.filters).toEqual({
        ticket_status: 'in_progress',
      });

      // On the Only status field.
      const onlyStatus = combobox(document.body, 'Only status').closest('.MuiFormControl-root') as HTMLElement;
      expect(
        await within(onlyStatus).findByText('filters.ticket_status "in_progress" is not a ticket status'),
      ).toBeInTheDocument();
      // On step 1's user picker.
      const picker = within(group(1)).getByLabelText('New owner').closest('.MuiFormControl-root') as HTMLElement;
      expect(within(picker).getByText('the user is not a support or admin user')).toBeInTheDocument();
      // The entity type has no field of its own.
      expect(screen.getByTestId('rule-server-error')).toHaveTextContent('entity_type must be lead, deal or ticket');
      expect(screen.queryByTestId('step-server-error')).not.toBeInTheDocument();
      expect(mockNavigate).not.toHaveBeenCalled();
    });

    it('ticket rules use status wording and ticket labels', async () => {
      render(<AutomationRuleForm />);
      expect(await selectOptions(document.body, 'Entity type')).toEqual(['Lead', 'Deal', 'Ticket']);
      await choose(document.body, 'Entity type', 'Ticket');

      expect(await selectOptions(document.body, 'Trigger')).toEqual([
        'Ticket created',
        'Ticket updated',
        'Ticket status changed',
        'Schedule',
      ]);

      await choose(document.body, 'Trigger', 'Ticket status changed');
      expect(screen.queryByRole('combobox', { name: /^From stage/ })).not.toBeInTheDocument();
      const statuses = ['Any', 'Open', 'In progress', 'Resolved', 'Closed'];
      expect(await selectOptions(document.body, 'From status')).toEqual(statuses);
      expect(await selectOptions(document.body, 'To status')).toEqual(statuses);

      await choose(document.body, 'Trigger', 'Schedule');
      expect(await selectOptions(document.body, 'Only status')).toEqual(statuses);

      await addStep('Condition');
      expect(await selectOptions(group(1), 'Field')).toEqual(['Status', 'Priority', 'Owner ID', 'Customer ID']);
      await choose(group(1), 'Field', 'Status');
      await choose(group(1), 'Operator', 'equals');
      expect(await valueSuggestions(group(1))).toEqual(['open', 'in_progress', 'resolved', 'closed']);
      await choose(group(1), 'Field', 'Priority');
      await choose(group(1), 'Operator', 'equals');
      expect(await valueSuggestions(group(1))).toEqual(['low', 'medium', 'high', 'urgent']);

      await addStep('Notify');
      expect(within(group(2)).getAllByText(TICKET_PLACEHOLDERS)).toHaveLength(2);
      await addStep('Send email');
      expect(within(group(3)).getAllByText(TICKET_PLACEHOLDERS)).toHaveLength(2);
      await addStep('Create task');
      expect(within(group(4)).getAllByText(TICKET_TASK_PLACEHOLDERS)).toHaveLength(2);
    });

    it('switching from ticket to lead resets the trigger and conditions', async () => {
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Switched');
      await choose(document.body, 'Entity type', 'Ticket');
      await choose(document.body, 'Trigger', 'Schedule');
      await choose(document.body, 'Only status', 'Closed');
      await choose(document.body, 'Trigger', 'Ticket status changed');
      await choose(document.body, 'From status', 'Open');
      await choose(document.body, 'To status', 'Resolved');
      await addStep('Condition');
      await choose(group(1), 'Field', 'Priority');
      await choose(group(1), 'Operator', 'equals');
      await chooseValue(group(1), 'urgent');

      await choose(document.body, 'Entity type', 'Lead');

      expect(combobox(document.body, 'Trigger')).toHaveTextContent('Lead created');
      expect(screen.queryByRole('combobox', { name: /^From status/ })).not.toBeInTheDocument();
      expect(within(group(1)).queryByRole('combobox', { name: 'Value' })).not.toBeInTheDocument();
      expect(await selectOptions(group(1), 'Field')).toEqual(['Status', 'Source', 'Classification', 'Company', 'Owner ID']);

      await choose(document.body, 'Trigger', 'Lead status changed');
      expect(combobox(document.body, 'From status')).toHaveTextContent('Any');
      expect(combobox(document.body, 'To status')).toHaveTextContent('Any');
      await choose(document.body, 'Trigger', 'Schedule');
      expect(combobox(document.body, 'Only status')).toHaveTextContent('Any');

      // The cleared condition field is refused before any request.
      submitCreate();
      expect(await within(group(1)).findByText('Pick a field')).toBeInTheDocument();
      expect(automationApi.createRule).not.toHaveBeenCalled();

      await choose(group(1), 'Field', 'Status');
      await choose(group(1), 'Operator', 'is empty');
      submitCreate();
      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      expect(vi.mocked(automationApi.createRule).mock.calls[0][0]).toEqual({
        name: 'Switched',
        description: '',
        entity_type: 'lead',
        trigger_type: 'schedule',
        trigger: { hour_utc: 9 },
        enabled: false,
        steps: [{ step_type: 'condition', config: { field: 'status', operator: 'is_empty', else: 'stop' } }],
      });
    });
  });

  describe('lead and deal rules unchanged', () => {
    it('a deal stage change rule is still created with deal stages', async () => {
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Proposal sent');
      await choose(document.body, 'Entity type', 'Deal');
      expect(await selectOptions(document.body, 'Trigger')).toEqual([
        'Deal created',
        'Deal updated',
        'Deal stage changed',
        'Schedule',
      ]);
      await choose(document.body, 'Trigger', 'Deal stage changed');
      expect(screen.queryByRole('combobox', { name: /^From status/ })).not.toBeInTheDocument();
      expect(await selectOptions(document.body, 'From stage')).toEqual([
        'Any',
        'Qualification',
        'Proposal',
        'Negotiation',
        'Won',
        'Lost',
      ]);
      await choose(document.body, 'From stage', 'Qualification');
      await choose(document.body, 'To stage', 'Proposal');
      await addStep('Notify');
      expect(within(group(1)).getAllByText(/^Placeholders: \{\{deal\.id\}\}/)).toHaveLength(2);
      type(field(group(1), 'Title'), 'Deal {{deal.title}} moved');

      submitCreate();
      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      expect(vi.mocked(automationApi.createRule).mock.calls[0][0]).toEqual({
        name: 'Proposal sent',
        description: '',
        entity_type: 'deal',
        trigger_type: 'deal_stage_changed',
        trigger: { from: 'qualification', to: 'proposal' },
        enabled: false,
        steps: [{ step_type: 'notify', config: { title: 'Deal {{deal.title}} moved', recipient: 'owner' } }],
      });
    });

    it('a lead schedule rule still sends filters.status', async () => {
      render(<AutomationRuleForm />);
      type(screen.getByRole('textbox', { name: 'Name' }), 'Stale contacted leads');
      await choose(document.body, 'Trigger', 'Schedule');
      type(screen.getByRole('spinbutton', { name: 'Hour (UTC)' }), '6');
      expect(await selectOptions(document.body, 'Only status')).toEqual([
        'Any',
        'New',
        'Contacted',
        'Qualified',
        'Unqualified',
        'Converted',
      ]);
      await choose(document.body, 'Only status', 'Contacted');
      await addStep('Notify');
      type(field(group(1), 'Title'), 'Lead {{lead.id}} is stale');

      submitCreate();
      await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
      expect(vi.mocked(automationApi.createRule).mock.calls[0][0]).toEqual({
        name: 'Stale contacted leads',
        description: '',
        entity_type: 'lead',
        trigger_type: 'schedule',
        trigger: { hour_utc: 6, filters: { status: 'contacted' } },
        enabled: false,
        steps: [{ step_type: 'notify', config: { title: 'Lead {{lead.id}} is stale', recipient: 'owner' } }],
      });
    });
  });
});
