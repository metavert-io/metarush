# Edit plans

**Prefer cut sheets for changes a person should read** (trims, reorders, word-anchored in/out points, transitions,
notes): `metarush cut show <project> --out <file>`, edit the Markdown, `metarush cut check <file>`, then
`metarush cut apply <file> --confirm <token>`. See [cut-sheets.md](cut-sheets.md). A cut sheet becomes the same
typed ops as below (so it rebases the same way); use JSON plans when generating a whole cut programmatically or
when a tool hands you ops (QA fixes).

A plan is JSON handed to `metarush edit check --plan <file> --json` and then `edit apply`. Exactly one of:
- **`spec`** — a complete cut (first revision, or a deliberate full rewrite), or
- **`ops`** — typed patch operations against `baseRevisionId` (preferred for revisions: they rebase safely).

```json
{
  "projectId": "prj_…",                 // or implied by baseRevisionId
  "baseRevisionId": "rev_…",            // required once the project has revisions (use the head)
  "instruction": "the user's request, verbatim",
  "rationale": "why this cut works, in two or three sentences",
  "spec": {
    "schemaVersion": 1,
    "title": "…",
    "output": {"preset": "vertical-1080p", "fps": 30, "platform": "shorts"},
    "clips": [
      {"id": "hook", "assetId": "ast_…", "in": 11.6, "out": 18.4,
       "framing": {"mode": "fill", "focusX": 0.5, "focusY": 0.4},
       "transitionIn": {"type": "cut"},
       "rationale": "Opens on the question; the pause before it is the tension.",
       "evidence": [{"kind": "transcript", "assetId": "ast_…", "from": 16.1, "to": 18.3, "quote": "What would you build…"},
                    {"kind": "insight", "assetId": "ast_…", "from": 15.06, "to": 16.56, "note": "1.5s pause (silencedetect)"}]}
    ],
    "audio": [{"id": "bed", "assetId": "ast_…", "role": "music", "in": 0, "out": 20, "at": 0, "gainDb": -14,
               "fadeIn": 1, "fadeOut": 2, "duckUnderDialogue": true, "rights": {"license": "…", "source": "…"}}],
    "captions": {"enabled": true, "style": "emphasis", "position": "lower", "maxWordsPerLine": 4, "emphasis": ["nobody"]},
    "overlays": [{"id": "who", "template": "lower-third.clean", "at": 1.0, "duration": 4,
                  "params": {"name": "Ada Lovelace", "title": "Engineer"}}],
    "notes": "Editorial treatment for reviewers."
  }
}
```

## Rules the validator enforces (fix what `edit check` reports; paths are JSON pointers)
- Times are **source seconds** for clips/audio (`in`/`out`), **timeline seconds** for `at`, overlays and manual captions.
  Clips play in order; the timeline is computed by MetaRush (crossfades overlap the previous clip).
- `in < out ≤ asset duration`; IDs are lowercase `[a-z][a-z0-9_-]*` and unique across clips/audio/overlays.
- Assets must be `available` and linked to the project (`metarush asset link`).
- Presets: `vertical-1080p` (1080x1920), `landscape-1080p` (1920x1080), `square-1080`. fps ∈ 24/25/30/50/60.
- Captions come from transcripts (`source: transcript`, default). `emphasis` style needs word-level timing; MetaRush
  downgrades it otherwise. Manual captions: `source: manual` + `segments`. `"showSpeakers": true` puts the speaker's
  name above each line (names the user gave; unnamed voices show as "Speaker N" when several people talk).
- Music needs recorded rights: on the track (`rights`) or on the file (`metarush asset rights`). Otherwise `edit check`
  warns `rights_unknown` and applying the cut asks the user (a to-do) where the music comes from.
- Overlays use vetted templates (`metarush edit templates`): `lower-third.clean`, `lower-third.cinematic`,
  `quote.kinetic`, `endcard.cta`, `datacard.stat` (needs a `source` — claims must be grounded), `title.card`.
  You provide parameters only. When no template fits, a project motion graphic a **person approved** can take the
  template's place: `{"id": "subs", "motion": {"name": "count-up", "hash": "<full version>"}, "at": 2, "duration": 4,
  "params": {…}}` (no `template`); its params are checked against its own schema, and a version nobody approved is
  refused (`motion_not_approved`). Writing one: the video-motion-design skill's reference/motion-code.md.
- **Layout pack roles**: title cards, lower thirds, end screens, quote cards and chapter cards should come from the
  project's layout pack (`metarush content pack show --project <prj> --json`). Name the role and leave the template
  out — `{"id": "intro", "role": "title-card", "at": 0, "duration": 3, "params": {"title": "…"}}` (`lower-third`,
  `end-screen`, `quote-card`, `chapter-card`; a variant after a colon, `lower-third:guest`) — and the check draws it
  with the pack's template or approved motion graphic, its look and the brand's logo; you give the words. A saved
  cut keeps what it was drawn with; `metarush content pack apply --project <prj>` moves the latest cut to the pack.
  `end.screen` (5–20 s) leaves room for a platform's next-video and subscribe elements.
- `locked: true` items cannot change unless the user asked (`--allow-unlock`). A locked final clip stays last.

## Fixing what `edit check` reports
Problem messages are plain words for the person (say them in your words); the `code` and `path` tell you what to change:
| code | the fix in the plan |
|---|---|
| `heavy_crop` | `clips[].framing.focusX/focusY` on the subject (look at a frame first), or faces (`output.framing: "faces"`, `framing.auto: "faces"`), or following a subject that moves when the person agrees (`"follow"`), or `framing.mode: "fit"`; sheet: `· focus 40% across`, `· focus on faces`, `· follow movement`, `· fit` |
| `asset_not_in_project` | `metarush asset link <asset> --project <prj>`, or link the project that has it |
| `not_video` | sound-only media goes in `audio[]`, not `clips[]` |
| `rights_unknown` | `audio[].rights` `{license, attribution, source}`, or `metarush asset rights` on the file; or ask the person (a to-do) |
| `motion_unknown`, `motion_schema` / `motion_not_approved` | `metarush motion check <name>` / a person approves it (never you) |
| `captions_no_evidence` / `caption_collision` / `captions_source_muted` | `metarush footage analyze` / `captions.position` top or middle / `captions.source: "voiceover"` |
| `claim_unsourced` | `overlays[].evidence` naming the source the person gave |
| `enhance_missing`, `enhanced_outdated` | `metarush sound enhance …` (the Sound tab) |
| `anchor_incomplete`, `anchor_kind` | `anchor.at` (time), `anchor.clipId` (clip), `anchor.assetId` + `at` (moment) |
| `beat_before_anchor`, `beat_after_anchor` | split the continuous recording per line |
| `nested_not_linked`, `resource_not_in_project` / `nested_newer` | `metarush project link <this> <other>` / `metarush cut update` |
| `resource_kind` (a repository) / `resource_page` (PDF pages) | `metarush resources make code\|diagram\|stats <repo>` / poppler, then `metarush resources refresh <name>` |
| `resource_unpinned`, `resource_version_unknown` | a `setResource` op or the sheet's `resources:` pin (checking a sheet pins) / `metarush cut update` |
| `split_cuts_word`, `voices_overlap` | `transitionIn.audio.mode: "auto"` |
| `ops_without_base`, `plan_shape`, `invalid_op` | set `baseRevisionId` (the head); exactly one of `spec` or `ops`; the message names the change and field |
Errors carry the exact command in `details.command` when one fixes them (`E_CONFIRM_REQUIRED`: `edit check` / `cut check`
first, then apply with the token).

## Transitions and sound at cuts
`clips[].transitionIn` is the cut *into* a clip (see `metarush edit transitions --json` for every option and range):
```json
{"id": "answer", "assetId": "ast_…", "in": 42.6, "out": 51.0,
 "transitionIn": {"type": "cut", "audio": {"mode": "lead", "duration": 0.4}}}
```
- `type`: `cut`, `crossfade` (Dissolve), `dip-to-black` (Fade through black), `dip-to-white`, `fade` (in from the
  background), `wipe`, `slide`, `push`, `zoom`, `whip`; `duration` seconds; `direction` left/right/up/down for wipe,
  slide, push and whip (the way the picture moves); `easing` ease-in-out (default), linear, ease-in, ease-out.
  Dissolve-like transitions overlap the two shots, so the cut gets that much shorter.
- `audio` (split edit): `{"mode": "lead", "duration": 0.4}` = this clip's sound starts 0.4 s before we see it (J-cut);
  `{"mode": "carry", "duration": 0.3}` = the previous clip's sound carries 0.3 s over this one (L-cut);
  `{"mode": "auto"}` = MetaRush picks the nearest pause (at most 1 s away, or `duration`); `blend` (s) is how long the
  two sounds cross (default 0.1). The footage must extend past the clip for the sound to reach (clamped with a warning).
- The end of the whole cut: the last clip's `"fadeOut": 1.5`.
- New cuts get `"mix": {"levelSpeech": true, "cutBlend": 0.1}` automatically (voices evened out between clips,
  click-free straight cuts); keep them when you rewrite a cut.
- Op: `{"op": "setTransition", "id": "answer", "transition": {"type": "crossfade", "duration": 0.6}}` sets one cut
  (a plain `{"type": "cut"}` or null resets it); `{"op": "setTransition", "id": "<last clip>", "edge": "end",
  "transition": {"type": "fade", "duration": 1.5}}` fades the end. Prefer it over `updateClip` with `transitionIn`.

## Narration and the mix
Narration lives in the spec as `voiceover`: an ordered script of lines (`beats`), each with an **anchor** that
MetaRush resolves to frames. The voiceover commands (`metarush voiceover …`, see cli.md) write these for you; write
them by hand only for small changes.

```json
"voiceover": {
  "beats": [
    {"id": "line-1", "text": "Every morning starts the same way.", "anchor": {"kind": "clip", "clipId": "hook", "offset": 0.4}},
    {"id": "line-2", "text": "Then the doors open.", "delivery": "warmer",
     "anchor": {"kind": "moment", "assetId": "ast_…", "at": 42.8, "quote": "right after the bell"}},
    {"id": "line-3", "text": "And nobody looks back.", "anchor": {"kind": "after", "gap": 0.6}},
    {"id": "line-4", "text": "See you tomorrow.", "anchor": {"kind": "time", "at": 24.5}}
  ],
  "settings": {"fitPolicy": "ask"}
},
"mix": {"music": {"gainDb": -3}, "ducking": {"musicUnderVoiceoverDb": -14}, "loudness": {"targetLufs": -14}}
```

- Anchors: `clip` = a clip's start (or `"edge": "end"`) + `offset` seconds, moves with the clip · `moment` = source
  seconds `at` in `assetId` (+ `clipId` if several clips show it), follows whichever clip shows that moment ·
  `after` = when the previous line's audio ends + `gap` (default 0.3 s) · `time` = timeline clock seconds, does not
  move. Prefer `clip`/`moment`; use `time` only for an exact time the user named.
- A line's audio is its `take` (a range of an audio asset with its source: scratch, tts, recorded, imported; `aiVoice`
  marks synthetic speech). Takes come from the voiceover commands — never invent asset IDs or times for them.
- Fit: lines without audio are timed at 155 words a minute (or the narrator's measured pace). `edit check` warns
  when a line runs into the next one: "Narration line 3 (“Every morning…”) has 28 words but only 4.1 seconds before
  the next line — it needs about 9.5 seconds at the narrator's pace" (the last line: "before the video ends").
- Mix buses `source` (the clips' own sound), `voiceover`, `music`, `sfx`: `gainDb`, `mute`, `solo`.
  `muteSource: true` (or a clip's `"muted": true`) turns off camera sound. Ducking is on by default with narration:
  music −14 dB and clip sound −18 dB under the voice, music −12 dB under on-camera speech. Captions can follow the
  narration: `captions.source` = `voiceover` or `both`. With `showSpeakers`, narration lines are named "Narrator"
  when captions mix dialogue and narration; narration-only captions show no names.

## Ops (use the head revision as `baseRevisionId`)
`insertClip {clip, after|before}`, `removeClip {id}`, `replaceClip {id, clip}`, `updateClip {id, set}`, `moveClip {id, after|before}`,
`addOverlay {overlay}`, `updateOverlay {id, set}`, `removeOverlay {id}`, `addAudio {track}`, `updateAudio {id, set}`, `removeAudio {id}`,
`setCaptions {captions}`, `setOutput {output}`, `setBrand {brand}`, `setNotes {text}`, `setTitle {text}`, `lock {id}`, `unlock {id}`,
`setScript {voiceover}` (whole narration; null removes it), `updateVoiceover {set}` (settings, not lines), `addBeat {beat, after|before}`,
`updateBeat {id, set}`, `moveBeat {id, anchor | after|before}`, `removeBeat {id}`, `setMix {mix}`, `setTransition {id, transition, edge?}`.
`lock`/`unlock` also take a line ID, `voiceover` (the whole narration) or `mix`.
The timeline's gestures (times on a clip are seconds in its footage, so they rebase over changes elsewhere):
`splitClip {id, at, newId?}` (cut in two at footage time `at`; the second part is `<id>-2` unless named),
`trimClip {id, edge: in|end, to, mode?: ripple|roll}` (move the start or end to footage time `to`; `roll` moves the
neighbouring clip's edge too, so the length stays), `slipClip {id, by}` (slide the footage under the clip by `by` s),
`deleteRange {from, to}` (cut out a stretch of the timeline, in timeline seconds, across clips) and
`removeWords {id, from, to, text?}` (cut out a stretch of one clip's footage; the `cut remove-words` command finds the
words and places the cuts in the pauses for you). `metarush edit preview --plan <file> --json` shows what a plan makes
(no token, nothing saved).
`set` is a partial object, e.g. `{"op": "updateClip", "id": "broll", "set": {"out": 2.5}}`. Nested objects merge field
by field (`{"framing": {"focusX": 0.4}}` keeps the mode); `null` resets a field (`{"volume": null}`).

If `edit apply` returns `E_REVISION_CONFLICT`, someone else changed the same items: re-read the head
(`revision show <head> --json`), tell the user what changed, rewrite the ops. Disjoint changes rebase automatically.
