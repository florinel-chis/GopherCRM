import { test, expect } from '@playwright/test';
import { RegisterPage } from '../pages/register.page';
import { DashboardPage } from '../pages/dashboard.page';
import { generateTestUser } from '../fixtures/test-data';

test.describe('Debug Avatar Display', () => {
  test('check avatar after registration', async ({ page }) => {
    const registerPage = new RegisterPage(page);
    const dashboardPage = new DashboardPage(page);
    
    // Use a specific test user for debugging
    const user = {
      firstName: 'Alice',
      lastName: 'TestUser',
      email: `test_debug_${Date.now()}@example.com`,
      password: 'TestPassword123'
    };
    
    // Go to registration page
    await registerPage.goto();
    
    // Fill and submit form
    await registerPage.fillForm(user);
    await page.waitForTimeout(1000);
    
    // Submit and wait for navigation
    await registerPage.submit();
    await page.waitForURL('/', { timeout: 15000 });
    
    // Extra wait for dashboard to fully load
    await page.waitForTimeout(3000);
    
    // Take a screenshot for debugging
    await page.screenshot({ path: 'dashboard-after-registration.png', fullPage: true });
    
    // Try multiple ways to find the avatar
    console.log('Looking for avatar elements...');
    
    // Method 1: Find all avatars
    const avatars = await page.locator('.MuiAvatar-root').all();
    console.log(`Found ${avatars.length} avatars`);
    
    for (let i = 0; i < avatars.length; i++) {
      const text = await avatars[i].textContent();
      console.log(`Avatar ${i}: "${text}"`);
    }
    
    // Method 2: Find avatar in AppBar
    const appBarAvatar = page.locator('.MuiAppBar-root .MuiAvatar-root');
    if (await appBarAvatar.count() > 0) {
      const text = await appBarAvatar.textContent();
      console.log(`AppBar Avatar: "${text}"`);
      expect(text?.trim()).toBe('A'); // Should be 'A' for Alice
    }
    
    // Method 3: Click on the avatar to open menu
    const avatarButton = page.locator('button:has(.MuiAvatar-root)').last();
    await avatarButton.click();
    await page.waitForTimeout(1000);
    
    // Check menu content
    const menuItems = await page.locator('.MuiMenuItem-root').allTextContents();
    console.log('Menu items:', menuItems);
    
    // The first menu item should be the user's name
    expect(menuItems[0]).toBe('Alice TestUser');
    
    // Take screenshot with menu open
    await page.screenshot({ path: 'dashboard-with-menu.png', fullPage: true });
  });
});