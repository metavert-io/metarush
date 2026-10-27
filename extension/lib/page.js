// "Send this page": what the page on screen says about itself and its
// readable text. readPage runs inside the tab (chrome.scripting, a function
// with no outside variables); cleanText and hexColor are shared with the
// Node tests (module.exports). No network, no page changes.
(function (root) {
  'use strict';

  /** Collapses spaces and blank lines, and keeps at most max characters. */
  function cleanText(s, max) {
    const t = String(s || '')
      .replace(/[ \t ]+/g, ' ')
      .replace(/ *\n */g, '\n')
      .replace(/\n{3,}/g, '\n\n')
      .trim();
    return t.length > max ? t.slice(0, max) : t;
  }

  /** "rgb(226, 85, 45)" or "#e2552d" → "#e2552d" (empty when unreadable). */
  function hexColor(c) {
    const s = String(c || '').trim().toLowerCase();
    if (/^#[0-9a-f]{6}$/.test(s)) return s;
    if (/^#[0-9a-f]{3}$/.test(s)) return '#' + s[1] + s[1] + s[2] + s[2] + s[3] + s[3];
    const m = /rgba?\((\d+),\s*(\d+),\s*(\d+)/.exec(s);
    return m ? '#' + [m[1], m[2], m[3]].map((x) => Math.min(255, Number(x)).toString(16).padStart(2, '0')).join('') : '';
  }

  /**
   * Runs in the tab: the page's title, description, site name, theme colour,
   * language and readable text. Self-contained (it is serialized into the tab).
   */
  function readPage() {
    const meta = (sel) => {
      const el = document.querySelector(sel);
      return el ? (el.getAttribute('content') || '').trim() : '';
    };
    let theme = meta('meta[name="theme-color"]');
    if (theme) {
      const el = document.createElement('div');
      el.style.color = theme;
      document.documentElement.appendChild(el);
      theme = getComputedStyle(el).color;
      el.remove();
    }
    const main = document.querySelector('main, article, [role=main]') || document.body;
    let text = main ? main.innerText : '';
    if (text.length < 200 && document.body) text = document.body.innerText;
    return {
      title: (document.title || meta('meta[property="og:title"]')).slice(0, 300),
      description: (meta('meta[name="description"]') || meta('meta[property="og:description"]')).slice(0, 600),
      siteName: meta('meta[property="og:site_name"]').slice(0, 120),
      themeColor: theme,
      lang: (document.documentElement.lang || '').slice(0, 20),
      text: String(text || '').slice(0, 200000),
    };
  }

  const api = { cleanText, hexColor, readPage };
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.MetaRushPage = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
