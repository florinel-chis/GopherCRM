import { Page, Locator } from '@playwright/test';

export class DashboardPage {
  readonly page: Page;
  
  constructor(page: Page) {
    this.page = page;
  }

  // Locators
  get pageTitle() {
    return this.page.locator('h4:has-text("Dashboard")');
  }

  get userAvatar() {
    return this.page.locator('button:has(> .MuiAvatar-root)').last();
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

  // Stat cards
  get totalLeadsCard() {
    return this.page.locator('text=Total Leads').locator('..').locator('..');
  }

  get totalCustomersCard() {
    return this.page.locator('text=Total Customers').locator('..').locator('..');
  }

  get openTicketsCard() {
    return this.page.locator('text=Open Tickets').locator('..').locator('..');
  }

  get pendingTasksCard() {
    return this.page.locator('text=Pending Tasks').locator('..').locator('..');
  }

  get conversionRateCard() {
    return this.page.locator('text=Conversion Rate').locator('..').locator('..');
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

  async getStatValue(statCard: Locator): Promise<string> {
    const valueElement = statCard.locator('h4');
    return await valueElement.textContent() || '';
  }

  async getTotalLeads(): Promise<number> {
    const value = await this.getStatValue(this.totalLeadsCard);
    return parseInt(value, 10);
  }

  async getTotalCustomers(): Promise<number> {
    const value = await this.getStatValue(this.totalCustomersCard);
    return parseInt(value, 10);
  }

  async getOpenTickets(): Promise<number> {
    const value = await this.getStatValue(this.openTicketsCard);
    return parseInt(value, 10);
  }

  async getPendingTasks(): Promise<number> {
    const value = await this.getStatValue(this.pendingTasksCard);
    return parseInt(value, 10);
  }

  async getConversionRate(): Promise<string> {
    const value = await this.getStatValue(this.conversionRateCard);
    return value;
  }

  async navigateToLeads() {
    await this.sidebarLeadsLink.click();
    await this.page.waitForURL('**/leads');
  }

  async navigateToCustomers() {
    await this.sidebarCustomersLink.click();
    await this.page.waitForURL('**/customers');
  }

  async getUserDisplayName(): Promise<string | null> {
    try {
      // The user avatar contains the first letter of first name only
      // Look for the avatar button in the AppBar
      const avatarButton = this.page.locator('button:has(.MuiAvatar-root)').last();
      await avatarButton.waitFor({ state: 'visible', timeout: 5000 });
      const avatar = avatarButton.locator('.MuiAvatar-root');
      const avatarText = await avatar.textContent();
      return avatarText?.trim() || null;
    } catch {
      return null;
    }
  }

  async getUserFullName(): Promise<string | null> {
    try {
      // Open the user menu to see the full name
      await this.openUserMenu();
      
      // The first menu item shows the full name
      const nameElement = this.userMenu.locator('.MuiMenuItem-root').first();
      await nameElement.waitFor({ state: 'visible', timeout: 2000 });
      const fullName = await nameElement.textContent();
      
      // Close the menu
      await this.page.keyboard.press('Escape');
      
      return fullName?.trim() || null;
    } catch {
      return null;
    }
  }
}