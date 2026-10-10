// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// A small Chrome DevTools Protocol client without dependencies, for the browser checks of the website (check-requests.mjs, screenshots).
// It starts a headless Chrome with a temporary profile and a debugging port that the system chooses, and exposes just what the
// checks need:
//
//   import { launch } from "./cdp.mjs";
//   const browser = await launch();                       // CHROME selects the executable
//   const page = await browser.newPage();
//   page.onRequest((r) => console.log(r.url));            // every request of the page, its frames and its workers
//   page.onConsole((m) => console.log(m.level, m.text));  // console errors, uncaught exceptions and browser messages (CSP violations)
//   await page.setViewport(1280, 800);
//   await page.setColorScheme("dark");                    // "light" | "dark"
//   await page.setReducedMotion(true);                    // prefers-reduced-motion: reduce (false: no-preference)
//   await page.goto("http://127.0.0.1:1313/");
//   await page.waitForIdle();                             // no request in flight for a moment
//   await page.evaluate("document.title");                // runs in the page, returns the value; addInitScript runs before the page
//   const png = await page.screenshot({ fullPage: true }); // a Buffer
//   await browser.close();
//
// Node.js 22 or newer (the global WebSocket). The executable: the environment variable CHROME, else Google Chrome on macOS, else
// google-chrome, chromium or chromium-browser from PATH. The profile directory is removed on close. Nothing is sent anywhere
// except to the pages that the caller opens.

import { spawn } from "node:child_process";
import { existsSync, mkdtempSync, readFileSync, rmSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const MAC_CHROME = "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";

function findChrome() {
  if (process.env.CHROME) return process.env.CHROME;
  if (existsSync(MAC_CHROME)) return MAC_CHROME;
  const path = (process.env.PATH || "").split(":");
  for (const name of ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"]) {
    for (const dir of path) {
      if (dir && existsSync(join(dir, name))) return join(dir, name);
    }
  }
  throw new Error("no Chrome found: set CHROME to the executable of Google Chrome or Chromium");
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

class Connection {
  constructor(ws) {
    this.ws = ws;
    this.nextId = 1;
    this.pending = new Map();
    this.handlers = [];
    ws.addEventListener("message", (event) => this.onMessage(JSON.parse(event.data)));
    ws.addEventListener("close", () => {
      for (const { reject } of this.pending.values()) reject(new Error("the connection to Chrome was closed"));
      this.pending.clear();
    });
  }

  onMessage(message) {
    if (message.id !== undefined) {
      const entry = this.pending.get(message.id);
      if (!entry) return;
      this.pending.delete(message.id);
      if (message.error) entry.reject(new Error(`${entry.method}: ${message.error.message}`));
      else entry.resolve(message.result);
      return;
    }
    for (const handler of this.handlers) handler(message);
  }

  send(method, params = {}, sessionId = undefined) {
    const id = this.nextId++;
    const payload = { id, method, params };
    if (sessionId) payload.sessionId = sessionId;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject, method });
      this.ws.send(JSON.stringify(payload));
    });
  }

  onEvent(handler) {
    this.handlers.push(handler);
  }
}

class Page {
  constructor(connection, sessionId) {
    this.connection = connection;
    this.sessionId = sessionId;
    this.requestHandlers = [];
    this.consoleHandlers = [];
    this.inflight = new Set();
    this.lastActivity = Date.now();
    this.loadWaiters = [];
    this.mediaFeatures = new Map();
    connection.onEvent((message) => this.onEvent(message));
  }

  async init() {
    await this.send("Page.enable");
    await this.send("Network.enable");
    await this.send("Runtime.enable");
    await this.send("Log.enable");
    // Workers and frames of the page run in their own sessions; watch their requests too.
    await this.send("Target.setAutoAttach", { autoAttach: true, waitForDebuggerOnStart: false, flatten: true });
  }

  send(method, params = {}) {
    return this.connection.send(method, params, this.sessionId);
  }

  onEvent(message) {
    // Events of the page session and of the sessions that it auto-attached carry the sessionId of their own session.
    if (message.method === "Target.attachedToTarget" && message.sessionId === this.sessionId) {
      const child = message.params.sessionInfo?.sessionId ?? message.params.sessionId;
      if (child) {
        this.children = this.children || new Set();
        this.children.add(child);
        this.connection.send("Network.enable", {}, child).catch(() => {});
        this.connection.send("Runtime.runIfWaitingForDebugger", {}, child).catch(() => {});
      }
      return;
    }
    const mine = message.sessionId === this.sessionId || (this.children && this.children.has(message.sessionId));
    if (!mine) return;
    switch (message.method) {
      case "Network.requestWillBeSent": {
        const { requestId, request } = message.params;
        // Only network requests count as in flight; blob: and data: requests (the search worker of the API reference) never report
        // loadingFinished and would keep waitForIdle waiting.
        if (/^https?:/.test(request.url)) this.inflight.add(`${message.sessionId}:${requestId}`);
        this.lastActivity = Date.now();
        for (const handler of this.requestHandlers) handler({ url: request.url, method: request.method, type: message.params.type });
        break;
      }
      case "Network.loadingFinished":
      case "Network.loadingFailed":
        this.inflight.delete(`${message.sessionId}:${message.params.requestId}`);
        this.lastActivity = Date.now();
        break;
      case "Log.entryAdded": {
        const { level, text, source } = message.params.entry;
        for (const handler of this.consoleHandlers) handler({ level, text, source });
        break;
      }
      case "Runtime.exceptionThrown": {
        const details = message.params.exceptionDetails;
        const text = details.exception?.description ?? details.text;
        for (const handler of this.consoleHandlers) handler({ level: "error", text, source: "exception" });
        break;
      }
      case "Runtime.consoleAPICalled":
        if (message.params.type === "error") {
          const text = message.params.args.map((a) => a.value ?? a.description ?? "").join(" ");
          for (const handler of this.consoleHandlers) handler({ level: "error", text, source: "console" });
        }
        break;
      case "Page.loadEventFired":
        for (const resolve of this.loadWaiters.splice(0)) resolve();
        break;
      default:
        break;
    }
  }

  /** Calls handler({url, method, type}) for every request of the page, its frames and its workers. */
  onRequest(handler) {
    this.requestHandlers.push(handler);
  }

  /** Calls handler({level, text, source}) for console errors, uncaught exceptions and browser log entries (for example CSP violations). */
  onConsole(handler) {
    this.consoleHandlers.push(handler);
  }

  async setViewport(width, height, { deviceScaleFactor = 1, mobile = false } = {}) {
    await this.send("Emulation.setDeviceMetricsOverride", { width, height, deviceScaleFactor, mobile });
  }

  /** Emulates one media feature (for example prefers-color-scheme); the features set before stay in force. */
  async setMediaFeature(name, value) {
    this.mediaFeatures.set(name, value);
    const features = [...this.mediaFeatures].map(([n, v]) => ({ name: n, value: v }));
    await this.send("Emulation.setEmulatedMedia", { features });
  }

  /** scheme: "light" or "dark" (prefers-color-scheme). */
  async setColorScheme(scheme) {
    await this.setMediaFeature("prefers-color-scheme", scheme);
  }

  /** reduce = true emulates prefers-reduced-motion: reduce, false emulates no-preference. */
  async setReducedMotion(reduce) {
    await this.setMediaFeature("prefers-reduced-motion", reduce ? "reduce" : "no-preference");
  }

  async goto(url, { timeoutMs = 30000 } = {}) {
    const loaded = new Promise((resolve) => this.loadWaiters.push(resolve));
    const result = await this.send("Page.navigate", { url });
    if (result.errorText) throw new Error(`navigation to ${url} failed: ${result.errorText}`);
    await Promise.race([loaded, sleep(timeoutMs).then(() => { throw new Error(`timeout loading ${url}`); })]);
  }

  /** Resolves when no request has been in flight for quietMs (default 500), or rejects after timeoutMs. */
  async waitForIdle({ quietMs = 500, timeoutMs = 15000 } = {}) {
    const start = Date.now();
    this.lastActivity = Date.now();
    while (Date.now() - start < timeoutMs) {
      if (this.inflight.size === 0 && Date.now() - this.lastActivity >= quietMs) return;
      await sleep(50);
    }
    throw new Error(`the page was not idle after ${timeoutMs} ms`);
  }

  /** Runs a script in every document of the page before any script of the page does. */
  async addInitScript(source) {
    await this.send("Page.addScriptToEvaluateOnNewDocument", { source });
  }

  /** Evaluates an expression in the page and returns its value (promises are awaited). */
  async evaluate(expression) {
    const { result, exceptionDetails } = await this.send("Runtime.evaluate", { expression, returnByValue: true, awaitPromise: true });
    if (exceptionDetails) throw new Error(`evaluation failed: ${exceptionDetails.exception?.description ?? exceptionDetails.text}`);
    return result.value;
  }

  /** Returns a PNG as a Buffer. fullPage captures the whole document. */
  async screenshot({ fullPage = false } = {}) {
    const params = { format: "png" };
    if (fullPage) {
      const { cssContentSize } = await this.send("Page.getLayoutMetrics");
      params.captureBeyondViewport = true;
      params.clip = { x: 0, y: 0, width: Math.ceil(cssContentSize.width), height: Math.ceil(cssContentSize.height), scale: 1 };
    }
    const { data } = await this.send("Page.captureScreenshot", params);
    return Buffer.from(data, "base64");
  }
}

class Browser {
  constructor(process, connection, profile) {
    this.process = process;
    this.connection = connection;
    this.profile = profile;
  }

  async newPage() {
    const { targetId } = await this.connection.send("Target.createTarget", { url: "about:blank" });
    const { sessionId } = await this.connection.send("Target.attachToTarget", { targetId, flatten: true });
    const page = new Page(this.connection, sessionId);
    await page.init();
    return page;
  }

  async close() {
    try {
      await this.connection.send("Browser.close");
    } catch {
      // already gone
    }
    this.process.kill("SIGTERM");
    await new Promise((resolve) => {
      if (this.process.exitCode !== null) resolve();
      else this.process.once("exit", resolve);
      setTimeout(resolve, 3000);
    });
    rmSync(this.profile, { recursive: true, force: true });
  }
}

/** Starts Chrome and returns a Browser. */
export async function launch() {
  if (typeof WebSocket === "undefined") throw new Error("Node.js 22 or newer is required (global WebSocket)");
  const profile = mkdtempSync(join(tmpdir(), "myriarch-cdp-"));
  const args = [
    "--headless=new",
    `--user-data-dir=${profile}`,
    "--remote-debugging-port=0",
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-extensions",
    "--disable-background-networking",
    "--disable-component-update",
    "--disable-sync",
    "--no-pings",
    "--hide-scrollbars",
    "--mute-audio",
    "about:blank",
  ];
  const child = spawn(findChrome(), args, { stdio: "ignore" });
  const portFile = join(profile, "DevToolsActivePort");
  const deadline = Date.now() + 20000;
  while (!existsSync(portFile)) {
    if (child.exitCode !== null) throw new Error(`Chrome exited with status ${child.exitCode}`);
    if (Date.now() > deadline) {
      child.kill("SIGTERM");
      throw new Error("Chrome did not open its debugging port");
    }
    await sleep(50);
  }
  await sleep(50);
  const [port, path] = readFileSync(portFile, "utf8").trim().split("\n");
  const ws = new WebSocket(`ws://127.0.0.1:${port}${path}`);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", () => reject(new Error("cannot connect to the debugging port of Chrome")), { once: true });
  });
  return new Browser(child, new Connection(ws), profile);
}
