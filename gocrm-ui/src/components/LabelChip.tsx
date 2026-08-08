import React from 'react';
import { Chip } from '@mui/material';
import type { ChipProps } from '@mui/material';
import { contrastingTextColor } from './labelColors';
import type { Label } from '@/types';

export interface LabelChipProps extends Omit<ChipProps, 'label' | 'color'> {
  label: Label;
}

/**
 * A task label rendered in its own colour, with the text colour chosen for
 * contrast rather than fixed, since label colours are user-supplied.
 */
export const LabelChip: React.FC<LabelChipProps> = ({ label, sx, size = 'small', ...chipProps }) => {
  const background = label.color;
  const textColor = contrastingTextColor(background);

  return (
    <Chip
      label={label.name}
      size={size}
      data-testid={`label-chip-${label.id}`}
      sx={{
        backgroundColor: background,
        color: textColor,
        fontWeight: 500,
        '& .MuiChip-deleteIcon': {
          color: textColor,
          opacity: 0.7,
          '&:hover': { color: textColor, opacity: 1 },
        },
        ...sx,
      }}
      {...chipProps}
    />
  );
};
