import { describe, expect, it } from 'vitest';
import { aeoExportErrorMessage, aeoExportFilename } from './aeoExport';

// An error shaped like the one axios rejects with for responseType 'blob':
// the JSON envelope arrives as a Blob.
const blobError = (status: number, body: string) =>
  Object.assign(new Error(`Request failed with status code ${status}`), {
    isAxiosError: true,
    response: { status, data: new Blob([body], { type: 'application/json' }) },
  });

const envelope = (code: string, details?: Record<string, unknown>) =>
  JSON.stringify({ success: false, error: { code, message: 'refused', details } });

describe('aeoExport', () => {
  it('builds the run and window filenames', () => {
    // 23:30 UTC on 14 Nov is already 15 Nov in a zone east of UTC: the name
    // must carry the UTC calendar date whatever the local zone.
    const now = new Date('2026-11-14T23:30:00Z');
    expect(aeoExportFilename({ run_id: 42 }, 'csv', now)).toBe('aeo-export-run-42.csv');
    expect(aeoExportFilename({ run_id: 42 }, 'json', now)).toBe('aeo-export-run-42.json');
    expect(aeoExportFilename({ days: 7 }, 'csv', now)).toBe('aeo-export-7d-2026-11-14.csv');
    expect(aeoExportFilename({ days: 90 }, 'json', now)).toBe('aeo-export-90d-2026-11-14.json');
    // Just after UTC midnight, still 14 Nov in a zone west of UTC.
    expect(aeoExportFilename({ days: 30 }, 'csv', new Date('2026-11-15T00:10:00Z'))).toBe(
      'aeo-export-30d-2026-11-15.csv',
    );
    expect(aeoExportFilename({ days: 30 }, 'json')).toMatch(
      /^aeo-export-30d-\d{4}-\d{2}-\d{2}\.json$/,
    );
  });

  it('reads the max_answers detail of a 422 blob error', async () => {
    await expect(
      aeoExportErrorMessage(blobError(422, envelope('EXPORT_TOO_LARGE', { max_answers: 10000 }))),
    ).resolves.toBe(
      'This export holds more than 10,000 answers. Choose a shorter window or a single run.',
    );
    await expect(
      aeoExportErrorMessage(blobError(422, envelope('EXPORT_TOO_LARGE'))),
    ).resolves.toBe('This export is too large. Choose a shorter window or a single run.');
    await expect(
      aeoExportErrorMessage(blobError(422, envelope('EXPORT_TOO_LARGE', { max_answers: '10000' }))),
    ).resolves.toBe('This export is too large. Choose a shorter window or a single run.');
    await expect(aeoExportErrorMessage(blobError(422, 'not json'))).resolves.toBe(
      'This export is too large. Choose a shorter window or a single run.',
    );
  });

  it('maps 400, 403, 404, 500 and unreadable errors to their messages', async () => {
    await expect(
      aeoExportErrorMessage(blobError(400, envelope('VALIDATION_ERROR', { days: 'invalid' }))),
    ).resolves.toBe('The export request was not accepted.');
    await expect(aeoExportErrorMessage(blobError(403, envelope('FORBIDDEN')))).resolves.toBe(
      'You do not have permission to export AEO results.',
    );
    await expect(aeoExportErrorMessage(blobError(404, envelope('NOT_FOUND')))).resolves.toBe(
      'This AEO run no longer exists.',
    );
    await expect(aeoExportErrorMessage(blobError(500, envelope('INTERNAL_ERROR')))).resolves.toBe(
      'Export failed. Please try again.',
    );
    await expect(aeoExportErrorMessage(blobError(502, '<html>bad gateway</html>'))).resolves.toBe(
      'Export failed. Please try again.',
    );
    // A network failure has no response at all.
    await expect(
      aeoExportErrorMessage(
        Object.assign(new Error('Network Error'), { isAxiosError: true, response: undefined }),
      ),
    ).resolves.toBe('Export failed. Please try again.');
    await expect(aeoExportErrorMessage(new Error('boom'))).resolves.toBe(
      'Export failed. Please try again.',
    );
  });
});
