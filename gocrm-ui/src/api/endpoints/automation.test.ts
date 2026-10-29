import { describe, it, expect, vi, beforeEach } from 'vitest';
import { automationApi } from './automation';
import type { AutomationRuleInput } from './automation';
import { api } from '../client';

vi.mock('../client', () => ({
  api: {
    get: vi.fn(),
    post: vi.fn(),
    put: vi.fn(),
    delete: vi.fn(),
  },
}));

const backendRule = {
  id: 7,
  created_at: '2026-10-28T08:00:00Z',
  updated_at: '2026-10-28T08:00:00Z',
  deleted_at: null,
  name: 'Welcome new leads',
  description: 'Notify the owner',
  entity_type: 'lead',
  trigger_type: 'lead_created',
  enabled: true,
  version: 3,
  schedule_cursor: 0,
  created_by_id: 1,
  trigger: { hour_utc: 0 },
  steps: [
    {
      id: 21,
      rule_id: 7,
      position: 1,
      step_type: 'notify',
      created_at: '2026-10-28T08:00:00Z',
      config: { recipient: 'owner', title: 'New lead', body: 'Call {{ lead.first_name }}' },
    },
  ],
};

const backendRun = {
  id: 31,
  created_at: '2026-10-28T09:00:00Z',
  updated_at: '2026-10-28T09:05:00Z',
  rule_id: 7,
  rule_version: 3,
  entity_type: 'lead',
  entity_id: 5,
  event_id: 12,
  dedupe_key: 'event:12:rule:7',
  status: 'waiting',
  next_position: 2,
  attempts: 0,
  resume_at: '2026-11-01T08:00:00Z',
};

const stepLog = [
  {
    id: 1,
    run_id: 31,
    position: 1,
    step_type: 'notify',
    outcome: 'succeeded',
    executed_at: '2026-10-28T09:00:00Z',
    details: { recipient_id: 3, notification_id: 11 },
  },
  {
    id: 2,
    run_id: 31,
    position: 2,
    step_type: 'wait',
    outcome: 'waiting',
    executed_at: '2026-10-28T09:00:01Z',
    details: { resume_at: '2026-11-01T08:00:00Z' },
  },
];

const ruleInput: AutomationRuleInput = {
  name: 'Welcome new leads',
  description: 'Notify the owner',
  entity_type: 'lead',
  trigger_type: 'lead_created',
  trigger: {},
  steps: [{ step_type: 'notify', config: { recipient: 'owner', title: 'New lead' } }],
};

describe('automationApi', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('lists rules with the filters as params and reads the total from meta', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: [backendRule],
      meta: { total: 41, page: 2, per_page: 20, total_pages: 3 },
    });

    const result = await automationApi.listRules({
      offset: 20,
      limit: 20,
      entity_type: 'lead',
      enabled: true,
      sort_by: 'name',
      sort_order: 'asc',
    });

    expect(api.get).toHaveBeenCalledWith('/automation/rules', {
      params: {
        offset: 20,
        limit: 20,
        entity_type: 'lead',
        enabled: true,
        sort_by: 'name',
        sort_order: 'asc',
      },
    });
    expect(result.rules).toHaveLength(1);
    expect(result.rules[0].name).toBe('Welcome new leads');
    expect(result.rules[0].steps).toHaveLength(1);
    expect(result.total).toBe(41);
  });

  it('sends enabled=false as a filter instead of dropping it', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [], meta: {} });

    await automationApi.listRules({ enabled: false });

    expect(api.get).toHaveBeenCalledWith('/automation/rules', { params: { enabled: false } });
  });

  it('falls back to the row count when meta carries no total', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendRule, { ...backendRule, id: 8 }] });

    const result = await automationApi.listRules();
    expect(result.total).toBe(2);

    vi.mocked(api.get).mockResolvedValue({ data: [], meta: { request_id: 'r1' } });
    await expect(automationApi.listRuns()).resolves.toEqual({ runs: [], total: 0 });
  });

  it('gets, creates and updates a rule and defaults missing steps to an empty array', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: backendRule });
    const fetched = await automationApi.getRule(7);
    expect(api.get).toHaveBeenCalledWith('/automation/rules/7');
    expect(fetched.steps).toEqual(backendRule.steps);

    vi.mocked(api.post).mockResolvedValue({
      data: { ...backendRule, steps: null, description: undefined, trigger: undefined },
    });
    const created = await automationApi.createRule(ruleInput);
    expect(api.post).toHaveBeenCalledWith('/automation/rules', ruleInput);
    expect(created.steps).toEqual([]);
    expect(created.description).toBe('');
    expect(created.trigger).toEqual({ hour_utc: 0 });

    const updateBody: AutomationRuleInput = { ...ruleInput, enabled: false };
    vi.mocked(api.put).mockResolvedValue({
      data: { ...backendRule, version: 4, steps: undefined },
    });
    const updated = await automationApi.updateRule(7, updateBody);
    expect(api.put).toHaveBeenCalledWith('/automation/rules/7', updateBody);
    expect(updated.version).toBe(4);
    expect(updated.steps).toEqual([]);
  });

  it('drops an explicit null condition value from a stored rule', async () => {
    vi.mocked(api.get).mockResolvedValue({
      data: {
        ...backendRule,
        steps: [
          {
            id: 22,
            rule_id: 7,
            position: 1,
            step_type: 'condition',
            created_at: '2026-10-28T08:00:00Z',
            config: { field: 'company', operator: 'is_empty', value: null, else: 'stop' },
          },
        ],
      },
    });

    const rule = await automationApi.getRule(7);
    const config = rule.steps[0].config;

    expect('value' in config).toBe(false);
    expect(config).toEqual({ field: 'company', operator: 'is_empty', else: 'stop' });
  });

  it('deletes a rule and resolves with no value on 204', async () => {
    vi.mocked(api.delete).mockResolvedValue({ data: '', status: 204 });

    await expect(automationApi.deleteRule(7)).resolves.toBeUndefined();
    expect(api.delete).toHaveBeenCalledWith('/automation/rules/7');
  });

  it('enables and disables a rule through their POST routes', async () => {
    vi.mocked(api.post).mockResolvedValueOnce({ data: { ...backendRule, enabled: true } });
    const enabled = await automationApi.enableRule(7);
    expect(api.post).toHaveBeenLastCalledWith('/automation/rules/7/enable');
    expect(enabled.enabled).toBe(true);

    vi.mocked(api.post).mockResolvedValueOnce({ data: { ...backendRule, enabled: false } });
    const disabled = await automationApi.disableRule(7);
    expect(api.post).toHaveBeenLastCalledWith('/automation/rules/7/disable');
    expect(disabled.enabled).toBe(false);
  });

  it('posts the dry-run body and returns the report', async () => {
    const report = {
      rule_id: 7,
      rule_version: 3,
      entity_type: 'lead',
      entity_id: 5,
      simulated_start: '2026-10-28T09:00:00Z',
      simulated_end: '2026-10-31T09:00:00Z',
      steps: [
        {
          position: 1,
          step_type: 'notify',
          outcome: 'succeeded',
          simulated_at: '2026-10-28T09:00:00Z',
          details: { recipient_id: 3 },
        },
      ],
    };
    vi.mocked(api.post).mockResolvedValue({ data: report });

    const result = await automationApi.dryRun(7, { entity_type: 'lead', entity_id: 5 });

    expect(api.post).toHaveBeenCalledWith('/automation/rules/7/dry-run', {
      entity_type: 'lead',
      entity_id: 5,
    });
    expect(result).toEqual(report);
  });

  it('lists runs with status, rule_id, entity_type and entity_id as params', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendRun], meta: { total: 9 } });

    const result = await automationApi.listRuns({
      offset: 0,
      limit: 10,
      status: 'waiting',
      rule_id: 7,
      entity_type: 'lead',
      entity_id: 5,
      sort_by: 'resume_at',
      sort_order: 'asc',
    });

    expect(api.get).toHaveBeenCalledWith('/automation/runs', {
      params: {
        offset: 0,
        limit: 10,
        status: 'waiting',
        rule_id: 7,
        entity_type: 'lead',
        entity_id: 5,
        sort_by: 'resume_at',
        sort_order: 'asc',
      },
    });
    expect(result.runs).toEqual([backendRun]);
    expect(result.total).toBe(9);
  });

  it('keeps a populated step log on getRun', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: { ...backendRun, steps: stepLog } });

    const run = await automationApi.getRun(31);

    expect(api.get).toHaveBeenCalledWith('/automation/runs/31');
    expect(run.steps).toEqual(stepLog);
    expect(run.dedupe_key).toBe('event:12:rule:7');
  });

  it('keeps a populated step log on cancelRun', async () => {
    vi.mocked(api.post).mockResolvedValue({
      data: { ...backendRun, status: 'cancelled', cancel_reason: 'manual', steps: stepLog },
    });

    const run = await automationApi.cancelRun(31);

    expect(api.post).toHaveBeenCalledWith('/automation/runs/31/cancel');
    expect(run.status).toBe('cancelled');
    expect(run.cancel_reason).toBe('manual');
    expect(run.steps).toEqual(stepLog);
  });

  it('leaves steps absent on list rows', async () => {
    vi.mocked(api.get).mockResolvedValue({ data: [backendRun], meta: { total: 1 } });

    const { runs } = await automationApi.listRuns({ entity_type: 'lead', entity_id: 5 });

    expect('steps' in runs[0]).toBe(false);
  });
});
