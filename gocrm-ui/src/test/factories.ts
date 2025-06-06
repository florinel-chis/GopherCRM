import type { User, Lead, Customer, Ticket, Task, Comment } from '@/types';

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