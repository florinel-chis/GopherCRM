# Admin Entity Testing Suite

This document describes the comprehensive admin testing suite for the GoCRM application, which tests all major entities (Leads, Customers, Tickets, Tasks, Users) with admin user privileges.

## Overview

The admin testing suite consists of:
- **Admin authentication helpers** for managing admin login/logout
- **Page object models** for each entity (Leads, Customers, Tickets, Tasks, Users)
- **CRUD test suites** for each entity
- **Comprehensive workflow tests** that demonstrate real CRM usage patterns
- **Data cleanup utilities** for maintaining test database hygiene

## Test Structure

```
e2e/
├── fixtures/
│   └── admin-user.ts          # Test data generators for all entities
├── helpers/
│   └── admin-auth.ts          # Admin authentication helper
├── pages/
│   ├── login.page.ts          # Login page object
│   ├── leads.page.ts          # Leads management page object
│   ├── customers.page.ts      # Customers management page object
│   ├── tickets.page.ts        # Tickets management page object
│   ├── tasks.page.ts          # Tasks management page object
│   └── users.page.ts          # Users management page object
├── scripts/
│   └── cleanup-admin-test-data.sh  # Database cleanup script
└── tests/
    ├── admin-leads.spec.ts    # Leads CRUD tests
    ├── admin-customers.spec.ts # Customers CRUD tests
    ├── admin-tickets.spec.ts  # Tickets CRUD tests
    ├── admin-tasks.spec.ts    # Tasks CRUD tests
    ├── admin-users.spec.ts    # Users management tests
    └── admin-entity-suite.spec.ts  # Comprehensive workflow tests
```

## Test Coverage

### Per Entity Tests
Each entity test suite covers:
- ✅ **View list page** - Navigation and page load
- ✅ **Create new entity** - Form filling and submission
- ✅ **Edit existing entity** - Data modification
- ✅ **View entity details** - Detail page navigation
- ✅ **Delete entity** - Removal functionality
- ✅ **Search functionality** - Finding specific records
- ✅ **Filter functionality** - Status/priority/role filtering
- ✅ **Form validation** - Required fields and data validation
- ✅ **Form cancellation** - Cancel button behavior
- ✅ **Navigation efficiency** - Moving between records

### Entity-Specific Features

#### Leads
- Lead source tracking
- Lead status management (new → contacted → qualified → closed)
- Lead conversion workflows
- Company and contact information management

#### Customers
- Complete address information
- Customer contact details
- Customer history tracking
- Duplicate email validation

#### Tickets
- Priority management (low, medium, high, urgent)
- Status tracking (open → in_progress → resolved → closed)
- Category classification
- Customer assignment
- Support workflow management

#### Tasks
- Due date management
- Priority levels
- Status progression (pending → in_progress → completed)
- Task assignment
- Related entity linking

#### Users
- Role-based access control (admin, sales, support, customer)
- User activation/deactivation
- Password validation
- Admin self-protection (cannot delete own account)
- Permission management through roles

### Comprehensive Workflow Tests
The entity suite includes realistic CRM workflows:
- **Lead to Customer conversion** - Complete sales process
- **Customer support workflow** - Ticket creation and task assignment
- **User role management** - Creating and managing different user types
- **Bulk operations** - Creating and managing multiple records
- **Cross-entity search** - Finding related information across entities
- **Error handling** - Graceful handling of invalid data

## Running the Tests

### Prerequisites
1. GoCRM backend server running on http://localhost:8080
2. MySQL database accessible
3. Frontend development server (optional, tests can run against built version)

### Available Commands

```bash
# Run all admin tests (headless)
npm run test:e2e:admin

# Run all admin tests (with browser UI)
npm run test:e2e:admin:headed

# Run comprehensive workflow suite only
npm run test:e2e:admin:suite

# Run specific entity tests
npx playwright test e2e/tests/admin-leads.spec.ts --headed
npx playwright test e2e/tests/admin-customers.spec.ts --headed
npx playwright test e2e/tests/admin-tickets.spec.ts --headed
npx playwright test e2e/tests/admin-tasks.spec.ts --headed
npx playwright test e2e/tests/admin-users.spec.ts --headed

# Clean up test data
npm run test:e2e:admin:cleanup
```

### Test Configuration
Tests use the slow configuration for better reliability:
- Sequential execution (no parallel tests)
- Extended timeouts (30 seconds)
- Increased wait times for UI updates
- Browser visible for debugging

## Test Data Management

### Data Generation
All test data is generated using Faker.js with realistic patterns:
- **Names**: Realistic first/last names
- **Emails**: Unique timestamped emails (@example.com domain)
- **Addresses**: Complete address information
- **Dates**: Future dates for due dates, past dates for testing edge cases
- **Phone numbers**: Valid phone number formats
- **Companies**: Realistic company names

### Data Cleanup
The cleanup script removes test data based on patterns:
- Emails containing `@example.com`
- Names starting with `SearchTest`, `BatchTest`, `TestUser`, etc.
- Titles containing test keywords
- Admin users created during testing (preserves real admin accounts)

### Database Considerations
- Tests create real data in the database
- Each test creates an admin user and logs in
- Tests are isolated (each starts fresh)
- Cleanup script resets auto-increment counters
- Production data is protected by pattern matching

## Authentication Flow

### Admin User Creation
1. Generate unique admin user data
2. Navigate to registration page
3. Fill registration form with admin role
4. Submit and wait for successful registration
5. Verify redirect to dashboard
6. Confirm authentication token storage

### Session Management
- Each test gets a fresh admin session
- Login state is verified before entity operations
- Logout happens after each test
- No session sharing between tests

## Error Handling and Edge Cases

### Form Validation
- Empty required fields
- Invalid email formats
- Password complexity requirements
- Password confirmation matching
- Duplicate email prevention
- Date validation (past due dates)

### UI State Management
- Form cancellation behavior
- Navigation between entities
- Page load verification
- Error message display
- Success message confirmation

### Data Integrity
- Relationship maintenance
- Duplicate prevention
- Status transition validation
- Role permission enforcement

## Debugging Tests

### Common Issues
1. **Backend not running**: Ensure GoCRM server is accessible
2. **Database connection**: Verify MySQL connection in cleanup script
3. **Timeout errors**: Tests may need longer wait times for slow systems
4. **Element not found**: Page objects may need locator updates

### Debug Commands
```bash
# Run with debug mode
npx playwright test e2e/tests/admin-leads.spec.ts --debug

# Run with UI mode for interactive debugging
npx playwright test e2e/tests/admin-leads.spec.ts --ui

# Generate test report
npm run test:e2e:report
```

### Screenshots and Videos
- Failed tests automatically generate screenshots
- Test runs include video recordings
- Debug artifacts stored in `test-results/` directory

## Extending the Test Suite

### Adding New Entity Tests
1. Create page object in `e2e/pages/`
2. Add data generator to `fixtures/admin-user.ts`
3. Create test file following existing patterns
4. Update cleanup script patterns
5. Add npm script commands

### Adding New Test Scenarios
1. Follow existing test patterns
2. Use AdminAuthHelper for authentication
3. Use page objects for UI interactions
4. Include cleanup considerations
5. Add appropriate assertions

### Best Practices
- Always use page objects for UI interactions
- Generate unique test data to avoid conflicts
- Include proper cleanup patterns
- Use descriptive test and variable names
- Add console.log statements for workflow tracking
- Handle async operations with proper waits
- Verify state changes after operations

## Performance Considerations

### Test Execution Time
- Each entity test suite: ~5-10 minutes
- Complete workflow suite: ~10-15 minutes
- Total admin test suite: ~30-45 minutes

### Optimization Strategies
- Tests run sequentially for reliability
- Minimal wait times while ensuring stability
- Efficient data generation
- Targeted cleanup operations
- Reuse admin sessions where possible

### Resource Usage
- Each test creates fresh admin user
- Database grows during test execution
- Cleanup script manages data accumulation
- Browser resources managed by Playwright

This testing suite provides comprehensive coverage of admin functionality across all major CRM entities, ensuring the application works correctly for administrative users managing the complete customer lifecycle.