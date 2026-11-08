#!/bin/sh
# Builds HELINK.exe (with the browsers' helper and the extension inside it), the Windows
# test binaries, and runs the Linux tests.
# usage: ./build.sh [--no-tests]
set -e
cd "$(dirname "$0")"
VERSION=2.4.0
export CGO_ENABLED=0
mkdir -p build embed
# The extension (Chromium folder + Firefox package), put where HELINK.exe embeds it.
python3 extension/build.py "$VERSION" embed
# The browsers' helper (a small console program the browsers start).
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o embed/helink-host.exe ./host
(cd winres/gen && go run . .. ../../rsrc_windows_amd64.syso "$VERSION.0")
# The extension's IDs in the stores (Chrome Web Store, Edge Add-ons), once it is published:
# one per line in extension/store-ids.txt. The helper then answers them too.
IDS=""
if [ -f extension/store-ids.txt ]; then
  IDS=$(grep -oE '^[a-p]{32}' extension/store-ids.txt | paste -sd, -)
fi
GOOS=windows GOARCH=amd64 go build -trimpath -ldflags "-H windowsgui -s -w -X main.storeExtensionIDs=$IDS -X main.appVersion=$VERSION" -o build/HELINK.exe .
GOOS=windows GOARCH=amd64 go test -c -o build/wintest.exe .
GOOS=windows GOARCH=amd64 go build -o build/faketool.exe ./devtools/faketool
cp embed/helink-host.exe build/helink-host.exe
ls -l build/HELINK.exe build/helink-host.exe
if [ "$1" != "--no-tests" ]; then
  go vet . ./ipc ./host
  go test -count=1 ./...
fi
