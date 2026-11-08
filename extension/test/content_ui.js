// The button on videos and its quality menu, in pages built like TikTok's (served for
// www.tiktok.com by the test itself) and a plain page: the extension's own APIs are faked,
// what the button sends is recorded.
// usage: node content_ui.js   (E2E_SHOTS=dir for screenshots)
'use strict';
const { chromium } = require('playwright');
const fs = require('fs');
const path = require('path');
const assert = require('assert');

const SRC = path.resolve(__dirname, '..', 'src', 'content.js');
const MEDIA = '/home/claude/ytest/webm/test.webm';
const shots = process.env.E2E_SHOTS || '';
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
async function until(what, fn, ms = 8000) {
  const end = Date.now() + ms;
  for (;;) {
    const v = await fn();
    if (v) return v;
    if (Date.now() > end) throw new Error('timeout: ' + what);
    await sleep(80);
  }
}

const FAKE = `
window.__sent = [];
window.__store = window.__store || {};
window.__answers = {
  qualities: { ok: true, id: 'b1', site: 'tiktok', items: [{ title: 'رقصة على البحر', options: [
    { key: 'v1080', kind: 'video', label: '1080p', tag: 'Full HD', sizeBytes: 31457280 },
    { key: 'v720', kind: 'video', label: '720p', tag: 'HD', sizeBytes: 12582912, recommended: true },
    { key: 'v480', kind: 'video', label: '480p', tag: '', sizeBytes: 6291456 },
    { key: 'mp3-320', kind: 'audio', label: '320k', tag: 'MP3', sizeBytes: 4800000 },
    { key: 'mp3-192', kind: 'audio', label: '192k', tag: 'MP3', sizeBytes: 2880000, recommended: true },
    { key: 'mp3-128', kind: 'audio', label: '128k', tag: 'MP3', sizeBytes: 1920000 }] }], sepReady: false },
  get: { ok: true, taskId: 't1' }
};
window.__delay = 300;
globalThis.chrome = {
  runtime: { id: 'test', sendMessage: (m) => { window.__sent.push(JSON.parse(JSON.stringify(m)));
    const a = window.__answers[m.type] || { ok: true };
    return new Promise((r) => setTimeout(() => r(a), m.type === 'qualities' ? window.__delay : 30)); } },
  storage: { local: { get: async () => window.__store, set: async (o) => { Object.assign(window.__store, o); } },
    onChanged: { addListener() {} } },
  i18n: { getUILanguage: () => (location.hash === '#ar' ? 'ar' : 'en') }
};`;

const css = `body{margin:0;background:#000;color:#fff;font:14px sans-serif}
#feed{height:100vh;overflow-y:scroll;scroll-snap-type:y mandatory}
article{height:100vh;scroll-snap-align:start;display:flex;align-items:center;justify-content:center}
.wrap{width:360px;height:640px;position:relative}
.wrap video{width:100%;height:100%;object-fit:cover;background:#234;display:block}
.ghost{position:absolute;inset:0;opacity:0}
.over{position:absolute;inset:0}
.side{position:absolute;right:-60px;bottom:40px;width:48px;height:200px;background:#333}`;
const art = (id, extra = '') => `<article data-e2e="recommend-list-item-container"><div class="wrap" id="xgwrapper-0-${id}">
<video src="/v.webm" muted loop playsinline></video>${extra}<div class="over"></div><div class="side"></div></div></article>`;
const FEED = `<!doctype html><html><head><title>TikTok - Make Your Day</title><style>${css}</style></head><body>
<div id="feed">${art('7300000000000000001')}${art('7300000000000000002',
  '<div class="wrap ghost" id="xgwrapper-1-7300000000000000099"><video src="/v.webm" muted></video></div>')}${art('7300000000000000003')}</div>
</body></html>`;
const grid = Array.from({ length: 8 }, (_, i) => `<a href="/@sea/video/73000000000000004${i}" style="display:inline-block;margin:6px">
<video src="/v.webm" muted style="width:200px;height:280px;background:#345;display:block"></video></a>`).join('');
const PROFILE = `<!doctype html><html><head><title>sea | TikTok</title><style>${css}
#modal{position:fixed;inset:0;background:rgba(0,0,0,.92);display:flex;align-items:center;justify-content:center}</style></head>
<body><div id="grid" style="padding:20px;width:900px">${grid}</div>
<div id="modal"><div class="wrap" id="xgwrapper-0-7300000000000000500"><video src="/v.webm" muted loop></video><div class="over"></div></div></div>
</body></html>`;
const PLAIN = `<!doctype html><html><head><title>A lesson</title></head><body style="margin:0;background:#fff">
<h1 style="margin:16px">A lesson</h1><video id="v" src="/v.webm" muted style="width:640px;height:360px;margin:16px;display:block;background:#123"></video></body></html>`;

async function main() {
  const browser = await chromium.launch();
  const results = [];
  const ok = (n) => { results.push(n); console.log('PASS', n); };
  try {
    const ctx = await browser.newContext({ viewport: { width: 1000, height: 760 } });
    await ctx.addInitScript(FAKE);
    await ctx.addInitScript({ path: SRC });
    await ctx.route(/^https:\/\/(www\.tiktok\.com|lessons\.example)\//, (route) => {
      const u = new URL(route.request().url());
      if (u.pathname === '/v.webm') return route.fulfill({ status: 200, contentType: 'video/webm', body: fs.readFileSync(MEDIA) });
      const body = u.hostname === 'lessons.example' ? PLAIN : u.pathname.startsWith('/@') ? PROFILE : FEED;
      return route.fulfill({ status: 200, contentType: 'text/html; charset=utf-8', body });
    });
    const page = await ctx.newPage();
    page.on('pageerror', (e) => console.log('page error:', e.message));
    const btn = page.locator('helink-video-button .b.on .go');
    const panel = page.locator('helink-video-button .p.on');
    const rect = (sel) => page.locator(sel).first().boundingBox();
    const sent = () => page.evaluate(() => window.__sent);

    // 1) The feed: the button over the video under a mouse that does not move, also after
    //    the feed scrolled to the next video; not over the invisible video kept ready.
    await page.goto('https://www.tiktok.com/foryou');
    const v1 = await rect('article:nth-child(1) .wrap > video');
    await page.mouse.move(v1.x + v1.width / 2, v1.y + v1.height / 2);
    await btn.waitFor({ timeout: 5000 });
    await sleep(4200); // longer than the moment a playing video shows it
    assert(await btn.count() === 1, 'the button stays while the mouse is on the video');
    await page.evaluate(() => { document.getElementById('feed').scrollTop = innerHeight; });
    await sleep(900);
    assert(await btn.count() === 1, 'the button is there after the feed moved under the still mouse');
    await btn.click();
    await panel.waitFor({ timeout: 3000 });
    await until('qualities asked', async () => (await sent()).some((m) => m.type === 'qualities'));
    let q = (await sent()).filter((m) => m.type === 'qualities').pop();
    assert(q.postUrl === 'https://www.tiktok.com/@/video/7300000000000000002', 'the second video, not the hidden one: ' + q.postUrl);
    ok('TikTok feed: the button stays on the video, follows the feed under a still mouse, skips the hidden video');

    // 2) The menu: video and MP3, sizes, without music; a quality starts the download.
    await page.locator('helink-video-button .p .row').first().waitFor({ timeout: 3000 });
    const rows = await page.locator('helink-video-button .p .row').allTextContents();
    assert(rows.length === 3 && rows[0].includes('1080p') && rows[0].includes('30 MB') && rows[1].includes('Recommended'), 'video rows ' + JSON.stringify(rows));
    if (shots) await page.screenshot({ path: path.join(shots, 'menu-tiktok-en.png') });
    await page.locator('helink-video-button .p .kinds button', { hasText: 'MP3' }).click();
    const arows = await page.locator('helink-video-button .p .row').allTextContents();
    assert(arows.length === 3 && arows[1].includes('192k'), 'audio rows ' + JSON.stringify(arows));
    await page.locator('helink-video-button .p .sw').click();
    await page.locator('helink-video-button .p .row', { hasText: '192k' }).click();
    await until('get sent', async () => (await sent()).some((m) => m.type === 'get'));
    const g = (await sent()).find((m) => m.type === 'get');
    assert(g.id === 'b1' && g.key === 'mp3-192' && g.noMusic === true && g.item === 0, 'get ' + JSON.stringify(g));
    await page.locator('helink-video-button .p .msg.ok').waitFor({ timeout: 2000 });
    await panel.waitFor({ state: 'detached', timeout: 4000 }).catch(() => {});
    await until('menu closed', async () => (await panel.count()) === 0, 4000);
    const store = await page.evaluate(() => window.__store.panel);
    assert(store && store.kind === 'audio' && store.noMusic === true, 'choices kept ' + JSON.stringify(store));
    ok('menu: qualities with sizes, video/MP3, without music; the click downloads and the menu closes');

    // 3) Asked as soon as the mouse is on the button; the menu opens with what came.
    await page.evaluate(() => { window.__sent = []; document.getElementById('feed').scrollTop = 2 * innerHeight; });
    await sleep(900);
    const v3c = await rect('article:nth-child(3) .wrap > video');
    await page.mouse.move(v3c.x + v3c.width / 2, v3c.y + v3c.height / 2);
    await btn.waitFor({ timeout: 3000 });
    const b3 = await btn.boundingBox();
    await page.mouse.move(b3.x + b3.width / 2, b3.y + b3.height / 2);
    await until('asked on hover', async () => (await sent()).some((m) => m.type === 'qualities'));
    await sleep(400);
    await btn.click();
    await page.locator('helink-video-button .p .row').first().waitFor({ timeout: 1000 });
    assert((await sent()).filter((m) => m.type === 'qualities').length === 1, 'asked once');
    // Escape closes it; so does a click elsewhere.
    await page.keyboard.press('Escape');
    await until('closed by Escape', async () => (await panel.count()) === 0, 2000);
    await btn.click();
    await panel.waitFor({ timeout: 2000 });
    await page.mouse.click(30, 30);
    await until('closed by a click elsewhere', async () => (await panel.count()) === 0, 2000);
    ok('asked on hover (once), Escape and a click elsewhere close the menu');

    // 4) ✕ hides the button for that video only.
    const b4 = await btn.boundingBox();
    await page.mouse.move(b4.x + b4.width / 2, b4.y + b4.height / 2);
    const x = page.locator('helink-video-button .b.on .x');
    await x.waitFor({ timeout: 2000 });
    await x.click();
    await sleep(300);
    const v3 = await rect('article:nth-child(3) .wrap > video');
    await page.mouse.move(v3.x + 40, v3.y + v3.height / 2);
    await sleep(800);
    assert(await btn.count() === 0, 'hidden for this video');
    await page.evaluate(() => { document.getElementById('feed').scrollTop = 0; });
    await sleep(900);
    await page.mouse.move(v1.x + 50, v1.y + 300);
    await btn.waitFor({ timeout: 3000 });
    ok('✕ hides the button for that video only');

    // 5) A video opened over the profile's grid: the one on top.
    await page.goto('https://www.tiktok.com/@sea/video/7300000000000000500');
    const vm = await rect('#modal video');
    await page.mouse.move(vm.x + vm.width / 2, vm.y + vm.height / 2);
    await btn.waitFor({ timeout: 5000 });
    await page.evaluate(() => { window.__sent = []; });
    await btn.click();
    await until('qualities asked', async () => (await sent()).some((m) => m.type === 'qualities'));
    q = (await sent()).find((m) => m.type === 'qualities');
    assert(q.postUrl === 'https://www.tiktok.com/@sea/video/7300000000000000500', 'the video on top: ' + q.postUrl);
    ok('a video opened over the grid: the one on top');

    // 6) Arabic, right to left, on a plain page; a failure offers HELINK's window.
    await page.goto('https://lessons.example/lesson#ar');
    await page.evaluate(() => { window.__answers.qualities = { ok: false, error: 'nothing_found', title: 'لم يُعثر على فيديو', message: 'لم يجد HELINK فيديو يمكن تنزيله في هذه الصفحة.' }; });
    const vp = await rect('#v');
    await page.mouse.move(vp.x + vp.width / 2, vp.y + vp.height / 2);
    await btn.waitFor({ timeout: 5000 });
    const dir = await page.locator('helink-video-button .b').getAttribute('dir');
    assert(dir === 'rtl', 'right to left');
    await btn.click();
    await page.locator('helink-video-button .p .msg.err').waitFor({ timeout: 3000 });
    const txt = await page.locator('helink-video-button .p').textContent();
    assert(txt.includes('لم يُعثر على فيديو') && txt.includes('فتح في HELINK') && txt.includes('حاول مرة أخرى'), 'error ' + txt);
    if (shots) await page.screenshot({ path: path.join(shots, 'menu-error-ar.png') });
    await page.evaluate(() => { window.__answers.qualities = Object.assign({}, window.__answers.qualities, { ok: true, id: 'b2', items: [{ title: 'درس الطبخ', options: [
      { key: 'vbest', kind: 'video', label: 'Video', tag: '', sizeBytes: 0, recommended: true },
      { key: 'mp3-192', kind: 'audio', label: '192k', tag: 'MP3', sizeBytes: 900000, recommended: true }] }], sepReady: true }); });
    await page.locator('helink-video-button .p .ft .rt').click();
    await page.locator('helink-video-button .p .row').first().waitFor({ timeout: 3000 });
    const r6 = await page.locator('helink-video-button .p .row').allTextContents();
    assert(r6.length === 1 && r6[0].includes('أفضل جودة'), 'Arabic rows ' + JSON.stringify(r6));
    // sizes with the digits of HELINK's window
    await page.locator('helink-video-button .p .kinds button').nth(1).click();
    const r7 = await page.locator('helink-video-button .p .row').allTextContents();
    assert(r7.length === 1 && r7[0].includes('879 كيلوبايت'), 'Arabic size ' + JSON.stringify(r7));
    await page.locator('helink-video-button .p .kinds button').nth(0).click();
    const pr = await page.locator('helink-video-button .p').boundingBox();
    const br = await btn.boundingBox();
    assert(Math.abs((pr.x + pr.width) - (br.x + br.width)) < 2 && pr.y > br.y, 'the menu under the button, its right edges aligned: ' + JSON.stringify([pr, br]));
    if (shots) await page.screenshot({ path: path.join(shots, 'menu-plain-ar.png') });
    await page.evaluate(() => { window.__sent = []; });
    await page.locator('helink-video-button .p .ft button', { hasText: 'فتح في HELINK' }).click();
    await until('sent to the window', async () => (await sent()).some((m) => m.type === 'video'));
    ok('Arabic, right to left: a failure offers HELINK\'s window and trying again');

    console.log('\n' + results.length + ' checks passed');
  } finally {
    await browser.close();
  }
}

main().catch((e) => {
  console.error('FAIL', e);
  process.exit(1);
});
