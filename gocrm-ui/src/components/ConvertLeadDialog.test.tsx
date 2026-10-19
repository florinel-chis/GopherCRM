import { describe, expect, it, vi } from 'vitest';
import { fireEvent, render, screen, waitFor } from '@/test/test-utils';
import { createMockLead } from '@/test/factories';
import { ConvertLeadDialog } from './ConvertLeadDialog';

describe('ConvertLeadDialog website', () => {
  it('passes the trimmed website to conversion', async () => {
    const onConfirm = vi.fn();
    render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={onConfirm} />,
    );
    fireEvent.change(screen.getByLabelText('Website'), {
      target: { value: '  https://example.com/about  ' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));
    await waitFor(() =>
      expect(onConfirm).toHaveBeenCalledWith(
        expect.objectContaining({ website: 'https://example.com/about' }),
      ),
    );
  });

  it.each([
    ['javascript:alert(1)', 'Website must be an http(s) URL'],
    ['https:///example.com', 'Website must be an http(s) URL'],
    ['https://example.com/' + 'a'.repeat(237), 'Website must be 255 characters or fewer'],
  ])('rejects %s', async (website, message) => {
    const onConfirm = vi.fn();
    render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={onConfirm} />,
    );
    fireEvent.change(screen.getByLabelText('Website'), { target: { value: website } });
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));
    expect(await screen.findByText(message)).toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();
  });
});
