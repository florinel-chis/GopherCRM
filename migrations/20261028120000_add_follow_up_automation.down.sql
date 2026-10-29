-- Reverse of 20261028120000_add_follow_up_automation: the six tables,
-- children first so no foreign key blocks a drop. Nothing else was touched by
-- the up migration, so nothing else is touched here.

DROP TABLE IF EXISTS notifications;
DROP TABLE IF EXISTS automation_run_steps;
DROP TABLE IF EXISTS automation_runs;
DROP TABLE IF EXISTS automation_events;
DROP TABLE IF EXISTS automation_steps;
DROP TABLE IF EXISTS automation_rules;
