-- Migration: Add Performance Indexes
-- Date: 2025-08-05
-- Description: Add composite and performance indexes to improve query performance

-- Composite indexes for common query patterns
CREATE INDEX idx_leads_owner_status ON leads(owner_id, status);
CREATE INDEX idx_tickets_customer_status ON tickets(customer_id, status);
CREATE INDEX idx_tickets_assigned_status ON tickets(assigned_to_id, status);
CREATE INDEX idx_tasks_assigned_status ON tasks(assigned_to_id, status);
CREATE INDEX idx_users_active_role ON users(is_active, role);
CREATE INDEX idx_api_keys_active_user ON api_keys(is_active, user_id);

-- Performance indexes
CREATE INDEX idx_users_last_login_at ON users(last_login_at);