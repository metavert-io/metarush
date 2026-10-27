// What a video page is and how "Send to MetaRush" can save its video. Shared by
// the content script (as a classic script: globalThis.MetaRushDetect) and the
// Node tests (module.exports). No network, no page changes.
(function (root) {
  'use strict';

  /** The site and post a page shows, or null when it is not a video page. */
  function videoPage(href) {
    let u;
    try {
      u = new URL(href);
    } catch {
      return null;
    }
    const host = u.hostname.replace(/^(www|m|mobile)\./, '');
    const parts = u.pathname.split('/').filter(Boolean);
    if (host === 'youtube.com') {
      if (parts[0] === 'watch' && /^[\w-]{11}$/.test(u.searchParams.get('v') || '')) {
        const id = u.searchParams.get('v');
        return { platform: 'youtube', videoId: id, url: 'https://www.youtube.com/watch?v=' + id };
      }
      if ((parts[0] === 'shorts' || parts[0] === 'live') && /^[\w-]{11}$/.test(parts[1] || '')) {
        return { platform: 'youtube', videoId: parts[1], url: 'https://www.youtube.com/watch?v=' + parts[1] };
      }
      return null;
    }
    if (host === 'tiktok.com') {
      if (parts.length >= 3 && parts[0].startsWith('@') && parts[1] === 'video' && /^\d{5,25}$/.test(parts[2])) {
        return { platform: 'tiktok', videoId: parts[2], handle: parts[0], url: 'https://www.tiktok.com/' + parts[0] + '/video/' + parts[2] };
      }
      return null;
    }
    if (host === 'x.com' || host === 'twitter.com') {
      if (parts.length >= 3 && parts[1] === 'status' && /^\d{5,25}$/.test(parts[2])) {
        const handle = parts[0] === 'i' ? '' : '@' + parts[0];
        return { platform: 'x', videoId: parts[2], handle, url: 'https://x.com/' + parts[0] + '/status/' + parts[2] };
      }
      return null;
    }
    return null;
  }

  /**
   * How the video element can be saved:
   *   drm     copy-protected (Encrypted Media Extensions in use): refused, never worked around
   *   direct  a plain file address the browser can fetch with your sign-in
   *   record  a stream in pieces (blob:/MediaSource): recorded while it plays, in real time
   *   none    no video playing
   */
  function saveMethod(v) {
    if (!v) return 'none';
    if (v.mediaKeys || v.encrypted) return 'drm';
    const src = String(v.currentSrc || v.src || '');
    if (/^https?:\/\//i.test(src)) return 'direct';
    if (src.startsWith('blob:') || v.srcObject || src.startsWith('mediasource:')) return v.canRecord === false ? 'none' : 'record';
    return src ? 'record' : 'none';
  }

  /**
   * The main player: the largest video element showing something, else the
   * largest one with a source (a video that hasn't started loading yet).
   */
  function mainVideo(videos) {
    let best = null;
    let area = 0;
    let fallback = null;
    let fallbackArea = 0;
    for (const v of videos) {
      const w = v.clientWidth || v.videoWidth || 0;
      const h = v.clientHeight || v.videoHeight || 0;
      if (w * h > area && (v.readyState === undefined || v.readyState > 0)) {
        best = v;
        area = w * h;
      }
      if (w * h > fallbackArea && (v.currentSrc || v.src || v.srcObject)) {
        fallback = v;
        fallbackArea = w * h;
      }
    }
    return best || fallback;
  }

  function text(el) {
    return el ? String(el.textContent || '').replace(/\s+/g, ' ').trim() : '';
  }

  function meta(doc, sel) {
    const el = doc.querySelector(sel);
    return el ? String(el.getAttribute('content') || '').trim() : '';
  }

  /** Provenance read from the page: title, account, date (whatever the page shows). */
  function provenance(page, doc) {
    const out = { platform: page.platform, url: page.url, videoId: page.videoId, title: '', uploader: '', handle: page.handle || '', channelUrl: '', uploadDate: '', description: '' };
    if (page.platform === 'youtube') {
      out.title = meta(doc, 'meta[name="title"]') || text(doc.querySelector('h1.ytd-watch-metadata, h1 yt-formatted-string')) || String(doc.title || '').replace(/ - YouTube$/, '');
      const owner = doc.querySelector('#owner ytd-channel-name a, ytd-video-owner-renderer a[href^="/@"], a.yt-simple-endpoint[href^="/@"]');
      out.uploader = text(owner) || meta(doc, 'link[itemprop="name"]');
      const href = owner ? owner.getAttribute('href') || '' : '';
      const m = href.match(/^\/(@[\w.-]+)/);
      if (m) {
        out.handle = m[1];
        out.channelUrl = 'https://www.youtube.com/' + m[1];
      }
      out.uploadDate = (meta(doc, 'meta[itemprop="uploadDate"]') || meta(doc, 'meta[itemprop="datePublished"]')).slice(0, 10);
      out.description = meta(doc, 'meta[name="description"]');
    } else if (page.platform === 'tiktok') {
      out.description = meta(doc, 'meta[property="og:description"]') || meta(doc, 'meta[name="description"]');
      out.title = text(doc.querySelector('[data-e2e="browse-video-desc"], h1[data-e2e="video-desc"]')) || out.description;
      out.uploader = text(doc.querySelector('[data-e2e="browse-username"], [data-e2e="video-author-uniqueid"]')) || '';
      if (out.handle) out.channelUrl = 'https://www.tiktok.com/' + out.handle;
    } else if (page.platform === 'x') {
      const article = doc.querySelector('article[data-testid="tweet"]') || doc;
      out.description = text(article.querySelector('[data-testid="tweetText"]'));
      out.title = out.description;
      const name = article.querySelector('[data-testid="User-Name"] span');
      out.uploader = text(name);
      const time = article.querySelector('time[datetime]');
      if (time) out.uploadDate = String(time.getAttribute('datetime') || '').slice(0, 10);
      if (out.handle) out.channelUrl = 'https://x.com/' + out.handle.slice(1);
    }
    out.title = out.title.slice(0, 300);
    out.description = out.description.slice(0, 1000);
    if (!/^\d{4}-\d{2}-\d{2}$/.test(out.uploadDate)) out.uploadDate = '';
    return out;
  }

  /** A file name for the saved video. */
  function fileName(title, ext) {
    const base = String(title || 'video').replace(/[\u0000-\u001f/\\:*?"<>|]/g, ' ').replace(/\s+/g, ' ').trim().replace(/^[.\s]+|[.\s]+$/g, '').slice(0, 120) || 'video';
    return base + '.' + (ext || 'mp4');
  }

  /** The recorder format: MP4 where the browser can record it, else WebM. */
  function recorderType(isSupported) {
    for (const t of ['video/mp4;codecs=avc1.640028,mp4a.40.2', 'video/mp4', 'video/webm;codecs=vp9,opus', 'video/webm;codecs=vp8,opus', 'video/webm']) {
      if (isSupported(t)) return t;
    }
    return '';
  }

  const api = { videoPage, saveMethod, mainVideo, provenance, fileName, recorderType };
  root.MetaRushDetect = api;
  if (typeof module !== 'undefined' && module.exports) module.exports = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
