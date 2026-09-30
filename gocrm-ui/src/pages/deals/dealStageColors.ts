import type { ChipProps } from '@mui/material';
import type { Theme } from '@mui/material/styles';
import type { DealStage } from '@/types';

// The one place the five stages get a colour. Open stages progress from
// neutral to warm; the two closed stages take the outcome colours.
export const STAGE_COLORS: Record<DealStage, ChipProps['color']> = {
  qualification: 'default',
  proposal: 'info',
  negotiation: 'warning',
  won: 'success',
  lost: 'error',
};

/**
 * The stage colour as a CSS colour from the theme, for borders and other
 * places a Chip `color` prop cannot reach. `default` maps to the grey the
 * default Chip uses.
 */
export const dealStagePaletteColor = (theme: Theme, stage: DealStage): string => {
  const color = STAGE_COLORS[stage] ?? 'default';
  if (color === 'default') {
    return theme.palette.grey[400];
  }
  return theme.palette[color].main;
};
