import { describe, it, expect } from 'vitest';
import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { WonThisMonthTile } from './WonThisMonthTile';
import { formatDealAmount } from '@/pages/deals/dealFormat';
import type { WonThisMonth } from '@/types';

const renderTile = (wonThisMonth: WonThisMonth | undefined, isLoading = false) =>
  render(
    <MemoryRouter>
      <WonThisMonthTile wonThisMonth={wonThisMonth} isLoading={isLoading} />
    </MemoryRouter>
  );

describe('WonThisMonthTile', () => {
  it('shows the count and one total per currency', () => {
    renderTile({
      count: 3,
      totals: [
        { currency: 'EUR', amount_cents: 450000 },
        { currency: 'USD', amount_cents: 99900 },
      ],
    });

    expect(screen.getByRole('region', { name: 'Won this month' })).toBeInTheDocument();
    expect(screen.getByTestId('won-this-month-count')).toHaveTextContent('3');
    const totals = screen.getAllByTestId('won-this-month-total');
    expect(totals.map((total) => total.getAttribute('data-currency'))).toEqual(['EUR', 'USD']);
    expect(totals[0]).toHaveTextContent(formatDealAmount(450000, 'EUR'));
    expect(totals[1]).toHaveTextContent(formatDealAmount(99900, 'USD'));
    expect(screen.getByRole('link', { name: 'View the board' })).toHaveAttribute('href', '/deals/board');
  });

  it('shows zero and an empty-state line when nothing was won this month', () => {
    renderTile({ count: 0, totals: [] });

    expect(screen.getByTestId('won-this-month-count')).toHaveTextContent('0');
    expect(screen.getByText('No deals won yet this month')).toBeInTheDocument();
    expect(screen.queryByTestId('won-this-month-total')).not.toBeInTheDocument();
  });

  it('says the pipeline could not be loaded instead of showing zero when the request failed', () => {
    render(
      <MemoryRouter>
        <WonThisMonthTile wonThisMonth={undefined} isLoading={false} isError />
      </MemoryRouter>
    );

    expect(screen.getByRole('alert')).toHaveTextContent('Pipeline could not be loaded');
    expect(screen.queryByTestId('won-this-month-count')).not.toBeInTheDocument();
    expect(screen.queryByText('No deals won yet this month')).not.toBeInTheDocument();
  });

  it('shows a placeholder while loading', () => {
    renderTile(undefined, true);

    expect(screen.queryByTestId('won-this-month-count')).not.toBeInTheDocument();
  });
});
