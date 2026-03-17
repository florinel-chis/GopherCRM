-- Rollback performance indexes migration
-- Drop additional indexes that were added for performance

-- Drop performance indexes
DROP INDEX idx_users_last_login_at ON users;

-- Drop composite indexes for common query patterns  
DROP INDEX idx_api_keys_active_user ON api_keys;
DROP INDEX idx_users_active_role ON users;
DROP INDEX idx_tasks_assigned_status ON tasks;
DROP INDEX idx_tickets_assigned_status ON tickets;
DROP INDEX idx_tickets_customer_status ON tickets;
DROP INDEX idx_leads_owner_status ON leads;

-- Drop foreign key indexes
DROP INDEX idx_api_keys_user_id ON api_keys;
DROP INDEX idx_tasks_assigned_to_id ON tasks;
DROP INDEX idx_tickets_assigned_to_id ON tickets;
DROP INDEX idx_tickets_customer_id ON tickets;
DROP INDEX idx_leads_owner_id ON leads;