import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

// Issue #320: scheduled or emailed AEO client reports are recorded as a deliberate cut in
// docs/reference/roadmap.md, with three prerequisites. These documentation tests pin the cut bullet, the
// trimmed follow-ups bullet, the use-cases.md gap references and the CHANGELOG entry.

const repoRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..', '..', '..');

const readRepo = (rel: string): string => readFileSync(path.join(repoRoot, rel), 'utf8');
const collapse = (text: string): string => text.replace(/\s+/g, ' ');

const CUT_PREFIX = '- **AEO scheduled client reports: deliberate cut**';
const FOLLOW_UPS_PREFIX = '- **AEO reporting: follow-ups**';

/** Indexes of the lines that start with `prefix`. */
function linesStartingWith(lines: string[], prefix: string): number[] {
  return lines.flatMap((line, index) => (line.startsWith(prefix) ? [index] : []));
}

/** The bullet starting at `start`, up to the next line starting `- ` or `## `. */
function bulletAt(lines: string[], start: number): string {
  let end = start + 1;
  while (end < lines.length && !lines[end].startsWith('- ') && !lines[end].startsWith('## ')) {
    end += 1;
  }
  return collapse(lines.slice(start, end).join('\n'));
}

/** The single bullet starting with `prefix`; fails on none or several. */
function onlyBullet(lines: string[], prefix: string): string {
  const hits = linesStartingWith(lines, prefix);
  expect(hits, prefix).toHaveLength(1);
  return bulletAt(lines, hits[0]);
}

function occurrences(text: string, needle: string): number {
  return text.split(needle).length - 1;
}

describe('AEO scheduled client reports deliberate cut (#320)', () => {
  it('roadmap.md has one cut bullet under Backend and delivery after the AEO reporting bullet', () => {
    const lines = readRepo('docs/reference/roadmap.md').split('\n');
    const cut = linesStartingWith(lines, CUT_PREFIX);
    expect(cut).toHaveLength(1);

    const section = linesStartingWith(lines, '## Backend and delivery');
    expect(section).toHaveLength(1);
    const nextHeading = lines.findIndex(
      (line, index) => index > section[0] && line.startsWith('## '),
    );
    expect(nextHeading).toBeGreaterThan(section[0]);

    const followUps = linesStartingWith(lines, FOLLOW_UPS_PREFIX);
    expect(followUps).toHaveLength(1);
    expect(followUps[0]).toBeGreaterThan(section[0]);
    expect(followUps[0]).toBeLessThan(cut[0]);
    expect(cut[0]).toBeLessThan(nextHeading);

    // No heading of its own: no heading line mentions the cut.
    const headings = lines.filter((line) => line.startsWith('#'));
    expect(headings.some((line) => /scheduled client reports/i.test(line))).toBe(false);
  });

  it('the cut bullet names the three prerequisites with #229, #216 and #238', () => {
    const lines = readRepo('docs/reference/roadmap.md').split('\n');
    const bullet = onlyBullet(lines, CUT_PREFIX);
    for (const phrase of [
      '#320',
      'not an accident',
      'GET /aeo/export',
      '#229',
      'schedule_slot',
      '#216',
      'log_only',
      '#238',
      'consent',
      'opt-out',
    ]) {
      expect(bullet, phrase).toContain(phrase);
    }
    const one = bullet.indexOf('(1)');
    const two = bullet.indexOf('(2)');
    const three = bullet.indexOf('(3)');
    expect(one).toBeGreaterThanOrEqual(0);
    expect(two).toBeGreaterThan(one);
    expect(three).toBeGreaterThan(two);
    // Each prerequisite carries its own issue reference.
    expect(bullet.slice(one, two)).toContain('#229');
    expect(bullet.slice(two, three)).toContain('#216');
    expect(bullet.slice(three)).toContain('#238');
  });

  it('the AEO reporting follow-ups bullet no longer lists scheduled or emailed reports as still open', () => {
    const lines = readRepo('docs/reference/roadmap.md').split('\n');
    const bullet = onlyBullet(lines, FOLLOW_UPS_PREFIX);
    for (const stale of ['scheduled, emailed', 'scheduled client reports']) {
      expect(bullet, stale).not.toContain(stale);
    }
    for (const phrase of [
      'threshold alerts',
      'PDF client reports',
      'export cap',
      'scripts/ci/check-api-docs.py',
      'see the deliberate cut below',
    ]) {
      expect(bullet, phrase).toContain(phrase);
    }
  });

  it('use-cases.md points the scheduled report gap to the roadmap cut instead of #244', () => {
    const raw = readRepo('docs/reference/use-cases.md');
    const text = collapse(raw);
    for (const stale of ['client reports remain a gap: #244', 'no scheduled client report: #244']) {
      expect(text, stale).not.toContain(stale);
    }
    const sentences = [
      'Threshold alerts remain a gap; scheduled or emailed client reports are a deliberate cut ' +
        '(#320, docs/reference/roadmap.md).',
      'there is no threshold alerting, and scheduled client reports are a deliberate cut ' +
        '(#320, docs/reference/roadmap.md).',
      'Still open: threshold alerting; scheduled or emailed client reports are a deliberate cut ' +
        '(#320, docs/reference/roadmap.md);',
    ];
    for (const sentence of sentences) {
      expect(occurrences(text, sentence), sentence).toBe(1);
    }

    const rows = raw.split('\n').filter((line) => line.includes(sentences[2]));
    expect(rows).toHaveLength(1);
    expect(rows[0].startsWith('|')).toBe(true);
    expect(rows[0]).toContain('#244, #318, #320');
  });

  it('CHANGELOG.md records the cut under Unreleased', () => {
    const changelog = readRepo('CHANGELOG.md');
    const start = changelog.indexOf('## [Unreleased]');
    expect(start).toBeGreaterThanOrEqual(0);
    // Entries since 1.2.0: under Unreleased or a newer version.
    const next = changelog.indexOf('\n## [1.2.0]', start + 1);
    const lines = changelog.slice(start, next === -1 ? undefined : next).split('\n');

    const bulletStarts = linesStartingWith(lines, '- ');
    const cutBullets = bulletStarts
      .map((index) => bulletAt(lines, index))
      .filter(
        (bullet) =>
          bullet.includes('#320') &&
          bullet.includes('deliberate cut') &&
          bullet.includes('scheduled'),
      );
    expect(cutBullets.length).toBeGreaterThanOrEqual(1);

    // The historical #244 entry stays as written.
    const historical = linesStartingWith(lines, '- **AEO results export (#244).**');
    expect(historical).toHaveLength(1);
    let end = historical[0] + 1;
    while (end < lines.length && !lines[end].startsWith('- ') && !lines[end].startsWith('### ')) {
      end += 1;
    }
    const entry = collapse(lines.slice(historical[0], end).join('\n'));
    expect(entry).toContain('Still open: threshold alerts and scheduled client reports.');
  });
});
