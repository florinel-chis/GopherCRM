import React, { useEffect, useRef, useState } from 'react';
import { Box, Typography } from '@mui/material';
import { quoteRenderApi, type QuoteRenderView } from '@/api/endpoints/quoteRender';
import { buildTimeProductName } from '@/hooks/useProductName';
import { QuoteDocument } from './QuoteDocument';

export const QUOTE_RENDER_UNAVAILABLE_MESSAGE = 'Quote not available';

type RenderState = { kind: 'loading' } | { kind: 'unavailable' } | { kind: 'ready'; view: QuoteRenderView };

/** The render credential: the URL fragment without its '#'. */
const credentialFromHash = (): string => window.location.hash.replace(/^#/, '');

/**
 * The page the server prints to PDF. Its headless browser opens
 * /quote-render#<credential>; the page loads the quote with that credential
 * and shows the same document as the print page, and nothing else: no
 * toolbar, no application chrome, no session. The credential is in the
 * fragment, so it is never sent to the server that serves this page, and it
 * is neither shown nor logged here. For anybody else the page says the quote
 * is not available: a credential works only while its render runs.
 */
export const QuoteRender: React.FC = () => {
  const [state, setState] = useState<RenderState>(() =>
    credentialFromHash() ? { kind: 'loading' } : { kind: 'unavailable' }
  );
  // One request per mount: the ref keeps a development double-mount from
  // sending it twice.
  const requested = useRef(false);

  useEffect(() => {
    const credential = credentialFromHash();
    if (!credential || requested.current) {
      return;
    }
    requested.current = true;
    quoteRenderApi
      .getQuoteRender(credential)
      .then((view) => setState({ kind: 'ready', view }))
      .catch(() => setState({ kind: 'unavailable' }));
  }, []);

  if (state.kind === 'ready') {
    const { quote, product_name: stored } = state.view;
    const productName = stored.trim() !== '' ? stored : buildTimeProductName();
    return (
      <Box component="main" data-testid="quote-render" sx={{ p: 3, '@media print': { p: 0 } }}>
        <QuoteDocument quote={quote} productName={productName} />
      </Box>
    );
  }

  return (
    <Box component="main" sx={{ p: 3 }}>
      {state.kind === 'unavailable' && (
        <Typography data-testid="quote-render-unavailable">{QUOTE_RENDER_UNAVAILABLE_MESSAGE}</Typography>
      )}
    </Box>
  );
};

export const Component = QuoteRender;
