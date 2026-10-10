// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Drives the REAL UI (headless Chromium-based browser) through the version-update flow of scripts/e2e-ui-updates.sh:
//
//   select   login form -> fleet page -> button "Auf Version X aktualisieren" -> dialog -> reason -> start -> rollout page; then observes the  // check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
//            live step bar (the same DOM a user sees) until the rollout is paused/completed; prints one JSON object
//   retry    rollout page -> "Fehlgeschlagene wiederholen" -> observes again
//   notreached  fleet page + rollout page after a host rollback: the "target not reached" alert, no "up to date" claim, the selection hint
//   inspect  fleet page: checks the "per UI gesetzt" marker (who, reason) and takes a screenshot
//
//   BASE=http://127.0.0.1:18090 FLEET=local-gw VERSION=0.162.0 REASON="..." ROLLOUT=<id> SHOTS=<dir> TIMEOUT=240 \
//   BROWSER=<chromium-based browser> NODE_PATH=<dir with playwright-core> node scripts/e2e/ui-updates/ui-update.mjs select|retry|inspect
//
// Output: the last stdout line is a JSON result; progress goes to stderr. Exit code 1 if a UI step could not be performed.
import { createRequire } from 'node:module';
import { mkdirSync } from 'node:fs';
const require = createRequire(import.meta.url);
const { chromium } = require('playwright-core');

const mode = process.argv[2] || 'select';
const BASE = process.env.BASE || 'http://127.0.0.1:18090';
const FLEET = process.env.FLEET || 'local-gw';
const VERSION = process.env.VERSION || '0.162.0';
const REASON = process.env.REASON || 'e2e via UI';
const SHOTS = process.env.SHOTS || '';
// UNTIL: regex of the phase texts that end the observation (the engine's own 'fehlgeschlagen' may come before the host's rollback)
const UNTIL = new RegExp(process.env.UNTIL || 'erfolgreich|zurückgerollt|fehlgeschlagen|abgebrochen', 'i');  // check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
const TIMEOUT = Number(process.env.TIMEOUT || 240) * 1000;
const BROWSER = process.env.BROWSER || '/Applications/Brave Browser.app/Contents/MacOS/Brave Browser';
if (SHOTS) mkdirSync(SHOTS, { recursive: true });

const log = (...a) => console.error(new Date().toISOString().slice(11, 19), ...a);
const problems = [];
const result = { mode, problems };

const browser = await chromium.launch({ executablePath: BROWSER, headless: true });
const ctx = await browser.newContext({ viewport: { width: 1500, height: 1000 }, locale: 'de-DE' });
const page = await ctx.newPage();
page.on('pageerror', (e) => problems.push(`pageerror: ${e.message}`));
page.on('console', (m) => {
  if (m.type() === 'error' && !/status of 401/.test(m.text())) problems.push(`console: ${m.text()}`);
});
page.on('response', (r) => {
  if (r.url().includes('/api/v1/') && r.status() >= 500) problems.push(`HTTP ${r.status()} ${r.request().method()} ${r.url()}`);
});
const shot = async (name) => {
  if (SHOTS) await page.screenshot({ path: `${SHOTS}/${name}.png`, fullPage: true });
};
const api = async (path) => {
  const r = await ctx.request.get(`${BASE}/api/v1${path}`);
  return r.ok() ? r.json() : null;
};

async function login() {
  await page.goto(`${BASE}/login`, { waitUntil: 'networkidle' });
  await page.locator('input[autocomplete=username]').fill('admin');
  await page.locator('input[type=password]').fill('admin');
  await page.getByRole('button', { name: /Anmelden|Sign in|Login/i }).first().click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15000 });
}

// What a user sees of the update flow on the rollout page: phase text and the status of every step segment.
async function readFlow() {
  return page.evaluate(() => {
    const bar = document.querySelector('[data-testid="update-steps-bar"]');
    if (!bar) return null;
    return {
      phase: bar.querySelector('[data-testid="update-phase"]')?.textContent?.trim() ?? '',
      steps: [...bar.querySelectorAll('[data-step]')].map((e) => `${e.getAttribute('data-step')}:${e.getAttribute('data-status')}`),
    };
  });
}

async function observe(rolloutId, label) {
  const phases = [];
  const stepSnapshots = [];
  const end = Date.now() + TIMEOUT;
  let lastKey = '';
  let status = '';
  let settledSince = 0;
  while (Date.now() < end) {
    const flow = await readFlow();
    if (flow) {
      const key = `${flow.phase}|${flow.steps.join(',')}`;
      if (key !== lastKey) {
        lastKey = key;
        if (phases[phases.length - 1] !== flow.phase) {
          phases.push(flow.phase);
          await shot(`${label}-${String(phases.length).padStart(2, '0')}-${flow.phase.replace(/[^A-Za-z0-9äöüß]+/g, '_')}`);  // check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
        }
        stepSnapshots.push(flow.steps.join(','));
        log('UI flow:', flow.phase, '|', flow.steps.join(' '));
      }
    }
    const ro = await api(`/rollouts/${rolloutId}`);
    status = ro?.status ?? status;
    // done when the rollout ended AND the UI shows a final phase for a while (the updater may still be rolling back)
    const final = flow && UNTIL.test(flow.phase);
    if (['completed', 'paused', 'failed', 'aborted'].includes(status) && final) {
      settledSince ||= Date.now();
      if (Date.now() - settledSince > 6000) break;
    } else settledSince = 0;
    await page.waitForTimeout(500);
  }
  await shot(`${label}-final`);
  return { rolloutStatus: status, phases, finalSteps: stepSnapshots[stepSnapshots.length - 1] ?? '', stepSnapshots };
}

try {
  await login();
  if (mode === 'select') {
    await page.goto(`${BASE}/fleets/${FLEET}`, { waitUntil: 'networkidle' });
    await page.getByTestId('update-to-version').waitFor({ timeout: 20000 });
    await shot('01-fleet-before');
    result.buttonText = (await page.getByTestId('update-to-version').textContent())?.trim();
    log('button:', result.buttonText);
    await page.getByTestId('update-to-version').click();
    const dlg = page.getByRole('dialog');
    await dlg.waitFor({ timeout: 10000 });
    await page.getByTestId('update-summary').waitFor({ timeout: 10000 });
    result.dialogText = (await dlg.innerText()).replace(/\s+/g, ' ').slice(0, 600);
    await shot('02-dialog');
    // pick the version in the list when it is not preselected
    const opt = dlg.getByText(new RegExp(`^${VERSION.replace(/\./g, '\\.')}$`)).first();
    if (await opt.count()) await opt.click().catch(() => {});
    const reason = dlg.locator('textarea').first();
    if (await reason.count()) await reason.fill(REASON);
    await shot('03-dialog-filled');
    await page.getByTestId('start-update').click();
    await page.waitForURL(/\/rollouts\/[0-9a-f-]{36}/, { timeout: 30000 });
    result.rolloutId = page.url().match(/\/rollouts\/([0-9a-f-]{36})/)[1];
    log('rollout page', result.rolloutId);
    Object.assign(result, await observe(result.rolloutId, 'rollout'));
    result.pageText = (await page.locator('main').innerText().catch(() => '')).replace(/\s+/g, ' ').slice(0, 1500);
  } else if (mode === 'retry') {
    const id = process.env.ROLLOUT;
    await page.goto(`${BASE}/rollouts/${id}`, { waitUntil: 'networkidle' });
    await page.getByTestId('retry-primary').waitFor({ timeout: 20000 });
    result.retryButton = (await page.getByTestId('retry-primary').textContent())?.trim();
    await shot('retry-00-before');
    await page.getByTestId('retry-primary').click();
    // the action dialog (optional reason) follows: confirm it
    const dlg = page.getByRole('dialog');
    await dlg.waitFor({ timeout: 10000 });
    const reason = dlg.locator('textarea, input[type=text]').first();
    if (await reason.count()) await reason.fill(REASON);
    await shot('retry-01-dialog');
    await dlg.locator('button:not([data-variant=default]):not([aria-label])').last().click();
    result.rolloutId = id;
    // the click must have an effect: the rollout leaves 'paused' and the UI starts a fresh attempt
    const left = Date.now() + 30000;
    while (Date.now() < left && (await api(`/rollouts/${id}`))?.status === 'paused') await page.waitForTimeout(500);
    result.afterRetryStatus = (await api(`/rollouts/${id}`))?.status;
    await page.waitForTimeout(1500);
    Object.assign(result, await observe(id, 'retry'));
  } else if (mode === 'notreached') {
    await page.goto(`${BASE}/fleets/${FLEET}`, { waitUntil: 'networkidle' });
    const alert = page.getByTestId('target-not-reached');
    await alert.waitFor({ timeout: 30000 }).catch(() => {});
    result.notReached = (await alert.innerText().catch(() => '')).replace(/\s+/g, ' ');
    await page.getByTestId('update-to-version').hover().catch(() => {});
    await page.waitForTimeout(500);
    const main = (await page.locator('main').innerText()).replace(/\s+/g, ' ');
    result.claimsUpToDate = /Bereits auf der neuesten erlaubten Version/.test(main + ' ' + (await page.locator('body').innerText()));  // check-language: ignore (UI text of the German locale, web/src/i18n/de.json)
    await shot('fleet-not-reached');
    await page.goto(`${BASE}/rollouts/${process.env.ROLLOUT}`, { waitUntil: 'networkidle' });
    const hint = page.getByTestId('selection-still-active');
    await hint.waitFor({ timeout: 20000 }).catch(() => {});
    result.rolloutHint = (await hint.innerText().catch(() => '')).replace(/\s+/g, ' ');
    await shot('rollout-selection-hint');
  } else if (mode === 'inspect') {
    await page.goto(`${BASE}/fleets/${FLEET}`, { waitUntil: 'networkidle' });
    await page.getByTestId('selection-info').waitFor({ timeout: 20000 });
    result.selectionInfo = (await page.getByTestId('selection-info').innerText()).replace(/\s+/g, ' ');
    result.sectionText = (await page.locator('main').innerText()).replace(/\s+/g, ' ').slice(0, 1800);
    result.history = (await page.getByTestId('version-history').innerText().catch(() => '')).replace(/\s+/g, ' ');
    await shot('fleet-after');
    if (process.env.ROLLOUT) {
      await page.goto(`${BASE}/rollouts/${process.env.ROLLOUT}`, { waitUntil: 'networkidle' });
      await page.waitForTimeout(1500);
      result.rolloutPageText = (await page.locator('main').innerText()).replace(/\s+/g, ' ').slice(0, 2000);
      result.objectObject = result.rolloutPageText.includes('[object Object]');
      await shot('rollout-detail');
    }
  }
} catch (e) {
  problems.push(`ui step failed: ${e.message}`);
  await shot('error').catch(() => {});
}
await browser.close();
console.log(JSON.stringify(result));
process.exit(problems.some((p) => p.startsWith('ui step failed')) ? 1 : 0);
