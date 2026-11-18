import { publicApi } from '../client';
import type { Quote } from '@/types';
import { transformQuoteFromBackend } from './quotes';

// The read of the PDF render page (/quote-render): the server's headless
// browser opens that page with a render credential in the URL fragment, and
// the page loads the quote with it here. The call goes through `publicApi`,
// which never sends a stored session's Authorization header; the credential
// travels in the body of a POST, never in a URL. It writes nothing.

export interface QuoteRenderView {
  /** The quote exactly as GET /quotes/:id returns it. */
  quote: Quote;
  /** The stored product name; empty when none is stored. */
  product_name: string;
}

interface BackendQuoteRenderView {
  quote?: unknown;
  product_name?: string | null;
}

export const quoteRenderApi = {
  /** Loads the quote a render credential belongs to. 400 once the render is over. */
  getQuoteRender: async (token: string): Promise<QuoteRenderView> => {
    const response = await publicApi.post<BackendQuoteRenderView>('/quotes/public/render', { token });
    return {
      quote: transformQuoteFromBackend(response.data.quote),
      product_name: typeof response.data.product_name === 'string' ? response.data.product_name : '',
    };
  },
};
