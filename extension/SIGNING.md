# Publishing the HELINK extension

HELINK works with the extension in two ways:

* **From HELINK's own folder (no store).** HELINK puts the extension in
  `%LOCALAPPDATA%\HELINK\extension\chromium`; the user adds it once with *Developer mode → Load
  unpacked* (HELINK's window "Add HELINK to your browser" walks them through it). Its ID is fixed
  (`bmkpbbkgkcbhaidhefjkhanimkfaefon`) by the public key in `keys/key.txt`, so HELINK's helper
  already accepts it. This works today in Chrome, Edge, Brave, Opera, Vivaldi and Yandex.
* **From the stores.** A store copy installs in one click and updates itself. Publishing is done
  once per browser family with your own developer accounts, as below.

Files in this package:

| File | For |
|---|---|
| `helink-2.2.1-chrome-edge-store.zip` | Chrome Web Store and Microsoft Edge Add-ons (no `key`: the store gives the ID) |
| `helink-2.2.1-firefox-unsigned.xpi` | addons.mozilla.org, to be signed |
| `helink-2.2.1-firefox-source.zip` | the same Firefox files, unpacked (only if a reviewer asks for the source) |

## 1. Chrome Web Store (Chrome, Brave, Opera, Vivaldi)

1. Open <https://chrome.google.com/webstore/devconsole>, sign in with the Google account that
   will own the extension, and pay the one-time registration fee.
2. *New item* → upload `helink-2.2.1-chrome-edge-store.zip`.
3. Store listing: name *HELINK – Download Manager*, the description, the 128 px icon
   (`src/icons/icon-128.png`), at least one 1280×800 screenshot, category *Productivity*,
   languages English and Arabic.
4. Privacy tab:
   * Single purpose: *Sends the user's downloads and videos to the HELINK download manager
     installed on the same computer.*
   * Permission justifications:
     * `downloads` – to take over the downloads the user starts and hand them to HELINK.
     * `nativeMessaging` – to talk to the HELINK program on the same computer.
     * `cookies` – to give HELINK the cookies of the site a file comes from, so downloads that
       need the user's session keep working. Only for that site, only on the user's computer.
     * `webRequest` and host permissions – to notice the video and audio files a page plays,
       so the HELINK button can offer them.
     * `contextMenus` – the "Download with HELINK" menu item.
     * `storage` – the extension's settings and where the user dragged the video button.
   * Data usage: no data is collected or sent anywhere; everything stays on the computer.
5. Submit for review. When it is published, copy its **item ID** (32 letters) into
   `extension/store-ids.txt` (one ID per line) and build HELINK again: its helper then answers
   the store copy too.

## 2. Microsoft Edge Add-ons

1. Register (free) at <https://partner.microsoft.com/dashboard/microsoftedge>.
2. *Create new extension* → upload the same `helink-2.2.1-chrome-edge-store.zip`, fill the
   listing with the same texts as above, submit.
3. When it is published, add its ID to `extension/store-ids.txt` too, and build HELINK again.

## 3. Firefox (addons.mozilla.org)

Firefox installs only add-ons signed by Mozilla. Signing is free.

1. Create an account at <https://addons.mozilla.org/developers/>.
2. *Submit a New Add-on* and choose:
   * **On this site** – a public listing on addons.mozilla.org (users install it from there), or
   * **On your own** – Mozilla signs the file and you give it out yourself (the quickest).
3. Upload `helink-2.2.1-firefox-unsigned.xpi`. The code is not minified, so no source upload is
   needed (`helink-2.2.1-firefox-source.zip` is there if a reviewer asks).
4. Download the signed `.xpi`, save it as `extension/signed/helink-firefox-2.2.1.xpi` and build
   HELINK again: HELINK then carries the signed copy, and its button *Install in Firefox* works
   in one click.

The same from the command line, with API keys from
<https://addons.mozilla.org/developers/addon/api/key/>:

    npx web-ext sign --source-dir dist/firefox --channel unlisted \
        --api-key "user:12345:67" --api-secret "…" --artifacts-dir signed
    mv signed/*.xpi signed/helink-firefox-2.2.1.xpi

Each new version of the extension is signed again (the version in the file must be new).

## The helper's IDs

The helper (`helink-host.exe`) answers only these extensions:

* Chromium: `bmkpbbkgkcbhaidhefjkhanimkfaefon` (from HELINK's folder) and the IDs listed in
  `extension/store-ids.txt` (added at build time by `build.sh`).
* Firefox: `helink@helaltech.com` (the ID inside the Firefox manifest; it does not change when
  Mozilla signs it).

The extension's ID comes from the public key in `keys/key.txt` (the `key` field HELINK's own
copy carries), so building needs nothing else; no private key is used or shipped.
