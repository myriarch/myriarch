// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Takes the screenshots of the landing page from the mock mode of the web UI (neutral, deterministic data: ADR-0022).
//
//   node website/scripts/screenshots.mjs [--out website/assets/images/screenshots] [--only dashboard,agents] [--skip-build]
//
// It builds the mock bundle (`pnpm build:mock` in web/), serves it with `vite preview` on 127.0.0.1:4173, opens the pages in a
// headless Chrome (1440x900, scale factor 1) in the light and in the dark colour scheme, and writes one PNG per page and scheme:
// <name>-light.png and <name>-dark.png. Everything it starts (vite preview, Chrome, the temporary profile) is stopped at the end.
// Node.js 22 or newer; Chrome as described in cdp.mjs. Nothing leaves the machine: the mock UI answers all requests itself.

import { execFileSync, spawn } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import { dirname, join, resolve } from "node:path";
import net from "node:net";
import { fileURLToPath } from "node:url";
import { launch } from "./cdp.mjs";

const here = dirname(fileURLToPath(import.meta.url));
const repoRoot = resolve(here, "..", "..");
const webDir = join(repoRoot, "web");

const HOST = "127.0.0.1";
const PORT = 4173;
const BASE = `http://${HOST}:${PORT}`;
const VIEWPORT = { width: 1440, height: 900 };
const SETTLE_MS = 1500;
// Mantine reads the colour scheme from this key (STORAGE_KEY_PREFIX + 'color-scheme' in web/src/brand.ts).
const COLOR_SCHEME_KEY = "myriarch.color-scheme";
const LANG_KEY = "myriarch.lang";

/** The pages of the landing page. `resolvePath` asks the page for the route when it depends on the mock data. */
const PAGES = [
  { name: "dashboard", path: "/" },
  { name: "agents", path: "/agents" },
  // The "Collector version" card: the frame that Git declares, and the update that the UI chooses within it.
  { name: "fleet-version", path: "/fleets/k8s-gateway" },
  {
    name: "rollout",
    // The id of a running rollout comes from the mock database, so a change of the fixtures does not break the script.
    async resolvePath(page) {
      const id = await page.evaluate(`(() => {
        const db = window.__myriarchMock && window.__myriarchMock.db;
        const running = db && db.rollouts.find((r) => r.rollout.status === 'running');
        return running ? running.rollout.id : null;
      })()`);
      if (!id) throw new Error("the mock database has no running rollout");
      return `/rollouts/${id}`;
    },
  },
];

function parseArgs(argv) {
  const opts = { out: join(repoRoot, "website", "assets", "images", "screenshots"), only: null, build: true };
  for (let i = 0; i < argv.length; i++) {
    const a = argv[i];
    if (a === "--out" && argv[i + 1]) opts.out = resolve(argv[++i]);
    else if (a === "--only" && argv[i + 1]) opts.only = new Set(argv[++i].split(","));
    else if (a === "--skip-build") opts.build = false;
    else throw new Error(`unknown argument ${a}\nusage: node screenshots.mjs [--out DIR] [--only NAMES] [--skip-build]`);
  }
  return opts;
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

function portIsFree(port) {
  return new Promise((resolvePort) => {
    const server = net.createServer();
    server.once("error", () => resolvePort(false));
    server.once("listening", () => server.close(() => resolvePort(true)));
    server.listen(port, HOST);
  });
}

async function waitForServer(url, child, timeoutMs = 30000) {
  const start = Date.now();
  while (Date.now() - start < timeoutMs) {
    if (child.exitCode !== null) throw new Error(`vite preview exited with status ${child.exitCode}`);
    try {
      const response = await fetch(url);
      if (response.ok) return;
    } catch {
      // not up yet
    }
    await sleep(200);
  }
  throw new Error(`vite preview did not answer on ${url}`);
}

function startPreview() {
  // A process group of its own, so that the whole tree (pnpm, node, vite) can be stopped.
  const child = spawn(
    "pnpm",
    ["exec", "vite", "preview", "--mode", "mock", "--outDir", "dist-mock", "--host", HOST, "--port", String(PORT), "--strictPort"],
    { cwd: webDir, stdio: "ignore", detached: true },
  );
  child.on("error", () => {});
  return child;
}

function stopPreview(child) {
  if (!child || child.exitCode !== null) return;
  try {
    process.kill(-child.pid, "SIGTERM");
  } catch {
    child.kill("SIGTERM");
  }
}

/** Waits for the network to be quiet. Pages with a live stream never are: after the time-out the fixed delay has to do. */
async function settle(page) {
  try {
    await page.waitForIdle({ timeoutMs: 6000 });
  } catch {
    // live stream: continue
  }
  await sleep(SETTLE_MS);
}

async function shoot(browser, scheme, spec, outDir) {
  const page = await browser.newPage();
  await page.setViewport(VIEWPORT.width, VIEWPORT.height, { deviceScaleFactor: 1 });
  await page.setColorScheme(scheme);
  await page.addInitScript(
    `try { window.localStorage.setItem(${JSON.stringify(COLOR_SCHEME_KEY)}, ${JSON.stringify(scheme)}); window.localStorage.setItem(${JSON.stringify(LANG_KEY)}, "en"); } catch (e) {}`,
  );
  let path = spec.path;
  if (!path) {
    // The route depends on the mock data: open the dashboard first and ask the database.
    await page.goto(`${BASE}/`);
    await settle(page);
    path = await spec.resolvePath(page);
  }
  await page.goto(`${BASE}${path}`);
  await settle(page);
  const png = await page.screenshot();
  const file = join(outDir, `${spec.name}-${scheme}.png`);
  writeFileSync(file, png);
  console.log(`wrote ${file} (${png.length} bytes)`);
}

async function main() {
  const opts = parseArgs(process.argv.slice(2));
  mkdirSync(opts.out, { recursive: true });
  if (opts.build) {
    execFileSync("pnpm", ["install", "--frozen-lockfile"], { cwd: webDir, stdio: "inherit", env: { ...process.env, CI: "true" } });
    execFileSync("pnpm", ["build:mock"], { cwd: webDir, stdio: "inherit" });
  }
  if (!(await portIsFree(PORT))) throw new Error(`port ${PORT} is in use`);

  let preview;
  let browser;
  try {
    preview = startPreview();
    await waitForServer(`${BASE}/`, preview);
    browser = await launch();
    for (const spec of PAGES) {
      if (opts.only && !opts.only.has(spec.name)) continue;
      for (const scheme of ["light", "dark"]) await shoot(browser, scheme, spec, opts.out);
    }
  } finally {
    if (browser) await browser.close();
    stopPreview(preview);
  }
}

main().then(
  () => process.exit(0),
  (error) => {
    console.error(error.message);
    process.exit(1);
  },
);
