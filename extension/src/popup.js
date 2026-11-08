'use strict';
/* The window under HELINK's button in the toolbar. */
const B = globalThis.browser || globalThis.chrome;
const $ = (id) => document.getElementById(id);
let L = 'en';
let tabId = -1;
let toastTimer = 0;

const t = (k, v) => helinkT(L, k, v);

function words() {
  document.documentElement.lang = L;
  document.documentElement.dir = L === 'ar' ? 'rtl' : 'ltr';
  document.querySelectorAll('[data-t]').forEach((el) => { el.textContent = t(el.dataset.t); });
}

function send(msg) {
  return B.runtime.sendMessage(msg).catch((e) => ({ ok: false, error: String(e) }));
}

function toast(text, bad) {
  const el = $('toast');
  el.textContent = text;
  el.classList.toggle('bad', !!bad);
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { el.textContent = ''; }, 4000);
}

function sizeText(n) {
  if (!n) return '';
  const u = L === 'ar' ? ['بايت', 'كيلوبايت', 'ميجابايت', 'جيجابايت'] : ['B', 'KB', 'MB', 'GB'];
  let i = 0;
  while (n >= 1024 && i < 3) { n /= 1024; i++; }
  return (i ? n.toFixed(n < 10 ? 1 : 0) : n) + ' ' + u[i];
}

function showStatus(st) {
  const box = $('status');
  box.className = 'status';
  let text = '', help = '';
  if (st.installed === false) {
    box.classList.add('bad');
    if (st.error === 'forbidden') {
      text = t('statusForbidden');
      help = t('statusForbiddenHelp');
    } else {
      text = t('statusMissing');
      help = t('statusMissingHelp');
    }
  } else if (st.running) {
    box.classList.add('ok');
    text = t('statusRunning');
  } else if (st.error) {
    box.classList.add('bad');
    text = t('statusError');
  } else {
    text = t('statusClosed');
  }
  $('statusText').textContent = text;
  $('statusHelp').textContent = help;
  const usable = st.installed !== false;
  $('catch').disabled = !usable;
  $('catch').checked = !!(st.settings && st.settings.catch) && usable;
  $('open').disabled = !usable;
}

function showMedia(list) {
  const ul = $('media');
  ul.textContent = '';
  $('none').hidden = list.length > 0;
  list.forEach((m, i) => {
    const li = document.createElement('li');
    const kind = document.createElement('span');
    const stream = m.kind === 'hls' || m.kind === 'dash';
    kind.className = 'kind' + (stream ? ' stream' : '');
    kind.textContent = stream ? t('kindStream') : m.kind === 'audio' ? t('kindAudio') : t('kindVideo');
    const what = document.createElement('div');
    what.className = 'what';
    const b = document.createElement('b');
    let name = '';
    try {
      const u = new URL(m.url);
      name = decodeURIComponent(u.pathname.split('/').pop() || u.hostname);
      what.title = m.url;
      b.textContent = sizeText(m.size) || (m.mime || '').replace(/^.*\//, '').toUpperCase() || u.hostname;
      const span = document.createElement('span');
      span.textContent = u.hostname.replace(/^www\./, '') + ' · ' + name;
      what.append(b, span);
    } catch (e) {
      b.textContent = m.url;
      what.append(b);
    }
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.textContent = t('download');
    btn.addEventListener('click', async () => {
      btn.disabled = true;
      const r = await send({ type: 'media', tabId, index: i });
      btn.disabled = false;
      if (r.ok) {
        toast(t('sent'));
        setTimeout(() => window.close(), 900);
      } else toast(t('failed'), true);
    });
    li.append(kind, what, btn);
    ul.append(li);
  });
}

async function load(force) {
  const r = await send({ type: 'state', force });
  if (!r || !r.ok) return;
  L = r.lang;
  words();
  $('ver').textContent = 'v' + r.version;
  tabId = r.tab ? r.tab.id : -1;
  showStatus(r.state);
  const web = r.tab && /^https?:/i.test(r.tab.url);
  $('pageVideo').disabled = !web || r.state.installed === false;
  showMedia(r.media || []);
}

$('catch').addEventListener('change', async () => {
  const on = $('catch').checked;
  const r = await send({ type: 'setSettings', settings: { catch: on } });
  if (!r.ok) {
    $('catch').checked = !on;
    toast(t('failed'), true);
  }
});

$('pageVideo').addEventListener('click', async () => {
  $('pageVideo').disabled = true;
  const r = await send({ type: 'pageVideo', tabId });
  $('pageVideo').disabled = false;
  if (r.ok) {
    toast(t('sent'));
    setTimeout(() => window.close(), 900);
  } else toast(r.error === 'not_web' ? t('notWeb') : t('failed'), true);
});

$('open').addEventListener('click', async () => {
  const r = await send({ type: 'show' });
  if (r.ok) window.close();
  else toast(t('failed'), true);
});

$('settings').addEventListener('click', () => {
  B.runtime.openOptionsPage();
  window.close();
});

load(false).then(() => load(true));
