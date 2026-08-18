#!/usr/bin/env node
// Capture same-page grok.com seed + curves + official HEX for statsig-hex-repair.
// Does not print SSO / proxy passwords. Do not commit secrets files.
import { createRequire } from "module";
import fs from "fs";
import path from "path";
import { fileURLToPath } from "url";

const require = createRequire(import.meta.url);
const here = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(here, "../../../..");

function loadSecrets() {
  const file = process.env.STATSIG_SECRETS || "/tmp/g2a-local-test/browser.secrets.json";
  if (fs.existsSync(file)) {
    return JSON.parse(fs.readFileSync(file, "utf8"));
  }
  const sso = process.env.GROK_SSO || "";
  if (!sso) {
    throw new Error("missing STATSIG_SECRETS or GROK_SSO");
  }
  return {
    local16_sso: sso,
    proxy_server: process.env.PROXY_SERVER || "",
    proxy_username_tmpl: process.env.PROXY_USERNAME_TMPL || "",
    proxy_password: process.env.PROXY_PASSWORD || "",
    local17_sticky: process.env.PROXY_STICKY || "statsig-repair",
  };
}

function findPlaywright() {
  const candidates = [
    process.env.PLAYWRIGHT_MODULE,
    "/Users/real/.npm-global/lib/node_modules/playwright",
    path.join(repoRoot, "node_modules/playwright"),
  ].filter(Boolean);
  for (const dir of candidates) {
    if (fs.existsSync(path.join(dir, "index.js")) || fs.existsSync(path.join(dir, "index.mjs"))) {
      return dir;
    }
  }
  return "playwright";
}

function findChromium() {
  if (process.env.PLAYWRIGHT_CHROMIUM) return process.env.PLAYWRIGHT_CHROMIUM;
  const home = process.env.HOME || "";
  const guessed = `${home}/Library/Caches/ms-playwright/chromium-1228/chrome-mac-arm64/Google Chrome for Testing.app/Contents/MacOS/Google Chrome for Testing`;
  return fs.existsSync(guessed) ? guessed : undefined;
}

function extractCurves(html) {
  const marker = html.includes('\\"curves\\":') ? '\\"curves\\":' : '"curves":';
  const idx = html.indexOf(marker);
  if (idx < 0) return [];
  let window = html.slice(idx, idx + 80000).replace(/\\"/g, '"');
  const start = window.indexOf("[[");
  if (start < 0) return [];
  let depth = 0;
  let end = -1;
  for (let i = start; i < window.length; i++) {
    if (window[i] === "[") depth++;
    else if (window[i] === "]") {
      depth--;
      if (depth === 0) {
        end = i + 1;
        break;
      }
    }
  }
  if (end < 0) return [];
  const groups = JSON.parse(window.slice(start, end));
  return groups.slice(0, 4).map((group) => {
    const parts = group.map((seg) => {
      const c = seg.color;
      const b = seg.bezier;
      return ` ${c[0]},${c[1]} ${c[2]},${c[3]} ${c[4]},${c[5]} h ${seg.deg} s ${b[0]},${b[1]} ${b[2]},${b[3]}`;
    });
    return "M 10,30 C" + parts.join(" C");
  });
}

const secrets = loadSecrets();
const { chromium } = require(findPlaywright());
const outDir = process.env.STATSIG_OUT_DIR || path.join(repoRoot, "backend/internal/infra/provider/web/testdata");
fs.mkdirSync(outDir, { recursive: true });

const result = { started_at: new Date().toISOString(), ok: false };
let browser;
try {
  const launch = {
    headless: process.env.STATSIG_HEADED !== "1",
    args: ["--disable-blink-features=AutomationControlled"],
    ignoreDefaultArgs: ["--enable-automation"],
  };
  const exe = findChromium();
  if (exe) launch.executablePath = exe;
  browser = await chromium.launch(launch);

  const contextOpts = {
    viewport: { width: 1280, height: 800 },
    locale: "zh-CN",
    userAgent:
      "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/146.0.0.0 Safari/537.36",
  };
  if (secrets.proxy_server) {
    const username = (secrets.proxy_username_tmpl || "").replace("{account}", secrets.local17_sticky || "statsig-repair");
    contextOpts.proxy = {
      server: secrets.proxy_server,
      username,
      password: secrets.proxy_password || "",
    };
  }
  const context = await browser.newContext(contextOpts);
  await context.addInitScript(() => {
    window.__sig = { digest: null, seeks: [], anims: [], chunks: [] };
    const origDigest = crypto.subtle.digest.bind(crypto.subtle);
    crypto.subtle.digest = function (algo, data) {
      try {
        const bytes = data instanceof ArrayBuffer ? new Uint8Array(data) : new Uint8Array(data.buffer || data);
        const text = new TextDecoder().decode(bytes);
        const idx = text.indexOf("obfiowerehiring");
        if (idx >= 0) {
          let seed = "";
          for (const meta of document.querySelectorAll("meta")) {
            const name = meta.getAttribute("name") || "";
            if (name.replace(/[‐‑‒–—―]/g, "-").includes("grok-site-verification")) {
              seed = meta.getAttribute("content") || "";
            }
          }
          window.__sig.digest = { seed, hex: text.slice(idx + "obfiowerehiring".length) };
        }
      } catch {
        /* ignore */
      }
      return origDigest(algo, data);
    };
    const origAnimate = Element.prototype.animate;
    Element.prototype.animate = function (keyframes, options) {
      const anim = origAnimate.apply(this, arguments);
      try {
        const duration = typeof options === "number" ? options : options && options.duration;
        if (duration === 4096) {
          const kfs = anim.effect && anim.effect.getKeyframes ? anim.effect.getKeyframes() : [];
          window.__sig.anims.push({ duration, keyframes: kfs });
          const desc = Object.getOwnPropertyDescriptor(Animation.prototype, "currentTime");
          if (desc && desc.set) {
            Object.defineProperty(anim, "currentTime", {
              configurable: true,
              get() {
                return desc.get.call(this);
              },
              set(value) {
                window.__sig.seeks.push({ value, href: location.href });
                return desc.set.call(this, value);
              },
            });
          }
        }
      } catch {
        /* ignore */
      }
      return anim;
    };
  });
  const sso = secrets.local16_sso || secrets.grokx_sso || "";
  if (sso) {
    await context.addCookies([
      { name: "sso", value: sso, domain: "grok.com", path: "/", secure: true, httpOnly: true },
      { name: "sso-rw", value: sso, domain: "grok.com", path: "/", secure: true, httpOnly: true },
    ]);
  }
  const page = await context.newPage();
  page.on("response", (response) => {
    const url = response.url();
    if (url.includes("cdn.grok.com/_next/static/chunks/") && url.endsWith(".js")) {
      result.chunks = result.chunks || [];
      if (result.chunks.length < 200) result.chunks.push(url);
    }
  });
  await page.goto("https://grok.com/imagine", { waitUntil: "domcontentloaded", timeout: 45000 });
  for (let i = 0; i < 28; i++) {
    const ready = await page.evaluate(() => Boolean(window.__sig.digest && window.__sig.digest.hex));
    if (ready) break;
    if (i === 4 || i === 10 || i === 16) {
      await page.evaluate(async () => {
        try {
          await fetch("/rest/modes", { method: "POST", headers: { "content-type": "application/json" }, body: "{}" });
        } catch {
          /* ignore */
        }
      });
    }
    await page.waitForTimeout(400);
  }
  const hooked = await page.evaluate(() => window.__sig);
  const html = await page.content();
  const paths = extractCurves(html);
  const fixture = {
    seed: hooked.digest && hooked.digest.seed,
    hex: hooked.digest && hooked.digest.hex,
    paths,
    source: `live grok.com/imagine same-page capture ${new Date().toISOString()}`,
  };
  const debug = {
    started_at: result.started_at,
    hex_len: fixture.hex ? fixture.hex.length : 0,
    seed_len: fixture.seed ? fixture.seed.length : 0,
    path_count: paths.length,
    seeks: hooked.seeks || [],
    anims: hooked.anims || [],
    signer_hint_chunks: (result.chunks || []).filter((url) => /38asg|1645|statsig/i.test(url)).slice(0, 20),
    chunk_count: (result.chunks || []).length,
  };
  const fixturePath = path.join(outDir, "statsig_live_pair.json");
  const debugPath = path.join(outDir, "statsig_live_pair.debug.json");
  if (fixture.seed && fixture.hex && paths.length === 4) {
    fs.writeFileSync(fixturePath, JSON.stringify(fixture, null, 2) + "\n");
    result.ok = true;
  }
  fs.writeFileSync(debugPath, JSON.stringify(debug, null, 2) + "\n");
  console.log(
    JSON.stringify(
      {
        ok: result.ok,
        fixture: result.ok ? fixturePath : null,
        debug: debugPath,
        hex_len: debug.hex_len,
        seed_len: debug.seed_len,
        path_count: debug.path_count,
        seek: debug.seeks[0] && debug.seeks[0].value,
      },
      null,
      2,
    ),
  );
  if (!result.ok) process.exit(2);
} catch (err) {
  console.error(String(err).slice(0, 400));
  process.exit(1);
} finally {
  if (browser) await browser.close();
}
