import React, { useEffect, useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Chip,
  FormControl,
  InputLabel,
  MenuItem,
  Paper,
  Select,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  TextField,
  Typography,
} from '@mui/material';
import { Loading } from '@/components/Loading';
import {
  signInAuditApi,
  SIGN_IN_AUDIT_METHODS,
  SIGN_IN_AUDIT_OUTCOMES,
  SIGN_IN_AUDIT_REASONS,
  type SignInAuditListParams,
  type SignInAuditMethod,
  type SignInAuditOutcome,
  type SignInAuditReason,
} from '@/api/endpoints';
import { formatDate, localDateTimeToUTC } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';

interface Filters {
  method: SignInAuditMethod | '';
  outcome: SignInAuditOutcome | '';
  reason: SignInAuditReason | '';
  userId: string;
  from: string;
  to: string;
}

const EMPTY_FILTERS: Filters = {
  method: '',
  outcome: '',
  reason: '',
  userId: '',
  from: '',
  to: '',
};

// A user id is sent only when it is a positive integer.
const userIdParam = (value: string): number | undefined =>
  /^[1-9]\d*$/.test(value) ? Number(value) : undefined;

/**
 * The sign-in audit log (#455): one row per password login, single sign-on
 * callback, refresh or failed API-key authentication, in the order the API
 * returns (newest first). Only the stored, pseudonymous columns are shown.
 */
export const SignInAudit: React.FC = () => {
  const [filters, setFilters] = useState<Filters>(EMPTY_FILTERS);
  const [page, setPage] = useState(0);
  const [rowsPerPage, setRowsPerPage] = useState(20);

  // A changed filter always starts again from the first page.
  const changeFilters = (patch: Partial<Filters>) => {
    setFilters((prev) => ({ ...prev, ...patch }));
    setPage(0);
  };

  const params: SignInAuditListParams = {
    method: filters.method === '' ? undefined : filters.method,
    outcome: filters.outcome === '' ? undefined : filters.outcome,
    reason: filters.reason === '' ? undefined : filters.reason,
    user_id: userIdParam(filters.userId),
    from: localDateTimeToUTC(filters.from),
    to: localDateTimeToUTC(filters.to),
    offset: page * rowsPerPage,
    limit: rowsPerPage,
  };

  const { data, isLoading, isError, error } = useQuery({
    queryKey: ['sign-in-audit', params],
    queryFn: () => signInAuditApi.list(params),
    // A log is read for what happened just now.
    staleTime: 0,
  });

  const rows = data?.rows ?? [];
  const total = data?.total ?? 0;
  const lastPage = Math.max(0, Math.ceil(total / rowsPerPage) - 1);
  // An empty page of a log that still has rows (rows were pruned): move to
  // the last page that exists, only ever backwards.
  const pastLastPage = rows.length === 0 && total > 0 && page > lastPage;

  useEffect(() => {
    if (pastLastPage) {
      setPage(lastPage);
    }
  }, [pastLastPage, lastPage]);

  const userIdInvalid = filters.userId !== '' && userIdParam(filters.userId) === undefined;

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Sign-in audit
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Password logins, single sign-on callbacks, refreshes and refused API keys, newest first.
        Rows hold ids, a reason code, the client network and the browser family; never an email or a
        name.
      </Typography>

      <Paper sx={{ mb: 2, p: 2 }}>
        <Box display="flex" gap={2} flexWrap="wrap">
          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel id="sign-in-audit-method-label" shrink>
              Method
            </InputLabel>
            <Select
              labelId="sign-in-audit-method-label"
              id="sign-in-audit-method"
              value={filters.method}
              onChange={(event) =>
                changeFilters({ method: event.target.value as SignInAuditMethod | '' })
              }
              label="Method"
              displayEmpty
              notched
            >
              <MenuItem value="">All methods</MenuItem>
              {SIGN_IN_AUDIT_METHODS.map((method) => (
                <MenuItem key={method} value={method}>
                  {method}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 150 }}>
            <InputLabel id="sign-in-audit-outcome-label" shrink>
              Outcome
            </InputLabel>
            <Select
              labelId="sign-in-audit-outcome-label"
              id="sign-in-audit-outcome"
              value={filters.outcome}
              onChange={(event) =>
                changeFilters({ outcome: event.target.value as SignInAuditOutcome | '' })
              }
              label="Outcome"
              displayEmpty
              notched
            >
              <MenuItem value="">All outcomes</MenuItem>
              {SIGN_IN_AUDIT_OUTCOMES.map((outcome) => (
                <MenuItem key={outcome} value={outcome}>
                  {outcome}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <FormControl size="small" sx={{ minWidth: 220 }}>
            <InputLabel id="sign-in-audit-reason-label" shrink>
              Reason
            </InputLabel>
            <Select
              labelId="sign-in-audit-reason-label"
              id="sign-in-audit-reason"
              value={filters.reason}
              onChange={(event) =>
                changeFilters({ reason: event.target.value as SignInAuditReason | '' })
              }
              label="Reason"
              displayEmpty
              notched
            >
              <MenuItem value="">All reasons</MenuItem>
              {SIGN_IN_AUDIT_REASONS.map((reason) => (
                <MenuItem key={reason} value={reason}>
                  {reason}
                </MenuItem>
              ))}
            </Select>
          </FormControl>

          <TextField
            size="small"
            type="number"
            label="User id"
            value={filters.userId}
            onChange={(event) => changeFilters({ userId: event.target.value })}
            error={userIdInvalid}
            helperText={userIdInvalid ? 'A positive whole number' : undefined}
            slotProps={{ inputLabel: { shrink: true }, htmlInput: { min: 1, step: 1 } }}
            sx={{ width: 140 }}
          />
          <TextField
            size="small"
            type="datetime-local"
            label="From"
            value={filters.from}
            onChange={(event) => changeFilters({ from: event.target.value })}
            slotProps={{ inputLabel: { shrink: true } }}
          />
          <TextField
            size="small"
            type="datetime-local"
            label="To"
            value={filters.to}
            onChange={(event) => changeFilters({ to: event.target.value })}
            slotProps={{ inputLabel: { shrink: true } }}
          />
        </Box>
      </Paper>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          <Alert severity="error" sx={{ m: 2 }}>
            {getAPIError(error)?.message ?? 'The sign-in audit could not be loaded.'}
          </Alert>
        ) : (
          <>
            <TableContainer>
              <Table size="small" aria-label="Sign-in audit">
                <TableHead>
                  <TableRow>
                    <TableCell>Time</TableCell>
                    <TableCell>Method</TableCell>
                    <TableCell>Outcome</TableCell>
                    <TableCell>Reason</TableCell>
                    <TableCell align="right">Attempts</TableCell>
                    <TableCell align="right">User id</TableCell>
                    <TableCell>Client network</TableCell>
                    <TableCell>Browser</TableCell>
                    <TableCell>Request id</TableCell>
                    <TableCell>Subject reference</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {rows.length === 0 && total === 0 ? (
                    <TableRow>
                      <TableCell colSpan={10} align="center" sx={{ py: 4 }}>
                        <Typography variant="body2" color="text.secondary">
                          No sign-ins match these filters.
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ) : (
                    rows.map((entry) => (
                      <TableRow key={entry.id} hover data-testid="sign-in-audit-row">
                        <TableCell>{formatDate(entry.created_at, 'PPpp')}</TableCell>
                        <TableCell>{entry.method}</TableCell>
                        <TableCell>
                          <Chip
                            size="small"
                            label={entry.outcome}
                            color={entry.outcome === 'success' ? 'success' : 'error'}
                            variant="outlined"
                          />
                        </TableCell>
                        <TableCell>{entry.reason || '-'}</TableCell>
                        <TableCell align="right">{entry.attempts}</TableCell>
                        <TableCell align="right">{entry.user_id ?? '-'}</TableCell>
                        <TableCell>{entry.client_network || '-'}</TableCell>
                        <TableCell>{entry.user_agent_family || '-'}</TableCell>
                        <TableCell sx={{ fontFamily: 'monospace' }}>
                          {entry.request_id || '-'}
                        </TableCell>
                        <TableCell sx={{ fontFamily: 'monospace', wordBreak: 'break-all' }}>
                          {entry.subject_ref ?? '-'}
                        </TableCell>
                      </TableRow>
                    ))
                  )}
                </TableBody>
              </Table>
            </TableContainer>
            <TablePagination
              component="div"
              rowsPerPageOptions={[10, 20, 50, 100]}
              count={total}
              page={Math.min(page, lastPage)}
              rowsPerPage={rowsPerPage}
              onPageChange={(_event, next) => setPage(next)}
              onRowsPerPageChange={(event) => {
                setRowsPerPage(Number(event.target.value));
                setPage(0);
              }}
            />
          </>
        )}
      </Paper>
    </Box>
  );
};

export function Component() {
  return <SignInAudit />;
}

Component.displayName = 'SignInAudit';
