// Captures the README screenshots from a seeded instance (see seed.py).
//
//   node docs/screenshots/shoot.mjs http://127.0.0.1:8090 docs/screenshots
//
// Drives a local Chrome over the DevTools protocol; needs Node 22+ (global
// WebSocket), ImageMagick (`magick`) and Chrome at CHROME or the macOS default path.
import { execFileSync, spawn } from "node:child_process";
import { mkdtempSync, readdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

const base = (process.argv[2] ?? "http://127.0.0.1:8090").replace(/\/$/, "");
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

// The recipe with a public link is the one shown on the recipe pages.
const recipes = await evaluate(`fetch('/recipes').then(r => r.text()).then(t => [...new Set([...t.matchAll(/href="(\\/recipes\\/[0-9a-f-]{36})"/g)].map(m => m[1]))])`);
let recipe, share;
for (const href of recipes) {
  const menu = await evaluate(`fetch('${href}/menu').then(r => r.text())`);
  const m = menu.match(/\/s\/[A-Za-z0-9_-]+/);
  if (m) { recipe = href; share = m[0]; break; }
}
if (!recipe) throw new Error("no recipe with a public link; run seed.py first");

// seed.py plans next week, so no day is in the past.
const d = new Date();
d.setDate(d.getDate() + ((8 - d.getDay()) % 7 || 7));
const nextMonday = `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;

// [file name, path, sizes, schemes]
// An optional last entry lists size-scheme pairs to skip.
const shots = [
  ["recipes", "/recipes", ["phone", "desktop"], ["light", "dark"], ["desktop-dark"]],
  ["recipe", recipe, ["phone", "desktop"], ["light"]],
  ["plan", `/plan?week=${nextMonday}`, ["phone", "desktop"], ["light"]],
  ["groceries", "/groceries", ["phone", "desktop"], ["light"]],
  ["cook", recipe + "/cook", ["phone"], ["light"]],
  ["settings", "/settings", ["phone", "desktop"], ["light"]],
  ["share", share, ["phone"], ["light"]],
];
const sizes = {
  phone: { width: 375, height: 812, deviceScaleFactor: 2, mobile: true },
  desktop: { width: 1280, height: 800, deviceScaleFactor: 1, mobile: false },
};
for (const scheme of ["light", "dark"]) {
  await send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-color-scheme", value: scheme }] });
  for (const [size, metrics] of Object.entries(sizes)) {
    await send("Emulation.setDeviceMetricsOverride", { ...metrics, screenWidth: metrics.width, screenHeight: metrics.height });
    for (const [name, path, onSizes, onSchemes, skip = []] of shots) {
      if (!onSizes.includes(size) || !onSchemes.includes(scheme) || skip.includes(`${size}-${scheme}`)) continue;
      // Two full rows of the library grid need a taller desktop window.
      const height = name === "recipes" && size === "desktop" ? 960 : metrics.height;
      await send("Emulation.setDeviceMetricsOverride", { ...metrics, height, screenWidth: metrics.width, screenHeight: height });
      await go(path);
      await sleep(500);
      const shot = await send("Page.captureScreenshot", { format: "png" });
      const file = name === "cook" || name === "share" ? `${name}-${size}.png` : `${name}-${size}-${scheme}.png`;
      writeFileSync(join(out, file), Buffer.from(shot.result.data, "base64"));
    }
  }
}
ws.close();
proc.kill();

// The screenshots are photo-heavy; 256 colours keep them small (needs ImageMagick).
for (const f of readdirSync(out).filter((f) => f.endsWith(".png"))) {
  execFileSync("magick", [join(out, f), "-colors", "256", "-define", "png:compression-level=9", "-strip", `PNG8:${join(out, f)}`]);
}
