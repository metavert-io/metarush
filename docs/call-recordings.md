# Call recordings: picture switches, flashes and printed names

This page covers picture switches found to the frame, split-second flashes at clip edges (and the fix that trims at
the switch), names printed in the picture (and the built-in name bar that covers them) and graphics that last a
section or the whole video. Code: `internal/calls`
(switches, who is on screen, printed names, the name bar), `internal/editspec/switches.go` (flashes, the frame grid,
the name bar overlay), `internal/editspec/span.go` (spanning graphics), `internal/worker/switches.go` (the job),
`internal/qa/calls.go` (the quality check), `video/src/templates/NameBar.tsx`.

## Why
Call recordings in "speaker view" (one recording per call whose picture flips to whoever talks) are hard to cut by
hand:

1. **Flashes.** A cut can open on a few stray frames of the other person, or a clip can start on a split-second of
   the wrong person, and nothing would say so without looking at stills one by one.
2. **Name changes "accurate to about a second".** Notes on who is on screen that come from the on-screen index
   (about one frame a second) would change a name bar slightly before or after the picture does.
3. **The call app's own names in the picture.** Zoom burns each person's name into the lower left; a video often
   wants one bar that covers them and shows the names it should use.
4. **A minute at most.** A graphic that has to stay on screen for a whole clip needs more than the one-minute limit of
   project motion graphics.

## Picture switches to the frame
The footage's cuts come from the evidence job: a 6 fps picture sampler snapped to ffmpeg's full-rate scene changes
(score ≥ 0.30) within 0.4 s. A cut is exact when it snapped, otherwise it is off by up to 1/12 s; the on-screen index
is bounded by those cuts and its readout rounds to the second. Call footage needs better than that: a name bar that
changes one frame late shows the wrong name for a frame, and a clip that starts one frame early flashes.

The **switches** step (job `switches`, any machine, ffmpeg only; insight `switches`, version `switches-1`) makes a
frame-accurate list of the footage's picture switches:

- **Candidates**: every cut the evidence found, plus the starts and ends of on-screen text spans and face tracks of the
  index that are not near a cut (Zoom's name tile changes exactly when the picture switches, so a tile change the cut
  detector missed is a switch to look at).
- **Exact already**: a cut that snapped to a full-rate scene change keeps its time; the last frame of the old picture
  is one frame before it.
- **Refined**: around every other candidate, MetaRush decodes ±0.6 s at the file's full frame rate
  (`select='gte(scene,0)',metadata=print` with the original timestamps) and takes the frame where the picture changes
  most. A peak counts when its score is at least 0.12 and three times the window's typical change; peaks closer than
  three frames are one switch. A strong peak (≥ 0.30) in a window that no candidate explains is a switch too (a flip
  shorter than the sampler's step).
- **What is stored**: per switch the first frame of the new picture (`at`), the last frame of the old one (`last`), how
  much the picture changed and, when it moved, where the earlier detectors had it (`was`); the file it was read from
  (the preview copy, constant frame rate) and its **precision**: 0 when the preview copy has the original's own frame
  rate (constant-rate footage: its frame times are the original's), else one preview frame (variable-rate footage is
  resampled for the preview copy, so a switch can be up to one frame earlier in the original).
- **When it runs**: after the evidence job for footage with picture and at least one cut, again when the on-screen
  index arrives with new candidates (idempotency key `switches:<clip>:<evidence>:<index>`), and through the `switches`
  catch-up step (v1) for footage analyzed before. `metarush footage switches <clip>` reads it (read-only, also for
  Claude); `metarush footage find-switches <clip> [--wait]` (op `footage.switches.find`, a card in the Claude panel)
  looks again; `footage visuals` and the dashboard's On screen tab use it for their shot boundaries.

**Who is on screen** in each shot between two switches (`calls.Shots`, read time, never stored): the name of the face
seen most in the shot (a person's confirmed face, or one MetaRush recognized by looks), else the printed name matched to
a person in People (first name or whole name, ignoring case), else the printed name as written, else the named voice
that talks most in the shot (marked unsure: in speaker view the picture can show someone listening). Each shot says how
it knows (`face`, `printed`, `voice`).

## Split-second flashes at clip edges
A **flash** is a footage switch inside a clip, so close to the clip's start or end that a split-second of the other
picture shows: at most 0.75 s, and at least one frame of it visible in the cut as resolved (the frame grid below). A
switch between two shots of the same person (a camera reframing) is not reported when MetaRush knows who both are.

- **Edit check / cut check**: warning `edge_flash` on the clip ("Clip 3, Lena's answer, opens with a split-second of
  Maya (0.2 s) before Lena appears."), so Claude sees it before anything is saved.
- **Quality check** (QA `qa-6`, check `call_footage`): finding `edge_flash`, warning (an error when the project has the
  rule "no flashes", [keeping.md](keeping.md)), with the fix as ops: `trimClip` to the exact switch, and — when the
  trimmed stretch has words — the clip's sound starting that much earlier (`setTransition` with sound `lead`, a J-cut)
  or the previous clip's sound carrying on (`carry`, an L-cut) for a flash at the end, so no words are lost.
- **The fix in one step**: `metarush cut flashes <project> [--dry-run]` (op `cut.flashes`) writes those ops for every
  flash of the latest cut, shows them first and saves them with a confirm token like every edit; in the Claude panel
  it is a card, in the dashboard **Trim the flashes** where the cut lists its warnings.

**The frame grid.** A clip shows source time `(TrimBefore + k·speed)/fps` at its frame `k` (`TrimBefore =
round(in·fps)` in output frames, Remotion's `trimBefore`). `editspec.StartAfter` gives the smallest in-point on that
grid that shows the new picture from the clip's first frame (the first sample at or after the switch), and
`editspec.EndBefore` the out-point whose last sample is at least half a source frame (and the switch's precision)
before the switch, so the old picture holds to the last frame whether the player shows the frame at or before a time
or the nearest one. Times are kept to the millisecond, well inside half a frame.

## Names printed in the picture and the built-in name bar
**Printed names** (`calls.NameTiles`): in a shot of at least 0.8 s, on-screen text that lasts most of the shot, sits in
a corner (its middle in the outer 40% across and the outer 22% up or down), is short (1–5 words, up to 40 letters),
small (at most a tenth of the picture high) and not a changing counter. A corner shows printed names when most shots
have one there (at least 60% of them, and at least one shot of 3 s). The area to cover is every tile's box there,
widened by 1.5% across and 1.2% up and down (the call app draws a dark box around the letters), reaching the edge of
the picture when it is within 3% of it. `footage visuals` says "Names printed by the call app at the bottom left:
“Lena O.”, “Maya Lindqvist”"; `footage switches` lists them per shot.

**The name bar** is a vetted template, `name.bar` (region lower, no time limit): an opaque band at the bottom of
the picture in the brand's colours with a thin accent line, tall enough to cover `cover` (the area to hide, in
fractions of the picture), the person's name (and an optional title) in it. It is solid from its first frame (nothing
of the printed name shows while it arrives); when the name changes it cross-fades the words, the band stays put.
`style: "tab"` draws a box only as wide as the area plus the name instead of a full-width band; `hide: true` lets a
change take the bar away (for footage without printed names), and a later change bring it back. `height: "half"`
 draws it about half as tall — never shorter than the area it covers: anchored to the picture's edge when the
printed names reach it, else centred on them — and `animate` says how a new name comes in: `fade` (what it always
did), `slide`, or `never` (the name is all there on its first frame; nothing moves). The house style of the
project's Content Settings ([resources](resources.md#the-house-style)) sets the look, height and animation of
the bar `cut names` writes and of new cuts' bars, and the quality check flags a bar that animates or is taller than
the house style says.

`metarush cut names <project> [--first-names] [--style band|tab] [--keep-titles] [--dry-run]` (op `cut.names`,
also the dashboard's **Cover the printed names** and a Claude card) writes one `name.bar` graphic for the whole video
(a [spanning graphic](#graphics-that-last-a-section-or-the-whole-video)):

- a **change at every picture switch** of the call footage the cut shows, at the switch's exact time in that footage,
  naming who is on screen (first names when asked, or when the project's rules say "first names only"), plus one at
  the start of each footage's first shot the cut uses; footage without printed names gets no changes (the bar's own
  fields, `hide: true`, apply there);
- `cover` mapped from the footage's printed-name area through each clip's crop into the picture (`editspec.CropRect`,
  the renderer's placement), per clip where it differs;
- the person-name lower thirds of the cut (`lower-third.*` whose name is a person in it) removed unless
  `--keep-titles`: the bar replaces them.

Where it is offered: `cut check` says "Names printed by the call app show at the bottom left in 4 clips: cover them
with your own name bar" (`printed_names`, information), the quality check notes printed names a render shows uncovered
(`printed_names`, with the graphic as fix ops) and a name bar that names someone else than who is on screen for more
than half a second (`name_bar_mismatch`, fix: set the names again), and Claude's skills offer it for call footage.
Shots nobody is known in show an empty bar; `cut check` says how many.

## Graphics that last a section or the whole video
A graphic's `at` and `duration` are seconds on the finished video, which suits a two-second title but not a bar that
must stay through every edit. Two additive fields (omitempty: older versions keep their hash and their plan):

- **`span`**: `{}` is the whole video; `{"from": "<clip>", "to": "<clip>"}` a section, from the start of one clip to the
  end of another (either may be left out: the first or last clip). With a span, `at` is seconds after the span starts
  (usually 0) and `duration` its length, 0 meaning "to the end of the span". The graphic follows the clips when the cut
  changes; a span whose clips are gone is an error (`span_clip`).
- **`changes`**: moments in the footage where the graphic's fields change without it leaving the screen:
  `{"footage": "<the clip's assetId>", "at": <seconds in that footage>, "params": {"name": "Lena"}}`. Wherever the
  cut shows that footage at or after the moment (until the footage's next change), the change's fields are merged
  over the graphic's own; frames showing footage without changes use the graphic's own fields. Anchoring to the
  footage, not to clips, is what keeps the names right through every trim, split and reorder: a moment belongs to a
  person whichever clip shows it. A change in footage the cut doesn't show is never seen (warning `change_unseen`).
  Changes merge into the graphic's fields (the result is validated like the fields themselves) and may not change
  resource fields (a picture: `change_resource`), so pins stay simple.
- **Long-lived graphics**: templates say whether they may stay on screen for a section (`TemplateInfo.Persistent`:
  `name.bar`, `logo.corner`); spanning any other template longer than its limit is an error (`overlay_too_long`). A
  project motion graphic says `meta.persistent: true` to stay up as long as a span asks (its check accepts any
  `maxDuration`, or none, and draws its test frames over 20 s); others keep the one-minute limit.
- **The plan**: Go resolves the span to frames (`from`, `durationInFrames`) and the changes to `segments` on the plan's
  graphic (`[{from, params}]`, frames from the graphic's start, every segment's full fields), so the renderer and the
  preview never compute times. Templates and motion graphics get `segments` and the current segment's start
  (`segmentFrom`) beside `params`: the current segment's fields; vetted templates that don't use them ignore them.
- **Quality check and captions**: a spanning graphic is never "moved out of the way" by a fix (`overlay_overlap` and
  `caption_overlay_stack` suggest moving the captions instead); captions lift above a name bar like above a lower third
  (`lowerRegionReserve` knows its height).
- **Cut sheets**: `- names: name.bar for the whole video · with name "Maya", cover {…}` (a section: `from open to
  seed-swap`, `from open to the end`, `from the start to seed-swap`; a span's `at` or `duration` other than 0 in the
  `{…}` block), and one line per change under it: `Change: at lena-call 2:15.40 · with name "Lena"` (the
  footage's short name and the moment in it; [cut-sheet.md](cut-sheet.md#graphics-sound-and-captions)).
  `TestRoundTripEveryField` covers both fields.

## Limits
- Switches are found where the picture changes at once (a cut). A slow cross-fade between people is not a switch.
- Who is on screen is only as good as the faces people named, the printed names and the voices; a shot nobody is known
  in shows an empty bar and `cut check` says so.
- Conversation clips and nested cuts are covered too (see below). A grid of call-app tiles inside one recording
  (the gallery view) is one picture: its printed names are covered only where the same corner holds a name in most shots.
- Printed names are read from the on-screen index, so they need a Mac to have looked at the footage; switches and
  flashes work everywhere.

## Nested cuts and conversations

- **A nested cut** switches at each hard cut between its own clips and at its call footage's switches, mapped onto
  its timeline (who is on screen comes from that footage). A clip of it that starts or ends a few frames from one of
  those is a flash like any other, and Trim the flashes trims it on the inner cut's frame grid. The name bar covers the
  printed names inside it (the area drawn through the inner clip's framing; changes are keyed `cut:<project>/<revision>`
  on the nested cut's timeline) and hides over its other footage. A nested cut with a name bar of its own keeps it: the
  outer bar stays hidden there. Played at another shape, a nested cut's own bar covers the names where that shape's
  crops put them.
- **A conversation clip** switches at each of its shots, and while a recording with switches of its own (Zoom's speaker
  view) fills the frame, at that recording's switches mapped onto the conversation's clock (drift included). A member
  shown alone is who is on screen (`by: member`) unless their recording's own shot says otherwise. The name bar covers a
  member's printed name while their recording is shown alone (the area drawn through its tile; changes keyed
  `conv:<name>` on the clock) and hides over layouts, cards and other footage; in a layout each tile's own name tag
  sits over that tile's printed name, opaque, in the brand's colours (one bar can't cover several tiles), whenever the
  cut has a name bar.
- The quality check (`call_footage`, the name-bar mismatch) looks inside both; cut sheets write their changes
  `Change: at conversation <name> 1:02.5` and `Change: at cut from <name> 0:12`.
