#!/usr/bin/env python3
"""Builds the HELINK extension:
  dist/chromium/              the folder the Chromium browsers load (with the key that fixes its ID)
  dist/helink-chromium.zip    the same, packed (HELINK embeds it and unpacks it for the user)
  dist/helink-chromium-store.zip  for the Chrome Web Store / Edge Add-ons (no key: the store gives the ID)
  dist/firefox/, dist/helink-firefox.xpi   for Firefox (to be signed by addons.mozilla.org)
usage: build.py VERSION [EMBED_DIR]"""
import json, os, shutil, sys, zipfile

here = os.path.dirname(os.path.abspath(__file__))
src = os.path.join(here, 'src')
dist = os.path.join(here, 'dist')
version = sys.argv[1] if len(sys.argv) > 1 else '2.3.1'
embed = sys.argv[2] if len(sys.argv) > 2 else None
SKIP = {'.DS_Store'}

def files(root):
    for d, _, fs in os.walk(root):
        for f in sorted(fs):
            if f in SKIP:
                continue
            p = os.path.join(d, f)
            yield p, os.path.relpath(p, root).replace(os.sep, '/')

def make(name, manifest_file, extra=None, drop=()):
    out = os.path.join(dist, name)
    shutil.rmtree(out, ignore_errors=True)
    shutil.copytree(src, out, ignore=shutil.ignore_patterns(*SKIP))
    with open(os.path.join(here, manifest_file), encoding='utf-8') as f:
        m = json.loads(f.read().replace('@VERSION@', version))
    if extra:
        m.update(extra)
    for k in drop:
        m.pop(k, None)
    with open(os.path.join(out, 'manifest.json'), 'w', encoding='utf-8') as f:
        json.dump(m, f, ensure_ascii=False, indent=2)
    return out

def pack(folder, zpath):
    if os.path.exists(zpath):
        os.remove(zpath)
    with zipfile.ZipFile(zpath, 'w', zipfile.ZIP_DEFLATED) as z:
        for p, rel in files(folder):
            z.write(p, rel)

os.makedirs(dist, exist_ok=True)
key = open(os.path.join(here, 'keys', 'key.txt')).read().strip()
chromium = make('chromium', 'manifest.chromium.json', {'key': key})
pack(chromium, os.path.join(dist, 'helink-chromium.zip'))
store = make('chromium-store', 'manifest.chromium.json')
pack(store, os.path.join(dist, 'helink-chromium-store.zip'))
shutil.rmtree(store)
firefox = make('firefox', 'manifest.firefox.json')
pack(firefox, os.path.join(dist, 'helink-firefox.xpi'))
# Firefox installs only packages signed by addons.mozilla.org: once this version is signed
# (see SIGNING.md), put the signed file in signed/helink-firefox-VERSION.xpi and HELINK
# carries that one.
signed = os.path.join(here, 'signed', 'helink-firefox-%s.xpi' % version)
xpi = os.path.join(dist, 'helink-firefox.xpi')
if os.path.exists(signed):
    shutil.copy(signed, os.path.join(dist, 'helink-firefox-signed.xpi'))
    xpi = signed
    print('Firefox: using the signed package', signed)
else:
    print('Firefox: no signed package for', version, '(Firefox will refuse the unsigned one; see SIGNING.md)')
if embed:
    os.makedirs(embed, exist_ok=True)
    shutil.copy(os.path.join(dist, 'helink-chromium.zip'), os.path.join(embed, 'extension-chromium.zip'))
    shutil.copy(xpi, os.path.join(embed, 'helink-firefox.xpi'))
print('extension', version, 'built in', dist)
