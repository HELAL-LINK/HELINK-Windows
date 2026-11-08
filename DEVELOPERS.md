> To build offline, take `HELINK-<version>-source.zip` from the Releases page: it also holds `third_party/` (the Go dependencies this `go.mod` points at). The page itself (README.md), `latest.json` and `assets/` are what HELINK reads and shows.

# HELINK 2.3 for Windows

A complete download manager for Windows 11 and 10 (64-bit): files in parts over many connections
with resume, downloads taken from the browsers through the HELINK extension, and videos from
YouTube, Facebook, Instagram, TikTok, X and the sites yt-dlp knows, in the quality chosen or as MP3,
with or without their music (as well as files already on the computer), and whole YouTube
courses and playlists at once. HELINK updates itself from its page on GitHub, when the user says so.
Interface in Modern Standard Arabic and English. Go + WebView2, one `HELINK.exe`; an installer
(`installer/helink.nsi`) or the portable exe.

## The pieces

```
browser ── HELINK extension (extension/src) ── native messaging ── helink-host.exe (host/)
                                                                        │ ipc (127.0.0.1 + token)
                                                                        ▼
                         HELINK.exe: engine (app.go, filedl.go, filetask.go…) + window (ui/index.html)
```

- **File engine** – `filedl.go`: HTTP/1.1 only (each part is its own TCP connection), the first
  request `Range: bytes=0-`, then parts with closed ranges; dynamic splitting (the part with the
  most time left is split), up to 16 connections, sparse `data.part` written with `WriteAt`,
  `state.json` every 2 s (fsync'd) so a download resumes after a crash, `If-Range` against a
  changed file, cookies sent the way the browser would (domain/path/secure/expiry), manual
  redirects, the system proxy. Errors have codes (`network`, `forbidden`, `gone`, `server`,
  `page_not_file`, `changed`, `disk`, `permission`…) that `filetask.go` turns into messages.
- **Downloads list** – `tasks.go`: every download (a file, `type: "file"`, or a video read by
  yt-dlp) is a `DownloadTask` kept in `downloads.json`; `MaxParallel` run at once (3 by default),
  the rest wait. Kinds (video, music, pictures, programs, documents, compressed, other) from the
  extension or the name; each kind in its own folder of the save folder (`Video`, `Music`,
  `Pictures`, `Programs`, `Documents`, `Compressed`, `Other`; `noCategoryDirs` turns it off). A file's cookies are kept apart (`work/cookies/<id>.json`, 0600) and
  dropped when it ends.
- **"New download" window** – `filetask.go`: a link (pasted, or sent by the browser) is asked
  about first (`file` events: `checking`, then `ready` with name, size, kind, folders, resumable,
  an unfinished download of the same file to continue with the new link; or `media` for a page
  with videos; or `error`). `manager.go`: settings and list-wide commands.
- **Browsers** – `browser.go`: the extension's messages (`hello`, `download`, `link`, `video`,
  `qualities`, `get`, `setSettings`…). `browserread.go`: the video button's menu: `qualities` reads
  the video (kept 15 minutes, one reading for repeated asks) and answers its qualities; `get`
  downloads the one chosen at once and emits `browserTask`. A browser download is probed here first (20 s); HELINK takes it only when it
  gets the same file, else the browser keeps it. `ext.json` lets the helper answer `hello` while
  HELINK is closed; the helper starts HELINK (`--background`) when a download needs it, and notes
  each visit in `seen.json` (shown in "Add HELINK to your browser").
- **ipc/** – the app ⇄ helper channel: TCP on 127.0.0.1, a random token in `ipc.json`
  (`%LOCALAPPDATA%\HELINK`), and native-messaging framing. `--quit` (the installer) asks the
  running HELINK to close through it.
- **Windows** – `main_windows.go` (window, bindings `nz_*`, compact "new download" window in
  front of the browser, the small window of downloads chosen in the browser's menu near the clock,
  shown without taking the keyboard and sized by the page, events queued until the page is ready),
  `winui_windows.go` (the page's life in WebView2: made when the window first shows, not while
  HELINK starts hidden near the clock; WebView2 told when the window shows or hides; checked at each
  showing (WebView2's window there and not hung, the page answering `nzPing`); loaded again or made
  anew when it broke, also on WebView2's `ProcessFailed`; closed after 20 minutes hidden; the window
  painted like the page meanwhile; a watchdog logs the stacks if the window's thread stops), `tray_windows.go` (icon near the
  clock, menu, balloons), `integrate_windows.go` (helper and extension extracted to
  `%LOCALAPPDATA%\HELINK`, native-messaging registration for Chrome, Edge, Brave, Opera, Vivaldi,
  Yandex, Chromium, Firefox, Waterfox, LibreWolf; start with Windows; `--unregister`),
  `embed_windows.go` (the helper and the extension packages inside HELINK.exe).
- **Videos** – `app.go`, `formats.go`, `sites.go`, `tools.go` as in 1.x: yt-dlp (nightly), Deno and
  FFmpeg downloaded on first start into `%LOCALAPPDATA%\HELINK\tools` and kept up to date. Pages of
  other sites are read by yt-dlp too; when nothing is found, what the page played (seen by the
  extension) is offered instead. The five known sites never get the browser's headers, YouTube
  never gets cookies.
- **Music removal** – the MDX-Net voice model Kim_Vocal_2 (UVR) in pure Go: `mdxconv.go` reads the
  downloaded ONNX model (67 MB from GitHub, SHA-256 checked), checks its graph against the
  network, folds the batch norms of the transposed convolutions and writes HELINK's fp16 weight
  file (33 MB, SHA-256 checked); `mdxnet.go` is the TFC-TDF U-Net (5 levels, every layer a matrix
  product in 6×16 tiles over all cores, below-normal priority on Windows), `mdxkernel_amd64.s` its
  AVX2/FMA tiles, `mdxwino.go` + `mdxwino_amd64.s` (generated by `devtools/gen_wino.py`) the 3×3
  convolutions of the first three levels the Winograd F(4×4,3×3) way, plain Go elsewhere;
  `mdxremover.go` the 7680-point STFT (`mdxfft.go`, mixed radix) as torch does it, streaming UVR's
  chunks of 261120 samples with their kept parts faded into each other over 8192 samples (no click
  where they meet). It matches ONNX Runtime to 119 dB, and the whole stream the reference to 65 dB.
  `nomusic.go` runs ffmpeg → remover → ffmpeg over pipes (a video's picture, subtitles, chapters
  copied; the format kept: MP3 with its cover and tags, M4A, OGG/Opus, FLAC, WAV, MP4, MKV, WEBM,
  AVI; others become MP3 or MKV), one file at a time. `septasks.go`: downloads "without music"
  (`noMusic`; a finished download is marked in its job folder so a pause never downloads it again),
  and files of the computer (`type: "sep"`, the copy made next to the original). Downloads and
  music removal are two lanes of the queue: a file waiting for the remover takes no download's place.
- **Courses and playlists** – `playlist.go`: a YouTube list (`playlist?list=…`, or a video opened
  from one: `listUrl` in its `info`, "Download the whole list"; the extension's menu has the same
  button) is read whole with `yt-dlp -J --flat-playlist` (titles and lengths, one request a hundred
  videos, at most 1000 shown) into an `info` event with `status: "list"`: its videos, which can't be
  downloaded (private, removed, members only, not out yet), which are in the downloads already as
  video or as sound (`have`: the window leaves those unticked, so a course pasted again gets its new
  lessons), and one set of qualities for all (`v1080`…`v360` as ceilings: `-S res:N,vcodec:h264,
  acodec:aac`; `mp3-320/192/128`). `DownloadList` adds each video ticked as a download of its own
  in `<save>/Video|Music/<list title>/`, named `NN - title` in the list's order (the same video in
  the same quality, not finished yet, is not added twice). Channels (`@name`) are not lists: the
  `channel` error says to open one of its playlists.
- **Updates** – `update.go`: `latest.json` on GitHub (`raw.githubusercontent.com/HELAL-LINK/
  HELINK-Windows/main/latest.json`, then the same file through jsDelivr) is `{"manifest": base64,
  "sig": base64}`: the manifest (app, version, date, notes in ar/en, `setup` and `portable` files with
  their addresses, size and SHA-256) signed with Ed25519; only the public key is in the program, so
  a changed or forged file is ignored (`update_verify`). Checked 20 s after the start, every 12 hours
  (the wall clock decides: a computer that slept looks when it wakes), every 2 minutes while offline,
  15 minutes after any other failure; `About › Check for updates` at once. A newer version opens the
  update's window once (or a notification from the icon when the window is closed, which opens it);
  "Not now" asks again a day later, the toolbar's "Update" button and the icon's menu keep it at
  hand. "Update now" downloads the file this copy needs (resumable, `%LOCALAPPDATA%\HELINK\update`),
  checks size and SHA-256, notes the downloads running (`ResumeAfterUpdate`), and
  (`update_windows.go`) an installed copy (next to `Uninstall HELINK.exe`, or where the installer's
  `InstallDir` says) starts the installer `/S /RELAUNCH` (it closes HELINK with `--quit`, waits for
  `HELINK.exe` to be free, installs quietly without touching the desktop shortcut, and opens the new
  one with `--updated`; it opens the old one again if it couldn't install); a portable copy renames
  its running `HELINK.exe` to `.old`, puts the zip's `HELINK.exe` in its place and starts it with
  `--updated=<pid>` (it waits up to 30 s for the old one to close). Either way the new HELINK
  continues the downloads, says it was updated (or that the update didn't happen), and removes what
  the update left. A copy in a folder Windows won't let it change gets `update_manual` and the page.
- **The page** – `ui/index.html`: the whole interface (list, sections, details with the parts bar,
  dialogs, settings, browser setup, About), Arabic and English, light and dark like Windows. Fonts
  and the About photo are embedded at build time (`ui.go`). `ui/mock.js` is a fake engine for
  previews and browser tests, never shipped.
- **Extension** – `extension/`: one source for Chromium (MV3, service worker) and Firefox (MV2,
  persistent background). Catches downloads (`onDeterminingFilename` / `onCreated`), the video
  button (shadow DOM, draggable, place kept per site; its menu of qualities asks HELINK and starts the
  download chosen; TikTok's feed watched so the button follows the video playing), the page's media (`webRequest`), menus,
  toolbar window, settings page, Arabic and English. A copy added unpacked from HELINK's folder
  restarts itself once when HELINK puts a newer version there (`reloadIfUpdated`), and its messages
  carry its version (`ext`), so HELINK can tell a browser still running an older one to restart.
  The Chromium ID is fixed by the public key in
  `extension/keys/key.txt`. Publishing: `extension/SIGNING.md`.

## Build (Linux, macOS or Windows; Go 1.24, Python 3)

    ./build.sh               # extension, helper, resources, HELINK.exe, Windows test binaries, Linux tests
    ./release.sh             # the same, then release/: Setup.exe (NSIS), portable zip, extension, source

`release.sh` needs `makensis` (NSIS 3) and `zip`. Store IDs of the extension, once published, go in
`extension/store-ids.txt`; a Firefox package signed by Mozilla in
`extension/signed/helink-firefox-VERSION.xpi`. The source package carries its dependencies in
`third_party/` (go.mod points there), so it builds offline. HELINK's copy of go-webview2 adds:
a window made without its WebView (`Deferred`), the WebView made again in the same window
(`Rebuild`, waiting at most a given time, failures not fatal), closed (`Release`), reloaded,
told when its window shows (`SetVisible`), WebView2's `ProcessFailed` event, and the window's
background colour and cursor.

## Publishing a version (and its update)

1. Raise `VERSION` in `build.sh` and `release.sh` (the program, the installer and the extension
   take it from there), and write what is new, one line each, in
   `docs/release-notes/<VERSION>.ar.txt` and `.en.txt` (the update's window shows these).
2. `./release.sh`
3. `devtools/publish_update.sh <clone of github.com/HELAL-LINK/HELINK-Windows> [key]` commits
   the installer and the portable zip at the root of the repository, then `latest.json` (signed by
   `devtools/updsign` with the update key, checked against the public key in `update.go`) naming
   them at that first commit (`raw.githubusercontent.com/…/<commit>/<file>`: an address that never
   changes, with no tag needed), with the download page (`README.md` from
   `devtools/download-page.md`, the pictures from `docs/screenshots`), and pushes main once. Every
   HELINK from 2.3 on then offers the update.
4. `HELINK_LIVE_UPDATE=1 go test -count=1 -run TestLiveUpdate -v .` checks what was published, as
   HELINK will read it.

The update key (`update-ed25519.key`, a base64 Ed25519 seed; default place
`~/helink-release-keys/`) is the only way to publish an update HELINK accepts: keep it safe and
backed up, and never in a repository or a package. Lost, HELINK can't be updated any more except by
installing a version with a new key by hand.

## Tests

    go test ./...                       # engine, file downloads (19 server scenarios), list, browser
                                        # helper end to end, videos with yt-dlp/ffmpeg from PATH (Linux)
    go test -race ./...
    HELINK_SEP_ARCHIVES=<dir with the model archives> HELINK_SEP_REF=<numpy/ONNX Runtime references> \
        go test -run 'Sep|NoMusic' -v .   # the remover against the references, every kind of file
                                          # through ffmpeg, downloads without music
    HELINK_LIVE_UPDATE=1 go test -count=1 -run TestLiveUpdate -v .
                                        # what is published on GitHub, read and checked as HELINK does
    sh extension/test/run.sh            # the extension in Chromium (Playwright) with the real helper
    FIREFOX_BIN=… GECKODRIVER=… sh extension/test/run_firefox.sh
                                        # the same in Firefox (Selenium), the add-on loaded as temporary
    NAZZELHA_PREVIEW=/tmp/p.html NAZZELHA_PREVIEW_DIR=<thumbs> go test -run TestWritePreview .
                                        # the page with ui/mock.js, for screenshots and UI tests
    # Windows code paths (registry, tray, Recycle Bin, helper, job objects, the window's page
    # lifecycle with a stand-in for WebView2), e.g. under Wine:
    LC_ALL=C.UTF-8 HELINK_HOST='Z:\…\build\helink-host.exe' FAKETOOL='Z:\…\build\faketool.exe' \
        xvfb-run wine64 build/wintest.exe -test.v
    # (update_windows_test.go: which copy updates how, the installer and the new HELINK.exe started
    # on their own, a running HELINK.exe replaced, the new one waiting for the old one to close)
    # music removal on Windows with the real ffmpeg.exe (LC_ALL=C.UTF-8 for Arabic file names):
    HELINK_SEP_DATA='Z:\…\data' HELINK_SEP_MEDIA='Z:\…\media' xvfb-run wine64 build/wintest.exe -test.run WinNoMusic
