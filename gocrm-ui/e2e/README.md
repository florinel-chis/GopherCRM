# GopherCRM E2E Tests

End-to-end browser automation tests for the GopherCRM registration flow using Playwright.

## Prerequisites

- Backend server running on http://localhost:8080
- Frontend dev server will auto-start on http://localhost:5173
- MySQL database running with gophercrm database

## Quick Start

```bash
# Run all tests (headless)
npm run test:e2e

# Run tests with browser visible
npm run test:e2e:headed

# Interactive UI mode (recommended for development)
npm run test:e2e:ui

# Debug mode (step through tests)
npm run test:e2e:debug

# View test report after running tests
npm run test:e2e:report

# Clean up test users from database
npm run test:e2e:cleanup
```

## Test Coverage

The registration tests cover:

✅ **Happy Path**
- Successful registration with valid data
- Automatic redirect to dashboard
- Token storage in localStorage
- User display in UI

✅ **Validation**
- Required field validation
- Email format validation
- Password strength requirements
- Password confirmation matching
- Error message display and clearing

✅ **User Experience**
- Password visibility toggle
- Form submission with Enter key
- Loading states during submission
- Form data preservation on errors
- Navigation to login page

✅ **Error Handling**
- Duplicate email registration
- Network errors
- Server errors

## Test Structure

```
e2e/
├── fixtures/
│   └── test-data.ts      # Test user data generation
├── pages/
│   ├── register.page.ts  # Registration page object model
│   └── dashboard.page.ts # Dashboard page object model
├── tests/
│   └── registration.spec.ts # Registration test suite
└── README.md            # This file
```

## Writing New Tests

1. **Use Page Objects**: Keep selectors and actions in page files
2. **Generate Test Data**: Use faker for dynamic test data
3. **Clean State**: Each test should be independent
4. **Wait Properly**: Use Playwright's built-in waiting mechanisms

Example:
```typescript
test('my new test', async ({ page }) => {
  const registerPage = new RegisterPage(page);
  const user = generateTestUser();
  
  await registerPage.goto();
  await registerPage.fillForm(user);
  await registerPage.submit();
  
  await expect(page).toHaveURL('/');
});
```

## Debugging Tips

1. **Use UI Mode**: Best for development
   ```bash
   npm run test:e2e:ui
   ```

2. **Add Screenshots**: For debugging failures
   ```typescript
   await page.screenshot({ path: 'debug.png' });
   ```

3. **Pause Execution**: Opens Playwright Inspector
   ```typescript
   await page.pause();
   ```

4. **View Console**: See browser console output
   ```typescript
   page.on('console', msg => console.log(msg.text()));
   ```

## Test Data Management

All test users are created with emails matching the pattern:
`test_{timestamp}_{random}@example.com`

This allows easy cleanup:
```bash
npm run test:e2e:cleanup
```

## Troubleshooting

**Tests fail with "Cannot connect to localhost:5173"**
- Make sure the frontend dev server is running
- Check if another process is using port 5173

**"User already exists" errors**
- Run `npm run test:e2e:cleanup` to remove test users
- Check if tests are creating users with static emails

**Tests are flaky**
- Add explicit waits for network requests
- Use `waitForLoadState('networkidle')`
- Check for race conditions in the UI

**Cannot see what's happening**
- Use headed mode: `npm run test:e2e:headed`
- Use debug mode: `npm run test:e2e:debug`
- Enable video recording in playwright.config.ts