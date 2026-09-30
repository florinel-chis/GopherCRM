import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import { DEAL_STAGES } from '@/types';
import type { Deal, DealPipeline, DealStage, DealStageChange, PipelineStage, PipelineTotals } from '@/types';
import { transformCustomerFromBackend } from './customers';
import { transformLeadFromBackend } from './leads';

// GET /deals takes offset/limit, a free-text search over title and notes,
// server-side filters and a sort column from the API allowlist.
export type DealSortColumn =
  | 'id'
  | 'title'
  | 'stage'
  | 'amount_cents'
  | 'probability'
  | 'expected_close_date'
  | 'closed_at'
  | 'created_at'
  | 'updated_at';

export interface DealFilters {
  offset?: number;
  limit?: number;
  search?: string;
  stage?: DealStage;
  company_id?: number;
  customer_id?: number;
  owner_id?: number;
  // `true` keeps the stages other than won and lost; anything else is not sent.
  open?: boolean;
  sort_by?: DealSortColumn;
  sort_order?: 'asc' | 'desc';
}

export interface DealListResult {
  deals: Deal[];
  total: number;
}

// GET /deals/pipeline filters. Sales is always scoped to its own deals by the
// API, whatever owner_id says.
export interface DealPipelineFilters {
  owner_id?: number;
  company_id?: number;
}

export interface DealSubResourceParams {
  offset?: number;
  limit?: number;
}

// The body of POST and PUT /deals. Omitted `currency` takes the configured
// default, omitted `probability` the stage default. The foreign keys follow
// the company_id rule of leads: absent keeps, 0 clears.
export interface CreateDealData {
  title: string;
  stage?: DealStage;
  amount_cents?: number;
  currency?: string;
  probability?: number;
  expected_close_date?: string | null;
  lost_reason?: string;
  source?: string;
  notes?: string;
  company_id?: number;
  customer_id?: number;
  lead_id?: number;
  owner_id?: number;
}

// PUT /deals/:id is a full update: the same body as create.
export type UpdateDealData = CreateDealData;

export interface ChangeDealStageData {
  stage: DealStage;
  probability?: number;
  lost_reason?: string;
}

// A deal as the API sends it: the optional columns may be absent, and the
// preloaded customer and lead are raw rows (first_name, last_name, company).
type RawDeal = Omit<
  Deal,
  'customer' | 'lead' | 'expected_close_date' | 'closed_at' | 'lost_reason' | 'source' | 'notes'
> & {
  customer?: unknown;
  lead?: unknown;
  expected_close_date?: string | null;
  closed_at?: string | null;
  lost_reason?: string;
  source?: string;
  notes?: string;
};

// The preloaded customer and lead are mapped to the frontend shape the same
// way the list endpoints map them.
export const transformDealFromBackend = (input: unknown): Deal => {
  const raw = input as RawDeal;
  const deal: Deal = {
    ...raw,
    customer: undefined,
    lead: undefined,
    expected_close_date: raw.expected_close_date ?? null,
    closed_at: raw.closed_at ?? null,
    lost_reason: raw.lost_reason ?? '',
    source: raw.source ?? '',
    notes: raw.notes ?? '',
  };
  if (raw.customer) {
    deal.customer = transformCustomerFromBackend(raw.customer);
  }
  if (raw.lead) {
    deal.lead = transformLeadFromBackend(raw.lead);
  }
  return deal;
};

// The pipeline as the board and the dashboard read it: exactly the five
// stages in board order, each with a totals array. A stage the response
// lacks, or a null totals list (a Go nil slice), reads as empty.
export const normalizePipelineStages = (input: unknown): PipelineStage[] => {
  const rows = Array.isArray(input) ? (input as Partial<PipelineStage>[]) : [];
  return DEAL_STAGES.map(({ value }) => {
    const row = rows.find((candidate) => candidate?.stage === value);
    return {
      stage: value,
      count: typeof row?.count === 'number' ? row.count : 0,
      totals: Array.isArray(row?.totals) ? (row.totals as PipelineTotals[]) : [],
    };
  });
};

const toListResult = (response: ApiResponseWithMeta<unknown[]>): DealListResult => {
  const rows = Array.isArray(response.data) ? response.data : [];
  const deals = rows.map(transformDealFromBackend);
  return { deals, total: response.meta?.total ?? deals.length };
};

export const dealsApi = {
  // The array is the envelope payload; the pagination totals only exist in
  // `meta`, which the client interceptor re-attaches to the response.
  getDeals: async (filters?: DealFilters): Promise<DealListResult> => {
    const params = filters ? { ...filters } : undefined;
    if (params && params.open !== true) {
      delete params.open;
    }
    const response = (await api.get<unknown[]>('/deals', {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },

  // Carries owner, company, customer and lead when they are set.
  getDeal: async (id: number): Promise<Deal> => {
    const response = await api.get<unknown>(`/deals/${id}`);
    return transformDealFromBackend(response.data);
  },

  // 400 on validation and INVALID_REFERENCE (the message names the field),
  // 403 when sales sends another owner_id; callers surface those.
  createDeal: async (data: CreateDealData): Promise<Deal> => {
    const response = await api.post<unknown>('/deals', data);
    return transformDealFromBackend(response.data);
  },

  updateDeal: async (id: number, data: UpdateDealData): Promise<Deal> => {
    const response = await api.put<unknown>(`/deals/${id}`, data);
    return transformDealFromBackend(response.data);
  },

  // Entering won or lost sets closed_at (and the probability to 100 or 0);
  // leaving them clears closed_at and lost_reason. The same stage again is a
  // 200 without a history row.
  changeStage: async (id: number, data: ChangeDealStageData): Promise<Deal> => {
    const response = await api.post<unknown>(`/deals/${id}/stage`, data);
    return transformDealFromBackend(response.data);
  },

  // Oldest first; the first row has from_stage null.
  getDealHistory: async (id: number): Promise<DealStageChange[]> => {
    const response = await api.get<DealStageChange[]>(`/deals/${id}/history`);
    return Array.isArray(response.data) ? response.data : [];
  },

  // Admin only; the history rows stay.
  deleteDeal: async (id: number): Promise<void> => {
    await api.delete(`/deals/${id}`);
  },

  // Count and per-currency totals per stage, over the deals the caller sees.
  getPipeline: async (filters?: DealPipelineFilters): Promise<DealPipeline> => {
    const response = await api.get<{ stages?: unknown }>('/deals/pipeline', {
      params: filters,
    });
    return { stages: normalizePipelineStages(response.data?.stages) };
  },

  getCompanyDeals: async (
    companyId: number,
    params?: DealSubResourceParams
  ): Promise<DealListResult> => {
    const response = (await api.get<unknown[]>(`/companies/${companyId}/deals`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },

  getCustomerDeals: async (
    customerId: number,
    params?: DealSubResourceParams
  ): Promise<DealListResult> => {
    const response = (await api.get<unknown[]>(`/customers/${customerId}/deals`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },
};
