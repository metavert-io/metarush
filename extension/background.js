// Send to MetaRush: the extension's service worker. It is the only part that
// talks to MetaRush (on this computer at http://127.0.0.1, or on the computer
// that runs it over Tailscale at https://<name>.ts.net: host permissions, so
// no web page can borrow it), with the send-only token it got when it paired.
// It never reads anything back from MetaRush: it starts uploads, sends their
// bytes, asks for link imports, and sends the page on screen ("Send this
// page": a screenshot of the tab and its text, D105).
/* global importScripts, MetaRushPage, MetaRushAddress */
importScripts('lib/page.js', 'lib/address.js');

const DEFAULT_BASE = 'http://127.0.0.1:4845';
const CHUNK = 8 * 1024 * 1024;

function version() {
  return chrome.runtime.getManifest().version;
}

function browserName() {
  const ua = navigator.userAgent || '';
  if (/Edg\//.test(ua)) return 'Edge';
  if (/OPR\//.test(ua)) return 'Opera';
  if (/Brave/.test(ua) || (navigator.brave && navigator.brave.isBrave)) return 'Brave';
  if (/Chrome\//.test(ua)) return 'Chrome';
  if (/Firefox\//.test(ua)) return 'Firefox';
  if (/Safari\//.test(ua)) return 'Safari';
  return 'A browser';
}

async function settings() {
  const s = await chrome.storage.local.get(['base', 'token', 'machine', 'who']);
  return { base: s.base || DEFAULT_BASE, token: s.token || '', machine: s.machine || '', who: s.who || '' };
}

class ExtError extends Error {
  constructor(code, message, hint) {
    super(message);
    this.code = code;
    this.hint = hint || '';
  }
}

function plain(err) {
  if (err instanceof ExtError) return { code: err.code, message: err.message, hint: err.hint };
  return { code: 'E_DAEMON_UNREACHABLE', message: 'MetaRush isn’t answering.', hint: 'Check that MetaRush is running (and, for another computer, that Tailscale is connected), then try again.' };
}

/** A request to MetaRush on this computer. */
async function call(path, opts = {}) {
  const { base, token } = await settings();
  const headers = { 'X-MetaRush-Client': 'extension', 'X-MetaRush-Extension-Version': version(), Accept: 'application/json', ...(opts.headers || {}) };
  if (!opts.noAuth) {
    if (!token) throw new ExtError('E_NOT_PAIRED', 'This browser isn’t paired with MetaRush yet.', 'Open the extension and pair it with a code from MetaRush (Settings, Browser extension).');
    headers.Authorization = 'Bearer ' + token;
  }
  let body = opts.body;
  if (opts.json !== undefined) {
    headers['Content-Type'] = 'application/json';
    body = JSON.stringify(opts.json);
  }
  let res;
  try {
    res = await fetch((opts.base || base) + path, { method: opts.method || 'POST', headers, body, credentials: 'omit', cache: 'no-store' });
  } catch {
    const remote = /^https:/.test(opts.base || base);
    throw new ExtError(
      'E_DAEMON_UNREACHABLE',
      remote ? 'MetaRush isn’t answering at its Tailscale address.' : 'MetaRush isn’t answering on this computer.',
      remote ? 'Check that Tailscale is connected here and that MetaRush runs on its computer, then try again.' : 'Open MetaRush again (ask Claude Code to open the MetaRush dashboard), then try again.',
    );
  }
  const j = await res.json().catch(() => null);
  if (!j || !j.ok) {
    const e = (j && j.error) || {};
    if (res.status === 401 && !opts.noAuth) await chrome.storage.local.remove(['token', 'machine', 'who']);
    throw new ExtError(e.code || 'E_HTTP_' + res.status, e.message || 'MetaRush couldn’t do that (error ' + res.status + ').', e.hint);
  }
  return j.data;
}

async function pair(input, code) {
  const addr = MetaRushAddress.check(input || DEFAULT_BASE);
  if (!addr.ok) throw new ExtError('E_INVALID_INPUT', addr.message);
  const base = addr.base;
  const out = await call('/ext/pair', { base, noAuth: true, json: { code, name: browserName(), version: version() } });
  // The pairing is the person's who made the code: what this browser sends is theirs.
  await chrome.storage.local.set({ base, token: out.token, machine: out.machine, who: out.for || '' });
  return { paired: true, machine: out.machine, who: out.for || '' };
}

async function status() {
  const s = await settings();
  if (!s.token) return { paired: false, base: s.base };
  try {
    const out = await call('/ext/hello', { json: {} });
    if (out.for && out.for !== s.who) await chrome.storage.local.set({ who: out.for });
    return { paired: true, base: s.base, machine: out.machine, who: out.for || s.who, online: true };
  } catch (e) {
    const p = plain(e);
    const still = (await settings()).token !== '';
    return { paired: still, base: s.base, machine: s.machine, who: s.who, online: false, problem: p };
  }
}

async function unpair() {
  try {
    await call('/ext/unpair', { json: {} });
  } catch {
    // Unpaired here even when MetaRush is not running; revoke it there too (Settings).
  }
  await chrome.storage.local.remove(['token', 'machine', 'who']);
  return { paired: false };
}

/** Upload bytes (an ArrayBuffer source) through the resumable upload protocol. */
async function upload(page, name, size, method, readChunk, onProgress) {
  const rec = await call('/ext/uploads', { json: { name, size, method, source: page } });
  let offset = rec.offset || 0;
  let tries = 0;
  while (offset < size) {
    const end = Math.min(offset + CHUNK, size);
    const chunk = await readChunk(offset, end);
    try {
      const r = await call('/ext/uploads/' + encodeURIComponent(rec.id), {
        method: 'PATCH',
        headers: { 'Upload-Offset': String(offset), 'Content-Type': 'application/offset+octet-stream' },
        body: chunk,
      });
      offset = r.offset;
      tries = 0;
      onProgress(offset / size);
    } catch (e) {
      if (++tries > 3) throw e;
      // A hiccup: ask where MetaRush is and continue from there.
      const at = await call('/ext/uploads/' + encodeURIComponent(rec.id) + '/offset', { json: {} }).catch(() => null);
      if (at) offset = at.offset;
    }
  }
  return rec.id;
}

/** "Save from this browser" for a plain file: fetched with the browser's own sign-in. */
async function saveDirect(port, page, src) {
  port.postMessage({ type: 'progress', stage: 'download', pct: 0 });
  let res;
  try {
    res = await fetch(src, { credentials: 'include' });
  } catch {
    throw new ExtError('E_FETCH', 'The browser couldn’t fetch this video’s file directly.', 'Record it while it plays instead.');
  }
  if (!res.ok || !res.body) throw new ExtError('E_FETCH', 'The site refused to hand over the video file (error ' + res.status + ').', 'Record it while it plays instead.');
  const type = (res.headers.get('Content-Type') || '').toLowerCase();
  if (type.startsWith('text/') || type.includes('mpegurl') || type.includes('dash+xml')) {
    throw new ExtError('E_FETCH', 'This video streams in pieces, so it can’t be saved as one file.', 'Record it while it plays instead.');
  }
  const total = Number(res.headers.get('Content-Length') || 0);
  const reader = res.body.getReader();
  const parts = [];
  let got = 0;
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    parts.push(value);
    got += value.byteLength;
    if (got > 4 * 1024 * 1024 * 1024) throw new ExtError('E_TOO_LARGE', 'This video is too large to save from the browser.', 'Use “Import the original” instead.');
    port.postMessage({ type: 'progress', stage: 'download', pct: total ? got / total : 0, bytes: got });
  }
  const blob = new Blob(parts, { type: type || 'video/mp4' });
  const ext = type.includes('webm') ? 'webm' : 'mp4';
  const name = MetaRushName(page.title, ext);
  await upload(page, name, blob.size, 'browser', (a, b) => blob.slice(a, b).arrayBuffer(), (pct) => port.postMessage({ type: 'progress', stage: 'upload', pct }));
}

function MetaRushName(title, ext) {
  const base = String(title || 'video').replace(/[\u0000-\u001f/\\:*?"<>|]/g, ' ').replace(/\s+/g, ' ').trim().replace(/^[.\s]+|[.\s]+$/g, '').slice(0, 120) || 'video';
  return base + '.' + ext;
}

function fromBase64(b64) {
  const s = atob(b64);
  const out = new Uint8Array(s.length);
  for (let i = 0; i < s.length; i++) out[i] = s.charCodeAt(i);
  return out;
}

// One port per "Send to MetaRush" from a page. While it is open the service
// worker stays alive (the page sends progress and a heartbeat).
chrome.runtime.onConnect.addListener((port) => {
  if (port.name !== 'metarush-send') return;
  let recording = null; // { page, name, size, chunks: Uint8Array[], have }
  port.onMessage.addListener(async (msg) => {
    try {
      switch (msg.type) {
        case 'ping':
          return;
        case 'import': {
          const out = await call('/ext/imports', { json: { url: msg.page.url } });
          port.postMessage({ type: 'done', how: 'import', message: out.message, status: out.status });
          return;
        }
        case 'direct':
          await saveDirect(port, msg.page, msg.src);
          port.postMessage({ type: 'done', how: 'browser', message: 'Sent to MetaRush. It is in the inbox and being prepared now.' });
          return;
        case 'record-begin':
          recording = { page: msg.page, name: msg.name, size: msg.size, chunks: [], have: 0 };
          port.postMessage({ type: 'ack' });
          return;
        case 'record-chunk': {
          const bytes = fromBase64(msg.data);
          recording.chunks.push(bytes);
          recording.have += bytes.byteLength;
          port.postMessage({ type: 'ack' });
          if (msg.last) {
            const blob = new Blob(recording.chunks);
            recording.chunks = [];
            await upload(recording.page, recording.name, blob.size, 'recording', (a, b) => blob.slice(a, b).arrayBuffer(), (pct) => port.postMessage({ type: 'progress', stage: 'upload', pct }));
            port.postMessage({ type: 'done', how: 'recording', message: 'Sent to MetaRush. It is in the inbox and being prepared now.' });
            recording = null;
          }
          return;
        }
      }
    } catch (e) {
      port.postMessage({ type: 'error', ...plain(e) });
    }
  });
});

/**
 * "Send this page": the tab as the person sees it, in their own signed-in
 * browser — a screenshot of what is on screen and the page's text — becomes a
 * web page snapshot in MetaRush's resources library. The person chose the tab
 * by opening the extension on it (activeTab); nothing else is read.
 */
async function sendPage(tabId) {
  const tab = await chrome.tabs.get(tabId);
  if (!tab || !/^https?:\/\//.test(tab.url || '')) {
    throw new ExtError('E_INVALID_INPUT', 'Open a web page first, then send it.');
  }
  let screenshot;
  try {
    screenshot = await chrome.tabs.captureVisibleTab(tab.windowId, { format: 'png' });
  } catch {
    throw new ExtError('E_CAPTURE', 'The browser didn’t let the extension see this page.', 'Click the extension’s button while the page is open, then choose Send this page.');
  }
  let info = {};
  try {
    const [res] = await chrome.scripting.executeScript({ target: { tabId }, func: MetaRushPage.readPage });
    info = (res && res.result) || {};
  } catch {
    // Some pages (the browser's own) can't be read: send the picture alone.
  }
  const out = await call('/ext/pages', {
    json: {
      url: tab.url,
      title: info.title || tab.title || '',
      description: info.description || '',
      siteName: info.siteName || '',
      themeColor: MetaRushPage.hexColor(info.themeColor),
      lang: info.lang || '',
      text: MetaRushPage.cleanText(info.text, 200000),
      screenshot,
    },
  });
  return { sent: true, status: out.status, message: out.message };
}

// The popup asks for pairing and status, and sends the page on screen.
chrome.runtime.onMessage.addListener((msg, _sender, reply) => {
  const run = async () => {
    switch (msg && msg.type) {
      case 'pair':
        return pair(msg.base, msg.code);
      case 'status':
        return status();
      case 'unpair':
        return unpair();
      case 'send-page':
        return sendPage(msg.tabId);
    }
    return { error: { code: 'E_INVALID_INPUT', message: 'Unknown request.' } };
  };
  run()
    .then((out) => reply(out))
    .catch((e) => reply({ error: plain(e) }));
  return true; // reply asynchronously
});
