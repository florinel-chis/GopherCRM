import { describe, it, expect, vi, beforeEach } from 'vitest';
import { AxiosError, type AxiosResponse, type InternalAxiosRequestConfig } from 'axios';
import { render, screen, waitFor, fireEvent, within } from '@/test/test-utils';
import { useNavigate, useParams } from 'react-router-dom';
import { automationApi, configurationsApi, leadsApi, usersApi } from '@/api/endpoints';
import {
  createMockAutomationRule,
  createMockDryRunReport,
  createMockLead,
  createMockUser,
} from '@/test/factories';
import { pickAutocompleteOption } from '@/test/autocomplete';
import type { AutomationRule, AutomationStep, AutomationStepConfig, AutomationStepType } from '@/types';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';
import { automationKeys } from './automationQueries';
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
  },
  usersApi: { getUsers: vi.fn(), getUser: vi.fn() },
  leadsApi: { getLeads: vi.fn(), getLead: vi.fn() },
  dealsApi: { getDeals: vi.fn(), getDeal: vi.fn() },
  configurationsApi: { getUIConfigurations: vi.fn() },
}));

const showSuccess = vi.fn();
const showError = vi.fn();
vi.mock('@/hooks/useSnackbar', () => ({
  useSnackbar: () => ({ showSuccess, showError }),
}));

const apiError = (status: number, code: string, message: string) =>
  new AxiosError(`Request failed with status code ${status}`, 'ERR_BAD_REQUEST', undefined, undefined, {
    status,
    statusText: 'Bad Request',
    headers: {},
    config: {} as InternalAxiosRequestConfig,
    data: { code, message, details: null },
  } as AxiosResponse);

const steps = (...items: Array<[AutomationStepType, AutomationStepConfig]>): AutomationStep[] =>
  items.map(([step_type, config], index) => ({
    id: index + 1,
    rule_id: 3,
    position: index + 1,
    step_type,
    created_at: '2026-10-28T08:00:00Z',
    config,
  }));

const storedRule = (overrides: Partial<AutomationRule>) =>
  createMockAutomationRule({ id: 3, name: 'Stored rule', version: 2, enabled: true, ...overrides });

const group = (n: number) => screen.getByRole('group', { name: `Step ${n}` });
const field = (container: HTMLElement, name: string) => within(container).getByRole('textbox', { name });
const type = (element: HTMLElement, value: string) => fireEvent.change(element, { target: { value } });

const choose = async (container: HTMLElement, label: string, option: string) => {
  fireEvent.mouseDown(within(container).getByRole('combobox', { name: new RegExp(`^${label}`) }));
  fireEvent.click(await screen.findByRole('option', { name: option }));
};

// The status / stage value is a free-text combobox that offers the known values.
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

// Condition, wait, "Notify A", "Notify B"; the condition jumps to "Notify A".
const jumpRule = () =>
  storedRule({
    steps: steps(
      ['condition', { field: 'status', operator: 'is_empty', else: 'jump', else_position: 3 }],
      ['wait', { duration_minutes: 60 }],
      ['notify', { title: 'Notify A', recipient: 'owner' }],
      ['notify', { title: 'Notify B', recipient: 'owner' }],
    ),
  });

const sam = createMockUser({ id: 7, first_name: 'Sam', last_name: 'Seller', role: 'sales' });

describe('AutomationRuleForm', () => {
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

  it('builds a lead_status_changed rule with wait, condition, task and notify steps and asserts the POST payload', async () => {
    render(<AutomationRuleForm />);
    expect(screen.getByRole('heading', { name: 'New automation rule' })).toBeInTheDocument();
    expect(screen.getByText('New rules start disabled unless switched on')).toBeInTheDocument();

    type(screen.getByRole('textbox', { name: 'Name' }), 'Follow up qualified leads');
    await choose(document.body, 'Trigger', 'Lead status changed');
    await choose(document.body, 'To status', 'Qualified');

    await addStep('Wait');
    type(within(group(1)).getByRole('spinbutton', { name: 'Duration' }), '2');

    await addStep('Condition');
    await choose(group(2), 'Field', 'Status');
    await choose(group(2), 'Operator', 'equals');
    await chooseValue(group(2), 'qualified');

    await addStep('Create task');
    // Title and description both list the narrower task placeholders.
    expect(within(group(3)).getAllByText('Placeholders: {{lead.id}}, {{lead.status}}, {{lead.source}}')).toHaveLength(2);
    type(field(group(3), 'Title'), 'Call lead {{lead.id}}');
    await choose(group(3), 'Priority', 'High');
    type(within(group(3)).getByRole('spinbutton', { name: 'Due in days' }), '1');

    await addStep('Notify');
    type(field(group(4), 'Title'), 'Lead {{lead.id}} is qualified');

    submitCreate();

    await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
    expect(automationApi.createRule).toHaveBeenCalledWith({
      name: 'Follow up qualified leads',
      description: '',
      entity_type: 'lead',
      trigger_type: 'lead_status_changed',
      trigger: { to: 'qualified' },
      enabled: false,
      steps: [
        { step_type: 'wait', config: { duration_minutes: 2880 } },
        { step_type: 'condition', config: { field: 'status', operator: 'eq', value: 'qualified', else: 'stop' } },
        {
          step_type: 'create_task',
          config: { title: 'Call lead {{lead.id}}', priority: 'high', due_in_days: 1, recipient: 'owner' },
        },
        { step_type: 'notify', config: { title: 'Lead {{lead.id}} is qualified', recipient: 'owner' } },
      ],
    });
    await waitFor(() => expect(mockNavigate).toHaveBeenCalledWith('/automation/rules'));
    expect(showSuccess).toHaveBeenCalledWith('Rule created');
  });

  it('refuses a 21st step client-side', async () => {
    const twenty = steps(
      ...Array.from({ length: 20 }, (_, i): [AutomationStepType, AutomationStepConfig] => [
        'notify',
        { title: `Step ${i + 1}`, recipient: 'owner' },
      ]),
    );
    await renderEdit(storedRule({ steps: twenty }));

    expect(screen.getAllByRole('group', { name: /^Step \d+$/ })).toHaveLength(20);
    expect(screen.getByRole('button', { name: 'Add step' })).toBeDisabled();
    expect(screen.getByText('A rule has at most 20 steps')).toBeInTheDocument();
  });

  it('refuses a backward else_position client-side', async () => {
    const { unmount } = render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Backward');
    await addStep('Notify');
    type(field(group(1), 'Title'), 'One');
    await addStep('Notify');
    type(field(group(2), 'Title'), 'Two');
    await addStep('Condition');
    await choose(group(3), 'Field', 'Status');
    await choose(group(3), 'Operator', 'is empty');
    await choose(group(3), 'If false', 'Jump to step');

    // The last step has nothing after it to jump to.
    fireEvent.mouseDown(within(group(3)).getByRole('combobox', { name: /^Jump to step/ }));
    const listbox = await screen.findByRole('listbox');
    expect(within(listbox).queryAllByRole('option')).toHaveLength(0);
    fireEvent.keyDown(listbox, { key: 'Escape' });

    submitCreate();
    expect(await within(group(3)).findByText('No later step to jump to')).toBeInTheDocument();
    expect(automationApi.createRule).not.toHaveBeenCalled();
    unmount();

    // A stored rule whose step 2 jumps back to step 1.
    await renderEdit(
      storedRule({
        steps: steps(
          ['notify', { title: 'One', recipient: 'owner' }],
          ['condition', { field: 'status', operator: 'is_empty', else: 'jump', else_position: 1 }],
          ['notify', { title: 'Three', recipient: 'owner' }],
        ),
      }),
    );
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    expect(await within(group(2)).findByText('Jump to a later step')).toBeInTheDocument();
    expect(screen.queryByRole('dialog', { name: 'Save rule changes?' })).not.toBeInTheDocument();
    expect(automationApi.updateRule).not.toHaveBeenCalled();
  });

  it('keeps a jump on its target step when steps are reordered', async () => {
    vi.mocked(automationApi.updateRule).mockImplementation(async (_id, data) => storedRule({ ...data, version: 3 } as never));
    await renderEdit(jumpRule());
    expect(within(group(1)).getByRole('combobox', { name: /^Jump to step/ })).toHaveTextContent('Step 3 · Notify');

    fireEvent.click(screen.getByRole('button', { name: 'Move step 3 down' }));
    expect(field(group(4), 'Title')).toHaveValue('Notify A');
    expect(within(group(1)).getByRole('combobox', { name: /^Jump to step/ })).toHaveTextContent('Step 4 · Notify');

    await saveAndConfirm();
    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
    const [, payload] = vi.mocked(automationApi.updateRule).mock.calls[0];
    expect(payload.steps[0].config).toEqual({ field: 'status', operator: 'is_empty', else: 'jump', else_position: 4 });
    expect(payload.steps.map((s) => s.config.title)).toEqual([undefined, undefined, 'Notify B', 'Notify A']);
  });

  it('clears a jump whose target step is removed and shows the error on the condition', async () => {
    await renderEdit(jumpRule());

    fireEvent.click(screen.getByRole('button', { name: 'Remove step 3' }));

    expect(await within(group(1)).findByText('The jump target was removed; pick another step')).toBeInTheDocument();
    expect(screen.getAllByRole('group', { name: /^Step \d+$/ })).toHaveLength(3);
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
    await waitFor(() => expect(within(group(1)).getByText('The jump target was removed; pick another step')).toBeInTheDocument());
    expect(screen.queryByRole('dialog', { name: 'Save rule changes?' })).not.toBeInTheDocument();
    expect(automationApi.updateRule).not.toHaveBeenCalled();
  });

  it('removes and reorders steps and submits them in the new order', async () => {
    vi.mocked(automationApi.updateRule).mockImplementation(async () => storedRule({ version: 3 }));
    await renderEdit(
      storedRule({
        steps: steps(
          ['wait', { duration_minutes: 1440 }],
          ['condition', { field: 'source', operator: 'eq', value: 'web', else: 'stop' }],
          ['create_task', { title: 'Task', due_in_days: 0, recipient: 'owner' }],
          ['notify', { title: 'Note', recipient: 'owner' }],
        ),
      }),
    );

    fireEvent.click(screen.getByRole('button', { name: 'Remove step 2' }));
    await waitFor(() => expect(screen.getAllByRole('group', { name: /^Step \d+$/ })).toHaveLength(3));
    expect(screen.getByRole('button', { name: 'Move step 1 up' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Move step 3 down' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Move step 3 up' }));
    expect(within(group(2)).getByText('Step 2 · Notify')).toBeInTheDocument();

    await saveAndConfirm();
    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
    const [id, payload] = vi.mocked(automationApi.updateRule).mock.calls[0];
    expect(id).toBe(3);
    expect(payload.steps).toEqual([
      { step_type: 'wait', config: { duration_minutes: 1440 } },
      { step_type: 'notify', config: { title: 'Note', recipient: 'owner' } },
      { step_type: 'create_task', config: { title: 'Task', due_in_days: 0, recipient: 'owner' } },
    ]);
  });

  it('submits an assign_owner step and a send_email step to a specific user', async () => {
    render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Hand over');

    await addStep('Assign owner');
    await pickAutocompleteOption('New owner', 'Sam Seller (sales)');

    await addStep('Send email');
    await choose(group(2), 'Send to', 'Specific user');
    await pickAutocompleteOption('User', 'Sam Seller (sales)');
    type(field(group(2), 'Subject'), 'Check {{lead.id}}');
    type(field(group(2), 'Body'), 'Please call');

    submitCreate();
    await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
    expect(vi.mocked(automationApi.createRule).mock.calls[0][0].steps).toEqual([
      { step_type: 'assign_owner', config: { user_id: 7 } },
      {
        step_type: 'send_email',
        config: { subject: 'Check {{lead.id}}', body: 'Please call', recipient: 'user', user_id: 7 },
      },
    ]);
  });

  it('submits a schedule trigger with hour, status filter and stale days', async () => {
    render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Stale contacted leads');
    await choose(document.body, 'Trigger', 'Schedule');
    type(screen.getByRole('spinbutton', { name: 'Hour (UTC)' }), '8');
    await choose(document.body, 'Only status', 'Contacted');
    type(screen.getByRole('spinbutton', { name: 'Not updated for (days)' }), '14');
    await addStep('Notify');
    type(field(group(1), 'Title'), 'Stale lead {{lead.id}}');

    submitCreate();
    await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
    const payload = vi.mocked(automationApi.createRule).mock.calls[0][0];
    expect(payload.trigger_type).toBe('schedule');
    expect(payload.trigger).toEqual({ hour_utc: 8, stale_days: 14, filters: { status: 'contacted' } });
  });

  it('submits an in-list with commas, surrounding spaces and an empty item verbatim', async () => {
    render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Companies');
    await addStep('Condition');
    await choose(group(1), 'Field', 'Company');
    await choose(group(1), 'Operator', 'is one of');
    type(field(group(1), 'Value 1'), 'Acme, Inc.');
    fireEvent.click(within(group(1)).getByRole('button', { name: 'Add value' }));
    type(field(group(1), 'Value 2'), '  Beta  ');
    fireEvent.click(within(group(1)).getByRole('button', { name: 'Add value' }));
    expect(field(group(1), 'Value 3')).toHaveValue('');

    submitCreate();
    await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
    expect(vi.mocked(automationApi.createRule).mock.calls[0][0].steps).toEqual([
      {
        step_type: 'condition',
        config: { field: 'company', operator: 'in', value: ['Acme, Inc.', '  Beta  ', ''], else: 'stop' },
      },
    ]);
  });

  it('submits a money condition in cents and refuses exponent and three-decimal amounts', async () => {
    render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Big deals');
    await choose(document.body, 'Entity type', 'Deal');
    await addStep('Condition');
    await choose(group(1), 'Field', 'Amount');
    await choose(group(1), 'Operator', '≥');
    expect(within(group(1)).getByText('Major units, e.g. 1500 or -12.50')).toBeInTheDocument();
    type(field(group(1), 'Currency'), 'EUR');

    for (const amount of ['1e3', '1.234']) {
      type(field(group(1), 'Amount'), amount);
      submitCreate();
      expect(
        await within(group(1)).findByText('Enter an amount like 1500 or -12.50 (at most two decimals)'),
      ).toBeInTheDocument();
    }
    expect(automationApi.createRule).not.toHaveBeenCalled();

    type(field(group(1), 'Amount'), '-12.50');
    submitCreate();
    await waitFor(() => expect(automationApi.createRule).toHaveBeenCalledTimes(1));
    const payload = vi.mocked(automationApi.createRule).mock.calls[0][0];
    expect(payload.entity_type).toBe('deal');
    expect(payload.trigger_type).toBe('deal_created');
    expect(payload.steps).toEqual([
      {
        step_type: 'condition',
        config: { field: 'amount_cents', operator: 'gte', value: -1250, currency: 'EUR', else: 'stop' },
      },
    ]);
  });

  it('editing shows the cancellation warning before the PUT and omits enabled', async () => {
    const rule = storedRule({
      version: 2,
      steps: steps(['notify', { title: 'Hello', recipient: 'owner' }]),
    });
    vi.mocked(automationApi.updateRule).mockImplementation(async (_id, data) => ({
      ...rule,
      name: data.name,
      version: 3,
    }));
    await renderEdit(rule);
    expect(screen.getByText('Version 2')).toBeInTheDocument();
    expect(screen.queryByText('New rules start disabled unless switched on')).not.toBeInTheDocument();

    type(screen.getByRole('textbox', { name: 'Name' }), 'Renamed rule');
    fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));

    const dialog = await screen.findByRole('dialog', { name: 'Save rule changes?' });
    expect(dialog).toHaveTextContent(
      "Saving creates version 3 and cancels this rule's pending, waiting and running runs.",
    );
    expect(automationApi.updateRule).not.toHaveBeenCalled();

    fireEvent.click(within(dialog).getByRole('button', { name: 'Save' }));
    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
    const [id, payload] = vi.mocked(automationApi.updateRule).mock.calls[0];
    expect(id).toBe(3);
    expect(payload).toEqual({
      name: 'Renamed rule',
      description: '',
      entity_type: 'lead',
      trigger_type: 'lead_created',
      trigger: {},
      steps: [{ step_type: 'notify', config: { title: 'Hello', recipient: 'owner' } }],
    });
    expect('enabled' in payload).toBe(false);
    expect(await screen.findByText('Version 3')).toBeInTheDocument();
    expect(showSuccess).toHaveBeenCalledWith('Rule saved');
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('re-submits an untouched edited rule with the same steps and condition values', async () => {
    const rule = storedRule({
      entity_type: 'deal',
      trigger_type: 'deal_created',
      steps: steps(
        ['condition', { field: 'amount_cents', operator: 'gte', value: 150050.5, currency: 'EUR', else: 'stop' }],
        ['condition', { field: 'source', operator: 'in', value: ['a, b', ' ', ''], else: 'stop' }],
        ['condition', { field: 'probability', operator: 'gt', value: 12.5, else: 'stop' }],
        ['notify', { title: 'Big one {{deal.title}}', recipient: 'owner' }],
      ),
    });
    vi.mocked(automationApi.updateRule).mockResolvedValue({ ...rule, version: 3 });
    await renderEdit(rule);

    type(screen.getByRole('textbox', { name: 'Name' }), 'Only the name changed');
    await saveAndConfirm();

    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
    const [, payload] = vi.mocked(automationApi.updateRule).mock.calls[0];
    expect(payload.name).toBe('Only the name changed');
    expect(payload.steps).toEqual(rule.steps.map(({ step_type, config }) => ({ step_type, config })));
  });

  it('opens the dry run from the edit page for the saved rule even with unsaved changes', async () => {
    const lead = createMockLead({ id: 5, contact_name: 'Ion Ionescu', company_name: 'Bolt Robotics' });
    vi.mocked(leadsApi.getLeads).mockResolvedValue({ data: [lead], total: 1, page: 1, limit: 20, total_pages: 1 });
    vi.mocked(automationApi.dryRun).mockResolvedValue(createMockDryRunReport({ rule_id: 3, rule_version: 3 }));
    await renderEdit(storedRule({ name: 'Stored name', version: 3 }));

    type(screen.getByRole('textbox', { name: 'Name' }), 'Typed name');
    expect(screen.getByTestId('dry-run-saved-version')).toHaveTextContent(
      'Simulates the saved version 3; save first to include unsaved changes.',
    );
    fireEvent.click(screen.getByRole('button', { name: 'Dry run' }));

    const dialog = await screen.findByRole('dialog', { name: 'Dry run: Stored name' });
    expect(screen.queryByRole('dialog', { name: 'Dry run: Typed name' })).not.toBeInTheDocument();
    // The page's Entity type select also reads "Lead", so pick inside the dialog.
    const leadInput = within(dialog).getByLabelText('Lead');
    leadInput.focus();
    fireEvent.mouseDown(leadInput);
    fireEvent.click(await screen.findByRole('option', { name: 'Ion Ionescu (Bolt Robotics)' }));
    await waitFor(() => expect(leadInput).toHaveValue('Ion Ionescu (Bolt Robotics)'));
    fireEvent.click(within(dialog).getByRole('button', { name: 'Run dry run' }));

    await waitFor(() => expect(automationApi.dryRun).toHaveBeenCalledWith(3, { entity_type: 'lead', entity_id: 5 }));
    expect(automationApi.updateRule).not.toHaveBeenCalled();
  });

  it('shows a server 400 message on the step it names and other messages above the buttons', async () => {
    vi.mocked(automationApi.createRule)
      .mockRejectedValueOnce(apiError(400, 'INVALID_INPUT', 'step 2: title must not contain a line break: validation failed'))
      .mockRejectedValueOnce(apiError(400, 'INVALID_REFERENCE', 'user 7 is inactive: assignee not found'));
    render(<AutomationRuleForm />);
    type(screen.getByRole('textbox', { name: 'Name' }), 'Server says no');
    await addStep('Notify');
    type(field(group(1), 'Title'), 'First');
    await addStep('Notify');
    type(field(group(2), 'Title'), 'Second');

    submitCreate();
    const stepError = await within(group(2)).findByTestId('step-server-error');
    expect(stepError).toHaveTextContent('title must not contain a line break');
    expect(within(group(1)).queryByTestId('step-server-error')).not.toBeInTheDocument();
    expect(screen.queryByTestId('rule-server-error')).not.toBeInTheDocument();

    submitCreate();
    expect(await screen.findByTestId('rule-server-error')).toHaveTextContent('user 7 is inactive');
    expect(screen.queryByTestId('step-server-error')).not.toBeInTheDocument();
    expect(automationApi.createRule).toHaveBeenCalledTimes(2);
    expect(mockNavigate).not.toHaveBeenCalled();
  });

  it('keeps free-text status values: a saved contains and an unlisted value round-trip and can be entered', async () => {
    const rule = storedRule({
      steps: steps(
        ['condition', { field: 'status', operator: 'contains', value: 'qual', else: 'stop' }],
        ['condition', { field: 'status', operator: 'eq', value: ' lost ', else: 'stop' }],
        ['condition', { field: 'status', operator: 'neq', value: '', else: 'stop' }],
      ),
    });
    vi.mocked(automationApi.updateRule).mockResolvedValue({ ...rule, version: 3 });
    await renderEdit(rule);

    expect(field(group(1), 'Value')).toHaveValue('qual');
    expect(within(group(2)).getByRole('combobox', { name: 'Value' })).toHaveValue(' lost ');
    expect(within(group(3)).getByRole('combobox', { name: 'Value' })).toHaveValue('');

    // Untouched: re-sent exactly as stored.
    await saveAndConfirm();
    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(1));
    expect(vi.mocked(automationApi.updateRule).mock.calls[0][1].steps).toEqual(
      rule.steps.map(({ step_type, config }) => ({ step_type, config })),
    );

    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Save rule changes?' })).not.toBeInTheDocument());

    // Edited: any text, spaces kept, empty allowed for contains.
    type(field(group(1), 'Value'), '');
    type(within(group(2)).getByRole('combobox', { name: 'Value' }), '  archived ');
    await saveAndConfirm();
    await waitFor(() => expect(automationApi.updateRule).toHaveBeenCalledTimes(2));
    const configs = vi.mocked(automationApi.updateRule).mock.calls[1][1].steps.map((s) => s.config);
    expect(configs[0]).toEqual({ field: 'status', operator: 'contains', value: '', else: 'stop' });
    expect(configs[1]).toEqual({ field: 'status', operator: 'eq', value: '  archived ', else: 'stop' });
  });

  it('a failed rule read shows the server message with Retry, and a 404 says the rule was not found', async () => {
    vi.mocked(useParams).mockReturnValue({ id: '3' });
    const rule = storedRule({});
    vi.mocked(automationApi.getRule)
      .mockRejectedValueOnce(apiError(500, 'INTERNAL_ERROR', 'Database unavailable'))
      .mockResolvedValueOnce(rule);
    const { unmount } = render(<AutomationRuleForm />);

    const alert = await screen.findByTestId('rule-load-error');
    expect(alert).toHaveTextContent('Database unavailable');
    expect(screen.queryByText('Rule not found')).not.toBeInTheDocument();
    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue('Stored rule'));
    expect(automationApi.getRule).toHaveBeenCalledTimes(2);
    unmount();

    vi.mocked(automationApi.getRule).mockRejectedValueOnce(apiError(404, 'NOT_FOUND', 'rule 3 not found'));
    render(<AutomationRuleForm />);
    expect(await screen.findByText('Rule not found')).toBeInTheDocument();
    expect(screen.queryByTestId('rule-load-error')).not.toBeInTheDocument();
  });

  it('a cached rule that another admin deleted gives Rule not found once the refetch answers 404', async () => {
    const rule = storedRule({});
    const client = createProductionQueryClient();
    client.setQueryData(automationKeys.rule(3), rule);
    await client.invalidateQueries({ queryKey: automationKeys.rule(3) });
    vi.mocked(useParams).mockReturnValue({ id: '3' });
    vi.mocked(automationApi.getRule).mockRejectedValue(apiError(404, 'NOT_FOUND', 'rule 3 not found'));
    render(withClient(client, <AutomationRuleForm />));

    expect(await screen.findByText('Rule not found')).toBeInTheDocument();
    expect(automationApi.getRule).toHaveBeenCalledWith(3);
    expect(screen.queryByRole('textbox', { name: 'Name' })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Save changes' })).not.toBeInTheDocument();
  });

  it('a failed refetch of a cached rule keeps the editor and shows the error with Retry', async () => {
    const rule = storedRule({});
    const client = createProductionQueryClient();
    client.setQueryData(automationKeys.rule(3), rule);
    await client.invalidateQueries({ queryKey: automationKeys.rule(3) });
    vi.mocked(useParams).mockReturnValue({ id: '3' });
    vi.mocked(automationApi.getRule)
      .mockRejectedValueOnce(apiError(500, 'INTERNAL_ERROR', 'Database unavailable'))
      .mockResolvedValueOnce(rule);
    render(withClient(client, <AutomationRuleForm />));

    const alert = await screen.findByTestId('rule-load-error');
    expect(alert).toHaveTextContent('Database unavailable');
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue('Stored rule');
    expect(screen.getByRole('button', { name: 'Save changes' })).toBeInTheDocument();

    fireEvent.click(within(alert).getByRole('button', { name: 'Retry' }));
    await waitFor(() => expect(screen.queryByTestId('rule-load-error')).not.toBeInTheDocument());
    expect(automationApi.getRule).toHaveBeenCalledTimes(2);
    expect(screen.getByRole('textbox', { name: 'Name' })).toHaveValue('Stored rule');
  });
});
