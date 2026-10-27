// Unit tests for "Send this page" helpers: node --test extension/test/
'use strict';
const test = require('node:test');
const assert = require('node:assert/strict');
const P = require('../lib/page.js');

test('page text is tidied and capped', () => {
  assert.equal(P.cleanText('  Build it once.\n\n\n\nWidget   turns clips  \n  into video  ', 1000), 'Build it once.\n\nWidget turns clips\ninto video');
  assert.equal(P.cleanText('abcdef', 3), 'abc');
  assert.equal(P.cleanText(undefined, 10), '');
});

test('theme colours become #rrggbb', () => {
  assert.equal(P.hexColor('rgb(226, 85, 45)'), '#e2552d');
  assert.equal(P.hexColor('rgba(0, 0, 0, 0.5)'), '#000000');
  assert.equal(P.hexColor('#E2552D'), '#e2552d');
  assert.equal(P.hexColor('#fa0'), '#ffaa00');
  assert.equal(P.hexColor('tomato'), '');
  assert.equal(P.hexColor(''), '');
});

test('readPage is self-contained (it is serialized into the tab)', () => {
  const src = P.readPage.toString();
  assert.ok(!/cleanText|hexColor|MetaRushPage/.test(src), 'it must not use names from outside its own body');
});
