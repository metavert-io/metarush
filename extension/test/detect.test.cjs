// Unit tests for the extension's page detection: node --test extension/test/
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const D = require('../lib/detect.js');

test('video pages on YouTube, TikTok and X', () => {
  assert.deepEqual(D.videoPage('https://www.youtube.com/watch?v=aqz-KE-bpKQ&t=3'), { platform: 'youtube', videoId: 'aqz-KE-bpKQ', url: 'https://www.youtube.com/watch?v=aqz-KE-bpKQ' });
  assert.equal(D.videoPage('https://www.youtube.com/shorts/abcdefghijk').videoId, 'abcdefghijk');
  assert.equal(D.videoPage('https://www.youtube.com/@BlenderOfficial'), null);
  assert.equal(D.videoPage('https://www.youtube.com/'), null);
  const tt = D.videoPage('https://www.tiktok.com/@gardenclub42/video/7000000000000000042?lang=en');
  assert.equal(tt.platform, 'tiktok');
  assert.equal(tt.handle, '@gardenclub42');
  assert.equal(tt.url, 'https://www.tiktok.com/@gardenclub42/video/7000000000000000042');
  const x = D.videoPage('https://twitter.com/primevideouk/status/1578401165338976258/video/1');
  assert.equal(x.platform, 'x');
  assert.equal(x.handle, '@primevideouk');
  assert.equal(x.url, 'https://x.com/primevideouk/status/1578401165338976258');
  assert.equal(D.videoPage('https://x.com/home'), null);
  assert.equal(D.videoPage('not a url'), null);
  assert.equal(D.videoPage('https://example.com/watch?v=aqz-KE-bpKQ'), null);
});

test('copy-protected video is refused; files are fetched; streams are recorded', () => {
  assert.equal(D.saveMethod({ mediaKeys: {}, currentSrc: 'blob:https://www.youtube.com/1' }), 'drm');
  assert.equal(D.saveMethod({ mediaKeys: {}, currentSrc: 'https://cdn.example/v.mp4' }), 'drm', 'DRM wins even for a plain file address');
  assert.equal(D.saveMethod({ encrypted: true, currentSrc: 'blob:x' }), 'drm');
  assert.equal(D.saveMethod({ mediaKeys: null, currentSrc: 'https://v16-webapp.tiktok.com/video.mp4' }), 'direct');
  assert.equal(D.saveMethod({ mediaKeys: null, currentSrc: 'blob:https://www.youtube.com/abc' }), 'record');
  assert.equal(D.saveMethod({ mediaKeys: null, currentSrc: '' }), 'none');
  assert.equal(D.saveMethod(null), 'none');
});

test('the main video is the largest one showing something', () => {
  const small = { clientWidth: 100, clientHeight: 50, readyState: 4 };
  const big = { clientWidth: 1280, clientHeight: 720, readyState: 4 };
  const empty = { clientWidth: 1920, clientHeight: 1080, readyState: 0 };
  assert.equal(D.mainVideo([small, big, empty]), big);
  assert.equal(D.mainVideo([]), null);
  const notYet = { clientWidth: 640, clientHeight: 360, readyState: 0, currentSrc: 'https://cdn.example/v.mp4' };
  assert.equal(D.mainVideo([notYet]), notYet, 'a video that has not started loading still counts when it has a source');
  assert.equal(D.mainVideo([{ clientWidth: 640, clientHeight: 360, readyState: 0 }]), null);
});

function fakeDoc(map) {
  return {
    title: map.title || '',
    querySelector(sel) {
      for (const [k, v] of Object.entries(map)) {
        if (sel.split(',').map((s) => s.trim()).includes(k)) return v;
      }
      return null;
    },
  };
}

const el = (attrs, textContent = '') => ({ textContent, getAttribute: (k) => attrs[k] ?? null });

test('provenance read from a YouTube page', () => {
  const page = D.videoPage('https://www.youtube.com/watch?v=aqz-KE-bpKQ');
  const doc = fakeDoc({
    'meta[name="title"]': el({ content: 'Big Buck Bunny 60fps 4K' }),
    '#owner ytd-channel-name a': el({ href: '/@BlenderOfficial' }, ' Blender '),
    'meta[itemprop="uploadDate"]': el({ content: '2014-11-10T06:05:55-08:00' }),
    'meta[name="description"]': el({ content: 'UHD High Frame rate version' }),
  });
  const p = D.provenance(page, doc);
  assert.equal(p.title, 'Big Buck Bunny 60fps 4K');
  assert.equal(p.uploader, 'Blender');
  assert.equal(p.handle, '@BlenderOfficial');
  assert.equal(p.channelUrl, 'https://www.youtube.com/@BlenderOfficial');
  assert.equal(p.uploadDate, '2014-11-10');
  assert.equal(p.url, 'https://www.youtube.com/watch?v=aqz-KE-bpKQ');
});

test('provenance read from an X post', () => {
  const page = D.videoPage('https://x.com/primevideouk/status/1578401165338976258');
  const article = fakeDoc({ '[data-testid="tweetText"]': el({}, 'A new power will be forged.'), 'time[datetime]': el({ datetime: '2022-10-07T15:05:37.000Z' }), '[data-testid="User-Name"] span': el({}, 'Prime Video UK & IE') });
  const doc = fakeDoc({ 'article[data-testid="tweet"]': article });
  const p = D.provenance(page, doc);
  assert.equal(p.title, 'A new power will be forged.');
  assert.equal(p.handle, '@primevideouk');
  assert.equal(p.uploadDate, '2022-10-07');
  assert.equal(p.uploader, 'Prime Video UK & IE');
});

test('file names and recorder formats', () => {
  assert.equal(D.fileName('a/b:c?', 'webm'), 'a b c.webm');
  assert.equal(D.fileName('', 'mp4'), 'video.mp4');
  assert.equal(D.recorderType((t) => t === 'video/webm'), 'video/webm');
  assert.equal(D.recorderType((t) => t.startsWith('video/mp4')), 'video/mp4;codecs=avc1.640028,mp4a.40.2');
  assert.equal(D.recorderType(() => false), '');
});
