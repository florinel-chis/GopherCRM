import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent, waitFor, within } from '@testing-library/react';
import { LostReasonDialog } from './LostReasonDialog';

describe('LostReasonDialog', () => {
  it('refuses an over-long reason and sends a trimmed one', () => {
    const onConfirm = vi.fn();
    render(<LostReasonDialog open onCancel={vi.fn()} onConfirm={onConfirm} />);

    const dialog = screen.getByRole('dialog', { name: 'Mark deal as lost' });
    const input = within(dialog).getByLabelText(/^Lost reason/);
    // maxLength stops typing, but a paste or a programmatic value can exceed it.
    fireEvent.change(input, { target: { value: 'x'.repeat(256) } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));
    expect(within(dialog).getByText('Lost reason must be 255 characters or fewer')).toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();

    fireEvent.change(input, { target: { value: '  Went with a competitor  ' } });
    fireEvent.click(within(dialog).getByRole('button', { name: 'Mark as lost' }));
    expect(onConfirm).toHaveBeenCalledWith('Went with a competitor');
  });

  it('starts from the initial reason each time it opens', async () => {
    const props = { onCancel: vi.fn(), onConfirm: vi.fn() };
    const { rerender } = render(<LostReasonDialog open initialReason="Budget cut" {...props} />);

    const input = screen.getByLabelText(/^Lost reason/);
    expect(input).toHaveValue('Budget cut');
    fireEvent.change(input, { target: { value: 'Typed but abandoned' } });

    rerender(<LostReasonDialog open={false} initialReason="Budget cut" {...props} />);
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    rerender(<LostReasonDialog open initialReason="Budget cut" {...props} />);

    expect(await screen.findByLabelText(/^Lost reason/)).toHaveValue('Budget cut');
  });

  it('disables both buttons while the change is pending', () => {
    render(<LostReasonDialog open pending onCancel={vi.fn()} onConfirm={vi.fn()} />);

    expect(screen.getByRole('button', { name: 'Cancel' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Mark as lost' })).toBeDisabled();
  });
});
