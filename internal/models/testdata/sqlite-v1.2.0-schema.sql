-- SQLite schema of GopherCRM v1.2.0 (commit c2b6588), as auto-migration wrote it
-- into a fresh file, dumped verbatim from sqlite_master. The rows below are the
-- smallest data set that makes the v1.2.0 -> next upgrade fail when the migrator
-- rebuilds a table while foreign keys are enforced: tickets and tasks reference
-- customers, a task references a lead, and a converted lead references its
-- customer. sqlite_sequence is omitted because SQLite creates it itself.
--
-- Executed by TestMigrateDatabase_SQLiteUpgradeFromV120 and friends
-- (database_sqlite_upgrade_test.go), statement by statement, on a file opened
-- through the production path. Keep the DDL as the release wrote it; only the
-- data is hand-made.

CREATE TABLE `aeo_answers` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`run_id` integer NOT NULL,`prompt_id` integer NOT NULL,`provider` varchar(40) NOT NULL,`model` varchar(120) NOT NULL,`attempt` integer NOT NULL DEFAULT 1,`answer_text` longtext,`brand_mentioned` numeric NOT NULL DEFAULT false,`first_mention_pos` integer NOT NULL,`competitor_mentions` text,`latency_ms` integer NOT NULL DEFAULT 0,`error` text);
CREATE INDEX `idx_aeo_answers_brand_mentioned` ON `aeo_answers`(`brand_mentioned`);
CREATE INDEX `idx_aeo_answers_deleted_at` ON `aeo_answers`(`deleted_at`);
CREATE INDEX `idx_aeo_answers_prompt_id` ON `aeo_answers`(`prompt_id`);
CREATE INDEX `idx_aeo_answers_provider` ON `aeo_answers`(`provider`);
CREATE INDEX `idx_aeo_answers_run_id` ON `aeo_answers`(`run_id`);
CREATE TABLE `aeo_citations` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`answer_id` integer NOT NULL,`url` varchar(1024) NOT NULL,`domain` varchar(255) NOT NULL,`is_owned` numeric NOT NULL DEFAULT false,`competitor_name` varchar(120),CONSTRAINT `fk_aeo_answers_citations` FOREIGN KEY (`answer_id`) REFERENCES `aeo_answers`(`id`));
CREATE INDEX `idx_aeo_citations_answer_id` ON `aeo_citations`(`answer_id`);
CREATE INDEX `idx_aeo_citations_deleted_at` ON `aeo_citations`(`deleted_at`);
CREATE INDEX `idx_aeo_citations_domain` ON `aeo_citations`(`domain`);
CREATE TABLE `aeo_profiles` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`brand_name` varchar(120) NOT NULL,`description` text,`brand_aliases` text,`owned_domains` text,`competitors` text);
CREATE INDEX `idx_aeo_profiles_deleted_at` ON `aeo_profiles`(`deleted_at`);
CREATE TABLE `aeo_prompts` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`text` varchar(500) NOT NULL,`is_active` numeric NOT NULL DEFAULT true,`created_by_id` integer);
CREATE INDEX `idx_aeo_prompts_created_by_id` ON `aeo_prompts`(`created_by_id`);
CREATE INDEX `idx_aeo_prompts_deleted_at` ON `aeo_prompts`(`deleted_at`);
CREATE INDEX `idx_aeo_prompts_is_active` ON `aeo_prompts`(`is_active`);
CREATE TABLE `aeo_runs` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`trigger` varchar(20) NOT NULL,`status` varchar(20) NOT NULL,`started_at` datetime NOT NULL,`completed_at` datetime,`total_queries` integer NOT NULL DEFAULT 0,`failed_queries` integer NOT NULL DEFAULT 0,`triggered_by_id` integer);
CREATE INDEX `idx_aeo_runs_deleted_at` ON `aeo_runs`(`deleted_at`);
CREATE INDEX `idx_aeo_runs_status` ON `aeo_runs`(`status`);
CREATE INDEX `idx_aeo_runs_trigger` ON `aeo_runs`(`trigger`);
CREATE INDEX `idx_aeo_runs_triggered_by_id` ON `aeo_runs`(`triggered_by_id`);
CREATE TABLE `api_keys` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`name` varchar(100) NOT NULL,`key_hash` text NOT NULL,`prefix` varchar(8) NOT NULL,`user_id` integer,`last_used_at` datetime,`expires_at` datetime,`is_active` numeric DEFAULT true,CONSTRAINT `fk_users_api_keys` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_api_keys_deleted_at` ON `api_keys`(`deleted_at`);
CREATE UNIQUE INDEX `idx_api_keys_key_hash` ON `api_keys`(`key_hash`);
CREATE TABLE `bulk_operation_items` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`operation_id` integer NOT NULL,`resource_id` integer,`status` varchar(20) NOT NULL DEFAULT "pending",`error` text,`data` text,CONSTRAINT `fk_bulk_operations_items` FOREIGN KEY (`operation_id`) REFERENCES `bulk_operations`(`id`));
CREATE INDEX `idx_bulk_operation_items_deleted_at` ON `bulk_operation_items`(`deleted_at`);
CREATE TABLE `bulk_operations` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`user_id` integer NOT NULL,`resource_type` varchar(50) NOT NULL,`type` varchar(20) NOT NULL,`status` varchar(20) NOT NULL DEFAULT "pending",`total_items` integer NOT NULL,`success_count` integer DEFAULT 0,`failure_count` integer DEFAULT 0,CONSTRAINT `fk_bulk_operations_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_bulk_operations_deleted_at` ON `bulk_operations`(`deleted_at`);
CREATE TABLE `configurations` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`config_key` varchar(255) NOT NULL,`value` text,`type` varchar(20) NOT NULL,`category` varchar(50) NOT NULL,`description` varchar(500),`default_value` text,`is_system` numeric DEFAULT false,`is_read_only` numeric DEFAULT false,`valid_values` text,`is_sensitive` numeric DEFAULT false);
CREATE INDEX `idx_configurations_deleted_at` ON `configurations`(`deleted_at`);
CREATE UNIQUE INDEX `idx_configurations_key` ON `configurations`(`config_key`);
CREATE TABLE `customers` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`first_name` varchar(100) NOT NULL,`last_name` varchar(100) NOT NULL,`email` varchar(255) NOT NULL,`phone` varchar(50),`company` varchar(200),`position` varchar(100),`address` varchar(255),`city` varchar(100),`state` varchar(100),`country` varchar(100),`postal_code` varchar(20),`notes` text,`user_id` integer,`assigned_to_id` integer,CONSTRAINT `fk_customers_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`),CONSTRAINT `fk_customers_assigned_to` FOREIGN KEY (`assigned_to_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_customers_deleted_at` ON `customers`(`deleted_at`);
CREATE UNIQUE INDEX `idx_customers_email` ON `customers`(`email`);
CREATE TABLE `form_confirmation_tokens` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`submission_id` integer NOT NULL,`token_hash` varchar(64) NOT NULL,`expires_at` datetime NOT NULL,`used_at` datetime);
CREATE INDEX `idx_form_confirmation_tokens_deleted_at` ON `form_confirmation_tokens`(`deleted_at`);
CREATE INDEX `idx_form_confirmation_tokens_submission_id` ON `form_confirmation_tokens`(`submission_id`);
CREATE UNIQUE INDEX `idx_form_confirmation_tokens_token_hash` ON `form_confirmation_tokens`(`token_hash`);
CREATE TABLE `form_submissions` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`form_id` integer NOT NULL,`data` mediumtext,`email` varchar(255),`status` varchar(20) NOT NULL,`spam_reason` varchar(100),`lead_id` integer,`ip_address` varchar(45),`user_agent` varchar(255),`referrer` varchar(512),`confirmed_at` datetime);
CREATE INDEX `idx_form_submissions_deleted_at` ON `form_submissions`(`deleted_at`);
CREATE INDEX `idx_form_submissions_email` ON `form_submissions`(`email`);
CREATE INDEX `idx_form_submissions_form_id` ON `form_submissions`(`form_id`);
CREATE INDEX `idx_form_submissions_lead_id` ON `form_submissions`(`lead_id`);
CREATE INDEX `idx_form_submissions_status` ON `form_submissions`(`status`);
CREATE TABLE `forms` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`name` varchar(255) NOT NULL,`description` text,`public_id` varchar(32) NOT NULL,`status` varchar(20) NOT NULL DEFAULT "draft",`fields` text,`submit_action` varchar(20) NOT NULL DEFAULT "message",`thank_you_message` text,`redirect_url` varchar(512),`consent_text` text,`notify_emails` text,`double_opt_in` numeric NOT NULL DEFAULT false,`confirmation_subject` varchar(255),`confirmation_body` text,`follow_up_subject` varchar(255),`follow_up_body` text,`content_url` varchar(512),`captcha_enabled` numeric NOT NULL DEFAULT false,`create_lead` numeric NOT NULL,`default_owner_id` integer,`allowed_domains` text,`created_by_id` integer);
CREATE INDEX `idx_forms_created_by_id` ON `forms`(`created_by_id`);
CREATE INDEX `idx_forms_default_owner_id` ON `forms`(`default_owner_id`);
CREATE INDEX `idx_forms_deleted_at` ON `forms`(`deleted_at`);
CREATE UNIQUE INDEX `idx_forms_public_id` ON `forms`(`public_id`);
CREATE TABLE `labels` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`name` varchar(50) NOT NULL,`color` varchar(7) NOT NULL);
CREATE INDEX `idx_labels_deleted_at` ON `labels`(`deleted_at`);
CREATE UNIQUE INDEX `idx_labels_name` ON `labels`(`name`);
CREATE TABLE `leads` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`first_name` varchar(100) NOT NULL,`last_name` varchar(100) NOT NULL,`email` varchar(255) NOT NULL,`phone` varchar(50),`company` varchar(200),`position` varchar(100),`source` varchar(100),`status` varchar(20) NOT NULL DEFAULT "new",`classification` varchar(20) DEFAULT "unclassified",`external_id` varchar(255),`notes` text,`owner_id` integer,`customer_id` integer,CONSTRAINT `fk_leads_customer` FOREIGN KEY (`customer_id`) REFERENCES `customers`(`id`),CONSTRAINT `fk_users_leads` FOREIGN KEY (`owner_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_leads_deleted_at` ON `leads`(`deleted_at`);
CREATE TABLE `password_reset_tokens` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`user_id` integer NOT NULL,`token_hash` varchar(255) NOT NULL,`expires_at` datetime NOT NULL,`used_at` datetime,CONSTRAINT `fk_password_reset_tokens_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_password_reset_tokens_deleted_at` ON `password_reset_tokens`(`deleted_at`);
CREATE UNIQUE INDEX `idx_password_reset_tokens_token_hash` ON `password_reset_tokens`(`token_hash`);
CREATE TABLE `refresh_tokens` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`user_id` integer NOT NULL,`token_hash` varchar(255) NOT NULL,`expires_at` datetime NOT NULL,`is_revoked` numeric DEFAULT false,CONSTRAINT `fk_refresh_tokens_user` FOREIGN KEY (`user_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_refresh_tokens_deleted_at` ON `refresh_tokens`(`deleted_at`);
CREATE UNIQUE INDEX `idx_refresh_tokens_token_hash` ON `refresh_tokens`(`token_hash`);
CREATE TABLE `task_labels` (`task_id` integer,`label_id` integer,PRIMARY KEY (`task_id`,`label_id`),CONSTRAINT `fk_task_labels_task` FOREIGN KEY (`task_id`) REFERENCES `tasks`(`id`),CONSTRAINT `fk_task_labels_label` FOREIGN KEY (`label_id`) REFERENCES `labels`(`id`));
CREATE TABLE `tasks` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`title` varchar(255) NOT NULL,`description` text,`status` varchar(20) NOT NULL DEFAULT "pending",`priority` varchar(20) NOT NULL DEFAULT "medium",`due_date` datetime,`assigned_to_id` integer,`lead_id` integer,`customer_id` integer,CONSTRAINT `fk_tasks_customer` FOREIGN KEY (`customer_id`) REFERENCES `customers`(`id`),CONSTRAINT `fk_users_tasks` FOREIGN KEY (`assigned_to_id`) REFERENCES `users`(`id`),CONSTRAINT `fk_tasks_lead` FOREIGN KEY (`lead_id`) REFERENCES `leads`(`id`));
CREATE INDEX `idx_tasks_deleted_at` ON `tasks`(`deleted_at`);
CREATE TABLE `tickets` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`title` varchar(255) NOT NULL,`description` text NOT NULL,`status` varchar(20) NOT NULL DEFAULT "open",`priority` varchar(20) NOT NULL DEFAULT "medium",`customer_id` integer,`assigned_to_id` integer,`resolution` text,CONSTRAINT `fk_tickets_assigned_to` FOREIGN KEY (`assigned_to_id`) REFERENCES `users`(`id`),CONSTRAINT `fk_customers_tickets` FOREIGN KEY (`customer_id`) REFERENCES `customers`(`id`));
CREATE INDEX `idx_tickets_deleted_at` ON `tickets`(`deleted_at`);
CREATE TABLE `users` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`email` varchar(255) NOT NULL,`password` varchar(255) NOT NULL,`first_name` varchar(100) NOT NULL,`last_name` varchar(100) NOT NULL,`role` varchar(20) NOT NULL DEFAULT "customer",`is_active` numeric DEFAULT true,`last_login_at` datetime,`failed_login_attempts` integer DEFAULT 0,`locked_until` datetime);
CREATE INDEX `idx_users_deleted_at` ON `users`(`deleted_at`);
CREATE UNIQUE INDEX `idx_users_email` ON `users`(`email`);

-- Data. Every row that follows references an earlier one, so the order matters
-- while foreign keys are enforced.
INSERT INTO `users` (`id`,`created_at`,`updated_at`,`email`,`password`,`first_name`,`last_name`,`role`,`is_active`) VALUES
  (1,'2026-09-25 08:00:00','2026-09-25 08:00:00','admin@fixture.invalid','$2a$10$not-a-real-hash','Fixture','Admin','admin',1);
INSERT INTO `customers` (`id`,`created_at`,`updated_at`,`first_name`,`last_name`,`email`,`company`,`assigned_to_id`) VALUES
  (1,'2026-09-25 08:01:00','2026-09-25 08:01:00','Cust','One','cust1@fixture.invalid','Cust One Ltd',1),
  (2,'2026-09-25 08:02:00','2026-09-25 08:02:00','Lead','Two','lead2@fixture.invalid','Lead Two GmbH',1);
INSERT INTO `leads` (`id`,`created_at`,`updated_at`,`first_name`,`last_name`,`email`,`company`,`source`,`status`,`owner_id`,`customer_id`) VALUES
  (1,'2026-09-25 08:03:00','2026-09-25 08:03:00','Lead','One','lead1@fixture.invalid','Lead One AG','web','new',1,NULL),
  (2,'2026-09-25 08:04:00','2026-09-25 08:05:00','Lead','Two','lead2@fixture.invalid','Lead Two GmbH','web','converted',1,2);
INSERT INTO `tickets` (`id`,`created_at`,`updated_at`,`title`,`description`,`status`,`priority`,`customer_id`,`assigned_to_id`) VALUES
  (1,'2026-09-25 08:06:00','2026-09-25 08:06:00','Ticket on customer 1','fixture','open','medium',1,1);
INSERT INTO `tasks` (`id`,`created_at`,`updated_at`,`title`,`status`,`priority`,`assigned_to_id`,`lead_id`,`customer_id`) VALUES
  (1,'2026-09-25 08:07:00','2026-09-25 08:07:00','Task on customer 1','pending','medium',1,NULL,1),
  (2,'2026-09-25 08:08:00','2026-09-25 08:08:00','Task on lead 1','pending','medium',1,1,NULL);
INSERT INTO `labels` (`id`,`created_at`,`updated_at`,`name`,`color`) VALUES
  (1,'2026-09-25 08:09:00','2026-09-25 08:09:00','upgrade','#336699');
INSERT INTO `task_labels` (`task_id`,`label_id`) VALUES (1,1);
INSERT INTO `api_keys` (`id`,`created_at`,`updated_at`,`name`,`key_hash`,`prefix`,`user_id`,`is_active`) VALUES
  (1,'2026-09-25 08:10:00','2026-09-25 08:10:00','upgrade-key','hmac$0000000000000000000000000000000000000000000000000000000000000000','00000000',1,1);
INSERT INTO `refresh_tokens` (`id`,`created_at`,`updated_at`,`user_id`,`token_hash`,`expires_at`,`is_revoked`) VALUES
  (1,'2026-09-25 08:11:00','2026-09-25 08:11:00',1,'1111111111111111111111111111111111111111111111111111111111111111','2026-10-25 08:11:00',0);
