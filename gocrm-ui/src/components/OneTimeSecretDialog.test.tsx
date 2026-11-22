import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@/test/test-utils';
import { OneTimeSecretDialog } from './OneTimeSecretDialog';

const SECRET = 'whsec_MfKQ9r8GKYqrTwjUPD8ILPZIo2LaLaSw';
const COPIED = 'Signing secret copied to clipboard';

const storageDump = () => JSON.stringify([{ ...localStorage }, { ...sessionStorage }]);

const renderDialog = (secret: string | null, onClose = vi.fn()) =>
  render(
    <OneTimeSecretDialog
      title="Signing secret"
      label="Signing secret"
      copyLabel="Copy signing secret"
      copiedMessage={COPIED}
      warning="Copy this secret now. It cannot be shown again."
      secret={secret}
      onClose={onClose}
      testId="webhook-secret"
    />,
  );

describe('OneTimeSecretDialog', () => {
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

  it('shows the title, the read-only value and the warning', () => {
    renderDialog(SECRET);

    expect(screen.getByRole('dialog', { name: 'Signing secret' })).toBeInTheDocument();
    expect(screen.getByTestId('webhook-secret')).toHaveValue(SECRET);
    expect(screen.getByTestId('webhook-secret')).toHaveAttribute('readonly');
    expect(screen.getByText(/cannot be shown again/i)).toBeInTheDocument();
  });

  it('copies the value to the clipboard and writes nothing to browser storage', async () => {
    renderDialog(SECRET);

    fireEvent.click(screen.getByRole('button', { name: 'Copy signing secret' }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith(SECRET));
    expect(await screen.findByText(COPIED)).toBeInTheDocument();
    expect(storageDump()).not.toContain('whsec_');
  });

  it('says so when the clipboard refuses', async () => {
    writeText.mockRejectedValue(new Error('denied'));
    renderDialog(SECRET);

    fireEvent.click(screen.getByRole('button', { name: 'Copy signing secret' }));

    expect(await screen.findByText(/select and copy it manually/i)).toBeInTheDocument();
    expect(screen.queryByText(COPIED)).not.toBeInTheDocument();
  });

  it('shows an error instead of success when the browser has no clipboard', async () => {
    vi.stubGlobal('navigator', { ...navigator, clipboard: undefined });
    renderDialog(SECRET);

    fireEvent.click(screen.getByRole('button', { name: 'Copy signing secret' }));

    expect(await screen.findByText(/select and copy it manually/i)).toBeInTheDocument();
    expect(screen.queryByText(COPIED)).not.toBeInTheDocument();
    expect(writeText).not.toHaveBeenCalled();
  });

  it('stays open on a click outside it', () => {
    const onClose = vi.fn();
    renderDialog(SECRET, onClose);

    const backdrop = document.querySelector('.MuiBackdrop-root');
    expect(backdrop).not.toBeNull();
    fireEvent.mouseDown(backdrop as Element);
    fireEvent.click(backdrop as Element);

    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByTestId('webhook-secret')).toHaveValue(SECRET);
  });

  it('stays open on Escape', () => {
    const onClose = vi.fn();
    renderDialog(SECRET, onClose);

    fireEvent.keyDown(screen.getByRole('dialog', { name: 'Signing secret' }), {
      key: 'Escape',
      code: 'Escape',
    });

    expect(onClose).not.toHaveBeenCalled();
    expect(screen.getByTestId('webhook-secret')).toHaveValue(SECRET);
  });

  it('asks to close once on Done', () => {
    const onClose = vi.fn();
    renderDialog(SECRET, onClose);

    fireEvent.click(screen.getByRole('button', { name: 'Done' }));
    expect(onClose).toHaveBeenCalledTimes(1);
  });

  it('renders nothing without a value', () => {
    renderDialog(null);

    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.queryByTestId('webhook-secret')).not.toBeInTheDocument();
    expect(document.body.innerHTML).not.toContain('whsec_');
  });
});
