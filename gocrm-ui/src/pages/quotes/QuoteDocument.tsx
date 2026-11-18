import React from 'react';
import {
  Box,
  Divider,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { quoteStatusLabel, type Quote } from '@/types';
import { formatDate } from '@/utils/date';
import { formatCalendarDate } from '@/pages/deals/dealFormat';
import { formatDiscountPercent, formatMoney, formatQuantity, quoteCustomerName } from './quoteFormat';

interface QuoteDocumentProps {
  quote: Quote;
  /** The name that heads the document. */
  productName: string;
}

/**
 * The quote as a document: header, customer, lines, totals and notes. It is
 * what the print page shows below its toolbar and what the render page shows
 * alone, so the browser's print and the server's PDF are the same document.
 * Under `@media print` the paper loses its shadow, padding and width limit.
 */
export const QuoteDocument: React.FC<QuoteDocumentProps> = ({ quote, productName }) => {
  // The issue date is the day the quote went out, or the day it was drafted.
  const issueDate = quote.sent_at ?? quote.created_at;

  return (
    <Paper
      component="article"
      aria-labelledby="quote-print-heading"
      sx={{ p: 4, maxWidth: 900, '@media print': { boxShadow: 'none', p: 0, maxWidth: 'none' } }}
    >
      <Box display="flex" justifyContent="space-between" alignItems="flex-start" flexWrap="wrap" gap={2}>
        <Box>
          <Typography variant="h4" component="h1" id="quote-print-heading">
            {productName}
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
            <TableCell align="right">Tax rate</TableCell>
            <TableCell align="right">Tax</TableCell>
            <TableCell align="right">Line total</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {quote.lines.length === 0 ? (
            <TableRow>
              <TableCell colSpan={7} data-testid="quote-print-empty">
                <Typography color="text.secondary">No lines</Typography>
              </TableCell>
            </TableRow>
          ) : (
            quote.lines.map((line) => (
              <TableRow key={line.id} data-testid="quote-print-line">
                <TableCell>{line.description}</TableCell>
                <TableCell align="right" data-testid="quote-print-line-quantity">
                  {formatQuantity(line.quantity)}
                </TableCell>
                <TableCell align="right">{formatMoney(line.unit_price_cents, quote.currency)}</TableCell>
                <TableCell align="right">{formatDiscountPercent(line.discount_bp)}</TableCell>
                <TableCell align="right" data-testid="quote-print-line-tax-rate">
                  {formatDiscountPercent(line.tax_rate_bp)}
                </TableCell>
                <TableCell align="right" data-testid="quote-print-line-tax">
                  {formatMoney(line.tax_cents, quote.currency)}
                </TableCell>
                <TableCell align="right" data-testid="quote-print-line-total">
                  {formatMoney(line.line_total_cents, quote.currency)}
                </TableCell>
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
              <TableCell>Net total</TableCell>
              <TableCell align="right" data-testid="quote-print-total">
                {formatMoney(quote.total_cents, quote.currency)}
              </TableCell>
            </TableRow>
            <TableRow>
              <TableCell>Tax</TableCell>
              <TableCell align="right" data-testid="quote-print-tax">
                {formatMoney(quote.tax_cents, quote.currency)}
              </TableCell>
            </TableRow>
            <TableRow>
              <TableCell sx={{ fontWeight: 'bold' }}>Gross total</TableCell>
              <TableCell align="right" sx={{ fontWeight: 'bold' }} data-testid="quote-print-gross">
                {formatMoney(quote.gross_total_cents, quote.currency)}
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
  );
};
