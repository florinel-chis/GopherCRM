import React from 'react';
import { Box, List, ListItem, ListItemIcon, ListItemText, Typography } from '@mui/material';
import { ArrowForward as ArrowIcon, FiberManualRecord as DotIcon } from '@mui/icons-material';
import { quoteStatusLabel, type QuoteStatusChange } from '@/types';
import { formatDate } from '@/utils/date';

export interface QuoteHistoryProps {
  history: QuoteStatusChange[];
  isLoading?: boolean;
}

const changedBy = (change: QuoteStatusChange): string => {
  if (!change.changed_by) {
    return `user #${change.changed_by_id}`;
  }
  return `${change.changed_by.first_name} ${change.changed_by.last_name}`.trim();
};

const describeChange = (change: QuoteStatusChange): string =>
  change.from_status === null
    ? `Created as ${quoteStatusLabel(change.to_status)}`
    : `${quoteStatusLabel(change.from_status)} to ${quoteStatusLabel(change.to_status)}`;

/**
 * The status changes of a quote as a vertical timeline, oldest first, as the
 * API returns them. The first row (from_status null) is the creation.
 */
export const QuoteHistory: React.FC<QuoteHistoryProps> = ({ history, isLoading = false }) => {
  if (isLoading && history.length === 0) {
    return <Typography color="text.secondary">Loading history…</Typography>;
  }
  if (history.length === 0) {
    return <Typography color="text.secondary">No status changes recorded</Typography>;
  }
  return (
    <List dense aria-label="Status history" data-testid="quote-history">
      {history.map((change, index) => (
        <ListItem key={change.id} alignItems="flex-start" data-testid="quote-history-row">
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
