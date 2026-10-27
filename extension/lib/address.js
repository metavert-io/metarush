// Where the extension finds MetaRush: on this computer (http://127.0.0.1 or
// localhost with a port), or the computer that runs MetaRush on the person's
// Tailscale network (https://<name>.<tailnet>.ts.net, remote access, D128).
// Nothing else: the extension's host permissions cover exactly these, and
// its token is only ever sent to the address it paired with. Shared with the
// Node tests (module.exports).
(function (root) {
  'use strict';

  const LOCAL = /^http:\/\/(127\.0\.0\.1|localhost):(\d{2,5})$/;
  // A Tailscale MagicDNS name: labels of letters, digits and hyphens, ending in .ts.net.
  const TAILNET = /^https:\/\/((?:[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?\.)+ts\.net)(?::(\d{2,5}))?$/;

  /**
   * Checks an address typed into the popup and returns it tidied
   * ({ ok: true, base, remote }) or why it can't be used ({ ok: false, message }).
   */
  function check(input) {
    const base = String(input || '')
      .trim()
      .replace(/\/+$/, '')
      .replace(/^(https?:\/\/)([^/]+)$/i, (_, s, h) => s.toLowerCase() + h.toLowerCase());
    const port = (p) => p === undefined || (Number(p) >= 1 && Number(p) <= 65535);
    let m = LOCAL.exec(base);
    if (m && port(m[2])) return { ok: true, base, remote: false };
    m = TAILNET.exec(base);
    if (m && port(m[2])) return { ok: true, base: m[2] === '443' ? 'https://' + m[1] : base, remote: true };
    if (/^http:\/\/[^/]+\.ts\.net(:\d+)?$/i.test(base)) {
      return { ok: false, message: 'Use the https address of the computer that runs MetaRush (https://… .ts.net).' };
    }
    return {
      ok: false,
      message: 'Use MetaRush on this computer (like http://127.0.0.1:4845) or the Tailscale address of the computer that runs it (like https://studio.tail1234.ts.net).',
    };
  }

  const api = { check };
  if (typeof module === 'object' && module.exports) module.exports = api;
  else root.MetaRushAddress = api;
})(typeof globalThis !== 'undefined' ? globalThis : this);
