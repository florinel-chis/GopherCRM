import { Chip } from '@mui/material';
import type { ChipProps } from '@mui/material';
import type { AutomationOutcome, AutomationRunStatus } from '@/types';
import { outcomeLabel, runStatusLabel } from './automationFormat';

type ChipColor = NonNullable<ChipProps['color']>;

const RUN_STATUS_COLORS: Record<AutomationRunStatus, ChipColor> = {
  pending: 'default',
  running: 'info',
  waiting: 'warning',
  completed: 'success',
  failed: 'error',
  cancelled: 'default',
};

const OUTCOME_COLORS: Record<AutomationOutcome, ChipColor> = {
  succeeded: 'success',
  skipped: 'default',
  failed: 'error',
  sending: 'info',
  waiting: 'warning',
};

export function RunStatusChip({ status }: { status: AutomationRunStatus }) {
  return (
    <Chip
      data-testid="run-status-chip"
      size="small"
      label={runStatusLabel(status)}
      color={RUN_STATUS_COLORS[status] ?? 'default'}
      variant={status === 'cancelled' ? 'outlined' : 'filled'}
    />
  );
}

export function OutcomeChip({ outcome }: { outcome: AutomationOutcome }) {
  return (
    <Chip
      data-testid="outcome-chip"
      size="small"
      label={outcomeLabel(outcome)}
      color={OUTCOME_COLORS[outcome] ?? 'default'}
    />
  );
}
