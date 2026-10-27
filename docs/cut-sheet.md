# Cut sheets

A **cut sheet** (`.cut.md`) is a cut written as Markdown: a short settings block, then one numbered line per clip,
a line for each transition, the narration script, and sections for graphics, sound and captions. You can read it like a script, change a
time or move a line, and save it as a new revision. Claude Code reads and writes the same format, and because it is
plain text it diffs well and can live in git.

It is a **projection** of the edit spec, not a second source of truth: the JSON spec in the database
stays canonical, and a cut sheet converts to it and back **without loss**. `Parse(Print(spec))` is always the same
spec (same `editspec.Hash`), which is proven by fixtures, every revision the acceptance run creates, 1,500 random
specs per test run, two native Go fuzz targets and a completeness test that sets every field of every spec type.

- [Where cut sheets show up](#where-cut-sheets-show-up)
- [A complete example](#a-complete-example)
- [Settings](#settings)
- [Clips](#clips)
- [Quoted words instead of times](#quoted-words-instead-of-times)
- [Transitions](#transitions)
- [Narration](#narration)
- [The mix](#the-mix)
- [Graphics, sound and captions](#graphics-sound-and-captions)
- [Notes, reasons and evidence](#notes-reasons-and-evidence)
- [The `{…}` block](#the--block)
- [Times and numbers](#times-and-numbers)
- [Saving, conflicts and locks](#saving-conflicts-and-locks)
- [Project folders and git](#project-folders-and-git)
- [Problems the checker reports](#problems-the-checker-reports)
- [Reserved and extension points](#reserved-and-extension-points)

## Where cut sheets show up

| Where | What |
|---|---|
| `metarush cut show <project> [--revision <id\|rN>] [--format md\|json] [--out <file>]` | Print a revision as a cut sheet (the latest by default). A project with no cut yet gets a starting sheet that lists its footage. |
| `metarush cut check <file>` | Read the sheet, check it against the project and report problems by line, the changes in plain words, where quoted words landed, and a confirm token. |
| `metarush cut diff <file>` | The changes compared with the revision the sheet came from and, if someone saved since, with the latest. `--details` adds the lines that differ. |
| `metarush cut apply <file> --confirm <token> [--write]` | Save the sheet as a new revision. `--write` rewrites your file as the new revision. In a terminal, leaving out `--confirm` shows the changes and asks first. |
| `metarush cut transition <project> <clip> <type> [seconds] [--direction] [--easing] [--lead <s> \| --carry <s> \| --auto] [--end]` | Change one cut of the latest version without editing a sheet: the same words as a `↳` line (see [Transitions](#transitions)). It shows the change and its warnings; `--confirm <token>` (or a yes in a terminal) saves it. |
| `metarush cut add\|move\|trim\|split\|delete\|delete-range\|remove-words <project> …` | One small change to the latest version without a sheet, naming clips as the sheet does (or by number) and times as the sheet writes them (`0:12.4`, `+0.5`, quoted words): `cut move <p> kitchen start`, `cut trim <p> 2 --end -0.5` (`--roll`, `--slip`), `cut split <p> hook '"what would"'`, `cut add <p> interview 0:12 0:18 --after hook`, `cut delete <p> broll`, `cut delete-range <p> 0:10 0:14`, `cut remove-words <p> "um, anyway"` (`--which`, `--all`; `--ums` for suggestions, `--pick 1,3`), `cut pauses <p> --longer-than 1s --to 0.5s` (long pauses between words shortened; `--pick 1,3`). Each shows the change first; `--confirm <token>` saves it, `--dry-run` never does. See [Small edits](#small-edits-without-a-sheet). |
| `metarush revision compare <a> <b> --project <p> --format md` | A Markdown page: what changed in words, then the cut sheet lines that changed. |
| `metarush project checkout\|status\|pull\|push` | A cut as files you can keep in git (see [Project folders](#project-folders-and-git)). |
| Dashboard: project → **Script** | The cut sheet three ways: **Formatted** (the default: read like a script, with who says what clip by clip, why each clip is there, transitions and their sound phrases as chips, narration, graphics (a project motion graphic by its name, with whether a person approved the version the cut uses and a link to its review), sound and captions in plain words; a clip plays the cut from there; anything not recognized is shown as written), **Markdown** (the sheet as text, highlighted, read only) and **Edit** (live checking, apply as a new revision). Any version opens with `?rev=` (older ones read only); **Compare versions** shows two. "View script" on the Current cut and on every version in Revisions opens it. The old tab name (`?tab=sheet`) still works. |

Every command takes `--json` (stable output for scripts and Claude Code) and `--details` (adds sheet positions,
problem codes, the dashboard link to the Cut sheet tab and the exact next command to the plain output; in JSON the
link is `sheetUrl`). The ops behind them are `cut.show`,
`cut.check`, `cut.diff`, `cut.apply`, `cut.compare`, `cut.transition`, `cut.add`, `cut.move`, `cut.trim`,
`cut.split`, `cut.delete`, `cut.deleteRange`, `cut.removeWords`, `project.checkout`, `project.status`,
`project.pull` and `project.push`.

### Small edits without a sheet

One clear change ("move the kitchen clip to the start", "cut out the part where I say 'um, anyway'", "end the second
clip half a second earlier") is one editing verb. It builds the typed ops a saved sheet would (`moveClip`,
`trimClip`, `splitClip`, `insertClip`, `removeClip`, `deleteRange`, `removeWords`) against the latest version, so it
is checked, confirmed, locked and rebased the same way, and the plain output reads like `cut check`: the change in one
sentence, how the length changes, what changes clip by clip and what to watch for at the cuts. Words come out in the
pauses around them, keeping the same air as quoted in and out points; a clip cut in two continues as `<clip>-2`
(the sheet then shows two clips). The same edits are what the dashboard's timeline and transcript make by hand.

## A complete example

```markdown
---
cut: 1
title: "What would you build?"
project: prj_01jabcdefghjkmnpqrstvwxyz0
base: rev_01jabcdefghjkmnpqrstvwxyz0   # r3 · 18 Oct 2026 14:02 · by Ada
output:
  shape: vertical   # 1080 × 1920
  frame rate: 30
  platform: reels
  framing: faces   # crops keep the main face in frame (clips with a focus of their own keep it)
captions:
  on: true
  style: emphasis
  position: lower
  words per line: 4
  timing: transcript
  emphasize: ["twice", "nobody"]
mix:
  music: -4 dB
  ducking:
    music under narration: -16 dB
brand:
  accent: "#E0A458"
  font: Fraunces
footage:
  interview-a: ast_01jabcdefghjkmnpqrstvwxyz0 sha256:9f2c4e1a7b3d5f60   # interview-a.mov · 0:27.3 · Ada
  broll-c:     ast_01jabcdefghjkmnpqrstvwxyz1 sha256:0d1e2f3a4b5c6d7e   # broll-c.mp4 · 0:10
  music-bed:   ast_01jabcdefghjkmnpqrstvwxyz2 sha256:1111222233334444   # music-bed.m4a · 0:30
---

An intimate 20-second story: doubt, two tries, the question.
Vertical for Reels; captions in the lower band.

## Clips

↳ fade in 0.5 s

1. hook: interview-a 0:00.4 → 0:04.2 · fill · focus 42% across, 38% down
   > Ada: “When I started building my …” … “… robot I had no idea.”

   Why: Opens on the confession; the doubt is the hook.
   Evidence: transcript · interview-a 0:00.5 → 0:04.1 · quote "When I started building my first robot I had no idea."
   Evidence: frame · interview-a from 0:02 · note "Face is left of center; focus kept on her."

   The director likes the breath before 'I had no idea'. Keep it.

↳ dissolve 0.4 s

2. garage: broll-c 0:01 → 0:03.25 · speed 1.25× · muted · label "The garage"
   > (no words spoken here)

   Why: B-roll of the workbench while she talks.

↳ cut

3. twice: interview-a 0:09.9 → 0:13.25 · volume 110%
   > Ada: “So we built it twice, …” … “… we built it very quickly.”

   Why: The repetition lands the point.
   Evidence: transcript · interview-a 0:10 → 0:13.1 · quote "So we built it twice, so we built it very quickly."

↳ fade through black 0.8 s

4. question: interview-a 0:15.8 → 0:19.5 · fades out 1.2 s · locked
   > Ada: “What would you build if nobody was watching?”

   Why: End on the thesis line.

   Locked: the client approved this ending.

   Do not trim the last word.

## Voiceover

- intro: "Nobody watched her first robot fail." · 0.4 s after twice starts
  > starts at 0:06.8 · fits (2.3 of 3 s)
  Delivery: quiet, a little amused

- turn: "So she built it again." · after the previous line
  > starts at 0:09.4 · fits (1.5 of 2.4 s)

## Graphics

- who: lower-third.clean at 0:00.8 for 3.2 s · with name "Ada", title "Builder and teacher", align "left"
  > over hook and garage

  Why: Introduce her early, away from the captions.

## Sound

- bed: music-bed 0:00 → 0:18 · music · starts at 0:00 · volume -16 dB · fades in 1 s · fades out 2.5 s · ducks under speech
  Rights: license "CC BY 4.0" · attribution "A composer" · source "a music library"

  Why: Warm pad under the talk; ducks for speech.
```

Lines that start with `>` and the `# …` comments in the settings are **derived**: MetaRush writes them (the first
and last words spoken in a clip, which clips a graphic shows over, file names, lengths and who speaks) and ignores
them when it reads the sheet. Change times, not quotes. People are named the way they were named in the library's
speakers; a voice nobody named shows as "Speaker 2" when there are several voices to tell apart, and short clips
name the speaker at each change of voice (`> Ada: “So what would you build?” Speaker 2: “A robot.”`).

## Settings

The block between the two `---` lines is YAML. Keys are case-insensitive and `-`/`_` count as spaces
(`words-per-line` is `words per line`).

| Key | Meaning |
|---|---|
| `cut: 1` | Required. The format version. |
| `title` | The cut's title. A `# Title` heading at the top of the body is accepted too (it must match). |
| `project` | Which project (needed for a project's first cut; otherwise `base` says it). |
| `base` | The revision this sheet was written from. Don't edit it: it is how MetaRush knows what you changed and whether someone saved since. |
| `branch` | Optional; a name like `short-version` saves to another branch instead of `main`. |
| `change` | Optional: `change: "what was asked"` or `change: {request: "…", why: "…", note: "…"}`, recorded with the new revision; `note` is a few words naming the new version ("flicker fixes and headline cards"; without one MetaRush takes the request's first clause). Never written by MetaRush. |
| `output` | `shape` (`vertical` 1080×1920, `landscape` 1920×1080, `square` 1080×1080; or a preset name), `size: WxH` (only when it differs from the shape), `frame rate` (24, 25, 30, 50, 60), `platform` (generic, youtube, shorts, tiktok, reels: sets the safe margins), `background` (`"#RRGGBB"`), `framing` (`faces`: crops keep the main face of each clip in frame, from its on-screen index, and clips with a focus of their own keep it; `follow` (also `follow movement`): crops move with the subject as it moves, jumping only at the footage's own cuts, and only when a person chose it; `center`: crops keep the middle. New cuts are saved with `faces`; a sheet without the line, from a cut made earlier, keeps the middle however its footage is indexed later, and adding the line saves that change as a new version). |
| `captions` | `on` (true/false), `style` (clean, emphasis, boxed), `position` (bottom, lower, middle, top), `size` (small, medium, large), `words per line` (1–12), `uppercase`, `speaker names` (show who is speaking above each line; narration is "Narrator" when captions mix it with dialogue), `timing` (where the words come from: `transcript` or `dialogue` for what people say on camera, `voiceover` for the narration, `both`, or `manual`: manual lines go in `## Captions`), `emphasize` (a list of words), `locked`. |
| `voiceover` | The narration as a whole (the lines are in `## Voiceover`): `layout` (`line by line`, the default, or `one recording`), `speaking rate` (words a minute, for fit checks until a recording measures it), `rate from` (default, measured, user), `fitting` (`auto`, `ask` or `shorten`: what happens when a line is too long for its spot), `pause` (the default gap before a line that follows the previous one, like `0.3 s`), `script` (`original` or `fitted`: which version of the script the lines are; with `fitted` and `parked` as JSON, see Narration), `locked`. |
| `mix` | The sound levels: `camera sound: off` (mute the clips' own sound), `clip sound`, `narration`, `music`, `sound effects` (each a level like `-6 dB`, optionally `, muted` or `, solo`; `normal` for none), `ducking` (`on`, `off`, or a group with `music under narration`, `clip sound under narration`, `music under speech` in dB, `dip time` and `recovery` in seconds), `loudness` (a target like `-14 LUFS`, or a group with `target`, `true peak` and `normalize`), `even out voices` (`on`/`off`), `blend at cuts` (seconds), `locked`. `mix: {}` keeps every default. |
| `brand` | `primary`, `accent`, `text`, `background` (`"#RRGGBB"`, in quotes because of the `#`), `font` (Inter, Fraunces, JetBrains Mono), `font file`, and the brand's logos: `logo` (the main one), `logo for dark backgrounds`, `logo for light backgrounds` — names from `resources:`; a graphic showing the main logo then shows the one made for its background. `brand: {}` is an empty brand. |
| `footage` | Short names for footage, like imports at the top of code: `name: ast_… sha256:<at least 8 hex>`. MetaRush makes each name from the clip's title in the library (`Ada's interview` → `adas-interview`), or from its file name; the comment shows the title when it differs from the file name, the file name and the length. The body uses the names; raw `ast_…` IDs work too. The hash pins the content: if it doesn't match the workspace's footage, the check fails. |
| `fixed` | The footage the cut plays from a fixed copy ([sources](sources.md)): `kitchen: sha256:<the copy's 64 hex>`, with a comment saying what it fixes ("steadier picture"). MetaRush writes it: footage put into the cut for the first time plays the fixed copy it uses then, and `metarush cut use-fixed` (the card's "Use the steadier picture in …") moves the cut to a copy or back to the original as a new version. Deleting a line plays the original again; a copy that isn't ready, or isn't that footage's, is an error. |
| `motion` | The project motion graphics the cut uses, like footage: `route-map: sha256:<full version>` (the comment says whether a person approved it). MetaRush writes one line per graphic used with a single version; the graphics lines then use the short version. See [Graphics](#graphics-sound-and-captions). |
| `extra` | Spec fields that have no other spelling yet, by their JSON names (see [the `{…}` block](#the--block)). |

Any group (`output`, `captions`, `voiceover`, `mix`, `brand`) also accepts the spec's own JSON field names (for example
`maxWordsPerLine: 4`). Unknown keys are errors with a list of the ones that exist.

The text between the settings and `## Clips` is the **treatment**: notes about the whole cut, shown to reviewers.

## Clips

```markdown
3. question: interview-a 0:11.5 → 0:18.63 · focus 45% across · locked
```

`N. name: footage start → end · setting · setting …`

- **The number** is decoration; the order of the lines is the order of the clips. Move a line to reorder. A note
  warns when the numbers don't match the order; MetaRush renumbers when it writes the sheet.
- **The name** (`question`) is how the clip is known in the history and in diffs: lowercase letters, digits, `-`
  and `_`, starting with a letter. Leave it out (`3. interview-a 0:11.5 → …`) and MetaRush makes one up from the
  footage name.
- **Footage**: a short name from the settings, or an `ast_…` ID. An unknown name is an error that suggests the
  closest names.
- **Start → end**: source times, see [Times](#times-and-numbers). `->` works for `→`, and `from 0:11.5 to 0:18.63`
  is the same thing.

Clip settings, each after a `·` (or `|`):

| Setting | Meaning |
|---|---|
| `speed 1.5×` | Play faster or slower (0.25× to 4×; `x` works for `×`). |
| `volume 80%` | The clip's own sound level (0% to 200%; 100% is unchanged). |
| `muted` | No sound from this clip. |
| `fill` / `fit` | Fill the frame by cropping, or show the whole picture with bars. |
| `focus 30% across, 40% down` | What to keep in view when cropping (0% is the left or top edge). Either part alone is fine. |
| `focus on faces` | Keep the main face in frame when cropping, from where faces are in the clip ([what MetaRush sees](visual-index.md#automatic-framing-from-faces)). With a focus as well, the focus is used where no face was seen. |
| `follow movement` | Move the crop with the subject as it moves: the main face, or what stands out where no face is seen; smoothly, jumping only at the footage's own cuts ([following movement](visual-index.md#following-movement)). `follow the action` and `follow the subject` mean the same. |
| `zoom 1.2×` | Zoom in (1× to 3×). |
| `fades out 1 s` | Fade the clip out at its end. |
| `label "The garage"` | A display name for the clip. |
| `locked` | Nothing may change this clip unless the person asks to unlock it. |
| `{field: value}` | Anything else, see [the `{…}` block](#the--block). |

### A composition of a Remotion project as a clip

A project's Remotion sources ([remotion-sources.md](remotion-sources.md)) turn compositions into clips. A clip
line can name the composition instead of footage:

```markdown
4. teaser: remotion kite KiteTeaserV3 0:00 → 0:12
5. promo: remotion kite KiteTeaserV3@3 · with headline "Ship faster", seconds 12
6. intro: remotion kite Open
```

`remotion <source> <Composition>[@<version>]`: the source's short name (`metarush remotion list`), the composition's
ID, and optionally the version: its number (`@3`) or at least 7 characters of it (for the version that was brought
in, also the project's own commit). Without a version, the newest version a person approved. The range is optional
(without one, the whole composition); `· with …` gives its props (text in quotes, numbers, `true`/`false`, lists and
`{…}` objects), merged over its defaults, and other clip settings work as usual. When the sheet is checked, the line
becomes the clip MetaRush made of exactly that version, composition, props and range, so the sheet MetaRush writes
back names it in `footage:` like any clip; its comment says where it came from (`made from “kite-teaser”: Open,
version 2`). A footage name that happens to be `remotion` still works as footage. Problems: `bad_remotion` (the line
can't be read), `remotion_unknown` (no such source or composition; the hint lists them), `remotion_ambiguous`,
`remotion_not_approved` (a person approves the version first) and `remotion_not_made` (make the clip first with
`metarush remotion render` or `remotion use`).

## Quoted words instead of times

A clip can start and end on words that are said, instead of times:

```markdown
1. doubt: interview-a from "When I started building" to "what I was doing"
2. answer: interview-a 0:20.9 → "fails first"
```

MetaRush finds the words in that footage's transcript (its word timings) and places the cut in the silence around
them: the start just before the first word, the end just after the last one (0.1 s and 0.15 s of air when the gap
allows, otherwise halfway into the gap), so a word is never clipped and nothing of the neighbouring word is
included. Matching ignores case and punctuation (`im` matches `I'm`), tolerates words split or joined differently
(`every one` / `everyone`) and small spelling differences (with a note saying what it used).

- **Not found**: an error that shows the closest words in the transcript and their time.
- **Said more than once**: an error that lists every place with its time and the words around it. Add a word or two,
  or use a time. When one end is a time or the other phrase, only matches that make sense (start before end) count.
- **Sentence-level transcripts** (no word timings) give an approximate point, with a note.

After saving, the sheet shows the exact times (and the derived quote). Quoted words are a way to write a time,
never stored as words: clock precision is always available.

Quotes read the transcript as it is now, with people's fixes ([transcripts.md](transcripts.md)): once "Maple Grove"
was joined into "Maplegrove", write `"Maplegrove"`. A fixed word keeps the time the old words took (a joined word
spans both), so a quote of it lands exactly where the old words were and the cut doesn't move; the `>` lines under
each clip show the fixed words too. A saved version still plays the words it was made with until the fixes go into a
new version.

## Transitions

A `↳` line (or `=>`) goes on its own line just above the clip it brings in. Above the first clip it describes how
the video starts.

| Write | Spec type | What you see |
|---|---|---|
| `↳ cut` | `cut` | A straight cut (the same as no line). |
| `↳ fade in 0.5 s` | `fade` | The clip fades in from the background color. |
| `↳ dissolve 0.4 s` | `crossfade` | The previous clip dissolves into this one (they overlap). |
| `↳ fade through black 1 s` | `dip-to-black` | The previous clip fades to black, then this one fades in. |
| `↳ fade through white 1 s` | `dip-to-white` | The same through white. |
| `↳ wipe left 0.5 s` | `wipe` | An edge sweeps across and reveals this clip. |
| `↳ slide left 0.4 s` | `slide` | This clip slides in over the previous one. |
| `↳ push left 0.4 s` | `push` | This clip pushes the previous one out of the frame. |
| `↳ zoom 0.4 s` | `zoom` | The picture zooms in and through into this clip. |
| `↳ whip right 0.3 s` | `whip` | A fast blurred swish into this clip. |

`crossfade`, `cross dissolve`, `fade`, `dip to black`, `dip to white`, `whip pan` and the spec names are accepted
too. The length is optional. Wipe, slide, push and whip take the way the picture moves after the name (`left`,
`right`, `up`, `down`; left when it is left out); on other transitions a direction is an error (`no_direction`).
`metarush edit transitions` lists them all with their usual lengths.

After the picture, `·`-separated phrases say the rest:

| Write | Spec fields | What happens |
|---|---|---|
| `· slows down` | `easing: ease-out` | The pace of the movement: `smooth` (the default), `even speed`, `speeds up`, `slows down` (`ease-in-out`, `linear`, `ease-in`, `ease-out` work too). |
| `↳ cut · Sam's voice starts 0.4 s early` | `audio: {mode: lead, duration: 0.4}` | A J cut: this clip's sound starts before we see it. The name is whoever MetaRush hears in the clip (just `voice starts 0.4 s early` when nobody is named); it is not stored. `J-cut 0.4 s` works too. |
| `↳ cut · voice carries 0.3 s` | `audio: {mode: carry, duration: 0.3}` | An L cut: the previous clip's sound carries on over this one (`Sam's voice carries 0.3 s`, `L-cut 0.3 s`). |
| `↳ cut · sound changes in a pause` | `audio: {mode: auto}` | MetaRush moves the sound change into the nearest pause so no word is cut (`auto`; `… within 0.6 s` limits how far). |
| `↳ dissolve 0.5 s · sound changes at the cut` | `audio: {mode: cut}` | The sound changes with the picture. |
| `… with a 0.2 s blend` / `… with no blend` | `audio.blend` | After any sound phrase: how long the two sounds blend. |

A phrase MetaRush doesn't know is an error (`unknown_transition_phrase`) whose hint lists these. A combination no
phrase can say (for example a sound change with a length that only a split takes) is kept exactly in a `{…}`
block. Changing a `↳` line saves as one `setTransition` change, which conflicts with someone else's change to
either clip and is refused when the clip before the cut is locked and the transition reaches into it.

## Narration

The narration is the script of the cut: `## Voiceover` lists the lines in the order they are spoken. Each line is a
bullet with a name, the words in quotes and **where it starts**, in plain words:

```markdown
## Voiceover

Recording: narration-take 0:00 → 0:42.5 · when hook starts · recorded

- intro: "When I started, I had no idea." · 0.5 s after hook starts · voice scratch
  Delivery: warm, a little slower
  Take: narration-01 0:00 → 0:02.4 · generated · AI voice · plays the cleaned version

- "So we built it twice." · after the previous line
- end: "What would you build?" · when question ends · locked
- laugh: "Listen to that." · at interview-a 0:12.3 in hook "right after she laughs"
- last: "Thanks for watching." · at 0:20
```

| Start point | Meaning |
|---|---|
| `when hook starts` / `when hook ends` | At the start or end of a clip; the line moves with the clip when the cut changes. |
| `0.5 s after hook starts`, `0.3 s before hook ends` | The same, a little later or earlier. |
| `at interview-a 0:12.3` (`in hook`, `"what happens"`) | At a moment in the footage: it follows the clip that shows it (`in hook` when several do). |
| `after the previous line`, `0.8 s after the previous line`, `right after the previous line` | When the line before it ends (with the default pause, a set pause, or none). |
| `0.3 s before the previous line ends` | Overlapping the end of the line before it. |
| `at 0:20` | At a clock time in the finished video (only when the person names one). |

A line without a start point follows the previous one, and a line without a name gets one (`line-2`).
After a line come its settings, each after a `·`: `voice scratch` (the free placeholder voice) or
`voice elevenlabs "Rachel" id <voice id> model <model id>`, `volume -2 dB`, `status draft | scratch | generated |
recorded | imported | final`, `locked`, or a `{…}` block. Under it:

- `Delivery: …` how to say it (sent to an AI voice as context, never spoken).
- `Take: <footage> 0:00 → 0:02.4 · …` the audio it plays: `scratch`, `generated`, `recorded` or `imported`,
  `AI voice`, `speed 1.04×`, `cleaned version <footage> "<preset>"` (with `shifted 0.12 s` when the cleaned file is
  offset), `plays the cleaned version`, and `id tk_…`. MetaRush writes takes when it makes or imports audio.
- `Why:` and `Evidence:` lines, as for clips.
- `> starts at 0:06.8 · fits (2.3 of 3 s)` is **derived**: where the line lands in the cut and whether it fits
  before the next one. MetaRush writes it; it is ignored when the sheet is read.

`Recording: …` (at the top of the section) is one continuous read placed by its own start point; with
`layout: one recording` it plays instead of the lines' own takes. Settings for the narration as a whole are in the
`voiceover:` group at the top.

The lines are the script version in use. When the person pasted or imported a whole script, the script exactly as
they gave it follows the lines under its own heading, between fences, and is never rewritten:

````markdown
### Original script

```text
Every morning starts in the kitchen.

Then the garden wakes up.
```
````

The fence is longer than any run of backticks in the script; a script with characters a fence can't hold
(carriage returns, other control characters) is kept as JSON in the settings instead (`original: "…"`). When
Claude fitted the script to the video, the `voiceover:` group says which version the lines are
(`script: original` or `script: fitted`) and keeps Claude's version (`fitted: {lines: […], note}`: each line's
`id`, the person's `original` words, Claude's `text`, `anchor`, `reason`, `kept`) and the lines of the version
not in use (`parked: […]`) as JSON, so switching versions and reading the sheet back lose nothing.

## The mix

The `mix:` group in the settings sets the sound levels:

```yaml
mix:
  camera sound: off
  music: -6 dB
  narration: 2 dB, solo
  ducking:
    music under narration: -14 dB
    dip time: 0.25 s
  loudness: -14 LUFS
  even out voices: on
  blend at cuts: 0.1 s
```

Levels are in decibels: 0 dB is unchanged, -6 dB is about half as loud. `camera sound: off` mutes every clip's own
sound (one clip: `· muted` on its line). With narration, music and clip sound dip while the voice speaks
(`ducking`), and the whole video is set to the platform's loudness unless `loudness` says otherwise.
`even out voices` (`levelSpeech`) turns each clip with speech toward the same loudness, so people recorded on
different cameras or microphones sound alike; `blend at cuts` (`cutBlend`) blends the sound over a few frames at
straight cuts so they never click. New cuts start with both on.

## Graphics, sound and captions

**`## Graphics`** (overlays: vetted templates, see `metarush edit templates`, or a project motion graphic a person
approved):

```markdown
- who: lower-third.clean at 0:00.8 for 3.2 s · with name "Ada", title "Builder and teacher" · locked
- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from "Lisbon", to "Porto"
```
`at` is a time in the finished video, `for` how long it shows; `with` gives the template's fields (text in quotes,
lists as `["a", "b"]`). The derived `> over …` line says which clips it covers.

**Layout pack parts** ([layout-packs.md](layout-packs.md)): a part of the project's layout pack can stand in
the template's place — `- intro: title card at 0:00 for 3 s · with title "Spring launch"` (title card, lower third,
end screen, quote card, chapter card; a variant after a colon: `lower third: guest`, or the names `title-card`,
`lower-third:guest`). Checking the sheet draws it with the pack; MetaRush writes it back with the template (or motion
graphic) it was drawn with and the part after `as`: `- intro: title.card at 0:00 for 3 s · as title card · with …`.

**Graphics that last a section or the whole video** ([call-recordings.md](call-recordings.md#graphics-that-last-a-section-or-the-whole-video))
name clips instead of times, so they follow every edit, and can change their fields while they stay on screen:

```markdown
- names: name.bar for the whole video · with name "Lena", cover {x: 0.0, y: 0.89, w: 0.2, h: 0.11}
  Change: at lena-call 2:15.40 · with name "Maya"
  Change: at lena-call 2:31.75 · with name "Lena"
- mark: logo.corner from open to seed-swap · with logo "logo"
```
`for the whole video`, `from <clip> to <clip>`, `from <clip> to the end` and `from the start to <clip>` set the span
(spec `span`); an offset or a length inside it goes in the `{…}` block (`{at: 1.5, duration: 20}`; no length = to the
span's end). Each `Change:` line under a graphic is a moment in a footage (its short name, and the time in that
footage, to the hundredth): wherever the cut shows that footage from that moment on, until its next change, the
graphic shows with those fields (spec `changes`). Only templates made to stay up (`name.bar`, `logo.corner`) and
motion graphics with `meta.persistent` may span longer than their usual limit (`overlay_too_long`); a change can't
swap a picture (`change_resource`); a change in footage the cut doesn't show is never seen (`change_unseen`).

A **project motion graphic** ([motion-graphics-as-code.md](motion-graphics-as-code.md)) is named by its folder name
and its version: `motion route-map@3fa9c2d1` (`motion: route-map@…` and a bare `route-map@…` work too). The version is
what `metarush motion check` printed: at least 8 characters of its SHA-256, or all 64. MetaRush pins the full version
under `motion:` in the settings (`route-map: sha256:3fa9c2d1…   # approved by Ada · 18 Oct 2026`), so printing and
reading a sheet keeps the exact version; a cut that uses two versions of one graphic writes each in full on its
line instead. A graphic that imports a **shared library** names the library's version after a `+`:
`motion end-card@3fa9c2d1+brand-theme@7e3a91c0` (pinned under `motion:` the same way); a line that names no library
version gets the newest one a person approved when the sheet is checked. A short version without a `motion:` line is looked up among the versions checked in the workspace when
the sheet is checked (`motion_unknown` when there is none, `motion_ambiguous` when several match). In the spec it is an
overlay with `motion: {name, hash}` and no `template`; its `with` fields are checked against the component's own
schema, and a version no person approved is refused (`motion_not_approved`, which names the approval step).

**Logos and pictures** (resources): a graphic names a resource by its short name, and the settings pin each
name to an exact version:

```markdown
resources:
  logo: res_01m5… sha256:e9dab12c…   # “Acme logo” · SVG
  deck: res_01m5… sha256:4be0a7f1…   # “Pitch deck” · PDF · a newer version is available
---
## Graphics
- title: title.card at 0:00.3 for 2.6 s · with title "Acme", logo "logo", image "deck#3"
- mark: logo.corner at 0:00 for 30 s · with logo "logo", corner "top-right", size "small"
```
Name a resource that isn't pinned yet (`metarush resources list --project <project>` lists the names) and checking the
sheet pins its current version for you (warning `resource_pinned`); a name the project can't use is `resource_unknown`.
A newer version changes nothing until you update it (`metarush cut update <project> --resources logo`, or edit the
pin). `brand:` can name a font file: `font file: brand-font`.

**Slides** are full-frame graphics for videos that explain software; they cover the picture while they show
and name resources like any graphic (a code slide, a diagram, a web page snapshot with `#2` for the phone picture and
`#3` for the whole page):

```markdown
- intro: slide.title at 0:00 for 3 s · with title "Widget", kicker "Part 1", subtitle "A job queue in one binary", logo "logo"
- why: slide.bullets at 0:03 for 6 s · with title "Why Widget", bullets ["One binary", "Retries built in", "Works offline"]
- loop: slide.code at 0:09 for 6 s · with title "The worker loop", code "main-loop", caption "internal/queue/queue.go"
- arch: slide.architecture at 0:15 for 5 s · with title "How it fits together", diagram "architecture"
- home: slide.screenshot at 0:20 for 6 s · with image "acme-com#3", frame "browser", scroll true
- said: slide.quote at 0:26 for 5 s · with text "It just kept working.", attribution "Ada", role "CTO, Acme"
```
`slide.bullets` shows its points one by one (`reveal "all"` shows them together); `slide.screenshot` frames the page
in a browser (with the page's own address unless `url` gives one) or, for a phone picture, a phone (`frame "none"`:
no frame), and scrolls a whole-page picture (`scroll false` holds the top). `metarush edit templates --json` lists
every field.

**Cuts from other projects**: a clip can play another project's cut, pinned to a version under `cuts:`:

```markdown
cuts:
  brand: prj_01m5…/rev_01m5…   # “Brand intro” · Brand · version 4 · 0:12.5 · a newer version (6) is available
---
## Clips
2. intro: cut from brand 0:00 → 0:12.5
   > Brand intro from Brand, version 4 (version 6 is newer)
```
The times are in that cut. `cut from Brand r4` (the project's name and a version) works too and is pinned when the
sheet is checked. `metarush cut nest <project> <other>` adds one without writing the sheet.

**`## Conversations`** (one call's recordings lined up; [conversations.md](conversations.md)) and the clips that
play them:

```markdown
## Conversations

- call: cnv_01m5… · quieter when not speaking by 18 dB
  Member: maya "Maya" · maya-cam · starts at 0:00 · tag "Host" · focus on faces
  Member: sam "Sam" · sam-cam · starts at 0:01.234 · drift +208.3 ppm · crop bottom 8% · look exposure 134%
  Member: gallery "Gallery view" · zoom-gallery · picture only · starts at 0:00.5

## Clips

1. talk: conversation call 0:00 → 0:51.2 · without sound from maya
   > Maya: "Hi Sam…" … Sam: "… a pleasure."

   Shot: 0:00 side by side · why "Open on everyone."
   Shot: 0:03.9 sam · why "Sam starts talking at 0:03.3; the picture follows 0.6 s later, in a pause."
   Shot: 0:31.6 side by side maya, sam · dissolve 0.3 s
   Shot: 0:36.7 card "Let us look at your setup." over sam
   Shot: 0:40 cutaway garage from 0:03.5
```
A member line says who it is (`id "Name"`), its footage, where its recording starts on the conversation's clock,
its drift, what it gives (`sound only`, `picture only`; picture and sound when not said), its tag, crop, framing,
colour correction and `muted`. The conversation line's settings: `quieter when not speaking [by N dB]` (the gate),
`sound always on`, `no name tags`. A conversation clip's times are on the conversation's clock; quoted words find
everyone's words there. Each `Shot:` starts at a time and shows one person (their short name), a layout
(`side by side`, `stacked`, `picture in picture`, `grid`, optionally with who), a `card "…"` (with `over <who>`), or
a `cutaway <footage> from <time>` (`b-roll` is read too), with `dissolve`, `why`. `metarush conversation angle`
changes one shot without writing the sheet.

**`## Sound`** (audio tracks):

```markdown
- bed: music-bed 0:00 → 0:18 · music · starts at 0:00 · volume -16 dB · fades in 1 s · fades out 2.5 s · ducks under speech by -12 dB
  Rights: license "CC BY 4.0" · attribution "A composer" · source "a music library"
```
The kind is `music`, `voiceover`, `sound effect` or `dialogue`; `starts at` is a time in the finished video;
`volume` is in dB (0 dB unchanged); `ducks under speech` makes it quieter while people talk. Quoted words work for
a sound's start and end too.

**`## Captions`** (manual caption lines, used when `timing: manual`):

```markdown
- 0:00.5 → 0:03.2 Host: "Forty people, one weekend."
- 0:03.2 → 0:06 "Here is what they built."
```
Times are in the finished video; the speaker name is optional.

## Notes, reasons and evidence

Under a clip, graphic or sound:
- `Why: …` is its rationale (Claude's or the editor's reason, shown in the dashboard).
- `Evidence: <kind> · <footage> <start → end | from t | to t> · quote "…" · note "…" · ref "…"` cites what supports
  it (kinds: transcript, insight, frame, scene, user, analysis). Clips and graphics only.
- Any other plain lines are **notes** (blank lines separate paragraphs). Notes are kept with the cut and never shown
  in the video.

A note line that would look like sheet structure (a list item, a heading, `Why:`…) starts with `\` when MetaRush
writes it, and the `\` is removed when it is read back. Text that plain lines can't hold exactly (leading spaces,
tabs) is written as `Notes: "…"` with JSON escapes.

## The `{…}` block

Every field of the spec has a friendly spelling, but any field can also be given in a block at the end of a line,
using the spec's JSON names and JSON values (names don't need quotes):

```markdown
2. garage: broll-c 0:01 → 0:03.25 · {label: "The garage", fadeOut: 0.5}
```

This is what keeps the format lossless as the spec grows: a field without a friendly spelling yet is written (and
read) in the block, and a field given both ways is an error, never a silent choice. Top-level fields go under
`extra:` in the settings. The completeness test (`TestRoundTripEveryField`) fails for any new spec field that is not
listed in `cutsheet.Spellings`, so new fields get a decision: a friendly spelling, or explicitly "block".

## Times and numbers

- Times are `m:ss.sss` (`h:mm:ss.sss` past an hour). MetaRush writes as many decimals as the value has (`0:11.6`,
  `1:15`, `0:00.139`); you can write any precision, and `12.5s` works too.
- Lengths are seconds with a unit: `0.4 s`, `0.4s`, `2 seconds`.
- Numbers are written as the shortest decimal that reads back to exactly the same value, and times and percentages
  are made from that decimal by moving digits, never by floating-point arithmetic, so nothing drifts across saves.

## Saving, conflicts and locks

A sheet becomes an ordinary edit plan: the typed ops that turn its base revision into the sheet (only the clips and
fields that changed), then the same path as every edit: check → confirm token → immutable revision, with the same
lock rules.

- **Someone saved meanwhile, different parts**: the change is combined with theirs automatically (`rebased: true`).
- **Someone saved meanwhile, same parts**: `E_REVISION_CONFLICT`, in plain words, naming what overlaps. Nothing is
  overwritten. Compare with `cut diff`, redo the change on the latest, or save it on another branch (`branch:`).
- **Locked items**: changing one is refused (`E_LOCKED`) unless the person asked and you pass `--allow-unlock`.
  Locking from the sheet is just adding `· locked`.
- **No changes**: `cut check` says so; `cut apply` refuses.
- The confirm token is bound to the exact sheet (and the message given with `-m`), as with `edit check`.

## Project folders and git

```bash
metarush project checkout "Builder story" ./builder-story   # cut.md, footage.md, .metarush/lock.json
metarush project status ./builder-story                    # your edits vs. the base; whether someone saved since
metarush project push ./builder-story --dry-run -m "end the opening on the garage line"
metarush project push ./builder-story --confirm <token> -m "end the opening on the garage line"
metarush project pull ./builder-story                      # bring in the latest (refuses to overwrite your edits)
```

- `cut.md` is the cut sheet (with every project source listed under `footage:`). `footage.md` is a read-only
  reference: each source's title, file name, length, size, who speaks and transcript excerpts with times and
  speaker names.
- `.metarush/lock.json` records the workspace, project, branch, base revision and its spec hash, the SHA-256 of the
  files as written, each footage name's asset ID and full content hash, and each project motion graphic the cut uses
  (its version and whether a person approved it). It is meant to be committed.
- `motion/<name>/` holds the project's motion graphics (their source; see
  [motion-graphics-as-code.md](motion-graphics-as-code.md)). Checkout and pull write the sources of the versions the
  cut uses when the folder lacks them (never over a folder that is there); `status` and `push` list each one's state
  and say when a folder has changes the cut doesn't use yet; `push` makes sure the media store holds every version the
  cut uses.
- Media is never copied; footage is referenced by ID and content hash, so anyone with access to the workspace gets
  the same video from a clone. No keys, tokens, logs or caches are written into the folder (a test scans for them).
- `push` saves `cut.md` like `cut apply` (rebasing or refusing as above) and rewrites the folder as the new
  revision. `pull` refuses when `cut.md` or `footage.md` have unsaved changes and names them; `--force` discards.

## Problems the checker reports

Every problem has a line and column (1-based, counted in characters), a plain message, usually a hint, and a stable
`code` for scripts (`--json`). The dashboard underlines the spot and lists them by line.

**Reading the sheet**: `empty`, `missing_settings`, `settings_not_closed`, `settings_syntax`, `settings_empty`,
`missing_marker`, `unsupported_version`, `unknown_setting`, `bad_value`, `bad_shape`, `bad_size`, `bad_color`,
`bad_project`, `bad_base`, `bad_branch`, `bad_extra`, `duplicate`, `bad_footage`, `bad_footage_name`,
`duplicate_footage`, `footage_mismatch`, `unknown_footage`, `missing_footage`, `unknown_section`,
`duplicate_section`, `unknown_heading`, `misplaced_title`, `title_mismatch`, `not_attached`, `not_supported`,
`quote_outside`, `missing_range`, `missing_arrow`, `bad_time`, `unexpected`, `bad_name`, `duplicate_id`,
`unknown_transition`, `unknown_transition_phrase`, `no_direction`, `dangling_transition`, `unknown_template`,
`bad_motion` (a motion graphic's name or version can't be read), `motion_unknown`, `motion_ambiguous`,
`motion_mismatch` (the line and the `motion:` pin disagree), `bad_remotion` (a Remotion composition line can't be
read), `remotion_unknown`, `remotion_ambiguous`, `remotion_not_approved`, `remotion_not_made`, `missing_at`, `missing_duration`, `bad_params`, `missing_name`, `missing_role`,
`bad_rights`, `missing_text`, `bad_speaker`, `bad_evidence`, `bad_quote`, `bad_block`, `captions_off`,
`bad_start` (a start point that can't be read), `missing_start` (a recording without one), `no_transcript`,
`phrase_not_found`, `phrase_ambiguous`, `phrase_order`, `phrase_not_allowed`, `bad_phrase`, `unknown_base`,
`wrong_project`, `missing_project`.

**Notes (warnings)**: `clip_numbers`, `footage_alias`, `fuzzy_anchor`, `approximate_anchor`, `whole_cut`.

**Checking the cut** (the same validation as `edit check`, placed on the right line): for example `out_of_range`,
`empty_range`, `missing_asset`, `asset_not_ready`, `asset_not_in_project`, `too_short`, `heavy_crop`,
`transition_clamped`, `locked`, `overlay_collision`, `caption_collision`, `motion_not_approved`, `motion_unknown`,
`remotion_not_approved` (a clip made from a Remotion version whose approval was withdrawn),
`overlay_long` (longer than a motion graphic's meta allows), `overlay_kind`, `rights_unknown`, `starts_after_end`,
`trimmed_to_end`, `captions_no_evidence`, `schema` (a value outside its allowed range), and at cuts `split_limited`
(the footage is too short for the split), `split_cuts_word`, `split_drops_words`, `voices_overlap`, `flash_shot`.

## Reserved and extension points

- **Transition phrases** are `cutsheet.TransitionClause`s (parse + print + the transition fields they cover;
  printing gets the cut's clips and who speaks in them, `cutsheet.CutContext`), and their fields are in
  `cutsheet.Spellings`. New transition types go in `cutsheet.TransitionNames` (and the catalog,
  `editspec.Transitions`). `cutsheet.TransitionPhrase` gives a cut's words for other surfaces.
- **Editing verbs** live in the `cut` command group (`internal/cli/cut.go` and `cutedit.go`; ops in
  `internal/ops/cutedit.go`, `CutEditCommon` and `editTarget.finish`): a new verb resolves names and times with
  `editTarget` (clips, positions, footage times or quoted words) and hands typed ops to `finish`, which checks or
  saves them and fills the plain output. `editspec.DiffOps` turns any edited spec into minimal typed ops (a changed
  cut becomes `setTransition`, a clip cut in two a `splitClip`).
- **Narration and the mix** follow the same rule as everything else: a new field of the narration or the mix
  fails the completeness test until it gets a spelling (`cutsheet.Spellings`) or is marked "block". Narration
  changes become line-level ops (`updateBeat`, `addBeat`, `moveBeat`, `removeBeat`, `updateVoiceover`), the mix one
  `setMix`, so they rebase like clip edits.
- **Resources and nested cuts**: `cutsheet/resources.go` reads and writes the `resources:` and `cuts:`
  pins and `cut from <name>` clip lines; an `Info` that also implements `cutsheet.ResourceLookup` and
  `cutsheet.CutLookup` writes their comments, pins names when a sheet is checked and finds cuts by project and
  version. Project motion graphics reuse the resource pins unchanged (their params marked `format: metarush-resource`).
- **Project motion graphics**: `cutsheet/motion.go` reads and writes `motion <name>@<version>` lines and the
  `motion:` pins; an `Info` that also implements `cutsheet.MotionLookup` resolves short versions and writes the
  approval comment. The overlay's `motion` field is in `cutsheet.Spellings` like every other field.
