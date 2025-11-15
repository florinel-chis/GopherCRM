# GopherCRM Magento 2 Integration - Implementation Plan

**Version:** 1.0
**Date:** 2025-11-15
**Duration:** 12 weeks (3 months)
**Team Size:** 3-4 developers + 1 QA
**Target Release:** Q1 2026

---

## EXECUTIVE SUMMARY

This implementation plan outlines the systematic development of Magento 2 integration for GopherCRM over 12 weeks, organized into 4 major phases with comprehensive testing at each stage.

### Key Deliverables
- ✅ Product catalog sync from Magento
- ✅ Quote creation and management
- ✅ Quote-to-order conversion
- ✅ Order tracking and analytics
- ✅ Real-time webhook processing
- ✅ Comprehensive E2E and API test suites

### Success Metrics
- **Code Coverage:** >80% unit, >70% integration, 100% critical paths E2E
- **Performance:** <200ms API response time (p95)
- **Reliability:** 99.9% sync accuracy
- **Quality:** <5 production bugs in first month

---

## PHASE 0: FOUNDATION & SETUP (Week 1-2)

### Week 1: Architecture & Infrastructure

#### Backend Tasks
- [ ] **Task 0.1.1:** Complete Phase 2 refactoring (context.Context throughout)
  - **Estimate:** 16 hours
  - **Owner:** Backend Lead
  - **Deliverable:** All repositories/services use context

- [ ] **Task 0.1.2:** Create Magento API client package
  - **Estimate:** 8 hours
  - **Files:** `internal/magento/client.go`, `internal/magento/auth.go`
  - **Deliverable:** Authenticated API client with retry logic

- [ ] **Task 0.1.3:** Implement OAuth 2.0 flow for Magento
  - **Estimate:** 6 hours
  - **Files:** `internal/magento/oauth.go`
  - **Deliverable:** Token management with auto-refresh

- [ ] **Task 0.1.4:** Create sync job framework
  - **Estimate:** 12 hours
  - **Files:** `internal/sync/manager.go`, `internal/sync/job.go`
  - **Deliverable:** Cron-based sync scheduler with logging

#### Database Tasks
- [ ] **Task 0.1.5:** Create database migrations for new tables
  - **Estimate:** 4 hours
  - **Files:** `migrations/008_create_products.sql` through `migrations/015_create_sync_logs.sql`
  - **Tables:** products, product_categories, quotes, quote_items, orders, order_status_history, integration_configs, sync_logs

- [ ] **Task 0.1.6:** Add indexes for performance
  - **Estimate:** 2 hours
  - **Deliverable:** Optimized queries for common access patterns

#### Testing Setup
- [ ] **Task 0.1.7:** Setup Playwright test infrastructure
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/playwright.config.ts`, `gocrm-ui/tests/setup/`
  - **Deliverable:** Playwright configured with fixtures, page objects

- [ ] **Task 0.1.8:** Create mock Magento API server for testing
  - **Estimate:** 12 hours
  - **Files:** `tests/mocks/magento/server.go`
  - **Deliverable:** HTTP server mimicking Magento API responses

**Week 1 Total:** 68 hours (1.7 person-weeks)

---

### Week 2: Core Models & Repository Layer

#### Backend Tasks
- [ ] **Task 0.2.1:** Implement Product model and repository
  - **Estimate:** 8 hours
  - **Files:** `internal/models/product.go`, `internal/repository/product_repository.go`
  - **Methods:** Create, GetByID, GetBySKU, Search, List, Update, Delete

- [ ] **Task 0.2.2:** Implement Quote model and repository
  - **Estimate:** 10 hours
  - **Files:** `internal/models/quote.go`, `internal/repository/quote_repository.go`
  - **Methods:** Create, GetByID, GetByCustomer, Update, Delete, AddItem, UpdateItem, RemoveItem, CalculateTotals

- [ ] **Task 0.2.3:** Implement Order model and repository
  - **Estimate:** 8 hours
  - **Files:** `internal/models/order.go`, `internal/repository/order_repository.go`
  - **Methods:** Create, GetByID, GetByMagentoID, GetByCustomer, List, UpdateStatus

- [ ] **Task 0.2.4:** Implement IntegrationConfig model and repository
  - **Estimate:** 4 hours
  - **Files:** `internal/models/integration_config.go`, `internal/repository/integration_repository.go`

#### Unit Tests
- [ ] **Task 0.2.5:** Unit tests for all repositories
  - **Estimate:** 12 hours
  - **Files:** `internal/repository/*_test.go`
  - **Coverage Target:** >85%

- [ ] **Task 0.2.6:** Mock repository implementations
  - **Estimate:** 6 hours
  - **Files:** `internal/repository/mocks/*_mock.go`
  - **Tool:** `mockery` or `testify/mock`

**Week 2 Total:** 48 hours (1.2 person-weeks)

---

## PHASE 1: PRODUCT CATALOG INTEGRATION (Week 3-4)

### Week 3: Product Sync Backend

#### Backend Tasks
- [ ] **Task 1.1.1:** Implement Magento product API client
  - **Estimate:** 10 hours
  - **Files:** `internal/magento/products.go`
  - **Methods:** GetProducts(), GetProductBySKU(), GetCategories(), GetInventory()

- [ ] **Task 1.1.2:** Create product sync service
  - **Estimate:** 12 hours
  - **Files:** `internal/service/product_sync_service.go`
  - **Logic:** Fetch from Magento, transform, upsert to DB, handle errors

- [ ] **Task 1.1.3:** Implement product search service
  - **Estimate:** 8 hours
  - **Files:** `internal/service/product_service.go`
  - **Methods:** Search(), Filter(), GetBySKU(), GetByCategory()

- [ ] **Task 1.1.4:** Create product API handlers
  - **Estimate:** 8 hours
  - **Files:** `internal/handler/product_handler.go`
  - **Endpoints:** GET /products, GET /products/:id, GET /products/search, GET /products/sku/:sku

- [ ] **Task 1.1.5:** Implement scheduled product sync job
  - **Estimate:** 6 hours
  - **Files:** `internal/sync/product_sync_job.go`
  - **Schedule:** Every 30 minutes

#### Integration Tests
- [ ] **Task 1.1.6:** Product sync integration tests
  - **Estimate:** 8 hours
  - **Files:** `tests/integration/product_sync_test.go`
  - **Scenarios:** Full sync, incremental sync, error handling, conflict resolution

#### API Tests
- [ ] **Task 1.1.7:** Product API endpoint tests
  - **Estimate:** 6 hours
  - **Files:** `tests/api/product_api_test.go`
  - **Tool:** Go `net/http/httptest` or Postman/Newman
  - **Coverage:** All product endpoints, edge cases, pagination

**Week 3 Total:** 58 hours (1.45 person-weeks)

---

### Week 4: Product Catalog UI & E2E Tests

#### Frontend Tasks
- [ ] **Task 1.2.1:** Create Product List page
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/src/pages/products/ProductList.tsx`
  - **Features:** Search, filter by category, pagination, stock status badges

- [ ] **Task 1.2.2:** Create Product Detail modal
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/src/components/products/ProductDetail.tsx`
  - **Features:** Images, attributes, variants, inventory, pricing

- [ ] **Task 1.2.3:** Create Product Search component
  - **Estimate:** 6 hours
  - **Files:** `gocrm-ui/src/components/products/ProductSearch.tsx`
  - **Features:** Autocomplete, SKU/name search, recent searches

#### E2E Tests (Playwright)
- [ ] **Task 1.2.4:** Product browsing E2E tests
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/tests/e2e/products/product-browsing.spec.ts`
  - **Scenarios:**
    ```typescript
    test('should search products by name', async ({ page }) => {
      await page.goto('/products');
      await page.fill('[data-testid=product-search]', 'Widget');
      await expect(page.locator('[data-testid=product-item]')).toHaveCount(5);
    });

    test('should filter products by category', async ({ page }) => {
      await page.goto('/products');
      await page.selectOption('[data-testid=category-filter]', 'Electronics');
      await expect(page.locator('[data-testid=product-item]')).toContainText('Electronics');
    });

    test('should display out-of-stock badge', async ({ page }) => {
      await page.goto('/products');
      const outOfStock = page.locator('[data-testid=stock-status-badge]:has-text("Out of Stock")');
      await expect(outOfStock).toBeVisible();
    });
    ```

- [ ] **Task 1.2.5:** Product detail view E2E tests
  - **Estimate:** 6 hours
  - **Files:** `gocrm-ui/tests/e2e/products/product-detail.spec.ts`
  - **Scenarios:** View details, image gallery, variant selection

#### Admin UI
- [ ] **Task 1.2.6:** Product sync admin panel
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/src/pages/admin/ProductSync.tsx`
  - **Features:** Trigger manual sync, view sync logs, sync status

**Week 4 Total:** 48 hours (1.2 person-weeks)

---

## PHASE 2: QUOTE MANAGEMENT (Week 5-7)

### Week 5: Quote Creation Backend

#### Backend Tasks
- [ ] **Task 2.1.1:** Implement quote service
  - **Estimate:** 14 hours
  - **Files:** `internal/service/quote_service.go`
  - **Methods:** Create(), AddItem(), UpdateItem(), RemoveItem(), CalculateTotals(), ApplyDiscount(), SetExpiration()

- [ ] **Task 2.1.2:** Implement tax calculation service
  - **Estimate:** 8 hours
  - **Files:** `internal/service/tax_service.go`
  - **Logic:** Fetch tax rules from Magento, calculate per line item

- [ ] **Task 2.1.3:** Create quote API handlers
  - **Estimate:** 10 hours
  - **Files:** `internal/handler/quote_handler.go`
  - **Endpoints:** POST /quotes, GET /quotes, GET /quotes/:id, PUT /quotes/:id, DELETE /quotes/:id, POST /quotes/:id/items

- [ ] **Task 2.1.4:** Implement quote PDF generation
  - **Estimate:** 12 hours
  - **Files:** `internal/service/pdf_service.go`
  - **Tool:** `jung-kurt/gofpdf` or `unidoc/unipdf`
  - **Template:** Professional quote template with logo, line items, terms

#### Unit Tests
- [ ] **Task 2.1.5:** Quote service unit tests
  - **Estimate:** 10 hours
  - **Files:** `internal/service/quote_service_test.go`
  - **Coverage:** All calculation logic, edge cases

#### Integration Tests
- [ ] **Task 2.1.6:** Quote workflow integration tests
  - **Estimate:** 8 hours
  - **Files:** `tests/integration/quote_workflow_test.go`
  - **Scenarios:** Create quote, add items, calculate totals, apply discounts, generate PDF

**Week 5 Total:** 62 hours (1.55 person-weeks)

---

### Week 6: Quote Management UI

#### Frontend Tasks
- [ ] **Task 2.2.1:** Create Quote List page
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/src/pages/quotes/QuoteList.tsx`
  - **Features:** Filter by status, search, date range, customer

- [ ] **Task 2.2.2:** Create Quote Creation form
  - **Estimate:** 16 hours
  - **Files:** `gocrm-ui/src/pages/quotes/CreateQuote.tsx`
  - **Features:**
    - Customer selection/autocomplete
    - Product search and add
    - Line item discount input
    - Custom line items
    - Real-time total calculation
    - Expiration date picker
    - Notes/terms editor

- [ ] **Task 2.2.3:** Create Quote Detail view
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/src/pages/quotes/QuoteDetail.tsx`
  - **Features:** View quote, edit, send, download PDF, status history

- [ ] **Task 2.2.4:** Implement quote email functionality
  - **Estimate:** 8 hours
  - **Backend:** `internal/service/email_service.go`
  - **Template:** HTML email with quote details and PDF attachment

#### E2E Tests (Playwright)
- [ ] **Task 2.2.5:** Quote creation E2E tests
  - **Estimate:** 12 hours
  - **Files:** `gocrm-ui/tests/e2e/quotes/quote-creation.spec.ts`
  - **Scenarios:**
    ```typescript
    test('should create quote with multiple products', async ({ page }) => {
      await page.goto('/quotes/create');

      // Select customer
      await page.fill('[data-testid=customer-search]', 'Acme Corp');
      await page.click('text=Acme Corp');

      // Add first product
      await page.click('[data-testid=add-product-btn]');
      await page.fill('[data-testid=product-search]', 'Widget');
      await page.click('[data-testid=product-item]:has-text("Premium Widget")');
      await page.fill('[data-testid=qty-input]', '10');
      await page.fill('[data-testid=discount-input]', '10'); // 10% discount

      // Add second product
      await page.click('[data-testid=add-product-btn]');
      await page.fill('[data-testid=product-search]', 'Gadget');
      await page.click('[data-testid=product-item]:has-text("Super Gadget")');
      await page.fill('[data-testid=qty-input]', '5');

      // Set expiration
      await page.fill('[data-testid=expiration-date]', '2025-12-31');

      // Add notes
      await page.fill('[data-testid=notes-textarea]', 'Bulk order discount applied');

      // Verify totals calculated
      await expect(page.locator('[data-testid=subtotal]')).toContainText('$724.95');
      await expect(page.locator('[data-testid=grand-total]')).toContainText('$775.19');

      // Save quote
      await page.click('[data-testid=save-quote-btn]');

      // Verify redirect to quote detail
      await expect(page).toHaveURL(/\/quotes\/\d+/);
      await expect(page.locator('[data-testid=quote-number]')).toContainText('Q-2025-');
    });

    test('should validate required fields', async ({ page }) => {
      await page.goto('/quotes/create');
      await page.click('[data-testid=save-quote-btn]');

      await expect(page.locator('[data-testid=error-customer]')).toContainText('Customer is required');
      await expect(page.locator('[data-testid=error-items]')).toContainText('At least one product required');
    });

    test('should apply line-item discount correctly', async ({ page }) => {
      await page.goto('/quotes/create');
      await selectCustomer(page, 'Test Customer');
      await addProduct(page, 'Widget', { qty: 10, price: 50, discount: 15 });

      await expect(page.locator('[data-testid=item-subtotal]')).toContainText('$425.00'); // 10 * $50 * 0.85
    });
    ```

- [ ] **Task 2.2.6:** Quote management E2E tests
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/tests/e2e/quotes/quote-management.spec.ts`
  - **Scenarios:** Edit quote, send email, download PDF, change status, delete quote

**Week 6 Total:** 62 hours (1.55 person-weeks)

---

### Week 7: Quote to Order Conversion

#### Backend Tasks
- [ ] **Task 2.3.1:** Implement quote-to-order conversion service
  - **Estimate:** 16 hours
  - **Files:** `internal/service/quote_conversion_service.go`
  - **Logic:**
    1. Validate quote status (must be accepted)
    2. Check product availability in Magento
    3. Create/verify customer in Magento
    4. Create cart in Magento
    5. Add items with custom pricing
    6. Set billing/shipping addresses
    7. Apply payment method
    8. Place order
    9. Link quote to order
    10. Update quote status to "converted"

- [ ] **Task 2.3.2:** Implement Magento cart/order API client
  - **Estimate:** 12 hours
  - **Files:** `internal/magento/cart.go`, `internal/magento/orders.go`
  - **Methods:** CreateCart(), AddCartItems(), SetAddresses(), PlaceOrder()

- [ ] **Task 2.3.3:** Create order conversion handler
  - **Estimate:** 6 hours
  - **Files:** `internal/handler/quote_handler.go` (add methods)
  - **Endpoint:** POST /quotes/:id/convert

#### Integration Tests
- [ ] **Task 2.3.4:** Quote conversion integration tests
  - **Estimate:** 12 hours
  - **Files:** `tests/integration/quote_conversion_test.go`
  - **Scenarios:**
    - Successful conversion with transaction rollback on failure
    - Customer creation in Magento
    - Custom pricing preservation
    - Error handling (product unavailable, Magento API error)
    - Idempotency (prevent duplicate orders)

#### E2E Tests
- [ ] **Task 2.3.5:** Quote conversion E2E tests
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/tests/e2e/quotes/quote-conversion.spec.ts`
  - **Scenarios:**
    ```typescript
    test('should convert quote to Magento order', async ({ page }) => {
      // Setup: Create quote with mock Magento
      const quoteId = await createTestQuote();

      await page.goto(`/quotes/${quoteId}`);

      // Change status to accepted
      await page.click('[data-testid=status-dropdown]');
      await page.click('text=Accepted');
      await page.click('[data-testid=save-status-btn]');

      // Convert to order
      await page.click('[data-testid=convert-to-order-btn]');

      // Confirm conversion dialog
      await page.click('[data-testid=confirm-convert-btn]');

      // Wait for API call and redirect
      await page.waitForURL(/\/orders\/\d+/);

      // Verify order created
      await expect(page.locator('[data-testid=order-number]')).toBeVisible();
      await expect(page.locator('[data-testid=magento-order-link]')).toBeVisible();

      // Verify quote status updated
      await page.goto(`/quotes/${quoteId}`);
      await expect(page.locator('[data-testid=status-badge]')).toContainText('Converted');
    });

    test('should show error if product out of stock', async ({ page }) => {
      // Mock Magento to return out of stock
      await mockMagento.setProductStock('WIDGET-001', 0);

      await page.goto(`/quotes/${quoteId}`);
      await page.click('[data-testid=convert-to-order-btn]');

      await expect(page.locator('[data-testid=error-message]'))
        .toContainText('Product WIDGET-001 is out of stock');
    });
    ```

**Week 7 Total:** 56 hours (1.4 person-weeks)

---

## PHASE 3: ORDER SYNC & TRACKING (Week 8-9)

### Week 8: Order Sync Backend

#### Backend Tasks
- [ ] **Task 3.1.1:** Implement Magento order API client
  - **Estimate:** 10 hours
  - **Files:** `internal/magento/orders.go`
  - **Methods:** GetOrders(), GetOrderByID(), SearchOrders()

- [ ] **Task 3.1.2:** Create order sync service
  - **Estimate:** 14 hours
  - **Files:** `internal/service/order_sync_service.go`
  - **Logic:**
    - Fetch orders from Magento (incremental by updated_at)
    - Match to CRM customers (by email)
    - Create/update order records
    - Update order status history
    - Handle pagination (Magento returns max 20/request)

- [ ] **Task 3.1.3:** Implement webhook handlers
  - **Estimate:** 12 hours
  - **Files:** `internal/handler/webhook_handler.go`
  - **Endpoints:**
    - POST /webhooks/magento/order-created
    - POST /webhooks/magento/order-updated
    - POST /webhooks/magento/product-updated
    - POST /webhooks/magento/inventory-changed
  - **Security:** Validate webhook signatures

- [ ] **Task 3.1.4:** Create order API handlers
  - **Estimate:** 8 hours
  - **Files:** `internal/handler/order_handler.go`
  - **Endpoints:** GET /orders, GET /orders/:id, GET /orders/customer/:id

- [ ] **Task 3.1.5:** Implement scheduled order sync job
  - **Estimate:** 6 hours
  - **Files:** `internal/sync/order_sync_job.go`
  - **Schedule:** Every 15 minutes

#### Integration Tests
- [ ] **Task 3.1.6:** Order sync integration tests
  - **Estimate:** 10 hours
  - **Files:** `tests/integration/order_sync_test.go`
  - **Scenarios:**
    - Full historical sync
    - Incremental sync
    - Webhook processing
    - Customer matching
    - Status updates

- [ ] **Task 3.1.7:** Webhook security tests
  - **Estimate:** 4 hours
  - **Files:** `tests/integration/webhook_security_test.go`
  - **Scenarios:** Valid signature, invalid signature, replay attacks

#### API Tests
- [ ] **Task 3.1.8:** Order API tests
  - **Estimate:** 6 hours
  - **Files:** `tests/api/order_api_test.go`
  - **Coverage:** All order endpoints, filtering, pagination

**Week 8 Total:** 70 hours (1.75 person-weeks)

---

### Week 9: Order Tracking UI & Analytics

#### Frontend Tasks
- [ ] **Task 3.2.1:** Create Order List page
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/src/pages/orders/OrderList.tsx`
  - **Features:** Filter by status/date/customer, search, export to CSV

- [ ] **Task 3.2.2:** Create Order Detail page
  - **Estimate:** 12 hours
  - **Files:** `gocrm-ui/src/pages/orders/OrderDetail.tsx`
  - **Features:**
    - Order header (number, status, date, totals)
    - Items table
    - Customer info card
    - Billing/shipping addresses
    - Status timeline
    - Link to Magento order
    - Payment info

- [ ] **Task 3.2.3:** Create Order Analytics dashboard
  - **Estimate:** 14 hours
  - **Files:** `gocrm-ui/src/pages/analytics/OrderAnalytics.tsx`
  - **Features:**
    - Revenue charts (daily/weekly/monthly)
    - Order count trends
    - Top customers
    - Top products
    - Average order value
    - Conversion rates (leads → customers → orders)

- [ ] **Task 3.2.4:** Enhance Customer Detail with order history
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/src/pages/customers/CustomerDetail.tsx`
  - **Features:** Order history tab, lifetime value, last order date

#### E2E Tests
- [ ] **Task 3.2.5:** Order viewing E2E tests
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/tests/e2e/orders/order-viewing.spec.ts`
  - **Scenarios:**
    ```typescript
    test('should display order details', async ({ page }) => {
      await page.goto('/orders/123');

      await expect(page.locator('[data-testid=order-number]')).toContainText('#000000123');
      await expect(page.locator('[data-testid=order-status]')).toContainText('Processing');
      await expect(page.locator('[data-testid=grand-total]')).toContainText('$500.90');

      // Verify items
      const items = page.locator('[data-testid=order-item]');
      await expect(items).toHaveCount(2);

      // Verify timeline
      await expect(page.locator('[data-testid=timeline-item]').first()).toContainText('Order Placed');
    });

    test('should filter orders by status', async ({ page }) => {
      await page.goto('/orders');

      await page.selectOption('[data-testid=status-filter]', 'processing');
      await expect(page.locator('[data-testid=order-row]')).toContainText('Processing');

      const completedOrders = page.locator('[data-testid=order-row]:has-text("Completed")');
      await expect(completedOrders).toHaveCount(0);
    });
    ```

- [ ] **Task 3.2.6:** Analytics dashboard E2E tests
  - **Estimate:** 6 hours
  - **Files:** `gocrm-ui/tests/e2e/analytics/order-analytics.spec.ts`
  - **Scenarios:** Chart loading, date range selection, data accuracy

**Week 9 Total:** 60 hours (1.5 person-weeks)

---

## PHASE 4: INTEGRATION ADMIN & POLISH (Week 10-12)

### Week 10: Integration Configuration UI

#### Backend Tasks
- [ ] **Task 4.1.1:** Implement integration config service
  - **Estimate:** 8 hours
  - **Files:** `internal/service/integration_service.go`
  - **Methods:** SaveConfig(), TestConnection(), GetSyncStatus(), TriggerManualSync()

- [ ] **Task 4.1.2:** Create sync log service
  - **Estimate:** 6 hours
  - **Files:** `internal/service/sync_log_service.go`
  - **Methods:** GetLogs(), GetLogsByType(), GetRecentErrors()

- [ ] **Task 4.1.3:** Create integration admin handlers
  - **Estimate:** 8 hours
  - **Files:** `internal/handler/integration_handler.go`
  - **Endpoints:**
    - POST /integrations/magento/configure
    - GET /integrations/magento/config
    - POST /integrations/magento/test
    - GET /integrations/magento/status
    - POST /integrations/magento/sync/:type
    - GET /integrations/magento/logs

#### Frontend Tasks
- [ ] **Task 4.1.4:** Create Magento Configuration page
  - **Estimate:** 12 hours
  - **Files:** `gocrm-ui/src/pages/admin/MagentoConfig.tsx`
  - **Features:**
    - OAuth credentials form
    - Test connection button
    - Sync interval settings
    - Enable/disable toggles for each sync type
    - Save configuration

- [ ] **Task 4.1.5:** Create Sync Logs page
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/src/pages/admin/SyncLogs.tsx`
  - **Features:**
    - Log list with filtering
    - Sync statistics
    - Error highlighting
    - Retry failed jobs
    - Manual sync triggers

- [ ] **Task 4.1.6:** Create Integration Status dashboard
  - **Estimate:** 8 hours
  - **Files:** `gocrm-ui/src/pages/admin/IntegrationStatus.tsx`
  - **Features:**
    - Connection status indicator
    - Last sync times
    - Success/failure rates
    - Upcoming sync schedules

#### E2E Tests
- [ ] **Task 4.1.7:** Integration admin E2E tests
  - **Estimate:** 10 hours
  - **Files:** `gocrm-ui/tests/e2e/admin/magento-integration.spec.ts`
  - **Scenarios:**
    ```typescript
    test('should configure Magento integration', async ({ page }) => {
      await page.goto('/admin/integrations/magento');

      await page.fill('[data-testid=base-url]', 'https://magento.example.com');
      await page.fill('[data-testid=consumer-key]', 'test_key');
      await page.fill('[data-testid=consumer-secret]', 'test_secret');
      await page.fill('[data-testid=access-token]', 'test_token');
      await page.fill('[data-testid=access-token-secret]', 'test_token_secret');

      // Test connection
      await page.click('[data-testid=test-connection-btn]');
      await expect(page.locator('[data-testid=connection-status]')).toContainText('Connected');

      // Save config
      await page.click('[data-testid=save-config-btn]');
      await expect(page.locator('[data-testid=success-message]')).toContainText('Configuration saved');
    });

    test('should trigger manual sync', async ({ page }) => {
      await page.goto('/admin/sync-logs');

      await page.click('[data-testid=sync-products-btn]');
      await expect(page.locator('[data-testid=sync-status]')).toContainText('Running');

      // Wait for completion
      await page.waitForSelector('[data-testid=sync-status]:has-text("Completed")');

      // Verify log entry
      await expect(page.locator('[data-testid=log-entry]').first()).toContainText('Products sync completed');
    });
    ```

**Week 10 Total:** 62 hours (1.55 person-weeks)

---

### Week 11: Performance Optimization & Error Handling

#### Backend Tasks
- [ ] **Task 4.2.1:** Implement caching layer (Redis)
  - **Estimate:** 12 hours
  - **Files:** `internal/cache/redis.go`, `internal/service/*_cached.go`
  - **Cache:** Product catalog (30 min TTL), customer data (5 min), order summaries (15 min)

- [ ] **Task 4.2.2:** Add database query optimization
  - **Estimate:** 8 hours
  - **Actions:**
    - Review slow query log
    - Add missing indexes
    - Optimize N+1 queries with GORM preloading
    - Add read replicas support

- [ ] **Task 4.2.3:** Implement retry queue for failed syncs
  - **Estimate:** 10 hours
  - **Files:** `internal/queue/retry_queue.go`
  - **Logic:** Exponential backoff, max retries, dead letter queue

- [ ] **Task 4.2.4:** Add comprehensive error handling
  - **Estimate:** 8 hours
  - **Actions:**
    - Centralized error types
    - Error categorization (transient vs permanent)
    - Error notification system (email/Slack)

- [ ] **Task 4.2.5:** Implement circuit breaker for Magento API
  - **Estimate:** 8 hours
  - **Files:** `internal/magento/circuit_breaker.go`
  - **Tool:** `sony/gobreaker`
  - **Logic:** Open circuit after 5 failures, half-open after 60s

#### Performance Tests
- [ ] **Task 4.2.6:** Load testing
  - **Estimate:** 8 hours
  - **Tool:** `k6` or `vegeta`
  - **Scenarios:**
    - 100 concurrent users browsing products
    - 50 concurrent quote creations
    - Bulk order sync (10,000 orders)
  - **Targets:** <200ms p95, >100 RPS sustained

- [ ] **Task 4.2.7:** Database performance testing
  - **Estimate:** 6 hours
  - **Actions:**
    - Benchmark queries
    - Test with 100k products, 50k orders
    - Verify index usage

**Week 11 Total:** 60 hours (1.5 person-weeks)

---

### Week 12: Final Testing, Documentation & Launch Prep

#### Testing Tasks
- [ ] **Task 4.3.1:** Comprehensive E2E test suite run
  - **Estimate:** 8 hours
  - **Action:** Run all Playwright tests, fix failures, achieve 100% critical path coverage

- [ ] **Task 4.3.2:** API contract testing
  - **Estimate:** 8 hours
  - **Tool:** Postman Collection + Newman
  - **Files:** `tests/api/gophercrm-magento.postman_collection.json`
  - **Coverage:** All endpoints, success and error cases

- [ ] **Task 4.3.3:** Security testing
  - **Estimate:** 8 hours
  - **Actions:**
    - Penetration testing (OWASP Top 10)
    - Dependency vulnerability scan (`go list -m -json all | nancy sleuth`)
    - API key/OAuth security review

- [ ] **Task 4.3.4:** User acceptance testing (UAT)
  - **Estimate:** 12 hours
  - **Participants:** Sales team (5 users)
  - **Scenarios:** Real-world quote creation, order tracking
  - **Deliverable:** UAT report with bugs/feedback

#### Documentation Tasks
- [ ] **Task 4.3.5:** API documentation (OpenAPI/Swagger)
  - **Estimate:** 8 hours
  - **Files:** `docs/api/openapi.yaml`
  - **Tool:** Swagger UI
  - **Coverage:** All new endpoints with examples

- [ ] **Task 4.3.6:** User manual
  - **Estimate:** 10 hours
  - **Files:** `docs/USER_MANUAL.md`
  - **Sections:**
    - Getting started
    - Product catalog browsing
    - Creating quotes
    - Converting quotes to orders
    - Order tracking
    - Troubleshooting

- [ ] **Task 4.3.7:** Admin guide
  - **Estimate:** 6 hours
  - **Files:** `docs/ADMIN_GUIDE.md`
  - **Sections:**
    - Magento integration setup
    - Sync configuration
    - Monitoring and logs
    - Error resolution
    - Performance tuning

- [ ] **Task 4.3.8:** Developer documentation
  - **Estimate:** 8 hours
  - **Files:** `docs/DEVELOPMENT.md`
  - **Sections:**
    - Architecture overview
    - Adding new integrations
    - Testing guidelines
    - Deployment process

#### DevOps Tasks
- [ ] **Task 4.3.9:** Production deployment checklist
  - **Estimate:** 6 hours
  - **Deliverable:** `docs/DEPLOYMENT.md` with:
    - Environment variables
    - Database migrations
    - Secrets management
    - Health checks
    - Rollback plan

- [ ] **Task 4.3.10:** Monitoring & alerting setup
  - **Estimate:** 8 hours
  - **Tools:** Prometheus + Grafana
  - **Metrics:**
    - API response times
    - Sync job success/failure rates
    - Magento API error rates
    - Database query performance
  - **Alerts:**
    - Sync failures
    - High API error rate (>5%)
    - Database connection issues

**Week 12 Total:** 82 hours (2.05 person-weeks)

---

## TESTING STRATEGY SUMMARY

### Test Pyramid

```
         /\
        /E2E\         30% - Critical user journeys (Playwright)
       /------\
      /  API  \       30% - API contract & integration tests
     /----------\
    /    Unit    \    40% - Business logic & edge cases
   /--------------\
```

### Coverage Targets

| Type | Tool | Target | Files |
|------|------|--------|-------|
| Unit Tests | Go testing + testify | >80% | `*_test.go` |
| Integration Tests | Go testing + testcontainers | >70% | `tests/integration/*_test.go` |
| API Tests | Newman/Postman | 100% endpoints | `tests/api/*.postman_collection.json` |
| E2E Tests | Playwright | 100% critical paths | `gocrm-ui/tests/e2e/**/*.spec.ts` |

### Critical E2E Test Scenarios

1. **Quote Creation Flow** (Priority: P0)
   - Search products
   - Add to quote
   - Apply discounts
   - Calculate totals
   - Save and send

2. **Quote Conversion Flow** (Priority: P0)
   - Accept quote
   - Convert to Magento order
   - Verify order created
   - Check status sync

3. **Order Tracking Flow** (Priority: P1)
   - View order list
   - Filter orders
   - View order details
   - Track status changes

4. **Product Sync Flow** (Priority: P1)
   - Trigger manual sync
   - View sync progress
   - Verify products updated
   - Check sync logs

5. **Admin Configuration** (Priority: P1)
   - Configure Magento credentials
   - Test connection
   - Set sync intervals
   - View integration status

### Playwright Test Structure

```typescript
// gocrm-ui/tests/e2e/fixtures/base.ts
import { test as base } from '@playwright/test';
import { MagentoMock } from './magento-mock';
import { TestDataFactory } from './test-data-factory';

type Fixtures = {
  magentoMock: MagentoMock;
  testData: TestDataFactory;
};

export const test = base.extend<Fixtures>({
  magentoMock: async ({}, use) => {
    const mock = new MagentoMock();
    await mock.start();
    await use(mock);
    await mock.stop();
  },

  testData: async ({ request }, use) => {
    const factory = new TestDataFactory(request);
    await use(factory);
    await factory.cleanup();
  },
});

// gocrm-ui/tests/e2e/quotes/quote-creation.spec.ts
import { test } from '../fixtures/base';
import { expect } from '@playwright/test';

test.describe('Quote Creation', () => {
  test.beforeEach(async ({ page, testData }) => {
    // Setup test data
    await testData.createCustomer({ name: 'Test Corp' });
    await testData.createProducts([
      { sku: 'WIDGET-001', name: 'Premium Widget', price: 49.99 },
      { sku: 'GADGET-001', name: 'Super Gadget', price: 99.99 },
    ]);

    // Login
    await page.goto('/login');
    await page.fill('[data-testid=email]', 'sales@example.com');
    await page.fill('[data-testid=password]', 'password');
    await page.click('[data-testid=login-btn]');
  });

  test('should create quote with custom pricing', async ({ page }) => {
    // Test implementation
  });
});
```

---

## EFFORT SUMMARY

### By Phase

| Phase | Duration | Person-Weeks | Person-Hours |
|-------|----------|--------------|--------------|
| Phase 0: Foundation | 2 weeks | 2.9 | 116 |
| Phase 1: Products | 2 weeks | 2.65 | 106 |
| Phase 2: Quotes | 3 weeks | 4.5 | 180 |
| Phase 3: Orders | 2 weeks | 3.25 | 130 |
| Phase 4: Integration & Polish | 3 weeks | 5.1 | 204 |
| **TOTAL** | **12 weeks** | **18.4** | **736** |

### By Role

| Role | Hours | FTE (40h/week) |
|------|-------|----------------|
| Backend Developer | 380 | 0.79 |
| Frontend Developer | 240 | 0.5 |
| QA Engineer (E2E + API testing) | 116 | 0.24 |
| **TOTAL** | **736** | **1.53** |

**Recommended Team:** 2 Full-Stack Developers + 1 QA Engineer

---

## RISKS & MITIGATION

| Risk | Probability | Impact | Mitigation |
|------|-------------|--------|------------|
| Magento API changes | Low | High | Version lock API calls, add schema validation |
| Performance degradation with large catalogs | Medium | High | Implement caching, pagination, indexing early |
| OAuth token management complexity | Medium | Medium | Use proven library, add token refresh monitoring |
| Quote calculation accuracy | Low | High | Comprehensive unit tests, match Magento tax rules exactly |
| Data sync failures | Medium | Medium | Retry queue, circuit breaker, fallback to polling |
| E2E test flakiness | High | Medium | Use Playwright best practices, add retries, stable selectors |
| Scope creep | Medium | High | Strict requirements freeze after week 2 |

---

## SUCCESS CRITERIA

### Technical Metrics
- ✅ API response time <200ms (p95)
- ✅ Sync accuracy >99.9%
- ✅ Unit test coverage >80%
- ✅ E2E test coverage 100% critical paths
- ✅ Zero data loss in production
- ✅ <5 production bugs in first month

### Business Metrics
- ✅ 80% sales team adoption within 1 month
- ✅ 50% reduction in quote creation time
- ✅ 25% increase in quote-to-order conversion
- ✅ 100% visibility into customer order history
- ✅ <2 hour average response time for quotes

---

## DEPLOYMENT PLAN

### Week 12 - Soft Launch
- Deploy to staging environment
- Invite 5 beta users (sales team)
- Monitor for 3 days
- Collect feedback

### Week 13 - Production Launch
- Deploy to production
- Enable for 25% of sales team (rolling deployment)
- Monitor metrics closely
- Daily check-ins

### Week 14 - Full Rollout
- Enable for 100% of sales team
- Announcement & training sessions
- Support team on standby
- Performance monitoring

### Week 15-16 - Post-Launch
- Address feedback
- Minor bug fixes
- Performance tuning
- Documentation updates

---

## APPENDICES

### Appendix A: Development Environment Setup

```bash
# Backend
cd GopherCRM
export JWT_SECRET="$(openssl rand -base64 48)"
export MAGENTO_BASE_URL="http://localhost:8080" # Mock server
go mod download
go run cmd/main.go

# Frontend
cd gocrm-ui
npm install
npm run dev

# Mock Magento Server
cd tests/mocks/magento
go run main.go

# Run Tests
go test ./... -v                           # Unit + Integration
npm run test:e2e                            # Playwright E2E
newman run tests/api/*.postman_collection.json  # API tests
```

### Appendix B: CI/CD Pipeline

```yaml
# .github/workflows/ci.yml
name: CI Pipeline
on: [push, pull_request]

jobs:
  backend-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-go@v4
        with:
          go-version: '1.24'
      - name: Unit Tests
        run: go test ./internal/... -cover -coverprofile=coverage.out
      - name: Integration Tests
        run: go test ./tests/integration/... -v
      - name: Upload Coverage
        uses: codecov/codecov-action@v3

  frontend-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
      - name: Install Dependencies
        run: cd gocrm-ui && npm ci
      - name: Unit Tests
        run: cd gocrm-ui && npm test
      - name: Build
        run: cd gocrm-ui && npm run build

  e2e-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - uses: actions/setup-node@v3
      - name: Install Playwright
        run: cd gocrm-ui && npx playwright install --with-deps
      - name: Run E2E Tests
        run: cd gocrm-ui && npm run test:e2e
      - name: Upload Test Results
        if: always()
        uses: actions/upload-artifact@v3
        with:
          name: playwright-report
          path: gocrm-ui/playwright-report/

  api-tests:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v3
      - name: Run Newman Tests
        run: newman run tests/api/*.postman_collection.json --reporters cli,json
```

---

**Document Version:** 1.0
**Last Updated:** 2025-11-15
**Next Review:** Weekly during implementation
**Status:** APPROVED - Ready for Development

---

**Total Implementation Effort:** 736 hours (18.4 person-weeks)
**Timeline:** 12 weeks
**Team:** 2 Full-Stack + 1 QA
**Expected Launch:** Q1 2026
**ROI:** 25% increase in conversion, 50% faster quote creation