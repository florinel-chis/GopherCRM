#!/bin/bash
# Clean up test users from the database
# This script removes all users with emails matching the test pattern

echo "Cleaning up test users from GopherCRM database..."

# Connect to MySQL and delete test users
mysql -u root gophercrm -e "DELETE FROM users WHERE email LIKE 'test_%@example.com';" 2>/dev/null

if [ $? -eq 0 ]; then
    # Count remaining test users
    COUNT=$(mysql -u root gophercrm -s -e "SELECT COUNT(*) FROM users WHERE email LIKE 'test_%@example.com';" 2>/dev/null)
    echo "✅ Test users cleaned up successfully!"
    echo "   Remaining test users: $COUNT"
else
    echo "❌ Error: Could not connect to database or clean up test users"
    echo "   Make sure MySQL is running and the gophercrm database exists"
fi

# Optional: Show all current users (commented out for privacy)
# echo ""
# echo "Current users in database:"
# mysql -u root gophercrm -e "SELECT id, email, first_name, last_name, role FROM users ORDER BY id DESC LIMIT 10;"