import React from 'react';
import { Chip, type ChipProps } from '@mui/material';
import { dealStageLabel, type DealStage } from '@/types';

// The one place the five stages get a colour. Open stages progress from
// neutral to warm; the two closed stages take the outcome colours.
const STAGE_COLORS: Record<DealStage, ChipProps['color']> = {
  qualification: 'default',
  proposal: 'info',
  negotiation: 'warning',
  won: 'success',
  lost: 'error',
};

export interface DealStageChipProps {
  stage: DealStage;
  size?: ChipProps['size'];
}

export const DealStageChip: React.FC<DealStageChipProps> = ({ stage, size = 'small' }) => (
  <Chip
    label={dealStageLabel(stage)}
    color={STAGE_COLORS[stage] ?? 'default'}
    size={size}
    data-testid="deal-stage-chip"
    data-stage={stage}
  />
);
