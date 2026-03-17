-- Remove default configurations
-- This migration removes the default system configurations

DELETE FROM configurations WHERE config_key IN (
    'leads.conversion.allowed_statuses',
    'leads.conversion.require_notes', 
    'leads.conversion.auto_assign_owner',
    'ui.theme.primary_color',
    'general.company_name',
    'security.session_timeout_hours',
    'tickets.auto_assign_support'
);