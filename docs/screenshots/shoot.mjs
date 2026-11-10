// Captures the README screenshots from a seeded instance (see seed.py).
//
//   node docs/screenshots/shoot.mjs http://localhost:8084 docs/screenshots
//
// Drives a local Chrome over the DevTools protocol; needs Node 22+ (global
// WebSocket) and Chrome at CHROME or the macOS default path.
import { spawn } from "node:child_process";
import { mkdtempSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const base = (process.argv[2] ?? "http://localhost:8084").replace(/\/$/, "");
const out = process.argv[3] ?? "docs/screenshots";
const chrome = process.env.CHROME ?? "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome";
const port = 9222;

const proc = spawn(chrome, [
  "--headless=new", `--remote-debugging-port=${port}`, "--hide-scrollbars", "--no-first-run",
  `--user-data-dir=${mkdtempSync(join(tmpdir(), "mise-shoot-"))}`, "about:blank",
], { stdio: "ignore" });

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
let targets;
for (let i = 0; i < 50 && !targets; i++) {
  try { targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json(); } catch { await sleep(200); }
}
const ws = new WebSocket(targets.find((t) => t.type === "page").webSocketDebuggerUrl);
await new Promise((r) => (ws.onopen = r));
let id = 0;
const pending = new Map();
ws.onmessage = (m) => {
  const msg = JSON.parse(m.data);
  if (msg.id && pending.has(msg.id)) { pending.get(msg.id)(msg); pending.delete(msg.id); }
};
const send = (method, params = {}) => new Promise((resolve) => {
  pending.set(++id, resolve);
  ws.send(JSON.stringify({ id, method, params }));
});
const evaluate = async (expression) => (await send("Runtime.evaluate", { expression, awaitPromise: true, returnByValue: true })).result?.result?.value;
const go = async (path) => { await send("Page.navigate", { url: base + path }); await sleep(1500); };

await send("Page.enable");
await go("/signin");
await evaluate(`(() => {
  document.querySelector('input[type=text]').value = 'demo';
  document.querySelector('input[type=password]').value = 'demo-password-123';
  document.querySelector('form').requestSubmit();
})()`);
await sleep(1500);

const recipes = await evaluate(`fetch('/recipes').then(r => r.text()).then(t => [...t.matchAll(/href="(\\/recipes\\/[0-9a-f-]{36})"/g)].map(m => m[1]))`);
const recipe = recipes[recipes.length - 1];

const pages = [
  ["recipes", "/recipes"],
  ["recipe", recipe],
  ["plan", "/plan"],
  ["groceries", "/groceries"],
  ["cook", recipe + "/cook"],
];
const sizes = {
  phone: { width: 375, height: 812, deviceScaleFactor: 2, mobile: true },
  desktop: { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false },
};
for (const scheme of ["light", "dark"]) {
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
  for (const [size, metrics] of Object.entries(sizes)) {
    await send("Emulation.setDeviceMetricsOverride", { ...metrics, screenWidth: metrics.width, screenHeight: metrics.height });
    for (const [name, path] of pages) {
      await go(path);
      const shot = await send("Page.captureScreenshot", { format: "png" });
      writeFileSync(join(out, `${name}-${size}-${scheme}.png`), Buffer.from(shot.result.data, "base64"));
    }
  }
}
ws.close();
proc.kill();
