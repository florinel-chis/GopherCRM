import type {
  User,
  Lead,
  Customer,
  Ticket,
  Task,
  Comment,
  Label,
  Company,
  Deal,
  DealStageChange,
  Product,
  Quote,
  QuoteLine,
  QuoteStatusChange,
  AutomationRule,
  AutomationRun,
  AutomationRunStep,
  AutomationDryRunReport,
  Notification,
} from '@/types';

export const createMockUser = (overrides?: Partial<User>): User => ({
  id: 1,
  email: 'test@example.com',
  first_name: 'Test',
  last_name: 'User',
  role: 'sales',
  is_active: true,
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockLead = (overrides?: Partial<Lead>): Lead => ({
  id: 1,
  company_name: 'Test Company',
  contact_name: 'John Doe',
  email: 'john@testcompany.com',
  phone: '+1234567890',
  status: 'new',
  source: 'website',
  notes: 'Test notes',
  owner_id: 1,
  owner: createMockUser(),
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockCustomer = (overrides?: Partial<Customer>): Customer => ({
  id: 1,
  company_name: 'Customer Company',
  contact_name: 'Jane Smith',
  email: 'jane@customer.com',
  phone: '+0987654321',
  address: '123 Main St',
  city: 'New York',
  state: 'NY',
  country: 'USA',
  postal_code: '10001',
  website: 'https://customer.com',
  industry: 'Technology',
  annual_revenue: 1000000,
  employee_count: 50,
  total_revenue: 500000,
  notes: 'Important customer',
  is_active: true,
  owner_id: 1,
  owner: createMockUser(),
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockTicket = (overrides?: Partial<Ticket>): Ticket => ({
  id: 1,
  subject: 'Test Ticket',
  description: 'This is a test ticket description',
  status: 'open',
  priority: 'medium',
  customer_id: 1,
  customer: createMockCustomer(),
  assigned_to: createMockUser({ id: 2, email: 'agent@example.com' }),
  assigned_to_id: 2,
  created_by: createMockUser(),
  created_by_id: 1,
  comments: [],
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockTask = (overrides?: Partial<Task>): Task => ({
  id: 1,
  title: 'Test Task',
  description: 'This is a test task',
  status: 'pending',
  priority: 'medium',
  due_date: '2024-12-31T00:00:00Z',
  assigned_to: 1,
  assignee: createMockUser(),
  created_by: 1,
  creator: createMockUser(),
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockLabel = (overrides?: Partial<Label>): Label => ({
  id: 1,
  name: 'Urgent',
  color: '#D62728',
  task_count: 0,
  ...overrides,
});

export const createMockComment = (overrides?: Partial<Comment>): Comment => ({
  id: 1,
  content: 'This is a test comment',
  ticket_id: 1,
  user_id: 1,
  user: createMockUser(),
  created_at: '2024-01-01T00:00:00Z',
  updated_at: '2024-01-01T00:00:00Z',
  ...overrides,
});

export const createMockCompany = (overrides?: Partial<Company>): Company => ({
  id: 1,
  name: 'Acme Widgets',
  domain: 'acme.example',
  website: 'https://www.acme.example',
  industry: 'Manufacturing',
  employee_range: '51-200',
  phone: '+40 21 555 0100',
  address: '1 Foundry Lane',
  city: 'Cluj',
  state: 'CJ',
  country: 'Romania',
  postal_code: '400001',
  notes: '',
  owner_id: 1,
  owner: createMockUser(),
  created_at: '2026-09-29T08:00:00Z',
  updated_at: '2026-09-29T08:00:00Z',
  ...overrides,
});

export const createMockDeal = (overrides?: Partial<Deal>): Deal => ({
  id: 1,
  title: 'Website redesign',
  stage: 'qualification',
  amount_cents: 1250000,
  currency: 'EUR',
  probability: 10,
  expected_close_date: '2026-12-15',
  closed_at: null,
  lost_reason: '',
  source: 'referral',
  notes: '',
  owner_id: 1,
  owner: createMockUser(),
  created_at: '2026-09-29T08:00:00Z',
  updated_at: '2026-09-29T08:00:00Z',
  ...overrides,
});

export const createMockDealStageChange = (
  overrides?: Partial<DealStageChange>
): DealStageChange => ({
  id: 1,
  deal_id: 1,
  from_stage: null,
  to_stage: 'qualification',
  changed_by_id: 1,
  changed_by: { id: 1, first_name: 'Test', last_name: 'User', email: 'test@example.com' },
  changed_at: '2026-09-29T08:00:00Z',
  ...overrides,
});

export const createMockProduct = (overrides?: Partial<Product>): Product => ({
  id: 1,
  name: 'Widget',
  sku: 'SKU-001',
  description: 'A standard widget',
  unit_price_cents: 1250,
  currency: 'EUR',
  is_active: true,
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
  ...overrides,
});

export const createMockQuoteLine = (overrides?: Partial<QuoteLine>): QuoteLine => ({
  id: 1,
  quote_id: 42,
  product_id: 1,
  position: 1,
  description: 'Widget',
  quantity: 2,
  unit_price_cents: 1250,
  discount_bp: 0,
  line_total_cents: 2500,
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
  ...overrides,
});

export const createMockQuote = (overrides?: Partial<Quote>): Quote => ({
  id: 42,
  number: 'Q-000042',
  title: 'Website redesign quote',
  status: 'draft',
  currency: 'EUR',
  subtotal_cents: 0,
  discount_cents: 0,
  total_cents: 0,
  valid_until: null,
  notes: '',
  sent_at: null,
  closed_at: null,
  last_emailed_at: null,
  owner_id: 1,
  owner: { id: 1, first_name: 'Test', last_name: 'User' },
  lines: [],
  created_at: '2026-10-26T08:00:00Z',
  updated_at: '2026-10-26T08:00:00Z',
  ...overrides,
});

export const createMockQuoteStatusChange = (
  overrides?: Partial<QuoteStatusChange>
): QuoteStatusChange => ({
  id: 1,
  quote_id: 42,
  from_status: null,
  to_status: 'draft',
  changed_by_id: 1,
  changed_by: { id: 1, first_name: 'Test', last_name: 'User' },
  changed_at: '2026-10-26T08:00:00Z',
  ...overrides,
});

// A disabled lead rule on lead_created with one notify step, as saved.
export const createMockAutomationRule = (overrides?: Partial<AutomationRule>): AutomationRule => ({
  id: 1,
  created_at: '2026-10-28T08:00:00Z',
  updated_at: '2026-10-28T08:00:00Z',
  deleted_at: null,
  name: 'Welcome new leads',
  description: '',
  entity_type: 'lead',
  trigger_type: 'lead_created',
  enabled: false,
  version: 1,
  schedule_cursor: 0,
  created_by_id: 1,
  trigger: { hour_utc: 0 },
  steps: [
    {
      id: 1,
      rule_id: overrides?.id ?? 1,
      position: 1,
      step_type: 'notify',
      created_at: '2026-10-28T08:00:00Z',
      config: {
        recipient: 'owner',
        title: 'New lead',
        body: 'Follow up with {{ lead.first_name }}',
      },
    },
  ],
  ...overrides,
});

// A pending run of rule #1 on lead #1; list rows carry no steps. The rule
// name is empty (a rule row that is gone) unless a test names it.
export const createMockAutomationRun = (overrides?: Partial<AutomationRun>): AutomationRun => ({
  id: 1,
  created_at: '2026-10-28T09:00:00Z',
  updated_at: '2026-10-28T09:00:00Z',
  rule_id: 1,
  rule_name: '',
  rule_deleted: false,
  rule_version: 1,
  entity_type: 'lead',
  entity_id: 1,
  event_id: 1,
  dedupe_key: 'event:1:rule:1',
  status: 'pending',
  next_position: 1,
  attempts: 0,
  ...overrides,
});

export const createMockAutomationRunStep = (
  overrides?: Partial<AutomationRunStep>,
): AutomationRunStep => ({
  id: 1,
  run_id: 1,
  position: 1,
  step_type: 'notify',
  outcome: 'succeeded',
  executed_at: '2026-10-28T09:00:00Z',
  details: { recipient_id: 1, notification_id: 1 },
  ...overrides,
});

export const createMockNotification = (overrides?: Partial<Notification>): Notification => ({
  id: 1,
  created_at: '2026-10-28T09:00:00Z',
  updated_at: '2026-10-28T09:00:00Z',
  user_id: 1,
  title: 'New lead',
  body: 'Follow up with Jane',
  entity_type: 'lead',
  entity_id: 1,
  run_id: 1,
  ...overrides,
});

export const createMockDryRunReport = (
  overrides?: Partial<AutomationDryRunReport>,
): AutomationDryRunReport => ({
  rule_id: 1,
  rule_version: 1,
  draft: false,
  entity_type: 'lead',
  entity_id: 1,
  simulated_start: '2026-10-28T09:00:00Z',
  simulated_end: '2026-10-28T09:00:00Z',
  steps: [
    {
      position: 1,
      step_type: 'notify',
      outcome: 'succeeded',
      simulated_at: '2026-10-28T09:00:00Z',
      details: { recipient_id: 1 },
    },
  ],
  ...overrides,
});
