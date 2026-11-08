//go:build windows

package main

import _ "embed"

// Put in place by build.sh: the browsers' helper, and the extension for the Chromium
// browsers (unpacked into %LOCALAPPDATA%\HELINK\extension\chromium) and for Firefox.
var (
	//go:embed embed/helink-host.exe
	hostExe []byte
	//go:embed embed/extension-chromium.zip
	extChromiumZip []byte
	//go:embed embed/helink-firefox.xpi
	extFirefoxXPI []byte
)
