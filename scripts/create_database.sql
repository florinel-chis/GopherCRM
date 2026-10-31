-- Create the gocrm database (the default DB_NAME in internal/config/config.go and .env.example)
CREATE DATABASE IF NOT EXISTS gocrm CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;

-- Show confirmation
SELECT 'Database gocrm created successfully' AS message;
