#!/bin/bash

# Cleanup script for admin test data
# This script removes test data created during admin entity tests

echo "🧹 Cleaning up admin test data..."

# Database connection details
DB_HOST=${DB_HOST:-localhost}
DB_PORT=${DB_PORT:-3306}
DB_NAME=${DB_NAME:-gocrm}
DB_USER=${DB_USER:-root}
DB_PASS=${DB_PASS:-}

# Function to execute SQL commands
execute_sql() {
    local sql="$1"
    if [ -z "$DB_PASS" ]; then
        mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" "$DB_NAME" -e "$sql"
    else
        mysql -h "$DB_HOST" -P "$DB_PORT" -u "$DB_USER" -p"$DB_PASS" "$DB_NAME" -e "$sql"
    fi
}

# Clean up test users (keep only non-test admin users)
echo "Cleaning up test users..."
execute_sql "DELETE FROM users WHERE email LIKE '%admin_%@example.com' OR email LIKE '%test_%@example.com' OR first_name LIKE 'SearchUser%' OR first_name LIKE 'BatchUser%' OR first_name LIKE 'SearchCustomer%' OR first_name LIKE 'BatchCustomer%';"

# Clean up test leads
echo "Cleaning up test leads..."
execute_sql "DELETE FROM leads WHERE email LIKE '%@example.com' OR first_name LIKE 'SearchLead%' OR first_name LIKE 'BatchLead%' OR first_name LIKE 'TestLead%' OR first_name LIKE 'SearchTest%' OR first_name LIKE 'AdminSearchTest%';"

# Clean up test customers
echo "Cleaning up test customers..."
execute_sql "DELETE FROM customers WHERE email LIKE '%@example.com' OR first_name LIKE 'SearchCustomer%' OR first_name LIKE 'BatchCustomer%' OR first_name LIKE 'TestCustomer%' OR first_name LIKE 'SearchTest%' OR first_name LIKE 'AdminSearchTest%' OR first_name = 'Minimal' OR first_name LIKE 'Updated%';"

# Clean up test tickets
echo "Cleaning up test tickets..."
execute_sql "DELETE FROM tickets WHERE title LIKE '%SearchTicket%' OR title LIKE '%BatchTicket%' OR title LIKE '%Test%' OR title LIKE 'Updated%' OR title LIKE '%AdminSearchTest%';"

# Clean up test tasks
echo "Cleaning up test tasks..."
execute_sql "DELETE FROM tasks WHERE title LIKE '%SearchTask%' OR title LIKE '%BatchTask%' OR title LIKE '%Test%' OR title LIKE 'Updated%' OR title LIKE '%AdminSearchTest%' OR title = 'Minimal Task';"

echo "✅ Admin test data cleanup completed!"

# Optional: Reset auto-increment counters to keep IDs clean
echo "Resetting auto-increment counters..."
execute_sql "ALTER TABLE users AUTO_INCREMENT = 1;"
execute_sql "ALTER TABLE leads AUTO_INCREMENT = 1;"
execute_sql "ALTER TABLE customers AUTO_INCREMENT = 1;"
execute_sql "ALTER TABLE tickets AUTO_INCREMENT = 1;"
execute_sql "ALTER TABLE tasks AUTO_INCREMENT = 1;"

echo "✅ All cleanup operations completed successfully!"