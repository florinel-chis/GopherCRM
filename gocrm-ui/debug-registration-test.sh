#!/bin/bash
# Debug script for registration tests with verbose output

echo "🔍 Debug Mode: GopherCRM Registration E2E Tests"
echo "=============================================="

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
echo "🎭 Starting Playwright tests in debug mode..."
echo "   - Tests run one by one"
echo "   - Slower timeouts"
echo "   - Video recording enabled"
echo ""

# Run specific test or all tests with slow config
if [ -n "$1" ]; then
    echo "Running specific test: $1"
    npx playwright test --config=playwright.config.slow.ts --headed -g "$1"
else
    echo "Running all registration tests..."
    npx playwright test --config=playwright.config.slow.ts --headed
fi

echo ""
echo "📊 To view the test report, run: npm run test:e2e:report"