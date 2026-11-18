import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, waitFor } from '@/test/test-utils';
import { QuoteRender, QUOTE_RENDER_UNAVAILABLE_MESSAGE } from './QuoteRender';
import { quoteRenderApi } from '@/api/endpoints/quoteRender';
import { createMockQuote, createMockQuoteLine } from '@/test/factories';
import { formatMoney } from './quoteFormat';

vi.mock('@/api/endpoints/quoteRender', () => ({
  quoteRenderApi: { getQuoteRender: vi.fn() },
}));

const CREDENTIAL = 'render-credential-CANARY-0123456789';

describe('QuoteRender', () => {
  const quote = createMockQuote({
    id: 42,
    number: 'Q-000042',
    title: 'Website redesign quote',
    status: 'sent',
    currency: 'EUR',
    subtotal_cents: 2500,
    discount_cents: 250,
    total_cents: 2250,
    valid_until: '2099-12-31',
    notes: 'Payment within 30 days',
    sent_at: '2026-10-27T10:00:00Z',
    customer: { id: 3, first_name: 'Jane', last_name: 'Smith', company: 'Acme Widgets' },
    lines: [
      createMockQuoteLine({ id: 7, description: 'Widget', quantity: 2, unit_price_cents: 1250, discount_bp: 1000, line_total_cents: 2250 }),
    ],
  });

  beforeEach(() => {
    vi.clearAllMocks();
    window.location.hash = `#${CREDENTIAL}`;
  });

  afterEach(() => {
    window.location.hash = '';
    vi.unstubAllEnvs();
  });

  it('loads the quote with the credential of the URL fragment and shows the document', async () => {
    vi.mocked(quoteRenderApi.getQuoteRender).mockResolvedValue({ quote, product_name: 'Acme CRM' });

    render(<QuoteRender />);

    expect(await screen.findByTestId('quote-print-number')).toHaveTextContent('Quote Q-000042');
    expect(quoteRenderApi.getQuoteRender).toHaveBeenCalledTimes(1);
    expect(quoteRenderApi.getQuoteRender).toHaveBeenCalledWith(CREDENTIAL);
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Acme CRM');
    expect(screen.getByTestId('quote-print-title')).toHaveTextContent('Website redesign quote');
    expect(screen.getByTestId('quote-print-date')).toHaveTextContent('Oct 27, 2026');
    expect(screen.getByTestId('quote-print-valid-until')).toHaveTextContent('Dec 31, 2099');
    expect(screen.getByTestId('quote-print-customer')).toHaveTextContent('Jane Smith');
    expect(screen.getAllByTestId('quote-print-line')).toHaveLength(1);
    expect(screen.getByTestId('quote-print-line-total')).toHaveTextContent(formatMoney(2250, 'EUR'));
    expect(screen.getByTestId('quote-print-subtotal')).toHaveTextContent(formatMoney(2500, 'EUR'));
    expect(screen.getByTestId('quote-print-notes')).toHaveTextContent('Payment within 30 days');
  });

  it('shows no toolbar, no button, no link and never the credential', async () => {
    vi.mocked(quoteRenderApi.getQuoteRender).mockResolvedValue({ quote, product_name: 'Acme CRM' });

    const { container } = render(<QuoteRender />);

    await screen.findByTestId('quote-print-number');
    expect(screen.queryByTestId('quote-print-toolbar')).not.toBeInTheDocument();
    expect(screen.queryByRole('button')).not.toBeInTheDocument();
    expect(screen.queryByRole('link')).not.toBeInTheDocument();
    expect(screen.queryByText('Print')).not.toBeInTheDocument();
    expect(screen.queryByText('Back to quote')).not.toBeInTheDocument();
    expect(container.innerHTML).not.toContain(CREDENTIAL);
  });

  it('falls back to the build-time product name when none is stored', async () => {
    vi.stubEnv('VITE_APP_NAME', 'Built-in CRM');
    vi.mocked(quoteRenderApi.getQuoteRender).mockResolvedValue({ quote, product_name: '   ' });

    render(<QuoteRender />);

    await screen.findByTestId('quote-print-number');
    expect(screen.getByRole('heading', { level: 1 })).toHaveTextContent('Built-in CRM');
  });

  it('says the quote is not available when the credential is refused', async () => {
    vi.mocked(quoteRenderApi.getQuoteRender).mockRejectedValue({
      response: { status: 400, data: { code: 'BAD_REQUEST', message: 'Invalid or expired render link' } },
    });

    const { container } = render(<QuoteRender />);

    expect(await screen.findByText(QUOTE_RENDER_UNAVAILABLE_MESSAGE)).toBeInTheDocument();
    expect(screen.queryByTestId('quote-print-number')).not.toBeInTheDocument();
    expect(container.innerHTML).not.toContain(CREDENTIAL);
    expect(container.innerHTML).not.toContain('Invalid or expired render link');
  });

  it('says the quote is not available without a credential and asks nothing', async () => {
    window.location.hash = '';

    render(<QuoteRender />);

    expect(screen.getByText(QUOTE_RENDER_UNAVAILABLE_MESSAGE)).toBeInTheDocument();
    await waitFor(() => expect(quoteRenderApi.getQuoteRender).not.toHaveBeenCalled());
  });
});
