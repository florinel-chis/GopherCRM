import { isAxiosError } from 'axios';
import type { AEOExportFormat, AEOExportSelection } from '@/api/endpoints/aeo';

export const AEO_EXPORT_GENERIC_ERROR = 'Export failed. Please try again.';

// The server names the attachment too (Content-Disposition), but CORS does not
// expose that header to the web app, so the client builds the same pattern:
// aeo-export-run-<id>.<ext> for a run, aeo-export-<days>d-<YYYY-MM-DD>.<ext>
// for a window, the date being the UTC calendar date of `now`.
export const aeoExportFilename = (
  selection: AEOExportSelection,
  format: AEOExportFormat,
  now: Date = new Date(),
): string => {
  if (selection.run_id !== undefined) {
    return `aeo-export-run-${selection.run_id}.${format}`;
  }
  const day = now.toISOString().slice(0, 10);
  return `aeo-export-${selection.days}d-${day}.${format}`;
};

const readBlobText = (blob: Blob): Promise<string> => {
  if (typeof blob.text === 'function') {
    return blob.text();
  }
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.onload = () => resolve(typeof reader.result === 'string' ? reader.result : '');
    reader.onerror = () => reject(reader.error);
    reader.readAsText(blob);
  });
};

interface ExportErrorEnvelope {
  error?: { code?: string; details?: { max_answers?: unknown } };
}

// With responseType 'blob' the JSON error envelope arrives as a Blob, which
// the shared interceptor does not parse; read it here. An unreadable body
// still maps by status.
const readEnvelope = async (data: unknown): Promise<ExportErrorEnvelope | null> => {
  try {
    if (typeof Blob !== 'undefined' && data instanceof Blob) {
      const parsed: unknown = JSON.parse(await readBlobText(data));
      return parsed && typeof parsed === 'object' ? (parsed as ExportErrorEnvelope) : null;
    }
    if (data && typeof data === 'object') {
      return data as ExportErrorEnvelope;
    }
  } catch {
    return null;
  }
  return null;
};

export const aeoExportErrorMessage = async (error: unknown): Promise<string> => {
  if (!isAxiosError(error) || !error.response) {
    return AEO_EXPORT_GENERIC_ERROR;
  }
  const { status, data } = error.response;
  switch (status) {
    case 422: {
      const envelope = await readEnvelope(data);
      const max = envelope?.error?.details?.max_answers;
      if (typeof max === 'number' && Number.isFinite(max)) {
        return `This export holds more than ${max.toLocaleString('en-US')} answers. Choose a shorter window or a single run.`;
      }
      return 'This export is too large. Choose a shorter window or a single run.';
    }
    case 400:
      return 'The export request was not accepted.';
    case 403:
      return 'You do not have permission to export AEO results.';
    case 404:
      return 'This AEO run no longer exists.';
    default:
      return AEO_EXPORT_GENERIC_ERROR;
  }
};

// Saves a downloaded Blob under `filename`. Firefox ignores a click on a
// detached anchor, and revoking the URL in the same tick can cancel the
// download, so the anchor is attached and the URL revoked on a later tick.
export const saveAEOExport = (blob: Blob, filename: string): void => {
  const url = URL.createObjectURL(blob);
  const anchor = document.createElement('a');
  anchor.href = url;
  anchor.download = filename;
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  setTimeout(() => URL.revokeObjectURL(url), 0);
};

// Only staff reach the AEO pages; the controls are hidden from anyone else.
export const canExportAEO = (role: string | undefined): boolean =>
  role === 'admin' || role === 'sales' || role === 'support';
