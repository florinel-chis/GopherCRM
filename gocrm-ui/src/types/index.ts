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
  created_at: string;
  updated_at: string;
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
  created_at: string;
  updated_at: string;
  completed_at?: string;
}

export interface APIKey {
  id: number;
  name: string;
  key_hash: string;
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