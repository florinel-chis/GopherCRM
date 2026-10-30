import React, { useState } from 'react';
import { useParams, Link as RouterLink } from 'react-router-dom';
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Grid,
  Link,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  Typography,
} from '@mui/material';
import { Cancel as CancelIcon } from '@mui/icons-material';
import { Loading } from '@/components/Loading';
import { ConfirmDialog } from '@/components/ConfirmDialog';
import { automationApi } from '@/api/endpoints';
import { useAuth } from '@/hooks/useAuth';
import { useSnackbar } from '@/hooks/useSnackbar';
import { isActiveRunStatus } from '@/types';
import { DATE_FALLBACK, formatDate } from '@/utils/date';
import { getAPIError, getHTTPStatus } from '@/utils/apiError';
import {
  AUTOMATION_DATETIME,
  cancelReasonLabel,
  entityLabel,
  entityPath,
  errorCodeLabel,
  runRuleLabel,
  stepTypeLabel,
} from './automationFormat';
import { afterRunCancelled, afterRunSettled, automationKeys } from './automationQueries';
import { OutcomeChip, RunStatusChip } from './AutomationChips';
import { AutomationStepDetails } from './AutomationStepDetails';

const Fact: React.FC<{ label: string; children: React.ReactNode }> = ({ label, children }) => (
  <Grid size={{ xs: 12, sm: 6, md: 3 }}>
    <Typography variant="caption" color="text.secondary" component="div">
      {label}
    </Typography>
    <Typography component="div">{children}</Typography>
  </Grid>
);

export const Component: React.FC = () => {
  const { id } = useParams();
  const runId = Number(id);
  const queryClient = useQueryClient();
  const { user } = useAuth();
  const { showSuccess, showError } = useSnackbar();
  const isAdmin = user?.role === 'admin';
  const [confirmOpen, setConfirmOpen] = useState(false);

  const { data: run, isLoading, error, refetch, isFetching } = useQuery({
    queryKey: automationKeys.run(runId),
    queryFn: () => automationApi.getRun(runId),
    enabled: Number.isInteger(runId) && runId > 0,
  });

  const cancelMutation = useMutation({
    mutationFn: () => automationApi.cancelRun(runId),
    onSuccess: async (cancelled) => {
      setConfirmOpen(false);
      // The response carries the step log, so the stored detail keeps it.
      await afterRunCancelled(queryClient, cancelled);
      showSuccess('Run cancelled');
    },
    onError: (err) => {
      setConfirmOpen(false);
      showError(getAPIError(err)?.message ?? 'Failed to cancel run');
      if (getHTTPStatus(err) === 409) {
        // The run finished meanwhile: re-read it and every list holding it.
        void afterRunSettled(queryClient, runId);
      }
    },
  });

  if (isLoading) {
    return <Loading />;
  }

  // A 404 wins over a cached copy (the run left the caller's scope or is
  // gone); any other failed refetch keeps the run on screen under an alert.
  const notFound = getHTTPStatus(error) === 404 || (!run && !error);
  const retryAlert = (
    <Alert
      severity="error"
      sx={{ mb: 2 }}
      action={
        <Button color="inherit" size="small" onClick={() => void refetch()} disabled={isFetching}>
          Retry
        </Button>
      }
    >
      {getAPIError(error)?.message ?? 'The run could not be loaded.'}
    </Alert>
  );

  if (!run || notFound) {
    return (
      <Box>
        <Typography variant="h5" gutterBottom>
          {notFound ? 'Run not found' : 'Failed to load run'}
        </Typography>
        {!notFound && retryAlert}
        <Link component={RouterLink} to="/automation/runs">
          Back to runs
        </Link>
      </Box>
    );
  }

  const ruleText = `${runRuleLabel(run)} · version ${run.rule_version}`;
  const canCancel = isAdmin && isActiveRunStatus(run.status);
  const steps = run.steps ?? [];

  return (
    <Box>
      {error && retryAlert}
      <Box display="flex" justifyContent="space-between" alignItems="center" flexWrap="wrap" gap={2} mb={3}>
        <Box display="flex" alignItems="center" gap={2}>
          <Typography variant="h4">Run #{run.id}</Typography>
          <RunStatusChip status={run.status} />
        </Box>
        {canCancel && (
          <Button
            variant="outlined"
            color="error"
            startIcon={<CancelIcon />}
            onClick={() => setConfirmOpen(true)}
            disabled={cancelMutation.isPending}
          >
            Cancel run
          </Button>
        )}
      </Box>

      <Paper sx={{ p: 3, mb: 3 }}>
        <Grid container spacing={2}>
          <Fact label="Rule">
            {/* A deleted rule has no editor page to link to. */}
            {isAdmin && !run.rule_deleted ? (
              <Link component={RouterLink} to={`/automation/rules/${run.rule_id}/edit`}>
                {ruleText}
              </Link>
            ) : (
              ruleText
            )}
          </Fact>
          <Fact label="Record">
            <Link component={RouterLink} to={entityPath(run.entity_type, run.entity_id)}>
              {entityLabel(run.entity_type, run.entity_id)}
            </Link>
          </Fact>
          <Fact label="Next step">{run.next_position || DATE_FALLBACK}</Fact>
          <Fact label="Attempts">{run.attempts}</Fact>
          <Fact label="Created">{formatDate(run.created_at, AUTOMATION_DATETIME)}</Fact>
          <Fact label="Updated">{formatDate(run.updated_at, AUTOMATION_DATETIME)}</Fact>
          <Fact label="Resumes">{formatDate(run.resume_at, AUTOMATION_DATETIME)}</Fact>
          <Fact label="Finished">{formatDate(run.finished_at, AUTOMATION_DATETIME)}</Fact>
          <Fact label="Cancel reason">
            {run.cancel_reason ? cancelReasonLabel(run.cancel_reason) : DATE_FALLBACK}
          </Fact>
          <Fact label="Error">{run.error_code ? errorCodeLabel(run.error_code) : DATE_FALLBACK}</Fact>
        </Grid>
      </Paper>

      <Paper sx={{ p: 3 }} component="section" aria-labelledby="run-step-log-heading">
        <Typography variant="h6" id="run-step-log-heading" mb={2}>
          Step log
        </Typography>
        {steps.length > 0 ? (
          <Table size="small" aria-label="Step log">
            <TableHead>
              <TableRow>
                <TableCell>Step</TableCell>
                <TableCell>Type</TableCell>
                <TableCell>Outcome</TableCell>
                <TableCell>Executed</TableCell>
                <TableCell>Details</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {steps.map((step) => (
                <TableRow key={step.id} data-testid="run-step-row">
                  <TableCell>#{step.position}</TableCell>
                  <TableCell>{stepTypeLabel(step.step_type)}</TableCell>
                  <TableCell>
                    <Box display="flex" alignItems="center" gap={1} flexWrap="wrap">
                      <OutcomeChip outcome={step.outcome} />
                      {step.error_code && (
                        <Typography variant="body2" color="error" component="span">
                          {errorCodeLabel(step.error_code)}
                        </Typography>
                      )}
                    </Box>
                  </TableCell>
                  <TableCell>{formatDate(step.executed_at, AUTOMATION_DATETIME)}</TableCell>
                  <TableCell>
                    <AutomationStepDetails details={step.details} />
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : (
          <Typography color="text.secondary">No step has run yet.</Typography>
        )}
      </Paper>

      <ConfirmDialog
        open={confirmOpen}
        title="Cancel run"
        message="The run stops before its next step. This cannot be undone."
        confirmText="Cancel run"
        cancelText="Keep running"
        severity="error"
        onConfirm={() => cancelMutation.mutate()}
        onCancel={() => setConfirmOpen(false)}
      />
    </Box>
  );
};
