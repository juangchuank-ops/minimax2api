#!/usr/bin/env node
/**
 * Theme and document-language adaptation check over the Chrome DevTools Protocol.
 *
 * The route and field checks talk HTTP; the render check drives the DOM. Neither
 * sees this class of defect, because the values involved live *outside* CSS:
 *
 *   - `<meta name="theme-color">` - the browser address bar / PWA title bar
 *   - `<html lang>` - what screen readers and the translate prompt read
 *
 * Both are set correctly for the default in index.html, and both have to be
 * resynced when the app's own state disagrees with that default. A static
 * `prefers-color-scheme` media query cannot see an in-app Light/Dark choice, and
 * a hardcoded lang cannot see the language switcher.
 *
 * The check therefore drives the *opposite* of the default on purpose. Testing
 * "OS and app agree" would pass even with the sync removed, because the static
 * markup and the JS would produce the same answer and the bug would be invisible.
 *
 * Start Chrome first:
 *   chrome --headless=new --disable-gpu --remote-debugging-port=9222 \
 *          --user-data-dir=<temp dir> about:blank
 *
 * Usage:
 *   node tools/theme.mjs [base] [cdp]
 */

const BASE = (process.argv[2] || "http://127.0.0.1:8080").replace(/\/$/, "");
const CDP = (process.argv[3] || "http://127.0.0.1:9222").replace(/\/$/, "");

// Must mirror `--background` in frontend/src/index.css.
const DARK_BG = "#070707";
const LIGHT_BG = "#fdfdfd";

const THEME_KEY = "theme";
const LANGUAGE_KEY = "minimax2api:language";

const problems = [];

function check(label, actual, expected) {
  const ok = JSON.stringify(actual) === JSON.stringify(expected);
  console.log(
    `  [${ok ? "ok" : "!!"}] ${label} · got ${JSON.stringify(actual)}` +
      (ok ? "" : ` want ${JSON.stringify(expected)}`),
  );
  if (!ok) problems.push(`${label}: got ${JSON.stringify(actual)}, want ${JSON.stringify(expected)}`);
}

const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));

async function newTarget() {
  const res = await fetch(`${CDP}/json/new?about:blank`, { method: "PUT" });
  if (!res.ok) throw new Error(`cannot create target: HTTP ${res.status}`);
  return res.json();
}

async function attach(wsUrl) {
  const ws = new WebSocket(wsUrl);
  await new Promise((resolve, reject) => {
    ws.addEventListener("open", resolve, { once: true });
    ws.addEventListener("error", () => reject(new Error("websocket failed")), {
      once: true,
    });
  });

  let nextId = 0;
  const pending = new Map();

  ws.addEventListener("message", (event) => {
    const msg = JSON.parse(event.data);
    if (msg.id !== undefined && pending.has(msg.id)) {
      const { resolve, reject } = pending.get(msg.id);
      pending.delete(msg.id);
      if (msg.error) reject(new Error(JSON.stringify(msg.error)));
      else resolve(msg.result);
    }
  });

  const send = (method, params = {}) =>
    new Promise((resolve, reject) => {
      const id = ++nextId;
      pending.set(id, { resolve, reject });
      ws.send(JSON.stringify({ id, method, params }));
    });

  async function evaluate(expression) {
    const res = await send("Runtime.evaluate", {
      expression,
      returnByValue: true,
      awaitPromise: true,
    });
    if (res.exceptionDetails) {
      throw new Error(res.exceptionDetails.exception?.description || "evaluate threw");
    }
    return res.result.value;
  }

  async function waitFor(expression, label, timeoutMs = 20000) {
    const deadline = Date.now() + timeoutMs;
    while (Date.now() < deadline) {
      try {
        if (await evaluate(expression)) return;
      } catch {
        // A navigation is in flight and the execution context is briefly gone.
      }
      await sleep(150);
    }
    throw new Error(`timed out waiting for ${label}`);
  }

  return { send, evaluate, waitFor, close: () => ws.close() };
}

/**
 * Set both storage keys, then reload and wait for React to mount.
 *
 * Order matters: storage can only be written once the page is on the target
 * origin, so the first navigation has to happen before anything is stored. A
 * failed navigation leaves the page on about:blank, whose origin is opaque and
 * rejects localStorage outright - which is why `waitFor` on the origin comes
 * first and the error below says "is the server up?".
 */
async function load(session, { theme, language, scheme }) {
  await session.send("Emulation.setEmulatedMedia", {
    features: [{ name: "prefers-color-scheme", value: scheme }],
  });
  await session.send("Page.navigate", { url: `${BASE}/` });
  await session.waitFor(
    `location.origin === ${JSON.stringify(BASE)}`,
    `the page to reach ${BASE} (is the server up?)`,
  );
  await session.evaluate(
    `localStorage.setItem(${JSON.stringify(THEME_KEY)}, ${JSON.stringify(theme)});
     localStorage.setItem(${JSON.stringify(LANGUAGE_KEY)}, ${JSON.stringify(language)});`,
  );
  await session.send("Page.reload", { ignoreCache: true });
  await session.waitFor(
    `document.querySelector('#root') && document.querySelector('#root').children.length > 0`,
    "the console to mount",
  );
  // Both syncs run in effects / on an i18next event, so let React settle.
  await sleep(700);
}

const READ_STATE = `(() => ({
  htmlLang: document.documentElement.lang,
  htmlClass: document.documentElement.className,
  metas: [...document.querySelectorAll('meta[name="theme-color"]')]
           .map(m => m.content),
  darkMQ: matchMedia('(prefers-color-scheme: dark)').matches,
  lightMQ: matchMedia('(prefers-color-scheme: light)').matches,
}))()`;

async function main() {
  console.log("MiniMax2API theme / language adaptation check");
  console.log(`base = ${BASE}`);
  console.log(`cdp  = ${CDP}\n`);

  const target = await newTarget();
  const session = await attach(target.webSocketDebuggerUrl);
  await session.send("Page.enable");
  await session.send("Runtime.enable");

  // Every case pins the OS preference to the OPPOSITE of the app's choice. That
  // is the only arrangement where a missing sync is detectable.
  console.log("theme-color follows the app theme, not the OS");
  const cases = [
    { theme: "dark", scheme: "light", want: DARK_BG, cls: "dark" },
    { theme: "light", scheme: "dark", want: LIGHT_BG, cls: "light" },
    { theme: "system", scheme: "dark", want: DARK_BG, cls: "dark" },
    { theme: "system", scheme: "light", want: LIGHT_BG, cls: "light" },
  ];
  for (const item of cases) {
    await load(session, { theme: item.theme, language: "zh-CN", scheme: item.scheme });
    const state = await session.evaluate(READ_STATE);
    const label = `app=${item.theme} os=${item.scheme}`;
    check(`${label} · <html> class`, state.htmlClass.includes(item.cls), true);
    check(`${label} · both metas`, state.metas, [item.want, item.want]);
    // Exactly one media query must match, otherwise "both metas agree" would be
    // satisfied without the browser having anything to choose from.
    check(`${label} · exactly one scheme matches`, state.darkMQ !== state.lightMQ, true);
  }

  console.log("\n<html lang> follows the language switcher");
  await load(session, { theme: "dark", language: "en", scheme: "dark" });
  check("language=en · <html lang>", (await session.evaluate(READ_STATE)).htmlLang, "en");

  await load(session, { theme: "dark", language: "zh-CN", scheme: "dark" });
  check("language=zh-CN · <html lang>", (await session.evaluate(READ_STATE)).htmlLang, "zh-CN");

  session.close();

  console.log();
  if (problems.length) {
    console.log(`THEME PROBLEMS (${problems.length}):`);
    for (const item of problems) console.log(`  - ${item}`);
    return 1;
  }
  console.log("THEME OK - theme-color and <html lang> both follow the app");
  return 0;
}

main()
  .then((code) => process.exit(code))
  .catch((error) => {
    console.error("theme check crashed:", error);
    process.exit(1);
  });
