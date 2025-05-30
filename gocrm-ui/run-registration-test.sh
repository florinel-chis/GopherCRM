#!/bin/bash
# Quick script to run registration tests with proper setup

echo "🚀 Running GopherCRM Registration E2E Tests"
echo "==========================================="

# Check if backend is running
if ! curl -s http://localhost:8080/health > /dev/null; then
    echo "❌ Backend server is not running on http://localhost:8080"
    echo "   Please start the backend first: cd .. && make run"
    exit 1
fi

echo "✅ Backend server is running"

# Clean up any existing test users
echo "🧹 Cleaning up test users..."
./cleanup-test-users.sh

echo ""
echo "🎭 Starting Playwright tests..."
echo ""

# Run the tests
npm run test:e2e:headed

echo ""
echo "📊 To view the test report, run: npm run test:e2e:report"