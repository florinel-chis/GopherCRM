# GopherCRM Magento 2 Integration - Product Requirements Document

**Version:** 1.0
**Date:** 2025-11-15
**Status:** Planning Phase
**Target:** Transform GopherCRM into a commerce-enabled CRM with Magento 2 integration

---

## 1. EXECUTIVE SUMMARY

### 1.1 Vision
Transform GopherCRM from a basic CRM into a **Commerce-Enabled CRM** that seamlessly integrates with Magento 2, enabling sales teams to manage the complete customer lifecycle from lead to order, all within a unified interface.

### 1.2 Business Objectives
1. **Unified Customer View** - Single source of truth combining CRM data + Magento commerce data
2. **Quote-to-Order Workflow** - Enable sales team to create quotes and convert to Magento orders
3. **Real-Time Inventory Visibility** - Sales reps see current product availability
4. **Order Tracking** - Monitor order status, shipments, and payments from CRM
5. **Data Synchronization** - Bidirectional sync between GopherCRM and Magento 2

### 1.3 Success Metrics
- **Sync Latency:** < 5 minutes for critical data (orders, inventory)
- **Data Accuracy:** 99.9% consistency between GopherCRM and Magento
- **API Performance:** < 500ms p95 for CRM API calls
- **User Adoption:** 80% of sales team using CRM for quotes within 3 months
- **Order Conversion:** 25% increase in lead-to-order conversion rate

---

## 2. STAKEHOLDERS

| Role | Responsibility | Key Concerns |
|------|---------------|--------------|
| Sales Team | Use CRM for quotes, orders, customer management | Ease of use, real-time data, mobile access |
| IT/DevOps | Deploy, maintain, monitor integrations | Reliability, performance, security |
| Product Manager | Define features, prioritize roadmap | ROI, time to market, competitive advantage |
| Magento Admin | Manage e-commerce platform | Data integrity, no disruption to existing flows |
| Customers | Place orders, receive quotes | Fast response, accurate pricing, inventory |

---

## 3. SYSTEM ARCHITECTURE

### 3.1 High-Level Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        GopherCRM                             │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   CRM Core   │  │   Commerce   │  │  Integration │      │
│  │              │  │    Module    │  │    Engine    │      │
│  │ - Leads      │  │ - Products   │  │ - Sync Jobs  │      │
│  │ - Customers  │  │ - Quotes     │  │ - Webhooks   │      │
│  │ - Tickets    │  │ - Orders     │  │ - Event Bus  │      │
│  │ - Tasks      │  │ - Inventory  │  │ - Queue      │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
│                           │                    │             │
│                           ▼                    ▼             │
│                  ┌──────────────────────────────────┐       │
│                  │   Magento 2 API Client           │       │
│                  │  - REST API / GraphQL            │       │
│                  │  - OAuth 2.0 Authentication      │       │
│                  │  - Rate Limiting & Retry Logic   │       │
│                  └──────────────────────────────────┘       │
└─────────────────────────────┬───────────────────────────────┘
                               │ HTTPS
                               ▼
┌─────────────────────────────────────────────────────────────┐
│                      Magento 2 Platform                      │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐      │
│  │   Products   │  │    Orders    │  │   Customers  │      │
│  │  Catalog API │  │   Sales API  │  │ Customer API │      │
│  └──────────────┘  └──────────────┘  └──────────────┘      │
│                                                               │
│  ┌──────────────────────────────────────────────────┐       │
│  │            Webhooks / Event System                │       │
│  │  - order.created, order.updated                   │       │
│  │  - product.updated, inventory.changed             │       │
│  └──────────────────────────────────────────────────┘       │
└─────────────────────────────────────────────────────────────┘
```

### 3.2 Data Flow Patterns

#### Pattern 1: Real-Time Sync (Webhooks)
```
Magento Event → Webhook → GopherCRM API → Database → UI Update
```
**Use Cases:** Order status changes, inventory updates, customer updates

#### Pattern 2: Scheduled Batch Sync
```
Cron Job → Magento API Poll → Transform Data → Bulk Insert/Update → Database
```
**Use Cases:** Product catalog sync, historical order import, customer reconciliation

#### Pattern 3: On-Demand Sync (User-Triggered)
```
User Action → API Request → Magento API → Real-Time Response → UI Display
```
**Use Cases:** Quote creation, inventory check, order placement

---

## 4. FUNCTIONAL REQUIREMENTS

### 4.1 Product Management

#### FR-PROD-001: Product Catalog Sync
**As a** sales representative
**I want** to browse the complete product catalog from Magento
**So that** I can create accurate quotes with current products

**Acceptance Criteria:**
- [ ] Products sync from Magento to GopherCRM every 30 minutes
- [ ] Product data includes: SKU, name, description, price, categories, attributes, images
- [ ] Search products by SKU, name, category
- [ ] Filter products by: category, price range, stock status
- [ ] View product details including variants/configurable options
- [ ] Product changes in Magento reflect in CRM within 5 minutes (webhook)

**API Endpoints:**
```
GET    /api/v1/products                 # List products (paginated)
GET    /api/v1/products/:id             # Get product details
GET    /api/v1/products/search?q=:query # Search products
GET    /api/v1/products/sku/:sku        # Get by SKU
POST   /api/v1/products/sync            # Trigger manual sync (admin)
```

**Database Schema:**
```sql
CREATE TABLE products (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    magento_id VARCHAR(255) UNIQUE NOT NULL,
    sku VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(500) NOT NULL,
    description TEXT,
    short_description TEXT,
    price DECIMAL(10,2) NOT NULL,
    special_price DECIMAL(10,2),
    cost DECIMAL(10,2),
    product_type VARCHAR(50) NOT NULL, -- simple, configurable, bundle, grouped
    status VARCHAR(50) NOT NULL, -- enabled, disabled
    visibility VARCHAR(50),
    weight DECIMAL(10,2),
    categories JSON,
    attributes JSON,
    images JSON,
    stock_qty INT,
    stock_status VARCHAR(50), -- in_stock, out_of_stock
    is_in_stock BOOLEAN,
    manage_stock BOOLEAN,
    synced_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL,
    INDEX idx_sku (sku),
    INDEX idx_magento_id (magento_id),
    INDEX idx_status (status, stock_status),
    INDEX idx_synced (synced_at)
);

CREATE TABLE product_categories (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    magento_id VARCHAR(255) UNIQUE NOT NULL,
    name VARCHAR(255) NOT NULL,
    parent_id BIGINT NULL,
    path VARCHAR(1000),
    level INT,
    position INT,
    is_active BOOLEAN DEFAULT TRUE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (parent_id) REFERENCES product_categories(id),
    INDEX idx_parent (parent_id),
    INDEX idx_active (is_active)
);
```

---

#### FR-PROD-002: Real-Time Inventory Check
**As a** sales representative
**I want** to see current inventory levels when creating quotes
**So that** I don't promise products that are out of stock

**Acceptance Criteria:**
- [ ] Inventory levels shown in product listings
- [ ] Color-coded stock status (green: >10, yellow: 1-10, red: 0)
- [ ] Real-time inventory check when adding to quote
- [ ] Warning if product out of stock
- [ ] Support for multi-source inventory (if Magento MSI enabled)

**API Endpoints:**
```
GET    /api/v1/inventory/product/:sku   # Get inventory for SKU
GET    /api/v1/inventory/check           # Bulk check (POST body with SKU list)
```

---

### 4.2 Quote Management

#### FR-QUOTE-001: Create Quote from CRM
**As a** sales representative
**I want** to create a custom quote for a customer
**So that** I can provide pricing for complex or custom orders

**Acceptance Criteria:**
- [ ] Create quote with customer association (link to CRM customer)
- [ ] Add products with custom quantities
- [ ] Apply discounts (percentage or fixed amount) per line item
- [ ] Add custom line items (services, fees)
- [ ] Calculate tax automatically (pull from Magento tax rules)
- [ ] Calculate shipping estimates
- [ ] Add notes/terms/conditions
- [ ] Save as draft or send to customer
- [ ] Generate PDF of quote
- [ ] Track quote status: draft, sent, accepted, rejected, expired
- [ ] Set expiration date

**API Endpoints:**
```
POST   /api/v1/quotes                   # Create quote
GET    /api/v1/quotes                   # List quotes
GET    /api/v1/quotes/:id               # Get quote details
PUT    /api/v1/quotes/:id               # Update quote
DELETE /api/v1/quotes/:id               # Delete quote
POST   /api/v1/quotes/:id/send          # Send to customer
POST   /api/v1/quotes/:id/convert       # Convert to Magento order
GET    /api/v1/quotes/:id/pdf           # Generate PDF
POST   /api/v1/quotes/:id/items         # Add item to quote
PUT    /api/v1/quotes/:id/items/:itemId # Update quote item
DELETE /api/v1/quotes/:id/items/:itemId # Remove quote item
```

**Database Schema:**
```sql
CREATE TABLE quotes (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    quote_number VARCHAR(50) UNIQUE NOT NULL,
    customer_id BIGINT NOT NULL,
    lead_id BIGINT NULL,
    magento_quote_id VARCHAR(255) NULL,
    status VARCHAR(50) NOT NULL, -- draft, sent, accepted, rejected, expired, converted
    currency VARCHAR(3) DEFAULT 'USD',
    subtotal DECIMAL(10,2) NOT NULL DEFAULT 0,
    discount_amount DECIMAL(10,2) DEFAULT 0,
    discount_description VARCHAR(255),
    tax_amount DECIMAL(10,2) DEFAULT 0,
    shipping_amount DECIMAL(10,2) DEFAULT 0,
    grand_total DECIMAL(10,2) NOT NULL DEFAULT 0,
    valid_until TIMESTAMP NULL,
    notes TEXT,
    terms TEXT,
    internal_notes TEXT,
    sent_at TIMESTAMP NULL,
    accepted_at TIMESTAMP NULL,
    converted_at TIMESTAMP NULL,
    magento_order_id VARCHAR(255) NULL,
    created_by BIGINT NOT NULL,
    updated_by BIGINT,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL,
    FOREIGN KEY (customer_id) REFERENCES customers(id),
    FOREIGN KEY (lead_id) REFERENCES leads(id),
    FOREIGN KEY (created_by) REFERENCES users(id),
    INDEX idx_customer (customer_id),
    INDEX idx_status (status),
    INDEX idx_created (created_at)
);

CREATE TABLE quote_items (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    quote_id BIGINT NOT NULL,
    product_id BIGINT NULL,
    sku VARCHAR(255),
    name VARCHAR(500) NOT NULL,
    description TEXT,
    qty INT NOT NULL DEFAULT 1,
    price DECIMAL(10,2) NOT NULL,
    discount_percent DECIMAL(5,2) DEFAULT 0,
    discount_amount DECIMAL(10,2) DEFAULT 0,
    tax_percent DECIMAL(5,2) DEFAULT 0,
    tax_amount DECIMAL(10,2) DEFAULT 0,
    row_total DECIMAL(10,2) NOT NULL,
    product_options JSON,
    is_custom BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    FOREIGN KEY (quote_id) REFERENCES quotes(id) ON DELETE CASCADE,
    FOREIGN KEY (product_id) REFERENCES products(id),
    INDEX idx_quote (quote_id)
);
```

---

#### FR-QUOTE-002: Convert Quote to Magento Order
**As a** sales representative
**I want** to convert an accepted quote into a Magento order
**So that** the order flows through the normal e-commerce fulfillment process

**Acceptance Criteria:**
- [ ] One-click conversion for accepted quotes
- [ ] Map quote items to Magento cart/order
- [ ] Preserve custom pricing and discounts
- [ ] Customer automatically created in Magento if doesn't exist
- [ ] Order created in Magento with status "pending"
- [ ] Payment method configurable (e.g., "Purchase Order", "Bank Transfer")
- [ ] Link created between CRM quote and Magento order
- [ ] Quote status changes to "converted"
- [ ] Notification sent to customer with order details

**Magento API Integration:**
```go
// Example: Convert quote to order
POST /rest/V1/carts/mine (create cart for customer)
POST /rest/V1/carts/mine/items (add items)
POST /rest/V1/carts/mine/billing-address (set billing)
POST /rest/V1/carts/mine/shipping-information (set shipping)
PUT  /rest/V1/carts/mine/order (place order)
```

---

### 4.3 Order Management

#### FR-ORDER-001: Sync Orders from Magento
**As a** sales manager
**I want** all Magento orders synced to the CRM
**So that** I can track customer purchase history and performance

**Acceptance Criteria:**
- [ ] Orders sync from Magento every 15 minutes
- [ ] Order data includes: order number, items, customer, status, payment, shipping, totals
- [ ] Historical orders imported on first sync
- [ ] Order status updates in real-time via webhooks
- [ ] View order timeline (created, paid, shipped, completed, cancelled)
- [ ] Link orders to CRM customers
- [ ] Link orders to quotes (if created from quote)

**API Endpoints:**
```
GET    /api/v1/orders                   # List orders
GET    /api/v1/orders/:id               # Get order details
GET    /api/v1/orders/customer/:custId  # Orders by customer
GET    /api/v1/orders/magento/:magentoId # Get by Magento ID
POST   /api/v1/orders/sync              # Trigger manual sync
```

**Database Schema:**
```sql
CREATE TABLE orders (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    magento_order_id VARCHAR(255) UNIQUE NOT NULL,
    order_number VARCHAR(50) UNIQUE NOT NULL,
    customer_id BIGINT NOT NULL,
    quote_id BIGINT NULL,
    status VARCHAR(50) NOT NULL,
    state VARCHAR(50) NOT NULL,
    currency VARCHAR(3) DEFAULT 'USD',
    subtotal DECIMAL(10,2) NOT NULL,
    discount_amount DECIMAL(10,2) DEFAULT 0,
    tax_amount DECIMAL(10,2) DEFAULT 0,
    shipping_amount DECIMAL(10,2) DEFAULT 0,
    grand_total DECIMAL(10,2) NOT NULL,
    total_paid DECIMAL(10,2) DEFAULT 0,
    total_refunded DECIMAL(10,2) DEFAULT 0,
    payment_method VARCHAR(100),
    shipping_method VARCHAR(100),
    shipping_description VARCHAR(255),
    customer_email VARCHAR(255),
    customer_firstname VARCHAR(100),
    customer_lastname VARCHAR(100),
    billing_address JSON,
    shipping_address JSON,
    items JSON,
    order_date TIMESTAMP NOT NULL,
    synced_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    deleted_at TIMESTAMP NULL,
    FOREIGN KEY (customer_id) REFERENCES customers(id),
    FOREIGN KEY (quote_id) REFERENCES quotes(id),
    INDEX idx_magento_id (magento_order_id),
    INDEX idx_customer (customer_id),
    INDEX idx_status (status),
    INDEX idx_order_date (order_date)
);

CREATE TABLE order_status_history (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    order_id BIGINT NOT NULL,
    status VARCHAR(50) NOT NULL,
    comment TEXT,
    is_customer_notified BOOLEAN DEFAULT FALSE,
    is_visible_on_front BOOLEAN DEFAULT FALSE,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    FOREIGN KEY (order_id) REFERENCES orders(id) ON DELETE CASCADE,
    INDEX idx_order (order_id)
);
```

---

#### FR-ORDER-002: Order Tracking & Analytics
**As a** sales representative
**I want** to see order statistics for my customers
**So that** I can provide better service and identify upsell opportunities

**Acceptance Criteria:**
- [ ] Customer lifetime value (total order value)
- [ ] Average order value
- [ ] Order frequency
- [ ] Last order date
- [ ] Most purchased products
- [ ] Order status breakdown (pending, processing, shipped, completed)
- [ ] Filter orders by date range, status, product

**API Endpoints:**
```
GET    /api/v1/analytics/customer/:id/orders     # Customer order stats
GET    /api/v1/analytics/orders/summary          # Overall order stats
GET    /api/v1/analytics/products/top-selling    # Top selling products
```

---

### 4.4 Customer Enhancement

#### FR-CUST-001: Magento Customer Sync
**As a** system administrator
**I want** CRM customers synchronized with Magento customers
**So that** there is a single source of truth for customer data

**Acceptance Criteria:**
- [ ] CRM customer creates Magento customer (on quote conversion)
- [ ] Magento customer creates/updates CRM customer (on order sync)
- [ ] Bidirectional sync for: name, email, phone, addresses
- [ ] Conflict resolution strategy (CRM wins for sales data, Magento wins for order data)
- [ ] Customer merge capability (duplicate detection)

**Enhanced Customer Schema:**
```sql
ALTER TABLE customers ADD COLUMN magento_customer_id VARCHAR(255) UNIQUE;
ALTER TABLE customers ADD COLUMN customer_group VARCHAR(100);
ALTER TABLE customers ADD COLUMN tax_vat VARCHAR(50);
ALTER TABLE customers ADD COLUMN website_id VARCHAR(50);
ALTER TABLE customers ADD COLUMN store_id VARCHAR(50);
ALTER TABLE customers ADD COLUMN synced_at TIMESTAMP;
ALTER TABLE customers ADD INDEX idx_magento_id (magento_customer_id);
```

---

### 4.5 Integration Configuration

#### FR-INT-001: Magento Connection Setup
**As a** system administrator
**I want** to configure Magento 2 API connection settings
**So that** the CRM can communicate with our Magento instance

**Acceptance Criteria:**
- [ ] Store Magento base URL
- [ ] OAuth 2.0 credentials (consumer key, consumer secret, access token, access token secret)
- [ ] Test connection button
- [ ] Configure sync intervals (products, orders, customers)
- [ ] Enable/disable specific integrations
- [ ] Webhook endpoints configuration
- [ ] Error notification settings

**API Endpoints:**
```
POST   /api/v1/integrations/magento/configure   # Save configuration
GET    /api/v1/integrations/magento/config      # Get configuration
POST   /api/v1/integrations/magento/test        # Test connection
GET    /api/v1/integrations/magento/status      # Sync status
POST   /api/v1/integrations/magento/sync/:type  # Trigger sync (products|orders|customers)
```

**Database Schema:**
```sql
CREATE TABLE integration_configs (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    integration_type VARCHAR(50) NOT NULL, -- magento2, shopify, etc.
    base_url VARCHAR(500) NOT NULL,
    api_version VARCHAR(20),
    consumer_key VARCHAR(255),
    consumer_secret VARCHAR(255),
    access_token TEXT,
    access_token_secret TEXT,
    webhook_secret VARCHAR(255),
    is_enabled BOOLEAN DEFAULT TRUE,
    sync_products_enabled BOOLEAN DEFAULT TRUE,
    sync_products_interval INT DEFAULT 30, -- minutes
    sync_orders_enabled BOOLEAN DEFAULT TRUE,
    sync_orders_interval INT DEFAULT 15,
    sync_customers_enabled BOOLEAN DEFAULT TRUE,
    last_product_sync TIMESTAMP,
    last_order_sync TIMESTAMP,
    last_customer_sync TIMESTAMP,
    error_count INT DEFAULT 0,
    last_error TEXT,
    last_error_at TIMESTAMP,
    created_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    INDEX idx_type (integration_type)
);

CREATE TABLE sync_logs (
    id BIGINT PRIMARY KEY AUTO_INCREMENT,
    integration_config_id BIGINT NOT NULL,
    sync_type VARCHAR(50) NOT NULL, -- products, orders, customers
    status VARCHAR(50) NOT NULL, -- running, completed, failed
    records_processed INT DEFAULT 0,
    records_created INT DEFAULT 0,
    records_updated INT DEFAULT 0,
    records_failed INT DEFAULT 0,
    error_message TEXT,
    started_at TIMESTAMP NOT NULL,
    completed_at TIMESTAMP,
    duration_seconds INT,
    FOREIGN KEY (integration_config_id) REFERENCES integration_configs(id),
    INDEX idx_type (sync_type, status),
    INDEX idx_started (started_at)
);
```

---

## 5. NON-FUNCTIONAL REQUIREMENTS

### 5.1 Performance
- **NFR-PERF-001:** Product search returns results in < 200ms (p95)
- **NFR-PERF-002:** Quote calculation completes in < 500ms
- **NFR-PERF-003:** Order sync processes 1000 orders in < 5 minutes
- **NFR-PERF-004:** Webhook processing latency < 1 second
- **NFR-PERF-005:** Support 100 concurrent users without degradation

### 5.2 Reliability
- **NFR-REL-001:** System uptime 99.9% (excluding planned maintenance)
- **NFR-REL-002:** Magento API failures automatically retry with exponential backoff (max 3 retries)
- **NFR-REL-003:** Failed sync jobs queued and retried within 1 hour
- **NFR-REL-004:** Data integrity checks run daily (compare CRM vs Magento)

### 5.3 Security
- **NFR-SEC-001:** Magento API credentials encrypted at rest
- **NFR-SEC-002:** All Magento API calls over HTTPS with certificate validation
- **NFR-SEC-003:** Webhook endpoints validate signatures
- **NFR-SEC-004:** Rate limiting on integration endpoints (prevent abuse)
- **NFR-SEC-005:** Audit log for all data sync operations

### 5.4 Scalability
- **NFR-SCAL-001:** Support product catalogs up to 100,000 SKUs
- **NFR-SCAL-002:** Support order volumes up to 10,000/day
- **NFR-SCAL-003:** Horizontal scaling for sync workers
- **NFR-SCAL-004:** Database partitioning strategy for orders (by year)

---

## 6. USER STORIES - DETAILED

### Epic 1: Product Management
```
US-1.1: As a sales rep, I can search products by SKU or name
US-1.2: As a sales rep, I can filter products by category
US-1.3: As a sales rep, I can see product inventory levels
US-1.4: As a sales rep, I can view product images and details
US-1.5: As an admin, I can manually trigger product sync
US-1.6: As an admin, I can see product sync status and history
```

### Epic 2: Quote Creation
```
US-2.1: As a sales rep, I can create a new quote for a customer
US-2.2: As a sales rep, I can add products to a quote
US-2.3: As a sales rep, I can apply line-item discounts
US-2.4: As a sales rep, I can add custom line items (services, fees)
US-2.5: As a sales rep, I can set quote expiration date
US-2.6: As a sales rep, I can save quote as draft
US-2.7: As a sales rep, I can send quote to customer via email
US-2.8: As a sales rep, I can download quote as PDF
US-2.9: As a sales rep, I can duplicate an existing quote
US-2.10: As a sales rep, I can see quote status history
```

### Epic 3: Quote to Order Conversion
```
US-3.1: As a sales rep, I can convert an accepted quote to Magento order
US-3.2: As a sales rep, I can preview order before submitting to Magento
US-3.3: As a sales rep, I can select payment method for order
US-3.4: As a sales rep, I receive confirmation when order created in Magento
US-3.5: As a sales rep, I can see link between quote and resulting order
```

### Epic 4: Order Management
```
US-4.1: As a sales rep, I can view all orders for a customer
US-4.2: As a sales rep, I can see order details (items, totals, addresses)
US-4.3: As a sales rep, I can track order status in real-time
US-4.4: As a sales rep, I can see order timeline
US-4.5: As a sales manager, I can see order analytics and reports
US-4.6: As a sales manager, I can filter orders by date, status, customer
```

### Epic 5: Integration Management
```
US-5.1: As an admin, I can configure Magento API credentials
US-5.2: As an admin, I can test Magento connection
US-5.3: As an admin, I can enable/disable specific sync jobs
US-5.4: As an admin, I can set sync intervals
US-5.5: As an admin, I can view sync logs and errors
US-5.6: As an admin, I receive alerts for sync failures
```

---

## 7. INTEGRATION SPECIFICATIONS

### 7.1 Magento 2 REST API Endpoints Used

| Purpose | Endpoint | Method | Frequency |
|---------|----------|--------|-----------|
| Get Products | `/rest/V1/products` | GET | Every 30 min |
| Get Product by SKU | `/rest/V1/products/:sku` | GET | On-demand |
| Get Inventory | `/rest/V1/stockStatuses/:sku` | GET | On-demand |
| Get Orders | `/rest/V1/orders` | GET | Every 15 min |
| Get Order by ID | `/rest/V1/orders/:id` | GET | On-demand |
| Create Customer | `/rest/V1/customers` | POST | On quote convert |
| Create Cart | `/rest/V1/carts` | POST | On quote convert |
| Add Cart Items | `/rest/V1/carts/:id/items` | POST | On quote convert |
| Place Order | `/rest/V1/carts/mine/order` | PUT | On quote convert |

### 7.2 Webhook Events to Handle

```json
{
  "webhooks": [
    {
      "event": "order.created",
      "endpoint": "/api/v1/webhooks/magento/order-created",
      "action": "Create or update order in CRM"
    },
    {
      "event": "order.updated",
      "endpoint": "/api/v1/webhooks/magento/order-updated",
      "action": "Update order status and details"
    },
    {
      "event": "product.updated",
      "endpoint": "/api/v1/webhooks/magento/product-updated",
      "action": "Sync product changes"
    },
    {
      "event": "inventory.changed",
      "endpoint": "/api/v1/webhooks/magento/inventory-changed",
      "action": "Update stock levels"
    },
    {
      "event": "customer.created",
      "endpoint": "/api/v1/webhooks/magento/customer-created",
      "action": "Create CRM customer if not exists"
    }
  ]
}
```

### 7.3 Data Mapping

#### Product Mapping: Magento → GopherCRM
```go
type MagentoProduct struct {
    ID               int                    `json:"id"`
    SKU              string                 `json:"sku"`
    Name             string                 `json:"name"`
    Price            float64                `json:"price"`
    Status           int                    `json:"status"`
    TypeID           string                 `json:"type_id"`
    Weight           float64                `json:"weight"`
    ExtensionAttributes *ProductExtension  `json:"extension_attributes"`
    CustomAttributes []CustomAttribute      `json:"custom_attributes"`
}

type ProductExtension struct {
    StockItem       *StockItem              `json:"stock_item"`
    CategoryLinks   []CategoryLink          `json:"category_links"`
}

// Map to CRM Product
func MapMagentoProduct(mp *MagentoProduct) *models.Product {
    return &models.Product{
        MagentoID:        strconv.Itoa(mp.ID),
        SKU:              mp.SKU,
        Name:             mp.Name,
        Price:            mp.Price,
        ProductType:      mp.TypeID,
        Status:           mapProductStatus(mp.Status),
        Weight:           mp.Weight,
        StockQty:         mp.ExtensionAttributes.StockItem.Qty,
        StockStatus:      mapStockStatus(mp.ExtensionAttributes.StockItem.IsInStock),
        Categories:       mapCategories(mp.ExtensionAttributes.CategoryLinks),
        Attributes:       mapCustomAttributes(mp.CustomAttributes),
    }
}
```

#### Order Mapping: Magento → GopherCRM
```go
type MagentoOrder struct {
    EntityID              int                `json:"entity_id"`
    IncrementID           string             `json:"increment_id"`
    State                 string             `json:"state"`
    Status                string             `json:"status"`
    CustomerEmail         string             `json:"customer_email"`
    CustomerFirstname     string             `json:"customer_firstname"`
    CustomerLastname      string             `json:"customer_lastname"`
    GrandTotal            float64            `json:"grand_total"`
    Subtotal              float64            `json:"subtotal"`
    TaxAmount             float64            `json:"tax_amount"`
    ShippingAmount        float64            `json:"shipping_amount"`
    DiscountAmount        float64            `json:"discount_amount"`
    Items                 []MagentoOrderItem `json:"items"`
    BillingAddress        *Address           `json:"billing_address"`
    ShippingAddress       *Address           `json:"shipping_address"`
    PaymentMethod         string             `json:"payment"`
    ShippingDescription   string             `json:"shipping_description"`
    CreatedAt             string             `json:"created_at"`
}

func MapMagentoOrder(mo *MagentoOrder, customerID uint) *models.Order {
    return &models.Order{
        MagentoOrderID:       strconv.Itoa(mo.EntityID),
        OrderNumber:          mo.IncrementID,
        CustomerID:           customerID,
        Status:               mo.Status,
        State:                mo.State,
        Subtotal:             mo.Subtotal,
        TaxAmount:            mo.TaxAmount,
        ShippingAmount:       mo.ShippingAmount,
        DiscountAmount:       mo.DiscountAmount,
        GrandTotal:           mo.GrandTotal,
        CustomerEmail:        mo.CustomerEmail,
        CustomerFirstname:    mo.CustomerFirstname,
        CustomerLastname:     mo.CustomerLastname,
        BillingAddress:       jsonMarshal(mo.BillingAddress),
        ShippingAddress:      jsonMarshal(mo.ShippingAddress),
        PaymentMethod:        mo.PaymentMethod,
        ShippingDescription:  mo.ShippingDescription,
        OrderDate:            parseTime(mo.CreatedAt),
    }
}
```

---

## 8. CONSTRAINTS & ASSUMPTIONS

### 8.1 Technical Constraints
- Magento 2.4.x or higher (supports REST API v1)
- OAuth 2.0 authentication required
- MySQL 8.0+ for database
- Go 1.24+ for backend
- React for frontend (existing gocrm-ui)

### 8.2 Business Constraints
- Initial rollout to sales team only (max 50 users)
- Single Magento instance initially (multi-tenant later)
- English language only (i18n later)
- USD currency primary (multi-currency later)

### 8.3 Assumptions
- Magento instance is accessible via HTTPS
- Magento has webhooks configured or accessible for polling
- Product catalog changes < 100 products/day
- Order volume < 500 orders/day initially
- Network latency to Magento < 100ms
- Sales team has basic CRM training

---

## 9. RISKS & MITIGATION

| Risk | Impact | Probability | Mitigation |
|------|--------|-------------|------------|
| Magento API rate limits | High | Medium | Implement request queuing, caching, backoff |
| Data sync conflicts | High | Medium | Implement conflict resolution, audit logs |
| Large catalog performance | Medium | High | Implement pagination, indexing, caching |
| Webhook delivery failures | Medium | Medium | Implement retry queue, polling fallback |
| OAuth token expiration | Medium | Low | Auto-refresh tokens, monitor expiry |
| Schema changes in Magento | High | Low | Version API calls, schema validation |
| Network connectivity issues | High | Low | Offline mode for quotes, queue sync |

---

## 10. SUCCESS CRITERIA

### 10.1 Technical Success
- [ ] 100% of Magento orders synced to CRM within 5 minutes
- [ ] Zero data loss during sync operations
- [ ] < 0.1% API error rate
- [ ] Product search responds in < 200ms
- [ ] Quote creation completes in < 2 seconds

### 10.2 Business Success
- [ ] 80% of sales team actively using quote feature
- [ ] 50% reduction in quote creation time
- [ ] 25% increase in quote-to-order conversion
- [ ] 100% visibility into customer order history
- [ ] 90% customer satisfaction with quote process

### 10.3 Quality Success
- [ ] Unit test coverage > 80%
- [ ] Integration test coverage > 70%
- [ ] E2E test coverage for all critical paths
- [ ] Zero critical bugs in production
- [ ] < 5 medium bugs per month

---

## 11. OUT OF SCOPE (v1.0)

The following features are explicitly out of scope for the initial release:

- Multi-Magento instance support
- Shopify/WooCommerce integrations
- Multi-currency support
- Multi-language support
- Custom product configurators in CRM
- Inventory management (purchase orders, transfers)
- Return merchandise authorization (RMA)
- Customer portal
- Mobile app
- Advanced analytics/BI
- Email marketing integration
- Social media integration

---

## 12. APPENDICES

### Appendix A: Magento 2 API Authentication Flow
```
1. Obtain consumer credentials from Magento admin
2. Request token: POST /oauth/token/request
3. Authorize token: Visit Magento admin authorization URL
4. Exchange for access token: POST /oauth/token/access
5. Use access token in all API requests: Authorization: Bearer <token>
6. Refresh token before expiration
```

### Appendix B: Sample Quote JSON
```json
{
  "id": 123,
  "quote_number": "Q-2025-00123",
  "customer_id": 456,
  "status": "sent",
  "currency": "USD",
  "items": [
    {
      "sku": "PROD-001",
      "name": "Premium Widget",
      "qty": 10,
      "price": 49.99,
      "discount_percent": 10,
      "row_total": 449.91
    }
  ],
  "subtotal": 449.91,
  "discount_amount": 0,
  "tax_amount": 35.99,
  "shipping_amount": 15.00,
  "grand_total": 500.90,
  "valid_until": "2025-12-31T23:59:59Z",
  "created_at": "2025-11-15T10:30:00Z"
}
```

---

**Document Version:** 1.0
**Last Updated:** 2025-11-15
**Status:** DRAFT - Pending Approval
**Next Review:** 2025-11-22
