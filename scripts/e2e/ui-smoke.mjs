// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// UI smoke test against a running myriarchd (embedded UI).
//
//   BASE=http://localhost:8080 BROWSER="/Applications/Brave Browser.app/Contents/MacOS/Brave Browser" \
//     NODE_PATH=<dir with node_modules/playwright-core> node scripts/e2e/ui-smoke.mjs
//
// Logs in through the real login form (dev login admin/admin), visits all pages
// in light and dark mode, and fails on console errors, page errors, failed API
// calls (5xx, unexpected 4xx) and missing page content. Also checks that the
// viewer role does not see operational actions. Exit code 0 = ok.
import { createRequire } from 'node:module';
const require = createRequire(import.meta.url);
const { chromium } = require('playwright-core');

const BASE = process.env.BASE || 'http://localhost:8080';
const MIN_AGENTS = Number(process.env.MIN_AGENTS || 1000);
const BROWSER = process.env.BROWSER || '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser';
const problems = [];
const results = [];

async function api(ctx, path) {
  const r = await ctx.request.get(BASE + '/api/v1' + path);
  return r.ok() ? r.json() : null;
}

async function loginViaForm(page, user, pass) {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.locator('input[autocomplete=username]').fill(user);
  await page.locator('input[type=password]').fill(pass);
  await page.getByRole('button', { name: /Anmelden|Sign in|Login/i }).first().click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15000 });
}

function watch(page, label) {
  page.on('console', (m) => {
    // the 401 of the unauthenticated /auth/me probe before login is expected browser noise
    if (m.type() === 'error' && !/status of 401/.test(m.text())) problems.push(`[${label}] console error: ${m.text()}`);
  });
  page.on('pageerror', (e) => problems.push(`[${label}] page error: ${e.message}`));
  page.on('response', (r) => {
    const u = new URL(r.url());
    if (!u.pathname.startsWith('/api/v1/')) return;
    const s = r.status();
    // 404 on effective/desired config is "not present", not an error (UI handles it)
    if (s >= 500 || (s >= 400 && s !== 404 && s !== 401)) problems.push(`[${label}] ${s} ${r.request().method()} ${u.pathname}${u.search}`);
    if (s === 404 && !/\/(effective|desired)-config$/.test(u.pathname)) problems.push(`[${label}] 404 ${u.pathname}`);
  });
}

const browser = await chromium.launch({ executablePath: BROWSER, headless: true });
try {
  for (const scheme of ['light', 'dark']) {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, colorScheme: scheme, locale: 'de-DE' });
    const page = await ctx.newPage();
    watch(page, `admin/${scheme}`);
    await loginViaForm(page, 'admin', 'admin');

    // dynamic ids for detail pages
    const agents = await api(ctx, '/agents?kind=collector&limit=1');
    const rollouts = await api(ctx, '/rollouts?limit=1');
    const fleets = await api(ctx, '/fleets');
    const releases = await api(ctx, '/releases');
    const pages = [
      ['/', 'dashboard'],
      ['/agents', 'agents'],
      ['/fleets', 'fleets'],
      ['/blueprints', 'blueprints'],
      ['/rollouts', 'rollouts'],
      ['/releases', 'releases'],
      ['/gitops', 'gitops'],
      ['/artifacts', 'artifacts'],
      ['/audit', 'audit'],
      ['/settings/system', 'settings-system'],
      ['/settings/tokens', 'settings-tokens'],
      ['/settings/notifications', 'settings-notifications'],
    ];
    if (agents?.items?.[0]) pages.push([`/agents/${agents.items[0].id}`, 'agent-detail']);
    if (rollouts?.items?.[0]) pages.push([`/rollouts/${rollouts.items[0].id}`, 'rollout-detail']);
    if (fleets?.items?.[0]) pages.push([`/fleets/${fleets.items[0].fleet.name}`, 'fleet-detail']);
    if (releases?.items?.[0]) pages.push([`/releases/otelcol-contrib/${releases.items[0].version}`, 'release-detail']);

    for (const [path, name] of pages) {
      await page.goto(BASE + path, { waitUntil: 'networkidle' });
      await page.waitForTimeout(500);
      const text = (await page.locator('body').innerText()).trim();
      const ok = text.length > 40 && !/Seite nicht gefunden|Page not found/i.test(text);
      results.push({ page: `${name}/${scheme}`, ok, chars: text.length });
      if (!ok) problems.push(`[admin/${scheme}] ${path}: page empty or 404`);
    }

    if (scheme === 'light') {
      // agent list: 1.000+ entries, server-side pagination, filters in the URL
      await page.goto(BASE + '/agents?kind=collector', { waitUntil: 'networkidle' });
      const total = (await api(ctx, '/agents?kind=collector&limit=1'))?.total ?? 0;
      results.push({ page: 'agents total', ok: total >= MIN_AGENTS, total });
      if (total < MIN_AGENTS) problems.push(`agents total ${total} < ${MIN_AGENTS}`);
      const rows = await page.locator('tbody tr').count();
      results.push({ page: 'agents rows rendered (paginated)', ok: rows > 0 && rows <= 200, rows });
      if (!(rows > 0 && rows <= 200)) problems.push(`agent rows ${rows}`);
      // language switch persisted
      await page.goto(BASE + '/', { waitUntil: 'networkidle' });
    }
    await ctx.close();
  }

  // viewer: no operational actions
  const vctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'de-DE' });
  const vpage = await vctx.newPage();
  watch(vpage, 'viewer');
  await loginViaForm(vpage, 'viewer', 'viewer');
  const rollouts = await api(vctx, '/rollouts?active=true&limit=1');
  if (rollouts?.items?.[0]) {
    await vpage.goto(`${BASE}/rollouts/${rollouts.items[0].id}`, { waitUntil: 'networkidle' });
    await vpage.waitForTimeout(500);
    const actions = await vpage.getByRole('button', { name: /Freigeben|Approve|Pausieren|Pause|Abbrechen|Abort|Rollback/i }).count();
    results.push({ page: 'viewer sees no rollout actions', ok: actions === 0, actions });
    if (actions !== 0) problems.push(`viewer sees ${actions} rollout action buttons`);
  }
  const forbidden = await vctx.request.post(BASE + '/api/v1/gitops/actions/sync');
  results.push({ page: 'viewer POST gitops sync rejected', ok: [401, 403].includes(forbidden.status()), status: forbidden.status() });
  if (![401, 403].includes(forbidden.status())) problems.push(`viewer POST sync -> ${forbidden.status()}`);
  await vctx.close();
} finally {
  await browser.close();
}

for (const r of results) console.log(`${r.ok ? 'ok  ' : 'FAIL'} ${r.page} ${JSON.stringify(r)}`);
// Console noise from aborted requests of fast navigations is not a defect.
const real = problems.filter((p) => !/net::ERR_ABORTED/.test(p));
if (real.length) {
  console.error('\nPROBLEMS:\n' + real.join('\n'));
  process.exit(1);
}
console.log('\nUI smoke test passed');
