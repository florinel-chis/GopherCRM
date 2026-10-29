import { api } from '../client';
import type { ApiResponseWithMeta } from '../client';
import type { Quote, QuoteLine, QuoteStatus, QuoteStatusChange } from '@/types';

// GET /quotes takes offset/limit, server-side filters and a sort column from
// the API allowlist. Sales is always narrowed to its own quotes by the API,
// whatever owner_id says.
export type QuoteSortColumn =
  | 'id'
  | 'title'
  | 'status'
  | 'total_cents'
  | 'currency'
  | 'valid_until'
  | 'sent_at'
  | 'created_at'
  | 'updated_at';

export interface QuoteFilters {
  offset?: number;
  limit?: number;
  status?: QuoteStatus;
  deal_id?: number;
  customer_id?: number;
  owner_id?: number;
  sort_by?: QuoteSortColumn;
  sort_order?: 'asc' | 'desc';
}

export interface QuoteListResult {
  quotes: Quote[];
  total: number;
}

export interface QuoteSubResourceParams {
  offset?: number;
  limit?: number;
  sort_by?: QuoteSortColumn;
  sort_order?: 'asc' | 'desc';
}

// The body of POST /quotes. At least one of deal_id and customer_id is
// required; with only deal_id the customer is taken from the deal. An omitted
// currency takes deals.default_currency.
export interface CreateQuoteData {
  title: string;
  deal_id?: number;
  customer_id?: number;
  currency?: string;
  valid_until?: string;
  notes?: string;
  owner_id?: number;
}

// PUT /quotes/:id is a partial update of a draft: every absent field keeps its
// stored value; valid_until null clears the date.
export interface UpdateQuoteData {
  title?: string;
  valid_until?: string | null;
  notes?: string;
  customer_id?: number;
  currency?: string;
  owner_id?: number;
}

// The body of POST /quotes/:id/lines. The discount is in basis points (12.5%
// is 1250); description and unit_price_cents default to the product's.
export interface QuoteLineData {
  product_id: number;
  quantity: number;
  discount_bp: number;
  unit_price_cents?: number;
  description?: string;
}

// PUT /quotes/:id/lines/:lineId is a partial update with the same bounds.
export interface UpdateQuoteLineData {
  quantity?: number;
  discount_bp?: number;
  unit_price_cents?: number;
  description?: string;
  position?: number;
}

// The optional columns may be absent on the wire (omitempty), and the list
// endpoints carry no lines.
type RawQuote = Omit<
  Quote,
  'valid_until' | 'notes' | 'sent_at' | 'closed_at' | 'last_emailed_at' | 'lines'
> & {
  valid_until?: string | null;
  notes?: string;
  sent_at?: string | null;
  closed_at?: string | null;
  last_emailed_at?: string | null;
  lines?: QuoteLine[] | null;
};

export const transformQuoteFromBackend = (input: unknown): Quote => {
  const raw = input as RawQuote;
  return {
    ...raw,
    valid_until: raw.valid_until ?? null,
    notes: raw.notes ?? '',
    sent_at: raw.sent_at ?? null,
    closed_at: raw.closed_at ?? null,
    last_emailed_at: raw.last_emailed_at ?? null,
    lines: Array.isArray(raw.lines) ? raw.lines : [],
  };
};

const toListResult = (response: ApiResponseWithMeta<unknown[]>): QuoteListResult => {
  const rows = Array.isArray(response.data) ? response.data : [];
  const quotes = rows.map(transformQuoteFromBackend);
  return { quotes, total: response.meta?.total ?? quotes.length };
};

export const quotesApi = {
  // The array is the envelope payload; the pagination totals only exist in
  // `meta`, which the client interceptor re-attaches to the response.
  getQuotes: async (filters?: QuoteFilters): Promise<QuoteListResult> => {
    const response = (await api.get<unknown[]>('/quotes', {
      params: filters,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },

  // With its lines by position, its totals and the owner, customer and deal
  // summaries. 403 for sales on another owner's quote.
  getQuote: async (id: number): Promise<Quote> => {
    const response = await api.get<unknown>(`/quotes/${id}`);
    return transformQuoteFromBackend(response.data);
  },

  // 400 VALIDATION_ERROR (no link, or a customer that is not the deal's),
  // 400 INVALID_REFERENCE (unknown deal, customer or owner), 409 on a closed
  // deal; callers surface the message.
  createQuote: async (data: CreateQuoteData): Promise<Quote> => {
    const response = await api.post<unknown>('/quotes', data);
    return transformQuoteFromBackend(response.data);
  },

  // 409 CONFLICT when the quote is not a draft.
  updateQuote: async (id: number, data: UpdateQuoteData): Promise<Quote> => {
    const response = await api.put<unknown>(`/quotes/${id}`, data);
    return transformQuoteFromBackend(response.data);
  },

  // Admin: any quote; sales: its own drafts only. Lines and history stay.
  deleteQuote: async (id: number): Promise<void> => {
    await api.delete(`/quotes/${id}`);
  },

  // Each line write returns the whole quote with its recomputed totals.
  addLine: async (id: number, data: QuoteLineData): Promise<Quote> => {
    const response = await api.post<unknown>(`/quotes/${id}/lines`, data);
    return transformQuoteFromBackend(response.data);
  },

  updateLine: async (id: number, lineId: number, data: UpdateQuoteLineData): Promise<Quote> => {
    const response = await api.put<unknown>(`/quotes/${id}/lines/${lineId}`, data);
    return transformQuoteFromBackend(response.data);
  },

  deleteLine: async (id: number, lineId: number): Promise<Quote> => {
    const response = await api.delete<unknown>(`/quotes/${id}/lines/${lineId}`);
    return transformQuoteFromBackend(response.data);
  },

  // 400 INVALID_STATUS_TRANSITION for a disallowed pair or a lost race, 409
  // CONFLICT for draft→sent without lines or accepted after valid_until.
  changeStatus: async (id: number, status: QuoteStatus): Promise<Quote> => {
    const response = await api.post<unknown>(`/quotes/${id}/status`, { status });
    return transformQuoteFromBackend(response.data);
  },

  // Oldest first; the first row has from_status null.
  getHistory: async (id: number): Promise<QuoteStatusChange[]> => {
    const response = await api.get<QuoteStatusChange[]>(`/quotes/${id}/history`);
    return Array.isArray(response.data) ? response.data : [];
  },

  // Sends the plaintext rendering to the customer's current address. 409 when
  // the quote is not sent or accepted, has no live customer or was revised
  // during the send; 502 MAIL_DELIVERY_FAILED when the mailer failed.
  emailQuote: async (id: number): Promise<Quote> => {
    const response = await api.post<unknown>(`/quotes/${id}/email`);
    return transformQuoteFromBackend(response.data);
  },

  getDealQuotes: async (dealId: number, params?: QuoteSubResourceParams): Promise<QuoteListResult> => {
    const response = (await api.get<unknown[]>(`/deals/${dealId}/quotes`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },

  getCustomerQuotes: async (
    customerId: number,
    params?: QuoteSubResourceParams
  ): Promise<QuoteListResult> => {
    const response = (await api.get<unknown[]>(`/customers/${customerId}/quotes`, {
      params,
    })) as ApiResponseWithMeta<unknown[]>;
    return toListResult(response);
  },
};
