import React, { useId, useState } from 'react';
import { useMutation } from '@tanstack/react-query';
import {
  Alert,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Stack,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Typography,
} from '@mui/material';
import { automationApi, dealsApi } from '@/api/endpoints';
import { LeadAutocomplete } from '@/components/LeadAutocomplete';
import { RecordAutocomplete } from '@/components/RecordAutocomplete';
import { dealStageLabel, type AutomationRule, type Deal } from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError } from '@/utils/apiError';
import { AUTOMATION_DATETIME, errorCodeLabel, stepTypeLabel } from './automationFormat';
import { OutcomeChip } from './AutomationChips';
import { AutomationStepDetails } from './AutomationStepDetails';

export interface DryRunDialogProps {
  rule: AutomationRule;
  open: boolean;
  onClose: () => void;
}

const DEAL_SEARCH_LIMIT = 20;

// Every stage: a rule may well be meant for won or lost deals, which the
// shared DealAutocomplete (open deals only) would hide.
const searchDeals = async (term: string): Promise<Deal[]> => {
  const result = await dealsApi.getDeals({ search: term || undefined, limit: DEAL_SEARCH_LIMIT });
  return result.deals;
};

const dealOptionLabel = (deal: Deal): string => `${deal.title} (${dealStageLabel(deal.stage)})`;

function DryRunContent({ rule, onClose }: { rule: AutomationRule; onClose: () => void }) {
  const [entityId, setEntityId] = useState<number | null>(null);

  const dryRun = useMutation({
    mutationFn: (id: number) => automationApi.dryRun(rule.id, { entity_type: rule.entity_type, entity_id: id }),
  });
  const report = dryRun.isSuccess ? dryRun.data : null;
  const errorMessage = dryRun.isError ? getAPIError(dryRun.error)?.message ?? 'The dry run failed' : null;

  return (
    <>
      <DialogContent>
        <Stack spacing={2} sx={{ pt: 1 }}>
          <Alert severity="info" data-testid="dry-run-notice">
            A dry run writes, sends and notifies nothing: no task, email, notification, owner change or run is
            created.
          </Alert>
          <TextField
            label="Record type"
            value={rule.entity_type === 'lead' ? 'Lead' : 'Deal'}
            InputProps={{ readOnly: true }}
            size="small"
          />
          {rule.entity_type === 'lead' ? (
            <LeadAutocomplete label="Lead" value={entityId} onChange={(id) => setEntityId(id)} />
          ) : (
            <RecordAutocomplete<Deal>
              value={entityId}
              onChange={(id) => setEntityId(id)}
              label="Deal"
              queryKeys={['deal', 'deals-all']}
              search={searchDeals}
              lookup={dealsApi.getDeal}
              getOptionLabel={dealOptionLabel}
              noOptionsText="No matching deals"
              emptySearchText="Type to search deals"
            />
          )}
          <Box>
            <Button
              variant="contained"
              disabled={entityId === null || dryRun.isPending}
              onClick={() => {
                if (entityId !== null) dryRun.mutate(entityId);
              }}
            >
              Run dry run
            </Button>
          </Box>

          {errorMessage ? (
            <Alert severity="error" data-testid="dry-run-error">
              {errorMessage}
            </Alert>
          ) : null}

          {report ? (
            <Box>
              <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
                {`Simulated from ${formatDate(report.simulated_start, AUTOMATION_DATETIME)} to ${formatDate(
                  report.simulated_end,
                  AUTOMATION_DATETIME,
                )} · rule version ${report.rule_version}`}
              </Typography>
              {report.steps.length === 0 ? (
                <Typography variant="body2">No step would run.</Typography>
              ) : (
                <Table size="small" aria-label="Dry run steps">
                  <TableHead>
                    <TableRow>
                      <TableCell>#</TableCell>
                      <TableCell>Step</TableCell>
                      <TableCell>Outcome</TableCell>
                      <TableCell>Simulated at</TableCell>
                      <TableCell>Details</TableCell>
                    </TableRow>
                  </TableHead>
                  <TableBody>
                    {report.steps.map((step) => (
                      <TableRow key={`${step.position}-${step.simulated_at}`} data-testid="dry-run-step">
                        <TableCell>#{step.position}</TableCell>
                        <TableCell>{stepTypeLabel(step.step_type)}</TableCell>
                        <TableCell>
                          <Stack direction="row" spacing={1} alignItems="center">
                            <OutcomeChip outcome={step.outcome} />
                            {step.error_code ? (
                              <Typography variant="body2" color="text.secondary" component="span">
                                {errorCodeLabel(step.error_code)}
                              </Typography>
                            ) : null}
                          </Stack>
                        </TableCell>
                        <TableCell>{formatDate(step.simulated_at, AUTOMATION_DATETIME)}</TableCell>
                        <TableCell>
                          <AutomationStepDetails details={step.details} />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </Box>
          ) : null}
        </Stack>
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Close</Button>
      </DialogActions>
    </>
  );
}

/**
 * Simulates the stored rule against one lead or deal. The content unmounts
 * when the dialog closes, so the picked record and the report start empty
 * every time it opens.
 */
export const DryRunDialog: React.FC<DryRunDialogProps> = ({ rule, open, onClose }) => {
  const titleId = useId();
  return (
    <Dialog open={open} onClose={onClose} aria-labelledby={titleId} maxWidth="md" fullWidth>
      <DialogTitle id={titleId}>{`Dry run: ${rule.name}`}</DialogTitle>
      {open ? <DryRunContent rule={rule} onClose={onClose} /> : null}
    </Dialog>
  );
};
