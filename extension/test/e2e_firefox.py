#!/usr/bin/env python3
"""End-to-end test of the HELINK extension in Firefox (Selenium + geckodriver), with the real
helper (helink-host) and HELINK's engine (a Go test binary acting as the app, started by the
helper on demand) - the Firefox twin of e2e.js.
usage: see run_firefox.sh (FIREFOX_BIN, GECKODRIVER, HELINK_HOST_BIN, HELINK_APP_BIN, E2E_TMP)."""
import hashlib, http.server, json, os, re, shutil, socketserver, sys, threading, time
from selenium import webdriver
from selenium.webdriver.common.action_chains import ActionChains
from selenium.webdriver.common.by import By
from selenium.webdriver.firefox.options import Options
from selenium.webdriver.firefox.service import Service

ROOT = os.path.abspath(os.path.join(os.path.dirname(__file__), '..', '..'))
XPI = os.path.join(ROOT, 'extension', 'dist', 'helink-firefox.xpi')
TMP = os.environ['E2E_TMP']
HOST_BIN = os.environ['HELINK_HOST_BIN']
APP_BIN = os.environ['HELINK_APP_BIN']
MEDIA = '/home/claude/ytest/webm'
UUID = 'f0e1d2c3-b4a5-4697-8899-aabbccddeeff'
EXT = 'moz-extension://' + UUID

files = {
    'setup.zip': os.urandom(3 * 1024 * 1024 + 17),
    'other.bin': os.urandom(1024 * 1024 + 5),
    'notes.txt': b'plain text the browser keeps\n' * 100,
    'form.zip': os.urandom(200 * 1024),
}
seen = []
PAGE = b'''<!doctype html><html><head><title>HELINK test page</title>
<style>body{font:16px sans-serif;margin:20px} video{display:block;margin:20px 0}</style></head><body>
<h1>Test</h1>
<p><a id="zip" href="/files/setup.zip">setup.zip</a> &middot; <a id="txt" href="/files/notes.txt">notes.txt</a></p>
<form id="f" method="POST" action="/files/form.zip"><button id="post">form download</button></form>
<video id="v" src="/media/test.webm" width="640" height="360" controls muted></video>
</body></html>'''


class Handler(http.server.BaseHTTPRequestHandler):
    protocol_version = 'HTTP/1.1'

    def log_message(self, *a):
        pass

    def send(self, code, headers, body=b''):
        self.send_response(code)
        for k, v in headers.items():
            self.send_header(k, v)
        if 'Content-Length' not in headers:
            self.send_header('Content-Length', str(len(body)))
        self.end_headers()
        if self.command != 'HEAD':
            self.wfile.write(body)

    def ranged(self, buf, headers):
        m = re.match(r'bytes=(\d+)-(\d*)', self.headers.get('Range') or '')
        if m:
            a = int(m.group(1))
            b = min(int(m.group(2)), len(buf) - 1) if m.group(2) else len(buf) - 1
            headers.update({'Content-Range': 'bytes %d-%d/%d' % (a, b, len(buf)), 'Content-Length': str(b - a + 1)})
            return self.send(206, headers, buf[a:b + 1])
        headers['Content-Length'] = str(len(buf))
        self.send(200, headers, buf)

    def do_POST(self):
        n = int(self.headers.get('Content-Length') or 0)
        if n:
            self.rfile.read(n)
        self.do_GET()

    def do_HEAD(self):
        self.do_GET()

    def do_GET(self):
        path = self.path.split('?')[0]
        if path == '/page.html':
            return self.send(200, {'Content-Type': 'text/html; charset=utf-8', 'Set-Cookie': 'sid=abc123; Path=/; HttpOnly'}, PAGE)
        if path.startswith('/files/'):
            name = path[7:]
            buf = files.get(name)
            if buf is None:
                return self.send(404, {})
            if name == 'notes.txt':
                return self.send(200, {'Content-Type': 'text/plain', 'Content-Disposition': 'attachment; filename="notes.txt"'}, buf)
            seen.append('%s %s cookie=%s ref=%s range=%s' % (self.command, self.path, self.headers.get('Cookie') or '',
                                                           self.headers.get('Referer') or '', self.headers.get('Range') or ''))
            if name == 'setup.zip' and 'sid=abc123' not in (self.headers.get('Cookie') or ''):
                return self.send(403, {'Content-Type': 'text/plain'}, b'no')
            return self.ranged(buf, {'Content-Type': 'application/octet-stream', 'Accept-Ranges': 'bytes',
                                     'Content-Disposition': 'attachment; filename="%s"' % name, 'ETag': '"%s-1"' % name})
        if path.startswith('/media/'):
            p = os.path.join(MEDIA, os.path.basename(path))
            if not os.path.exists(p):
                return self.send(404, {})
            seen.append('MEDIA %s range=%s' % (self.path, self.headers.get('Range') or ''))
            return self.ranged(open(p, 'rb').read(), {'Content-Type': 'video/webm', 'Accept-Ranges': 'bytes'})
        self.send(404, {})


class Server(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True

    def handle_error(self, request, client_address):
        pass  # the browser closing a connection early is not a failure


def until(what, fn, secs=30):
    end = time.time() + secs
    while True:
        v = fn()
        if v:
            return v
        if time.time() > end:
            raise AssertionError('timeout: ' + what)
        time.sleep(0.15)


def same_file(p, buf):
    return os.path.exists(p) and os.path.getsize(p) == len(buf) and hashlib.sha256(open(p, 'rb').read()).digest() == hashlib.sha256(buf).digest()


def saved_anywhere(save, name):
    """HELINK saves each kind of file in its own folder (Video, Music, Compressed, Other…)."""
    return os.path.exists(os.path.join(save, name)) or any(os.path.exists(os.path.join(save, d, name)) for d in os.listdir(save))


def main():
    srv = Server(('127.0.0.1', 0), Handler)
    threading.Thread(target=srv.serve_forever, daemon=True).start()
    base = 'http://127.0.0.1:%d' % srv.server_address[1]
    data, save, browser_dl, home = (os.path.join(TMP, d) for d in ('data', 'Downloads', 'browser-downloads', 'home'))
    events = os.path.join(TMP, 'events.jsonl')
    nm = os.path.join(home, '.mozilla', 'native-messaging-hosts')
    for d in (data, save, browser_dl, nm):
        os.makedirs(d, exist_ok=True)
    with open(os.path.join(nm, 'com.helaltech.helink.json'), 'w') as f:
        json.dump({'name': 'com.helaltech.helink', 'description': 'HELINK test', 'path': HOST_BIN, 'type': 'stdio',
                   'allowed_extensions': ['helink@helaltech.com']}, f)
    # Firefox, and so the helper it starts, and the app the helper starts, get these.
    os.environ.update({'HOME': home, 'HELINK_DATA': data, 'NAZZELHA_SAVE': save, 'NAZZELHA_DEV_TOOLS': '1',
                       'HELINK_HELPER_APP': '1', 'HELINK_EVENTS': events,
                       'HELINK_APP_CMD': APP_BIN + ' -test.run=^TestHelperBrowserApp$ -test.timeout=10m'})

    def app_events():
        if not os.path.exists(events):
            return []
        return [json.loads(l) for l in open(events).read().splitlines() if l.strip()]

    opts = Options()
    opts.binary_location = os.environ['FIREFOX_BIN']
    opts.add_argument('-headless')
    for k, v in {
        'browser.download.folderList': 2, 'browser.download.dir': browser_dl, 'browser.download.useDownloadDir': True,
        'browser.download.always_ask_before_handling_new_types': False,
        'browser.helperApps.neverAsk.saveToDisk': 'application/zip,application/octet-stream,text/plain',
        'browser.download.manager.showWhenStarting': False, 'browser.download.alwaysOpenPanel': False,
        'extensions.webextensions.uuids': json.dumps({'helink@helaltech.com': UUID}),
        'media.autoplay.default': 0, 'intl.locale.requested': 'en-US', 'datareporting.policy.dataSubmissionEnabled': False,
    }.items():
        opts.set_preference(k, v)
    drv = webdriver.Firefox(options=opts, service=Service(executable_path=os.environ['GECKODRIVER']))
    drv.set_window_size(1100, 900)
    results = []

    def ok(name):
        results.append(name)
        print('PASS', name, flush=True)

    def ext_call(script, *args):
        """Runs an async script in an extension page (the options tab): browser.* is there."""
        return drv.execute_async_script('''
            const done = arguments[arguments.length - 1];
            const args = Array.prototype.slice.call(arguments, 0, arguments.length - 1);
            (async (...a) => { %s })(...args).then((r) => done(r === undefined ? null : r), (e) => done({ err: String(e) }));
        ''' % script, *args)

    try:
        drv.install_addon(XPI, temporary=True)
        main_tab = drv.current_window_handle

        # 1) The settings page reaches the helper (HELINK is closed: the helper answers).
        drv.get(EXT + '/options.html')
        until('settings page test results', lambda: drv.find_elements(By.CSS_SELECTOR, '#results li.ok'), 20)
        texts = [e.text for e in drv.find_elements(By.CSS_SELECTOR, '#results li')]
        print('settings page says:', texts)
        ok('extension installed in Firefox, the helper answers its settings page')
        ext_tab = drv.current_window_handle

        # 2) A download Firefox starts goes to HELINK (cookie, referrer, in parts), Firefox's is cancelled.
        drv.switch_to.new_window('tab')
        page_tab = drv.current_window_handle
        drv.get(base + '/page.html')
        drv.find_element(By.ID, 'zip').click()
        got = os.path.join(save, 'Compressed', 'setup.zip')
        until('setup.zip saved by HELINK', lambda: same_file(got, files['setup.zip']), 60)
        assert any('/files/setup.zip' in l and 'sid=abc123' in l and 'ref=' + base + '/page.html' in l and 'range=bytes=' in l for l in seen), \
            'HELINK asked with the cookie, the referrer and in parts:\n' + '\n'.join(seen)
        time.sleep(1.5)
        left = [f for f in os.listdir(browser_dl) if f.startswith('setup')]
        assert not left, 'Firefox kept a copy: %s' % left
        drv.switch_to.window(ext_tab)
        listed = ext_call('return (await browser.downloads.search({})).map((d) => [d.filename.split("/").pop(), d.state]);')
        assert not any(n.startswith('setup') and st == 'complete' for n, st in listed), 'Firefox list: %s' % listed
        ok('Firefox download taken by HELINK (cookie + referrer + ranges), Firefox copy held then cancelled')

        # 3) Kinds of files not in the list, and forms (POST), stay with Firefox.
        drv.switch_to.window(page_tab)
        drv.find_element(By.ID, 'txt').click()
        until('notes.txt by Firefox', lambda: os.path.exists(os.path.join(browser_dl, 'notes.txt')), 20)
        assert not saved_anywhere(save, 'notes.txt'), 'HELINK took notes.txt'
        drv.find_element(By.ID, 'post').click()
        until('form.zip by Firefox', lambda: same_file(os.path.join(browser_dl, 'form.zip'), files['form.zip']), 20)
        assert not saved_anywhere(save, 'form.zip'), 'HELINK took a POST download'
        ok('other kinds of files and form (POST) downloads stay with Firefox')

        # 4) The button on the video: hovering shows it, clicking sends the video to HELINK.
        drv.get(base + '/page.html')
        video = until('video', lambda: drv.find_elements(By.ID, 'v'), 10)[0]
        drv.execute_script('arguments[0].play().catch(() => {})', video)
        ActionChains(drv).move_to_element(video).perform()
        go = until('the HELINK button on the video', lambda: drv.execute_script('''
            const h = document.querySelector('helink-video-button');
            const b = h && h.shadowRoot && h.shadowRoot.querySelector('.b.on .go');
            return b || null;'''), 10)
        vr, br = video.rect, go.rect
        assert br['x'] > vr['x'] + vr['width'] / 2 and br['y'] < vr['y'] + 60, 'button at the top right: %s %s' % (br, vr)
        n0 = len(app_events())
        ActionChains(drv).move_to_element(go).click().perform()

        def shadow(sel):
            return drv.execute_script(
                "const h = document.querySelector('helink-video-button');"
                "return h && h.shadowRoot ? Array.from(h.shadowRoot.querySelectorAll(arguments[0])) : [];", sel)
        rows = until('the menu with the qualities', lambda: shadow('.p.on .row') or None, 90)
        assert not any(e['event'] in ('browserVideo', 'info') for e in app_events()[n0:]), 'the window was used for the menu'
        ok('video button in Firefox -> menu with the qualities HELINK read (%d choices)' % len(rows))
        rows[0].click()
        until('the menu says started', lambda: shadow('.p .msg.ok') or None, 30)
        add = until('HELINK added the download', lambda: next((e for e in app_events()[n0:] if e['event'] == 'task' and e['data'].get('op') == 'add'), None), 20)
        tid = add['data']['task']['id']
        until('downloaded', lambda: next((e for e in app_events()[n0:] if e['event'] == 'task' and (e['data'].get('task') or {}).get('id') == tid
                                          and e['data']['task'].get('state') == 'done'), None), 120)
        ok('a quality chosen in the menu in Firefox -> downloaded by HELINK')

        # 5) The toolbar window's state: HELINK running, the page's video listed; download it.
        drv.switch_to.window(ext_tab)
        st = ext_call('''
            const [tab] = await browser.tabs.query({ url: '*://127.0.0.1/page.html' });
            const s = await browser.runtime.sendMessage({ type: 'state', tabId: tab.id, force: true });
            return { running: s.state.running, installed: s.state.installed, media: s.media, tab: tab.id };''', base)
        assert st.get('running') is True and st.get('installed') is True, 'state %s' % st
        assert any(m['url'].endswith('/media/test.webm') and m['kind'] == 'video' for m in st['media']), 'media %s' % st['media']
        idx = next(i for i, m in enumerate(st['media']) if m['url'].endswith('/media/test.webm'))
        r = ext_call('return browser.runtime.sendMessage({ type: "media", tabId: a[0], index: a[1] });', st['tab'], idx)
        assert r and r.get('ok'), 'media download %s' % r
        webm = open(os.path.join(MEDIA, 'test.webm'), 'rb').read()
        until('test.webm saved', lambda: same_file(os.path.join(save, 'Video', 'test.webm'), webm), 30)
        ok("toolbar window state in Firefox: the page's video listed and downloaded")

        # 6) "Download link with HELINK" (the menu, called as Firefox would, in the background page).
        r = ext_call('''
            const bg = await browser.runtime.getBackgroundPage();
            const [tab] = await browser.tabs.query({ url: '*://127.0.0.1/page.html' });
            const res = await bg.helinkTest.onMenu({ menuItemId: 'helink-link', linkUrl: a[0] + '/files/other.bin', pageUrl: a[0] + '/page.html' }, tab);
            return JSON.parse(JSON.stringify(res));''', base)
        assert r and r.get('ok'), 'menu %s' % r
        until('other.bin saved', lambda: same_file(os.path.join(save, 'Other', 'other.bin'), files['other.bin']), 30)
        ok('menu "Download link with HELINK" in Firefox')

        # 7) The settings page saves to HELINK; Arabic, right to left.
        drv.get(EXT + '/options.html')
        until('settings page ready', lambda: drv.find_elements(By.CSS_SELECTOR, '#results li.ok'), 20)
        mk = drv.find_element(By.ID, 'minKB')
        mk.clear()
        mk.send_keys('2048')
        sk = drv.find_element(By.ID, 'skip')
        sk.clear()
        sk.send_keys('bank.example\nhttps://www.Shop.example/x')
        drv.find_element(By.ID, 'save').click()
        until('settings saved', lambda: drv.find_element(By.ID, 'saveMsg').text, 10)
        ext = json.load(open(os.path.join(data, 'ext.json')))
        assert ext['catchMinKB'] == 2048 and ext['skipSites'] == ['bank.example', 'shop.example'], 'ext.json %s' % ext
        drv.execute_script('const s = document.getElementById("lang"); s.value = "ar"; s.dispatchEvent(new Event("change"));')
        until('rtl', lambda: drv.execute_script('return document.documentElement.dir') == 'rtl', 5)
        ok('settings page in Firefox saves to HELINK, Arabic right to left')

        # 8) Catching switched off: Firefox keeps its downloads.
        mk = drv.find_element(By.ID, 'minKB')
        mk.clear()
        mk.send_keys('0')
        drv.find_element(By.ID, 'save').click()
        time.sleep(0.5)
        ext_call('return browser.runtime.sendMessage({ type: "setSettings", settings: { catch: false } });')
        os.remove(got)
        drv.switch_to.window(page_tab)
        drv.get(base + '/page.html')
        drv.find_element(By.ID, 'zip').click()
        until('setup.zip by Firefox', lambda: same_file(os.path.join(browser_dl, 'setup.zip'), files['setup.zip']), 20)
        time.sleep(0.8)
        assert not os.path.exists(got), 'HELINK took it while switched off'
        ok('catching switched off: Firefox keeps its downloads')
    finally:
        open(os.path.join(data, 'stop'), 'w').close()
        try:
            drv.quit()
        except Exception:
            pass
        srv.shutdown()
    print('\n%d checks passed' % len(results))


if __name__ == '__main__':
    try:
        main()
    except Exception as e:
        print('FAIL', e)
        print('requests seen:\n' + '\n'.join(seen))
        sys.exit(1)
