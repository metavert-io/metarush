// The way out for website snapshots (D105). A snapshot's browser has network
// access, but only through this proxy: Chrome sends it every request (the
// render jail's flags, with this proxy instead of the refusing one), and the
// proxy resolves each name itself, refuses addresses on this computer or a
// local network, and connects to the address it checked (so a name that
// changes its answer can't slip through). Plain-http WebSocket upgrades are
// refused; a byte budget stops a page that downloads without end.
import dns from 'node:dns/promises';
import http from 'node:http';
import https from 'node:https';
import net from 'node:net';

const v4 = (ip) => ip.split('.').map(Number);

/** Loopback, private, link-local, carrier-grade NAT, benchmark, multicast, broadcast, "this network". */
const local4 = ([a, b]) =>
  a === 0 || a === 10 || a === 127 || (a === 100 && b >= 64 && b <= 127) || (a === 169 && b === 254) || (a === 172 && b >= 16 && b <= 31) ||
  (a === 192 && b === 168) || (a === 198 && (b === 18 || b === 19)) || a >= 224;

/** An IPv6 address as its eight 16-bit groups (an IPv4 tail included), or null. */
export const ipv6Groups = (s) => {
  let x = String(s).toLowerCase();
  const zone = x.indexOf('%');
  if (zone >= 0) x = x.slice(0, zone);
  let tail = [];
  const dotted = /(\d+\.\d+\.\d+\.\d+)$/.exec(x);
  if (dotted) {
    if (!net.isIPv4(dotted[1])) return null;
    const [a, b, c, d] = v4(dotted[1]);
    tail = [(a << 8) | b, (c << 8) | d];
    x = x.slice(0, -dotted[1].length);
    if (!x.endsWith('::')) x = x.replace(/:$/, '');
  }
  const halves = x.split('::');
  if (halves.length > 2) return null;
  const groups = (h) => (h === '' ? [] : h.split(':').map((g) => (/^[0-9a-f]{1,4}$/.test(g) ? parseInt(g, 16) : NaN)));
  const head = groups(halves[0]);
  const rest = halves.length === 2 ? groups(halves[1]) : [];
  const fill = 8 - head.length - rest.length - tail.length;
  if (fill < 0 || (halves.length === 1 && fill !== 0)) return null;
  const g = [...head, ...new Array(halves.length === 2 ? fill : 0).fill(0), ...rest, ...tail];
  return g.length === 8 && g.every((n) => Number.isInteger(n)) ? g : null;
};

/**
 * Is an address on this computer or a local network? IPv4: loopback, private,
 * link-local, carrier-grade NAT, benchmark, multicast, broadcast. IPv6: the
 * same, plus any IPv6 form that carries such an IPv4 address (mapped
 * ::ffff:7f00:1, compatible ::127.0.0.1, NAT64 64:ff9b::/96, 6to4 2002::/16),
 * the rest of ::/8, unique-local, link- and site-local. Anything that isn't an
 * address: yes.
 */
export const isLocalIP = (ip) => {
  const s = String(ip).toLowerCase().replace(/^\[|\]$/g, '');
  if (net.isIPv4(s)) return local4(v4(s));
  if (!net.isIPv6(s)) return true;
  const g = ipv6Groups(s);
  if (!g) return true;
  const inner = (hi, lo) => [hi >> 8, hi & 255, lo >> 8, lo & 255];
  const zero = (from, to) => g.slice(from, to).every((n) => n === 0);
  if (zero(0, 5) && (g[5] === 0xffff || g[5] === 0)) return local4(inner(g[6], g[7])); // mapped, compatible, ::, ::1
  if (g[0] === 0x64 && g[1] === 0xff9b && zero(2, 6)) return local4(inner(g[6], g[7])); // NAT64
  if (g[0] === 0x2002) return local4(inner(g[1], g[2])); // 6to4
  // ::/8 (reserved, never on the internet), unique-local, link-local, site-local, multicast.
  return (g[0] & 0xff00) === 0 || (g[0] & 0xfe00) === 0xfc00 || (g[0] & 0xffc0) === 0xfe80 || (g[0] & 0xffc0) === 0xfec0 || (g[0] & 0xff00) === 0xff00;
};

const localName = (host) => {
  const h = host.toLowerCase();
  return h === 'localhost' || h.endsWith('.localhost') || h.endsWith('.local') || h.endsWith('.internal') || (!h.includes('.') && !net.isIP(h));
};

/**
 * Resolves a host and returns the address to connect to, or throws when the
 * host is (or resolves to) this computer or a local network.
 */
export const checkHost = async (host, allowLocal, { lookup = dns.lookup } = {}) => {
  const h = String(host).replace(/^\[|\]$/g, '');
  if (!allowLocal && localName(h)) throw Object.assign(new Error(`${h} is on this computer or a local network`), { local: true });
  const addrs = net.isIP(h) ? [{ address: h }] : await lookup(h, { all: true, verbatim: true });
  if (!addrs.length) throw new Error(`${h} has no address`);
  if (!allowLocal && addrs.some((a) => isLocalIP(a.address))) throw Object.assign(new Error(`${h} is on this computer or a local network`), { local: true });
  return addrs[0].address;
};

const splitHostPort = (s) => {
  const m = /^\[([^\]]+)\]:(\d+)$/.exec(s) ?? /^([^:]+):(\d+)$/.exec(s);
  return m ? [m[1], Number(m[2])] : [s, 443];
};

/**
 * Starts the proxy on loopback. Returns {port, refused (hosts), reached (hosts), close}.
 */
export const startFilteringProxy = ({ allowLocal = false, budgetBytes = 400 << 20 } = {}) =>
  new Promise((resolve) => {
    const refused = new Set();
    const reached = new Set();
    let used = 0;
    const sockets = new Set();
    const count = (n) => {
      used += n;
      return used <= budgetBytes;
    };
    const server = http.createServer(async (req, res) => {
      let u;
      try {
        u = new URL(req.url);
      } catch {
        res.writeHead(400).end();
        return;
      }
      if (u.protocol !== 'http:') {
        res.writeHead(400).end();
        return;
      }
      let ip;
      try {
        ip = await checkHost(u.hostname, allowLocal);
      } catch (e) {
        if (e.local) refused.add(u.hostname);
        res.writeHead(403, { 'Content-Type': 'text/plain', Connection: 'close' }).end('blocked by MetaRush');
        return;
      }
      reached.add(u.hostname);
      const headers = { ...req.headers };
      delete headers['proxy-connection'];
      delete headers['proxy-authorization'];
      const up = http.request({ host: ip, port: Number(u.port) || 80, method: req.method, path: u.pathname + u.search, headers, setHost: false }, (r) => {
        res.writeHead(r.statusCode ?? 502, r.headers);
        r.on('data', (d) => {
          if (!count(d.length)) {
            r.destroy();
            res.destroy();
          }
        });
        r.pipe(res);
      });
      up.on('error', () => res.destroy());
      req.pipe(up);
    });
    server.on('connect', async (req, socket, head) => {
      sockets.add(socket);
      socket.on('close', () => sockets.delete(socket));
      socket.on('error', () => socket.destroy());
      const [host, port] = splitHostPort(req.url ?? '');
      let ip;
      try {
        ip = await checkHost(host, allowLocal);
      } catch (e) {
        if (e.local) refused.add(host);
        socket.end('HTTP/1.1 403 Forbidden\r\nConnection: close\r\n\r\n');
        return;
      }
      reached.add(host);
      const up = net.connect(port, ip, () => {
        socket.write('HTTP/1.1 200 Connection Established\r\n\r\n');
        if (head?.length) up.write(head);
        up.on('data', (d) => {
          if (!count(d.length)) {
            up.destroy();
            socket.destroy();
          }
        });
        up.pipe(socket);
        socket.pipe(up);
      });
      sockets.add(up);
      up.on('close', () => sockets.delete(up));
      up.on('error', () => socket.destroy());
      socket.on('close', () => up.destroy());
    });
    server.on('upgrade', (_req, socket) => socket.destroy());
    server.on('clientError', (_e, socket) => socket.destroy());
    server.listen(0, '127.0.0.1', () =>
      resolve({
        port: server.address().port,
        refused,
        reached,
        used: () => used,
        close: () =>
          new Promise((r) => {
            for (const s of sockets) s.destroy();
            server.close(() => r());
          }),
      }),
    );
  });

/**
 * Downloads a small file (a page's preview picture or icon, its logos, a
 * Google font) with the same rules: the address is checked first (an address
 * written as numbers too), the connection goes to the address checked, no
 * redirects are followed, and the size and the time are capped. `accept` and
 * `userAgent` say what is asked for (Google Fonts sends WOFF2 to a browser's
 * user agent).
 */
export const fetchSmall = async (url, { allowLocal = false, maxBytes = 5 << 20, timeoutMs = 20000, accept = 'image/*', userAgent = '' } = {}) => {
  let u;
  try {
    u = new URL(url);
  } catch {
    throw new Error('not an address');
  }
  if ((u.protocol !== 'http:' && u.protocol !== 'https:') || u.username || u.password) throw new Error('not a web address');
  const ip = await checkHost(u.hostname, allowLocal);
  const family = net.isIPv6(ip) ? 6 : 4;
  // Node asks for every address when it may try several (autoSelectFamily).
  const lookup = (_hostname, opts, cb) => (opts?.all ? cb(null, [{ address: ip, family }]) : cb(null, ip, family));
  const mod = u.protocol === 'https:' ? https : http;
  return new Promise((resolve, reject) => {
    const headers = { 'User-Agent': /^[\x20-\x7e]{1,400}$/.test(userAgent) ? userAgent : 'Mozilla/5.0 (MetaRush snapshot)', Accept: /^[\x20-\x7e]{1,200}$/.test(accept) ? accept : '*/*' };
    const req = mod.get(u, { lookup, agent: false, timeout: timeoutMs, headers }, (res) => {
      if ((res.statusCode ?? 0) >= 300) {
        res.resume();
        reject(new Error(`HTTP ${res.statusCode}`));
        return;
      }
      const chunks = [];
      let n = 0;
      res.on('data', (d) => {
        n += d.length;
        if (n > maxBytes) {
          req.destroy(new Error('too large'));
          return;
        }
        chunks.push(d);
      });
      res.on('end', () => resolve({ body: Buffer.concat(chunks), type: String(res.headers['content-type'] ?? '') }));
      res.on('error', reject);
    });
    const total = setTimeout(() => req.destroy(new Error('timed out')), timeoutMs);
    req.on('close', () => clearTimeout(total));
    req.on('timeout', () => req.destroy(new Error('timed out')));
    req.on('error', reject);
  });
};
