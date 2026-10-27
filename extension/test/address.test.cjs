// Unit tests for the MetaRush address the extension pairs with: node --test extension/test/
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const A = require('../lib/address.js');

test('this computer', () => {
  assert.deepEqual(A.check('http://127.0.0.1:4845'), { ok: true, base: 'http://127.0.0.1:4845', remote: false });
  assert.deepEqual(A.check(' http://localhost:4841/ '), { ok: true, base: 'http://localhost:4841', remote: false });
  assert.equal(A.check('http://127.0.0.1').ok, false, 'a port is needed');
  assert.equal(A.check('http://127.0.0.1:99999').ok, false);
});

test('the computer that runs MetaRush, over Tailscale', () => {
  assert.deepEqual(A.check('https://studio-mac.tail0000.ts.net'), { ok: true, base: 'https://studio-mac.tail0000.ts.net', remote: true });
  assert.deepEqual(A.check('https://Studio-Mac.tail0000.ts.net/'), { ok: true, base: 'https://studio-mac.tail0000.ts.net', remote: true });
  assert.deepEqual(A.check('https://studio-mac.tail0000.ts.net:443'), { ok: true, base: 'https://studio-mac.tail0000.ts.net', remote: true });
  assert.deepEqual(A.check('https://studio-mac.tail0000.ts.net:8443'), { ok: true, base: 'https://studio-mac.tail0000.ts.net:8443', remote: true });
});

test('anything else is refused, with a plain reason', () => {
  for (const bad of ['', 'http://studio-mac.tail0000.ts.net', 'https://evil.example.com', 'https://ts.net', 'https://studio-mac.tail0000.ts.net.evil.com',
    'https://studio-mac.tail0000.ts.net/path', 'https://user@studio-mac.tail0000.ts.net', 'http://192.168.1.5:4845', 'javascript:alert(1)', 'https://-bad.ts.net']) {
    const r = A.check(bad);
    assert.equal(r.ok, false, bad);
    assert.match(r.message, /MetaRush|https/, bad);
  }
});

test('the manifest lets the service worker reach exactly these addresses', () => {
  const m = JSON.parse(fs.readFileSync(path.join(__dirname, '..', 'manifest.json'), 'utf8'));
  for (const p of ['http://127.0.0.1/*', 'http://localhost/*', 'https://*.ts.net/*']) assert.ok(m.host_permissions.includes(p), p);
  const embed = fs.readFileSync(path.join(__dirname, '..', 'embed.go'), 'utf8');
  assert.match(embed, /lib\/address\.js/, 'the address checker is part of the built-in extension');
  const bg = fs.readFileSync(path.join(__dirname, '..', 'background.js'), 'utf8');
  assert.match(bg, /importScripts\([^)]*'lib\/address\.js'/);
});
