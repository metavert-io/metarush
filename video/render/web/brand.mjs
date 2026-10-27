// A web page's brand (D165): its logos and icons, its colours and its fonts,
// read in the snapshot's own filtering browser (render/snapshot.mjs) and kept
// next to the page's pictures. Nothing new reaches the network: the page is
// read where it already is, and the chosen pictures and one Google font file
// are downloaded with fetchSmall (the proxy's rules: never this computer or a
// local network, no redirects, 5 MB each). A site's own font files are never
// downloaded, only named (their licence is unknown); a Google font's file is
// (an open licence), and only when the page itself loaded it.
//
// READ_BRAND runs in the page and returns raw facts. Everything that decides —
// how logo-like a picture is, which colours count, which font to keep, reading
// Google's CSS — is a plain function here, tested offline by web/test.mjs.
import crypto from 'node:crypto';
import fs from 'node:fs';
import path from 'node:path';
import { fetchSmall } from './proxy.mjs';

/** How many logo and icon pictures a snapshot keeps. */
export const MAX_LOGOS = 3;
/** The largest logo drawn in the page (its SVG markup) that is kept. */
export const MAX_SVG_BYTES = 400 << 10;
/** An element of the page needs at least this score to count as a logo. */
export const MIN_LOGO_SCORE = 2;

/** The page's own icons (in its <head>): they always count, after real logos. */
export const ICON_KINDS = new Set(['touch-icon', 'icon', 'mask-icon']);

/** Words that say "logo" in a class, id, alt text, title, label or file name. */
export const LOGO_WORDS = 'logo|brand|wordmark|logotype';

/**
 * CSS custom properties that name colours, with how much each says about the
 * brand; the first rule that matches counts (--brand-accent is an accent).
 */
export const COLOR_VAR_RULES = [
  ['primary', 0.9],
  ['accent|secondary|highlight|cta', 0.7],
  ['brand', 0.9],
  ['^--(colou?r|clr)[-_]|[-_](colou?r|clr)$|theme|main|link', 0.4],
];

/** Font names that are not a family of their own (generic and system fonts). */
export const GENERIC_FONTS = ['serif', 'sans-serif', 'monospace', 'cursive', 'fantasy', 'system-ui', 'ui-serif', 'ui-sans-serif', 'ui-monospace',
  'ui-rounded', 'emoji', 'math', 'fangsong', '-apple-system', 'blinkmacsystemfont', 'inherit', 'initial', 'unset', 'revert'];

/** Where a colour came from, strongest first (theme-color and the page's own colour names lead). */
export const COLOR_SOURCES = ['theme-color', 'mask-icon', 'css', 'logo', 'header', 'button', 'section', 'link', 'header-text', 'background', 'text'];

const round = (n, d = 2) => Math.round(n * 10 ** d) / 10 ** d;

/** How much a CSS custom property's name says it is a brand colour (0: not a colour name). */
export const colorVarWeight = (name) => {
  for (const [re, w] of COLOR_VAR_RULES) if (new RegExp(re, 'i').test(String(name))) return w;
  return 0;
};

/** "#ABC", "#aabbcc" → "#aabbcc"; anything else → "". */
export const normHex = (s) => {
  const v = String(s ?? '').trim().toLowerCase();
  if (/^#[0-9a-f]{6}$/.test(v)) return v;
  if (/^#[0-9a-f]{3}$/.test(v)) return `#${v[1]}${v[1]}${v[2]}${v[2]}${v[3]}${v[3]}`;
  return '';
};

/**
 * Reads the page's brand. Runs in the page (page.evaluate), so it uses nothing
 * from this module: opts carries the shared rules. Returns the raw facts:
 * logo candidates, colours with where each came from, and fonts.
 */
export const READ_BRAND = (opts) => {
  const maxSvg = opts?.maxSvgBytes ?? 400000;
  const logoRe = new RegExp(opts?.logoWords ?? 'logo', 'i');
  const varRules = (opts?.varRules ?? []).map(([re, w]) => [new RegExp(re, 'i'), w]);
  const generic = new Set(opts?.generic ?? []);
  const vw = innerWidth || 1440;
  const vh = innerHeight || 900;
  const screen = vw * vh;
  const out = { viewport: { width: vw, height: vh }, host: location.hostname.replace(/^www\./, ''), siteName: '', candidates: [], colors: [], used: [], loaded: [], google: [] };
  const meta = (sel) => document.querySelector(sel)?.getAttribute('content')?.trim() ?? '';
  const abs = (v) => {
    try {
      return v ? new URL(v, location.href).href : '';
    } catch {
      return '';
    }
  };
  out.siteName = (meta('meta[property="og:site_name"]') || meta('meta[name="application-name"]')).slice(0, 120);

  // Any CSS colour (rgb, hsl, oklch, a name…) → [r, g, b, alpha] through a 1×1 canvas.
  const cv = document.createElement('canvas');
  cv.width = 1;
  cv.height = 1;
  const g = cv.getContext('2d', { willReadFrequently: true });
  const rgba = (s) => {
    const v = String(s ?? '').trim();
    if (!g || !v || /^(transparent|none|currentcolor|inherit|initial|unset|revert)$/i.test(v) || v.length > 120) return null;
    g.fillStyle = '#010203';
    g.fillStyle = v;
    if (String(g.fillStyle).toLowerCase() === '#010203' && v.toLowerCase() !== '#010203') return null; // not a colour
    g.clearRect(0, 0, 1, 1);
    g.fillRect(0, 0, 1, 1);
    const d = g.getImageData(0, 0, 1, 1).data;
    return [d[0], d[1], d[2], d[3] / 255];
  };
  const hex = (c) => `#${c.slice(0, 3).map((x) => x.toString(16).padStart(2, '0')).join('')}`;
  const solid = (s) => {
    const c = rgba(s);
    return c && c[3] >= 0.5 ? hex(c) : '';
  };
  const visible = (el) => {
    const st = getComputedStyle(el);
    if (st.display === 'none' || st.visibility === 'hidden' || Number(st.opacity) === 0) return null;
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 ? r : null;
  };
  // How much of the first screens an element covers.
  const covers = (r, screens) => {
    const w = Math.max(0, Math.min(r.right, vw) - Math.max(r.left, 0));
    const h = Math.max(0, Math.min(r.bottom, vh * screens) - Math.max(r.top, 0));
    return (w * h) / screen;
  };
  const color = (s, from, weight) => {
    const h = solid(s);
    if (h && weight > 0) out.colors.push({ hex: h, from, weight: Math.round(weight * 1000) / 1000 });
  };
  const all = (sel, max) => [...document.querySelectorAll(sel)].slice(0, max);

  // --- colours ---
  all('meta[name="theme-color"]', 3).forEach((m, i) => color(m.getAttribute('content'), 'theme-color', i === 0 ? 1 : 0.6));
  const names = new Set();
  const collect = (style) => {
    for (let i = 0; i < (style?.length ?? 0) && names.size < 400; i++) if (String(style[i]).startsWith('--')) names.add(style[i]);
  };
  collect(getComputedStyle(document.documentElement));
  if (document.body) collect(getComputedStyle(document.body));
  const walk = (rules, depth) => {
    for (const r of rules ?? []) {
      try {
        if (r.cssRules && depth < 4) walk(r.cssRules, depth + 1);
        if (r.style && /(^|,)\s*(:root|html|body)\b/i.test(r.selectorText ?? '')) collect(r.style);
      } catch {
        // a rule MetaRush can't read
      }
    }
  };
  for (const sh of document.styleSheets) {
    try {
      walk(sh.cssRules, 0);
    } catch {
      // another site's stylesheet: its rules can't be read
    }
  }
  const rootStyle = getComputedStyle(document.documentElement);
  const bodyStyle = document.body ? getComputedStyle(document.body) : null;
  let vars = 0;
  for (const n of names) {
    const rule = varRules.find(([re]) => re.test(n));
    if (!rule) continue;
    const v = (rootStyle.getPropertyValue(n) || bodyStyle?.getPropertyValue(n) || '').trim();
    const h = v && v.length <= 80 ? solid(v) : '';
    if (!h) continue;
    out.colors.push({ hex: h, from: `css:${n.slice(0, 60)}`, weight: rule[1] });
    if (++vars >= 40) break;
  }
  for (const el of all('header, [role=banner], nav, [role=navigation], .header, #header, .site-header, .navbar, .masthead', 30)) {
    const r = visible(el);
    const a = r ? covers(r, 1) : 0;
    if (a <= 0) continue;
    const st = getComputedStyle(el);
    color(st.backgroundColor, 'header', Math.min(1, a * 2));
    color(st.color, 'header-text', Math.min(0.6, a));
  }
  for (const el of all('button, .btn, .button, [class*="btn-"], [class~="btn"], a.cta, .cta, [class*="cta"], input[type=submit], input[type=button], a[role=button]', 120)) {
    const r = visible(el);
    const a = r ? covers(r, 2) : 0;
    if (a > 0) color(getComputedStyle(el).backgroundColor, 'button', Math.min(1, a * 8));
  }
  for (const el of all('a[href]', 300)) {
    const r = visible(el);
    const a = r ? covers(r, 2) : 0;
    if (a > 0) color(getComputedStyle(el).color, 'link', Math.min(0.3, a * 4));
  }
  for (const el of all('body > *, main > *, main section, body > div > *, [class*="hero"]', 200)) {
    const r = visible(el);
    const a = r ? covers(r, 1) : 0;
    if (a >= 0.08) color(getComputedStyle(el).backgroundColor, 'section', Math.min(1, a));
  }
  const pageBg = solid(bodyStyle?.backgroundColor) || solid(rootStyle.backgroundColor) || '#ffffff';
  out.colors.push({ hex: pageBg, from: 'background', weight: 0.5 });
  color(bodyStyle?.color, 'text', 0.5);

  // --- logo candidates ---
  const nameWords = [...new Set([out.siteName, out.host.split('.')[0]].map((s) => String(s ?? '').toLowerCase().replace(/[^a-z0-9]+/g, '')).filter((s) => s.length >= 3))];
  const saysName = (s) => {
    const t = String(s).toLowerCase().replace(/[^a-z0-9]+/g, '');
    return nameWords.some((w) => t.includes(w));
  };
  // The words around an element: its own and up to two ancestors' (stopping at its link).
  const wordsOf = (el, extra = '') => {
    let s = extra;
    let e = el;
    for (let i = 0; e && i < 3; i++, e = e.parentElement) {
      s += ` ${['class', 'id', 'aria-label', 'alt', 'title'].map((k) => e.getAttribute?.(k) ?? '').join(' ')}`;
      if (e.tagName === 'A') break;
    }
    return s;
  };
  const homePath = /^\/([a-z]{2}(-[a-z]{2,4})?\/?)?(index\.(html?|php|aspx?))?$/i;
  const isHome = (a) => {
    try {
      const u = new URL(a.getAttribute('href') ?? '', location.href);
      return u.origin === location.origin && homePath.test(u.pathname) && !u.search;
    } catch {
      return false;
    }
  };
  const stripSel = '[class*="partner" i], [class*="client" i], [class*="customer" i], [class*="sponsor" i], [class*="logos" i], [class*="trusted" i], [class*="brands" i], [id*="partner" i], [id*="client" i], [id*="sponsor" i]';
  const headerSel = 'header, [role=banner], .header, #header, .site-header, .navbar, .masthead, .topbar, .top-bar';
  const backdropOf = (el) => {
    for (let e = el.parentElement; e; e = e.parentElement) {
      const c = rgba(getComputedStyle(e).backgroundColor);
      if (c && c[3] >= 0.5) return hex(c);
    }
    return pageBg;
  };
  const facts = (el, r, kind, url, extra = '') => {
    const words = wordsOf(el, extra);
    const link = el.closest('a[href]');
    return {
      kind, url, width: Math.round(r.width), height: Math.round(r.height), top: Math.round(r.top + scrollY), left: Math.round(r.left + scrollX),
      logoWord: logoRe.test(words), nameWord: saysName(words), home: !!link && isHome(link), header: !!el.closest(headerSel),
      nav: !!el.closest('nav, [role=navigation]'), button: !!el.closest('button, [role=button]'), strip: !!el.closest(stripSel), backdrop: backdropOf(el),
    };
  };
  const SVGNS = 'http://www.w3.org/2000/svg';
  const PAINT = ['fill', 'fill-opacity', 'fill-rule', 'clip-rule', 'stroke', 'stroke-width', 'stroke-opacity', 'stroke-linecap', 'stroke-linejoin', 'stroke-dasharray', 'opacity'];
  const TEXT = ['font-family', 'font-size', 'font-weight', 'font-style', 'letter-spacing', 'text-anchor'];
  const DEFAULTS = { opacity: '1', 'fill-opacity': '1', 'stroke-opacity': '1', stroke: 'none', 'stroke-dasharray': 'none', 'fill-rule': 'nonzero', 'clip-rule': 'nonzero',
    'stroke-linecap': 'butt', 'stroke-linejoin': 'miter', 'stop-opacity': '1', 'font-style': 'normal', 'letter-spacing': 'normal', 'text-anchor': 'start' };
  // A paint server inside the SVG stays a local reference (url(#g)), never the page's address.
  const localUrl = (v) => v.replace(/url\(\s*["']?[^"')#]*(#[^"')]+)["']?\s*\)/g, 'url($1)');
  // An SVG drawn in the page as a file of its own: the page's CSS (colours,
  // currentColor) written onto its shapes, its size, and the symbols it uses.
  const serialize = (svg) => {
    const clone = svg.cloneNode(true);
    const from = [svg, ...svg.querySelectorAll('*')];
    const to = [clone, ...clone.querySelectorAll('*')];
    for (let i = 0; i < from.length && i < to.length && i < 3000; i++) {
      const st = getComputedStyle(from[i]);
      const tag = from[i].tagName.toLowerCase();
      if (st.display === 'none') {
        to[i].setAttribute('display', 'none');
        continue;
      }
      const props = tag === 'stop' ? ['stop-color', 'stop-opacity'] : tag === 'text' || tag === 'tspan' ? [...PAINT, ...TEXT] : PAINT;
      for (const p of props) {
        const v = st.getPropertyValue(p);
        if (!v || DEFAULTS[p] === v || (p === 'stroke-width' && st.getPropertyValue('stroke') === 'none')) continue;
        to[i].setAttribute(p, localUrl(v));
      }
    }
    const r = svg.getBoundingClientRect();
    const w = String(Math.max(1, Math.round(r.width)));
    const h = String(Math.max(1, Math.round(r.height)));
    clone.setAttribute('xmlns', SVGNS);
    if (!clone.getAttribute('viewBox')) clone.setAttribute('viewBox', `0 0 ${w} ${h}`);
    clone.setAttribute('width', w);
    clone.setAttribute('height', h);
    clone.removeAttribute('style'); // its page sizes (100%, 2em) mean nothing on their own
    const ids = new Set([...clone.querySelectorAll('[id]')].map((e) => e.id));
    let defs = null;
    for (const u of clone.querySelectorAll('use')) {
      const ref = u.getAttribute('href') ?? u.getAttributeNS('http://www.w3.org/1999/xlink', 'href') ?? '';
      if (!ref.startsWith('#') || ids.has(ref.slice(1))) continue;
      const t = document.getElementById(ref.slice(1));
      if (!t || !(t instanceof SVGElement)) continue;
      defs ??= clone.insertBefore(document.createElementNS(SVGNS, 'defs'), clone.firstChild);
      defs.appendChild(t.cloneNode(true));
      ids.add(ref.slice(1));
    }
    const s = new XMLSerializer().serializeToString(clone);
    return s.length <= maxSvg ? s : '';
  };
  const svgColors = (svg) => {
    const set = new Set();
    let n = 0;
    for (const e of [svg, ...svg.querySelectorAll('*')]) {
      if (++n > 400) break;
      const st = getComputedStyle(e);
      for (const p of ['fill', 'stroke']) {
        const c = rgba(st.getPropertyValue(p));
        if (c && c[3] > 0.1) set.add(hex(c));
      }
    }
    return set.size;
  };
  // A picture's own pixels, when the page may read them (same site): is it
  // see-through, and how many colours make up most of it?
  const look = (img) => {
    try {
      if (!img.complete || !img.naturalWidth) return {};
      const s = 48;
      const c = document.createElement('canvas');
      c.width = s;
      c.height = s;
      const x = c.getContext('2d', { willReadFrequently: true });
      x.drawImage(img, 0, 0, s, s);
      const d = x.getImageData(0, 0, s, s).data;
      let clear = 0;
      let opaque = 0;
      const bins = new Map();
      for (let i = 0; i < d.length; i += 4) {
        if (d[i + 3] < 128) {
          clear++;
          continue;
        }
        opaque++;
        const k = ((d[i] >> 5) << 6) | ((d[i + 1] >> 5) << 3) | (d[i + 2] >> 5);
        bins.set(k, (bins.get(k) ?? 0) + 1);
      }
      let cover = 0;
      let n = 0;
      for (const v of [...bins.values()].sort((a, b) => b - a)) {
        if (cover >= opaque * 0.9) break;
        cover += v;
        n++;
      }
      return { transparent: clear > s * s * 0.02, colorCount: n };
    } catch {
      return {}; // a picture from another site can't be read
    }
  };
  const near = (el) => logoRe.test(wordsOf(el)) || !!el.closest(headerSel) || !!el.closest('nav, [role=navigation]') || (!!el.closest('a[href]') && isHome(el.closest('a[href]')));
  const max = 40;
  for (const svg of document.querySelectorAll('svg')) {
    if (out.candidates.length >= max) break;
    if (svg.parentElement?.closest('svg')) continue; // only whole drawings
    const r = visible(svg);
    if (!r || !near(svg)) continue;
    const markup = serialize(svg);
    if (markup) out.candidates.push({ ...facts(svg, r, 'svg', ''), svg: markup, transparent: true, colorCount: svgColors(svg) });
  }
  for (const img of document.querySelectorAll('img')) {
    if (out.candidates.length >= max) break;
    const r = visible(img);
    const src = img.currentSrc || img.src;
    if (!r || !src) continue;
    const file = ` ${src.split(/[?#]/)[0].split('/').pop() ?? ''}`;
    if (!(near(img) || logoRe.test(file) || saysName(wordsOf(img, file)))) continue;
    out.candidates.push({ ...facts(img, r, 'img', abs(src), file), ...look(img) });
  }
  for (const el of document.querySelectorAll('[class*="logo" i], [id*="logo" i]')) {
    if (out.candidates.length >= max) break;
    if (el.tagName === 'IMG' || el.closest('svg')) continue;
    const m = /url\(\s*["']?([^"')]+)["']?\s*\)/.exec(getComputedStyle(el).backgroundImage ?? '');
    const r = m ? visible(el) : null;
    if (r) out.candidates.push(facts(el, r, 'background', abs(m[1])));
  }
  for (const l of document.querySelectorAll('link[rel][href]')) {
    if (out.candidates.length >= max) break;
    const rel = String(l.getAttribute('rel')).toLowerCase().split(/\s+/);
    const kind = rel.includes('apple-touch-icon') || rel.includes('apple-touch-icon-precomposed') ? 'touch-icon' : rel.includes('mask-icon') ? 'mask-icon' : rel.includes('icon') ? 'icon' : '';
    const url = kind ? abs(l.getAttribute('href')) : '';
    if (!url) continue;
    const sizes = String(l.getAttribute('sizes') ?? '').toLowerCase();
    const size = sizes === 'any' ? 512 : Math.max(0, ...sizes.split(/\s+/).map((x) => Number(x.split('x')[0]) || 0));
    out.candidates.push({ kind, url, type: String(l.getAttribute('type') ?? '').toLowerCase(), size: size || (kind === 'touch-icon' ? 180 : 0) });
    if (kind === 'mask-icon') color(l.getAttribute('color'), 'mask-icon', 0.8);
  }
  const og = meta('meta[property="og:image"]') || meta('meta[name="twitter:image"]');
  if (og && abs(og)) out.candidates.push({ kind: 'preview', url: abs(og), logoWord: logoRe.test(og.split(/[?#]/)[0]) });

  // --- fonts ---
  const firstFamily = (stack) => {
    for (const part of String(stack ?? '').split(',')) {
      const f = part.trim().replace(/^["']|["']$/g, '').trim();
      if (f && !generic.has(f.toLowerCase())) return f.slice(0, 80);
    }
    return '';
  };
  for (const [sel, from] of [['h1', 'h1'], ['body', 'body'], ['h2', 'h2'], ['nav', 'nav']]) {
    const el = document.querySelector(sel);
    const f = el ? firstFamily(getComputedStyle(el).fontFamily) : '';
    if (f) out.used.push({ family: f, from });
  }
  try {
    for (const ff of document.fonts) {
      const f = ff.status === 'loaded' ? firstFamily(ff.family) : '';
      if (f && !out.loaded.includes(f) && out.loaded.length < 20) out.loaded.push(f);
    }
  } catch {
    // no font list
  }
  for (const l of document.querySelectorAll('link[href*="fonts.googleapis.com/css"]')) out.google.push({ href: l.href, loaded: !!l.sheet && !l.disabled });
  const imports = (sheet, depth) => {
    let rules;
    try {
      rules = sheet.cssRules;
    } catch {
      return;
    }
    for (const r of rules ?? []) {
      if (r.href && /^https:\/\/fonts\.googleapis\.com\/css/.test(r.href)) out.google.push({ href: r.href, loaded: !!r.styleSheet });
      else if (r.styleSheet && depth < 3) imports(r.styleSheet, depth + 1);
    }
  };
  for (const sh of document.styleSheets) imports(sh, 0);
  out.google = out.google.slice(0, 10);
  return out;
};

/** The options READ_BRAND runs with (the rules above, which the page can't import). */
export const readBrandOptions = () => ({ maxSvgBytes: MAX_SVG_BYTES, logoWords: LOGO_WORDS, varRules: COLOR_VAR_RULES, generic: GENERIC_FONTS });

/**
 * How logo-like a candidate is, with the reasons. An element of the page earns
 * points for saying "logo" or the site's name, linking home, sitting in the
 * header or navigation at the top left, a logo's size and shape, a see-through
 * background and few colours, and loses them in a button, a strip of other
 * companies' logos or far down the page. The page's own icons and its preview
 * picture get fixed scores (larger first), after real logos.
 */
export const scoreLogo = (c, view = { width: 1440, height: 900 }) => {
  const hints = [];
  const w = Number(c.width) || 0;
  const h = Number(c.height) || 0;
  let s = 0;
  if (ICON_KINDS.has(c.kind) || c.kind === 'preview') {
    const size = Number(c.size) || Math.max(w, h);
    const svg = /svg/.test(c.type ?? '') || /\.svg(\?|#|$)/i.test(c.url ?? '');
    switch (c.kind) {
      case 'touch-icon':
        s = 3;
        hints.push('its app icon');
        break;
      case 'mask-icon':
        s = 1.8;
        hints.push('its pinned-tab icon');
        break;
      case 'icon':
        s = svg ? 2.8 : size >= 96 ? 2.4 : size >= 48 ? 2 : 1.4;
        hints.push('its site icon');
        break;
      default:
        s = 0.8;
        hints.push('its preview picture');
        if (c.logoWord) {
          s += 2;
          hints.push('says logo');
        }
    }
    if (c.kind !== 'preview' && size >= 128) s += 0.3;
    return { score: round(s), hints };
  }
  if (c.logoWord) {
    s += 4;
    hints.push('says logo');
  }
  if (c.nameWord) {
    s += 2;
    hints.push('names the site');
  }
  if (c.home) {
    s += 2.5;
    hints.push('links home');
  }
  if (c.header) {
    s += 1.5;
    hints.push('in the header');
  } else if (c.nav) {
    s += 1;
    hints.push('in the navigation');
  }
  const top = Number(c.top) || 0;
  if (top < 200) {
    s += 1;
    hints.push('at the top');
  } else if (top < view.height) s += 0.3;
  else s -= 1.5;
  if ((Number(c.left) || 0) < view.width / 3) s += 0.5;
  if (w < 12 || h < 12) {
    s -= 4;
    hints.push('tiny');
  } else if (w < 24 && h < 24) {
    s -= 1.5;
    hints.push('small');
  } else if (w > 700 || h > 320) {
    s -= 2.5;
    hints.push('large');
  } else s += 1;
  const aspect = w && h ? w / h : 1;
  s += aspect > 12 || aspect < 0.3 ? -1 : 0.3;
  if (c.button && !c.logoWord) {
    s -= 2;
    hints.push('in a button');
  }
  if (c.strip && !c.home && !c.header) {
    s -= 4;
    hints.push("among other companies' logos");
  }
  if (c.kind === 'svg' || c.transparent) {
    s += 0.5;
    hints.push('see-through');
  }
  if (Number.isFinite(c.colorCount)) {
    if (c.colorCount <= 4) {
      s += 0.5;
      hints.push('few colours');
    } else if (c.colorCount > 24) {
      s -= 1;
      hints.push('many colours');
    }
  }
  return { score: round(s), hints };
};

const kindOrder = ['svg', 'img', 'background', 'touch-icon', 'icon', 'mask-icon', 'preview'];

/**
 * The candidates worth keeping, most logo-like first: scored, an element of
 * the page only above MIN_LOGO_SCORE, .ico files left out, one per address
 * (or per drawing), at most `max`… and then some spares, so a download that
 * fails can be replaced (keepBrand stops at MAX_LOGOS).
 */
export const rankLogos = (cands, view, max = MAX_LOGOS * 3) => {
  const seen = new Set();
  return (cands ?? [])
    .map((c) => ({ ...c, ...scoreLogo(c, view) }))
    .filter((c) => (ICON_KINDS.has(c.kind) || c.kind === 'preview' ? c.score > 0 : c.score >= MIN_LOGO_SCORE))
    .filter((c) => c.svg || (/^(https?|data):/i.test(c.url ?? '') && !/\.ico(\?|#|$)/i.test(c.url) && !/icon$/.test(c.type ?? '')))
    .sort((a, b) => b.score - a.score || kindOrder.indexOf(a.kind) - kindOrder.indexOf(b.kind) || String(a.url ?? '').localeCompare(String(b.url ?? '')))
    .filter((c) => {
      const key = c.svg ? `svg:${crypto.createHash('sha256').update(c.svg).digest('hex')}` : c.url;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .slice(0, max);
};

const sourceRank = (from) => {
  const f = String(from ?? '');
  const i = COLOR_SOURCES.indexOf(f.startsWith('css') ? 'css' : f);
  return i < 0 ? COLOR_SOURCES.length : i;
};

/**
 * One entry per colour: weights added up, the strongest source kept (theme
 * colour, then the page's colour names, the logo, the header, buttons…).
 * The page's declared colours come first, then by weight; at most `max`.
 */
export const mergeColors = (list, max = 16) => {
  const by = new Map();
  for (const c of list ?? []) {
    const hex = normHex(c?.hex);
    const w = Number(c?.weight);
    if (!hex || !(w > 0)) continue;
    const cur = by.get(hex);
    const rank = sourceRank(c.from);
    if (!cur) by.set(hex, { hex, from: String(c.from ?? ''), weight: w, rank });
    else {
      cur.weight += w;
      if (rank < cur.rank) {
        cur.from = String(c.from ?? '');
        cur.rank = rank;
      }
    }
  }
  const declared = (c) => (c.rank <= sourceRank('css') ? 0 : 1);
  return [...by.values()]
    .sort((a, b) => declared(a) - declared(b) || b.weight - a.weight || a.hex.localeCompare(b.hex))
    .slice(0, max)
    .map(({ hex, from, weight }) => ({ hex, from, weight: Math.round(weight * 1000) / 1000 }));
};

/** The first real family of a CSS font-family list ("Inter", not system-ui or sans-serif). */
export const firstFamily = (stack) => {
  for (const part of String(stack ?? '').split(',')) {
    const f = part.trim().replace(/^["']|["']$/g, '').trim();
    if (f && !GENERIC_FONTS.includes(f.toLowerCase())) return f;
  }
  return '';
};

/** The families a Google Fonts stylesheet link asks for (css2 and the older css form). */
export const googleFamilies = (href) => {
  let u;
  try {
    u = new URL(href);
  } catch {
    return [];
  }
  if (u.protocol !== 'https:' || u.hostname !== 'fonts.googleapis.com' || !/^\/css2?$/.test(u.pathname)) return [];
  const out = [];
  for (const fam of u.searchParams.getAll('family')) {
    for (const part of fam.split('|')) {
      const name = part.split(':')[0].replace(/\+/g, ' ').trim();
      if (name && /^[\p{L}\p{N} '-]{1,80}$/u.test(name)) out.push(name);
    }
  }
  return [...new Set(out)];
};

/**
 * The page's fonts, the most telling first: the headings', the text's, the
 * navigation's, then Google Fonts links and other fonts it loaded. `google`
 * says the family is on Google Fonts.
 */
export const pickFonts = (raw, max = 8) => {
  const googleLinks = (raw?.google ?? []).flatMap((g) => googleFamilies(g.href).map((family) => ({ family, href: g.href, loaded: !!g.loaded })));
  const isGoogle = new Set(googleLinks.map((g) => g.family.toLowerCase()));
  const out = [];
  const seen = new Set();
  const add = (family, from) => {
    const f = String(family ?? '').trim();
    if (!f || seen.has(f.toLowerCase()) || GENERIC_FONTS.includes(f.toLowerCase())) return;
    seen.add(f.toLowerCase());
    out.push({ family: f, from, google: isGoogle.has(f.toLowerCase()) });
  };
  for (const u of raw?.used ?? []) add(u.family, u.from);
  for (const g of googleLinks) add(g.family, 'google');
  for (const f of raw?.loaded ?? []) add(f, 'loaded');
  return out.slice(0, max);
};

/**
 * The one Google font whose file is kept: the most telling family that is on
 * Google Fonts and whose stylesheet the page actually loaded (so fetching it
 * again reveals nothing new). Null when there is none.
 */
export const fontToKeep = (fonts, google) => {
  for (const f of fonts ?? []) {
    if (!f.google) continue;
    for (const g of google ?? []) {
      if (g.loaded && googleFamilies(g.href).some((x) => x.toLowerCase() === f.family.toLowerCase())) return { family: f.family, href: g.href };
    }
  }
  return null;
};

/**
 * The regular weight's WOFF2 file of a family in a Google Fonts stylesheet:
 * upright, the weight nearest 400 (a variable font's range counts), the Latin
 * subset first. Only fonts.gstatic.com addresses. "" when there is none.
 */
export const googleFontFile = (css, family) => {
  const want = String(family ?? '').toLowerCase();
  const blocks = [];
  for (const m of String(css ?? '').matchAll(/(?:\/\*\s*([\w-]+)\s*\*\/\s*)?@font-face\s*\{([^}]*)\}/g)) {
    const body = m[2];
    const fam = /font-family:\s*['"]?([^;'"]+?)['"]?\s*;/i.exec(body)?.[1]?.trim().toLowerCase() ?? '';
    const style = /font-style:\s*([\w-]+)/i.exec(body)?.[1]?.toLowerCase() ?? 'normal';
    const wm = /font-weight:\s*(\d+)(?:\s+(\d+))?/i.exec(body);
    const lo = wm ? Number(wm[1]) : 400;
    const hi = wm?.[2] ? Number(wm[2]) : lo;
    const url = /url\(\s*(['"]?)(https:\/\/fonts\.gstatic\.com\/[^)'"\s]+)\1\s*\)\s*format\(\s*['"]?woff2['"]?\s*\)/i.exec(body)?.[2] ?? '';
    const range = /unicode-range:\s*([^;]+)/i.exec(body)?.[1] ?? '';
    const latin = m[1] === 'latin' || /U\+0000-00FF/i.test(range) || !range;
    if (url && fam === want) blocks.push({ style, dist: lo <= 400 && 400 <= hi ? 0 : Math.min(Math.abs(lo - 400), Math.abs(hi - 400)), latin, url });
  }
  blocks.sort((a, b) => (a.style === 'normal' ? 0 : 1) - (b.style === 'normal' ? 0 : 1) || a.dist - b.dist || (a.latin ? 0 : 1) - (b.latin ? 0 : 1));
  return blocks[0]?.url ?? '';
};

/** What a downloaded picture is, by its first bytes (never by its name): png, jpg, gif, webp, avif, svg, or "" (an .ico, or not a picture). */
export const pictureExt = (body) => {
  const b = Buffer.isBuffer(body) ? body : Buffer.from(body ?? []);
  if (b.length >= 8 && b[0] === 0x89 && b.toString('latin1', 1, 4) === 'PNG') return 'png';
  if (b.length >= 3 && b[0] === 0xff && b[1] === 0xd8 && b[2] === 0xff) return 'jpg';
  if (b.length >= 6 && /^GIF8[79]a$/.test(b.toString('latin1', 0, 6))) return 'gif';
  if (b.length >= 12 && b.toString('latin1', 0, 4) === 'RIFF' && b.toString('latin1', 8, 12) === 'WEBP') return 'webp';
  if (b.length >= 12 && b.toString('latin1', 4, 8) === 'ftyp' && /^avi[fs]$/.test(b.toString('latin1', 8, 12))) return 'avif';
  const head = b.toString('utf8', 0, Math.min(b.length, 4096)).replace(/^﻿/, '').trimStart();
  if (/^(<\?xml[^>]*>\s*)?(<!--[\s\S]*?-->\s*)*(<!doctype svg[^>]*>\s*)?(<!--[\s\S]*?-->\s*)*<svg[\s>]/i.test(head)) return 'svg';
  return '';
};

/** A picture written into the page's address itself (data:…), as bytes and its kind. */
export const fromDataURL = (u, maxBytes = 5 << 20) => {
  const m = /^data:([^;,]*)((?:;[^;,]*)*?)(;base64)?,([\s\S]*)$/i.exec(String(u ?? ''));
  if (!m || m[4].length > maxBytes * 2) return null;
  let body;
  try {
    body = m[3] ? Buffer.from(m[4], 'base64') : Buffer.from(decodeURIComponent(m[4]), 'utf8');
  } catch {
    return null;
  }
  const ext = body.length > 0 && body.length <= maxBytes ? pictureExt(body) : '';
  return ext ? { body, ext } : null;
};

/** A font family as a file name ("Fraunces.woff2"): its family is read from the name. */
export const fontFileName = (family) => `${String(family ?? '').replace(/[^\p{L}\p{N} _-]+/gu, '').trim().slice(0, 60) || 'font'}.woff2`;

/**
 * Keeps the brand READ_BRAND found: the best logo and icon pictures (at most
 * MAX_LOGOS, one per picture) and a Google font's regular weight, written into
 * dir; the colours and fonts as they are. Returns what the page event carries
 * as `brand` (null when the page says nothing about its brand). `fetch` is
 * fetchSmall (a test passes its own).
 */
export const keepBrand = async (raw, { dir, allowLocal = false, userAgent = '', fetch = fetchSmall, log = () => {} } = {}) => {
  if (!raw || !dir) return null;
  const view = raw.viewport ?? { width: 1440, height: 900 };
  const logos = [];
  const kept = new Set();
  for (const c of rankLogos(raw.candidates, view)) {
    if (logos.length >= MAX_LOGOS) break;
    try {
      let got = null;
      if (c.svg) got = { body: Buffer.from(c.svg, 'utf8'), ext: 'svg' };
      else if (/^data:/i.test(c.url)) got = fromDataURL(c.url);
      else {
        const res = await fetch(c.url, { allowLocal, maxBytes: 5 << 20, timeoutMs: 15000, userAgent, accept: 'image/avif,image/webp,image/svg+xml,image/*;q=0.8' });
        const ext = pictureExt(res.body);
        got = ext && res.body.length > 0 ? { body: res.body, ext } : null;
      }
      if (!got) continue;
      const sum = crypto.createHash('sha256').update(got.body).digest('hex');
      if (kept.has(sum)) continue; // the same picture under another address
      kept.add(sum);
      const file = path.join(dir, `logo-${logos.length + 1}.${got.ext}`);
      fs.writeFileSync(file, got.body);
      logos.push({ file, url: c.svg ? '' : c.url, kind: c.kind, hints: c.hints, width: Number(c.width) || 0, height: Number(c.height) || 0, score: c.score, backdrop: normHex(c.backdrop) });
    } catch (e) {
      log(`logo ${c.svg ? '(drawn in the page)' : String(c.url).slice(0, 120)}: ${e.message}`);
    }
  }
  const colors = mergeColors(raw.colors);
  const fonts = pickFonts(raw);
  const keep = fontToKeep(fonts, raw.google);
  if (keep) {
    try {
      const css = await fetch(keep.href, { allowLocal, maxBytes: 512 << 10, timeoutMs: 15000, userAgent, accept: 'text/css,*/*;q=0.1' });
      const url = googleFontFile(css.body.toString('utf8'), keep.family);
      if (url) {
        const font = await fetch(url, { allowLocal, maxBytes: 5 << 20, timeoutMs: 15000, userAgent, accept: '*/*' });
        if (font.body.length > 4 && font.body.toString('latin1', 0, 4) === 'wOF2') {
          const file = path.join(dir, fontFileName(keep.family));
          fs.writeFileSync(file, font.body);
          const f = fonts.find((x) => x.family.toLowerCase() === keep.family.toLowerCase());
          if (f) Object.assign(f, { file, url });
        }
      }
    } catch (e) {
      log(`the font ${keep.family}: ${e.message}`);
    }
  }
  if (!logos.length && !colors.length && !fonts.length) return null;
  return { logos, colors, fonts };
};
