# Conversations from online calls

A **conversation** is several recordings of one talk — one per person from Zoom, Riverside or
SquadCast, a phone and a webcam, or per-person sound plus Zoom's speaker or gallery view — aligned on one clock.
In a cut, its picture switches between people and layouts while its **sound is every person's own microphone
track, mixed continuously**. Today's storyline is different: each clip's sound follows its picture. Everything
here is additive: a cut without conversations is resolved, printed and rendered exactly as before (golden tests).

Every surface uses the same ops: the dashboard's Conversation tab, the CLI (`metarush conversation …`, `--json`
everywhere) and the skills.

- [The model](#the-model)
- [In the edit spec](#in-the-edit-spec)
- [Sync](#sync)
- [Sound](#sound)
- [Choosing the picture](#choosing-the-picture)
- [Layouts](#layouts)
- [Clean-up](#clean-up)
- [Webcams that match](#webcams-that-match)
- [Imports](#imports)
- [Outputs](#outputs)
- [Cut sheets](#cut-sheets)
- [The timeline and the dashboard](#the-timeline-and-the-dashboard)
- [The renderer](#the-renderer)
- [QA](#qa)
- [Commands](#commands)
- [Measurements](#measurements)

## The model

Two layers, like resources:

| | Where | What |
|---|---|---|
| **The conversation** | collection `conversations` (`cnv_…`), in one or more projects | the members (recording, person, what each is used for), the alignment (where each recording starts on the conversation's clock, its clock drift, how sure the match is and how it was found), clean-up suggestions with their accept/skip state, colour measurements, where it came from (Zoom folder, Riverside export…). It changes: a nudge, a new suggestion. |
| **The pin in a cut** | `spec.conversations.<name>` | the exact alignment and the per-person picture settings this cut renders with (crop, framing, colour correction) and how the sound is gated. A revision never changes when the conversation does; using a new alignment is a change to the cut (one `setConversation` op), like updating a pinned resource. |

A **conversation clip** is a clip whose `assetId` is `conv:<name>`. To the rest of the engine it looks like footage
(nested cuts do the same): its length is the conversation's length, its transcript is everyone's words on
the conversation's clock (each from their own recording, so who said what is exact), its loudness the mix's. So the
picture timeline, split edits at its ends, ducking, captions, the speech check, the cut report, trims and splits work
unchanged. What is new is inside it: **shots** (`clip.shots`), the picture over time — one person, a layout, a text
card or other footage — while the sound runs on.

```text
conversation clip (conv:call, 0:00 → 12:30 of the conversation's clock)
  picture  │ Maya │ Sam ─────────────│ side by side │ Sam ──────│ card "What did…" │ Sam …
  sound    │ Maya's track ════════════════════════════════════════════════════════════
           │ Sam's track     ════════════════════════════════════════════════════════════
```

## In the edit spec

```jsonc
"conversations": {
  "call": {
    "id": "cnv_01j…",
    "members": [
      {"id": "maya", "name": "Maya", "assetId": "ast_…", "start": 0, "tag": "Host"},
      {"id": "sam", "name": "Sam", "assetId": "ast_…", "start": 12.345, "drift": 20,
       "crop": {"top": 0.06, "bottom": 0.08}, "framing": {"auto": "faces", "zoom": 1.1},
       "look": {"exposure": 1.06, "contrast": 0.98, "warmth": -0.03}},
      {"id": "gallery", "name": "Gallery view", "assetId": "ast_…", "use": "picture", "start": 0.2}
    ],
    "gate": {"mode": "speaking", "depthDb": 18},
    "sampleAligned": true
  }
},
"clips": [
  {"id": "talk", "assetId": "conv:call", "in": 0, "out": 750,
   "shots": [
     {"at": 0, "layout": "side-by-side", "rationale": "Open on both of them."},
     {"at": 4.2, "member": "maya", "rationale": "Maya asks the first question."},
     {"at": 15.8, "member": "sam", "rationale": "Sam starts talking at 0:15.4; the picture follows 0.4 s later (we hear them before we see them), in a pause."},
     {"at": 31.2, "layout": "side-by-side", "rationale": "Both laugh for 1.6 s."},
     {"at": 62.5, "card": "What did you build first?", "members": ["sam"]},
     {"at": 70, "broll": {"assetId": "ast_…", "in": 3.5}, "rationale": "Cutaway while Sam describes the garage."}
   ]}
]
```

- **Members**: `id` (the short name shots use), `name` (shown in name tags and captions; per-person sound settings
  find them by it), `assetId`, `use` (`both`, the default: picture and sound of one person; `sound`: a person's
  audio only; `picture`: a shared picture such as Zoom's gallery view, whose sound is not mixed), `start` (seconds:
  where the recording starts on the conversation's clock; negative when it started before), `drift` (parts per
  million: how much faster the recording's clock runs; member time = (t − start) × (1 + drift/10⁶)), `personId`
  (People), `tag` (the name tag's second line), `crop` (fractions removed from each edge: app chrome), `framing`
  (the clip framing fields: fill/fit, faces, focus, zoom), `look` (the colour correction, below), `muted`.
- **Gate**: how each track gets quieter while that person isn't speaking (`speaking`, by `depthDb`, 6–40) or not at
  all (`off`, absent). New conversation cuts are written with `{"mode": "speaking", "depthDb": 18}`.
- **`sampleAligned`**: each member is placed at the very sample sync found (the plan carries trims below a
  frame; drift plays from a resampled copy). New cuts and `conversation pin` set it; a pin without it places members
  on frames, exactly as cuts made before it did. Cut sheets say "placed to the sample".
- **Shots** (on the clip, in time order, conversation seconds): exactly one of `member`, `layout`
  (`side-by-side`, `stacked`, `pip`, `grid`, with `members` in order; default everyone with a picture), `card`
  (text, with `members[0]` softened behind it), `broll` (`assetId`, `in`: other footage, the conversation's sound
  continuing). `dissolve` (seconds) blends the switch into a shot; absent is a cut. `rationale` is the "Why:".
  Before the first shot's time the first shot shows; a shot lasts until the next one.
- **`clip.mutedMembers`**: people whose sound is left out of this clip (the off-camera interviewer's question
  under a text card).
- **Ops**: `setConversation {id: name, conversation: pin | null}`; shots change with `updateClip` (the list is
  replaced whole, like every array in a merge patch). `splitClip`, `trimClip`, `deleteRange` and `removeWords`
  keep the picture: each part keeps the shots in its range and the part that starts inside a shot starts with
  that shot (Go and `web/src/lib/editops.ts`, golden-tested together).
- **Validation**: unknown conversation (`conversation_unknown`), member or recording (`conversation_member`,
  `missing_asset`), a speed other than 1 (`conversation_speed`), shots out of order (`shot_order`), a shot that
  isn't exactly one kind (`shot_kind`), a layout with the wrong number of people or someone without a picture
  (`layout_members`), side by side on a vertical cut (`layout_shape`, a warning: stacked fits), a shot shorter than
  half a second (`shot_short`, a warning), a picture that isn't there yet at that moment (`member_not_recording`,
  a warning), a pin nobody plays (`conversation_unused`, a warning).

## Sync

`metarush conversation create --members <assets…> [--names …]` lines the recordings up by their sound:

1. Each recording's sound is read at 16 kHz mono in the speech band (300–3400 Hz) and turned into energy every
   10 ms; its **onsets** (the rise of the level above the background + 6 dB, square-rooted so loud and quiet
   microphones weigh closer) are split into the track's own voice (rising to within 15 dB of its speaking level) and
   the quiet rest (what it picked up from elsewhere).
2. **The reference** is a picture member with sound when there is one (Zoom's main recording or a gallery view: the
   call's mix, which has everyone in it), else the longest recording.
3. **Cross-correlation** (FFT, ±10 minutes by default, `--search`). Against the mix, a person's own onsets give one
   sharp peak. Between two people's own tracks only what each microphone picked up from the other is shared; on an
   online call that arrives a network delay late in each direction, so there are **two peaks** (own × other's quiet,
   and the reverse), the true offset is halfway between them and their distance is twice the call's delay (reported).
   One peak: one-way pick-up (possibly off by the delay, said so). No clear peak (both wore headphones): the files
   are taken to start together, unsure, and the person is asked to nudge or use a clap.
4. **Drift**: 30-second windows every 15 s (±0.25 s around the offset, sub-sample peaks from a parabola), a line
   fitted with outliers dropped (MAD) when the windows span a minute or more; its slope is the drift in ppm
   (`playbackRate` = 1 + drift/10⁶). Recordings from different computers drift by tens to hundreds of ppm.
5. **Confidence** (0–1; very sure ≥ 0.8, fairly sure ≥ 0.5, else unsure) from how far the peak stands out (a z-score)
   and how well the windows agree with the line.

`conversation nudge` moves one member by seconds or frames (`--frames 1`); `conversation sync --clap` lines up the
sharpest loud transient in each member's first minute (refined on the samples); `--mark sam=0:05.9,maya=0:03.2`
lines up moments a person picked; `--together` takes the files to start together. Files a call app wrote to start
together (Zoom's separate audio since 5.8) are lined up as the app recorded them (`app`, very sure). Cuts keep
their alignment until a person uses the new one ("Use it in the cut", `conversation pin`).

## Sound

Each member whose sound is used (`both`, `sound`) plays its own track over the conversation clip's whole sound
window, at its offset and drift (the plan's audio element has `playbackRate` = 1 + drift/10⁶; with `sampleAligned`
the renderer plays a copy resampled to the call's clock instead, and the trim keeps its part below a frame), as an
ordinary audio element (`conversation: <clip>`, `member: <id>`). The clip's own envelope (volume, the transitions at its
ends, ducking under narration, the master gain) is multiplied into every member's, so Go stays the only mixer.

- **Whose words are whose**. A real recognizer also transcribes the other person coming out of the
  speakers — often as a second voice that says *more* than the member (the guest talks longer), sometimes as
  different words. With each track's momentary loudness measured: a voice in a member's own transcript clearly
  quieter (10 dB) than the loudest voice there is someone else; a word heard 18 dB or more under the track's
  speaking level while another member speaks (then or up to 0.7 s before: the call's delay) is picked up, whatever
  it was heard as; the same word in two tracks within 0.7 s is picked up where it is 10 dB under the track's level;
  a stretch mostly picked up (60 %) goes whole. A track's speaking level is the loud end of what it hears above its
  background (their own voice is the loudest thing in their microphone), or the middle of its words when higher.
  Without measurements: the voice that says most is the member, and the later copy of a word is the pick-up.
- **Who is speaking** (activity, the same for the gate, the automatic cut, clean-up and QA): each member's own words
  (0.05 s before, 0.1 s after), plus sound without words (laughing, "mm") at least 0.3 s long well above their
  background and near their speaking level.
- **Gating** (`gate.mode: speaking`): each track plays at full level while that person speaks (opening 0.08 s
  early: the future is known) and holds 0.25 s after, then lowers by `depthDb` over 0.15 s. Crosstalk keeps both
  open. Without headphones this removes most of the echo and the other person's voice from the wrong microphone.
- **Levels and clearer voices** (Sound) apply per track: `mix.voices` by the member's name (a level over
  the whole track; clean, studio or cloud on their recording), and the copies cover the stretch of the recording the
  cut plays. **Even out voices** (`mix.levelSpeech`) levels each person's speech toward the typical level of the
  people in the cut; the conversation clip then counts once, at that level.
- **Captions** come from each person's own transcript, on the conversation's clock, named with the member's name
  (exact: a track is one person), so `captions.showSpeakers` names them without diarization. Bleed words are left
  out.

## Choosing the picture

`metarush conversation cut <conversation> --project <p> --style <style>` writes an ordinary editable cut (one
conversation clip with shots, its Why: notes, name tags as lower thirds the first time each person is seen) and
checks it like any plan (`--apply` saves it). The rules, from the per-person speech activity:

- **Cut to whoever is talking.** A turn starts when someone speaks for longer than a short reply ("mm-hm", "yeah":
  under 0.8 s doesn't take the picture).
- **Voices lead pictures by default.** The picture follows the new speaker 0.3–0.6 s after their first word (a
  J-cut: we hear them before we see them); in reaction-heavy cuts it goes to the listener as the speaker finishes
  (an L-cut). The reasons say it in those plain words.
- **Never mid-word.** A switch lands in a pause between words of whoever is speaking; with no pause within reach it
  lands at a word's edge.
- **Minimum shot length** per style; shorter shots are absorbed by their neighbours.
- **Listening reactions**: a laugh or a short reply by the listener during a long turn shows the listener for
  1.5–2.5 s; a turn longer than the style's limit gets a reaction shot at a sentence end.
- **Dynamic layouts**: when two or more people talk or laugh together for 0.6 s or more, the picture goes to the
  split layout (side by side on landscape, stacked on vertical, the grid for three or more) and back to the speaker
  afterwards.

| Style | What it does |
|---|---|
| `podcast` (host + guest) | Opens on both, then the speaker; shots at least 2 s; reactions on laughs and replies; split when both talk; at most 30 s on one picture. |
| `interview` (off-camera interviewer) | Never shows the interviewer (`--interviewer`, else the person who asks the questions). `--questions audio` (default) keeps the questions over the guest listening; `--questions cards` removes them and shows each as a text card over the guest. |
| `panel` (3+) | Opens on the grid; the speaker otherwise; the grid when three talk; shots at least 2.5 s. |
| `reactions` | Shots from 1.2 s; listeners every 4–6 s of a long turn; L-cuts. |
| `broll` (talking head and cutaways) | The speaker, with cutaways from `--broll` footage 3–5 s long during long turns (never over a turn's first or last words), matched to what is said by the footage's on-screen words and titles. |

Every shot says why it is there ("Sam starts talking at 0:15.4; the picture follows 0.4 s later (we hear them
before we see them), in a pause"). The result is an ordinary cut: the timeline, the cut sheet, Claude Code and the CLI change it
like any other.

## Layouts

Vetted layouts, their geometry computed in Go (`editspec.LayoutTiles`) and drawn verbatim by Remotion:

| Layout | Shape | People |
|---|---|---|
| `side-by-side` | landscape, square | 2–3, equal columns |
| `stacked` | vertical, square | 2–3, equal rows |
| `pip` | any | 2: the first full frame, the second in a rounded inset (bottom right) |
| `grid` | any | 2–9: the most even grid for the frame |

Each tile frames its person from the visual index: the face box (median of the biggest face over the shot)
is scaled to the same height in every tile and its eyes placed on the same line (a third of the way down), so
people look the same size with matching headroom; the crop never shows outside the picture (or the member's
`crop`). Without a face, the member's own framing, else the middle. Name tags (`name` and `tag`, from People and
their notes) sit at the bottom of each tile in layouts; a person's first appearance gets a `lower-third.clean`.

**How layouts look** (from the [layout pack](layout-packs.md)): a pin's `layoutStyle` moves the inset to
another corner, makes it small or large, square or a circle (a round camera bubble), and draws a line round it in one
of the brand's colours; side by side and stacked get a gap (none, thin, or wide with the same margin round the
pictures), a line round each picture, the brand's background between them and rounded corners. Go places it all
(`LayoutTilesStyled`, `PlanTile.border`, `PlanShot.backdrop`); without a style the picture is exactly as before. A
new cut's conversations and conversations added later take the pack's; `metarush content pack apply` puts it on the
latest cut.

## Clean-up

`metarush conversation cleanup <conversation>` (and the Conversation tab's "Check the call") finds suggestions;
`conversation decide <conversation> c1 c3 --accept|--skip|--reopen` records what a person chose. Accepting is an
ordinary checked change to the latest cut: new shots over frozen pictures and crops first, then the
stretches to cut, latest first (`deleteRange`). Running it again keeps the decisions (same kind, member and moment).

| Kind | Found by | Accepting |
|---|---|---|
| **Call talk** | sentences about the call ("can you hear me?", "you're on mute", "you froze", "I lost you", "are we recording?", "can you see my screen?") and a short yes-style answer to such a question | cuts the stretch |
| **Dead air** | nobody heard for 2.5 s or more (`--dead-air`), the start and the end too | shortens it to 0.6 s (0.3 s at the ends) |
| **Frozen picture** | `freezedetect` on each member's original (1.5 s or more; a preview copy can smooth a stall over), else their stored evidence | shows someone else over it, then goes back (`switch`); flagged with one picture |
| **Sound dropout** | digital silence (under the background − 30 dB, and −80 dB) 0.08–2 s long with the person talking within 0.6 s on both sides | cuts it when no word overlaps it; inside a word it is flagged ("Noted": cutting can't bring the word back) |
| **The app's labels** | on-screen text (the visual index) present for half the recording or more within 15 % of an edge (name labels, "Recording" banners) | crops that side off the member (at most 22 % a side), on the cut's pin and the conversation |

## Webcams that match

`metarush conversation match <conversation>` measures each member's picture on about a dozen frames across the
recording (every length/12, at least 2 s), where the skin is: the middle of the index's typical face box (else the
upper middle of the picture, where webcams put faces), leaving out the darkest and brightest tenth (eyes, mouth,
highlights). It fits a `look` (exposure, contrast, saturation, warmth, tint, each clamped to a correction's range)
to the reference member with Nelder–Mead: **brightness (L\*) and contrast (L\* spread) fully, colour (a\*, b\*)
half way** — people's skin differs; a blue cast on a face is the camera. Remotion applies the look as one sRGB
colour matrix computed by Go (`editspec.LookMatrix`); the same matrix on the measured pixels gives the "after"
numbers: ΔE (CIE76) and the L\* difference to the reference, before and after, kept on the conversation
(`members[].color`) and shown in plain words ("colour difference 19 → 3.9, brightness 17 → 0"). `--off` plays the
webcams as recorded again.

## Imports

`metarush conversation detect <folder>` says what a set of files is; `metarush conversation import <folder>
--project <p>` adds the files and makes the conversation. The dashboard never reads this computer's folders: when
two or more files arrive together (the Add box, or a drop anywhere), it asks `conversation.detect` about their
names (a dropped folder keeps its relative paths) and offers **Make a conversation from these**.

| Source | Recognized by | Members |
|---|---|---|
| Zoom local recording, "Record a separate audio file for each participant" (5.8 and later) | `<date> <time> <topic>` folder, `video<n>.mp4`, `audio<n>.m4a` (the mix), `Audio Record/audio<Name><i><n>.m4a` | each participant's audio (`sound`, the squeezed name split: "MayaLindqvist" → "Maya Lindqvist"), the video (`picture`); the mix is left out. They start together (`app`, very sure); the topic names the conversation |
| Zoom before 5.8 | `zoom_0.mp4`, `audio_only_<i>_<Name>.m4a` | the same, lined up by their sound with a note (muted stretches were left out of each track) |
| Zoom cloud download | `GMT<date>_Recording….mp4`, `Audio only - <Name>.m4a` | each person's audio, the first video |
| Riverside | `riverside_<name>_<raw-video-cfr\|raw-video\|video\|raw-audio\|compressed-audio>_<studio>_<n>.<ext>` | one per person (video before audio); raw tracks start when each person started, so they are lined up by sound; the studio names the conversation |
| SquadCast and any folder | two or more recordings | each recording, named from its file ("ana_cam" → "Ana"); a lone video named like a gallery or meeting view among audio files is the shared picture |
| A screen recording made in the dashboard ([recordings](recordings.md)) | the recorder | `screen` (picture, shown whole; its sound mixed only when it is its own), `camera` (picture) and `voice` (sound), lined up by the browser's clock (method `clock`) and checked by the microphone's copy in the pictures (method `audio` when it agrees) |

## Outputs

- **The episode**: `conversation cut` writes 16:9 by default (`--shape vertical|square` for others).
- **Best moments**: `metarush conversation moments <conversation>` proposes stretches that stand on their own: they
  start cleanly (a turn's first sentence, or after a pause) and end cleanly (the turn ends or a pause follows), stay
  out of call talk and dead air and have no pause of 2 s inside; 15–60 s by default (`--min`, `--max`). Each says
  why it might work — a question and its answer (+2), people laughing or talking together (+1.5), a strong first
  line (a claim, a number, "the trick", +1), both people in it (+0.5), a natural speaking pace (+0.5), a length near
  the middle of the range — and overlapping ones give way to the better. Claude Code chooses with the person
  (skills: the strongest true moment, not the loudest).
- **Shorts**: `metarush conversation short <conversation> --from … --to …` makes each a new project linked to the
  conversation's project (`project.link`): the automatic cut of that stretch, vertical by default, captions with
  names. `--nest` plays the episode's own stretch as one nested clip (`cut:<project>/<revision>`), same shape
  as the episode. Without `--apply` it says what it would make.

## Cut sheets

```markdown
## Conversations

- call: cnv_01jabcdefghjkmnpqrstvwxyz0 · quieter when not speaking 18 dB
  Member: maya “Maya” · maya-cam · starts at 0:00 · tag “Host”
  Member: sam “Sam” · sam-cam · starts at 0:12.345 · drift +20 ppm · crop top 6%, bottom 8% · focus on faces · zoom 1.1× · look exposure 106%, contrast 98%, warmth -3%
  Member: gallery “Gallery view” · zoom-gallery · picture only · starts at 0:00.2

## Clips

1. talk: conversation call 0:00 → 12:30 · without sound from maya
   > Maya: “So tell me…” … Sam: “… exactly.”

   Shot: 0:00 side by side maya, sam · why “Open on both of them.”
   Shot: 0:04.2 maya
   Shot: 0:15.8 sam · why “Sam answers; the picture follows 0.4 s later.”
   Shot: 1:02.5 card “What did you build first?” over sam · dissolve 0.3 s
   Shot: 1:10 cutaway garage from 0:03.5
```

`b-roll` and `broll` are read as `cutaway` too. The section and the `Shot:` lines are lossless (`TestRoundTripEveryField`, the random specs and both fuzzers cover
them); quoted words on a conversation clip are found in the conversation's merged transcript.

## The timeline and the dashboard

- **Conversation tab** (shown once a project has a conversation): the recordings with how each lines up in words
  and how sure, "a frame earlier/later", "Line up again" (by sound, together, on a clap), "Use it in the cut" when
  the cut plays an earlier alignment; **Make the cut** (style cards, shape, the layout when people talk together; a
  styled dialog first when it replaces a cut — never a browser dialog); **How it plays** (quieter while not
  speaking, names on tiles); **Clean up the call** (Accept, Skip, Noted, Undo skip); **Match the webcams** (the
  measured before → after); **Shorts** (Find the best moments → Make a short → Open the short). **Make a
  conversation** picks recordings already in the project.
- **Timeline**: a conversation clip shows its shots (each person's thumbnail and colour, layouts split, cards,
  cutaways) with labels; with the clip selected each shot is a button: change it for that shot or from the playhead,
  or remove the switch — through the working copy (`updateClip` with the new shots), like every gesture.
- **Sound tab**: each member is a person there (their level and a clearer voice apply to their own track).
- **Add box**: "These look like one call — Make a conversation from these".
- **Claude Code panel** (`internal/assistant/conversation_actions.go`): Claude reads conversations
  (`conversation list|show|styles|moments`, all on its read-only list) and asks for changes with the ordinary
  commands; each becomes a card the server builds from Claude's command and previews with nothing saved
  (`conversation.PreviewCut`, `PreviewDecide`, `PreviewAngle`, `PreviewShort`: no version, no decision, no
  remembered style): **make the cut** in a style ("Make an automatic cut of “Garden call”: reaction-heavy", the
  style in words, the shots, the diff, and that it replaces the current cut, which stays in History), **accept or
  skip clean-up suggestions** (one — "Cut out the talk about the call at 0:00.4–0:02.2 (“Hi Sam, can you hear me
  okay?”)" — or all that are open, listed, by the exact suggestions the card shows), **change an angle** ("Show
  Maya and Sam side by side from 0:12.3 in the conversation"), **make a short** (its new project's name), and
  **look the call over** (the clean-up search). Confirming runs the same op as the CLI with apply, as the person:
  a confirmed cut then remembers its style, accepted suggestions are marked with the version they made. A call
  folder is added from the Add box, and the Conversation tab's own changes (line up again, nudge, match, settings)
  point there.

## The renderer

`PlanClip.conversation` holds the resolved shots in clip frames: kind, tiles (rectangle in output pixels, where
the picture sits inside it, the member's recording, `trimBefore` at the shot's first frame and, with
`sampleAligned`, `trimFraction` for the rest below a frame, `playbackRate`, a colour matrix, the name tag), cards
and dissolves. Drifting members' sounds are listed by `editspec.DriftCopies`; the render worker makes those copies
(ffmpeg: decoded from the start, trimmed by sample, resampled to 48 kHz) before Remotion runs. `video/src/clips/Conversation.tsx` draws them (muted pictures;
the sound is the plan's audio elements). Render self-tests: angle switches, a split layout, picture-in-picture.

## QA

Three checks for cuts with conversation clips (QA version `qa-4`, docs/qa.md), from `editspec.ConversationPlays`
(who is heard and seen when, on the timeline) and the conversation's record:

- `conversation_voices` (measured on the render): each person's loudness over the words only they say against
  the loudest person's — 6 dB quieter is a warning with a fix that turns them up (`mix.voices`), 20 dB an error;
  someone whose microphone the clip leaves out while they talk is a note. Per-person speaking time, screen time and
  loudness go to `measurements.conversations`.
- `conversation_picture` (from the plan): one person alone on screen longer than the style's limit (podcast 30 s,
  interview 45 s, panel 30 s, reactions 12 s, cutaways 40 s), someone talking that long unseen, or someone doing a
  fifth of the talking but on screen less than half that share (an off-camera interviewer is exempt); the fixes are
  new shots.
- `conversation_glitches`: a member's frozen webcam on screen (their evidence and clean-up's freezes; the fix shows
  someone else over it), a sound dropout clean-up found where that person is heard (unless someone chose to keep
  it), a recording lined up unsurely and never nudged. Frozen frames there count as explained in `frozen_frames`.

## Commands

```bash
metarush conversation create --project <p> --members <clip> <clip>… [--names "Maya,Sam"] [--use both,sound,picture] [--align audio|together|none]
metarush conversation list [--project <p>] | show <conversation> [--details]
metarush conversation sync <conversation> [--clap | --together | --mark sam=0:05.9,maya=0:03.2] [--search 600]
metarush conversation nudge <conversation> --member sam (--by 0.04 | --frames 1 | --to 1.25)
metarush conversation update <conversation> [--name …] [--member sam --rename … --tag … --use … --person … --muted …]
metarush conversation pin <conversation> [--project <p>] [--apply]
metarush conversation cut <conversation> [--project <p>] --style podcast|interview|panel|reactions|broll [--shape …] [--from … --to …] [--apply]
metarush conversation angle --project <p> --at 0:31.2 (--member sam | --layout side-by-side [--members …] | --card "…" | --broll <clip> | --remove) [--apply]
metarush conversation settings --project <p> [--quieter true|false] [--depth 18] [--name-tags true|false] [--member sam --crop 0,0,0,0.1 | --no-crop] [--apply]
metarush conversation styles
metarush conversation cleanup <conversation> [--kinds call-talk,dead-air,freeze,dropout,chrome] [--dead-air 2.5]
metarush conversation decide <conversation> <c1> [<c2>…|open] --accept|--skip|--reopen [--apply]
metarush conversation match <conversation> [--reference maya] [--off] [--apply]
metarush conversation detect <folder> | --files a.m4a,b.m4a
metarush conversation import <folder> --project <p> [--name …]
metarush conversation moments <conversation> [--min 15 --max 60 --count 6]
metarush conversation short <conversation> --from 1:02 --to 1:48 [--name …] [--shape …] [--nest] [--apply]
```

Every command has `--json`; the parity test (`internal/cli/conversation_parity_test.go`) lists each op with its
command and the flags the dashboard's controls use.

## Measurements

From `go test ./internal/conversation/ ./internal/qa/` on the generated call (`scripts/make-conversation-fixtures.sh`:
two TTS voices on two "machines", each microphone picking up the other 22 dB down and 0.18 s late; Sam's file
starts 1.234 s in on a clock 208.333 ppm fast; his webcam freezes for 2 s at 0:30 and his sound drops out for
0.35 s at 0:40.332) and from the same call ingested for real (Parakeet transcripts, the Apple Vision index),
cut, rendered and checked.

| What | Measured | Truth |
|---|---|---|
| Sam's start, two-way pick-up between the two people's tracks | 1.2290 s (call delay 0.183 s) | 1.234 s (0.18 s) |
| Against the call's mix: Maya, Sam | −0.5006 s, 0.9084 s | −0.500 s, 0.914 s |
| Drift over the 318.7 s long version | 209.3 ppm, start 1.2342 s, 0.1 ms off at the end, 20 of 20 windows agree (spread 0.8 ms) | 208.333 ppm |
| Real ingest (51 s, too short for drift) | 1.229 s, fairly sure | 1.234 s |
| Sound dropout found | 40.349–40.679 s | 40.332–40.682 s |
| Frozen webcam found (on the original) | 29.996–31.996 s | 30.0–32.0 s |
| Call talk (real transcripts) | 0:00.4–0:07.3, "Hi Sam, can you hear me okay? Yes, I can hear you now. Sorry, I was on mute." | turns 0.6–7.05 s |
| Webcam match (Sam to Maya, face region, 12 frames) | ΔE 18.8 → 3.9; brightness (L\*) 17.1 → 0.0 (exposure 1.34, contrast 0.96, saturation 0.94) | — |
| Gate, two renders of the same cut, least-squares fit of the rendered sound to each microphone over Sam's answers | Maya's microphone 17.6 dB quieter while she listens (planned 18 dB); Sam's voice coming back through it from about 29 dB to about 46 dB under his own | — |
| Automatic cut on the real transcripts | switches 0.2–0.6 s after each speaker starts, in pauses; Maya's "mm-hmm" kept as a reaction at 0:18.9; both on screen at the laugh from 0:31.6; longest single shot 6.1 s | mm-hmm 19.38 s, laugh 31.76 s |
| QA of that render | both voices heard alike (−16.3 and −16.6 LUFS over the words only each says); the freeze and the dropout reported | — |

Before the loudness-based attribution, the real transcripts doubled sentences (each microphone's recognizer also
wrote the other person's words) and the opening call talk was missed (the guest's voice, saying more, was taken for
the host's own); after it, no doubled sentences and the exchange is found whole.

Placement: a pin with `sampleAligned` (new cuts, `metarush conversation pin`) places each member's sound,
clearer copy and picture to the sample: the plan carries the rest of each trim below a frame (`trimFraction`), the
renderer moves a sound's trim by Remotion's whole-millisecond start delay, and a drifting recording plays from a copy
the render worker resamples to the call's clock (`playbackRate` 1). Pins without it keep frame placement (at most one
frame late: Sam at 1.2333 s for 1.229 s), byte for byte as before. A person's own picture and sound move together
either way, so lip sync is untouched.

| Placement check | Frame placement (before) | `sampleAligned` |
|---|---|---|
| Plan: where each member's sound starts vs. sync (`TestConversationSampleAligned`, generated cases) | 1.7–16.4 ms off | under 0.02 µs |
| Render self-test, two voices with bursts at known times (`render:selftest`, sync variant) | 13.3 ms apart | 0.1 µs |
| Same, with 208.3 ppm drift (`atempo` before, a resampled copy after) | 17 ms apart (`atempo` wanders ±15–17 ms) | 0.1 µs |
| Drift copy made from an AAC file (`TestDriftCopyLinesUpToTheSample`) | — | within 2.3 µs |
| Preview: where each media element is when it seeks (`web/scripts/sync-preview-check.mjs`, Chromium) | up to a frame | within 1 µs |

Preview playback: browsers start each media element on its own 128-sample step (2.7 ms at 48 kHz) and correct
only when an element is more than 0.15 s off while playing, so two people can still be up to 21 ms apart while the
preview plays (never in the render).
