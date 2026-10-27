// The network jail for every render page (D70). Remotion's headless Chromium
// gets, on top of Remotion's own flags:
//   * a proxy that refuses everything (and records which host was tried),
//     with only the render's own loopback servers bypassing it: the media
//     server for its footage, Remotion's frame server and the bundle server;
//   * host-resolver rules that make every other name unresolvable (no DNS
//     leaks: prefetch, WebRTC, beacons);
//   * WebRTC limited to the (refusing) proxy.
// The bundle's page also carries a Content-Security-Policy (shared.mjs), and
// the Node side refuses to download anything outside the same allowlist on the
// page's behalf (Remotion fetches audio and video sources in Node). Component
// code never runs in Node.
import cp from 'node:child_process';
import { syncBuiltinESMExports } from 'node:module';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';
import os from 'node:os';

/** Content-Security-Policy for the render page (loopback only; ports are narrowed by the proxy jail). */
export const RENDER_CSP = [
  "default-src 'self'",
  "script-src 'self' 'unsafe-inline' http://127.0.0.1:* http://localhost:*",
  "style-src 'self' 'unsafe-inline'",
  "img-src 'self' data: blob: http://127.0.0.1:* http://localhost:*",
  "media-src 'self' data: blob: http://127.0.0.1:* http://localhost:*",
  "font-src 'self' data:",
  "connect-src 'self' http://127.0.0.1:* http://localhost:*",
  "worker-src 'self' blob:",
  "frame-src 'none'",
  "object-src 'none'",
  "base-uri 'none'",
  "form-action 'none'",
].join('; ');

// Remotion's frame server checks a port on every loopback and wildcard address
// and then binds the wildcard (@remotion/renderer get-port.js, port-config.js);
// a port given to it that is taken on any of them fails the render ("You
// specified port … but it is not available").
const remotionHosts = () => {
  const all = Object.values(os.networkInterfaces()).flat();
  const v6 = all.some((c) => c.family === 'IPv6' && !c.internal);
  return {
    bind: v6 ? '::' : '0.0.0.0',
    check: [all.some((c) => c.family === 'IPv6' && c.internal) ? '::1' : null, all.some((c) => c.family === 'IPv4' && c.internal) ? '127.0.0.1' : null, v6 ? '::' : null, '0.0.0.0'].filter(Boolean),
  };
};

const listen = (port, host) =>
  new Promise((resolve) => {
    const s = net.createServer((c) => c.destroy());
    s.unref();
    s.on('error', () => resolve(null));
    s.listen({ port, host, exclusive: true }, () => resolve(s));
  });
const closeServer = (s) => new Promise((resolve) => s.close(() => resolve()));

/**
 * A port for Remotion's own server, so the jail can allow exactly it: free on
 * every address Remotion checks, and held on the address it binds until
 * release() (call it right before the first Remotion call) so nothing else
 * takes it while the browser starts.
 */
export const reservePort = async () => {
  const { bind, check } = remotionHosts();
  for (let i = 0; i < 20; i++) {
    const hold = await listen(0, bind);
    if (!hold) break;
    const { port } = hold.address();
    await closeServer(hold);
    let free = true;
    for (const h of check) {
      const s = await listen(port, h);
      if (!s) {
        free = false;
        break;
      }
      await closeServer(s);
    }
    const held = free ? await listen(port, bind) : null;
    if (held) {
      let released = false;
      return {
        port,
        release: async () => {
          if (released) return;
          released = true;
          await closeServer(held);
        },
      };
    }
  }
  throw new Error("couldn't find a free local port for the renderer");
};

/** Starts the refusing proxy. Every request through it gets 403; the host is recorded. */
export const startRefusingProxy = () =>
  new Promise((resolve) => {
    const tried = new Set();
    const note = (h) => {
      if (h) tried.add(String(h).replace(/:(80|443)$/, '').slice(0, 200));
    };
    const server = http.createServer((req, res) => {
      try {
        note(new URL(req.url).host);
      } catch {
        note(req.headers.host);
      }
      res.writeHead(403, { 'Content-Type': 'text/plain', Connection: 'close' }).end('blocked by MetaRush: renders have no network access');
    });
    server.on('connect', (req, socket) => {
      note(req.url);
      socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
    });
    server.on('clientError', (_e, socket) => socket.destroy());
    server.listen(0, '127.0.0.1', () => resolve({ port: server.address().port, tried, close: () => new Promise((r) => server.close(() => r())) }));
  });

const isRenderChrome = (args) => Array.isArray(args) && args.includes('--remote-debugging-port=0') && args.some((a) => typeof a === 'string' && a.startsWith('--user-data-dir='));

let jail = null; // { proxyPort, bypass: ['127.0.0.1:1234', …] }
const launched = []; // Chrome processes we saw start
let realSpawn = null;

const rewrite = (args) => {
  const kept = args.filter((a) => !(typeof a === 'string' && (a.startsWith('--proxy-server') || a.startsWith('--no-proxy-server') || a.startsWith('--proxy-bypass-list'))));
  return [
    ...kept,
    `--proxy-server=http://127.0.0.1:${jail.proxyPort}`,
    `--proxy-bypass-list=<-loopback>;${jail.bypass.join(';')}`,
    '--host-resolver-rules=MAP * ~NOTFOUND, EXCLUDE localhost, EXCLUDE 127.0.0.1',
    '--force-webrtc-ip-handling-policy=disable_non_proxied_udp',
    '--webrtc-ip-handling-policy=disable_non_proxied_udp',
    '--dns-prefetch-disable',
  ];
};

/**
 * Jails every Chrome the renderer starts from now on. bypass lists the
 * loopback "host:port" pairs the page may reach directly.
 */
export const jailChrome = ({ proxyPort, bypass }) => {
  jail = { proxyPort, bypass: [...new Set(bypass)] };
  if (realSpawn) return;
  realSpawn = cp.spawn;
  cp.spawn = function spawn(cmd, args, opts) {
    if (jail && isRenderChrome(args)) {
      const child = realSpawn.call(this, cmd, rewrite(args), opts);
      launched.push(child);
      return child;
    }
    return realSpawn.call(this, cmd, args, opts);
  };
  // Remotion's ESM build reads child_process through its ESM namespace.
  syncBuiltinESMExports();
};

/** Updates the loopback pairs the page may reach (before a browser starts). */
export const allowLoopback = (...pairs) => {
  if (jail) jail.bypass = [...new Set([...jail.bypass, ...pairs])];
};

/** Did a jailed Chrome start? (Fail closed when a component would run without it.) */
export const chromeWasJailed = () => launched.length > 0;

/** Kills every Chrome this process started (their own process groups). */
export const killChromes = () => {
  for (const c of launched) {
    if (c.exitCode !== null || c.signalCode !== null) continue;
    try {
      process.kill(-c.pid, 'SIGKILL');
    } catch {
      try {
        c.kill('SIGKILL');
      } catch {
        // already gone
      }
    }
  }
};

const hostOf = (u) => {
  try {
    const x = new URL(u);
    return { host: hostPort(x.href), ok: x.protocol === 'http:' || x.protocol === 'https:' };
  } catch {
    return { host: '', ok: false };
  }
};

/**
 * Refuses Node-side downloads (Remotion fetches audio and video sources the
 * page names) outside the allowed "host:port" pairs.
 */
export const guardDownloads = (allowedHosts, onBlocked) => {
  const allowed = new Set(allowedHosts);
  const ok = (u) => {
    const { host, ok: web } = hostOf(u);
    return web && allowed.has(host);
  };
  const refuse = (u) => {
    const { host } = hostOf(u);
    onBlocked?.(host || String(u).slice(0, 80));
    return new Error(`MetaRush blocked a download from ${host || 'an unknown place'}: renders only reach their own footage`);
  };
  const wrapGet = (mod) => {
    const real = mod.get;
    mod.get = function get(url, ...rest) {
      const u = typeof url === 'string' || url instanceof URL ? String(url) : `${mod === https ? 'https' : 'http'}://${url?.host ?? url?.hostname ?? ''}${url?.port ? ':' + url.port : ''}`;
      if (!ok(u)) throw refuse(u);
      return real.call(this, url, ...rest);
    };
  };
  wrapGet(http);
  wrapGet(https);
  syncBuiltinESMExports();
  if (typeof globalThis.fetch === 'function') {
    const realFetch = globalThis.fetch;
    globalThis.fetch = (input, init) => {
      const u = typeof input === 'string' || input instanceof URL ? String(input) : input?.url;
      if (!ok(u)) return Promise.reject(refuse(u));
      return realFetch(input, init);
    };
  }
  return { allow: (...hosts) => hosts.forEach((h) => allowed.add(h)) };
};

/** "host:port" for an http URL. */
export const hostPort = (u) => {
  const x = new URL(u);
  return `${x.hostname}:${x.port || (x.protocol === 'https:' ? 443 : 80)}`;
};
