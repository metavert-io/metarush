# MetaRush on Windows 11

MetaRush runs on a Mac or a Linux computer. From Windows 11 you use it **in Edge or Chrome**: the dashboard opens
from that computer over Tailscale, a private network between your own devices. Your footage is uploaded to
that computer, the work (transcripts, previews, renders, Claude Code) happens there, and what you download comes back
to your Windows PC. There is no Windows installer, and the `metarush` commands don't run on Windows: use the
dashboard.

## What you need

- A Mac or Linux computer that runs MetaRush, set up by its owner (docs/installation.md), and turned on.
- Windows 11 with Microsoft Edge or Google Chrome, up to date.
- Tailscale on the Windows PC, signed in to the same tailnet as that computer (the owner can invite you).

## Setting it up

**On the computer that runs MetaRush** (its owner, once — these steps ask before they change anything):

1. In the Tailscale admin console, turn on **DNS → HTTPS Certificates** (and MagicDNS).
2. In a terminal: `metarush remote enable --allow <your Tailscale account> --apply`. It gives MetaRush its address,
   `https://<computer>.<tailnet>.ts.net`, and lists you. `metarush remote status` says when it is ready.
3. Choose how people sign in, in **Settings → People**: with **Tailscale accounts**, listed accounts are signed
   in by themselves; with **email and password**, invite yourself (`metarush users invite <email>`) and use the link.

**On the Windows PC:**

1. Install Tailscale from tailscale.com/download (Windows), sign in, and check it says **Connected**.
2. Open Edge or Chrome and go to `https://<computer>.<tailnet>.ts.net` (the address `metarush remote status`
   printed). The padlock shows: Tailscale gives the address a real certificate.
3. Sign in (by itself with a Tailscale account, or with your email and password).
4. Optional: in Edge, **… → Apps → Install this site as an app**; in Chrome, **⋮ → Cast, save and share → Install
   page as app**. MetaRush then opens in its own window from the Start menu.

## Using it

- **Adding footage:** drag files or a whole folder from File Explorer onto any page, or use **Choose files** /
  **Choose a folder**. Files keep their names (accents too); Windows' own files in a folder — `Thumbs.db`,
  `desktop.ini`, anything in a Recycle Bin — are left out. Big files upload in pieces and carry on after a dropped
  connection; keep the tab open until they finish.
- **Watching and editing:** previews play in the browser; the keyboard shortcuts use **Ctrl** where a Mac uses ⌘
  (press **?** on the timeline to see them all).
- **Claude Code:** the panel runs on the computer that hosts MetaRush, with its Claude account (Settings → Claude Code
  says whose).
- **Downloads** (renders, captions files, a project's receipt, recipes) go to your Downloads folder. Names Windows
  can't keep — with `\ / : * ? " < > |`, or called `CON`, `NUL` and the like — arrive with `-` in place of those
  characters or a `_` in front.
- **Text you paste** with Windows line endings is stored like any other text.
- **Recording narration** in the browser asks for the microphone once (Edge and Chrome remember it per site).

## What doesn't work from Windows (yet)

- The `metarush` commands, the watched folder and linked folders run on the computer that hosts MetaRush; from
  Windows, use the dashboard.
- **Send to an editing app** writes a timeline whose clips point at files on the host; after copying the footage to
  your PC, relink them in your editing app.
- Recognizing faces and what's on screen needs a Mac in the workspace.

## When something goes wrong

| What you see | What to do |
|---|---|
| The address doesn't open | Tailscale on the PC says Connected? Is the host on, with MetaRush running (`metarush remote status` there)? |
| "This account can't sign in here" | Ask the owner to add your Tailscale account (`metarush remote allow <account>`), or to invite your email. |
| Uploads stop on a laptop going to sleep | Wake it and open the dashboard: unfinished uploads carry on where they stopped. |
| A preview doesn't play | Reload the page; the preview copy may still be being made (the clip's row says so). |

## Checking it on Windows

MetaRush's tests play a Windows browser on a Mac (`scripts/windows-e2e.sh`: Chromium with Windows' identity, 1366×768
at 125%, Windows-style file names, folders and line endings; and Microsoft Edge when it is installed). A person
checks the real thing on a Windows 11 PC, in **Edge** and in **Chrome**, against a host with remote access on:

1. Open the address, sign in, and look at Home, a project's Sources, the Edit tab, a version's page and Settings:
   nothing cut off or scrolling sideways at your usual scaling (100%, 125%, 150%).
2. Drag a clip from File Explorer onto a project, then a folder that holds `Thumbs.db` or `desktop.ini`: the clips
   arrive under their names, Windows' files don't.
3. Upload a clip named with an accent (`Café.mp4`); on the host, add one whose file name has a colon or a question
   mark (Windows can't make one). Download both (a clip's original, or a render): File Explorer shows the names.
4. Paste text copied from Notepad into a version's delivery notes (Write them…): saved and shown line by line.
5. On the timeline, press **?**: the shortcuts say Ctrl. Try Ctrl+Z and Ctrl+Shift+Z.
6. Play a review copy; record a narration line with the microphone; ask Claude something in the panel.
7. Install the site as an app (above) and repeat step 1 in its window.

Write down what differs between Edge and Chrome, with screenshots.
