# GopherCRM Data Model Documentation

This document describes the complete data model for the GopherCRM application based on the backend implementation.

## Base Model

All entities inherit from a common base model with the following fields:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| id | uint | Yes | Auto-generated unique identifier |
| created_at | timestamp | Yes | Record creation timestamp |
| updated_at | timestamp | Yes | Last update timestamp |
| deleted_at | timestamp | No | Soft delete timestamp |

## User Model

Represents system users with different roles and permissions.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| email | string | Yes | - | unique, max 255 | User's email address |
| password | string | Yes | - | max 255 | Hashed password (hidden in API responses) |
| first_name | string | Yes | - | max 100 | User's first name |
| last_name | string | Yes | - | max 100 | User's last name |
| role | enum | Yes | 'customer' | max 20 | User role (see below) |
| is_active | boolean | Yes | true | - | Account active status |
| last_login_at | timestamp | No | - | - | Last login timestamp |

### User Roles
- `admin` - Full system access
- `sales` - Access to leads and customers
- `support` - Access to tickets and customer support
- `customer` - Limited access to own data

### Relationships
- Has many Leads (as owner)
- Has many Tasks (as assignee)
- Has many APIKeys

## Lead Model

Represents potential customers in the sales pipeline.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| first_name | string | Yes | - | max 100 | Lead's first name |
| last_name | string | Yes | - | max 100 | Lead's last name |
| email | string | Yes | - | max 255 | Lead's email address |
| phone | string | No | - | max 50 | Phone number |
| company | string | No | - | max 200 | Company name |
| position | string | No | - | max 100 | Job position/title |
| source | string | No | - | max 100 | Lead source (e.g., website, referral) |
| status | enum | Yes | 'new' | max 20 | Lead status (see below) |
| notes | text | No | - | - | Additional notes |
| owner_id | uint | Yes | - | FK to users | Assigned sales user |
| customer_id | uint | No | - | FK to customers | ID when converted to customer |
| company_id | uint | No | - | FK to companies | Linked company record (independent of the text `company`) |

### Lead Status Values
- `new` - Newly created lead
- `contacted` - Initial contact made
- `qualified` - Qualified as potential customer
- `unqualified` - Not a good fit
- `converted` - Converted to customer

### Lead Sources (Common Values)
- `website` - Company website
- `referral` - Customer referral
- `cold_call` - Sales outreach
- `advertisement` - Ad campaigns
- `social_media` - Social platforms
- `email_campaign` - Email marketing
- `trade_show` - Events/trade shows
- `other` - Other sources

## Company Model

An organisation that leads and customers link to through `company_id`. Holds no personal data;
deleting one soft-deletes the row and clears the links. `domain` is unique among live rows,
checked in the service rather than by an index.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| name | string | Yes | - | max 200 | Company name (trimmed; not unique) |
| domain | string | No | - | max 255, unique among live rows | Normalised host, e.g. `acme.example` |
| website | string | No | - | max 255, http(s) URL | Website |
| industry | string | No | - | max 100 | Free text |
| employee_range | string | No | - | one of `1-10`, `11-50`, `51-200`, `201-500`, `501-1000`, `1000+` | Head-count band |
| phone | string | No | - | max 50 | Phone number |
| address | string | No | - | max 255 | Street address |
| city | string | No | - | max 100 | City |
| state | string | No | - | max 100 | State/Province |
| country | string | No | - | max 100 | Country |
| postal_code | string | No | - | max 20 | ZIP/Postal code |
| notes | text | No | - | max 65535 bytes | Free text |
| owner_id | uint | No | - | FK to users | Account manager |

`customer_count`, `lead_count` and `deal_count` are computed on read (live rows only) and are not
columns.

## Deal Model

A sales opportunity, optionally linked to a company, a customer and the lead it grew from, always
owned by a user. Holds no personal data; deleting one is a soft delete that keeps the history.
Money is an integer amount in minor units next to an ISO 4217 code — sums are only meaningful per
currency.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| title | string | Yes | - | max 200 | Deal title (trimmed) |
| stage | string | Yes | `qualification` | one of the stage values below | Pipeline stage |
| amount_cents | int64 | Yes | 0 | ≥ 0 | Amount in minor units |
| currency | string | Yes | `deals.default_currency` (EUR) | `^[A-Z]{3}$` | ISO 4217 code |
| probability | int | Yes | per stage | 0–100; won is 100, lost is 0 | Chance of closing |
| expected_close_date | date | No | - | `YYYY-MM-DD` on the wire | Expected close |
| closed_at | datetime | No | - | set on entering won or lost, cleared on leaving them | When the deal closed |
| lost_reason | string | No | - | max 255; stored only while lost | Why the deal was lost |
| source | string | No | - | max 100 | Free text |
| notes | text | No | - | max 65535 bytes | Free text |
| company_id | uint | No | - | FK to companies | Linked company |
| customer_id | uint | No | - | FK to customers | Linked customer |
| lead_id | uint | No | - | FK to leads | Lead it grew from |
| owner_id | uint | Yes | the caller | FK to users | Staff account working the deal |

### Deal Stage Values
- `qualification` (default probability 10)
- `proposal` (40)
- `negotiation` (70)
- `won` (100, closed)
- `lost` (0, closed)

### Deal Stage Change

`deal_stage_changes` is append-only: one row per stage the deal entered, written in the same
transaction as the create or the change, never updated, kept when the deal is soft-deleted.

| Field | Type | Required | Constraints | Description |
|-------|------|----------|-------------|-------------|
| deal_id | uint | Yes | FK to deals, indexed | The deal |
| from_stage | string | No | null on the creating row | Stage left |
| to_stage | string | Yes | a stage value | Stage entered |
| changed_by_id | uint | Yes | FK to users | Who made the change |
| changed_at | datetime | Yes | - | When |

## Customer Model

Represents confirmed customers in the system.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| first_name | string | Yes | - | max 100 | Customer's first name |
| last_name | string | Yes | - | max 100 | Customer's last name |
| email | string | Yes | - | unique, max 255 | Customer's email |
| phone | string | No | - | max 50 | Phone number |
| company | string | No | - | max 200 | Company name |
| position | string | No | - | max 100 | Job position |
| address | string | No | - | max 255 | Street address |
| city | string | No | - | max 100 | City |
| state | string | No | - | max 100 | State/Province |
| country | string | No | - | max 100 | Country |
| postal_code | string | No | - | max 20 | ZIP/Postal code |
| notes | text | No | - | - | Additional notes |
| user_id | uint | No | - | FK to users | Associated user account |
| company_id | uint | No | - | FK to companies | Linked company record (independent of the text `company`) |

### Relationships
- May have an associated User account
- Has many Tickets

## Ticket Model

Represents customer support tickets.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| title | string | Yes | - | max 255 | Ticket title/subject |
| description | text | Yes | - | - | Detailed description |
| status | enum | Yes | 'open' | max 20 | Ticket status (see below) |
| priority | enum | Yes | 'medium' | max 20 | Priority level (see below) |
| customer_id | uint | Yes | - | FK to customers | Customer who created ticket |
| assigned_to_id | uint | No | - | FK to users | Assigned support user |
| resolution | text | No | - | - | Resolution notes |

### Ticket Status Values
- `open` - New/unassigned ticket
- `in_progress` - Being worked on
- `resolved` - Solution provided
- `closed` - Ticket closed

### Ticket Priority Values
- `low` - Low priority
- `medium` - Normal priority
- `high` - High priority
- `urgent` - Urgent/critical

## Task Model

Represents tasks and to-dos for users.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| title | string | Yes | - | max 255 | Task title |
| description | text | No | - | - | Task details |
| status | enum | Yes | 'pending' | max 20 | Task status (see below) |
| priority | enum | Yes | 'medium' | max 20 | Priority level (see below) |
| due_date | timestamp | No | - | - | Due date |
| assigned_to_id | uint | Yes | - | FK to users | Assigned user |
| lead_id | uint | No | - | FK to leads | Related lead |
| customer_id | uint | No | - | FK to customers | Related customer |

### Task Status Values
- `pending` - Not started
- `in_progress` - Being worked on
- `completed` - Finished
- `cancelled` - Cancelled

### Task Priority Values
- `low` - Low priority
- `medium` - Normal priority
- `high` - High priority

## APIKey Model

Represents API keys for programmatic access.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| name | string | Yes | - | max 100 | Descriptive name |
| key_hash | string | Yes | - | unique, max 128 | Hashed API key (hidden); `hmac$` + 64 hex characters |
| prefix | string | Yes | - | max 8 | Key prefix for identification |
| user_id | uint | Yes | - | FK to users | Key owner |
| last_used_at | timestamp | No | - | - | Last usage timestamp |
| expires_at | timestamp | No | - | - | Expiration timestamp |
| is_active | boolean | Yes | true | - | Active status |

## Configuration Model

Represents system configuration settings that control application behavior.

### Fields

| Field | Type | Required | Default | Constraints | Description |
|-------|------|----------|---------|-------------|-------------|
| config_key | string | Yes | - | unique, max 255 | Configuration key identifier |
| value | string | Yes | - | - | Configuration value (JSON string) |
| type | enum | Yes | 'string' | max 20 | Value type (see below) |
| category | string | Yes | - | max 50 | Configuration category |
| description | string | Yes | - | max 500 | Human-readable description |
| default_value | string | Yes | - | - | Default value for reset operations |
| is_system | boolean | Yes | false | - | Whether it's a system configuration |
| is_read_only | boolean | Yes | false | - | Whether value can be modified |
| valid_values | string | No | - | - | JSON array of valid values |

### Configuration Types
- `string` - Text value
- `boolean` - True/false value
- `integer` - Numeric value
- `array` - JSON array

### Configuration Categories
- `general` - General application settings
- `ui` - User interface settings
- `security` - Security-related settings
- `leads` - Lead management settings
- `customers` - Customer management settings
- `deals` - Deals settings
- `tickets` - Ticket system settings
- `tasks` - Task management settings
- `integration` - Third-party integrations

### Key Configuration Settings
- `general.company_name` - Company name displayed in the application
- `ui.theme.primary_color` - Primary theme color
- `security.session_timeout_hours` - Session timeout duration
- `leads.conversion.allowed_statuses` - Lead statuses that allow conversion
- `leads.conversion.require_notes` - Whether conversion notes are required
- `leads.conversion.auto_assign_owner` - Auto-assign lead owner to customer
- `deals.default_currency` - Currency a new deal takes when none is sent (three upper-case letters; EUR as shipped; also served on `GET /configurations/ui`)
- `tickets.auto_assign_support` - Auto-assign tickets to support users

## API Request/Response Format

### Frontend to Backend Field Mapping

The frontend uses different field names for some models. Here's the mapping:

#### Lead Model
- Frontend: `company_name` → Backend: `company`
- Frontend: `contact_name` → Backend: `first_name` + `last_name`

#### Customer Model
- Frontend: `company_name` → Backend: `company`
- Frontend: `contact_name` → Backend: `first_name` + `last_name`

### Response Envelope

All API responses are wrapped in a standard envelope:

```json
{
  "success": boolean,
  "data": object | array,    // Only on success
  "error": {                  // Only on error
    "code": string,
    "message": string,
    "details": object
  },
  "meta": {
    "request_id": string,
    "page": number,           // For paginated responses
    "per_page": number,       // For paginated responses
    "total": number,          // For paginated responses
    "total_pages": number     // For paginated responses
  }
}
```

### Dashboard Statistics Response

The dashboard stats endpoint returns aggregated statistics:

```json
{
  "success": true,
  "data": {
    "total_leads": 25,
    "total_customers": 18,
    "open_tickets": 3,
    "pending_tasks": 7,
    "conversion_rate": 72.0
  }
}
```

### Error Codes
- `VALIDATION_ERROR` - Input validation failed
- `UNAUTHORIZED` - Authentication required
- `FORBIDDEN` - Access denied
- `NOT_FOUND` - Resource not found
- `CONFLICT` - Resource conflict (e.g., duplicate)
- `INTERNAL_ERROR` - Server error
- `BAD_REQUEST` - Invalid request
- `TOO_MANY_REQUESTS` - Rate limit exceeded

## Data Validation Rules

### Email Validation
- Must be valid email format
- Maximum 255 characters
- Must be unique (for User and Customer models)

### Password Requirements
- Minimum 8 characters
- Stored as bcrypt hash
- Never returned in API responses

### String Length Limits
- Names: 100 characters
- Email: 255 characters
- Company: 200 characters
- Phone: 50 characters
- Address: 255 characters
- Title fields: 255 characters
- Text fields: No limit

### Required Field Validation
All fields marked as "Required" must be provided on creation. Updates can be partial.

## Business Logic Rules

### Lead Conversion
- When a lead is converted to a customer, the `customer_id` field is populated
- Lead status changes to 'converted'
- A new customer record is created with data from the lead

### User Assignment
- Sales users can only assign leads to themselves unless they're admin
- Support users are assigned to tickets
- Tasks can be assigned to any active user

### Soft Deletes
- Records are not physically deleted
- `deleted_at` timestamp is set
- Deleted records are excluded from normal queries

### Timestamps
- All timestamps are stored in UTC
- `created_at` and `updated_at` are automatically managed
- Custom timestamps (like `last_login_at`) must be explicitly set