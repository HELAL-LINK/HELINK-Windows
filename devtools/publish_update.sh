#!/bin/sh
# Publishes a release of HELINK on its page, github.com/HELAL-LINK/HELINK-Windows, which is
# also where every HELINK finds its updates (update.go). Two commits on main:
#   1. the installer and the portable zip of release/, at the root of the repository (the previous
#      version's leave the branch; the commit that added them keeps them, so their addresses work
#      for good);
#   2. latest.json, signed with the update key (devtools/updsign), naming those files at commit 1,
#      and README.md, the download page, made again from devtools/download-page.md with the notes
#      of docs/release-notes/<VERSION>.ar.txt and .en.txt (assets/: the logo, the pictures).
# Both go up in one push of main, so HELINK never reads a latest.json whose files are not there.
# (A tag v<VERSION> is made too, and pushed when the connection allows it; nothing needs it.)
# Run again for the same version with the same files, it reuses the commit that holds them.
# usage: ./release.sh, then
#        devtools/publish_update.sh <clone of HELINK-Windows> [key file] [--no-push]
# The key file (default ~/helink-release-keys/update-ed25519.key) is the private half of the key in
# update.go: keep it safe, and never in a repository or a package. To make a later version, raise
# VERSION in build.sh and release.sh, write docs/release-notes/<VERSION>.ar.txt and .en.txt (one
# line each thing new), ./release.sh, then this. PUBLISH_TRAILER: lines put at the end of the
# commit messages, if any.
set -e
cd "$(dirname "$0")/.."
SRC=$(pwd)
REPO=${1:?usage: devtools/publish_update.sh <clone of HELINK-Windows> [key file] [--no-push]}
KEY=${2:-$HOME/helink-release-keys/update-ed25519.key}
PUSH=1
[ "$3" = "--no-push" ] && PUSH=0
VERSION=$(sed -n 's/^VERSION=//p' release.sh)
GH=HELAL-LINK/HELINK-Windows
SETUP=HELINK-$VERSION-Setup.exe
PORTABLE=HELINK-$VERSION-portable.zip
for f in "release/$SETUP" "release/$PORTABLE" "docs/release-notes/$VERSION.ar.txt" "docs/release-notes/$VERSION.en.txt"; do
  [ -f "$f" ] || { echo "missing $f (./release.sh first, and the notes)"; exit 1; }
done
[ -d "$REPO/.git" ] || { echo "$REPO is not a clone of $GH"; exit 1; }

# The key must be the one HELINK knows, or every HELINK would turn this update down.
PUB=$(go run ./devtools/updsign -key "$KEY" -pubkey)
grep -q "\"$PUB\"" update.go || { echo "$KEY is not HELINK's update key (update.go has another)"; exit 1; }

commit() { # commit <title> <body>
  printf '%s\n\n%s\n%s' "$1" "$2" "${PUBLISH_TRAILER:+
$PUBLISH_TRAILER
}" | git commit -q -F -
}

cd "$REPO"
git pull -q --ff-only origin main || true
# 1. The files. The same version must be the same files (a new build is a new version).
if [ -f "$SETUP" ] || [ -f "$PORTABLE" ]; then
  if ! cmp -s "$SETUP" "$SRC/release/$SETUP" || ! cmp -s "$PORTABLE" "$SRC/release/$PORTABLE"; then
    echo "$VERSION is published with other files: raise VERSION in build.sh and release.sh"; exit 1
  fi
else
  for old in HELINK-*-Setup.exe HELINK-*-portable.zip; do
    if [ -f "$old" ]; then git rm -q "$old"; fi
  done
  cp "$SRC/release/$SETUP" "$SRC/release/$PORTABLE" .
  git add "$SETUP" "$PORTABLE"
  commit "HELINK $VERSION: its files" "The installer and the portable copy of HELINK $VERSION."
fi
FILES=$(git log -1 --format=%H -- "$SETUP")
git merge-base --is-ancestor "$FILES" HEAD
git ls-tree -r --name-only "$FILES" | grep -qx "$PORTABLE"

# 2. latest.json naming them at that commit, and the download page.
(cd "$SRC" && go run ./devtools/updsign -key "$KEY" -version "$VERSION" -setup "release/$SETUP" \
  -portable "release/$PORTABLE" -notes "docs/release-notes/$VERSION" -repo "$GH" -ref "$FILES" -out release/latest.json)
cp "$SRC/release/latest.json" .
mkdir -p assets
cp "$SRC/winres/icon-256.png" assets/helink.png
for p in window-ar window-en course-ar course-en; do
  if [ -f "$SRC/docs/screenshots/$p.png" ]; then cp "$SRC/docs/screenshots/$p.png" "assets/$p.png"; fi
done
python3 - "$SRC" "$VERSION" "$GH" "$SETUP" "$PORTABLE" "$FILES" <<'EOF'
import os, sys, datetime
src, version, gh, setup, portable, ref = sys.argv[1:7]
page = open(os.path.join(src, 'devtools', 'download-page.md'), encoding='utf-8').read()
def notes(lang):
    lines = [l.strip() for l in open(os.path.join(src, 'docs', 'release-notes', version + '.' + lang + '.txt'), encoding='utf-8')]
    return '\n'.join('- ' + l for l in lines if l and not l.startswith('#'))
def mb(name):
    return max(1, round(os.path.getsize(name) / (1 << 20)))
def url(name):
    return 'https://github.com/%s/raw/%s/%s' % (gh, ref, name)
words = {
    '@VERSION@': version, '@SETUP@': setup, '@PORTABLE@': portable,
    '@SETUP_URL@': url(setup), '@PORTABLE_URL@': url(portable),
    '@SETUP_MB_AR@': '%d ميجابايت' % mb(setup), '@PORTABLE_MB_AR@': '%d ميجابايت' % mb(portable),
    '@SETUP_MB_EN@': '%d MB' % mb(setup), '@PORTABLE_MB_EN@': '%d MB' % mb(portable),
    '@NOTES_AR@': notes('ar'), '@NOTES_EN@': notes('en'),
    '@DATE@': datetime.date.today().isoformat(),
}
for k, v in words.items():
    page = page.replace(k, v)
for name in ('window-ar', 'window-en', 'course-ar', 'course-en'):
    if not os.path.exists(os.path.join('assets', name + '.png')):
        page = '\n'.join(l for l in page.split('\n') if 'assets/%s.png' % name not in l)
open('README.md', 'w', encoding='utf-8').write(page)
EOF
git add -A
if git diff --cached --quiet; then
  echo "latest.json and the page are as they were"
else
  commit "HELINK $VERSION" "latest.json (signed) offers HELINK $VERSION to every HELINK from 2.3 on, its files at
$FILES; the download page names them too."
fi
git tag -f "v$VERSION" >/dev/null
git log --stat -2 | cat
if [ $PUSH = 1 ]; then
  git push origin HEAD:main
  git push -f origin "refs/tags/v$VERSION" 2>/dev/null || echo "(the tag v$VERSION stays here: this connection does not take tags; nothing needs it)"
  echo "published: https://github.com/$GH (HELINK reads https://raw.githubusercontent.com/$GH/main/latest.json)"
else
  echo "not pushed (--no-push): git -C $REPO push origin HEAD:main"
fi
