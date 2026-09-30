import { Page, Locator, Response, expect } from '@playwright/test';
import type { DealStageLabel } from './deals.page';

/** The pipeline board at /deals/board: one region per stage, cards with a "Deal actions" menu. */
export class DealsBoardPage {
  readonly page: Page;

  constructor(page: Page) {
    this.page = page;
  }

  /** Exists only on the board, so it marks the committed route. */
  get heading() {
    return this.page.getByRole('heading', { level: 4, name: 'Deal pipeline' });
  }

  /** The List / Board toggle shared by the list and the board. */
  viewToggle(name: 'List' | 'Board') {
    return this.page.getByRole('button', { name, exact: true });
  }

  /** A stage column, a region named by its stage label. */
  column(label: DealStageLabel): Locator {
    return this.page.getByRole('region', { name: label, exact: true });
  }

  columnHeader(label: DealStageLabel): Locator {
    return this.column(label).getByTestId('deal-board-column-header');
  }

  columnCount(label: DealStageLabel): Locator {
    return this.column(label).getByTestId('deal-board-count');
  }

  /** The card of a deal in one column, found by its stamped title. */
  cardIn(label: DealStageLabel, title: string): Locator {
    return this.column(label).getByTestId('deal-board-card').filter({ hasText: title });
  }

  /** Won and Lost start collapsed; their header is a button with aria-expanded. */
  closedColumnToggle(label: 'Won' | 'Lost'): Locator {
    return this.page.getByRole('button', { name: label, exact: true });
  }

  get lostDialog() {
    return this.page.getByRole('dialog', { name: 'Mark deal as lost' });
  }

  async goto() {
    await this.page.goto('/deals/board');
    await expect(this.heading).toBeVisible();
  }

  /**
   * The number in a column header ("3 deals"). Waits for the pipeline to have
   * loaded; the header shows a skeleton before that.
   */
  async countIn(label: DealStageLabel): Promise<number> {
    const count = this.columnCount(label);
    await expect(count).toHaveText(/^\d+ deals?$/);
    return parseInt((await count.textContent()) ?? '', 10);
  }

  async expand(label: 'Won' | 'Lost') {
    const toggle = this.closedColumnToggle(label);
    if ((await toggle.getAttribute('aria-expanded')) !== 'true') {
      await toggle.click();
    }
    await expect(toggle).toHaveAttribute('aria-expanded', 'true');
  }

  private async openMenu(title: string) {
    await this.page.getByRole('button', { name: `Deal actions for ${title}` }).click();
    await expect(this.page.getByRole('menu')).toBeVisible();
  }

  private stageResponse() {
    return this.page.waitForResponse(
      (response) =>
        /\/deals\/\d+\/stage$/.test(new URL(response.url()).pathname) &&
        response.request().method() === 'POST'
    );
  }

  /** Moves a card through its menu to an open stage or to won; returns the stage response. */
  async moveCard(title: string, target: Exclude<DealStageLabel, 'Lost'>): Promise<Response> {
    await this.openMenu(title);
    const responsePromise = this.stageResponse();
    await this.page.getByRole('menuitem', { name: `Move to ${target}`, exact: true }).click();
    return await responsePromise;
  }

  /** Moves a card to lost: the menu opens the reason dialog, which is confirmed with `reason`. */
  async moveCardToLost(title: string, reason: string): Promise<Response> {
    await this.openMenu(title);
    await this.page.getByRole('menuitem', { name: 'Move to Lost', exact: true }).click();
    await expect(this.lostDialog).toBeVisible();
    await this.lostDialog.getByLabel(/^Lost reason/).fill(reason);
    const responsePromise = this.stageResponse();
    await this.lostDialog.getByRole('button', { name: 'Mark as lost' }).click();
    const response = await responsePromise;
    await expect(this.lostDialog).toBeHidden();
    return response;
  }
}
