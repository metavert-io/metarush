#!/usr/bin/env node
// Website snapshots (spawned by a `fetch` job, D105).
//
//   node render/snapshot.mjs --url <address> --out <dir> [--depth 0|1] [--max-pages N]
//        [--timeout-ms N] [--full-height N] [--allow-local]
//
// A fresh headless Chromium (Remotion's, a new empty profile: signed out)
// takes each page three times — on a computer (1440×900), on a phone
// (390×844) and the whole page — and reads its readable text, title,
// description, theme colour, preview picture and icon. On the first page it
// also reads the site's brand (D165, web/brand.mjs): its most logo-like
// pictures (kept next to the screenshots), its colours and its fonts (a
// Google font's file is kept; a site's own font is only named). At depth 1 it
// also takes the same-site pages the first page links to (up to --max-pages in
// all). Unlike renders it has network access, but only through
// web/proxy.mjs: never to this computer or a local network (--allow-local is
// for tests with a page served on loopback). Downloads and dialogs are
// refused, each page has a time limit and the whole run a total one.
// stdout: JSON lines only (versions, page, skipped, done | error). Logs: stderr.
import fs from 'node:fs';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { UsageError, chromiumOptions, emit, ensureBrowserInstalled, fail, intFlag, log, pinCwd, remotionVersion, watchdog } from './shared.mjs';
import { chromeWasJailed, jailChrome, killChromes } from './motion/jail.mjs';
import { READ_BRAND, keepBrand, readBrandOptions } from './web/brand.mjs';
import { fetchSmall, startFilteringProxy } from './web/proxy.mjs';

const DESKTOP = { width: 1440, height: 900, deviceScaleFactor: 2, mobile: false };
const PHONE = { width: 390, height: 844, deviceScaleFactor: 3, mobile: true };

const SKIP_EXT = /\.(pdf|zip|gz|tgz|dmg|exe|pkg|msi|png|jpe?g|gif|webp|svg|ico|mp4|mov|webm|mp3|wav|m4a|csv|xlsx?|docx?|pptx?|json|xml|rss|atom)$/i;
const SKIP_PATH = /(log-?in|log-?out|sign-?in|sign-?up|register|cart|checkout|account|wp-admin|admin|password|unsubscribe|oauth|auth\/)/i;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

const canonical = (u) => {
  try {
    const x = new URL(u);
    x.hash = '';
    if (x.pathname !== '/' && x.pathname.endsWith('/')) x.pathname = x.pathname.slice(0, -1);
    return x.href;
  } catch {
    return '';
  }
};
const siteOf = (u) => {
  try {
    return new URL(u).hostname.toLowerCase().replace(/^www\./, '');
  } catch {
    return '';
  }
};

/** What the page says about itself, its readable text and its links (runs in the page). */
const READ_PAGE = () => {
  const meta = (sel) => document.querySelector(sel)?.getAttribute('content')?.trim() ?? '';
  const abs = (v) => {
    try {
      return v ? new URL(v, location.href).href : '';
    } catch {
      return '';
    }
  };
  const toHex = (c) => {
    if (!c) return '';
    const el = document.createElement('div');
    el.style.color = c;
    document.body.appendChild(el);
    const rgb = getComputedStyle(el).color;
    el.remove();
    const m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(rgb);
    return m ? '#' + [m[1], m[2], m[3]].map((x) => Number(x).toString(16).padStart(2, '0')).join('') : '';
  };
  const main = document.querySelector('main, article, [role=main]') ?? document.body;
  let text = (main?.innerText ?? '').replace(/[ \t ]+/g, ' ').replace(/\n\s*\n\s*\n+/g, '\n\n').trim();
  if (text.length < 200 && main !== document.body) text = (document.body?.innerText ?? '').replace(/[ \t ]+/g, ' ').replace(/\n\s*\n\s*\n+/g, '\n\n').trim();
  const icons = [...document.querySelectorAll('link[rel~="icon"], link[rel="apple-touch-icon"], link[rel="apple-touch-icon-precomposed"]')]
    .map((l) => ({ href: abs(l.getAttribute('href')), size: Number((l.getAttribute('sizes') ?? '').split('x')[0]) || (l.rel.includes('apple') ? 180 : 32), type: l.getAttribute('type') ?? '' }))
    .filter((i) => i.href && !/\.ico(\?|$)/i.test(i.href))
    .sort((a, b) => b.size - a.size);
  const navLinks = new Set([...document.querySelectorAll('nav a[href], header a[href]')].map((a) => a.href));
  const links = [...document.querySelectorAll('a[href]')].map((a) => a.href).filter((h) => /^https?:/i.test(h));
  links.sort((a, b) => Number(navLinks.has(b)) - Number(navLinks.has(a)));
  return {
    title: (document.title || meta('meta[property="og:title"]')).trim().slice(0, 300),
    description: (meta('meta[name="description"]') || meta('meta[property="og:description"]')).slice(0, 600),
    siteName: meta('meta[property="og:site_name"]').slice(0, 120),
    themeColor: toHex(meta('meta[name="theme-color"]')),
    lang: (document.documentElement.lang ?? '').slice(0, 20),
    preview: abs(meta('meta[property="og:image"]') || meta('meta[name="twitter:image"]')),
    icon: icons[0]?.href ?? '',
    text: text.slice(0, 200000),
    links: [...new Set(links)].slice(0, 400),
    height: Math.max(document.documentElement.scrollHeight, document.body?.scrollHeight ?? 0),
  };
};

/** Scrolls through the page so lazy pictures load, then back to the top. */
const SCROLL_THROUGH = async () => {
  const h = () => Math.max(document.documentElement.scrollHeight, document.body?.scrollHeight ?? 0);
  const step = Math.max(400, Math.floor(innerHeight * 0.9));
  for (let y = 0, n = 0; y < Math.min(h(), 20000) && n < 40; y += step, n++) {
    scrollTo(0, y);
    await new Promise((r) => setTimeout(r, 120));
  }
  scrollTo(0, 0);
  await new Promise((r) => setTimeout(r, 250));
  return h();
};

/** Waits until the page stops loading things (or a short limit). */
const settle = async (page, ms = 4000) => {
  const end = Date.now() + ms;
  let last = -1;
  let still = 0;
  while (Date.now() < end) {
    const n = await page.evaluate(() => performance.getEntriesByType('resource').length).catch(() => -1);
    if (n === last) {
      still += 250;
      if (still >= 750) break;
    } else {
      still = 0;
      last = n;
    }
    await sleep(250);
  }
  await page.evaluate(() => document.fonts?.ready?.then(() => true)).catch(() => {});
};

const shoot = async (client, file, clip) => {
  const res = await client.send('Page.captureScreenshot', { format: 'png', fromSurface: true, ...(clip ? { clip, captureBeyondViewport: true } : {}) });
  const data = res?.value?.data ?? res?.data;
  if (!data) throw new Error('the browser returned no picture');
  fs.writeFileSync(file, Buffer.from(data, 'base64'));
};

const main = async () => {
  emit({ type: 'versions', remotion: remotionVersion() });
  let args;
  try {
    ({ values: args } = parseArgs({
      options: {
        url: { type: 'string' },
        out: { type: 'string' },
        depth: { type: 'string' },
        'max-pages': { type: 'string' },
        'timeout-ms': { type: 'string' },
        'full-height': { type: 'string' },
        'allow-local': { type: 'boolean' },
      },
      strict: true,
    }));
  } catch (e) {
    throw new UsageError(e.message);
  }
  if (!args.url || !args.out) throw new UsageError('--url and --out are required');
  const start = canonical(args.url);
  if (!/^https?:\/\//.test(start)) throw new UsageError('--url must be a web address');
  const outDir = path.resolve(args.out);
  const depth = Number(args.depth ?? 0) === 1 ? 1 : 0;
  const maxPages = depth ? intFlag(args['max-pages'], 5, 'max-pages') : 1;
  const timeoutMs = intFlag(args['timeout-ms'], 45000, 'timeout-ms', 5000);
  const fullHeight = intFlag(args['full-height'], 16000, 'full-height', 1000);
  const allowLocal = !!args['allow-local'];
  fs.mkdirSync(outDir, { recursive: true });
  const dog = watchdog({ totalMs: timeoutMs * Math.min(maxPages, 20) * 3 + 90000, what: 'the snapshot' });

  // The browser's only way out: the filtering proxy (never this computer or the local network).
  const proxy = await startFilteringProxy({ allowLocal });
  jailChrome({ proxyPort: proxy.port, bypass: [] });
  pinCwd();
  await ensureBrowserInstalled();
  const { openBrowser } = await import('@remotion/renderer');
  const browser = await openBrowser('chrome', { chromiumOptions, logLevel: 'warn' });
  if (!chromeWasJailed()) {
    await browser.close({ silent: true }).catch(() => {});
    throw new Error("MetaRush couldn't route the snapshot browser through its filter, so it won't open the page (update MetaRush).");
  }
  const close = async () => {
    await browser.close({ silent: true }).catch(() => {});
    killChromes();
    await proxy.close().catch(() => {});
  };
  try {
    const page = await browser.newPage({ context: () => null, logLevel: 'warn', indent: false, pageIndex: 0, onBrowserLog: null, onLog: () => {} });
    const client = page._client();
    await client.send('Page.enable').catch(() => {});
    await client.send('Page.setDownloadBehavior', { behavior: 'deny' }).catch(() => {});
    client.on('Page.javascriptDialogOpening', () => {
      client.send('Page.handleJavaScriptDialog', { accept: false }).catch(() => {});
    });
    const ua = String(await page.evaluate(() => navigator.userAgent)).replace(/HeadlessChrome/g, 'Chrome');
    const chromeVer = /Chrome\/([\d.]+)/.exec(ua)?.[1] ?? '130.0.0.0';
    const phoneUA = `Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/${chromeVer} Mobile Safari/537.36`;
    const view = async (v, agent) => {
      await client.send('Emulation.setDeviceMetricsOverride', { width: v.width, height: v.height, deviceScaleFactor: v.deviceScaleFactor, mobile: v.mobile });
      await client.send('Emulation.setTouchEmulationEnabled', { enabled: v.mobile, maxTouchPoints: v.mobile ? 5 : 1 }).catch(() => {});
      await client.send('Emulation.setUserAgentOverride', { userAgent: agent }).catch(() => {});
    };
    const open = async (url) => {
      const r = await page.goto({ url, timeout: timeoutMs });
      const status = typeof r?.status === 'function' ? r.status() : 200;
      if (status >= 400) throw new Error(`the site answered ${status}`);
      await settle(page);
    };

    const queue = [{ url: start, from: '' }];
    const seen = new Set([start]);
    const skipped = [];
    let done = 0;
    for (let i = 0; i < queue.length && done < maxPages; i++) {
      const { url, from } = queue[i];
      const dir = path.join(outDir, `page-${String(i + 1).padStart(2, '0')}`);
      fs.mkdirSync(dir, { recursive: true });
      const shots = {};
      try {
        // On a computer: the screen, then the whole page.
        await view(DESKTOP, ua);
        await open(url);
        const finalUrl = canonical(await page.evaluate(() => location.href)) || url;
        if (!/^https?:\/\//.test(finalUrl)) throw new Error('the page went somewhere that is not a web page');
        const info = await page.evaluate(READ_PAGE);
        // The site's brand (D165), from the first page only: read now, at the
        // top of the page on a computer; its pictures are fetched below.
        const rawBrand =
          i === 0
            ? await page.evaluate(READ_BRAND, readBrandOptions()).catch((e) => {
                log(`brand of ${finalUrl}: ${e.message}`);
                return null;
              })
            : null;
        shots.desktop = path.join(dir, 'desktop.png');
        await shoot(client, shots.desktop);
        const height = Math.min(fullHeight, Math.max(DESKTOP.height, await page.evaluate(SCROLL_THROUGH).catch(() => info.height)));
        shots.full = path.join(dir, 'full.png');
        await shoot(client, shots.full, { x: 0, y: 0, width: DESKTOP.width, height, scale: 0.5 });
        // On a phone.
        await view(PHONE, phoneUA);
        try {
          await open(finalUrl);
          shots.mobile = path.join(dir, 'mobile.png');
          await shoot(client, shots.mobile);
        } catch (e) {
          log(`phone view of ${finalUrl} failed: ${e.message}`);
        }
        const textFile = path.join(dir, 'text.txt');
        fs.writeFileSync(textFile, info.text ?? '');
        const extra = {};
        for (const [k, u] of [['preview', info.preview], ['icon', info.icon]]) {
          if (!u) continue;
          try {
            const got = await fetchSmall(u, { allowLocal });
            const ext = /png/.test(got.type) ? 'png' : /jpe?g/.test(got.type) ? 'jpg' : /webp/.test(got.type) ? 'webp' : /svg/.test(got.type) ? 'svg' : /gif/.test(got.type) ? 'gif' : '';
            if (ext && got.body.length > 0) {
              const f = path.join(dir, `${k}.${ext}`);
              fs.writeFileSync(f, got.body);
              extra[k] = f;
            }
          } catch (e) {
            log(`${k} of ${finalUrl}: ${e.message}`);
          }
        }
        const brand = rawBrand
          ? await keepBrand(rawBrand, { dir, allowLocal, userAgent: ua, log }).catch((e) => {
              log(`brand of ${finalUrl}: ${e.message}`);
              return null;
            })
          : null;
        done++;
        emit({ type: 'page', done, of: Math.min(maxPages, queue.length), page: { url: finalUrl, requested: url, from, title: info.title, description: info.description, siteName: info.siteName,
          themeColor: info.themeColor, lang: info.lang, textFile, shots, ...extra, ...(brand ? { brand } : {}) } });
        // Depth 1: same-site pages the first page links to.
        if (depth === 1 && i === 0) {
          const site = siteOf(finalUrl);
          for (const l of info.links ?? []) {
            const c = canonical(l);
            if (!c || seen.has(c) || siteOf(c) !== site || SKIP_EXT.test(new URL(c).pathname) || SKIP_PATH.test(new URL(c).pathname)) continue;
            seen.add(c);
            queue.push({ url: c, from: finalUrl });
          }
        }
      } catch (e) {
        const why = /timeout|timed out/i.test(e.message) ? 'it took too long to load' : e.message;
        skipped.push({ url, why });
        if (i === 0) {
          const blocked = [...proxy.refused];
          const msg = blocked.length && !allowLocal ? `the page leads to ${blocked[0]}, which is on this computer or a local network` : why;
          throw new Error(msg);
        }
      } finally {
        if (Object.keys(shots).length === 0) fs.rmSync(dir, { recursive: true, force: true });
      }
    }
    const over = Math.max(0, queue.length - done - skipped.length);
    if (skipped.length || over) emit({ type: 'skipped', skipped: skipped.slice(0, 20), over });
    dog.stop();
    await close();
    emit({ type: 'done', pages: done, refused: [...proxy.refused].sort(), bytes: proxy.used() });
  } catch (e) {
    await close();
    throw e;
  }
};

main().then(
  () => process.exit(0),
  (err) => fail(err),
);
