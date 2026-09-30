-- What a failed first start of the release after v1.2.0 leaves behind on top of
-- sqlite-v1.2.0-schema.sql: the migrator created `companies` and added
-- `customers.company_id` outside any transaction before the table rebuild that
-- adds the foreign key failed. The column therefore exists without its
-- constraint and index, and `leads` is untouched. Applied by
-- TestMigrateDatabase_SQLiteUpgradeFromHalfAppliedV120.

CREATE TABLE `companies` (`id` integer PRIMARY KEY AUTOINCREMENT,`created_at` datetime,`updated_at` datetime,`deleted_at` datetime,`name` varchar(200) NOT NULL,`domain` varchar(255),`website` varchar(255),`industry` varchar(100),`employee_range` varchar(20),`phone` varchar(50),`address` varchar(255),`city` varchar(100),`state` varchar(100),`country` varchar(100),`postal_code` varchar(20),`notes` text,`owner_id` integer,CONSTRAINT `fk_companies_owner` FOREIGN KEY (`owner_id`) REFERENCES `users`(`id`));
CREATE INDEX `idx_companies_owner_id` ON `companies`(`owner_id`);
CREATE INDEX `idx_companies_deleted_at` ON `companies`(`deleted_at`);
ALTER TABLE `customers` ADD `company_id` integer;
