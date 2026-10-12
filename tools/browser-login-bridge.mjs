#!/usr/bin/env node
/**
 * Browser login bridge for MiniMax Agent.
 *
 * MiniMax Agent (agent.minimax.io / agent.minimaxi.com) has no OAuth polling
 * endpoint: the login product is a JWT that the site keeps in localStorage
 * under the `_token` key. This process starts a *headful* Chrome on a private
 * profile, opens the target site, lets the operator log in by hand, and polls
 * `localStorage._token` over the Chrome DevTools Protocol. Once the token
 * appears it is handed back to the caller.
 *
 * Zero third-party dependencies: only Node builtins and the global WebSocket
 * (Node 22). The CDP plumbing is inlined on purpose so this file is
 * self-contained and does not import tools/render.mjs.
 *
 * HTTP surface (JSON everywhere), default http://127.0.0.1:8766
 *   GET    /health              -> {ok:true}
 *   POST   /login               -> {sessionId, target, chromePid}  (returns immediately)
 *   GET    /login/<sessionId>   -> {status:"pending"|"ok"|"timeout"|"error", ...}
 *   DELETE /login/<sessionId>   -> {cancelled:true}
 *
 * Options:
 *   --port <n>                       listen port (or BRIDGE_PORT)
 *   CHROME_BIN                       chrome executable (else the platform install
 *                                    paths for macOS/Windows/Linux, then google-chrome
 *                                    / chromium / chromium-browser on PATH)
 *   BRIDGE_LOGIN_TTL_SECONDS         login window, default 600
 *
 * Usage:
 *   node tools/browser-login-bridge.mjs --port 8766
 */

import http from "node:http";
import net from "node:net";
import { spawn } from "node:child_process";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";

const DEFAULT_TARGET = "https://agent.minimax.io";
const POLL_INTERVAL_MS = 1000;
// How long to wait for Chrome to publish its debugging endpoint before calling
// startup a failure. Chrome normally opens it in well under a second.
const CDP_WAIT_MS = 15000;
const TOKEN_KEY = "_token";

// Reads the JWT and the origin it was read from. localStorage is per-origin, so
// the origin tells the caller which site actually produced the token.
const TOKEN_EXPR = `(() => {
  let token = null;
  try { token = localStorage.getItem(${JSON.stringify(TOKEN_KEY)}); } catch (e) { token = null; }
  return {
    token: typeof token === "string" ? token : null,
    origin: (() => { try { return location.origin; } catch (e) { return ""; } })(),
  };
})()`;

// ---------------------------------------------------------------------------
// CLI / env
// ---------------------------------------------------------------------------

function argValue(name) {
  const args = process.argv.slice(2);
  for (let i = 0; i < args.length; i += 1) {
    if (args[i] === `--${name}`) return args[i + 1];
    if (args[i].startsWith(`--${name}=`)) return args[i].slice(name.length + 3);
  }
  return undefined;
}

function positiveInt(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) && n > 0 ? Math.floor(n) : fallback;
}

const PORT = positiveInt(argValue("port") ?? process.env.BRIDGE_PORT, 8766);
const HOST = "127.0.0.1";
const TTL_MS = positiveInt(process.env.BRIDGE_LOGIN_TTL_SECONDS, 600) * 1000;

// ---------------------------------------------------------------------------
// State
// ---------------------------------------------------------------------------

/** @type {Map<string, object>} sessionId -> session */
const sessions = new Map();
/** Live Chrome child processes, for cleanup on shutdown. */
const children = new Set();

// Persistent Chrome profile shared by every /login. Keeping it on disk means a
// user who logged in once is still logged in on the next browser-login request,
// so the bridge can grab the token immediately instead of asking them to log in
// again. Never deleted.
const PROFILE_DIR = path.join(os.tmpdir(), "minimax-login-profile");
// Chrome allows only one instance per profile: a second one is redirected to
// the running instance and exits at once. profileChrome records whichever
// process currently owns PROFILE_DIR (or the PROFILE_RESERVED placeholder while
// one is still starting) so a concurrent /login is rejected instead of racing.
const PROFILE_RESERVED = Symbol("profile-reserved");
let profileChrome = null;

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

function originOf(url) {
  try {
    return new URL(url).origin;
  } catch {
    return "";
  }
}

// ---------------------------------------------------------------------------
// Chrome discovery / process control
// ---------------------------------------------------------------------------

function which(cmd) {
  const dirs = (process.env.PATH || "").split(path.delimiter);
  for (const dir of dirs) {
    if (!dir) continue;
    const full = path.join(dir, cmd);
    try {
      fs.accessSync(full, fs.constants.X_OK);
      return full;
    } catch {
      /* keep looking */
    }
  }
  return null;
}

// isExecutable reports whether an absolute candidate path points at something
// we can actually run. Used for the platform install paths below, which are not
// on PATH and so cannot go through which().
function isExecutable(candidate) {
  try {
    fs.accessSync(candidate, fs.constants.X_OK);
    return true;
  } catch {
    return false;
  }
}

// expandEnv expands a Windows-style "%VAR%\rest" path against process.env.
// Node already receives environment variables with %VAR% unexpanded (unlike the
// shell, which would expand $VAR), so this is the only form we need to handle.
function expandEnv(candidate) {
  if (!candidate.includes("%")) return candidate;
  return candidate.replace(/%([^%]+)%/g, (whole, name) => {
    const value = process.env[name];
    return typeof value === "string" && value ? value : whole;
  });
}

// chromeCandidates returns the well-known install locations for the current
// platform. These are absolute paths because Chrome/Chromium/Edge are not
// normally on PATH on macOS or Windows. Empty/undefined entries (e.g. an
// unset %PROGRAMFILES%) are dropped by the caller.
function chromeCandidates() {
  if (process.platform === "darwin") {
    return [
      "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
      "/Applications/Chromium.app/Contents/MacOS/Chromium",
      "/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge",
    ];
  }
  if (process.platform === "win32") {
    return [
      "%ProgramFiles%\\Google\\Chrome\\Application\\chrome.exe",
      "%ProgramFiles(x86)%\\Google\\Chrome\\Application\\chrome.exe",
      "%LocalAppData%\\Google\\Chrome\\Application\\chrome.exe",
      "%ProgramFiles(x86)%\\Microsoft\\Edge\\Application\\msedge.exe",
    ];
  }
  // Linux: absolute paths that are sometimes not on PATH (e.g. minimal
  // containers, snap-only installs). PATH lookup still runs afterwards.
  return ["/usr/bin/google-chrome", "/usr/bin/chromium", "/snap/bin/chromium"];
}

// findChrome prefers CHROME_BIN, then the platform install paths, then the
// common PATH names. A CHROME_BIN that does not resolve falls through to the
// rest of the chain rather than aborting, so a stale value cannot hide a
// perfectly good Chrome that is otherwise discoverable. Returns null when
// nothing usable is found so the caller can answer with a clear error.
function findChrome() {
  const fromEnv = process.env.CHROME_BIN;
  if (fromEnv) {
    if (path.isAbsolute(fromEnv) || fromEnv.includes(path.sep)) {
      if (isExecutable(fromEnv)) return fromEnv;
    } else {
      const found = which(fromEnv);
      if (found) return found;
    }
  }
  for (const raw of chromeCandidates()) {
    const candidate = expandEnv(raw);
    // A %VAR% that did not expand (variable unset) would otherwise be probed
    // as a literal relative path; skip those.
    if (!candidate || candidate.includes("%") || !path.isAbsolute(candidate)) continue;
    if (isExecutable(candidate)) return candidate;
  }
  for (const name of ["google-chrome", "google-chrome-stable", "chromium", "chromium-browser"]) {
    const found = which(name);
    if (found) return found;
  }
  return null;
}

// freePort asks the kernel for an unused port and immediately releases it.
// There is a tiny race before Chrome binds it, which is acceptable here.
function freePort() {
  return new Promise((resolve, reject) => {
    const server = net.createServer();
    server.on("error", reject);
    server.listen(0, HOST, () => {
      const { port } = server.address();
      server.close(() => resolve(port));
    });
  });
}

// spawnChrome resolves once the OS confirms the process started, and rejects if
// the executable cannot be launched. The child leads its own process group so
// the whole Chrome tree (zygote, gpu, crashpad helpers) can be signalled at
// once — those helpers outlive the main process and keep the profile dir busy.
function spawnChrome(bin, args) {
  return new Promise((resolve, reject) => {
    const proc = spawn(bin, args, { stdio: "ignore", detached: true });
    const onError = (error) => {
      proc.removeListener("spawn", onSpawn);
      reject(error);
    };
    const onSpawn = () => {
      proc.removeListener("error", onError);
      resolve(proc);
    };
    proc.once("error", onError);
    proc.once("spawn", onSpawn);
  });
}

function signalTree(proc, signal) {
  // Negative pid targets the whole process group. Fall back to the single
  // process if the group is already gone.
  try {
    process.kill(-proc.pid, signal);
    return;
  } catch {
    /* fall through */
  }
  try {
    proc.kill(signal);
  } catch {
    /* already gone */
  }
}

function killChrome(proc) {
  if (!proc || proc.exitCode !== null || proc.signalCode !== null) return;
  signalTree(proc, "SIGTERM");
  const timer = setTimeout(() => {
    if (proc.exitCode === null && proc.signalCode === null) signalTree(proc, "SIGKILL");
  }, 3000);
  timer.unref?.();
  proc.once("exit", () => {
    clearTimeout(timer);
    children.delete(proc);
  });
}

// ---------------------------------------------------------------------------
// Minimal CDP client (inlined; see tools/render.mjs for the same shape)
// ---------------------------------------------------------------------------

function httpGetJson(url) {
  return new Promise((resolve, reject) => {
    const req = http.get(url, { timeout: 5000 }, (res) => {
      let data = "";
      res.setEncoding("utf8");
      res.on("data", (chunk) => {
        data += chunk;
      });
      res.on("end", () => {
        try {
          resolve(JSON.parse(data));
        } catch (error) {
          reject(error);
        }
      });
    });
    req.on("timeout", () => req.destroy(new Error("request timeout")));
    req.on("error", reject);
  });
}

async function waitForCdp(debugPort, timeoutMs) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    try {
      const info = await httpGetJson(`http://${HOST}:${debugPort}/json/version`);
      if (info && info.webSocketDebuggerUrl) return true;
    } catch {
      /* not up yet */
    }
    await sleep(200);
  }
  return false;
}

function attach(wsUrl) {
  return new Promise((resolve, reject) => {
    let ws;
    try {
      ws = new WebSocket(wsUrl);
    } catch (error) {
      reject(error);
      return;
    }
    const openTimer = setTimeout(() => {
      try {
        ws.close();
      } catch {
        /* ignore */
      }
      reject(new Error("websocket open timeout"));
    }, 10000);

    ws.addEventListener(
      "open",
      () => {
        clearTimeout(openTimer);
        let nextId = 0;
        let closed = false;
        const pending = new Map();

        ws.addEventListener("message", (event) => {
          let msg;
          try {
            msg = JSON.parse(event.data);
          } catch {
            return;
          }
          if (msg.id !== undefined && pending.has(msg.id)) {
            const { resolve: res, reject: rej } = pending.get(msg.id);
            pending.delete(msg.id);
            if (msg.error) rej(new Error(JSON.stringify(msg.error)));
            else res(msg.result);
          }
        });
        ws.addEventListener("close", () => {
          closed = true;
          for (const { reject: rej } of pending.values()) rej(new Error("websocket closed"));
          pending.clear();
        });
        ws.addEventListener("error", () => {
          /* a close event follows; handled above */
        });

        resolve({
          get closed() {
            return closed;
          },
          send(method, params = {}) {
            const id = (nextId += 1);
            return new Promise((res, rej) => {
              if (closed) {
                rej(new Error("websocket closed"));
                return;
              }
              pending.set(id, { resolve: res, reject: rej });
              ws.send(JSON.stringify({ id, method, params }));
            });
          },
          close() {
            try {
              ws.close();
            } catch {
              /* ignore */
            }
          },
        });
      },
      { once: true },
    );

    ws.addEventListener(
      "error",
      () => {
        clearTimeout(openTimer);
        reject(new Error("websocket failed"));
      },
      { once: true },
    );
  });
}

async function evaluate(cdp, expression) {
  const { result, exceptionDetails } = await cdp.send("Runtime.evaluate", {
    expression,
    returnByValue: true,
    awaitPromise: true,
  });
  if (exceptionDetails) {
    throw new Error(
      exceptionDetails.exception?.description || exceptionDetails.text || "evaluate failed",
    );
  }
  return result.value;
}

// connectCdp re-reads the target list and attaches to the current active page.
// Called on first use and again whenever the previous WebSocket died (a login
// redirect can invalidate it), so navigation does not stop the poll loop.
async function connectCdp(debugPort, origin) {
  let targets;
  try {
    targets = await httpGetJson(`http://${HOST}:${debugPort}/json`);
  } catch {
    return null;
  }
  if (!Array.isArray(targets)) return null;
  const pages = targets.filter((t) => t.type === "page" && t.webSocketDebuggerUrl);
  if (!pages.length) return null;
  const preferred = pages.find((t) => typeof t.url === "string" && t.url.startsWith(origin)) || pages[0];
  try {
    return await attach(preferred.webSocketDebuggerUrl);
  } catch {
    return null;
  }
}

// ---------------------------------------------------------------------------
// Session lifecycle
// ---------------------------------------------------------------------------

function stopPolling(session) {
  if (session.timer) {
    clearTimeout(session.timer);
    session.timer = null;
  }
  if (session.cdp) {
    try {
      session.cdp.close();
    } catch {
      /* ignore */
    }
    session.cdp = null;
  }
}

function destroySession(session) {
  stopPolling(session);
  const proc = session.chromeProc;
  session.chromeProc = null;
  session.userDataDir = null;
  // The profile is persistent login state, so it is deliberately left on disk
  // for the next /login to reuse. Only the Chrome process is stopped here.
  if (!proc) return;
  killChrome(proc);
}

function expire(session) {
  session.status = "timeout";
  session.error = "login window expired";
  destroySession(session);
  console.log(`[bridge] ${session.id} timeout`);
}

async function runPollLoop(session) {
  const origin = originOf(session.target);
  let ticks = 0;

  const tick = async () => {
    if (session.status !== "pending") return;
    if (Date.now() >= session.deadline) {
      expire(session);
      return;
    }
    ticks += 1;

    let value = null;
    try {
      if (!session.cdp || session.cdp.closed) {
        session.cdp = await connectCdp(session.debugPort, origin);
      }
      if (session.cdp) value = await evaluate(session.cdp, TOKEN_EXPR);
    } catch {
      // A dead socket (navigation / crash) just means reconnect next tick.
      if (session.cdp) {
        try {
          session.cdp.close();
        } catch {
          /* ignore */
        }
      }
      session.cdp = null;
      value = null;
    }

    if (session.status !== "pending") return;

    const token = value && typeof value.token === "string" ? value.token.trim() : "";
    if (token) {
      session.status = "ok";
      session.token = token;
      session.host = value.origin || origin;
      stopPolling(session);
      // Never log the token itself, only its length.
      console.log(`[bridge] ${session.id} ok host=${session.host} tokenLength=${token.length}`);
      return;
    }

    if (Date.now() >= session.deadline) {
      expire(session);
      return;
    }
    if (ticks % 10 === 0) {
      const left = Math.max(0, Math.round((session.deadline - Date.now()) / 1000));
      console.log(`[bridge] ${session.id} poll pending (${left}s left)`);
    }
    session.timer = setTimeout(tick, POLL_INTERVAL_MS);
  };

  session.timer = setTimeout(tick, 500);
}

// ---------------------------------------------------------------------------
// HTTP
// ---------------------------------------------------------------------------

function sendJson(res, status, obj) {
  if (res.writableEnded) return;
  const body = JSON.stringify(obj);
  res.writeHead(status, {
    "content-type": "application/json; charset=utf-8",
    "content-length": Buffer.byteLength(body),
    "cache-control": "no-store",
  });
  res.end(body);
}

function readBody(req, limit = 1_000_000) {
  return new Promise((resolve, reject) => {
    let data = "";
    req.setEncoding("utf8");
    req.on("data", (chunk) => {
      data += chunk;
      if (data.length > limit) {
        req.destroy();
        reject(new Error("request body too large"));
      }
    });
    req.on("end", () => resolve(data));
    req.on("error", reject);
  });
}

async function handleLogin(req, res) {
  let raw = "";
  try {
    raw = await readBody(req);
  } catch (error) {
    sendJson(res, 400, { error: `cannot read request body: ${error.message}` });
    return;
  }

  let parsed = {};
  if (raw.trim()) {
    try {
      parsed = JSON.parse(raw);
    } catch {
      sendJson(res, 400, { error: "invalid JSON body" });
      return;
    }
  }

  const target =
    typeof parsed.target === "string" && parsed.target.trim() ? parsed.target.trim() : DEFAULT_TARGET;
  let parsedTarget;
  try {
    parsedTarget = new URL(target);
  } catch {
    sendJson(res, 400, { error: "invalid target url" });
    return;
  }
  if (parsedTarget.protocol !== "https:" && parsedTarget.protocol !== "http:") {
    sendJson(res, 400, { error: "target must be an http(s) url" });
    return;
  }

  // Optional outbound proxy for Chrome, e.g. "http://127.0.0.1:7890" or
  // "socks5://127.0.0.1:1080". Passed through verbatim; when empty/absent no
  // --proxy-server flag is added and behaviour is unchanged.
  const proxy =
    typeof parsed.proxy === "string" && parsed.proxy.trim() ? parsed.proxy : "";

  const chrome = findChrome();
  if (!chrome) {
    sendJson(res, 500, {
      error:
        "Chrome not found; set CHROME_BIN or install google-chrome/chromium/chromium-browser on PATH",
    });
    return;
  }

  // One Chrome per profile: reject rather than spawn a second instance that
  // Chrome would silently redirect to the running one and then exit.
  if (profileChrome) {
    sendJson(res, 409, {
      error: "a browser login window is already open; finish or cancel it first",
    });
    return;
  }

  let debugPort;
  try {
    debugPort = await freePort();
  } catch (error) {
    sendJson(res, 500, { error: `could not allocate a debug port: ${error.message}` });
    return;
  }

  // Persistent profile shared across logins (never mkdtemp'd / deleted), so the
  // session survives and the user is not asked to log in every time.
  try {
    fs.mkdirSync(PROFILE_DIR, { recursive: true });
  } catch (error) {
    sendJson(res, 500, { error: `could not create the profile dir: ${error.message}` });
    return;
  }
  const userDataDir = PROFILE_DIR;

  const args = [
    `--remote-debugging-port=${debugPort}`,
    ...(proxy ? [`--proxy-server=${proxy}`] : []),
    `--user-data-dir=${userDataDir}`,
    "--no-first-run",
    "--no-default-browser-check",
    "--disable-extensions",
    target,
  ];

  // Reserve the profile synchronously for the whole startup window so a
  // concurrent /login cannot slip a second Chrome in before we have a pid.
  profileChrome = PROFILE_RESERVED;

  let chromeProc;
  try {
    chromeProc = await spawnChrome(chrome, args);
  } catch (error) {
    profileChrome = null;
    sendJson(res, 500, { error: `failed to start Chrome: ${error.message}` });
    return;
  }

  // Hand the reservation over to the real process and release it whenever
  // Chrome exits (cleanup, cancel, timeout, or crash) so the next login can use
  // the profile again.
  profileChrome = chromeProc;
  chromeProc.once("exit", () => {
    if (profileChrome === chromeProc) profileChrome = null;
  });

  children.add(chromeProc);
  chromeProc.once("exit", () => children.delete(chromeProc));

  const up = await waitForCdp(debugPort, CDP_WAIT_MS);
  if (!up) {
    killChrome(chromeProc);
    sendJson(res, 500, { error: "Chrome did not expose a debugging port in time" });
    return;
  }

  const id = globalThis.crypto.randomUUID();
  const session = {
    id,
    target,
    chromePid: chromeProc.pid,
    chromeProc,
    debugPort,
    userDataDir,
    status: "pending",
    token: null,
    host: null,
    error: null,
    cdp: null,
    timer: null,
    createdAt: Date.now(),
    deadline: Date.now() + TTL_MS,
  };
  sessions.set(id, session);

  console.log(
    `[bridge] ${id} started target=${target}${proxy ? ` proxy=${proxy}` : ""} chromePid=${chromeProc.pid} debugPort=${debugPort} ttl=${Math.round(TTL_MS / 1000)}s`,
  );

  runPollLoop(session).catch((error) => {
    if (session.status !== "pending") return;
    session.status = "error";
    session.error = error.message;
    destroySession(session);
    console.log(`[bridge] ${id} error ${error.message}`);
  });

  sendJson(res, 200, { sessionId: id, target, chromePid: chromeProc.pid });
}

function handleGetLogin(res, id) {
  const session = sessions.get(id);
  if (!session) {
    sendJson(res, 404, { error: "unknown session" });
    return;
  }
  switch (session.status) {
    case "pending":
      sendJson(res, 200, { status: "pending" });
      return;
    case "ok":
      sendJson(res, 200, { status: "ok", token: session.token, host: session.host });
      return;
    case "timeout":
      sendJson(res, 200, { status: "timeout", error: session.error || "login window expired" });
      return;
    default:
      sendJson(res, 200, { status: "error", error: session.error || "unknown error" });
  }
}

function handleDeleteLogin(res, id) {
  const session = sessions.get(id);
  if (!session) {
    sendJson(res, 404, { error: "unknown session" });
    return;
  }
  sessions.delete(id);
  session.status = "cancelled";
  destroySession(session);
  console.log(`[bridge] ${id} cancelled`);
  sendJson(res, 200, { cancelled: true });
}

const server = http.createServer((req, res) => {
  let url;
  try {
    url = new URL(req.url, `http://${HOST}`);
  } catch {
    sendJson(res, 400, { error: "bad request url" });
    return;
  }
  const method = req.method || "GET";
  const pathname = url.pathname;

  if (pathname === "/health") {
    if (method !== "GET") {
      sendJson(res, 405, { error: "method not allowed" });
      return;
    }
    sendJson(res, 200, { ok: true });
    return;
  }

  if (pathname === "/login") {
    if (method !== "POST") {
      sendJson(res, 405, { error: "method not allowed" });
      return;
    }
    handleLogin(req, res).catch((error) => {
      console.error(`[bridge] /login failed: ${error.message}`);
      sendJson(res, 500, { error: error.message });
    });
    return;
  }

  const match = pathname.match(/^\/login\/([^/]+)$/);
  if (match) {
    const id = decodeURIComponent(match[1]);
    if (method === "GET") {
      handleGetLogin(res, id);
      return;
    }
    if (method === "DELETE") {
      handleDeleteLogin(res, id);
      return;
    }
    sendJson(res, 405, { error: "method not allowed" });
    return;
  }

  sendJson(res, 404, { error: "not found" });
});

server.on("error", (error) => {
  console.error(`[bridge] server error: ${error.message}`);
  process.exit(1);
});

server.listen(PORT, HOST, () => {
  console.log(`[bridge] listening on http://${HOST}:${PORT}`);
  console.log(`[bridge] chrome=${findChrome() || "<not found>"} ttl=${Math.round(TTL_MS / 1000)}s`);
});

// ---------------------------------------------------------------------------
// Shutdown
// ---------------------------------------------------------------------------

let shuttingDown = false;
async function shutdown(signal) {
  if (shuttingDown) return;
  shuttingDown = true;
  console.log(`[bridge] ${signal} received, shutting down`);

  const procs = [];
  for (const session of sessions.values()) {
    session.status = "cancelled";
    if (session.timer) clearTimeout(session.timer);
    if (session.cdp) {
      try {
        session.cdp.close();
      } catch {
        /* ignore */
      }
    }
    if (session.chromeProc) procs.push(session.chromeProc);
  }
  sessions.clear();

  // Kill the Chrome processes and wait for them to actually exit (bounded). The
  // persistent profile directory is intentionally kept on disk.
  const exits = procs.map(
    (proc) =>
      new Promise((resolve) => {
        if (proc.exitCode !== null || proc.signalCode !== null) {
          resolve();
          return;
        }
        proc.once("exit", resolve);
        signalTree(proc, "SIGKILL");
      }),
  );
  await Promise.race([Promise.all(exits), sleep(5000)]);

  try {
    server.close();
  } catch {
    /* ignore */
  }
  process.exit(0);
}

process.on("SIGINT", () => shutdown("SIGINT"));
process.on("SIGTERM", () => shutdown("SIGTERM"));
