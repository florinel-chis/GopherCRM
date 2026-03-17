-- Insert default configurations
-- This migration adds the default system configurations

INSERT IGNORE INTO configurations (config_key, value, type, category, description, default_value, is_system, is_read_only, valid_values) VALUES 
('leads.conversion.allowed_statuses', '["qualified", "contacted"]', 'array', 'leads', 'Lead statuses that allow conversion to customer', '["qualified"]', 1, 0, ''),
('leads.conversion.require_notes', 'false', 'boolean', 'leads', 'Whether conversion notes are required when converting leads', 'false', 1, 0, ''),
('leads.conversion.auto_assign_owner', 'true', 'boolean', 'leads', 'Whether to automatically assign the lead owner as customer owner', 'true', 1, 0, ''),
('ui.theme.primary_color', '#1976d2', 'string', 'ui', 'Primary theme color for the application', '#1976d2', 0, 0, ''),
('general.company_name', 'GopherCRM', 'string', 'general', 'Company name displayed in the application', 'GopherCRM', 0, 0, ''),
('security.session_timeout_hours', '24', 'integer', 'security', 'Session timeout in hours', '24', 1, 0, '[1, 8, 24, 48, 72, 168]'),
('tickets.auto_assign_support', 'true', 'boolean', 'tickets', 'Whether to automatically assign tickets to available support users', 'false', 0, 0, '');