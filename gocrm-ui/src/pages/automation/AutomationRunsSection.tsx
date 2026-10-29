import React, { useState } from 'react';
import { Link as RouterLink } from 'react-router-dom';
import { useQuery } from '@tanstack/react-query';
import {
  Alert,
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
import { automationApi, type AutomationRunFilters } from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import type { AutomationEntityType } from '@/types';
import { getAPIError } from '@/utils/apiError';
import { formatDate } from '@/utils/date';
import { AUTOMATION_DATETIME } from './automationFormat';
import { automationKeys } from './automationQueries';
import { RunStatusChip } from './AutomationChips';

const ROWS_PER_PAGE_OPTIONS = [5, 10, 25];

export interface AutomationRunsSectionProps {
  entityType: AutomationEntityType;
  entityId: number;
}

/**
 * The "Automation" section of a lead or deal page: the record's runs over
 * GET /automation/runs?entity_type&entity_id. The run history is admin and
 * sales on the API (sales narrowed to the records it owns), so the section
 * renders nothing for the other roles and never issues the request for them.
 */
export const AutomationRunsSection: React.FC<AutomationRunsSectionProps> = ({ entityType, entityId }) => {
  const { user } = useAuth();
  const canView = user?.role === 'admin' || user?.role === 'sales';
  const [paging, setPaging] = useState({ page: 0, limit: 5 });

  const filters: AutomationRunFilters = {
    entity_type: entityType,
    entity_id: entityId,
    offset: paging.page * paging.limit,
    limit: paging.limit,
  };

  // Under the run-list prefix, so rule changes and cancels mark it stale.
  const { data, error, isError, refetch, isFetching } = useQuery({
    queryKey: automationKeys.runList(filters),
    queryFn: () => automationApi.listRuns(filters),
    enabled: canView,
  });

  if (!canView) {
    return null;
  }

  const headingId = `${entityType}-automation-heading`;

  return (
    <Paper sx={{ p: 3, mb: 3 }} component="section" aria-labelledby={headingId}>
      <Box display="flex" justifyContent="space-between" alignItems="center" mb={2}>
        <Typography variant="h6" id={headingId}>
          Automation{data && ` (${data.total})`}
        </Typography>
        <Link
          component={RouterLink}
          to={`/automation/runs?entity_type=${entityType}&entity_id=${entityId}`}
          variant="body2"
        >
          View all
        </Link>
      </Box>
      {isError ? (
        <Alert
          severity="error"
          action={
            <Button color="inherit" size="small" onClick={() => void refetch()} disabled={isFetching}>
              Retry
            </Button>
          }
        >
          {getAPIError(error)?.message ?? 'The automation runs could not be loaded.'}
        </Alert>
      ) : data && data.runs.length > 0 ? (
        <>
          <Table size="small" aria-label="Automation runs">
            <TableHead>
              <TableRow>
                <TableCell>Run</TableCell>
                <TableCell>Rule</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Resumes</TableCell>
                <TableCell>Created</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {data.runs.map((run) => (
                <TableRow key={run.id} hover data-testid="record-run-row">
                  <TableCell>
                    <Link component={RouterLink} to={`/automation/runs/${run.id}`}>
                      #{run.id}
                    </Link>
                  </TableCell>
                  <TableCell>{`Rule #${run.rule_id} · v${run.rule_version}`}</TableCell>
                  <TableCell>
                    <RunStatusChip status={run.status} />
                  </TableCell>
                  <TableCell>{formatDate(run.resume_at, AUTOMATION_DATETIME)}</TableCell>
                  <TableCell>{formatDate(run.created_at, AUTOMATION_DATETIME)}</TableCell>
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
        <Typography color="text.secondary">
          {data ? `No automation runs for this ${entityType}` : 'Loading runs…'}
        </Typography>
      )}
    </Paper>
  );
};
