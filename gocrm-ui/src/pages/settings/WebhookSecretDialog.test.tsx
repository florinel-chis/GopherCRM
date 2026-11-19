import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@/test/test-utils';
import { WebhookSecretDialog } from './WebhookSecretDialog';

const SECRET = 'whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw';

const storageDump = () => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]);

describe('WebhookSecretDialog', () => {
  const writeText = vi.fn();

  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    sessionStorage.clear();
    writeText.mockResolvedValue(undefined);
    vi.stubGlobal('navigator', { ...navigator, clipboard: { writeText } });
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('shows the secret with the notice that it cannot be shown again', () => {
    render(<WebhookSecretDialog secret={SECRET} onClose={vi.fn()} />);

    const dialog = screen.getByRole('dialog', { name: 'Signing secret' });
    expect(dialog).toBeInTheDocument();
    expect(screen.getByTestId('webhook-secret')).toHaveValue(SECRET);
    expect(screen.getByTestId('webhook-secret')).toHaveAttribute('readonly');
    expect(screen.getByText(/cannot be shown again/i)).toBeInTheDocument();
  });

  it('copies the secret to the clipboard and writes nothing to browser storage', async () => {
    render(<WebhookSecretDialog secret={SECRET} onClose={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Copy signing secret' }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SECRET));
    expect(await screen.findByText('Signing secret copied to clipboard')).toBeInTheDocument();
    expect(storageDump()).not.toContain('whsec_');
  });

  it('says so when the clipboard refuses', async () => {
    writeText.mockRejectedValue(new Error('denied'));
    render(<WebhookSecretDialog secret={SECRET} onClose={vi.fn()} />);

    fireEvent.click(screen.getByRole('button', { name: 'Copy signing secret' }));

    expect(await screen.findByText(/select and copy it manually/i)).toBeInTheDocument();
  });

  it('asks to close on Done and renders nothing without a secret', () => {
    const onClose = vi.fn();
    const view = render(<WebhookSecretDialog secret={SECRET} onClose={onClose} />);

    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(onClose).toHaveBeenCalledTimes(1);

    view.unmount();
    render(<WebhookSecretDialog secret={null} onClose={onClose} />);
    expect(screen.queryByTestId('webhook-secret')).not.toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain('whsec_');
  });
});
