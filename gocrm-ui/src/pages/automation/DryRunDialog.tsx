import React, { useCallback, useRef, useId, useState } from 'react';
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
import { automationApi, dealsApi, leadsApi, type AutomationRuleInput } from '@/api/endpoints';
import { RecordAutocomplete } from '@/components/RecordAutocomplete';
import { dealStageLabel, type AutomationEntityType, type AutomationRule, type Deal, type Lead } from '@/types';
import { formatDate } from '@/utils/date';
import { getAPIError, getErrorMessage, getHTTPStatus } from '@/utils/apiError';
import { AUTOMATION_DATETIME, errorCodeLabel, stepTypeLabel } from './automationFormat';
import { OutcomeChip } from './AutomationChips';
import { AutomationStepDetails } from './AutomationStepDetails';

/** Unsaved rule editor values, simulated through POST /automation/rules/dry-run. */
export interface DryRunDraft {
  name: string;
  entity_type: AutomationEntityType;
  payload: AutomationRuleInput;
}

interface DryRunDialogBaseProps {
  open: boolean;
  onClose: () => void;
  /** Called with a draft refusal (400) so the editor can mark its fields. */
  onValidationError?: (error: unknown) => void;
}

/**
 * Either a saved rule (its stored version is simulated) or a draft (the
 * editor's current values, saved nowhere).
 */
export type DryRunDialogProps = DryRunDialogBaseProps &
  ({ rule: AutomationRule; draft?: undefined } | { draft: DryRunDraft; rule?: undefined });

type DryRunSource = { kind: 'saved'; rule: AutomationRule } | { kind: 'draft'; draft: DryRunDraft };

const SEARCH_LIMIT = 20;

// Same search and label as LeadAutocomplete, used directly so a failed search
// can be reported instead of showing as an empty list.
const searchLeads = async (term: string): Promise<Lead[]> => {
  const result = await leadsApi.getLeads({ search: term || undefined, limit: SEARCH_LIMIT });
  return result.data;
};

const leadOptionLabel = (lead: Lead): string => {
  const name = lead.contact_name || lead.email;
  return lead.company_name ? `${name} (${lead.company_name})` : name;
};

// Every stage: a rule may well be meant for won or lost deals, which the
// shared DealAutocomplete (open deals only) would hide.
const searchDeals = async (term: string): Promise<Deal[]> => {
  const result = await dealsApi.getDeals({ search: term || undefined, limit: SEARCH_LIMIT });
  return result.deals;
};

const dealOptionLabel = (deal: Deal): string => `${deal.title} (${dealStageLabel(deal.stage)})`;

function DryRunContent({
  source,
  onClose,
  onValidationError,
}: {
  source: DryRunSource;
  onClose: () => void;
  onValidationError?: (error: unknown) => void;
}) {
  const entityType = source.kind === 'saved' ? source.rule.entity_type : source.draft.entity_type;
  const [entityId, setEntityId] = useState<number | null>(null);
  const [searchError, setSearchError] = useState<string | null>(null);
  // Only the newest search may set or clear the error: an older one that
  // fails late must not flag the results of a newer one.
  const latestSearch = useRef(0);

  const dryRun = useMutation({
    mutationFn: (id: number) =>
      source.kind === 'saved'
        ? automationApi.dryRun(source.rule.id, { entity_type: entityType, entity_id: id })
        : automationApi.dryRunDraft({ rule: source.draft.payload, entity_type: entityType, entity_id: id }),
    onError: (error) => {
      if (source.kind === 'draft' && getHTTPStatus(error) === 400) onValidationError?.(error);
    },
  });
  const { reset: resetDryRun } = dryRun;
  // Only the run for the record picked now is shown: a new pick drops the
  // previous result, and an answer still in flight for an earlier pick (the
  // mutation is reset, and the report must name the picked id) never lands.
  const forPicked = dryRun.variables === entityId;
  const report = dryRun.isSuccess && forPicked && dryRun.data.entity_id === entityId ? dryRun.data : null;
  const errorMessage =
    dryRun.isError && forPicked ? getAPIError(dryRun.error)?.message ?? 'The dry run failed' : null;

  const pick = useCallback(
    (id: number | null) => {
      setEntityId(id);
      resetDryRun();
    },
    [resetDryRun],
  );

  const reportingSearch = useCallback(
    <T,>(search: (term: string) => Promise<T[]>) =>
      async (term: string): Promise<T[]> => {
        latestSearch.current += 1;
        const ticket = latestSearch.current;
        try {
          const records = await search(term);
          if (ticket === latestSearch.current) setSearchError(null);
          return records;
        } catch (error) {
          if (ticket === latestSearch.current) {
            setSearchError(getAPIError(error)?.message ?? getErrorMessage(error, 'request failed'));
          }
          throw error;
        }
      },
    [],
  );
  const searchMessage = searchError ? `Records could not be loaded: ${searchError}` : undefined;

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
            value={entityType === 'lead' ? 'Lead' : 'Deal'}
            InputProps={{ readOnly: true }}
            size="small"
          />
          {entityType === 'lead' ? (
            <RecordAutocomplete<Lead>
              value={entityId}
              onChange={(id) => pick(id)}
              label="Lead"
              queryKeys={['lead', 'leads-dry-run']}
              search={reportingSearch(searchLeads)}
              lookup={leadsApi.getLead}
              getOptionLabel={leadOptionLabel}
              noOptionsText="No matching leads"
              emptySearchText="Type to search leads"
              error={!!searchMessage}
              helperText={searchMessage}
            />
          ) : (
            <RecordAutocomplete<Deal>
              value={entityId}
              onChange={(id) => pick(id)}
              label="Deal"
              queryKeys={['deal', 'deals-all']}
              search={reportingSearch(searchDeals)}
              lookup={dealsApi.getDeal}
              getOptionLabel={dealOptionLabel}
              noOptionsText="No matching deals"
              emptySearchText="Type to search deals"
              error={!!searchMessage}
              helperText={searchMessage}
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
                )} · ${report.draft ? 'unsaved form values' : `rule version ${report.rule_version}`}`}
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
 * Simulates a saved rule, or the rule editor's unsaved values, against one
 * lead or deal. The content unmounts when the dialog closes, so the picked
 * record and the report start empty every time it opens.
 */
export const DryRunDialog: React.FC<DryRunDialogProps> = ({ rule, draft, open, onClose, onValidationError }) => {
  const titleId = useId();
  const source: DryRunSource = draft ? { kind: 'draft', draft } : { kind: 'saved', rule: rule as AutomationRule };
  const name = draft ? draft.name.trim() || 'unsaved rule' : (rule as AutomationRule).name;
  return (
    <Dialog open={open} onClose={onClose} aria-labelledby={titleId} maxWidth="md" fullWidth>
      <DialogTitle id={titleId}>{`Dry run: ${name}`}</DialogTitle>
      {open ? <DryRunContent source={source} onClose={onClose} onValidationError={onValidationError} /> : null}
    </Dialog>
  );
};
