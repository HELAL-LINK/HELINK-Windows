/* HELINK's button on videos: it shows over a video while the mouse is on it (and for a
   moment when a video starts playing), can be dragged anywhere on the video (the place is
   kept for each site), and opens a menu of the video's qualities, video or MP3, read by
   HELINK; the one chosen is downloaded by HELINK right away, which shows its progress.
   "Open in HELINK" sends the video to HELINK's window instead. */
(() => {
  'use strict';
  const B = globalThis.browser || globalThis.chrome;
  if (!B || !B.runtime || !B.runtime.id) return;
  const alive = () => { try { return !stopped && !!(B.runtime && B.runtime.id); } catch (e) { return false; } };
  // One copy per page. After an update of the extension the copy already here is cut off
  // from it (and the new one is put in the open pages): the new one takes over.
  let stopped = false;
  const timers = [];
  const prev = window.__helinkButton;
  if (prev && typeof prev.alive === 'function' && prev.alive()) return;
  if (prev && typeof prev.stop === 'function') prev.stop();
  window.__helinkButton = {
    alive,
    stop() {
      stopped = true;
      timers.forEach(clearInterval);
      if (ui) ui.host.remove();
    }
  };

  const WORDS = {
    ar: {
      download: 'تنزيل', hide: 'إخفاء الزر لهذا الفيديو', sending: 'جارٍ الإرسال…', sent: 'أُرسل إلى HELINK',
      noApp: 'ثبّت برنامج HELINK أولًا', failed: 'تعذّر الإرسال إلى HELINK', tip: 'تنزيل الفيديو بـ HELINK (اسحبه لتحريكه)',
      menu: 'تنزيل بـ HELINK', reading: 'جارٍ قراءة الجودات…', video: 'فيديو', audio: 'صوت MP3', noMusic: 'بدون موسيقى',
      noMusicTip: 'يُنزّل HELINK الفيديو ثم يُزيل الموسيقى ويُبقي الأصوات', noMusicFirst: 'أول مرة فقط يُنزّل HELINK أداة إزالة الموسيقى (نحو 64 ميجابايت)',
      openApp: 'فتح في HELINK', started: 'بدأ التنزيل في HELINK', already: 'هذا التنزيل في HELINK بالفعل', retry: 'حاول مرة أخرى',
      close: 'إغلاق', best: 'أفضل جودة', hd: 'جودة عالية', sd: 'جودة عادية', recommended: 'موصى بها', part: 'فيديو {n}',
      kb: 'كيلوبايت', mb: 'ميجابايت', gb: 'جيجابايت', noChoices: 'لم يجد HELINK جودات لهذا الفيديو.',
      appClosed: 'HELINK يبدأ الآن…', wholeList: 'تنزيل القائمة كاملة'
    },
    en: {
      download: 'Download', hide: 'Hide the button for this video', sending: 'Sending…', sent: 'Sent to HELINK',
      noApp: 'Install the HELINK app first', failed: "Couldn't send it to HELINK", tip: 'Download the video with HELINK (drag to move)',
      menu: 'Download with HELINK', reading: 'Reading the qualities…', video: 'Video', audio: 'MP3 audio', noMusic: 'Without music',
      noMusicTip: 'HELINK downloads the video, then removes the music and keeps the voices', noMusicFirst: 'The first time only, HELINK downloads its music remover (about 64 MB)',
      openApp: 'Open in HELINK', started: 'Downloading in HELINK', already: 'This download is already in HELINK', retry: 'Try again',
      close: 'Close', best: 'Best quality', hd: 'High quality', sd: 'Standard quality', recommended: 'Recommended', part: 'Video {n}',
      kb: 'KB', mb: 'MB', gb: 'GB', noChoices: "HELINK found no qualities for this video.",
      appClosed: 'Starting HELINK…', wholeList: 'Download the whole list'
    }
  };
  const site = location.hostname.replace(/^www\./, '').toLowerCase();
  let lang = 'en'; // the browser's language until the settings say otherwise (below)
  let enabled = true;
  let positions = {};
  let prefs = { kind: 'video', noMusic: false };
  const hiddenFor = new WeakSet(); // videos whose button was closed with ✕
  const W = () => WORDS[lang] || WORDS.en;

  function pickLang(pref) {
    if (pref === 'ar' || pref === 'en') return pref;
    let ui = '';
    try { ui = B.i18n.getUILanguage(); } catch (e) { ui = navigator.language || ''; }
    return /^ar\b/i.test(ui) ? 'ar' : 'en';
  }

  lang = pickLang('auto');

  function listed(list) {
    return (list || []).some((s) => site === s || site.endsWith('.' + s));
  }

  function apply(o) {
    if ('lang' in o) lang = pickLang(o.lang);
    if (o.helink && o.helink.settings) {
      const s = o.helink.settings;
      enabled = s.buttons !== false && !listed(s.buttonSkip);
    }
    if (o.pos) positions = o.pos || {};
    if (o.panel && typeof o.panel === 'object') prefs = Object.assign(prefs, o.panel);
    if (ui) ui.words();
    if (!enabled) hide(0);
  }

  try {
    B.storage.local.get(['helink', 'lang', 'pos', 'panel']).then(apply, () => {});
    B.storage.onChanged.addListener((ch, area) => {
      if (area !== 'local') return;
      const o = {};
      for (const k of ['helink', 'lang', 'pos', 'panel']) if (ch[k]) o[k] = ch[k].newValue;
      apply(o);
    });
  } catch (e) { /* storage unavailable here */ }

  function savePrefs() {
    try { B.storage.local.set({ panel: prefs }); } catch (e) { /* storage unavailable */ }
  }

  // -------------------------------------------------------------------------
  // The videos of the page (in its shadow parts too)
  // -------------------------------------------------------------------------

  const videos = new Set();

  function lightScan() {
    const list = document.getElementsByTagName('video');
    for (let i = 0; i < list.length; i++) videos.add(list[i]);
  }

  function deepScan(root, budget) {
    const w = document.createTreeWalker(root, NodeFilter.SHOW_ELEMENT);
    let n;
    while ((n = w.nextNode()) && budget.left-- > 0) {
      if (n.shadowRoot) {
        const inner = n.shadowRoot.querySelectorAll('video');
        for (let i = 0; i < inner.length; i++) videos.add(inner[i]);
        deepScan(n.shadowRoot, budget);
      }
    }
  }

  function boxOf(v) {
    if (!v.isConnected) {
      videos.delete(v);
      return null;
    }
    const r = v.getBoundingClientRect();
    if (r.width < 150 || r.height < 100) return null;
    if (r.bottom <= 0 || r.right <= 0 || r.top >= innerHeight || r.left >= innerWidth) return null;
    return r;
  }

  /* Shown: not hidden by itself or by what it is in (players keep the next video ready,
     invisible, in the same place). */
  function visible(v) {
    try {
      if (typeof v.checkVisibility === 'function') return v.checkVisibility({ opacityProperty: true, visibilityProperty: true });
    } catch (e) { /* older browser */ }
    const cs = getComputedStyle(v);
    return cs.visibility !== 'hidden' && cs.display !== 'none' && Number(cs.opacity) > 0.05;
  }

  /* The video under the point: the one on top where several are (a window over the feed,
     the next video waiting under the current one), else the one playing, else the smallest. */
  function videoAt(x, y) {
    const cands = [];
    for (const v of videos) {
      const r = boxOf(v);
      if (!r || x < r.left || x > r.right || y < r.top || y > r.bottom || !visible(v)) continue;
      cands.push({ v, area: r.width * r.height });
    }
    if (cands.length <= 1) return cands.length ? cands[0].v : null;
    let stack = [];
    try { stack = document.elementsFromPoint(x, y); } catch (e) { /* no layout yet */ }
    const depth = (v) => {
      const i = stack.indexOf(v);
      return i < 0 ? Infinity : i;
    };
    cands.sort((a, b) => depth(a.v) - depth(b.v) || Number(b.v.paused === false) - Number(a.v.paused === false) || a.area - b.area);
    return cands[0].v;
  }

  // -------------------------------------------------------------------------
  // The post a video belongs to (the sites' readers want the post's address)
  // -------------------------------------------------------------------------

  function up(el) {
    return el.parentElement || (el.getRootNode && el.getRootNode() !== document && el.getRootNode().host) || null;
  }

  /* A link matching re near the video, without going up into the feed (where other
     videos are). */
  function nearLink(v, re) {
    let el = up(v);
    for (let i = 0; el && i < 16; i++, el = up(el)) {
      if (el.getElementsByTagName && el.getElementsByTagName('video').length > 1) break;
      const links = el.querySelectorAll ? el.querySelectorAll('a[href]') : [];
      for (let j = 0; j < links.length; j++) {
        if (re.test(links[j].href)) return links[j].href;
      }
    }
    return '';
  }

  /* A TikTok video's number, from the attributes of what holds it (the player's id ends
     with it: "xgwrapper-0-7432…"). */
  function tiktokId(v) {
    const re = /(?:^|\D)(\d{17,20})(?:\D|$)/;
    for (let el = v, i = 0; el && i < 14; i++, el = up(el)) {
      const attrs = el.attributes || [];
      for (let j = 0; j < attrs.length; j++) {
        const a = attrs[j];
        if (a.name === 'id' || a.name.startsWith('data-')) {
          const m = re.exec(a.value);
          if (m) return m[1];
        }
      }
      // Not up into the feed, where the other videos are.
      if (i > 0 && el.getElementsByTagName && el.getElementsByTagName('video').length > 1) break;
    }
    return '';
  }

  function postUrl(v) {
    const h = site;
    const here = location.href;
    if (h.endsWith('youtube.com') || h === 'youtu.be' || h.endsWith('youtube-nocookie.com')) {
      const m = /\/embed\/([\w-]{11})/.exec(location.pathname);
      if (m) return 'https://www.youtube.com/watch?v=' + m[1];
      if (/^\/(watch|shorts\/|live\/)/.test(location.pathname) && v.closest && v.closest('#movie_player, #shorts-player, ytd-reel-video-renderer[is-active]')) return here;
      return nearLink(v, /youtube\.com\/(watch\?v=|shorts\/)/) || here;
    }
    if (h.endsWith('instagram.com')) {
      return nearLink(v, /instagram\.com\/([\w.]+\/)?(p|reel|reels|tv)\/[\w-]+/) || here;
    }
    if (h.endsWith('facebook.com')) {
      return nearLink(v, /facebook\.com\/(.+\/videos\/|reel\/|watch\/?\?v=|.+\/posts\/|permalink\.php|story\.php|share\/[vr]\/)/) || here;
    }
    if (h === 'x.com' || h.endsWith('.x.com') || h.endsWith('twitter.com')) {
      return nearLink(v, /(x|twitter)\.com\/[^/]+\/status\/\d+/) || here;
    }
    if (h.endsWith('tiktok.com')) {
      const link = nearLink(v, /tiktok\.com\/@[^/]*\/(video|photo)\/\d+/);
      if (link) return link;
      const id = tiktokId(v);
      if (id) {
        const own = /\/@([^/?#]+)\/(video|photo)\/(\d+)/.exec(location.pathname);
        return own && own[3] === id ? here : 'https://www.tiktok.com/@/video/' + id;
      }
      return here;
    }
    return here;
  }

  function srcOf(v) {
    const s = v.currentSrc || v.src || ((v.querySelector && v.querySelector('source[src]')) || {}).src || '';
    return /^https?:/i.test(s) ? s : '';
  }

  // -------------------------------------------------------------------------
  // The qualities, asked for as soon as the mouse is on the button
  // -------------------------------------------------------------------------

  const asked = new Map(); // post address -> {at, p: promise of the answer}

  function videoMessage(v, type) {
    return {
      type, postUrl: postUrl(v), frameUrl: location.href,
      title: (v.getAttribute('aria-label') || v.title || document.title || '').slice(0, 300), src: srcOf(v)
    };
  }

  function send(msg) {
    if (!alive()) return Promise.resolve({ ok: false, error: 'extension' });
    try {
      return B.runtime.sendMessage(msg).then((r) => r || { ok: false, error: 'extension' }, () => ({ ok: false, error: 'extension' }));
    } catch (e) {
      return Promise.resolve({ ok: false, error: 'extension' });
    }
  }

  function qualities(v, again) {
    const msg = videoMessage(v, 'qualities');
    const k = msg.postUrl + '|' + msg.src;
    const old = asked.get(k);
    if (old && !again && Date.now() - old.at < 10 * 60 * 1000) return old.p;
    const entry = { at: Date.now(), p: null };
    entry.p = send(msg).then((r) => {
      if (!r || !r.ok) asked.delete(k); // a failure is asked again next time
      return r;
    });
    asked.set(k, entry);
    if (asked.size > 40) asked.delete(asked.keys().next().value);
    return entry.p;
  }

  function sizeText(b) {
    const w = W();
    if (!(b > 0)) return '';
    const mib = 1024 * 1024;
    const num = (x, d) => x.toLocaleString('en-US', { maximumFractionDigits: d }); // the digits HELINK's window uses, in both languages
    if (b < mib) return num(Math.max(1, Math.round(b / 1024)), 0) + ' ' + w.kb;
    if (b < 10 * mib) return num(Math.round(b / mib * 10) / 10, 1) + ' ' + w.mb;
    if (b < 1000 * mib) return num(Math.round(b / mib), 0) + ' ' + w.mb;
    return num(Math.round(b / mib / 1024 * 10) / 10, 1) + ' ' + w.gb;
  }

  function labelOf(o) {
    const w = W();
    if (o.key === 'vbest') return w.best;
    if (o.key === 'vhd') return w.hd;
    if (o.key === 'vsd') return w.sd;
    return o.label || o.key;
  }

  // -------------------------------------------------------------------------
  // The button and its menu
  // -------------------------------------------------------------------------

  const SVG = 'http://www.w3.org/2000/svg';
  /* Built element by element: some pages forbid HTML strings (Trusted Types). */
  function svg(cls, viewBox, paths) {
    const el = document.createElementNS(SVG, 'svg');
    if (cls) el.setAttribute('class', cls);
    el.setAttribute('viewBox', viewBox);
    el.setAttribute('aria-hidden', 'true');
    for (const p of paths) {
      const path = document.createElementNS(SVG, 'path');
      for (const k of Object.keys(p)) path.setAttribute(k, p[k]);
      el.appendChild(path);
    }
    return el;
  }
  const icon = () => svg('', '212 737 264 351', [
    { fill: 'currentColor', d: 'M222 747V1078L284.5 1032.5V950.5H402.5V1030.5L461 1078V858L402.5 896.5H284.5V793Z' },
    { fill: '#F27221', d: 'M466 758L408 804L403 878L460 840.5Z' }]);
  const arrow = () => svg('arrow', '0 0 24 24', [{ fill: 'none', stroke: 'currentColor', 'stroke-width': '2.4',
    'stroke-linecap': 'round', 'stroke-linejoin': 'round', d: 'M12 4v11m0 0l-4.5-4.5M12 15l4.5-4.5M5 19.5h14' }]);
  const CSS = `
    :host { all: initial; }
    .b { position: fixed; left: 0; top: 0; display: flex; align-items: center; height: 34px; border-radius: 17px;
      background: rgba(17,17,17,.94); color: #fff; box-shadow: 0 3px 14px rgba(0,0,0,.45), 0 0 0 1px rgba(255,255,255,.14);
      font: 600 13px/1 system-ui, -apple-system, "Segoe UI", Tahoma, Arial, sans-serif; user-select: none; -webkit-user-select: none;
      cursor: grab; opacity: 0; transform: translateY(-3px); transition: opacity .14s ease, transform .14s ease; pointer-events: none;
      touch-action: none; white-space: nowrap; }
    .b.on { opacity: 1; transform: none; pointer-events: auto; }
    .b.drag { cursor: grabbing; transition: none; }
    .go { all: unset; display: flex; align-items: center; gap: 7px; height: 34px; padding: 0 13px 0 10px; cursor: inherit; color: #fff; border-radius: 17px; }
    .go:focus-visible { outline: 2px solid #F27221; outline-offset: 1px; }
    .go svg { width: 17px; height: 17px; flex: none; }
    .go .arrow { width: 16px; height: 16px; color: #F27221; }
    .b:hover .go .t, .b.open .go .t { color: #F27221; }
    .x { all: unset; display: none; width: 22px; height: 22px; margin-inline-end: 6px; margin-inline-start: -6px; border-radius: 11px;
      align-items: center; justify-content: center; color: #bbb; font-size: 11px; cursor: pointer; }
    .b:hover .x { display: flex; }
    .b.open .x { display: none; }
    .x:hover { background: #333; color: #fff; }
    .b.busy .arrow, .p .spin { animation: spin .9s linear infinite; }
    .b.ok { background: rgba(20,110,60,.95); }
    .b.bad { background: rgba(150,30,30,.95); }
    @keyframes spin { to { transform: rotate(360deg); } }

    .p { position: fixed; left: 0; top: 0; width: 292px; max-width: calc(100vw - 16px); display: none; flex-direction: column;
      background: rgba(20,20,20,.98); color: #f2f2f2; border-radius: 12px; box-shadow: 0 10px 34px rgba(0,0,0,.55), 0 0 0 1px rgba(255,255,255,.12);
      font: 400 13px/1.35 system-ui, -apple-system, "Segoe UI", Tahoma, Arial, sans-serif; overflow: hidden; user-select: none; -webkit-user-select: none; }
    .p.on { display: flex; }
    .p button { all: unset; box-sizing: border-box; cursor: pointer; }
    .p button:focus-visible { outline: 2px solid #F27221; outline-offset: -2px; }
    .hd { display: flex; align-items: center; gap: 8px; padding: 10px 10px 8px 12px; }
    .hd .ti { flex: 1; min-width: 0; font-weight: 600; white-space: nowrap; overflow: hidden; text-overflow: ellipsis; color: #fff; }
    .hd .cl { width: 24px; height: 24px; border-radius: 12px; display: flex; align-items: center; justify-content: center; color: #aaa; font-size: 12px; flex: none; }
    .hd .cl:hover { background: #333; color: #fff; }
    .kinds { display: flex; margin: 0 12px 8px; background: #2b2b2b; border-radius: 9px; padding: 3px; gap: 3px; }
    .kinds button { flex: 1; text-align: center; padding: 6px 4px; border-radius: 7px; color: #ddd; font-weight: 600; }
    .kinds button[aria-pressed="true"] { background: #F27221; color: #111; }
    .parts { display: flex; flex-wrap: wrap; gap: 5px; margin: 0 12px 8px; }
    .parts button { padding: 3px 9px; border-radius: 10px; background: #2b2b2b; color: #ddd; font-size: 12px; }
    .parts button[aria-pressed="true"] { background: #fff; color: #111; }
    .list { overflow: auto; padding: 0 6px; flex: 0 1 auto; min-height: 0; }
    .p .row { display: flex; align-items: center; gap: 8px; width: 100%; padding: 9px 10px; border-radius: 8px; color: #f2f2f2; }
    .p .row:hover, .p .row:focus-visible { background: #2e2e2e; }
    .p .row .l { font-weight: 700; color: #fff; min-width: 58px; }
    .p .row .g { font-size: 11px; color: #bbb; border: 1px solid #555; border-radius: 5px; padding: 0 5px; line-height: 16px; }
    .p .row .r { font-size: 11px; color: #F27221; }
    .p .row .s { margin-inline-start: auto; color: #bbb; font-variant-numeric: tabular-nums; }
    .p .row.busy { opacity: .7; pointer-events: none; }
    .p .row .spin { width: 14px; height: 14px; border: 2px solid #777; border-top-color: #F27221; border-radius: 50%; margin-inline-start: auto; }
    .msg { padding: 14px 14px 10px; color: #ddd; display: flex; align-items: center; gap: 10px; }
    .msg .spin { width: 16px; height: 16px; border: 2px solid #555; border-top-color: #F27221; border-radius: 50%; flex: none; }
    .msg.err { flex-direction: column; align-items: stretch; gap: 4px; }
    .msg.err b { color: #ffb4a8; }
    .msg.ok { color: #9fe0b3; font-weight: 600; }
    .opts { display: flex; align-items: center; gap: 8px; padding: 8px 12px; border-top: 1px solid #2e2e2e; margin-top: 6px; }
    .p .sw { display: flex; align-items: center; gap: 8px; color: #ddd; padding: 2px 0; }
    .sw .k { width: 30px; height: 17px; border-radius: 9px; background: #555; position: relative; flex: none; transition: background .12s; }
    .sw .k::after { content: ""; position: absolute; top: 2px; inset-inline-start: 2px; width: 13px; height: 13px; border-radius: 50%; background: #fff; transition: inset-inline-start .12s; }
    .sw[aria-checked="true"] .k { background: #F27221; }
    .sw[aria-checked="true"] .k::after { inset-inline-start: 15px; }
    .ft { display: flex; gap: 8px; padding: 8px 12px 11px; }
    .ft button { color: #FFB27A; font-weight: 600; padding: 2px 0; }
    .ft button:hover { text-decoration: underline; }
    .ft .rt { margin-inline-start: auto; }
    @media (prefers-reduced-motion: reduce) { .b { transition: none; } .b.busy .arrow, .p .spin { animation: none; } }`;

  let ui = null;
  let current = null;
  let hideTimer = 0;
  let stateTimer = 0;
  let pinned = false; // while dragging or sending, or while the menu is open

  function hostParent() {
    const fs = document.fullscreenElement;
    return fs && fs.tagName !== 'VIDEO' ? fs : document.documentElement;
  }

  function el(tag, cls, text) {
    const e = document.createElement(tag);
    if (cls) e.className = cls;
    if (text != null) e.textContent = text;
    return e;
  }

  function makeUI() {
    // A copy left by an older version of the extension (updated while this page was open).
    for (const old of document.querySelectorAll('helink-video-button')) old.remove();
    const hostEl = document.createElement('helink-video-button');
    hostEl.style.cssText = 'all:initial;position:fixed;left:0;top:0;width:0;height:0;z-index:2147483647;';
    const root = hostEl.attachShadow({ mode: 'open' });
    let styled = false;
    // Chromium: a stylesheet of its own (no page rule touches it). Firefox's content scripts
    // cannot give a page's shadow root such a sheet: a <style> there.
    if (typeof globalThis.browser === 'undefined') {
      try {
        const sheet = new CSSStyleSheet();
        sheet.replaceSync(CSS);
        root.adoptedStyleSheets = [sheet];
        styled = root.adoptedStyleSheets.length === 1;
      } catch (e) { /* older browser */ }
    }
    if (!styled) {
      const st = document.createElement('style');
      st.textContent = CSS;
      root.appendChild(st);
    }
    const box = el('div', 'b');
    box.setAttribute('role', 'group');
    const go = el('button', 'go');
    go.type = 'button';
    go.setAttribute('aria-haspopup', 'dialog');
    go.setAttribute('aria-expanded', 'false');
    const label = el('span', 't');
    go.append(icon(), label, arrow());
    const x = el('button', 'x', '✕');
    x.type = 'button';
    box.append(go, x);
    const panel = el('div', 'p');
    panel.setAttribute('role', 'dialog');
    root.append(box, panel);
    const u = {
      host: hostEl, box, go, x, label, panel,
      words() {
        const w = W();
        box.dir = panel.dir = lang === 'ar' ? 'rtl' : 'ltr';
        if (!box.classList.contains('busy') && !box.classList.contains('ok') && !box.classList.contains('bad')) label.textContent = w.download;
        go.title = w.tip;
        x.title = w.hide;
        x.setAttribute('aria-label', w.hide);
        panel.setAttribute('aria-label', w.menu);
        if (menu) render();
      }
    };
    u.words();
    x.addEventListener('click', (e) => {
      e.stopPropagation();
      if (current) hiddenFor.add(current);
      hide(0);
    });
    // Dragging moves it; a click without moving opens the menu.
    let press = null;
    go.addEventListener('pointerenter', () => {
      if (current && enabled && alive()) qualities(current, false); // a head start
    });
    go.addEventListener('pointerdown', (e) => {
      if (e.button !== 0) return;
      e.preventDefault();
      e.stopPropagation();
      const r = box.getBoundingClientRect();
      press = { x: e.clientX, y: e.clientY, dx: e.clientX - r.left, dy: e.clientY - r.top, moved: false, id: e.pointerId };
      try { go.setPointerCapture(e.pointerId); } catch (err) { /* old browser */ }
    });
    go.addEventListener('pointermove', (e) => {
      if (!press || e.pointerId !== press.id) return;
      if (!press.moved && Math.hypot(e.clientX - press.x, e.clientY - press.y) > 5) {
        press.moved = true;
        pinned = true;
        box.classList.add('drag');
      }
      if (press.moved) {
        place(e.clientX - press.dx, e.clientY - press.dy, true);
        if (menu) placePanel();
      }
    });
    const end = (e) => {
      if (!press || e.pointerId !== press.id) return;
      const p = press;
      press = null;
      box.classList.remove('drag');
      if (p.moved) {
        pinned = !!menu;
        remember();
        return;
      }
      if (e.type === 'pointerup') toggleMenu();
    };
    go.addEventListener('pointerup', end);
    go.addEventListener('pointercancel', end);
    go.addEventListener('click', (e) => {
      e.preventDefault();
      e.stopPropagation();
      if (e.detail === 0) toggleMenu(); // the keyboard (Enter, Space)
    });
    // The page must not take what happens on the button or the menu as its own (a click
    // that pauses the video, keys that go to the next one).
    for (const ev of ['mousedown', 'mouseup', 'click', 'dblclick', 'contextmenu', 'pointerdown', 'pointerup', 'keydown', 'keyup', 'keypress', 'wheel']) {
      box.addEventListener(ev, (e) => e.stopPropagation());
      panel.addEventListener(ev, (e) => {
        if (ev === 'keydown' && e.key === 'Escape') closeMenu(true);
        e.stopPropagation();
      });
    }
    hostParent().appendChild(hostEl);
    return u;
  }

  /* The button's place on the video: a share of the room left inside it (0..1 each way),
     kept for each site. Top right at first. */
  function spot() {
    const p = positions[site];
    return p && typeof p.rx === 'number' ? p : { rx: 1, ry: 0 };
  }

  function place(left, top, free) {
    const r = current && boxOf(current);
    if (!r || !ui) return;
    const bw = ui.box.offsetWidth || 110;
    const bh = ui.box.offsetHeight || 34;
    const m = 10;
    const minL = r.left + m, maxL = Math.max(minL, r.right - bw - m);
    const minT = r.top + m, maxT = Math.max(minT, r.bottom - bh - m);
    if (!free) {
      const s = spot();
      left = minL + s.rx * (maxL - minL);
      top = minT + s.ry * (maxT - minT);
    }
    left = Math.min(Math.max(left, minL), maxL);
    top = Math.min(Math.max(top, minT), maxT);
    ui.box.style.left = Math.round(left) + 'px';
    ui.box.style.top = Math.round(top) + 'px';
  }

  function remember() {
    const r = current && boxOf(current);
    if (!r || !ui) return;
    const b = ui.box.getBoundingClientRect();
    const m = 10;
    const roomX = Math.max(1, r.width - b.width - 2 * m);
    const roomY = Math.max(1, r.height - b.height - 2 * m);
    const rx = Math.min(1, Math.max(0, (b.left - r.left - m) / roomX));
    const ry = Math.min(1, Math.max(0, (b.top - r.top - m) / roomY));
    positions = Object.assign({}, positions, { [site]: { rx: Math.round(rx * 1000) / 1000, ry: Math.round(ry * 1000) / 1000 } });
    try {
      B.storage.local.set({ pos: positions });
    } catch (e) { /* storage unavailable */ }
  }

  function show(v, forMs) {
    if (!enabled || !alive() || hiddenFor.has(v)) return;
    if (!ui) ui = makeUI();
    if (!ui.host.isConnected) hostParent().appendChild(ui.host);
    clearTimeout(hideTimer);
    hideTimer = 0;
    if (current !== v) {
      if (menu) closeMenu(false);
      current = v;
      setState('');
    }
    place();
    ui.box.classList.add('on');
    if (forMs) hideTimer = setTimeout(() => hide(0), forMs);
  }

  function hide(after) {
    clearTimeout(hideTimer);
    hideTimer = 0;
    const go = () => {
      hideTimer = 0;
      if (pinned || !ui) return;
      ui.box.classList.remove('on');
    };
    if (after) hideTimer = setTimeout(go, after);
    else go();
  }

  function setState(s, ms) {
    if (!ui) return;
    clearTimeout(stateTimer);
    const w = W();
    ui.box.classList.remove('busy', 'ok', 'bad');
    pinned = !!menu;
    switch (s) {
      case 'busy':
        ui.box.classList.add('busy');
        ui.label.textContent = w.sending;
        pinned = true;
        break;
      case 'ok':
        ui.box.classList.add('ok');
        ui.label.textContent = w.sent;
        break;
      case 'noapp':
      case 'bad':
        ui.box.classList.add('bad');
        ui.label.textContent = s === 'noapp' ? w.noApp : w.failed;
        break;
      default:
        ui.label.textContent = w.download;
    }
    if (ms) {
      stateTimer = setTimeout(() => {
        setState('');
        if (!overVideo()) hide(1200);
      }, ms);
    }
    if (current) place();
  }

  // ---- the menu

  let menu = null; // {video, state: 'reading'|'ready'|'error'|'started', answer, item, busy, note}

  function toggleMenu() {
    if (menu) closeMenu(true);
    else openMenu();
  }

  function openMenu() {
    if (!current || !ui) return;
    if (!alive()) {
      ui.host.remove();
      return;
    }
    const v = current;
    menu = { video: v, state: 'reading', answer: null, item: 0, busy: '', note: '' };
    pinned = true;
    ui.box.classList.add('open');
    ui.go.setAttribute('aria-expanded', 'true');
    ui.panel.classList.add('on');
    render();
    placePanel();
    load(false);
  }

  function load(again) {
    const m = menu;
    if (!m) return;
    m.state = 'reading';
    render();
    qualities(m.video, again).then((r) => {
      if (menu !== m) return;
      m.answer = r;
      m.state = r && r.ok ? 'ready' : 'error';
      if (m.state === 'ready') {
        const it = (r.items || [])[m.item];
        const kinds = new Set(((it && it.options) || []).map((o) => o.kind));
        if (!kinds.has(prefs.kind) && kinds.size) prefs.kind = kinds.has('video') ? 'video' : 'audio';
      }
      render();
      placePanel();
    });
  }

  function closeMenu(focusButton) {
    if (!menu || !ui) return;
    menu = null;
    ui.panel.classList.remove('on');
    ui.box.classList.remove('open');
    ui.go.setAttribute('aria-expanded', 'false');
    pinned = ui.box.classList.contains('busy');
    if (focusButton) try { ui.go.focus({ preventScroll: true }); } catch (e) { /* gone */ }
    if (!overVideo()) hide(700);
  }

  function placePanel() {
    if (!menu || !ui) return;
    const b = ui.box.getBoundingClientRect();
    const p = ui.panel;
    const pw = p.offsetWidth || 292;
    const vh = innerHeight, vw = innerWidth, m = 8;
    const room = Math.max(160, Math.max(vh - b.bottom, b.top) - 2 * m);
    p.style.maxHeight = Math.min(460, room) + 'px';
    const ph = p.offsetHeight || 200;
    let top = b.bottom + 6;
    if (top + ph > vh - m && b.top - 6 - ph >= m) top = b.top - 6 - ph;
    top = Math.max(m, Math.min(top, vh - ph - m));
    let left = lang === 'ar' ? b.right - pw : b.left;
    left = Math.max(m, Math.min(left, vw - pw - m));
    p.style.left = Math.round(left) + 'px';
    p.style.top = Math.round(top) + 'px';
  }

  function render() {
    if (!menu || !ui) return;
    const w = W();
    const m = menu;
    const p = ui.panel;
    p.textContent = '';
    const a = m.answer && m.answer.ok ? m.answer : null;
    const it = a ? (a.items || [])[m.item] : null;
    // The title, and the menu's close button.
    const hd = el('div', 'hd');
    hd.append(el('div', 'ti', (it && it.title) || w.menu));
    const cl = el('button', 'cl', '✕');
    cl.type = 'button';
    cl.title = w.close;
    cl.setAttribute('aria-label', w.close);
    cl.addEventListener('click', () => closeMenu(true));
    hd.append(cl);
    p.append(hd);
    if (m.state === 'reading') {
      const msg = el('div', 'msg');
      msg.append(el('span', 'spin'), el('span', '', w.reading));
      p.append(msg);
      p.append(footer(false));
      return;
    }
    if (m.state === 'started') {
      p.append(el('div', 'msg ok', '✓ ' + (m.note || w.started)));
      return;
    }
    if (m.state === 'error' || !it) {
      const r = m.answer || {};
      const msg = el('div', 'msg err');
      if (r.error === 'no_host' || r.error === 'forbidden') msg.append(el('b', '', w.noApp));
      else if (r.title || r.message) {
        if (r.title) msg.append(el('b', '', r.title));
        if (r.message) msg.append(el('span', '', r.message));
      } else msg.append(el('b', '', a ? w.noChoices : w.failed));
      p.append(msg);
      p.append(footer(r.error !== 'no_host' && r.error !== 'forbidden'));
      return;
    }
    // Several videos in one post.
    if (a.items.length > 1) {
      const parts = el('div', 'parts');
      a.items.forEach((x, i) => {
        const btn = el('button', '', w.part.replace('{n}', String(i + 1)));
        btn.type = 'button';
        btn.setAttribute('aria-pressed', String(i === m.item));
        btn.addEventListener('click', () => { m.item = i; render(); placePanel(); });
        parts.append(btn);
      });
      p.append(parts);
    }
    const opts = it.options || [];
    const kinds = ['video', 'audio'].filter((k) => opts.some((o) => o.kind === k));
    let kind = kinds.includes(prefs.kind) ? prefs.kind : kinds[0];
    if (kinds.length > 1) {
      const ks = el('div', 'kinds');
      for (const k of kinds) {
        const btn = el('button', '', k === 'video' ? w.video : w.audio);
        btn.type = 'button';
        btn.setAttribute('aria-pressed', String(k === kind));
        btn.addEventListener('click', () => {
          prefs.kind = k;
          savePrefs();
          render();
          placePanel();
        });
        ks.append(btn);
      }
      p.append(ks);
    }
    const list = el('div', 'list');
    for (const o of opts.filter((x) => x.kind === kind)) {
      const row = el('button', 'row' + (m.busy === o.key ? ' busy' : ''));
      row.type = 'button';
      row.append(el('span', 'l', labelOf(o)));
      if (o.tag && o.tag !== labelOf(o)) row.append(el('span', 'g', o.tag));
      if (o.recommended) row.append(el('span', 'r', w.recommended));
      if (m.busy === o.key) row.append(el('span', 'spin'));
      else row.append(el('span', 's', sizeText(o.sizeBytes)));
      row.addEventListener('click', () => get(o));
      list.append(row);
    }
    p.append(list);
    if (m.note) {
      const msg = el('div', 'msg err');
      msg.append(el('b', '', m.note));
      p.append(msg);
    }
    // Without music.
    const optsRow = el('div', 'opts');
    const sw = el('button', 'sw');
    sw.type = 'button';
    sw.setAttribute('role', 'switch');
    sw.setAttribute('aria-checked', String(!!prefs.noMusic));
    sw.title = a.sepReady ? w.noMusicTip : w.noMusicTip + '. ' + w.noMusicFirst;
    sw.append(el('span', 'k'), el('span', '', w.noMusic));
    sw.addEventListener('click', () => {
      prefs.noMusic = !prefs.noMusic;
      savePrefs();
      render();
    });
    optsRow.append(sw);
    p.append(optsRow);
    p.append(footer(false));
  }

  function footer(withRetry) {
    const w = W();
    const ft = el('div', 'ft');
    const open = el('button', '', w.openApp);
    open.type = 'button';
    open.addEventListener('click', () => openInApp());
    ft.append(open);
    // A YouTube video opened from a list (a course): all of it in HELINK's window.
    const list = youtubeList();
    if (list) {
      const all = el('button', '', w.wholeList);
      all.type = 'button';
      all.addEventListener('click', () => openList(list));
      ft.append(all);
    }
    if (withRetry) {
      const rt = el('button', 'rt', w.retry);
      rt.type = 'button';
      rt.addEventListener('click', () => load(true));
      ft.append(rt);
    }
    return ft;
  }

  async function get(o) {
    const m = menu;
    if (!m || !m.answer || !m.answer.ok || m.busy) return;
    const w = W();
    m.busy = o.key;
    m.note = '';
    render();
    const r = await send({ type: 'get', id: m.answer.id, item: m.item, key: o.key, noMusic: !!prefs.noMusic });
    if (menu !== m) return;
    m.busy = '';
    if (r && r.ok) {
      m.state = 'started';
      m.note = r.existing ? w.already : w.started;
      render();
      placePanel();
      setState('ok', 2500);
      setTimeout(() => { if (menu === m) closeMenu(false); }, 1600);
      return;
    }
    if (r && r.error === 'expired') { // HELINK restarted, or the menu stayed open long
      asked.clear();
      load(true);
      return;
    }
    m.note = (r && (r.title || r.message)) || (r && (r.error === 'no_host' || r.error === 'forbidden') ? w.noApp : w.failed);
    render();
    placePanel();
  }

  // youtubeList: the list this YouTube page plays from ("&list=…"), as the link to all of it; ""
  // for none, or for the lists YouTube makes itself (mixes, Watch later, Liked videos).
  function youtubeList() {
    if (!/(^|\.)youtube\.com$/.test(site)) return '';
    let id = '';
    try { id = new URL(location.href).searchParams.get('list') || ''; } catch (e) { return ''; }
    if (!/^[A-Za-z0-9_-]{2,80}$/.test(id) || /^(RD|UL)/i.test(id) || /^(WL|LL|LM)$/i.test(id)) return '';
    return 'https://www.youtube.com/playlist?list=' + id;
  }

  async function openList(url) {
    closeMenu(false);
    if (!ui || ui.box.classList.contains('busy')) return;
    if (!alive()) {
      ui.host.remove();
      return;
    }
    setState('busy');
    const r = await send({ type: 'list', url });
    if (r && r.ok) setState('ok', 2500);
    else setState(r && (r.error === 'no_host' || r.error === 'forbidden') ? 'noapp' : 'bad', 4500);
  }

  async function openInApp() {
    const v = menu ? menu.video : current;
    closeMenu(false);
    if (!v || !ui || ui.box.classList.contains('busy')) return;
    if (!alive()) {
      ui.host.remove();
      return;
    }
    current = v;
    setState('busy');
    const r = await send(videoMessage(v, 'video'));
    if (r && r.ok) setState('ok', 2500);
    else setState(r && (r.error === 'no_host' || r.error === 'forbidden') ? 'noapp' : 'bad', 4500);
  }

  // -------------------------------------------------------------------------
  // Following the mouse, the players and the page
  // -------------------------------------------------------------------------

  let mx = -1, my = -1, inside = false, frame = 0;

  function overVideo() {
    return inside && current && videoAt(mx, my) === current;
  }

  /* Which video the mouse is on, again: after it moves, after a scroll (the feed moves under
     a still mouse), when videos change, and every moment meanwhile. */
  function check() {
    frame = 0;
    if (stopped || (!ui && !videos.size)) return;
    if (ui && !alive()) { // the extension was updated: this copy goes
      ui.host.remove();
      ui = null;
      return;
    }
    if (menu) {
      if (!menu.video.isConnected || !boxOf(menu.video)) closeMenu(false);
      else {
        place(parseFloat(ui.box.style.left), parseFloat(ui.box.style.top), true);
        placePanel();
      }
      return;
    }
    if (pinned) {
      if (current && ui) place(parseFloat(ui.box.style.left), parseFloat(ui.box.style.top), ui.box.classList.contains('drag'));
      return;
    }
    const v = inside ? videoAt(mx, my) : null;
    if (v) show(v);
    else if (ui && ui.box.classList.contains('on') && !hideTimer) hide(700);
  }
  const soon = () => { if (!frame) frame = requestAnimationFrame(check); };

  document.addEventListener('mousemove', (e) => {
    inside = true;
    if (ui && e.composedPath && e.composedPath()[0] === ui.host) {
      clearTimeout(hideTimer);
      hideTimer = 0;
      return;
    }
    mx = e.clientX;
    my = e.clientY;
    soon();
  }, { capture: true, passive: true });
  document.addEventListener('pointerdown', (e) => {
    if (!menu || !ui) return;
    const path = e.composedPath ? e.composedPath() : [];
    if (!path.includes(ui.host)) closeMenu(false);
  }, true);
  document.addEventListener('keydown', (e) => {
    if (menu && e.key === 'Escape') {
      closeMenu(true);
      e.stopPropagation();
    }
  }, true);

  // The files the players play, for the toolbar window's list (the browser may have
  // them in memory already, so no request shows them).
  const told = new Set();
  const tell = (e) => {
    const t = e.target;
    if (!t || (t.tagName !== 'VIDEO' && t.tagName !== 'AUDIO')) return;
    const src = srcOf(t);
    if (!src || told.has(src) || !alive()) return;
    told.add(src);
    send({ type: 'seen', url: src, kind: t.tagName === 'AUDIO' ? 'audio' : 'video' });
  };
  document.addEventListener('loadedmetadata', tell, true);

  document.addEventListener('play', (e) => {
    tell(e);
    const v = e.target;
    if (!v || v.tagName !== 'VIDEO') return;
    videos.add(v);
    if (inside && mx >= 0) {
      soon(); // a new video under a mouse that did not move (scrolling a feed)
      return;
    }
    // The mouse elsewhere: shown for a moment.
    if (boxOf(v) && visible(v) && !menu && (!ui || !ui.box.classList.contains('on'))) show(v, 3500);
  }, true);
  for (const ev of ['playing', 'loadeddata', 'emptied']) document.addEventListener(ev, (e) => {
    if (e.target && e.target.tagName === 'VIDEO') {
      videos.add(e.target);
      soon();
    }
  }, true);

  let scrollTimer = 0;
  const follow = () => {
    if (menu || pinned) {
      soon();
      return;
    }
    if (ui && current && ui.box.classList.contains('on')) {
      if (!boxOf(current)) hide(0);
      else if (!ui.box.classList.contains('drag')) place();
    }
    // When the scrolling stops, the video under the mouse may be another one.
    clearTimeout(scrollTimer);
    scrollTimer = setTimeout(soon, 120);
  };
  addEventListener('scroll', () => requestAnimationFrame(follow), { capture: true, passive: true });
  addEventListener('resize', () => requestAnimationFrame(follow), { passive: true });
  document.addEventListener('fullscreenchange', () => {
    if (!ui) return;
    hostParent().appendChild(ui.host);
    requestAnimationFrame(follow);
  });

  // A page that changes without loading (TikTok, YouTube…): the menu belongs to the old one.
  let lastUrl = location.href;
  function start() {
    document.documentElement.addEventListener('mouseleave', () => {
      inside = false;
      if (!menu) hide(700);
    });
    lightScan();
    let scanTimer = 0;
    const mo = new MutationObserver(() => {
      if (!scanTimer) scanTimer = setTimeout(() => { scanTimer = 0; lightScan(); soon(); }, 300);
    });
    try {
      mo.observe(document.documentElement, { childList: true, subtree: true });
    } catch (e) { /* no document yet */ }
    timers.push(setInterval(() => {
      lightScan();
      if (location.href !== lastUrl) {
        lastUrl = location.href;
        if (menu) closeMenu(false);
      }
      if (inside && document.visibilityState === 'visible') soon();
    }, 600));
    timers.push(setInterval(() => { if (document.documentElement) deepScan(document.documentElement, { left: 30000 }); }, 6000));
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
  else start();
})();
