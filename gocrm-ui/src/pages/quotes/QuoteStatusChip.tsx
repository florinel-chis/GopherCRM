import React from 'react';
import { Chip, type ChipProps } from '@mui/material';
import { quoteStatusLabel, type QuoteStatus } from '@/types';

const STATUS_COLORS: Record<QuoteStatus, ChipProps['color']> = {
  draft: 'default',
  sent: 'info',
  accepted: 'success',
  rejected: 'error',
  expired: 'warning',
};

export interface QuoteStatusChipProps {
  status: QuoteStatus;
  size?: ChipProps['size'];
}

export const QuoteStatusChip: React.FC<QuoteStatusChipProps> = ({ status, size = 'small' }) => (
  <Chip
    label={quoteStatusLabel(status)}
    color={STATUS_COLORS[status] ?? 'default'}
    size={size}
    data-testid="quote-status-chip"
    data-status={status}
  />
);
