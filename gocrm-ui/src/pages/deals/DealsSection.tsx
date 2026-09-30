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
import type { DealListResult, DealSubResourceParams } from '@/api/endpoints';
import { DealStageChip } from './DealStageChip';
import { formatCalendarDate, formatDealAmount } from './dealFormat';

const ROWS_PER_PAGE_OPTIONS = [5, 10, 25];

export interface DealsSectionProps {
  /** Distinguishes the section's query from the parent record's own. */
  queryKey: readonly unknown[];
  fetchDeals: (params: DealSubResourceParams) => Promise<DealListResult>;
  /** Where "New Deal" goes, with the parent id already in the query string. */
  newDealPath: string;
  /** Admin and sales write deals; the section itself is theirs too. */
  canWrite: boolean;
  headingId: string;
  emptyText: string;
}

/**
 * The "Deals" section of a company or customer page: one small paginated
 * table over the sub-resource endpoint and a prefilled "New Deal" button.
 */
export const DealsSection: React.FC<DealsSectionProps> = ({
  queryKey,
  fetchDeals,
  newDealPath,
  canWrite,
  headingId,
  emptyText,
}) => {
  const navigate = useNavigate();
  const [paging, setPaging] = useState({ page: 0, limit: 5 });

  const { data } = useQuery({
    queryKey: [...queryKey, paging],
    queryFn: () => fetchDeals({ offset: paging.page * paging.limit, limit: paging.limit }),
  });

  return (
    <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby={headingId}>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
        <Typography variant="h6" id={headingId}>
          Deals{data && ` (${data.total})`}
        </Typography>
        {canWrite && (
          <Button variant="outlined" size="small" startIcon={<AddIcon />} onClick={() => navigate(newDealPath)}>
            New Deal
          </Button>
        )}
      </Box>
      {data && data.deals.length > 0 ? (
        <>
          <Table size="small" aria-label="Deals">
            <TableHead>
              <TableRow>
                <TableCell>Title</TableCell>
                <TableCell>Stage</TableCell>
                <TableCell align="right">Amount</TableCell>
                <TableCell>Expected close</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {data.deals.map((deal) => (
                <TableRow key={deal.id} hover>
                  <TableCell>
                    <Link component={RouterLink} to={`/deals/${deal.id}`}>
                      {deal.title}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <DealStageChip stage={deal.stage} />
                  </TableCell>
                  <TableCell align="right">{formatDealAmount(deal.amount_cents, deal.currency)}</TableCell>
                  <TableCell>{formatCalendarDate(deal.expected_close_date)}</TableCell>
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
        <Typography color="text.secondary">{emptyText}</Typography>
      )}
    </Paper>
  );
};
