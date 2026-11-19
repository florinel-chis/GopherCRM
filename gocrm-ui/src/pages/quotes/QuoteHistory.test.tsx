import { describe, it, expect } from 'vitest';
import { render, screen, within } from '@/test/test-utils';
import { QuoteHistory } from './QuoteHistory';
import { createMockQuoteStatusChange } from '@/test/factories';

describe('QuoteHistory', () => {
  it('renders one row per change, oldest first, with who and when', () => {
    render(
      <QuoteHistory
        history={[
          createMockQuoteStatusChange({ id: 1, from_status: null, to_status: 'draft', changed_at: '2026-10-26T08:00:00Z' }),
          createMockQuoteStatusChange({
            id: 2,
            from_status: 'draft',
            to_status: 'sent',
            changed_by_id: 2,
            changed_by: { id: 2, first_name: 'Ana', last_name: 'Pop' },
            changed_at: '2026-10-27T09:30:00Z',
          }),
        ]}
      />
    );

    const rows = within(screen.getByRole('list', { name: 'Status history' })).getAllByTestId('quote-history-row');
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Created as Draft');
    expect(rows[0]).toHaveTextContent('Test User');
    expect(rows[1]).toHaveTextContent('Draft to Sent');
    expect(rows[1]).toHaveTextContent('Ana Pop');
    expect(rows[1]).toHaveTextContent('2026');
    expect(rows[1]).not.toHaveTextContent('Client');
  });

  it('shows a client answer as the client and a staff row without a user by its id', () => {
    render(
      <QuoteHistory
        history={[
          createMockQuoteStatusChange({
            id: 2,
            from_status: 'draft',
            to_status: 'sent',
            source: 'staff',
            changed_by_id: 7,
            changed_by: undefined,
          }),
          createMockQuoteStatusChange({
            id: 3,
            from_status: 'sent',
            to_status: 'accepted',
            source: 'client',
            changed_by_id: null,
            changed_by: undefined,
          }),
        ]}
      />
    );

    const rows = screen.getAllByTestId('quote-history-row');
    expect(rows[0]).toHaveTextContent('Draft to Sent');
    expect(rows[0]).toHaveTextContent('user #7');
    expect(rows[0]).not.toHaveTextContent('Client');
    expect(rows[1]).toHaveTextContent('Sent to Accepted');
    expect(rows[1]).toHaveTextContent('Client');
    expect(rows[1]).not.toHaveTextContent('user #');
    expect(rows[1]).not.toHaveTextContent('null');
  });

  it('shows a declined answer as the client too, even next to a user summary', () => {
    render(
      <QuoteHistory
        history={[
          createMockQuoteStatusChange({ id: 4, from_status: 'sent', to_status: 'rejected', source: 'client', changed_by_id: null }),
        ]}
      />
    );

    const row = screen.getByTestId('quote-history-row');
    expect(row).toHaveTextContent('Sent to Rejected');
    expect(row).toHaveTextContent('Client');
    expect(row).not.toHaveTextContent('Test User');
  });

  it('says so when there is nothing yet, and while loading', () => {
    const { rerender } = render(<QuoteHistory history={[]} />);
    expect(screen.getByText('No status changes recorded')).toBeInTheDocument();

    rerender(<QuoteHistory history={[]} isLoading />);
    expect(screen.getByText('Loading history…')).toBeInTheDocument();
  });
});
