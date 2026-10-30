import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type {
  AutomationDryRunReport,
  AutomationEntityType,
  AutomationRule,
  AutomationRun,
  AutomationRunStatus,
  AutomationStep,
  AutomationStepConfig,
  AutomationStepType,
  AutomationTrigger,
  AutomationTriggerType,
} from '@/types';

// Sort columns from the API allowlists (internal/utils/sort.go). An empty
// sort_by means created_at desc whatever sort_order says.
export type AutomationRuleSortColumn =
  | 'id'
  | 'name'
  | 'entity_type'
  | 'trigger_type'
  | 'enabled'
  | 'version'
  | 'created_at'
  | 'updated_at';
export type AutomationRunSortColumn = 'id' | 'created_at' | 'updated_at' | 'status' | 'resume_at';

export interface AutomationRuleFilters {
  offset?: number;
  limit?: number;
  entity_type?: AutomationEntityType;
  enabled?: boolean;
  sort_by?: AutomationRuleSortColumn;
  sort_order?: 'asc' | 'desc';
}

// Sales is narrowed to runs on the leads and deals it owns by the API itself.
// entity_id is accepted alone but is ambiguous: always send entity_type too.
export interface AutomationRunFilters {
  offset?: number;
  limit?: number;
  status?: AutomationRunStatus;
  rule_id?: number;
  entity_type?: AutomationEntityType;
  entity_id?: number;
  sort_by?: AutomationRunSortColumn;
  sort_order?: 'asc' | 'desc';
}

export interface AutomationStepInput {
  step_type: AutomationStepType;
  config: AutomationStepConfig;
}

// Body of POST and PUT. PUT without `enabled` keeps the stored switch.
export interface AutomationRuleInput {
  name: string;
  description: string;
  entity_type: AutomationEntityType;
  trigger_type: AutomationTriggerType;
  trigger: Partial<AutomationTrigger>;
  enabled?: boolean;
  steps: AutomationStepInput[];
}

export interface AutomationDryRunInput {
  entity_type: AutomationEntityType;
  entity_id: number;
}

// Body of POST /automation/rules/dry-run: an unsaved rule body (the create
// body; `enabled` is ignored) and the record to simulate it against.
export interface AutomationDraftDryRunInput {
  rule: AutomationRuleInput;
  entity_type: AutomationEntityType;
  entity_id: number;
}

export interface AutomationRuleListResult {
  rules: AutomationRule[];
  total: number;
}

export interface AutomationRunListResult {
  runs: AutomationRun[];
  total: number;
}

type RawAutomationRule = Omit<AutomationRule, 'description' | 'trigger' | 'steps'> & {
  description?: string | null;
  trigger?: AutomationTrigger | null;
  steps?: AutomationStep[] | null;
};

// An explicit JSON null condition value means "no value" on the server, the
// same as an absent one (is_empty / is_not_empty); dropping it keeps the form
// model to a single representation.
const withoutNullValue = (step: AutomationStep): AutomationStep => {
  if (step.config?.value !== null) {
    return step;
  }
  const config: AutomationStepConfig = { ...step.config };
  delete config.value;
  return { ...step, config };
};

export const transformRuleFromBackend = (input: unknown): AutomationRule => {
  const raw = input as RawAutomationRule;
  return {
    ...raw,
    description: raw.description ?? '',
    trigger: raw.trigger ?? { hour_utc: 0 },
    steps: Array.isArray(raw.steps) ? raw.steps.map(withoutNullValue) : [],
  };
};

// Run steps arrive on GET /automation/runs/:id and on the cancel response;
// list rows carry none. Whatever arrived is kept as it is.
export const transformRunFromBackend = (input: unknown): AutomationRun => input as AutomationRun;

const rowsOf = (response: ApiResponseWithMeta<unknown[]>): unknown[] =>
  Array.isArray(response.data) ? response.data : [];

export const automationApi = {
  // Steps are preloaded on every row. `meta.total` is omitted when it is 0.
  listRules: async (filters?: AutomationRuleFilters): Promise<AutomationRuleListResult> => {
    const response = (await api.get<unknown[]>('/automation/rules', {
      params: filters,
    })) as ApiResponseWithMeta<unknown[]>;
    const rules = rowsOf(response).map(transformRuleFromBackend);
    return { rules, total: response.meta?.total ?? rules.length };
  },

  getRule: async (id: number): Promise<AutomationRule> => {
    const response = await api.get<unknown>(`/automation/rules/${id}`);
    return transformRuleFromBackend(response.data);
  },

  // 400 INVALID_INPUT (validation) or INVALID_REFERENCE (unknown, inactive or
  // customer user): `message` is the first refusal and `details.fields`
  // lists every refused field as { field, code, message }, with step paths
  // such as steps[2].config.title (zero-based).
  createRule: async (data: AutomationRuleInput): Promise<AutomationRule> => {
    const response = await api.post<unknown>('/automation/rules', data);
    return transformRuleFromBackend(response.data);
  },

  // Bumps the version and cancels the rule's active runs (rule_changed).
  updateRule: async (id: number, data: AutomationRuleInput): Promise<AutomationRule> => {
    const response = await api.put<unknown>(`/automation/rules/${id}`, data);
    return transformRuleFromBackend(response.data);
  },

  // 204 with no body; active runs are cancelled (rule_deleted).
  deleteRule: async (id: number): Promise<void> => {
    await api.delete(`/automation/rules/${id}`);
  },

  enableRule: async (id: number): Promise<AutomationRule> => {
    const response = await api.post<unknown>(`/automation/rules/${id}/enable`);
    return transformRuleFromBackend(response.data);
  },

  // Cancels the rule's active runs (rule_disabled).
  disableRule: async (id: number): Promise<AutomationRule> => {
    const response = await api.post<unknown>(`/automation/rules/${id}/disable`);
    return transformRuleFromBackend(response.data);
  },

  // Simulates the stored rule against one record and writes nothing.
  dryRun: async (id: number, data: AutomationDryRunInput): Promise<AutomationDryRunReport> => {
    const response = await api.post<AutomationDryRunReport>(
      `/automation/rules/${id}/dry-run`,
      data,
    );
    return response.data;
  },

  // Simulates unsaved form values (a new rule or unsaved edits) and saves
  // nothing; the same 400 answers as createRule. The report has draft: true.
  dryRunDraft: async (data: AutomationDraftDryRunInput): Promise<AutomationDryRunReport> => {
    const response = await api.post<AutomationDryRunReport>('/automation/rules/dry-run', data);
    return response.data;
  },

  // Rows carry no steps; each names its rule (rule_name, rule_deleted).
  listRuns: async (filters?: AutomationRunFilters): Promise<AutomationRunListResult> => {
    const response = (await api.get<unknown[]>('/automation/runs', {
      params: filters,
    })) as ApiResponseWithMeta<unknown[]>;
    const runs = rowsOf(response).map(transformRunFromBackend);
    return { runs, total: response.meta?.total ?? runs.length };
  },

  // With its step log; 404 for sales outside its scope.
  getRun: async (id: number): Promise<AutomationRun> => {
    const response = await api.get<unknown>(`/automation/runs/${id}`);
    return transformRunFromBackend(response.data);
  },

  // Admin only. 409 INVALID_STATUS_TRANSITION once the run has finished.
  cancelRun: async (id: number): Promise<AutomationRun> => {
    const response = await api.post<unknown>(`/automation/runs/${id}/cancel`);
    return transformRunFromBackend(response.data);
  },
};
