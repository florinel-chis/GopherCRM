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
// tax_rate_bp is the line's tax rate in basis points (0..10000, 21% is 2100);
// the server treats an absent rate as 0.
export interface QuoteLineData {
  product_id: number;
  quantity: number;
  discount_bp: number;
  tax_rate_bp?: number;
  unit_price_cents?: number;
  description?: string;
}

// PUT /quotes/:id/lines/:lineId is a partial update with the same bounds. An
// absent tax_rate_bp keeps the stored rate; 0 clears it.
export interface UpdateQuoteLineData {
  quantity?: number;
  discount_bp?: number;
  tax_rate_bp?: number;
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

/**
 * The request timeout of the two calls that can wait for a PDF render:
 * `emailQuote` and `downloadQuotePdf`. The server bounds a render at 15 s and
 * an SMTP delivery at 30 s, so the email call can take 45 s; the client's
 * default timeout (30 s) would abort a call the server still completes, and
 * the user would send the quote a second time. 55 s is above that worst case
 * and below the 60 s read timeout a reverse proxy usually has.
 */
export const QUOTE_PDF_REQUEST_TIMEOUT_MS = 55000;

const isRecord = (value: unknown): value is Record<string, unknown> =>
  typeof value === 'object' && value !== null && !Array.isArray(value);

// The envelope's error object, or undefined when the text is not an envelope.
const parseErrorEnvelope = (text: string): Record<string, unknown> | undefined => {
  try {
    const parsed: unknown = JSON.parse(text);
    if (isRecord(parsed) && isRecord(parsed.error)) {
      return parsed.error;
    }
    // Already unwrapped (an interceptor got there first).
    if (isRecord(parsed) && typeof parsed.code === 'string') {
      return parsed;
    }
  } catch {
    // Not JSON: a proxy's error page, say.
  }
  return undefined;
};

// Blob.text() where the runtime has it, a FileReader where it does not.
const readBlobText = (blob: Blob): Promise<string> => {
  if (typeof blob.text === 'function') {
    return blob.text();
  }
  return new Promise<string>((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '');
    reader.onerror = () => reject(reader.error ?? new Error('unreadable response body'));
    reader.readAsText(blob);
  });
};

// Gives an error whose response body is a Blob the flat `{ code, message }`
// body of every other API error. Anything else is returned untouched.
const withBlobErrorBody = async (error: unknown): Promise<unknown> => {
  if (!isRecord(error) || !isRecord(error.response) || !(error.response.data instanceof Blob)) {
    return error;
  }
  let flat: Record<string, unknown> | undefined;
  try {
    flat = parseErrorEnvelope(await readBlobText(error.response.data));
  } catch {
    flat = undefined;
  }
  error.response.data = flat ?? {};
  return error;
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

  // Sends the plaintext rendering to the customer's current address, with the
  // quote's PDF attached when the server has PDF output on (a failed render
  // falls back to the text-only mail and still succeeds). 409 when the quote
  // is not sent or accepted, has no live customer or was revised during the
  // send; 502 MAIL_DELIVERY_FAILED when the mailer failed. The server may
  // take up to 45 s, hence the longer timeout.
  emailQuote: async (id: number): Promise<Quote> => {
    const response = await api.post<unknown>(`/quotes/${id}/email`, undefined, {
      timeout: QUOTE_PDF_REQUEST_TIMEOUT_MS,
    });
    return transformQuoteFromBackend(response.data);
  },

  // The quote as a PDF file, requested with the session's Authorization like
  // every other call. 503 PDF_UNAVAILABLE when the server has PDF output off,
  // 503 PDF_RENDER_FAILED when the render failed or ran out of time (up to
  // 15 s). With responseType 'blob' an error body is a Blob too, which the
  // client's envelope unwrapping does not see: it is read here and the error
  // is rethrown with `response.data` in the flat `{ code, message }` shape
  // every other call produces. A body that cannot be read as the envelope
  // leaves the error without a message.
  downloadQuotePdf: async (id: number): Promise<Blob> => {
    try {
      const response = await api.get<Blob>(`/quotes/${id}/pdf`, {
        responseType: 'blob',
        timeout: QUOTE_PDF_REQUEST_TIMEOUT_MS,
      });
      return response.data;
    } catch (error) {
      throw await withBlobErrorBody(error);
    }
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
