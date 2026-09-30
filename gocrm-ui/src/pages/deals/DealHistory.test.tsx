import { describe, it, expect } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import { DealHistory } from './DealHistory';
import { createMockDealStageChange } from '@/test/factories';

describe('DealHistory', () => {
  it('renders one row per change, oldest first, with who and when', () => {
    render(
      <DealHistory
        history={[
          createMockDealStageChange({ id: 1, from_stage: null, to_stage: 'qualification', changed_at: '2026-09-29T08:00:00Z' }),
          createMockDealStageChange({
            id: 2,
            from_stage: 'qualification',
            to_stage: 'proposal',
            changed_by: { id: 2, first_name: 'Ana', last_name: 'Pop', email: 'ana@example.com' },
            changed_at: '2026-09-30T09:30:00Z',
          }),
        ]}
      />
    );

    const rows = within(screen.getByRole('list', { name: 'Stage history' })).getAllByTestId('deal-history-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Created in Qualification');
    expect(rows[0]).toHaveTextContent('Test User');
    expect(rows[1]).toHaveTextContent('Qualification to Proposal');
    expect(rows[1]).toHaveTextContent('Ana Pop');
    expect(rows[1]).toHaveTextContent('2026');
  });

  it('falls back to the user id when the row carries no user', () => {
    render(<DealHistory history={[createMockDealStageChange({ changed_by: undefined, changed_by_id: 7 })]} />);

    expect(screen.getByTestId('deal-history-row')).toHaveTextContent('user #7');
  });

  it('says so when there is nothing yet', () => {
    render(<DealHistory history={[]} />);

    expect(screen.getByText('No stage changes recorded')).toBeInTheDocument();
  });

  it('shows a loading line while the first page is in flight', () => {
    render(<DealHistory history={[]} isLoading />);

    expect(screen.getByText('Loading history…')).toBeInTheDocument();
  });
});
