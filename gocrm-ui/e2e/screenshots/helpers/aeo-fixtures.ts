import { Page, Route } from '@playwright/test';

/**
 * Mock AEO data for the documentation captures.
 *
 * A real AEO run spends provider credit, takes minutes and photographs whatever
 * brand happens to be configured, so the AEO captures are taken against this
 * fixed data instead: GopherCRM tracking its own visibility against fictional
 * competitors on reserved `.example` domains. Every AEO API call the pages make
 * is answered here; everything else (login, navigation, configuration keys)
 * still goes to the real backend.
 *
 * Values come from a fixed formula, so re-running the suite produces the same
 * images apart from dates, which are relative to the capture day. Shapes and
 * units follow internal/models/aeo.go: every rate is a percentage in 0..100
 * with one decimal, and days and ranges are "YYYY-MM-DD".
 */

const BRAND = 'GopherCRM';
const COMPETITORS = [
  { name: 'Northwind CRM', aliases: ['Northwind'], domain: 'northwind-crm.example' },
  { name: 'Contoso Sales', aliases: [], domain: 'contoso-sales.example' },
  { name: 'Fabrikam Desk', aliases: ['Fabrikam'], domain: 'fabrikam-desk.example' },
];
const ENGINE = { provider: 'gpt-oss', model: 'openai/gpt-oss-20b' };

const DAY_MS = 24 * 60 * 60 * 1000;
const now = new Date();
const daysAgo = (days: number, hour = 6) => {
  const date = new Date(now.getTime() - days * DAY_MS);
  date.setUTCHours(hour, 0, 0, 0);
  return date;
};
const isoDay = (date: Date) => date.toISOString().slice(0, 10);

/** Deterministic 0..1 wobble so series look measured rather than flat. */
const wobble = (seed: number) => (Math.sin(seed * 12.9898) * 43758.5453) % 1;
/** A 0..1 ratio as the API reports it: a percentage with one decimal. */
const pct = (ratio: number) => Math.round(ratio * 1000) / 10;

const profile = {
  id: 1,
  brand_name: BRAND,
  description: 'Open-source CRM written in Go, with a React front end: leads, customers, tickets, tasks and embeddable web forms.',
  brand_aliases: ['Gopher CRM'],
  owned_domains: ['gophercrm.example'],
  competitors: COMPETITORS,
};

const providers = [
  { name: 'anthropic', model: 'claude-opus-5', configured: false },
  { name: 'openai', model: 'gpt-4o-mini', configured: false },
  { name: 'gemini', model: 'gemini-flash-latest', configured: false },
  { name: 'kimi', model: 'moonshot-v1-8k', configured: false },
  { name: 'perplexity', model: 'sonar', configured: false },
  { name: ENGINE.provider, model: ENGINE.model, configured: true },
];

const runs = [3, 2, 1].map((n) => ({
  id: n,
  trigger: n === 3 ? 'manual' : 'scheduled',
  status: 'completed',
  started_at: daysAgo(3 - n).toISOString(),
  completed_at: new Date(daysAgo(3 - n).getTime() + 4 * 60 * 1000).toISOString(),
  total_queries: 6,
  failed_queries: 0,
}));

const promptTexts = [
  'What is a good open-source CRM written in Go?',
  'Which self-hosted CRM has built-in support ticketing?',
  'Best CRM with embeddable web forms and double opt-in',
  'Lightweight CRM for a small agency with GDPR erasure',
  'Open-source alternatives to hosted sales CRMs',
  'CRM with an API-key authenticated REST API',
];
const promptVisibility = [1, 2 / 3, 2 / 3, 1, 1 / 3, 2 / 3];

const prompts = promptTexts.map((text, index) => ({
  id: index + 1,
  text,
  is_active: index !== 4,
  created_at: daysAgo(20).toISOString(),
  visibility: pct(promptVisibility[index]),
  answer_count: 3,
  mention_count: Math.round(promptVisibility[index] * 3),
  last_run_at: runs[0].completed_at,
}));

const answerTexts = [
  `For a CRM written in Go, **GopherCRM** is the most complete open-source option: a Gin and GORM backend with a React front end, covering leads, customers, tickets and tasks. It runs as a single binary against MySQL or MariaDB.

Other options people compare it with:

1. **Northwind CRM**: a hosted CRM with a strong sales pipeline, but not self-hostable.
2. **Fabrikam Desk**: focused on support ticketing rather than sales.

If you want to own your data and extend the code, GopherCRM is the one to start with (see gophercrm.example/docs).`,
  `Several CRMs fit that description. **Northwind CRM** and **Contoso Sales** are the best-known hosted products. For a self-hosted, Go-based option, **GopherCRM** covers the same core entities with role-based access and an API.

Sources: https://gophercrm.example/features, https://northwind-crm.example/pricing`,
  `Most Go developers looking for a CRM end up comparing a few projects. **GopherCRM** stands out for its layered architecture and test coverage, while **Fabrikam Desk** is lighter but ticket-only.

See https://gophercrm.example/docs/architecture for a walkthrough.`,
];

const answers = runs.map((run, index) => {
  const text = answerTexts[index];
  const mentioned = text.includes(BRAND);
  const citations = Array.from(text.matchAll(/https?:\/\/([a-z0-9.-]+)\S*/g)).map((match, n) => ({
    id: index * 10 + n + 1,
    answer_id: index + 1,
    url: match[0].replace(/[),.]+$/, ''),
    domain: match[1],
    is_owned: match[1] === 'gophercrm.example',
    competitor_name: COMPETITORS.find((c) => c.domain === match[1])?.name,
  }));
  return {
    id: index + 1,
    run_id: run.id,
    prompt_id: 1,
    provider: ENGINE.provider,
    model: ENGINE.model,
    attempt: 1,
    answer_text: text,
    brand_mentioned: mentioned,
    first_mention_pos: mentioned ? text.indexOf(BRAND) : -1,
    competitor_mentions: Object.fromEntries(
      COMPETITORS.filter((c) => text.includes(c.name)).map((c) => [c.name, 1])
    ),
    latency_ms: 8200 + index * 1300,
    citations,
    created_at: run.completed_at,
  };
});

function dashboard(days: number) {
  const window = Math.min(days, 30);
  const timeline = Array.from({ length: window }, (_, i) => {
    const day = daysAgo(window - 1 - i);
    const overall = pct(0.55 + 0.25 * (i / window) + 0.08 * wobble(i + 1));
    return { day: isoDay(day), overall, by_provider: { [ENGINE.provider]: overall } };
  });
  const competitorTimeline = timeline.map((point, i) => ({
    day: point.day,
    by_company: {
      [BRAND]: point.overall,
      'Northwind CRM': pct(0.5 + 0.1 * wobble(i + 7)),
      'Contoso Sales': pct(0.3 + 0.08 * wobble(i + 13)),
      'Fabrikam Desk': pct(0.22 + 0.06 * wobble(i + 19)),
    },
  }));
  const shares = [
    { company: BRAND, is_brand: true, mentions: 13, visibility: pct(13 / 18) },
    { company: 'Northwind CRM', is_brand: false, mentions: 9, visibility: pct(9 / 18) },
    { company: 'Contoso Sales', is_brand: false, mentions: 5, visibility: pct(5 / 18) },
    { company: 'Fabrikam Desk', is_brand: false, mentions: 4, visibility: pct(4 / 18) },
  ];
  const totalMentions = shares.reduce((sum, s) => sum + s.mentions, 0);
  return {
    from: isoDay(daysAgo(days - 1)),
    to: isoDay(now),
    days,
    total_answers: 18,
    failed_answers: 0,
    brand_mentions: 13,
    visibility: pct(13 / 18),
    by_provider: [{ provider: ENGINE.provider, answers: 18, mentions: 13, visibility: pct(13 / 18) }],
    timeline,
    share_of_voice: shares.map((s) => ({ ...s, share: pct(s.mentions / totalMentions) })),
    competitor_timeline: competitorTimeline,
    last_run_at: runs[0].completed_at,
  };
}

function citationsReport(days: number) {
  return {
    from: isoDay(daysAgo(days - 1)),
    to: isoDay(now),
    total_answers: 18,
    total_citations: 14,
    answers_with_citations: 9,
    owned_citation_rate: pct(0.389),
    by_company: [
      { company: BRAND, is_brand: true, citations: 7, citation_rate: pct(0.389), with_brand_mention: 7, brand_mention_rate: pct(1) },
      { company: 'Northwind CRM', is_brand: false, citations: 4, citation_rate: pct(0.222), with_brand_mention: 3, brand_mention_rate: pct(0.75) },
      { company: 'Contoso Sales', is_brand: false, citations: 2, citation_rate: pct(0.111), with_brand_mention: 1, brand_mention_rate: pct(0.5) },
      { company: 'Fabrikam Desk', is_brand: false, citations: 1, citation_rate: pct(0.056), with_brand_mention: 1, brand_mention_rate: pct(1) },
    ],
    top_domains: [
      { domain: 'gophercrm.example', company: BRAND, is_owned: true, citations: 7, citation_rate: pct(0.389), with_brand_mention: 7, brand_mention_rate: pct(1) },
      { domain: 'northwind-crm.example', company: 'Northwind CRM', is_owned: false, citations: 4, citation_rate: pct(0.222), with_brand_mention: 3, brand_mention_rate: pct(0.75) },
      { domain: 'contoso-sales.example', company: 'Contoso Sales', is_owned: false, citations: 2, citation_rate: pct(0.111), with_brand_mention: 1, brand_mention_rate: pct(0.5) },
      { domain: 'fabrikam-desk.example', company: 'Fabrikam Desk', is_owned: false, citations: 1, citation_rate: pct(0.056), with_brand_mention: 1, brand_mention_rate: pct(1) },
    ],
  };
}

const ok = (route: Route, data: unknown) =>
  route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify({ success: true, data }),
  });

/** Answers every AEO API call from the fixtures above. */
export async function mockAeoApi(page: Page): Promise<void> {
  await page.route(/\/api\/v1\/aeo\//, (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname.replace(/^.*\/api\/v1\/aeo/, '');
    const days = Number(url.searchParams.get('days') ?? 30);

    if (route.request().method() !== 'GET') {
      // The captures never save or run anything; refuse loudly if one tries.
      return route.fulfill({ status: 405, body: 'screenshot fixtures are read-only' });
    }
    if (path === '/profile') return ok(route, profile);
    if (path === '/providers') return ok(route, providers);
    if (path === '/prompts') return ok(route, prompts);
    if (/^\/prompts\/\d+\/answers$/.test(path)) return ok(route, answers);
    if (path === '/runs') return ok(route, runs);
    if (path === '/dashboard') return ok(route, dashboard(days));
    if (path === '/citations') return ok(route, citationsReport(days));
    return route.fulfill({ status: 404, body: `no screenshot fixture for ${path}` });
  });
}
