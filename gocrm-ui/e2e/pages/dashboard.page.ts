import { Page, expect } from '@playwright/test';

export class DashboardPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators
  get pageTitle() {
    return this.page.locator('h4:has-text("Dashboard")');
  }

  /** The app bar button that opens the account menu, addressed by its accessible name. */
  get userAvatar() {
    return this.page.getByRole('button', { name: 'Account menu' });
  }

  get userMenu() {
    return this.page.locator('[role="menu"]');
  }

  get logoutMenuItem() {
    return this.page.locator('[role="menuitem"]:has-text("Logout")');
  }

  get profileMenuItem() {
    return this.page.locator('[role="menuitem"]:has-text("Profile")');
  }

  // Deal widgets (admin and sales)
  get wonThisMonthTile() {
    return this.page.getByRole('region', { name: 'Won this month' });
  }

  get wonThisMonthCount() {
    return this.page.getByTestId('won-this-month-count');
  }

  /** Loads the dashboard and returns the "Won this month" count once it has rendered. */
  async readWonThisMonth(): Promise<number> {
    await this.page.goto('/');
    await expect(this.pageTitle).toBeVisible();
    await expect(this.wonThisMonthCount).toHaveText(/^\d+$/);
    return parseInt((await this.wonThisMonthCount.textContent()) ?? '', 10);
  }

  // Quick action buttons
  get newLeadButton() {
    return this.page.locator('button:has-text("New Lead")');
  }

  get newTicketButton() {
    return this.page.locator('button:has-text("New Ticket")');
  }

  get newTaskButton() {
    return this.page.locator('button:has-text("New Task")');
  }

  get viewCustomersButton() {
    return this.page.locator('button:has-text("View Customers")');
  }

  // Navigation
  get sidebarLeadsLink() {
    return this.page.locator('text=Leads').first();
  }

  get sidebarCustomersLink() {
    return this.page.locator('text=Customers').first();
  }

  get sidebarTicketsLink() {
    return this.page.locator('text=Tickets').first();
  }

  get sidebarTasksLink() {
    return this.page.locator('text=Tasks').first();
  }

  // Actions
  async waitForDashboardToLoad() {
    await this.pageTitle.waitFor({ state: 'visible' });
    await this.page.waitForLoadState('networkidle');
  }

  async openUserMenu() {
    await this.userAvatar.click();
    await this.userMenu.waitFor({ state: 'visible' });
  }

  async logout() {
    await this.openUserMenu();
    await this.logoutMenuItem.click();
  }

  async navigateToLeads() {
    await this.sidebarLeadsLink.click();
    await this.page.waitForURL('**/leads');
  }

  async navigateToCustomers() {
    await this.sidebarCustomersLink.click();
    await this.page.waitForURL('**/customers');
  }
}
