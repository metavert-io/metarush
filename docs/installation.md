# Installation

Get MetaRush one of three ways, then run **`metarush setup`**: a few questions with suggested answers, about five
minutes in all, with no accounts or keys needed for MetaRush itself. MetaRush needs only itself, a place for your
projects and a storage folder to start; setup provides both. Everything else — FFmpeg, Node.js and the Remotion
renderer, transcription on this computer, the link downloader and more — is an optional **component** that unlocks
named features: setup offers them as a checklist with the suggested ones ticked, and `metarush components` or
Settings → Components adds the rest later (see [Components](#components)). The Homebrew cask and formula bring FFmpeg
and Node.js along.

## With Homebrew (macOS and Linux)
```bash
brew tap metavert-io/metarush                       # the tap metavert-io/homebrew-metarush
brew install --cask metavert-io/metarush/metarush   # the binary, with FFmpeg and Node.js (recommended)
metarush setup
```

The tap has a **cask** and a **formula** for the same release. The cask unpacks the pre-built binary (and installs
FFmpeg and Node.js through Homebrew), so it works whatever state your Command Line Tools are in. The formula (`brew install metavert-io/metarush/metarush`)
also installs ffmpeg and node and puts the renderer package, skills and docs under `share/`, but it has no bottles,
so on macOS Homebrew treats it as a source build and refuses it until the Command Line Tools are current
(`xcode-select --install`, or Software Update). On Linux either works.

- **How it works.** The cask and the formula download each archive from the GitHub API
  (`/repos/metavert-io/metarush/releases/assets/<id>`, `Accept: application/octet-stream`); `scripts/release.sh`
  writes both for every release. Anonymous requests to GitHub's API are rate-limited: when many downloads come from
  one address, `HOMEBREW_GITHUB_API_TOKEN` (any GitHub token) raises the limit, and Homebrew drops the header when
  GitHub redirects to the download.
- **Gatekeeper:** Homebrew marks cask downloads as quarantined; releases made without a Developer ID aren't
  notarized, so the cask clears that flag on what it unpacked (a `postflight_steps` `xattr` step, harmless for a
  notarized build); the formula is never quarantined.
- **Upgrading:** `brew upgrade --cask metarush` (or `brew upgrade metarush` for the formula), then `metarush
  migrate`, then `metarush service restart` if you use the background service ([deployment.md](deployment.md)). The
  new version sets up its own renderer when it first starts.

## From a release archive
Each [release](https://github.com/metavert-io/metarush/releases) has `metarush_<version>_<os>_<arch>.tar.gz` for
darwin/arm64, darwin/amd64, linux/arm64 and linux/amd64, and `checksums.txt` (SHA-256). An archive holds the
`metarush` binary, `renderer/` (the renderer package the binary also carries), `skills/`, `docs/`, the README,
license and changelog.

```bash
gh release download v0.1.0-rc1 -R metavert-io/metarush -p 'metarush_*_darwin_arm64.tar.gz' -p checksums.txt
shasum -a 256 -c checksums.txt --ignore-missing          # must say OK
tar -xzf metarush_*_darwin_arm64.tar.gz
mkdir -p ~/.local/bin && install -m 755 metarush_*_darwin_arm64/metarush ~/.local/bin/metarush
metarush setup
```

**Signed and notarized builds** open like any other app (macOS checks the notarization online the first time). A
release made **without** a Developer ID says so in its notes ("Not notarized"); a binary from such a release
downloaded **with a browser** is blocked the first time it runs ("cannot be opened because Apple cannot check it for
malicious software"). Either run `xattr -d com.apple.quarantine ~/.local/bin/metarush` once, or try to run it, then
open System Settings → Privacy & Security and choose **Open Anyway**. Downloads made with `gh` or `curl` are not
quarantined, and the Homebrew cask clears the flag itself. On Linux, nothing is needed.

## From a clone
```bash
git clone git@github.com:metavert-io/metarush.git && cd metarush
./bootstrap.sh            # for developers: checks/installs Go, Node 22, ffmpeg, whisper.cpp + model, Remotion browser; builds bin/metarush
                          # (and on a Mac with the Xcode tools, bin/metarush-vision: see "looking at what's on screen")
export PATH="$PWD/bin:$PATH"
metarush setup            # interactive; or a development home: ./start.sh (renders from the checkout)
```
`bootstrap.sh` is for people working on MetaRush itself (it builds from source and installs the local transcriber
and its models); `--dry-run` shows the plan; `--yes` for unattended machines. It prints the Remotion license notice.
Nothing MetaRush says to a person points at it: people add what they need with `metarush components add` or
Settings → Components.
`scripts/deploy.sh` builds the checkout and installs it on this machine (or another one over SSH) the safe way: see
[deployment.md](deployment.md).

## The renderer
MetaRush renders with Remotion. Every binary carries its **own** renderer package: the `video/` project, the schema
types it imports and the npm lockfile that pins every package. `metarush renderer install` unpacks it into
`<home>/renderer/<release>-<digest>/` and runs `npm ci` there with your Node.js, then downloads Remotion's headless
browser (once per Remotion version, shared by every renderer that uses it: about 180 MB to download, 600 MB on disk). The
renderer is the `renderer` component (suggested in setup; it brings Node.js and FFmpeg first). Once it is on a
computer, the daemon and workers set up each new version's by themselves the first time they start, so after an
upgrade the first render may wait a few minutes (it says so). If you left the renderer out at setup or removed it in
Settings → Components, nothing sets it up by itself, and renders say "Rendering needs the Remotion renderer (about 400
MB). Add it in Settings → Components." Renders, stills and motion-graphic checks use only that folder: a
source checkout, or a merge into main, never changes what an installed MetaRush renders.

```bash
metarush renderer status            # ready / missing / installing / removed / damaged, and what to do (--details: folders)
metarush renderer install           # set it up or repair it (--force sets it up again)
metarush renderer prune             # keep this version's and the newest others (3 in all; --keep N)
```

- `metarush renderer install` is itself a request to set the renderer up, like `metarush setup --components
  renderer`: it sets it up from a script too (no terminal: deploy over SSH, the Homebrew cask's next step) and
  records that a command asked. Three exceptions, each saying so and ending with success, so a script goes on: a
  ready renderer ("already set up"); an AI app (Claude Code, `METARUSH_AGENT`, `--agent`), which never starts a
  download for a renderer nobody chose (a renderer that is wanted is still repaired or upgraded); and a renderer a
  person left out, which a script sets up only with `--force` (a person at their own terminal just runs it).
- A renderer that was set up and then **deleted or damaged** is not set up again behind your back: `doctor`, the
  render's error and the daemon log say to run `metarush renderer install`.
- **Older installations** that pointed `tools.videoDir` at a checkout move to the per-version renderer on upgrade,
  with a notice (daemon log, `metarush migrate`, `doctor`). The setting stays in `config.yaml`, unused, so going back
  to an older binary still works.
- **Developers**: `metarush renderer dev <checkout>/video` renders from a live checkout (it sets `tools.videoDir` and
  `tools.devRenderer: true`; `--off` goes back). Use it for a development home (`./start.sh` turns it on for
  `.dev/home`), not for an installation people rely on; `doctor` warns when it is on. `init --video-dir` does the same.
- Offline: `npm ci` needs the npm registry the first time a package version is seen (npm keeps a cache); the browser
  download needs the internet once per Remotion version.

## `metarush setup`
One command sets MetaRush up on this computer. It asks, with a suggested answer for each (press Enter):

| Question | Suggested | Other answers | Flag |
|---|---|---|---|
| Where footage, preview copies and finished videos are kept | a folder on this computer (`<home>/media`) | another folder, or a shared drive; **advanced:** a cloud bucket — S3-compatible (Tigris, Cloudflare R2, Backblaze B2, AWS and others) or Google Cloud Storage | `--storage folder\|s3\|gcs`, `--media-root`, `--media-shared`, the `--s3-*` and `--gcs-*` flags of `init` |
| Where your projects are kept | a database on this computer, set up by MetaRush ([below](#the-database-on-this-computer)) | a database you already have: MongoDB Atlas (it has a free tier) or your own server | `--database here\|elsewhere`, `--db-uri` (better: `METARUSH_MONGODB_URI`), `--db-name`, `--database-port` |
| The dashboard's port on this computer | 4845, or the next free one | any port above 1024 | `--port` |
| Start MetaRush when you log in, and keep it running | yes (`metarush service`) | no: opening the dashboard starts it | `--service=false` |
| Teach Claude Code MetaRush's video skills | yes when Claude Code is installed (into `~/.claude/skills`, or `$CLAUDE_CONFIG_DIR/skills`); without it nothing is written there until setup runs again | no | `--skills`, `--skills=false` |
| Which optional components to add ([Components](#components)) | the suggested ones: FFmpeg, Node.js and the Remotion renderer, transcription, the link downloader, PDF pages, a stand-in voice, clearer voices, the sandbox, and on a Mac seeing what's on screen and faces | tick or untick any (a checklist; `none` for none) | `--components ffmpeg,renderer,…`, `--no-components` |

It also takes `init`'s `--workspace-name`, `--join`, `--new-workspace`, `--user-name`, `--user-email`,
`--installation-name` and `--secrets keychain|env` (with `env`, the database address and an S3 secret go into
`<home>/.env`). Then it says what was set up, **what works now with no keys and what a key adds** (the same
list is Home's Getting started and `metarush setup status`), whether **Claude Code** is installed and signed in
(in Claude Code's own words; MetaRush never signs you in and never reads its sign-in), and opens the dashboard.

- **Unattended** (an agent, a script, a computer nobody sits at): `metarush setup --yes --json` takes the suggested
  answer for everything not given as a flag and prints the result as JSON (what was set up, what works, the
  commands for what's left in `next`).
- **Running it again is safe:** what is set up stays; the database on this computer is started if it isn't
  running; the background service and the skills are set up if they are missing; components you chose before and
  that aren't here (a download that failed, say) are added, the ones you left out stay out, and a suggested one
  nobody decided on is added only when you answer the checklist; `--port` moves the
  dashboard. It never changes where things are kept — moving footage is Settings → Storage (`metarush storage migrate`).
- **Run by an AI app** (Claude Code in a terminal, `--agent`): setup does everything else but adds no component;
  it lists them for a person (`components.forAPerson`, `next`). **Without a terminal** (a script, CI) setup installs
  only the components named with `--components`; the suggested ones are listed for a person. Installing programs is
  a person's decision.
- `metarush init` (below) is the older, technical way, with every database and storage setting as a flag; setup is
  built on it.

## The database on this computer
With the suggested answer, setup sets up a database just for this MetaRush:

- **What:** MongoDB Community Server, MongoDB's free database server, downloaded by MetaRush from MongoDB's own
  download server (about 85 MB) — never shipped inside MetaRush. The version is pinned (8.0) and the download is
  checked against the SHA-256 MongoDB publishes for it; only the server and its licence files are kept, in
  `<home>/database/mongodb/<version>/`. It comes under MongoDB's own licence (the Server Side Public License; the
  licence file is next to the server). MongoDB makes it for Macs (Apple silicon and Intel) and for Ubuntu 20.04–24.04,
  Debian 12, RHEL/Rocky/Alma 8–10, Amazon Linux 2023 and SUSE 15; elsewhere setup offers a database elsewhere instead.
- **Where:** your projects in `<home>/database/data` — never in a folder that syncs to the cloud or a git
  repository (setup refuses: a copy of that folder would open every project; choose another home with `--home`).
  The one exception is a git repository of your whole home folder (a dotfiles repository): it is allowed, and setup
  and doctor warn that cleaning that repository's ignored files would delete your projects. It listens only on this computer
  (127.0.0.1) on a port setup picks (from 27150; `database.local.port` in `config.yaml`) and always requires a sign-in.
  The port is one no other MetaRush on this computer uses: each home's choice is remembered in
  `database-ports.json` in your config folder (on a Mac `~/Library/Application Support/MetaRush/`, on Linux
  `~/.config/MetaRush/`), even while that home is stopped. A home on a drive that isn't connected loses its claim
  (its database can't be checked), so another home may take the port meanwhile; MetaRush then says plainly that
  another program uses the port when that home starts again (`metarush database status --details` shows it). MetaRush makes two: an administrator (used only for upgrades) and
  its own, which may read and write MetaRush's database and nothing else. Their passwords are generated at setup and
  kept where MetaRush keeps secrets — the keychain, or with `--secrets env` MetaRush's in `<home>/.env` and the
  administrator's in a private file of its own — never in `config.yaml` or an environment variable. Nobody on the
  network, and no other account or program on this computer without those passwords, can read it; `metarush doctor`
  checks that it refuses a connection without one. If your terminal sets `METARUSH_MONGODB_URI`, setup asks you to
  remove it first (MetaRush would use that database instead). An address an earlier setup kept for another database
  is replaced only after you say yes (unattended: only with `--replace-saved-database`), and the old one is kept
  aside, in the keychain or a private file in `<home>/database`; setup says where. A setup that stops half way stops
  the database and says to run setup again, which carries on where it stopped.
- **Running:** anything that needs it starts it (a command, the dashboard, the background service), and while
  MetaRush runs in the background it keeps it running. `metarush stop` stops it too (`--keep-database` leaves it).
  It is always asked to shut down cleanly, never killed. Stopping or restarting the background service doesn't stop
  it (on Linux the service stops only MetaRush, then cleans up whatever else MetaRush left running).
- **Looking after it:** `metarush database status` (`--details`: version, port, folder, log), `start`, `stop`, and
  `metarush doctor` checks it. **Upgrades** happen only when you ask: when a newer MetaRush comes with a newer
  version, `metarush database status` and doctor say so; then, in your own terminal (only a person upgrades it; an
  agent may ask what it would do with `--dry-run`), `metarush stop && metarush database upgrade` downloads and checks
  the new version, stops the database for a moment and starts it again on the new version with all your projects
  (one release at a time; never back to an older one). An upgrade that stopped half way is offered again and
  finished the next time.
- **Offline computers:** copy MongoDB's archive for this computer (the URL is in `internal/localdb/pins.go`) and set
  `METARUSH_MONGODB_ARCHIVE` to it; it is checked against the same SHA-256.
- **Backups:** the data is ordinary files in `<home>/database/data`; back up the home with MetaRush stopped (or use
  MongoDB's own tools). Your footage is in its storage folder, apart from the database.
- **Another computer later:** a database on this computer is for this computer. For a team, use MongoDB Atlas (or
  your own server) and shared storage; `metarush machine invite` / `join` then add computers.

## Components
MetaRush needs only **itself, a place for your projects and a storage folder** to start; `metarush setup` provides
the last two. Everything else is an optional component that unlocks named features. One list in MetaRush
says what each one unlocks, how big it is, how it is found and how it is added; `metarush setup`, `metarush
components`, `metarush doctor`, Home's Getting started and Settings → Components all read it.

```bash
metarush components                    # what is here, and what each optional one adds (--details: versions, folders, commands)
metarush components add renderer       # add one, with what it needs first (people only, in your own terminal)
metarush components remove renderer    # take off what MetaRush added, and stop it being set up by itself
metarush components --json             # the same for scripts (ComponentsOut)
```

| Component | Unlocks | About | Suggested | Added with |
|---|---|---|---|---|
| `ffmpeg` | Bringing in footage: preview copies, thumbnails, waveforms, finding scenes and pauses, fixes, recordings, the check of every finished video | 45 MB | yes | Homebrew; on a Mac without it, MetaRush's download (static FFmpeg 9); on Linux your packages |
| `node` | Runs the renderer, motion graphics checks, Remotion projects, web page snapshots | 50 MB | yes | Homebrew, else Node.js 22 from nodejs.org |
| `renderer` | Finished videos and review copies, motion graphics, Remotion projects | 400 MB | yes | MetaRush (`npm ci` and Remotion's browser; needs `node` and `ffmpeg`, added first) |
| `transcription` | Transcripts and captions on this computer, cutting by words, telling voices apart | 550 MB | yes | MetaRush's downloads: sherpa-onnx 1.13.8, Parakeet v2 and the speaker models (Apple silicon and Intel Macs, Linux x64); elsewhere whisper.cpp from Homebrew and its model |
| `other-languages` | Transcripts in about a hundred languages (whisper.cpp), finding a clip's language | 1.6 GB | no | whisper.cpp from Homebrew (or your own build) and MetaRush's download of its model |
| `link-import` | Importing videos from YouTube, TikTok, X and other links | 40 MB | yes | Homebrew (with Deno), else yt-dlp's own program |
| `pdf-pages` | The pages of a PDF in your videos | 30 MB | yes | Homebrew; on Linux your packages (`poppler-utils`) |
| `screen-index` | Seeing what's on screen with Apple Vision (Mac) | 5 MB | yes | MetaRush builds it (needs Apple's Command Line Tools) |
| `faces` | Recognizing the people you named across clips (Mac) | 40 MB | yes | MetaRush's download of the face model |
| `clean-voice` | Clearer voices on this computer, voice clarity scores | 30 MB | yes | MetaRush's downloads (DeepFilterNet) |
| `stand-in-voice` | A free robotic voice to time narration | 5 MB | yes | Built into macOS; Homebrew or your packages on Linux (`espeak-ng`) |
| `sandbox` | Abilities and a Remotion project's own steps, kept away from your files | — | yes | Built into macOS; your packages on Linux (`bubblewrap`) |
| `studio-voice` | Studio voices | 1.8 GB | no | MetaRush (Python environment and model) |
| `stabilizer` | The better steadying of shaky footage | 25 MB | no | MetaRush's download (Mac; also set up the first time footage is steadied) |
| `ai-models` | Free AI voices and picture upscaling on this computer | 410 MB | no | MetaRush's downloads |
| `google-drive` | Google Drive folders without syncing them | 25 MB | no | Homebrew (`rclone`), then `rclone config` |
| `github` | GitHub repositories for code slides; sending Remotion changes back | 15 MB | no | Homebrew (`gh`) |
| `diagrams` | D2 diagrams | 30 MB | no | Homebrew (`d2`) |
| `claude-code` | Editing by asking | 200 MB | no | Homebrew's cask on a Mac, else Claude Code's own installer |
| `tailscale` | Opening MetaRush from your other computers | 50 MB | no | You (tailscale.com), then Settings → People |

- **How a component is added**: with **Homebrew** where it is on the computer (`brew install --formula …`,
  never `sudo`); else with **MetaRush's own download**, pinned to an exact file and checked before it is used, into
  `<home>/tools/<…>` (programs; `config.yaml`'s `tools.*` then names them) or the models folder (`tools.modelsDir`,
  shared by every home on the computer); else MetaRush says, in plain words, how you add it yourself (on Linux
  without Homebrew, FFmpeg, poppler and bubblewrap come from your system's packages, which need an administrator).
  MetaRush never runs `sudo`.
- **What you choose is remembered** (`<home>/state/components.json`): a component you left out at setup or removed
  is never set up by itself, and the features that need it say so with the one step: "Rendering needs the Remotion
  renderer (about 400 MB). Add it in Settings → Components." A waiting job says the same ("Making transcripts needs
  the transcriber and its speech models…"), and so do link imports, PDF pages, D2 diagrams and footage that couldn't
  be added.
- **People only, owners only**: adding or removing is never done by Auto, Claude (in the dashboard or in a
  terminal), an MCP client or an AI app's draft; `metarush components add` runs only in your own terminal. In the
  dashboard, Add it shows the size, the time and, for the renderer, Remotion's licence first, then adds in the
  background while you carry on.
- **Removing** takes away only what MetaRush put there (its downloads and builds); what Homebrew or your system
  installed stays, and MetaRush says how to remove it there.
- **Remotion's licence:** Remotion, which renders your videos, is free for individuals and for companies of up to 3
  people; larger companies need a Remotion Company License (remotion.dev/license) before using it. MetaRush says so
  whenever the renderer is added.
- **Offline or locked-down computers:** add the components while online; `metarush components --details` shows each
  download's address, and doctor's COMPONENTS check lists what isn't here.
- **What MetaRush checks:** every download's exact size and SHA-256 before anything is kept (an address that isn't
  versioned, like osxexperts' FFmpeg zips, by the program inside, as its publisher states it); redirects only to the
  publisher's own download hosts; archives unpacked without writing outside their folder; and everything it put in
  `<home>/tools` is written down file by file (all of Node.js, npm included; the transcriber's folder; each program)
  in `<home>/tools/pinned.json`. Before a program there runs, its whole download is checked against that record, and
  a single program also against the checksum built into MetaRush: one that was changed, or that has no record, isn't
  run (add it again in Settings → Components). yt-dlp updates itself through MetaRush, which writes the new one down.
  This catches accidental changes and other programs changing those files; it doesn't stop someone who can sign in as
  you, who could change the record (or MetaRush) too. evermeet also signs its zips with GPG
  (`ffprobe-9.0.2.zip.sig`): to check a signature by hand, `gpg --verify` it against evermeet's key.

## Claude Code
MetaRush works without Claude Code (the dashboard edits, makes transcripts and renders); with it, you edit by
asking in plain words. Setup and `metarush doctor` (its CLAUDE CODE check) say whether it is installed and signed
in, asking only `claude --version` and `claude auth status`. To install it: `metarush components add claude-code`
(Homebrew's cask on a Mac), or `npm install -g @anthropic-ai/claude-code`; then open `claude` once in a terminal and
sign in.

## `metarush init`
Choose the **database** and the **media store** independently:
- Database: any MongoDB URI (Atlas recommended for teams; a local replica set for development via
  `scripts/dev-mongo.sh`). Pass it via `METARUSH_MONGODB_URI` (keeps it out of shell history) or let init prompt.
- Media store: `--media s3 --s3-preset tigris|r2|b2|aws|gcs|custom --s3-bucket … --s3-access-key-id …` with the
  secret in `METARUSH_S3_SECRET_ACCESS_KEY`, or `--media filesystem --media-root <dir> [--media-shared]`, or native
  Google Cloud Storage: `--media gcs --gcs-bucket <bucket> [--gcs-prefix <prefix>] [--gcs-credentials <key.json>]`.
  `--gcs-credentials` is the path of a service-account JSON key (stored as a path, never copied; `chmod 600` it);
  omit it to use Application Default Credentials — on Cloud Run/GCE the attached service account, elsewhere
  `GOOGLE_APPLICATION_CREDENTIALS` or `gcloud auth application-default login`. Grant the account
  `roles/storage.objectUser` on the bucket; for direct (signed-URL) dashboard playback without a key file, also let it
  sign as itself (`roles/iam.serviceAccountTokenCreator`). Env overrides: `METARUSH_GCS_BUCKET`, `METARUSH_GCS_PREFIX`,
  `METARUSH_GCS_CREDENTIALS`.
- Joining a teammate's workspace: preferably `metarush machine join <bundle>` with a bundle from
  `metarush machine invite <name>` on an admin machine (this machine's own storage key and database user; see
  [runbook-add-machine.md](runbook-add-machine.md)); or by hand, same database + `--join <workspace-id>`. init warns
  if your media store differs from the workspace's canonical store, and refuses a revoked installation.
- Atlas: a URI cannot prove which organization owns a cluster. Verify with `atlas organizations list` / `atlas projects
  list` and pass `--atlas-org-id/--atlas-org-name/--atlas-project-id` so the installation records it.
- Secrets: `--secrets keychain` (default) or `--secrets env` (config holds `${METARUSH_…}` placeholders; values come
  from the environment or `<home>/.env`, mode 0600). Env mode refuses a home whose `.env` would sit inside a cloud-sync
  folder (iCloud Drive, Google Drive, OneDrive, Dropbox, Box — no override) or inside a git repository
  (`--allow-unsafe-secret-path` allows it only when git ignores the file). Keep homes, and anything holding secrets,
  outside synced folders and repositories; every home gets a `.gitignore` with `*` anyway. See
  [security.md](security.md#where-secrets-may-live).
- Non-interactive: `--yes` plus flags, or `--config settings.yaml` (same keys as `config.yaml`; secrets via env).
- With `--secrets env`, init writes the database address (and a storage secret) into `<home>/.env`. If the home
  already keeps the address of another database, init asks before replacing it (unattended:
  `--replace-saved-database`) and keeps the old one aside in `<home>/database/previous.secret` (or the keychain).

## Optional: narration
- **Scratch voice** (free, robotic, for checking timing): built in on macOS (`say`); on Linux install `espeak-ng`
  (e.g. `apt install espeak-ng`). Without either, `voiceover scratch` says it is not available.
- **Recording in the terminal** (`metarush voiceover record`): ffmpeg with avfoundation on macOS (allow microphone
  access for your terminal app in System Settings → Privacy & Security → Microphone) or ALSA/PulseAudio on Linux;
  `--list-devices` shows the microphones.
- **ElevenLabs** (natural AI voices, billed to your ElevenLabs account): create an API key in your account, then on each
  machine run `metarush providers set elevenlabs` (hidden prompt) or open Settings → Integrations in the dashboard,
  and choose a voice there (or `metarush providers settings elevenlabs --voice <name>`). Keys are per machine and are
  never copied between machines or into the workspace.

## Optional: clearer voices
Making a voice clearer ([sound.md](sound.md)) works three ways. None of them changes your recordings.

- **Clean** (on your computer, free): nothing to install by hand. The first time you use it, MetaRush downloads the
  voice cleaner (DeepFilterNet, about 30 MB, macOS and Linux on Apple Silicon, Intel and ARM) and the voice clarity
  score's model into `<home>/components`, each pinned to a release and checked before use. To set it up ahead of
  time (for example before going offline): `metarush sound setup`.
- **Studio** (on your computer, slower, stronger): a one-time setup you run yourself, about 1.8 GB:

  ```bash
  metarush sound setup studio      # or Set it up on the Sound tab
  ```

  It needs Python 3.12–3.14; when none is found and Homebrew is there, it installs Homebrew's `python@3.13` (only
  Python itself: no other Homebrew package is updated). The packages go into their own environment in
  `<home>/components/studio-voice` (pinned versions, prebuilt wheels only), and the voice model is downloaded from
  Hugging Face at a pinned revision. On a Mac with Apple Silicon it runs on the graphics chip; elsewhere on the
  processor (much slower). Setup measures its speed; `metarush sound show` says it. Studio checks every word with
  the local transcriber, so keep transcription installed.
- **Cloud** (ElevenLabs voice isolation, 1,000 credits a minute of speech): uses the same per-machine key as AI
  narration (see above). You see the price first; the project's budget applies.
- Removing them: delete `<home>/components` (they are set up again when needed).

## Optional: link imports (YouTube, TikTok, X)
`metarush import <link>` and the dashboard's "Paste a link" use **yt-dlp**, and YouTube needs a **JavaScript runtime**
for its signature challenges (yt-dlp supports Deno, Node 22+, QuickJS and Bun; it enables only Deno by default).
The `link-import` component adds them (suggested in setup):

```bash
metarush components add link-import   # Homebrew's yt-dlp (with Deno), or yt-dlp's own program where Homebrew isn't
metarush import --check-tools
```

- **Linux / without Homebrew:** the self-contained yt-dlp release binary (it bundles the challenge scripts and
  updates itself) or `pipx install "yt-dlp[default]"`, plus Deno (https://deno.com). MetaRush finds them on PATH, or
  set `tools.ytDlp` / `tools.deno` (or `METARUSH_YTDLP` / `METARUSH_DENO`). Without Deno, MetaRush hands yt-dlp the
  Node 22 it already uses for Remotion.
- **Staying current matters**: sites change often and an old downloader stops working.
  - **By itself (yt-dlp only):** while the daemon runs it updates yt-dlp about once a week, only when no import is
    running. It never upgrades Python, Deno or any other Homebrew package in the background, because such an upgrade
    can break other tools on your Mac: with Homebrew it upgrades yt-dlp only when everything yt-dlp relies on is
    already installed and current (`brew upgrade --formula yt-dlp`, leaving packages that use yt-dlp alone). When a
    newer yt-dlp would need newer Python or Deno, it waits: `metarush doctor` warns, and Settings → Footage → Link imports says
    so, until you update it. `metarush import --auto-update off` (or the Settings checkbox) turns the weekly update off.
  - **When you ask:** `metarush import --update-tools` (or Update now in Settings) updates it the way it was installed:
    `brew upgrade yt-dlp`, which also updates what yt-dlp needs (Python, Deno) when Homebrew has newer versions;
    `yt-dlp -U` for the self-contained build; a pip install is updated with pip.
  - `metarush doctor` shows the version, how old it is, the JavaScript runtime, the challenge scripts and a waiting
    update. `tools.brew` (or `METARUSH_BREW`) points MetaRush at a Homebrew that isn't on the PATH.
- **Bot checks and sign-in walls:** "Use my browser's sign-in" (or `--browser-signin chrome|safari|firefox|brave|edge`)
  lets yt-dlp read that browser's cookies for one import. On macOS, allow the keychain prompt for Chrome, Brave or
  Edge; Safari needs Full Disk Access for the program running MetaRush (System Settings → Privacy & Security).
- **PO tokens:** not needed for public videos today, and no provider plugin is installed. If YouTube starts demanding
  them, the bgutil-ytdlp-pot-provider plugin runs locally (it talks only to Google) and yt-dlp picks it up from its
  plugin folders by itself; `metarush doctor` lists it. MetaRush does not install it for you.

## Optional: looking at what's on screen (macOS)
On a Mac, MetaRush indexes what is in each clip (people and faces, the place, things and animals, text on screen)
with Apple Vision, on your computer and at no cost ([visual-index.md](visual-index.md)). It needs a small helper
program, `metarush-vision`, built from Swift source that ships inside `metarush`, so it needs Swift:

```bash
xcode-select --install     # the Xcode Command Line Tools (swiftc); macOS 12 or newer
metarush doctor            # builds the helper if needed; "ON-SCREEN INDEX" shows it
metarush footage visuals status
```

- `./bootstrap.sh` builds `bin/metarush-vision` next to `bin/metarush` when `swiftc` is there. Otherwise `metarush
  doctor`, or the worker before the first clip it looks at, builds it into `<home>/tools/metarush-vision` (a few
  seconds; one build per helper version is kept in `~/Library/Caches/metarush/vision`, so other homes on the Mac reuse
  it). Installs from Homebrew or a release archive work the same way: the binary carries the helper's source, and the
  helper is compiled on your Mac, so Gatekeeper has nothing to block.
- After an upgrade that changes the helper, it is rebuilt by itself; without `swiftc`, `doctor` says the helper is from
  an older MetaRush and keeps using it while its format still matches.
- `tools.vision` (or `METARUSH_VISION`) points MetaRush at a helper you built yourself
  (`swiftc -O -o metarush-vision tools/vision/main.swift`).
- Looking at clips is a big job: on battery below your threshold it waits like transcription does.
- **Recognizing faces across clips** ([visual-index.md](visual-index.md#faces-and-people)) needs nothing to
  install by hand. It runs the face model with onnxruntime, which comes with the transcription engine (sherpa-onnx),
  and downloads the model the first time a clip's faces are fingerprinted, into `<home>/components/faces/`, checked
  against its SHA-256 before it is used (a file that doesn't match is refused and never kept):

  | File | From (pinned revision) | Size | SHA-256 | Licence |
  |---|---|---|---|---|
  | `face_recognition_sface_2021dec.onnx` | `https://huggingface.co/opencv/face_recognition_sface/resolve/3d7082438a6e4551e840c9b2bb60b71e8da4b524/face_recognition_sface_2021dec.onnx` (OpenCV's mirror of OpenCV Zoo's `models/face_recognition_sface`) | 38,696,353 bytes | `0ba9fbfa01b5270c96627c4ef784da859931e02f04419c829e83484087c34e79` | Apache-2.0 |

  To have it before going offline, copy the file there yourself (the checksum is checked all the same). `metarush
  doctor` says whether this Mac recognizes faces. Without onnxruntime faces are still found in clips, only not
  recognized across them.
- **Linux and Windows:** Apple Vision does not exist there, so the index is skipped: `doctor` says so, and a Mac in the
  same workspace indexes the clips (clips imported elsewhere catch up when that Mac's daemon starts). Everything else,
  including visual change detection and smart contact sheets, works everywhere.

## Optional: the browser extension
The "Send to MetaRush" extension saves videos from your own signed-in browser when a download is blocked. Load it
unpacked from the folder `metarush extension path` prints and pair it with `metarush extension pair` (or Settings →
Browser extension): [browser-extension.md](browser-extension.md).

## Optional: Claude Desktop and other MCP clients
MetaRush can be a local MCP server (stdio, off unless a client starts it): `claude mcp add metarush -- metarush mcp
serve` for Claude Code, or an `mcpServers` entry with the full path of `metarush` and `["mcp", "serve"]` in Claude
Desktop's config (no `mcp-remote` bridge: that is for HTTP servers). Read-only unless you add `--allow-changes`.
Details: [mcp.md](mcp.md).

## Optional: other editing apps
`metarush export timeline <version> --format otio|fcpxml|xmeml --out <file> --media <folder>` (or Send to editing app
in the dashboard) hands a cut to DaVinci Resolve, Final Cut Pro or Premiere Pro with its footage; nothing to install.
What each format keeps: [timeline-export.md](timeline-export.md).

## Homes
Each installation is a directory (default `~/.metarush`, or `--home` / `METARUSH_HOME`) containing `config.yaml`,
`cache/`, `scratch/`, `state/`, `logs/` and a `.gitignore` that ignores everything. Several homes can coexist on one
machine (the repo's dev instance uses `.dev/home`, which git ignores). Projects and footage may live on shared drives;
the home should not — `doctor` fails a home inside a sync folder or one git would commit.

Credential files MetaRush creates for you to move (join bundles, the CI dotenv) go to `~/.metarush-bundles/` (0700)
by default; `machine join` deletes the bundle once the join succeeds (`--keep-bundle` keeps it).

## Local cache
Each installation keeps verified copies of the media it works with in `cache.dir`, limited to `cache.maxGB`. While
the daemon (or `worker start`) runs it prunes least-recently-used copies back under the limit every
`cache.pruneIntervalMinutes` (default 10; negative turns it off), never touching pinned copies or ones a job is
using. Before going offline, `metarush cache prefetch --project <id> --pin` (or `--revision <id>`) downloads
everything renders and reviews need; `metarush sync status --project <id>` confirms ("Offline: ready"), and also
lists interrupted ingests, queued jobs and whether the database and media store answer. `metarush cache repair`
re-verifies every copy if a disk misbehaved.

## Upgrading
Replace the binary file rather than overwriting it in place: on macOS, `cp` onto an existing executable can leave a
stale code-signature cache and the system then kills it on launch (exit 137, no output). Use
`rm -f ~/.local/bin/metarush && cp bin/metarush ~/.local/bin/` (or `install -m 755`, which also creates a new file).
`scripts/deploy.sh` does all of this from a checkout. Rebuild or replace the binary; `metarush serve` applies
additive migrations automatically (`metarush migrate` to do it explicitly) and sets up the new version's renderer
(`metarush renderer install` to do it first; `metarush renderer prune` removes old ones). Skills: `metarush skills
install` again — files you edited are kept unless `--force`. A background service picks the new binary up with
`metarush service restart`.

## Uninstalling
`metarush service uninstall` (if you installed the service), then delete the binary and the home directory (the
renderers live in it). Shared data (database, bucket) is untouched; remove those deliberately with your provider's
tools if you really mean to. Keychain entries are under the service name "MetaRush". With Homebrew: `brew uninstall
--cask metarush` (or `brew uninstall metarush`) and `brew untap metavert-io/metarush`.

## Signing and notarizing releases
For whoever makes MetaRush's releases: macOS builds are signed with a Developer ID (hardened runtime) and
notarized by Apple when the Mac that runs `scripts/release.sh` is set up for it. Nothing about the identity is in the
repository. Without it, `scripts/release.sh --snapshot` and releases say "not signed" in one line and work as before.

**One-time steps** (people only: they need the Apple account's owner):

1. **Apple Developer Program.** The account that will sign must be a member (developer.apple.com → Account; a yearly
   fee). Note its **Team ID** (Membership details).
2. **A Developer ID Application certificate** on the Mac that makes releases: Xcode → Settings → Accounts → (the
   Apple ID) → Manage Certificates → **+** → **Developer ID Application** (only the account holder can create it). It
   lands in the login keychain. Check: `security find-identity -v -p codesigning` lists
   `"Developer ID Application: <name> (<team id>)"`.
3. **A notarization profile** in the same keychain: create an app-specific password at account.apple.com (Sign-In
   and Security → App-Specific Passwords), then run
   `xcrun notarytool store-credentials metarush-notary --apple-id <apple id email> --team-id <team id>` and paste the
   password when it asks (it is stored in the keychain, never in a file).
4. **Tell the release script**, in `release-signing.env` at the top of the checkout (git ignores it) or in the
   environment:
   ```bash
   METARUSH_SIGN_IDENTITY="Developer ID Application: <name> (<team id>)"
   METARUSH_NOTARY_PROFILE=metarush-notary
   ```
5. **Check:** `scripts/release.sh --snapshot` signs and notarizes both macOS builds (a few minutes each) and ends
   with "macOS builds signed (hardened runtime) and notarized". Over SSH, unlock the keychain first
   (`security unlock-keychain ~/Library/Keychains/login.keychain-db`).

What happens: after each macOS build (before the archives are made) `scripts/macos-sign.sh` runs `codesign
--options runtime --timestamp` with `scripts/macos-entitlements.plist` (library validation off, because MetaRush
loads sherpa-onnx's and onnxruntime's libraries), checks the signature, then sends a zip of the binary to Apple with
`notarytool --wait`; anything but Accepted stops the release and prints Apple's log. A program on its own can't
carry its notarization ticket (Apple's stapler takes apps, disk images and installer packages only), so macOS checks
it online the first time the program runs. With only `METARUSH_SIGN_IDENTITY` set, builds are signed but not
notarized.
