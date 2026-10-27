# The "Send to MetaRush" browser extension

A small extension for Chrome, Brave and Edge (Manifest V3; Safari notes below). On YouTube, TikTok and X video pages
it adds a **Send to MetaRush** button. It sends the video to MetaRush on this computer in one of two ways:

| Choice | What happens | When to use it |
|---|---|---|
| **Import the original** | MetaRush downloads the video itself, like `metarush import <link>`: best quality up to 4K, saved as MP4. | Usually. It is fast and gives the best quality. |
| **Save from this browser** | The video is fetched in your own signed-in browser and uploaded to MetaRush. If the page plays a plain file, that file is downloaded with your sign-in. If it streams in pieces (YouTube, most of X), the playing video is **recorded in real time** while it plays, so a 10-minute video takes 10 minutes. | When MetaRush's own download is blocked (a bot check, a sign-in wall, a members-only video you can watch). |

Copy-protected video (DRM: rentals, paid films, some music videos) is detected and refused. MetaRush never works
around copy protection.

Either way the clip arrives in the inbox with where it came from — the site, the address, the account, the upload
date and the title the page shows — and goes through the normal pipeline (duplicates, preview, transcript, speakers).

## Send this page

On any web page, click the extension's toolbar button and choose **Send this page**: a picture of what is on screen
and the page's text (with its title, description, theme colour and language) go to the workspace's resources as a
**web page snapshot**, dated, like `metarush resources add <address>` makes (see [resources](resources.md#web-page-snapshots)).
It is the way to bring in a page as you see it: signed in, after you clicked through to the right state, or one
MetaRush's own snapshot browser can't reach. Sending the same page again adds a new dated version (the same picture
again is nothing new). Graphics and slides use it by name (`slide.screenshot` with `image "acme-com-pricing"`).

The extension reads the page only when you click (Chrome's `activeTab`); it never sees other tabs or pages you don't
send. Where the browser doesn't let extensions read a page (its own pages, the extension store) it says so, or sends
the picture alone.

## MetaRush on another computer (Tailscale)

When MetaRush runs on another of your computers (remote access: `metarush remote enable` there), the extension
can send to it over your Tailscale network. In the popup, open **MetaRush address** and use that computer's
address, `https://<name>.<tailnet>.ts.net` (Settings → Browser extension, opened from that address, shows it and
offers the extension as a download, since the folder MetaRush keeps is on the other computer). Only
`http://127.0.0.1:<port>`, `http://localhost:<port>` and `https://….ts.net` addresses are taken; the token is only
ever sent to the address it paired with. What you send is yours in MetaRush: the browser is paired for whoever made
the code (below).

## Install (load unpacked)

1. Find the extension's folder on this machine:

   ```bash
   metarush extension path
   ```

   (Settings → Browser extension shows the same folder with a Copy button.) MetaRush keeps it up to date there.
2. Open `chrome://extensions` (Brave: `brave://extensions`, Edge: `edge://extensions`) and turn on **Developer mode**.
3. Choose **Load unpacked** and pick that folder. Pin **Send to MetaRush** to the toolbar if you like.
4. After a MetaRush update, press the reload button on the extension's card.

## Pair it with MetaRush

The extension can only talk to MetaRush after you pair it, once per browser:

1. In MetaRush, signed in as yourself, open **Settings → Browser extension → Pair a browser** (or, on the computer
   that runs MetaRush, `metarush extension pair` in your own terminal: that pairs it for the owner). You get a code
   like `K7QD-3MXA`. It works once, for 10 minutes, and the browser it pairs is yours.
2. Click the extension's toolbar button, type the code and choose **Pair**. (Only change the address if MetaRush runs
   on another port on this computer, for example a development copy.)

## Whose a browser is

A pairing belongs to the person who made the code. Everything that browser sends — videos, imports, page snapshots —
is credited to them, like anything else they add (in the audit trail, the activity log and "Added by"). Several
people's codes can wait at once.

Settings → Browser extension shows **Your browsers**, each with **Remove**; an owner also sees **Everyone's
browsers**, with whose each is, and can remove any of them. `metarush extension list` shows the same (at the host's
terminal you are the owner); `metarush extension revoke <id>` cuts one off at once, `--all` every one of yours and
`--everyone` every browser (owners).

A browser stops working the moment its person's account is turned off, removed or not yet joined (and, with "Just
me on this computer" sign-in, every account but the owner's): each request it makes is checked, and it says "This
browser was paired by Maya's account, which is turned off in MetaRush". It works again when the account is turned
back on. Browsers paired before pairings had a person belong to the person this MetaRush was set up for (its owner).

## What a paired browser can and can't do

The extension gets a token that can **only send things in**: start an upload into the inbox, send that upload's
bytes, ask MetaRush to import a link, and send the page on screen. It can't read anything back — no clips, projects,
jobs, resources, activity or settings — and it works only from the extension that paired (its
`chrome-extension://…` address). A browser paired before version 0.2.0 keeps sending videos; for **Send this page**
it says "pair it again" (a new code from Settings): MetaRush never widens what an existing pairing may do. See
[security](security.md#browser-extension).

## Safari

Safari runs web extensions only from an app bundle. To try it on a Mac with Xcode:

```bash
xcrun safari-web-extension-converter "$(metarush extension path)" --app-name "Send to MetaRush" --macos-only
```

Build and run the generated project, then enable the extension in Safari → Settings → Extensions (for an unsigned
build, first turn on Develop → Allow Unsigned Extensions). Recording uses `captureStream`, which Safari supports from
version 17; direct downloads need the site's files to allow it. Safari support is untested.

## Troubleshooting

| What you see | What to do |
|---|---|
| "MetaRush isn't answering on this computer" | Start it: `metarush dashboard`. |
| "This browser isn't paired with MetaRush" | Pair again with a new code. |
| "This browser was paired before it could send pages" | Pair again with a new code from Settings → Browser extension (videos keep working meanwhile). |
| "This browser was paired by …'s account, which is turned off" | Pair it again as yourself, with a code from Settings → Browser extension; or ask an owner to turn that account back on. |
| "The browser didn't let the extension see this page" | Click the toolbar button while the page is open and in front, then **Send this page**. |
| "The site refused to hand over the video file" | Use the recording, or **Import the original**. |
| "This browser can't record this video" | The site blocks recording; use **Import the original**. |
| The recording has no sound | Keep the tab's sound on while it records (a muted tab records silence in some browsers). |

## For developers

The files are in `extension/` (embedded in the binary by `extension/embed.go`): `background.js` (the service worker,
the only part that talks to MetaRush), `content.js` (the button and panel), `lib/detect.js` (page detection, shared
with the tests), `lib/page.js` (what Send this page reads: run in the tab by `chrome.scripting`), `popup.*` (pairing,
Send this page), `lib/address.js` (the MetaRush addresses it pairs with). Permissions: `storage`, `activeTab` and `scripting`;
host access to MetaRush on this computer (`127.0.0.1`, `localhost`) or on another over Tailscale (`https://*.ts.net`)
and the three video sites and their media hosts. Tests: `node --test extension/test/detect.test.cjs extension/test/page.test.cjs extension/test/address.test.cjs`;
`web/scripts/linkimport-e2e.mjs` loads the extension in Chromium and pairs it with a test daemon, and
`web/scripts/resources2-e2e.mjs` sends a stub page with it.
