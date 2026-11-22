import React from 'react';
import {
  Alert,
  Box,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TablePagination,
  TableRow,
  Typography,
} from '@mui/material';
import { Loading } from '@/components/Loading';
import { usePagedList } from '@/hooks/usePagedList';
import { roleAuditApi, type RoleAuditAction, type RoleAuditMethod } from '@/api/endpoints';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';

const ACTION_LABELS: Record<RoleAuditAction, string> = {
  role_created: 'Role created',
  role_updated: 'Role updated',
  role_deleted: 'Role deleted',
  user_role_changed: 'User role changed',
  user_activated: 'Account activated',
  user_deactivated: 'Account deactivated',
  user_erased: 'Account erased',
  user_email_changed: 'Email changed',
};

const METHOD_LABELS: Record<RoleAuditMethod, string> = {
  bearer: 'Session',
  api_key: 'API key',
  oidc: 'Single sign-on',
  scim: 'SCIM',
  migration: 'Migration',
};

// An unknown value is shown as it is stored.
const actionLabel = (action: string): string => ACTION_LABELS[action as RoleAuditAction] ?? action;
const methodLabel = (method: string): string => METHOD_LABELS[method as RoleAuditMethod] ?? method;

const DASH = '—';

/**
 * The role audit log: role changes and account activations, deactivations,
 * erasures and login-email changes, in the order the API returns (newest
 * first). Read-only; the rows hold ids and role names only.
 */
export const RoleAudit: React.FC = () => {
  const {
    rows,
    total,
    isLoading,
    isError,
    error,
    displayPage,
    rowsPerPage,
    onPageChange,
    onRowsPerPageChange,
  } = usePagedList({
    queryKey: (params) => ['role-audit', params],
    queryFn: (params) => roleAuditApi.list(params),
    // A log is read for what happened just now.
    staleTime: 0,
  });

  return (
    <Box>
      <Typography variant="h4" component="h1" gutterBottom>
        Role audit
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 2 }}>
        Role creation, changes and deletion, users&apos; role changes, and account activation,
        deactivation, erasure and email changes, newest first. Rows hold user ids and role names;
        never an email or a person&apos;s name.
      </Typography>

      <Paper>
        {isLoading ? (
          <Loading />
        ) : isError ? (
          <Alert severity="error" sx={{ m: 2 }}>
            {getAPIError(error)?.message ?? 'The role audit could not be loaded.'}
          </Alert>
        ) : (
          <>
            <TableContainer>
              <Table size="small" aria-label="Role audit">
                <TableHead>
                  <TableRow>
                    <TableCell>Time</TableCell>
                    <TableCell>Action</TableCell>
                    <TableCell>Method</TableCell>
                    <TableCell align="right">Acting user</TableCell>
                    <TableCell align="right">Target user</TableCell>
                    <TableCell>Role</TableCell>
                  </TableRow>
                </TableHead>
                <TableBody>
                  {rows.length === 0 && total === 0 ? (
                    <TableRow>
                      <TableCell colSpan={6} align="center" sx={{ py: 4 }}>
                        <Typography variant="body2" color="text.secondary">
                          No role audit rows yet.
                        </Typography>
                      </TableCell>
                    </TableRow>
                  ) : (
                    rows.map((entry) => (
                      <TableRow key={entry.id} hover data-testid="role-audit-row">
                        <TableCell>{formatDate(entry.created_at, 'PPpp')}</TableCell>
                        <TableCell>{actionLabel(entry.action)}</TableCell>
                        <TableCell>{methodLabel(entry.auth_method)}</TableCell>
                        <TableCell align="right">{entry.actor_user_id ?? DASH}</TableCell>
                        <TableCell align="right" data-testid="role-audit-target">
                          {entry.target_user_id ?? DASH}
                        </TableCell>
                        <TableCell>{entry.role_name || DASH}</TableCell>
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
              page={displayPage}
              rowsPerPage={rowsPerPage}
              onPageChange={onPageChange}
              onRowsPerPageChange={onRowsPerPageChange}
            />
          </>
        )}
      </Paper>
    </Box>
  );
};

export function Component() {
  return <RoleAudit />;
}

Component.displayName = 'RoleAudit';
