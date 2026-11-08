#!/bin/sh
# Builds what the extension's Firefox test needs and runs it (Linux, Firefox + geckodriver).
# usage: FIREFOX_BIN=/path/to/firefox GECKODRIVER=/path/to/geckodriver sh extension/test/run_firefox.sh
set -e
cd "$(dirname "$0")/../.."
: "${FIREFOX_BIN:?set FIREFOX_BIN}" "${GECKODRIVER:?set GECKODRIVER}"
T=$(mktemp -d /tmp/helink-ff-XXXX)
python3 extension/build.py 2.2.1 >/dev/null
go build -o "$T/helink-host" ./host
go test -c -o "$T/helinkapp" .
E2E_TMP="$T" HELINK_HOST_BIN="$T/helink-host" HELINK_APP_BIN="$T/helinkapp" python3 extension/test/e2e_firefox.py
