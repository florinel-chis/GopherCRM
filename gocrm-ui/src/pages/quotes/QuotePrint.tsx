import React from 'react';
import { useParams, useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  Box,
  Button,
  Divider,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { ArrowBack as BackIcon, Print as PrintIcon } from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { quotesApi } from '@/api/endpoints';
import { quoteStatusLabel } from '@/types';
import { formatDate } from '@/utils/date';
import { formatCalendarDate } from '@/pages/deals/dealFormat';
import { formatDiscountPercent, formatMoney, quoteCustomerName } from './quoteFormat';

/**
 * The printable quote: a print-styled route, not a PDF. The application
 * chrome (app bar, navigation, breadcrumbs) and the buttons below are hidden
 * under `@media print` by the layout and by `displayPrint`; the quote itself
 * is what the browser prints when the Print button calls window.print().
 */
export const Component: React.FC = () => {
  const { id } = useParams();
  const navigate = useNavigate();
  const quoteId = Number(id);

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

  const appName = import.meta.env.VITE_APP_NAME || 'GoCRM';
  // The issue date is the day the quote went out, or the day it was drafted.
  const issueDate = quote.sent_at ?? quote.created_at;

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

      <Paper
        component="article"
        aria-labelledby="quote-print-heading"
        sx={{ p: 4, maxWidth: 900, '@media print': { boxShadow: 'none', p: 0, maxWidth: 'none' } }}
      >
        <Box display="flex" justifyContent="space-between" alignItems="flex-start" flexWrap="wrap" gap={2}>
          <Box>
            <Typography variant="h4" component="h1" id="quote-print-heading">
              {appName}
            </Typography>
            <Typography variant="h5" component="h2" data-testid="quote-print-number">
              Quote {quote.number}
            </Typography>
            <Typography variant="subtitle1" data-testid="quote-print-title">
              {quote.title}
            </Typography>
          </Box>
          <Box textAlign="right">
            <Typography variant="body2">
              Date: <span data-testid="quote-print-date">{formatDate(issueDate, 'MMM dd, yyyy')}</span>
            </Typography>
            <Typography variant="body2">
              Valid until: <span data-testid="quote-print-valid-until">{formatCalendarDate(quote.valid_until)}</span>
            </Typography>
            <Typography variant="body2">Status: {quoteStatusLabel(quote.status)}</Typography>
          </Box>
        </Box>

        <Divider sx={{ my: 3 }} />

        <Box mb={3} data-testid="quote-print-customer">
          <Typography variant="overline" color="text.secondary">
            Prepared for
          </Typography>
          {quote.customer ? (
            <>
              <Typography>{quoteCustomerName(quote.customer)}</Typography>
              {quote.customer.company && <Typography>{quote.customer.company}</Typography>}
            </>
          ) : (
            <Typography color="text.secondary">No customer linked</Typography>
          )}
        </Box>

        <Table size="small" aria-label="Quote lines">
          <TableHead>
            <TableRow>
              <TableCell>Description</TableCell>
              <TableCell align="right">Qty</TableCell>
              <TableCell align="right">Unit price</TableCell>
              <TableCell align="right">Discount</TableCell>
              <TableCell align="right">Line total</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {quote.lines.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5}>
                  <Typography color="text.secondary">No lines</Typography>
                </TableCell>
              </TableRow>
            ) : (
              quote.lines.map((line) => (
                <TableRow key={line.id} data-testid="quote-print-line">
                  <TableCell>{line.description}</TableCell>
                  <TableCell align="right">{line.quantity}</TableCell>
                  <TableCell align="right">{formatMoney(line.unit_price_cents, quote.currency)}</TableCell>
                  <TableCell align="right">{formatDiscountPercent(line.discount_bp)}</TableCell>
                  <TableCell align="right">{formatMoney(line.line_total_cents, quote.currency)}</TableCell>
                </TableRow>
              ))
            )}
          </TableBody>
        </Table>

        <Box display="flex" justifyContent="flex-end" mt={2}>
          <Table size="small" sx={{ width: 'auto', minWidth: 280 }} aria-label="Totals">
            <TableBody>
              <TableRow>
                <TableCell>Subtotal</TableCell>
                <TableCell align="right" data-testid="quote-print-subtotal">
                  {formatMoney(quote.subtotal_cents, quote.currency)}
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell>Discount</TableCell>
                <TableCell align="right" data-testid="quote-print-discount">
                  {formatMoney(quote.discount_cents, quote.currency)}
                </TableCell>
              </TableRow>
              <TableRow>
                <TableCell sx={{ fontWeight: 'bold' }}>Total</TableCell>
                <TableCell align="right" sx={{ fontWeight: 'bold' }} data-testid="quote-print-total">
                  {formatMoney(quote.total_cents, quote.currency)}
                </TableCell>
              </TableRow>
            </TableBody>
          </Table>
        </Box>

        {quote.notes && (
          <Box mt={3}>
            <Typography variant="overline" color="text.secondary">
              Notes
            </Typography>
            <Typography variant="body2" sx={{ whiteSpace: 'pre-wrap' }} data-testid="quote-print-notes">
              {quote.notes}
            </Typography>
          </Box>
        )}
      </Paper>
    </Box>
  );
};
