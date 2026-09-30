import React from 'react';
import { Chip, type ChipProps } from '@mui/material';
import { dealStageLabel, type DealStage } from '@/types';
import { STAGE_COLORS } from './dealStageColors';

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
