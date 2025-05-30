import { Page, expect } from '@playwright/test';
import { RegisterPage } from '../pages/register.page';
import { LoginPage } from '../pages/login.page';
import { DashboardPage } from '../pages/dashboard.page';
import { generateAdminUser, type AdminUser, testAdminCredentials } from '../fixtures/admin-user';

export class AdminAuthHelper {
  readonly page: Page;
  private adminUser: AdminUser | null = null;

  constructor(page: Page) {
    this.page = page;
  }

  /**
   * Creates a new admin user and logs them in
   */
  async createAndLoginAdmin(): Promise<AdminUser> {
    const loginPage = new LoginPage(this.page);
    const dashboardPage = new DashboardPage(this.page);

    // Use the existing admin user
    this.adminUser = {
      firstName: 'Test',
      lastName: 'Admin',
      email: testAdminCredentials.email,
      password: testAdminCredentials.password,
      role: 'admin'
    };

    // Login with the admin user
    await loginPage.goto();
    
    // Set up response listener before making the call
    const responsePromise = this.page.waitForResponse(
      response => response.url().includes('/api/auth/login')
    );
    
    await loginPage.login(this.adminUser.email, this.adminUser.password);
    const response = await responsePromise;
    
    // Verify login was successful
    expect(response.status()).toBe(200);
    
    // Wait for redirect to dashboard
    await this.page.waitForURL('/', { timeout: 10000 });
    await dashboardPage.waitForDashboardToLoad();
    
    // Verify admin is logged in
    const token = await this.page.evaluate(() => localStorage.getItem('gophercrm_token'));
    expect(token).toBeTruthy();

    return this.adminUser;
  }

  /**
   * Logs in an existing admin user
   */
  async loginExistingAdmin(adminUser: AdminUser): Promise<void> {
    const loginPage = new LoginPage(this.page);
    const dashboardPage = new DashboardPage(this.page);

    await loginPage.goto();
    await loginPage.login(adminUser.email, adminUser.password);
    
    // Wait for login response
    const response = await loginPage.submitAndWaitForResponse();
    expect(response.status()).toBe(200);
    
    // Wait for redirect to dashboard
    await this.page.waitForURL('/', { timeout: 10000 });
    await dashboardPage.waitForDashboardToLoad();
    
    this.adminUser = adminUser;
  }

  /**
   * Ensures admin is logged in (creates new admin if needed)
   */
  async ensureAdminLoggedIn(): Promise<AdminUser> {
    try {
      // First, navigate to the app to ensure we're on a proper page
      const currentUrl = this.page.url();
      if (!currentUrl.includes('localhost:5173') || currentUrl === 'about:blank') {
        await this.page.goto('/');
        await this.page.waitForLoadState('networkidle');
      }

      // Check if already logged in
      const token = await this.page.evaluate(() => {
        try {
          return localStorage.getItem('gophercrm_token');
        } catch {
          return null;
        }
      });
      
      if (this.page.url().includes('/login') || !token) {
        return await this.createAndLoginAdmin();
      }
      
      // If we have a stored admin user, return it
      if (this.adminUser) {
        return this.adminUser;
      }
      
      // Otherwise create new admin
      return await this.createAndLoginAdmin();
    } catch (error) {
      // If anything fails, create a new admin
      return await this.createAndLoginAdmin();
    }
  }

  /**
   * Logs out the current admin user
   */
  async logout(): Promise<void> {
    try {
      // Navigate to a main page first to ensure we're in the right place
      await this.page.goto('/');
      await this.page.waitForLoadState('networkidle');
      
      const dashboardPage = new DashboardPage(this.page);
      await dashboardPage.logout();
      
      // Wait for redirect to login page
      await this.page.waitForURL('/login', { timeout: 5000 });
    } catch (error) {
      // If logout fails, just clear the token manually
      await this.page.evaluate(() => {
        try {
          localStorage.removeItem('gophercrm_token');
          localStorage.removeItem('gophercrm_refresh_token');
        } catch {}
      });
      
      // Navigate to login page manually
      await this.page.goto('/login');
    }
    
    this.adminUser = null;
  }

  /**
   * Returns the current admin user
   */
  getCurrentAdmin(): AdminUser | null {
    return this.adminUser;
  }

  /**
   * Navigates to a specific page while ensuring admin is logged in
   */
  async navigateAsAdmin(path: string): Promise<AdminUser> {
    const admin = await this.ensureAdminLoggedIn();
    await this.page.goto(path);
    await this.page.waitForLoadState('networkidle');
    return admin;
  }
}