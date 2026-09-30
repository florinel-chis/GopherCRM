import { describe, it, expect, vi } from 'vitest';
import { render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { PipelineChart } from './PipelineChart';
import { formatDealAmount } from '@/pages/deals/dealFormat';
import type { PipelineStage } from '@/types';

// recharts' ResponsiveContainer needs ResizeObserver, which jsdom lacks.
global.ResizeObserver = vi.fn().mockImplementation(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

const renderChart = (stages: PipelineStage[] | undefined, defaultCurrency = 'EUR', isLoading = false) =>
  render(
    <MemoryRouter>
      <PipelineChart stages={stages} defaultCurrency={defaultCurrency} isLoading={isLoading} />
    </MemoryRouter>
  );

const empty = (stage: PipelineStage['stage']): PipelineStage => ({ stage, count: 0, totals: [] });

const mixed: PipelineStage[] = [
  {
    stage: 'qualification',
    count: 3,
    totals: [
      { currency: 'EUR', amount_cents: 300000, weighted_cents: 30000 },
      { currency: 'USD', amount_cents: 120000, weighted_cents: 12000 },
    ],
  },
  { stage: 'proposal', count: 1, totals: [{ currency: 'EUR', amount_cents: 50000, weighted_cents: 20000 }] },
  { stage: 'negotiation', count: 1, totals: [{ currency: 'RON', amount_cents: 700000, weighted_cents: 490000 }] },
  // Closed stages are not part of the chart.
  { stage: 'won', count: 5, totals: [{ currency: 'GBP', amount_cents: 999900, weighted_cents: 999900 }] },
  empty('lost'),
];

describe('PipelineChart', () => {
  it('sums the open stages in the default currency and lists every other currency per stage', () => {
    renderChart(mixed);

    expect(screen.getByRole('heading', { name: 'Pipeline by stage' })).toBeInTheDocument();
    expect(screen.getByTestId('pipeline-default-total')).toHaveTextContent(
      `Open pipeline in EUR: ${formatDealAmount(350000, 'EUR')}`
    );

    const table = screen.getByRole('table', { name: 'Other currencies' });
    const headers = within(table).getAllByRole('columnheader').map((cell) => cell.textContent);
    expect(headers).toEqual(['Currency', 'Qualification', 'Proposal', 'Negotiation']);
    const ron = within(table).getByRole('rowheader', { name: 'RON' }).closest('tr') as HTMLElement;
    expect(within(ron).getAllByRole('cell').map((cell) => cell.textContent)).toEqual([
      formatDealAmount(0, 'RON'),
      formatDealAmount(0, 'RON'),
      formatDealAmount(700000, 'RON'),
    ]);
    const usd = within(table).getByRole('rowheader', { name: 'USD' }).closest('tr') as HTMLElement;
    expect(within(usd).getAllByRole('cell')[0]).toHaveTextContent(formatDealAmount(120000, 'USD'));
    // Won deals (GBP here) are not in the open pipeline.
    expect(within(table).queryByRole('rowheader', { name: 'GBP' })).not.toBeInTheDocument();
    expect(within(table).getAllByRole('row')).toHaveLength(3);
  });

  it('draws in the configured default currency', () => {
    renderChart(mixed, 'USD');

    expect(screen.getByTestId('pipeline-default-total')).toHaveTextContent(
      `Open pipeline in USD: ${formatDealAmount(120000, 'USD')}`
    );
    const table = screen.getByRole('table', { name: 'Other currencies' });
    expect(within(table).getByRole('rowheader', { name: 'EUR' })).toBeInTheDocument();
    expect(within(table).queryByRole('rowheader', { name: 'USD' })).not.toBeInTheDocument();
  });

  it('shows no currency table when every open deal is in the default currency', () => {
    renderChart([
      { stage: 'qualification', count: 1, totals: [{ currency: 'EUR', amount_cents: 1000, weighted_cents: 100 }] },
      empty('proposal'),
      empty('negotiation'),
      empty('won'),
      empty('lost'),
    ]);

    expect(screen.getByTestId('pipeline-default-total')).toHaveTextContent(formatDealAmount(1000, 'EUR'));
    expect(screen.queryByRole('table')).not.toBeInTheDocument();
  });

  it('shows an empty state when there are no open deals', () => {
    renderChart([
      empty('qualification'),
      empty('proposal'),
      empty('negotiation'),
      { stage: 'won', count: 2, totals: [{ currency: 'EUR', amount_cents: 1000, weighted_cents: 1000 }] },
      empty('lost'),
    ]);

    expect(screen.getByText('No open deals yet')).toBeInTheDocument();
    expect(screen.queryByTestId('pipeline-default-total')).not.toBeInTheDocument();
  });

  it('says the pipeline could not be loaded instead of the empty state when the request failed', () => {
    render(
      <MemoryRouter>
        <PipelineChart stages={undefined} defaultCurrency="EUR" isLoading={false} isError />
      </MemoryRouter>
    );

    expect(screen.getByRole('alert')).toHaveTextContent('Pipeline could not be loaded');
    expect(screen.queryByText('No open deals yet')).not.toBeInTheDocument();
    expect(screen.queryByTestId('pipeline-default-total')).not.toBeInTheDocument();
  });

  it('links to the board', () => {
    renderChart(mixed);

    expect(screen.getByRole('link', { name: 'Open the board' })).toHaveAttribute('href', '/deals/board');
  });
});
