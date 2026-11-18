import React from 'react';
import { useParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Box, Button, Typography } from '@mui/material';
import { ArrowBack as BackIcon, Print as PrintIcon } from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { quotesApi } from '@/api/endpoints';
import { useProductName } from '@/hooks/useProductName';
import { QuoteDocument } from './QuoteDocument';

/**
 * The printable quote: a print-styled route the browser prints when the Print
 * button calls window.print(). The application chrome (app bar, navigation,
 * breadcrumbs) and the buttons below are hidden under `@media print` by the
 * layout and by `displayPrint`. The document itself is QuoteDocument, which
 * the server also prints to PDF through the public render page (QuoteRender)
 * when PDF output is on.
 */
export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const quoteId = Number(id);
  const productName = useProductName();

  const { data: quote, isLoading, isError } = useQuery({
    queryKey: ['quote', quoteId],
    queryFn: () => quotesApi.getQuote(quoteId),
    enabled: !!id,
  });

  if (isError) {
    return (
      <Box>
        <Typography variant="h5" gutterBottom>
          Quote not found
        </Typography>
        <Button variant="outlined" onClick={() => navigate('/quotes')}>
          Back to quotes
        </Button>
      </Box>
    );
  }

  if (isLoading || !quote) {
    return <Loading />;
  }

  return (
    <Box data-testid="quote-print">
      <Box display="flex" gap={1} mb={2} sx={{ displayPrint: 'none' }} data-testid="quote-print-toolbar">
        <Button variant="outlined" startIcon={<BackIcon />} component={RouterLink} to={`/quotes/${quote.id}`}>
          Back to quote
        </Button>
        <Button variant="contained" startIcon={<PrintIcon />} onClick={() => window.print()}>
          Print
        </Button>
      </Box>

      <QuoteDocument quote={quote} productName={productName} />
    </Box>
  );
};
