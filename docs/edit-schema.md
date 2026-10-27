# Edit schema

The canonical, generated schemas live in `schema/` (`edit-plan.schema.json`, `edit-spec.schema.json`,
`render-plan.schema.json`, `template.*.schema.json`, `ops.schema.json`). Print one with
`metarush schema show edit-plan`. The skills' reference `skills/_shared/edit-plans.md` is the practical guide.

- **Plan** = `{projectId?, baseRevisionId?, branch?, instruction, rationale, spec | ops}`.
- **Spec** = output (preset/size/fps/platform, `framing`: `faces` keeps each cropped clip's main face in frame, written
  into new cuts; absent in older cuts, which keep the middle) + ordered `clips` (source in/out seconds, speed, volume, framing,
  `transitionIn`, `fadeOut`, rationale, evidence, notes, locks) + `audio` layers (role, timeline `at`, gain, fades,
  ducking, rights, notes) + `captions` (from transcripts, the narration or both, or manual; with `showSpeakers` the
  narration is named "Narrator" only when captions mix it with dialogue) + `overlays` (vetted template ID + validated
  params, notes — or, instead of `template`, `motion: {name, hash}`: a project motion graphic a person approved,
  its params validated against the component's own schema; and `role`, the part it plays in the
  [layout pack](layout-packs.md)) + `brand` + `notes` + `voiceover` + `mix`. `notes` on a clip, sound or graphic is a person's own
  text; it travels with the cut and never appears in the video.
- **Graphics that last a section or the whole video** ([call-recordings.md](call-recordings.md#graphics-that-last-a-section-or-the-whole-video)):
  an overlay's `span` (`{}` the whole video, `{from, to}` clip IDs, either end left out = the first or last clip) puts
  it from the start of its first clip to the end of its last, `at`/`duration` then measured inside the span (duration
  0 = to its end); `changes` (`[{footage: <assetId>, at: <seconds in that footage>, params}]`) change its fields
  wherever the cut shows that footage from that moment on (until its next change). Both are omitempty (older cuts keep
  their hash). Validation: `span_clip`, `overlay_too_long` (only `TemplateInfo.Persistent` templates — `name.bar`,
  `logo.corner` — and motion graphics with `meta.persistent` may stay up longer than their limit), `change_resource`,
  `change_unseen` (warning). The plan's overlay gets `segments` (`[{from, params}]`, frames from the overlay's start,
  each with its full fields); templates and motion graphics receive `segments` and `segmentFrom`. The `name.bar`
  template (`name`, `title`, `cover` {x, y, w, h} in fractions of the picture, `style` band | tab, `hide`, `logo`)
  covers the names a call app printed.
- **Resources** = `spec.resources`: short name → `{id, sha256}`, the exact version of a logo, picture, PDF or
  font the cut shows. Template parameters marked `format: metarush-resource` name them: `logo` on `title.card`,
  `lower-third.clean`, `lower-third.cinematic` and `endcard.cta`; `image` on `title.card` and `endcard.cta` (a PDF
  page as `"deck#3"`); `logo` on `logo.corner` (`corner` top-right (default) | top-left | bottom-right | bottom-left,
  `size` small | medium | large, `opacity` 0.2–1; region `corner`). `brand.fontFile` names a font resource used
  instead of `brand.font`. Validation: `resource_unknown` (a name that isn't pinned), `resource_version_unknown`,
  `resource_not_in_project` (not the project's, the workspace library's or a linked project's), `resource_kind`
  (a font where a picture goes, or the reverse), `resource_page`, `resource_unpinned` (pinned but unused: a warning),
  `resource_removed` and `resource_not_uploaded` (warnings: it still renders, from this computer when only this
  computer has it). `setResource {name, resource}` pins or updates one (null removes it); `editspec.DiffOps` writes
  it for changed pins. The RenderPlan carries `resources` (name → kind, size, pages, family) and `resourceSources`
  (SHA-256 → URL: `/media/resources/file/<sha>` in previews, the render's loopback server in renders).
- **Nested cuts**: a clip whose `assetId` is `cut:<projectId>/<revisionId>` plays that revision of another
  project's cut (its `in`/`out` are seconds in that cut; `speed` must be 1; framing is ignored). The project must use
  that project (`project_links`, or it is its own past), cycles (`nested_cycle`) and more than three levels
  (`nested_too_deep`) are refused, `nested_newer` warns when a newer version exists. The RenderPlan's `nested`
  (keyed like the clip's `assetId`) holds `{projectId, revisionId, number, title, projectName, newerRevisionId?,
  newerNumber?, plan}`: the inner cut resolved at the outer size and frame rate. Its sound is in the outer `audio`
  list (`nested: <clipId>`, envelopes composed with the outer clip's), so the inner plan's own `audio` is empty and
  its captions are dropped when the outer cut shows captions.
- **Voiceover** = ordered `beats` (id, text, `anchor`, delivery notes, rationale/evidence, optional per-line `voice`,
  `take`, `gainDb`, state, lock) + `settings` (speaking rate, `fitPolicy` auto | ask | shorten, default gap) + `layout`
  (`beats`, or `continuous` with one recording placed by its own anchor). Anchor kinds: `clip` (clipId, `edge`
  start|end, `offset`), `moment` (`assetId` + source `at`, optional clipId, quote/ref), `after` (`gap`), `time`
  (timeline `at`). A take is a range of an audio asset (`in`/`out`, `speed`, `source` scratch|tts|recorded|imported,
  `aiVoice`) with an optional cleaned-up version (`processed`, `useProcessed` for A/B). A line has until the next line
  that starts after it in time (not in script order), or the end of the video: that is the room the fit check
  measures, so lines placed out of order never get negative room. The fit report (`voiceover.show`, `voiceover fit`)
  says it twice: `message` names the line ("Line 3 (“And by noon…”) has 25 words but only 7.7 seconds before the
  video ends…") and `short` says the same with "This line" for a card that already shows which line it is.
  The studio timeline's narration lane sends `moveBeat {id, anchor}` through its working copy (applied in the browser
  too, golden-tested against Go). `voiceover.line` (CLI `voiceover beat add|update|move|remove`) builds and checks the one-line plan (`addBeat`,
  `updateBeat`, `moveBeat`, `removeBeat`); with `to` (seconds) it ties the line to what plays there: the shot under
  that point (the latest-starting one, offset to 0.1 s), the moment or previous line it already follows when it
  stays on them, or the exact time with `exact`. `voiceover.parse` with `clip` writes one shot's narration: the first
  line starts 0.3 s into the shot unless the script places it, and the new lines replace only the lines that started
  over that shot.
- **Script versions**, all optional and additive on `voiceover`: `original` is the script exactly as the person
  pasted or imported it (never rewritten; a whole-script parse sets it, appending lines does not); `fitted` is
  Claude's version fitted to the video (`lines`: `id`, the person's `original` words, Claude's `text`, `anchor`,
  `reason`, and `kept` when the person keeps their own words for that line; `note`); `active` says which version
  the `beats` are (`original` when absent, or `fitted`); `parked` holds the other version's lines, with their takes
  and anchors, while it is not in use. A pasted script is used as-is by default (`voiceover.parse`: one line per
  paragraph, else per row, else per sentence, each after the line before, no timing guessed; `spread` spreads lines
  without a start point over the shots, one per shot, as before). `voiceover.fitted` checks Claude's proposal and
  makes it the version in use (`keep` keeps the person's words on those lines); `voiceover.use` switches versions
  (each keeps its own audio and places; `audioKept` says when the version set aside keeps audio).
- **Live takes**: `voiceover.place` places one recording made while the cut played (`assetId` + `from`, the
  playhead when recording began; `until` ends a re-recorded range; `lines` limits it to some lines). It keeps the
  whole recording, matches its words to the lines, cuts it only in the pauses between lines (where the sound is
  quiet, never inside a word: 0.06 s before a line's first sound, the cut in the longest quiet stretch of the pause)
  and gives each line found a take on the recording plus an anchor where it was said (`clip` + offset to the
  millisecond, a `moment` past 60 s into a shot, `time` outside the shots). Lines in the stretch that were not heard
  lose their old sound and follow the line before (`not-heard`); a line before the stretch that runs into it stops
  where the new take's sound begins, with a 0.05 s crossfade (`trimmed`). The take records keep the read (`readId`,
  `rd_…`), the anchor it was said at (`pin`, `pinAt`) and the one planned before (`planned`), so `read` + `where`
  `planned` puts the lines at their planned spots and `recorded` back where they were said. Consecutive lines of one
  read that still follow each other play as one audio element (`beatIds` on the plan's audio), so the read plays
  exactly as recorded until an edit moves a shot. The fit report adds `intoNextShot` and `nextShot` when a line's
  sound runs more than 0.3 s past the end of the shot it starts on, with fixes (hold the shot longer, start earlier).
- **Mix** = buses `source`, `voiceover`, `music`, `sfx` (`gainDb`, `mute`, `solo`), `muteSource` (and per-clip
  `muted`), `ducking` (enabled, music/source under narration, music under dialogue, attack/release), `loudness`
  (`targetLufs` from the platform by default, `truePeakDbtp`, `normalize`), `levelSpeech` (even out how loud people
  sound from clip to clip) and `cutBlend` (seconds the sound blends at straight cuts). New cuts get
  `{"levelSpeech": true, "cutBlend": 0.1}` from `edit check/apply` (a full spec with no base, or whose base already has
  them); without them a cut sounds exactly as cuts did before, and a mix holding only them keeps the original mixer
  (no automatic ducking or normalization). `mix.set` (CLI `mix set`) turns plain changes (`buses` with gain/mute/solo,
  `clips` with `muted`, `muteSource`, ducking depths, `targetLufs` or `loudnessAuto`, `levelSpeech`, `cutBlend`,
  `reset`) into one checked plan (`setMix`, plus `updateClip` for single clips), applied with `apply`; `mix.preview`
  resolves the same change into the plan the cut would play, with the effective mix, and saves nothing (the
  dashboard's mixer plays it before saving).
- **Transitions** (`clips[].transitionIn`, the cut *into* a clip): `type` cut | fade (in from the background) |
  crossfade (shown as Dissolve) | dip-to-black (Fade through black) | dip-to-white (Fade through white) | wipe | slide |
  push | zoom | whip, `duration` (s), `direction` left/right/up/down (wipe, slide, push, whip: the way the picture
  moves; default left) and `easing` ease-in-out (default) | linear | ease-in | ease-out. Crossfade, wipe, slide, push,
  zoom and whip overlap the two shots (clamped to half the shorter one); dips fade out, then in. The whole cut's fade
  in is the first clip's `fade`; its fade out is the last clip's `fadeOut`. `metarush edit transitions` (and
  `schema/transitions.json`) lists them with plain descriptions, ranges and the project styles.
- **Split edits** (`transitionIn.audio`): `mode` cut (with the picture) | lead (this clip's sound starts `duration` s
  before its picture: a J-cut) | carry (the previous clip's sound carries `duration` s over this one: an L-cut) | auto
  (MetaRush moves the sound change into the nearest pause, at most `duration` s, default 1, so no word is cut), and
  `blend` (s; the transition's own, else `mix.cutBlend`, else 0.1). Only one clip's sound is primary at a time: the
  other fades out (or in) over the blend, starting where the incoming sound starts. The footage limits how far a
  sound can reach past its picture (it is clamped, with a plain warning); narration anchors stay tied to the picture.
- **Ops**: `insertClip, removeClip, replaceClip, updateClip, moveClip, addOverlay, updateOverlay, removeOverlay,
  addAudio, updateAudio, removeAudio, setCaptions, setOutput, setBrand, setNotes, setTitle, lock, unlock, setScript,
  updateVoiceover, addBeat, updateBeat, moveBeat, removeBeat, setMix, setTransition`. `lock`/`unlock` also take a line
  ID, `voiceover` or `mix`. The `update*` ops take `set` as a JSON merge patch (RFC 7386): nested objects merge
  (`{"framing": {"focusX": 0.4}}` keeps the other framing fields), `null` removes a field so it falls back to its
  default, arrays are replaced whole, and unknown fields are rejected. The cut an ops plan produces is validated
  against the schema as well, so `updateClip {"speed": 9}` is reported as outside 0.25–4. `setTransition {id,
  transition}` sets one cut (picture and sound; null or a plain cut resets it); with `"edge": "end"` it sets the
  clip's fade-out (`fade`/`dip-to-black` + duration). It touches the clip, the clip before it and the cut itself
  (`cut:<before>><clip>`), so a concurrent change to either clip, or a reorder that separates them, conflicts instead
  of rebasing; a locked previous clip refuses transitions that reach into it (overlaps, dips, split sound). Saving a
  cut sheet turns a changed `↳` line into a `setTransition` too.
- **Editing ops** (the studio timeline, transcript edits and `metarush cut …` verbs); times on a clip are source
  seconds, so they mean the same moment of footage after a rebase:
  - `splitClip {id, at, newId?}` cuts a clip in two at `at` in its footage. The first part keeps the clip's name,
    place, transition and notes; the second (`newId`, default `<id>-2`, `-3`… — a trailing `-N` is replaced, not
    extended) starts with a straight cut and takes over the fade-out. Touches both.
  - `trimClip {id, edge in|end, to, mode?}` moves the clip's start or end to `to` in its footage. `ripple` (default)
    lets the rest of the cut move; `roll` moves the neighbouring clip's edge by the same time on the timeline (its own
    speed respected), so the cut keeps its length, and touches the neighbour and the cut between them too.
  - `slipClip {id, by}` slides the footage under a clip by `by` seconds (positive: later in the recording); its length
    and place stay the same.
  - `deleteRange {from, to}` removes a stretch of the **timeline** (seconds) across clips; the rest closes up. Clips
    cut in two get `<id>-2` parts; parts shorter than two frames go with the stretch; a straight cut joins what is
    left. It touches the places of the clips it cuts (`at:<clip>`), and `ChangedIDs` reports a clip whose start moved,
    so a change before it made meanwhile conflicts instead of cutting the wrong part.
  - `removeWords {id, from, to, text?, newId?}` removes `from`–`to` of a clip's footage (normally words and the pauses
    around them; `text` records the words). Whatever is left becomes the clip, its end, its start, or two parts.
  Every op refuses locked clips unless the plan allows unlocking, and a batch of them reads in plain words through
  `editspec.OpsSummary` ("Moved 2 clips, trimmed 1, took out words in 1 place"), which the timeline's saves use.
- **Previews**: `edit.preview` (`metarush edit preview --plan …`) takes any plan and returns, without saving or a
  token: whether it can be saved, its problems and warnings in plain words, a summary and the changes, the length, a
  conflict when a newer version changes the same parts (with who saved it), and with `--json` the resulting spec and
  its render plan. The dashboard's timeline calls it after every gesture.
- **RenderPlan**: what Go resolves a spec into — integer frames for every clip, audio layer, caption word and overlay,
  plus safe-area margins, dialogue ranges for ducking and per-asset source URLs. Clips carry `transitionIn` (`type`,
  `durationInFrames`, `direction`, `easing`, and `audio`: `mode`, `result`, `cutFrame`, `editFrame`, `blendFrames`,
  `requestedFrames`/`limited` when the footage allowed less, `noPause`), `transitionOut` when the next clip moves them
  too (push, zoom, whip), and `audio` when their sound has its own window (`from`, `durationInFrames`, `trimBefore`,
  `blendInFrames`, `blendOutFrames`, `gain`; the picture then plays silently). With speech leveling a clip also has
  `speechLufs` and `levelDb`, and `mix` has `levelSpeech`, `speechTargetLufs` and `cutBlendFrames`. Ducking ranges,
  captions (words appear when they are heard) and QA's expected speech follow the sound windows. With narration or a mix it also carries
  `voiceover` (each line's anchor frame, audio start/end, window, words, fit), `mix` (effective buses, ducking, the
  frames where narration speaks, loudness target, estimated loudness and master gain) and a `gain` envelope per clip
  and audio element (`[{f, g}]` keyframes Remotion interpolates linearly), so the preview and the render mix the same.
  A narration line's `windowEnd` is where its room ends (the next line in time, or the end). Plans made for the
  dashboard preview also carry `sourceChannels` (the channel count of each source with sound): the renderer turns a
  mono file into stereo at −3 dB per side, the browser at full level, so the preview plays mono sources 3 dB quieter
  to sound like the render. Renders ignore it.

Validation layers: JSON Schema (structural, JSON-pointer paths) → semantic preflight (assets exist/available/in project,
ranges within duration, IDs unique, overlay/caption collisions, crop warnings, VFR/HDR sources, rights,
caption timing evidence, locks vs. the parent revision) → cuts (transitions shortened to fit, splits short of footage
— "Sam's clip has only 0.2 s of sound before it starts, so Sam's voice starts 0.2 s early instead of 0.4 s" — a sound
change that cuts or loses words, two voices at once, no pause for auto, single-frame flashes, a direction or easing a
transition does not use; one implementation, `editspec.CutReport`, shared with QA) → narration (every anchor resolves to a clip or moment in the
cut, takes inside their assets, line order, fit against the next line at the narrator's pace, lines past the end).

**Cut sheets** ([cut-sheet.md](cut-sheet.md)) are a lossless Markdown view of a spec: `metarush cut show` prints one
for any revision and `cut check`/`cut diff`/`cut apply` read it back. Saving a sheet never sends the whole spec; it is
turned into the smallest ops plan against the revision it was printed from (`editspec.DiffOps`), so it rebases and
conflicts exactly like a hand-written ops plan. JSON stays canonical.

Versioning: `schemaVersion` is 1. Future versions add migrations for stored revisions and job payloads.
