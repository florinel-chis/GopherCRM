import { Page, Route } from '@playwright/test';
import type {
  AEOAnswer,
  AEOCitation,
  AEOCitationDomainStat,
  AEOCitationsReport,
  AEODashboard,
  AEOProfile,
  AEOPrompt,
  AEOProviderStatus,
  AEORun,
} from '../../../src/api/endpoints/aeo';

/**
 * Mock AEO data for the documentation captures.
 *
 * A real AEO run spends provider credit, takes minutes and photographs whatever
 * brand happens to be configured, so the AEO captures are taken against this
 * simulated history instead: GopherCRM tracking its own visibility against
 * fictional competitors on reserved `.example` domains, one scheduled run a
 * day for 30 days on a single self-hosted engine.
 *
 * Only the answers are invented (deterministically, so re-running the suite
 * gives the same images apart from dates, which are relative to the capture
 * day). Every figure the pages show is aggregated from those answers with the
 * backend's own rules (internal/service/aeo_service.go), so the dashboard,
 * prompt list, drawer and citations page agree with each other:
 *   - rates are percentages in 0..100 with one decimal;
 *   - visibility = answers mentioning a company / answers;
 *   - share of voice = a company's mention events / all mention events;
 *   - citation rates = citations to a company or domain / all citations;
 *   - every day in the window gets a timeline point, zero when nothing ran.
 */

const BRAND = 'GopherCRM';
const OWNED_DOMAIN = 'gophercrm.example';
const COMPETITORS = [
  { name: 'Northwind CRM', aliases: ['Northwind'], domain: 'northwind-crm.example' },
  { name: 'Contoso Sales', aliases: [], domain: 'contoso-sales.example' },
  { name: 'Fabrikam Desk', aliases: ['Fabrikam'], domain: 'fabrikam-desk.example' },
];
/** A cited domain that belongs to nobody tracked, as real answers have. */
const REVIEW_DOMAIN = 'crm-reviews.example';
const ENGINE = { provider: 'gpt-oss', model: 'openai/gpt-oss-20b' };
const RUN_DAYS = 30;
/** Prompt 5 was switched off ten days ago; runs since then skip it. */
const INACTIVE_PROMPT_ID = 5;
const INACTIVE_SINCE_DAYS = 10;

const DAY_MS = 24 * 60 * 60 * 1000;
const now = new Date();
/** Run `age` days back, finished two hours before the capture (never in the future). */
const runTime = (age: number) => new Date(now.getTime() - age * DAY_MS - 2 * 60 * 60 * 1000);
const isoDay = (date: Date) => date.toISOString().slice(0, 10);
const dayAgo = (age: number) => isoDay(new Date(now.getTime() - age * DAY_MS));

/** Deterministic value in [0, 1) for a seed, so the history is the same every run. */
const unit = (seed: number) => {
  const x = Math.sin(seed * 12.9898 + 78.233) * 43758.5453;
  return x - Math.floor(x);
};
/** A ratio as the API reports it: a percentage with one decimal. */
const pct = (part: number, whole: number) => (whole > 0 ? Math.round((part / whole) * 1000) / 10 : 0);

const profile: AEOProfile = {
  id: 1,
  brand_name: BRAND,
  description: 'Open-source CRM written in Go, with a React front end: leads, customers, tickets, tasks and embeddable web forms.',
  brand_aliases: ['Gopher CRM'],
  owned_domains: [OWNED_DOMAIN],
  competitors: COMPETITORS,
};

const providers: AEOProviderStatus[] = [
  { name: 'anthropic', model: 'claude-opus-5', configured: false },
  { name: 'openai', model: 'gpt-4o-mini', configured: false },
  { name: 'gemini', model: 'gemini-flash-latest', configured: false },
  { name: 'kimi', model: 'moonshot-v1-8k', configured: false },
  { name: 'perplexity', model: 'sonar', configured: false },
  { name: ENGINE.provider, model: ENGINE.model, configured: true },
];

/** Prompt texts with how likely an answer is to mention the brand (rising over the month). */
const promptSpecs = [
  { text: 'What is a good open-source CRM written in Go?', brand: 0.9 },
  { text: 'Which self-hosted CRM has built-in support ticketing?', brand: 0.6 },
  { text: 'Best CRM with embeddable web forms and double opt-in', brand: 0.55 },
  { text: 'Lightweight CRM for a small agency with GDPR erasure', brand: 0.75 },
  { text: 'Open-source alternatives to hosted sales CRMs', brand: 0.35 },
  { text: 'CRM with an API-key authenticated REST API', brand: 0.5 },
];
/** How likely each competitor is to come up in any answer. */
const competitorOdds: Record<string, number> = {
  'Northwind CRM': 0.5,
  'Contoso Sales': 0.3,
  'Fabrikam Desk': 0.22,
};

// --- the simulated history ----------------------------------------------------

interface SimAnswer {
  answer: AEOAnswer;
  age: number;
}

const runs: AEORun[] = [];
const history: SimAnswer[] = [];
let answerId = 0;
let citationId = 0;

for (let age = RUN_DAYS - 1; age >= 0; age--) {
  const runId = RUN_DAYS - age;
  const finished = runTime(age);
  const promptIds = promptSpecs
    .map((_, index) => index + 1)
    .filter((id) => id !== INACTIVE_PROMPT_ID || age >= INACTIVE_SINCE_DAYS);
  runs.push({
    id: runId,
    trigger: age === 0 ? 'manual' : 'scheduled',
    status: 'completed',
    started_at: new Date(finished.getTime() - 4 * 60 * 1000).toISOString(),
    completed_at: finished.toISOString(),
    total_queries: promptIds.length,
    failed_queries: 0,
  });

  // Visibility improves over the month: the brand odds rise by up to 25 points.
  const trend = 0.25 * (1 - age / RUN_DAYS);
  for (const promptId of promptIds) {
    const seed = runId * 100 + promptId;
    const mentionsBrand = unit(seed) < promptSpecs[promptId - 1].brand + trend;
    const mentioned = COMPETITORS.filter((c, n) => unit(seed * 7 + n + 1) < competitorOdds[c.name]);
    history.push({ age, answer: buildAnswer(++answerId, runId, promptId, finished, seed, mentionsBrand, mentioned) });
  }
}

function buildAnswer(
  id: number,
  runId: number,
  promptId: number,
  createdAt: Date,
  seed: number,
  mentionsBrand: boolean,
  mentioned: typeof COMPETITORS
): AEOAnswer {
  const cited: string[] = [];
  const paragraphs: string[] = [];
  if (mentionsBrand) {
    paragraphs.push(
      `**${BRAND}** is a strong fit: an open-source CRM with a Go backend and a React front end that covers leads, customers, tickets and tasks, and runs as a single binary against MySQL or MariaDB.`
    );
    if (unit(seed * 3) < 0.55) cited.push(`https://${OWNED_DOMAIN}/docs`);
  }
  if (mentioned.length > 0) {
    const items = mentioned.map((c) => `- **${c.name}**: ${competitorBlurb(c.name)}`);
    paragraphs.push((mentionsBrand ? 'Other options people compare it with:' : 'Commonly recommended options:') + '\n\n' + items.join('\n'));
    for (const [n, c] of mentioned.entries()) {
      if (unit(seed * 5 + n) < 0.4) cited.push(`https://${c.domain}/features`);
    }
  }
  if (!mentionsBrand && mentioned.length === 0) {
    paragraphs.push('It depends on team size and whether you need to self-host; most teams shortlist two or three products and trial them against a real pipeline.');
  }
  if (unit(seed * 11) < 0.2) cited.push(`https://${REVIEW_DOMAIN}/comparisons`);
  if (cited.length > 0) paragraphs.push(`Sources: ${cited.join(', ')}`);

  const text = paragraphs.join('\n\n');
  const citations: AEOCitation[] = cited.map((url) => {
    const domain = new URL(url).hostname;
    return {
      id: ++citationId,
      answer_id: id,
      url,
      domain,
      is_owned: domain === OWNED_DOMAIN,
      competitor_name: COMPETITORS.find((c) => c.domain === domain)?.name,
    };
  });
  return {
    id,
    run_id: runId,
    prompt_id: promptId,
    provider: ENGINE.provider,
    model: ENGINE.model,
    attempt: 1,
    answer_text: text,
    brand_mentioned: mentionsBrand,
    first_mention_pos: mentionsBrand ? text.indexOf(BRAND) : -1,
    competitor_mentions: Object.fromEntries(mentioned.map((c) => [c.name, 1])),
    latency_ms: 6000 + Math.round(unit(seed * 13) * 6000),
    citations,
    created_at: createdAt.toISOString(),
  };
}

function competitorBlurb(name: string): string {
  switch (name) {
    case 'Northwind CRM':
      return 'a hosted CRM with a strong sales pipeline, but not self-hostable.';
    case 'Contoso Sales':
      return 'a sales-first suite with forecasting and a large integration catalogue.';
    default:
      return 'focused on support ticketing rather than sales.';
  }
}

// --- aggregations (mirroring internal/service/aeo_service.go) ------------------

const inWindow = (days: number) => history.filter((h) => h.age < days).map((h) => h.answer);
const companies = [BRAND, ...COMPETITORS.map((c) => c.name)];
const mentionsCompany = (answer: AEOAnswer, company: string) =>
  company === BRAND ? answer.brand_mentioned : (answer.competitor_mentions[company] ?? 0) > 0;

function prompts(days: number): AEOPrompt[] {
  return promptSpecs.map((spec, index) => {
    const id = index + 1;
    const answers = inWindow(days).filter((a) => a.prompt_id === id);
    const mentions = answers.filter((a) => a.brand_mentioned).length;
    return {
      id,
      text: spec.text,
      is_active: id !== INACTIVE_PROMPT_ID,
      created_at: runTime(RUN_DAYS + 2).toISOString(),
      visibility: pct(mentions, answers.length),
      answer_count: answers.length,
      mention_count: mentions,
      last_run_at: history.filter((h) => h.answer.prompt_id === id).map((h) => h.answer.created_at).pop(),
    };
  });
}

function promptAnswers(promptId: number, runId: number | null): AEOAnswer[] {
  return history
    .map((h) => h.answer)
    .filter((a) => a.prompt_id === promptId && (runId === null || a.run_id === runId))
    .reverse()
    .slice(0, 50);
}

function dashboard(days: number): AEODashboard {
  const answers = inWindow(days);
  const byCompany = Object.fromEntries(
    companies.map((company) => [company, answers.filter((a) => mentionsCompany(a, company)).length])
  );
  const mentionEvents = companies.reduce((sum, company) => sum + byCompany[company], 0);

  const timeline: AEODashboard['timeline'] = [];
  const competitorTimeline: AEODashboard['competitor_timeline'] = [];
  for (let age = days - 1; age >= 0; age--) {
    const day = dayAgo(age);
    const dayAnswers = answers.filter((a) => isoDay(new Date(a.created_at)) === day);
    const overall = pct(dayAnswers.filter((a) => a.brand_mentioned).length, dayAnswers.length);
    timeline.push({
      day,
      overall,
      by_provider: dayAnswers.length > 0 ? { [ENGINE.provider]: overall } : {},
    });
    competitorTimeline.push({
      day,
      by_company:
        dayAnswers.length > 0
          ? Object.fromEntries(
              companies.map((company) => [
                company,
                pct(dayAnswers.filter((a) => mentionsCompany(a, company)).length, dayAnswers.length),
              ])
            )
          : {},
    });
  }

  return {
    from: dayAgo(days - 1),
    to: dayAgo(0),
    days,
    total_answers: answers.length,
    failed_answers: 0,
    brand_mentions: byCompany[BRAND],
    visibility: pct(byCompany[BRAND], answers.length),
    by_provider: [
      {
        provider: ENGINE.provider,
        answers: answers.length,
        mentions: byCompany[BRAND],
        visibility: pct(byCompany[BRAND], answers.length),
      },
    ],
    timeline,
    share_of_voice: companies.map((company) => ({
      company,
      is_brand: company === BRAND,
      mentions: byCompany[company],
      share: pct(byCompany[company], mentionEvents),
      visibility: pct(byCompany[company], answers.length),
    })),
    competitor_timeline: competitorTimeline,
    last_run_at: runs[runs.length - 1].completed_at,
  };
}

function citationsReport(days: number): AEOCitationsReport {
  const answers = inWindow(days);
  const rows = new Map<string, { citations: number; withBrand: number; owned: boolean; company: string }>();
  for (const answer of answers) {
    for (const citation of answer.citations) {
      const row = rows.get(citation.domain) ?? {
        citations: 0,
        withBrand: 0,
        owned: citation.is_owned,
        company: citation.is_owned ? BRAND : (citation.competitor_name ?? ''),
      };
      row.citations++;
      if (answer.brand_mentioned) row.withBrand++;
      rows.set(citation.domain, row);
    }
  }
  const total = [...rows.values()].reduce((sum, row) => sum + row.citations, 0);
  const owned = [...rows.values()].filter((row) => row.owned).reduce((sum, row) => sum + row.citations, 0);

  const topDomains: AEOCitationDomainStat[] = [...rows.entries()]
    .map(([domain, row]) => ({
      domain,
      company: row.company,
      is_owned: row.owned,
      citations: row.citations,
      citation_rate: pct(row.citations, total),
      with_brand_mention: row.withBrand,
      brand_mention_rate: pct(row.withBrand, row.citations),
    }))
    .sort((a, b) => b.citations - a.citations || a.domain.localeCompare(b.domain))
    .slice(0, 20);

  return {
    from: dayAgo(days - 1),
    to: dayAgo(0),
    total_answers: answers.length,
    total_citations: total,
    answers_with_citations: answers.filter((a) => a.citations.length > 0).length,
    owned_citation_rate: pct(owned, total),
    by_company: companies.map((company) => {
      const companyRows = [...rows.values()].filter((row) => row.company === company);
      const citations = companyRows.reduce((sum, row) => sum + row.citations, 0);
      const withBrand = companyRows.reduce((sum, row) => sum + row.withBrand, 0);
      return {
        company,
        is_brand: company === BRAND,
        citations,
        citation_rate: pct(citations, total),
        with_brand_mention: withBrand,
        brand_mention_rate: pct(withBrand, citations),
      };
    }),
    top_domains: topDomains,
  };
}

// --- routing -------------------------------------------------------------------

const ok = (route: Route, data: unknown) =>
  route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ success: true, data }),
  });

/**
 * Answers every AEO API call from the simulated history. Matching is on the
 * `/aeo/` path segment of XHR and fetch requests, whatever the API prefix, so
 * no AEO call can slip through to a real backend and photograph its brand;
 * page navigations to the SPA's own /aeo routes pass through untouched.
 */
export async function mockAeoApi(page: Page): Promise<void> {
  await page.route(/\/aeo(\/|$|\?)/, (route) => {
    const request = route.request();
    if (!['xhr', 'fetch'].includes(request.resourceType())) {
      return route.continue();
    }
    const url = new URL(request.url());
    const path = url.pathname.replace(/^.*\/aeo/, '');
    const days = Number(url.searchParams.get('days') ?? 30);

    if (request.method() !== 'GET') {
      // The captures never save or run anything; refuse loudly if one tries.
      return route.fulfill({ status: 405, body: 'screenshot fixtures are read-only' });
    }
    if (path === '/profile') return ok(route, profile);
    if (path === '/providers') return ok(route, providers);
    if (path === '/prompts') return ok(route, prompts(days));
    const answersMatch = path.match(/^\/prompts\/(\d+)\/answers$/);
    if (answersMatch) {
      const runId = url.searchParams.get('run_id');
      return ok(route, promptAnswers(Number(answersMatch[1]), runId ? Number(runId) : null));
    }
    if (path === '/runs') {
      const limit = Number(url.searchParams.get('limit') ?? 20);
      return ok(route, [...runs].reverse().slice(0, limit));
    }
    if (path === '/dashboard') return ok(route, dashboard(days));
    if (path === '/citations') return ok(route, citationsReport(days));
    return route.fulfill({ status: 404, body: `no screenshot fixture for ${path}` });
  });
}
