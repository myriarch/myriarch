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
// every part ends on an identity transform (the unchanged mark), the mark is held, the dots retract and a second cycle starts; the mark is
// its own pause control (no visible button): a click, or Enter or Space on the focused mark, stops the motion and shows the static mark,
// the next one resumes at the retraction of a cycle without a jump and the ring does not grow again, aria-pressed follows, the focus ring
// shows for keyboard focus and not after a mouse click, and the loop stands still while the hero is out of view or the document is hidden;
// with prefers-reduced-motion: reduce nothing animates and the mark has no role and is not focusable; without JavaScript the rollout plays
// once and the mark is a plain image. The check takes about a minute.
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
    dots: [...mark.querySelectorAll('.dot')].map((p) => getComputedStyle(p).transform),
    ring: getComputedStyle(mark.querySelector('.ring')).transform,
    ringAnimations: mark.querySelector('.ring').getAnimations().length,
  });
})()`;

/**
 * The clock of the loop: the animation of the last dot of the mark. pos is the position within the cycle in ms (progress of the effect times
 * the duration); it includes the negative animation delay, which currentTime (ms) does not, so it tells where in the 10 s cycle the mark is.
 */
const LOOP_STATE = `(() => {
  const dot = document.querySelector('svg.landing-hero__mark .r3.c3');
  const animation = dot && dot.getAnimations()[0];
  if (!animation) return JSON.stringify({ running: false });
  const timing = animation.effect.getComputedTiming();
  return JSON.stringify({ running: true, iteration: timing.currentIteration, progress: timing.progress, pos: timing.progress === null ? null : timing.progress * Number(timing.duration), ms: Number(animation.currentTime), iterations: timing.iterations });
})()`;

/** The mark as a control: what a keyboard user, a pointer user and a screen reader get. */
const CONTROL_STATE = `(() => {
  const mark = document.querySelector('svg.landing-hero__mark');
  if (!mark) return JSON.stringify({ found: false });
  const box = mark.getBoundingClientRect();
  const style = getComputedStyle(mark);
  const root = getComputedStyle(document.documentElement);
  return JSON.stringify({
    found: true,
    visibleButton: document.querySelector('.landing-hero__motion, .landing-hero__art button') !== null,
    role: mark.getAttribute('role'),
    tabindexAttribute: mark.getAttribute('tabindex'),
    tabIndex: mark.tabIndex,
    label: mark.getAttribute('aria-label'),
    pressed: mark.getAttribute('aria-pressed'),
    ariaHidden: mark.getAttribute('aria-hidden'),
    cursor: style.cursor,
    x: box.x,
    y: box.y,
    width: box.width,
    height: box.height,
    focused: document.activeElement === mark,
    focusVisible: mark.matches(':focus-visible'),
    outlineStyle: style.outlineStyle,
    outlineWidth: parseFloat(style.outlineWidth),
    outlineColor: style.outlineColor,
    scrollY: window.scrollY,
    from: root.getPropertyValue('--landing-hero-from').trim(),
    to: root.getPropertyValue('--landing-hero-to').trim(),
  });
})()`;

const IDENTITY = new Set(["none", "matrix(1, 0, 0, 1, 0, 0)"]);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const json = async (page, expression) => JSON.parse(await page.evaluate(expression));
const moved = (state) => state.transforms.filter((t) => !IDENTITY.has(t)).length;
const movedDots = (state) => state.dots.filter((t) => !IDENTITY.has(t)).length;

/** Where a resumed loop starts: at the retraction that ends a cycle (9.4 s of 10 s, see $myr-retract-at in _landing.scss). */
const RESUME_FROM = 9350;
const CYCLE = 10000;

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

/** A click of the mouse in the middle of the mark (real input events, so that :focus-visible behaves as for a visitor). */
async function clickMark(page) {
  const c = await json(page, CONTROL_STATE);
  const x = c.x + c.width / 2;
  const y = c.y + c.height / 2;
  await page.send("Input.dispatchMouseEvent", { type: "mouseMoved", x, y });
  await page.send("Input.dispatchMouseEvent", { type: "mousePressed", x, y, button: "left", clickCount: 1 });
  await page.send("Input.dispatchMouseEvent", { type: "mouseReleased", x, y, button: "left", clickCount: 1 });
}

const KEYS = {
  Tab: { key: "Tab", code: "Tab", windowsVirtualKeyCode: 9 },
  Enter: { key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r" },
  Space: { key: " ", code: "Space", windowsVirtualKeyCode: 32, text: " " },
};

/** A key press with real input events: keyDown (with its text) and keyUp. */
async function pressKey(page, name, { shift = false } = {}) {
  const k = KEYS[name];
  const modifiers = shift ? 8 : 0;
  await page.send("Input.dispatchKeyEvent", { type: k.text ? "keyDown" : "rawKeyDown", modifiers, ...k });
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", modifiers, key: k.key, code: k.code, windowsVirtualKeyCode: k.windowsVirtualKeyCode });
}

/** Moves the focus to the mark with the Tab key from the top of the page; true when the mark has the focus. */
async function tabToMark(page) {
  await page.evaluate("(() => { if (document.activeElement) document.activeElement.blur(); window.scrollTo({ top: 0, behavior: 'instant' }); })()");
  for (let i = 0; i < 60; i++) {
    await pressKey(page, "Tab");
    if ((await json(page, CONTROL_STATE)).focused) return true;
  }
  return false;
}

/** The mark of a page that can animate: a control with a role, a name and a state, no visible button, a pointer, a big target. */
async function checkControl(page, label, findings, { pressed = "false" } = {}) {
  const c = await json(page, CONTROL_STATE);
  if (!c.found) {
    findings.push(`${label}: the mark (svg.landing-hero__mark) is not on the page`);
    return;
  }
  if (c.visibleButton) findings.push(`${label}: the page shows a pause button; the mark itself is the control`);
  if (c.role !== "button") findings.push(`${label}: the mark has the role ${JSON.stringify(c.role)}, expected "button"`);
  if (c.tabIndex !== 0 || c.tabindexAttribute !== "0") findings.push(`${label}: the mark is not reachable with the keyboard (tabindex ${JSON.stringify(c.tabindexAttribute)})`);
  if (c.ariaHidden !== null) findings.push(`${label}: the mark is a control but has aria-hidden=${JSON.stringify(c.ariaHidden)}`);
  if (!c.label || !/pause/i.test(c.label)) findings.push(`${label}: the mark has no aria-label that names the action (${JSON.stringify(c.label)})`);
  if (c.pressed !== pressed) findings.push(`${label}: aria-pressed of the mark is ${JSON.stringify(c.pressed)}, expected ${JSON.stringify(pressed)}`);
  if (c.cursor !== "pointer") findings.push(`${label}: the cursor over the mark is ${JSON.stringify(c.cursor)}, expected "pointer"`);
  if (c.width < 24 || c.height < 24) findings.push(`${label}: the target of the mark is ${c.width}x${c.height} px, expected at least 24x24`);
}

/** The focus ring of the keyboard focus: shown, 2 px or more, and a contrast of 3:1 on the hero (WCAG 1.4.11). */
function checkFocusRing(c, label, findings) {
  if (!c.focused) {
    findings.push(`${label}: the mark does not take the keyboard focus`);
    return;
  }
  if (!c.focusVisible || c.outlineStyle === "none" || c.outlineWidth < 2) findings.push(`${label}: the mark shows no focus ring for the keyboard (${JSON.stringify({ visible: c.focusVisible, style: c.outlineStyle, width: c.outlineWidth })})`);
  for (const bg of [c.from, c.to]) {
    const ratio = contrast(c.outlineColor, bg);
    if (ratio === null) findings.push(`${label}: cannot compute the contrast of the focus ring (${c.outlineColor} on ${bg})`);
    else if (ratio < 3) findings.push(`${label}: the focus ring has a contrast of ${ratio.toFixed(2)}:1 on ${bg}, expected 3:1`);
  }
}

/** The mark of a page that cannot animate (reduced motion, no JavaScript): a plain image, no role, not focusable, nothing to pause. */
async function checkPlainImage(page, label, findings, state = null) {
  const c = state ?? (await json(page, CONTROL_STATE));
  if (!c.found) return;
  if (c.visibleButton) findings.push(`${label}: the page shows a pause button although nothing can be paused`);
  if (c.role !== null) findings.push(`${label}: the mark has the role ${JSON.stringify(c.role)}, expected none (nothing to pause)`);
  if (c.tabindexAttribute !== null || c.tabIndex >= 0) findings.push(`${label}: the mark is focusable (tabindex ${JSON.stringify(c.tabindexAttribute)}, tabIndex ${c.tabIndex}), expected not`);
  if (c.pressed !== null) findings.push(`${label}: the mark has aria-pressed=${JSON.stringify(c.pressed)}, expected none`);
  if (c.ariaHidden !== "true") findings.push(`${label}: the mark is not hidden from assistive technology (aria-hidden ${JSON.stringify(c.ariaHidden)})`);
  if (c.cursor === "pointer") findings.push(`${label}: the cursor over the mark is a pointer although nothing can be paused`);
}

/** True once the mark is static: no animation runs on it and every part is on its identity transform. */
async function expectStatic(page, label, what, findings) {
  const state = await json(page, MARK_STATE);
  if (state.animations !== 0) findings.push(`${label}: ${state.animations} animations run on the mark ${what}, expected none`);
  if (moved(state) > 0) findings.push(`${label}: ${moved(state)} parts of the mark are not on an identity transform ${what}`);
}

/**
 * The mark plays again after a stop: the nine dots animate and the ring does not (it grew once, on load), and the loop is in the
 * retraction that ends a cycle (position 9.35 s to 10 s, the dots shrink from the full mark, so nothing jumps), not in the hold and not
 * at scale 0 of a cycle start. A mark that resumed in the hold would look like a static one, so at least one dot is off its identity
 * transform.
 */
async function expectPlaying(page, label, what, findings) {
  const state = await json(page, MARK_STATE);
  const loop = await json(page, LOOP_STATE);
  if (state.animations !== 9) findings.push(`${label}: ${state.animations} animations run on the mark ${what}, expected 9 (the nine dots)`);
  if (state.ringAnimations !== 0) findings.push(`${label}: the ring has ${state.ringAnimations} running animations ${what}, expected none (it grows once)`);
  if (!IDENTITY.has(state.ring)) findings.push(`${label}: the ring is not on its identity transform ${what} (${state.ring})`);
  if (!loop.running || loop.iteration !== 0 || loop.pos === null || loop.pos < RESUME_FROM || loop.pos >= CYCLE) {
    findings.push(`${label}: the mark does not resume at the retraction of a cycle ${what} (${JSON.stringify(loop)})`);
  }
  if (movedDots(state) === 0) findings.push(`${label}: no dot is moving ${what}, the mark looks like the static one`);
}

/** Full motion in one appearance: the loop, the click on the mark, the observers. About 25 s. */
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
  await checkControl(page, label, findings);

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
      if (identity && loop.pos > 2000 && holdStart === null) holdStart = loop.pos;
      if (!identity && holdStart !== null && holdEnd === null) holdEnd = loop.pos;
      if (!identity && loop.progress >= 0.94) sawRetract = true;
      if (holdStart === null && loop.pos > 2800) {
        findings.push(`${label}: the first rollout is not finished after ${Math.round(loop.pos)} ms (${moved(state)} parts still move)`);
        holdStart = loop.pos;
      }
    } else if (loop.iteration === 1) {
      secondCycle = true;
      if (moved(state) > 0) secondCycleMoved = true;
      if (loop.pos > 1500) break;
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

  // A click on the mark stops the motion and shows the static mark; the next click resumes at the retraction of a cycle (no jump). A
  // mouse click gives the mark the focus but no focus ring (:focus-visible).
  await clickMark(page);
  await sleep(250);
  await expectStatic(page, label, "after the mark was clicked", findings);
  const paused = await json(page, CONTROL_STATE);
  if (paused.pressed !== "true") findings.push(`${label}: aria-pressed is ${JSON.stringify(paused.pressed)} after the click, expected "true"`);
  if (!paused.focused) findings.push(`${label}: the mark does not take the focus when it is clicked`);
  if (paused.focusVisible || paused.outlineStyle !== "none") findings.push(`${label}: the mark shows a focus ring after a mouse click (${JSON.stringify({ visible: paused.focusVisible, style: paused.outlineStyle })})`);
  await sleep(1500);
  await expectStatic(page, label, "while paused", findings);
  await clickMark(page);
  await sleep(150);
  await expectPlaying(page, label, "after the second click", findings);
  const playing = await json(page, CONTROL_STATE);
  if (playing.pressed !== "false") findings.push(`${label}: aria-pressed is ${JSON.stringify(playing.pressed)} after the second click, expected "false"`);

  // Out of view: no motion; back in view: resumed at the retraction.
  await page.evaluate("window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' })");
  await sleep(600);
  await expectStatic(page, label, "while the hero is out of view", findings);
  await page.evaluate("window.scrollTo({ top: 0, behavior: 'instant' })");
  await sleep(400);
  await expectPlaying(page, label, "when the hero is back in view", findings);

  // A hidden document: no motion; visible again: resumed at the retraction.
  await page.evaluate("(() => { Object.defineProperty(document, 'hidden', { configurable: true, get: () => true }); document.dispatchEvent(new Event('visibilitychange')); })()");
  await sleep(200);
  await expectStatic(page, label, "while the document is hidden", findings);
  await page.evaluate("(() => { delete document.hidden; document.dispatchEvent(new Event('visibilitychange')); })()");
  await sleep(200);
  await expectPlaying(page, label, "when the document is visible again", findings);

  // A paused mark stays paused when the hero leaves the view and comes back.
  await clickMark(page);
  await page.evaluate("window.scrollTo({ top: document.documentElement.scrollHeight, behavior: 'instant' })");
  await sleep(400);
  await page.evaluate("window.scrollTo({ top: 0, behavior: 'instant' })");
  await sleep(400);
  await expectStatic(page, label, "when the hero came back after the visitor had paused it", findings);

  // Reduced motion switched on while the page is open: the mark stops being a control and stays still; switched off again, the control
  // is back with the choice of the visitor (paused).
  await page.setReducedMotion(true);
  await sleep(300);
  await expectStatic(page, label, "when reduced motion was switched on", findings);
  await checkPlainImage(page, `${label}, reduced motion switched on`, findings);
  await page.setReducedMotion(false);
  await sleep(300);
  await checkControl(page, `${label}, reduced motion switched off (still paused)`, findings, { pressed: "true" });
  await expectStatic(page, label, "after reduced motion was switched off while paused", findings);
}

/**
 * The keyboard: Tab reaches the mark, the focus ring shows (contrast 3:1), Enter and Space (on keyup after a keydown on the mark, like a native button, without
 * scrolling the page) pause and resume it, and aria-pressed follows.
 */
async function checkKeyboard(browser, base, scheme, findings) {
  const label = `/ (motion, keyboard, ${scheme})`;
  const page = await browser.newPage();
  await page.setViewport(1280, 900);
  await page.setColorScheme(scheme);
  await page.setReducedMotion(false);
  await page.goto(base + "/");
  await page.waitForIdle();
  await sleep(300);
  if (!(await tabToMark(page))) {
    findings.push(`${label}: the Tab key does not reach the mark`);
    return;
  }
  checkFocusRing(await json(page, CONTROL_STATE), label, findings);
  for (const key of ["Enter", "Space"]) {
    await pressKey(page, key);
    await sleep(250);
    await expectStatic(page, label, `after ${key} on the focused mark`, findings);
    const paused = await json(page, CONTROL_STATE);
    if (paused.pressed !== "true") findings.push(`${label}: aria-pressed is ${JSON.stringify(paused.pressed)} after ${key}, expected "true"`);
    if (paused.scrollY !== 0) findings.push(`${label}: the page scrolled by ${paused.scrollY} px on ${key}`);
    checkFocusRing(paused, `${label} (paused)`, findings);
    await pressKey(page, key);
    await sleep(150);
    await expectPlaying(page, label, `after ${key} on the focused mark again`, findings);
    const playing = await json(page, CONTROL_STATE);
    if (playing.pressed !== "false") findings.push(`${label}: aria-pressed is ${JSON.stringify(playing.pressed)} after ${key} again, expected "false"`);
  }
  // Space acts on keyup, like a native button: the key down alone changes nothing.
  await page.send("Input.dispatchKeyEvent", { type: "keyDown", ...KEYS.Space });
  await sleep(150);
  const down = await json(page, CONTROL_STATE);
  if (down.pressed !== "false") findings.push(`${label}: Space toggled the mark on key down (aria-pressed ${JSON.stringify(down.pressed)}), expected on key up`);
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: KEYS.Space.key, code: KEYS.Space.code, windowsVirtualKeyCode: KEYS.Space.windowsVirtualKeyCode });
  await sleep(150);
  const up = await json(page, CONTROL_STATE);
  if (up.pressed !== "true") findings.push(`${label}: Space did not toggle the mark on key up (aria-pressed ${JSON.stringify(up.pressed)})`);
  // A key up without a key down on the mark (the focus moved in between) changes nothing, like on a native button.
  await page.send("Input.dispatchKeyEvent", { type: "keyUp", key: KEYS.Space.key, code: KEYS.Space.code, windowsVirtualKeyCode: KEYS.Space.windowsVirtualKeyCode });
  await sleep(150);
  const stray = await json(page, CONTROL_STATE);
  if (stray.pressed !== "true") findings.push(`${label}: a Space key up without a key down toggled the mark (aria-pressed ${JSON.stringify(stray.pressed)})`);
}

/** With prefers-reduced-motion: reduce nothing animates, and the mark is a plain image: no role, not focusable, no pause button. */
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
  await checkPlainImage(page, label, findings);
  await clickMark(page);
  await sleep(300);
  await checkPlainImage(page, `${label}, after a click`, findings);
  await expectStatic(page, label, "with reduced motion after a click", findings);
  await sleep(1200);
  await expectStatic(page, label, "with reduced motion after 1.5 s", findings);
}

/** Without JavaScript the page shows the rollout once and then the unchanged mark, a plain image. */
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
  const control = await page.send("Runtime.evaluate", { expression: CONTROL_STATE, returnByValue: true });
  await checkPlainImage(page, label, findings, JSON.parse(control.result.value));
}

/**
 * The mark of the landing page. Full motion (both appearances): the rollout runs on the ring and the nine dots (ten animations), the
 * first cycle ends on identity transforms (the unchanged mark) and holds it, the dots retract, a second cycle starts; the mark is its own
 * pause control (role, name, state, a pointer, a big target): a click, or Enter or Space on the focused mark, stops the motion and shows
 * the static mark, the next one resumes at the retraction of a cycle; the focus ring shows for the keyboard and not after a mouse click;
 * the loop does not run while the hero is out of view or the document is hidden. With prefers-reduced-motion: reduce no animation runs
 * and the mark is a plain image (no role, not focusable). Without JavaScript the rollout plays once and the mark is a plain image.
 */
async function checkMotion(browser, base, findings) {
  for (const scheme of ["light", "dark"]) {
    await checkLoop(browser, base, scheme, findings);
    await checkKeyboard(browser, base, scheme, findings);
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
