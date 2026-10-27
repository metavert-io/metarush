# Resources, project links and nested cuts

Logos, pictures, SVGs, PDFs and fonts live in a library next to footage, with
GitHub repositories pinned at a commit and dated web page snapshots; MetaRush makes code slides, diagrams and
repository cards from them, and vetted slide templates show them. Projects can use each other's footage,
resources, approved motion graphics and cuts by reference, and a cut can play another project's cut inside it.
Everything here is the same ops on every surface: the dashboard, the CLI (`--json` everywhere) and the skills.

## What a resource is

| | |
|---|---|
| Kinds | `image` (PNG, JPEG, WebP; HEIC and AVIF photos are converted to JPEG), `svg` (cleaned: no scripts, no event handlers, nothing loaded from elsewhere), `pdf` (each page also kept as a picture — poppler, optional; `metarush doctor` checks it), `font` (TTF, OTF, WOFF2; family and style read from the file), `repo` (a GitHub repository at a commit, below), `website` (a dated web page snapshot, below) |
| Identity | `res_…` ID, a **short name** cuts use (`logo`, lowercase, unique where it is seen), a title, tags, a description, a licence note |
| Versions | every version is its file's SHA-256 in the media store (content-addressed, never stored twice), with size, dimensions, transparency, main colours, a thumbnail, page pictures, and where it came from (`source`: file, linked folder, URL, repository, project, generated + lineage). `current` is the version new uses pick |
| Scope | a project's own, or the **workspace library** (every project sees it). "Promote to workspace" (`resources promote`) moves one up; the brand kit promotes what it uses |
| Tags | a person's tags, plus suggestions with a reason (`autoTags`: "logo" for a file called logo or a small transparent picture with few colours, "screenshot" for screen-sized pictures, "icon", "diagram"…). Keeping one makes it a tag; rejecting one stops MetaRush suggesting it |
| Removing | `resources remove` hides it from the library; cuts that pin it keep rendering it (removing is reversible: `--restore`) |

Adding a file whose name (or the short name asked for, `--name`) matches a resource already there is a **new version** of it; the same bytes
again are "already there".

## Adding things: one classifier

`add.route` decides by content, not extension, and every entry point uses it: the dashboard's Add box (Home, a
project's Sources and Resources tabs), a drop or ⌘V anywhere in the dashboard, and `metarush add`.

| What | Where it goes |
|---|---|
| video, audio | footage: the project's Sources, or the Inbox (an animated GIF is footage) |
| pictures, SVGs, PDFs, fonts | the project's resources, or the workspace library from Home |
| a folder (CLI, or "Choose a folder" in a project) | a **linked folder** of that project (`--copy` adds its files once instead). Dropping a folder in a browser adds its files once: browsers don't reveal its path |
| YouTube, TikTok, X and other video links | the link import (`ingest.url`), into the project |
| GitHub repositories | a **repository** resource pinned at its current commit (a fetch job on this machine, below) |
| web pages; a picture's or PDF's address | a **web page snapshot** (a fetch job, below); a picture's address downloads the picture as a resource with its address as the source |
| a link that could be either | asked once: "Import the video" or "It's a page" |

Uploads from the browser go through the normal upload pipeline (precheck, resumable upload, ingest), which hands
pictures to `resources.AddFile`; the tray says "Added to Resources as “logo”".

## Linked folders

A linked folder has a shared record (collection `linked_folders`: name, project or workspace, upload policy, how it
stands on each machine) and a **path per machine** in that machine's `config.yaml` (`resources.folders`). The daemon's
folder manager (`intake.FolderManager`) looks through each folder every few seconds: a file must stop changing first
(it shows as "arriving"), a new file is added, a changed file becomes a new version of the same resource, a file that
goes away leaves its resource marked **missing from folder**. A file that can't be added (say its project was
cleared out) is tried again by itself after a minute, five minutes, half an hour, then every six hours while its size
and modification time stay the same (nothing is read or hashed meanwhile, `failed` in the machine's
`<home>/state/folders/<id>.json`); a change to it is tried as soon as it stops changing, and **Look again**
(`resources folder <name> --rescan`) tries every such file at once. Its latest problem stays in the folder's status
(`errors`, `failed`, `retryAt`; "1 file couldn't be added; MetaRush tries it again in about 5 minutes") until it is
added or goes away. Footage in a linked folder becomes sources of the project. Upload policy (`--upload`): `small` (default: files up to `resources.uploadSmallMB`, 25 MB, at once; larger
ones when a cut first uses them), `all`, `on-use`. A version only one machine has is uploaded by a `resource-upload`
job pinned to that machine; a render that needs it waits for it ("waiting for … to be uploaded"). On another machine
the folder shows "Linked on another machine — choose where it is here" (`resources folder <name> --path …`, or
"Find it on this computer").

### Google Drive folders without syncing them

A Google Drive folder synced to this computer (Google Drive for desktop) is an ordinary linked folder. One that
isn't synced can still feed **recipe runs**: a recipe's pictures or footage input names it as rclone does
(`gdrive:Teaser/Art`), and each run lists it with the person's own rclone and fetches only the files it uses — see
[recipes.md](recipes.md#a-google-drive-folder-as-an-input). A picture fetched that way is a picture of its own in
the workspace library, tied to its Drive file (`driveFile`: its Drive ID, the folder, its name, Google's checksum), so
new contents of the same file are a new version of the same picture (migration 42 keeps one picture per Drive file);
its version's `source` is `drive` with the file's address. Footage fetched that way is an ordinary clip whose source
says Google Drive and its Drive ID. `metarush doctor` says whether rclone and a Google Drive remote are set up.

## Content Settings

What every video made with them uses — the **brand** (with its five named logos), the **spelling list**, the
**narration voice**, **house rules**, the **house style** and **house sign-offs** — is a record that belongs to a
project (`content_settings`, `cnt_…`). Each project uses one of:

| Uses | What it gets | Edit it |
|---|---|---|
| **the default** (new projects start here) | the settings of the project whose "Use for new projects" is ticked (`content_defaults`; one per workspace, any member may tick it, ticking another moves it) | there |
| **another project's** | that project's settings, live: changes there show up here; through chains (A follows B, B follows C: C's); a choice that would go round in a circle is refused | there |
| **its own** | its own record: empty, or a copy of what it used; following another later sets it aside, and "its own" brings it back | here |

A project that uses another's shows them read-only: "From fernbrook — edit them there, or give this project its own";
changing settings other projects use says "Also used by 3 projects" (followers counted through chains). A followed
project that is gone sends its followers back to the default and tells the people involved (a notification). Footage
in no project uses the default's spelling list; a clip in several projects uses all of their lists.

The parts are registered once (`internal/content` `Register`: a name, a label, when it is empty, a short summary, how
to copy it, what to check), so following, copies and the project's Settings tab work for every part the same way.

| Part | Where it is kept | Changed with |
|---|---|---|
| Brand | `brandKit` (below; its five named logos too) | `brand.set` (`metarush brand set [--project]`), `content.logo.set|remove` (`metarush content logo`) |
| Spelling | `spelling_entries` with the owning project | `transcript.spelling.add|remove` |
| Narration voice | `voice.elevenlabs`: the voice, model, voice settings, region, favourite voices — never a budget | `provider.settings` (`metarush providers settings elevenlabs [--project]`) |
| House rules | `houseRules` (a project's own rules stay on the project) | `content.rules.add|remove` (`metarush content rules`) |
| House style | `style` (below): what new cuts start with | `content.style.set|apply` (`metarush content style`) |
| House sign-offs | `signoffs`; whether each was given is the project's (`Project.houseSignoffs`) | `content.signoffs.add|remove` (`metarush content signoffs`) |
| Layout pack | `pack` (and a proposed `packDraft`): the graphics and call layouts every project using the settings draws with ([layout-packs.md](layout-packs.md)) | `content.pack.set|propose|accept|decline|apply` (`metarush content pack`) |

Every part's op names a project with its own settings, or none for the default's (the answer says "These are
fernbrook's settings, which new projects use."); a project that uses another's is refused (`E_CONFLICT`, with the
command to give it its own). `metarush content show|follow|own|default --project <p>` (ops `content.show`,
`content.follow`, `content.own`, `content.setDefault`) choose and show; `--json` everywhere.

**How changes reach existing work.** New cuts, transcripts and narration lines use a change at once. Saved versions
never change: after a brand change `brand.set` lists the latest cuts of the projects using those settings that don't
have it yet (`latestCuts`), and the dashboard offers "Use the new brand on the latest cut" for each (a `brand.apply`,
a checked change); a spelling added with "Fix existing transcripts too" corrects every clip using the list
(`transcript.spelling.apply --settings-of <project>` does it later).

**In the dashboard** each project's **Settings** tab (it was "What it keeps"; `?tab=memory` still opens it) shows
Content Settings — Uses (the default / another project's / its own), "Use for new projects", Brand (with its five
named logos), Spelling, Narration voice, House rules, House style, House sign-offs, "Also used by N projects" — and,
below, This project (brief, its own rules, sign-offs — the house's among them, marked with whose settings they come
from — and what it keeps). Settings → Brand and Settings → Footage → Spelling are gone: `/settings/brand` and
`/settings/content` open the default's (a short page to choose a project when there is none).

**Migration 36** moved today's data so nothing anyone sees changed: the workspace's brand kit, spelling list and voice
choices became the settings of the most recently active project that used them (now the default); projects with a
kit, spellings or voice choices of their own got their own settings with exactly what they used (their kit or the
workspace's, the workspace's spellings and theirs, the workspace's voice with theirs over it); the others use the
default. Budgets and project rules stayed where they were. The named logos, house style and house sign-offs
needed no migration: a kit's logo is its main logo and its logo for dark backgrounds stays that.

### The house style

What new cuts of every project using the settings start with (`style`, `editspec.HouseStyle`). Nothing set: new cuts
get MetaRush's own choices, exactly as before.

| Part | Choices | New cuts | The latest cut ("Use the house style") |
|---|---|---|---|
| Captions | on or off; look (clean, emphasis, boxed); size (small, medium, large); position (bottom, lower, middle, top) | on: captions with the look, size and position; a cut's own captions keep what they set | turned on or off, and given the look, size and position |
| Name bar | look (a band across the picture, or a tab as wide as the names); height (full, or about half as tall — never shorter than the printed names it covers); animation (names fade in, slide in, or never move) | every `name.bar` without its own look, height or animation; `metarush cut names` writes its bar that way | every name bar that isn't locked |
| Name titles | clean or cinematic | lower thirds switch to the house look when their length fits it | the same |
| Framing | faces, follow, fixed (the middle) | `output.framing` | the same |
| Sound at cuts | the blend at straight cuts (seconds); voices evened out or left as recorded | `mix.cutBlend`, `mix.levelSpeech` | the same (an older cut's sound changes only when the style says so) |
| Intro card | title card or title slide, its seconds, its words (default: the cut's title) | added at the start when the cut has no opening card, clear of other cards | added when the cut has none |
| End card | end card or title slide, its seconds, a headline (needed), a line under it, a call to action, a handle | added at the end when the cut has none, clear of other cards | added when the cut has none |

`metarush content style show|set|apply [--project <p>]` (ops `content.style.show|set|apply`): `set` changes one
thing at a time (`--captions on --caption-size large`, `--name-bar-height half --name-bar-animates never`,
`--name-titles cinematic`, `--framing fixed`, `--cut-blend 0.15`, `--level-speech off`, `--intro title.card
[--intro-title …]`, `--end-card endcard.cta --end-card-headline "…" [--end-card-cta … --end-card-handle …]`; an empty
value or `--clear <part>` goes back to MetaRush's own) and answers the latest cuts that don't follow it, each with what
would change; `apply` is one checked change (`--dry-run`, then `--confirm <token>`; the dashboard saves at once).
Claude's context at the start of every turn says the style ("House style from “fernbrook”'s settings …"), and the quality
check's `house_style` measures what it can: captions missing when the style says on, a name bar that animates when it
says never, a name bar taller than it says — each with the change that fixes it — and notes graphics that don't follow
the [layout pack](layout-packs.md) (`pack_role`, `pack_override`).

### House sign-offs

OKs every project using the settings needs before a video goes out ("Legal OKs it before export"; `signoffs`,
`metarush content signoffs list|add|remove`). A cut's sign-offs (`metarush cut signoffs`, the project's Sign-offs)
are its own, then the house's, marked with whose settings they come from; ticking a house one (`cut signoffs done`)
records the OK for that project's cut only (`Project.houseSignoffs`), taking one off a cut is refused (it is the
settings'), and taking it out of the settings takes the OKs given for it too. The reminders before approving,
exporting and delivering read both, and never lock. See [keeping.md](keeping.md#sign-offs).

## The brand kit

Part of Content Settings (`brandKit`): five named logos — `logo` (**Main**), `logoDark` (**For dark
backgrounds**), `logoLight` (**For light backgrounds**), `logoIcon` (**Square icon**) and `logoWordmark`
(**Wordmark**) — `primary`, `accent`, `text`, `background` (taken from the main logo — its main colours — until a
person changes one; `--from-logo` takes them again), `font` (a bundled one) or `fontResource` (a font file).
`brand.show` (its `logos`: each with its label and the name cuts use) / `brand.set` / `brand.apply`, the Brand part of
a project's Settings tab. Each logo is uploaded right there or chosen from the resources (`content.logo.set|remove`,
`metarush content logo set <main|dark|light|icon|wordmark> <file or resource> [--project]`): a file goes through the
ordinary pipeline into the resources of the project whose settings hold the brand — versioned, content-addressed,
an SVG cleaned, a PDF's pages kept as pictures (a PDF logo shows its first page) — and every project using the
settings may show it.

A new cut (a full spec with no base) starts with the brand of the settings its project uses: its colours and font,
pins `logo`, `logo-dark`, `logo-light`, `logo-icon`, `logo-wordmark` and `brand-font`, and names the first three in
its brand (`brand.logo`, `logoDark`, `logoLight`). **Logos made for their background**: when a cut's brand names its
main logo, every title card, end card, lower third, name bar and title slide whose `logo` is the main one shows the
logo made for the colour it draws the logo on (`brand.background`: a colour white stands out on more than black
gets the logo for dark backgrounds, else the one for light backgrounds), and the main one when the cut has no such
logo; Go makes the choice in the plan (`editspec.PickLogo`), so the preview, the render and the quality check
agree. `logo.corner` sits on the footage and keeps the main logo; name `logo-icon` or `logo-wordmark` in a graphic to
show them. Cuts made before (no `brand.logo`) show exactly what they name. `brand apply <project>` puts the brand —
logos included — on an existing cut as a checked change, and after a change to the brand or a logo the latest cuts
that don't have it are offered it. When the project whose settings hold the brand is cleared out for good, its
logo files move to the workspace library with its other pictures, so the cuts that pin them keep showing them; the
projects that used its settings use the default ones again, and its house style and house sign-offs go with its
settings.
The brand's resources stay where they are (a project's own logo stays in it); projects using the settings may pin
them all the same, and a resource of another project chosen for a brand moves to the workspace library.

## Cuts that use resources

`spec.resources` pins short names to exact versions (`{id, sha256}`); template parameters marked
`format: metarush-resource` name them (`title.card` logo/image, the lower thirds' logo, `endcard.cta` logo/image,
`logo.corner`). Cut sheets show the pins under `resources:` and pin a name for you when a sheet is checked. A newer
version never changes an approved cut: `metarush cut update` (an explicit, checked change) moves pins to the latest.
See [edit-schema.md](edit-schema.md) and [cut-sheet.md](cut-sheet.md).

**Rendering.** Go puts every file the plan draws in `RenderPlan.resourceSources` (SHA-256 → URL). The dashboard
preview uses `/media/resources/file/<sha>` (same origin, local-first: the cache, a linked folder on this machine, or
the media store). A render's worker fetches the pinned files into its cache (`renderResources`) and serves them from
the same loopback media server as the footage (`r-<sha>`, with their real content type; an SVG is served with a
sandboxing CSP), which the network jail allows. Remotion draws pictures with `Img` (the frame waits for them) and
loads font files as data with `FontFace` (`useBrandFontFile`), so the render page's CSP is unchanged.

**Credits.** QA notes pictures from the web whose licence isn't recorded (information only, never blocking).
Delivery sidecars list every resource version a video shows with where it came from (those shown through a nested
cut, at any depth, carry `in`: that cut's project), list the nested cuts, and credit pictures from the web and
outside footage inside nested cuts. QA and the sidecar read the same list (`RenderExtras.ShownResources`).

## Project links

`project_links`: project A uses project B — **everything** by default (footage, resources, approved motion graphics,
its cut; new things appear by themselves), or `only` a filter (kinds, tags, people in the footage, linked folders;
AND across fields, OR within one) or `items` (picked things). Links are not transitive. Nothing is copied: A sees B's
assets and resources with the same hashes, marked `from: "B"` (`project.show` assets, `resources.list`, search, the
Claude Code panel's context: "12 resources here, plus 40 from Brand"). `project.unlink` keeps whatever A's cuts
already use (`kept`), and says so. CLI: `metarush project link <this> <other> [--only kind=resources] [--items …]`,
`project use`, `project unlink`, `project links`.

A link's source can also be a **collection**: a named shelf of footage from the Library. It works like a
project that has footage and nothing else: `metarush project link "Spring promo" B-roll` brings in everything on it
(or `--items`, or a tag or person filter), marked "from B-roll"; clips put on it later show up too. A name that is
both a project and a collection needs the ID.

## Nested cuts and copy-in

A clip whose `assetId` is `cut:<project>/<revision>` plays that revision of another project's cut. `editspec`
resolves it (`withNested`, `nestFacts`): to the timeline it is footage as long as that cut, whose "transcript" is the
speech it plays; `Resolve` adds `RenderPlan.nested[key]` (the inner cut resolved at this cut's size and frame rate)
and flattens the inner sound into this plan's `audio` (envelopes composed with the outer clip's), so Go stays the
only mixer. Remotion draws the inner plan inside the clip (`NestedPicture` in `ClipLayer`), silent, with its
overlays, captions (unless the outer cut shows its own), brand and resources. Cycles and depth > 3 are refused; a
newer revision is reported (`nested_newer`, "a newer version is available" in the dashboard and sheet) and
`cut update` moves the pin. `cut copy-in` instead copies the inner cut's clips, transitions, graphics and narration
into this cut as a starting point (no live link).

## Moving, removing and deleting footage

- `asset.move` (`asset move <asset> --from <project> --to <project>`): refused while the source project's current
  cut uses it.
- Removing from a project (`asset link --unlink`, "Remove from this project") never deletes: footage in no project
  is in the Inbox.
- `asset.delete` (`asset delete <asset> --forever`, "Delete…"): `HumanOnly`; the CLI refuses Claude Code and
  scripts (it asks a person to type "delete" in a terminal). Refused while another project has it or any version of
  any cut uses it (the refusal says where). Deletes the asset and everything derived from it (transcript, voices
  and speaker runs, analysis including what's on screen, face tracks, questions, upload records, notifications), cancels queued work, then each stored file
  unless anything else references its hash (assets and variants, resources, renders, QA frames, motion graphics).
- `asset.where` / `resources.where` / `cut.where`: every place it is used, as rows the dashboard links (a project's
  Sources tab, a cut's version, the brand kit; for footage also its collections, the Archive and the Trash).
- The Library adds two gentler steps before `asset.delete`: **Archive** (`asset.archive`, out of the Inbox
  without a project) and **Move to Trash** (`asset.trash`, undone with `asset.restore`). Deleting what is in the
  Trash (`trash.empty`) is people only and uses the same checks as `asset.delete` (`deleteCheck`).

## GitHub repositories

`metarush resources add https://github.com/acme/widget --project <prj> [--ref <branch|tag|commit>] --wait` (or a
paste in the Add box) queues a **fetch job** pinned to this machine: its network and the person's own sign-in.

| | |
|---|---|
| The commit | `git ls-remote` resolves the branch, tag or default branch to a commit; a `/tree/<ref>`, `/blob/<ref>/…`, `/commit/<sha>` or `/releases/tag/<tag>` address names the ref |
| The fetch | a shallow fetch of that commit into a fresh bare repository, with no prompts (the person's credential helper answers, or the fetch fails with "sign in with gh auth login, then gh auth setup-git"), hooks off (theirs and the repository's), https only. MetaRush never asks for, reads or stores a token |
| The snapshot | the commit's blobs read raw (`ls-tree` + `cat-file`: no filter, attribute, hook or LFS smudge runs), **text files only**: pictures and other binaries, Git LFS pointers, links, submodules, odd names and files over `resources.repos.fileKB` (512 KB) are left out and listed; it stops at `totalMB` (64) or `files` (20,000). A canonical tar.gz (sorted paths, the commit's time, no owners) in the media store: the same commit is the same version. The pictures in folders you name are kept too, as pictures of their own ([below](#pictures-from-a-repository)) |
| The overview | README (its start on the version; the whole file in the overview), top-level folders, languages by size (GitHub's figures through `gh api` when it answers), licence (GitHub's, or read from LICENSE), description, stars, recent commits and contributors (`gh api` with the person's `gh` sign-in, else read from a deeper blobless fetch: `history` says which). `metarush resources show <repo> --json` → `repo`, plus `made`: what was made from it |
| Reading it | `metarush resources path <repo>` unpacks a **read-only copy** (files 0444) under the cache and prints its folder. Claude reads it with Read, Grep and Glob; nothing in it ever runs |
| Updating | `metarush resources refresh <repo> [--ref …] --wait` pins the newest commit as a new version and makes new versions of what was made from it (below); cuts keep theirs until `metarush cut update` |

A repository is never drawn itself (`resource_kind` if a graphic names it): cuts show what is made from it.

### Pictures from a repository

Art kept in git — a folder of character art, the logos in `brand/` — can be used like any picture. A repository keeps
the pictures under the folders (or files) you name, its **picture paths** (`Resource.picturePaths`, at most 20; `.`
is the whole repository):

```bash
metarush resources add https://github.com/acme/widget --project "Teaser" --pictures art/characters --wait
metarush resources pictures widget --project "Teaser"                          # what it keeps, and what it left out
metarush resources pictures widget --project "Teaser" --keep brand --wait      # another folder's pictures
metarush resources pictures widget --project "Teaser" --drop art/old           # stop keeping one in step
```

| | |
|---|---|
| What is kept | every file under the paths whose **bytes** say PNG, JPEG, WebP, GIF or SVG (a name says nothing: `hero` with a PNG's bytes is kept, `notes.png` that is text is not). SVGs are cleaned like any SVG (`changes` says what went; the original's hash is `originalSha256`) |
| Left out, with the reason | Git LFS pointers (never downloaded), links, submodules, odd names, animated GIFs (footage), HEIC/AVIF, PDFs, fonts and other files that aren't pictures, files larger than `resources.repos.pictureMB` (25 MB), and what passes `picturesMB` (250 MB) or `pictures` (250) in one fetch. Text files under a path stay the snapshot's and aren't mentioned |
| Each picture | a picture resource of its own **next to the repository** (its project, or the workspace library), named after it and the file (`widget-alice`, numbered when taken), tied to its file (`repoFile`: the repository and the path). Its version's `source` says exactly where it came from — `repo@commit:path` (`github.com/acme/widget@1a2b3c4d:art/characters/alice.png`), with the repository's licence, and its lineage is the repository version. The same bytes in two files are two pictures; nothing is stored twice |
| A newer commit | `resources refresh` keeps the pictures again: a changed file is a **new version of the same picture**, a new file a new picture, and a picture whose file went away is marked **missing** ("no longer in the repository"), never removed — cuts keep the versions they pin until `cut update`. A picture a person removed stays removed |
| What a version kept | each repository version lists what it kept (`repo.pictures`: path, picture, version, size), what it left out (`picturesLeftOut`, the first 100, and `picturesSkippedWhy`) and the paths it looked under; `resources show` (its overview's `pictures`) and `resources pictures [--path <folder>] [--version <commit>]` read it |
| Changing the paths | `resources pictures --keep` brings the new folders' pictures in **at the commit the repository shows** (a fetch for its pictures only: no new version of its files); a wider folder takes the place of the folders it holds. `--drop` stops keeping a folder in step: its pictures stay in the library, and cuts that show them keep them |

Nothing from a repository runs: the pictures are blobs read raw, sniffed, decoded by Go's image readers (sizes and
colours; huge pictures aren't decoded) or cleaned as SVG, and stored by their SHA-256. A repository promoted to the
workspace library takes its pictures along. Delivery notes credit a picture from a repository with its provenance, and
the quality check notes one whose licence isn't recorded (information only).

**In the dashboard** the repository's details show **Pictures it keeps**: the folders (× stops keeping one, for the
project that owns it), the pictures at its commit, the files left out and why, and **Keep its pictures** for another
folder. **For Claude**, `resources show <repo> --json` has the same (`repo.pictures`); `resources pictures … --keep`
and `resources add … --pictures` are cards the person confirms (Auto runs them). A recipe's pictures input can take
its pictures from such a folder ([recipes.md](recipes.md#pictures-from-a-repository-path)).

## Web page snapshots

`metarush resources add https://acme.com --project <prj> [--depth 1 --max-pages 5] --wait` (or a paste; a link
that could be a video asks first) queues a fetch job that runs `render/snapshot.mjs` on this machine:

| | |
|---|---|
| The browser | Remotion's headless Chromium with a fresh, empty profile (signed out), **with** network access but only through MetaRush's filtering proxy (`render/web/proxy.mjs`): every name is resolved by the proxy, addresses on this computer or a local network (loopback, private, link-local, carrier-grade NAT, multicast) are refused, and the proxy connects to the address it checked. Plain-http WebSocket upgrades, downloads and dialogs are refused; a byte budget, a time limit per page (`resources.pages.timeoutSec`, 45 s) and a total limit stop a page that never ends |
| What it keeps | the page on a computer (1440×900 at 2×), on a phone (390×844 at 3×, a phone's user agent) and the **whole page** (scrolled first so lazy pictures load; at most `fullHeightK` thousand pixels tall), its readable text (searchable; the main article when there is one), title, description, site name, theme colour, language, preview picture (og:image) and icon |
| The resource | kind `website`: its file is the computer picture; its page pictures are #1 computer, #2 phone, #3 whole page (`homepage#2` in a graphic). Named after the site and path (`acme-com-pricing`), titled after the page |
| Depth 1 | also the same-site pages the first page links to (navigation links first; sign-in, account, cart and file links skipped), `--max-pages` in all (default 5, at most 20); each is its own resource |
| Dated | every snapshot is a version with its date; `metarush resources refresh <page>` takes a new one. The address of a picture or PDF downloads it instead (a normal resource whose source is the address) |

### Logo, colours and fonts from a website
A snapshot used to keep the page's icon and preview picture only as notes on the snapshot, and its colours as the
screenshot's main colours; a person who wanted the logo on a title card had to find the file themselves, and graphics
kept MetaRush's own colours. Now the snapshot also reads the page's brand (`render/snapshot.mjs`, in the same filtering
browser) and keeps it:

- **Logos and icons as their own pictures.** Candidates (`render/web/brand.mjs`, read in the page): inline SVGs and
  pictures in the header or navigation, pictures whose address, `alt`, class, id or label says "logo" (or brand,
  wordmark) or the site's name, the home link's picture, CSS background pictures on elements called logo,
  `apple-touch-icon`, `rel=icon` (SVG kept, `.ico` left out), `mask-icon` and the preview picture (og:image). They are
  ordered by how logo-like they are — points for saying "logo" or the site's name, linking home, the header or
  navigation, the top left, a logo's size and shape, a see-through background and few colours (read from the
  picture's own pixels when the page may); fewer in a button (a menu icon), in a strip of other companies' logos
  ("partners", "clients", "trusted by"), far down the page, tiny or photo-sized; the page's own icons come after real
  logos, larger first, and the preview picture last. The first three that download (one per picture: the same bytes
  under another address count once) are kept as picture resources of the same scope as the page, named after the
  site (`fernbrook-example-logo`, `fernbrook-example-logo-2`, `fernbrook-example-icon`; an og:image is `…-preview`), titled with the site's
  name ("<site> logo"), suggested tags `logo` or `icon` (from the page), with source kind `url`: the picture's own address (a logo drawn in
  the page: the page's), the page's site, and the page's version as lineage. The page snapshot lists them as
  `site.brand.logos` (with how logo-like each is, why, and the colour behind it on the page). Inline SVGs are
  written out with the page's CSS on their shapes (colours, `currentColor`, the symbols they use) and cleaned like any
  SVG; pictures are fetched with the proxy's rules (5 MB each, no redirects). The same picture again is the same
  version; a changed logo is a new version of the same resource; a picture a person removed stays removed.
- **Colours** (`site.brand.colors`, each with `from` and a weight): the page's `theme-color` (and its `mask-icon`
  colour), its CSS custom properties that name colours (`--primary`, `--brand…`, `--accent…`, `--color-…`, `--…-color`;
  `--brand-accent` counts as an accent), the colours that cover the most of the header, the buttons, the links and the
  first screen, weighted by area, the page's background and text, and the main colours of the most logo-like picture
  (`from: logo`). One entry per colour (weights added, the strongest source kept); the page's declared colours first.
- **Fonts** (`site.brand.fonts`): the first real family of the headings, the text and the navigation (`h1`, `body`,
  `h2`, `nav`; system and generic fonts skipped), Google Fonts links (`google: true`) and the other families the page
  loaded (`document.fonts`). A Google font's regular weight (upright, nearest 400, the Latin subset; WOFF2 from
  fonts.gstatic.com) is kept as a font resource (`fernbrook-example-font`, licence note "Google Fonts (open licence)") — only
  when the page itself loaded its stylesheet; a site's own font is only named (its licence is unknown).
- **A brand kit suggestion**: `metarush brand suggest --from <page> [--project <p>]` (op `brand.suggest`; without
  `--from`, the project's newest snapshot that found a brand) proposes, each with a plain reason (`why`): the logo (the
  most logo-like picture; an icon only when the page shows no logo), the four colours and the font, the same way every
  time. **Primary**: the most saturated, most used brand colour — the page's declared colours first (theme colour, its
  CSS colour names), then the logo's, then the rest of the page. **Background**: the neutral the logo sits on (the
  header's or the page's), else the header's or the page's own neutral, else its darkest. **Text**: the page's text
  colours first, readable on the background (contrast at least 4.5), else near-white or near-black. **Accent**: a
  second brand colour of another hue that stands out on the background (contrast at least 3), else the primary again.
  **Font**: the kept Google font file (`fontResource`), with the nearest bundled font as `font`; else the nearest
  bundled font (Inter, Fraunces or JetBrains Mono) to the headings' family. A page in another project can be used once
  the projects are linked (its logo and font live there).
- **Taking it in one step**: `metarush brand set --project <p> --from-site <page> [--apply]` (op `brand.set` with
  `fromSite`, which needs a project: a site's kit is a project's own). The site's colours count as chosen (a new logo
  keeps them; Take colours from the logo takes the logo's again). `--apply` also puts the kit on the project's latest
  cut through the same check as `brand apply` (a new version): outside the dashboard nothing is saved — the kit
  included — until the same command runs with the change's confirm token (`--confirm`; a person at a terminal is
  asked instead); `--dry-run` only shows the kit and the change. In the dashboard the page's resource and the Brand
  page show the suggestion ("Suggested from …": the pictures, the four colours, the font, Why these) with **Use for
  this project**, which checks first and asks in a styled dialog when a new version of the cut would be saved; in
  the Claude panel it is a "Keep this logo in the sources and the brand kit?" card ([keeping.md](keeping.md)). So the
  cut's graphics — templates and motion graphics alike, which draw `brand` — use the colours.

**One brand per client.** A team with several clients gives each client's projects one set of Content Settings:
one project has its own, the others follow it. `brand show|set --project <p>` and `brand apply <p>` work on
the brand a project uses (`ops.KitFor`): new cuts start with it, motion checks draw with it, and the Brand part
of a project with a website snapshot offers "From its website". `brand show --project` says whose settings hold the
brand (`from`, `readOnly`) and lists the project's website snapshots a kit can be suggested from (`sites`).

The browser extension's **Send this page** adds the page as the person sees it in their own signed-in browser: a
picture of the visible tab and its text, into the workspace library (`/ext/pages`, the `pages` scope; see
[browser-extension.md](browser-extension.md)).

## Slide pictures made from a repository

| | |
|---|---|
| `metarush resources make code <repo> --file <path> --lines 40-72 [--highlight 44-46,60] [--language go] [--theme dark\|light]` | the lines highlighted with Shiki (in Node: the code is data), drawn as a panel with line numbers, the file and lines in a header and the highlighted lines marked in the brand's accent; at most 80 lines, lines over 110 characters shortened |
| `metarush resources make diagram [<repo>] --from architecture.mmd \| --source "…" [--format mermaid\|d2]` | a diagram Claude (or a person) wrote: Mermaid drawn by Mermaid in the page (strict security level, SVG text labels, the brand's colours), or D2 by the `d2` program when installed (`--bundle=false`, a proxy that refuses everything). The SVG is cleaned like any SVG |
| `metarush resources make stats <repo>` | a card: the repository's name, description, languages, files, stars, contributors, licence, commit |

In the dashboard's Claude Code panel the same requests (`resources add`, `resources make`, `resources refresh`) are
action cards the person confirms; Claude reads the repository's read-only copy, writes a diagram's Mermaid in its
proposals folder, and the card lists the diagram's boxes before anything is made.

Code slides and cards are drawn in the same network-jailed headless browser as renders (`render/generate.mjs`: a
page MetaRush writes, its fonts and Mermaid from a loopback server only that run knows). Each is an ordinary
picture (or SVG) resource whose source says exactly where it came from — `repo@commit:path:lines`
(`github.com/acme/widget@1a2b3c4d:internal/queue/queue.go:46-63`) — and how it was made (`source.madeWith`). When the
repository is refreshed, code slides follow their lines to where they moved (a new version; a slide whose code
changed stays, with a note), cards are drawn again, diagrams stay. `--name` of an existing resource adds a new
version of it.

## Slides

Six vetted templates show these (and any picture) in the brand's colours and font, full-frame, in every shape of
video, with plain fields:

| Template | Fields |
|---|---|
| `slide.title` | `title`, `subtitle`, `kicker` (a label above), `logo`, `image` (darkened behind) |
| `slide.bullets` | `title`, `bullets` (1–6, one by one or `reveal: all`), `image` (beside the points) |
| `slide.code` | `title`, `code` (a code slide), `caption`; long code scrolls |
| `slide.architecture` | `title`, `diagram`, `caption` |
| `slide.screenshot` | `title`, `image` (`page`, `page#2`, `page#3`), `frame` (browser, phone, none; default from the picture's shape), `url` (default the page's own), `caption`, `scroll` (default on for a whole page) |
| `slide.quote` | `text`, `attribution`, `role`, `image` |

Cut sheets write them like any graphic (`- loop: slide.code at 0:09 for 6 s · with title "The worker loop", code
"main-loop"`); see [cut-sheet.md](cut-sheet.md).

## Resources in project motion graphics

A component draws resources by name with `useResource` from `@metarush/motion`: a name in quotes
(`useResource("logo")`, `"deck#3"`) or a field whose schema says `format: metarush-resource`
(`z.string().meta({ format: 'metarush-resource' })`, then `useResource(params.shot)`); `<ResourceImage pic={…} />`
draws it (the frame waits for it) and `fitInside` sizes it. The import scan accepts only those two forms and no
new imports. `motion check` looks the names (and the sample parameters' values) up as the project sees them, fails
on a name the project lacks, and draws its test frames with exactly those files, served by hash from loopback. A
cut that uses the component pins the names its code uses (a cut sheet pins them when checked), and renders serve
them like any resource. See [motion-graphics-as-code.md](motion-graphics-as-code.md).

## Tags, words and credits

On a Mac the on-device helper looks at SVGs (drawn as pictures first with `sips`), the first three pages of a PDF
and a page snapshot's computer picture like any picture: tags with why, and the words in them (a page keeps its
own readable text). Delivery sidecars credit page snapshots ("Includes a snapshot of Widget pricing (19 Oct
2026): https://…") and what was made from a repository ("Includes code from github.com/acme/widget@1a2b3c4d:
internal/queue/queue.go:46-63 (MIT): https://…"), with `provenance` on each resource; QA notes, for information
only, pictures from a page or a repository whose licence isn't recorded.

## Extension points

| Hook | For |
|---|---|
| `resources.RegisterTagger(Tagger)` · `RegisterTextFinder(TextFinder)` | more tag suggestions and words found. In use: the visual index (`internal/visual/still.go`, Mac only) for pictures, SVGs, PDF pages and page snapshots; `madeTagger` for what MetaRush made |
| `resources.RegisterURLSource(URLSource)` | addresses that become resources: `github` (repositories) and `website` (pages and pictures) are registered by `internal/resources` |
| `ops.RegisterDeleteHook(name, fn)` | features that keep data per footage remove it when footage is deleted |
| templates family | more slide templates join the catalog the same way (`editspec.Templates`, `video/src/templates/registry.tsx`) |

## Trying it

```bash
metarush add ~/Brand/logo.svg --project "Spring promo"          # → Resources ("logo")
metarush brand set --logo logo                                   # colours from the logo
metarush project link "Spring promo" Brand                       # everything from Brand
metarush cut nest "Spring promo" Brand --dry-run                 # Brand's cut at the end (before a locked ending): shown first
metarush cut show "Spring promo"                                 # resources: and cuts: pins, "cut from brand"

metarush resources add https://github.com/acme/widget --project "Widget tour" --wait   # → "widget" at its commit
metarush resources show widget --details                         # README, languages, recent commits, contributors
metarush resources path widget                                   # a read-only copy for Claude to read
metarush resources make code widget --file internal/queue/queue.go --lines 46-63 --highlight 52-55 --name main-loop
metarush resources make diagram widget --from architecture.mmd --name architecture
metarush resources make stats widget
metarush resources add https://acme.com --project "Widget tour" --depth 1 --wait     # → "acme-com", "acme-com-pricing" … and "acme-com-logo", "acme-com-icon"
metarush brand suggest --project "Widget tour"                    # the kit acme.com suggests: logo, colours, font, and why
metarush brand set --project "Widget tour" --from-site acme-com --apply   # the project's own kit, on its latest cut (asks first)
metarush resources refresh widget --wait                         # newest commit; code slides follow their lines
metarush resources pictures widget --keep art/characters --wait  # the pictures in a folder, each a picture of its own
```
`scripts/resources-e2e.sh` checks the dashboard flows (the Add box, safe drags, the Sources "…" menu, Delete…,
Where it's used, nesting another project's cut) on a seeded throwaway installation and takes screenshots
(`web/scripts/resources-e2e.mjs`); `web/scripts/resources2-e2e.mjs` pastes a
repository and a web page, previews the slides and sends a page from the browser extension; `scripts/acceptance.sh`
runs the CLI flow end to end (a fixture repository and a local site included), render included.
