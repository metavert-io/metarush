'use strict';
// The guest page (MetaRush's guest listener). It signs a browser in — the
// one-time link's button, or the organization's account — lists what was
// shared with this guest, plays a version and keeps a commenter's comments.
// Everything someone typed goes into the page as text, never as markup.
(function () {
  var $ = function (id) { return document.getElementById(id); };
  var current = null; // the version being watched
  var refreshed = 0; // signed links asked for again since the video last played on
  // A one-time link's secret part: taken out of the address bar (and this
  // page's place in the history) as the page opens, kept only here until the
  // guest presses Continue.
  var linkToken = location.pathname === '/welcome' && location.hash.length > 1 ? location.hash.slice(1) : '';
  if (linkToken) history.replaceState(null, '', '/welcome');

  function show(id) {
    ['welcome', 'signin', 'shares', 'watch'].forEach(function (s) { $(s).hidden = s !== id; });
  }

  function say(text, bad) {
    var m = $('message');
    m.textContent = text || '';
    m.hidden = !text;
    m.className = 'message' + (bad ? ' bad' : '');
  }

  function words(err) {
    if (!err) return 'Something went wrong. Try again.';
    return [err.message, err.hint].filter(Boolean).join(' ');
  }

  // ask calls the guest API: the page's own header on every change.
  function ask(method, path, body) {
    var opts = { method: method, credentials: 'same-origin', headers: { 'Accept': 'application/json' } };
    if (method !== 'GET') {
      opts.headers['X-MetaRush-Guest'] = '1';
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body || {});
    }
    return fetch(path, opts).then(function (res) {
      return res.json().catch(function () { return { ok: false, error: { message: 'MetaRush gave an answer the page can’t read.' } }; })
        .then(function (env) {
          if (env && env.ok) return env.data;
          var e = (env && env.error) || { message: 'Something went wrong.' };
          e.status = res.status;
          throw e;
        });
    });
  }

  function clock(s) {
    if (typeof s !== 'number' || !isFinite(s) || s < 0) s = 0;
    var m = Math.floor(s / 60), sec = Math.floor(s % 60);
    return m + ':' + (sec < 10 ? '0' : '') + sec;
  }

  function when(iso) {
    var d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    return d.toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
  }

  function el(tag, cls, text) {
    var e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text !== undefined && text !== null) e.textContent = String(text);
    return e;
  }

  function signedOut(err) {
    $('who-bar').hidden = true;
    if (err) say(words(err), true);
    start(false);
  }

  function fail(err) {
    if (err && err.status === 401) return signedOut(err);
    say(words(err), true);
  }

  /* ---------- Signing in ---------- */

  function start(withState) {
    var token = linkToken;
    if (token) {
      show('welcome');
      $('use-link').onclick = function () {
        $('use-link').disabled = true;
        linkToken = '';
        ask('POST', '/guest/api/link', { token: token }).then(function () {
          history.replaceState(null, '', '/');
          say('');
          load();
        }).catch(function (err) {
          history.replaceState(null, '', '/');
          $('use-link').disabled = false;
          say(words(err), true);
          signinPage();
        });
      };
      return;
    }
    if (withState === false) return signinPage();
    load();
  }

  function signinPage(sso) {
    show('signin');
    var b = $('sso');
    var go = function (title) {
      if (!title) { b.hidden = true; return; }
      b.hidden = false;
      b.textContent = 'Sign in with ' + title;
      $('signin-words').textContent = 'Sign in with your ' + title + ' account if what was shared went to its email, or open the sign-in link you were sent.';
      b.onclick = function () {
        b.disabled = true;
        ask('POST', '/guest/api/sso/start', {}).then(function (out) {
          location.assign(out.url);
        }).catch(function (err) { b.disabled = false; say(words(err), true); });
      };
    };
    if (sso !== undefined) return go(sso);
    ask('GET', '/guest/api/state').then(function (st) { go(st.sso); }).catch(function () { go(''); });
  }

  function load() {
    ask('GET', '/guest/api/state').then(function (st) {
      if (!st.signedIn) return signinPage(st.sso || '');
      $('who-bar').hidden = false;
      $('me').textContent = 'Signed in as ' + (st.guest.name ? st.guest.name + ' (' + st.guest.email + ')' : st.guest.email);
      list();
    }).catch(fail);
  }

  $('signout').onclick = function () {
    ask('POST', '/guest/api/signout', {}).then(function () {
      $('video').removeAttribute('src');
      say('You signed out.');
      signedOut();
    }).catch(fail);
  };

  /* ---------- What was shared ---------- */

  function list() {
    ask('GET', '/guest/api/shared').then(function (out) {
      show('shares');
      $('shares-summary').textContent = out.summary;
      var box = $('share-list');
      box.textContent = '';
      out.shares.forEach(function (sh) {
        var card = el('section', 'share');
        card.setAttribute('data-guest-share', '');
        card.appendChild(el('h3', '', '“' + sh.project + '”'));
        var meta = 'You can ' + (sh.canComment ? 'watch and comment' : 'watch') + ' until ' + (when(sh.ends) || sh.endsWords.replace(/^until /, '')) + (sh.from ? ' · shared by ' + sh.from : '');
        card.appendChild(el('p', 'share-meta', meta));
        if (sh.message) card.appendChild(el('p', 'note', sh.message));
        if (sh.kept) card.appendChild(el('p', 'hint', 'The owner’s rules keep this project’s videos from guests right now.'));
        var ul = el('ul', 'versions');
        if (!sh.versions.length) ul.appendChild(el('li', 'empty', 'Nothing to watch yet: a version shows here once it has a review copy.'));
        sh.versions.forEach(function (v) {
          var li = el('li');
          li.setAttribute('data-guest-version', String(v.number));
          li.appendChild(el('span', 'v-name', 'Version ' + v.number + (v.cut ? ' · ' + v.cut : '')));
          li.appendChild(el('span', 'v-meta', (v.ready ? clock(v.duration) + ' · ' : '') + when(v.made)));
          var b = el('button', 'button small', v.ready ? 'Watch' : 'No review copy yet');
          b.type = 'button';
          b.disabled = !v.ready;
          b.setAttribute('data-guest-open', v.id);
          b.onclick = function () { watch(v.id, '“' + sh.project + '”'); };
          li.appendChild(b);
          ul.appendChild(li);
        });
        card.appendChild(ul);
        box.appendChild(card);
      });
    }).catch(fail);
  }

  /* ---------- Watching ---------- */

  $('back').onclick = function () {
    $('video').pause();
    $('video').removeAttribute('src');
    current = null;
    list();
  };

  function watch(id, project) {
    refreshed = 0;
    ask('GET', '/guest/api/versions/' + encodeURIComponent(id)).then(function (out) {
      say('');
      current = out;
      show('watch');
      $('watch-title').textContent = 'Version ' + out.version.number + ' of ' + (project || '“' + out.project + '”');
      var v = $('video');
      $('video-problem').hidden = true;
      if (out.poster) v.poster = out.poster; else v.removeAttribute('poster');
      v.src = out.url;
      comments();
    }).catch(fail);
  }

  // A signed link ends after two minutes: ask again (the grant is checked
  // each time) and go on where it was. Three tries in a row without the video
  // playing on in between, and it stops.
  $('video').addEventListener('playing', function () { refreshed = 0; });
  $('video').addEventListener('error', function () {
    if (!current) return;
    if (refreshed >= 3) {
      $('video-problem').textContent = 'The video stopped loading. Go back and open it again.';
      $('video-problem').hidden = false;
      return;
    }
    refreshed++;
    var v = $('video'), at = v.currentTime || 0, playing = !v.paused;
    ask('GET', '/guest/api/versions/' + encodeURIComponent(current.version.id)).then(function (out) {
      current = out;
      v.src = out.url;
      v.addEventListener('loadedmetadata', function once() {
        v.removeEventListener('loadedmetadata', once);
        try { v.currentTime = at; } catch (e) { /* not seekable yet */ }
        if (playing) v.play().catch(function () {});
      });
    }).catch(fail);
  });

  $('video').addEventListener('timeupdate', function () {
    $('pin-at').textContent = clock($('video').currentTime);
  });

  /* ---------- Comments ---------- */

  function comments() {
    if (!current) return;
    ask('GET', '/guest/api/versions/' + encodeURIComponent(current.version.id) + '/comments').then(function (out) {
      $('form').hidden = !out.canComment;
      $('talk-hint').textContent = out.canComment ? 'Pin a comment to a time, or leave it for the whole video.' : 'You can watch this version; comments are for commenters.';
      var ul = $('list');
      ul.textContent = '';
      if (out.canComment && !out.comments.length) ul.appendChild(el('li', 'empty', 'No comments from you yet.'));
      out.comments.forEach(function (c) {
        var li = el('li');
        li.setAttribute('data-guest-comment', '');
        var meta = el('div', 'c-meta');
        meta.appendChild(el('span', '', when(c.made)));
        if (typeof c.at === 'number') {
          var at = el('button', 'at', clock(c.at));
          at.type = 'button';
          at.onclick = function () { $('video').currentTime = c.at; $('video').play().catch(function () {}); };
          meta.appendChild(at);
        }
        li.appendChild(meta);
        li.appendChild(el('p', 'c-text', c.body));
        ul.appendChild(li);
      });
    }).catch(fail);
  }

  $('form').addEventListener('submit', function (e) {
    e.preventDefault();
    if (!current) return;
    var text = $('text').value.trim();
    var st = $('status');
    if (!text) { st.textContent = 'Write the comment first.'; st.className = 'status bad'; return; }
    var body = { body: text };
    if ($('pin').checked) body.at = Math.round(($('video').currentTime || 0) * 1000) / 1000;
    $('send').disabled = true;
    ask('POST', '/guest/api/versions/' + encodeURIComponent(current.version.id) + '/comments', body).then(function (out) {
      $('send').disabled = false;
      $('text').value = '';
      st.textContent = out.summary;
      st.className = 'status ok';
      comments();
    }).catch(function (err) {
      $('send').disabled = false;
      if (err && err.status === 401) return signedOut(err);
      st.textContent = words(err);
      st.className = 'status bad';
    });
  });

  start();
})();
