import React, { useState } from 'react';
import { useNavigate, Link as RouterLink } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  Box,
  Button,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TablePagination,
  TableRow,
  Typography,
} from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { quotesApi } from '@/api/endpoints';
import { isClosedDealStage, type Deal } from '@/types';
import { QuoteStatusChip } from './QuoteStatusChip';
import { QuoteCreateDialog } from './QuoteCreateDialog';
import { formatMoney } from './quoteFormat';

const ROWS_PER_PAGE_OPTIONS = [5, 10, 25];

export interface DealQuotesSectionProps {
  deal: Deal;
  /** Admin and sales start quotes; the section itself is theirs too. */
  canWrite: boolean;
}

/**
 * The "Quotes" section of a deal page: one small paginated table over
 * GET /deals/:id/quotes and a "New quote" button that opens the create
 * dialog prefilled with the deal. The button is hidden on a won or lost deal,
 * which the API refuses with a 409.
 */
export const DealQuotesSection: React.FC<DealQuotesSectionProps> = ({ deal, canWrite }) => {
  const navigate = useNavigate();
  const [paging, setPaging] = useState({ page: 0, limit: 5 });
  const [createOpen, setCreateOpen] = useState(false);

  const { data } = useQuery({
    queryKey: ['deal', deal.id, 'quotes', paging],
    queryFn: () => quotesApi.getDealQuotes(deal.id, { offset: paging.page * paging.limit, limit: paging.limit }),
  });

  const canStart = canWrite && !isClosedDealStage(deal.stage);

  return (
    <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby="deal-quotes-heading">
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
        <Typography variant="h6" id="deal-quotes-heading">
          Quotes{data && ` (${data.total})`}
        </Typography>
        {canStart && (
          <Button variant="outlined" size="small" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
            New quote
          </Button>
        )}
      </Box>
      {data && data.quotes.length > 0 ? (
        <>
          <Table size="small" aria-label="Quotes">
            <TableHead>
              <TableRow>
                <TableCell>Number</TableCell>
                <TableCell>Title</TableCell>
                <TableCell>Status</TableCell>
                <TableCell align="right">Total</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {data.quotes.map((quote) => (
                <TableRow key={quote.id} hover data-testid="deal-quote-row">
                  <TableCell>
                    <Link component={RouterLink} to={`/quotes/${quote.id}`}>
                      {quote.number}
                    </Link>
                  </TableCell>
                  <TableCell>{quote.title}</TableCell>
                  <TableCell>
                    <QuoteStatusChip status={quote.status} />
                  </TableCell>
                  <TableCell align="right">{formatMoney(quote.total_cents, quote.currency)}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
          <TablePagination
            component="div"
            count={data.total}
            page={paging.page}
            rowsPerPage={paging.limit}
            rowsPerPageOptions={ROWS_PER_PAGE_OPTIONS}
            onPageChange={(_, page) => setPaging((prev) => ({ ...prev, page }))}
            onRowsPerPageChange={(event) => setPaging({ page: 0, limit: parseInt(event.target.value, 10) })}
          />
        </>
      ) : (
        <Typography color="text.secondary">No quotes for this deal</Typography>
      )}

      {canStart && (
        <QuoteCreateDialog
          open={createOpen}
          initialDeal={deal}
          onClose={() => setCreateOpen(false)}
          onCreated={(quote) => {
            setCreateOpen(false);
            navigate(`/quotes/${quote.id}`);
          }}
        />
      )}
    </Paper>
  );
};
