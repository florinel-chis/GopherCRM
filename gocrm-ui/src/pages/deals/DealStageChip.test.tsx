import { describe, it, expect } from 'vitest';
import { render, screen } from '@/test/test-utils';
import { DealStageChip } from './DealStageChip';
import { DEAL_STAGES } from '@/types';

describe('DealStageChip', () => {
  it.each(DEAL_STAGES)('labels $value as "$label"', ({ value, label }) => {
    render(<DealStageChip stage={value} />);

    const chip = screen.getByTestId('deal-stage-chip');
    expect(chip).toHaveTextContent(label);
    expect(chip).toHaveAttribute('data-stage', value);
  });

  it('colours won and lost by outcome and leaves qualification neutral', () => {
    const { rerender } = render(<DealStageChip stage="won" />);
    expect(screen.getByTestId('deal-stage-chip')).toHaveClass('MuiChip-colorSuccess');

    rerender(<DealStageChip stage="lost" />);
    expect(screen.getByTestId('deal-stage-chip')).toHaveClass('MuiChip-colorError');

    rerender(<DealStageChip stage="qualification" />);
    expect(screen.getByTestId('deal-stage-chip')).toHaveClass('MuiChip-colorDefault');
  });
});
