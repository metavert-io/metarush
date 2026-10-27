# Cut sheets: the friendly way to read and change a cut

A cut sheet is the cut written as Markdown: settings at the top, one numbered line per clip, a `↳` line for each
transition, then Graphics, Sound and Captions. It converts to and from the edit spec without loss, so editing the
sheet is the same as writing ops, but the user can read it and you change only the lines that should change.

## Read the cut
- `metarush cut show <project> --out <file> --json` writes the latest revision (`--revision r3` for an older one,
  `--format json` for the spec). A project with no cut yet gets a starting sheet that lists all of its footage.
- `metarush revision compare r3 r5 --project <project> --format md` explains a change between two revisions.

## Edit the sheet
```markdown
1. hook: interview-a 0:00.139 → 0:03.88 · focus 45% across
   > “When I started building my …” … “… robot in my tiny garage,”

   Why: The confession is the hook.
   Evidence: transcript · interview-a 0:00.24 → 0:06.4 · quote "When I started building my first robot"

   A note the user wrote about this clip.

↳ dissolve 0.4 s

2. garage: broll-c 0:01 → 0:03.5 · muted
```
- **Clip line**: `N. name: footage start → end · settings`. Times are source times, `m:ss.sss` (`h:mm:ss.sss` past an
  hour; `12.5s` also works); any precision is kept. Footage short names come from `footage:` in the settings
  (MetaRush makes them from each clip's title).
- **Words instead of times**: `from "Then my neighbor" to "nobody was watching"`, or mix: `0:11.5 → "watching"`.
  MetaRush finds the words in that footage's transcript and cuts in the silence just outside them (never mid-word,
  never into the next word). Not found, or said more than once → an error that lists candidates with their times:
  add words or use a time. After saving, the sheet shows the exact times.
- **Order** is the order of the lines (numbers are renumbered for you). Delete a line to remove a clip; add a
  numbered line (with a new name) to add one.
- **Transitions** go on the line above the clip they bring in: `↳ cut`, `↳ fade in 0.5 s`, `↳ dissolve 0.4 s`,
  `↳ fade through black 1 s` (or `white`), and the flashy ones with the way they move: `↳ slide left 0.4 s`,
  `↳ wipe up 0.5 s`, `↳ push right 0.4 s`, `↳ zoom 0.4 s`, `↳ whip left 0.3 s` (only when the style asks; see
  editorial.md). After `·`: the pace (`slows down`, `speeds up`, `even speed`, `smooth`) and the **sound at the
  cut**: `↳ cut · Sam's voice starts 0.4 s early` (J cut), `↳ cut · voice carries 0.3 s` (L cut), `↳ cut · sound
  changes in a pause` (or `auto`: MetaRush finds the pause so no word is cut), optionally `with a 0.2 s blend`.
  The name in "Sam's voice" is written by MetaRush and not stored; `voice starts 0.4 s early` means the same.
  One cut on its own: `metarush cut transition <prj> <clip> cut --lead 0.4 --json` (checks; `--confirm` saves).
- **Clip settings** after `·`: `speed 1.5×`, `volume 80%`, `muted`, `fill` / `fit`, `focus 30% across, 40% down`,
  `focus on faces` (the crop follows the main face of the clip's on-screen index, even in a cut without
  `framing: faces`; the output settings' `framing: faces | center` sets it for every clip without a focus of its own),
  `follow movement` (the crop moves with the subject as it moves; `framing: follow` in the output settings does it
  for every clip without a framing of its own; only when the person chose it),
  `zoom 1.2×`, `fades out 1 s`, `label "…"`, `locked`; anything else as `{field: value}` with the spec's JSON names.
- **A Remotion composition as a clip**: `4. intro: remotion teaser Intro` (the whole composition), with a range
  `remotion teaser Intro 0:02 → 0:06`, a version `remotion teaser Intro@3` (default: the newest approved) and props
  `· with headline "Ship faster"`. Checking the sheet finds the clip MetaRush made of exactly that
  (`remotion_not_made`: make it first with `metarush remotion render`; `remotion_not_approved`: a person approves the
  version first); the sheet written back names it in `footage:` like any clip.
- `Why:` is the clip's rationale, `Evidence:` lines cite sources, plain lines under a clip are notes.
- Lines starting with `>` are filled in by MetaRush (the first and last words spoken, and who says them:
  `> Ada: “…”`); edits to them are ignored. The `# …` comment on each `footage:` line lists who talks in it.
- **Graphics**: `- who: lower-third.clean at 0:00.8 for 3.2 s · with name "Ada", title "Builder"` (vetted
  templates, see `metarush edit templates`), or a project motion graphic a person approved:
  `- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from "Lisbon", to "Porto"` (the version is what
  `metarush motion check` printed; MetaRush pins the full version under `motion:` in the settings).
  A part of the project's **layout pack** goes in the template's place — `- intro: title card at 0:00 for 3 s ·
  with title "Launch"` (title card, lower third, end screen, quote card, chapter card; a variant after a colon:
  `lower third: guest`) — and checking the sheet draws it with the pack; MetaRush writes it back as
  `- intro: title.card at 0:00 for 3 s · as title card · with …`, which you keep as it is.
- **Logos and pictures** (resources): pin them by a short name under `resources:` in the settings
  (`logo: res_… sha256:…`, written by MetaRush), then use the name in a graphic: `· with title "Launch", logo "logo"`
  (`title.card`, `lower-third.*` and `endcard.cta` take `logo`; `title.card` and `endcard.cta` also `image`, a PDF
  page as `"deck#3"`), or keep it in a corner: `- mark: logo.corner at 0:00 for 30 s · with logo "logo", corner
  "top-right"`. A name that isn't pinned yet is pinned for you when the sheet is checked, to the resource's
  current version (`metarush resources list --project <prj>` lists names); later versions change nothing until
  you update (`metarush cut update <prj> --resources logo`). `brand:` can name a font file: `font file: brandfont`.
- **Slides** (full-frame, in the brand's colours and font; footage and captions hide behind them):
  `- intro: slide.title at 0:00 for 3 s · with title "How Widget works", kicker "Part 1", logo "logo"`,
  `- why: slide.bullets at 0:03 for 6 s · with title "Why it is fast", bullets ["Runs on your computer", "Stores files once"]`,
  `- loop: slide.code at 0:09 for 6 s · with title "The worker loop", code "main-loop", caption "Every job is leased"`,
  `- arch: slide.architecture at 0:15 for 6 s · with title "How it fits together", diagram "architecture"`,
  `- home: slide.screenshot at 0:21 for 4 s · with image "homepage"` (`homepage#2` the phone picture, `homepage#3`
  the whole page, which scrolls; `frame "browser"`, `"phone"` or `"none"`), and
  `- said: slide.quote at 0:25 for 4 s · with text "…", attribution "Ada Park", role "Producer"`. The code,
  diagram and screenshot are resources made or fetched first (see [resources.md](resources.md)); they pin like logos.
- **Another project's cut** inside this one: pin it under `cuts:` (`brand: prj_…/rev_…`, written by MetaRush) and
  use it as a clip: `3. intro: cut from brand 0:00 → 0:12.5` (its start and end in that cut's time). It plays as
  one block at the version pinned; `> Brand intro from Brand, version 4 (version 6 is newer)` says when there is a
  newer one. Easier: `metarush cut nest <prj> --from "Brand"` (end of the cut), and to copy its clips in as a
  starting point instead, `metarush cut copy-in <prj> --from "Brand"`. Both need the person's OK like any change.
- **Sound**: `- bed: music-bed 0:00 → 0:20 · music · starts at 0:00 · volume -16 dB · ducks under speech`, plus a
  `Rights: license "…" · source "…"` line for music.
- **Narration** (`## Voiceover`): one bullet per line, in the order spoken, with the words in quotes and where the
  line starts: `- intro: "When I started…" · 0.5 s after hook starts` (also `when hook ends`, `after the previous
  line`, `0.8 s after the previous line`, `at interview-a 0:12.3 in hook "right after she laughs"`, `at 0:20` for a
  clock time the person named). Place lines by shots and moments so they move with the cut. Under a line:
  `Delivery: warm, slower`, `Take: …` (written by MetaRush when audio is made; leave it), `Why:`. The derived
  `> starts at 0:06.8 · fits (2.3 of 3 s)` says where it lands and whether it fits; a line that is too long says so.
- **Mix** (settings): `mix:` with `camera sound: off`, `music: -6 dB` (also `clip sound`, `narration`,
  `sound effects`; add `, muted` or `, solo`), `ducking:` (`music under narration: -14 dB`…), `loudness: -14 LUFS`,
  `even out voices: on` (people sound equally loud from clip to clip) and `blend at cuts: 0.1 s`.
  Edit it here when the levels change along with other parts of the cut; a request about the levels alone ("the
  music drowns out the narration") is the `video-voiceover` skill's mix steps (`mix show` explains what plays
  how loud). Tell the person what they will hear ("the music now dips further under your voice"), never these
  setting words.
- **Settings** (YAML at the top): `title`, `output` (shape, frame rate, platform, framing), `captions` (on, style, position,
  words per line, timing: `transcript`, `voiceover` or `both`, `speaker names: true` to show who speaks),
  `voiceover` (`fitting: auto | ask | shorten`, `pause: 0.3 s`, `layout`, `locked`), `mix`, `brand`, `footage`.
  Never edit `base:`: it records which revision you started from.
- **Fixed copies**: `fixed:` in the settings lists the footage the cut plays from a fixed copy (a steadier
  picture, cleaned-up voices), each as `<footage name>: sha256:…` with a comment saying what it fixes. MetaRush writes
  it: footage put into the cut for the first time plays the copy it uses now, and `metarush cut use-fixed <prj>
  <clip>` (or deleting a line here) moves the cut to the copy or back to the original as a new version. Leave the
  values as they are.
- Optionally record the request: `change: {request: "<the user's words>", why: "<your reasoning>", note: "<a few
  words naming the version>"}` in the settings. The note ("flicker fixes and headline cards") is what the new version
  is called next to its number; without one MetaRush takes the request's first clause.

## Small edits without a sheet
For one small change, the cut's editing verbs are quicker than a sheet: they name clips the way the sheet does (or by
number), check the change and print it in plain words with a confirm token; the same command with `--confirm <token>`
saves it as a new version.
- `metarush cut move <prj> kitchen start --json` (or `after hook`, `before 3`, a place number).
- `metarush cut trim <prj> hook --end -0.5 --json` (also `--start +1.2`, a time `--end 0:18.4`, or words
  `--end "very quickly"`; `--roll` moves the cut between two clips instead; `--slip 2` slides the footage).
- `metarush cut split <prj> hook "and then" --json`, `metarush cut delete <prj> broll --json`,
  `metarush cut delete-range <prj> 0:12 0:15.5 --json`, `metarush cut add <prj> interview-a 0:12.4 0:18 --after hook --json`.
- `metarush cut remove-words <prj> "um, anyway" --json` takes those words out with the pauses around them (said more
  than once: it lists where, then `--which 2` or `--all`). `--ums` suggests filler sounds, words said twice and false
  starts, numbered; show them to the person and save the ones they want with `--pick 1,3` (or all of them). With
  nothing to suggest it says so (`noChange: true`, no `removals`); tell the person the speech is already clean.
  People can also do this themselves in the studio: the Transcript beside the player cuts selected words out, and
  "Remove ums and restarts" plays each suggestion before they choose.
Use a sheet when several things change together, for anything the verbs don't cover, or to keep the cut in git.

## Check, show, save
1. `metarush cut check <file> --json` → `valid`, `problems` (line, col, plain message, hint, stable `code`),
   `changes` (plain words), `anchors` (where quoted words landed), `confirmToken`. Fix every error and re-check.
2. Show the user the `changes` list and any warnings that matter (`metarush cut diff <file>` shows the lines).
3. After they agree: `metarush cut apply <file> --confirm <token> --write --json` (pass the same `-m` message you
   checked with, if any). `--write` refreshes your file to the new revision.
- `rebased: true`: someone saved a newer revision; your changes did not overlap theirs, so both were kept. Say so.
- `E_REVISION_CONFLICT`: you both changed the same clip. `metarush cut diff <file>` shows their edit next to yours;
  re-read the latest (`cut show`) and redo the change there.
- `E_LOCKED`: the change touches a locked item. Only if the user explicitly asked: `--allow-unlock` on check and apply.

## Project folders (for git)
`metarush project checkout <project> <folder>` writes `cut.md`, `footage.md` (footage, lengths, speakers, transcript
excerpts with times) and `.metarush/lock.json`. Edit `cut.md`, then `metarush project status <folder>`,
`metarush project push <folder> --dry-run -m "…"` → `project push <folder> --confirm <token> -m "…"`.
`project pull` brings in the latest and refuses to overwrite unsaved edits unless `--force`. Media is never copied.
Tell the person what the folder holds and where it is in plain words; if writing it needs their OK, say what it will
create and ask. Never hand them the command or the project's ID to run themselves.
