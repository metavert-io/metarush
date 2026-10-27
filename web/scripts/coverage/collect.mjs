// Loaded into a browser run's script under COVERAGE=1 (node --import, by scripts/lib/e2e.sh; D195):
// every page Playwright opens records which of the dashboard's code it ran (Chromium's JS
// coverage), and before a page, its context or the browser closes, the record is written to
// METARUSH_WEB_COVERAGE_DIR. web/scripts/coverage/report.mjs turns the records into lcov against
// web/src (through the coverage build's source maps). The test scripts themselves are unchanged.
import { createHash } from 'node:crypto';
import { mkdirSync, writeFileSync } from 'node:fs';
import path from 'node:path';

const out = process.env.METARUSH_WEB_COVERAGE_DIR;
if (out) {
  const { chromium } = await import('playwright');
  mkdirSync(out, { recursive: true });
  const sources = new Set();
  let n = 0;
  // page -> the promise of its coverage having started (a page is tracked once).
  const started = new Map();

  const track = (page) => {
    if (!started.has(page)) {
      started.set(
        page,
        page.coverage.startJSCoverage({ resetOnNavigation: false }).then(
          () => true,
          () => false,
        ),
      );
    }
    return started.get(page);
  };

  const save = async (page) => {
    const s = started.get(page);
    if (!s) return;
    started.delete(page);
    if (!(await s)) return;
    let entries = [];
    try {
      entries = await page.coverage.stopJSCoverage();
    } catch {
      return;
    }
    // Only the dashboard's own bundles (/assets/*.js), each source written once.
    const kept = [];
    for (const e of entries) {
      let pathname = '';
      try {
        pathname = new URL(e.url).pathname;
      } catch {
        continue;
      }
      if (!/^\/assets\/[^/]+\.js$/.test(pathname)) continue;
      const file = path.basename(pathname);
      if (!sources.has(file) && e.source) {
        writeFileSync(path.join(out, `source-${createHash('sha256').update(file).digest('hex').slice(0, 16)}.json`), JSON.stringify({ file, source: e.source }));
        sources.add(file);
      }
      kept.push({ file, functions: e.functions });
    }
    if (kept.length) writeFileSync(path.join(out, `page-${process.pid}-${n++}.json`), JSON.stringify(kept));
  };

  const wrapPage = (page) => {
    if (page.__metarushCoverage) return page;
    page.__metarushCoverage = true;
    const close = page.close.bind(page);
    page.close = async (...a) => {
      await save(page);
      return close(...a);
    };
    void track(page);
    return page;
  };

  const wrapContext = (ctx) => {
    if (ctx.__metarushCoverage) return ctx;
    ctx.__metarushCoverage = true;
    for (const p of ctx.pages()) wrapPage(p);
    ctx.on('page', wrapPage);
    const newPage = ctx.newPage.bind(ctx);
    ctx.newPage = async (...a) => {
      const p = wrapPage(await newPage(...a));
      await track(p);
      return p;
    };
    const close = ctx.close.bind(ctx);
    ctx.close = async (...a) => {
      for (const p of ctx.pages()) await save(p);
      return close(...a);
    };
    return ctx;
  };

  const wrapBrowser = (browser) => {
    const newContext = browser.newContext.bind(browser);
    browser.newContext = async (...a) => wrapContext(await newContext(...a));
    const newPage = browser.newPage.bind(browser);
    browser.newPage = async (...a) => {
      const p = await newPage(...a);
      wrapContext(p.context());
      wrapPage(p);
      await track(p);
      return p;
    };
    const close = browser.close.bind(browser);
    browser.close = async (...a) => {
      for (const ctx of browser.contexts()) for (const p of ctx.pages()) await save(p);
      return close(...a);
    };
    return browser;
  };

  const launch = chromium.launch.bind(chromium);
  chromium.launch = async (...a) => wrapBrowser(await launch(...a));
  const persistent = chromium.launchPersistentContext.bind(chromium);
  chromium.launchPersistentContext = async (...a) => wrapContext(await persistent(...a));
}
