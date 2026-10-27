// The extension's popup: pair with MetaRush on this computer, see that it is
// connected, send the video on this tab, unpair.
(() => {
  'use strict';
  const $ = (id) => document.getElementById(id);
  const show = (id) => {
    for (const s of ['loading', 'unpaired', 'paired']) $(s).hidden = s !== id;
  };
  const ask = (msg) => new Promise((resolve) => chrome.runtime.sendMessage(msg, resolve));

  async function load() {
    const st = await ask({ type: 'status' });
    if (!st || st.error) {
      show('unpaired');
      return;
    }
    if (!st.paired) {
      if (st.base) $('base').value = st.base;
      show('unpaired');
      $('code').focus();
      return;
    }
    const whose = st.who ? `, for ${st.who}` : '';
    $('paired-line').textContent = st.machine ? `Paired with MetaRush on ${st.machine}${whose}.` : `Paired with MetaRush${whose}.`;
    if (st.online === false && st.problem) {
      $('offline').textContent = [st.problem.message, st.problem.hint].filter(Boolean).join(' ');
      $('offline').hidden = false;
    } else {
      $('offline').hidden = true;
    }
    show('paired');
  }

  $('pair-form').addEventListener('submit', async (e) => {
    e.preventDefault();
    const btn = $('pair-btn');
    btn.disabled = true;
    btn.textContent = 'Pairing…';
    $('pair-error').hidden = true;
    const out = await ask({ type: 'pair', code: $('code').value, base: $('base').value });
    btn.disabled = false;
    btn.textContent = 'Pair';
    if (!out || out.error) {
      const err = (out && out.error) || { message: 'Pairing didn’t work.' };
      $('pair-error').textContent = [err.message, err.hint].filter(Boolean).join(' ');
      $('pair-error').hidden = false;
      return;
    }
    await load();
  });

  $('unpair-btn').addEventListener('click', async () => {
    await ask({ type: 'unpair' });
    await load();
  });

  // Send this page: the tab the person opened the extension on.
  $('page-btn').addEventListener('click', async () => {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!tab || tab.id == null) return;
    const btn = $('page-btn');
    const result = $('page-result');
    btn.disabled = true;
    btn.textContent = 'Sending…';
    result.hidden = true;
    const out = await ask({ type: 'send-page', tabId: tab.id });
    btn.disabled = false;
    btn.textContent = 'Send this page';
    if (!out || out.error) {
      const err = (out && out.error) || { message: 'Sending the page didn’t work.' };
      result.className = 'error';
      result.textContent = [err.message, err.hint].filter(Boolean).join(' ');
    } else {
      result.className = 'ok';
      result.textContent = out.message;
    }
    result.hidden = false;
  });

  $('send-btn').addEventListener('click', async () => {
    const [tab] = await chrome.tabs.query({ active: true, currentWindow: true });
    if (!tab || tab.id == null) return;
    chrome.tabs.sendMessage(tab.id, { type: 'open-panel' }, (r) => {
      if (chrome.runtime.lastError || !r || !r.ok) {
        $('send-note').textContent = 'Open a YouTube, TikTok or X video first, then try again.';
        return;
      }
      window.close();
    });
  });

  void load();
})();
