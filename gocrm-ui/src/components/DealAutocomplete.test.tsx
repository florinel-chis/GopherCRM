import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent } from '@/test/test-utils';
import { DealAutocomplete } from './DealAutocomplete';
import { dealsApi, type DealFilters } from '@/api/endpoints';
import { createMockDeal } from '@/test/factories';
import { createProductionQueryClient } from '@/test/productionQueryClient';
import { withClient } from '@/test/dealQueryCache';

vi.mock('@/api/endpoints', () => ({
  dealsApi: {
    getDeals: vi.fn(),
    getDeal: vi.fn(),
  },
}));

// Under full-suite load the debounced search plus the option render can
// outlast the default one-second wait.
const WAIT = { timeout: 3000 };

const openDeal = createMockDeal({ id: 3, title: 'Acme renewal', stage: 'proposal' });
const wonDeal = createMockDeal({ id: 4, title: 'Acme expansion', stage: 'won' });

// The term the user types; an empty search returns nothing, so every option on
// screen comes from the response to this term.
const TERM = 'acme';

const typeTerm = (label = 'Deal') => {
  const input = screen.getByLabelText(label);
  input.focus();
  fireEvent.change(input, { target: { value: TERM } });
};

const callsFor = (term: string): DealFilters[] =>
  vi
    .mocked(dealsApi.getDeals)
    .mock.calls.map(([filters]) => filters ?? {})
    .filter((filters) => filters.search === term);

describe('DealAutocomplete', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  describe('open-only filter', () => {
    it('keeps the open-only and any-deal searches in separate caches', async () => {
      // What the API answers: open-only searches carry only the open deal, an
      // any-deal search carries the won deal too.
      vi.mocked(dealsApi.getDeals).mockImplementation(async (filters) => {
        if (!filters?.search) {
          return { deals: [], total: 0 };
        }
        const deals = filters.open === true ? [openDeal] : [openDeal, wonDeal];
        return { deals, total: deals.length };
      });
      const client = createProductionQueryClient();

      // 1. The quote dialog's picker (open-only) searches the term.
      const first = render(withClient(client, <DealAutocomplete value={null} onChange={vi.fn()} />));
      typeTerm();
      expect(await screen.findByRole('option', { name: 'Acme renewal' }, WAIT)).toBeInTheDocument();
      expect(screen.queryByRole('option', { name: 'Acme expansion' })).not.toBeInTheDocument();
      expect(callsFor(TERM)).toEqual([{ search: TERM, open: true, limit: 20 }]);
      first.unmount();

      // 2. The task form's picker (any deal) searches the same term: it must
      // not be served the open-only page, and sends no open key.
      const second = render(
        withClient(
          client,
          <DealAutocomplete
            value={null}
            onChange={vi.fn()}
            openOnly={false}
            testId="task-deal-autocomplete"
          />
        )
      );
      typeTerm();
      expect(await screen.findByRole('option', { name: 'Acme expansion' }, WAIT)).toBeInTheDocument();
      expect(screen.getByRole('option', { name: 'Acme renewal' })).toBeInTheDocument();
      const termCalls = callsFor(TERM);
      expect(termCalls).toHaveLength(2);
      expect(termCalls[1]).toEqual({ search: TERM, limit: 20 });
      expect(termCalls[1]).not.toHaveProperty('open');
      second.unmount();

      // 3. The open-only picker again: its own cached page answers (no further
      // request, which would fail here), never the any-deal page with the won
      // deal.
      vi.mocked(dealsApi.getDeals).mockRejectedValue(new Error('no further request expected'));
      render(withClient(client, <DealAutocomplete value={null} onChange={vi.fn()} />));
      typeTerm();
      expect(await screen.findByRole('option', { name: 'Acme renewal' }, WAIT)).toBeInTheDocument();
      // Let the debounced term settle before checking nothing else turned up.
      await new Promise((resolve) => setTimeout(resolve, 400));
      expect(screen.getByRole('option', { name: 'Acme renewal' })).toBeInTheDocument();
      expect(screen.queryByRole('option', { name: 'Acme expansion' })).not.toBeInTheDocument();
      expect(callsFor(TERM)).toHaveLength(2);
    });

    it("keeps the quote picker's open-only search and test id by default", async () => {
      vi.mocked(dealsApi.getDeals).mockResolvedValue({ deals: [], total: 0 });
      const client = createProductionQueryClient();

      const { container } = render(
        withClient(client, <DealAutocomplete value={null} onChange={vi.fn()} />)
      );

      expect(container.querySelector('[data-testid="deal-autocomplete"]')).not.toBeNull();
      expect(container.querySelector('[data-testid="task-deal-autocomplete"]')).toBeNull();

      const input = screen.getByLabelText('Deal');
      input.focus();
      fireEvent.mouseDown(input);
      expect(await screen.findByText('Type to search open deals', {}, WAIT)).toBeInTheDocument();
      expect(dealsApi.getDeals).toHaveBeenCalledWith({ search: undefined, open: true, limit: 20 });

      fireEvent.change(input, { target: { value: 'nothing' } });
      expect(await screen.findByText('No matching open deals', {}, WAIT)).toBeInTheDocument();
      await waitFor(() =>
        expect(dealsApi.getDeals).toHaveBeenCalledWith({ search: 'nothing', open: true, limit: 20 })
      );
      for (const [filters] of vi.mocked(dealsApi.getDeals).mock.calls) {
        expect(filters).toMatchObject({ open: true });
      }
    });
  });
});
