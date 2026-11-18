import { publicApi } from '../client';

// The public quote response link (#382): the client opens the link of the
// quote email without a session. Both calls go through `publicApi`, which
// never sends the stored session's Authorization header.

export type QuoteResponseDecision = 'accept' | 'decline';

export interface QuotePublicLine {
  description: string;
  quantity: number;
  unit_price_cents: number;
  discount_bp: number;
  line_total_cents: number;
  tax_rate_bp: number;
  tax_cents: number;
}

// Everything the public routes disclose about a quote: no id, title, notes,
// owner or customer.
export interface QuotePublicView {
  number: string;
  status: string;
  currency: string;
  /** YYYY-MM-DD; null when the quote has no validity date. */
  valid_until: string | null;
  lines: QuotePublicLine[];
  subtotal_cents: number;
  discount_cents: number;
  /** Net total after discounts, before tax. */
  total_cents: number;
  tax_cents: number;
  gross_total_cents: number;
}

type BackendQuotePublicView = Omit<QuotePublicView, 'valid_until' | 'lines'> & {
  valid_until?: string | null;
  lines?: QuotePublicLine[] | null;
};

// The API omits valid_until when it is not set.
const normalize = (view: BackendQuotePublicView): QuotePublicView => ({
  ...view,
  valid_until: view.valid_until ?? null,
  lines: Array.isArray(view.lines) ? view.lines : [],
});

const RESPONSE_PATH = '/quotes/public/response';

export const quoteResponseApi = {
  /** Shows the quote behind the link. Never spends the token. */
  getQuoteResponse: async (token: string): Promise<QuotePublicView> => {
    const response = await publicApi.get<BackendQuotePublicView>(RESPONSE_PATH, { params: { token } });
    return normalize(response.data);
  },

  /** Records the client's decision. The link works once. */
  respond: async (token: string, decision: QuoteResponseDecision): Promise<QuotePublicView> => {
    const response = await publicApi.post<BackendQuotePublicView>(RESPONSE_PATH, { token, decision });
    return normalize(response.data);
  },
};
