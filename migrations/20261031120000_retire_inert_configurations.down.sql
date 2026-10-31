-- Restore the two retired entries as 20250806120002_add_default_configurations
-- seeded them. Their earlier values are not recoverable. They still have no
-- effect, and an application that retires them deletes them again on start.
INSERT IGNORE INTO configurations (config_key, value, type, category, description, default_value, is_system, is_read_only, valid_values, created_at, updated_at) VALUES
('security.session_timeout_hours', '24', 'integer', 'security', 'Session timeout in hours', '24', 1, 0, '[1, 8, 24, 48, 72, 168]', NOW(), NOW()),
('tickets.auto_assign_support', 'true', 'boolean', 'tickets', 'Whether to automatically assign tickets to available support users', 'false', 0, 0, '', NOW(), NOW());
