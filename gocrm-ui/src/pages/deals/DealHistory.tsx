import React from 'react';
import { Box, List, ListItem, ListItemIcon, ListItemText, Typography } from '@mui/material';
import { ArrowForward as ArrowIcon, FiberManualRecord as DotIcon } from '@mui/icons-material';
import { dealStageLabel, type DealStageChange } from '@/types';
import { formatDate } from '@/utils/date';

export interface DealHistoryProps {
  history: DealStageChange[];
  isLoading?: boolean;
}

const changedBy = (change: DealStageChange): string => {
  if (!change.changed_by) {
    return `user #${change.changed_by_id}`;
  }
  const name = `${change.changed_by.first_name} ${change.changed_by.last_name}`.trim();
  return name || change.changed_by.email;
};

const describeChange = (change: DealStageChange): string =>
  change.from_stage === null
    ? `Created in ${dealStageLabel(change.to_stage)}`
    : `${dealStageLabel(change.from_stage)} to ${dealStageLabel(change.to_stage)}`;

/**
 * The stage changes of a deal as a vertical timeline, oldest first, as the
 * API returns them. The first row (from_stage null) is the creation.
 */
export const DealHistory: React.FC<DealHistoryProps> = ({ history, isLoading = false }) => {
  if (isLoading && history.length === 0) {
    return <Typography color="text.secondary">Loading history…</Typography>;
  }
  if (history.length === 0) {
    return <Typography color="text.secondary">No stage changes recorded</Typography>;
  }
  return (
    <List dense aria-label="Stage history" data-testid="deal-history">
      {history.map((change, index) => (
        <ListItem key={change.id} alignItems="flex-start" data-testid="deal-history-row">
          <ListItemIcon sx={{ minWidth: 32, mt: 0.5 }}>
            {index === history.length - 1 ? (
              <DotIcon color="primary" fontSize="small" />
            ) : (
              <ArrowIcon color="action" fontSize="small" />
            )}
          </ListItemIcon>
          <ListItemText
            primary={describeChange(change)}
            secondary={
              <Box component="span">
                {changedBy(change)} · {formatDate(change.changed_at, 'MMM dd, yyyy HH:mm')}
              </Box>
            }
          />
        </ListItem>
      ))}
    </List>
  );
};
