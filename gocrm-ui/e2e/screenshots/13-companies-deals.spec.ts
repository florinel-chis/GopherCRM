import { test, expect, request as playwrightRequest, type APIRequestContext } from '@playwright/test';
import { capture } from './helpers/capture';
import { ensureAdminLoggedIn } from './helpers/login';
import { testAdminCredentials } from '../fixtures/admin-user';
import { API_BASE_URL } from '../helpers/env';

/**
 * Documentation captures for companies and deals (FEATURES.md sections 4b and
 * 4c): the company list and detail, the deal list, the pipeline board and one
 * deal's detail page with its stage history.
 *
 * Three companies, a customer and a lead linked to the first company, and five
 * deals across the pipeline stages are seeded through the API. Every record
 * has a fixed name (or email) and is created only when missing, so repeated
 * runs against the same database do not pile up rows on the screens being
 * captured. Nothing is deleted.
 */
test.describe.configure({ mode: 'serial' });

const CURRENCY = 'EUR';
const LOADED = { timeout: 30 * 1000 };

interface SeedCompany {
  name: string;
  domain: string;
  website: string;
  industry: string;
  employee_range: string;
  phone: string;
  address: string;
  city: string;
  country: string;
  postal_code: string;
  notes: string;
}

const COMPANIES: SeedCompany[] = [
  {
    name: 'Harbor Logistics',
    domain: 'harbor-logistics.example',
    website: 'https://harbor-logistics.example',
    industry: 'Logistics',
    employee_range: '201-500',
    phone: '+40 241 555 010',
    address: 'Strada Portului 12',
    city: 'Constanta',
    country: 'Romania',
    postal_code: '900178',
    notes: 'Port operator with three regional depots. Head office decides on all software purchases.',
  },
  {
    name: 'Northwind Bakery',
    domain: 'northwind-bakery.example',
    website: 'https://northwind-bakery.example',
    industry: 'Food & beverage',
    employee_range: '51-200',
    phone: '+40 264 555 220',
    address: 'Strada Memorandumului 8',
    city: 'Cluj-Napoca',
    country: 'Romania',
    postal_code: '400114',
    notes: 'Regional bakery chain, twelve shops.',
  },
  {
    name: 'Contoso Clinics',
    domain: 'contoso-clinics.example',
    website: 'https://contoso-clinics.example',
    industry: 'Healthcare',
    employee_range: '11-50',
    phone: '+40 21 555 0330',
    address: 'Bulevardul Unirii 45',
    city: 'Bucharest',
    country: 'Romania',
    postal_code: '030823',
    notes: 'Private outpatient clinics.',
  },
];

const MAIN_DEAL_TITLE = 'Harbor Logistics fleet tracking';

interface SeedDeal {
  title: string;
  company: string;
  /** The stages the deal walks through after creation, in order. */
  path: string[];
  amountCents: number;
  closeInDays: number;
  lostReason?: string;
}

const DEALS: SeedDeal[] = [
  {
    title: MAIN_DEAL_TITLE,
    company: 'Harbor Logistics',
    path: ['proposal', 'negotiation'],
    amountCents: 4800000,
    closeInDays: 30,
  },
  { title: 'Harbor Logistics depot rollout', company: 'Harbor Logistics', path: [], amountCents: 1200000, closeInDays: 60 },
  { title: 'Northwind Bakery loyalty CRM', company: 'Northwind Bakery', path: ['proposal'], amountCents: 950000, closeInDays: 21 },
  { title: 'Contoso Clinics patient intake', company: 'Contoso Clinics', path: ['proposal', 'negotiation', 'won'], amountCents: 2200000, closeInDays: 10 },
  {
    title: 'Northwind Bakery online orders',
    company: 'Northwind Bakery',
    path: ['proposal', 'lost'],
    amountCents: 600000,
    closeInDays: 5,
    lostReason: 'Chose a hosted shop platform',
  },
];

interface Seeded {
  companyId: number;
  mainDealId: number;
}

let seeded: Seeded | null = null;

function dateFromToday(days: number): string {
  return new Date(Date.now() + days * 24 * 60 * 60 * 1000).toISOString().slice(0, 10);
}

/** A list answer's rows: a bare array, or the one array inside a wrapper such as `{ leads: [...] }`. */
function rowsOf<T>(data: unknown): T[] {
  if (Array.isArray(data)) return data as T[];
  if (data && typeof data === 'object') {
    const inner = Object.values(data).find(Array.isArray);
    if (inner) return inner as T[];
  }
  return [];
}

async function getRows<T>(api: APIRequestContext, headers: Record<string, string>, path: string, params: Record<string, string | number>) {
  const response = await api.get(`${API_BASE_URL}${path}`, { headers, params });
  expect(response.status(), `GET ${path}`).toBe(200);
  return rowsOf<T>((await response.json()).data);
}

async function post(api: APIRequestContext, headers: Record<string, string>, path: string, data: unknown) {
  const response = await api.post(`${API_BASE_URL}${path}`, { headers, data });
  expect(response.status(), `POST ${path}`).toBeLessThan(300);
  return (await response.json()).data;
}

async function seedData(): Promise<Seeded> {
  const api = await playwrightRequest.newContext();
  try {
    const login = await api.post(`${API_BASE_URL}/auth/login`, {
      data: { email: testAdminCredentials.email, password: testAdminCredentials.password },
    });
    expect(login.status()).toBe(200);
    const headers = { Authorization: `Bearer ${(await login.json()).data.token}` };
    const me = await api.get(`${API_BASE_URL}/users/me`, { headers });
    expect(me.status()).toBe(200);
    const adminId: number = (await me.json()).data.id;

    const companyIds = new Map<string, number>();
    for (const company of COMPANIES) {
      const rows = await getRows<{ id: number; name: string }>(api, headers, '/companies', {
        search: company.name,
        limit: 100,
      });
      const existing = rows.find((row) => row.name === company.name);
      companyIds.set(company.name, existing ? existing.id : (await post(api, headers, '/companies', company)).id);
    }
    const harborId = companyIds.get('Harbor Logistics') as number;

    // One contact of the first company as a customer and one as a lead, so
    // its detail page lists both.
    const customerEmail = 'maria.ionescu@harbor-logistics.example';
    const customers = await getRows<{ id: number; email: string }>(api, headers, '/customers', {
      search: customerEmail,
      limit: 100,
    });
    let customerId = customers.find((row) => row.email === customerEmail)?.id;
    if (!customerId) {
      customerId = (
        await post(api, headers, '/customers', {
          first_name: 'Maria',
          last_name: 'Ionescu',
          email: customerEmail,
          company: 'Harbor Logistics',
          company_id: harborId,
          position: 'Operations Director',
          city: 'Constanta',
          country: 'Romania',
        })
      ).id;
    }

    const leadEmail = 'andrei.pop@harbor-logistics.example';
    const leads = await getRows<{ id: number; email: string }>(api, headers, '/leads', { search: leadEmail, limit: 100 });
    if (!leads.some((row) => row.email === leadEmail)) {
      await post(api, headers, '/leads', {
        first_name: 'Andrei',
        last_name: 'Pop',
        email: leadEmail,
        company: 'Harbor Logistics',
        company_id: harborId,
        position: 'Depot Manager',
        status: 'contacted',
        source: 'Referral',
        owner_id: adminId,
      });
    }

    let mainDealId = 0;
    for (const deal of DEALS) {
      const rows = await getRows<{ id: number; title: string }>(api, headers, '/deals', { search: deal.title, limit: 100 });
      let id = rows.find((row) => row.title === deal.title)?.id;
      if (!id) {
        const companyId = companyIds.get(deal.company) as number;
        id = (
          await post(api, headers, '/deals', {
            title: deal.title,
            stage: 'qualification',
            amount_cents: deal.amountCents,
            currency: CURRENCY,
            expected_close_date: dateFromToday(deal.closeInDays),
            company_id: companyId,
            customer_id: companyId === harborId ? customerId : undefined,
            source: 'Referral',
          })
        ).id as number;
        // Each move writes one stage-history row.
        for (const stage of deal.path) {
          await post(api, headers, `/deals/${id}/stage`, {
            stage,
            lost_reason: stage === 'lost' ? deal.lostReason : undefined,
          });
        }
      }
      if (deal.title === MAIN_DEAL_TITLE) {
        mainDealId = id;
      }
    }

    return { companyId: harborId, mainDealId };
  } finally {
    await api.dispose();
  }
}

async function ensureSeeded(): Promise<Seeded> {
  if (!seeded) {
    seeded = await seedData();
  }
  return seeded;
}

test.describe('Screenshots - Companies and deals', () => {
  test.beforeAll(async () => {
    test.setTimeout(120 * 1000);
    await ensureSeeded();
  });

  test.beforeEach(async ({ page }) => {
    test.setTimeout(90 * 1000);
    await ensureAdminLoggedIn(page);
  });

  test('companies list', async ({ page }) => {
    await page.goto('/companies');
    await expect(page.getByRole('heading', { name: 'Companies' })).toBeVisible(LOADED);
    const rows = page.locator('table tbody tr');
    for (const company of COMPANIES) {
      await expect(rows.filter({ hasText: company.name }).first()).toBeVisible(LOADED);
    }

    await capture(page, 'companies', '01-list', { fullPage: true });
  });

  test('company detail', async ({ page }) => {
    const { companyId } = await ensureSeeded();
    await page.goto(`/companies/${companyId}`);
    await expect(page.getByRole('heading', { name: 'Harbor Logistics', level: 4 })).toBeVisible(LOADED);
    await expect(
      page.locator('section[aria-labelledby="company-customers-heading"]').getByText('Maria', { exact: false }).first()
    ).toBeVisible(LOADED);
    await expect(page.locator('section[aria-labelledby="company-deals-heading"]').getByText(MAIN_DEAL_TITLE)).toBeVisible(
      LOADED
    );

    await capture(page, 'companies', '02-detail', { fullPage: true });
  });

  test('deals list', async ({ page }) => {
    await page.goto('/deals');
    await expect(page.getByRole('heading', { name: 'Deals', level: 4 })).toBeVisible(LOADED);
    const rows = page.locator('table tbody tr');
    for (const deal of DEALS) {
      await expect(rows.filter({ hasText: deal.title }).first()).toBeVisible(LOADED);
    }

    await capture(page, 'deals', '01-list', { fullPage: true });
  });

  test('deals board', async ({ page }) => {
    await page.goto('/deals/board');
    await expect(page.getByRole('heading', { name: 'Deal pipeline' })).toBeVisible(LOADED);
    for (const stage of ['qualification', 'proposal', 'negotiation', 'won', 'lost']) {
      await expect(page.getByTestId(`deal-board-column-${stage}`)).toBeVisible(LOADED);
    }
    await expect(page.getByTestId('deal-board-card').filter({ hasText: MAIN_DEAL_TITLE })).toBeVisible(LOADED);

    await capture(page, 'deals', '02-board', { fullPage: true });
  });

  test('deal detail with stage history', async ({ page }) => {
    const { mainDealId } = await ensureSeeded();
    await page.goto(`/deals/${mainDealId}`);
    await expect(page.getByRole('heading', { name: MAIN_DEAL_TITLE, level: 4 })).toBeVisible(LOADED);
    // Created in qualification, then moved to proposal and negotiation.
    await expect(page.getByTestId('deal-history-row')).toHaveCount(3, LOADED);

    await capture(page, 'deals', '03-detail', { fullPage: true });
  });
});
