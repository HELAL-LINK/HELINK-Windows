// End-to-end test of the HELINK extension in Chromium, with the real helper (helink-host)
// and HELINK's engine (a Go test binary acting as the app, started by the helper on demand).
// usage: node e2e.js   (after: python3 ../build.py 2.2.1; go build -o $TMP/helink-host ./host;
//        go test -c -o $TMP/helinkapp .)  — see run.sh
'use strict';
const { chromium } = require('playwright');
const http = require('http');
const fs = require('fs');
const path = require('path');
const crypto = require('crypto');
const assert = require('assert');

const ROOT = path.resolve(__dirname, '..', '..');
const EXT = path.join(ROOT, 'extension', 'dist', 'chromium');
const EXT_ID = fs.readFileSync(path.join(ROOT, 'extension', 'keys', 'id.txt'), 'utf8').trim();
const TMP = process.env.E2E_TMP || fs.mkdtempSync('/tmp/helink-e2e-');
const HOST_BIN = process.env.HELINK_HOST_BIN;
const APP_BIN = process.env.HELINK_APP_BIN;
const MEDIA = '/home/claude/ytest/webm'; // VP9 + Opus: Chromium plays it (it has no H.264)
const shots = process.env.E2E_SHOTS || '';

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, ms = 30000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timeout: ' + what);
    await sleep(150);
  }
}

// ---------------------------------------------------------------------------
// The web site
// ---------------------------------------------------------------------------
const files = {
  'setup.zip': crypto.randomBytes(3 * 1024 * 1024 + 17),
  'other.bin': crypto.randomBytes(1024 * 1024 + 5),
  'notes.txt': Buffer.from('plain text the browser keeps\n'.repeat(100)),
  'form.zip': crypto.randomBytes(200 * 1024)
};
const seen = [];
function serveFile(req, res, buf, name, needCookie) {
  seen.push(req.method + ' ' + req.url + ' cookie=' + (req.headers.cookie || '') + ' ref=' + (req.headers.referer || '') + ' range=' + (req.headers.range || ''));
  if (needCookie && !(req.headers.cookie || '').includes('sid=abc123')) {
    res.writeHead(403, { 'Content-Type': 'text/plain' });
    return res.end('no');
  }
  const h = { 'Content-Type': 'application/octet-stream', 'Accept-Ranges': 'bytes', 'Content-Disposition': 'attachment; filename="' + name + '"', ETag: '"' + name + '-1"' };
  const m = /bytes=(\d+)-(\d*)/.exec(req.headers.range || '');
  if (m) {
    const a = Number(m[1]);
    const b = m[2] ? Math.min(Number(m[2]), buf.length - 1) : buf.length - 1;
    res.writeHead(206, Object.assign(h, { 'Content-Range': `bytes ${a}-${b}/${buf.length}`, 'Content-Length': b - a + 1 }));
    return res.end(buf.subarray(a, b + 1));
  }
  res.writeHead(200, Object.assign(h, { 'Content-Length': buf.length }));
  res.end(buf);
}
const server = http.createServer((req, res) => {
  const u = new URL(req.url, 'http://x');
  if (u.pathname === '/page.html') {
    res.writeHead(200, { 'Content-Type': 'text/html; charset=utf-8', 'Set-Cookie': 'sid=abc123; Path=/; HttpOnly' });
    return res.end(`<!doctype html><html><head><title>HELINK test page</title>
<style>body{font:16px system-ui;margin:20px} video{display:block;margin:20px 0}</style></head><body>
<h1>Test</h1>
<p><a id="zip" href="/files/setup.zip">setup.zip</a> · <a id="txt" href="/files/notes.txt">notes.txt</a></p>
<form id="f" method="POST" action="/files/form.zip"><button id="post">form download</button></form>
<video id="v" src="/media/test.webm" width="640" height="360" controls muted></video>
</body></html>`);
  }
  if (u.pathname.startsWith('/files/')) {
    const name = u.pathname.slice(7);
    const buf = files[name];
    if (!buf) { res.writeHead(404); return res.end(); }
    if (name === 'notes.txt') {
      res.writeHead(200, { 'Content-Type': 'text/plain', 'Content-Disposition': 'attachment; filename="notes.txt"' });
      return res.end(buf);
    }
    return serveFile(req, res, buf, name, name === 'setup.zip');
  }
  if (u.pathname.startsWith('/media/')) {
    const p = path.join(MEDIA, path.basename(u.pathname));
    if (!fs.existsSync(p)) { res.writeHead(404); return res.end(); }
    const buf = fs.readFileSync(p);
    seen.push('MEDIA ' + req.url + ' range=' + (req.headers.range || ''));
    const m = /bytes=(\d+)-(\d*)/.exec(req.headers.range || '');
    const h = { 'Content-Type': 'video/webm', 'Accept-Ranges': 'bytes' };
    if (m) {
      const a = Number(m[1]);
      const b = m[2] ? Math.min(Number(m[2]), buf.length - 1) : buf.length - 1;
      res.writeHead(206, Object.assign(h, { 'Content-Range': `bytes ${a}-${b}/${buf.length}`, 'Content-Length': b - a + 1 }));
      return res.end(buf.subarray(a, b + 1));
    }
    res.writeHead(200, Object.assign(h, { 'Content-Length': buf.length }));
    return res.end(buf);
  }
  res.writeHead(404);
  res.end();
});

// ---------------------------------------------------------------------------
async function main() {
  assert(HOST_BIN && APP_BIN, 'HELINK_HOST_BIN and HELINK_APP_BIN must be set');
  await new Promise((r) => server.listen(0, '127.0.0.1', r));
  const base = 'http://127.0.0.1:' + server.address().port;
  const data = path.join(TMP, 'data');
  const save = path.join(TMP, 'Downloads');
  const browserDl = path.join(TMP, 'browser-downloads');
  const profile = path.join(TMP, 'profile');
  const events = path.join(TMP, 'events.jsonl');
  for (const d of [data, save, browserDl, path.join(profile, 'NativeMessagingHosts')]) fs.mkdirSync(d, { recursive: true });
  // HELINK saves each kind of file in its own folder (Video, Music, Compressed, Other…).
  const inSave = (kind, name) => path.join(save, kind, name);
  const savedAnywhere = (name) => fs.existsSync(path.join(save, name)) ||
    fs.readdirSync(save).some((d) => fs.existsSync(path.join(save, d, name)));
  // The browser's own downloads go to browserDl, without asking.
  fs.mkdirSync(path.join(profile, 'Default'), { recursive: true });
  // Developer mode on, as for anyone who added the extension from HELINK's folder.
  fs.writeFileSync(path.join(profile, 'Default', 'Preferences'), JSON.stringify({
    download: { default_directory: browserDl, prompt_for_download: false, directory_upgrade: true },
    savefile: { default_directory: browserDl },
    extensions: { ui: { developer_mode: true } }
  }));
  fs.writeFileSync(path.join(profile, 'NativeMessagingHosts', 'com.helaltech.helink.json'), JSON.stringify({
    name: 'com.helaltech.helink', description: 'HELINK test', path: HOST_BIN, type: 'stdio',
    allowed_origins: ['chrome-extension://' + EXT_ID + '/']
  }));
  const env = Object.assign({}, process.env, {
    HELINK_DATA: data, NAZZELHA_SAVE: save, NAZZELHA_DEV_TOOLS: '1', HELINK_HELPER_APP: '1', HELINK_EVENTS: events,
    HELINK_APP_CMD: APP_BIN + ' -test.run=^TestHelperBrowserApp$ -test.timeout=10m'
  });
  const ctx = await chromium.launchPersistentContext(profile, {
    channel: 'chromium', headless: true, env, acceptDownloads: true,
    args: ['--disable-extensions-except=' + EXT, '--load-extension=' + EXT, '--lang=en-US'], viewport: { width: 1100, height: 800 }
  });
  const results = [];
  const ok = (name) => { results.push('PASS ' + name); console.log('PASS', name); };
  try {
    let sw = ctx.serviceWorkers()[0] || await ctx.waitForEvent('serviceworker', { timeout: 15000 });
    assert(sw.url().startsWith('chrome-extension://' + EXT_ID + '/'), 'extension id ' + sw.url());
    ok('extension loaded with its fixed ID ' + EXT_ID);

    // Playwright takes the browser's downloads over; give them back to the browser (and so
    // to the extension), as for a person.
    const cdpPage = ctx.pages()[0] || await ctx.newPage();
    const cdp = await ctx.newCDPSession(cdpPage);
    await cdp.send('Browser.setDownloadBehavior', { behavior: 'default' });
    // What the browser itself downloaded (as its downloads page shows it).
    const browserList = () => sw.evaluate(async () => (await chrome.downloads.search({})).map((d) => ({
      name: (d.filename || '').split('/').pop(), url: d.url, state: d.state, error: d.error || '', exists: d.exists })));
    const browserGot = async (name) => (await browserList()).find((d) => d.name === name && d.state === 'complete');

    const appEvents = () => (fs.existsSync(events) ? fs.readFileSync(events, 'utf8').trim().split('\n').filter(Boolean).map((l) => JSON.parse(l)) : []);
    const page = await ctx.newPage();
    page.on('console', (m) => { if (m.type() === 'error') console.log('page console:', m.text()); });
    await page.goto(base + '/page.html');

    // 1) The toolbar window before HELINK runs: closed (the helper answers from ext.json, or nothing yet).
    const popup = await ctx.newPage();
    await popup.goto('chrome-extension://' + EXT_ID + '/popup.html');
    const status1 = await popup.locator('#statusText').innerText();
    console.log('popup status before any download:', status1);
    await popup.close();

    // 2) A download the browser starts goes to HELINK (with the page's cookie and referrer).
    await page.click('#zip');
    const got = inSave('Compressed', 'setup.zip');
    await until('setup.zip saved by HELINK', () => fs.existsSync(got) && fs.statSync(got).size === files['setup.zip'].length, 60000);
    assert(Buffer.compare(fs.readFileSync(got), files['setup.zip']) === 0, 'setup.zip content');
    assert(seen.some((l) => l.includes('/files/setup.zip') && l.includes('sid=abc123') && l.includes('ref=' + base + '/page.html') && l.includes('range=bytes=')),
      'HELINK asked with the cookie, the referrer and in parts:\n' + seen.join('\n'));
    await sleep(800);
    const list1 = await browserList();
    assert(!list1.some((d) => /setup/.test(d.url) && d.state === 'complete'), 'the browser kept setup.zip: ' + JSON.stringify(list1));
    assert(!fs.readdirSync(browserDl).some((f) => f.startsWith('setup')), 'browser copy on disk: ' + fs.readdirSync(browserDl));
    ok('browser download taken by HELINK (cookie + referrer + ranges), browser copy cancelled');

    // 3) A kind of file not in the list stays with the browser.
    await page.click('#txt');
    await until('notes.txt downloaded by the browser', () => browserGot('notes.txt'), 15000);
    assert(!savedAnywhere('notes.txt'), 'HELINK should not take notes.txt');
    ok('other kinds of files stay with the browser');

    // 4) A download made by a form (POST) stays with the browser.
    await page.click('#post');
    await until('form.zip downloaded by the browser', () => browserGot('form.zip'), 15000);
    assert(!savedAnywhere('form.zip'), 'HELINK should not take a POST download');
    ok('form (POST) downloads stay with the browser');

    // 5) The button on the video: hovering shows it, clicking sends the video to HELINK.
    await page.goto(base + '/page.html');
    await page.waitForSelector('#v');
    await page.evaluate(() => document.getElementById('v').play().catch(() => {}));
    const box = await page.locator('#v').boundingBox();
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    const btn = page.locator('helink-video-button .b.on .go');
    await btn.waitFor({ timeout: 8000 });
    const bb = await btn.boundingBox();
    assert(bb.x > box.x + box.width / 2 && bb.y < box.y + 60, 'button at the top right of the video: ' + JSON.stringify(bb));
    if (shots) await page.screenshot({ path: path.join(shots, 'ext-video-button-en.png'), clip: { x: box.x - 10, y: box.y - 10, width: box.width + 20, height: box.height + 20 } });
    // Drag it to the bottom left: the place is kept for the site.
    await page.mouse.move(bb.x + bb.width / 2, bb.y + bb.height / 2);
    await page.mouse.down();
    await page.mouse.move(box.x + 60, box.y + box.height - 30, { steps: 8 });
    await page.mouse.up();
    await sleep(300);
    const pos = await sw.evaluate(async () => (await chrome.storage.local.get('pos')).pos);
    const kept = pos && pos['127.0.0.1'];
    assert(kept && kept.rx < 0.2 && kept.ry > 0.8, 'position kept: ' + JSON.stringify(pos));
    ok('video button shown on hover, dragged, position kept per site');
    const evCount = appEvents().length;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await btn.waitFor({ timeout: 8000 });
    const bb2 = await btn.boundingBox();
    assert(bb2.x < box.x + box.width / 2 && bb2.y > box.y + box.height / 2, 'button where it was dragged: ' + JSON.stringify(bb2));
    // Clicking opens the menu: HELINK reads the qualities (yt-dlp on the page).
    await btn.click();
    const rows = page.locator('helink-video-button .p .row');
    await rows.first().waitFor({ timeout: 90000 });
    const choices = await rows.count();
    assert(choices > 0, 'qualities in the menu');
    assert(!appEvents().slice(evCount).some((e) => e.event === 'browserVideo' || e.event === 'info'), 'the window was not used for the menu');
    if (shots) await page.screenshot({ path: path.join(shots, 'ext-menu-en.png') });
    ok('video button → menu with the qualities HELINK read (' + choices + ' choices)');
    // MP3 chosen in the menu: HELINK downloads it at once and shows its progress.
    await page.locator('helink-video-button .p .kinds button', { hasText: 'MP3' }).click();
    await rows.first().click();
    await page.locator('helink-video-button .p .msg.ok').waitFor({ timeout: 30000 });
    const add = await until('HELINK added the download', () => appEvents().slice(evCount).find((e) => e.event === 'task' && e.data.op === 'add'), 20000);
    assert(appEvents().slice(evCount).some((e) => e.event === 'browserTask' && e.data.id === add.data.task.id), 'shown as started from the browser');
    await until('the MP3 saved', () => appEvents().slice(evCount).find((e) => e.event === 'task' && e.data.task && e.data.task.id === add.data.task.id && e.data.task.state === 'done'), 120000);
    assert(fs.existsSync(path.join(save, 'Music')) && fs.readdirSync(path.join(save, 'Music')).some((f) => f.endsWith('.mp3')), 'an MP3 in Music: ' + fs.readdirSync(save));
    ok('MP3 chosen in the menu → downloaded by HELINK right away');
    // "Open in HELINK" still sends the video to HELINK's window.
    const evCount2 = appEvents().length;
    await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
    await btn.waitFor({ timeout: 8000 });
    await until('the menu closed by itself', async () => (await page.locator('helink-video-button .p.on').count()) === 0, 5000);
    await btn.click();
    await page.locator('helink-video-button .p.on .ft button').first().click();
    await until('the button says sent', async () => (await page.locator('helink-video-button .b.ok').count()) > 0, 30000);
    const bv = await until('HELINK got the video', () => appEvents().slice(evCount2).find((e) => e.event === 'browserVideo'), 20000);
    const info = await until('HELINK read the page', () => appEvents().slice(evCount2).find((e) => e.event === 'info' && e.data.seq === bv.data.seq && (e.data.status === 'done' || e.data.status === 'error')), 90000);
    assert.strictEqual(info.data.status, 'done', 'page read: ' + JSON.stringify(info.data.error));
    assert(info.data.items[0].options.length > 0, 'qualities found');
    ok('"Open in HELINK" → HELINK\'s window read the page (' + info.data.items[0].options.length + ' choices)');

    // 6) The toolbar window: HELINK running, the page's video listed, download it from there.
    const pop = await ctx.newPage();
    await pop.goto('chrome-extension://' + EXT_ID + '/popup.html?tab=' + 0);
    // (opened as a tab here: ask for the state of the test page's tab)
    const tabId = await sw.evaluate(async (u) => (await chrome.tabs.query({ url: u + '/page.html' }))[0].id, base);
    const st = await pop.evaluate(async (id) => chrome.runtime.sendMessage({ type: 'state', tabId: id, force: true }), tabId);
    assert(st.state.running === true && st.state.installed === true, 'state ' + JSON.stringify(st.state));
    if (!st.media.some((m) => m.url.endsWith('/media/test.webm'))) {
      const dbg = await sw.evaluate(async (id) => ({ keys: Array.from(media.keys()), list: await tabMedia(id), all: await chrome.storage.session.get(null) }), tabId);
      console.log('debug media', tabId, JSON.stringify(dbg));
      console.log('video src in page:', await page.evaluate(() => { const v = document.getElementById('v'); return [v.currentSrc, v.readyState, v.networkState]; }));
    }
    assert(st.media.some((m) => m.url.endsWith('/media/test.webm') && m.kind === 'video'), 'media ' + JSON.stringify(st.media));
    const r = await pop.evaluate(async (id) => chrome.runtime.sendMessage({ type: 'media', tabId: id, index: 0 }), tabId);
    assert(r.ok, 'media download ' + JSON.stringify(r));
    await until('test.webm saved', () => fs.existsSync(inSave('Video', 'test.webm')) && fs.statSync(inSave('Video', 'test.webm')).size === fs.statSync(path.join(MEDIA, 'test.webm')).size, 30000);
    ok('toolbar window: HELINK running, the page\'s video listed and downloaded');
    // The window itself, as the user sees it.
    await pop.setViewportSize({ width: 360, height: 560 });
    await pop.reload();
    await pop.waitForTimeout(800);
    if (shots) await pop.screenshot({ path: path.join(shots, 'ext-popup-en.png') });

    // 7) "Download link with HELINK" (the menu, called as the browser would).
    await sw.evaluate(async (a) => {
      const [tab] = await chrome.tabs.query({ url: a.base + '/page.html' });
      return helinkTest.onMenu({ menuItemId: 'helink-link', linkUrl: a.base + '/files/other.bin', pageUrl: a.base + '/page.html' }, tab);
    }, { base });
    await until('other.bin saved', () => fs.existsSync(inSave('Other', 'other.bin')) && fs.statSync(inSave('Other', 'other.bin')).size === files['other.bin'].length, 30000);
    ok('menu "Download link with HELINK"');

    // 8) The settings page: changes reach HELINK (and the language switches).
    const opt = await ctx.newPage();
    await opt.goto('chrome-extension://' + EXT_ID + '/options.html');
    await opt.locator('#results li.ok').first().waitFor({ timeout: 15000 });
    await opt.fill('#minKB', '2048');
    await opt.fill('#skip', 'bank.example\nhttps://www.Shop.example/x');
    await opt.click('#save');
    await until('settings saved', async () => (await opt.locator('#saveMsg').innerText()).length > 0, 10000);
    const ext = JSON.parse(fs.readFileSync(path.join(data, 'ext.json'), 'utf8'));
    assert(ext.catchMinKB === 2048 && ext.skipSites.join() === 'bank.example,shop.example', 'ext.json ' + JSON.stringify(ext));
    ok('settings page saves to HELINK');
    await opt.selectOption('#lang', 'ar');
    await opt.waitForTimeout(300);
    assert.strictEqual(await opt.evaluate(() => document.documentElement.dir), 'rtl');
    if (shots) {
      await opt.setViewportSize({ width: 900, height: 1300 });
      await opt.screenshot({ path: path.join(shots, 'ext-options-ar.png'), fullPage: true });
    }
    ok('settings page in Arabic, right to left');

    // 9) Small files (under the minimum) and catching switched off stay with the browser.
    await opt.fill('#minKB', '0');
    await opt.click('#save');
    await sleep(500);
    await pop.bringToFront();
    await pop.evaluate(async () => chrome.runtime.sendMessage({ type: 'setSettings', settings: { catch: false } }));
    fs.unlinkSync(got);
    await page.bringToFront();
    await page.click('#zip');
    await until('setup.zip by the browser', () => browserGot('setup.zip'), 15000);
    await sleep(800);
    assert(!fs.existsSync(got), 'HELINK took it while switched off');
    ok('catching switched off: the browser keeps its downloads');

    // 10) Arabic button and popup, for the screenshots.
    if (shots) {
      await page.goto(base + '/page.html');
      await page.waitForSelector('#v');
      const b2 = await page.locator('#v').boundingBox();
      await page.mouse.move(b2.x + b2.width / 2, b2.y + b2.height / 2);
      await page.locator('helink-video-button .b.on').waitFor({ timeout: 8000 });
      await sleep(300);
      await page.screenshot({ path: path.join(shots, 'ext-video-button-ar.png'), clip: { x: b2.x - 10, y: b2.y - 10, width: b2.width + 20, height: b2.height + 20 } });
      await pop.evaluate(async () => chrome.runtime.sendMessage({ type: 'setSettings', settings: { catch: true } }));
      await pop.reload();
      await pop.waitForTimeout(800);
      await pop.screenshot({ path: path.join(shots, 'ext-popup-ar.png') });
    }

    // 11) HELINK put newer files in the extension's folder (HELINK was updated): the extension
    // starts again with them, once, and the open pages get the new button.
    const mf = path.join(EXT, 'manifest.json');
    const orig = fs.readFileSync(mf, 'utf8');
    try {
      assert(await sw.evaluate(() => helinkTest.newerVersion('2.10.0', '2.9.1') && !helinkTest.newerVersion('2.2.1', '2.2.1') &&
        !helinkTest.newerVersion('2.1.9', '2.2')), 'version order');
      assert.strictEqual(await sw.evaluate(() => { helinkTest.resetReloadCheck(); return helinkTest.reloadIfUpdated(50); }), false, 'nothing new: no restart');
      fs.writeFileSync(mf, JSON.stringify(Object.assign(JSON.parse(orig), { version: '9.9.9' }), null, 2));
      const next = ctx.waitForEvent('serviceworker', { timeout: 20000 });
      await sw.evaluate(() => { helinkTest.resetReloadCheck(); return helinkTest.reloadIfUpdated(200); }).catch(() => {}); // it stops as it restarts
      const sw2 = await next;
      const v2 = await until('the new files run', async () => sw2.evaluate(() => chrome.runtime.getManifest().version).catch(() => ''), 15000);
      assert.strictEqual(v2, '9.9.9');
      await page.bringToFront(); // opened before the restart: the new button is put in it
      await page.waitForSelector('#v');
      const b3 = await page.locator('#v').boundingBox();
      await page.mouse.move(b3.x + 5, b3.y + 5);
      await page.mouse.move(b3.x + b3.width / 2, b3.y + b3.height / 2);
      const go3 = page.locator('helink-video-button .b.on .go');
      await go3.waitFor({ timeout: 10000 });
      await go3.click();
      await page.locator('helink-video-button .p .row').first().waitFor({ timeout: 30000 });
      await page.keyboard.press('Escape');
      ok('updated by HELINK: the extension restarts with its new files; the open page gets its working button');
    } finally {
      fs.writeFileSync(mf, orig);
    }
  } finally {
    fs.writeFileSync(path.join(data, 'stop'), '');
    await ctx.close().catch(() => {});
    server.close();
  }
  console.log('\n' + results.length + ' checks passed');
}

main().catch((e) => {
  console.error('FAIL', e && e.stack || e);
  console.error('requests seen:\n' + seen.join('\n'));
  process.exit(1);
});
