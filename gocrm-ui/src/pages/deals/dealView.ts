// The deals pages remember whether the viewer last chose the list or the
// board. A per-browser convenience only: storage can be missing or throw
// (private windows, blocked site data), and then the list is the default.
export type DealView = 'list' | 'board';

export const DEAL_VIEW_STORAGE_KEY = 'gcrm.deals.view';

export const readDealView = (): DealView => {
  try {
    return localStorage.getItem(DEAL_VIEW_STORAGE_KEY) === 'board' ? 'board' : 'list';
  } catch {
    return 'list';
  }
};

export const writeDealView = (view: DealView): void => {
  try {
    localStorage.setItem(DEAL_VIEW_STORAGE_KEY, view);
  } catch {
    // Not remembered; the toggle still navigates.
  }
};
