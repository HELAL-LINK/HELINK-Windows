#!/bin/sh
# Builds what the extension's end-to-end test needs and runs it (Linux, Chromium from Playwright).
set -e
cd "$(dirname "$0")/../.."
T=$(mktemp -d /tmp/helink-e2e-XXXX)
python3 extension/build.py 2.2.1 >/dev/null
go build -o "$T/helink-host" ./host
go test -c -o "$T/helinkapp" .
E2E_TMP="$T" HELINK_HOST_BIN="$T/helink-host" HELINK_APP_BIN="$T/helinkapp" node extension/test/e2e.js
