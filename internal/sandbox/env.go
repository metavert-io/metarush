package sandbox

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
)

// NodePreload is loaded first into every Node.js process of a run with the
// proxy (NODE_OPTIONS=--require): before Node.js 24 neither fetch nor the
// http and https modules read proxy settings, and inside the sandbox a
// direct connection is refused, so this sends them to MetaRush's proxy —
// fetch by address (/__metarush/<scheme>/<host>/<path> on the proxy, plain
// HTTP on this computer's loopback), the http and https modules through
// CONNECT tunnels (their global agents). Code that brings its own agent or
// socket is refused by the sandbox, never let out. It holds no key.
const NodePreload = `'use strict';
// MetaRush: this run reaches the network only through MetaRush's proxy.
(function () {
  const http = require('node:http');
  const https = require('node:https');
  const net = require('node:net');
  const tls = require('node:tls');
  if (!process.env.METARUSH_PROXY) return;
  const proxy = new URL(process.env.METARUSH_PROXY);
  const token = decodeURIComponent(proxy.password || '');
  const auth = token ? 'Basic ' + Buffer.from(decodeURIComponent(proxy.username || 'metarush') + ':' + token).toString('base64') : '';
  const PH = proxy.hostname;
  const PP = Number(proxy.port);
  const isProxy = (host, port) => (host === PH || host === 'localhost') && Number(port) === PP;

  function tunnel(host, port, cb) {
    const s = net.connect(PP, PH);
    let buf = Buffer.alloc(0);
    let done = false;
    const fail = (e) => { if (!done) { done = true; s.destroy(); cb(e); } };
    s.once('error', fail);
    s.on('data', function onData(d) {
      buf = Buffer.concat([buf, d]);
      const i = buf.indexOf('\r\n\r\n');
      if (i < 0) { if (buf.length > 65536) fail(new Error('MetaRush proxy: no answer')); return; }
      s.removeListener('data', onData);
      const head = buf.subarray(0, i).toString('latin1');
      const rest = buf.subarray(i + 4);
      const status = Number(head.split(' ')[1]);
      if (status !== 200) {
        const said = rest.toString('utf8').trim() || head.split('\r\n')[0];
        return fail(new Error('MetaRush proxy: ' + said));
      }
      if (rest.length) s.unshift(rest);
      done = true;
      s.removeListener('error', fail);
      cb(null, s);
    });
    s.write('CONNECT ' + host + ':' + port + ' HTTP/1.1\r\nHost: ' + host + ':' + port + '\r\n' + (auth ? 'Proxy-Authorization: ' + auth + '\r\n' : '') + '\r\n');
  }

  class TunnelHTTPS extends https.Agent {
    createConnection(options, cb) {
      const host = options.host || options.hostname || 'localhost';
      const port = options.port || 443;
      if (isProxy(host, port)) return super.createConnection(options, cb);
      tunnel(host, port, (err, sock) => {
        if (err) return cb(err);
        const servername = options.servername || (net.isIP(host) ? undefined : host);
        cb(null, tls.connect(Object.assign({}, options, { socket: sock, servername })));
      });
    }
  }
  class TunnelHTTP extends http.Agent {
    createConnection(options, cb) {
      const host = options.host || options.hostname || 'localhost';
      const port = options.port || 80;
      if (isProxy(host, port)) return super.createConnection(options, cb);
      tunnel(host, port, cb);
    }
  }
  try { https.globalAgent = new TunnelHTTPS({ keepAlive: true }); } catch (e) {}
  try { http.globalAgent = new TunnelHTTP({ keepAlive: true }); } catch (e) {}

  const orig = globalThis.fetch;
  if (typeof orig !== 'function') return;
  globalThis.fetch = function fetch(input, init) {
    let u;
    try {
      u = new URL(typeof input === 'string' || input instanceof URL ? String(input) : input.url);
    } catch (e) {
      return orig(input, init);
    }
    if ((u.protocol !== 'http:' && u.protocol !== 'https:') || isProxy(u.hostname, u.port || (u.protocol === 'https:' ? 443 : 80))) return orig(input, init);
    const target = 'http://' + PH + ':' + PP + '/__metarush/' + u.protocol.slice(0, -1) + '/' + encodeURIComponent(u.host) + u.pathname + u.search;
    const req = (typeof Request === 'function' && input instanceof Request) ? input : null;
    const opts = {};
    if (req) {
      opts.method = req.method;
      opts.headers = req.headers;
      opts.redirect = req.redirect;
      opts.signal = req.signal;
      if (req.body) { opts.body = req.body; opts.duplex = 'half'; }
    }
    Object.assign(opts, init || {});
    const headers = new Headers(opts.headers);
    if (token) headers.set('X-Metarush-Proxy', token);
    opts.headers = headers;
    return orig(target, opts);
  };
})();
`

// PythonCustomize is loaded by Python at its start (sitecustomize on
// PYTHONPATH) in a run with the proxy: Pythons built with LibreSSL (Apple's
// own python3) don't read SSL_CERT_FILE, so this makes Python's default
// certificate check trust the run's certificate authority too. It holds no
// key and changes nothing else.
const PythonCustomize = `# MetaRush: trust this run's proxy certificate (Pythons built with LibreSSL ignore SSL_CERT_FILE).
import os as _os, ssl as _ssl
_ca = _os.environ.get("METARUSH_CA")
if _ca:
    _orig = _ssl.create_default_context
    def create_default_context(purpose=_ssl.Purpose.SERVER_AUTH, *, cafile=None, capath=None, cadata=None):
        ctx = _orig(purpose, cafile=cafile, capath=capath, cadata=cadata)
        if purpose == _ssl.Purpose.SERVER_AUTH:
            try:
                ctx.load_verify_locations(_ca)
            except Exception:
                pass
        return ctx
    _ssl.create_default_context = create_default_context
    _ssl._create_default_https_context = create_default_context
`

// ProxyEnv is what a program's environment says about the network when it
// goes through MetaRush's proxy: the proxy's address under every name
// programs read (Python, curl, Go, Node.js ≥ 24 and the preload), nothing
// bypassing it, the run's certificate authority as the only one to trust,
// the Node.js preload and Python's sitecustomize folder (caFile, preload and
// pythonDir are in the room; "" leaves one out).
func ProxyEnv(proxyURL, caFile, preload, pythonDir string) []string {
	env := []string{
		"HTTPS_PROXY=" + proxyURL, "https_proxy=" + proxyURL, "HTTP_PROXY=" + proxyURL, "http_proxy=" + proxyURL,
		"ALL_PROXY=" + proxyURL, "all_proxy=" + proxyURL, "NO_PROXY=.metarush.invalid", "no_proxy=.metarush.invalid",
		"SSL_CERT_FILE=" + caFile, "REQUESTS_CA_BUNDLE=" + caFile, "CURL_CA_BUNDLE=" + caFile, "NODE_EXTRA_CA_CERTS=" + caFile,
		"GIT_SSL_CAINFO=" + caFile, "METARUSH_CA=" + caFile, "METARUSH_PROXY=" + proxyURL,
	}
	if preload != "" {
		env = append(env, "NODE_OPTIONS=--require "+preload)
	}
	if pythonDir != "" {
		env = append(env, "PYTHONPATH="+pythonDir)
	}
	return env
}

// WriteHelpers writes the run's certificate authority, the Node.js preload
// and Python's sitecustomize into dir (a folder of the room) and returns
// their paths.
func WriteHelpers(dir string, caPEM []byte) (caFile, preload, pythonDir string, err error) {
	pythonDir = filepath.Join(dir, "python")
	if err = os.MkdirAll(pythonDir, 0o700); err != nil {
		return
	}
	caFile, preload = filepath.Join(dir, "ca.pem"), filepath.Join(dir, "proxy.cjs")
	for p, b := range map[string][]byte{caFile: caPEM, preload: []byte(NodePreload), filepath.Join(pythonDir, "sitecustomize.py"): []byte(PythonCustomize)} {
		if err = os.WriteFile(p, b, 0o600); err != nil {
			return
		}
	}
	return
}

// BaseEnv is the rest of a program's environment, built from nothing: a
// PATH of the system's programs (and the given folders first), a home and a
// temporary folder in the room, plain settings. No variable of MetaRush's
// or of the person's own environment comes along.
func BaseEnv(pathFirst []string, home, tmp string) []string {
	path := append(append([]string{}, pathFirst...), "/usr/bin", "/bin", "/usr/sbin", "/sbin")
	who := "metarush"
	if u, err := user.Current(); err == nil && u.Username != "" {
		who = u.Username
	}
	return []string{"PATH=" + strings.Join(Dedupe0(path), string(os.PathListSeparator)), "HOME=" + home, "TMPDIR=" + tmp, "TMP=" + tmp, "TEMP=" + tmp,
		"USER=" + who, "LOGNAME=" + who, "SHELL=/bin/sh", "LANG=en_US.UTF-8", "LC_ALL=en_US.UTF-8", "TERM=dumb", "CI=1", "NO_COLOR=1", "FORCE_COLOR=0",
		"PYTHONDONTWRITEBYTECODE=1", "PYTHONUNBUFFERED=1", "PYTHONNOUSERSITE=1", "NO_UPDATE_NOTIFIER=1", "npm_config_update_notifier=false",
		"XDG_CACHE_HOME=" + filepath.Join(home, ".cache"), "XDG_CONFIG_HOME=" + filepath.Join(home, ".config"), "XDG_DATA_HOME=" + filepath.Join(home, ".local", "share")}
}

// Dedupe0 removes repeats and empty strings, keeping the order.
func Dedupe0(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if x != "" && !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}
