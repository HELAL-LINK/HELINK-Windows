'use strict';
/* HELINK's settings page in the browser. The settings live in the HELINK app (one place
   for every browser); the language of the extension is the extension's own. */
const B = globalThis.browser || globalThis.chrome;
const $ = (id) => document.getElementById(id);
let L = 'en';
let langPref = 'auto';
let lastTest = null;

const t = (k, v) => helinkT(L, k, v);

function words() {
  document.documentElement.lang = L;
  document.documentElement.dir = L === 'ar' ? 'rtl' : 'ltr';
  document.title = t('optTitle');
  document.querySelectorAll('[data-t]').forEach((el) => { el.textContent = t(el.dataset.t); });
}

function send(msg) {
  return B.runtime.sendMessage(msg).catch((e) => ({ ok: false, error: String(e) }));
}

function lines(text) {
  return text.split(/[\s,;]+/).map((s) => s.trim()).filter(Boolean);
}

function fill(s) {
  $('catch').checked = s.catch !== false;
  $('dialog').checked = s.dialog !== false;
  $('types').value = (s.catchTypes || HELINK_DEFAULT_TYPES).join(' ');
  $('minKB').value = String(s.catchMinKB || 0);
  $('skip').value = (s.skipSites || []).join('\n');
  $('buttons').checked = s.buttons !== false;
  $('noButtons').value = (s.buttonSkip || []).join('\n');
}

function showResults(r) {
  lastTest = r;
  const ul = $('results');
  ul.textContent = '';
  const add = (ok, text) => {
    const li = document.createElement('li');
    li.className = ok ? 'ok' : 'bad';
    li.textContent = text;
    ul.append(li);
  };
  if (!r) return;
  if (r.host) add(true, t('hostOk'));
  else add(false, r.hostError === 'forbidden' ? t('statusForbiddenHelp') : t('hostMissing'));
  if (r.host) {
    if (r.state.running) add(true, t('appRunning', { v: r.state.version || '' }));
    else add(true, t('appClosed'));
  }
}

async function test() {
  $('test').disabled = true;
  $('results').textContent = '';
  const li = document.createElement('li');
  li.textContent = t('testing');
  $('results').append(li);
  const r = await send({ type: 'test' });
  $('test').disabled = false;
  showResults(r && r.ok ? r : { host: false });
  return r;
}

async function load() {
  const r = await send({ type: 'state', force: false });
  const o = await B.storage.local.get('lang');
  langPref = o.lang || 'auto';
  L = helinkLang(langPref);
  $('lang').value = langPref;
  words();
  $('ver').textContent = 'v' + B.runtime.getManifest().version;
  if (r && r.ok) fill(r.state.settings || {});
  const tr = await test();
  if (tr && tr.ok && tr.state && tr.state.settings) fill(tr.state.settings);
}

$('test').addEventListener('click', test);
$('typesReset').addEventListener('click', () => { $('types').value = HELINK_DEFAULT_TYPES.join(' '); });
$('resetPos').addEventListener('click', async () => {
  await send({ type: 'resetPositions' });
  $('posMsg').textContent = t('resetDone');
});
$('lang').addEventListener('change', async () => {
  langPref = $('lang').value;
  await B.storage.local.set({ lang: langPref });
  L = helinkLang(langPref);
  words();
  showResults(lastTest);
  $('saveMsg').textContent = '';
  $('posMsg').textContent = '';
});

$('save').addEventListener('click', async () => {
  const settings = {
    catch: $('catch').checked,
    dialog: $('dialog').checked,
    catchTypes: lines($('types').value).map((e) => e.replace(/^\./, '').toLowerCase()),
    catchMinKB: Math.max(0, parseInt($('minKB').value, 10) || 0),
    skipSites: lines($('skip').value),
    buttons: $('buttons').checked,
    buttonSkip: lines($('noButtons').value)
  };
  $('save').disabled = true;
  const r = await send({ type: 'setSettings', settings });
  $('save').disabled = false;
  const msg = $('saveMsg');
  msg.classList.toggle('bad', false);
  msg.textContent = r.saved ? t('saved') : t('savedLocal');
  if (r.settings) fill(r.settings);
  setTimeout(() => { msg.textContent = ''; }, 5000);
});

load();
