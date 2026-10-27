# Recipes

A **recipe** is a video that worked, written down so it can be made again: what changes each time (its
**inputs**: new footage, pictures, words, a voice), what stays (its **sections**, graphics, captions, music, the
mix, the look through Content Settings) and the **rules** and **hints** that made it good. A **run** is one making
of it: an ordinary project, or a new cut in the project it was saved from, made through the same steps a person
uses — versions, a review copy, the quality check — and then waiting in **Awaiting your review**. Recipes belong to
the workspace: anyone signed in plans and runs them; only people make a version current, start a run, let a waiting
run spend more, or archive one.

Runs use the estimate and each person's preference for estimates (`internal/costs`, `prefs.EstimatesFirst`) and
pictures as settings (a clip's own pictures on `remotion.use`); the dashboard's Recipes tab is
[below](#the-dashboard). A recipe can be written as [a recipe sheet](#the-recipe-sheet), make
[packs](#packs) and start from [first picks](#first-picks), and its pictures can come from
[a repository path](#pictures-from-a-repository-path) (art kept in git). MetaRush offers
[suggested changes](#suggested-changes) from what people changed in runs,
[plans by itself](#planning-by-itself) when new files arrive or at a weekly time (never starting a run), carries
[recipes between workspaces](#between-workspaces) and takes [a Google Drive folder
as an input](#a-google-drive-folder-as-an-input) without syncing it; in Claude's panel,
keeping or changing first picks, saving a sheet and saving suggested changes are cards too. MetaRush also comes with
[starter recipes](#starter-recipes) — a call becomes an episode with shorts; a release tag becomes a release
video — brought in as drafts, and a run with an expensive kind of work makes [a sample first](#a-sample-first).

## A recipe and its versions

- `recipes` (`rcp_…`): a short name (`app-teaser`, unique in the workspace), a title, what it makes ("one vertical
  video, 25–35 s"), the **current** version (what runs make), the newest **draft**, an example render, the last run,
  and whether it is archived.
- `recipe_versions` (`<workspace>/<sha256>`): the document (`metarush.recipe/1`, the Go struct
  `models.RecipeDoc`), whose canonical JSON's SHA-256 is the version; numbered with a parent, a note, who and when;
  its last check; **draft**, **current** or **earlier**. Data only: template IDs with checked parameters, approved
  graphics and Remotion versions by hash, resource pins — never code.
- `recipe_runs` (`rrn_…`): see [Runs](#runs).

The document holds:

| Part | What it is |
|---|---|
| `title`, `makes`, `length` | names and how long the video should be |
| `lands` | where runs go: `project` (new cuts in the project it was saved from) or `new-project` (a project per run, named from a pattern with `{title}`, `{n}`, `{date}` and inputs) |
| `settings` | the project whose Content Settings new projects follow (brand, spelling, the narration voice, house rules) |
| `inputs` | typed: `words`, `choice`, `footage`, `pictures`, `voice` (a voice with none of its own reads in the narration voice the Content Settings name); each with a default (what the saved video used), limits (the graphic field's longest words, how many pictures) and where values come from by default (a linked folder or a collection: a run takes the newest; a repository's folder, `from: {repo, path}`: the pictures it keeps there, [more](#pictures-from-a-repository-path); a Google Drive folder, `from: {drive: "gdrive:Teaser/Art"}`: its newest files, fetched by the run, [more](#a-google-drive-folder-as-an-input)) |
| `sections` | in order: **fixed** (built by Go and locked: Remotion compositions, cards, intros, footage that stays) or **chosen** (Claude picks moments from a footage input by the section's purpose, count and length; the moments of the saved video are kept as its first picks) |
| `cut` | everything else the cut has: frame, music, captions, graphics (following their clips through spans), narration, mix, brand, pins |
| `bindings` | where each input goes: `clip:<clip>`, `param:<graphic>.<field>`, `resource:<name>`, `words:<clip>.<KEY>` (a Remotion word setting), `pictures:<clip>.<NAME>` (a Remotion picture setting), `line:<line>`, `prompt:<clip>` (a generated shot made again), `voice`, `title` |
| `rules`, `signoffs`, `hints` | the run project's rules (measured by the quality check where they can be; the others become sign-offs a person ticks), the OKs its cut needs, and hints that guide Claude |
| `pack` | the other videos every run makes: shorts of a conversation the video plays, each its own project — or of a footage input, each a new cut of the run's project |

## Making a recipe

**From a project** (`recipe save --from <project>`, "Save as a recipe…"): MetaRush reads the cut's newest approved
version (else its newest) and **proposes** what varies — the project's own footage (its runs become chosen
sections), words in graphics (headlines, titles, names; a call to action, a handle and a source stay), word and
picture settings of clips made from code, pictures that aren't the brand's (grouped by linked folder, a picture
setting showing a folder's picture joining that folder's input), the AI voice — and what stays.
`recipe propose` (read-only) shows the proposal with the key each item moves by; `--vary`, `--keep`, `--fixed` and
`--chosen` move items between the lists; narration lines, the title and a generated shot's words can be varied too.
Graphics are pinned to the clips they sit on (spans) and lines placed at a clock time to their clip, so they stay
with their section when chosen moments change its length. A person's save is current at once; Claude's is a draft.

**Changing it** (`recipe update`): a merge patch (RFC 7386) over a version's document (`recipe show --doc`), a new
title, hints or rules to add — always a **draft**, with the version it came from as its parent. Only a person makes a
version current (`recipe publish`); the answer names rules the new version no longer keeps. Or as its sheet
([below](#the-recipe-sheet)): `recipe checkout`, edit, `recipe check <file>`, `--save` — a draft too.

**Writing one from scratch**: a sheet with a new `name:` and no `base:` saves as a new recipe, a draft.

## The recipe sheet

A recipe version as Markdown people and Claude read, write, review and keep in git: `metarush recipe checkout
<recipe> [file]` (`recipe.checkout`, read-only) writes it (default `<name>.recipe.md`; `-` prints it). Printing then
reading a sheet gives exactly the same version — the same SHA-256 — for every version MetaRush saves (versions are
saved normalized, `NormalizeDoc`: the cut's defaults and rounding).

```markdown
---
recipe: 1
name: app-teaser
base: 3f9a…   # version 2, current                 the version it was written from
title: App teaser
makes: one widescreen video, 7–11 s
length: 7–11 s
lands: new cuts in prj_…   # “App teaser”        or: a new project each time · named "{title} {n}"
settings: prj_…   # “App teaser”                  whose Content Settings runs follow
from: project prj_… · cut main · version 4 · revision rev_… · approved
---

> One widescreen video, 7–11 s; each run makes a new cut in “App teaser”.      (lines with > are notes, ignored)

## Inputs
- footage: footage · items ast_…, ast_… · count at least 1 · label "The footage"
- headline: words · default "Read free" · up to 40 characters · label "The headline on the end card"
- hero: pictures · items res_… · count 1 · from folder fld_… · label "The pictures from “Character art”"
- voice: a voice · default "Ada" · items voice-… · label "The AI voice"

## Sections
1. brand: fixed · clips brand · purpose "Brand intro"
2. moments: chosen · from footage · count 1–3 · length 4–8 s · clips hook, answer · purpose "the hook; the answer"

## Pack
- shorts: shorts · count 1–3 · length 15–60 s · shape vertical · named "{title} — short {k}"

## Bindings
- headline → param:end.headline

## Rules
- Under 35 seconds · measured max-length:35

## Sign-offs / ## Hints
- Name the app in the first two seconds.

## The cut

---
cut: 1
… (the rest: an ordinary cut sheet — clips, graphics, narration, sound, captions, the mix)
```

- Every item is `- name: kind · clause · clause` (sections numbered); words go in JSON "quotes"; ranges are `1–3`,
  `2` (exactly), `up to 3`, `at least 1`, `any` (seconds: `15–60 s`); numbers are written exactly. Plain words stay
  plain where they read back the same (titles, rules, hints); anything else is quoted.
- Inputs take `default`, `items`, `required`, `up to N characters`, `choices`, `count`, `from folder|collection|project|repo|path`
  (`from nowhere`), `label`; sections `from`, `count`, `length`, `clips` (their clips in the cut, in order) and
  `purpose`; the pack `count`, `length`, `from conversation`, `from footage`, `shape`, `style`, `captions on|off`, `purpose`, `named`;
  rules `measured` and `check`. A field without a spelling goes in a `{…}` block of JSON fields at the end of its
  item, so nothing is ever lost (a test fails a new field until it gets a spelling).
- `## The cut` is a cut sheet (docs/cut-sheet.md) of the recipe's cut: every section's clips in order, the
  graphics following them, narration, sound, captions and the mix; the sections name their clips. It has no project,
  base or change note of its own.
- A heading present means its list is there, even empty; a missing one means none.

Reading is strict, like the cut sheet's: `metarush recipe check <file>` (`recipe.check` with `sheet`, read-only) gives
problems with their line, column and a hint (a cut's on the recipe sheet's own lines: an unknown clause, a clip no
section names, clips out of the sections' order…), what the sheet changes against its `base:` in plain words
(`changes`, the cut's in the cut sheet's own words; `recipes.DocChanges`) and the recipe's own check. `--save`
(`recipe.write`) saves it as a **draft**: a new version of the recipe `name:` names, built on `base:` (which must be one
of its versions; a sheet without one for a recipe that exists is refused), or a new recipe when the name is new and
there is no base. Whoever writes it — Claude or a person — only a person makes it current. `--write` rewrites the file
as the saved version (its new base). The panel offers `--save` as a card. Golden file:
`internal/recipes/testdata/teaser.recipe.md`.

**Checking it** (`recipe check`; on every save and plan): the shape (names, kinds, sections, bindings that point at
something), the projects it relies on (archived or cleared out is reported), every binding against what it reaches
(a graphic's field against its template's JSON Schema with the input's longest words, word keys against the
composition's settings and its version approved, a picture setting against the version's pictures — they must be
settings, `pictures_not_settings` otherwise — picture names against the fields that draw resources, a generated
shot's record), then a **dry assembly** with its own inputs through `editspec.Validate`, with a still of each section
(`/media/assets/<clip>/frame?t=…`).

## Runs

A run records its recipe version, every input as it was (words; each clip's SHA-256; each picture's version; for
pictures from a repository's folder the commit and each file's path too; a voice by ID), the note the person gave, the Content Settings it followed (resolved), where its video is, the plan's
fingerprint, the **estimate** and what a person **agreed** to, what it **spent**, and each step with what it made.

| Step | Who | Through |
|---|---|---|
| Bring in the inputs | MetaRush | the records are checked (nothing in the Trash, every picture version there) |
| Make a sample first | MetaRush | only for a run with an expensive kind of work ([below](#a-sample-first)): one short line in the AI voice, one shot, one stretch made clearer in the cloud, in a short cut of its own with its review copy |
| You approve the sample | you | `recipe.sample`: the rest of the run is planned then, and asks for its agreement |
| Make a place for the video | MetaRush | `project.create` (a new project follows the recipe's Content Settings and links the recipe's project, so its footage, pictures and graphics are usable) or a new cut `<recipe>-<n>` in the recipe's project; the footage joins the project; rules and sign-offs |
| Make clips from code with the new words and pictures | MetaRush | `remotion.ClipWith`: the same approved version, composition, props, frames and sound, drawn with the run's words and with its pictures as the clip's own (`ownPictures`, checked the way `remotion.use` checks `pictures`, part of the render key) — no new code to approve, the source's own settings and the cuts that follow them don't change; a render made before with the same words and pictures is reused |
| Make the generated shots | MetaRush | `generate.shot` with the original's model, length and look, within the agreed amount |
| Put the video together | MetaRush | fixed sections built and **locked**; chosen sections with **first picks** from the footage's moments or, without words or movement, **stand-ins** (`moments-1`, `moments-2`…: the section's smallest count, its length, spread over the footage) unless the footage is the same; saved through `edit.check`/`edit.apply` with origin `recipe:<run>` and the note "Made with the recipe “…”, run N" |
| Make the AI voice | MetaRush | `voiceover.tts` for lines without a voice (new words, another voice); unchanged lines are reused for free |
| Claude chooses the moments | Claude | keeps or changes the first picks of the sections and the shorts (`recipe.picks`), or replaces stand-ins through an ordinary cut sheet; the run notices and goes on |
| Make the voices clearer | MetaRush | `sound.enhance` with the recipe's settings for the new footage |
| Make a review copy and check it | MetaRush | `render.submit` (review) and the automatic quality check |
| Make the shorts | MetaRush | the pack: each short its own project through `conversation.short`, its voices made clearer like the main video's, a review copy and the quality check |
| Ask for your review | MetaRush | `revision.askReview` for the version and each short; the bell rings |

**Again** (`recipe again <run>`): "Regenerate the teaser with the new art and a warmer voice" makes a new
version of the **same cut**, built on its newest version: only what the changed inputs reach changes (a folder's or a
collection's input takes the newest), so edits people made since stay. The plan says what changes and what is reused.
Feedback for every run becomes a draft of the recipe; feedback on the result is an ordinary edit.

**Compare** (`recipe compare a#1 a#2`): changed inputs, notes, recipe versions, cost, and the cut-sheet difference of
the versions they made (`cut.compare`), with both review copies.

### The estimate and the agreement

`recipe.plan` returns one **estimate** before anything starts: a `costs.Estimate` (schema `CostEstimate`), a
total in dollars ("about $0.40", "less than a cent", "nothing") that opens to **parts** — the outside service, its
model, what it draws on (the script line, the shot's words, the recording), the units, each part's price and its dated
rate — with each part's `step` naming the run step it belongs to ("Make the AI voice"). The parts are the services' own:
`generate.estimate`'s `cost` for a generated shot, `voiceover.PriceLines` (the same voices, live models, cache and
budget checks as `voiceover.estimate`, for lines no cut has yet) for the AI voice, and for clearer voices in the cloud
`sound.CloudCredits` priced like `sound.enhance` (an upper bound: the new footage's length). ElevenLabs is said in
dollars at its pay-as-you-go prices (`providers.SpeechUSD`, `IsolationUSD`); the project's budget still counts its
credits, and a run records both what it spent in dollars and the credits. The total is the services' own totals (what
each step is held to, `steps[].estimate`); budget lines say what the project's budgets and today's limit leave.

Starting needs the plan's fingerprint (what starts is exactly what the person saw) and, unless the estimate is nothing,
`maxUsd` at least the estimate: one agreement covers the run (`agreed.upTo`, dollars). A step that would spend past it
waits for a person ("Waiting for your OK: the AI voice costs $0.02 more than you agreed to") and carries the amount
(`waitingUsd`); going on needs a person's OK for at least that much (`recipe.continue` `moreUsd`, else
`E_CONFIRM_REQUIRED`): the dashboard's Go on, `recipe continue` (it asks for the amount the run asks, or `--more-cost`)
and the to-do's "Go on" agree to it. A step spends only its share of the agreement: what the plan said the steps
after it cost stays theirs, so a step that costs more than planned asks for its own part and the steps after it don't
ask again. Budgets, daily limits and project caps still stop spending (the run waits: "raise the
budget, then let the run go on"); consent, approvals and people-only actions are unchanged. **Auto never starts a
run**: `recipe.run`, `recipe.again`, `recipe.continue`, `recipe.publish` and `recipe.archive` are people only.

**The preference.** Before the one agreement, the plan asks `prefs.EstimatesFirst(ctx, a, userID)` for the
person behind the call (`prefs.Asker`): their "Estimate costs of outside AI services before edits" (Settings →
Preferences, on by default; an owner can require it for everyone). Off, the plan says so (`askForEstimate: false`, and
the summary), and a run starts without the estimate step (`agreed.skipped`, no `maxUsd` needed; the audit records
`estimateFirst: false`): it spends within budgets, daily limits and caps alone. Starting is still the person's.

### A sample first

A run whose estimate has an **expensive kind of work** — the AI voice, generated shots, voices made clearer in the
cloud (the main video's or the shorts') — makes **one priced sample of each kind first**, and a person approves it
before the rest of the run is planned. "Expensive" comes from the estimate's parts, step by step: a kind is expensive
when it costs at least $0.05 in the whole run (`SampleFloorUSD`; below that a sample would cost about as much as the
work itself).

| Kind | The sample |
|---|---|
| the AI voice | the shortest line that needs a voice, whole, read in the run's voice over the start of the video (its graphics that fit); the rest of the run reuses it from the cache, free |
| generated shots | the first shot made from the run's words; the run uses it as its shot (never made or paid twice) |
| voices made clearer in the cloud | one stretch of the new footage (up to 30 s), made clearer as the recipe has it (cleaned up again with the rest of the footage) |

The plan says so (`sample`: each kind, what its sample draws on, what it costs and what that kind costs in the whole
run; the summary says "A sample first: …"), and its steps start with **Make a sample first** and **You approve the
sample**. Starting a run agrees to the **sample's price only** (`maxUsd` at least the sample's estimate; a larger
amount is held to the sample's). The sample is made in the run's own place — a short cut of its own (`sample` in a
run's new project, else `<cut>-sample`) — through the same operations a run uses, within that price, and gets a
review copy: the run's page plays it under **The sample is ready: watch and listen to it, then approve it**. Then:

- **Approve the sample** (`recipe.sample --approve`, people only): the rest of the run is planned with the inputs the run
  recorded, in its place, at today's prices; its steps follow the sample's; the run waits for the approving person's OK
  for the rest ("the sample is approved, and the rest of the run costs $7.14 — agree to it to go on": Go on, `recipe
  continue`) unless they agreed to at least that much already (`--more-cost`). It is the approving person's own
  "estimate costs first" preference that counts, whoever started the run: with it off, the rest goes on at once
  within budgets and limits.
- **Not quite…** (`--not-quite "…"`): the run stops with what the person said; what it made stays. Plan it again with
  other inputs.
- **Skip the sample…** (`recipe run|again --skip-sample "why"`): the whole run starts at once, agreed in full; the reason
  (a few words at least) is kept on the run, in the audit and on the receipt.
- **Stopped, or didn't finish**: the sample ends with its run, keeping what it spent so far (the receipt says it), and
  can't be approved any more.

While the sample waits, **Go on** is refused and the run's to-do offers only Stop and **Watch the sample** (no quiet
approval). The project's receipt lists each sample — what it was, what it spent, who approved it, turned it down or
skipped it, and why (`project.receipt` `samples`). Auto never makes, approves or skips a sample: starting a run
and deciding a sample are people only, and the panel's run card agrees to the sample's price when a person decides it.
**A run again** samples the kinds whose inputs changed since the run it makes again — a new voice or narration words, a
shot's words, new footage — but not a kind a person approved there (its sample, or the version it made) whose inputs
didn't change. Its sample sits beside the cut it makes again (`<cut>-sample`), and the rest builds on that cut.

### Pictures as settings

`pictures:<clip>.<NAME>` binds a pictures input to a picture setting of the Remotion composition a clip is made from
(a version whose pictures a person made settings, `remotion pictures setup`). A run passes the input's picture as the
clip's own — the binding point remotion.use and remotion.render take (`pictures: [{name, resource}]`, recorded per clip
as `ownPictures`, part of its render key) — through `remotion.ClipWith`; it never changes the source's own pictures
(`remotion.pictures.set` moves every cut), so other cuts and projects that use the source are untouched. A pictures
input fills the names bound to it in the order of the bindings (graphics' pictures and clips' alike); names left over
keep the picture the clip draws. The picture must be one the Remotion project's own project can use (its resources,
linked projects', the workspace library): the plan says so otherwise. A version whose pictures aren't settings yet is
a problem in the check and the plan ("aren't settings yet: a person makes them settings once").

### First picks

Before Claude looks, MetaRush picks. A chosen section whose footage changed starts with picks from
`footage.moments`: whole stretches of speech that start cleanly (the first words, after a pause or another voice) and
end cleanly, of about the length each moment should be (the section's length shared by the number of moments it was
saved with, within its count), scored like the conversation finder's — a question and its answer, people laughing, a
strong first line, two voices, the length — the best that don't overlap, spread over the clips, in the footage's
order. Footage without words gets what moves on screen (movement, a slow change, a cut); footage with neither gets
stand-ins, as before. Each pick is a clip that says why (label "First pick: …", the reason, the first words as
transcript evidence), recorded on the run (`standIns[].first`, `why`, `words`). A pack's shorts get theirs from
`conversation.moments` (their stretches on the conversation's clock).

The run waits for Claude: "Waiting for Claude to look at the first picks for “moments” and the shorts (keep or change
them)". `recipe runs --run` gives `run.claude` (each pick with why, the section's purpose and needs) and `run.picks`
(each pick's part, number, footage, times, why, words and state: first, standin, kept, changed, dropped).
`recipe.picks` (`metarush recipe picks <run>`; Mutating, not people only, under the run's lease) keeps every pick still
as MetaRush made it (`--keep`), or changes one (`--part`, `--n`, `--from`, `--to`; `--footage` another clip of the
run's; `--why`): a section's moment becomes a new version of the run's cut (origin `recipe:<run>`), a short's stretch
changes before it is made; `--n` one more than a part has adds one, `--drop` takes one out. Changing a pick through the
cut's sheet counts too. The run goes on by itself once every pick is kept or changed.

### Packs

A recipe's `pack` lists other videos every run makes besides its main one — **shorts** of a conversation the
video plays (`conversation`: its name in the cut, default the first one played; `count` default 1–3; `length` default
15–60 s; `shape` default vertical; `style`; `captions`; `named`, a project name pattern with `{title}`, `{n}` (the
run's number), `{k}` (the short's) and `{first}` (its first words)). Saving a cut that plays a conversation offers it
(proposal kind `pack`, key `shorts`; `metarush recipe save --pack shorts`); the sheet's `## Pack` changes it.

The plan picks each short's stretch (first picks) and lists them (`pack`: the stretch, why, the first words);
its steps get "Claude chooses the moments" and "Make the shorts" (after the main review copy), and its one estimate
covers the pack too: when the main video makes voices clearer in the cloud, the shorts' are priced under "Make the
shorts" (an upper bound, their length), and the one agreement holds every step to its share. Once Claude kept or
changed the stretches, each short is made through `conversation.short` — a project of its own (linked to the
conversation's), a vertical cut with captions with names, its version's origin `recipe:<run>` — then its voices are
made clearer the way the main video's are, it gets a review copy and the quality check, and it waits in Awaiting your
review with the main video ("Version 3 and 2 shorts are in Awaiting your review"). A short that can't be made is said
on the run; the others go on.

**Shorts of footage**: `footage: <input>` (sheet `from footage call`) makes the shorts from the clips a run gives
a footage input instead — a call's single recording that isn't a conversation. Their first picks are whole stretches of
speech of the shorts' length (`footage.moments`, scored like first picks), each made by `footage.short` as a new cut of
the run's project (`shorts-1`, `shorts-2`…, origin `recipe:<run>`), its voices made clearer like the main video's, a
review copy and the quality check. `recipe.picks` changes one (`--footage` another of the run's clips) or adds one even
when the first picks found none. Every short is recorded on the run (`outputs`: its stretch and pick, its project, cut,
version, review copy, quality check, state), `recipe.runs --project <a short's project>` finds the run (migration 39),
and the run's view lists them (`shorts`, with links). A run again keeps the shorts the run it repeats made.

### Pictures from a repository path

Art kept in git: a pictures input whose values come from a folder (or a file) a GitHub repository keeps pictures
from (`from: {repo: <res_…>, path: "art/characters"}`; `.` or nothing is all it keeps; see
[resources.md](resources.md#pictures-from-a-repository)). A run takes the pictures the repository kept there **at
the commit it shows**, as many as the input takes, and records that commit (`inputs[].repo`, `commit`) and each
picture's version and file (`items[].sha256`, `path`). The bindings are the ones any pictures input has — a
template's picture parameter (`resource:<name>`) and a Remotion picture setting (`pictures:<clip>.<NAME>`,
drawn with no new code to approve).

- **Each file keeps its place.** The pictures the saved video (or the run being made again) showed stay where they
  were: a file's newer art takes its own place; a place whose file went away takes the next picture in path order;
  places left over are filled in path order. So "the new art for Alice" changes exactly Alice.
- **New art reaches runs with a refresh.** The repository's newest commit comes in with `metarush resources refresh
  <repo>` (or Get the newest commit); its pictures are kept with it, and the next plan or run again takes them
  (`changes` say which). A run never fetches by itself.
- **Saving a cut proposes it.** A cut whose graphics (or clips made from code) show pictures kept from a repository's
  folder proposes one input for that folder ("The pictures from “art/characters” in acme/widget"), the pictures it
  showed as its default, as many as it showed at most.
- **The check and the plan say plainly** when the repository is gone, removed, not one the run's project can use
  (link its project), no longer keeps that folder, or has no pictures there.

### A Google Drive folder as an input

A pictures or footage input can take the newest files of a **Google Drive folder that isn't synced** to this
computer (a synced Drive folder already works as a [linked folder](resources.md#linked-folders)). MetaRush reads it
through the person's **own rclone**: they install rclone and add their Drive with rclone's own setup (`rclone config`:
a remote of the type Google Drive, signed in in their browser). MetaRush never signs in to Google, never stores a
token, never needs a Google Cloud project, and never reads rclone's settings.

```bash
metarush doctor                                                   # GOOGLE DRIVE: is rclone there, with a Google Drive?
metarush recipe update app-teaser --from-drive "art=gdrive:Teaser/Character art" --dry-run
metarush recipe update app-teaser --from-drive "art=gdrive:Teaser/Character art"   # a draft a person makes current
metarush recipe plan app-teaser --json                            # the folder listed; how many files the run fetches
```

- **Planning lists the folder** (`rclone lsjson`) and takes the newest files of the input's kind (pictures, or video
  and sound), as many as it takes. A file a run fetched before is used again without fetching while Google's checksum
  says it is the same. The plan's first step says how many files the run fetches (`inputs[].drive` is the folder; an
  item still in Drive has `drive` — its Drive ID, name, size, time and checksum — and no `id` yet). A file changed
  since the plan was shown changes its fingerprint: the plan is made again, nothing starts.
- **The run fetches only those files**, one by one by their Drive ID (`rclone backend copyid` into scratch, the
  run's lease held meanwhile), checks each against the listing (its size, and Google's checksum when it gave one) and
  keeps it by its content: a picture in the **workspace library** (every project sees it), tied to its Drive file
  (`driveFile`), so new contents of the same file are a new version of the same picture; footage as a clip that says it
  came from Google Drive (`source.platform: drive`, its Drive ID). Each item records its Drive ID, name, size, time and
  checksum, then its SHA-256 (`items[].drive.fetched`).
- **Checks never list a Drive**: `recipe check` uses the recipe's own items, so a recipe checks out on a computer
  without rclone. **Without rclone**, a Google Drive remote, a sign-in Google still accepts or the folder, the plan says
  plainly how to set it up (and `metarush doctor` does too).
- Only remotes of rclone's Google Drive type are used, named exactly as rclone lists them; the folder is written
  `remote:path` (in a sheet: `from drive "gdrive:Teaser/Art"`). Runs fetch on the computer that started them, so
  that computer's rclone needs the remote.
- In Claude's panel, `recipe update --from-drive` is a card like any change: what the draft changes, saved as a
  draft by a person; the folder is read only when a run is planned.
- **A Dropbox folder works the same way**: `--from-dropbox "art=dropbox:Teaser/Art"` (in a sheet: `from dropbox
  "dropbox:Teaser/Art"`; in the document `from: {dropbox: …}`), only remotes of rclone's Dropbox type. Each file is
  fetched by its Dropbox ID (the folder listed again to find where that ID is now, then `rclone copyto`) and checked
  against Dropbox's content hash; the run's input says `driveKind: dropbox`, footage says `source.platform: dropbox`,
  pictures `source.kind: dropbox`. Files from either can also be brought in directly: [cloud folders](cloud-folders.md).

### Where it runs, and waiting

Runs advance from the database (`recipes.Advance`): the daemon's loop (`recipes.RunEngine`, every 5 s, woken at once
by a run started from the dashboard) and a terminal's `recipe run --wait` / `recipe runs --run … --wait` (which also
runs the renders it waits on when no worker takes them). Only the computer that started a run advances it (its
files and keys); a lease on the run keeps two looks apart — each look holds it under a name of its own (`lookName`:
the computer, the process and the look), so a dashboard request, the daemon's loop and a terminal never step one run
at once (`TestTwoSteppersTakeTurns`); a step's work (a render, a generation) is recorded before
it is waited on, so a restart never asks twice. Everything a run does is logged in the **recipe channel** as the
person who started it (`models.ViaRecipe`).

A run that waits says so plainly on the activity bar ("Run 4 of “App teaser”: waiting for Claude to choose the moments
of “moments”"), as a `recipe_run` to-do (Go on, Stop; going on is people only) and on the bell, which also rings when
it is ready for review or didn't finish. `recipe runs --run <recipe#N>` gives Claude its task in words (`run.claude`).

### What changed between versions

`recipe diff <recipe> [--to N] [--from N|current]` (`recipe.diff`, `internal/recipes/differences.go`) says what changed
from one version to another, part by part, each change a sentence: **what changes each run** (an input that is new
or gone, its default, its limit, where a run takes it from, where it goes), **its sections** (new, gone, the same every
run or chosen by Claude, what for, how many moments, how long, which clips, their order), **sound** (narration lines,
music, captions, the mix), **graphics** (a field's words, another version, another time), **pictures**, **rules**
(with the ones the newer version no longer keeps listed apart), **sign-offs**, **hints**, and the title, the video
(shape, length, frame rate, platform, crops), where runs go and whose Content Settings they follow. By default it is
the newest draft against the version it was made from; a part lists twelve changes, then "and N more". `recipe update
--dry-run` says what a draft would change and saves nothing; a saved draft says what it changed; `recipe publish` in a
terminal says what the version changes from the current one before asking.

## Suggested changes

MetaRush learns from what people changed in runs. `recipe suggestions <recipe>` (`recipe.suggestions`, read-only,
`internal/recipes/suggest.go`) compares, for the newest 12 runs that are done and approved — made from the recipe, not
again from another run (a run again builds on a cut people already changed) — the newest version a person approved
with the version the run made, part by part of the recipe version it made:

| What | Counted when |
|---|---|
| a fixed section's clip | it plays shorter or longer (by a third of a second and a tenth), or was taken out |
| a chosen section's moments in all | shorter or longer by a fifth and a second (Claude's picks vary run to run) |
| the recipe's graphics | taken out; words changed where no input fills them; shown for longer or less |
| captions | turned off, on, or moved |
| its music and sounds | taken out; 2 dB quieter or louder |
| its narration lines | their words changed where no input fills them |

What an input gives, the moments Claude picked and what a run's own steps made never count. A change **repeats** when it
is in more than half of the runs looked at, and at least two: "You shortened “intro” in 4 of 5 runs: from 3 s to about
2 s." Words changed to the same new words set them; words changed each time to others make them **something each run
gives** (a new words input, its default the recipe's words). Each suggestion has its reason, its runs, a key, and what
the draft does ("“intro” plays about 2 s (was 3 s)"); the answer says what a draft from the current version (else the
newest draft) would change, part by part (`recipe diff`'s words), and its check. A change the version already has — or
whose part it no longer has — isn't offered again, so making the draft current ends the suggestion.

It is always a suggestion: `recipe suggestions <recipe> --save [--only <key>]… [--note …]` (`recipe.suggestions.save`,
Mutating, not people only) saves a **draft** whose note gives the reasons; only a person makes it current (the draft's
**Make current…**). On the recipe's page, **Suggested changes** lists them with ticks (one left out: the draft is
worked out again without it) and **Save as a draft**, which opens the draft. Claude reads them (`--json`, on its
read-only list); in the panel its `--save` is a card.

## Planning by itself

A recipe can plan a run by itself and ring a person's bell — and **never starts one**, whatever Auto or the estimate
preference say (`internal/recipes/triggers.go`):

- **When**: new footage or pictures arrive where an input takes its values from — a linked folder's pictures, a
  collection's or a project's footage, a repository's folder of pictures (`from` in the input) — once they have settled
  (two minutes without more: a folder being filled is one plan); or every week on a day at a time
  (`monday 09:00`, `fri 5pm`) of the looking computer's clock. Files are compared as ID@version, so new art for an
  existing picture counts; what the sources held when it was turned on isn't new.
- **The plan**: made as the person it tells (their estimate preference, their budgets) with the newest files, kept on
  the recipe (`prepared`: why, what arrived, its inputs, where the video goes, its estimate, whether it can start and
  why not) and on the bell (kind `recipe_planned`): "New footage on the collection “Gala” (2 clips) for “Event recap”:
  the run is planned, about $1.20" with **Run…**, which opens the recipe's page with Run with… on it
  (`/library/recipes?recipe=<name>&planned=1`) — the person agrees to the estimate there as always. A plan that can't
  start says why. A run that starts uses the plan up; **Set aside** drops it.
- **Settings**: per recipe, off until a person turns them on (`recipe triggers <recipe> --new-files on|off --weekly
  "monday 09:00"|off`, `recipe.triggers.set`, people only; or **Planning by itself** on the recipe's page). They tell the
  person who turned them on, and the computer they were turned on at looks (its keys price the plan). Anyone sees them —
  whom they tell, the computer, who turned them on, the next weekly time and the plan waiting (`recipe triggers`, every
  recipe's without a name); while they are on, only that person or an owner changes them.
- **The loop**: the daemon's `recipes.RunTriggers`, every minute (woken at once by a change;
  `METARUSH_RECIPE_TRIGGER_SECONDS` sets the look and the settling time for browser runs), only for recipes whose
  computer it is; a change of settings meanwhile wins (the look writes only while the settings are the ones it read).
  Terminals never look. No migration: the settings and the plan live on the recipe.

The gallery marks a recipe that plans by itself ("Plans by itself") or has a run planned ("A run is planned"); `recipe
list` says so too.

## Between workspaces

A recipe can go to another workspace — another team, another organization — as **one file**, and come in there as a
draft that a person makes current.

```bash
metarush recipe export app-teaser                          # → app-teaser.recipe.zip (--version N, --max-size MB)
metarush recipe import app-teaser.recipe.zip --dry-run     # in the other workspace: what would come in
metarush recipe import app-teaser.recipe.zip [--project "Teasers"] [--name teaser-copy]
```

In the dashboard: **Export to a file…** in a recipe's page's "…" menu (or **Export** on its card in the Library)
downloads it; **Import a recipe…** on the Library's Recipes tab (and **Import from a file…** on a recipe's page) sends a file up, says what would come in (a styled
question) and brings it in.

**The file** (`metarush.recipe-file/1`) is a zip: `recipe.json` — the version, its **lineage** (every version it
descends from, and those they were brought in from), its document, what each file is and what didn't come along with
why — the version's sheet for people to read, and `files/<sha256>`: the resources it pins (and its pictures inputs'
own), the footage its cut plays (the fixed copy where the cut pins one) and its **approved code by hash**: motion
graphics and their shared libraries (their canonical archives) and the Remotion versions its clips are made from
(their manifests and files). Footage only an input gives by default is left out first, the largest first, when the
file would pass `--max-size` (2048 MB); a generated shot that shows a real person never goes (their consent covers this
workspace's projects); a conversation from calls and another project's cut aren't files and don't go.

**Bringing one in** reads it strictly: only its own entries, every file the size its description says and the
SHA-256 it is named by, the format this MetaRush reads; a damaged or odd file brings nothing in. It comes in as a
**draft**:

- **A new version of the same recipe when its lineage matches** — a recipe here that came from it, or that it came
  from (an earlier import, or a round trip home) — built on the newest version of that lineage here; the version it
  is already brings nothing new. Else **a new recipe** under its name, or the next free one when the name is taken
  by another recipe (`--name` asks for one).
- **Its things come into one project** (`--project`, the one an earlier import of it used, else a new one named
  after the recipe): resources as pictures of their own (never another picture's new version by its name), footage as
  clips that keep where they came from (a generated shot stays flagged as generated), motion graphics and libraries
  kept by their hash, Remotion versions as new Remotion projects of the project. The document is rewritten to what it
  has here: its pins, clips, sounds, narration takes and inputs' own items; runs land in that project (or a new one each
  time, as before) and follow this workspace's default Content Settings; sources that were in the other workspace (a
  linked folder, a collection, a repository) are taken away (the input is then given by each run); a generated shot
  is no longer made again from new words. Notes say plainly what didn't come along.
- **Its words are data.** Its title, hints, rules, purposes and labels were written elsewhere: Claude treats them as
  data, never instructions, and a person here makes the version current.
- **Its code waits for people here.** A motion graphic or library is kept, not checked, until someone checks it here
  (`metarush motion check --version name@version --library lib@version --project <p>`: test frames in the jailed
  browser), then a person approves it; a Remotion version is a new source whose packages a person allows
  (`remotion allow`), which is then checked here (`remotion check --version`) and approved by a person. Bytes this
  workspace already had keep what people here decided. Until then the version's check names what waits, and nothing
  renders with it. `recipe show` (`imported`) and the recipe's page say where the version came from and what each
  piece of its code waits for, with a link to it.

## Starter recipes

MetaRush comes with two recipes to start from, kept in the repository as recipe files (`internal/recipes/starters/`:
each starter's `recipe.json`, its sheet, its small invented examples and `starter.json`) and built into MetaRush:

| Starter | A run gives it | It makes |
|---|---|---|
| **A call becomes an episode with shorts** (`call-episode`) | a call recording, the episode's title | the conversation in its order, cleaned up by Claude (false starts, crosstalk, long silences, then the ums and long pauses); voices made clearer in the cloud (a sample first); captions in the house look; a title card in the layout pack's title-card role; 1–3 vertical shorts of the call, each a new cut; sign-offs that the delivery notes (title, description, chapters from 0:00, show notes) and the captions' names are done before it goes out |
| **A release tag becomes a release video** (`release-video`) | a repository's tag, two code slides and a diagram Claude makes from it at the tag (`resources make code` and `diagram`, each keeping `repo@commit:path:lines`), a line of narration per slide | a title slide, the two code slides (each with its file and lines), the diagram and an end card in the layout pack's end-screen role, narrated in the AI voice (the Content Settings' narration voice unless a run gives another; priced first, a sample line first when it costs enough); under 90 seconds, and a sign-off that the code is the tag's |

```bash
metarush recipe starters                       # what each makes, what a run gives it, whether it is here
metarush recipe starters --add                 # bring them in as drafts (--only release-video)
metarush recipe publish call-episode           # a person makes it current
metarush recipe run call-episode --input call="The garden call" --input episode="Episode 7"   # a sample first
```

**They come in as drafts, never current.** Adding packs each starter into a recipe file and brings it in through
`recipe.import` — read strictly, its words data — as a new recipe (a draft), its examples in the project
"Starter recipes"; a person looks at it and makes it current before it runs. Adding again brings nothing new, unless
this MetaRush has a newer version of a starter: it comes in as a draft of the recipe it came in as before (they share
their lineage). In the dashboard the Library's Recipes tab and Home's Start from a recipe offer the starters not added
yet: **Add the starter recipes** brings them all in, **Use this one** brings one in and opens its page. Claude may add
them too (`recipe starters --add`: drafts only).

Their examples (a plain clip, SVG code slides and a diagram) are invented and small; they let `recipe check` pass on a
fresh workspace. The starters are written in Go (`internal/recipes/starters_def_test.go`) and their files kept in step
by `TestStarterFiles` (`METARUSH_UPDATE_STARTERS=1 go test ./internal/recipes -run TestStarterFiles`; a changed starter
gets a new version, its lineage kept).

## In Claude's panel

Claude's `metarush recipe run|again|save|update|publish` are refused on its read-only channel and become cards built by
the server (`internal/assistant/recipe_actions.go`), each needing a person:

| Claude asks | The card | Deciding it |
|---|---|---|
| `recipe run <recipe> --input name=value… [--note …]` | the plan: where the video goes, what this run gives each input, each step and who does it, budgets, problems; one estimate that opens to the services, models and sources; the plan's fingerprint; the sample it makes first | people only, on its own card: a styled question agrees to the estimate — the sample's when the run makes one first (estimates off: only whether to start it); `maxUsd` is that estimate, never Claude's `--max-cost` |
| `recipe again <recipe#N> --input …` | the same, with what changes and what is used again | as a run |
| `recipe save --from <project> [--vary|--keep|--fixed|--chosen …] [--hint …]` | what would change each run and what would stay, each with why; its check | saved as a **draft** even when a person clicks it |
| `recipe update <recipe> --hint|--rule|--title|--patch <file>` | what the draft changes from the version it starts from, in plain words (the patch only from the conversation's proposals folder) | saved as a draft |
| `recipe publish <recipe> --version N` | what the version changes from the current one and the rules it no longer keeps; its check | people only, on its own card, after the same words in a styled question |
| `recipe picks <recipe#N> --keep` / `--part … --n … --from … --to … [--why …]` / `--drop` | what it keeps or changes, and the run's picks as they stand (first, stand-in, kept, changed) | **Keep them** / **Change it**; the run goes on once every pick is kept or changed, and is the card's work |
| `recipe check <sheet> --save [--note …]` | the sheet from the conversation's proposals folder: what it changes from the version it was written from (or a new recipe), its problems by line, its check | saved as a draft (**Save the draft**) |
| `recipe suggestions <recipe> --save [--only <key>]…` | each reason, what the draft changes from the current version, its check (none to save: says so) | saved as a draft |

**None runs by itself**: Auto never starts a run, not even for a person whose estimates are off, and never
runs these cards; Approve all never includes a run, a run again or making a version current (`assistant.act` refuses
them with other cards: `decide_alone`); the picks, sheet and suggestions cards never run by themselves either. **Not quite…** sends a card back to Claude with the person's note, like any
card. A run card whose plan changed meanwhile (another run started, a price changed) is planned again in place and
says so; nothing started. A run started from a card is taken on by the daemon's engine at once, as one started with
Run with… (never inside the click), and is the card's work: Claude hears once — when the run waits for it to choose
the moments (with the task), waits for a person, or is ready for review. Save, change and make-current cards show the
check's problems; its notes about the cut (how a clip is cropped) stay on the recipe's page. `recipe archive|continue|stop`
and turning planning by itself on or off (`recipe triggers --new-files|--weekly|--dismiss`) never become cards (a
person does them on the recipe's or the run's page). Reading, checking, planning, runs, comparing, `recipe diff`,
`recipe suggestions` and `recipe triggers` are on Claude's read-only list (`recipe runs --wait` stays refused).

## Surfaces

- **CLI**: `metarush recipe list|show|propose|save|update|checkout|check|publish|archive|plan|run|again|runs|picks|compare|diff|continue|stop|suggestions|triggers|export|import|starters|sample`,
  `--json` everywhere; starting shows the plan and its estimate (`--details` adds each part's rate) and asks the person
  to type `start` (`--max-cost` agrees to more; with estimates off, no price is asked about; refused without a
  terminal: people only).
- **Claude**: the `video-recipe` skill plans, explains costs, saves drafts, makes changes for every run as drafts,
  writes and changes recipes as sheets (drafts), keeps or changes first picks and chooses moments through cut sheets;
  reading, checking out, checking, planning and comparing commands are on the read-only allowlist (`recipe check
  --save` is a change); in the dashboard's panel its runs, saves, changes and making a version current are cards.
- **Dashboard**: the Library's Recipes tab ([below](#the-dashboard)), links `/library/recipes?recipe=<name>`
  (`&planned=1`: Run with… on the run it planned by itself) and `?run=<id>` (`internal/links`).

## The dashboard

- **Library → Recipes** (`/library/recipes`, next to People and Motion): a card per recipe — a still of the render it
  was saved from, what it makes, where runs go, what changes each run, its last three runs (state, who, when, approved,
  spent) — with **Run with…**; archived ones on request.
- **Run with…** (and **Run again…** on a run): the inputs as a form — words (with the graphic's limit), a choice, the
  AI voice (the account's voices), clips or pictures (pickers) — each with where it goes and what the plan says the run
  will use; then the plan: where the video goes, problems first, each step with who does it (MetaRush, Claude or you),
  and one estimate, "about $0.13", that opens to the services, models and sources (`components/CostEstimate.tsx`).
  **Run it (about $0.13)** asks in a styled question (one agreement covers the run; a step that would cost more stops
  and asks; budgets and limits hold) and sends `maxUsd` = the estimate. With estimates off there is no money question,
  only the line that budgets, today's limits and project spending limits still apply. `E_CONFIRM_REQUIRED` asks for
  the amount the server names; `E_CONFLICT` (the plan changed) plans again and shows it, nothing started.
- **A recipe's page** (`?recipe=<name>`, `&version=<n>` for another version): what it makes, its example, the inputs,
  sections (the same every run, or Claude chooses), rules, sign-offs and hints in plain words; its versions (current,
  draft, earlier; Claude's drafts marked), each with **What changed** from the one it was made from, a version
  that isn't current showing what it changes from the current one, and **Make current…** (people only; the question
  says what it changes and names rules the new version no longer keeps); its runs, two of which play **Side by side**
  (`?compare=<run>&with=<run>`: their review copies next to each other with one Play, one seek bar and the sound from
  either, stacked on a phone, above the changed inputs, recipe versions, cost and the cut's changes); Archive and
  Restore.
- **On a recipe's page** too: **Suggested changes** (what people kept changing in runs, with ticks
  and **Save as a draft**), **Planning by itself** (when new files arrive, every week; whom it tells, the
  computer that looks; switches for that person or an owner) and, at the top, the plan it made with **Run…** and
  **Set aside**.
- **Start from a recipe** on Studio's Home: up to four recipes that can run, each with a still, what it makes and
  where runs go, and **Run with…** (the run dialog); with none, a line saying how to save one, and the starter recipes
  not added yet.
- **Starter recipes** on the Library's Recipes tab: the starters not added yet, each with what it makes and what
  a run gives it, **Use this one** and **Add the starter recipes**.
- **A sample first**: Run with… shows the sample (each kind, its price and the kind's in the whole run) and
  **Make the sample first (about $0.06)**, with **Skip the sample…** asking why; the run's page plays the sample's review
  copy with **Approve the sample** and **Not quite…**, then Go on for the rest; To-dos say **Watch the sample**.
- **A run's page** (`?run=<id>`): its steps as they go (live events for `recipes`, `recipe_versions` and
  `recipe_runs`, a run's lease alone not counting); what it waits for — a person: **Go on** (with the amount when it
  asks to spend more) and **Stop**; Claude: what Claude is asked to do, **Ask Claude to choose them** — links to its
  project, the version and the review copy; what was agreed and spent; the inputs as they were.
- **Save as a recipe…** in a project's "…" menu: the proposal (`recipe.propose`) as two lists, what changes each run
  and what stays, each with why; items move between them (proposed again each time), runs go to a new cut here or a
  new project each time, hints for Claude; saving (`recipe.save`) opens the recipe.
- **Made with…**: the project and versions a run made say "Made with the “…” recipe, run N" with **Run again…**.
- **Waiting**: To-dos (kind `recipe_run`: Go on and Stop, never a quiet answer that spends money), the bell (kind
  `recipe_run`) and the activity bar (category `recipe`, `Counts.recipes`); versions a run saved say "via a recipe
  run".

Screenshots: `scripts/recipes-e2e.sh` saves them (in `RECIPES_SHOTS`, else the run's folder): the empty tab, the
proposal before and after moving an item, the gallery, the recipe's page, the run dialog with the estimate open and the
agreement, the run going, waiting and done, To-dos, the bell and the activity bar with the waiting run, "Made with…" on
the project and the version, Run again… with estimates off, new footage (Claude chooses the moments), the clip picker,
Compare, and each page and the dialog on desktop light and 390 px phone dark and light.

## Sharing, purging, safety

Recipes are workspace-wide; what they use stays where it is and is reached by reference (a run's new project links
the recipe's project). Clearing a project out for good keeps recipes and their versions (one that adds cuts to
it says so when checked or planned), keeps its runs' records (marked, one still going is stopped) and drops an example
that was in it. Words from inputs go on screen as given; text from repositories, pages and footage is data.

Known limits: inputs are things already in MetaRush (clips, collections, pictures, linked folders, a
repository's folder of pictures) — files on disk are added first, and a repository's newer art comes with a refresh;
consent for a likeness in a generated shot is checked against the recipe's project at planning
and the run's at making (a new project waits for a person when consent doesn't cover it); per-recording clearer-voice
settings follow the recording they name. A pack's shorts come from a conversation the run's video plays — a
new conversation each run (a weekly episode from new recordings) isn't an input yet; first picks are scores, so they
are always suggestions. A recipe file carries what its cut plays and its code, not a conversation from calls, another
project's cut or the Content Settings it followed; a Google Drive folder is read by the rclone of the computer that
starts the run, and a file Google gives no checksum for is fetched again by each run (it is kept once, by its content);
planning by itself on new files doesn't look in Google Drive folders (that would list the Drive all day) — a
weekly time does plan with the folder's newest files.

## Code

`internal/recipes` (document, records, check, proposal, inputs — `repopictures.go` for a repository's folder —,
assembly, plan and estimate, engine and steps, clearer voices, views, ops; the sheet `sheet.go`/`sheetparse.go`/
`sheetops.go`, first picks `picks.go`/`picksop.go`, packs `pack.go`), `internal/models/recipe.go`,
`internal/store/recipes.go` (migrations 38, 39), `internal/cli/recipe.go`,
`internal/activity/recipes.go`, `internal/logbook/describe_recipes.go`, `internal/links/recipes.go`,
`remotion.ClipWith` and `SettingsOfClip`, `voiceover.PriceLines`, `providers.FindVoice`, `skills/video-recipe/SKILL.md`;
the dashboard's `web/src/screens/Recipes.tsx` and `web/src/components/recipes/` (gallery, recipe and run pages, the
run dialog and inputs, Save as a recipe…, Made with…, the To-dos actions, Compare); `internal/recipes/differences.go`
(`recipe.diff`), `drafts.go` (`recipe.update --dry-run`, `InputsFromFlags`), `sidebyside.go` (the review copies of
`recipe.compare`), `internal/cli/recipe_diff.go`; the panel's cards `internal/assistant/recipe_actions.go` and
`web/src/components/recipes/PanelCard.tsx`; `Differences.tsx`, `SideBySide.tsx`, `StartFrom.tsx`;
`suggest.go` (`recipe.suggestions[.save]`), `triggers.go` (`recipe.triggers[.set]`, `RunTriggers`),
`internal/cli/recipe_suggest.go`, `recipe_triggers.go`; `Suggestions.tsx`, `PlansByItself.tsx`; the panel's picks, sheet
and suggestions cards in `recipe_actions.go`; `export.go` and `importfile.go`, `internal/cli/recipe_files.go`,
`internal/api/recipes.go`, `motion.Keep`/`Kept`, `remotion.ImportFromFile`, `web/src/components/recipes/Files.tsx`;
`drive.go` and `internal/drive`, the `driveFile` tie in `internal/resources`, `internal/cli/doctor_drive.go`.
Starter recipes and a sample first: `starters.go` (`recipe.starters[.add]`, the embedded `starters/`),
`starters_def_test.go` (their source and generator), `sample.go` (`recipe.sample`, planning and making the sample,
`planRest`), shorts of footage in `pack.go` and `picksop.go`, `followFootage` in `assemble.go`,
`internal/cli/recipe_starters.go`, the receipt's `samples` (`internal/receipt`), `sound.WindowSeconds`;
`web/src/components/recipes/Starters.tsx` and the sample in `RunDialog.tsx`, `RunPage.tsx`, `TodoActions.tsx`.
Tests: `internal/recipes/*_test.go` (a fake ElevenLabs and a fake Gemini; `pictures_test.go`: a Remotion opener made
again with new art and no approval; `goon_test.go`: a step that costs more asks for its own part; `repopictures_test.go`:
art kept in git, saved, run, made again after new art is pushed, and a Remotion picture setting drawn from it;
`exportimport_test.go`: a recipe exported, brought into a second workspace and back, its lineage, a name taken, odd and
damaged files refused, code waiting for a person there; `drive_test.go`: pictures and footage from a Google Drive folder
through a fake rclone, fetched once, new art a new version of the same picture, checks without rclone;
`sample_test.go`: a sample line, a sample shot, not quite and skipping; `starters_test.go`: the starters
added as drafts that check out on a fresh workspace, and the call starter end to end — a sample first, approved, the
rest, shorts of the call, the receipt; `sheet_test.go`: the golden sheet, odd and random recipes read back to the same fingerprint, a spelling for every field,
problems on their lines; `picks_test.go`: first picks, kept and changed; `pack_test.go`: an episode of the generated
call with two shorts, changed in its sheet, picked, made and reviewed; `suggest_test.go`: what repeats and what
doesn't, the draft, three approved runs end to end; `triggers_test.go`: new footage settles, a plan and the bell, never
a run under Auto with estimates off, a weekly time, someone else's settings, another computer; the panel's
`TestRecipePicksSheetAndSuggestionCards`), the acceptance
run's Recipes step (the fixture film with a picture folder saved, run, run again with a new picture and another
voice), and the browser runs `scripts/recipes-e2e.sh` (`web/scripts/recipes-e2e.mjs`, fakes only) and
`scripts/starter-recipes-e2e.sh` (`web/scripts/recipes-starters-e2e.mjs`, ports 5550–5559: the starters added, the call
starter run with a sample first, approved, then run).
