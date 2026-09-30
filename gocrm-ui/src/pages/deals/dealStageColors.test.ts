import { describe, it, expect } from 'vitest';
import { createTheme } from '@mui/material/styles';
import { dealStagePaletteColor, STAGE_COLORS } from './dealStageColors';

describe('dealStagePaletteColor', () => {
  const theme = createTheme();

  it('resolves each stage to the theme colour of its chip', () => {
    expect(dealStagePaletteColor(theme, 'qualification')).toBe(theme.palette.grey[400]);
    expect(dealStagePaletteColor(theme, 'proposal')).toBe(theme.palette.info.main);
    expect(dealStagePaletteColor(theme, 'negotiation')).toBe(theme.palette.warning.main);
    expect(dealStagePaletteColor(theme, 'won')).toBe(theme.palette.success.main);
    expect(dealStagePaletteColor(theme, 'lost')).toBe(theme.palette.error.main);
  });

  it('covers every stage in the one colour map', () => {
    expect(Object.keys(STAGE_COLORS).sort()).toEqual(['lost', 'negotiation', 'proposal', 'qualification', 'won']);
  });
});
