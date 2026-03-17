-- Rollback initial schema migration for GopherCRM
-- Drops all tables in reverse dependency order

-- Drop foreign key constraints and tables
DROP TABLE IF EXISTS refresh_tokens;
DROP TABLE IF EXISTS configurations;
DROP TABLE IF EXISTS api_keys;
DROP TABLE IF EXISTS tasks;
DROP TABLE IF EXISTS tickets;
DROP TABLE IF EXISTS leads;
DROP TABLE IF EXISTS customers;
DROP TABLE IF EXISTS users;