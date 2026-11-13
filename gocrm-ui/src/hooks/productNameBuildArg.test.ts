import { readFileSync } from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { FALLBACK_PRODUCT_NAME, buildTimeProductName } from './useProductName';

// Issue #303: the Docker UI image accepts an optional VITE_APP_NAME build
// argument. No repository test builds the image, so the Dockerfile is checked
// statically; the helper tests pin what the empty default and a passed value
// resolve to; the documentation tests keep the retired "not shipped" wording out.

const uiRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '..', '..');
const repoRoot = path.resolve(uiRoot, '..');

const readUi = (rel: string): string => readFileSync(path.join(uiRoot, rel), 'utf8');
const readRepo = (rel: string): string => readFileSync(path.join(repoRoot, rel), 'utf8');
const collapse = (text: string): string => text.replace(/\s+/g, ' ');

const API_ARG = 'ARG VITE_API_BASE_URL=/api/v1';
const API_ENV = 'ENV VITE_API_BASE_URL=$VITE_API_BASE_URL';
const NAME_ARG = 'ARG VITE_APP_NAME=';
const NAME_ENV = 'ENV VITE_APP_NAME=$VITE_APP_NAME';
const BUILD = 'RUN npm run build';

/** The Dockerfile split into its build stage and its final (nginx) stage, as trimmed lines. */
function dockerStages(): { build: string[]; final: string[] } {
  const lines = readUi('Dockerfile')
    .split('\n')
    .map((line) => line.trim());
  const froms = lines.flatMap((line, index) => (/^FROM\s/i.test(line) ? [index] : []));
  expect(froms).toHaveLength(2);
  expect(lines[froms[0]]).toBe('FROM node:22-alpine AS build');
  return { build: lines.slice(froms[0], froms[1]), final: lines.slice(froms[1]) };
}

/** The index of the single line equal to `wanted`; fails on none or several. */
function onlyIndex(lines: string[], wanted: string): number {
  const hits = lines.flatMap((line, index) => (line === wanted ? [index] : []));
  expect(hits, wanted).toHaveLength(1);
  return hits[0];
}

describe('UI Dockerfile VITE_APP_NAME build argument', () => {
  it('declares ARG VITE_APP_NAME with an empty default in the build stage before npm run build', () => {
    const { build, final } = dockerStages();
    const copy = onlyIndex(build, 'COPY . .');
    const arg = onlyIndex(build, NAME_ARG);
    const run = onlyIndex(build, BUILD);
    expect(copy).toBeLessThan(arg);
    expect(arg).toBeLessThan(run);
    expect(final.some((line) => /VITE_APP_NAME/.test(line))).toBe(false);
  });

  it('exports ENV VITE_APP_NAME from the argument before npm run build', () => {
    const { build, final } = dockerStages();
    const arg = onlyIndex(build, NAME_ARG);
    const env = onlyIndex(build, NAME_ENV);
    const run = onlyIndex(build, BUILD);
    expect(arg).toBeLessThan(env);
    expect(env).toBeLessThan(run);
    expect(final).not.toContain(NAME_ENV);
  });

  it('keeps VITE_API_BASE_URL and the .env exclusion in .dockerignore', () => {
    const { build } = dockerStages();
    const arg = onlyIndex(build, API_ARG);
    const env = onlyIndex(build, API_ENV);
    const run = onlyIndex(build, BUILD);
    expect(arg).toBeLessThan(env);
    expect(env).toBeLessThan(run);

    const ignored = readUi('.dockerignore')
      .split('\n')
      .map((line) => line.trim());
    expect(ignored).toContain('.env');
    expect(ignored).toContain('.env.*');
  });

  it('names VITE_APP_NAME in the header comment', () => {
    const lines = readUi('Dockerfile').split('\n');
    const header: string[] = [];
    for (const line of lines) {
      if (!line.startsWith('#')) break;
      header.push(line);
    }
    const text = collapse(header.join(' '));
    expect(text).toContain('VITE_APP_NAME');
    expect(text).toContain('VITE_API_BASE_URL');
    expect(text).toMatch(/inlined by Vite/);
  });
});

describe('buildTimeProductName with the image default', () => {
  afterEach(() => {
    vi.unstubAllEnvs();
  });

  it('returns GoCRM when VITE_APP_NAME is the empty image default', () => {
    vi.stubEnv('VITE_APP_NAME', '');
    expect(buildTimeProductName()).toBe('GoCRM');
    expect(buildTimeProductName()).toBe(FALLBACK_PRODUCT_NAME);
  });

  it('returns the name passed as the build argument', () => {
    vi.stubEnv('VITE_APP_NAME', 'Acme CRM');
    expect(buildTimeProductName()).toBe('Acme CRM');
  });
});

describe('VITE_APP_NAME build argument documentation', () => {
  it('DOCKER.md documents docker build --build-arg VITE_APP_NAME, explains the public bundle and drops the retired wording', () => {
    const text = collapse(readRepo('docs/DOCKER.md'));
    for (const phrase of ['docker build --build-arg VITE_APP_NAME=', '#308', 'not a secret', 'JavaScript']) {
      expect(text, phrase).toContain(phrase);
    }
    for (const stale of [
      'is the only build argument',
      'not shipped',
      'never reaches the UI image',
      'add `ARG VITE_APP_NAME`',
      'docker history',
    ]) {
      expect(text, stale).not.toContain(stale);
    }
  });

  it('FORKING.md no longer says the Docker UI image ignores VITE_APP_NAME or bakes nothing else', () => {
    const text = collapse(readRepo('docs/FORKING.md'));
    expect(text).toContain('--build-arg VITE_APP_NAME');
    for (const stale of [
      'ignores `VITE_APP_NAME`',
      '(not shipped; see [DOCKER.md](DOCKER.md))',
      'and nothing else (`gocrm-ui/Dockerfile:16-17`)',
    ]) {
      expect(text, stale).not.toContain(stale);
    }
  });

  it('FORKING.md cites the Dockerfile lines that hold both build arguments', () => {
    const forking = readRepo('docs/FORKING.md');
    const citations = [...forking.matchAll(/gocrm-ui\/Dockerfile:(\d+)-(\d+)/g)];
    expect(citations.length).toBeGreaterThan(0);

    const dockerfile = readUi('Dockerfile').split('\n');
    for (const citation of citations) {
      const from = Number(citation[1]);
      const to = Number(citation[2]);
      expect(from, citation[0]).toBeGreaterThanOrEqual(1);
      expect(to, citation[0]).toBeLessThanOrEqual(dockerfile.length);
      const cited = dockerfile
        .slice(from - 1, to)
        .map((line) => line.trim())
        .filter((line) => line !== '');
      expect([...cited].sort(), citation[0]).toEqual([API_ARG, API_ENV, NAME_ARG, NAME_ENV].sort());
    }
  });

  it('USE_CASES.md drops the missing build argument and cites #308 for Compose build.args', () => {
    const text = collapse(readRepo('docs/USE_CASES.md'));
    expect(text).not.toContain('takes no `VITE_APP_NAME` build argument');
    expect(text).not.toContain('build argument (no issue yet)');
    expect(text).toContain('build.args');
    expect(text).toContain('#308');
  });

  it('ROADMAP.md tracks the Compose build.args pass-through under #308', () => {
    const text = collapse(readRepo('docs/ROADMAP.md'));
    expect(text).toContain('build.args');
    expect(text).toContain('VITE_APP_NAME');
    expect(text).toContain('#308');
  });

  it('CHANGELOG.md Unreleased records the build argument, cites #308 and keeps the #231 entry', () => {
    const changelog = readRepo('CHANGELOG.md');
    const start = changelog.indexOf('## [Unreleased]');
    expect(start).toBeGreaterThanOrEqual(0);
    const next = changelog.indexOf('\n## [', start + 1);
    const unreleased = collapse(changelog.slice(start, next === -1 ? undefined : next));
    for (const phrase of ['#303', 'VITE_APP_NAME', '#308', '#231', 'SERVER_PROVISIONING.md']) {
      expect(unreleased, phrase).toContain(phrase);
    }
    expect(unreleased).not.toContain('build argument remain open');
  });

  it('the edited documents keep the stale #222 phrases out', () => {
    for (const rel of ['docs/DOCKER.md', 'docs/FORKING.md', 'docs/USE_CASES.md', 'CHANGELOG.md']) {
      const text = collapse(readRepo(rel));
      for (const stale of [
        'build argument is tracked in #222',
        'build argument (#222)',
        'production override is tracked in #222',
        'A shipped production override is #222',
        'until #222 ships one',
      ]) {
        expect(text, `${rel}: ${stale}`).not.toContain(stale);
      }
    }
  });
});

describe('VITE_APP_NAME signed-out browser spec', () => {
  it('visits /login and /register and expects the loadEnv-resolved name, else GoCRM, without toggling registration', () => {
    const spec = readUi('e2e/tests/product-name-signed-out.spec.ts');
    for (const phrase of [
      '/login',
      '/register',
      'Welcome to ',
      'Create your ',
      'loadEnv',
      "loadEnv('development', process.cwd(), 'VITE_')",
      'VITE_APP_NAME',
      'GoCRM',
    ]) {
      expect(spec, phrase).toContain(phrase);
    }
    expect(spec).not.toContain('setPublicRegistration');
  });
});
