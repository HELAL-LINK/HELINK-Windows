#!/bin/sh
# Builds HELINK and puts what is delivered in release/:
#   HELINK-VERSION-Setup.exe          the installer (for the user only, no administrator)
#   HELINK-VERSION-portable.zip       HELINK.exe alone, with the guides
#   HELINK-extension-VERSION.zip      the extension for the stores (Chrome/Edge, Firefox) + how to publish it
#   HELINK-VERSION-source.zip         the source, buildable offline (dependencies in third_party)
# usage: ./release.sh [--no-tests]
set -e
cd "$(dirname "$0")"
VERSION=2.4.0
./build.sh "$@"
rm -rf release
mkdir -p release

# The installer.
(cd installer && makensis -V2 -DVERSION=$VERSION -DEXE=../build/HELINK.exe -DOUT=../release/HELINK-$VERSION-Setup.exe helink.nsi)

# HELINK.exe alone.
P=release/HELINK-$VERSION-portable
mkdir -p "$P"
cp build/HELINK.exe "$P/"
cp "docs/README-ar.txt" "$P/طريقة التثبيت والاستخدام.txt"
cp docs/README-en.txt "$P/HELINK-guide-en.txt"
cp docs/THIRD-PARTY-NOTICES.txt "$P/"
(cd release && zip -qr -X "HELINK-$VERSION-portable.zip" "HELINK-$VERSION-portable")
rm -rf "$P"

# The extension, for the stores.
E=release/HELINK-extension-$VERSION
mkdir -p "$E"
cp extension/dist/helink-chromium-store.zip "$E/helink-$VERSION-chrome-edge-store.zip"
if [ -f extension/dist/helink-firefox-signed.xpi ]; then
  cp extension/dist/helink-firefox-signed.xpi "$E/helink-$VERSION-firefox-signed.xpi"
fi
cp extension/dist/helink-firefox.xpi "$E/helink-$VERSION-firefox-unsigned.xpi"
cp extension/SIGNING.md "$E/"
(cd extension/dist && zip -qr -X "../../$E/helink-$VERSION-firefox-source.zip" firefox)
(cd release && zip -qr -X "HELINK-extension-$VERSION.zip" "HELINK-extension-$VERSION")
rm -rf "$E"

# The source. The extension's private key stays here (the public key in keys/key.txt keeps
# the extension's ID; nothing is signed with the private one).
S=release/src/helink-windows
mkdir -p "$S"
tar --exclude=./build --exclude=./release --exclude=./nazzelha --exclude=./embed --exclude=./extension/dist \
    --exclude='./extension/keys/*.pem' -cf - . | (cd "$S" && tar -xf -)
mkdir -p "$S/third_party"
cp -r ../deps/jchv_go-webview2 "$S/third_party/go-webview2"
cp -r ../deps/jchv_go-winloader "$S/third_party/go-winloader"
mkdir -p "$S/third_party/x-sys"
cp -r ../deps/golang_sys/windows "$S/third_party/x-sys/windows"
cp -r ../deps/golang_sys/cpu "$S/third_party/x-sys/cpu"
cp ../deps/golang_sys/go.mod ../deps/golang_sys/LICENSE ../deps/golang_sys/PATENTS "$S/third_party/x-sys/" 2>/dev/null || true
# The tool that writes the icon and the manifest into HELINK.exe (winres/gen).
cp -r ../deps/tc-hib_winres "$S/third_party/winres"
cp -r ../deps/nfnt_resize "$S/third_party/nfnt-resize"
mkdir -p "$S/third_party/x-image" "$S/third_party/x-text"
cp -r ../deps/golang_image/bmp "$S/third_party/x-image/bmp"
cp ../deps/golang_image/go.mod ../deps/golang_image/LICENSE ../deps/golang_image/PATENTS "$S/third_party/x-image/"
cp ../deps/golang_text/go.mod ../deps/golang_text/LICENSE ../deps/golang_text/PATENTS "$S/third_party/x-text/"
# Their git folders are not part of the source.
find "$S/third_party" -name .git -prune -exec rm -rf {} +
sed -i -e 's|=> ../../../deps/tc-hib_winres|=> ../../third_party/winres|' \
       -e 's|=> ../../../deps/nfnt_resize|=> ../../third_party/nfnt-resize|' \
       -e 's|=> ../../../deps/golang_image|=> ../../third_party/x-image|' \
       -e 's|=> ../../../deps/golang_text|=> ../../third_party/x-text|' "$S/winres/gen/go.mod"
sed -i -e 's|=> ../deps/jchv_go-webview2|=> ./third_party/go-webview2|' \
       -e 's|=> ../deps/jchv_go-winloader|=> ./third_party/go-winloader|' \
       -e 's|=> ../deps/golang_sys|=> ./third_party/x-sys|' "$S/go.mod"
(cd release/src && zip -qr -X "../HELINK-$VERSION-source.zip" helink-windows)
rm -rf release/src
ls -l release
