(function () {
  'use strict';
  // A review page MetaRush keeps in the workspace's own bucket (D313–D316).
  // Everything it reaches is a link signed for this page alone: the video,
  // and the slots reviewers' comments go into. It holds no key and no address
  // of MetaRush; reviewers' words are shown as text, never as markup.
  var D = JSON.parse(document.getElementById('data').textContent);
  var $ = function (id) { return document.getElementById(id); };
  var video = $('video'), list = $('list'), marks = $('marks'), statusLine = $('status'), sendButton = $('send');
  var sender = D.from || 'the person who sent it';
  var L = D.limits;

  var local = {
    get: function (k) { try { return window.localStorage.getItem('mr:' + D.page + ':' + k); } catch (e) { return null; } },
    set: function (k, v) { try { window.localStorage.setItem('mr:' + D.page + ':' + k, v); } catch (e) { /* private window */ } }
  };
  function clock(s) {
    s = Math.max(0, Math.floor(s || 0));
    var h = Math.floor(s / 3600), m = Math.floor((s % 3600) / 60), x = s % 60;
    return (h ? h + ':' + (m < 10 ? '0' : '') : '') + m + ':' + (x < 10 ? '0' : '') + x;
  }
  function when(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: 'numeric', minute: '2-digit' });
  }
  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = text;
    return e;
  }
  // Plain text only: control characters and direction marks go.
  function clean(s, max) {
    s = String(s === undefined || s === null ? '' : s).replace(/[\u0000-\u0008\u000b\u000c\u000e-\u001f\u007f-\u009f​-‏‪-‮⁦-⁩﻿]/g, '');
    return s.length > max ? s.slice(0, max) : s;
  }
  function say(text, tone) {
    statusLine.textContent = text || '';
    statusLine.className = 'status' + (tone ? ' ' + tone : '');
  }

  // Who sent it, and until when it works.
  $('from').textContent = D.from ? 'Sent by ' + D.from + ' for review' : 'Sent for review';
  $('ends').textContent = 'This link works until ' + when(D.expires) + '. After that, the page and the video are gone.';
  if (D.draft) $('draft').hidden = false;
  if (D.message) {
    $('note').hidden = false;
    $('note-from').textContent = 'A note from ' + sender + (D.for ? ' for ' + D.for : '');
    $('note-text').textContent = D.message;
  }

  // The video.
  video.src = D.video.url;
  if (D.poster) video.poster = D.poster;
  $('length').textContent = clock(D.video.duration);
  video.addEventListener('error', function () {
    var p = $('video-problem');
    p.hidden = false;
    p.textContent = 'The video can’t be played here. The link may have ended: ask ' + sender + ' for a new one.';
  });
  if (D.download) {
    var dl = $('download');
    dl.hidden = false;
    dl.href = D.download.url;
    dl.setAttribute('download', D.download.name);
  }
  function seek(t) {
    if (typeof t !== 'number' || !isFinite(t)) return;
    try { video.currentTime = Math.max(0, t); } catch (e) { /* not loaded yet */ }
  }

  // Pinning: the moment you started writing.
  var pinAt = 0;
  function showPin() { $('pin-at').textContent = clock(pinAt); }
  video.addEventListener('timeupdate', function () {
    $('now').textContent = clock(video.currentTime);
    if (!$('text').value) { pinAt = video.currentTime; showPin(); }
    follow(video.currentTime);
  });
  $('text').addEventListener('focus', function () { if (!this.value) { pinAt = video.currentTime; showPin(); } });
  $('text').addEventListener('input', function () { if (this.value && !video.paused) video.pause(); });
  $('text').addEventListener('keydown', function (e) {
    if (e.key === 'Enter' && (e.metaKey || e.ctrlKey)) { e.preventDefault(); submit(); }
  });

  // Comments: each reviewer's browser writes one slot of its own.
  var me = local.get('writer');
  if (!me || !/^[0-9a-f]{32}$/.test(me)) {
    var b = new Uint8Array(16);
    window.crypto.getRandomValues(b);
    me = Array.prototype.map.call(b, function (x) { return (x < 16 ? '0' : '') + x.toString(16); }).join('');
    local.set('writer', me);
  }
  var seats = [];
  var mySeat = parseInt(local.get('seat') || '', 10);
  if (!(mySeat >= 0 && mySeat < D.seats.length)) mySeat = -1;
  // This browser keeps only its random name for itself and its slot's
  // number (the bucket's address may be shared with other sites' pages):
  // its comments and the reviewer's name are read back from its slot.
  var mine = [];
  $('name').value = D.for || '';

  function parse(text) {
    var o;
    try { o = JSON.parse(text); } catch (e) { return null; }
    if (!o || typeof o !== 'object' || typeof o.writer !== 'string' || !Array.isArray(o.comments)) return null;
    var out = { writer: o.writer.slice(0, 64), name: clean(o.name, L.name).trim(), comments: [] };
    o.comments.slice(0, L.perReviewer).forEach(function (c) {
      if (!c || typeof c.text !== 'string' || typeof c.n !== 'number') return;
      var at = typeof c.at === 'number' && isFinite(c.at) && c.at >= 0 ? c.at : null;
      out.comments.push({ n: c.n, at: at, text: clean(c.text, L.text), made: typeof c.made === 'string' ? c.made.slice(0, 40) : '' });
    });
    return out;
  }
  function read(i) {
    return fetch(D.seats[i].get, { cache: 'no-store', credentials: 'omit' })
      .then(function (r) { return r.ok ? r.text() : ''; }, function () { return ''; })
      .then(function (t) { return t ? parse(t) : null; });
  }
  function load() {
    return Promise.all(D.seats.map(function (s, i) { return read(i); })).then(function (got) {
      seats = got;
      seats.forEach(function (s, i) {
        if (s && s.writer === me) {
          mySeat = i;
          if (s.comments.length >= mine.length) mine = s.comments.slice();
          if (s.name) $('name').value = s.name;
        }
      });
      draw();
    });
  }
  function pick() {
    if (mySeat >= 0 && (!seats[mySeat] || seats[mySeat].writer === me)) return mySeat;
    var free = [];
    for (var i = 0; i < D.seats.length; i++) {
      if (seats[i] && seats[i].writer === me) return i;
      if (!seats[i]) free.push(i);
    }
    return free.length ? free[Math.floor(Math.random() * free.length)] : -1;
  }
  function write(name, comments, tries) {
    var k = pick();
    if (k < 0 || tries > D.seats.length) {
      return Promise.reject('This link has no room for more reviewers. Ask ' + sender + ' for a new link.');
    }
    return read(k).then(function (cur) {
      if (cur && cur.writer !== me) { // someone else's browser took it meanwhile
        seats[k] = cur;
        mySeat = -1;
        return write(name, comments, tries + 1);
      }
      var body = JSON.stringify({ v: 1, writer: me, name: name, comments: comments });
      var size = new TextEncoder().encode(body).length;
      if (size > D.seatSize) throw 'Your comments are longer than this link takes. Ask ' + sender + ' for a new link.';
      body += new Array(D.seatSize - size + 1).join(' ');
      var headers = {};
      Object.keys(D.seats[k].headers || {}).forEach(function (h) { headers[h] = D.seats[k].headers[h]; });
      return fetch(D.seats[k].put, { method: 'PUT', headers: headers, body: body, credentials: 'omit' }).then(function (r) {
        if (!r.ok) throw r.status === 403 ? 'This link has ended, so comments can’t be sent any more.' : 'The comment couldn’t be sent. Try again.';
        mySeat = k;
        local.set('seat', String(k));
        seats[k] = { writer: me, name: name, comments: comments };
      }, function () { throw 'The comment couldn’t be sent: check the connection and try again.'; });
    });
  }
  var busy = false;
  function submit() {
    if (busy) return;
    var name = clean($('name').value, L.name).trim();
    var text = clean($('text').value, L.text).trim();
    if (!name) { say('Say your name first, so they know who wrote it.', 'bad'); $('name').focus(); return; }
    if (!text) { say('Write a comment first.', 'bad'); $('text').focus(); return; }
    if (mine.length >= L.perReviewer) { say('You’ve sent as many comments as this link takes. Ask ' + sender + ' for a new link.', 'bad'); return; }
    busy = true;
    sendButton.disabled = true;
    say('Sending…');
    var c = { n: (mine.length ? mine[mine.length - 1].n : 0) + 1, at: $('pin').checked ? Math.round(pinAt * 1000) / 1000 : null, text: text, made: new Date().toISOString() };
    write(name, mine.concat([c]), 0).then(function () {
      mine.push(c);
      $('text').value = '';
      pinAt = video.currentTime;
      showPin();
      say('Sent. ' + (D.from || 'They') + ' will see it with the video.', 'ok');
      draw();
    }, function (why) {
      say(typeof why === 'string' ? why : 'The comment couldn’t be sent. Try again.', 'bad');
    }).then(function () {
      busy = false;
      setTimeout(function () { sendButton.disabled = false; }, 1500);
    });
  }
  $('form').addEventListener('submit', function (e) { e.preventDefault(); submit(); });
  $('refresh').addEventListener('click', function () { say('Looking for new comments…'); load().then(function () { say(''); }); });

  function everyone() {
    var out = [];
    seats.forEach(function (s) {
      if (!s || s.writer === me) return;
      s.comments.forEach(function (c) { out.push({ who: s.name || 'Someone', at: c.at, text: c.text, made: c.made, mine: false }); });
    });
    var myName = clean($('name').value, L.name).trim() || 'You';
    mine.forEach(function (c) { out.push({ who: myName, at: c.at, text: c.text, made: c.made, mine: true }); });
    out.sort(function (a, b) {
      if (a.at === null && b.at !== null) return 1;
      if (b.at === null && a.at !== null) return -1;
      if (a.at !== b.at) return a.at - b.at;
      return a.made < b.made ? -1 : a.made > b.made ? 1 : 0;
    });
    return out;
  }
  function draw() {
    var all = everyone();
    list.textContent = '';
    marks.textContent = '';
    $('count').textContent = all.length ? '(' + all.length + ')' : '';
    if (!all.length) {
      list.appendChild(el('li', 'empty', 'No comments yet. Pause where something should change and write what you’d like.'));
      return;
    }
    var length = D.video.duration || video.duration || 0;
    all.forEach(function (c) {
      var li = el('li');
      var meta = el('div', 'meta');
      meta.appendChild(el('span', 'who', c.who + (c.mine ? ' (you)' : '')));
      if (c.at !== null) {
        var at = el('button', 'at', clock(c.at));
        at.type = 'button';
        at.setAttribute('aria-label', 'Play from ' + clock(c.at));
        at.addEventListener('click', function () { seek(c.at); });
        meta.appendChild(at);
        if (length > 0) {
          var m = el('button', 'mark' + (c.mine ? ' mine' : ''));
          m.type = 'button';
          m.style.left = Math.min(100, Math.max(0, (c.at / length) * 100)) + '%';
          m.setAttribute('aria-label', 'Comment by ' + c.who + ' at ' + clock(c.at));
          m.title = c.who + ' at ' + clock(c.at);
          m.addEventListener('click', function () { seek(c.at); });
          marks.appendChild(m);
        }
      }
      li.appendChild(meta);
      li.appendChild(el('p', 'text', c.text));
      list.appendChild(li);
    });
  }

  // What is said, line by line.
  var lines = [];
  if (D.words && D.words.length) {
    $('script-part').hidden = false;
    D.words.forEach(function (turn) {
      var t = el('div', 'turn');
      if (turn.speaker) t.appendChild(el('p', 'speaker', turn.speaker));
      var p = el('p');
      turn.lines.forEach(function (l) {
        var b = el('button', 'line', l.text);
        b.type = 'button';
        b.title = clock(l.from);
        b.addEventListener('click', function () { seek(l.from); pinAt = l.from; showPin(); });
        p.appendChild(b);
        p.appendChild(document.createTextNode(' '));
        lines.push({ from: l.from, to: l.to, el: b });
      });
      t.appendChild(p);
      $('script').appendChild(t);
    });
  }
  var lit = null;
  function follow(t) {
    if (!lines.length) return;
    var now = null;
    for (var i = 0; i < lines.length; i++) {
      var next = i + 1 < lines.length ? lines[i + 1].from : Infinity;
      if (t >= lines[i].from && t < Math.max(lines[i].to, Math.min(next, lines[i].to + 1))) { now = lines[i]; break; }
    }
    if (now === lit) return;
    if (lit) lit.el.classList.remove('now');
    lit = now;
    if (lit) lit.el.classList.add('now');
  }

  showPin();
  draw();
  load();
})();
