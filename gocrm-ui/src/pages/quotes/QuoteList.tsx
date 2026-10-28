import React, { useState, useCallback, useMemo } from 'react';
import { useNavigate } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import { Box, Button, FormControl, InputLabel, MenuItem, Paper, Select, Typography } from '@mui/material';
import { Add as AddIcon } from '@mui/icons-material';
import { DataTable, type Column } from '@/components/DataTable';
import { Loading } from '@/components/Loading';
import { quotesApi, type QuoteFilters, type QuoteSortColumn } from '@/api/endpoints';
import { QUOTE_STATUSES, type Quote, type QuoteStatus } from '@/types';
import { formatCalendarDate } from '@/pages/deals/dealFormat';
import { QuoteStatusChip } from './QuoteStatusChip';
import { QuoteCreateDialog } from './QuoteCreateDialog';
import { formatMoney, quoteCustomerName } from './quoteFormat';

// Columns the API accepts in sort_by (its allowlist); the others render
// without a sort header so a click can never produce a rejected request.
const SORTABLE_COLUMNS = new Set<string>(['id', 'title', 'status', 'total_cents', 'valid_until', 'created_at']);

interface ListState {
  page: number;
  limit: number;
  status: QuoteStatus | '';
  sort_by?: QuoteSortColumn;
  sort_order?: 'asc' | 'desc';
}

export const Component: React.FC = () => {
  const navigate = useNavigate();

  // Quotes are admin and sales only on the API; the route guard keeps the
  // other roles out and both roles may start a quote.
  const [state, setState] = useState<ListState>({ page: 1, limit: 10, status: '' });
  const [createOpen, setCreateOpen] = useState(false);

  const filters: QuoteFilters = useMemo(
    () => ({
      offset: (state.page - 1) * state.limit,
      limit: state.limit,
      status: state.status || undefined,
      sort_by: state.sort_by,
      sort_order: state.sort_order,
    }),
    [state]
  );

  const { data, isLoading } = useQuery({
    queryKey: ['quotes', filters],
    queryFn: () => quotesApi.getQuotes(filters),
  });

  const columns: Column<Quote>[] = useMemo(
    () => [
      { id: 'id', label: 'Number', minWidth: 110, format: (_value: unknown, row: Quote) => row.number },
      { id: 'title', label: 'Title', minWidth: 200 },
      {
        id: 'status',
        label: 'Status',
        minWidth: 110,
        format: (value: QuoteStatus) => <QuoteStatusChip status={value} />,
      },
      {
        id: 'customer',
        label: 'Customer',
        minWidth: 160,
        sortable: false,
        format: (_value: unknown, row: Quote) => {
          if (!row.customer) {
            return '—';
          }
          const name = quoteCustomerName(row.customer);
          return row.customer.company ? `${name} (${row.customer.company})` : name;
        },
      },
      {
        id: 'deal',
        label: 'Deal',
        minWidth: 160,
        sortable: false,
        format: (_value: unknown, row: Quote) => row.deal?.title ?? '—',
      },
      {
        id: 'total_cents',
        label: 'Total',
        minWidth: 120,
        align: 'right',
        format: (value: number, row: Quote) => formatMoney(value, row.currency),
      },
      {
        id: 'valid_until',
        label: 'Valid until',
        minWidth: 130,
        format: (value: string | null) => formatCalendarDate(value),
      },
    ],
    []
  );

  const handleSort = useCallback((field: string, order: 'asc' | 'desc') => {
    if (!SORTABLE_COLUMNS.has(field)) {
      return;
    }
    setState((prev) => ({ ...prev, sort_by: field as QuoteSortColumn, sort_order: order, page: 1 }));
  }, []);

  const handleStatusFilter = useCallback((value: string) => {
    setState((prev) => ({ ...prev, status: value as QuoteStatus | '', page: 1 }));
  }, []);

  const handlePageChange = useCallback((page: number) => {
    setState((prev) => ({ ...prev, page: page + 1 }));
  }, []);

  const handleRowsPerPageChange = useCallback((rowsPerPage: number) => {
    setState((prev) => ({ ...prev, limit: rowsPerPage, page: 1 }));
  }, []);

  if (isLoading && !data) {
    return <Loading />;
  }

  return (
    <Box>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={3}>
        <Typography variant="h4">Quotes</Typography>
        <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
          New Quote
        </Button>
      </Box>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} alignItems="center" flexWrap="wrap">
          <FormControl size="small" sx={{ minWidth: 170 }}>
            <InputLabel id="quote-status-filter-label">Status</InputLabel>
            <Select
              labelId="quote-status-filter-label"
              id="quote-status-filter"
              value={state.status}
              onChange={(e) => handleStatusFilter(e.target.value)}
              label="Status"
              displayEmpty
              renderValue={(value: QuoteStatus | '') =>
                value === '' ? 'All statuses' : QUOTE_STATUSES.find((s) => s.value === value)?.label
              }
            >
              <MenuItem value="">All statuses</MenuItem>
              {QUOTE_STATUSES.map((status) => (
                <MenuItem key={status.value} value={status.value}>
                  {status.label}
                </MenuItem>
              ))}
            </Select>
          </FormControl>
        </Box>
      </Paper>

      <DataTable
        columns={columns}
        data={data?.quotes || []}
        totalCount={data?.total || 0}
        page={state.page - 1}
        rowsPerPage={state.limit}
        loading={isLoading}
        onSort={handleSort}
        sortBy={state.sort_by ?? ''}
        sortOrder={state.sort_order ?? 'asc'}
        onPageChange={handlePageChange}
        onRowsPerPageChange={handleRowsPerPageChange}
        onRowClick={(quote) => navigate(`/quotes/${quote.id}`)}
      />

      <QuoteCreateDialog
        open={createOpen}
        onClose={() => setCreateOpen(false)}
        onCreated={(quote) => {
          setCreateOpen(false);
          navigate(`/quotes/${quote.id}`);
        }}
      />
    </Box>
  );
};
