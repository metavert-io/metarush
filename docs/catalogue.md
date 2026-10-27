# The catalogue: what MetaRush can do

Everything MetaRush can do is an entry in one catalogue that people browse and agents search. It has three
kinds of entries:

- **Operations** — every operation of the registry (`internal/ops`), with its command and the words people use for
  it. Agents find them with `find` (MCP) or `metarush ops find`, and run them by name.
- **First-party abilities** — what MetaRush already does, as about fifty abilities in the menu: understand
  footage, fix sources, make media (with each kind of AI work), bring things in, shape the cut, check and deliver.
  Each runs as the operation it is, sometimes with fields fixed ("Steady shaky footage" is `asset.fixes.set` with the
  steady fix ticked), and says what it needs on a computer.
- **Added abilities** — the abilities a workspace added (docs/abilities.md), once checked; found and run under
  their manifest's rules.

`metarush catalogue list` and Library → Abilities show the first-party and added abilities side by side, each saying
whether it **works on this computer now** or what it **needs first**.

## Where it comes from

A first-party ability is a small annotation next to its operation, never a hand-written list:

```go
ops.Offer("footage.moments", ops.Offering{ID: "best-moments", Group: "understand", Title: "Best moments",
	Does:  "Finds the moments worth a look across a project or one clip, from what was said, pauses and picture changes.",
	Words: []string{"highlights", "best takes", "hooks"}, Needs: ops.Needs{Works: "transcripts"}})
```

- `ID` is stable: receipts and recipes name it. It is never renamed or reused; one taken away goes into
  `catalog.RetiredFirstParty` with why.
- `Group` is the part of the menu; `Title` and `Does` are what people read (plain words, checked by the test).
- `Input` are fields of the operation's input the ability always runs with (a preset). An operation is the ability
  "as it is" for at most one annotation.
- `Words` are other words people use for it (find matches them).
- `Needs`: `Works` — items of the first run's "what works here" (`transcripts`, `editing`, `renders`,
  `checks`, `scratch-voice`), every one needed (transcripts need FFmpeg too, so they come with `editing`); `OS`
  (`mac`, `linux`) and `Partly` (what it still does on the other systems: "only plain-colour backgrounds", so it works
  there in part and says so); `Programs` (allowlisted programs MetaRush doesn't set up by itself: `git`, `gh`,
  `yt-dlp`, `whisper-cli`…); `Keys` (integrations, any one is enough); `Claude` (it uses the person's Claude Code,
  counted toward their plan); `Priced` (priced first, paid to the service); `Leaves` (what leaves this computer when
  it runs; required with a key or Claude Code); `Setup` (a check another package registers with
  `ops.RegisterReady`: `studio-voices`; `on-screen-helper`, which asks `vision.CanRun`: a Mac, FFmpeg and the
  helper or what builds it; and `local-<model>` for each model on this computer, which asks its adapter whether it is
  set up here).

The kinds of AI work (`ai.make`) are annotated from the AI services' catalogue (`internal/aiservices/catalog.json`):
each needs a key of any service with a model of that kind, so a new kind or service shows up by itself. The models on
this computer are offered as their own entries (`offline-voices`, `ai-larger-pictures`, `ai-cut-outs`): free,
no key, nothing leaves, and works here once the model is set up (`metarush ai local setup`, owners).

`internal/catalog/firstparty.json` is generated from the annotations at build time:

```bash
METARUSH_UPDATE_CATALOGUE=1 go test ./internal/catalog -run TestFirstPartyCatalogue   # writes the file
go test ./internal/catalog                                                           # fails while it is out of date
```

The generator refuses an unknown group, operation, field, program, integration or check, a missing title or
sentence, transcripts without FFmpeg, partial support without systems, a key or Claude Code without what leaves,
and an ID the file had that is neither offered nor retired.

**What it costs** is one of four classes everywhere (find, describe, the menu, the dashboard): `free` — no price and
no key, the only one that says "it costs nothing"; `priced` — each run priced first and paid to the service;
`keyed` — no price of its own, but a key to a service, which may charge (an added ability whose manifest says
`free` but uses a key is `keyed`); `plan` — it uses the person's Claude Code.

## Names, and what receipts record

A first-party ability's name is `metarush/<id>` (`metarush/steady-footage`): no operation (dotted) or added ability
(lowercase and dashes) is named so. An added ability can't take a first-party ID, present or retired either:
`ability check` and `ability approve` refuse it, so `metarush catalogue show <name>` — which looks for an added
ability by that name first, then a first-party one by its full name, its bare ID or the operation it is — always
means one thing. Its `madeWith` is the same name; a receipt or a recipe step records it with
MetaRush's version beside it. An added ability's `madeWith` is `name@version`.

## What works here

Whether an ability works on this computer is computed from this computer's state, the same way `metarush setup`
and Home's Getting started say it (op `setup.status`): the first run's items, the system, the programs, the
keys (only whether one is there; never read out), whether Claude Code is installed and the setup checks. One that
doesn't work says what it needs first, in plain words: "needs a Mac", "needs FFmpeg", "needs an ElevenLabs key",
"needs Claude Code". Nothing is computed without a workspace open (the eval, a program without one): then nobody can
tell, and nothing says either way.

An added ability works here once an owner approved a version and a person set it up on this computer; until then it
says "needs an owner's approval" or "needs setting up on this computer" and can't be run.

## In find

`find` shows each operation once. A first-party ability that is an operation as it is (no fields fixed) lends that
operation its title, group, words and what it needs, and find lists the operation: `footage.moments — … (needs the
transcriber)`. One with fields fixed is an entry of its own (`metarush/steady-footage`); describe gives its
operation's input without the fixed fields, and run sends the caller's input to the operation with them set (another
value for one is refused, and so is a key that is a fixed one in another case — `TICK`, or `ticK` with the Kelvin
sign — since the operation's JSON reads keys without minding case). With `--kind first-party` each first-party ability is itself.

A hit's line says who may run it, whether it is priced or uses the person's Claude plan, what leaves the computer
(added abilities) and what it needs here first.

## The command line and the dashboard

```bash
metarush catalogue list                          # everything, group by group, each saying works here or what it needs
metarush catalogue list --works-now              # only what works on this computer now
metarush catalogue list --group fix --built-in   # one part of the menu; --added for the workspace's own
metarush catalogue show steady-footage           # one ability: what it needs, what it costs, who starts it (--details: what runs it)
metarush catalogue list --json                   # every field (catalog.CatalogueListOut)
```

Library → Abilities opens on the same menu: each group of first-party abilities, then the abilities the workspace
added, with filters (Everything, Works here, Built in, Added, Waiting for an owner). A built-in ability has a page of
its own (`?builtin=<id>`); an added one keeps its page (`?ability=<name>`).

## Measuring it

The tool-selection eval (`internal/catalog/selection.json`) ranks what find searches with no workspace open — the
operations and the first-party catalogue, each operation once — and seven sample abilities
(`internal/abilities/samples/`, invented services) the way a workspace's are ranked. A first-party ability that runs
an operation with fields fixed counts as that operation for a request that wants it. Cases for abilities and
first-party abilities were added after the original 166 (every third held out, written before any tuning).

## The first-party abilities

Generated from `firstparty.json` (a test keeps this list naming every ID).

### Understand footage (10)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `best-moments` | Best moments | `footage.moments` | the transcriber, FFmpeg | — |
| `contact-sheets` | Contact sheets | `footage.sheet` | FFmpeg | — |
| `spoken-language` | Each clip's language | `footage.language` | the transcriber, FFmpeg, whisper-cli | — |
| `faces` | Faces across clips | `footage.faces` | a Mac, the on-screen helper | — |
| `pauses-and-cuts` | Pauses, cuts and loud parts | `footage.evidence` | FFmpeg | — |
| `call-switches` | Picture switches in calls | `footage.switches.find` | FFmpeg | — |
| `scene-descriptions` | Scene descriptions | `footage.describe` | Claude Code | pictures of each scene leave this computer, for Claude |
| `transcripts` | Transcripts and captions | `footage.analyze` | the transcriber, FFmpeg | — |
| `whats-on-screen` | What's on screen | `footage.visuals` | a Mac, the on-screen helper | — |
| `who-speaks` | Who speaks | `footage.speakers.detect` | the transcriber, FFmpeg | — |

### Fix sources (12)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `remove-background` | A photo's subject | `asset.fixes.set` `tick: ["background"]` | a Mac (elsewhere, only plain-colour backgrounds) | — |
| `balance-colour` | Balance colour | `asset.fixes.set` `tick: ["colour"]` | FFmpeg | — |
| `change-a-spoken-word` | Change a spoken word | `cut.say.make` | the transcriber, FFmpeg, ElevenLabs or Google Gemini key, priced first | the new words, with the speaker's words around them, leave this computer for the service that holds the voice copy |
| `clearer-voices` | Clearer voices | `sound.enhance` `mode: "clean"` | FFmpeg | — |
| `even-loudness` | Even out loudness | `asset.fixes.set` `tick: ["loudness"]` | FFmpeg | — |
| `larger-pictures` | Larger, sharper pictures | `asset.fixes.set` `tick: ["larger"]` | — | — |
| `reduce-grain` | Reduce grain | `asset.fixes.set` `tick: ["grain"]` | FFmpeg | — |
| `remove-hum` | Remove hum | `asset.fixes.set` `tick: ["hum"]` | FFmpeg | — |
| `steady-footage` | Steady shaky footage | `asset.fixes.set` `tick: ["steady"]` | FFmpeg | — |
| `studio-voices` | Studio voices | `sound.enhance` `mode: "studio"` | FFmpeg, the studio voice cleaner | — |
| `trim-silent-ends` | Trim silent ends | `asset.fixes.set` `tick: ["ends"]` | FFmpeg | — |
| `cloud-voices` | Voices cleaned in the cloud | `sound.enhance` `mode: "cloud"` | ElevenLabs key, priced first | the voices' sound leaves this computer |

### Make media (24)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `own-presenters` | A person's own presenter | `presenter.make` (people only) | D-ID key, priced first | a video of the person and their filmed consent leave this computer, and the service keeps the presenter until it is retired |
| `brand-from-a-site` | A website's brand | `brand.suggest` | — | — |
| `ai-cut-outs` | AI cut-outs | `ai.make` `kind: "background-removal", model: "local/apple-vision"` | a Mac, the on-screen helper | — |
| `ai-larger-pictures` | AI larger pictures | `ai.make` `kind: "upscale", model: "local/real-esrgan-x4v3"` | the Real-ESRGAN picture model, set up once by an owner | — |
| `ai-music` | AI music | `ai.make` `kind: "music"` | ElevenLabs or Google Gemini key, priced first | your words leave this computer |
| `ai-narration` | AI narration | `voiceover.tts` | ElevenLabs key, priced first | the script's words leave this computer |
| `ai-pictures` | AI pictures | `ai.make` `kind: "picture"` | Google Gemini key, priced first | your words and any pictures you give it leave this computer |
| `ai-sound-effects` | AI sound effects | `ai.make` `kind: "sound-effect"` | ElevenLabs key, priced first | your words leave this computer |
| `ai-speech` | AI speech | `ai.make` `kind: "voice"` | Google Gemini or ElevenLabs key, priced first | the words leave this computer |
| `other-voice` | Another voice | `ai.make` `kind: "voice-change"` | ElevenLabs key, priced first | the recording's sound leaves this computer |
| `cloud-transcripts` | Cloud transcripts | `ai.make` `kind: "transcription"` | ElevenLabs or Google Gemini key, priced first | the clip's sound leaves this computer |
| `code-slides` | Code slides | `resources.make` `kind: "code"` | Node.js and FFmpeg | — |
| `diagrams` | Diagrams | `resources.make` `kind: "diagram"` | Node.js and FFmpeg | — |
| `dubbing` | Dubbing | `ai.make` `kind: "dubbing"` | ElevenLabs key, priced first | the clip's sound leaves this computer |
| `generated-shots` | Generated shots | `generate.shot` | Google Gemini key, priced first | your words and any pictures you give it leave this computer |
| `lip-sync` | Lip sync | `ai.make` `kind: "lip-sync"` | sync. key, priced first | the part of the footage and the new sound leave this computer |
| `fitted-music` | Music fitted to the cut | `sound.music.add` | FFmpeg | — |
| `presenter-clips` | Presenter clips | `ai.make` `kind: "presenter"` | D-ID or HeyGen key, priced first | the sound it reads leaves this computer |
| `offline-voices` | Offline voices | `ai.make` `kind: "voice", model: "local/kokoro-v1"` | the Kokoro voice, set up once by an owner | — |
| `remotion-clips` | Remotion compositions as clips | `remotion.render` | Node.js and FFmpeg | — |
| `remotion-refreshes` | Remotion refreshes | `remotion.refresh.start` | Node.js and FFmpeg | — |
| `repository-cards` | Repository cards | `resources.make` `kind: "stats"` | Node.js and FFmpeg | — |
| `stand-in-narration` | Stand-in narration | `voiceover.scratch` | a system voice | — |
| `voice-copies` | Voice copies | `ai.voices.copy` | ElevenLabs or Google Gemini key, priced first | a recording of the person's voice leaves this computer |

### Bring things in (10)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `art-in-git` | Art kept in git | `resources.pictures.set` | git | — |
| `call-recordings` | Call recordings | `conversation.import` | FFmpeg | — |
| `cloud-folders` | Files from Google Drive or Dropbox | `add.cloud` | rclone | — |
| `footage-files` | Footage from files | `ingest.files` | FFmpeg | — |
| `github-repositories` | GitHub repositories | `resources.fetch` `as: "repo"` | git | — |
| `pictures-and-pdfs` | Pictures, PDFs and fonts | `resources.add` | — | — |
| `remotion-projects` | Remotion projects | `remotion.import` | Node.js and FFmpeg | — |
| `timeline-import` | Timelines from other editors | `timeline.import` | FFmpeg | — |
| `video-links` | Videos from links | `ingest.url` | FFmpeg, yt-dlp | — |
| `web-pages` | Web pages | `resources.fetch` `as: "page"` | Node.js and FFmpeg | — |

### Shape the cut (10)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `automatic-call-cut` | Automatic call cuts | `conversation.cut` | FFmpeg | — |
| `call-clean-up` | Call clean-up | `conversation.cleanup` | FFmpeg | — |
| `caption-looks` | Caption looks | `cut.captions` | — | — |
| `cover-call-names` | Cover printed names | `cut.names` | FFmpeg | — |
| `find-and-replace` | Find and replace in transcripts | `transcript.replace` | — | — |
| `matched-webcams` | Matched webcams | `conversation.match` | FFmpeg | — |
| `remove-ums` | Remove ums | `cut.removeWords` `cleanup: true` | the transcriber, FFmpeg | — |
| `shorts` | Shorts | `footage.short` | FFmpeg | — |
| `tighten-pauses` | Tighten pauses | `cut.pauses` | the transcriber, FFmpeg | — |
| `trim-flashes` | Trim flashes | `cut.flashes` | FFmpeg | — |

### Check and deliver (9)

| ID | Ability | Runs | Needs | What leaves |
|---|---|---|---|---|
| `caption-check` | Caption check | `captions.check` | — | — |
| `captions-files` | Captions files | `captions.file` | — | — |
| `translated-captions` | Captions in other languages | `captions.translate` | Claude Code | the captions' words leave this computer, for Claude |
| `changes-as-pull-request` | Changes back as a pull request | `remotion.push.start` | git, gh | the version's changes leave this computer, for its repository |
| `delivery` | Delivery | `deliver.export` | — | — |
| `post-to-youtube` | Post to YouTube | `publish.send` | — | the version's video, title, description and captions leave this computer, for YouTube |
| `quality-check` | Quality check | `qa.run` | FFmpeg | — |
| `ready-to-deliver` | Ready to deliver? | `delivery.ready` | — | — |
| `share-for-review` | Share a version for review | `share.create` | media kept in a cloud bucket | the review copy, and its words if you include them, go to people outside the workspace through your cloud bucket |
| `timelines` | Timelines for other editors | `export.timeline` | — | — |
| `delivery-notes` | Titles, chapters and show notes | `delivery.notes.set` | — | — |
