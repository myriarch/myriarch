// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Runtime check of the website (ADR-0031, privacy rule): loads pages in a headless Chrome and fails on
//   - a request to an origin other than the one that serves the site (scripts, styles, fonts, images, XHR, workers, frames),
//   - a Content Security Policy violation or another console error (the CSP of the production build must not break the page),
//   - a page that does not answer 200 (404.html must answer, from a static server, with the 404 status),
//   - a broken feature of the theme: the search must find results, a page with a Mermaid diagram must render its SVG, the static API reference must render its tables and its operations must be found by the site search.
//
//   node website/scripts/check-requests.mjs [--base http://127.0.0.1:1313] [--pages website/scripts/request-pages.txt] [--page /path ...]
//
// It also checks the motion of the mark on the landing page (checkMotion): the rollout plays on the ten parts of the mark, every part ends
// on an identity transform (the unchanged mark), and with prefers-reduced-motion: reduce nothing animates.
//
// Serve the production build first (make website-preview, port 1313). Every page is loaded in the light and in the dark appearance.
// Chrome: the environment variable CHROME, else Google Chrome on macOS, else google-chrome or chromium (see cdp.mjs). Node.js 22 or newer.
// Exit status: 0 = clean, 1 = findings, 2 = environment error (no Chrome, site not reachable, bad arguments).

import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";
import { launch } from "./cdp.mjs";

const here = dirname(fileURLToPath(import.meta.url));

function parseArgs(argv) {
  const opts = { base: "http://127.0.0.1:1313", pagesFile: join(here, "request-pages.txt"), pages: [] };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--base" && argv[i + 1]) opts.base = argv[++i];
    else if (a === "--pages" && argv[i + 1]) opts.pagesFile = argv[++i];
    else if (a === "--page" && argv[i + 1]) opts.pages.push(argv[++i]);
    else throw new Error(`unknown argument ${a}\nusage: node check-requests.mjs [--base URL] [--pages FILE] [--page PATH ...]`);
  }
  if (opts.pages.length === 0) {
    opts.pages = readFileSync(opts.pagesFile, "utf8")
      .split("\n")
      .map((l) => l.trim())
      .filter((l) => l && !l.startsWith("#"));
  }
  return opts;
}

const LOCAL_SCHEMES = new Set(["data:", "blob:", "about:", "chrome:", "devtools:"]);

/** True when the request leaves the origin of the site. data:, blob: and about: are no network requests. */
export function isForeign(url, origin) {
  let u;
  try {
    u = new URL(url);
  } catch {
    return true;
  }
  if (LOCAL_SCHEMES.has(u.protocol)) return false;
  return u.origin !== origin;
}

async function waitFor(page, expression, timeoutMs = 10000) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (await page.evaluate(expression)) return true;
    await new Promise((r) => setTimeout(r, 100));
  }
  return false;
}

async function checkPage(browser, base, path, scheme, findings) {
  const origin = new URL(base).origin;
  const label = `${path} (${scheme})`;
  const page = await browser.newPage();
  let requests = 0;
  page.onRequest((r) => {
    requests++;
    if (isForeign(r.url, origin)) findings.push(`${label}: request to another origin: ${r.url}`);
  });
  page.onConsole((m) => {
    // The favicon of a 404 response and the like are not errors of the site; a CSP violation or a script error is.
    if (m.level === "error" && !/Failed to load resource: the server responded with a status of 404/.test(m.text)) {
      findings.push(`${label}: console ${m.source}: ${m.text.split("\n")[0].slice(0, 200)}`);
    }
  });
  await page.setViewport(1280, 900);
  await page.setColorScheme(scheme);
  await page.goto(base + path);
  await page.waitForIdle();
  const status = await page.evaluate("performance.getEntriesByType('navigation')[0]?.responseStatus ?? 0");
  const wantStatus = path === "/404.html" ? 404 : 200;
  if (status !== wantStatus && !(path === "/404.html" && status === 200)) findings.push(`${label}: status ${status}, expected ${wantStatus}`);
  const theme = await page.evaluate("document.documentElement.getAttribute('data-bs-theme')");
  if (theme !== scheme) findings.push(`${label}: the page uses the theme ${theme}, expected ${scheme}`);

  // Search: type a query, the index (a file of the site) is loaded and results appear.
  if (path === "/docs/") {
    await page.evaluate("(() => { const i = document.querySelector('.td-search__input'); i.focus(); i.value = 'rollout'; i.dispatchEvent(new Event('change', {bubbles: true})); })()");
    const found = await waitFor(page, "document.querySelectorAll('.td-offline-search-results a').length > 0");
    if (!found) findings.push(`${label}: the search for "rollout" found nothing`);
    await page.waitForIdle();
  }
  // Mermaid: the diagram becomes an SVG, from the self-hosted copy.
  const hasMermaid = await page.evaluate("document.querySelectorAll('.mermaid').length");
  if (hasMermaid > 0) {
    const rendered = await waitFor(page, "document.querySelector('.mermaid svg') !== null", 15000);
    if (!rendered) findings.push(`${label}: a Mermaid diagram was not rendered`);
    await page.waitForIdle();
  }
  // API reference: static pages generated from the contract. A tag page has its summary table and one table per parameter list and
  // response list, and the operations and permissions of the contract are found by the site search.
  if (path === "/docs/reference/api/agents/") {
    const shape = await page.evaluate(
      "JSON.stringify({tables: document.querySelectorAll('.td-content table').length, sections: document.querySelectorAll('.td-content h2').length, scripts: [...document.scripts].map((s) => s.src).filter((u) => /redoc|swagger|scalar/i.test(u)).length})",
    );
    const { tables, sections, scripts } = JSON.parse(shape);
    if (tables < 2 || sections < 2) findings.push(`${label}: the API reference page has ${tables} tables and ${sections} operation sections`);
    if (scripts > 0) findings.push(`${label}: the API reference page loads a viewer script`);
  }
  if (path === "/docs/" && scheme === "light") {
    for (const query of ["approveRolloutWave", "rollouts:operate"]) {
      await page.evaluate("(() => { const i = document.querySelector('.td-search__input'); i.focus(); i.value = ''; i.dispatchEvent(new Event('change', {bubbles: true})); })()");
      await waitFor(page, "document.querySelectorAll('.td-offline-search-results a').length === 0", 3000);
      await page.evaluate(`(() => { const i = document.querySelector('.td-search__input'); i.value = ${JSON.stringify(query)}; i.dispatchEvent(new Event('change', {bubbles: true})); })()`);
      const found = await waitFor(
        page,
        "[...document.querySelectorAll('.td-offline-search-results a')].some((a) => new URL(a.href).pathname.startsWith('/docs/reference/api/'))",
        10000,
      );
      if (!found) findings.push(`${label}: the search for "${query}" finds no page of the API reference`);
    }
    await page.waitForIdle();
  }
  return requests;
}

/** The parts of the mark and their computed transforms in one expression (JSON). */
const MARK_STATE = `(() => {
  const mark = document.querySelector('svg.landing-hero__mark');
  if (!mark) return JSON.stringify({ found: false });
  const parts = [...mark.querySelectorAll('.ring, .dot')];
  return JSON.stringify({
    found: true,
    parts: parts.length,
    animations: mark.getAnimations({ subtree: true }).length,
    transforms: parts.map((p) => getComputedStyle(p).transform),
  });
})()`;

const IDENTITY = new Set(["none", "matrix(1, 0, 0, 1, 0, 0)"]);

/**
 * The mark of the landing page: at 1280 px the rollout animation runs on the ring and the nine dots (ten animations) and, when
 * it has finished, every part has an identity transform, so the mark is the unchanged mark. With prefers-reduced-motion: reduce no
 * animation runs on the mark and the parts are in their final state from the start.
 */
async function checkMotion(browser, base, findings) {
  for (const reduce of [false, true]) {
    const label = `/ (motion, ${reduce ? "reduced motion" : "full motion"})`;
    const page = await browser.newPage();
    await page.setViewport(1280, 900);
    await page.setColorScheme("light");
    await page.setReducedMotion(reduce);
    await page.goto(base + "/");
    await page.waitForIdle();
    const first = JSON.parse(await page.evaluate(MARK_STATE));
    if (!first.found) {
      findings.push(`${label}: the mark (svg.landing-hero__mark) is not on the page`);
      continue;
    }
    if (first.parts !== 10) findings.push(`${label}: the mark has ${first.parts} parts, expected 10`);
    if (reduce) {
      if (first.animations !== 0) findings.push(`${label}: ${first.animations} animations run on the mark with reduced motion`);
    } else if (first.animations !== 10) {
      findings.push(`${label}: ${first.animations} animations run on the mark, expected 10 (the ring and nine dots)`);
    }
    // wait for the end of every animation (at most about 2.4 s long), then every part must be in its final state
    await page.evaluate(
      "Promise.all(document.querySelector('svg.landing-hero__mark').getAnimations({ subtree: true }).map((a) => a.finished.catch(() => {})))",
    );
    const last = JSON.parse(await page.evaluate(MARK_STATE));
    const moved = last.transforms.filter((t) => !IDENTITY.has(t));
    if (moved.length > 0) findings.push(`${label}: ${moved.length} parts of the mark do not end on an identity transform (${moved[0]})`);
  }
}

async function main() {
  let opts;
  try {
    opts = parseArgs(process.argv.slice(2));
  } catch (e) {
    console.error(`check-requests: ${e.message}`);
    return 2;
  }
  try {
    const res = await fetch(opts.base + "/", { signal: AbortSignal.timeout(5000) });
    if (!res.ok) throw new Error(`status ${res.status}`);
  } catch (e) {
    console.error(`check-requests: ${opts.base} is not reachable (${e.message}); start make website-preview first`);
    return 2;
  }
  let browser;
  try {
    browser = await launch();
  } catch (e) {
    console.error(`check-requests: cannot start Chrome: ${e.message}`);
    return 2;
  }
  const findings = [];
  let loads = 0;
  let requests = 0;
  try {
    for (const path of opts.pages) {
      for (const scheme of ["light", "dark"]) {
        requests += await checkPage(browser, opts.base, path, scheme, findings);
        loads++;
      }
    }
    await checkMotion(browser, opts.base, findings);
  } catch (e) {
    console.error(`check-requests: ${e.message}`);
    await browser.close();
    return 2;
  }
  await browser.close();
  for (const f of [...new Set(findings)].sort()) console.log(f);
  console.log(`check-requests: ${loads} page loads, ${requests} requests, ${new Set(findings).size} findings`);
  return findings.length > 0 ? 1 : 0;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  process.exit(await main());
}
