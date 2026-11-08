// Preview-only stand-in for the Windows side (never shipped in the app): the page with a
// small fake engine, for screenshots and browser tests.
// Usage: preview.html?demo=<name>&lang=en&theme=dark&freeze=1&site=youtube|tiktok|x|instagram
//   demo: list (default) | empty | details | menu | add | add-checking | add-error | file | file-browser |
//         file-queue | video | video-loading | video-error | settings (&tab=general|downloads|browser) |
//         browsers (&seen=-1|seconds) | about | delete | close | setup | setup-error |
//         compact-file | compact-video | compact-checking |
//         mini (downloads chosen in the browser's menu, near the clock) | mini-done | mini-error |
//         nomusic (removing the music: the list) | nomusic-video (the switch on) | nomusic-menu | nomusic-details |
//         nomusic-pick (files chosen on the computer) | nomusic-setup (the remover downloaded the first time) |
//         playlist (a course: all its videos at once) | playlist-again (the same course, its first lessons
//         downloaded already) | video-inlist (a video opened from a list) |
//         update (a new version: its window) | update-dl (downloading it) | update-error | about-update (About, with an update)
//   freeze=1: the downloads do not move.
window.__nzMock = function (emit) {
  const q = new URLSearchParams(location.search);
  const demo = q.get('demo') || 'list';
  const freeze = q.get('freeze') === '1';
  const siteParam = q.get('site') || 'youtube';
  const data = window.__nzPreviewData || {};
  let lang = q.get('lang') === 'en' ? 'en' : 'ar';
  const t2 = (ar, en) => (lang === 'en' ? en : ar);
  const later = (ms, f) => setTimeout(f, ms);
  const clone = (x) => JSON.parse(JSON.stringify(x));
  const posts = () => (lang === 'en' && data.postsEn ? data.postsEn : data.posts || {});
  const post = (site) => posts()[site] || posts().youtube;
  const known = { youtube: 1, tiktok: 1, x: 1, instagram: 1, facebook: 1 };
  const siteOf = (text) =>
    /tiktok/.test(text) ? 'tiktok' : /(\/\/|\.)(x|twitter)\.com/.test(text) ? 'x' : /instagram/.test(text) ? 'instagram' :
    /facebook|fb\.watch/.test(text) ? 'facebook' : /youtu/.test(text) ? 'youtube' : '';
  const siteNames = { youtube: ['يوتيوب', 'YouTube'], tiktok: ['تيك توك', 'TikTok'], x: ['إكس', 'X'], instagram: ['إنستجرام', 'Instagram'], facebook: ['فيسبوك', 'Facebook'] };
  const siteName = (s) => (siteNames[s] ? t2(siteNames[s][0], siteNames[s][1]) : s || '');
  const hostOf = (u) => { try { return new URL(u).hostname.replace(/^www\./, ''); } catch (_) { return ''; } };

  // ---------------------------------------------------------------- words, like the engine's
  const KB = 1024, MB = 1024 * 1024, GB = 1024 * MB;
  const trim = (n, d) => String(Number(n.toFixed(d)));
  function size(b) {
    if (!(b > 0)) return '';
    if (b < MB) return trim(Math.max(1, Math.round(b / KB)), 0) + t2(' كيلوبايت', ' KB');
    if (b < 10 * MB) return trim(b / MB, 1) + t2(' ميجابايت', ' MB');
    if (b < 1000 * MB) return trim(Math.round(b / MB), 0) + t2(' ميجابايت', ' MB');
    return trim(b / GB, 1) + t2(' جيجابايت', ' GB');
  }
  function speedText(b) {
    if (!(b > 0)) return '';
    if (b < MB) return trim(Math.max(1, Math.round(b / KB)), 0) + t2(' كيلوبايت/ث', ' KB/s');
    return trim(b / MB, 1) + t2(' ميجابايت/ث', ' MB/s');
  }
  function etaText(s) {
    if (!(s >= 0)) return '';
    if (s < 10) return t2('متبقٍ بضع ثوانٍ', 'A few seconds left');
    if (s < 60) return t2('متبقٍ أقل من دقيقة', 'Less than a minute left');
    const m = Math.round(s / 60);
    if (s < 3600) return lang === 'en' ? 'About ' + m + (m === 1 ? ' minute' : ' minutes') + ' left' : 'متبقٍ نحو ' + (m === 1 ? 'دقيقة' : m === 2 ? 'دقيقتين' : m <= 10 ? m + ' دقائق' : m + ' دقيقة');
    const h = Math.round(s / 3600);
    return lang === 'en' ? 'About ' + h + (h === 1 ? ' hour' : ' hours') + ' left' : 'متبقٍ نحو ' + (h === 1 ? 'ساعة' : h === 2 ? 'ساعتين' : h <= 10 ? h + ' ساعات' : h + ' ساعة');
  }
  let saveDir = 'C:\\Users\\Ali\\Downloads\\HELINK';
  const catFolder = { video: 'Video', music: 'Music', pictures: 'Pictures', programs: 'Programs', documents: 'Documents', compressed: 'Compressed', other: 'Other' };
  const folderLabel = (dir) => {
    if (dir === 'C:\\Users\\Ali\\Downloads\\HELINK') return t2('التنزيلات ← HELINK', 'Downloads › HELINK');
    if (dir === 'D:\\Downloads') return t2('Downloads (القرص D)', 'Downloads (drive D)');
    const name = dir.split('\\').pop();
    if (Object.values(catFolder).includes(name) && dir.includes('\\')) return folderLabel(dir.slice(0, dir.lastIndexOf('\\'))) + t2(' \u200f← ', ' › ') + name;
    return name;
  };
  const settings = {
    saveDir, maxParallel: 3, conns: 8, categoryDirs: true, catch: true, dialog: true, autostart: true, tray: true, notify: true,
    buttons: true, catchTypes: null, catchMinKB: 0, skipSites: [], buttonSkip: [], introShown: q.get('intro') !== '1',
  };
  const defaultTypes = ('3gp 7z aac ace aif apk arj asf avi bin bz2 deb dmg exe flac flv gz gzip img iso jar lzh m4a m4v mkv mov mp3 mp4 ' +
    'mpe mpeg mpg msi msix msu ogg ogv pdf pkg ppt pptx qt r0 r1 ra rar rm rmvb rpm sea sit sitx tar tgz tif tiff wav webm wma wmv xz z zip').split(' ');
  const dirFor = (cat) => (settings.categoryDirs ? saveDir + '\\' + catFolder[cat] : saveDir);
  function view() {
    return {
      saveDir, saveDirLabel: folderLabel(saveDir), maxParallel: settings.maxParallel, conns: settings.conns, categoryDirs: settings.categoryDirs,
      catch: settings.catch, dialog: settings.dialog, autostart: settings.autostart, tray: settings.tray, notify: settings.notify,
      buttons: settings.buttons, catchTypes: settings.catchTypes || defaultTypes, defaultTypes, catchMinKB: settings.catchMinKB,
      skipSites: settings.skipSites, buttonSkip: settings.buttonSkip, lang, introShown: !!settings.introShown,
    };
  }
  const E = {
    network: ['تعذّر الوصول إلى {site}', 'تأكد من اتصالك بالإنترنت، ثم حاول مرة أخرى.', "Can't reach {site}", 'Check your internet connection, then try again.', true, ''],
    link_expired: ['انتهت صلاحية رابط التنزيل', 'رفض {site} إكمال التنزيل، وغالبًا انتهت صلاحية الرابط. ابدأ تنزيل الملف مرة أخرى من الموقع في المتصفح، وسيُكمل HELINK من حيث توقف.',
      'The download link expired', '{site} refused to continue the download; the link has probably expired. Start downloading the file again from the website in your browser, and HELINK will continue where it stopped.', true, ''],
    nothing_found: ['لا يوجد ما يمكن تنزيله هنا', 'لم يجد HELINK ملفًا أو فيديو في هذا الرابط. تأكد من الرابط، أو افتح الصفحة في المتصفح واضغط زر HELINK الذي يظهر على الفيديو.',
      'Nothing to download here', 'HELINK found no file or video at this link. Check the link, or open the page in your browser and click the HELINK button that appears on the video.', false, 'newLink'],
    login: ['يتطلب هذا الفيديو تسجيل الدخول', 'لا يسمح إنستجرام بتنزيل هذا الفيديو دون حساب. يُنزّل التطبيق الفيديوهات التي يستطيع أي شخص مشاهدتها دون تسجيل الدخول.',
      'This video requires signing in', "Instagram doesn't allow downloading this video without an account. The app downloads videos that anyone can watch without signing in.", false, 'newLink'],
    setup_failed: ['لم يكتمل التجهيز', 'غالبًا انقطع الإنترنت أثناء تنزيل أدوات التطبيق. تأكد من اتصالك بالإنترنت، ثم اضغط «إعادة المحاولة».',
      "Setup didn't finish", 'The internet probably dropped while the tools were downloading. Check your connection, then click Try again.', true, ''],
    invalid_url: ['هذا الرابط غير صالح', 'هذا ليس رابط فيديو أو ملف. افتح الفيديو أو الملف وانسخ رابطه من شريط العنوان أو من زر «مشاركة»، ثم الصقه هنا.',
      "This link doesn't work", "This isn't a link to a video or a file. Open it, copy its link from the address bar or the Share button, and paste it here.", false, 'newLink'],
    product_blocked: ['لم يسمح الموقع لـ HELINK بقراءة صفحة المنتج', 'افتح صفحة المنتج في المتصفح، ثم اضغط زر HELINK في شريط الأدوات واختر «صور وفيديوهات المنتج». أو اضغط بالزر الأيمن على الصفحة واختر «تنزيل صور المنتج بـ HELINK».',
      "The site didn't let HELINK read the product page", 'Open the product page in your browser, then click the HELINK button in the toolbar and choose "Product photos and videos". Or right-click the page and choose "Download the product\'s photos with HELINK".', false, 'useBrowser'],
    clip_range: ['الجزء قصير جدًا', 'اجعل الجزء ثانية واحدة على الأقل: حرّك بدايته أو نهايته على شريط الوقت.', 'The part is too short', 'Make the part at least one second long: move its start or end on the timeline.', false, ''],
    sep_no_audio: ['لا يوجد صوت في هذا الملف', 'هذا الملف بلا صوت، لذلك لا توجد فيه موسيقى لإزالتها.', 'This file has no sound', "This file has no sound, so there's no music to remove.", false, ''],
    file_missing: ['الملف غير موجود', 'لم يعد الملف الأصلي في مكانه؛ ربما نُقل أو حُذف، أو فُصلت ذاكرة USB التي عليها. أعده إلى مكانه ثم حاول مرة أخرى، أو أضفه من مكانه الجديد.',
      'File not found', "The original file is no longer where it was: it may have been moved or deleted, or it's on a USB drive that was removed. Put it back and try again, or add it from its new place.", true, ''],
  };
  function err(code, site, details) {
    const e = E[code] || E.network;
    const s = '\u2068' + (siteName(site) || site || t2('الموقع', 'the site')) + '\u2069';
    return { code, title: t2(e[0], e[2]).replace('{site}', s), message: t2(e[1], e[3]).replace('{site}', s), retry: e[4], action: e[5] || undefined, details: details || '' };
  }

  // ---------------------------------------------------------------- the downloads
  const tasks = new Map();
  let rev = 0, idn = 0;
  const now = Date.now();
  const ext = (n) => { const i = n.lastIndexOf('.'); return i > 0 ? n.slice(i + 1).toUpperCase() : ''; };
  const baseOf = (n) => { const i = n.lastIndexOf('.'); return i > 0 ? n.slice(0, i) : n; };
  const noMusicSuffix = () => t2(' (بدون موسيقى)', ' (no music)');
  // The music remover: set up once (about 64 MB), one file at a time.
  let sepReady = demo !== 'nomusic-setup';
  function json(t, withThumb) {
    const running = t.state === 'running';
    const isFile = t.type === 'file';
    const total = t.total || 0;
    const done = Math.round((t.percent / 100) * total);
    const isSep = t.type === 'sep';
    const sepStage = t.state === 'running' && /^sep|^separate$|^merge$/.test(t.stage || '') && (isSep || t.noMusic);
    const c = {
      id: t.id, type: t.type, title: t.title, channel: t.channel || '', durationText: t.durationText || '', rev: t.rev,
      url: t.url || '', site: t.site || '', siteName: isSep ? t2('الموقع', 'the site') : isFile ? hostOf(t.url) : siteName(t.site), kind: t.kind, key: t.key || '',
      label: isFile ? ext(t.fileName) : isSep ? ext(t.srcName) : t.label, sizeText: size(total), state: t.state, stage: t.stage || '', percent: t.percent,
      doneText: done > 0 && !sepStage && !isSep ? size(done) : '', totalText: sepStage || isSep ? '' : size(total), speedText: '', etaText: '', partsCount: t.partsCount || 0,
      partIndex: t.partIndex || 0, createdAt: t.createdAt, category: t.category, page: t.page || '', saveDir: t.saveDir,
      folderLabel: folderLabel(t.saveDir), size: total, done, total, noMusic: !!t.noMusic,
    };
    if (t.list) c.list = t.list;
    if (t.clip) c.clip = t.clip;
    if (isSep) {
      c.srcPath = t.srcDir + '\\' + t.srcName;
      c.srcName = t.srcName;
      c.srcFolder = t.srcDir;
      c.srcFolderLabel = folderLabel(t.srcDir);
    }
    if (t.stage === 'sepmodel' && t.state === 'running') {
      c.doneText = size(t.modelDone || 0);
      c.totalText = size(64 * MB);
    }
    if (isFile) {
      c.fileName = t.fileName;
      c.resumable = t.resumable;
      c.referer = t.page || '';
      if (!total) { c.sizeText = ''; c.totalText = ''; c.doneText = t.bytes ? size(t.bytes) : ''; c.done = t.bytes || 0; }
      if (running) {
        c.conns = t.conns;
        if (t.segs) c.segments = t.segs.map((s) => [s[0], s[1], s[2]]);
      }
    }
    if (running) {
      c.speed = t.speed;
      c.speedText = speedText(t.speed);
      c.eta = total && t.speed ? ((total - done) / t.speed) : -1;
      if (sepStage) c.eta = t.stage === 'separate' && t.percent > 1 ? t.sepEta : -1;
      c.etaText = c.eta >= 0 ? etaText(c.eta) : '';
    }
    if (t.state === 'error' && t.errorCode) c.error = err(t.errorCode, isFile ? hostOf(t.url) : t.site, t.errorDetails);
    if (t.state === 'done') {
      const nm = isSep ? baseOf(t.srcName) + noMusicSuffix() + '.' + t.srcName.split('.').pop() :
        t.title + (t.kind === 'audio' ? '' : ' - ' + t.label) + (t.noMusic ? noMusicSuffix() : '') + (t.kind === 'audio' ? '.mp3' : '.mp4');
      const path = t.saveDir + '\\' + (isFile ? t.fileName : nm);
      c.result = { path, name: path.split('\\').pop(), folder: t.saveDir, folderLabel: folderLabel(t.saveDir), sizeText: size(total), kind: t.kind };
      c.finishedAt = t.finishedAt;
    }
    if (t.stopping) c.stopping = t.stopping;
    if (withThumb && t.thumb) c.thumb = t.thumb;
    return c;
  }
  function pub(t, op, withThumb) {
    t.rev = ++rev;
    emit('task', { op: op || 'update', task: json(t, withThumb) });
  }
  const list = () => [...tasks.values()].sort((a, b) => b.createdAt - a.createdAt).map((t) => json(t, true));
  const inSepLane = (t) => t.state === 'running' && (t.type === 'sep' || t.sepPhase);
  function runningCount() { let n = 0; for (const t of tasks.values()) if (t.state === 'running' && !inSepLane(t)) n++; return n; }
  function nextQueued(sep) {
    let next = null;
    for (const t of tasks.values()) if (t.state === 'queued' && (t.type === 'sep') === sep && (!next || t.queuedAt < next.queuedAt)) next = t;
    return next;
  }
  function begin(next) {
    next.state = 'running';
    next.stage = next.type === 'file' ? 'download' : 'preparing';
    next.sepPhase = false;
    pub(next);
    run(next);
  }
  function schedule() {
    while (runningCount() < settings.maxParallel) {
      const next = nextQueued(false);
      if (!next) break;
      begin(next);
    }
    if (![...tasks.values()].some(inSepLane)) { const n = nextQueued(true); if (n) begin(n); }
  }
  // Removing the music: waits its turn, sets the remover up the first time, then removes it.
  let sepBusy = null;
  function separate(t) {
    t.sepPhase = true;
    t.speed = 0;
    schedule(); // a download's place is free again
    const go = () => {
      if (t.state !== 'running' || t.stopping) return;
      if (sepBusy && sepBusy !== t) { t.stage = 'sepwait'; t.percent = 0; pub(t); later(500, go); return; }
      sepBusy = t;
      if (!sepReady) {
        t.stage = 'sepmodel';
        t.modelDone = t.modelDone || 0;
        t.percent = (t.modelDone / (64 * MB)) * 100;
        pub(t);
        if (freeze) return;
        t.timer = setInterval(() => {
          if (t.state !== 'running' || t.stopping) return;
          t.modelDone = Math.min(64 * MB, t.modelDone + 4.9 * MB);
          t.percent = (t.modelDone / (64 * MB)) * 100;
          t.speed = 5.2 * MB;
          if (t.modelDone >= 64 * MB) { clearInterval(t.timer); sepReady = true; t.speed = 0; emit('sep', { ready: true }); later(200, go); }
          pub(t);
        }, 400);
        return;
      }
      t.stage = 'separate';
      t.speed = 0;
      t.percent = t.sepStart || 0;
      t.sepEta = 95;
      pub(t);
      if (freeze) return;
      t.timer = setInterval(() => {
        if (t.state !== 'running' || t.stopping) return;
        t.percent = Math.min(100, t.percent + 6 + Math.random() * 4);
        t.sepEta = Math.max(1, (100 - t.percent) * 0.9);
        if (t.percent >= 100) {
          clearInterval(t.timer);
          t.stage = t.kind === 'video' ? 'merge' : 'save';
          pub(t);
          later(700, () => {
            if (t.state !== 'running') return;
            if (sepBusy === t) sepBusy = null;
            t.state = 'done'; t.stage = ''; t.sepPhase = false; t.finishedAt = Date.now();
            pub(t);
            schedule();
          });
          return;
        }
        pub(t);
      }, 450);
    };
    go();
  }
  function makeSegs(t) {
    const n = t.conns || 8, per = Math.floor(t.total / n);
    t.segs = [];
    for (let i = 0; i < n; i++) {
      const s = i * per, e = i === n - 1 ? t.total : (i + 1) * per;
      t.segs.push([s, e, Math.round((e - s) * Math.min(1, Math.max(0, t.percent / 100 + (Math.random() - 0.5) * 0.25)))]);
    }
  }
  function syncSegs(t) {
    if (!t.segs) return;
    const target = (t.percent / 100) * t.total;
    let have = t.segs.reduce((a, s) => a + s[2], 0);
    for (const s of t.segs) {
      if (have >= target) break;
      const room = s[1] - s[0] - s[2];
      const add = Math.min(room, (target - have) / 2 + Math.random() * (target - have) / 2);
      s[2] += add;
      have += add;
    }
  }
  function run(t) {
    clearInterval(t.timer);
    if (t.type === 'file' && t.total && !t.segs) makeSegs(t);
    if (t.type === 'sep' || (t.noMusic && t.downloaded)) { separate(t); return; }
    if (freeze) return;
    t.timer = setInterval(() => {
      if (t.state !== 'running' || t.stopping) return;
      if (t.type === 'file') {
        t.speed = (t.baseSpeed || 3 * MB) * (0.8 + Math.random() * 0.4);
        if (!t.total) { t.bytes = (t.bytes || 0) + t.speed * 0.5; pub(t); return; }
        t.percent = Math.min(100, t.percent + (t.speed * 0.5 * 100) / t.total);
        syncSegs(t);
      } else {
        t.stage = t.kind === 'audio' ? 'audio' : t.percent < 88 ? 'video' : 'audio';
        t.partsCount = t.kind === 'audio' ? 1 : 2;
        t.partIndex = t.stage === 'audio' && t.kind !== 'audio' ? 2 : 1;
        t.percent = Math.min(100, t.percent + 2 + Math.random() * 2);
        t.speed = (1.5 + Math.random()) * MB;
      }
      if (t.percent >= 100 && t.noMusic) {
        clearInterval(t.timer);
        t.downloaded = true;
        t.percent = 0;
        separate(t);
        return;
      }
      if (t.percent >= 100) {
        clearInterval(t.timer);
        t.stage = t.type === 'file' ? 'save' : t.kind === 'audio' ? 'convert' : 'merge';
        t.speed = 0;
        pub(t);
        later(800, () => {
          if (t.state !== 'running') return;
          t.state = 'done';
          t.stage = '';
          t.finishedAt = Date.now();
          pub(t);
          schedule();
        });
        return;
      }
      pub(t);
    }, 500);
  }
  function addFile(name, cat, total, extra) {
    const t = Object.assign({
      id: 'f' + (++idn), type: 'file', kind: 'file', title: name, fileName: name, category: cat, total, percent: 0, state: 'queued',
      url: 'https://downloads.example.com/files/' + encodeURIComponent(name), saveDir: dirFor(cat), resumable: true, conns: settings.conns,
      createdAt: now - (idn * 7 + 3) * 60000, queuedAt: now, speed: 0,
    }, extra || {});
    tasks.set(t.id, t);
    return t;
  }
  function addMedia(p, i, key, extra) {
    const it = p.items[i];
    const opt = it.options.find((o) => o.key === key) || it.options[0];
    const t = Object.assign({
      id: 'd' + (++idn), type: '', url: { youtube: 'https://youtu.be/dQw4w9WgXcQ', tiktok: 'https://www.tiktok.com/@sara/video/7300000000000000000',
        x: 'https://x.com/egfootball/status/1840000000000000000', instagram: 'https://www.instagram.com/reel/C1abcdEFGhi/' }[p.site],
      site: p.site, title: it.meta.title || t2('فيديو', 'Video'), channel: it.meta.channel || '', durationText: it.meta.durationText || '',
      kind: opt.kind, key: opt.key, label: opt.label, total: opt.sizeBytes || 80 * MB, percent: 0, state: 'queued',
      category: opt.kind === 'audio' ? 'music' : 'video', saveDir: dirFor(opt.kind === 'audio' ? 'music' : 'video'),
      createdAt: now - (idn * 7 + 3) * 60000, queuedAt: now, thumb: it.meta.thumbnail || '', speed: 0,
    }, extra || {});
    tasks.set(t.id, t);
    return t;
  }
  function addSep(name, dir, kind, total, extra) {
    const t = Object.assign({
      id: 's' + (++idn), type: 'sep', kind, title: name, srcName: name, srcDir: dir, category: kind === 'video' ? 'video' : 'music',
      total, percent: 0, state: 'queued', saveDir: dir, noMusic: true, createdAt: now - (idn * 7 + 3) * 60000, queuedAt: now + idn, speed: 0,
    }, extra || {});
    tasks.set(t.id, t);
    return t;
  }
  // Files a person might choose on the computer.
  const desk = 'C:\\Users\\Ali\\Desktop\\' + t2('دروس', 'Lessons');
  const music = 'C:\\Users\\Ali\\Music';
  const sampleFiles = () => [
    [t2('درس التفسير - الحلقة 3.mp4', 'Tafsir lesson - part 3.mp4'), desk, 'video', 214 * MB],
    [t2('محاضرة الأخلاق.mp3', 'Lecture on manners.mp3'), desk, 'audio', 38 * MB],
    [t2('بودكاست الصباح.m4a', 'Morning podcast.m4a'), music, 'audio', 52 * MB],
  ];
  function fillNoMusic() {
    const yt = post('youtube'), tt = post('tiktok');
    const f = sampleFiles();
    addSep(f[0][0], f[0][1], 'video', f[0][3], { state: 'running', stage: 'separate', percent: 37, sepStart: 37, sepEta: 75 });
    addSep(f[1][0], f[1][1], 'audio', f[1][3], { state: 'queued' });
    addMedia(yt, 0, 'v720', { state: 'running', stage: 'video', percent: 58.4, speed: 2.1 * MB, partsCount: 2, partIndex: 1, noMusic: true });
    addMedia(tt, 0, tt.items[0].options.find((o) => o.kind === 'audio').key, { state: 'done', percent: 100, noMusic: true, finishedAt: now - 30 * 60000 });
    addSep(f[2][0], f[2][1], 'audio', f[2][3], { state: 'done', percent: 100, finishedAt: now - 50 * 60000 });
    addSep(t2('مقطع بلا صوت.mp4', 'Silent clip.mp4'), desk, 'video', 9 * MB, { state: 'error', percent: 0, errorCode: 'sep_no_audio' });
    addMedia(yt, 0, 'v1080', { state: 'done', percent: 100, finishedAt: now - 3 * 86400000, createdAt: now - 3 * 86400000 - 120000 });
    addFile(t2('تقرير المبيعات - الربع الثالث.pdf', 'Sales report Q3.pdf'), 'documents', 2.4 * MB, { state: 'done', percent: 100, finishedAt: now - 40 * 60000 });
    let k = 0;
    for (const t of tasks.values()) {
      if (!t.createdAt || t.createdAt > now) t.createdAt = now - (++k) * 5 * 60000;
      t.rev = ++rev;
    }
    // Started once the page is ready for events.
    for (const t of tasks.values()) if (t.state === 'running') { if (t.type === 'sep') { sepBusy = t; t.sepPhase = true; } later(0, () => run(t)); }
  }
  function fillList() {
    const yt = post('youtube'), tt = post('tiktok'), x = post('x'), ig = post('instagram');
    addFile(t2('برنامج الحسابات - إصدار 2026.exe', 'Accounting Suite 2026.exe'), 'programs', 48.2 * MB, { state: 'running', percent: 63.4, baseSpeed: 4.2 * MB, conns: 8 });
    addMedia(yt, 0, 'v720', { state: 'running', stage: 'video', percent: 45.2, speed: 2.4 * MB, partsCount: 2, partIndex: 1 });
    addFile('ubuntu-24.04.1-desktop-amd64.iso', 'compressed', 5.8 * GB, { state: 'running', percent: 12.6, baseSpeed: 9.6 * MB, conns: 16 });
    addMedia(x, 0, x.items[0].options[0].key, { state: 'queued' });
    addFile(t2('ملفات المشروع النهائية.rar', 'Project files (final).rar'), 'compressed', 764 * MB, { state: 'paused', percent: 41.3 });
    addFile(t2('عرض تقديمي للعملاء.pptx', 'Client presentation.pptx'), 'documents', 22.5 * MB, { state: 'paused', percent: 30.2, resumable: false });
    addFile('driver_setup_v5.zip', 'compressed', 312 * MB, { state: 'error', percent: 57.9, errorCode: 'link_expired', errorDetails: 'HTTP 403 Forbidden from cdn.example.com' });
    addMedia(ig, 1, ig.items[1].options[0].key, { state: 'error', percent: 23, errorCode: 'network', errorDetails: 'ERROR: [download] Got error: The read operation timed out' });
    addFile(t2('تقرير المبيعات - الربع الثالث.pdf', 'Sales report Q3.pdf'), 'documents', 2.4 * MB, { state: 'done', percent: 100, finishedAt: now - 40 * 60000 });
    addMedia(tt, 0, tt.items[0].options.find((o) => o.kind === 'audio').key, { state: 'done', percent: 100, finishedAt: now - 2 * 3600000 });
    addFile('VLC-media-player-3.0.21-win64.msi', 'programs', 41 * MB, { state: 'done', percent: 100, finishedAt: now - 26 * 3600000, createdAt: now - 26 * 3600000 - 60000 });
    addFile(t2('صور رحلة الإسكندرية.jpg', 'Alexandria trip.jpg'), 'pictures', 3.1 * MB, { state: 'done', percent: 100, finishedAt: now - 30 * 3600000, createdAt: now - 30 * 3600000 - 20000 });
    addMedia(yt, 0, 'v1080', { state: 'done', percent: 100, finishedAt: now - 3 * 86400000, createdAt: now - 3 * 86400000 - 120000 });
    let k = 0;
    for (const t of tasks.values()) {
      if (t.state === 'running' && t.type === 'file') t.speed = t.baseSpeed;
      if (!t.createdAt || t.createdAt > now) t.createdAt = now - (++k) * 5 * 60000;
      t.rev = ++rev;
      if (t.state === 'running') run(t);
    }
  }

  // ---------------------------------------------------------------- a course (a playlist)
  const courseTitles = () => lang === 'en'
    ? ['Welcome and what you will learn', 'Installing the tools', 'Your first project', 'Working with text', 'Colours and shapes', '[Private video]',
      'Layers and groups', 'Exporting for print', 'Exporting for social media', 'Logos: the basics', 'Practice: a shop banner', 'Mistakes to avoid',
      'Final project', 'Questions and answers']
    : ['مقدمة الكورس وما ستتعلمه', 'تثبيت البرامج المطلوبة', 'أول مشروع لك', 'التعامل مع النصوص', 'الألوان والأشكال', '[Private video]',
      'الطبقات والمجموعات', 'التصدير للطباعة', 'التصدير لمواقع التواصل', 'أساسيات تصميم الشعار', 'تطبيق عملي: بانر لمحل', 'أخطاء شائعة يجب تجنبها',
      'المشروع النهائي', 'أسئلة وأجوبة'];
  const courseDur = [312, 548, 1265, 903, 1122, 0, 987, 744, 811, 1430, 1694, 655, 2210, 1580];
  function hms(sec) { if (!sec) return ''; const h = Math.floor(sec / 3600), m = Math.floor(sec % 3600 / 60), x = sec % 60; return (h ? h + ':' + String(m).padStart(2, '0') : m) + ':' + String(x).padStart(2, '0'); }
  function courseList() {
    const thumbs = [];
    for (const site of ['youtube', 'x', 'instagram', 'tiktok']) for (const it of (post(site).items || [])) if (it.meta.thumbnail) thumbs.push(it.meta.thumbnail);
    const entries = courseTitles().map((title, i) => ({ n: i + 1, id: 'VID' + String(i).padStart(8, '0'), title, channel: t2('أكاديمية التصميم', 'Design Academy'),
      duration: courseDur[i], durationText: hms(courseDur[i]), thumbnail: thumbs.length ? thumbs[i % thumbs.length] : '', available: !/^\[/.test(title),
      // demo=playlist-again: the course pasted again, its first lessons downloaded already
      have: demo === 'playlist-again' && i < 4 ? ['video'] : undefined }));
    const total = entries.filter((e) => e.available).reduce((a, e) => a + e.duration, 0);
    const opts = [[1080, 'Full HD', t2('جودة عالية للشاشات الكبيرة', 'High quality for big screens'), 20], [720, 'HD', t2('جودة جيدة بحجم مناسب', 'Good quality, reasonable size'), 11],
      [480, '', t2('جودة متوسطة بحجم أصغر', 'Medium quality, smaller file'), 6], [360, '', t2('جودة عادية تناسب الهاتف', 'Standard quality, fine for phones'), 4]]
      .map(([q, tag, desc, mb]) => ({ key: 'v' + q, kind: 'video', label: q + 'p', tag, desc, recommended: q === 720, perMinute: mb * MB }))
      .concat([[320, t2('أعلى جودة للصوت، والحجم أكبر', 'Best sound, larger file')], [192, t2('جودة جيدة بحجم مناسب', 'Good quality, reasonable size')], [128, t2('حجم صغير يوفّر المساحة', 'Small file that saves space')]]
        .map(([k, desc]) => ({ key: 'mp3-' + k, kind: 'audio', label: k + 'k', tag: 'MP3', desc, recommended: k === 192, perMinute: k * 1000 / 8 * 60 })));
    return { id: 'PLcourse0001', title: t2('كورس تصميم الجرافيك للمبتدئين', 'Graphic design for beginners'), channel: t2('أكاديمية التصميم', 'Design Academy'),
      url: 'https://www.youtube.com/playlist?list=PLcourse0001', entries, count: entries.filter((e) => e.available).length, duration: total,
      durationText: hms(total), more: false, options: opts };
  }
  let curList = null;

  // ---------------------------------------------------------------- HELINK's own update
  const updNotes = () => lang === 'en'
    ? ['Download a whole course or playlist at once, numbered in order.', 'HELINK tells you when a new version is out, and updates itself when you say so.', 'Faster and steadier music removal.']
    : ['تنزيل كورس أو قائمة تشغيل كاملة مرة واحدة، مرقّمة بالترتيب.', 'يخبرك HELINK بالإصدارات الجديدة ويُحدِّث نفسه عندما تسمح له.', 'إزالة الموسيقى أسرع وأكثر ثباتًا.'];
  const upd = { state: /^(update|about-update)/.test(demo) ? 'available' : 'latest', done: 0, snoozed: false };
  if (demo === 'update-dl') { upd.state = 'downloading'; upd.done = 1.9 * MB; }
  if (demo === 'update-error') { upd.state = 'error'; upd.failedIn = 'install'; }
  function updView(prompt) {
    const v = { state: upd.state, current: '2.4.0', page: 'https://github.com/HELAL-LINK/HELINK-Windows', prompt: !!prompt, checkedAt: Date.now() };
    if (upd.state !== 'latest' && upd.state !== '' && upd.state !== 'checking') {
      Object.assign(v, { version: '2.4.1', notes: updNotes(), sizeText: size(3.9 * MB), snoozed: upd.snoozed });
    }
    if (upd.state === 'downloading') Object.assign(v, { done: upd.done, total: 3.9 * MB, doneText: size(upd.done), totalText: size(3.9 * MB), percent: upd.done / (3.9 * MB) * 100 });
    if (upd.state === 'error') { v.error = err('network', 'github.com', 'Get "https://raw.githubusercontent.com/...": dial tcp: lookup raw.githubusercontent.com: no such host'); v.failedIn = upd.failedIn || 'check'; }
    return v;
  }
  let updTimer = 0;

  // ---------------------------------------------------------------- links, files, videos
  let seq = 0, pendSeq = 0, current = null, fetchTimer = 0;
  const pending = new Map();
  const readOpts = new Map();
  const fileExt = /\.(zip|rar|7z|exe|msi|iso|pdf|docx?|xlsx?|pptx?|mp3|mp4|mkv|apk|tar|gz)(\?|#|$)/i;
  function nameFromURL(u) { try { return decodeURIComponent(new URL(u).pathname.split('/').pop()) || 'download'; } catch (_) { return 'download'; } }
  function catOf(n) {
    const e = n.split('.').pop().toLowerCase();
    if (/^(mp4|mkv|webm|avi|mov)$/.test(e)) return 'video';
    if (/^(mp3|m4a|wav|flac|ogg|opus)$/.test(e)) return 'music';
    if (/^(jpe?g|png|gif|webp|bmp|heic|avif)$/.test(e)) return 'pictures';
    if (/^(exe|msi|apk|dmg)$/.test(e)) return 'programs';
    if (/^(pdf|docx?|xlsx?|pptx?|txt|epub)$/.test(e)) return 'documents';
    if (/^(zip|rar|7z|iso|tar|gz)$/.test(e)) return 'compressed';
    return 'other';
  }
  function pendingEvent(p) {
    const folders = {};
    for (const c of Object.keys(catFolder)) folders[c] = { folder: dirFor(c), label: folderLabel(dirFor(c)) };
    const ev = { pid: p.pid, status: 'ready', from: p.from, name: p.name, size: p.size, sizeText: size(p.size), resumable: p.resumable,
      category: p.category, folder: dirFor(p.category), folderLabel: folderLabel(dirFor(p.category)), folders, url: p.url,
      host: hostOf(p.url), page: p.page || '', at: Date.now() };
    for (const t of tasks.values()) {
      if (t.type === 'file' && t.state !== 'done' && t.fileName.toLowerCase() === p.name.toLowerCase()) {
        ev.existing = { id: t.id, percent: t.percent, doneText: size(Math.round(t.percent / 100 * t.total)), state: t.state };
      }
    }
    return ev;
  }
  function newPending(url, from, extra) {
    const name = (extra && extra.name) || nameFromURL(url);
    const p = Object.assign({ pid: 'p' + (++pendSeq), url, from, name, size: 18.7 * MB, resumable: true, category: catOf(name) }, extra || {});
    pending.set(p.pid, p);
    return p;
  }
  function startFetch(text, s, opts) {
    const site = siteOf(text) || 'page';
    clearTimeout(fetchTimer);
    emit('info', { seq: s, status: 'loading', site: known[site] ? site : hostOf(text), siteName: known[site] ? siteName(site) : hostOf(text) });
    if (/nothing|empty/.test(text) || site === 'page' && /blog/.test(text)) {
      fetchTimer = later(900, () => emit('info', { seq: s, status: 'error', site: hostOf(text), siteName: hostOf(text), error: err('nothing_found', hostOf(text)),
        media: opts && opts.media ? opts.media : [] }));
      return;
    }
    if (/playlist\?list=/.test(text)) {
      fetchTimer = later(900, () => { curList = { seq: s, list: courseList() }; emit('info', { seq: s, status: 'list', site: 'youtube', siteName: siteName('youtube'), list: curList.list }); });
      return;
    }
    const p = post(known[site] ? site : 'youtube');
    fetchTimer = later(1100, () => {
      current = Object.assign(clone(p), { seq: s });
      const ev = Object.assign({ seq: s, status: 'done' }, clone(p));
      if (/[?&]list=PL/.test(text)) ev.listUrl = 'https://www.youtube.com/playlist?list=PLcourse0001';
      emit('info', ev);
    });
  }
  function fetch(text, opts, before) {
    const s = ++seq;
    if (opts) readOpts.set(s, opts);
    if (before) before(s);
    startFetch(text, s, opts);
    return s;
  }
  function addLink(text, from, opts) {
    text = String(text || '').trim();
    const m = text.match(/https?:\/\/\S+/i);
    if (!m) return { error: err('invalid_url') };
    const url = m[0];
    if (known[siteOf(url)]) return { kind: 'media', seq: fetch(url, opts) };
    if (from === 'link' && productSiteOf(url) && /\/dp\/|\/item\/|product-detail|\/p\/|-g-\d+/.test(url)) return readProduct(url);
    const p = newPending(url, from || 'link', opts && opts.pending);
    p.waiting = true;
    emit('file', { pid: p.pid, status: 'checking', url, from: p.from });
    later(700, () => {
      if (!pending.has(p.pid)) return;
      p.waiting = false;
      if (/bad|broken|404/.test(url)) { pending.delete(p.pid); emit('file', { pid: p.pid, status: 'error', url, from: p.from, error: err('nothing_found', hostOf(url)) }); return; }
      if (!fileExt.test(url)) {
        pending.delete(p.pid);
        fetch(url, opts, (s) => emit('file', { pid: p.pid, status: 'media', seq: s, url, from: p.from }));
        return;
      }
      if (!settings.dialog && p.from === 'browser') { const r = confirm(p.pid, { start: true }); emit('file', { pid: p.pid, status: 'started', id: r.id }); return; }
      emit('file', pendingEvent(p));
    });
    return { kind: 'file', pid: p.pid };
  }
  // ---------------------------------------------------------------- products (2.4)
  const prodSites = { amazon: ['أمازون', 'Amazon'], alibaba: ['علي بابا', 'Alibaba'], aliexpress: ['علي إكسبريس', 'AliExpress'], noon: ['نون', 'Noon'], temu: ['تيمو', 'Temu'] };
  const productSiteOf = (u) => (/amazon\./.test(u) ? 'amazon' : /aliexpress/.test(u) ? 'aliexpress' : /alibaba/.test(u) ? 'alibaba' : /noon\.com/.test(u) ? 'noon' : /temu\.com/.test(u) ? 'temu' : '');
  const products = new Map();
  let prodSeq = 0;
  function productEvent(id) {
    const p = products.get(id);
    return { id, status: 'ready', site: p.site, siteName: t2(prodSites[p.site][0], prodSites[p.site][1]), title: p.title, page: p.page,
      items: p.media.map((m, i) => ({ i, kind: m.kind, url: m.url, stream: !!m.stream })) };
  }
  function sampleProduct(site, page) {
    const media = [{ kind: 'video', url: 'mock-clip.webm' }];
    for (let i = 1; i <= 9; i++) media.push({ kind: 'image', url: 'prod-' + i + '.jpg' });
    if (site === 'amazon') media.splice(1, 0, { kind: 'video', url: 'https://m.media-amazon.com/images/S/vse-vms-transcoding-artifact/1/default.jobtemplate.hls.m3u8', stream: true });
    return { site, page, title: t2('شاحن أنكر 735 نانو II بقوة 65 واط مع كابل USB-C', 'Anker 735 Charger (Nano II 65W) with USB-C cable'), media };
  }
  function readProduct(url) {
    const id = ++prodSeq;
    const site = productSiteOf(url);
    emit('product', { id, status: 'loading', site, page: url });
    later(freeze ? 10 : 900, () => {
      if (/blocked|temu/.test(url)) { emit('product', { id, status: 'error', site, page: url, error: err('product_blocked') }); return; }
      products.set(id, sampleProduct(site, url));
      emit('product', productEvent(id));
    });
    return { kind: 'product', id };
  }
  function productFromBrowser(site) {
    const id = ++prodSeq;
    products.set(id, sampleProduct(site, 'https://www.' + site + '.com/item'));
    emit('product', productEvent(id));
    return id;
  }

  function confirm(pid, c) {
    const p = pending.get(pid);
    if (!p || p.waiting) return { error: err('network') };
    pending.delete(pid);
    if (c.continue) {
      const t = tasks.get(c.continue);
      if (t) { t.url = p.url; if (t.state === 'paused' || t.state === 'error') api.resume(t.id); return { id: t.id, existing: true }; }
    }
    const name = (c.name || p.name).trim();
    const cat = c.category || p.category;
    const t = addFile(name, cat, p.size, { state: c.start ? 'queued' : 'paused', saveDir: c.folder || dirFor(cat), resumable: p.resumable,
      url: p.url, page: p.page || '', createdAt: Date.now(), queuedAt: Date.now() });
    pub(t, 'add');
    if (c.start) schedule();
    return { id: t.id };
  }

  // ---------------------------------------------------------------- what the page calls
  const api = {
    init: async () => {
      const setup = demo.startsWith('setup');
      return {
        screen: setup ? 'setup' : 'ready', platform: 'windows', saveDir, saveDirLabel: folderLabel(saveDir), version: '2.4.0', lang,
        setupSizeText: setup ? size(146 * MB) : '', missing: setup ? ['ytdlp', 'deno', 'ffmpeg'] : [], busy: '', lastKind: q.get('kind') === 'audio' ? 'audio' : 'video',
        parallel: settings.maxParallel, restore: { info: null }, tasks: list(), settings: view(), pending: [],
        sepReady, sepSizeText: size(64 * MB), update: updView(false), updated: demo === 'updated' ? '2.4.0' : undefined,
      };
    },
    update: async () => updView(false),
    checkUpdate: async () => {
      upd.state = 'checking';
      later(700, () => { upd.state = q.get('upd') === 'none' ? 'latest' : 'available'; emit('update', updView(false)); });
      return updView(false);
    },
    updateNow: async () => {
      window.__updateNow = (window.__updateNow || 0) + 1;
      upd.state = 'downloading';
      upd.done = upd.done || 0;
      emit('update', updView(false));
      clearInterval(updTimer);
      if (freeze) return { ok: true };
      updTimer = setInterval(() => {
        if (upd.state !== 'downloading') { clearInterval(updTimer); return; }
        upd.done = Math.min(3.9 * MB, upd.done + 0.7 * MB);
        if (upd.done >= 3.9 * MB) { clearInterval(updTimer); upd.state = 'installing'; }
        emit('update', updView(false));
      }, 300);
      return { ok: true };
    },
    updateLater: async () => { window.__updateLater = (window.__updateLater || 0) + 1; upd.snoozed = true; emit('update', updView(false)); },
    cancelUpdate: async () => { window.__updateCancel = (window.__updateCancel || 0) + 1; clearInterval(updTimer); upd.state = 'available'; emit('update', updView(false)); },
    downloadList: async (s, picks, key, noMusic) => {
      window.__lastList = [s, picks, key, noMusic];
      if (!curList || curList.seq !== s) return { error: err('network') };
      const l = curList.list;
      const audio = /^mp3/.test(key);
      const opt = l.options.find((o) => o.key === key) || l.options[1];
      const cat = audio ? 'music' : 'video';
      const dir = dirFor(cat) + '\\' + l.title;
      const ids = [];
      for (const e of l.entries) {
        if (!picks.includes(e.n) || !e.available) continue;
        const t = Object.assign({
          id: 'd' + (++idn), type: '', url: 'https://www.youtube.com/watch?v=' + e.id, site: 'youtube', title: e.title, channel: e.channel,
          durationText: e.durationText, kind: audio ? 'audio' : 'video', key, label: audio ? opt.label : opt.label, total: Math.round(e.duration / 60 * opt.perMinute * 0.7),
          percent: 0, state: 'queued', category: cat, saveDir: dir, createdAt: Date.now() + ids.length, queuedAt: Date.now() + ids.length,
          thumb: e.thumbnail, speed: 0, noMusic: !!noMusic, list: l.title,
        });
        tasks.set(t.id, t);
        pub(t, 'add', true);
        ids.push(t.id);
      }
      schedule();
      return { ids, added: ids.length, skipped: 0, folder: folderLabel(dir) };
    },
    ready: async () => { if (!api.started) { api.started = true; later(60, runDemo); } },
    setLang: async (l) => { lang = l === 'en' ? 'en' : 'ar'; window.__lang = lang; },
    tasks: async () => list(),
    setup: async () => {
      if (demo.startsWith('setup')) return;
      let p = 0;
      const tm = setInterval(() => {
        p += 5;
        emit('setup', { phase: p > 96 ? 'verify' : 'download', percent: Math.min(100, p) });
        if (p >= 100) { clearInterval(tm); later(300, () => emit('setup', { phase: 'done', percent: 100 })); }
      }, 150);
    },
    paste: async () => 'https://downloads.example.com/files/HELINK-Setup-2.0.0.exe',
    addLink: async (text) => { window.__lastAdd = text; return addLink(text, 'link'); },
    confirmFile: async (pid, c) => { window.__lastConfirm = [pid, c]; return confirm(pid, c); },
    cancelFile: async (pid) => { window.__lastCancelFile = pid; pending.delete(pid); },
    pendingFiles: async () => [...pending.values()].filter((p) => !p.waiting).map(pendingEvent),
    useMedia: async (s, index) => {
      const o = readOpts.get(s);
      const m = o && o.media && o.media[index];
      if (!m) return { error: err('network') };
      if (m.kind === 'hls') return { kind: 'media', seq: fetch('https://youtu.be/x', null) };
      return addLink(m.url, 'browser', { pending: { name: m.name || nameFromURL(m.url), size: m.size || 0 } });
    },
    fetch: async (text) => fetch(text),
    readProduct: async (u) => readProduct(u),
    productDownload: async (id, picks) => {
      window.__lastProduct = [id, picks];
      const p = products.get(id);
      if (!p) return { error: err('network') };
      const dir = dirFor('pictures') + '\\' + p.title;
      let img = 0, vid = 0;
      for (const i of [...picks].sort((a, b) => a - b)) {
        const m = p.media[i];
        if (!m) continue;
        const name = m.kind === 'video' ? t2('فيديو ', 'video ') + (++vid) + '.mp4' : String(++img).padStart(2, '0') + '.jpg';
        const t = addFile(name, m.kind === 'video' ? 'video' : 'pictures', m.kind === 'video' ? 6 * MB : 420 * KB,
          { state: 'queued', saveDir: dir, resumable: true, url: m.url, page: p.page, createdAt: Date.now() + i, queuedAt: Date.now() + i });
        pub(t, 'add');
      }
      schedule();
      return { added: picks.length, folder: dir };
    },
    clipPreview: async (i) => {
      if (!current) return { kind: 'none' };
      const it = current.items[i] || current.items[0];
      if (q.get('nopreview') === '1') return { item: i, kind: 'none', duration: it.meta.duration, title: it.meta.title, thumbnail: it.meta.thumbnail };
      return { item: i, kind: 'av', video: 'mock-clip.webm', duration: it.meta.duration, title: it.meta.title, thumbnail: it.meta.thumbnail };
    },
    clipClosed: async () => { window.__clipClosed = (window.__clipClosed || 0) + 1; },
    clipDownload: async (i, key, start, end, noMusic) => {
      window.__lastClip = [i, key, start, end, noMusic];
      if (!current) return { error: err('network') };
      if (end - start < 1) return { error: err('clip_range') };
      const clk = (x) => { x = Math.floor(x); const m = Math.floor(x / 60), sec = x % 60; return m + ':' + String(sec).padStart(2, '0'); };
      const it = current.items[i] || current.items[0];
      const opt = it.options.find((o) => o.key === key) || it.options[0];
      const share = it.meta.duration ? (end - start) / it.meta.duration : 0.1;
      const t = addMedia(current, i, key, { createdAt: Date.now(), queuedAt: Date.now(), noMusic: !!noMusic, clip: clk(start) + ' – ' + clk(end),
        title: (it.meta.title || '') + ' - ' + t2('مقطع ', 'part ') + clk(start).replace(':', '.') + '-' + clk(end).replace(':', '.'),
        total: Math.max(200 * KB, Math.round((opt.sizeBytes || 80 * MB) * share)), durationText: clk(end - start) });
      pub(t, 'add', true);
      schedule();
      return { id: t.id };
    },
    cancelFetch: async () => { clearTimeout(fetchTimer); seq++; },
    download: async (i, key, noMusic) => {
      window.__lastDownload = [i, key, noMusic];
      if (!current) return { error: err('network') };
      const t = addMedia(current, i, key, { createdAt: Date.now(), queuedAt: Date.now(), noMusic: !!noMusic });
      pub(t, 'add', true);
      schedule();
      return { id: t.id };
    },
    pause: async (id) => {
      const t = tasks.get(id);
      if (!t) return;
      if (t.state === 'queued') { t.state = 'paused'; pub(t); return; }
      if (t.state !== 'running') return;
      t.stopping = 'pause';
      pub(t);
      later(300, () => { delete t.stopping; clearInterval(t.timer); t.state = 'paused'; t.stage = ''; t.speed = 0; pub(t); schedule(); });
    },
    resume: async (id) => {
      const t = tasks.get(id);
      if (!t || !(t.state === 'paused' || t.state === 'error')) return;
      t.state = 'queued';
      delete t.errorCode;
      if (t.type === 'file' && t.resumable === false) { t.percent = 0; t.segs = null; }
      t.queuedAt = Date.now();
      pub(t);
      schedule();
    },
    pauseAll: async () => { for (const t of tasks.values()) if (t.state === 'running' || t.state === 'queued') api.pause(t.id); },
    resumeAll: async () => { for (const t of tasks.values()) if (t.state === 'paused' || t.state === 'error') api.resume(t.id); },
    deleteTask: async (id) => api.remove([id], false),
    remove: async (ids, withFile) => {
      window.__lastRemove = [ids, withFile];
      for (const id of ids) {
        const t = tasks.get(id);
        if (!t) continue;
        clearInterval(t.timer);
        tasks.delete(id);
        emit('task', { op: 'remove', id });
      }
      schedule();
    },
    clearFinished: async () => { for (const t of [...tasks.values()]) if (t.state === 'done') api.remove([t.id]); },
    settings: async () => view(),
    setSettings: async (ch) => {
      window.__lastSettings = ch;
      for (const [k, v] of Object.entries(ch)) {
        if (k === 'catchTypes') settings.catchTypes = v == null ? null : v.map((e) => String(e).toLowerCase().replace(/^\./, '')).filter(Boolean).sort();
        else if (k in settings) settings[k] = v;
      }
      schedule();
      return view();
    },
    dialogDone: async () => { window.__dialogDone = (window.__dialogDone || 0) + 1; },
    showMini: async () => { window.__showMini = (window.__showMini || 0) + 1; emit('mode', { compact: true, reason: 'task' }); },
    miniFit: async (h) => { window.__miniFit = h; },
    pong: async (n) => { window.__pong = n; },
    expand: async () => { window.__expanded = true; emit('mode', { compact: false }); },
    hide: async () => {},
    exit: async () => {},
    copyText: async (t) => { window.__lastCopy = t; return true; },
    openFile: async (p) => { window.__lastOpen = p; return true; },
    showInFolder: async (p) => { window.__lastShow = p; return true; },
    openPage: async (u) => { window.__lastPage = u; return true; },
    openFolder: async (d) => { window.__openedFolder = d || saveDir; },
    pickFolder: async (id) => {
      later(200, () => {
        const changed = saveDir !== 'D:\\Downloads';
        saveDir = 'D:\\Downloads';
        emit('folder', { saveDir, saveDirLabel: folderLabel(saveDir), changed, taskId: id || undefined });
      });
    },
    pickDir: async (purpose) => { later(200, () => emit('dirPicked', { purpose, ok: true, folder: 'D:\\Downloads', label: folderLabel('D:\\Downloads') })); },
    contact: async (how) => { window.__lastContact = how; return true; },
    browsers: async () => ({
      browsers: [
        { id: 'chrome', name: 'Google Chrome', kind: 'chromium', page: 'chrome://extensions' },
        { id: 'edge', name: 'Microsoft Edge', kind: 'chromium', page: 'edge://extensions' },
        { id: 'brave', name: 'Brave', kind: 'chromium', page: 'brave://extensions' },
        { id: 'firefox', name: 'Mozilla Firefox', kind: 'firefox', page: 'about:addons' },
      ],
      folder: 'C:\\Users\\Ali\\AppData\\Local\\HELINK\\extension\\chromium', xpi: 'C:\\Users\\Ali\\AppData\\Local\\HELINK\\extension\\helink-firefox.xpi',
      id: 'bmkpbbkgkcbhaidhefjkhanimkfaefon', seenAgo: q.has('seen') ? Number(q.get('seen')) : (demo === 'browsers' ? -1 : 300),
    }),
    openBrowser: async (id, page) => { window.__lastBrowser = [id, page]; return true; },
    openExtFolder: async (kind) => { window.__lastExtFolder = kind; },
    confirmClose: async () => { window.__closed = true; },
    log: async () => '20:01:02 example log line',
    removeMusic: async (id) => {
      window.__lastRemoveMusic = (window.__lastRemoveMusic || []).concat(id);
      const t = tasks.get(id);
      if (!t || t.state !== 'done') return { error: err('file_missing') };
      const name = t.type === 'file' ? t.fileName : json(t).result.name;
      const n = addSep(name, t.saveDir, t.kind === 'audio' || t.category === 'music' ? 'audio' : 'video', t.total, { createdAt: Date.now(), queuedAt: Date.now() });
      pub(n, 'add');
      schedule();
      return { id: n.id };
    },
    pickSepFiles: async () => {
      window.__picked = (window.__picked || 0) + 1;
      later(250, () => {
        const ids = [];
        for (const [name, dir, kind, total] of sampleFiles()) {
          const t = addSep(name, dir, kind, total, { createdAt: Date.now(), queuedAt: Date.now() + ids.length });
          pub(t, 'add');
          ids.push(t.id);
        }
        schedule();
        emit('sepPicked', { ok: true, ids, skipped: 0 });
      });
    },
  };

  // ---------------------------------------------------------------- the scenes
  if (/^nomusic/.test(demo)) fillNoMusic();
  else if (!/^(empty|setup)/.test(demo) && !/^(compact|mini)/.test(demo)) fillList();
  const $ = (id) => document.getElementById(id);
  const click = (sel) => { const e = typeof sel === 'string' ? document.querySelector(sel) : sel; if (e) e.click(); };
  const rowOf = (n) => document.querySelectorAll('#rows .item')[n];
  const down = (e, opts) => e && e.dispatchEvent(new MouseEvent('mousedown', Object.assign({ bubbles: true, button: 0 }, opts || {})));
  function browserFile(extra) {
    const p = newPending(extra.url, 'browser', extra);
    emit('file', pendingEvent(p));
    return p;
  }
  function runDemo() {
    switch (demo) {
      case 'details': down(rowOf(0)); later(50, () => { const r = rowOf(0); r && r.dispatchEvent(new MouseEvent('dblclick', { bubbles: true })); }); break;
      case 'menu': {
        const r = rowOf(4);
        down(r);
        const b = r.getBoundingClientRect();
        r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, clientX: b.left + b.width * 0.35, clientY: b.top + b.height / 2 }));
        break;
      }
      case 'add': click('#add-btn'); break;
      case 'add-checking': click('#add-btn'); later(80, () => { $('add-input').value = 'https://downloads.example.com/files/HELINK-Setup-2.0.0.exe'; addLink($('add-input').value, 'link'); }); break;
      case 'add-error': click('#add-btn'); later(80, () => { $('add-input').value = 'https://www.example.com/blog/nothing-here'; click('#add-go'); }); break;
      case 'file': {
        const p = newPending('https://downloads.example.com/files/HELINK-Setup-2.0.0.exe', 'link', { name: 'HELINK-Setup-2.0.0.exe', size: 11.6 * MB, category: 'programs' });
        emit('file', pendingEvent(p));
        break;
      }
      case 'file-browser':
        browserFile({ url: 'https://cdn.example.com/dl/driver_setup_v5.zip?token=a8f3', name: 'driver_setup_v5.zip', size: 312 * MB, category: 'compressed', resumable: false, page: 'https://www.example.com/drivers' });
        break;
      case 'file-queue':
        browserFile({ url: 'https://cdn.example.com/a/report-2026.pdf', name: t2('تقرير 2026.pdf', 'Report 2026.pdf'), size: 3.1 * MB, category: 'documents' });
        browserFile({ url: 'https://cdn.example.com/a/photos.zip', name: 'photos.zip', size: 912 * MB, category: 'compressed' });
        browserFile({ url: 'https://cdn.example.com/a/song.mp3', name: 'song.mp3', size: 7.4 * MB, category: 'music' });
        break;
      case 'video': {
        const s = fetch(({ youtube: 'https://youtu.be/dQw4w9WgXcQ', tiktok: 'https://vm.tiktok.com/ZMabc/', x: 'https://x.com/a/status/1', instagram: 'https://www.instagram.com/p/C1/' })[siteParam] || 'https://youtu.be/x');
        emit('browserVideo', { seq: s, page: '' });
        if (freeze) { clearTimeout(fetchTimer); const p = post(siteParam); current = Object.assign(clone(p), { seq: s }); emit('info', Object.assign({ seq: s, status: 'done' }, clone(p))); }
        break;
      }
      case 'clip': case 'clip-nopreview': {
        const s = fetch('https://youtu.be/dQw4w9WgXcQ');
        emit('browserVideo', { seq: s, page: '' });
        clearTimeout(fetchTimer);
        const p = post('youtube');
        current = Object.assign(clone(p), { seq: s });
        emit('info', Object.assign({ seq: s, status: 'done' }, clone(p)));
        later(150, () => click('#video-clip'));
        break;
      }
      case 'clip-link': click('#clip-btn'); break;
      case 'clip-loading': click('#clip-btn'); later(60, () => { $('c-input').value = 'https://youtu.be/dQw4w9WgXcQ'; click('#clip-go'); }); break;
      case 'product': click('#prod-btn'); later(60, () => { $('p-input').value = 'https://www.amazon.eg/-/en/Anker-735-Charger/dp/B09W2PBRJR'; click('#prod-go'); }); break;
      case 'product-ask': click('#prod-btn'); break;
      case 'product-error': click('#prod-btn'); later(60, () => { $('p-input').value = 'https://www.temu.com/eg/charger-g-601099512345678.html'; click('#prod-go'); }); break;
      case 'compact-product': emit('mode', { compact: true, reason: 'product' }); productFromBrowser('aliexpress'); break;
      case 'video-loading': {
        const s = ++seq;
        emit('browserVideo', { seq: s, page: '' });
        emit('info', { seq: s, status: 'loading', site: siteParam, siteName: siteName(siteParam) });
        break;
      }
      case 'video-error': {
        const s = ++seq;
        readOpts.set(s, { media: [{ url: 'https://media.example.com/v/clip-720.mp4', kind: 'video', size: 48 * MB }, { url: 'https://media.example.com/v/master.m3u8', kind: 'hls' }] });
        emit('browserVideo', { seq: s, page: 'https://www.example.com/watch/123' });
        emit('info', { seq: s, status: 'loading', site: 'example.com', siteName: 'example.com' });
        emit('info', { seq: s, status: 'error', site: 'example.com', siteName: 'example.com', error: err('nothing_found', 'example.com', 'ERROR: Unsupported URL: https://www.example.com/watch/123'),
          media: [{ index: 0, kind: 'video', label: 'MP4', host: 'media.example.com', sizeText: size(48 * MB) }, { index: 1, kind: 'hls', label: t2('بث', 'Stream'), host: 'media.example.com' }] });
        break;
      }
      case 'settings': click('#set-btn'); if (q.get('tab')) later(120, () => click('.stabs [data-pane="' + q.get('tab') + '"]')); break;
      case 'browsers': click('#br-btn'); break;
      case 'about': click('#about-btn'); break;
      case 'about-update': click('#about-btn'); break;
      case 'update': emit('update', updView(true)); break;
      case 'update-dl':
      case 'update-error': click('#upd-btn'); break;
      case 'playlist':
      case 'playlist-again': {
        const s = fetch('https://www.youtube.com/playlist?list=PLcourse0001');
        emit('browserVideo', { seq: s, page: '' });
        if (freeze) { clearTimeout(fetchTimer); curList = { seq: s, list: courseList() }; emit('info', { seq: s, status: 'list', site: 'youtube', siteName: siteName('youtube'), list: curList.list }); }
        break;
      }
      case 'video-inlist': {
        const s = fetch('https://www.youtube.com/watch?v=dQw4w9WgXcQ&list=PLcourse0001');
        emit('browserVideo', { seq: s, page: '' });
        if (freeze) { clearTimeout(fetchTimer); const p = post('youtube'); current = Object.assign(clone(p), { seq: s }); emit('info', Object.assign({ seq: s, status: 'done', listUrl: 'https://www.youtube.com/playlist?list=PLcourse0001' }, clone(p))); }
        break;
      }
      case 'delete': down(rowOf(8)); down(rowOf(9), { ctrlKey: true }); later(30, () => click('#tb-delete')); break;
      case 'close': emit('closeRequest', { what: 'download' }); break;
      case 'setup': emit('setup', { phase: 'download', percent: 41 }); break;
      case 'setup-error': emit('setup', { phase: 'error', error: err('setup_failed') }); break;
      case 'compact-file':
        browserFile({ url: 'https://cdn.example.com/dl/HELINK-Setup-2.0.0.exe', name: 'HELINK-Setup-2.0.0.exe', size: 11.6 * MB, category: 'programs', page: 'https://helink.example.com/download' });
        emit('mode', { compact: true, reason: 'file' });
        break;
      case 'compact-checking':
        addLink('https://cdn.example.com/dl/' + (freeze ? 'slow/' : '') + 'archive.zip', 'browser');
        emit('mode', { compact: true, reason: 'file' });
        if (freeze) pending.clear();
        break;
      case 'nomusic-video':
      case 'nomusic-setup': {
        const s = fetch('https://youtu.be/dQw4w9WgXcQ');
        emit('browserVideo', { seq: s, page: '' });
        clearTimeout(fetchTimer);
        const p = post(siteParam);
        current = Object.assign(clone(p), { seq: s });
        emit('info', Object.assign({ seq: s, status: 'done' }, clone(p)));
        later(120, () => { const sw = $('v-nomusic'); if (sw && sw.getAttribute('aria-checked') !== 'true') sw.click(); });
        break;
      }
      case 'nomusic-menu': {
        const r = [...document.querySelectorAll('#rows .item')].find((x) => x.classList.contains('s-done') && (x.querySelector('.lab') || {}).textContent === '1080p');
        down(r);
        const b = r.getBoundingClientRect();
        r.dispatchEvent(new MouseEvent('contextmenu', { bubbles: true, clientX: b.left + b.width * 0.35, clientY: b.top + b.height / 2 }));
        break;
      }
      case 'nomusic-details': { const r = rowOf(0); down(r); later(50, () => r.dispatchEvent(new MouseEvent('dblclick', { bubbles: true }))); break; }
      case 'nomusic-pick': click('#sep-btn'); break;
      case 'mini':
      case 'mini-done':
      case 'mini-error': {
        // Chosen in the browser's menu: a video and its sound, downloading at once.
        const p = post(siteParam);
        const v = addMedia(p, 0, (p.items[0].options.find((o) => o.kind === 'video' && o.recommended) || p.items[0].options[0]).key,
          { createdAt: Date.now() - 4000, queuedAt: Date.now() - 4000 });
        const a = addMedia(p, 0, (p.items[0].options.find((o) => o.kind === 'audio') || p.items[0].options[0]).key,
          { createdAt: Date.now(), queuedAt: Date.now() });
        if (demo === 'mini-done') for (const t of [v, a]) { t.state = 'done'; t.percent = 100; t.finishedAt = Date.now(); }
        if (demo === 'mini-error') { a.state = 'error'; a.errorCode = 'network'; v.percent = 37; }
        for (const t of [v, a]) pub(t, 'add', true);
        for (const t of [v, a]) emit('browserTask', { id: t.id });
        emit('mode', { compact: true, reason: 'task' });
        schedule();
        break;
      }
      case 'compact-video': {
        const s = fetch('https://youtu.be/dQw4w9WgXcQ');
        emit('browserVideo', { seq: s, page: '' });
        emit('mode', { compact: true, reason: 'video' });
        if (freeze) { clearTimeout(fetchTimer); const p = post(siteParam); current = Object.assign(clone(p), { seq: s }); emit('info', Object.assign({ seq: s, status: 'done' }, clone(p))); }
        break;
      }
    }
  }
  window.__nzMockApi = api;
  window.__nzMockEmit = emit;
  return api;
};
