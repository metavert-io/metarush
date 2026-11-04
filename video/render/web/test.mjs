#!/usr/bin/env node
// Checks for the web page snapshot's way out (D105; CI: npm run test:motion -w
// video runs this too): which addresses count as this computer or a local
// network, and that the filtering proxy and the small-file download never
// reach them — a loopback "sink" server must see no request — while still
// reaching an allowed address, within the byte budget. Then the site's brand
// (D165, brand.mjs), offline: how logo-like candidates are, colours, fonts,
// Google's CSS, and keeping the pictures with a stand-in for the download.
import fs from 'node:fs';
import http from 'node:http';
import net from 'node:net';
import os from 'node:os';
import path from 'node:path';
import {
  colorVarWeight, firstFamily, fontFileName, fontToKeep, fromDataURL, googleFamilies, googleFontFile, keepBrand, mergeColors, normHex, pickFonts, pictureExt,
  rankLogos, scoreLogo,
} from './brand.mjs';
import { checkHost, fetchSmall, ipv6Groups, isLocalIP, startFilteringProxy } from './proxy.mjs';

let failures = 0;
const check = (ok, what) => {
  process.stderr.write(`[web-test] ${ok ? 'PASS' : 'FAIL'}  ${what}\n`);
  if (!ok) failures++;
};

// --- addresses ---
const local = ['127.0.0.1', '127.8.9.1', '10.1.2.3', '172.16.0.1', '172.31.255.255', '192.168.1.5', '169.254.169.254', '100.64.0.1', '0.0.0.0', '0.1.2.3',
  '198.18.0.1', '224.0.0.1', '255.255.255.255', '::', '::1', '[::1]', 'fd00::1', 'fc00::1', 'fe80::1', 'fec0::1', 'ff02::1', '::ffff:127.0.0.1',
  '::ffff:7f00:1', '[::ffff:7f00:1]', '0:0:0:0:0:ffff:7f00:1', '::ffff:a00:1', '::7f00:1', '::127.0.0.1', '64:ff9b::7f00:1', '64:ff9b::192.168.0.1',
  '2002:7f00:1::', '2002:c0a8:101::1', 'fe80::1%en0', 'not-an-address', '::ffff:1:2:3:4:5:6'];
const public_ = ['93.184.216.34', '8.8.8.8', '172.32.0.1', '100.128.0.1', '2606:2800:220:1:248:1893:25c8:1946', '2001:4860:4860::8888', '::ffff:5db8:d822',
  '64:ff9b::5db8:d822', '2002:5db8:d822::1'];
for (const ip of local) check(isLocalIP(ip), `${ip} is local`);
for (const ip of public_) check(!isLocalIP(ip), `${ip} is not local`);
check(JSON.stringify(ipv6Groups('::ffff:127.0.0.1')) === JSON.stringify([0, 0, 0, 0, 0, 0xffff, 0x7f00, 1]), 'an IPv4 tail becomes two groups');
check(ipv6Groups('1:2:3:4:5:6:7:8:9') === null && ipv6Groups('1::2::3') === null && ipv6Groups('12345::') === null, 'malformed IPv6 is no address');

const refusedHost = async (host, opts) => {
  try {
    await checkHost(host, false, opts);
    return false;
  } catch (e) {
    return !!e.local;
  }
};
for (const h of ['localhost', 'app.localhost', 'printer.local', 'db.internal', 'nas', '127.0.0.1', '[::1]', '[::ffff:7f00:1]', '169.254.169.254']) {
  check(await refusedHost(h), `checkHost refuses ${h}`);
}
// A public-looking name whose answer is local (DNS rebinding) is refused; one
// answer local among public ones is enough.
const fakeDNS = (answers) => ({ lookup: async () => answers.map((address) => ({ address, family: net.isIPv6(address) ? 6 : 4 })) });
check(await refusedHost('rebind.example.com', fakeDNS(['127.0.0.1'])), 'a name that resolves to loopback is refused');
check(await refusedHost('mixed.example.com', fakeDNS(['93.184.216.34', '10.0.0.7'])), 'a name with one local answer is refused');
check(await refusedHost('mapped.example.com', fakeDNS(['::ffff:7f00:1'])), 'a name that resolves to a mapped loopback address is refused');
check((await checkHost('ok.example.com', false, fakeDNS(['93.184.216.34']))) === '93.184.216.34', 'a public name gives the address it was checked with');

// --- a loopback sink that must see nothing unless allowed ---
const seen = [];
const sink = http.createServer((req, res) => {
  seen.push(req.url);
  if (req.url === '/asked') {
    res.writeHead(200, { 'Content-Type': 'application/json' }).end(JSON.stringify({ ua: req.headers['user-agent'], accept: req.headers.accept }));
    return;
  }
  if (req.url === '/big') {
    res.writeHead(200, { 'Content-Type': 'application/octet-stream' });
    res.end(Buffer.alloc(200 << 10, 1));
    return;
  }
  if (req.url === '/moved') {
    res.writeHead(302, { Location: '/pic.png' }).end();
    return;
  }
  res.writeHead(200, { 'Content-Type': 'image/png' }).end('png');
});
sink.on('connect', (_req, socket) => {
  seen.push('CONNECT');
  socket.end('HTTP/1.1 200 OK\r\n\r\n');
});
// The sink answers on both loopbacks (127.0.0.1 and ::1), on one port: two listeners. One on '::'
// would be dual-stack, but it would also answer on every other interface — the house network,
// whoever is on it — for as long as the test runs.
const sink6 = http.createServer(sink.listeners('request')[0]);
sink6.on('connect', sink.listeners('connect')[0]);
const listenOn = (srv, p, host) =>
  new Promise((resolve, reject) => {
    srv.once('error', reject);
    srv.listen(p, host, () => {
      srv.off('error', reject);
      resolve();
    });
  });
let port = 0;
for (let tries = 0; ; tries++) {
  await listenOn(sink, 0, '127.0.0.1');
  port = sink.address().port;
  try {
    await listenOn(sink6, port, '::1'); // the same port on ::1, unless something has it there
    break;
  } catch (e) {
    await new Promise((r) => sink.close(r));
    if (tries === 9) throw e;
  }
}

const viaProxy = (proxyPort, target) =>
  new Promise((resolve) => {
    const req = http.request({ host: '127.0.0.1', port: proxyPort, method: 'GET', path: target, headers: { Host: new URL(target).host } }, (res) => {
      let n = 0;
      res.on('data', (d) => (n += d.length));
      res.on('end', () => resolve({ status: res.statusCode, bytes: n }));
      res.on('error', () => resolve({ status: res.statusCode, bytes: n, cut: true }));
      res.on('aborted', () => resolve({ status: res.statusCode, bytes: n, cut: true }));
    });
    req.on('error', () => resolve({ status: 0, bytes: 0, cut: true }));
    req.end();
  });
const tunnel = (proxyPort, hostPort) =>
  new Promise((resolve) => {
    const s = net.connect(proxyPort, '127.0.0.1', () => s.write(`CONNECT ${hostPort} HTTP/1.1\r\nHost: ${hostPort}\r\n\r\n`));
    let got = '';
    s.on('data', (d) => {
      got += d.toString();
      if (got.includes('\r\n\r\n')) {
        s.destroy();
        resolve(Number(/^HTTP\/1\.1 (\d+)/.exec(got)?.[1] ?? 0));
      }
    });
    s.on('error', () => resolve(0));
    s.on('close', () => resolve(Number(/^HTTP\/1\.1 (\d+)/.exec(got)?.[1] ?? 0)));
  });

{
  const proxy = await startFilteringProxy({ allowLocal: false });
  const targets = [`http://127.0.0.1:${port}/a`, `http://localhost:${port}/b`, `http://[::1]:${port}/c`, `http://[::ffff:7f00:1]:${port}/d`, `http://[::ffff:127.0.0.1]:${port}/e`,
    `http://0.0.0.0:${port}/f`];
  for (const t of targets) {
    const r = await viaProxy(proxy.port, t);
    check(r.status === 403, `the proxy refuses ${t} (${r.status})`);
  }
  for (const hp of [`127.0.0.1:${port}`, `localhost:${port}`, `[::1]:${port}`, `[::ffff:7f00:1]:${port}`]) {
    check((await tunnel(proxy.port, hp)) === 403, `the proxy refuses a tunnel to ${hp}`);
  }
  check(seen.length === 0, `the loopback sink saw nothing (${seen.join(', ') || 'no requests'})`);
  check(proxy.refused.size >= 4, 'the proxy records the hosts it refused');
  await proxy.close();
}

for (const u of [`http://127.0.0.1:${port}/pic.png`, `http://[::ffff:7f00:1]:${port}/pic.png`, `http://localhost:${port}/pic.png`, `http://user:pw@example.com/pic.png`,
  'file:///etc/hosts']) {
  let refused = false;
  try {
    await fetchSmall(u);
  } catch {
    refused = true;
  }
  check(refused, `the small download refuses ${u}`);
}
check(seen.length === 0, 'the loopback sink still saw nothing');

// --- allowed (tests only): it reaches the sink, within its limits ---
{
  const proxy = await startFilteringProxy({ allowLocal: true, budgetBytes: 64 << 10 });
  const ok = await viaProxy(proxy.port, `http://127.0.0.1:${port}/pic.png`);
  check(ok.status === 200 && ok.bytes === 3, `allowed: the proxy reaches the sink (${ok.status}, ${ok.bytes} bytes)`);
  check((await tunnel(proxy.port, `127.0.0.1:${port}`)) === 200, 'allowed: a tunnel is opened');
  const big = await viaProxy(proxy.port, `http://127.0.0.1:${port}/big`);
  check(big.bytes < 200 << 10, `the byte budget stops a page that downloads too much (${big.bytes} of ${200 << 10} bytes)`);
  await proxy.close();
}
{
  const got = await fetchSmall(`http://localhost:${port}/pic.png`, { allowLocal: true }).catch((e) => e);
  check(got?.body?.toString() === 'png', `allowed: a name is looked up once and the download goes to that address (${got?.message ?? 'ok'})`);
  const big = await fetchSmall(`http://127.0.0.1:${port}/big`, { allowLocal: true, maxBytes: 1024 }).catch((e) => e);
  check(big instanceof Error && /too large/.test(big.message), 'the small download stops past its size limit');
  const moved = await fetchSmall(`http://127.0.0.1:${port}/moved`, { allowLocal: true }).catch((e) => e);
  check(moved instanceof Error && /302/.test(moved.message), 'the small download follows no redirects');
  const asked = await fetchSmall(`http://127.0.0.1:${port}/asked`, { allowLocal: true, userAgent: 'Mozilla/5.0 Chrome/130', accept: 'text/css' }).catch((e) => e);
  const said = asked?.body ? JSON.parse(asked.body.toString()) : {};
  check(said.ua === 'Mozilla/5.0 Chrome/130' && said.accept === 'text/css', `the small download says what it asks for (${JSON.stringify(said)})`);
  const plain = await fetchSmall(`http://127.0.0.1:${port}/asked`, { allowLocal: true, userAgent: 'bad\r\nheader: x' }).catch((e) => e);
  check(/MetaRush snapshot/.test(plain?.body?.toString() ?? '') && /image/.test(plain?.body?.toString() ?? ''), 'a user agent with a line break is not sent');
}

// --- the site's brand (D165), offline ---
check(colorVarWeight('--brand-primary') === 0.9 && colorVarWeight('--brand') === 0.9 && colorVarWeight('--accent') === 0.7 && colorVarWeight('--brand-accent') === 0.7 &&
  colorVarWeight('--color-ink') === 0.4 && colorVarWeight('--link-color') === 0.4, 'colour names in CSS: brand and primary first, then accents, then other colour names');
check(colorVarWeight('--radius-pill') === 0 && colorVarWeight('--space-4') === 0, 'other CSS names are not colours');
check(normHex('#ABC') === '#aabbcc' && normHex('#E2552D') === '#e2552d' && normHex('red') === '' && normHex('#12345') === '', 'colours are written #rrggbb');

const view = { width: 1440, height: 900 };
const cands = [
  { kind: 'img', url: 'https://widget.example/logo.svg', width: 32, height: 32, top: 20, left: 86, logoWord: true, nameWord: true, home: true, header: true, transparent: true, colorCount: 2, backdrop: '#1b1b1f' },
  { kind: 'svg', url: '', svg: '<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 32"><circle cx="16" cy="16" r="14" fill="rgb(226, 85, 45)"/></svg>', width: 120, height: 32, top: 3640, left: 86,
    logoWord: true, nameWord: true, home: true, colorCount: 2, backdrop: '#fffaf5' },
  { kind: 'svg', url: '', svg: '<svg xmlns="http://www.w3.org/2000/svg"><path d="M2 5h16"/></svg>', width: 20, height: 20, top: 26, left: 1300, header: true, nav: true, button: true, colorCount: 1 },
  { kind: 'img', url: 'https://widget.example/partners/acme-logo.png', width: 120, height: 40, top: 1500, left: 200, logoWord: true, strip: true, colorCount: 3 },
  { kind: 'img', url: 'https://widget.example/hero.jpg', width: 1440, height: 600, top: 0, left: 0, header: true, colorCount: 40 },
  { kind: 'touch-icon', url: 'https://widget.example/icon.png', size: 180 },
  { kind: 'icon', url: 'https://widget.example/icon.png', type: 'image/png', size: 64 },
  { kind: 'icon', url: 'https://widget.example/favicon.ico', type: 'image/x-icon', size: 32 },
  { kind: 'preview', url: 'https://widget.example/preview.png' },
];
const header = scoreLogo(cands[0], view);
check(header.score > 10 && header.hints.includes('links home') && header.hints.includes('in the header'), `the header's home-link logo scores highest (${header.score}: ${header.hints.join(', ')})`);
check(scoreLogo(cands[2], view).score < 2, `a menu icon in a button is not a logo (${scoreLogo(cands[2], view).score})`);
check(scoreLogo(cands[3], view).score < 2, `another company's logo in a strip is not the site's (${scoreLogo(cands[3], view).score})`);
check(scoreLogo(cands[4], view).score < 2, `a large photo in the header is not a logo (${scoreLogo(cands[4], view).score})`);
const ranked = rankLogos(cands, view);
check(JSON.stringify(ranked.map((c) => c.kind)) === JSON.stringify(['img', 'svg', 'touch-icon', 'preview']),
  `logos are ordered by how logo-like they are, icons after them (${ranked.map((c) => `${c.kind} ${c.score}`).join(', ')})`);
check(ranked.filter((c) => c.url === 'https://widget.example/icon.png').length === 1 && !ranked.some((c) => /\.ico$/.test(c.url)), 'one candidate per address, and .ico files are left out');
check(JSON.stringify(rankLogos(cands, view)) === JSON.stringify(ranked), 'ranking is the same every time');

const merged = mergeColors([
  { hex: '#E2552D', from: 'theme-color', weight: 1 }, { hex: '#e2552d', from: 'css:--brand-primary', weight: 0.9 }, { hex: '#1b1b1f', from: 'header', weight: 0.2 },
  { hex: '#1b1b1f', from: 'button', weight: 0.05 }, { hex: '#1b1b1f', from: 'text', weight: 0.5 }, { hex: '#2d6ae2', from: 'css:--brand-accent', weight: 0.7 },
  { hex: '#fffaf5', from: 'background', weight: 0.5 }, { hex: 'nope', from: 'link', weight: 1 }, { hex: '#00ff00', from: 'link', weight: 0 },
]);
check(JSON.stringify(merged) === JSON.stringify([
  { hex: '#e2552d', from: 'theme-color', weight: 1.9 }, { hex: '#2d6ae2', from: 'css:--brand-accent', weight: 0.7 }, { hex: '#1b1b1f', from: 'header', weight: 0.75 },
  { hex: '#fffaf5', from: 'background', weight: 0.5 },
]), `one entry per colour, the page's own colour names first (${JSON.stringify(merged)})`);

check(firstFamily('-apple-system, "Segoe UI", Helvetica, Arial, sans-serif') === 'Segoe UI' && firstFamily('"Fraunces", Georgia, serif') === 'Fraunces' && firstFamily('system-ui, sans-serif') === '',
  'a font list names its first real family');
check(JSON.stringify(googleFamilies('https://fonts.googleapis.com/css2?family=Fraunces:wght@400;700&family=Open+Sans&display=swap')) === JSON.stringify(['Fraunces', 'Open Sans']) &&
  JSON.stringify(googleFamilies('https://fonts.googleapis.com/css?family=Roboto:400,700|Lobster')) === JSON.stringify(['Roboto', 'Lobster']) &&
  googleFamilies('https://fonts.example.com/css2?family=Fraunces').length === 0 && googleFamilies('http://fonts.googleapis.com/css2?family=Inter').length === 0,
'Google Fonts links name their families (only Google, only https)');
const fontLink = 'https://fonts.googleapis.com/css2?family=Fraunces:wght@400;700&display=swap';
const rawFonts = { used: [{ family: 'Fraunces', from: 'h1' }, { family: 'Segoe UI', from: 'body' }, { family: 'Fraunces', from: 'h2' }], loaded: ['Lobster'], google: [{ href: fontLink, loaded: false }] };
const fonts = pickFonts(rawFonts);
check(JSON.stringify(fonts) === JSON.stringify([{ family: 'Fraunces', from: 'h1', google: true }, { family: 'Segoe UI', from: 'body', google: false }, { family: 'Lobster', from: 'loaded', google: false }]),
  `fonts: headings first, Google's marked (${JSON.stringify(fonts)})`);
check(fontToKeep(fonts, rawFonts.google) === null, 'a Google font the page did not load is only named');
check(JSON.stringify(fontToKeep(fonts, [{ href: fontLink, loaded: true }])) === JSON.stringify({ family: 'Fraunces', href: fontLink }), 'a Google font the page loaded is kept');
const googleCSS = `/* latin-ext */
@font-face { font-family: 'Fraunces'; font-style: normal; font-weight: 400; src: url(https://fonts.gstatic.com/s/fraunces/v1/ext-400.woff2) format('woff2'); unicode-range: U+0100-02BA; }
/* latin */
@font-face { font-family: 'Fraunces'; font-style: italic; font-weight: 400; src: url(https://fonts.gstatic.com/s/fraunces/v1/italic-400.woff2) format('woff2'); unicode-range: U+0000-00FF, U+0131; }
/* latin */
@font-face { font-family: 'Fraunces'; font-style: normal; font-weight: 700; src: url(https://fonts.gstatic.com/s/fraunces/v1/latin-700.woff2) format('woff2'); unicode-range: U+0000-00FF, U+0131; }
/* latin */
@font-face { font-family: 'Fraunces'; font-style: normal; font-weight: 400; src: url(https://fonts.gstatic.com/s/fraunces/v1/latin-400.woff2) format('woff2'); unicode-range: U+0000-00FF, U+0131; }
@font-face { font-family: 'Evil'; font-style: normal; font-weight: 400; src: url(https://evil.example/x.woff2) format('woff2'); }`;
check(googleFontFile(googleCSS, 'Fraunces') === 'https://fonts.gstatic.com/s/fraunces/v1/latin-400.woff2', "Google's CSS: the upright regular weight, Latin first");
check(googleFontFile(googleCSS.replace(/font-weight: 400; src: url\(https:\/\/fonts.gstatic.com\/s\/fraunces\/v1\/latin-400/, 'font-weight: 100 900; src: url(https://fonts.gstatic.com/s/fraunces/v1/var'), 'Fraunces') ===
  'https://fonts.gstatic.com/s/fraunces/v1/var.woff2', "a variable font's range counts as the regular weight");
check(googleFontFile(googleCSS, 'Evil') === '' && googleFontFile(googleCSS, 'Inter') === '', 'only fonts.gstatic.com files, and only the family asked for');
check(fontFileName('Open Sans') === 'Open Sans.woff2' && fontFileName('../../etc') === 'etc.woff2' && fontFileName('') === 'font.woff2', 'a font file is named after its family, safely');

const PNG = Buffer.from('89504e470d0a1a0a0000000d4948445200000001000000010806000000', 'hex');
const SVG = Buffer.from('<?xml version="1.0"?>\n<!-- a logo -->\n<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64"><rect width="64" height="64" fill="#E2552D"/></svg>');
check(pictureExt(PNG) === 'png' && pictureExt(Buffer.from([0xff, 0xd8, 0xff, 0xe0])) === 'jpg' && pictureExt(Buffer.from('GIF89a......')) === 'gif' &&
  pictureExt(Buffer.from('RIFF\0\0\0\0WEBPVP8 ')) === 'webp' && pictureExt(SVG) === 'svg', 'pictures are recognized by their first bytes');
check(pictureExt(Buffer.from([0, 0, 1, 0, 1, 0])) === '' && pictureExt(Buffer.from('<!doctype html><html>')) === '' && pictureExt(Buffer.alloc(0)) === '', 'an .ico or a web page is not kept as a picture');
check(fromDataURL(`data:image/png;base64,${PNG.toString('base64')}`)?.ext === 'png' && fromDataURL(`data:image/svg+xml,${encodeURIComponent(SVG.toString())}`)?.ext === 'svg' &&
  fromDataURL('data:text/html,<b>hi</b>') === null && fromDataURL('data:image/svg+xml,%E0%A4%A') === null, 'pictures written into the page are read, other things are not');

{
  const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'mr-brand-'));
  const asked = [];
  const fakeFetch = async (url, opts) => {
    asked.push({ url, opts });
    if (url === 'https://widget.example/logo.svg') return { body: SVG, type: 'image/svg+xml' };
    if (url === 'https://widget.example/icon.png') return { body: PNG, type: 'image/png' };
    if (url === 'https://widget.example/same-as-icon.png') return { body: PNG, type: 'image/png' };
    if (url === fontLink) return { body: Buffer.from(googleCSS), type: 'text/css' };
    if (url === 'https://fonts.gstatic.com/s/fraunces/v1/latin-400.woff2') return { body: Buffer.from('wOF2fake-font-bytes'), type: 'font/woff2' };
    throw new Error('HTTP 404');
  };
  const raw = {
    viewport: view,
    candidates: [
      cands[0],
      { ...cands[0], url: 'https://widget.example/missing-logo.svg', top: 30 }, // fails to download: the next one takes its place
      { kind: 'touch-icon', url: 'https://widget.example/icon.png', size: 180 },
      { kind: 'icon', url: 'https://widget.example/same-as-icon.png', type: 'image/png', size: 96 }, // the same picture again
      cands[1],
      cands[8],
    ],
    colors: [{ hex: '#e2552d', from: 'theme-color', weight: 1 }, { hex: '#1b1b1f', from: 'header', weight: 0.2 }],
    used: rawFonts.used,
    loaded: [],
    google: [{ href: fontLink, loaded: true }],
  };
  const kept = await keepBrand(raw, { dir, userAgent: 'Mozilla/5.0 Chrome/130', fetch: fakeFetch });
  const files = (kept?.logos ?? []).map((l) => path.basename(l.file));
  check(JSON.stringify(files) === JSON.stringify(['logo-1.svg', 'logo-2.svg', 'logo-3.png']) && kept.logos.map((l) => l.kind).join(',') === 'img,svg,touch-icon',
    `the best three pictures are kept; one that can't be downloaded is replaced by the next (${files.join(', ')}: ${kept?.logos?.map((l) => l.kind).join(', ')})`);
  check(fs.readFileSync(path.join(dir, 'logo-2.svg'), 'utf8').startsWith('<svg') && kept.logos[1].url === '' && kept.logos[0].backdrop === '#1b1b1f',
    'a logo drawn in the page is written from its markup; the colour behind a logo comes along');
  const font = kept?.fonts?.find((f) => f.family === 'Fraunces');
  check(font?.file && path.basename(font.file) === 'Fraunces.woff2' && fs.readFileSync(font.file, 'latin1').startsWith('wOF2') && font.url.startsWith('https://fonts.gstatic.com/'),
    `the Google font's regular weight is kept (${font?.file ?? 'none'})`);
  check(asked.every((a) => a.opts?.maxBytes <= 5 << 20 && a.opts?.allowLocal === false) && asked.some((a) => a.url === fontLink && a.opts.userAgent.includes('Chrome')),
    "every download is a small one, never local, and Google's CSS is asked for as a browser");
  check(!asked.some((a) => a.url.includes('preview.png')), 'no more pictures are fetched once three are kept');
  check(JSON.stringify(kept.colors) === JSON.stringify([{ hex: '#e2552d', from: 'theme-color', weight: 1 }, { hex: '#1b1b1f', from: 'header', weight: 0.2 }]), 'colours come along merged');
  check((await keepBrand({ viewport: view, candidates: [], colors: [], used: [], loaded: [], google: [] }, { dir, fetch: fakeFetch })) === null, 'a page that says nothing about its brand has none');
  const icons = await keepBrand({ viewport: view, candidates: [raw.candidates[2], raw.candidates[3], cands[8]], colors: [], used: [], loaded: [], google: [] }, { dir: fs.mkdtempSync(path.join(dir, 'x-')), fetch: fakeFetch });
  check(icons?.logos?.length === 1 && icons.logos[0].kind === 'touch-icon', `the same picture under another address is kept once (${icons?.logos?.map((l) => l.url).join(', ')})`);
  fs.rmSync(dir, { recursive: true, force: true });
}

sink.close();
sink6.close();
process.stderr.write(`[web-test] ${failures === 0 ? 'all passed' : `${failures} failed`}\n`);
process.exit(failures === 0 ? 0 : 1);
