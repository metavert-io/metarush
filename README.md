# MetaRush

**Edit video by talking to Claude Code.** *Any tool. Your style. Your call.*

Drop in raw takes, interviews, B-roll and screen recordings, ask for "an intimate 60-second story that keeps the
vulnerable pause, vertical, with tasteful captions", and a real MP4 comes back: a cut built on what was actually said
and shown, with the reason for every choice, that you can make again. Nudge it by hand on a simple timeline or by
deleting words from the transcript, while collaborators on other computers see each other's edits live. No Premiere,
no lock-in, no surprise bills, and your footage stays on your drives.

**Contents**
- [Highlights](#highlights) · [Install Guide](#install-guide) · [How it fits together](#how-it-fits-together)
- The manual: [Getting set up and finding your way](#getting-set-up-and-finding-your-way) ·
  [Bringing in footage](#bringing-in-footage) · [Working with Claude](#working-with-claude) ·
  [Editing a cut](#editing-a-cut) · [AI services and generation](#ai-services-and-generation) ·
  [Your rules](#your-rules) · [Review and sign-off](#review-and-sign-off) · [Delivering](#delivering) ·
  [Abilities, the catalogue and recipes](#abilities-the-catalogue-and-recipes) · [Running MetaRush](#running-metarush)
- Reference: [Commands you'll use](#commands-youll-use) · [Configuration](#configuration) ·
  [Development](#development) · [License](#license)

## Highlights
Everything you'd reach for in an editor, driven by a conversation:
- **Edit by saying what you want.** Ask Claude Code for the video you have in mind, in a terminal or right in the
  dashboard; it reads every transcript, looks at the frames and comes back with a cut and the reason for every choice.
- **Transcripts with speakers, and editing by the words.** Every clip is written down on your computer, word by word,
  in the language it's spoken in and with who says what; select words and press Delete, or let MetaRush find the ums
  and false starts.
- **Captions in your style.** Six caption looks drawn in your brand's colours and font, set once in your house style,
  and a word you fix in the transcript is fixed in the captions too.
- **Vertical, square and landscape versions.** Vertical and square cuts keep the speaker's face in frame or follow
  someone across the shot, and the best moments become shorts for phones in one click.
- **Footage from your drives or from a link.** Drop files and folders, watch a folder, or paste a YouTube, TikTok or
  X link; duplicates are caught before uploading, and every clip gets a name that says what's in it.
- **A screen and camera recorder with a teleprompter.** Record a window, a tab or your whole screen with your camera
  and microphone, right in the dashboard, while your script scrolls where only you can see it.
- **Cleaner sound, music and AI voices.** Clearer voices made on your computer, music that fits the video and gets
  quieter when someone talks, and narration in your own voice or an AI voice, priced before it's made.
- **Title cards, lower thirds and layout packs.** Vetted graphics in your brand's colours, font and logos, a layout
  pack that says which graphic draws each part, and motion graphics Claude writes when nothing fits, used only once
  a person approves them.
- **Every version kept, with undo.** Every change is a new version and nothing is overwritten; Undo and Redo go back
  through your saves, and when two people change the same clips you choose whose change stays.

And what sets MetaRush apart:
- **Any tool, model or service, brought in as an ability.** A new capability is a small folder (a stock library, a
  model, a service) that runs in a sandbox, reaches only the places it names and runs only after an owner approves
  that exact version.
- **Your rules on every video.** Content Settings per client hold the brand, the house style, house rules and
  sign-offs, what may leave this computer and the promises made to the client, and budgets and daily limits mean no
  surprise bills.
- **Nothing behind your back.** Paid work shows its price first, every project has a receipt, a face or voice is used
  only with that person's recorded consent, AI-made media is marked everywhere, and a version with problems or a
  missing sign-off waits until it's fixed or a person sends it with a note.
- **Your footage stays on your drives.** With the suggested setup your footage and projects stay on this computer,
  and transcription, clearer voices, offline voices and picture clean-up run on models on this computer, free; the
  cloud is a choice you make.
- **Use it from any AI agent.** Claude Code learns MetaRush's skills; Codex, Cursor, Claude Desktop and other MCP
  clients connect through `metarush mcp serve`; and every feature is a command with `--json` output.
- **Free and open source, at cost.** MetaRush is MIT-licensed and makes no AI calls of its own: your Claude plan and
  your own keys do the work, so money goes only to the services you choose, at their prices (larger companies need
  a [Remotion license](#remotion-license)).

## Install Guide
About five minutes on a Mac or Linux computer; MetaRush itself needs no accounts or keys. `metarush setup` offers
what its features use (FFmpeg, Node.js, the Remotion renderer, transcription on this computer) as components,
the big ones ticked for you.

### Remotion license
⚠️ MetaRush is MIT-licensed, but it renders with **Remotion, which is not**. It applies once the Remotion renderer is
added (setup suggests it). Remotion is free for individuals and for organizations of up to 3 people; **larger
companies need a Remotion Company License** before using MetaRush to render.
Each organization that installs MetaRush is responsible for its own license. Details: [docs/licensing.md](docs/licensing.md)
and https://www.remotion.dev/docs/license.

### 1. Get MetaRush
**From source**: the way to get MetaRush until its first release. You need Git, Go 1.27 or newer and Node.js 22 or
newer (on a Mac: `brew install go node`):
```bash
git clone https://github.com/metavert-io/metarush.git && cd metarush   # get the code
./bootstrap.sh                                                          # checks the tools, builds the dashboard and the metarush program; asks before installing anything
export PATH="$PWD/bin:$PATH"                                            # use the metarush you just built
```
`bootstrap.sh` is the developers' path and also sets up what a development checkout uses; `--no-models --no-mongo`
leaves out the transcription models and the database, which `metarush setup` offers in the next step anyway.
`scripts/deploy.sh` installs a checkout on this computer or another one, stamped with its version.

**With Homebrew, or a release archive: with the first release.** Releases will come as a Homebrew cask
(`brew install --cask metavert-io/metarush/metarush`, which also installs FFmpeg and Node.js) and as signed archives
for macOS and Linux (arm64 and x86-64) with checksums. This section will say how once they're out.

### 2. Set it up
```bash
metarush setup                       # storage, a database, the dashboard, the background service, Claude Code's skills, the components
metarush components                  # what each optional component unlocks; metarush components add <name> adds one
metarush doctor                      # check that everything is ready
metarush dashboard                   # open the dashboard (Getting started says what works now)
```
`metarush setup` asks a few questions, each with a suggested answer (press Enter): where your footage and finished
videos are kept (a folder on this computer), where your projects are kept (a database on this computer that
MetaRush downloads from MongoDB and looks after, or MongoDB Atlas, or your own server), the dashboard's port, whether
MetaRush starts when you log in, whether Claude Code learns MetaRush's video skills, and which optional components to
add: MetaRush needs only itself, a place for projects and a storage folder, and the rest — FFmpeg, the Remotion
renderer, transcription on this computer, the link downloader and more — is a checklist with the suggested ones ticked
(`--components`, `--no-components`). It ends by saying what works now with no keys and what a key adds, and opens the
dashboard. Running it again is safe; Settings → Components adds the rest later.

- Unattended (an agent, a script): `metarush setup --yes --json`, with any answer as a flag (`metarush setup --help`).
- `metarush init` is the older, technical way to set up (every database and storage setting as a flag).
- A Mac that renders for your team: see [the example below](#example-an-always-on-mac-mini-for-your-team).
- Or add MetaRush to Claude Code as a plugin: `claude plugin marketplace add metavert-io/metarush`, then
  `claude plugin install metarush@metarush`. It needs the `metarush` program too ([the plugin](docs/plugin.md), also
  for Codex and other AI apps).

### 3. Ask for your first cut
In Claude Code: *"Import ~/Movies/Interviews into a new project called Builder story and show me the three strongest
opening moments."*

### Example: an always-on Mac mini for your team
A Mac nobody sits at can run MetaRush around the clock, taking on renders, transcripts and the other heavy work
while laptops sleep or run on battery, and everyone can open its dashboard from their own computer over Tailscale.
Here it's called `studio-mac`, on a tailnet whose addresses end in `tail0000.ts.net`.

**Prepare the Mac, once.** MetaRush runs in the session of the user who runs it, so after a restart that user has to
be signed in. That is a trade-off: automatic login signs in by itself but needs FileVault off; if you keep FileVault
on, someone signs in after each restart (at the Mac or over screen sharing). In System Settings also turn on
**Prevent automatic sleeping** and **Start up automatically after a power failure**, and Remote Login if you'll update
it from another computer. Install Homebrew and Tailscale and sign in to your tailnet; in Tailscale's admin console
turn on **DNS → HTTPS Certificates** (with MagicDNS). Then install MetaRush as in [step 1](#1-get-metarush).

**Either: the Mac runs everything.** Footage and projects live on the Mac, and people use its dashboard in a browser:
```bash
metarush setup                                        # footage, projects and the background service on this Mac
metarush remote status                                # Tailscale connected? HTTPS certificates on?
metarush remote enable --allow sam@example.com --apply   # https://studio-mac.tail0000.ts.net for you and Sam (asks before running tailscale serve)
metarush users invite sam@example.com                 # or invite people to sign in with a password: a one-time link to send
metarush doctor                                       # its remote checks cover Tailscale, certificates and serve
```
**Or: a render worker for a shared workspace.** When your team's workspace keeps its projects in MongoDB Atlas (or
your own server) and its footage in shared storage, the Mac joins it as one more computer that does the work:
```bash
metarush machine invite studio-mac --atlas            # on the workspace's admin computer: the Mac's own storage key and database user, in a join bundle
metarush machine join <bundle>                        # on the Mac: join the workspace with that bundle
metarush service install --worker-only                # on the Mac: renders and background jobs, no dashboard, restarted if it stops
metarush worker list                                  # from any computer: studio-mac is online and takes renders
```
**Keep it up to date** from a checkout with `git pull && scripts/deploy.sh --host studio-mac --worker-only`, or,
once releases are out, with `brew upgrade --cask metarush && metarush migrate && metarush service restart` on the Mac. Settings → General on any computer lists
the Mac with the version it runs and says when it is older. When something is off: `metarush service logs -n 200`,
`metarush doctor`, `metarush renderer status`. More: [deployment](docs/deployment.md),
[add a machine](docs/runbook-add-machine.md).

### Windows 11
There is no Windows build; use MetaRush in Edge or Chrome from a Mac or Linux computer that runs it, over Tailscale
([docs/windows.md](docs/windows.md)).

Details, tokens, signing and the database on this computer: [docs/installation.md](docs/installation.md).

## How it fits together
MetaRush is a skills-first, AI-native video editing harness: Claude Code (or another agent) does the editorial
thinking through MetaRush's skills and commands, and MetaRush does the rest on your machines.
```text
Claude Code ─ skills ─▶ metarush CLI ─┐            ┌─▶ MongoDB: projects, assets, revisions, jobs (metadata only)
Browser ◀─ local daemon (127.0.0.1) ──┴─ ops ──────┴─▶ media store: Tigris/R2/B2/S3/GCS or a folder (content-addressed)
             dashboard · worker · watched inbox          local cache per machine (verified copies, never authoritative)
```
- **One program.** One Go binary (CLI + daemon + worker), a React dashboard embedded in it, Remotion templates in
  `video/`: the binary carries them with their npm lockfile and sets up a renderer of its own version in
  `<home>/renderer/`, so a change to a checkout never changes what an installed copy renders.
- **Shared by every computer.** A local dashboard on every machine shows the same shared projects, revisions and
  renders, so collaborators see each other's edits live. Every surface calls the same operation registry.
- **Code runs only after a person approves it.** A project's own motion graphics are versioned by the SHA-256 of
  their sources and render only in a network-jailed headless browser, after a person approves that version (every
  render page is jailed the same way); a whole Remotion project brought in as a source renders with its own pinned
  Remotion version in the same jail, also only after a person approves the version; its own build steps run only in
  a refresh a person starts, in the operating system's sandbox with a minimal environment.
- **One source of truth.** The edit spec (JSON) is the source of truth; a cut sheet is a lossless Markdown view of
  it, and saving one becomes an ordinary ops plan, so it is checked, confirmed, locked and rebased like any other
  edit.

See [docs/architecture.md](docs/architecture.md).

## Getting set up and finding your way

### Set up in about five minutes
`metarush setup` asks a few questions with suggested answers and sets up:
- storage (a folder on this computer);
- a database on this computer: MongoDB's free server, downloaded from MongoDB and looked after by MetaRush (started
  when needed, kept running, upgraded only when you ask: `metarush database`);
- the dashboard, the background service and Claude Code's skills.

Every answer is also a flag and `--json` prints the result. It says whether Claude Code is installed and signed in
(never signing in for you), and Home's **Getting started** lists what works free on this computer and what a key
adds. MongoDB Atlas or your own server stays a choice. The steps are in the [Install Guide](#install-guide).

### Finding your way
The top bar has Studio, Library (the Inbox, collections, the Archive and Trash, plus People, Motion, Recipes and
Abilities tabs), Renders and To-dos; the activity button with its live dot, the bell and the account menu sit at the
right.

The account menu (your picture when people sign in, a menu button when it's just you) opens Settings, switches light
and dark, and signs you out. Settings has a page per section, each with its own address (`/settings/extension`), and
older links redirect:
- **General**: which version you have and what's new in it;
- **People**: your account, users, remote access;
- **Preferences**: yours (estimates, Ask first or Auto, light or dark);
- **Claude Code**, **Footage**, **Browser extension**, **Storage**, **Integrations** (keys and looks), **Log** and
  **Command line**.

A project's brand, spelling list and house rules are in that project's own **Settings** tab
([Content Settings](#content-settings-shared-by-a-clients-projects)).

### Accounts and sign-in
A new install is just you on this computer ([docs/accounts.md](docs/accounts.md)).
- **Choose how people sign in** in **Settings → People**: email and password, or Tailscale accounts, giving your own
  sign-in at the same step. Then invite people with a one-time link you send them (it continues the history of
  anyone who used MetaRush before).
- **Passwords** are stored with argon2id, checked against common passwords, and wrong guesses are slowed down and
  locked; owners make reset-password links (`metarush users reset-password <email>`).
- **Only owners change this computer's settings** (enforced on the server, [the list](docs/accounts.md#what-only-owners-change)).
  Everyone sees every setting, but only owners change people and sign-in, remote access, the storage and the Trash,
  keys and the team's budgets and limits, Claude Code, the watched folder and link imports, the log and the weekly
  improvement report, and clearing archived projects out for good. A member sees the controls greyed with "Only an
  owner can change this. Owner: …". This computer's own command line counts as the owner.
- **The browser extension is each person's**: anyone signed in pairs their own browser in Settings → Browser
  extension, what it sends is credited to them, they see and remove their own browsers (owners see and remove
  everyone's), and a turned-off account's browsers stop at once.
- **Everyone's changes say who made them**, and each person can add a picture (`metarush account avatar <file>`).

Commands: `metarush auth status|method|reset`, `metarush users list|invite|reset-password|disable|enable|remove|role`,
`metarush account …`.

### Use it from your other computers, over Tailscale
`metarush remote enable --allow <their Tailscale account> --apply` gives the dashboard an HTTPS address on your
tailnet (`https://<computer>.<tailnet>.ts.net`, served by `tailscale serve`); MetaRush itself still listens on this
computer only, and any other HTTPS proxy works too, with `--url` (a worked example:
[an always-on Mac mini](#example-an-always-on-mac-mini-for-your-team)).
- **Who gets in** is the sign-in method you choose in **Settings → People**: people sign in with their email and
  password (invite them; links you copy and send) or, as an option, with their Tailscale account, each as
  themselves in the audit trail, the activity log and "Approved by". One-time links (`metarush dashboard`) sign in
  only you, on this computer.
- **It works like being there**: the microphone works over that HTTPS address, actions that happen on the other
  computer say "on <its name>", and downloads come to your browser.
- **How it stands**: Settings → People → Remote access shows how it stands and who may sign in;
  `metarush remote status|disable|allow|remove|signout` and `doctor` cover the rest
  ([security](docs/security.md#remote-access-over-tailscale)).

### From Windows 11
Use Edge or Chrome against the Mac or Linux computer that runs MetaRush, over Tailscale
([docs/windows.md](docs/windows.md)); downloads get names Windows keeps.

### Always know what is happening
- **The activity bar** shows uploads, footage being prepared, renders, quality checks and Claude Code requests with
  progress, the machine doing the work and, for anything waiting, why (on battery, no machine that can do it, busy),
  with **Process now** and **Retry**.
- **The notification bell** (footage ready, renders, quality checks, duplicates, battery pauses) follows you to every
  machine; each job opens in plain words with a "Go to" link to what it made.

### Asks instead of guessing
Unknown voices, music without known rights, a project with no goal or audience, two people who sound the same, and
cuts waiting for approval become questions on the **To-dos** page (badges on videos and projects; job details and the
activity bar say when a clip has questions) and in `metarush todos`. Each answer is applied right away.

### Find anything
Search what was said (whole or partial words, with timecodes and who said it), what's on screen (things, animals,
places, text on signs and titles), people's names (the clips where they speak), titles, filenames, projects, versions
and comments. A result opens the clip at that moment with the word highlighted.

## Bringing in footage

### Ingest anything
Add footage from the CLI, a browser drop zone or a watched inbox folder. MetaRush recognizes each file by its content
(not by its extension), keeps one copy of identical files (SHA-256 deduped) and sends them as resumable uploads to a
shared media store, verified. Then it makes:
- proxies, contact sheets and waveforms;
- **local transcripts with word timing and speaker turns**, in each clip's own language
  ([below](#each-clip-in-its-own-language)); nothing leaves your machine;
- technical evidence: scene changes, pauses, black or frozen frames, loudness, and how the picture changes (cuts,
  pans and zooms, someone walking in).

### Uploads you can trust
The dashboard shows each file's stage (sending → checking → fingerprinting → storing → processing → ready) with speed
and time left, notices stalls and reconnects, and rebuilds everything from the server after a reload: drop the same
file again and it continues where it stopped. Files identical to footage you already have are caught **before**
uploading (size + fingerprint, confirmed by a sampled hash): skip them, or keep both names on one stored copy.

### Import from links
Paste a YouTube, TikTok or X link in the Inbox (or press ⌘V or drop a link anywhere in the dashboard, or run
`metarush import <link>`). MetaRush looks the video up, downloads it on your computer in the best quality up to 4K
(MP4), then adds it like a dropped file (duplicates caught, preview, transcript and speakers made), showing "Looking
up the video → Downloading (percent, speed) → Adding to MetaRush" in the activity bar.
- **Where it came from stays with it**: the clip keeps its site, account, upload date, original title and stated
  license, and shows them as "From YouTube · @handle · 12 Mar 2024 · open original"; cut sheets, `footage.md` and
  delivery sidecars credit it.
- **Playlists and channels** import nothing unless you ask (`--playlist 5`).
- **When a site wants to check you're not a bot**, "Use my browser's sign-in" downloads it with your browser's
  sign-in for that one import (MetaRush never copies your cookies).
- Copy-protected (DRM) video is refused.

### The browser extension
The **Send to MetaRush** browser extension saves a video from your own signed-in browser, and its **Send this page**
adds a web page as you see it there ([browser extension](docs/browser-extension.md)). Each person pairs their own
browser ([accounts](#accounts-and-sign-in)).

### A watched folder
A watched folder (set up from the Inbox or Settings, with the native folder picker on a Mac) adds new footage by
itself and applies changes without a restart.

### Google Drive and Dropbox folders, without syncing
Through your own rclone, list a folder and bring files in by name or ID (`metarush import cloud dropbox:Shoot
--list`), each checked against the listing; MetaRush never holds a sign-in
([Google Drive and Dropbox folders](docs/cloud-folders.md)).

### An edit from another editing app
Drop an OpenTimelineIO, FCPXML (1.8–1.11), Final Cut Pro 7 XML or EDL timeline in the Add box (or `metarush import
timeline <file> --project <project>`) and it becomes a new cut. Clips are matched to your footage by MetaRush's own
note or by name, length and timecode (the files a timeline names are never opened), and footage that isn't found
gets a plain question: use a clip, add a file, or leave it out. Transitions, split edits, levels, chapters and notes
come with it, and you get a list of what can't be carried over
([timelines from other editors](docs/timeline-import.md)).

### Record your screen, with the teleprompter
**Record your screen** in the Add box records a window, a tab or your whole screen, your camera and your microphone
right in the dashboard, also from another computer over Tailscale ([docs/recordings.md](docs/recordings.md)).
- **Nothing lost**: each is its own file, sent to MetaRush while you record, so a closed tab loses nothing recorded.
- **The teleprompter** scrolls your script (the project's or a pasted one) at an easy pace, floats above other
  windows if you like, and never shows in a window or tab you share.
- **Ready to cut**: stopped, the screen, camera and voice are footage lined up on one clock (checked by their sound)
  and one conversation in the project; **Start a cut** shows them with the camera in a corner or side by side.
- Refused, missing or silent devices and lost connections are said plainly, with what to do.

### Conversations from online calls
One recording per person from Zoom ("separate audio file for each participant"), Riverside, SquadCast or anything
else becomes one **conversation** ([docs/conversations.md](docs/conversations.md)). Drop a call's folder in the Add
box and it offers **Make a conversation from these** (`metarush conversation import <folder>`).
- **Lined up by their sound**, even when they started at different times on computers whose clocks drift apart (each
  microphone hearing the other through the speakers is enough). MetaRush plays each at the very sample it found, in
  the render and the preview, and says how sure it is; nudge a recording a frame if lips and voice don't match.
- **The picture switches, the voices don't**: in the cut the picture switches between people and layouts (side by
  side, stacked, picture in picture, a grid) while **everyone's own microphone plays all the time**, each a little
  quieter while its person listens (echo and the other voice through the speakers stay out); captions name each
  person exactly.
- **Make the cut** in a style (host and guest podcast, off-camera interviewer with questions heard or as text cards,
  panel, reaction-heavy, talking head with cutaways): the picture follows whoever talks, voices start a moment before
  we see them, switches land in pauses, listening reactions and laughs show both, and every shot says why. Then click
  any shot on the timeline to change it.
- **Clean up the call**: "can you hear me?", long silences, a frozen webcam, a sound dropout and the app's name labels
  are suggestions to accept or skip.
- **Match the webcams** (brightness and colour, measured before and after).
- **Shorts**: the best moments (a question and its answer, a laugh) become new projects with vertical cuts of their
  own.
- Quality checks say when someone is hard to hear, stays off screen too long, or a glitch shows.

### Speaker-view call recordings, cleaned up
For one recording whose picture flips to whoever talks (Zoom's speaker view), MetaRush finds every switch to the frame
(`metarush footage switches <clip>`) and flags split-second flashes of the wrong person at clip edges in the edit
check and the quality check ([docs/call-recordings.md](docs/call-recordings.md)).
- **Trim the flashes** (`metarush cut flashes <project>`) trims them all at the exact switch.
- **Cover the printed names** (`metarush cut names <project> [--first-names]`) puts your own name bar over the names
  the call app burned into the picture, for the whole video, changing at the exact frame the picture switches.
- Both work inside cuts played in other cuts and in conversations of several recordings, where each tile of a layout
  gets its own name tag over the printed name.

### Each clip in its own language
MetaRush listens for a clip's language before writing its words down: English on NVIDIA Parakeet (via sherpa-onnx),
Spanish, French, German, Portuguese, Japanese and about 90 more on whisper.cpp, all on your computer.
- The info card says "Spoken in Spanish" with **Change…** (or `metarush footage language <clip> --set es`), which
  writes the words down again.
- In other languages words follow the sound and pauses show at their length (DTW times with pause edges from the
  silence), invented thanks over silence are dropped, and Japanese and Chinese words are pieces you can cut
  between.

### Knows who is speaking
Every transcript is split by voice (whichever recognizer made it). Watch a few seconds of a voice (each sample is a
small video tile that plays just that moment) and type a name once; MetaRush recognizes that person in your other
videos, shows names in the transcript, on a "who speaks when" lane and (optionally) on captions, and Claude can follow
"use only the guest's answers" ([speakers, people and to-dos](docs/speakers.md)).
- **Its guesses look like guesses**: voices it named by itself look different ("recognized by voice, 68% match") and
  come with a one-click "Not Maya?"; new ones are announced once ("Recognized Maya's voice in 2 more videos", with
  Check).
- **Wrong speaker on a line?** Every transcript line has a chip to give it to someone else, someone new, or "not
  sure".
- **One wrong name can't spread**: two voices only become one person after you say so (with Undo), and each person's
  voice is learned only from what you confirmed.
- **One picker everywhere** finds people as you type (accents, typos: "Did you mean Lena?").
- **Notes about a person** (who they are, how to name them, what to avoid) guide Claude's name tags and choices.
- Footage imported before an upgrade catches up by itself. Voice fingerprints stay on your machine.

### Asks who someone is once, not every time they appear
The faces of each clip are grouped by how they look and who is talking while they are on screen, so a long call in
speaker view asks "Is this Lena?" once, with a few of her faces and "Seen 24 times in “Lena's call”", instead of 24
times; one answer names all of them.
- **Names the video shows or says** (a call app's name under each picture, a lower third, a title; "Hi, I'm Nora")
  are suggestions with their evidence, and **Use the names shown in the video** answers a whole call in one click;
  nothing is learned from them until you accept.
- **Only the people who matter**: people who never speak, or are seen for under a second, aren't asked about; faces in
  your cut come first.
- **Quick to answer**: the To-dos page shows these questions as a keyboard-driven grid. "Is this Lena?" takes a face
  that looks like hers or her name shown; who is talking (from 6 seconds) only makes her a quick pick on "Who is
  this?". Answer many at once: **None of these are Lena**, **Not Lena in this clip**, **Skip all in this clip /
  project**, or select tiles.
- **It learns which hints to trust**: MetaRush keeps score of its hints (who was talking, the looks, names shown or
  said, the voice) and stops suggesting names from one that is often wrong in your workspace
  (`metarush people evidence`).

### Shows you what a name was learned from
Before "Is this really Maya?" you hear this voice next to the voices Maya's voice was learned from, how alike each is,
with the odd one marked; **Not Maya** on an earlier one fixes that mistake right there and her voice is learned again.
"Not Maya?" on a face shows the faces hers was learned from, the same way.

### Sees what's in the picture
On a Mac, MetaRush looks at about one frame a second of every clip, plus the moments the picture changes, with Apple
Vision (on your computer, free): who is on screen and how close (close-up, medium, wide), the place, things and
animals, and text on screen ([what MetaRush sees](docs/visual-index.md)).
- **Read out per clip**: each clip's On screen tab reads it out ("0:00–0:06 close-up · 1 face (Lena) · indoors · no
  text"), and so does `metarush footage visuals`, with gestures (pointing, a hand raised) and text in any language.
- **Faces across clips**: name a face once and MetaRush **recognizes that person in your other clips** (SFace, on
  your Mac, learned only from faces you confirmed: "looks like Maya", with "Not Maya?"); when it is less sure, To-dos
  asks "Is this Maya?" once per clip (who is talking alone never asks it).
- **The right frames for Claude**: contact sheets pick one frame per shot, then pans, movement and new speakers, and
  label each frame with its time and who is saying what (`0:42.0 · Lena: “and then the bunny…”`), so Claude looks at
  the right moments and checks a frame before saying anything about the picture.
- **Optional scene descriptions**: written scene descriptions by your own Claude Code are off unless you turn them on
  (they use your Claude Code usage).

What this does for framing is under [Vertical, square and landscape](#vertical-square-and-landscape).

### Names that say what things are
A clip is “Maya's call with Lena (32 min)”, not `IMG_0042.MOV`: MetaRush names it from its first look (who's in it,
what it's about, a title on screen, a link's title, the recording date) and keeps the file name on its info card
([sources](docs/sources.md)). A name you typed is never replaced; better names MetaRush finds later wait for one
click (or **Use all**). Cuts get names too (“Main open day video”), and every version a short note (“flicker fixes
and headline cards”).

### An info card for every clip, and fixes
Each clip's info card says where it came from, what MetaRush found (length, picture and sound, people, words on
screen), where it's used, and two or three fixes for its kind, marked when they fit ("The camera shakes a lot",
"There's a hum"):
- **footage**: steady the camera (vid.stab), balance colour, reduce grain;
- **calls**: even out each person's volume, clean up voices, reduce blockiness, steady a shaky webcam (a call is
  steadied one picture at a time, between its switches);
- **sound**: even loudness, remove hum, trim silent ends;
- **pictures**: remove the background (a photo's subject with Apple Vision on a Mac), trim empty edges, make it
  larger.

Ticking makes a fixed copy (the original is kept) that everything after uses; saved cuts keep their media until you
use the copy in them as a new version, with one click.

### An Inbox you can empty, and nothing lost
New footage waits in the Inbox of the **Library** (`/library`) until you add it to a project, put it on a
**collection** (shelves you name: B-roll, Family, Stock…; a clip can be on several and in projects too), **archive**
it ("keep for later") or **move it to the Trash**: from its "…" menu or for several selected clips at once, each with
Undo.
- **Collections in projects**: a project can use everything on a collection ("Use in a project…", or "From another
  project" in its Sources), and clips put on it later show up there by themselves.
- **The Trash is two steps**: moving there is instant and **Restore** puts a clip back where it was; **Delete
  forever** and **Empty Trash** are for people only, after a warning that says how much goes ("Permanently delete 3
  videos (1.2 GB)?"), and never touch a clip a cut uses. Nothing is emptied on its own unless you choose "Empty the
  Trash after 30 days" in Settings.
- **Counts and search**: the Library's tabs count the Inbox, collections, the Archive, footage in projects and the
  Trash, and its search looks through all of them, the Trash included.
- **A library that stays out of the way**: clips get editable titles (the original filename is kept), grid or list
  views, multi-select, and drag-and-drop onto projects with Undo.

### Tidy footage safely
Each source's "…" menu moves it to another project, removes it from this one (it stays in the workspace, back in the
Inbox when no project has it, with Undo), shows **where it's used**, or deletes it for good: only by a person, after a
styled confirmation, and never while another project or any version of a cut uses it. Dragging clips onto a project
lights up exactly one card, says "Add to Spring promo" by the pointer, and the toast offers Undo and Move….

## Working with Claude

### Grounded editorial judgment
Claude Code reads transcripts, detector output and the contact sheets and frames MetaRush extracts, then writes a
schema-validated edit plan with a rationale and evidence for every cut. No extra AI API bill: it uses your Claude
Code sign-in, and MetaRush makes no model calls. `metarush setup` teaches Claude Code MetaRush's video skills, so in a
terminal you just ask ([try this](#3-ask-for-your-first-cut)).

### Talk it through in the dashboard too
The **Ask Claude Code** button in the top bar opens real conversations with *your* Claude Code on this machine, using
the same skills under a read-only tool allowlist ([the panel](docs/assistant.md)).
- **A real conversation**: ask follow-ups (typed while Claude works, they queue), interrupt or stop without losing
  the thread, and pick a conversation up days later: it resumes Claude Code's own session. Replies stream as
  formatted text with the stills Claude looked at; cards and messages say when; long code folds away; stills and a
  graphic's test frames open large. Everything it shows is in plain words: no file names, IDs or codes.
- **Plans become cards**: plans it checked become cards with a plain summary you apply with one click, and changes it
  cannot make itself become cards you run (render a review copy, approve, import, write or move narration lines, make
  the AI voice, change the mix, cut a call in a style, accept clean-up suggestions, change an angle, make shorts, set
  a person's level, make a voice clearer, add or fit music). An AI voice or cloud clean-up card shows its price
  first, a studio clean-up card its one-time setup.
- **Fewer clicks**: several cards from one reply are one card with **Approve all**; a save card offers **Save and
  render a review copy**; Claude asks questions as cards you answer with a tick or a few words; a badge by the
  message box counts what waits for you and ‹ › jumps to each.
- **Say what's wrong**: every card has **Not quite…**: say what should change and Claude makes a new version. **Not
  now** means later, not never. A note you give when rejecting a version or turning down a graphic goes to the
  conversation that made it.
- **Claude keeps going**: Claude hears when work it asked for *finishes* (the review copy and its quality check, the
  snapshot, the import) and carries on by itself. After an update Claude hears what's new.
- **One project per conversation**: a conversation stays about the project it started in, whatever page you type
  from (the header names it; opening another project switches to that project's conversation), and a new one starts
  with **the project so far**: the versions, review copies, earlier conversations and what you decided.
- **Kept with the workspace**: conversations are kept in the workspace database (browse and search them from any
  machine, continue them on the machine that holds them), and a full-screen view puts the list, the thread and the
  current cut side by side.
- **Attachments**: attach pictures and PDFs to a message by dropping, pasting or the paperclip.
- **Whose plan pays**: it uses your Claude Code sign-in (plan or API key, and says which); MetaRush makes no model
  calls. Without Claude Code, the panel prepares a prompt to paste instead.

### Ask first, or Auto
Safe steps run by themselves (a setting, on by default): checking a graphic Claude made, a review copy, and bringing
in a link you typed start without a click. Approvals, saves, final renders and anything that costs money wait for
you, unless you choose **Auto** by the message box, which also runs Claude's changes that can be undone (saving a
version with its review copy, sound and transcript fixes, links) while money, final renders, approvals and deleting
still ask. With your estimates turned off, Auto may also spend, within the limits a person set
([budgets](#budgets-limits-and-estimates)).

### Conversations with several people
With several people, a project's list shows everyone's conversations in it ("Started by Sam", **Everyone / Just
mine**); anyone can continue one, each message says who wrote it, and the cards Claude raises answering you are
yours (your badge, your ‹ ›, your notification) while everyone sees them in the thread.

### Whose Claude answers
The Claude Code panel uses the host computer's Claude Code and its Claude account; every conversation and Settings →
Claude Code say whose account answers and whether its terms are Anthropic's consumer terms (Free, Pro, Max: training
follows the account's privacy settings) or commercial terms (Team, Enterprise, an API key: not used for training).

### "Keep this?" — the project remembers
When a conversation brings in something worth keeping (a website, a logo you attached, who the video is for and
where it goes, a style rule, a fact about someone, who must OK it, a problem raised twice, something MetaRush
couldn't do, a spelling), the Claude panel asks once (**Keep** / **Don't keep**, never twice)
([docs/keeping.md](docs/keeping.md)).
- Kept rules are read by Claude in every conversation and measured by the quality check where it can (captions at
  the top, first names only, no flashes, a length, the shape).
- Sign-offs remind before approving, exporting and downloading.
- `metarush keep …`, `metarush project rules …` and `metarush cut signoffs …` do it all from a terminal.

### Any AI app can propose, only people decide
Connect Codex, Cursor, Claude Desktop or Claude Code with `metarush mcp serve --propose` (or hold an app's commands to
it with `--propose` / `METARUSH_PROPOSE`), and every change it asks for is saved as a draft (a new version beside the
cut, or a Content Settings or other change kept until approved) saying which app made it and why
([the review desk](docs/review-desk.md)).
- Drafts wait in Awaiting your review with what changes in words, the review copy MetaRush makes by itself, the
  checks and what it costs.
- A person approves (a version goes into the cut, keeping newer changes), says **Not quite** with what should change
  (the app hears it on its next call and makes a new draft) or turns it down. Approving, delivering and spending stay
  people's.
- `metarush draft save|list|approve|not-quite|turn-down`; `metarush revision packet <draft or version>` is one page
  (Markdown too) to hand a draft to a client or a colleague.

### Claude Desktop and other MCP clients
`metarush mcp serve` offers MetaRush over stdio as a few tools: `find` names what does a task described in plain
words, `describe` shows how, `run` does a read and `run_change` a change, beside everyday tools (10 tools, 12 with
changes). Or it offers one kind of work as direct tools with `--toolset review|edit|library|admin`. It is read-only
unless you allow changes (each one then confirmed, and on tools of their own so your client can keep asking you
about them), never does anything only a person may do, and puts every call in the log ([MCP](docs/mcp.md)).

### Find what does a task
`metarush ops find <what you want to do>` lists what MetaRush can do for it, best first, the same answer every time;
the video skills send anything they don't cover to it, and stay within Claude Code's skill listing.
`metarush skills eval` scores how often the search picks right on 186 everyday requests, with the first-party
abilities and sample added ones in the catalogue (`--live` asks real Claude Code).

## Editing a cut

### Safe, auditable edits
Every change makes a new version and nothing is overwritten: each edit is a dry-run → confirm token → immutable
revision. Locked segments stay locked; concurrent edits rebase automatically when they don't overlap and conflict
loudly when they do.

### Edit by hand, too
The studio's **Edit** tab has a timeline without gaps:
- **Arrange**: drag clips to reorder them, drag footage in (the part you marked, or all of it), drag a clip off to
  remove it.
- **Trim and split**: trim an edge (⌥ moves the cut between two clips instead), split at the playhead, mark a stretch
  on the ruler and delete it.
- **Precise and quick**: it snaps to the playhead, the cuts and the words people say, zooms, shows each clip's frames
  and sound, has a key for every drag (press ?) and works with a finger on a phone.
- **Transitions**: click the marker between two clips to choose a transition from picture tiles with a looping
  preview, in plain words ("Hear the next clip a moment early").

Every change plays at once and saves itself a moment later as an ordinary version; Undo and Redo go back through
saves, and if someone else changed the same clips meanwhile you choose Keep mine or Keep theirs.

### Edit by the words
The **Transcript** beside the player shows what the cut says: select words and press Delete to cut them out in the
pauses around them, or let **Remove ums and restarts** suggest filler sounds, words said twice and false starts
(listen to each one taken out, then keep it or take it out). **Tighten pauses** shortens long pauses between words,
playing each one first. A clip's transcript in Sources works the same way (cut words out of the video, or mark them
to add just those words).

The same edits work from a terminal: `metarush cut move|trim|split|add|delete|delete-range|remove-words` show the
change first and save it with the confirm token.

### Fix the transcript, and the captions follow
([docs/transcripts.md](docs/transcripts.md))
- **Fix a word where you see it**: double-click a word in the cut's transcript, click a caption in the review
  player, or select a stretch and say who really says it. Only the words that differ change and each keeps its
  timing (joining "Fern Brook" into "FernBrook" too).
- **Find and replace** across a clip, a project or everything lists every place with a play button before anything
  changes.
- **Spelling lists** (part of each project's Content Settings; a clip in several projects uses all of theirs)
  correct new transcripts and tell the transcriber what to expect; fix the same word three times and MetaRush offers
  to add it.
- **Every fix is kept**: MetaRush keeps its own transcript and every fix says who and when, with Undo.
- **Saved versions keep their words**: fixes wait until you make **one new version and one review copy** for all of
  them, and nothing renders by itself.
- **Check the captions** before a video goes out: it lists fixed words, words the computer wasn't sure of and names
  that aren't on a list; captions files (.srt, .vtt) carry every fix without a new render.
- Claude asks for the same changes as cards you confirm.

### The real thing a minute later
About a minute after the last change to a cut, MetaRush makes a review copy of its latest version by itself (once per
version, on any number of machines; a newer version replaces a copy that hasn't started; on battery it waits and says
so; `metarush render follow on|off`).
- Above the Edit tab's timeline a switch picks the **Live preview** (drawn in the browser, unsaved changes included)
  or the **Review copy** of the latest version, which plays by default when nothing is unsaved; "Making a review copy
  of version 8…" shows its progress.
- A version's page lists its renders, with when they were made, and plays them.
- In the live preview a checked motion graphic shows the still its check drew nearest the moment, in its place,
  marked "still — moves in renders" (pictures only: the dashboard never runs a graphic's code).

### Cut sheets: the cut as text you can read and change
`metarush cut show` prints any version as a short Markdown file: numbered clips with their first and last words (and
who says them), transitions in plain words ("dissolve", "fade through black"), the narration script with where each
line starts ("0.5 s after the drone shot starts") and whether it fits, the sound mix, graphics, sound, captions, and
the notes and reasons behind each cut ([docs/cut-sheet.md](docs/cut-sheet.md)).
- **Edit it as text**: change a time, reorder lines, write `from "so I quit" to "in my tiny garage"` instead of
  times, add a note. `cut check` points at the exact line of any mistake and says how to fix it, and `cut apply`
  saves it as a new version through the same confirm step (changes someone else saved meanwhile are folded in when
  they don't overlap).
- **The Script tab** shows it three ways: **Formatted** (the cut read like a script: who says what, clip by clip, why
  each clip is there, how one leads into the next; click a clip to play the cut from there), **Markdown** and **Edit**
  (live checks), for any version, with a side-by-side comparison of versions.
- **In git**: `project checkout` puts a cut in a folder you can keep in git.
- **From Claude**: in the Claude Code panel, a cut sheet Claude writes becomes a card with the changes and the changed
  lines that you save with one click.

### Transitions and sound at cuts
- **Split edits built in**: the next person's voice can start a moment before we see them, or a voice can carry on
  over the next shot, and the outgoing clip's own sound steps aside on its own; "auto" moves the change into the
  nearest pause so no word is cut.
- **A library of transitions** (cut, dissolve, fade through black or white, wipe, slide, push, zoom, whip) with
  directions and pacing, planned to the frame and drawn the same in the preview and the render.
- **No clicks or jumps**: straight cuts blend for a moment so they never click, dissolves blend at an even loudness,
  and voices recorded on different cameras or microphones are evened out from clip to clip.
- **A style per project** (Natural, Snappy, Smooth, Playful) that Claude follows; edit check and QA point out splits
  the footage is too short for, cut-off words, two voices at once, single-frame flashes and jumps in loudness at a
  cut.
- **Written the way an editor says them**: cut sheets write them as `↳ cut · Sam's voice starts 0.4 s early` or
  `↳ slide left 0.4 s · slows down`, and `metarush cut transition` changes one cut without opening a sheet.

### Captions
- **Six caption looks**: Clean, The spoken word lights up, Boxed, Big and bold, A box follows the spoken word and Plain
  subtitles, each drawn in the brand's colours and font. Pick one under the player or with
  `metarush cut captions <project> --look bold`; the house style offers every look and the quality check notes a cut
  that differs.
- **Captions in other languages**: **Ask Claude to translate…** on a version's page (or `metarush captions lines` and
  `captions translate` from a terminal), and Claude Code translates each caption line on your own Claude plan,
  keeping the spelling list's names as written; the files say they were machine-translated. Later text fixes follow:
  a fixed name is carried into the translation, and a line that changed otherwise is listed to translate again.
  Delivery writes a SubRip and a WebVTT file per language next to the video.

### Vertical, square and landscape
- **Faces stay in frame**: new vertical and square cuts keep the speaker's face in frame (`framing: faces`; older
  cuts keep their framing until you switch it on, which saves a new version).
- **Follow movement** (`framing: follow`) moves the crop with someone walking across the shot, smoothly, jumping only
  at the footage's own cuts.
- **Checked on every render**: the quality check finds a face the crop cuts off and captions or graphics over a face
  or over text in the footage, each with a fix.
- **Shorts from any footage**: ask Claude for shorts (Moments → **Ask Claude for shorts**, or in the panel); each
  moment it proposes becomes, with one click, a new cut of the project for phones framed as the house style says
  (following the subject when you ask) with captions on, approved like any cut
  (`metarush footage short <project> --clip <ast> --from --to`).

### Narration and mixing
Write or import a voiceover script and place each line the way you would say it: when a shot starts, at a moment in
the footage ("right after she laughs"), after the previous line, or at an exact time; lines tied to shots move with
the edit.
- **Your voice or an AI voice**: check timing with a free system voice, generate natural narration with ElevenLabs
  (your own key, the cost shown first, unchanged lines never paid for twice), or import or record your own read,
  matched to the script and cleaned up without losing the original.
- **The mix takes care of itself**: the mixer turns music and the clips' own sound down whenever someone talks and
  sets the overall loudness for the platform, identically in the preview and the render.
- AI narration is marked in delivery sidecars.
- **With Claude**: in the Claude Code panel, Claude writes and places the narration and adjusts the mix as cards you
  confirm; it never handles your API key, copies a voice or uses the microphone.

### A Voiceover tab in the studio
**Add voiceover** on the current cut (the whole cut or one shot) asks how you want to start: write your own script,
ask Claude to suggest one (the Claude Code panel opens with the request ready), use a recording you have, record it
now, or an AI voice.
- **Each line is a card** with its words, where it starts said the way you would say it ("0.5 s after Kitchen
  starts", "right after the line before", "when Sam says …", or an exact time), delivery notes and, if you like, its
  own voice. A line that runs long says so plainly, with one-click fixes (hold the shot longer, start earlier, move
  the next line, a slightly faster AI voice).
- **Drag a line** along the narration lane of the timeline (Edit tab) and it ties itself to the shot it lands on,
  snapping to clip starts and words (hold Alt for an exact time); like every timeline gesture it saves itself in the
  background and undo puts it back.
- **Your script, or Claude's fit**: a pasted script is used as you wrote it, or, if you ask, fitted to the video by
  Claude: its version appears side by side with yours, changed words highlighted and a reason for each change. Keep
  either one line by line and switch between the two at any time (your original is always kept, in the cut sheet
  too).
- **AI voice**: pick a voice by its sample, see the price before anything is made, and remake one line at a time
  (unchanged lines cost nothing).
- The preview plays mono recordings as loud as the render does.

### Record in the browser, live while the video plays
After a 3-2-1 count-in the cut plays from where you are (muted, or quietly as a guide) and you speak; the take lands
exactly where you said each line. It is kept whole, cut only in the pauses, and each line starts where you said it,
tied to the shot playing there, so it moves with the edit.
- **The teleprompter follows the video**: the line to say now, the next one dimmed with a countdown about two seconds
  before its moment, and a mini timeline with the playhead; Space or a clicker brings the next line in.
- **Afterwards** the take shows on the narration lane where it landed, lines running into the next shot or past the
  end come with one-click fixes, and **Snap lines to their planned spots** is a button if you want them on the plan.
- **Redo just a part**: re-record from any point or just one line, and only that stretch is replaced. **One line at
  a time** is still there.
- **Every take is kept**, so you can compare takes and the original against the cleaned-up version.

### A Sound tab
Camera sound, narration, music and effects, and **each person** louder or quieter only while they speak; "Turn off
the camera sound", "Music gets quieter when someone talks", "Even out voices", "Smooth cuts" and a loudness named for
where the video goes. You hear a change in the preview before you save it ([docs/sound.md](docs/sound.md)).
- **Clearer voices**, per person or the narration, without touching the recordings:
  - **Clean**: on your computer, fast and free: less hiss, hum and room echo, softer harsh s sounds;
  - **Studio**: on your computer, a rebuilt studio voice, every word checked so nothing anyone said changes; its
    model loads once for all the recordings of a run, and on Linux it uses an NVIDIA graphics card;
  - **Cloud**: ElevenLabs, priced first.

  Each has a strength slider, an A/B switch per person and "hear every voice as recorded", and the result in plain
  words (the voice clarity score before and after, how much quieter the background is, warnings when it went too
  far).
- **Music** dropped on the tab or the timeline's music lane fits the video by itself: it starts with it and ends with
  it where the music starts a new phrase (or on a bar line or a beat), read across the whole song at its own tempo.
  Or place it by hand by dragging it. It has its own level and "gets quieter when someone talks".

### Title cards, lower thirds and graphics
- **Real rendering** with Remotion and vetted motion-graphics templates (lower thirds, kinetic quotes, data cards with
  mandatory sources, end cards, word-emphasis captions): the dashboard preview and the final render share one
  frame-accurate plan.
- **A layout pack** says which template, or which motion graphic a person approved, draws each title card, lower
  third, end screen, quote card and chapter card, and how call recordings' picture-in-picture and side-by-side
  layouts look, all in the brand's colours, font and logos (MetaRush's own pack, in the house style, until you
  choose) ([layout packs](docs/layout-packs.md)). A cut names a graphic's part (`- intro: title card at 0:00 for 3 s`)
  and MetaRush draws it with the pack; Settings → Layout pack previews each part and offers the latest cuts the
  change. Claude can propose a whole pack with graphics of its own: each is approved on its own and the pack becomes
  current only when a person accepts it.
  `metarush content pack show|set|propose|accept|decline|apply|preview`.
- **Graphics that stay up**: a graphic can last the whole video or a section (`for the whole video`,
  `from hook to answer` in a cut sheet), follow those clips through every edit, and change what it says at moments in
  the footage while it stays on screen. Motion graphics are checked in the shapes of the cuts that use them (a short
  and the main video: both), with test frames grouped by shape.

### Project motion graphics, approved by a person
When no template fits (a route drawn on a map, a counter, a chart that builds), Claude Code can write a small Remotion
component for the project in `motion/<name>/` ([docs/motion-graphics-as-code.md](docs/motion-graphics-as-code.md)).
- **Checked first**: `metarush motion check` makes sure it only uses what it's allowed to (no files, no network, no
  other programs), draws test frames in a browser that can't reach the internet and makes a contact sheet; then it
  waits.
- **A person approves**: nothing renders until a person approves that exact version on the **Motion graphics** page
  (the code, what changed, the frames and a short preview) or with `metarush motion approve` in a terminal. Changing
  one character makes a new version that waits again, and an approval can be withdrawn.
- Templates stay the default. Project motion graphics can draw resources too (`useResource("logo")`).

### Logos, pictures, PDFs and fonts
Drop them anywhere (one Add box on Home and on a project's Sources and Resources tabs, or `metarush add`) and MetaRush
sorts them out: footage goes to Sources, pictures, SVGs (cleaned of scripts), PDFs (each page becomes a picture) and
fonts to Resources, HEIC photos become JPEG ([resources](docs/resources.md)).
- **Named and versioned**: each gets a short name ("logo"), versions, its main colours, a thumbnail and suggested
  tags.
- **Pinned in cuts**: cuts pin them by name to an exact version. Title cards, lower thirds and end cards can show the
  logo (or a picture behind them), the new `logo.corner` keeps it in a corner, and the cut can use a font file.
- **Link a folder** and new or changed files come in by themselves (a file that disappears stays, marked missing).
- The brand kit made from them lives in [Content Settings](#the-brand-kit-and-five-named-logos).

### Explain software with its own code and pages
Paste a GitHub repository or a web page in the Add box (or `metarush resources add <link>`).
- **A repository** is kept at its current commit: its text files, README, languages, recent history and
  contributors, fetched with your own git/gh sign-in (never stored). Claude reads a read-only copy
  (`metarush resources path widget`); MetaRush never runs anything from it.
- **A web page** becomes a dated snapshot: screenshots for a computer and a phone, the whole page, its text, title,
  colours and icon. The browser extension's **Send this page** adds the page as you see it in your own signed-in
  browser.
- **Slides without custom code**: from a repository MetaRush makes **code slides** (highlighted with Shiki),
  **architecture diagrams** Claude writes in Mermaid or D2, and a **repository card**, each saying exactly where it
  came from (`acme/widget@1a2b3c4d:internal/queue/queue.go:47-64`), and six vetted **slide templates** (title,
  points, code, architecture, screenshot, quote) show them in the brand's colours. So "make an architecture slide
  from the repo", "show the homepage at the end" or "put a code snippet of the main loop on screen" need no custom
  code.
- **Refreshing** pins a newer commit or takes a new snapshot; cuts update only when you say.
- **Art kept in git**: a repository can also keep the pictures in some of its folders
  (`metarush resources pictures widget --keep art/characters`, `resources add <repo> --pictures <folder>`, or "Keep
  its pictures" in the repository's details). Each PNG, JPEG, WebP, GIF or SVG file there becomes a picture of its
  own that says exactly where it came from (`acme/widget@1a2b3c4d:art/characters/alice.png`): told by its bytes, not
  its name, SVGs cleaned, Git LFS pointers, links and files past the limits left out with the reason. A newer commit
  makes new versions of the pictures that changed, adds new files and marks the ones that went away; a recipe's
  pictures input can take its pictures from such a folder.

### Remotion projects as sources
Bring a whole Remotion project into a project: paste its folder or GitHub address in the Sources tab's Add box, or
`metarush import remotion <folder | GitHub address> --project <name>`
([docs/remotion-sources.md](docs/remotion-sources.md)).
- **Brought in carefully**: MetaRush keeps its files as version 1 (never changing the original; `.env` files and
  anything that looks like a secret, installed packages, build caches and coding-assistant settings are left out and
  listed), installs the package versions its lockfile pins with install scripts off, keeps its Google Fonts, reads its
  compositions in a browser that can't reach the internet, and adds the videos it already rendered as footage.
- **Compositions become clips**: each composition (a video the project's code makes) becomes a clip once a person
  approves the version. **Use in the cut** renders it with the project's own Remotion version (once per version,
  composition and props) and it then behaves like any footage. Cut sheets can name a composition:
  `4. intro: remotion teaser Intro`.
- **Claude can edit the code** on MetaRush's own copy (`remotion checkout`, then `remotion check`: the words that
  changed, a type check and before/after stills); the new version waits for a person under **Review**, and approving
  it moves the latest cut's clips to it while older versions of the cut keep theirs.
- **Scenes and sounds** shows what a project holds: each scene with a picture, its words on screen and where they are
  written, its music and voice with their timing and levels (`remotion map`).
- **Words and pictures as settings**: its words can become **settings** (one approved change, then headlines change
  from a form, checked in the sandbox, with no new code to approve; `remotion words`), and so can its **pictures**
  (one approved change, then **Replace…** takes new art, uploaded or from the project's resources, checked in the
  sandbox and served to the render by its fingerprint, with no new code to approve; a clip can have pictures of its
  own, the way recipes fill in new art; `remotion pictures`).
- **Reuse its parts**: its sounds can come in as the cut's own **tracks** (`--sound layers`); a scene can become a
  **motion graphic** any cut uses, with the project's colours, fonts and components as one shared library approved
  once per version (`remotion lift`); and a version waiting for you shows up in To-dos and the bell.
- **Refresh from its own steps** runs the project's own build (its npm scripts: a voice-over, a site capture, new
  data) when a person starts it, never by itself and never by Claude. The plan shows every command, the keys from
  Settings → Integrations it gets and under which names, where it may write and the hosts it may reach. It runs with
  the network on but kept away from MetaRush's files, keys and your home folder (macOS's sandbox, bubblewrap on
  Linux), and what it writes becomes a new version you approve.
- **Send changes back** puts an approved version's changes on a new branch of the repository it came from, with a
  pull request on GitHub: never forced, never the main branch (`remotion refresh`, `remotion push`).

### Use another project's things
Link a project ("use the resources from Brand") and its footage, resources, approved motion graphics and cut appear
here, marked "from Brand", nothing copied, or only some of it (by kind, tag, person or linked folder, or picked
items). Play its cut **inside** this one as one block pinned to a version (a newer version is pointed out, updating
is your call), or copy its clips in as a starting point.

## AI services and generation
MetaRush works without any AI service keys. When you add one (`metarush providers set …` or Settings →
Integrations; see [Configuration](#configuration)), every paid step is priced before it runs and counts against
the limits a person set ([budgets](#budgets-limits-and-estimates)).

### Models on this computer
**Models on this computer** (free, no key, nothing leaves it) sit beside the cloud services in the same estimate: the
local recognizers as a transcription provider, offline voices (Kokoro, Kitten), a picture's background taken away
(Apple Vision, on a Mac) and pictures made 2× or 4× larger (Real-ESRGAN). A person sets up the ones that need a
download, once (`metarush ai local`).

### AI services
With the Google Gemini and ElevenLabs keys you already have, **Generate** also makes pictures (into the resources
library), AI speech, sound effects, music, a recording in another voice, dubs into other languages (the picture
kept), copies of a voice (with the person's consent and, for Google, their reading of its consent script) and
transcripts in the cloud (on request; local stays the default) ([docs/ai-services.md](docs/ai-services.md)).
- **Priced first**: each is priced with the model and why, up to two other models (cheaper, better), the model's
  licence and what the service keeps.
- **Within your limits and marked**: it counts against the same budget and daily limit as generated video, lands
  marked as AI-generated, and is deleted at the provider where its API allows.
- **Per client**: a project's Content Settings can keep a client's work away from AI services, rule out a provider or
  choose the model for a kind of work.

### Generative video
**Generate a shot** in a project's Sources (or **Generate video** in the drop box, on Home too) makes a new clip with
Google Gemini (your own key) from a few words, starting from a photo or continuing from a frame of your footage,
between two frames, or with an approved look of a person or a character: 3 to 10 seconds, wide or tall, a cheap draft
or final quality, up to four takes at once ([docs/generative-video.md](docs/generative-video.md)).
- **The price comes first** and is on the button; nothing is made without it, and it counts against the project's
  budget and today's limit for the team (Settings → Integrations).
- **Marked and explained**: shots land in Sources as ordinary clips, marked AI-generated everywhere (tiles, the
  Script, the cut sheet, the quality check, and every export's notes with what YouTube, TikTok, Instagram and
  Facebook ask), with how they were made (prompt, pictures, model, price, what Google's filters said).
- **More takes, never lost ones**: Again, Variations, Extend and Edit make new takes and never replace one.
- **Someone's likeness only with their consent** ([consent](#consent-for-anyones-face-or-voice)).
- **Nothing left at Google**: what Google stored is deleted right after the download. Google makes video only for
  projects with billing turned on: a key whose project is on the free tier is told so on its first shot, at no cost.
- **Veo** (3.1 and 3.1 Fast) comes through your own Google Cloud project (Vertex AI): a service account's key given
  once (`metarush providers set vertex --key-stdin < key-file`), then `--model vertex/veo-3.1-fast-generate-001`;
  priced by the second, nothing left in the project.

### Presenters
**Generate → Presenter** has a presenter read your lines on camera: a clip's sound or a narration line of the cut
([docs/presenters.md](docs/presenters.md)).
- **In the voice they agreed to**: a person's own presenter speaks in their voice copy or a recording in which every
  voice is named as them, or in an AI voice that is no one's.
- **Made with their consent**: a person's presenter is made at an AI service (D-ID first, on its own key) from their
  approved look, a minute or more of them talking to the camera and their consent recorded on camera, naming the
  service (their own included); a test clip, then a person approves it.
- **Gone when they say so**: retiring it, withdrawing the consent or the consent ending deletes it at the service and
  lists the cuts that use its clips.
- **Stock presenters** (HeyGen's, on its own key) only where the licence covers commercial use and client work.
- Priced first and marked as an AI presenter everywhere; making, testing, approving and retiring a presenter are
  steps only people take.

### Lip sync
**Lip sync** matches a clip's lips to new sound with the sync. service (your own key), only with the consent of
everyone it changes.

### Change a spoken word
Right-click words in a clip's transcript → **Say it differently…** and type the new words: the cut says them in the
speaker's **own voice copy** ([docs/spoken-words.md](docs/spoken-words.md)).
- **Only with their consent**: their recorded consent for new words with the service that keeps their voice copy, for
  a voice a person named — never a child or a public figure.
- **Checked on this computer**: MetaRush asks with the speaker's words around the new ones, then checks that the
  recognizer hears exactly the new words and that the voice sounds like them, fits them in over the room's own quiet,
  and lets you hear before and after.
- **A person decides**: priced first; **Put it in the cut** makes a new version and a review copy, the clip and its
  transcript stay as they were, and Undo puts back what was said. On request **the mouth matches** too (lip sync,
  with everyone's consent). Never automatic.
- **Marked everywhere** as made with AI: the quality check, the export's notes, the receipt, captions files and both
  transcripts. If the consent is withdrawn or ends, a version that plays the new words can't go out until they're
  taken out.

## Your rules

### Content Settings, shared by a client's projects
A project's brand, spelling list, narration voice, house rules, house style, house sign-offs and layout pack live in
its **Settings** tab ([resources](docs/resources.md#content-settings)). Together they are the project's
**Content Settings**, one set per client, shared by its projects; what may leave this computer and the ship gate can
be set there too.
- **Whose settings**: each project uses the default (the project whose **Use for new projects** is ticked), another
  project's (live: changes there show up here) or its own (empty, or a copy).
- **Shared openly**: projects that use another's show them read-only ("From Spring promo — edit them there");
  changing shared settings says "Also used by 3 projects", and a new brand is offered for each latest cut.
- Budgets stay each project's. `metarush content show|follow|own|default`.

### The brand kit and five named logos
- **The brand kit** (a project's Settings tab → Brand) keeps the logos, colours taken from the logo, and font; new
  cuts start with it.
- **Five named logos**: Main, For dark backgrounds, For light backgrounds, Square icon and Wordmark, each uploaded
  right in the Brand part. Cards, name titles, the name bar and slides show the logo made for their background (by
  contrast with the colour behind them), else the main one. `metarush content logo set|remove`.
- **A website's brand comes with its snapshot**: its logo and icons kept as pictures of their own, its colours and
  fonts read, and a suggested kit (`metarush brand suggest --project <p>`, or **Use for this project** on the
  website's details) taken in one step and put on the latest cut as a checked change.

### A house style
The house style is what new cuts start with: captions (on or off, look, size, position), the name bar (band or tab,
about half as tall, still covering the printed name, and whether names animate), the name titles' look, framing,
sound at cuts, an intro and an end card. The latest cuts are offered "Use the house style on the latest cut"; Claude
follows it and the quality check measures captions and the name bar. `metarush content style show|set|apply`. The
[layout pack](#title-cards-lower-thirds-and-graphics) is part of it too.

### House rules and promises
- **House rules** are rules for every video using a project's settings (`metarush content rules list|add|remove`); a
  project's own rules are `metarush project rules`. Claude reads them in every conversation, and the quality check
  measures the ones it can.
- **Promises made to the client, checked on every render**: "captions on every video", "under 60 seconds for shorts",
  "logo in the last 3 s", "no music louder than speech", "nothing generated without disclosure", "no faces cut off",
  "at least 1080p". They are kept as promises (by you, or from a card when Claude hears one) and measured in every
  quality check with a plain sentence and a result; a broken promise holds the version
  ([the ship gate](#ready-to-deliver-or-held)).

### House sign-offs
**House sign-offs** are OKs every project using the settings needs ("Legal OKs it before export"), listed with each
project's own sign-offs and ticked per project. `metarush content signoffs list|add|remove`.

### What may leave this computer, per client
A part of Content Settings says which of a client's things (footage, voices, faces, words, pictures) may go to AI
services (by provider), abilities (by the places they reach), Claude and AI apps (by the terms of the Claude
account), cloud storage, delivery and people outside the workspace (review links), or that nothing leaves
([what may leave](docs/what-may-leave.md)).
- **Asked every time**: every place that sends something asks first and refuses plainly, naming the rule, whose
  settings hold it and who set it.
- **Tightening is easy, loosening isn't**: anyone keeps more here; letting more leave takes a person who is an owner.
- **Proof**: the receipt says what left and where, and "Nothing of … left this computer" when that is so
  (`metarush project leaves`). `metarush content leave [set]`.

### Budgets, limits and estimates
- **Estimates you can open**: generated shots, the AI voice, clearer voices in the cloud and Claude's cards for them
  say what an edit is expected to cost as an estimate ("about $0.40") that opens to what it draws on: the service,
  its model, the clip, script line or picture, the units and each part's price (with the date of the prices;
  ElevenLabs at its pay-as-you-go rates, budgets still count credits).
- **Budgets and daily limits**: paid AI work counts against the project's budget and the team's daily limit, both
  set by a person (Settings → Integrations; `metarush generate configure`).
- **Your preferences**: **Settings → Preferences** has each person's own choices: "Estimate costs of outside AI
  services before edits" (on by default; off, paid steps run within your budgets and limits without the estimate
  step, and under Auto Claude's changes that cost money run by themselves, while consent, approvals and people-only
  steps still ask; an owner can ask everyone for estimates), Ask first or Auto, and light or dark.
  `metarush account preferences [--estimates on|off] [--require-estimates on|off]`.
- **Auto spends only within limits a person set**: with Auto on and estimates off, Claude's changes that cost money
  run by themselves only where a person set the project's budget and the team's daily limit with that service (who
  set each is kept); what Auto spends is on the receipt, and the bell says each day what it spent.

### Consent for anyone's face or voice
- **Generated shots**: someone's likeness only with their consent, recorded on the People page (your own statement,
  or their recording), through a look you check on a test sheet and approve; never a child's or a public figure's.
- **Voices**: copies of a voice only with the person's consent and, for Google, their reading of its consent script.
- **Presenters**: a person's presenter only with their consent recorded on camera, naming the service; withdrawing it
  deletes the presenter at the service ([presenters](#presenters)).
- **Lip sync**: only with the consent of everyone it changes.

### AI disclosure
Everything AI-made says so: generated shots and other AI media are marked as AI-generated everywhere (tiles, the
Script, the cut sheet, the quality check, and every export's notes with what YouTube, TikTok, Instagram and Facebook
ask), AI narration is marked in delivery sidecars, presenters are marked as an AI presenter everywhere, and a promise
like "nothing generated without disclosure" is measured on every render.

## Review and sign-off

### What waits for you, and when things happened
"Awaiting your review" lists the versions Claude and other AI apps made, their drafts, and the ones someone asked to
have reviewed (a review copy asked for, or **Ask for review**; `metarush revision waiting`), not every hand edit.
Versions are Draft, Approved or Needs changes, and renders, versions, clips and resources show when ("Today 3:04
PM"), the exact date on hover.

### Working together
Every computer in the workspace shares the same projects, versions and renders, so collaborators see each other's
edits live; edits that don't overlap are combined, and overlapping ones are flagged rather than overwritten
([collaboration](docs/collaboration.md)). Review comments, on a project, a version or a clip and optionally at a
moment, are added with `metarush comment add` and listed with `comment list`, and search finds them. To hand a
version or a draft to a client or a colleague, `metarush revision packet` makes one page.

### Sign-offs
The OKs a cut needs before it goes out (a project's own, `metarush cut signoffs`, and the
[house sign-offs](#house-sign-offs)) remind you when approving, exporting and delivering, and hold the version while
the ship gate is on.

### QA of the actual render
`metarush qa run` decodes the finished MP4 and checks it against its plan ([docs/qa.md](docs/qa.md)):
- duration, size and frame rate;
- black and frozen frames (planned fades, dips and end cards are told apart from defects);
- loudness and true peak against platform conventions, and silence where the transcript has speech;
- caption/overlay layout;
- narration (over on-camera speech, drowned out, distorted, not matching its script);
- and it listens to the render word by word for repeated or missing speech.

It runs automatically after every render and extracts a labeled contact sheet and frames at every cut so Claude can
look. Fixes are proposed as new revisions, never applied silently.

### Ready to deliver, or held
With the ship gate on (new workspaces; older ones are asked once, and a client's Content Settings can say otherwise),
a version doesn't go out (delivery, or a timeline into a client's delivery folder) while the quality check found
problems, hasn't checked it, or a sign-off is still waiting ([before it goes out](docs/ship-gate.md)).
- The version page says **Ready to deliver** or **Held by …** with one step for each (fix it as a new version, check
  it again, tick the sign-off).
- A person can **Deliver anyway…** with a note that goes into the delivery's record and notes file and onto the
  receipt; Auto and agents never can.
- `metarush deliver ready|override|gate`.

### Share a version with someone outside the workspace
**Share for review…** on a version's page makes an expiring link (up to a week) for a client or a colleague without
an account ([outside reviewers](docs/outside-reviewers.md)).
- They watch its review copy, read what is said if you allow (speakers named, as Speaker 1, 2…, or not at all),
  download it if you allow, and leave comments pinned to times. Their comments come back as the version's comments,
  marked "(outside)", and ring your bell; Claude reads them only as quoted feedback.
- The page is a private, signed page in your own cloud bucket: nothing new reaches this computer. Taking the link back
  deletes its page and video at once; its comment slots go once their last comments are in.
- Making a link is a person's step; owners decide who may send them and for how long; What may leave decides whose
  things may go to people outside the workspace; with the ship gate on, a held version goes out only marked as a
  draft, or with your note; the receipt lists every link.
- A link ends by itself, its last comments in first, when the media moves, the client's rules or someone's consent
  change, or owners turn links off, and you hear why on the bell. `metarush share …`.

## Delivering

### Delivery
`metarush deliver` copies approved final renders to a folder with descriptive names and a JSON sidecar each
(revision, spec hash, sources, music rights), hash-verified, never overwriting, never uploading. Next to the video it
also writes a `.notes.md` file (the version's titles, chapters and show notes, credits for what abilities brought in,
and the note of a version sent out anyway) and a captions file where the place it goes takes one (not TikTok or
Reels); the record carries the receipt, and sidecars credit footage imported from links and mark AI narration.

### Titles, descriptions, chapters and show notes
Claude Code writes a version's delivery notes on your own Claude plan (**Ask Claude to write them** on the version's
page, or `metarush deliver notes set` from a terminal), or you write them yourself; chapters are checked against the
video (the first at 0:00, YouTube's rules said plainly). They sit on the version without changing it, and delivery
writes them into its record and a `.notes.md` file next to the video, ready to paste. No AI calls of MetaRush's own:
no credits, no extra bill.

### Hand a cut to another editing app
**Send to editing app** (or `metarush export timeline`) writes a version as a timeline for **DaVinci Resolve**
(OpenTimelineIO), **Final Cut Pro** (FCPXML) or **Premiere Pro** (Final Cut Pro 7 XML), with the original footage
beside it under readable names: clips with their in and out points, split edits, transitions, narration and music
with their levels and fades, and captions and graphics as markers (captions also as an .srt). It says plainly what a
format cannot carry ([timeline export](docs/timeline-export.md)).

### Post to YouTube as you
An approved version goes to your own channel through your own sign-in on the computer that runs MetaRush: the final
render with the delivery notes' title, description and chapters and the captions, private by default, after you see
exactly what will be sent. It waits for the same quality check, sign-offs and client rules as delivery, and every
post is recorded on the version and the receipt. A person does it every time
([posting to video platforms](docs/publishing.md)).

### The receipt and the cost of the video, at cost
A project's **Receipt** tab (and `metarush project receipt`) lists every request to an outside AI service (service,
model, kind, what was sent, what it cost, whether it was deleted there afterwards, why it ran), the Claude Code
conversations and whose account answered, generated media and what Auto spent ([the receipt](docs/receipt.md)). It
adds the cost up at the services' own prices for the project and for each version (shown on the version's page), and
says what left this computer. Delivered videos carry it in their record.

## Abilities, the catalogue and recipes

### Abilities: capabilities added without a new MetaRush
An ability is a folder with an `ability.yaml` (what it does and runs, inputs, outputs, cost, keys, places, what
leaves) and its script or program, versioned by a fingerprint of the folder ([abilities](docs/abilities.md)).
1. **Checked**: `ability check` runs it once in the sandbox with the network off.
2. **Approved**: an owner approves each version after reading what it may do (**Library → Abilities**, To-dos and the
   bell).
3. **Set up**: a person sets it up on each computer (programs pinned, keys typed by an owner). Abilities can read
   files a person sets up per computer (`ability setup --file`, copied into each run).
4. **Run**: members run it, planned first, in the sandbox, reaching only the places it declared and only through
   MetaRush, which adds its keys so the code never sees them and records every request. What it brings back is added
   with how it was made (`madeWith`).

Who starts a run follows its manifest: free, no key and words-only, anyone (Claude too); a key, a person; priced, a
person who agrees to the estimate; pictures, sound or footage leaving, or something outside MetaRush changing, a
person each time.
- **Found like everything else**: agents find the workspace's abilities with the same `find` as everything else; each
  says what it costs, what leaves the computer and whether it works here, and one waiting for an owner or not set up
  here can't be run.
- **Abilities travel between workspaces**: `ability export` (or Export to a file… on its page) writes a file another
  workspace brings in (`ability import`, or Bring in an ability file…) as a draft, never approved or set up there,
  whatever it was where it came from; a person checks it there (`ability check --version`), then an owner approves
  it.

### A starter pack of seven abilities
([abilities/](abilities/README.md)) Stock videos and photos from Pexels, public-domain and openly licensed media from
Wikimedia Commons and the Internet Archive, sound effects from Freesound, English captions by whisper.cpp kept with
the clip (`metarush footage captions <clip> --language en`), and pictures from ComfyUI on this computer: the one local
service an owner can allow, plain HTTP through MetaRush.
- Each file brought in keeps its licence and credit (a clip's rights; the delivery's `.notes.md`, ready to paste).
- Content Credentials with c2patool and DaVinci Resolve through its MCP server are written in the shape they need,
  marked not yet runnable, with what MetaRush still has to do.

### A menu of what MetaRush does, and what works on this computer
About fifty first-party abilities, in groups: understand footage, fix sources, make media (each kind of AI work),
bring things in, shape the cut, check and deliver. Each is a small annotation on the operation it runs, with a stable
ID receipts and recipes can name ([the catalogue](docs/catalogue.md)). **Library → Abilities** and
`metarush catalogue list [--works-now] [--group …]` show them beside the abilities the workspace added, each saying
**Works here** or what it needs first (a Mac, FFmpeg, a key, Claude Code, an owner's approval, setting it up here),
computed like the first run's "what works here".

### Recipes: make a video that worked again
Save a cut as a recipe, saying what changes each run (footage, words in graphics, words and pictures of clips made
from code, pictures, the AI voice) and what stays (sections, music, captions, the look, rules), then run it with new
inputs ([recipes](docs/recipes.md)). A run makes:
- a new cut or project made through the ordinary steps, fixed sections locked;
- clips made from code drawn again with new words and art (no code to approve);
- moments Claude chooses from new footage;
- a review copy and the quality check.

Every run is planned first with one estimate ("about $0.60", opening to each step's service, model, source and price)
that a person agrees to (or not, with estimates off in Settings → Preferences: budgets still hold). "Again with the
new art and a warmer voice" makes a new version of the same cut.

### Recipes in the dashboard and the Claude panel
- **Library → Recipes**: cards with the example and the last runs; **Run with…** shows the inputs, the plan's steps
  (who does each) and the estimate that opens.
- **A recipe's page**: versions with what each changed in plain words, Claude's drafts, **Make current…** saying what
  it changes, runs and **Side by side**: two runs' review copies playing together.
- **A run's page**: live steps, **Go on** / **Stop**, links to the project, version and review copy.
- **Elsewhere**: **Save as a recipe…** in a project's "…" menu, "Made with the “…” recipe, run N" with **Run again…**
  on what a run made, and **Start from a recipe** on Studio's Home.
- **In the Claude panel** a run, a run again, a save, a change and making a version current are cards with the plan
  and its estimate or what changes; none runs by itself (Auto never starts a run), and Claude hears when a run it
  asked for waits for it. Keeping or changing first picks and saving a sheet or the suggested changes are cards too.

### Sheets, first picks and shorts
- **A sheet**: a recipe is also a **sheet**, Markdown that reads back to exactly the same version:
  `metarush recipe checkout`, edit, `recipe check` (problems by line, what it changes), `--save` a draft only a
  person makes current.
- **First picks**: new footage starts with MetaRush's **first picks** of the moments, each with why, which Claude
  keeps or changes (`recipe picks`).
- **Shorts with every run**: a recipe saved from a cut that plays a conversation can make its **shorts** with every run
  (`--pack shorts`): each its own project, picked, made, checked and waiting for review with the main video, under
  the same estimate. A pack's shorts can come from a recording a run gives.

### Recipes that learn and plan
- Recipes **learn from runs**: what people kept changing in more than half of the approved runs ("You shortened
  “intro” in 4 of 5 runs") is suggested on the recipe's page and to Claude, and saved as a draft only a person makes
  current (`recipe suggestions [--save]`).
- A recipe can **plan a run by itself** when new footage or pictures arrive where an input takes them from, or every
  week, and ring your bell with the plan and its estimate (**Run…**); it never starts one (`recipe triggers`).

### Recipes and your files
- **Art kept in git**: a pictures input can take its pictures from a folder a GitHub repository keeps pictures from;
  a run records the commit and each picture's hash, and puts a file's newer art in its own place.
- **A Google Drive folder as an input** without syncing it: `recipe update --from-drive art=gdrive:Teaser/Art` points
  an input at a folder read through your own rclone (MetaRush never holds a Google token); each run lists it,
  fetches only the files it uses and records each one's Drive ID and hash; `metarush doctor` says how to set rclone
  up.
- **Between workspaces**: `metarush recipe export` (or **Export to a file…** on a recipe's page, **Export** on its
  card) writes one file (the version, its history, its sheet, the pictures it pins, the footage its cut plays and its
  approved code by hash), and `metarush recipe import` (or **Import a recipe…** on the Recipes tab) brings it into
  another workspace as a draft: a new version of the same recipe when their history meets, else a new recipe; its
  things in one project; its words treated as data; its motion graphics and Remotion versions waiting for a check and
  a person's approval there.

### Starter recipes and a sample first
- **Starter recipes**: MetaRush comes with "a call becomes an episode with shorts" and "a release tag becomes a
  release video", offered on the Recipes tab and Home (**Add the starter recipes**, **Use this one**;
  `metarush recipe starters --add`) and brought in as drafts a person makes current.
- **A sample first**: a run with an expensive kind of AI work (the AI voice, generated shots, cloud clean-up) first
  makes one small priced sample of each in a cut of its own. You agree only to its price, watch it, and **Approve the
  sample** (the rest is planned and asks for its own OK), say **Not quite…**, or skip it with a reason; its cost is
  on the receipt.

## Running MetaRush

### Your infrastructure
MongoDB (Atlas or any) for metadata and any S3-compatible bucket (**Tigris on Fly.io**, Cloudflare R2, Backblaze B2,
AWS S3), native **Google Cloud Storage** (service account or Application Default Credentials, no HMAC keys), or a
folder, chosen independently at install ([installation](docs/installation.md)). The suggested setup keeps both on
this computer; a team that shares a workspace across computers uses MongoDB Atlas (or its own server) and shared
storage.
- **Move between them** any time from Settings → Storage (*Move the media to another storage*: check first, then
  move, with progress in the activity bar) or with `metarush storage migrate --to <file>`: every file is copied and
  checked, it can stop and continue, it switches only once everything is there, and the old storage is never changed
  or deleted.
- **Your other computers** are told they still use the old storage and switch with one form and their own keys.

### Plays through a database hiccup
The dashboard remembers where each clip's and render's files are, so when the database briefly stops answering (an
Atlas election, a dropped connection) the preview, the clip viewer and renders keep playing: a cut you opened plays
to the end, clips not reached yet included. When something really can't play, the player says so plainly
("Reconnecting…", "MetaRush can't reach its database right now; it keeps trying."), tries again by itself with
growing pauses and goes on from the same moment.

### More computers, and laptops
- **A machine that renders for the team** (a Mac mini) runs `metarush service install --worker-only`; each computer
  joins with its own storage key and database user (`metarush machine invite`; the
  [worked example](#example-an-always-on-mac-mini-for-your-team), [add a machine](docs/runbook-add-machine.md),
  [deployment](docs/deployment.md)).
- **Laptop-friendly**: big jobs (transcripts, scene analysis, renders) wait while a machine is on battery below the
  level you choose (default 25%), never silently; **Process now** runs them anyway.
- **Works offline**: `cache prefetch` pulls everything a render or review needs onto the machine (resumable,
  verified); the daemon keeps the local cache under its size limit without touching pinned or in-use media;
  `sync status` shows what is local, what is only in the shared store, and what is still in flight.

### Which version you have, and what's new in it
Settings → General (About MetaRush) heads Settings with the version and its day ("MetaRush 0.2.0 · 20 Oct 2026"),
what's new in it from the changelog built into the program (a build with changes not released yet lists them as
"Coming in the next version"), and every computer in the workspace with the version it runs, saying plainly when one
is older; it reads right from another computer too.
- `metarush version` says the same in a terminal (`--details`: the exact build, what's new and your computers;
  `--json` the build alone).
- The number lives in `VERSION`; builds past a release say `0.2.0-dev.3`, and `scripts/bump-version.sh` cuts a
  release ([versions](docs/deployment.md#versions)).

### A log of everything, and ideas from it
MetaRush keeps a record of what it does: every command and change, jobs from start to finish, the programs it runs,
ElevenLabs requests (what they did and cost, never your key or your text) and your conversations with Claude Code
(what you asked, what it answered, what it ran, which changes you accepted). Passwords and keys are removed before
anything is saved.
- **Read it** on the **Log** page ("See log" on a failed job or a problem notification jumps there) or with
  `metarush log`; `metarush log bundle` makes a bug report you can share.
- **Ideas from it**: Claude Code turns the log into improvement ideas (what it had to work around, what kept
  failing) that you keep or dismiss on the **Improvements** page.
- **Your prompts, your call**: what you ask Claude is kept with the workspace (default), on this machine only, or not
  at all; Claude Code sessions in a terminal are recorded only if you turn that on.

### Deleting a project is safe
**Delete project** in a project's "…" menu (or `metarush project delete`, anyone, people only) moves it to **Archived
projects** (linked from Home's Projects): it leaves Home, the lists and search, its footage stays in the Library and
its Claude Code conversations can still be read. **Restore** brings it back as it was, and links to it land on a page
that says it is archived.
- An owner **deletes archived projects for good** (one, or **Clear out archived projects**;
  `metarush project purge <project>|--all-archived`) after a warning that lists what goes: its versions, renders
  (their files only when nothing else uses the same bytes), quality checks, Claude Code conversations, kept things,
  rules, comments, links and Content Settings (projects following them go back to the default and are told).
- **Footage is never deleted**: what only it had goes back to the Inbox, and footage another project's cut uses joins
  that project.
- It is refused while another project's cut plays one of its cuts, and a clear-out that stopped halfway finishes when
  run again.

### Export the workspace, and bring it back
One archive holds everything about the work: projects, versions, transcripts, comments, people and their consent
records, Claude Code conversations and the activity log — and the footage itself when you include it. Use it for a
backup, to move to another computer or service, or to leave with everything you made
([exporting and restoring](docs/workspace-export.md)).
- **Export** from Settings → Storage → *Export the workspace* (owners): check what it would hold, then Export…; or
  `metarush workspace export --to <folder> [--media included] [--tar] [--plan]`. The footage is listed by where it is
  kept, or copied in and checked.
- **It only reads.** An export never updates the database, so it also reads a workspace an older MetaRush left as it
  was, with a database user that may only read; another database's address comes on standard input or from an
  environment variable, never the command line.
- **Private, and whole.** The archive is readable only by your account, and passwords, keys and each computer's own
  settings are never in it. Every kind of record says whether it goes in and why; a manifest checks every file.
- **Restore** into an empty database on the new computer with `metarush setup --restore <archive>` (or `metarush
  workspace restore <archive>`): every file is checked first, the footage is copied in or looked for, the copy is
  checked against the archive and brought up to date, and nothing is ever deleted. Owners and people only, never
  Claude or another AI app.

## Commands you'll use
Every command takes `--json` and `--details`. Plain output speaks plain words (what happened to the video and what to
do next); `--details` adds the IDs, file paths, codes and exact commands, and errors show the command that fixes them;
`--json` has everything, in the same shape as always. `metarush schema show edit-plan` prints the plan schema.

### Setting up and getting around
| Command | What it does |
|---|---|
| `metarush setup` / `setup --yes --json [--database elsewhere --db-uri …] [--storage s3 …] [--service=false]` / `setup status` | set MetaRush up on this computer with suggested answers (again: checks and fixes what is missing) / unattended / what works now, with no keys and with one |
| `metarush components` / `components add <name>` / `components remove <name>` | what MetaRush can use on this computer and what each optional component unlocks / add one, with Homebrew or MetaRush's own checked download (a person, in a terminal) / take off one MetaRush added |
| `metarush database status\|start\|stop` / `database upgrade [--yes]` | the database on this computer: whether it runs, start or stop it (MetaRush starts it by itself when needed; `metarush stop` stops it too) / move it to the version this MetaRush comes with (MetaRush stopped first) |
| `metarush dashboard` | open the console (the **Ask Claude Code** panel talks to Claude Code on this machine) |
| `metarush remote status` / `remote enable --allow <account> [--apply]` / `remote disable [--apply]` | remote access over Tailscale: how it stands (HTTPS certificates, serve, who signs in); turn it on (the address, who signs in by their Tailscale account; `--apply` runs `tailscale serve --bg <port>` after asking); turn it off |
| `metarush remote allow <account>` / `remote remove <account>` / `remote signout [<account>\|--all]` / `metarush dashboard --print-link --remote` | who signs in by their Tailscale account (people only); sign out other computers (a lost device); a one-time link for a browser on another computer |
| `metarush extension path` / `extension pair` / `extension list` / `extension revoke <id>\|--all\|--everyone` | the Send to MetaRush browser extension: folder to load, pair a browser (in your terminal; here it is the owner's), your browsers (an owner: everyone's), unpair one, all of yours, or everyone's |
| `metarush activity [--watch]` | what is running or waiting (and why), on every machine |
| `metarush notifications list` / `notifications read --all` | your notifications |
| `metarush search "<words>"` | what was said (with timecodes), what's on screen, people's names, titles, projects, versions, comments |
| `metarush job show <id>` / `job run-now <id>` | a job in plain words / run it now, even on battery |

### Footage and the Library
| Command | What it does |
|---|---|
| `metarush ingest <files or folders> --project <id> --wait` | import + process footage |
| `metarush import <link>… [--project <id>] [--wait]` (`--dry-run` looks it up only) | import videos from YouTube, TikTok or X links |
| `metarush import <link> --browser-signin chrome` · `… --playlist 5` | download with your browser's sign-in (this import only) · the first videos of a playlist or channel |
| `metarush import --check-tools` / `import --update-tools` / `import --auto-update off` | the downloader (yt-dlp) on this machine: version, update it, automatic weekly updates |
| `metarush import cloud <remote:folder> (--list \| --name <file> \| --id <id> \| --all) [--project <project>]` | files from a Google Drive or Dropbox folder through your own rclone, without syncing it |
| `metarush import timeline <file> --project <project> [--relink <footage>=<clip>] [--add <footage>=<file>] [--skip <footage>] [--read]` | a timeline from another editing app (.otio, .fcpxml, .fcpxmld, .xml, .edl) as a new cut, footage matched, questions asked, what can't come over listed |
| `metarush add <files, folders or links>… [--project <project>]` | add anything: footage to Sources (or the Inbox), pictures, SVGs, PDFs and fonts to Resources, folders linked, video links imported |
| `metarush asset move <asset> --from "Test project" --to "Spring promo"` / `asset where <asset>` / `asset delete <asset> --forever` | move footage between projects; where it is used; delete it for good (a person, in a terminal) |
| `metarush library [inbox\|collections\|archived\|projects\|trash] [--collection B-roll] [--search "beach"]` | the Library: each view with counts; a search covers all of them, the Trash included |
| `metarush asset archive <asset>… [--project <id>]` / `asset unarchive <asset>…` | file clips in the Archive (out of the Inbox; with `--project`, out of that project too) and back |
| `metarush collection create "B-roll"` / `collection add B-roll <asset>… [--create]` / `collection remove` / `collection list` / `collection show` / `collection rename` / `collection delete` | named shelves for footage; deleting one never deletes a clip |
| `metarush project link "Spring promo" B-roll` | a project uses everything on a collection (clips put on it later too) |
| `metarush asset trash <asset>… [--project <id>]` / `asset restore <asset>…` / `trash list` | move clips to the Trash (refused while a cut uses them) and back; what is there, from where, how big |
| `metarush trash empty [<asset>…]` / `trash settings [--empty-after 30 \| --off]` | delete what is in the Trash for good, after saying how much (a person, in a terminal); empty it on its own after N days (off by default) |
| `metarush asset rights <music-asset> --license "royalty-free library"` | record where music comes from |
| `metarush ingest precheck <files>` / `ingest --keep-both` | find duplicates before importing / keep both names |
| `metarush asset update <id> --title "<title>"` / `asset update <id> --suggested` | rename a clip (the filename stays) / take MetaRush's better name |
| `metarush asset card <clip>` / `asset names [--accept-all]` | a clip's info card / the better names MetaRush has (and using them all; names you typed stay) |
| `metarush asset fixes <clip> [--tick steady,colour] [--untick grain] [--none] [--kind call] [--look]` | a source's fixes: what fits and why; tick or untick them (a fixed copy is made, the original kept) |
| `metarush watch status` / `watch set <folder> [--create]` / `watch off` / `watch choose --set` | the watched folder |

### Transcripts, speakers and faces
| Command | What it does |
|---|---|
| `metarush footage moments <project>` / `footage transcript <asset>` / `footage sheet <asset>` | evidence (moments include cuts, slow changes and movement, with frames to look at) |
| `metarush footage sheet <asset> [--from 0:40 --to 1:10] [--mode smart\|even] [--details]` / `footage keyframes <asset>` | a labeled contact sheet: one frame per shot, changes, new speakers (or evenly spaced); which frames and why |
| `metarush footage visuals <asset> [--from … --to …]` / `footage visuals status` | what's on screen, line by line (Mac: Apple Vision); whether clips get looked at here |
| `metarush footage faces <asset>` / `footage faces name <face> "Ada"` / `footage faces reject <face>` | faces in a clip and whose they are (named by someone, or recognized by the face and not confirmed yet); name one (which teaches MetaRush the face), or take a name back |
| `metarush footage describe <asset> [--wait]` / `footage describe settings --on [--project <id>]` | written scene descriptions by your Claude Code (optional, uses your Claude Code usage) |
| `metarush footage speakers <asset> [--samples]` / `footage transcript <asset> --speaker "Ada"` | who speaks, clips to listen to; one person's lines |
| `metarush footage speakers rename <asset> "Speaker 2" "Ada"` / `speakers hint <asset> --count 2 --wait` (or `--more`) | name a voice (remembered across videos); say how many people talk |
| `metarush footage language <asset> [--choices]` / `footage language <asset> --set es [--wait]` | which language a clip is spoken in and how MetaRush knows; set it, which writes its words down again in that language |
| `metarush footage speakers assign <asset> --from 0:12 --to 0:15 --person "Ada"` (`--new-person "<name>"`, `--unsure`) / `speakers unmerge <asset> <voice>` | fix who said a line; separate two voices that were combined |
| `metarush footage speakers detect [<asset>…\|--all] [--wait]` | find who speaks (again), e.g. in clips imported before speaker detection |
| `metarush transcript find "Fern Brook" --project <p> [--with FernBrook]` → `transcript replace "Fern Brook" "FernBrook" --project <p> [--only <place>…] [--spelling]` | find every place a word is said (with the words around it), then change them; joins and splits keep their timing |
| `metarush transcript fix <clip> --at 0:12.4 "FernBrook"` (or `--from … --to …`) / `transcript speaker <clip> --from … --to … --person "Maya"` | retype what is said; say who says a stretch |
| `metarush transcript history <clip>` / `transcript undo <fix>` (`--batch <batch>`, `--last <clip>`) / `transcript show <clip> --original` | a clip's fixes with who and when; undo one or a whole replace; the computer's own transcript |
| `metarush transcript spelling add FernBrook [--project <p>] --heard-as "Fern Brook" [--fix-existing]` / `spelling list\|remove\|apply [--settings-of <p>]\|suggestions` | how names and terms are written (the list of a project's Content Settings; without `--project`, the default's): new transcripts are corrected and the transcriber expects them |
| `metarush transcript pending list <project>` / `transcript pending apply <project> [--no-render]` | text fixes since the latest version; one new version and one review copy for all of them |
| `metarush people list` / `people show "Ada"` / `people update "Ada" --notes "…"` / `people rename\|merge\|split\|rebuild …` | everyone you named, where they speak, notes about them; fix or rebuild a voice |
| `metarush people learned "Ada" [--asset <asset> --voice "Speaker 2" \| --face <face>]` / `people unlearn "Ada" (--asset <asset> --voice "Speaker 1" \| --face <face>) --as "Rex" [--new-person] \| --unsure` | what a person's voice and face are learned from, how alike each is (to a voice or face in question too), the odd one marked; take a wrong one back and learn them again |
| `metarush todos list [--project <id>]` / `todos answer <id> "Ada"` (or `--suggestion 1`, or `--same-as <id>` for faces) / `todos dismiss <id>` | questions MetaRush needs you to answer |
| `metarush todos not "Maya" [--project <id>] [--asset <id>]` / `footage faces not-here <asset> "Lena"` / `todos skip-faces --asset <id>\|--project <id>` / `people evidence` | answer many face questions at once: none of them are Lena, Lena is nowhere on screen in that clip, skip them all; how often each kind of hint about people was right (the ones often wrong only offer quick picks) |
| `metarush todos groups [--project <id>] [--asset <id>]` / `todos accept-shown <asset>` | who is on screen: one question per person per clip (faces, how often they are seen, why a name is suggested); use the names a video shows for everyone in it |

### Calls and screen recordings
| Command | What it does |
|---|---|
| `metarush recording list [--project <id>]` / `recording show <id> [--details]` | screen recordings made in the dashboard: tracks, length, footage, projects |
| `metarush recording cut <id> [--layout pip\|side-by-side\|screen\|camera] [--apply]` / `recording link <id> --project <id>` / `recording finish <id> --keep` | start a cut from a recording, link it into a project, keep what arrived after a closed tab |
| `metarush conversation import <folder> --project <id>` / `conversation create --project <id> --members a.mov b.mov --names "Ana,Bo"` | a call's recordings (Zoom, Riverside, SquadCast, one per person) lined up as one conversation |
| `metarush conversation cut "<name>" --style podcast\|interview\|panel\|reactions\|broll [--shape vertical] [--apply]` | the automatic cut: whoever talks, reactions, both on screen when they talk together, a reason per shot |
| `metarush conversation angle --project <id> --at 0:31.2 --member sam` / `--layout side-by-side` | change what a conversation clip shows from a moment on |
| `metarush conversation cleanup "<name>"` / `conversation decide "<name>" c1 --accept` | call talk, silences, frozen webcams, dropouts and app labels to accept or skip |
| `metarush conversation match "<name>" --apply` / `conversation nudge "<name>" --member sam --frames 1` | matching webcams (measured); a recording a frame later |
| `metarush conversation moments "<name>"` / `conversation short "<name>" --from 0:25.7 --to 0:32.8 --apply` | the best moments; a short from one, as a new project |
| `metarush footage switches <clip>` / `footage find-switches <clip> [--wait]` | a call recording's picture switches to the frame, who is on screen in each shot, the names the call app printed / look again |
| `metarush cut flashes <project>` / `cut names <project> [--first-names] [--style band\|tab] [--keep-titles]` | trim the split-second flashes at the clips' edges at the exact switch / cover the printed names with your own name bar for the whole video (each shows the change first) |

### Claude and other AI apps
| Command | What it does |
|---|---|
| `metarush assistant ask "<message>" --project <id> --wait` / `assistant continue <id> "<message>" --wait` | the panel's conversations from a terminal (`--model`, `--effort`, `--raw`) |
| `metarush assistant ask "<message>" --attach logo.png --project <p>` | attach pictures or PDFs to a message (Claude can look at them; MetaRush asks whether to keep them) |
| `metarush assistant conversations list [--project …] [--mine \| --all-users] [--search …]` / `conversations show <id>` | browse conversations from every machine (a project's: everyone's; otherwise yours) |
| `metarush assistant proposals <id>` → `assistant apply <id> <plan> --confirm <token>` / `assistant act <id> <card>… [--all] [--save-only] [--choice N] [--answer "…"] [--not-quite "…"]` | apply a plan / run, answer, send back with what should change, or set aside what Claude asked for (several at once, a question card; a save also renders a review copy) |
| `metarush assistant mode [ask\|auto]` · `metarush project so-far <project>` | yours: Ask first, or Auto (Claude's changes that can be undone run by themselves) · what happened in a project so far (what a new conversation there starts with) |
| `metarush assistant status` / `assistant settings [--model …] [--effort …] [--limit N] [--compact-at 200k] [--cache-hour on\|off] [--hold-news on\|off] [--safe-steps on\|off]` | can Claude Code run here (billing mode, limits, your plan's usage, what waits for you)? defaults, the token limit, summaries, the cache; safe steps by themselves |
| `metarush keep source\|logo\|brief\|rule\|check\|person\|signoff\|improve\|spelling … --project <p>` (`rule`/`check` `--every-project`: a house rule) / `keep list --project <p>` / `keep decline <item>` | keep what a conversation brought in (a website, a logo for the brand kit, the brief, a rule, a fact about someone, a sign-off, an idea for MetaRush, a spelling); what was offered, kept or declined |
| `metarush improve add "<what MetaRush couldn't do>"` | put one idea on MetaRush's improvement list |
| `metarush mcp serve [--allow-changes \| --propose] [--agent <name>] [--toolset core\|review\|edit\|library\|admin\|all]` / `mcp tools [--toolset …]` | MetaRush for Claude Desktop, Claude Code, Codex, Cursor and other MCP clients (stdio; read-only unless allowed): the core's find, describe, run (reads) and run_change (changes) reach every operation; a toolset offers one kind of work as tools with modes, reads and changes apart; `--propose` saves every change as a draft for a person to review |
| `metarush draft save [operation] [--input …] [--sheet <file>] [--plan <file>] --note <why>` / `draft list [--by <app>\|--all]` / `draft approve\|not-quite\|turn-down <draft>` | An AI app's drafts: propose a change without making it, list drafts and decisions; a person approves (a version goes into the cut), says Not quite with what should change, or turns it down |
| `metarush revision packet <draft or version> [--markdown] [--out <file>]` | One page for a draft or version: what changed and why, checks, cost, who made it, what's waiting |
| `--agent <name>` / `--propose` (any command; `METARUSH_AGENT`, `METARUSH_PROPOSE`) | Name the AI app running the command; hold it to proposing (changes become drafts, nothing people-only runs) |
| `metarush ops find <what you want to do> [--details]` / `ops describe <name>` | what does a task, best first, one line each / how to use one (its input with an example, its command) |
| `metarush skills eval [--details] [--live [--mcp <toolset>] [--limit N]]` | how often the search picks the right operation for 186 everyday requests; `--live` asks real Claude Code (your Claude account; never in CI) |

### Editing a cut
| Command | What it does |
|---|---|
| `metarush project create "<name>" --tone intimate --platform shorts` | new project with a brief |
| `metarush project link "Spring promo" Brand [--only kind=resources] [--items intro]` / `project links` / `project unlink` | use another project's things (everything by default); what stays when unlinking |
| `metarush cut nest <project> Brand` (`--revision r4`, `--after hook`) / `cut copy-in <project> Brand` / `cut update <project>` / `cut where <project>` | play another project's cut inside this one, or copy it in; update pins to the latest; which cuts play this one |
| `metarush edit check --plan plan.json` → `edit apply --plan plan.json --confirm <token>` | change a cut |
| `metarush revision compare <a> <b>` (`--format md`: as cut sheets) | what changed |
| `metarush cut show <project> [--revision r3] [--out story.cut.md]` | the cut as an editable cut sheet |
| `metarush cut check story.cut.md` → `cut apply story.cut.md --confirm <token>` | check an edited sheet, then save it as a new version |
| `metarush cut diff story.cut.md` | what an edited sheet changes, and whether someone saved a newer version |
| `metarush cut transition <project> 3 dissolve 0.5` · `… lena cut --lead 0.4` · `… 4 cut --auto` | change one cut: the transition, a voice that starts early or carries over, or a sound change in the nearest pause (shows the change first) |
| `metarush cut framing <project> follow\|faces\|center [--clip 3]` | how crops are framed: follow the subject as it moves, keep faces in frame, or keep the middle (the whole cut or one clip; shows the change first) |
| `metarush footage short <project> --clip <asset> --from 0:12 --to 0:41 [--piece <asset>@<from>-<to> …] [--name …] [--look bold] [--framing follow\|faces\|center] [--shape vertical\|square]` | a short from any footage: a new cut for phones, the crop following the subject, captions on (shows it first; approved like any cut) |
| `metarush cut captions <project> [--look bold] [--size large] [--position top] [--words-per-line 3] [--on\|--off]` / `captions looks [--project <p>]` | how the cut's captions look (six vetted looks; shows the change first); the looks and the house style's |
| `metarush cut move <project> kitchen start` · `cut trim <project> 2 --end -0.5` (`--roll`, `--slip 0.3`) · `cut split <project> hook '"what would"'` | move, trim or split one clip (named as in the cut sheet, or by number); shows the change first, the same command with `--confirm <token>` saves it |
| `metarush cut add <project> interview 0:12 0:18 --after hook` · `cut delete <project> broll` · `cut delete-range <project> 0:10 0:14` | put footage in; take clips, or a stretch of the video, out (the rest closes up) |
| `metarush cut remove-words <project> "um, anyway"` (`--which 2`, `--all`) · `cut remove-words <project> --ums` (`--pick 1,3`) | take words out, cutting in the pauses around them; suggested ums, repeated words and false starts |
| `metarush cut pauses <project> [--longer-than 1s] [--to 0.5s] [--clip c] [--pick 1,3]` | shorten the long pauses between words anywhere in the cut (any footage, calls included); the dry run lists them, saving takes all or those picked (the Transcript tab's **Tighten pauses** plays each one first) |
| `metarush edit preview --plan plan.json` | what a plan would do, saving nothing (what the timeline shows after each gesture) |
| `metarush project checkout <project> <folder>` / `project status` / `project pull` / `project push` | the cut as files in a folder (no media copied) |
| `metarush edit transitions` | every transition and sound change at a cut, in plain words, with lengths and directions |
| `metarush project update <id> --transition-style natural\|snappy\|smooth\|playful` | how transitions should feel in this project |
| `metarush cut use-fixed <project> <clip> [--original]` / `cut rename <project> "<name>"` / `revision note <version> "<note>"` | a new version that plays a fixed copy (or the original) / name a cut / a version's note |

### Narration and sound
| Command | What it does |
|---|---|
| `metarush voiceover script import script.txt --project <id>` → `voiceover scratch` / `voiceover tts --estimate` | narration lines placed against the cut; free timing voice; AI voice cost |
| `metarush voiceover import narration.wav --project <id>` / `voiceover record --project <id>` | your own read, matched to the script and placed |
| `metarush voiceover record --live --from 0:12 --project <id>` / `voiceover takes place take.wav --from 0:12 --project <id>` | one take while the video plays: it lands where you spoke, each line where you said it (`--until` re-records a stretch) |
| `metarush voiceover takes place rd_… --planned --project <id>` / `… --at-recorded` | a live take's lines at their planned spots, or back where you said them |
| `metarush voiceover script fit --project <id>` / `voiceover script use original\|fitted` | Claude Code fits your script to the video (shown first, your original kept); switch versions |
| `metarush voiceover script import script.txt --project <id> --clip <shot>` | narration for one shot: it starts with the shot and replaces only the lines over it |
| `metarush voiceover beat move <line> --to 0:12.4 --project <id>` (`--exact`) / `beat add --text "…" --to 0:08` | put a line at a point of the video; it ties itself to the shot playing there (or keeps the clock time) |
| `metarush mix show --project <id>` / `mix set --bus music --gain -3` / `mix set --mute-source` | levels; music and camera sound get quieter under the voice |
| `metarush mix set --project <id> --bus music --gain -6 --preview` / `… --apply` | hear how a mix change would sound before saving it; or save it straight away |
| `metarush mix set --project <id> --level-speech on` / `--cut-blend 0.1` | even out voices between clips; blend the sound at straight cuts |
| `metarush sound show --project <id>` / `sound levels --speaker "Maya" --gain 3` | the sound in plain words; one person louder or quieter only while they speak |
| `metarush sound enhance Lena --project <id> [--mode clean\|studio\|cloud] [--estimate]` / `sound compare` | a clearer voice (the recording is kept); how much clearer it got |
| `metarush sound music add song.m4a --project <id>` / `sound music set music --level -16 --duck off` | music fitted to the video, ending on a phrase (`--ending phrase\|beat`); its level and dips |
| `metarush sound setup studio` | studio quality on this computer (about 1.8 GB, once; clean sets itself up) |

### Graphics, resources and Remotion projects
| Command | What it does |
|---|---|
| `metarush resources list [--project <project>]` / `resources show logo` / `resources where logo` / `resources path logo` | the logos, pictures, PDFs and fonts a project can use (its own, linked projects', the workspace library's) |
| `metarush resources add logo.svg --project <project>` (`--workspace`, `--name logo`) / `resources update logo --tags brand` / `resources promote logo` / `resources remove logo` | add, describe, share with every project, remove |
| `metarush resources link ~/Brand --project <project>` / `resources folders` / `resources folder Brand --upload all` / `resources unlink Brand` | a linked folder kept in step (per machine; large files upload when a cut first uses them; a file that couldn't be added is tried again, at once with `--rescan`) |
| `metarush resources add https://github.com/acme/widget --project <project> --wait` / `resources add https://acme.com --depth 1 --wait` | a GitHub repository pinned at a commit, or a dated web page snapshot (and the same-site pages it links to) |
| `metarush resources path widget` / `resources show widget` | a repository's read-only copy for Claude to read / its overview (README, languages, history, what was made from it) |
| `metarush resources make code widget --file main.go --lines 40-72` / `make diagram widget --from arch.mmd` / `make stats widget` | a code slide, an architecture diagram (Mermaid or D2) or a repository card, with exact provenance |
| `metarush resources refresh widget --wait` | a repository's newest commit or a page's new snapshot (new versions; `metarush cut update` moves cuts to them) |
| `metarush resources pictures widget [--keep art/characters] [--drop art/old] [--wait]` | the pictures a repository keeps from its folders, each a picture of its own pinned to the commit; keep another folder's or stop keeping one in step |
| `metarush motion new <name>` → `motion check motion/<name> --project <id>` | start a project motion graphic from a working example; check it (imports, types, test frames, contact sheet, preview) |
| `metarush motion list` / `motion show <name>[@version] [--source]` / `motion studio <name>` | the motion graphics and whether a person approved them; one version in detail; preview it in Remotion Studio |
| `metarush motion approve <name>@<version>` (`--reject`) / `motion revoke <name>@<version>` | people only (dashboard or a terminal): approve or turn down one exact version; withdraw an approval |
| `metarush import remotion <folder \| GitHub address> --project <id> [--wait]` | bring in a Remotion project: its files, packages, fonts and compositions; the videos in its `out/` as footage |
| `metarush remotion list --project <id>` / `remotion show <name>[@version]` / `remotion stills <name> <composition> --at 1.5,3` | its compositions (size, frame rate, length), versions and approvals; one version (the check, what was left out); frames of any version |
| `metarush remotion use <name> <composition> --project <id> [--props '{…}'] [--from s --to s]` / `remotion render …` | make a composition of an approved version into a clip and put it in the cut / only make the clip |
| `metarush remotion checkout <name> <folder>` → `remotion check <folder> -m "…"` / `remotion update <name> --project <id>` | edit its code on MetaRush's own copy and check it (words, type check, before/after stills) / move the latest cut to the newest approved version |
| `metarush remotion approve <name>@<version>` (`--reject`) / `remotion revoke` / `remotion allow` | people only (dashboard or a terminal): approve or turn down a version; withdraw an approval; allow new packages |
| `metarush remotion map <name>[@version] [--composition C] [--again]` | its scenes with times and stills, words on screen and where they are written, pictures, sounds (timing, level, role), fonts, timing data |
| `metarush remotion words <name>` / `words setup <name>` / `words set <name> KEY="…" [--check]` | its on-screen words; make them settings (a version a person approves); change them as settings (checked in the sandbox, clips made again) |
| `metarush remotion pictures list <name>` / `pictures setup <name>` / `pictures set <name> HERO <file\|resource> [--check]` (`--original HERO`) | its pictures; make them settings (a version a person approves); replace them as settings (checked in the sandbox, clips made again); `remotion use … --picture HERO=<file\|resource>` gives one clip its own |
| `metarush remotion use <name> <composition> --sound layers` | the picture alone, with its music, voice lines and effects as the cut's own tracks and narration |
| `metarush remotion lift <name> <composition> [--scene N] [--name end-card] [--library brand-theme]` | a scene as a motion graphic, the project's theme as a shared library (both checked, approved by a person) |
| `metarush remotion refresh plan <name> --step "npm run vo" --key elevenlabs=ELEVENLABS_API_KEY --output public/voice/` / `refresh start …` / `refresh runs <name>` / `refresh log <name> [N]` | what a refresh from its own steps would do (commands, keys and their names, where it writes, hosts); start it (people only); past refreshes; what one printed (keys hidden) |
| `metarush remotion push plan <name>[@version] [--branch b] [--no-pr] [--words]` / `push start …` / `push list <name>` | an approved version's changes in plain words and where they go; send them back as a new branch and a pull request (people only); what was sent |

### AI services
| Command | What it does |
|---|---|
| `metarush providers set elevenlabs` / `providers status elevenlabs` | add this machine's ElevenLabs key (hidden prompt), check it |
| `metarush providers set gemini` / `generate settings --project <id>` | add this machine's Google Gemini key; models, prices, the project's budget and today's limit |
| `metarush generate estimate "a red paper boat on a pond" --project <id> --seconds 3` / `generate shot … --max-cost 0.20 [--wait]` | price a generated shot (the estimate's parts: the video, what is sent, the thinking allowance), then make it with the price you agreed to — or, with your estimates off, within your budgets (`--from <clip>@0:12`, `--look <name>`, `--takes 3`) |
| `metarush generate again\|extend\|edit <clip>` / `generate list --project <id>` / `generate show <generation>` | new takes, a longer or changed copy (each priced first); what was made and how |
| `metarush consent record --person <name> --self --adult --statement "…"` / `looks create\|test\|approve` | consent and looks for a person's likeness (people only) |
| `metarush ai models [--kind music]` / `ai estimate picture "a kite" --project <id>` / `ai make music "calm guitar" --seconds 30 --max-cost 0.08` | AI services: models with prices and terms; price, then make a picture, speech, a sound effect, music, a voice change (`--clip`), a dub (`--into es`) or a cloud transcript |
| `metarush ai voices copy --person <name> --sample <clip> --consent-clip <clip>` / `ai voices list\|remove` / `ai rules show\|set --project <id>` | voice copies (people only, with consent); which AI services a project's work may use |
| `metarush ai list --project <id>` / `ai show <work>` / `ai cancel <work>` | what AI services made, what it cost and what they still keep |
| `metarush ai local` / `ai local setup kokoro-v1` / `ai make upscale --picture logo --scale 4` / `ai make background-removal --picture logo` | the AI models on this computer (free, nothing leaves it) and setting one up (owners); a picture made larger or its background taken away |
| `metarush presenter start --person <name>` / `presenter make <presenter> --take <clip> --reading <clip>` / `presenter test\|approve\|retire <presenter>` | a person's own presenter at an AI service, with their consent recorded on camera (people only, priced first) |
| `metarush ai make presenter --presenter <presenter\|stock:<id>> --clip <clip>` (or `--line 3`) / `presenter list\|show\|stock` / `presenter opt-out <service>` | a presenter reading a clip's sound or a narration line on camera; presenters, stock presenters by licence, a service's training opt-out |
| `metarush cut say estimate\|make <project> (--clip <c> --from <s> --to <s> \| --quote "…") --as "new words"` · `cut say place\|undo\|mouth\|show <change>` · `cut say list <project>` | change a word or short phrase someone said into new words in their own voice copy, with their consent: priced first, people only, checked on this computer, put in the cut as a new version, undone, the mouth matched on request |

### Your rules
| Command | What it does |
|---|---|
| `metarush content show [--project <p>]` / `content follow <other> --project <p>` / `content follow --default --project <p>` / `content own --project <p> [--copy]` / `content default --project <p>` | Content Settings (brand, spelling list, narration voice, house rules, house style, house sign-offs): whose a project uses — the default's, another project's (live) or its own — and "Use for new projects" |
| `metarush brand show [--project <p>]` / `brand set [--project <p>] --logo logo --logo-dark logo-white [--logo-light …] [--logo-icon …] [--logo-wordmark …]` (colours from the logo) / `brand apply <project>` | the brand of a project's Content Settings (without `--project`, the default's); the latest cuts that could use a new one; put it on an existing cut |
| `metarush brand suggest --project <p>` / `brand set --project <p> --from-site <page> [--apply]` | a website's logo, colours and font as a suggested kit, with why; take it as the brand of the project's own settings (and put it on its latest cut) |
| `metarush content logo set <main\|dark\|light\|icon\|wordmark> <file or resource> [--project <p>]` / `content logo remove <which>` | the brand's five named logos (a file is added to the resources first); graphics show the one made for their background |
| `metarush content style show\|set\|apply [--project <p>]` (`set --captions on --caption-size large --name-bar-height half --name-bar-animates never --end-card endcard.cta --end-card-headline "…"`) | the house style new cuts start with; `apply` puts it on a project's latest cut as a checked change |
| `metarush content pack show\|preview [--project <p>]` / `content pack set [--project <p>] --role lower-third --template lower-third.cinematic --fields '{"align":"center"}'` (`--pip-shape circle --pip-frame accent --gap wide --backdrop brand`, `--remove <role>`, `--reset`) / `content pack apply --project <p>` | the layout pack: what draws each title card, lower third, end screen, quote card and chapter card, and call layouts' looks; `apply` puts it on a project's latest cut as a checked change |
| `metarush content pack propose --file pack.json [--project <p>] [--dry-run]` / `content pack accept` (people only) / `content pack decline` | a whole pack proposed (its graphics may be motion graphics, each approved by a person), made current by a person once they are approved, or set aside |
| `metarush content rules list\|add\|remove [--project <p>]` | house rules: rules for every video using a project's settings (a project's own are `metarush project rules`) |
| `metarush project rules <p>` / `project rules add <p> "Captions at the top" [--check]` / `project rules remove <p> 2` | a project's rules: Claude follows them, the quality check measures what it can |
| `metarush project rules add <p> "<words>" --promise` / `metarush keep promise "<words>" --project <p> [--every-project]` | a promise made to the client, measured in every quality check |
| `metarush content signoffs list\|add\|remove [--project <p>]` | house sign-offs every project using the settings needs (each project ticks its own with `metarush cut signoffs done`) |
| `metarush cut signoffs <p>` / `cut signoffs add <p> "Lena OKs the final version"` / `cut signoffs done <p> 1 [--note …]` (`undo`, `remove`) | the OKs a cut needs before it goes out: a reminder when approving, exporting and delivering |
| `metarush content leave [--project <p>]` / `content leave set [--project <p>] [--nothing] [--keep ai=footage,voices] [--let …] [--only claude=commercial] [--anyone …] [--note …]` | What may leave: which of a client's things may go to AI services, abilities, Claude and AI apps, cloud storage, delivery and people outside the workspace, and to whom (people only; letting more leave takes a person who is an owner) |
| `metarush account preferences [--estimates on\|off] [--require-estimates on\|off]` | yours (Settings → Preferences): whether costs of outside AI services are estimated before your edits (on by default); owners can ask everyone for estimates |
| `metarush assistant spending [--project <p>]` | where Auto may spend money by itself (the budgets and daily limits a person set, and who) and what it spent for you today |
| `metarush project update <p> --deliverable "by email" --deliverable "social shorts"` | where the video goes (each place carries its shape: social shorts are vertical) |

### Review and delivery
| Command | What it does |
|---|---|
| `metarush render submit --revision <id> --quality review --wait` | real MP4 (a review copy: quick, half size) |
| `metarush render follow [on\|off]` | whether a review copy of each cut's latest version is made by itself about a minute after the last change (on by default) |
| `metarush revision waiting` · `revision ask-review <version>` | what waits for review (Claude's drafts and ones someone asked about); ask for a version to be reviewed |
| `metarush qa run <render-or-revision> --wait` / `metarush qa show <id>` | decode + check the render, frames to look at |
| `metarush deliver ready <project> [--version rN] [--json]` | Ready to deliver, or what holds the version (the quality check, promises, sign-offs) and the step that clears each |
| `metarush deliver override <project> --version rN --note "…"` | send a held version out anyway (a person in their own terminal); the note is kept and shows on the receipt |
| `metarush deliver gate [on\|off]` / `metarush content gate <on\|off\|workspace> [--project <p>]` | whether versions wait for the quality check and sign-offs (owners; off is a person's) |
| `metarush deliver --revision <id> --out ~/Deliveries/<project>` | approved finals + JSON sidecars to a folder |
| `metarush deliver notes show <project> [--version rN]` / `deliver notes set <project> --from notes.md [--by claude]` (or `--title … --chapter "0:00 Intro"…`, `--clear`) | a version's title, other titles, description, chapters and show notes; delivery writes them next to the video |
| `metarush captions check <project> [--version rN]` / `captions file <project> [--format srt\|vtt] [--version-words] [--language es] --out <file>` / `deliver captions <folder>` | words to look at before a video goes out; a captions file with every fix (no new render), or in another language; update delivered captions files |
| `metarush captions languages <project>` / `captions lines <project> --to es [--missing]` / `captions translate <project> --to es --from es.json [--human]` / `captions remove-language <project> --to es` | captions in other languages: what is translated, the lines to translate with the glossary, keep a translation (marked machine-translated unless a person did it), take a language off |
| `metarush export timeline <revision> --format otio\|fcpxml\|xmeml --out <file> [--media <folder>] [--link]` | the version as a timeline for DaVinci Resolve, Final Cut Pro or Premiere Pro, footage beside it under readable names |
| `metarush publish platforms\|setup youtube\|connect youtube` | YouTube on this computer: set up by an owner once with their own Google client, each person signs in with their own channel |
| `metarush publish plan\|post <project> [--version rN] [--privacy private\|unlisted\|public]` | exactly what posting a version would send and what holds it; then post it as you (people only) |
| `metarush share plan\|create <project> [--version rN] [--days 7] [--words [--speakers names\|labels\|none]] [--download] [--for <name>] [--message <note>] [--as-draft] [--anyway <note>]` | Share a version for review with someone outside the workspace: what a link would show and whether it may be made; making one is a person's step and prints the link to send |
| `metarush share list [<project>] [--version rN] [--all]` / `share link\|revoke <link>` / `share sync [<link>]` | the review links sent (what each shows, until when, comments back); the link again; take one back (its page and video at once, its comment slots once their last comments are in); bring in new comments now |
| `metarush share settings [set --on\|--off --owners-only\|--everyone --max-days N]` | who may send review links and for how long (changing it: owners) |
| `metarush project receipt <project> [--version rN]` | the receipt: every request to outside AI services, Claude's conversations and whose account answered, generated media, Auto's spending, the cost at cost per version and project, and what left this computer |
| `metarush project leaves <project>` | what of a project left this computer and where (AI services, Claude and AI apps, abilities, delivery, people outside the workspace, cloud storage), and what its What may leave kept here |

### Abilities, the catalogue and recipes
| Command | What it does |
|---|---|
| `metarush recipe propose --from <project>` / `recipe save --from <project> [--vary key] [--keep key] [--pack shorts]` | what a recipe from a cut would vary and keep / save it (a person's save is current, Claude's a draft; `--pack shorts` makes its shorts with every run) |
| `metarush recipe checkout <recipe> [file]` / `recipe check <file> [--save [--note …] [--write]]` | the recipe as its sheet (Markdown, reads back exactly) / what an edited sheet changes and its problems by line; `--save` makes a draft |
| `metarush recipe picks <recipe>#N --keep` / `recipe picks <recipe>#N --part moments --n 2 --from 0:12 --to 0:19` | keep a run's first picks of the moments and the shorts / change, add or `--drop` one |
| `metarush recipe list` / `recipe show <recipe>` / `recipe check <recipe>` | recipes (what each makes, inputs, last runs); one with its inputs, sections, versions and check |
| `metarush recipe plan <recipe> --input headline="…" --input art=a,b` / `recipe run <recipe> … [--wait]` | the plan and its estimate (nothing starts) / start it (people only: type start) |
| `metarush recipe again <recipe>#N --input voice=Cleo --note "a warmer voice"` / `recipe runs [--run <recipe>#N] [--wait]` / `recipe compare <recipe>#1 <recipe>#2` | make it again (a new version of the same cut) / runs and what they wait for / two runs side by side |
| `metarush recipe update <recipe> --patch file.json` / `recipe publish <recipe>` / `recipe continue <run>` / `recipe stop <run>` / `recipe archive <recipe>` | a draft of the recipe (`--dry-run`: what it would change) / make a version current, let a waiting run go on (people only) / stop a run / archive |
| `metarush recipe diff <recipe> [--to N] [--from N\|current]` | what changed between two versions in plain words (default: the newest draft against the version it was made from) |
| `metarush recipe suggestions <recipe> [--only key]… [--save]` | what people kept changing in its approved runs, with why and what a draft would change; `--save` makes the draft (a person makes it current) |
| `metarush recipe triggers [<recipe>] [--new-files on\|off] [--weekly "monday 09:00"\|off] [--dismiss]` | when recipes plan a run by themselves, whom they tell and the plan waiting; changing it is people only; it never starts a run |
| `metarush recipe export <recipe> [file]` / `recipe import <file> [--project P] [--name n] [--dry-run]` | the recipe as one file for another workspace (its pictures, footage and approved code by hash) / bring one in as a draft (its code waits for a person here) |
| `metarush recipe update <recipe> --from-drive art=gdrive:Teaser/Art` | an input takes the newest files of a Google Drive folder each run, through your own rclone (a draft) |
| `metarush recipe starters [--add [--only call-episode]]` | the starter recipes MetaRush comes with and whether each is here / bring them in as drafts (a person makes each current) |
| `metarush recipe run <recipe> … [--skip-sample "why"]` / `recipe sample <recipe>#N --approve [--more-cost N] \| --not-quite "…"` | a run with an expensive kind of work makes a sample first (agree to its price only) / approve it — the rest is planned and asks for its OK — or stop the run with what isn't right (people only) |
| `metarush ability list` / `ability find "<task>"` / `ability show <name>` | abilities (approved or waiting, set up here, cost, what leaves); at most eight that might do a task, one line each; one with what it may do, inputs, versions and runs |
| `metarush ability check <folder>` / `ability check --version <name@version>` | check an ability's folder (or a version brought in from another workspace): its manifest, no keys or large files, its test once in the sandbox with the network off; a pass waits for an owner |
| `metarush ability export <name[@version]> [file]` / `ability import <file> [--dry-run] [--as-version]` | a version as a file another workspace brings in; bring one in as a draft (never approved or set up here) |
| `metarush ability approve <name[@version]> [--reject] [--open-network] [--local-service]` / `ability revoke <name>` | approve (or turn down) a version after reading what it may do (with the whole internet, or the one service on this computer it names), or take an approval back — owners, at an interactive terminal |
| `metarush ability setup <name> [--program python3=/path] [--file name=/path] [--key name] [--forget]` | set it up on this computer: the programs it runs, pinned by fingerprint; files it reads (a model) and its keys (owners; keys typed) |
| `metarush footage captions <clip> [--language en] [--save file]` | the captions files kept with a clip (English captions an ability made), one language's words, or that file saved |
| `metarush ability plan <name> --input words="…"` / `ability run … [--max-cost USD] [--wait]` / `ability runs [name] [--id run]` | what a run would do and cost (who decides); start it (a person for anything priced or that sends media away); past runs, one with its requests and what it printed |
| `metarush motion check --version name@version [--library lib@version]` | check a motion graphic a recipe file brought in (test frames in the jailed browser; a person approves it after) |
| `metarush catalogue list [--works-now] [--group fix] [--built-in\|--added]` / `catalogue show <id>` | the menu of abilities, built in and added, each saying whether it works on this computer or what it needs first / one with what it needs, costs and runs |

### Running MetaRush
| Command | What it does |
|---|---|
| `metarush project delete <project>` / `project restore <project>` / `project archived` | delete a project (it moves to Archived projects; people only) / bring it back / list archived ones |
| `metarush project purge <project>\|--all-archived [--dry-run]` | delete archived projects for good: what goes is listed first, footage goes back to the Inbox (owners, in your own terminal) |
| `metarush cache prefetch --project <id> [--pin]` / `cache repair` / `cache status` | offline copies, verify, usage |
| `metarush sync status [--project <id>]` | local vs canonical-only, pending ingests, jobs, reachability |
| `metarush storage migrate --to <store-config> [--dry-run] [--read-back]` | move the media to another storage (S3-compatible, Google Cloud Storage or a folder): checked, resumable, switches when complete, deletes nothing (people only; `--dry-run` for anyone; the dashboard's Settings → Storage does the same) |
| `metarush storage status` | which storage this computer uses, and whether the workspace moved on (then: switch this computer too) |
| `metarush workspace export --to <folder> [--media referenced\|included] [--tar] [--plan]` / `workspace exports` | one archive of the whole workspace, footage listed or copied in; `--plan` says what it would hold and writes nothing; it only reads the database (owners, in your own terminal; another database with `--db-uri-stdin`/`--db-uri-env`) / the exports made on this computer |
| `metarush workspace restore <archive> [--plan]` / `metarush setup --restore <archive>` | restore an export into an empty database (every file checked first; the copy checked and brought up to date; nothing deleted) / set a new computer up from one |
| `metarush machine invite <name> --atlas` → on the new machine `metarush machine join <bundle>` | add a machine with its own storage key and database user |
| `metarush machine list --keys` / `machine revoke <name> --atlas` | see machines and their keys; cut off a lost one |
| `metarush machine rekey [--dry-run]` | a machine that shares the old storage key gets its own, in place (people only; the old key is deleted by you once no machine uses it) |
| `metarush worker settings` / `worker configure --heavy-battery-threshold 25` | battery policy for this machine |
| `metarush log list [--problems] [--since 2h] [--project <id>]` / `log show <entry>` / `log tail --follow` | what MetaRush did, in plain words |
| `metarush log search "<words>"` / `log export --since 7d --out log.jsonl` | find something in the log / save entries as JSON lines |
| `metarush log bundle --since 2h` | a bug report (zip: entries, failed jobs with their error output, setup check, versions; no secrets) |
| `metarush log configure --prompts workspace\|local\|off --retention-days 90` / `log purge --before 90d` | where your prompts are kept, how long entries stay, delete old entries |
| `metarush log claude-hooks enable` (or `status` / `disable`) | also record Claude Code terminal sessions that use MetaRush (off by default) |
| `metarush improve signals` → (Claude Code, skill `metarush-improve`) → `improve save --file report.md`; `improve list` / `keep` / `dismiss` / `export` | improvement ideas from the log |
| `metarush improve report [--dry-run]` / `improve schedule --weekly on` | the whole pipeline now (Claude Code headless, no shell); the weekly report while the daemon runs (off by default, people only) |
| `metarush renderer status` / `renderer install` / `renderer prune` / `renderer dev <checkout>/video` (`--off`) | this version's own renderer: is it ready, set it up or repair it, remove old ones; developers: render from a live checkout |
| `metarush service install [--worker-only]` / `service status` / `service logs -f` / `service restart` / `service uninstall` | run MetaRush at login, restarted if it crashes (launchd, systemd); `--worker-only` without the dashboard |
| `metarush serve --worker-only` | the daemon without the dashboard (what the worker-only service runs) |
| `metarush version` / `version --details` / `version --json` | which MetaRush this is; with what's new in it and the version each of your computers runs (older ones flagged); the build alone as JSON |
| `scripts/bump-version.sh 0.2.0\|patch\|minor\|major [--dry-run]` | cut a release from a checkout: `VERSION`, the changelog's section, a commit and the tag (never pushes) |

## Configuration
`metarush setup` (or `metarush init`) writes `~/.metarush/config.yaml` (`--home` for another installation). Values
support `${VAR}` / `${VAR:default}`.

### Secrets and keys
- **Never in the file**: secrets live in the OS keychain or `METARUSH_*` environment variables. Secret files (an
  env-mode `<home>/.env`, join bundles, CI dotenv files) are refused inside cloud-sync folders and wherever git would
  commit them, homes carry a `.gitignore` with `*`, and `doctor` flags homes or secrets in repositories and synced
  folders ([security](docs/security.md#where-secrets-may-live)).
- **Integration API keys** (ElevenLabs, Google Gemini, D-ID, HeyGen) are per machine:
  `metarush providers set elevenlabs|gemini|d-id|heygen` (hidden prompt, `--key-stdin`, or
  `METARUSH_ELEVENLABS_API_KEY` / `METARUSH_GEMINI_API_KEY`, never a command-line argument) or Settings → Integrations
  in the dashboard. Keys go to the keychain (or the env-mode `<home>/.env`) and never into the database,
  `config.yaml`, logs or the audit trail; the dashboard only ever shows the last four characters.
- **Shared, non-secret choices** (voice, model, voice settings, favorites, per-project spending limits) live in the
  workspace and project records (`metarush providers settings elevenlabs`, or the same screen). Generated video's
  model, draft and final quality, the team's daily limit and each project's budget are people-only
  (`metarush generate configure`, or Settings → Integrations → Generative video).

### Key settings
- **Database and storage**: `database.{uri,name}`, `database.local.port` (set by `metarush setup` when the database is
  the one on this computer, in `<home>/database`: its port on 127.0.0.1), `media.provider` (`filesystem` | `s3` with
  `preset/endpoint/region/bucket/prefix` | `gcs` with `bucket/prefix/credentialsFile`),
  `cache.{dir,maxGB,pruneIntervalMinutes}`.
- **Dashboard and remote access**: `dashboard.{host,port}` (default 127.0.0.1:4845), `dashboard.publicURL` (remote
  access: the `https://<computer>.<tailnet>.ts.net` address `tailscale serve` provides; accepted only from loopback),
  `dashboard.tailscaleLogins` (the Tailscale accounts signed in by themselves) and `dashboard.machineName` (this
  computer's name for people elsewhere), all three set by `metarush remote` and applied without a restart;
  `tools.tailscale` (default: on PATH or in the Tailscale app).
- **Workers and the watched folder**: `worker.{concurrency,heavyRenders,heavyBatteryThreshold}` (big jobs wait on
  battery below this percent; default 25, 0 = never), `worker.watch.{enabled,dir,stableSeconds,moveProcessed,projectId}`
  (the watched folder; edited by `metarush watch` and the dashboard, applied without a restart).
- **Transcription**: `transcription.{provider,language}` (`auto` = Parakeet for English, whisper.cpp otherwise |
  `parakeet` | `whispercpp` | `none`; `language` empty or `auto` (the default) = each clip's language is heard, a
  language code = every clip is that language), `transcription.resident` (default on: the worker keeps the Parakeet
  model, and whisper.cpp's through `whisper-server` from the same build next to `whisper-cli`, loaded in a model
  server between clips, stopped after 10 idle minutes; `false` or `METARUSH_TRANSCRIPTION_RESIDENT=0` loads it for
  every batch or clip), `tools.whisperServer` (default: the `whisper-server` next to `whisper-cli`, never another one
  on PATH).
- **Rendering**: `tools.videoDir` + `tools.devRenderer` (developers only: render from a live checkout instead of this
  version's own renderer in `<home>/renderer/`; a `videoDir` alone is an older installation's setting and is
  ignored), `tools.npm` (npm's `npm-cli.js`; default: the npm next to Node.js).
- **The weekly report and recordings**: `improve.{weekly,day,hour,budgetUsd,model}` (the weekly improvement report;
  off by default), `recordings.{maxGB,minFreeGB,keepDays}` (screen recordings received here: the most one recording
  holds, default 100; the free space its pieces never go below, default 2; how long pieces of a recording nobody
  finished are kept, default 14 days, its maker told two days before).
- **Link imports**: `tools.ytDlp` / `tools.deno` / `tools.brew` (the link-import downloader, its JavaScript runtime
  and the Homebrew that updates it; default: Homebrew, then PATH), `imports.autoUpdate` (default on: the daemon
  updates yt-dlp, and only yt-dlp, about once a week while no import runs) and `imports.browser` (the browser whose
  sign-in this machine used last, only ever a suggestion).
- **Resources**: `resources.uploadSmallMB` (files from linked folders up to this size upload at once, larger ones when
  a cut first uses them; default 25) and `resources.folders` (this machine's linked folders and where they are here;
  edited by `metarush resources link|folder|unlink` and the dashboard, applied without a restart), `resources.repos`
  (`fileKB` 512, `totalMB` 64, `files` 20000: what a repository snapshot keeps; `pictureMB` 25, `picturesMB` 250,
  `pictures` 250: the largest picture kept from its folders, and what one fetch keeps in all) and `resources.pages`
  (`maxPages` 5 at depth 1, `timeoutSec` 45 per page, `fullHeightK` 16: the tallest whole-page picture in thousands
  of pixels).
- **Other programs**: `tools.git`, `tools.gh` and `tools.d2` (found on PATH by default), `tools.rclone` (the person's
  own rclone for Google Drive folders; found on PATH by default, `METARUSH_RCLONE`), `tools.vision` (the on-screen
  helper `metarush-vision`, default: built by MetaRush into `<home>/tools` on a Mac), `tools.claude` (Claude Code
  path, default: on PATH).
- **Claude Code in the dashboard**:
  `assistant.{model,effort,tokenLimitMillions,compact,compactAtTokens,autoCompactTokens,cacheHour,holdNews,maxBudgetUsd,maxSteps,timeoutMinutes,idleMinutes,disabled,safeSteps}`
  (defaults: Claude Code's own model and effort; 50 million tokens per conversation; a summary once Claude's memory
  passes 200,000 tokens, Claude Code's own at 300,000; the prompt cache kept an hour; your clicks on Claude's cards
  told with your next message; with an API key also $2.00 per conversation; 60 steps and 10 minutes per message;
  sessions sleep after 10 idle minutes and resume on the next message; safe steps run by themselves: `safeSteps` is
  this computer's master switch, and off, nothing runs by itself and nobody's Auto is offered). Settings → Claude
  Code changes the model and effort, Settings → Claude Code (Usage and memory) the rest.
- **The log**: `log.prompts` (`workspace` default: anyone who can read the workspace database can read what people
  asked Claude; `local`: this machine only; `off`) and `log.retentionDays` (0 = keep entries until you delete them),
  set with `metarush log configure` or Settings → Log, `/settings/log`
  ([what the log keeps](docs/security.md#activity-log)).

### More docs
[installation](docs/installation.md) · [deployment, service and releases](docs/deployment.md) ·
[collaboration](docs/collaboration.md) · [add a machine](docs/runbook-add-machine.md) · [edit schema](docs/edit-schema.md) ·
[cut sheets](docs/cut-sheet.md) · [security](docs/security.md) ·
[render QA](docs/qa.md) · [outside reviewers](docs/outside-reviewers.md) · [exporting and restoring a workspace](docs/workspace-export.md) ·
[speakers, people and to-dos](docs/speakers.md) · [motion graphics as code](docs/motion-graphics-as-code.md) ·
[browser extension](docs/browser-extension.md) · [what MetaRush sees](docs/visual-index.md) · [resources, project links and nested cuts](docs/resources.md) · [timeline export](docs/timeline-export.md) · [timelines from other editors](docs/timeline-import.md) · [posting to video platforms](docs/publishing.md) · [Google Drive and Dropbox folders](docs/cloud-folders.md) · [the plugin](docs/plugin.md) · [MCP](docs/mcp.md) · [the catalogue](docs/catalogue.md) · [Remotion sources](docs/remotion-sources.md) · [recipes](docs/recipes.md) · [abilities](docs/abilities.md) · [testing](docs/testing.md) · [the review desk](docs/review-desk.md)

## Development
`./start.sh` runs a local MongoDB replica set, the daemon on :4841 and the Vite dashboard on :4840 against the dev
installation in `.dev/home`. **`scripts/check.sh --quick`** before a commit and **`--full`** before a release run the
checks below in order, log each step and end with a table; [testing](docs/testing.md) says what runs where and when
(CI on every push, browser runs nightly, Mac-only), how coverage is measured (`scripts/coverage.sh`, Codecov, the
`coverage-baseline.json` ratchet) and how runs clean up after themselves (`scripts/lib/e2e.sh`). See
[CLAUDE.md](CLAUDE.md).

### Tests
- `go test ./...` (uses throwaway `metarush-test-*` databases on localhost only) and `npm run typecheck`.
- `npm run test:motion -w video`: the motion-graphics import scan, archive, type check and bundler, no browser; and
  the web page snapshot's filtering proxy, which must never reach this computer or the local network.
- `npm run render:selftest -w video`: renders the templates and the slides, a mixed-audio check, an approved sample
  motion graphic, one that draws resources, and the network-jail checks.
- `metarush skills test [--live]` (with the skill listing's budget) and `metarush skills eval` (the tool-selection
  eval; go test gates it).
- `scripts/secret-scan.sh`: likely secrets in tracked files; also in CI.
- `scripts/acceptance.sh`: a fresh installation through ingest → plan → revision → cut sheets → render → dashboard
  compare/download/approve; it cleans up through `scripts/droptestdb`, which drops only local `metarush-test-*`
  databases.
- `npm test -w web` runs the dashboard's own checks: the person picker's matching against the Go matcher's table and
  the Script view's reader against the cut sheet fixtures.
- `node --test extension/test/detect.test.cjs extension/test/page.test.cjs` tests the extension's page detection and
  what Send this page reads.
- `python3 -I tools/vision/eval/faces.py --metarush bin/metarush` measures face recognition as it ships
  (`--helper <metarush-vision>`: Apple's feature print).

### Browser runs
- `node web/scripts/assistant-e2e.mjs` holds a real conversation through the Claude Code panel in a browser
  (follow-up, reload mid-reply, plan applied, full screen, action cards) and screenshots each step (it spends real
  Claude usage unless `tools.claude` points at a fake).
- `node web/scripts/integrations-e2e.mjs` drives Settings → Integrations against the fake ElevenLabs API
  (`go run ./internal/testutil/fakeeleven/cmd/fakeeleven`, then start the daemon with `METARUSH_ELEVENLABS_BASE_URL`
  pointing at it) and checks that no response, page or browser storage ever contains the key.
- `scripts/generate-e2e.sh` runs `web/scripts/generate-e2e.mjs` on a throwaway installation against the fake Gemini
  API (`internal/testutil/fakevideogen`): the key flow, budgets, Generate a shot, Variations, Continue this shot,
  consent refusals and recording, looks, a free-tier key's shot, the Script's Generated line, with screenshots.
- `scripts/ai-services-e2e.sh` (ports 5400–5402) drives the Generate panel's other kinds against the fake Gemini and
  ElevenLabs APIs: a picture and music priced with their alternatives and made, a sound effect, a cloud transcript,
  Content Settings forbidding a service, and the AI work list, with screenshots.
- `scripts/presenters-e2e.sh` (ports 5510–5513) drives Generate → Presenter against the fake D-ID and HeyGen APIs
  (`internal/testutil/fakedid`, `fakeheygen`): a test clip, approving, a clip with green behind it, a stock
  presenter's clip, a presenter begun with D-ID's consent words, Content Settings' No presenters, retiring (deleted at
  the fake), with screenshots.
- `scripts/spoken-word-e2e.sh` (ports 5580–5581) drives **Say it differently…** against the fake ElevenLabs API
  with the real recognizer and voice fingerprints on this computer: the consent form's new words, the price, the check,
  before and after, Put it in the cut, both transcripts' marks, the captions note, Undo and a withdrawn consent, with
  screenshots.
- `scripts/recipes-e2e.sh` runs `web/scripts/recipes-e2e.mjs` (ports 5390–5392) against the fake ElevenLabs and the
  fake Gemini: Save as a recipe…, the Library's Recipes tab, a recipe's page, Run with… with the estimate opened and
  agreed in a styled question, the run's page following it live, a run waiting for a person (To-dos, the bell, the
  activity bar) and Go on, "Made with…" on the new project and version, Run again… with estimates off (no money
  question), versions with what each changed, two runs side by side, Start from a recipe on Home, then Claude's
  recipe cards in the panel with a fake `claude` (`web/scripts/recipes-cards-e2e.mjs`: a run with its plan and
  estimate that Auto never starts, Not quite…, the agreement, a save, a change, Make current), then suggested changes and
  planning by itself (`web/scripts/recipes-tier3-e2e.mjs`: two runs whose boat a person shortened become a suggested change saved as a
  draft; planning by itself turned on, a clip joining the project makes a plan and rings the bell, no run starts
  though Auto is on and estimates are off; Claude's picks, sheet and suggestions cards), desktop and 390 px phone in
  both themes, screenshots in `RECIPES_SHOTS` (default: the run's folder).
- `scripts/abilities-e2e.sh` runs `web/scripts/abilities-e2e.mjs` (port 5420; needs a sandbox and python3): two
  abilities checked from the CLI, Library → Abilities, To-dos and the bell, an owner's approval in a styled question,
  Set up on this computer, Run… with its plan to the picture it brought back (which says how it was made), a key
  typed hidden and shown masked, taking an approval back, desktop and 390 px phone in both themes.
- `scripts/starter-e2e.sh` runs `web/scripts/starter-e2e.mjs` (ports 5530–5539; needs a sandbox and python3): the
  starter pack's ComfyUI ability checked from its folder, approved in the browser with the one local service it
  names, set up and run against a fake ComfyUI on this computer, the picture it brought back; the other starter
  abilities checked and listed.
- `scripts/catalogue-e2e.sh` runs `web/scripts/catalogue-e2e.mjs` (ports 5500–5509; needs a sandbox and python3): the
  menu of abilities with what works here, its filters, a built-in ability's page, an added ability approved and set
  up until the menu and the command line say it works here, desktop and 390 px phone in both themes.
- `scripts/ship-gate-e2e.sh` runs `web/scripts/ship-gate-e2e.mjs` (ports 5560–5569; ffmpeg and a renderer on this
  computer): a final render checked here measures two promises (one broken), delivery refuses the version, the
  version's page says Held by …, a sign-off is ticked in one click and the version goes out anyway with a note in a
  styled dialog, then delivery writes who and why next to the video and on the receipt, desktop and 390 px phone in
  both themes.
- `node web/scripts/cutsheet-e2e.mjs` drives the studio's Script tab (Formatted, Markdown and Edit: live check, save,
  older versions, compare, a clip that plays the cut from there; desktop and phone, both themes).
- `node web/scripts/motion-e2e.mjs` drives the Motion graphics page (review, approve, withdraw; desktop and phone, both
  themes).
- `node web/scripts/linkimport-e2e.mjs` pastes links into the Inbox against a daemon whose downloader is the fake
  (`go build -o <dir>/yt-dlp ./internal/testutil/fakeytdlp/cmd/fake-yt-dlp`, then `METARUSH_YTDLP=<dir>/yt-dlp`) and
  loads the browser extension in Chromium to pair it.
- `scripts/resources-e2e.sh` runs `web/scripts/resources-e2e.mjs` (ports 5660–5669; the fake downloader): the Add
  box and a drop and a pasted link into a project, Inbox clips dragged onto project cards (Undo, Move…, two at once),
  the Sources "…" menu (Where it's used, Move to another project…, Remove from this project, Delete…), another
  project's cut nested from the dashboard, desktop and 390 px phone in both themes.
- `node web/scripts/resources2-e2e.mjs` pastes a GitHub repository and a web page into a project's Add box (a fixture
  repository through `METARUSH_GIT_BASE`, `scripts/make-fixture-repo.sh`; a local site with
  `METARUSH_PAGES_ALLOW_LOCAL=1`), previews a cut of slides in the studio, and sends a stub page from the extension
  (desktop and phone, both themes).
- `node web/scripts/visual-e2e.mjs` checks what MetaRush sees in the dashboard (the On screen tab, the frame grid,
  visual search, "Is this …?" on People and To-dos; desktop and phone, both themes) against a workspace with an
  indexed clip.
- `node web/scripts/framing-e2e.mjs` presses Keep faces in frame on a cut made without it and checks the new version
  and Undo, then Follow movement and Hold still.
- `node web/scripts/faces-e2e.mjs` checks faces recognized across clips (People and the On screen tab, "Not …?").
- `scripts/people-e2e.sh` runs `web/scripts/people-e2e.mjs` on a throwaway installation with the speaker-view call
  fixture (`scripts/make-call-fixtures.sh`): the grid of face questions, the names the call shows in one click, the
  voice comparison dialog fixing an earlier wrong voice in place, the faces a name was learned from, and answers for
  many at once on a second copy of the call (None of these are …, Not … in this clip, Skip all in this project) (Mac
  only; desktop and phone, both themes).
- `scripts/workspace-export-e2e.sh` runs `web/scripts/workspace-export-e2e.mjs` (ports 5670–5679): a workspace
  exported from Settings → Storage (the plan, the styled confirm, the archive ready), a member refused, then restored
  with the command line into a fresh home and an empty database, and the dashboard opened on the restored copy
  (desktop and phone, both themes).

### Releases
`scripts/release.sh --snapshot` builds and verifies every release archive without publishing anything
(`scripts/release.sh vX.Y.Z` publishes, after the same dry run); `scripts/deploy.sh --dry-run` shows a deploy.

## License
Speech recognition uses NVIDIA Parakeet TDT 0.6B v2 (https://huggingface.co/nvidia/parakeet-tdt-0.6b-v2), licensed under CC BY 4.0, converted to ONNX by the sherpa-onnx project.
Face recognition uses SFace (Zhong et al., IEEE TIP 2021) as published in OpenCV Zoo (https://github.com/opencv/opencv_zoo/tree/main/models/face_recognition_sface), licensed under Apache 2.0, downloaded on first use and checked against its SHA-256.

MIT © 2026 Metavert LLC. Remotion and other dependencies have their own licenses — see [docs/licensing.md](docs/licensing.md).
