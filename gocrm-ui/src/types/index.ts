export interface User {
  id: number;
  email: string;
  first_name: string;
  last_name: string;
  role: 'admin' | 'sales' | 'support' | 'customer';
  is_active: boolean;
  created_at: string;
  updated_at: string;
  last_login_at?: string;
}

export interface Lead {
  id: number;
  company_name: string;
  contact_name: string;
  email: string;
  phone: string;
  status: 'new' | 'contacted' | 'qualified' | 'converted' | 'lost';
  source: string;
  notes: string;
  owner_id: number;
  owner?: User;
  // Curated link to a Company record. `company_name` (the free-text column)
  // stays the forms contract; the link is set from the CRM only.
  company_id?: number;
  company_record?: Company;
  created_at: string;
  updated_at: string;
}

export interface Customer {
  id: number;
  company_name: string;
  contact_name: string;
  email: string;
  phone: string;
  address?: string;
  city?: string;
  state?: string;
  country?: string;
  postal_code?: string;
  website?: string;
  industry?: string;
  annual_revenue?: number;
  employee_count?: number;
  total_revenue: number;
  notes?: string;
  is_active: boolean;
  owner_id: number;
  owner?: User;
  assigned_to_id?: number;
  assigned_to?: User;
  company_id?: number;
  company_record?: Company;
  created_at: string;
  updated_at: string;
}

export const COMPANY_EMPLOYEE_RANGES = [
  '1-10',
  '11-50',
  '51-200',
  '201-500',
  '501-1000',
  '1000+',
] as const;

export type CompanyEmployeeRange = (typeof COMPANY_EMPLOYEE_RANGES)[number] | '';

export interface Company {
  id: number;
  name: string;
  domain: string;
  website: string;
  industry: string;
  employee_range: CompanyEmployeeRange;
  phone: string;
  address: string;
  city: string;
  state: string;
  country: string;
  postal_code: string;
  notes: string;
  owner_id?: number;
  owner?: User;
  // Only GET /companies/:id reports the counts.
  customer_count?: number;
  lead_count?: number;
  created_at: string;
  updated_at: string;
}

// Deal pipeline stages in board order. `probability` is the default the API
// applies when a create or stage change carries none; `won` is always 100 and
// `lost` always 0 whatever is sent.
export const DEAL_STAGES = [
  { value: 'qualification', label: 'Qualification', probability: 10 },
  { value: 'proposal', label: 'Proposal', probability: 40 },
  { value: 'negotiation', label: 'Negotiation', probability: 70 },
  { value: 'won', label: 'Won', probability: 100 },
  { value: 'lost', label: 'Lost', probability: 0 },
] as const;

export type DealStage = (typeof DEAL_STAGES)[number]['value'];

export const DEAL_STAGE_VALUES = DEAL_STAGES.map((stage) => stage.value) as [
  DealStage,
  ...DealStage[],
];

export const isClosedDealStage = (stage: DealStage): boolean =>
  stage === 'won' || stage === 'lost';

export const dealStageLabel = (stage: DealStage): string =>
  DEAL_STAGES.find((candidate) => candidate.value === stage)?.label ?? stage;

export const dealStageDefaultProbability = (stage: DealStage): number =>
  DEAL_STAGES.find((candidate) => candidate.value === stage)?.probability ?? 0;

export interface Deal {
  id: number;
  title: string;
  stage: DealStage;
  // Integer minor units with an ISO 4217 code; the UI divides by 100 only
  // for display and never sums across currencies.
  amount_cents: number;
  currency: string;
  probability: number;
  // Calendar date as YYYY-MM-DD, or null. Compared in local time from the
  // string itself so a timezone can never shift the day.
  expected_close_date: string | null;
  // ISO timestamp, set while the stage is won or lost, null otherwise.
  closed_at: string | null;
  lost_reason: string;
  source: string;
  notes: string;
  company_id?: number;
  company?: Company;
  customer_id?: number;
  customer?: Customer;
  lead_id?: number;
  lead?: Lead;
  owner_id: number;
  owner?: User;
  created_at: string;
  updated_at: string;
}

// One row of GET /deals/:id/history. `from_stage` is null on the row written
// when the deal was created.
export interface DealStageChange {
  id: number;
  deal_id: number;
  from_stage: DealStage | null;
  to_stage: DealStage;
  changed_by_id: number;
  changed_by?: Pick<User, 'id' | 'first_name' | 'last_name' | 'email'>;
  changed_at: string;
}

// One currency's share of a pipeline stage (GET /deals/pipeline). Money is
// never summed across currencies. `weighted_cents` is the sum of
// amount_cents × probability / 100, rounded once by the API.
export interface PipelineTotals {
  currency: string;
  amount_cents: number;
  weighted_cents: number;
}

// One stage of the pipeline. The API sends all five stages in board order;
// an empty stage has count 0 and no totals.
export interface PipelineStage {
  stage: DealStage;
  count: number;
  totals: PipelineTotals[];
}

export interface DealPipeline {
  stages: PipelineStage[];
}

// GET /dashboard/pipeline: the pipeline plus the deals won in the current
// calendar month (UTC), totalled per currency.
export interface WonThisMonth {
  count: number;
  totals: Pick<PipelineTotals, 'currency' | 'amount_cents'>[];
}

export interface DashboardPipeline extends DealPipeline {
  won_this_month: WonThisMonth;
}

export interface Ticket {
  id: number;
  subject: string;
  description: string;
  status: 'open' | 'in_progress' | 'resolved' | 'closed';
  priority: 'low' | 'medium' | 'high' | 'urgent';
  customer_id: number;
  customer?: Customer;
  assigned_to?: User;
  assigned_to_id?: number;
  created_by?: User;
  created_by_id: number;
  comments?: Comment[];
  created_at: string;
  updated_at: string;
  closed_at?: string;
}

export interface Comment {
  id: number;
  content: string;
  ticket_id: number;
  user_id: number;
  user?: User;
  created_at: string;
  updated_at: string;
}

export interface Label {
  id: number;
  name: string;
  color: string;
  // Only the list endpoint reports how many tasks carry the label.
  task_count?: number;
}

export interface Task {
  id: number;
  title: string;
  description: string;
  status: 'pending' | 'in_progress' | 'completed' | 'cancelled';
  priority: 'low' | 'medium' | 'high';
  due_date: string;
  assigned_to: number;
  assignee?: User;
  created_by: number;
  creator?: User;
  labels?: Label[];
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

// The stored hash is deliberately absent: models.APIKey tags it `json:"-"`, so
// it is never on the wire, and the endpoint transform deletes it defensively.
export interface APIKey {
  id: number;
  name: string;
  // Short non-secret head of the key (models.APIKey.Prefix), the only part the
  // API ever returns after creation — enough to identify a key, useless as one.
  prefix?: string;
  last_used_at?: string;
  expires_at?: string;
  user_id: number;
  user?: User;
  created_at: string;
  is_active: boolean;
}

export interface LoginRequest {
  email: string;
  password: string;
  remember_me?: boolean;
}

export interface LoginResponse {
  token: string;
  refresh_token?: string;
  user: User;
}

export interface RegisterRequest {
  email: string;
  password: string;
  first_name: string;
  last_name: string;
  role?: 'admin' | 'sales' | 'support' | 'customer';
}

export interface APIError {
  message: string;
  code?: string;
  details?: Record<string, any>;
}

export interface PaginationParams {
  page?: number;
  limit?: number;
  sort_by?: string;
  sort_order?: 'asc' | 'desc';
}

export interface PaginatedResponse<T> {
  data: T[];
  total: number;
  page: number;
  limit: number;
  total_pages: number;
}

export interface DashboardStats {
  total_leads: number;
  total_customers: number;
  open_tickets: number;
  pending_tasks: number;
  conversion_rate: number;
  average_ticket_resolution_time: number;
}