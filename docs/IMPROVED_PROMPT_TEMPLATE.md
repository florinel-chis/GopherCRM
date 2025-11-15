# Improved Prompt Template: Magento 2 Integration for GopherCRM

## Purpose
This document demonstrates how to transform a vague feature request into a comprehensive, actionable implementation plan using the "ultrathink" methodology.

---

## ❌ ORIGINAL VAGUE PROMPT

> "Add Magento 2 integration to the CRM system - manage customers, quotes, orders, product list. Include tests."

**Problems with this prompt:**
- No specific business objectives
- Unclear scope (what exactly to integrate?)
- No acceptance criteria
- Missing technical specifications
- Vague testing requirements
- No timeline or effort estimates
- No consideration of data flow, security, or edge cases

---

## ✅ IMPROVED "ULTRATHINK" PROMPT

### Context & Business Objectives

**As the product owner of GopherCRM, I need to integrate with Magento 2 to enable our sales team to create quotes and track orders within the CRM, eliminating the need to switch between systems.**

**Business Goals:**
1. Reduce quote creation time by 50% (from 15 min to 7.5 min avg)
2. Increase quote-to-order conversion rate by 25%
3. Provide 100% visibility into customer order history
4. Enable sales team to work offline with local product catalog
5. Achieve 80% user adoption within first month of launch

**Target Users:**
- Sales Representatives (30 users) - Create quotes, track orders
- Sales Managers (5 users) - View analytics, manage pipeline
- System Administrators (2 users) - Configure integration, monitor sync

---

### Functional Requirements (Prioritized)

#### MUST HAVE (v1.0 - MVP)

**FR-1: Product Catalog Sync**
- Sync products from Magento to GopherCRM every 30 minutes
- Include: SKU, name, price, inventory, categories, images
- Enable search by SKU/name and filter by category/stock status
- Real-time inventory check when adding to quote
- **Acceptance Criteria:**
  - [ ] Sales rep can search products in <200ms
  - [ ] Inventory levels accurate within 5 minutes
  - [ ] Support catalogs up to 10,000 SKUs
  - [ ] Product images display correctly

**FR-2: Quote Creation & Management**
- Create quotes with customer association
- Add products with custom pricing/discounts
- Calculate taxes automatically (via Magento tax rules)
- Save as draft, send to customer, generate PDF
- Track quote status: draft/sent/accepted/rejected/expired
- **Acceptance Criteria:**
  - [ ] Quote creation takes <3 minutes
  - [ ] Tax calculation matches Magento exactly
  - [ ] PDF generation completes in <2 seconds
  - [ ] Quote emails delivered successfully

**FR-3: Quote-to-Order Conversion**
- One-click conversion of accepted quote to Magento order
- Preserve custom pricing and discounts
- Auto-create customer in Magento if doesn't exist
- Link CRM quote to Magento order
- **Acceptance Criteria:**
  - [ ] Conversion completes in <10 seconds
  - [ ] Custom pricing transferred correctly
  - [ ] Zero data loss during conversion
  - [ ] Order appears in Magento admin immediately

**FR-4: Order Sync & Tracking**
- Sync all Magento orders to CRM every 15 minutes
- Real-time updates via webhooks (order status changes)
- Display order timeline, items, totals, addresses
- Link orders to CRM customers and quotes
- **Acceptance Criteria:**
  - [ ] Orders sync with 99.9% accuracy
  - [ ] Status updates appear within 1 minute (webhook)
  - [ ] Historical orders (last 2 years) imported on first sync
  - [ ] Sales rep sees complete customer order history

**FR-5: Integration Configuration**
- Admin UI to configure Magento OAuth credentials
- Test connection before saving
- Enable/disable specific sync types
- View sync logs and error reports
- **Acceptance Criteria:**
  - [ ] Configuration takes <10 minutes
  - [ ] Connection test provides clear feedback
  - [ ] Failed syncs automatically retry
  - [ ] Errors logged with actionable messages

#### SHOULD HAVE (v1.1 - Future)
- Multi-Magento instance support
- Customer segmentation import
- Inventory reservations
- RMA/returns tracking

#### NICE TO HAVE (v2.0 - Later)
- Shopify integration
- Multi-currency support
- Custom product configurators

---

### Technical Specifications

#### Architecture

**Integration Pattern:**
- **Product Catalog:** Scheduled batch sync (every 30 min) + webhook for changes
- **Orders:** Scheduled incremental sync (every 15 min) + webhooks for real-time updates
- **Quote Conversion:** Synchronous API call to Magento
- **Customer Sync:** Bidirectional (CRM → Magento on quote convert, Magento → CRM on order sync)

**Data Flow:**
```
1. Product Sync:
   Cron Job → Magento API → Transform → GopherCRM DB → Cache

2. Quote Conversion:
   CRM Quote → Magento Cart API → Magento Order API → CRM Order

3. Order Sync:
   Magento Webhook → GopherCRM API → Validate → DB → UI Update
   Fallback: Cron Job → Magento Orders API → Incremental Sync
```

**Technology Stack:**
- **Magento API:** REST API v1 (Magento 2.4+)
- **Authentication:** OAuth 2.0 with token refresh
- **Database:** MySQL 8.0+ with new tables (products, quotes, orders)
- **Caching:** Redis for product catalog (30 min TTL)
- **Queue:** Go channels + persistent queue for retry logic
- **Webhooks:** Signature validation with HMAC-SHA256

#### Database Schema (Summary)

```sql
-- 8 new tables
products (id, magento_id, sku, name, price, stock_qty, categories, images, ...)
product_categories (id, magento_id, name, parent_id, path, ...)
quotes (id, quote_number, customer_id, status, items, totals, valid_until, ...)
quote_items (id, quote_id, product_id, qty, price, discount, ...)
orders (id, magento_order_id, customer_id, quote_id, status, totals, ...)
order_status_history (id, order_id, status, comment, created_at, ...)
integration_configs (id, type, base_url, credentials, sync_settings, ...)
sync_logs (id, sync_type, status, records_processed, errors, duration, ...)
```

#### API Endpoints (New)

```
Products:
  GET    /api/v1/products
  GET    /api/v1/products/:id
  GET    /api/v1/products/search?q=:query
  GET    /api/v1/products/sku/:sku
  POST   /api/v1/products/sync (admin)

Quotes:
  POST   /api/v1/quotes
  GET    /api/v1/quotes
  GET    /api/v1/quotes/:id
  PUT    /api/v1/quotes/:id
  DELETE /api/v1/quotes/:id
  POST   /api/v1/quotes/:id/items
  POST   /api/v1/quotes/:id/send
  POST   /api/v1/quotes/:id/convert
  GET    /api/v1/quotes/:id/pdf

Orders:
  GET    /api/v1/orders
  GET    /api/v1/orders/:id
  GET    /api/v1/orders/customer/:custId

Webhooks:
  POST   /api/v1/webhooks/magento/order-created
  POST   /api/v1/webhooks/magento/order-updated
  POST   /api/v1/webhooks/magento/product-updated

Integration:
  POST   /api/v1/integrations/magento/configure
  GET    /api/v1/integrations/magento/status
  POST   /api/v1/integrations/magento/test
  GET    /api/v1/integrations/magento/logs
```

---

### Testing Requirements (Comprehensive)

#### 1. Unit Tests (40% of effort)

**Backend:**
- All Magento API client methods (mocked HTTP responses)
- Quote calculation logic (taxes, discounts, totals)
- Product search/filter logic
- Order sync transformation logic
- **Coverage Target:** >80%
- **Tools:** Go testing, testify/mock
- **Files:** `internal/**/*_test.go`

**Frontend:**
- Quote form validation
- Total calculation display
- Product search component
- **Coverage Target:** >70%
- **Tools:** Vitest, React Testing Library
- **Files:** `gocrm-ui/src/**/*.test.tsx`

#### 2. Integration Tests (30% of effort)

**Scenarios:**
```go
TestProductSync_FullSync() {
  // Mock Magento API returning 100 products
  // Verify all 100 inserted into DB correctly
  // Verify categories linked
  // Verify images stored
}

TestQuoteConversion_Success() {
  // Create quote with 3 products
  // Mock Magento cart/order creation success
  // Verify order created in CRM
  // Verify quote status = "converted"
  // Verify link between quote and order
}

TestOrderSync_IncrementalWithWebhook() {
  // Sync 10 existing orders
  // Trigger webhook for order status change
  // Verify status updated in CRM within 1 second
  // Verify no duplicate orders
}

TestQuoteConversion_MagentoError() {
  // Mock Magento API returning 500 error
  // Verify quote status unchanged
  // Verify error logged
  // Verify user sees helpful error message
}
```

**Tools:**
- Go testing with testcontainers (MySQL)
- Mock Magento HTTP server
- **Files:** `tests/integration/*_test.go`

#### 3. API Contract Tests (10% of effort)

**Postman Collections:**
```json
{
  "name": "GopherCRM Magento Integration",
  "tests": [
    {
      "name": "Create Quote - Success",
      "request": "POST /api/v1/quotes",
      "tests": [
        "Status code is 201",
        "Response has quote_number",
        "Grand total calculated correctly"
      ]
    },
    {
      "name": "Convert Quote - Product Out of Stock",
      "request": "POST /api/v1/quotes/:id/convert",
      "tests": [
        "Status code is 400",
        "Error message mentions out of stock",
        "Quote status unchanged"
      ]
    }
  ]
}
```

**Execution:**
- Newman (Postman CLI) in CI/CD
- **Coverage:** 100% of API endpoints
- **Files:** `tests/api/*.postman_collection.json`

#### 4. End-to-End Tests with Playwright (20% of effort)

**Critical User Journeys:**

```typescript
// Quote Creation Flow
test('Sales rep creates quote and converts to order', async ({ page }) => {
  // Login as sales rep
  await loginAs(page, 'sales@example.com');

  // Navigate to create quote
  await page.goto('/quotes/create');

  // Select customer
  await page.fill('[data-testid=customer-search]', 'Acme Corp');
  await page.click('text=Acme Corp');

  // Add product 1
  await page.click('[data-testid=add-product]');
  await page.fill('[data-testid=product-search]', 'Widget');
  await page.click('[data-testid=product-result]:has-text("Premium Widget")');
  await page.fill('[data-testid=qty]', '10');
  await page.fill('[data-testid=discount]', '10'); // 10% discount

  // Add product 2
  await page.click('[data-testid=add-product]');
  await page.fill('[data-testid=product-search]', 'Gadget');
  await page.click('[data-testid=product-result]:has-text("Super Gadget")');
  await page.fill('[data-testid=qty]', '5');

  // Verify totals calculated
  await expect(page.locator('[data-testid=subtotal]')).toContainText('$949.90');
  await expect(page.locator('[data-testid=tax]')).toContainText('$76.00');
  await expect(page.locator('[data-testid=grand-total]')).toContainText('$1,025.90');

  // Save quote
  await page.click('[data-testid=save-quote]');

  // Verify redirect to quote detail
  await expect(page).toHaveURL(/\/quotes\/\d+/);
  const quoteNumber = await page.locator('[data-testid=quote-number]').textContent();
  expect(quoteNumber).toMatch(/Q-2025-\d+/);

  // Change status to accepted
  await page.click('[data-testid=status-dropdown]');
  await page.click('text=Accepted');
  await page.click('[data-testid=save-status]');

  // Convert to order
  await page.click('[data-testid=convert-to-order]');
  await page.click('[data-testid=confirm-conversion]');

  // Wait for conversion (may take a few seconds)
  await page.waitForURL(/\/orders\/\d+/, { timeout: 10000 });

  // Verify order created
  await expect(page.locator('[data-testid=order-number]')).toBeVisible();
  const orderNumber = await page.locator('[data-testid=order-number]').textContent();
  expect(orderNumber).toMatch(/#\d+/);

  // Verify Magento link present
  await expect(page.locator('[data-testid=magento-order-link]')).toBeVisible();

  // Verify order total matches quote
  await expect(page.locator('[data-testid=grand-total]')).toContainText('$1,025.90');
});

// Product Search Flow
test('Sales rep searches for product with low stock', async ({ page }) => {
  await loginAs(page, 'sales@example.com');
  await page.goto('/products');

  await page.fill('[data-testid=product-search]', 'Widget');
  await page.waitForSelector('[data-testid=product-result]');

  const lowStockProduct = page.locator('[data-testid=product-result]:has([data-testid=stock-badge]:has-text("Low Stock"))');
  await expect(lowStockProduct).toBeVisible();

  // Click to view details
  await lowStockProduct.click();

  // Verify stock level displayed
  await expect(page.locator('[data-testid=stock-qty]')).toContainText('5');
});

// Order Tracking Flow
test('Sales rep views customer order history', async ({ page }) => {
  await loginAs(page, 'sales@example.com');
  await page.goto('/customers/123');

  // Click orders tab
  await page.click('[data-testid=orders-tab]');

  // Verify orders loaded
  const orders = page.locator('[data-testid=order-row]');
  await expect(orders).toHaveCountGreaterThan(0);

  // Click on recent order
  await orders.first().click();

  // Verify order details
  await expect(page.locator('[data-testid=order-items]')).toBeVisible();
  await expect(page.locator('[data-testid=order-timeline]')).toBeVisible();
});

// Admin Integration Setup Flow
test('Admin configures Magento integration', async ({ page }) => {
  await loginAs(page, 'admin@example.com');
  await page.goto('/admin/integrations/magento');

  // Fill configuration
  await page.fill('[data-testid=base-url]', 'https://magento.example.com');
  await page.fill('[data-testid=consumer-key]', 'test_key_12345');
  await page.fill('[data-testid=consumer-secret]', 'test_secret_67890');
  await page.fill('[data-testid=access-token]', 'test_token_abcde');
  await page.fill('[data-testid=access-token-secret]', 'test_token_secret_fghij');

  // Test connection
  await page.click('[data-testid=test-connection]');
  await expect(page.locator('[data-testid=connection-status]')).toContainText('Connected', { timeout: 5000 });

  // Save configuration
  await page.click('[data-testid=save-config]');
  await expect(page.locator('[data-testid=success-message]')).toContainText('Configuration saved');

  // Trigger manual product sync
  await page.goto('/admin/sync-logs');
  await page.click('[data-testid=sync-products]');

  // Wait for sync to complete
  await expect(page.locator('[data-testid=sync-status]')).toContainText('Completed', { timeout: 30000 });
  await expect(page.locator('[data-testid=products-synced]')).toContainText(/\d+ products/);
});
```

**Test Organization:**
```
gocrm-ui/tests/
├── e2e/
│   ├── fixtures/
│   │   ├── base.ts (custom fixtures)
│   │   ├── magento-mock.ts (mock Magento server)
│   │   └── test-data-factory.ts (create test data)
│   ├── products/
│   │   ├── product-browsing.spec.ts
│   │   ├── product-search.spec.ts
│   │   └── product-detail.spec.ts
│   ├── quotes/
│   │   ├── quote-creation.spec.ts
│   │   ├── quote-management.spec.ts
│   │   ├── quote-conversion.spec.ts
│   │   └── quote-pdf.spec.ts
│   ├── orders/
│   │   ├── order-viewing.spec.ts
│   │   ├── order-filtering.spec.ts
│   │   └── order-history.spec.ts
│   └── admin/
│       ├── magento-config.spec.ts
│       ├── sync-logs.spec.ts
│       └── integration-status.spec.ts
└── api/
    └── *.postman_collection.json
```

**Playwright Configuration:**
```typescript
// playwright.config.ts
export default defineConfig({
  testDir: './tests/e2e',
  timeout: 30000,
  retries: process.env.CI ? 2 : 0,
  use: {
    baseURL: 'http://localhost:5173',
    trace: 'on-first-retry',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'firefox',
      use: { ...devices['Desktop Firefox'] },
    },
  ],
  webServer: {
    command: 'npm run dev',
    port: 5173,
    reuseExistingServer: !process.env.CI,
  },
});
```

#### 5. Performance Testing

**Load Tests (k6):**
```javascript
import http from 'k6/http';
import { check } from 'k6';

export let options = {
  stages: [
    { duration: '2m', target: 50 },  // Ramp up to 50 users
    { duration: '5m', target: 50 },  // Stay at 50 users
    { duration: '2m', target: 100 }, // Ramp up to 100 users
    { duration: '5m', target: 100 }, // Stay at 100 users
    { duration: '2m', target: 0 },   // Ramp down
  ],
  thresholds: {
    http_req_duration: ['p(95)<200'], // 95% of requests under 200ms
    http_req_failed: ['rate<0.01'],   // <1% errors
  },
};

export default function () {
  // Product search
  let searchRes = http.get(`${__ENV.BASE_URL}/api/v1/products/search?q=widget`);
  check(searchRes, {
    'product search status 200': (r) => r.status === 200,
    'product search fast': (r) => r.timings.duration < 200,
  });

  // Create quote
  let quotePayload = JSON.stringify({
    customer_id: 123,
    items: [{ sku: 'WIDGET-001', qty: 10, price: 49.99 }],
  });
  let quoteRes = http.post(`${__ENV.BASE_URL}/api/v1/quotes`, quotePayload, {
    headers: { 'Content-Type': 'application/json' },
  });
  check(quoteRes, {
    'quote creation status 201': (r) => r.status === 201,
  });
}
```

---

### Success Metrics & Acceptance Criteria

#### Technical Metrics
- [ ] **API Performance:** p95 response time <200ms for all endpoints
- [ ] **Sync Accuracy:** 99.9% data consistency between CRM and Magento
- [ ] **Sync Reliability:** <0.1% failed sync jobs
- [ ] **Test Coverage:**
  - Unit tests: >80%
  - Integration tests: >70%
  - E2E tests: 100% of critical paths
  - API contract tests: 100% of endpoints
- [ ] **Code Quality:**
  - Zero critical security vulnerabilities
  - GoLint/GoVet passing
  - ESLint passing with no warnings
  - Zero memory leaks

#### Business Metrics
- [ ] **User Adoption:** 80% of sales team using quote feature within 1 month
- [ ] **Efficiency:** 50% reduction in average quote creation time (15min → 7.5min)
- [ ] **Conversion:** 25% increase in quote-to-order conversion rate
- [ ] **Visibility:** 100% of orders visible in CRM within 15 minutes
- [ ] **Customer Satisfaction:** >90% sales team satisfaction score
- [ ] **Quality:** <5 production bugs in first month post-launch

#### Operational Metrics
- [ ] **Uptime:** 99.9% (excluding planned maintenance)
- [ ] **Incident Response:** <2 hour MTTR for critical issues
- [ ] **Support:** <4 hour first response time for user issues
- [ ] **Training:** 100% of sales team completed training

---

### Timeline & Milestones

**Duration:** 12 weeks (3 months)
**Team:** 2 Full-Stack Developers + 1 QA Engineer

**Milestones:**
- **Week 2:** Foundation complete (DB schema, API client, test infrastructure)
- **Week 4:** Product catalog browsing functional with E2E tests
- **Week 7:** Quote creation and conversion complete with E2E tests
- **Week 9:** Order sync and tracking complete with E2E tests
- **Week 11:** Performance optimized, all tests passing
- **Week 12:** UAT complete, documentation ready, production deployment

**Go-Live:** End of Week 12 (soft launch), Week 13 (full rollout)

---

### Risk Management

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Magento API rate limits exceeded | Medium | High | Implement request queuing, caching, exponential backoff |
| Large product catalog performance | High | High | Implement pagination, indexing, Redis caching from day 1 |
| Quote calculation mismatch with Magento | Low | Critical | Extensive unit tests, validate against Magento sandbox |
| Webhook delivery failures | Medium | Medium | Implement retry queue, fallback to polling |
| E2E test flakiness | High | Low | Use Playwright best practices, retries, wait strategies |
| OAuth token management bugs | Medium | High | Use proven library (golang.org/x/oauth2), add monitoring |
| Data sync race conditions | Medium | High | Implement transaction support, idempotency keys |

---

### Definition of Done

A feature is considered "done" when:
1. ✅ Code written and peer-reviewed
2. ✅ Unit tests passing (>80% coverage)
3. ✅ Integration tests passing (>70% coverage)
4. ✅ E2E tests passing (100% critical paths)
5. ✅ API contract tests passing (100% endpoints)
6. ✅ Performance tests passing (p95 <200ms)
7. ✅ Security review complete (no critical vulns)
8. ✅ Code linted and formatted
9. ✅ Documentation updated (API docs, user guide)
10. ✅ Deployed to staging and tested by QA
11. ✅ Product owner acceptance
12. ✅ Ready for production deployment

---

## PROMPT IMPROVEMENT SUMMARY

### What Makes This Improved?

1. **Specific Business Objectives** - Measurable goals (50% time reduction, 25% conversion increase)
2. **Prioritized Requirements** - MUST/SHOULD/NICE TO HAVE with clear v1.0 scope
3. **Technical Specifications** - Exact API endpoints, database schema, architecture patterns
4. **Comprehensive Testing Strategy** - 40/30/10/20 split with concrete examples
5. **Realistic Timeline** - 12 weeks with weekly milestones
6. **Success Metrics** - Technical + Business + Operational KPIs
7. **Risk Management** - Identified risks with mitigation strategies
8. **Clear Acceptance Criteria** - Each requirement has testable conditions
9. **Test Coverage Targets** - Specific percentages and tools
10. **Detailed E2E Scenarios** - Actual Playwright code examples

### Prompt Template for Future Features

```markdown
# Feature Request: [Feature Name]

## Business Context
**User Story:** As a [role], I want [feature] so that [benefit]
**Business Goal:** [Measurable objective]
**Success Metric:** [KPI with target number]

## Functional Requirements
**MUST HAVE:**
- [ ] FR-1: [Requirement] | Acceptance: [Criteria]

## Technical Specifications
- Architecture Pattern: [Pattern]
- API Endpoints: [List]
- Database Changes: [Schema]
- Integration Points: [External systems]

## Testing Requirements
1. Unit Tests: [Coverage target] - [Tools]
2. Integration Tests: [Scenarios] - [Tools]
3. E2E Tests: [User journeys] - [Playwright specs]
4. API Tests: [Endpoints] - [Postman collections]

## Timeline
- Duration: [Weeks]
- Milestones: [Week-by-week]
- Team: [Roles needed]

## Success Criteria
- Technical: [Metrics]
- Business: [KPIs]
- Quality: [Standards]

## Risks & Mitigation
- [Risk]: [Mitigation strategy]
```

---

**This improved prompt provides everything needed to implement the feature successfully with high quality and comprehensive testing.**
