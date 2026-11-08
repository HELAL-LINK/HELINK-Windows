'use strict';
/* HELINK for the browsers. It hands the browser's downloads to the HELINK app (faster, in
   parts, resumable), sends it the videos the user asks for (the button on videos, the
   menus, the toolbar window), and notes the videos and sounds each page plays.
   One file for the Chromium browsers (a service worker) and for Firefox (a background
   page). The app is reached through "native messaging": the browser starts HELINK's
   small helper (helink-host.exe), which passes each message to HELINK. */

if (typeof importScripts === 'function') importScripts('strings.js');

const B = globalThis.browser || globalThis.chrome;
const FIREFOX = typeof globalThis.browser !== 'undefined' && !!globalThis.browser.runtime;
const HOST = 'com.helaltech.helink';
const ACTION = B.action || B.browserAction;
const UA = (globalThis.navigator && navigator.userAgent) || '';
const DEFAULT_SETTINGS = {
  catch: true, dialog: true, buttons: true, catchTypes: HELINK_DEFAULT_TYPES.slice(),
  catchMinKB: 0, skipSites: [], buttonSkip: []
};

// ---------------------------------------------------------------------------
// Talking to the app
// ---------------------------------------------------------------------------

/* native sends one message to HELINK and resolves with its answer, or with
   {ok: false, error}: "no_host" (HELINK not installed), "forbidden" (it does not know
   this extension), "timeout", "host_error". */
function native(msg, timeoutMs) {
  msg = Object.assign({ ext: B.runtime.getManifest().version }, msg); // HELINK knows an old extension by its absence
  return new Promise((resolve) => {
    let done = false;
    const finish = (r) => {
      if (!done) {
        done = true;
        clearTimeout(timer);
        resolve(r && typeof r === 'object' ? r : { ok: false, error: 'host_error' });
      }
    };
    const fail = (e) => {
      const m = String((e && e.message) || e || '').toLowerCase();
      let code = 'host_error';
      if (m.includes('not found') || m.includes('no such native application')) code = 'no_host';
      else if (m.includes('forbidden') || m.includes('does not have permission')) code = 'forbidden';
      finish({ ok: false, error: code, detail: String((e && e.message) || e || '') });
    };
    const timer = setTimeout(() => finish({ ok: false, error: 'timeout' }), timeoutMs || 30000);
    try {
      if (FIREFOX) {
        browser.runtime.sendNativeMessage(HOST, msg).then(finish, fail);
        return;
      }
      const p = chrome.runtime.sendNativeMessage(HOST, msg, (resp) => {
        if (chrome.runtime.lastError) fail(chrome.runtime.lastError);
        else finish(resp);
      });
      if (p && typeof p.then === 'function') p.catch(() => {}); // answered by the callback
    } catch (e) {
      fail(e);
    }
  });
}

async function stored(key, fallback) {
  try {
    const o = await B.storage.local.get(key);
    return o && o[key] !== undefined ? o[key] : fallback;
  } catch (e) {
    return fallback;
  }
}

async function store(key, value) {
  try {
    await B.storage.local.set({ [key]: value });
  } catch (e) { /* storage full or gone: nothing to do */ }
}

/* state is what is known of the app: {installed, running, error, version, settings,
   checked}. It is asked again after a minute (hello does not start the app). */
async function state(force) {
  const st = await stored('helink', { installed: null, running: false, error: '', settings: null, checked: 0 });
  if (!force && st.checked && Date.now() - st.checked < 60000) return st;
  const r = await native({ type: 'hello' }, 15000);
  if (r.ok) {
    st.installed = true;
    st.running = !!r.running;
    st.error = '';
    if (r.settings && typeof r.settings === 'object') {
      st.settings = Object.assign({}, DEFAULT_SETTINGS, r.settings);
      st.version = r.settings.version || st.version || '';
    }
  } else {
    // Not installed, or installed without knowing this extension: nothing goes there.
    // A slow or broken answer says nothing new.
    if (r.error === 'no_host' || r.error === 'forbidden') st.installed = false;
    st.running = false;
    st.error = r.error;
  }
  if (!st.settings) st.settings = Object.assign({}, DEFAULT_SETTINGS);
  st.checked = Date.now();
  await store('helink', st);
  if (r.ok) reloadIfUpdated();
  return st;
}

/* The extension added from HELINK's folder ("Load unpacked", in the Chromium browsers) runs
   the files the browser read when it started. When HELINK puts newer ones there (HELINK was
   updated), the extension starts again once with them; the open pages get the new button
   (onInstalled, below). */
let reloadChecked = 0;
async function reloadIfUpdated(waitMs) {
  if (FIREFOX || Date.now() - reloadChecked < 5 * 60000) return false;
  reloadChecked = Date.now();
  try {
    const self = B.management && B.management.getSelf ? await B.management.getSelf() : null;
    if (self ? self.installType !== 'development' : 'update_url' in B.runtime.getManifest()) return false;
    const onDisk = async () => {
      const r = await fetch(B.runtime.getURL('manifest.json'), { cache: 'no-store' });
      return String((await r.json()).version || '');
    };
    const v = await onDisk();
    if (!newerVersion(v, B.runtime.getManifest().version) || (await stored('reloadedFor', '')) === v) return false;
    // HELINK may still be writing the other files.
    await new Promise((done) => setTimeout(done, waitMs === undefined ? 10000 : waitMs));
    if ((await onDisk()) !== v) return false;
    await store('reloadedFor', v); // once: never again and again if the browser keeps the old files
    B.runtime.reload();
    return true;
  } catch (e) {
    return false;
  }
}

function newerVersion(a, b) {
  const x = String(a).split('.').map((n) => parseInt(n, 10) || 0);
  const y = String(b).split('.').map((n) => parseInt(n, 10) || 0);
  for (let i = 0; i < Math.max(x.length, y.length); i++) {
    if ((x[i] || 0) !== (y[i] || 0)) return (x[i] || 0) > (y[i] || 0);
  }
  return false;
}

async function lang() {
  return helinkLang(await stored('lang', 'auto'));
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

function isWeb(u) {
  return /^https?:\/\//i.test(u || '');
}

function hostOf(u) {
  try {
    return new URL(u).hostname.toLowerCase().replace(/^www\./, '');
  } catch (e) {
    return '';
  }
}

function listed(list, u) {
  const h = hostOf(u);
  return !!h && (list || []).some((s) => h === s || h.endsWith('.' + s));
}

function isYouTube(u) {
  const h = hostOf(u);
  return h === 'youtube.com' || h.endsWith('.youtube.com') || h === 'youtu.be' || h.endsWith('googlevideo.com');
}

function baseName(p) {
  return String(p || '').split(/[\\/]/).pop();
}

function nameFromUrl(u) {
  try {
    return decodeURIComponent(new URL(u).pathname.split('/').pop() || '');
  } catch (e) {
    return '';
  }
}

function extOf(name) {
  const m = /\.([A-Za-z0-9]{1,12})$/.exec(name || '');
  return m ? m[1].toLowerCase() : '';
}

/* cookiesFor collects the browser's cookies for the addresses (each sent by HELINK only
   where it belongs). */
async function cookiesFor(urls, opts) {
  const out = new Map();
  for (const u of new Set((urls || []).filter(isWeb))) {
    const q = { url: u };
    if (opts && opts.storeId) q.storeId = opts.storeId;
    else if (opts && opts.incognito && !FIREFOX) q.storeId = '1';
    if (FIREFOX) q.firstPartyDomain = null;
    let list = [];
    try {
      list = await B.cookies.getAll(q);
    } catch (e) {
      try {
        delete q.firstPartyDomain;
        list = await B.cookies.getAll(q);
      } catch (e2) {
        list = [];
      }
    }
    for (const c of list) {
      out.set(c.name + '|' + c.domain + '|' + c.path, {
        name: c.name, value: c.value, domain: c.domain, path: c.path, secure: !!c.secure,
        hostOnly: !!c.hostOnly, expirationDate: c.expirationDate || 0
      });
    }
  }
  return Array.from(out.values());
}

// ---------------------------------------------------------------------------
// Downloads the browser starts
// ---------------------------------------------------------------------------

/* Downloads made by a form (POST) cannot be asked again by HELINK: the browser keeps them. */
const posts = new Map();
try {
  B.webRequest.onBeforeRequest.addListener((d) => {
    if (d.method === 'POST') {
      posts.set(d.url, Date.now());
      if (posts.size > 200) posts.delete(posts.keys().next().value);
    }
  }, { urls: ['<all_urls>'], types: ['main_frame', 'sub_frame', 'other'] });
} catch (e) { /* not allowed here */ }

function postedRecently(u) {
  const t = posts.get(u);
  return !!t && Date.now() - t < 5 * 60 * 1000;
}

/* The kinds of file a server names (Content-Type), for a download whose name says nothing. */
const MIME_EXT = {
  'application/zip': 'zip', 'application/x-zip-compressed': 'zip', 'application/x-rar-compressed': 'rar', 'application/vnd.rar': 'rar',
  'application/x-7z-compressed': '7z', 'application/x-msdownload': 'exe', 'application/x-msdos-program': 'exe',
  'application/x-msi': 'msi', 'application/x-ms-installer': 'msi', 'application/vnd.android.package-archive': 'apk',
  'application/pdf': 'pdf', 'application/x-iso9660-image': 'iso', 'application/gzip': 'gz', 'application/x-tar': 'tar',
  'video/mp4': 'mp4', 'video/x-matroska': 'mkv', 'video/webm': 'webm', 'video/quicktime': 'mov', 'video/x-msvideo': 'avi',
  'audio/mpeg': 'mp3', 'audio/mp4': 'm4a', 'audio/x-m4a': 'm4a', 'audio/wav': 'wav', 'audio/flac': 'flac', 'audio/ogg': 'ogg',
  'application/epub+zip': 'epub'
};

/* Downloads HELINK refused once go to the browser when asked again right after. */
const refused = new Map();

/* wanted says whether HELINK is to be asked about a download the browser started: what it
   is ({url, name, size}), or null when the browser keeps it. */
async function wanted(item) {
  const url = item.finalUrl || item.url;
  if (!isWeb(item.url) || (item.byExtensionId && item.byExtensionId !== B.runtime.id)) return null;
  if (item.state && item.state !== 'in_progress') return null;
  if (postedRecently(item.url)) return null;
  const r = refused.get(item.url);
  if (r && Date.now() - r < 60 * 1000) return null;
  const st = await state(false);
  const s = st.settings || DEFAULT_SETTINGS;
  if (st.installed === false || !s.catch) return null;
  const name = baseName(item.filename) || nameFromUrl(url);
  const types = s.catchTypes || [];
  // A name without a known ending ("download.php", "get?id=7"): the kind the server said.
  if (!types.includes(extOf(name)) && !types.includes(MIME_EXT[String(item.mime || '').toLowerCase()] || '')) return null;
  const size = item.totalBytes > 0 ? item.totalBytes : item.fileSize > 0 ? item.fileSize : -1;
  if (s.catchMinKB > 0 && size >= 0 && size < s.catchMinKB * 1024) return null;
  if (listed(s.skipSites, item.referrer) || listed(s.skipSites, url)) return null;
  return { url, name, size };
}

/* takeDownload asks HELINK about a download the browser started; true when HELINK took it
   (the browser's own is then cancelled and removed from its list, and the page says that
   HELINK has it). The answer can take a while when HELINK has to start first (the helper
   starts it and waits up to 20 s, then HELINK checks the file, up to 20 s): the browser
   waits for it, so that the file is never downloaded twice. */
async function takeDownload(item, w) {
  try {
    w = w || await wanted(item);
    if (!w) return false;
    const { url, name, size } = w;
    const cookies = await cookiesFor([item.url, item.finalUrl], { incognito: item.incognito, storeId: item.cookieStoreId });
    const resp = await native({
      type: 'download', url: item.url, finalUrl: item.finalUrl && item.finalUrl !== item.url ? item.finalUrl : '',
      referrer: item.referrer || '', filename: name, mime: item.mime || '', size, cookies, userAgent: UA,
      pageUrl: item.referrer || ''
    }, 70000);
    if (!resp.ok) {
      if (resp.error || resp.reason) console.info('HELINK: the browser keeps', url, resp.error || resp.reason);
      refused.set(item.url, Date.now());
      return false;
    }
    try {
      await B.downloads.cancel(item.id);
    } catch (e) {
      return false; // too late: the browser has it
    }
    try {
      await B.downloads.erase({ id: item.id });
    } catch (e) { /* stays in the list as cancelled */ }
    sayTaken(resp.name || name, size, resp.dialog);
    return true;
  } catch (e) {
    console.warn('HELINK:', e);
    return false;
  }
}

/* The browser's own download bubble (Chromium) stays hidden while HELINK is asked: a file
   HELINK takes never shows there as if the browser were downloading it. One count for the
   downloads being decided; the bubble comes back when none is left. */
const CAN_HIDE_UI = !FIREFOX && !!(B.downloads && B.downloads.setUiOptions);
let deciding = 0;
let uiTimer = 0;
function setUi(enabled) {
  if (!CAN_HIDE_UI) return;
  try {
    const p = B.downloads.setUiOptions({ enabled });
    if (p && typeof p.catch === 'function') p.catch(() => {});
  } catch (e) { /* no "downloads.ui" permission */ }
}
function hideUi() {
  deciding++;
  clearTimeout(uiTimer);
  setUi(false);
}
function showUi(delayMs) {
  deciding = Math.max(0, deciding - 1);
  if (deciding) return;
  clearTimeout(uiTimer);
  uiTimer = setTimeout(() => { if (!deciding) setUi(true); }, delayMs || 0);
}
// A worker that starts fresh has nothing being decided (one stopped mid-way left it hidden).
setUi(true);

/* likelyWanted is a quick look, before anything is asked, at whether HELINK may take a
   download: then the bubble is hidden from the first moment. */
function likelyWanted(item) {
  if (!isWeb(item.url) || (item.byExtensionId && item.byExtensionId !== B.runtime.id)) return false;
  const s = (cached && cached.settings) || DEFAULT_SETTINGS;
  if (cached && (cached.installed === false || s.catch === false)) return false;
  const name = baseName(item.filename) || nameFromUrl(item.finalUrl || item.url);
  const types = s.catchTypes || HELINK_DEFAULT_TYPES;
  return types.includes(extOf(name)) || types.includes(MIME_EXT[String(item.mime || '').toLowerCase()] || '');
}
let cached = null;
stored('helink', null).then((st) => { cached = st; });
B.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.helink) cached = changes.helink.newValue || null;
});

/* sayTaken shows, in the page the user is on, that HELINK is downloading the file (a small
   card of HELINK's, for a few seconds), and marks HELINK's button in the toolbar. */
async function sayTaken(name, size, dialog) {
  try {
    const tabs = await B.tabs.query({ active: true, lastFocusedWindow: true });
    const tab = tabs && tabs[0];
    if (tab && tab.id >= 0 && isWeb(tab.url)) {
      const msg = { type: 'helinkTaken', name: String(name || ''), size: size > 0 ? size : 0, dialog: !!dialog, lang: await lang() };
      try {
        await B.tabs.sendMessage(tab.id, msg, { frameId: 0 });
      } catch (e) { /* a page without HELINK's script (the store, a PDF…) */ }
    }
  } catch (e) { /* no window */ }
  try {
    await ACTION.setBadgeText({ text: '↓' });
    setTimeout(() => { try { ACTION.setBadgeText({ text: '' }); } catch (e) { /* gone */ } }, 4000);
  } catch (e) { /* older browsers */ }
}

if (!FIREFOX && B.downloads.onDeterminingFilename) {
  // Chromium: the browser waits for the name, which gives time to ask HELINK.
  B.downloads.onDeterminingFilename.addListener((item, suggest) => {
    const hide = likelyWanted(item);
    if (hide) hideUi();
    takeDownload(item).then((taken) => {
      if (hide) showUi(taken ? 1500 : 0);
      if (!taken) {
        try {
          suggest();
        } catch (e) { /* the download is gone */ }
      }
    });
    return true;
  });
} else {
  // Firefox: the download has just started. It is held while HELINK is asked (a small
  // file would be finished before the answer), then cancelled, or let go on.
  B.downloads.onCreated.addListener(async (item) => {
    const w = await wanted(item);
    if (!w) return;
    let held = false;
    try {
      await B.downloads.pause(item.id);
      held = true;
    } catch (e) { /* finished already, or cannot pause */ }
    const taken = await takeDownload(item, w);
    if (!taken && held) {
      try {
        await B.downloads.resume(item.id);
      } catch (e) {
        console.warn('HELINK: could not let the browser go on with', w.url, e);
      }
    }
  });
}

// ---------------------------------------------------------------------------
// Videos and sounds the pages play
// ---------------------------------------------------------------------------

const media = new Map(); // tab id -> [{url, kind, mime, size}]
const session = B.storage && B.storage.session;

/* One change of the lists at a time (the events come close together). */
let chain = Promise.resolve();
function serial(fn) {
  const p = chain.then(fn, fn);
  chain = p.catch(() => {});
  return p;
}

function header(headers, name) {
  const h = (headers || []).find((x) => x.name.toLowerCase() === name);
  return h ? h.value || '' : '';
}

/* The address of a file without the parts that only pick a range of it. */
function mediaKey(u) {
  try {
    const x = new URL(u);
    ['bytestart', 'byteend', 'range', 'rn', 'rbuf', 'ump'].forEach((p) => x.searchParams.delete(p));
    return x.toString();
  } catch (e) {
    return u;
  }
}

async function tabMedia(tabId) {
  if (media.has(tabId)) return media.get(tabId);
  let list = [];
  if (session) {
    try {
      const o = await session.get('m' + tabId);
      list = o['m' + tabId] || [];
    } catch (e) { /* not available */ }
  }
  media.set(tabId, list);
  return list;
}

async function saveTabMedia(tabId, list) {
  media.set(tabId, list);
  if (session) {
    try {
      await session.set({ ['m' + tabId]: list });
    } catch (e) { /* not available */ }
  }
  try {
    await ACTION.setBadgeText({ tabId, text: list.length ? String(list.length) : '' });
  } catch (e) { /* the tab is gone */ }
}

async function clearTabMedia(tabId) {
  media.delete(tabId);
  if (session) {
    try {
      await session.remove('m' + tabId);
    } catch (e) { /* not available */ }
  }
  try {
    await ACTION.setBadgeText({ tabId, text: '' });
  } catch (e) { /* the tab is gone */ }
}

async function noteMedia(d) {
  if (d.tabId < 0 || d.statusCode < 200 || d.statusCode >= 400) return;
  const ct = header(d.responseHeaders, 'content-type').toLowerCase();
  const url = d.url;
  let kind = '';
  if (/mpegurl/.test(ct) || /\.m3u8(\?|#|$)/i.test(url)) kind = 'hls';
  else if (/dash\+xml/.test(ct) || /\.mpd(\?|#|$)/i.test(url)) kind = 'dash';
  else if (/^video\//.test(ct)) kind = 'video';
  else if (/^audio\//.test(ct)) kind = 'audio';
  if (!kind) return;
  // YouTube is read from its page; parts of streams are not files of their own.
  if (isYouTube(url) || /video\/mp2t|iso\.segment|mp4a-latm/.test(ct) || /\.(ts|m4s|m4f|aac)(\?|$)/i.test(url)) return;
  let size = 0;
  const cr = /\/(\d+)\s*$/.exec(header(d.responseHeaders, 'content-range'));
  if (cr) size = Number(cr[1]);
  else size = Number(header(d.responseHeaders, 'content-length')) || 0;
  if ((kind === 'video' || kind === 'audio') && d.type === 'xmlhttprequest' && size > 0 && size < 512 * 1024) return;
  const list = (await tabMedia(d.tabId)).slice();
  const key = mediaKey(url);
  const i = list.findIndex((m) => mediaKey(m.url) === key);
  const item = { url: kind === 'hls' || kind === 'dash' ? url : key, kind, mime: ct.split(';')[0], size };
  if (i >= 0) {
    if (size > (list[i].size || 0)) list[i].size = size;
    else return;
  } else {
    if (kind === 'hls' && list.filter((m) => m.kind === 'hls').length >= 3) return; // a stream's own lists
    list.unshift(item);
    list.splice(20);
  }
  await saveTabMedia(d.tabId, list);
}

try {
  // (onResponseStarted, unlike onHeadersReceived, also sees what comes from the cache.)
  B.webRequest.onResponseStarted.addListener((d) => {
    serial(() => noteMedia(d));
  }, { urls: ['<all_urls>'], types: ['media', 'xmlhttprequest', 'object', 'other'] }, ['responseHeaders']);
  // A new page in the tab: what the old one played goes. (A request event too, so it
  // always comes before the new page's own.)
  B.webRequest.onBeforeRequest.addListener((d) => {
    if (d.tabId >= 0) serial(() => clearTabMedia(d.tabId));
  }, { urls: ['<all_urls>'], types: ['main_frame'] });
} catch (e) { /* not allowed here */ }

B.tabs.onRemoved.addListener((tabId) => {
  serial(() => clearTabMedia(tabId));
});

// ---------------------------------------------------------------------------
// Sending videos and links
// ---------------------------------------------------------------------------

/* sendVideo asks HELINK for the video of a page: its post's own address first (the
   sites' readers know all qualities), plus what the page played, in case. */
async function sendVideo(v, tab) {
  const pageUrl = (tab && tab.url) || v.pageUrl || v.frameUrl || '';
  const list = tab && tab.id >= 0 ? (await tabMedia(tab.id)).slice() : [];
  if (isWeb(v.src) && !list.some((m) => mediaKey(m.url) === mediaKey(v.src))) {
    list.unshift({ url: v.src, kind: 'video', mime: '', size: 0 });
  }
  const target = v.postUrl || (isWeb(v.frameUrl) && v.frameUrl !== pageUrl && /\/embed\//.test(v.frameUrl) ? v.frameUrl : '') || pageUrl;
  const urls = [target, pageUrl].concat(list.map((m) => m.url)).filter((u) => isWeb(u) && !isYouTube(u));
  const cookies = await cookiesFor(urls, { incognito: tab && tab.incognito, storeId: tab && tab.cookieStoreId });
  return native({
    type: 'video', pageUrl, postUrl: target, title: v.title || (tab && tab.title) || '', media: list,
    cookies, userAgent: UA
  }, 60000);
}

/* qualities asks HELINK for a video's qualities, for the menu on its button (the same
   reading as sendVideo, answered here instead of in HELINK's window). */
async function qualities(v, tab) {
  const pageUrl = (tab && tab.url) || v.pageUrl || v.frameUrl || '';
  const list = tab && tab.id >= 0 ? (await tabMedia(tab.id)).slice() : [];
  if (isWeb(v.src) && !list.some((m) => mediaKey(m.url) === mediaKey(v.src))) {
    list.unshift({ url: v.src, kind: 'video', mime: '', size: 0 });
  }
  const target = v.postUrl || (isWeb(v.frameUrl) && v.frameUrl !== pageUrl && /\/embed\//.test(v.frameUrl) ? v.frameUrl : '') || pageUrl;
  const urls = [target, pageUrl].concat(list.map((m) => m.url)).filter((u) => isWeb(u) && !isYouTube(u));
  const cookies = await cookiesFor(urls, { incognito: tab && tab.incognito, storeId: tab && tab.cookieStoreId });
  return native({
    type: 'qualities', pageUrl, postUrl: target, title: v.title || (tab && tab.title) || '', media: list,
    cookies, userAgent: UA
  }, 115000);
}

/* sendProduct sends a product's page as the user sees it (Amazon, Alibaba, AliExpress,
   Noon, Temu): HELINK finds its photos and videos in it and shows them in its window. The
   page's text holds the product's data (the pictures at full size); the browser's own copy is
   used because these sites answer programs with a check page. */
async function sendProduct(tab) {
  if (!tab || !isWeb(tab.url) || !helinkProductSite(tab.url)) return { ok: false, error: 'not_product' };
  const grab = () => {
    const html = document.documentElement ? document.documentElement.outerHTML : '';
    return { html: html.length > 24e6 ? html.slice(0, 24e6) : html, title: document.title || '', url: location.href };
  };
  let page = null;
  try {
    if (FIREFOX) {
      const r = await browser.tabs.executeScript(tab.id, { code: '(' + grab.toString() + ')()' });
      page = r && r[0];
    } else {
      const r = await chrome.scripting.executeScript({ target: { tabId: tab.id }, func: grab });
      page = r && r[0] && r[0].result;
    }
  } catch (e) {
    return { ok: false, error: 'no_page' };
  }
  if (!page || !page.html) return { ok: false, error: 'no_page' };
  const cookies = await cookiesFor([page.url || tab.url], { incognito: tab.incognito, storeId: tab.cookieStoreId });
  return native({ type: 'product', pageUrl: page.url || tab.url, title: page.title || tab.title || '', html: page.html, cookies, userAgent: UA }, 60000);
}

async function sendLink(url, pageUrl, tab) {
  const cookies = await cookiesFor([url], { incognito: tab && tab.incognito, storeId: tab && tab.cookieStoreId });
  return native({ type: 'link', url, referrer: pageUrl || '', pageUrl: pageUrl || '', cookies, userAgent: UA }, 60000);
}

// ---------------------------------------------------------------------------
// Menus
// ---------------------------------------------------------------------------

// (chrome.* in Firefox too: its callbacks work there, unlike browser.*'s.)
const MENUS = (globalThis.chrome && chrome.contextMenus) || B.contextMenus;

async function makeMenus() {
  const l = await lang();
  const t = (k) => helinkT(l, k);
  try {
    await new Promise((r) => MENUS.removeAll(() => r(void (globalThis.chrome && chrome.runtime.lastError))));
  } catch (e) { /* none yet */ }
  const add = (o) => {
    try {
      MENUS.create(o, () => void (globalThis.chrome && chrome.runtime.lastError));
    } catch (e) { /* already there */ }
  };
  add({ id: 'helink-link', title: t('menuLink'), contexts: ['link'] });
  add({ id: 'helink-media', title: t('menuMedia'), contexts: ['video'] });
  add({ id: 'helink-audio', title: t('menuAudio'), contexts: ['audio'] });
  add({ id: 'helink-page', title: t('menuPage'), contexts: ['page', 'frame'] });
  add({ id: 'helink-product', title: t('menuProduct'), contexts: ['page', 'image'], documentUrlPatterns: HELINK_PRODUCT_PATTERNS });
}

async function onMenu(info, tab) {
  const page = info.pageUrl || (tab && tab.url) || '';
  switch (info.menuItemId) {
    case 'helink-link':
      return sendLink(info.linkUrl, page, tab);
    case 'helink-media':
    case 'helink-audio':
      if (isWeb(info.srcUrl) && info.menuItemId === 'helink-audio') return sendLink(info.srcUrl, page, tab);
      return sendVideo({ src: isWeb(info.srcUrl) ? info.srcUrl : '', frameUrl: info.frameUrl, pageUrl: page }, tab);
    case 'helink-page':
      return sendVideo({ frameUrl: info.frameUrl, pageUrl: page }, tab);
    case 'helink-product':
      return sendProduct(tab);
  }
  return null;
}

MENUS.onClicked.addListener((info, tab) => {
  onMenu(info, tab);
});

/* The pages already open get the button too, after an install or an update (the copy an
   older version left there is cut off from the extension). */
async function injectIntoOpenTabs() {
  let tabs = [];
  try {
    tabs = await B.tabs.query({ url: ['http://*/*', 'https://*/*'] });
  } catch (e) {
    return;
  }
  for (const t of tabs) {
    try {
      if (FIREFOX) await browser.tabs.executeScript(t.id, { file: 'content.js', allFrames: true });
      else await chrome.scripting.executeScript({ target: { tabId: t.id, allFrames: true }, files: ['content.js'] });
    } catch (e) { /* a page that takes no scripts (the store, a PDF…) */ }
  }
}

B.runtime.onInstalled.addListener((d) => {
  makeMenus();
  state(true);
  if (d && (d.reason === 'install' || d.reason === 'update')) injectIntoOpenTabs();
});
if (B.runtime.onStartup) {
  B.runtime.onStartup.addListener(() => {
    makeMenus();
    state(true);
  });
}
if (FIREFOX) makeMenus(); // Firefox forgets the menus when the background page starts again
try {
  ACTION.setBadgeBackgroundColor({ color: '#F27221' });
} catch (e) { /* older browsers */ }

B.storage.onChanged.addListener((changes, area) => {
  if (area === 'local' && changes.lang) makeMenus();
});

// ---------------------------------------------------------------------------
// Messages from the pages (the button on videos), the toolbar window and the settings
// ---------------------------------------------------------------------------

async function activeTab() {
  const tabs = await B.tabs.query({ active: true, currentWindow: true });
  return tabs && tabs[0];
}

/* A video or sound element of a page plays a file (it may come from the browser's
   memory, without any request to note). */
async function noteSeen(tabId, url, kind) {
  if (tabId < 0 || !isWeb(url) || isYouTube(url)) return;
  const list = (await tabMedia(tabId)).slice();
  const key = mediaKey(url);
  if (list.some((m) => mediaKey(m.url) === key)) return;
  list.unshift({ url: key, kind: kind === 'audio' ? 'audio' : 'video', mime: '', size: 0 });
  list.splice(20);
  await saveTabMedia(tabId, list);
}

async function handle(msg, sender) {
  switch (msg && msg.type) {
    case 'seen':
      if (sender.tab) await serial(() => noteSeen(sender.tab.id, msg.url, msg.kind));
      return { ok: true };
    case 'video': // the button on a video: "open in HELINK"
      return sendVideo(msg, sender.tab);
    case 'qualities': // the button's menu
      return qualities(msg, sender.tab);
    case 'get': // a quality chosen in that menu
      return native({ type: 'get', id: String(msg.id || ''), item: Number(msg.item) || 0, key: String(msg.key || ''), noMusic: !!msg.noMusic }, 30000);
    case 'state': {
      const st = await state(!!msg.force);
      const tab = msg.tabId >= 0 ? await B.tabs.get(msg.tabId).catch(() => null) : await activeTab();
      return {
        ok: true, state: st, lang: await lang(), version: B.runtime.getManifest().version,
        tab: tab ? { id: tab.id, url: tab.url || '', title: tab.title || '' } : null,
        media: tab && tab.id >= 0 ? await tabMedia(tab.id) : []
      };
    }
    case 'pageVideo': {
      const tab = await B.tabs.get(msg.tabId).catch(() => null);
      if (!tab || !isWeb(tab.url)) return { ok: false, error: 'not_web' };
      return sendVideo({ pageUrl: tab.url }, tab);
    }
    case 'media': { // one of the files the page played, from the toolbar window
      const tab = await B.tabs.get(msg.tabId).catch(() => null);
      const m = tab ? (await tabMedia(tab.id))[msg.index] : null;
      if (!m) return { ok: false, error: 'gone' };
      if (m.kind === 'hls' || m.kind === 'dash') return sendVideo({ postUrl: m.url, pageUrl: tab.url }, tab);
      return sendLink(m.url, tab.url, tab);
    }
    case 'product': { // the toolbar window's "Product photos and videos"
      const tab = await B.tabs.get(msg.tabId).catch(() => null);
      return sendProduct(tab);
    }
    case 'list': { // a YouTube list (a course) from the button's menu: HELINK shows all its videos
      const url = String(msg.url || '');
      if (!/^https:\/\/www\.youtube\.com\/playlist\?list=[A-Za-z0-9_-]{2,80}$/.test(url)) return { ok: false, error: 'bad_message' };
      // Never YouTube's cookies: HELINK reads YouTube without an account.
      return native({ type: 'link', url, referrer: '', pageUrl: (sender.tab && sender.tab.url) || '', cookies: [], userAgent: UA }, 60000);
    }
    case 'show':
      return native({ type: 'show' }, 30000);
    case 'setSettings': {
      const resp = await native({ type: 'setSettings', settings: msg.settings }, 30000);
      const st = await stored('helink', { settings: Object.assign({}, DEFAULT_SETTINGS) });
      st.settings = Object.assign({}, DEFAULT_SETTINGS, st.settings, resp.ok && resp.settings ? resp.settings : msg.settings);
      await store('helink', st);
      return { ok: true, saved: !!resp.ok, settings: st.settings, error: resp.ok ? '' : resp.error };
    }
    case 'test': {
      const ping = await native({ type: 'ping' }, 15000);
      const st = await state(true);
      return { ok: true, host: !!ping.ok, hostError: ping.ok ? '' : ping.error, state: st };
    }
    case 'resetPositions':
      await store('pos', {});
      return { ok: true };
  }
  return { ok: false, error: 'unknown' };
}

B.runtime.onMessage.addListener((msg, sender, sendResponse) => {
  handle(msg, sender).then(sendResponse, (e) => sendResponse({ ok: false, error: String(e) }));
  return true;
});

// For the tests (the menus cannot be clicked by a program).
globalThis.helinkTest = { onMenu, takeDownload, wanted, state, native, injectIntoOpenTabs, reloadIfUpdated, newerVersion,
  uiHidden: () => deciding > 0, likelyWanted, sendProduct,
  resetReloadCheck: () => { reloadChecked = 0; } };
