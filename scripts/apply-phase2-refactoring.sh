#!/bin/bash
#
# Phase 2 Refactoring Application Script
# This script helps apply the Phase 2 architectural improvements systematically
#
# Usage: ./scripts/apply-phase2-refactoring.sh [domain]
#   domain: user, lead, customer, ticket, task, apikey, config, dashboard, or "all"
#
# Example: ./scripts/apply-phase2-refactoring.sh user
#

set -e

DOMAIN=$1

if [ -z "$DOMAIN" ]; then
    echo "Usage: $0 <domain|all>"
    echo "Domains: user, lead, customer, ticket, task, apikey, config, dashboard, all"
    exit 1
fi

echo "🔧 Phase 2 Refactoring Script"
echo "======================================="
echo "Domain: $DOMAIN"
echo ""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

function print_success() {
    echo -e "${GREEN}✓${NC} $1"
}

function print_warning() {
    echo -e "${YELLOW}⚠${NC} $1"
}

function print_error() {
    echo -e "${RED}✗${NC} $1"
}

function print_info() {
    echo -e "${YELLOW}ℹ${NC} $1"
}

# Checklist for manual refactoring
echo "📋 Refactoring Checklist for $DOMAIN domain:"
echo ""
echo "□ 1. Update Repository Interface"
echo "    File: internal/repository/interfaces.go"
echo "    Action: Add 'ctx context.Context' as first parameter to all $DOMAIN methods"
echo ""
echo "□ 2. Update Repository Implementation"
echo "    File: internal/repository/${DOMAIN}_repository.go"
echo "    Actions:"
echo "      - Add 'ctx context.Context' parameter to all methods"
echo "      - Replace 'r.db' with 'r.db.WithContext(ctx)'"
echo "      - Add transaction variants (*Tx methods)"
echo "      - Add preloading to List methods"
echo ""
echo "□ 3. Update Service Interface"
echo "    File: internal/service/interfaces.go"
echo "    Action: Add 'ctx context.Context' as first parameter"
echo ""
echo "□ 4. Update Service Implementation"
echo "    File: internal/service/${DOMAIN}_service.go"
echo "    Actions:"
echo "      - Add 'logger *logging.Logger' to struct"
echo "      - Add to constructor"
echo "      - Add 'ctx context.Context' to all methods"
echo "      - Pass ctx to repository calls"
echo "      - Replace utils.Logger with s.logger.WithContext(ctx)"
echo "      - Use transactions for multi-step operations"
echo ""
echo "□ 5. Update Handler"
echo "    File: internal/handler/${DOMAIN}_handler.go"
echo "    Actions:"
echo "      - Add 'ctx := c.Request.Context()' at start of each method"
echo "      - Pass ctx to service calls"
echo ""
echo "□ 6. Update Tests"
echo "    Files: internal/*/${DOMAIN}_*_test.go"
echo "    Actions:"
echo "      - Add 'ctx := context.Background()' to tests"
echo "      - Pass ctx to all method calls"
echo "      - Update mock expectations to include ctx"
echo ""
echo "□ 7. Run Tests"
echo "    Command: go test ./internal/repository ./internal/service ./internal/handler -v"
echo ""
echo "□ 8. Update Integration Tests"
echo "    Files: tests/${DOMAIN}_integration_test.go"
echo ""

# Run tests if requested
if [ "$DOMAIN" = "test" ]; then
    print_info "Running tests..."
    go test ./... -v
    exit 0
fi

# Build check
echo ""
print_info "Checking if code compiles..."
if go build ./...; then
    print_success "Code compiles successfully"
else
    print_error "Compilation failed - fix errors before proceeding"
    exit 1
fi

echo ""
echo "✅ Complete the checklist above, then run:"
echo "   ./scripts/apply-phase2-refactoring.sh test"
echo ""
