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
// It also checks the motion of the mark on the landing page (checkMotion, light and dark): the rollout plays on the ten parts of the mark,
// every part ends on an identity transform (the unchanged mark), the mark is held, the dots retract and a second cycle starts; the pause
// button (an accessible button) stops the motion and shows the static mark, play starts a clean cycle, and the loop stands still while the
// hero is out of view or the document is hidden; with prefers-reduced-motion: reduce nothing animates and there is no button; without
// JavaScript the rollout plays once. The check takes about a minute.
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
    ring: getComputedStyle(mark.querySelector('.ring')).transform,
  });
})()`;

/** The clock of the loop: the animation of the last dot of the mark (iteration, progress within the cycle, time in ms). */
const LOOP_STATE = `(() => {
  const dot = document.querySelector('svg.landing-hero__mark .r3.c3');
  const animation = dot && dot.getAnimations()[0];
  if (!animation) return JSON.stringify({ running: false });
  const timing = animation.effect.getComputedTiming();
  return JSON.stringify({ running: true, iteration: timing.currentIteration, progress: timing.progress, ms: Number(animation.currentTime), iterations: timing.iterations });
})()`;

/** The pause button: what a keyboard user and a screen reader get. */
const BUTTON_STATE = `(() => {
  const button = document.querySelector('.landing-hero__motion');
  if (!button) return JSON.stringify({ found: false });
  const box = button.getBoundingClientRect();
  const style = getComputedStyle(button);
  const root = getComputedStyle(document.documentElement);
  return JSON.stringify({
    found: true,
    tag: button.tagName,
    type: button.getAttribute('type'),
    disabled: button.disabled,
    hiddenAttribute: button.hidden,
    display: style.display,
    width: box.width,
    height: box.height,
    tabIndex: button.tabIndex,
    label: button.getAttribute('aria-label'),
    pressed: button.getAttribute('aria-pressed'),
    color: style.color,
    borderColor: style.borderTopColor,
    from: root.getPropertyValue('--landing-hero-from').trim(),
    to: root.getPropertyValue('--landing-hero-to').trim(),
  });
})()`;

const IDENTITY = new Set(["none", "matrix(1, 0, 0, 1, 0, 0)"]);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const json = async (page, expression) => JSON.parse(await page.evaluate(expression));
const moved = (state) => state.transforms.filter((t) => !IDENTITY.has(t)).length;

/** Relative luminance of an sRGB color given as "#rgb", "#rrggbb" or "rgb(r, g, b)"; null when the text is none of these. */
function luminance(text) {
  let rgb;
  const hex = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(text);
  if (hex) {
    const h = hex[1].length === 3 ? [...hex[1]].map((c) => c + c).join("") : hex[1];
    rgb = [0, 2, 4].map((i) => parseInt(h.slice(i, i + 2), 16));
  } else {
    const m = /^rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(text);
    if (!m) return null;
    rgb = [m[1], m[2], m[3]].map(Number);
  }
  const [r, g, b] = rgb.map((v) => {
    const c = v / 255;
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4;
  });
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

export function contrast(a, b) {
  const la = luminance(a);
  const lb = luminance(b);
  if (la === null || lb === null) return null;
  return (Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05);
}

/** The pause button as an accessible control: a real button, reachable, labelled, at least 24x24 px, readable in this appearance. */
async function checkButton(page, label, findings) {
  const b = await json(page, BUTTON_STATE);
  if (!b.found) {
    findings.push(`${label}: the pause button (.landing-hero__motion) is not on the page`);
    return;
  }
  if (b.tag !== "BUTTON" || b.type !== "button") findings.push(`${label}: the pause control is not a <button type="button">`);
  if (b.disabled || b.hiddenAttribute || b.display === "none") findings.push(`${label}: the pause button is not shown (hidden=${b.hiddenAttribute}, display=${b.display})`);
  if (b.tabIndex < 0) findings.push(`${label}: the pause button is not reachable with the keyboard (tabIndex ${b.tabIndex})`);
  if (b.width < 24 || b.height < 24) findings.push(`${label}: the pause button is ${b.width}x${b.height} px, expected at least 24x24`);
  if (!b.label || !/pause/i.test(b.label)) findings.push(`${label}: the pause button has no aria-label that names the action (${JSON.stringify(b.label)})`);
  if (b.pressed !== "false") findings.push(`${label}: aria-pressed of the pause button is ${JSON.stringify(b.pressed)} while the mark plays, expected "false"`);
  for (const [what, color] of [["glyph", b.color], ["border", b.borderColor]]) {
    for (const bg of [b.from, b.to]) {
      const ratio = contrast(color, bg);
      if (ratio === null) findings.push(`${label}: cannot compute the contrast of the pause button (${color} on ${bg})`);
      else if (ratio < 4.5) findings.push(`${label}: the ${what} of the pause button has a contrast of ${ratio.toFixed(2)}:1 on ${bg}, expected 4.5:1`);
    }
  }
  // focus ring, as a keyboard user gets it
  const focus = await page.evaluate(`(() => {
    const button = document.querySelector('.landing-hero__motion');
    button.focus();
    const style = getComputedStyle(button);
    return JSON.stringify({ active: document.activeElement === button, visible: button.matches(':focus-visible'), outlineStyle: style.outlineStyle, outlineWidth: parseFloat(style.outlineWidth) });
  })()`);
  const f = JSON.parse(focus);
  if (!f.active) findings.push(`${label}: the pause button cannot take the keyboard focus`);
  else if (!f.visible || f.outlineStyle === "none" || f.outlineWidth < 2) findings.push(`${label}: the pause button shows no focus ring (${JSON.stringify(f)})`);
  await page.evaluate("document.querySelector('.landing-hero__motion').blur()");
}

/** True once the mark is static: no animation runs on it and every part is on its identity transform. */
async function expectStatic(page, label, what, findings) {
  const state = await json(page, MARK_STATE);
  if (state.animations !== 0) findings.push(`${label}: ${state.animations} animations run on the mark ${what}, expected none`);
  if (moved(state) > 0) findings.push(`${label}: ${moved(state)} parts of the mark are not on an identity transform ${what}`);
}

/** The mark plays: the dots (and, before the first stop, the ring) animate, and the loop is at the start of a cycle. */
async function expectPlaying(page, label, what, findings) {
  const state = await json(page, MARK_STATE);
  const loop = await json(page, LOOP_STATE);
  if (state.animations < 9) findings.push(`${label}: ${state.animations} animations run on the mark ${what}, expected at least 9 (the nine dots)`);
  else if (!loop.running || loop.iteration !== 0 || loop.ms > 1500) {
    findings.push(`${label}: the mark does not start from the beginning of a cycle ${what} (${JSON.stringify(loop)})`);
  }
}

/** Full motion in one appearance: the loop, the pause button, the observers. About 20 s. */
async function checkLoop(browser, base, scheme, findings) {
  const label = `/ (motion, full motion, ${scheme})`;
  const page = await browser.newPage();
  await page.setViewport(1280, 900);
  await page.setColorScheme(scheme);
  await page.setReducedMotion(false);
  await page.goto(base + "/");
  await page.waitForIdle();
  const first = await json(page, MARK_STATE);
  if (!first.found) {
    findings.push(`${label}: the mark (svg.landing-hero__mark) is not on the page`);
    return;
  }
  if (first.parts !== 10) findings.push(`${label}: the mark has ${first.parts} parts, expected 10`);
  if (first.animations !== 10) findings.push(`${label}: ${first.animations} animations run on the mark, expected 10 (the ring and nine dots)`);
  await checkButton(page, label, findings);

  // The first cycle: the rollout ends on identity transforms and the unchanged mark is held (6 to 8.5 s), then the dots retract (the
  // transforms move again), and a second cycle starts. The ring stays where it is for the whole time.
  let holdStart = null;
  let holdEnd = null;
  let sawRetract = false;
  let secondCycle = false;
  let secondCycleMoved = false;
  let ringMoved = 0;
  const deadline = Date.now() + 14000;
  while (Date.now() < deadline) {
    const loop = await json(page, LOOP_STATE);
    const state = await json(page, MARK_STATE);
    if (!loop.running) {
      findings.push(`${label}: the loop stopped while the mark was in view`);
      break;
    }
    if (loop.iteration === 0) {
      const identity = moved(state) === 0;
      if (identity && loop.ms > 2000 && holdStart === null) holdStart = loop.ms;
      if (!identity && holdStart !== null && holdEnd === null) holdEnd = loop.ms;
      if (!identity && loop.progress >= 0.94) sawRetract = true;
      if (holdStart === null && loop.ms > 2800) {
        findings.push(`${label}: the first rollout is not finished after ${Math.round(loop.ms)} ms (${moved(state)} parts still move)`);
        holdStart = loop.ms;
      }
    } else if (loop.iteration === 1) {
      secondCycle = true;
      if (moved(state) > 0) secondCycleMoved = true;
      if (loop.ms > 1500) break;
    }
    if (!IDENTITY.has(state.ring)) ringMoved++;
    await sleep(100);
  }
  if (holdStart === null) findings.push(`${label}: the first cycle never ended on identity transforms`);
  else if (holdEnd === null) findings.push(`${label}: the mark was never retracted after the hold`);
  else if (holdEnd - holdStart < 6000 || holdEnd - holdStart > 8500) {
    findings.push(`${label}: the unchanged mark is held for ${Math.round(holdEnd - holdStart)} ms, expected 6000 to 8500`);
  }
  if (!sawRetract) findings.push(`${label}: the dots do not retract at the end of the cycle`);
  if (!secondCycle) findings.push(`${label}: no second cycle starts after the hold`);
  else if (!secondCycleMoved) findings.push(`${label}: the second cycle does not move the dots`);
  if (ringMoved > 0) findings.push(`${label}: the ring left its identity transform ${ringMoved} times after it had grown (it must stay in place)`);

  // The pause button stops the motion and shows the static mark; play starts a clean cycle.
  await page.evaluate("document.querySelector('.landing-hero__motion').click()");
  await sleep(250);
  await expectStatic(page, label, "after the pause button was pressed", findings);
  const pausedButton = await json(page, BUTTON_STATE);
  if (pausedButton.pressed !== "true") findings.push(`${label}: aria-pressed is ${JSON.stringify(pausedButton.pressed)} after pause, expected "true"`);
  await sleep(1500);
  await expectStatic(page, label, "while paused", findings);
  await page.evaluate("document.querySelector('.landing-hero__motion').click()");
  await sleep(150);
  await expectPlaying(page, label, "after play was pressed", findings);
  const playing = await json(page, BUTTON_STATE);
  if (playing.pressed !== "false") findings.push(`${label}: aria-pressed is ${JSON.stringify(playing.pressed)} after play, expected "false"`);

  // Out of view: no motion; back in view: a clean cycle start.
  await page.evaluate("window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' })");
  await sleep(600);
  await expectStatic(page, label, "while the hero is out of view", findings);
  await page.evaluate("window.scrollTo({ top: 0, behavior: 'instant' })");
  await sleep(400);
  await expectPlaying(page, label, "when the hero is back in view", findings);

  // A hidden document: no motion; visible again: a clean cycle start.
  await page.evaluate("(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')); })()");
  await sleep(200);
  await expectStatic(page, label, "while the document is hidden", findings);
  await page.evaluate("(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); })()");
  await sleep(200);
  await expectPlaying(page, label, "when the document is visible again", findings);

  // A paused mark stays paused when the hero leaves the view and comes back.
  await page.evaluate("document.querySelector('.landing-hero__motion').click()");
  await page.evaluate("window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' })");
  await sleep(400);
  await page.evaluate("window.scrollTo({ top: 0, behavior: 'instant' })");
  await sleep(400);
  await expectStatic(page, label, "when the hero came back after the visitor had paused it", findings);
}

/** With prefers-reduced-motion: reduce nothing animates and there is no pause button. */
async function checkReducedMotion(browser, base, scheme, findings) {
  const label = `/ (motion, reduced motion, ${scheme})`;
  const page = await browser.newPage();
  await page.setViewport(1280, 900);
  await page.setColorScheme(scheme);
  await page.setReducedMotion(true);
  await page.goto(base + "/");
  await page.waitForIdle();
  const first = await json(page, MARK_STATE);
  if (!first.found) {
    findings.push(`${label}: the mark (svg.landing-hero__mark) is not on the page`);
    return;
  }
  if (first.parts !== 10) findings.push(`${label}: the mark has ${first.parts} parts, expected 10`);
  await expectStatic(page, label, "with reduced motion", findings);
  const button = await json(page, BUTTON_STATE);
  if (button.found && button.display !== "none") findings.push(`${label}: the pause button is shown with reduced motion (display ${button.display})`);
  await sleep(1500);
  await expectStatic(page, label, "with reduced motion after 1.5 s", findings);
}

/** Without JavaScript the page shows the rollout once and then the unchanged mark, and no button. */
async function checkWithoutScript(browser, base, findings) {
  const label = "/ (motion, no JavaScript)";
  const page = await browser.newPage();
  await page.setViewport(1280, 900);
  await page.setColorScheme("light");
  await page.setReducedMotion(false);
  await page.send("Emulation.setScriptExecutionDisabled", { value: true });
  await page.goto(base + "/");
  await sleep(800);
  const running = await page.send("Runtime.evaluate", { expression: MARK_STATE, returnByValue: true });
  const first = JSON.parse(running.result.value);
  if (!first.found) {
    findings.push(`${label}: the mark (svg.landing-hero__mark) is not on the page`);
    return;
  }
  if (first.animations !== 10) findings.push(`${label}: ${first.animations} animations run on the mark, expected 10`);
  const limits = await page.send("Runtime.evaluate", {
    expression: `JSON.stringify([...document.querySelector('svg.landing-hero__mark').getAnimations({ subtree: true })].map((a) => a.effect.getComputedTiming().iterations))`,
    returnByValue: true,
  });
  if (JSON.parse(limits.result.value).some((n) => n !== 1)) findings.push(`${label}: an animation of the mark repeats without JavaScript`);
  await sleep(2600);
  const last = JSON.parse((await page.send("Runtime.evaluate", { expression: MARK_STATE, returnByValue: true })).result.value);
  if (moved(last) > 0) findings.push(`${label}: ${moved(last)} parts of the mark do not end on an identity transform`);
  const button = await page.send("Runtime.evaluate", { expression: BUTTON_STATE, returnByValue: true });
  const b = JSON.parse(button.result.value);
  if (b.found && b.display !== "none") findings.push(`${label}: the pause button is shown although nothing can pause the animation (it plays once)`);
}

/**
 * The mark of the landing page. Full motion (both appearances): the rollout runs on the ring and the nine dots (ten animations), the
 * first cycle ends on identity transforms (the unchanged mark) and holds it, the dots retract, a second cycle starts; the pause button
 * (a real button of at least 24x24 px with a label, a state, a focus ring and readable contrast) stops the motion and shows the static
 * mark, play starts a clean cycle; the loop does not run while the hero is out of view or the document is hidden. With
 * prefers-reduced-motion: reduce no animation runs and there is no button. Without JavaScript the rollout plays once.
 */
async function checkMotion(browser, base, findings) {
  for (const scheme of ["light", "dark"]) {
    await checkLoop(browser, base, scheme, findings);
    await checkReducedMotion(browser, base, scheme, findings);
  }
  await checkWithoutScript(browser, base, findings);
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
