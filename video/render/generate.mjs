#!/usr/bin/env node
// MetaRush generated resources (spawned by `metarush resources make`, D106).
//
//   node render/generate.mjs code|diagram|stats --in <file.json> --out <file> [--bundle-cache <dir>]
//
// code     a code slide: the code is highlighted with Shiki here in Node (its
//          tokens are data: escaped into a page MetaRush writes) and drawn as a
//          PNG panel with line numbers, a header and highlighted lines
// diagram  a Mermaid diagram Claude described, drawn to SVG by Mermaid
// stats    a repository card: name, languages, files, contributors, licence
//
// Everything is drawn in the same network-jailed headless browser as renders
// (motion/jail.mjs): the page, its fonts and Mermaid come from a loopback
// server only this run knows; nothing from a repository ever runs.
// stdout: JSON lines only (versions, done {output, width, height, versions} | error).
import crypto from 'node:crypto';
import fs from 'node:fs';
import http from 'node:http';
import { createRequire } from 'node:module';
import path from 'node:path';
import { parseArgs } from 'node:util';
import { UsageError, chromiumOptions, emit, ensureBrowserInstalled, fail, log, pinCwd, remotionVersion, watchdog } from './shared.mjs';
import { hostPort } from './motion/jail.mjs';
import { openSession } from './motion/session.mjs';

const require = createRequire(import.meta.url);
const VERSIONS = {
  shiki: require('shiki/package.json').version,
  mermaid: require('mermaid/package.json').version,
  remotion: remotionVersion(),
};

const esc = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
const hex = (s, def) => (/^#[0-9a-fA-F]{6}$/.test(String(s ?? '')) ? String(s) : def);
const rgba = (h, a) => {
  const n = parseInt(h.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${a})`;
};

/** A font file from MetaRush's own packages (served to the page). */
const fontFile = (pkg, file) => {
  const dir = path.dirname(require.resolve(`${pkg}/package.json`));
  return path.join(dir, 'files', file);
};

/** Serves the page and its files at http://127.0.0.1:<port>/<token>/… (nothing else). */
const servePage = (files) =>
  new Promise((resolve) => {
    const token = crypto.randomBytes(16).toString('hex');
    const server = http.createServer((req, res) => {
      const m = /^\/([0-9a-f]{32})\/([a-z0-9._-]+)$/.exec(new URL(req.url, 'http://x').pathname);
      const f = m && m[1] === token ? files[m[2]] : undefined;
      if (!f || (req.method !== 'GET' && req.method !== 'HEAD')) {
        res.writeHead(404).end();
        return;
      }
      const body = f.body ?? fs.readFileSync(f.file);
      res.writeHead(200, { 'Content-Type': f.type, 'Cache-Control': 'no-store', 'X-Content-Type-Options': 'nosniff', 'Access-Control-Allow-Origin': '*' });
      res.end(req.method === 'HEAD' ? undefined : body);
    });
    server.listen(0, '127.0.0.1', () => {
      const base = `http://127.0.0.1:${server.address().port}/${token}`;
      resolve({ base, close: () => new Promise((r) => server.close(() => r())) });
    });
  });

const PAGE_CSP = "default-src 'none'; script-src 'self'; style-src 'self' 'unsafe-inline'; font-src 'self'; img-src 'self' data:; connect-src 'none'; frame-src 'none'; object-src 'none'; base-uri 'none'; form-action 'none'";

const pageHTML = (body, css, scripts = []) => `<!doctype html><html><head><meta charset="utf-8">
<meta http-equiv="Content-Security-Policy" content="${PAGE_CSP}">
<style>
@font-face { font-family: "MR Mono"; src: url("mono.woff2") format("woff2"); font-weight: 100 800; }
@font-face { font-family: "MR Sans"; src: url("sans.woff2") format("woff2"); font-weight: 100 900; }
html, body { margin: 0; padding: 0; background: transparent; }
body { -webkit-font-smoothing: antialiased; text-rendering: geometricPrecision; }
${css}
</style>${scripts.map((s) => `<script src="${s}"></script>`).join('')}</head><body>${body}</body></html>`;

// --- code slides -------------------------------------------------------------

const MAX_LINES = 80;

/** Highlights code with Shiki (tokens only; the code is data). */
const highlight = async (code, language, theme) => {
  const { codeToTokens, bundledLanguages } = await import('shiki');
  const lang = bundledLanguages[language] ? language : 'text';
  const name = theme === 'light' ? 'github-light-default' : 'github-dark-default';
  const r = await codeToTokens(code, { lang, theme: name });
  return { tokens: r.tokens, fg: r.fg ?? (theme === 'light' ? '#1f2328' : '#e6edf3'), bg: r.bg ?? (theme === 'light' ? '#ffffff' : '#0d1117'), lang };
};

const codePage = async (input) => {
  const code = String(input.code ?? '');
  const lines = code.split('\n');
  if (lines.length > MAX_LINES) throw new UsageError(`a code slide shows at most ${MAX_LINES} lines`);
  const theme = input.theme === 'light' ? 'light' : 'dark';
  const { tokens, fg, bg, lang } = await highlight(code, String(input.language ?? 'text'), theme);
  const first = Number.isInteger(input.firstLine) && input.firstLine > 0 ? input.firstLine : 1;
  const hl = new Set();
  for (const [a, b] of Array.isArray(input.highlight) ? input.highlight : []) for (let n = a; n <= b; n++) hl.add(n);
  const brand = input.brand ?? {};
  const accent = hex(brand.accent, '#E0A458');
  const digits = String(first + lines.length - 1).length;
  const rows = tokens
    .map((line, i) => {
      const n = first + i;
      const spans = line
        .map((t) => {
          const style = [`color:${hex(t.color, fg)}`];
          if (t.fontStyle & 1) style.push('font-style:italic');
          if (t.fontStyle & 2) style.push('font-weight:650');
          if (t.fontStyle & 4) style.push('text-decoration:underline');
          return `<span style="${style.join(';')}">${esc(t.content)}</span>`;
        })
        .join('');
      return `<div class="ln${hl.has(n) ? ' hl' : ''}"><span class="n">${n}</span><span class="t">${spans || ' '}</span></div>`;
    })
    .join('');
  const h = input.header ?? {};
  const light = theme === 'light';
  const bar = light ? '#f3f4f6' : '#161b22';
  const muted = light ? '#59636e' : '#9198a1';
  const css = `
#panel { display: inline-block; margin: 28px; min-width: 880px; border-radius: 22px; overflow: hidden; background: ${bg}; color: ${fg};
  box-shadow: 0 18px 48px rgba(0,0,0,${light ? 0.18 : 0.42}), 0 0 0 1px ${light ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.07)'}; }
.bar { display: flex; align-items: center; gap: 18px; padding: 16px 26px; background: ${bar}; border-bottom: 1px solid ${light ? 'rgba(0,0,0,0.07)' : 'rgba(255,255,255,0.06)'};
  font: 500 20px "MR Sans", sans-serif; color: ${muted}; white-space: nowrap; }
.dots { display: flex; gap: 9px; } .dots i { width: 13px; height: 13px; border-radius: 50%; display: block; background: ${light ? '#d1d5db' : '#30363d'}; }
.path { color: ${light ? '#1f2328' : '#e6edf3'}; font-weight: 600; } .lines { color: ${muted}; } .repo { margin-left: auto; color: ${muted}; font-size: 17px; }
pre { margin: 0; padding: 22px 0 26px; font: 400 26px/1.58 "MR Mono", monospace; font-variant-ligatures: none; tab-size: 4; }
.ln { display: flex; padding: 0 40px 0 0; white-space: pre; } .ln .n { width: ${digits + 2}ch; flex: none; text-align: right; padding-right: 1.4ch; color: ${muted}; opacity: 0.55; user-select: none; }
.ln.hl { background: ${rgba(accent, light ? 0.16 : 0.18)}; box-shadow: inset 5px 0 0 ${accent}; } .ln.hl .n { opacity: 0.95; color: ${accent}; }
`;
  const body = `<div id="panel"><div class="bar"><span class="dots"><i></i><i></i><i></i></span><span class="path">${esc(h.path)}</span><span class="lines">${esc(h.lines)}</span><span class="repo">${esc(h.repo)}</span></div><pre>${rows}</pre></div>`;
  return { html: pageHTML(body, css), info: { lang } };
};

// --- repository cards ------------------------------------------------------------

// Language colours (GitHub's), for the bar.
const LANG_COLORS = {
  Go: '#00ADD8', TypeScript: '#3178c6', JavaScript: '#f1e05a', Python: '#3572A5', Rust: '#dea584', Java: '#b07219', Kotlin: '#A97BFF', Swift: '#F05138',
  C: '#555555', 'C++': '#f34b7d', 'C#': '#178600', Ruby: '#701516', PHP: '#4F5D95', Shell: '#89e051', HTML: '#e34c26', CSS: '#663399', SCSS: '#c6538c',
  Vue: '#41b883', Svelte: '#ff3e00', Dart: '#00B4AB', Elixir: '#6e4a7e', Haskell: '#5e5086', Lua: '#000080', Zig: '#ec915c', Scala: '#c22d40',
  'Objective-C': '#438eff', Dockerfile: '#384d54', Makefile: '#427819', SQL: '#e38c00', Nix: '#7e7eff', HCL: '#844FBA', Julia: '#a270ba', R: '#198CE7',
};

const statsPage = (input) => {
  const theme = input.theme === 'light' ? 'light' : 'dark';
  const light = theme === 'light';
  const brand = input.brand ?? {};
  const accent = hex(brand.accent, '#E0A458');
  const bg = light ? '#ffffff' : '#11141a';
  const fg = light ? '#1f2328' : '#eef0f3';
  const muted = light ? '#59636e' : '#9aa3ad';
  const langs = Array.isArray(input.languages) ? input.languages.slice(0, 6) : [];
  const bar = langs.map((l) => `<i style="flex:${Math.max(0.02, Number(l.share) || 0)};background:${LANG_COLORS[l.name] ?? accent}"></i>`).join('');
  const legend = langs
    .map((l) => `<span><b style="background:${LANG_COLORS[l.name] ?? accent}"></b>${esc(l.name)} <em>${(Math.round((Number(l.share) || 0) * 1000) / 10).toFixed(1)}%</em></span>`)
    .join('');
  const stat = (n, label) => (n || n === 0 ? `<div class="stat"><strong>${esc(typeof n === 'number' ? n.toLocaleString('en-US') : n)}</strong><span>${esc(label)}</span></div>` : '');
  const topics = (Array.isArray(input.topics) ? input.topics.slice(0, 5) : []).map((t) => `<span class="chip">${esc(t)}</span>`).join('');
  const css = `
#panel { display: inline-block; margin: 28px; width: 1180px; border-radius: 26px; padding: 44px 50px 46px; background: ${bg}; color: ${fg};
  box-shadow: 0 18px 48px rgba(0,0,0,${light ? 0.16 : 0.45}), 0 0 0 1px ${light ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.07)'}; font-family: "MR Sans", sans-serif; box-sizing: border-box; }
.kicker { font: 600 19px "MR Sans"; letter-spacing: 0.14em; text-transform: uppercase; color: ${accent}; }
h1 { margin: 10px 0 0; font: 700 54px/1.08 "MR Sans"; letter-spacing: -0.02em; word-break: break-word; }
.desc { margin: 16px 0 0; font: 400 25px/1.45 "MR Sans"; color: ${muted}; }
.bar { display: flex; height: 14px; border-radius: 7px; overflow: hidden; margin: 34px 0 14px; gap: 3px; } .bar i { display: block; height: 100%; }
.legend { display: flex; flex-wrap: wrap; gap: 10px 26px; font: 500 21px "MR Sans"; } .legend b { display: inline-block; width: 13px; height: 13px; border-radius: 50%; margin-right: 9px; vertical-align: 1px; }
.legend em { font-style: normal; color: ${muted}; margin-left: 4px; }
.stats { display: flex; gap: 46px; margin-top: 34px; padding-top: 28px; border-top: 1px solid ${light ? 'rgba(0,0,0,0.08)' : 'rgba(255,255,255,0.08)'}; }
.stat strong { display: block; font: 700 40px/1 "MR Sans"; letter-spacing: -0.01em; } .stat span { display: block; margin-top: 8px; font: 500 19px "MR Sans"; color: ${muted}; }
.chips { margin-top: 26px; display: flex; flex-wrap: wrap; gap: 10px; } .chip { padding: 7px 16px; border-radius: 999px; font: 500 18px "MR Sans"; background: ${rgba(accent, 0.14)}; color: ${light ? fg : accent}; }
`;
  const body = `<div id="panel"><div class="kicker">Repository</div><h1>${esc(input.repo)}</h1>${input.description ? `<p class="desc">${esc(input.description)}</p>` : ''}
${langs.length ? `<div class="bar">${bar}</div><div class="legend">${legend}</div>` : ''}
<div class="stats">${stat(input.files, 'files')}${input.stars ? stat(input.stars, 'stars') : ''}${input.contributors ? stat(input.contributors, 'recent contributors') : ''}${input.license ? stat(input.license, 'licence') : ''}${stat(input.commit, `commit, ${input.committedAt ?? ''}`)}</div>
${topics ? `<div class="chips">${topics}</div>` : ''}</div>`;
  return { html: pageHTML(body, css) };
};

// --- diagrams ----------------------------------------------------------------------

/** Mermaid's settings: strict security, SVG text labels (no embedded HTML), the brand's colours. */
const mermaidConfig = (input) => {
  const theme = input.theme === 'light' ? 'light' : 'dark';
  const brand = input.brand ?? {};
  const accent = hex(brand.accent, '#E0A458');
  const light = theme === 'light';
  return {
    startOnLoad: false,
    securityLevel: 'strict',
    htmlLabels: false,
    flowchart: { htmlLabels: false, curve: 'basis', padding: 18, nodeSpacing: 46, rankSpacing: 58, useMaxWidth: false },
    sequence: { useMaxWidth: false }, class: { useMaxWidth: false }, state: { useMaxWidth: false }, er: { useMaxWidth: false },
    theme: 'base',
    fontFamily: 'Arial, "Liberation Sans", Helvetica, sans-serif',
    themeVariables: {
      fontFamily: 'Arial, "Liberation Sans", Helvetica, sans-serif',
      fontSize: '18px',
      background: light ? '#ffffff' : '#11141a',
      primaryColor: light ? '#f4f5f7' : '#1c2129',
      primaryTextColor: light ? '#1f2328' : '#eef0f3',
      primaryBorderColor: accent,
      lineColor: light ? '#59636e' : '#9aa3ad',
      secondaryColor: light ? '#fff6e8' : '#2a2318',
      tertiaryColor: light ? '#eef6ff' : '#17202b',
      clusterBkg: light ? '#fafafa' : '#151a21',
      clusterBorder: light ? '#d0d7de' : '#3a414b',
      edgeLabelBackground: light ? '#ffffff' : '#11141a',
      noteBkgColor: light ? '#fff6e8' : '#2a2318',
      noteTextColor: light ? '#1f2328' : '#eef0f3',
      actorBkg: light ? '#f4f5f7' : '#1c2129', actorBorder: accent, actorTextColor: light ? '#1f2328' : '#eef0f3',
    },
  };
};

// --- main --------------------------------------------------------------------------

const capturePanel = async (page, outFile) => {
  const rect = await page.evaluate(async () => {
    await document.fonts.ready;
    const p = document.getElementById('panel');
    const r = p.getBoundingClientRect();
    return { x: Math.floor(r.left) - 28, y: Math.floor(r.top) - 28, width: Math.ceil(r.width) + 56, height: Math.ceil(r.height) + 56 };
  });
  const client = page._client();
  await client.send('Emulation.setDefaultBackgroundColorOverride', { color: { r: 0, g: 0, b: 0, a: 0 } });
  const res = await client.send('Page.captureScreenshot', { format: 'png', clip: { ...rect, scale: 1 }, captureBeyondViewport: true, fromSurface: true });
  const data = res?.value?.data ?? res?.data;
  if (!data) throw new Error('the browser returned no picture');
  fs.writeFileSync(outFile, Buffer.from(data, 'base64'));
  return { width: rect.width * 2, height: rect.height * 2 };
};

const run = async (kind, args) => {
  const input = JSON.parse(fs.readFileSync(path.resolve(args.in), 'utf8'));
  const outFile = path.resolve(args.out);
  const dog = watchdog({ totalMs: 180000, what: 'drawing it' });
  const files = {
    'mono.woff2': { file: fontFile('@fontsource-variable/jetbrains-mono', 'jetbrains-mono-latin-wght-normal.woff2'), type: 'font/woff2' },
    'sans.woff2': { file: fontFile('@fontsource-variable/inter', 'inter-latin-wght-normal.woff2'), type: 'font/woff2' },
  };
  let built;
  if (kind === 'code') built = await codePage(input);
  else if (kind === 'stats') built = statsPage(input);
  else {
    if (typeof input.source !== 'string' || !input.source.trim()) throw new UsageError('a diagram needs its description');
    if (input.format && input.format !== 'mermaid') throw new UsageError('this script draws Mermaid diagrams (D2 runs as its own program)');
    files['mermaid.js'] = { file: path.join(path.dirname(require.resolve('mermaid/package.json')), 'dist', 'mermaid.min.js'), type: 'text/javascript; charset=utf-8' };
    built = { html: pageHTML('<div id="out"></div>', '', ['mermaid.js']) };
  }
  files['page.html'] = { body: Buffer.from(built.html), type: 'text/html; charset=utf-8' };
  const server = await servePage(files);
  pinCwd();
  await ensureBrowserInstalled();
  // The same jail as renders: only this run's page server is reachable.
  const session = await openSession({ plan: { overlays: [], clips: [], audio: [], sources: {} }, extraLoopback: [hostPort(server.base)], chromiumOptions, log });
  try {
    const page = await session.browser.newPage({ context: () => null, logLevel: 'warn', indent: false, pageIndex: 0, onBrowserLog: null, onLog: () => {} });
    await page.setViewport({ width: kind === 'stats' ? 1300 : 2600, height: 1200, deviceScaleFactor: 2 });
    await page.goto({ url: `${server.base}/page.html`, timeout: 60000 });
    let result;
    if (kind === 'diagram') {
      const svg = await page.evaluate(
        async (source, config) => {
          const m = globalThis.mermaid;
          if (!m) throw new Error('Mermaid did not load');
          m.initialize(config);
          try {
            await m.parse(source);
          } catch (e) {
            return { error: String(e?.message ?? e).slice(0, 600) };
          }
          const r = await m.render('metarush-diagram', source);
          return { svg: r.svg };
        },
        input.source,
        mermaidConfig(input),
      );
      if (svg?.error) throw new UsageError(`Mermaid can't read the diagram: ${svg.error}`);
      if (!svg?.svg || !svg.svg.startsWith('<svg')) throw new Error('Mermaid drew nothing');
      fs.writeFileSync(outFile, svg.svg);
      const vb = /viewBox="([-\d.]+)\s+([-\d.]+)\s+([\d.]+)\s+([\d.]+)"/.exec(svg.svg);
      result = { width: vb ? Math.round(Number(vb[3])) : 0, height: vb ? Math.round(Number(vb[4])) : 0 };
    } else {
      result = await capturePanel(page, outFile);
    }
    const blocked = session.blocked();
    if (blocked.length) emit({ type: 'blocked', hosts: blocked });
    dog.stop();
    await session.close();
    await server.close();
    emit({ type: 'done', output: outFile, ...result, versions: VERSIONS, ...(built.info ?? {}) });
  } catch (e) {
    await session.close();
    await server.close();
    throw e;
  }
};

const main = async () => {
  emit({ type: 'versions', ...VERSIONS });
  const [cmd, ...rest] = process.argv.slice(2);
  let args;
  try {
    ({ values: args } = parseArgs({ args: rest, options: { in: { type: 'string' }, out: { type: 'string' }, 'bundle-cache': { type: 'string' } }, strict: true }));
  } catch (e) {
    throw new UsageError(e.message);
  }
  if (!['code', 'diagram', 'stats'].includes(cmd)) throw new UsageError('usage: generate.mjs code|diagram|stats --in <file.json> --out <file>');
  if (!args.in || !args.out) throw new UsageError('--in and --out are required');
  await run(cmd, args);
};

main().then(
  () => process.exit(0),
  (err) => fail(err),
);
