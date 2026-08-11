import { describe, it, expect, vi, beforeEach } from 'vitest';
import { render, screen, waitFor, fireEvent, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { Component as AEOCitations } from './AEOCitations';
import type { AEOCitationsReport } from '@/api/endpoints/aeo';

// recharts' ResponsiveContainer needs ResizeObserver, which jsdom lacks.
global.ResizeObserver = vi.fn().mockImplementation(() => ({
  observe: vi.fn(),
  unobserve: vi.fn(),
  disconnect: vi.fn(),
}));

const mockGetCitations = vi.fn();

vi.mock('@/api/endpoints/aeo', () => ({
  aeoApi: {
    getCitations: (days?: number) => mockGetCitations(days),
  },
}));

const reportFixture: AEOCitationsReport = {
  from: '2026-07-12',
  to: '2026-08-11',
  total_answers: 24,
  total_citations: 31,
  answers_with_citations: 12,
  owned_citation_rate: 33.3,
  by_company: [
    {
      company: 'Acme',
      is_brand: true,
      citations: 8,
      citation_rate: 25,
      with_brand_mention: 6,
      brand_mention_rate: 75,
    },
    {
      company: 'Globex',
      is_brand: false,
      citations: 5,
      citation_rate: 12.5,
      with_brand_mention: 1,
      brand_mention_rate: 20,
    },
  ],
  top_domains: [
    {
      domain: 'acme.com',
      company: 'Acme',
      is_owned: true,
      citations: 8,
      citation_rate: 25,
      with_brand_mention: 6,
      brand_mention_rate: 75,
    },
    {
      domain: 'g2.com',
      company: '',
      is_owned: false,
      citations: 4,
      citation_rate: 16.7,
      with_brand_mention: 2,
      brand_mention_rate: 50,
    },
  ],
};

const emptyReport: AEOCitationsReport = {
  from: '2026-07-12',
  to: '2026-08-11',
  total_answers: 0,
  total_citations: 0,
  answers_with_citations: 0,
  owned_citation_rate: 0,
  by_company: [],
  top_domains: [],
};

const renderPage = () => {
  const queryClient = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });

  return render(
    <QueryClientProvider client={queryClient}>
      <AEOCitations />
    </QueryClientProvider>
  );
};

describe('AEOCitations', () => {
  beforeEach(() => {
    mockGetCitations.mockReset();
    mockGetCitations.mockResolvedValue(reportFixture);
  });

  it('shows a spinner while the report loads', () => {
    mockGetCitations.mockImplementation(() => new Promise(() => {}));

    renderPage();

    expect(screen.getByRole('progressbar')).toBeInTheDocument();
    expect(screen.getByText('AEO Citations')).toBeInTheDocument();
  });

  it('renders an error alert when the request fails', async () => {
    mockGetCitations.mockRejectedValue(new Error('boom'));

    renderPage();

    expect(
      await screen.findByText(/Failed to load the AEO citations report/)
    ).toBeInTheDocument();
  });

  it('renders the empty state when no answers exist in the range', async () => {
    mockGetCitations.mockResolvedValue(emptyReport);

    renderPage();

    expect(await screen.findByText('No AEO data yet')).toBeInTheDocument();
    expect(screen.queryByText('Top cited domains')).not.toBeInTheDocument();
  });

  it('renders the headline rates and both comparison charts', async () => {
    renderPage();

    expect(await screen.findByText('Owned-domain citation rate')).toBeInTheDocument();
    expect(screen.getByText('33.3%')).toBeInTheDocument();
    expect(screen.getByText('Total citations')).toBeInTheDocument();
    expect(screen.getByText('31')).toBeInTheDocument();
    expect(screen.getByText('50.0% of 24 answers')).toBeInTheDocument();

    expect(screen.getByTestId('aeo-citation-rate-chart')).toBeInTheDocument();
    expect(screen.getByTestId('aeo-brand-mention-rate-chart')).toBeInTheDocument();
    expect(screen.getByText('Citation rate by company')).toBeInTheDocument();
    expect(screen.getByText('Citations with brand mention')).toBeInTheDocument();
  });

  it('renders the top-domains table with the owned marker', async () => {
    renderPage();

    expect(await screen.findByText('Top cited domains')).toBeInTheDocument();

    const ownedRow = screen.getByText('acme.com').closest('tr');
    expect(ownedRow).not.toBeNull();
    expect(within(ownedRow as HTMLElement).getByText('Owned')).toBeInTheDocument();
    expect(ownedRow).toHaveTextContent('75.0%');

    const otherRow = screen.getByText('g2.com').closest('tr');
    expect(otherRow).toHaveTextContent('16.7%');
    // An unattributed domain renders an em dash in the company column.
    expect(otherRow).toHaveTextContent('—');
  });

  it('notes when the range produced no citations at all', async () => {
    mockGetCitations.mockResolvedValue({
      ...reportFixture,
      total_citations: 0,
      answers_with_citations: 0,
      owned_citation_rate: 0,
      top_domains: [],
    } satisfies AEOCitationsReport);

    renderPage();

    expect(
      await screen.findByText('No citations were extracted in this range.')
    ).toBeInTheDocument();
    expect(screen.getByText('No cited domains in this range.')).toBeInTheDocument();
  });

  it('defaults to 30 days and refetches when another range is picked', async () => {
    renderPage();

    await waitFor(() => expect(mockGetCitations).toHaveBeenCalledWith(30));

    fireEvent.click(screen.getByRole('button', { name: 'Last 7 days' }));

    await waitFor(() => expect(mockGetCitations).toHaveBeenCalledWith(7));
  });
});
