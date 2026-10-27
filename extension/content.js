// Send to MetaRush: the button on YouTube, TikTok and X video pages. It reads
// what the page shows (title, account, date) and sends the video to MetaRush on
// this computer, either as a link MetaRush downloads itself ("Import the
// original") or saved in this browser with your sign-in ("Save from this
// browser": the file when the page plays a plain file, otherwise a real-time
// recording of the playing video). Copy-protected (DRM) video is refused.
(() => {
  'use strict';
  if (window.top !== window || globalThis.__metarushSend) return;
  globalThis.__metarushSend = true;
  const D = globalThis.MetaRushDetect;

  const css = `
    :host { all: initial; }
    .btn { position: fixed; right: 18px; bottom: 18px; z-index: 2147483646; display: inline-flex; align-items: center; gap: 8px;
      padding: 10px 14px; border-radius: 999px; border: 0; cursor: pointer; font: 600 13px/1 system-ui, -apple-system, sans-serif;
      background: #f2a03d; color: #1b1206; box-shadow: 0 6px 24px rgba(0,0,0,.35); }
    .btn:hover { filter: brightness(1.05); }
    .btn:focus-visible { outline: 2px solid #fff; outline-offset: 2px; }
    .panel { position: fixed; right: 18px; bottom: 70px; z-index: 2147483647; width: 340px; max-width: calc(100vw - 36px);
      border-radius: 16px; padding: 16px; background: #17171c; color: #ececf1; font: 13px/1.45 system-ui, -apple-system, sans-serif;
      box-shadow: 0 12px 40px rgba(0,0,0,.5); border: 1px solid rgba(255,255,255,.08); }
    .panel h2 { margin: 0 0 2px; font-size: 14px; font-weight: 600; }
    .who { color: #a7a7b3; font-size: 12px; margin-bottom: 12px; overflow-wrap: anywhere; }
    .row { display: flex; flex-direction: column; gap: 8px; }
    button.act { all: unset; box-sizing: border-box; display: block; width: 100%; padding: 10px 12px; border-radius: 10px; cursor: pointer;
      background: #24242c; border: 1px solid rgba(255,255,255,.1); }
    button.act:hover:not([disabled]) { background: #2c2c36; }
    button.act[disabled] { opacity: .5; cursor: default; }
    button.act b { display: block; font-size: 13px; }
    button.act span { display: block; color: #a7a7b3; font-size: 12px; margin-top: 2px; }
    .status { margin-top: 12px; font-size: 12.5px; }
    .status.err { color: #ff8f8f; }
    .status.ok { color: #8fe3a5; }
    .bar { height: 4px; border-radius: 4px; background: #2c2c36; margin-top: 8px; overflow: hidden; }
    .bar > i { display: block; height: 100%; width: 0; background: #f2a03d; transition: width .3s; }
    .close { all: unset; position: absolute; top: 10px; right: 10px; cursor: pointer; color: #a7a7b3; padding: 4px 8px; border-radius: 6px; }
    .close:hover { color: #fff; background: #24242c; }
    .stop { margin-top: 10px; }
  `;

  let host = null;
  let root = null;
  let panel = null;
  let busy = false;

  // Only a person's own clicks count: a page script can see the button (the
  // shadow root is open, for accessibility tools and tests) but a synthetic
  // click it dispatches is not trusted, so it can never send a video.
  function trusted(fn) {
    return (e) => {
      if (!e.isTrusted) return;
      fn(e);
    };
  }

  function ui() {
    if (host) return;
    host = document.createElement('metarush-send');
    root = host.attachShadow({ mode: 'open' });
    const style = document.createElement('style');
    style.textContent = css;
    root.appendChild(style);
    const btn = document.createElement('button');
    btn.className = 'btn';
    btn.type = 'button';
    btn.textContent = 'Send to MetaRush';
    btn.setAttribute('aria-label', 'Send this video to MetaRush');
    btn.addEventListener('click', trusted(() => (panel ? closePanel() : openPanel())));
    root.appendChild(btn);
    document.documentElement.appendChild(host);
  }

  function refresh() {
    const page = D.videoPage(location.href);
    if (page) {
      ui();
      host.style.display = '';
    } else if (host) {
      host.style.display = 'none';
      if (!busy) closePanel();
    }
  }

  function closePanel() {
    if (panel && !busy) {
      panel.remove();
      panel = null;
    }
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function siteName(p) {
    return { youtube: 'YouTube', tiktok: 'TikTok', x: 'X' }[p] || 'the web';
  }

  function clock(s) {
    if (!isFinite(s) || s <= 0) return '';
    s = Math.round(s);
    const h = Math.floor(s / 3600);
    const m = Math.floor((s % 3600) / 60);
    const sec = String(s % 60).padStart(2, '0');
    return h ? `${h}:${String(m).padStart(2, '0')}:${sec}` : `${m}:${sec}`;
  }

  function openPanel() {
    const page = D.videoPage(location.href);
    if (!page) return;
    const info = D.provenance(page, document);
    const video = D.mainVideo(document.querySelectorAll('video'));
    const method = D.saveMethod(video ? { mediaKeys: video.mediaKeys, currentSrc: video.currentSrc, src: video.src, srcObject: video.srcObject } : null);
    panel = el('div', 'panel');
    panel.setAttribute('role', 'dialog');
    panel.setAttribute('aria-label', 'Send to MetaRush');
    const close = el('button', 'close', '×');
    close.type = 'button';
    close.setAttribute('aria-label', 'Close');
    close.addEventListener('click', trusted(closePanel));
    panel.appendChild(close);
    panel.appendChild(el('h2', '', info.title || 'This video'));
    panel.appendChild(el('div', 'who', [info.handle || info.uploader, siteName(page.platform)].filter(Boolean).join(' on ')));
    const row = el('div', 'row');
    const status = el('div', 'status');
    const bar = el('div', 'bar');
    const fill = el('i');
    bar.appendChild(fill);
    bar.style.display = 'none';

    const imp = el('button', 'act');
    imp.type = 'button';
    imp.appendChild(el('b', '', 'Import the original'));
    imp.appendChild(el('span', '', 'MetaRush downloads it on this computer, best quality.'));
    imp.addEventListener('click', trusted(() => run('import', info, null, status, bar, fill, [imp, save])));

    const save = el('button', 'act');
    save.type = 'button';
    save.appendChild(el('b', '', 'Save from this browser'));
    let note = '';
    if (method === 'drm') note = 'This video is copy-protected (DRM), so it can’t be saved. MetaRush never works around copy protection.';
    else if (method === 'direct') note = 'Saves the file this page plays, with your sign-in.';
    else if (method === 'record') note = `This video streams in pieces, so it is recorded while it plays, in real time${video && video.duration ? ` (${clock(video.duration)})` : ''}. Keep this tab open.`;
    else note = 'Start playing the video first.';
    save.appendChild(el('span', '', note));
    save.disabled = method === 'drm' || method === 'none';
    save.addEventListener('click', trusted(() => run(method, info, video, status, bar, fill, [imp, save])));

    row.appendChild(imp);
    row.appendChild(save);
    panel.appendChild(row);
    panel.appendChild(bar);
    panel.appendChild(status);
    root.appendChild(panel);
  }

  function say(status, text, tone) {
    status.textContent = text;
    status.className = 'status' + (tone ? ' ' + tone : '');
  }

  function connect() {
    const port = chrome.runtime.connect({ name: 'metarush-send' });
    const waiters = [];
    const listeners = [];
    port.onMessage.addListener((m) => {
      if (m.type === 'ack') {
        const w = waiters.shift();
        if (w) w();
        return;
      }
      for (const l of listeners) l(m);
    });
    const ping = setInterval(() => port.postMessage({ type: 'ping' }), 15000);
    port.onDisconnect.addListener(() => clearInterval(ping));
    return {
      send: (m) => port.postMessage(m),
      ack: () => new Promise((r) => waiters.push(r)),
      on: (fn) => listeners.push(fn),
      close: () => {
        clearInterval(ping);
        port.disconnect();
      },
    };
  }

  async function run(how, info, video, status, bar, fill, buttons) {
    if (busy) return;
    busy = true;
    buttons.forEach((b) => (b.disabled = true));
    const port = connect();
    const finished = new Promise((resolve) => {
      port.on((m) => {
        if (m.type === 'progress') {
          bar.style.display = '';
          fill.style.width = Math.round((m.pct || 0) * 100) + '%';
          say(status, m.stage === 'upload' ? `Sending to MetaRush… ${Math.round((m.pct || 0) * 100)}%` : `Getting the video… ${m.pct ? Math.round(m.pct * 100) + '%' : ''}`);
        } else if (m.type === 'done') {
          fill.style.width = '100%';
          say(status, m.message || 'Sent to MetaRush.', 'ok');
          resolve(true);
        } else if (m.type === 'error') {
          say(status, [m.message, m.hint].filter(Boolean).join(' '), 'err');
          resolve(false);
        }
      });
    });
    try {
      if (how === 'import') {
        say(status, 'Asking MetaRush to import it…');
        port.send({ type: 'import', page: info });
      } else if (how === 'direct') {
        say(status, 'Getting the video…');
        port.send({ type: 'direct', page: info, src: video.currentSrc || video.src });
      } else if (how === 'record') {
        await record(video, info, port, status, bar, fill);
      } else if (how === 'drm') {
        say(status, 'This video is copy-protected (DRM), so it can’t be saved.', 'err');
        port.send({ type: 'ping' });
        busy = false;
        port.close();
        return;
      }
      await finished;
    } catch (e) {
      say(status, e && e.message ? e.message : String(e), 'err');
    } finally {
      busy = false;
      port.close();
      buttons.forEach((b) => (b.disabled = false));
    }
  }

  function toBase64(buf) {
    const bytes = new Uint8Array(buf);
    let s = '';
    for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode.apply(null, bytes.subarray(i, i + 0x8000));
    return btoa(s);
  }

  async function record(video, info, port, status, bar, fill) {
    if (video.mediaKeys) throw new Error('This video is copy-protected (DRM), so it can’t be saved. MetaRush never works around copy protection.');
    let stream;
    try {
      stream = video.captureStream ? video.captureStream() : video.mozCaptureStream();
    } catch {
      throw new Error('This browser can’t record this video (the site doesn’t allow it). Try “Import the original”.');
    }
    const type = D.recorderType((t) => window.MediaRecorder && MediaRecorder.isTypeSupported(t));
    if (!type) throw new Error('This browser can’t record video.');
    const rec = new MediaRecorder(stream, { mimeType: type, videoBitsPerSecond: 8000000, audioBitsPerSecond: 192000 });
    const parts = [];
    rec.ondataavailable = (e) => e.data && e.data.size && parts.push(e.data);
    const stopped = new Promise((r) => (rec.onstop = r));
    video.currentTime = 0;
    await video.play().catch(() => undefined);
    rec.start(1000);
    bar.style.display = '';
    const stop = el('button', 'act stop');
    stop.type = 'button';
    stop.appendChild(el('b', '', 'Stop and send what was recorded'));
    stop.addEventListener('click', trusted(() => rec.state !== 'inactive' && rec.stop()));
    status.parentNode.appendChild(stop);
    const tick = setInterval(() => {
      const d = video.duration || 0;
      fill.style.width = d ? Math.round((video.currentTime / d) * 100) + '%' : '0%';
      say(status, `Recording in real time… ${clock(video.currentTime)}${d ? ' of ' + clock(d) : ''}. Keep this tab open.`);
      port.send({ type: 'ping' });
    }, 1000);
    video.addEventListener('ended', () => rec.state !== 'inactive' && rec.stop(), { once: true });
    await stopped;
    clearInterval(tick);
    stop.remove();
    const blob = new Blob(parts, { type });
    if (!blob.size) throw new Error('Nothing was recorded.');
    const ext = type.startsWith('video/mp4') ? 'mp4' : 'webm';
    say(status, 'Sending to MetaRush…');
    port.send({ type: 'record-begin', page: { ...info }, name: D.fileName(info.title, ext), size: blob.size });
    await port.ack();
    const CH = 4 * 1024 * 1024;
    for (let off = 0; off < blob.size; off += CH) {
      const buf = await blob.slice(off, Math.min(off + CH, blob.size)).arrayBuffer();
      port.send({ type: 'record-chunk', data: toBase64(buf), last: off + CH >= blob.size });
      await port.ack();
    }
  }

  // The extension's popup can open the panel for the page it is on.
  chrome.runtime.onMessage.addListener((msg, _s, reply) => {
    if (msg && msg.type === 'open-panel') {
      refresh();
      if (D.videoPage(location.href)) {
        if (!panel) openPanel();
        reply({ ok: true });
      } else reply({ ok: false });
    }
  });

  refresh();
  setInterval(refresh, 1000); // the sites change pages without reloading
})();
