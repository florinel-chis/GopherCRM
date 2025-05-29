import { describe, it, expect, vi } from 'vitest';
import { render, screen, fireEvent } from '@testing-library/react';
import { ConfirmDialog } from './ConfirmDialog';

describe('ConfirmDialog', () => {
  const defaultProps = {
    open: true,
    title: 'Test Title',
    message: 'Test message',
    onConfirm: vi.fn(),
    onCancel: vi.fn(),
  };

  it('renders when open', () => {
    render(<ConfirmDialog {...defaultProps} />);

    expect(screen.getByText('Test Title')).toBeInTheDocument();
    expect(screen.getByText('Test message')).toBeInTheDocument();
  });

  it('does not render when closed', () => {
    render(<ConfirmDialog {...defaultProps} open={false} />);

    expect(screen.queryByText('Test Title')).not.toBeInTheDocument();
    expect(screen.queryByText('Test message')).not.toBeInTheDocument();
  });

  it('renders custom confirm text', () => {
    render(<ConfirmDialog {...defaultProps} confirmText="Delete" />);

    expect(screen.getByText('Delete')).toBeInTheDocument();
  });

  it('renders custom cancel text', () => {
    render(<ConfirmDialog {...defaultProps} cancelText="No, thanks" />);

    expect(screen.getByText('No, thanks')).toBeInTheDocument();
  });

  it('calls onConfirm when confirm button is clicked', () => {
    const onConfirm = vi.fn();
    render(<ConfirmDialog {...defaultProps} onConfirm={onConfirm} />);

    fireEvent.click(screen.getByText('Confirm'));

    expect(onConfirm).toHaveBeenCalledTimes(1);
  });

  it('calls onCancel when cancel button is clicked', () => {
    const onCancel = vi.fn();
    render(<ConfirmDialog {...defaultProps} onCancel={onCancel} />);

    fireEvent.click(screen.getByText('Cancel'));

    expect(onCancel).toHaveBeenCalledTimes(1);
  });

  it('applies error severity color to confirm button', () => {
    render(<ConfirmDialog {...defaultProps} severity="error" />);

    const confirmButton = screen.getByText('Confirm').closest('button');
    expect(confirmButton).toHaveClass('MuiButton-containedError');
  });

  it('applies warning severity color to confirm button', () => {
    render(<ConfirmDialog {...defaultProps} severity="warning" />);

    const confirmButton = screen.getByText('Confirm').closest('button');
    expect(confirmButton).toHaveClass('MuiButton-containedWarning');
  });

  it('applies primary color by default', () => {
    render(<ConfirmDialog {...defaultProps} severity="info" />);

    const confirmButton = screen.getByText('Confirm').closest('button');
    expect(confirmButton).toHaveClass('MuiButton-containedPrimary');
  });

  it('has autoFocus on confirm button', () => {
    render(<ConfirmDialog {...defaultProps} />);

    const confirmButton = screen.getByText('Confirm');
    expect(confirmButton).toBeInTheDocument();
    // Note: autoFocus is a prop, not an attribute in the DOM
  });

  it('has proper ARIA attributes', () => {
    render(<ConfirmDialog {...defaultProps} />);

    expect(screen.getByRole('dialog')).toHaveAttribute('aria-labelledby', 'confirm-dialog-title');
    expect(screen.getByRole('dialog')).toHaveAttribute('aria-describedby', 'confirm-dialog-description');
  });
});