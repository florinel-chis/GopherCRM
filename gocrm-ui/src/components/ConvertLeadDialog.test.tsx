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

// The administrator's conversion settings (GET /configurations/ui) and the
// server's refusals of POST /leads/:id/convert.
describe('ConvertLeadDialog conversion settings', () => {
  const notesRefusal = {
    response: {
      status: 400,
      data: {
        code: 'VALIDATION_ERROR',
        message: 'Conversion notes are required.',
        details: { field: 'notes' },
      },
    },
  };

  it('requires notes when the setting is on and does not submit blank ones', async () => {
    const onConfirm = vi.fn();
    render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={onConfirm} requireNotes />,
    );
    expect(screen.getByLabelText(/Conversion Notes/)).toBeRequired();
    fireEvent.change(screen.getByLabelText(/Conversion Notes/), { target: { value: '   ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));

    expect(await screen.findByText('Conversion notes are required.')).toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();

    fireEvent.change(screen.getByLabelText(/Conversion Notes/), { target: { value: ' Signed the contract ' } });
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));
    await waitFor(() =>
      expect(onConfirm).toHaveBeenCalledWith(expect.objectContaining({ notes: 'Signed the contract' })),
    );
  });

  it('applies a notes requirement that arrives after the dialog mounted', async () => {
    const onConfirm = vi.fn();
    const lead = createMockLead();
    const { rerender } = render(
      <ConvertLeadDialog open lead={lead} onClose={vi.fn()} onConfirm={onConfirm} />,
    );
    rerender(<ConvertLeadDialog open lead={lead} onClose={vi.fn()} onConfirm={onConfirm} requireNotes />);
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));
    expect(await screen.findByText('Conversion notes are required.')).toBeInTheDocument();
    expect(onConfirm).not.toHaveBeenCalled();
  });

  it('keeps notes optional when the setting is off', async () => {
    const onConfirm = vi.fn();
    render(<ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={onConfirm} />);
    expect(screen.getByLabelText(/Conversion Notes/)).not.toBeRequired();
    fireEvent.click(screen.getByRole('button', { name: 'Convert to Customer' }));
    await waitFor(() => expect(onConfirm).toHaveBeenCalledWith({ company_name: 'Test Company' }));
  });

  it("shows the server's notes refusal on the notes field", async () => {
    render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={vi.fn()} error={notesRefusal} />,
    );
    const notes = screen.getByLabelText(/Conversion Notes/);
    await waitFor(() => expect(notes).toHaveAttribute('aria-invalid', 'true'));
    expect(screen.getByText('Conversion notes are required.')).toBeInTheDocument();
    // A field refusal is shown on its field, not repeated above the form.
    expect(screen.queryByTestId('convert-lead-error')).not.toBeInTheDocument();
  });

  it('shows the server message when the lead is already converted', () => {
    render(
      <ConvertLeadDialog
        open
        lead={createMockLead()}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        error={{ response: { status: 400, data: { code: 'BAD_REQUEST', message: 'lead already converted' } } }}
      />,
    );
    expect(screen.getByTestId('convert-lead-error')).toHaveTextContent('lead already converted');
  });

  it("shows the server's status refusal", () => {
    const message = 'Leads with status "new" cannot be converted. Allowed statuses: qualified.';
    render(
      <ConvertLeadDialog
        open
        lead={createMockLead()}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        error={{
          response: {
            status: 400,
            data: {
              code: 'VALIDATION_ERROR',
              message,
              details: { field: 'status', status: 'new', allowed_statuses: ['qualified'] },
            },
          },
        }}
      />,
    );
    expect(screen.getByTestId('convert-lead-error')).toHaveTextContent(message);
  });

  it('falls back to a generic message when the server gives none', () => {
    render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={vi.fn()} error={new Error('Network Error')} />,
    );
    expect(screen.getByTestId('convert-lead-error')).toHaveTextContent('Failed to convert lead');
  });

  it('mentions owner assignment only when the setting is on', () => {
    const { unmount } = render(
      <ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={vi.fn()} autoAssignOwner />,
    );
    expect(screen.getByText(/lead's owner becomes the customer's assigned user/i)).toBeInTheDocument();
    unmount();

    render(<ConvertLeadDialog open lead={createMockLead()} onClose={vi.fn()} onConfirm={vi.fn()} />);
    expect(screen.queryByText(/lead's owner becomes the customer's assigned user/i)).not.toBeInTheDocument();
  });
});

describe('ConvertLeadDialog without known settings', () => {
  it.each([
    ['loading', 'Loading the conversion settings'],
    ['unavailable', 'Could not load the conversion settings'],
  ] as const)('claims no rule and does not convert while settings are %s', async (status, message) => {
    const onConfirm = vi.fn();
    render(
      <ConvertLeadDialog
        open
        lead={createMockLead()}
        onClose={vi.fn()}
        onConfirm={onConfirm}
        settingsStatus={status}
        requireNotes={false}
        autoAssignOwner
      />,
    );
    expect(screen.getByTestId('convert-lead-settings')).toHaveTextContent(message);
    expect(screen.getByRole('button', { name: 'Convert to Customer' })).toBeDisabled();
    // Neither "optional" notes nor an owner rule is claimed.
    expect(screen.queryByText('Additional Customer Information (Optional)')).not.toBeInTheDocument();
    expect(screen.queryByText(/lead's owner becomes/i)).not.toBeInTheDocument();
    expect(screen.getByLabelText(/Conversion Notes/)).not.toBeRequired();
  });

  it('retries loading the settings', () => {
    const onRetrySettings = vi.fn();
    render(
      <ConvertLeadDialog
        open
        lead={createMockLead()}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
        settingsStatus="unavailable"
        onRetrySettings={onRetrySettings}
      />,
    );
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(onRetrySettings).toHaveBeenCalledTimes(1);
  });

  it('starts from a fresh form for another lead', () => {
    const { rerender } = render(
      <ConvertLeadDialog open lead={createMockLead({ id: 1 })} onClose={vi.fn()} onConfirm={vi.fn()} />,
    );
    fireEvent.change(screen.getByLabelText(/Conversion Notes/), { target: { value: 'For lead one' } });
    rerender(
      <ConvertLeadDialog
        open
        lead={createMockLead({ id: 2, company_name: 'Other Co' })}
        onClose={vi.fn()}
        onConfirm={vi.fn()}
      />,
    );
    expect(screen.getByLabelText(/Conversion Notes/)).toHaveValue('');
    expect(screen.getByLabelText('Company Name')).toHaveValue('Other Co');
  });
});
