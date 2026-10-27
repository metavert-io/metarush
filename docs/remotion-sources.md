# Remotion sources

A **Remotion source** is a whole Remotion project (code, its `public/` files, its own package versions) kept inside a
MetaRush project, so a film someone already made in Remotion becomes a springboard: its compositions turn into clips
that mix with real footage, generated shots, narration and music; its existing renders become footage at once; and
Claude can keep editing its code on MetaRush's own history, with every version checked in the render jail and
approved by a person before it reaches a cut.

It is not a [project motion graphic](motion-graphics-as-code.md): those are small, single-folder overlays
under a strict import allowlist with no audio, no `public/` files and bundled fonts only. A Remotion source is kept
whole and rendered whole, in the same no-network jail, with a narrower trust story of its own (below).

## What a person and Claude see

```bash
metarush import remotion ~/films/kite-teaser --project "Kite"      # a folder (never modified)
metarush import remotion https://github.com/acme/kite-teaser --project "Kite" --wait
metarush remotion list --project Kite                # sources, versions, compositions (size, fps, length)
metarush remotion show kite                          # one source: versions, packages, notes, renders
metarush remotion use kite KiteTeaserV3 --project Kite          # make the clip (render) and add it to the cut
metarush remotion stills kite Open --at 1.5,3        # look at frames of any version (jailed, nothing saved)
metarush remotion checkout kite ./kite            # MetaRush's branch, as files (with git for diffs)
#   … Claude edits ./kite/src/story.ts …
metarush remotion check ./kite -m "Shorter Analyze headline"   # new version: summary, type check, before/after stills
metarush remotion approve kite@3fa9c2d1              # people only (dashboard or an interactive terminal)
metarush remotion map kite                           # scenes, words on screen, pictures, sounds, a still per scene
metarush remotion words setup kite                   # once: its words become settings (a version a person approves)
metarush remotion words set kite END.tagline="Ship it" --check   # then words change as settings, checked in the jail
metarush remotion pictures setup kite                # once: its pictures become settings (a version a person approves)
metarush remotion pictures set kite HERO ~/art/hero-v2.png --check   # then art is replaced as data, checked in the jail
metarush remotion use kite Teaser --sound layers          # the picture alone; its music and voice as the cut's tracks
metarush remotion lift kite V3End --name end-card    # a scene as a motion graphic; the theme as a shared library
metarush remotion refresh plan kite --step "npm run vo" --key elevenlabs=ELEVENLABS_API_KEY   # its own steps (people start them)
metarush remotion push plan kite@4                   # an approved version's changes, back to its repository (people only)
```

In the dashboard, a project's **Sources** tab lists its Remotion projects (where each came from, its versions and
whether a person approved them, every composition with size, frame rate and length, a **Use in the cut** button, and
the videos it already had), a version waiting for a person shows the change in plain words with before/after stills
and **Approve** / **Turn down** buttons, and the tab's Add box takes a folder path or a GitHub link.

## Data model

| Collection | ID | What it holds |
|---|---|---|
| `remotion_sources` | `rms_…` | One source in one project: short `name` (unique per project), `title`, `origin` (folder: base name, machine, the path as typed with `~`; GitHub: canonical URL, ref, commit; the original commit and whether the folder had uncommitted changes), `head` (newest version), `approved` (newest approved version), the package set people allowed (`packages`: lockfile hash and list), what the import left out and why, the existing renders it brought in. |
| `remotion_versions` | `<workspace>/<hash>` | One version: `hash` = SHA-256 of the canonical manifest, `parent`, `number` (1, 2, 3… per source), `kind` (`import` \| `edit`), message, who/when/where; the manifest object; counts; the lockfile hash, the Remotion version and the packages; Google Fonts (URL → object); `remotion.config.ts` notes; the static read (compositions as written, imports per file, the import graph); the jailed read (exact compositions); the check (`check`: summary, file changes, changed on-screen words, type problems, affected compositions, before/after stills); state `waiting` \| `approved` \| `rejected` \| `revoked` with decisions (compare-and-swap, like motion graphics). |
| `remotion_renders` | `<workspace>/<key>` | The render cache: `key` = SHA-256 of {source, version, composition, canonical props, frames, render settings}; state, job, the clip it became (`assetId`, `sha256`), length, size, machine, what the jail blocked. |
| `remotion_refreshes` | `rmf_…` | One refresh: the plan a person started (steps with their command lines, keys as provider → variable name, outputs, hosts, the fingerprint), who and on which computer, the version it started from, each step's outcome, the log (an object, keys hidden), what it changed and what it left out, the version it made, state. |
| `remotion_pushes` | `rmp_…` | One push back: the version, where it went (repository, branch, the original commit and the new one), the pull request, who, when, state. |

Clips made from a source are ordinary assets whose `source` provenance has `platform: "remotion"` and a `remotion`
block: source, version (hash and number), composition, props, frames, render key, `kind` (`render`: MetaRush made it;
`existing`: it was in the project's `out/` when it was imported, with its path).

Migration **26** creates the three collections' indexes. The project map, words as settings and lifting add fields,
not collections (no migration): a
version's `map` (state and summary; the document is an object), `copy`, `wordsHelper` and `settings`, each
composition's `words` (the setting keys its code reads); a source's `words` and `wordsPending`; a render's
`words`/`wordsNumber` and `sound`/`sounds`. Pictures as settings adds fields too (no
migration): a version's `pictures`, `picturesLeft`, `picturesHelper` and `picturesRead`, each composition's
`pictures` (the names its code draws); a source's `pictures` and `picturesPending`; a render's and a clip's
`pictures`/`picturesNumber` and `ownPictures` (a render also keeps `pictureValues`, to serve them).

## Storage

- **A version is a manifest** (`metarush.remotion/1`: sorted `{path, size, sha256}` for every file, compact JSON); its
  SHA-256 is the version. Every file is its own content-addressed object in the media store (`objects/<sha>`), so a
  103 MB `public/` folder is stored once and an edit that touches one file uploads one file; every machine in the
  workspace can render any version.
- **What comes along**: the project's text and code, `package.json` and `package-lock.json`, and everything in
  `public/` (pictures, sound, video, data; even when `.gitignore` names it). **What never does**: `.env*` and other
  secret files (keys, certificates, `.npmrc`, credentials) and any file whose contents look like a secret;
  `node_modules`, `.git`; files `.gitignore` excludes (outside `public/`); build caches; `out/` (its videos become
  footage instead); instructions and settings for coding assistants and other tools (`.claude/`, `CLAUDE.md`,
  `AGENTS.md`, `.cursor*`, `.github/`, `.vscode/`, `.husky/`), because a checkout Claude edits must not carry another
  project's instructions or hooks; links and unusual names. Every left-out file is listed with a plain reason.
- **The original is never touched**: a folder is only read (git is asked read-only questions with optional locks
  off); a GitHub repository is fetched into a temporary bare repository with hooks, prompts and other protocols off
  and its blobs read raw (the same git runner as repositories), with the person's own sign-in.
- **Per machine** (never shared, rebuilt on demand): the work tree of a version (`<cache>/remotion/work/<hash>`,
  files cloned from the cache), bundles per version and render settings, and the package environments.

## Rendering

1. **Its own Remotion.** Each source renders with the packages its lockfile pins, installed once per lockfile hash
   into `<home>/remotion/envs/<key>` with `npm ci --ignore-scripts` from the public npm registry. Before installing,
   MetaRush refuses a lockfile with a package from anywhere else, without an integrity hash, or a git, file or link
   dependency. `@remotion/bundler` and `@remotion/renderer` come from the same lockfile and must match `remotion`'s
   version. Chrome Headless Shell is shared per Chrome version (a copy of the renderer's own when they match).
2. **MetaRush's bundler settings.** `video/render/source/run.mjs` (MetaRush's code, from the per-version renderer)
   loads the source's own `@remotion/bundler` and bundles the entry point with webpack and MetaRush's configuration:
   no webpack override from the project, a loader that points `@remotion/google-fonts` at the bundle's own copies,
   the render page's Content-Security-Policy injected into the bundle's page. `remotion.config.ts` is never run: it
   is read as text (`Config.setX(literal)` calls) and only known-safe settings are applied (frame image format, JPEG
   quality); the rest are listed as notes (Rspack, premount, webpack overrides, concurrency…).
3. **The jail.** Every page that runs project code (reading compositions, checks, stills, renders) runs in the render
   jail: the refusing proxy with only the bundle's loopback server bypassing it, host-resolver rules, WebRTC limited
   to the proxy, the CSP, the Node download guard, a per-frame time limit and a total limit scaled to the length (10
   minutes plus 6× the composition's length; compositions longer than 15 minutes are refused). A browser that can't
   be jailed stops the run (fail closed). `staticFile()` reads `public/` from the bundle on loopback; Google Fonts
   come from copies MetaRush downloaded at import (only `fonts.gstatic.com`, by HTTPS, size-capped) and serves from
   the bundle itself.
4. **Reading compositions without running Node code**: a static read of the entry point and `Root` with the
   TypeScript parser (`analyze.mjs`: ids, sizes, frame rates, lengths written as numbers, `calculateMetadata`, folders,
   imports per file) and a jailed evaluation of the bundle (`getCompositions` in the browser) for lengths computed in
   code. The jailed read is the truth; the static read is shown until it has run and explains failures.
5. **A composition becomes a clip by rendering it** (`remotion-render` job, heavy, any machine): render in the jail
   with MetaRush's encoding (H.264, CRF 18, AAC; the project's own sound included), check the length and size, then
   ingest it as footage (transcribed, indexed) with provenance, and record the render. The same key never renders
   twice.

## The editing loop

1. `remotion checkout <source> [folder]` writes a version's files (code; `public/` with `--public`) into a folder,
   with `.metarush/remotion.json` (source, base version) and a git repository whose commit is the base, so `git diff`
   shows Claude's changes.
2. Claude edits the code (never running the project's scripts or `npm`).
3. `remotion check <folder>` (or `--version` to check a recorded one again) makes the folder a version on MetaRush's
   branch (parent: the base) and checks it: the change in plain words (files, and on-screen words that changed, old →
   new), packages (a version that adds or changes packages stops here until a person allows them:
   `remotion allow`, people only), a type check with MetaRush's own TypeScript against the source's package types (no
   project code runs), the compositions the change can affect (the import graph from each composition to the changed
   files; any `public/` change affects the compositions that use it), a low-resolution pass over each affected
   composition before and after (every second, in the jail) to find where the picture changed, and full-size
   before/after stills there. A passing version waits for a person. Checks of unapproved versions are fine; that is
   what they are for.
4. A person reviews it (dashboard: Sources → the version; or `remotion show`) and approves it (people only, like
   `motion approve`; Claude may check and preview but never approve).
5. Approval moves the clips: every latest cut that plays clips of this source from an older version gets them
   rendered at the approved version and saved as a new revision ("Moved 2 clips to version 3 of kite"), keeping
   their in and out points (clamped when the composition got shorter). `--keep-cuts` skips this; `remotion update`
   does it later. Older revisions keep their clips: they point at the old renders, which never change (render
   stability).

Rendering a composition into a cut needs an approved version; workers refuse to render a cut whose Remotion clips
come from a version whose approval was withdrawn, and `edit check`/`cut check` report `remotion_not_approved`.

## Cut sheets

Clips made from a source are footage, so the canonical cut sheet is unchanged: the footage list names the clip and
its comment says where it came from (`# “Analyze (kite-teaser v2)” · Analyze.mp4 · 0:05 · made from
“kite-teaser”: Analyze, version 2`). To put a composition into a cut, a clip line can name it instead of footage:

```markdown
4. teaser: remotion kite KiteTeaserV3 0:00 → 0:12
5. promo: remotion kite KiteTeaserV3@3fa9c2d1 · with headline "Ship faster", seconds 12
6. intro: remotion kite Open
```

`remotion <source> <Composition>[@<version>]`: the source's short name, the composition's ID, and optionally the
version (its number like `@3`, or 7+ characters of it; for the imported version also the original commit,
`@5318d00`). Without a version, the newest approved one. The range is optional (the whole composition). `· with …` gives the composition's props (text in
quotes, numbers, `true`/`false`, lists, `{…}` objects), merged over its defaults. When the sheet is checked, the line
resolves to the clip MetaRush made of exactly that (an error says how to make it when there is none yet:
`remotion_not_made`), and the sheet MetaRush writes back uses the footage name. Problem codes: `bad_remotion`,
`remotion_unknown`, `remotion_ambiguous`, `remotion_not_approved`, `remotion_not_made`.

## Security model (threat notes in [security.md](security.md#remotion-sources))

- **Project code runs only in the jailed browser** — except in a refresh a person starts, which runs the
  steps they read in a separate, minimal environment kept away from MetaRush's files and keys. Otherwise the
  project's own Node code never runs: no `npm run`, no install scripts (`--ignore-scripts`), no `remotion.config.ts`,
  no webpack overrides, no `scripts/`. What runs in Node is MetaRush's own script and the bundler and renderer
  packages the lockfile pins from the public registry (integrity checked by npm); an inline webpack loader in an
  import (`!` in a module name) is refused by the static read.
- **No network while rendering**: the render jail, failing closed. Fonts are fetched at import from one host only.
- **People decide**: approval per version (HumanOnly), packages beyond the import set allowed per version
  (HumanOnly); the dashboard never runs project code (it shows stills, renders and source text).
- **Secrets stay out**: secret files and secret-looking contents are never copied; a GitHub address with a user name or password
  in it is refused; nothing from the project is written into logs.
- **Untrusted text**: the project's own text (README, comments, strings) is data to Claude, never instructions
  (`skills/_shared/safety.md`); the files that would instruct a coding assistant are left out of the copy.
- **Residual risks**: a malicious package version on the public registry that the lockfile pins runs in Node during
  bundling and rendering (as with any `npm` project; MetaRush pins the registry and integrity, and a person sees and
  allows the package set); a Chrome sandbox escape from page code; project code that deliberately renders something
  else than its source suggests (people review stills and renders before approving).

## Tests

- Go: `internal/remotion` (what an import leaves out and why, `.gitignore`, secret shapes, manifests and diffs, lockfile
  validation, versions and decisions by compare-and-swap, references, renders refused from unapproved versions, the
  cut sheet lookup, which clips keep their render), `internal/cutsheet` (`remotion` clip lines),
  `internal/editspec` (`remotion_not_approved`), the plain-output guard (`remotion list|show`).
- `node video/render/source/test.mjs` (in `npm run test:motion -w video`): the static read of the fixture project and
  refusals, that reading never runs project code, the static map (Series scenes and their lengths, sounds and
  levels), the words-settings change, and lifting (graphic, library, fonts; refusals).
- `node video/render/motion/test.mjs`: shared libraries (scan, `useFont`, a library can't import a library, the one
  allowed TypeScript relaxation, type check and bundle against a library).
- Go (the map, words and lifting): `internal/editspec` (library pins: unpinned, unknown, waiting, mixed; library fonts as font uses; the
  plan's motion hashes include libraries), `internal/cutsheet` (`+lib@version` round trip and fill-in),
  `internal/remotion` (lift names, the approval question and bell, a person-only answer, words-check pictures).
- `scripts/acceptance.sh`, step "Remotion sources": the fixture project (`tests/fixtures/remotion/fixture-film`: three
  compositions — Title, Card and a two-scene Film with music under it and a sound — a picture and sounds from
  `public/`, a Google Font; its postinstall exits 42) imported from a folder with a `.env`, coding-assistant files
  and a render in `out/` beside it, through approval, a render with props, an edit checked and approved into the
  cut, a change that reaches the internet refused, and withdrawal. Step "Remotion sources (the map, words and lifting)": the map (Film's
  two scenes with exact times, its words with where they are written, its music and sound with their levels, a
  still per scene), the approval to-do and bell, Film in the cut with its sounds as tracks, its words made settings
  (a version a person approves) and a headline changed as a setting (checked, then the clip made again and the cut
  moved), the end card lifted into a motion graphic with the shared theme (its font a resource), rendered in a cut,
  and a theme change approved once moving the cut's graphic to the new library version.
- `scripts/remotion-e2e.sh` (`web/scripts/remotion-e2e.mjs`): the dashboard in a real browser, desktop light and dark
  and a phone, including the scenes, words and the to-do.

## The project map, words as settings, lifted graphics, sound in layers

### The project map

`metarush remotion map <source>[@version] [--composition C] [--again] [--json]` (op `remotion.map`; the dashboard's
**Scenes and sounds** under Sources) shows what a version holds: per composition its **scenes** (each entry of a
`Series` or `TransitionSeries`, or a `Sequence`; the whole composition when it has none) with exact times, what sets
each length (`file:line` of the expression, or "worked out from its data" with clues such as
`src/generated/voiceover.json`), the **words on screen** (the literal or constant that writes them, `file:line`, the
setting key once words are settings, whether they spill out of their box), the **pictures** (`staticFile` assets),
the **sounds** (file, start, end, trims, rate, level and how it changes — fades and dips — and a guess of its role:
music, voice line or effect, with the voice line's words from a timing file), the **fonts**, the **data files** the
code reads, and a **still per scene**.

How it is made — a `remotion-task` job (heavy, any machine with Node), once per version (`--again` remakes it):

1. **The static read** (`render/source/map.mjs`, nothing runs): words, pictures, sounds, fonts and data per file;
   each `Series.Sequence`/`TransitionSeries.Sequence`/`Sequence` with its length expression and the component it
   draws; copy files.
2. **MetaRush's probe in the jail** (`render/source/probe/*`): for this run only, the bundler resolves the project's
   own imports of `remotion` and `@remotion/transitions` (a webpack rule-level alias that applies to the project's
   files, never to `node_modules` or the probe itself) to thin wrappers that report, through the page console, where
   each `Sequence`/`Series`/`TransitionSeries` entry starts and how long it is (`[metarush-scene]`), and — in the
   still pass — every text node's box, font and size after fonts load, and the pictures drawn (`[metarush-text]`).
   Sounds come from Remotion's own asset list (`renderFrames` with no images written): file, frame, volume, trims,
   rate. The probe adds no global hooks and changes nothing the project draws; it runs under the same render jail as
   every render.
3. **Joined in Go** (`internal/remotion/mapping.go`): static facts are matched to what the page saw (by name, the
   component's file, length and distance in the import graph), stills are kept as objects, and the map document
   (`metarush.remotion-map/1`) is stored content-addressed with a summary on the version (`map`).

### Words as settings

Many Remotion projects keep their words in one file of constants (`src/story.ts`). Once, Claude (or a person) runs
`metarush remotion words setup <source>` (dashboard: **Words on screen → Make its words settings**): MetaRush writes
the next version itself — each exported text constant of the copy files wrapped as `words("NAME", …)`, and a small
helper `src/metarush-words.ts` that gives back the value as written unless a setting changed it (read from the
render's input prop `metarushWords`). It goes through the normal check (nothing should look different: the check
compares) and **a person approves it**, like any version.

After that, words change as **settings**, not code: `metarush remotion words set <source> END.tagline="…" [--check]`
(op `remotion.words.set`; the dashboard form):

- the values live on the source (`remotion_sources.words`: numbered changes, who and when, at most 50 kept; empty
  words go back to the code's);
- each change is **drawn in the jail and checked** before it is made: lengths before and after (a composition that
  gets longer or shorter), words that spill out of their box or the frame (the probe's measurements),
  before/after stills; `--check` only checks (kept as the pending check);
- renders pass only the keys a composition reads (canonical JSON), which is part of the render key, so clips are
  made once per set of words; the latest cuts' clips that show a changed key are made again and move (a new
  version of each cut, like approving a version);
- no new version of the code and no code approval: the approved code is unchanged and words are data. Anyone who may
  change cuts may change words (Claude included); every change is recorded and undoable.

### Sound in layers

`metarush remotion use <source> <Composition> --sound layers` (dashboard: **Use in the cut, its sounds as tracks**)
renders the composition **without its sound** (render key `sound=layers`) and brings its sounds into the cut:
MetaRush reads where each `<Audio>` plays (Remotion's asset list from a render with no images: file, start, trims,
volume over time, rate), adds each distinct file from `public/` as footage (content-addressed, provenance kept) and
places it at the clip's position — music as a music track with its level, fades and dips under voices, effects as
effect tracks, voice lines with words (from a timing file) as narration lines anchored to the clip — so the Sound tab,
the mix and a generated voiceover take over. The default stays "sound as the project made it" (`mixed`).

### Lifting a scene into a motion graphic; shared libraries

`metarush remotion lift <source> <Composition> [--scene N|name] [--name end-card] [--library brand-theme]`
(dashboard: **Make it a motion graphic** on a scene) turns a scene into a [project motion graphic](motion-graphics-as-code.md)
any cut can use, and the files it shares with the rest of the project into **one shared library**:

- **The graphic**: the scene component's file (through simple wrappers such as `EndV3 = () => <End tagline={…} />`,
  whose values become the preview) and an `index.tsx` MetaRush writes that draws it at its own size, fitted to the
  frame (`<Sequence width height>` so its own `useVideoConfig()` sizes hold), with its props as the parameters.
- **The library** (`@metarush/lib/<source>-theme`): every other file the scene imports (theme, components, words),
  with an `index.tsx` that exports them and `library.json`; the Google Fonts its code loads become **font resources
  in the workspace library** (the files MetaRush kept at import) drawn with `useFont` from a `useThemeFonts()` hook;
  the words helper is fixed to the words at the time of the lift. Lifting a second scene keeps everything the
  library already holds.
- Refused, plainly: scenes that play sounds or videos, draw the project's own files (`staticFile`), or import packages
  beyond the motion allowlist. Code lifted from a project is type-checked the way its project checked it (a
  declared `noUncheckedIndexedAccess: false`, the only relaxation allowed).
- Both are checked like every motion graphic (allowlist scan, types, bundle, jailed test frames) and **wait for a
  person**. A library version is approved once for every graphic that uses it: approving it moves the latest cuts'
  graphics to it (a new version of each cut). Cuts pin library versions per graphic (`MotionRef.libs`; cut sheets
  `motion end-card@3fa9c2d1+brand-theme@7e3a91c0`, filled in with the newest approved version when a line names
  none).

### A version waiting for a person

When a version can be approved — read in the jail, its packages allowed, its check passed — the To-dos page asks
"Can version 3 of “Teaser” go into cuts?" (`remotion_approval`, people only; answered with the same approval as under
Sources, one question per source for its newest waiting version, closed when it is decided anywhere) and the bell
tells the person who made the version (`remotion_waiting`, once per version), with a link to its review.

## Refreshing from its own steps, sending changes back

Many Remotion projects have a build of their own: some teaser generators run `npm run build`, which syncs
assets, captures a web app with Playwright, asks ElevenLabs for a voice-over and music, then renders. MetaRush lets a
person run those steps from MetaRush (**refresh**) and send what MetaRush changed back to the original repository
(**push back**). Both are people-only, never automatic and never Claude's; both show everything first.

```bash
metarush remotion refresh plan teaser --step "npm run vo" --step "npm run capture" \
    --key elevenlabs=ELEVENLABS_API_KEY --output public/voiceover/ --output src/generated/   # what would run (read-only)
metarush remotion refresh start teaser …the same flags…     # people only: the plan again, then type "start"
metarush remotion refresh runs teaser                        # past refreshes: steps, what changed, the version made
metarush remotion refresh log teaser 2                       # what refresh 2 printed (keys hidden)
metarush remotion push plan teaser@4 [--branch metarush/teaser-v4] [--no-pr] [--words]   # the changes, in plain words
metarush remotion push start teaser@4 …                      # people only: a new branch (and a pull request on GitHub)
metarush remotion push list teaser
```

In the dashboard, a source's card has **Refresh from its own steps** and **Send changes back**: each opens a panel
with the plan in plain words and a button that asks once more in a styled dialog.

### Refreshing: the project's own steps, started by a person

A refresh runs **steps from the project's newest version**: `npm run <script>` for a script in its `package.json`
(the plan shows the script's command line), `node <file>` for one of its files, or `npx --no <tool>` for a tool its
packages already have (never a download). Steps are typed arguments, never a shell line MetaRush builds; `npm run`
runs the script's own line the way npm always does. Scripts that render (`remotion render`) are marked: MetaRush
makes clips itself.

`remotion.refresh.plan` (read-only; Claude may show it) says, before anything runs:

- **the exact commands**, in order, with each script's line;
- **the keys passed** — only those the person picks, from **Settings → Integrations** on this computer (ElevenLabs,
  Gemini), each under the variable name the project expects (`elevenlabs=ELEVENLABS_API_KEY`), shown masked
  (`••••3f2a`). MetaRush suggests the names from what the code reads (`process.env.ELEVENLABS_API_KEY`) and lists the
  other variables it reads that MetaRush doesn't set (they keep the code's own defaults). Names that steer programs
  (`PATH`, `HOME`, `NODE_OPTIONS`, `DYLD_*`, `LD_*`, `npm_config_*`, `METARUSH_*`…) are refused;
- **what they may write** — the outputs: folders (`public/voiceover/`), files (`src/generated/voiceover.json`) or
  patterns (`public/**/*.wav`). The default is `public/` and the data files its code imports;
- **the hosts they may reach**: the ones its code and scripts name (`api.elevenlabs.io`, named in
  `scripts/generate-voiceover.mjs`) and the services of the keys passed;
- **where it runs** (this computer, and how it is kept apart: below) and what never happens.

`remotion.refresh.start` is **HumanOnly** (the dashboard's button, or an interactive terminal that types `start`):
it takes the plan's fingerprint, so what starts is exactly what the person saw — a new version or a changed plan
asks to look again. It records the run (`remotion_refreshes`, `rmf_…`), remembers its refresh steps on the source for the
next plan, and queues a `remotion-refresh` job **pinned to this computer** (its keys; one at a time per source,
never retried by itself: a refresh that stopped halfway — MetaRush stopped, the computer slept — is reported, not run
again). Progress is plain in the activity bar ("Refreshing “Teaser”: step 2 of 3, “vo”").

### Where a refresh runs

**Outside the render jail, with the network on** — fetching a voice or capturing a site is what the steps are for —
but in **a separate, minimal process environment**:

- **A fresh copy**: the version's files written into a new folder in the system's temporary folder (copies, never
  links to MetaRush's cache), with a private copy of its installed packages (an APFS clone of the package
  environment on a Mac, read-only on Linux); its `.env` was never copied in and is never read.
- **A minimal environment** built from nothing: `PATH` (Node.js's folder and the system's), a fresh `HOME` and
  `TMPDIR` for this run, a cache folder kept per source for npm and Playwright's browsers, `CI=1`, and the picked
  keys under their names. Nothing of MetaRush's own environment (its database address, storage keys, its home)
  reaches the steps.
- **Kept apart from MetaRush and the person's files** (fail closed): on a Mac every step runs under `sandbox-exec`
  with a profile MetaRush writes per run — no reading or writing in MetaRush's home, cache, scratch and media
  folders, the person's home folder (except Node.js's own installation) or the project's original folder; writing
  only in its copy, its temporary folders and its cache; the keychain out of reach (MetaRush's provider keys live
  there). On Linux the same with bubblewrap (`bwrap`): the home folders hidden behind empty folders, MetaRush's
  folders not there at all. A computer that can't keep a refresh apart refuses it plainly (install bubblewrap).
- **Never this computer's services**: the network stays on for the internet, but a step
  reaches nothing on this computer — on a Mac no loopback address at all (MetaRush's database, dashboard and its
  media, model and bundle servers listen there, some on ports chosen as they start), MetaRush's own ports (the
  dashboard's, the developer ports, the database's when it is on this computer) on any address, no unix socket in the
  shared temporary folder or the hidden folders, and nothing listens for others; on Linux, where a step sharing the
  computer's network can't be kept off its loopback, the steps get a network of their own (their loopback is theirs)
  and reach the internet over HTTP(S) through MetaRush's proxy, as an ability's open network does (Node.js's `fetch`
  through the preload). A step that serves something to itself on loopback doesn't work on a Mac.
- **No terminal, nothing left behind**: each step starts in a session of its own (no controlling terminal), and what
  it leaves running is killed when it ends.
- **What the steps wrote is copied first**: the copy is read through an `os.Root`, each file opened without following
  a link, and the new version is made from MetaRush's copies of the files it takes.
- **Limits**: 30 minutes per step, 2 hours per refresh; canceling stops the whole process group.
- **Logs**: each step's output is kept (at most 1 MB, its start and end), with the keys passed, MetaRush's known
  secrets and anything shaped like a credential hidden before it is written (`remotion refresh log`, the dashboard's
  Details). The activity log records the run like any other.

The hosts are shown, not enforced: MetaRush doesn't limit which public hosts a refresh reaches (only this computer's
services are closed to it). That is why a person reads the plan and starts it.

### What a refresh makes

When the steps finish, MetaRush compares the copy with the version the refresh started from. **Changes inside the
outputs** are taken, by the import's rules (no secret files or secret-looking contents, no `node_modules`, `.git`,
`out/` or caches, the same size limits); **changes elsewhere are listed and left out**, with why. The result is a new
version (`kind: refresh`, its message naming the steps) on top of the source's newest version — when someone made
another version while the refresh ran, the outputs go on top of that one — and it goes through the **normal check**
(what changed in plain words, packages, the type check, the compositions it affects, before/after stills in
the jail) and **waits for a person** (a to-do and the bell). Approving it moves the latest cuts' clips like any
version; nothing goes into a cut before that. A refresh that changed nothing makes no version and says so; a step
that fails stops the refresh with its last lines in plain words, and nothing is kept.

### Sending changes back

`remotion.push.plan` (read-only) compares **what was brought in** (the newest imported version: the original commit)
with **an approved version** and says it in plain words: files added, changed and removed (with lines), the words
that changed, packages; where it goes — the GitHub repository it came from, a folder's own remote (any git address:
GitHub, another host, a path) or, for a folder with no remote, its own repository on this computer — **a new branch**
(default `metarush/<name>-v<N>`) and, on GitHub, **a pull request** into the default branch (with `gh`, the person's
sign-in). `remotion.push.start` is **HumanOnly** (fingerprint of the plan, like a refresh) and queues a `remotion-push`
job pinned to this computer (the person's git and gh sign-in; nothing is stored):

1. a fresh bare repository fetches the original commit (depth 1, hooks and prompts off; https, ssh and local paths
   only);
2. a commit is written with git's plumbing — the original commit's files with MetaRush's changes applied, so files
   the import left out (`.github/`, `CLAUDE.md`, `out/`, `.gitignore`d files) stay exactly as they were — signed by
   the person's own git identity;
3. it is pushed to the branch with a plain `git push` — **never `--force`, never the default branch** (the remote's
   HEAD, `main` and `master` are refused), **never a branch MetaRush didn't make**: sending a later version to a
   branch MetaRush made adds a commit on top (a fast-forward), and a branch someone else changed since is refused;
4. on GitHub, `gh pr create` (or the pull request already open for the branch).

The words helper is MetaRush's, so it never goes back: constants are unwrapped to plain code, keeping the
code's own words, and with `--words` the current word settings are written into them. Every push is a
`remotion_pushes` record (`rmp_…`: version, where, branch, commit, pull request, who, when). Migration **34** creates
the indexes of `remotion_refreshes` and `remotion_pushes`.

### Tests of refreshing and sending changes back

- Go (`internal/remotion`): plans (steps from scripts, refused names, keys and variables the code reads, outputs and
  hosts, the fingerprint), the minimal environment, the sandbox profile and — on a Mac — a step that tries to read
  MetaRush's home, the original folder's `.env` and the keychain and to write outside its copy (all refused) while a
  synthetic step writes a JSON file and a picture and a fake "voice" step uses a fake key (hidden in the log); outputs
  taken and changes elsewhere left out; the new version checked and waiting for a person; pushes to local bare
  repositories (`file://`): the branch, a later version as a fast-forward, a branch someone else changed refused,
  the default branch refused, never forced, the words helper unwrapped, a pull request through a fake `gh`.
- `scripts/acceptance.sh`, step "Remotion sources (refreshing and sending changes back)", and `scripts/remotion-e2e.sh` (the panels in the browser,
  desktop light and dark and a phone), both with `scripts/make-refresh-fixture.sh`: a copy of the fixture film that
  builds itself (`npm run make-data` changes the number its title shows, `npm run voice` uses a fake key), a git
  repository whose remote is a local bare repository — refreshed (the title's before/after stills show the new
  number; a file written elsewhere left out; the key hidden in the log), approved, and sent back as a new branch
  with `main` untouched. `node video/render/source/test.mjs` checks that unwrapping the words helper gives back the
  file as it was, with settings written in only when asked.

## Pictures as settings

The words-as-settings pattern applied to a project's **pictures**: an app teaser made from character
art gets new art without a new version of its code to approve.

```bash
metarush remotion pictures list teaser                  # the pictures it draws; settings, or what can become one
metarush remotion pictures setup teaser                 # once: the change that makes them settings (a person approves it)
metarush remotion pictures set teaser HERO ~/art/hero-v2.png --check   # draw it with no network, change nothing
metarush remotion pictures set teaser HERO=hero-v2 LOGO=~/art/logo.svg # replace (resources or files), clips made again
metarush remotion pictures set teaser --original HERO   # the project's own picture again
metarush remotion use teaser Characters --picture HERO=hero-v2 --project P   # this clip's own picture (recipes)
```

In the dashboard: Sources → the project → **Pictures** lists each picture (the project's own and what replaces it,
where it is seen); **Replace…** takes a picture uploaded on the spot (it goes to the project's resources) or one of
the project's resources; **Check the new pictures** draws them first, **Use the new pictures** makes the change.

### The one-time change

`remotion.pictures.setup` (a `remotion-task` job, `analyze.mjs pictures-setup`, nothing runs) finds the pictures the
code draws from its own files and writes the next version with each reference wrapped — the safest static rewrite,
an identity call around an expression, so nothing changes until a setting does:

- `staticFile("hero.png")` with a literal path naming a picture file in `public/` (`.png .jpg .jpeg .webp .gif .svg
  .avif .bmp`; `remotion`'s own `staticFile`, also through `import * as R`) becomes
  `picture("HERO", staticFile("hero.png"))`;
- a default import of a picture file (`import hero from "./art/hero.png"`, a URL in Remotion's bundle) is renamed
  `heroAsWritten`, and `const hero = picture("HERO", heroAsWritten);` follows the imports, so every use is
  unchanged (refused, and left as it is, when the import brings more than the picture or the code uses it before the
  imports end);
- the helper `src/metarush-pictures.ts` (next to the files it is used in) gives back the value as written unless the
  render's input prop `metarushPictures` names a picture MetaRush serves (`/__metarush_pictures/<sha256>.<ext>`);
  when a file already has its own `picture`, the import is `picture as metarushPicture`.

Names come from the files: `hero.png` → `HERO`, `logo-dark.svg` → `LOGO_DARK`; a clash takes the folder
(`public/art/hero.png` → `ART_HERO`), then a number. A file drawn in several places is one setting. References whose
file is worked out as the video plays (`` staticFile(`cast/${id}.png`) ``), or that name a file outside the project
or one it doesn't have, stay code, listed with why (`picturesLeft`): Claude can write each picture's file name into
the code first (an ordinary change). The version goes through the normal check (nothing should look different) and
waits for a person. Setup can run again later for pictures added since; it wraps only those. A version read by an
earlier build has no picture facts yet: the first look (`remotion.pictures`, the panel, `pictures list`) queues a
short read of its code as text (a `remotion-task`, nothing runs; `picturesRead` marks it done) and says so
meanwhile; Claude's read-only channel starts nothing.

### Replacing a picture

`remotion.pictures.set` (`set: [{name, resource | file}]`; neither: back to the project's own picture):

- **The art is a resource** of the project (`internal/resources`): a file on this computer is added first (sniffed by
  content, HEIC converted, an SVG cleaned — refused over the network); a resource must be a picture or an SVG (not a
  PDF, font or page snapshot), at most 50 MB and 64 million pixels, a still picture (an animated GIF is footage),
  an SVG with nothing left to clean; a version kept on one computer only is uploaded so every computer can render it.
- **It is served by its hash**, never by a path from the project: renders pass `metarushPictures: {NAME:
  "/__metarush_pictures/<sha256>.<ext>"}` and `run.mjs` copies the file from MetaRush's cache into the bundle's
  folder under that name (`serve-pictures.mjs`, checked against the hash first) — the bundle's own loopback server
  is the only place the jailed page may reach, as for Google Fonts.
- **It is checked in the jail first** (a `remotion-task` job): the compositions that draw it, before and after
  (lengths; whether the new picture is seen on screen, through the probe; its shape and size against the picture it
  replaces — "wider than the one it replaces, so it may be cropped or squeezed", "smaller … so it may look soft";
  before/after stills where the map saw the picture); `--check` (Check the new pictures) keeps the check only.
- **It is part of the render key** (`pictures=` the canonical JSON of name → served file, only names the composition
  draws; a render with the project's own pictures keeps its old key), so clips are made once per set of pictures;
  then the latest cuts' clips that draw it are made again and move (a new version of each cut). Changes are numbered
  on the source (`pictures`, at most 50 kept, who and when), compare-and-swap. No new version of the code, no new
  approval; anyone who may change cuts may change pictures (Claude included, asking first).

Word and picture checks each draw with the other's current settings, so a check shows what a render will make.
Sending changes back takes this helper out too (`analyze.mjs bake`: the calls unwrapped, the consts removed
and the imports renamed back, the helper deleted — the files exactly as they were); replaced pictures stay in
MetaRush.

### The binding point for recipes

A recipe input binds to a picture by **source and name**: `{source: "teaser", picture: "HERO"}`; a list input (3–8
pictures) binds to an ordered list of names, each run's pictures filling them in order (names left over keep the
project's own). `remotion.pictures` is what a recipe's check reads: each name, whether it is a `setting`, the
compositions that draw it (`used`) and where the map saw it (`seen`), and the size of the picture it replaces
(`width`, `height`) to check an input's shape against.

A run passes the input's pictures **per clip**: `remotion.use` and `remotion.render` take `pictures: [{name,
resource | file}]` (Go `RemotionRenderIn.Pictures`; CLI `--picture HERO=<file|resource>`). They are checked like a
change of the source's pictures, win over the source's for that clip only (`{name}` alone keeps the project's own
picture there), are part of its render key, and stay with the clip (`RemotionClipSource.ownPictures`) when it moves
to a newer approved version — other cuts and projects that use the source are untouched. `remotion.pictures.set`
changes the project's art everywhere; a run never calls it. A version whose pictures aren't settings yet answers
`E_VALIDATION` ("aren't settings yet"): the recipe's check says so and points at `remotion pictures setup`, which a
person approves once.

### Tests

- `node video/render/source/test.mjs`: the fixture film's picture found, wrapped and read back as a setting, its name
  kept out of the map's words, unwrapped back to the file it was; imported pictures, clashing names, a local
  `picture`, an import used before the imports end, a computed path and paths outside the project or missing from
  it (left as code), and both helpers taken out at once; replaced pictures served by hash (an unusual name or a
  file that isn't its hash refused).
- Go (`internal/remotion/pictures_test.go`): what a render draws (the source's, a clip's own over them, only names
  its composition draws), keys and props; the checks of `remotion.pictures.set` (unapproved, unknown or not-yet
  names, twice, a font, a file over the network, too many pixels) and the work queued with the picture's value; the
  list; setup again for what isn't a setting; a version read by an earlier build read once (one job, never from
  Claude's channel); a change recorded with no new version and the latest cut's clip made again, then back to the
  project's own. `internal/cli` (`remotion pictures list` speaks plainly; every op has a
  command), `internal/assistant` (the panel reads `remotion pictures …` commands).
- `scripts/remotion-e2e.sh` (Pictures): the offer, its version approved, a picture uploaded with Replace, checked
  (stills load), used — the Title clip in the cut made again with it — then the CLI's list and `--original`;
  desktop light and dark and a phone.
