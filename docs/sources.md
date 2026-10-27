# Sources: names, info cards and fixes

Every clip, recording, piece of music and picture a project uses is a **source**. This page covers three things a
person sees about each one: its **descriptive name** (and the descriptive names of cuts and versions), its **info
card**, and its **fixes** — the pre-processing MetaRush can do to it ("Steady the camera", "Remove hum"). It also
covers **Generate video** in the drop box.

- [Descriptive names](#descriptive-names)
- [The info card](#the-info-card)
- [Fixes](#fixes)
- [How each fix works, and what it measures](#how-each-fix-works-and-what-it-measures)
- [Steadying the camera: the comparison](#steadying-the-camera-the-comparison)
- [Generate video in the drop box](#generate-video-in-the-drop-box)
- [From a terminal (and Claude Code)](#from-a-terminal-and-claude-code)

## Descriptive names

A source has two names: its **original file name** (`IMG_0042.MOV`, never changed, shown on the info card and behind
Details) and its **title**, which is what everything else says ("Lena's call with Maya (32 min)").

**Who named it** is recorded with the title (`titleBy`): the file name (the default), MetaRush, or a person. MetaRush
never replaces a name a person typed.

**At import** MetaRush names the source from its first look at it, in this order of strength:

| What MetaRush knows | Example |
|---|---|
| A link's page title (imports from YouTube, TikTok, X, the web) | “How to prune tomatoes” |
| A call: the people in it, by their voices and faces | Lena's call with Maya (32 min) |
| A screen recording: who talks, or the title on screen | Lena's screen recording (5 min) |
| People in a video, and what they talk about (the words said most, from the transcript) | Maya talks about the garden (4 min) |
| A title on screen at the start | “Spring Open House” (2 min) |
| A voice recording | Voice recording about the garden (2 min) |
| Music: the title and artist the file carries | “Morning Light” by Ana Ruiz |
| A camera's own file name (IMG_…, DSC…, GX…, PXL_…) and the day it was recorded | Video from 12 Oct 2026, 3:40 pm (2 min) |

When none of these is known, the title stays the file name until a person changes it. The name gets better while the
first look runs (the transcript, the voices, what's on screen): until every first analysis of the source has
finished, MetaRush updates the title with each better name, unless a person has named it. After that the name stays
put, and a better name MetaRush finds later (someone names a voice, say) is offered as a **suggestion** next to the
title and on the info card ("Better name: Lena's call with Maya"), applied with one click. Over a name a person
typed, MetaRush offers nothing: that name is theirs.

**Sources added before descriptive names** keep their titles. MetaRush works out a suggestion for each (a catch-up
step, `name-suggestions` v1), and a project's Sources offers them together: "MetaRush has better names for 12 clips"
→ **Use all** (or **See them** first; `metarush asset names --accept-all` in a terminal). Using all never touches a title a person typed: older titles that differ from the file name
(someone renamed it, or it came from a link) are kept, and the answer says how many were kept.

**Cuts and versions** get descriptive names too:
- A **cut** (a project's main cut, or another cut of it) has a name kept outside the edit (`branches.<cut>.title`), so
  renaming it never makes a new version and never undoes an approval. Until someone names it, the dashboard shows the
  suggestion: the version's own title when it is descriptive, else what the brief says it is for ("Main awareness
  video"), else its shape and length ("Main 45-second vertical cut"). A title like “fernbrook main v2” (the project's
  name, the cut's name and a version number) is not descriptive, so it is never used as the cut's name.
- A **version** has a short **note** saying what changed ("flicker fixes and headline cards"), kept with the version
  but outside the edit: changing the note never changes what the version plays. A change saved with a note (a plan's
  `note`, a cut sheet's `change: {note: …}`, Claude's plans and sheets) keeps it; otherwise MetaRush writes one from what was asked ("Start the second
  line later") or, failing that, from the changes ("3 clips trimmed, a title added"). People can change it; MetaRush
  never replaces a person's note.

**One name helper.** Every surface names things through the same helpers: the dashboard, the CLI's plain output, the
Claude Code panel (`internal/assistant/names.go`: clips by their title, versions as "version 4, “flicker fixes”" with
their note, cuts by their names; its cards rename clips and cuts, take better names, tick fixes and move a cut to a
fixed copy), cut sheets (the footage list keeps stable IDs and adds the title and the file name as a
comment: `kitchen: ast_… sha256:…   # “Lena's call with Maya” · IMG_0042.MOV · 32:10 · Lena, Maya`), to-dos,
notifications and the activity log (which describe entries with today's names).

## The info card

Each source has one info card, built on the server (`asset.card`, `metarush asset card`) so the dashboard, a terminal
and Claude see the same thing:

- **Its name and its original file**, with the suggestion when there is one, and who named it.
- **Where it came from**: when and how it was added (dropped in the browser, a terminal, a watched folder, the browser
  extension), the link it was imported from (site, account, date, license), when it was recorded and on what (the
  camera or phone the file names), the app that recorded it, how a generated shot was made (the prompt and the model),
  the Remotion project and composition it was rendered from.
- **What MetaRush found**: its length; the picture (size, frame rate, HDR, a frame rate that changes while it plays,
  how shaky, how grainy, the colour); the sound (channels, how loud, background noise, hum); the people (voices and
  faces, named or not); words on screen; what it is about.
- **Where it's used**: projects, collections, and the cuts that play it (which versions, and whether they play the
  original or a fixed copy).
- **Its fixes** (below).

In the dashboard the card is under a clip's player in Sources ("About this clip", with its fixes beside it), and a
picture's fixes are in its details in Resources. A better name shows next to the clip's title ("Better name: … · Use
it").

## Fixes

Each kind of source has a few fixes, shown as checkboxes. MetaRush measures every source after its first look and
**marks the fixes that fit**, saying why ("The camera shakes a lot", "There's a hum").

| Kind of source | Fixes |
|---|---|
| Camera video | **Steady the camera** · **Balance colour and brightness** · **Reduce grain** |
| Call or screen recording | **Even out each person's volume** · **Clean up voices** · **Reduce compression blockiness** · **Steady the camera** |
| Voice or audio recording | **Clean up voices** · **Even out loudness** · **Remove hum** |
| Music | **Even out loudness** · **Trim silence at the ends** |
| Picture or logo | **Remove the background** · **Trim empty edges** · **Make it larger and sharper** |

MetaRush works out the kind (a call: a recording in a conversation, or one a call app's name gives away; a screen
recording, by its name or the app that recorded it; music: a music rights record, the file's music or artist tags, a
music-like name, or fewer than 20 words a minute over at least 20 seconds); a person can change it on the card.

**The original is always kept.** Ticking fixes makes a **fixed copy**: one file made from the original with every
ticked fix, in a fixed order, recorded with its **fix steps** (each fix, its settings and the version of every tool;
`fixSteps`) and stored by its content (SHA-256). The ticked fixes' **fingerprint** names the copy (`fingerprint`), so
the same fixes on the same original are never made twice, on any machine (`source_copies`; their names changed once, keeping
every value). The work is a `prepare` job on a worker (heavy: it waits on battery like transcripts), and the
activity bar shows it ("Steadying the camera in “Kitchen chat”, 40%"). **Unticking** goes back: with nothing ticked the
original is used again at once, and a combination made before is reused.

**Everything after a fix uses the fixed copy.** Once the copy is ready it becomes the source's **current media**:
the preview copy, the thumbnail, the sound wave and the steady copy are the copy's, and MetaRush re-runs what the fix
affects on it — a sound fix: the transcript and the voices (names people gave are kept; people's transcript fixes are
carried over by the transcript's own re-transcription path), the loudness and the pauses; a picture fix: what's
on screen and the faces (names kept), the cuts and movement. Claude, cutting and new renders use the current media.

**Saved versions never change** (render stability). A version plays exactly the media it was saved with: the
edit pins a fixed copy by its SHA-256 (`spec.fixed`: footage → copy), and a version without a pin plays the original.
Analysis is kept per media (`footage_insights.media`): a version that plays the original keeps reading the original's
loudness, music beats and on-screen index, so its sound levels and framing don't move when a copy's analysis arrives;
its words come from the transcript pins. A fixed copy reaches a saved cut only as **a new version**:
- footage put into a cut for the first time after it was fixed plays the fixed copy (the new version pins it);
- for footage a cut already plays, the info card offers **Use the steadier picture in “Main open day video”**: one
  click saves a new version that plays the fixed copy (`cut.useFixed`, `setFixed` in plans, `fixed:` in cut sheets),
  and **Use the original** goes back the same way.

Renders, previews, the quality check and timelines for other editing apps (`export timeline`, the copy named
"<title> (fixed)") all take the media a version pins.

**Music's silent ends** are not cut out of the file: "Trim silence at the ends" measures where the music starts and
stops, and MetaRush plays it from its first sound to its last wherever it is added from then on (`sound music add`).
For a cut that already plays it, **Use the trimmed music** saves a version with the track's in and out moved.

**Pictures and logos** are resources, which already have versions: a fixed picture is a new version of the
resource (with its fix steps and the version it came from), current for new uses; unticking makes the original version
current again. Cuts pin resource versions, so a cut keeps its picture until someone updates it ("a newer version is
available" → **Use the cleaner picture**).

## How each fix works, and what it measures

Each fix measures the same thing before (the mark) and after (on the copy), and the card says both ("shake: 0.65% →
0.01% of the picture's width a frame").

| Fix | How | Marked when |
|---|---|---|
| Steady the camera | vid.stab (two passes: motion, then a smoothed camera path over about a second with the zoom that keeps the edges filled, bicubic) through a vid.stab-capable ffmpeg: the one on the machine when it has it, else a pinned build MetaRush downloads into `<home>/components/stabilizer` and checks by SHA-256 (the system ffmpeg is never touched). Without one: ffmpeg's basic steadying (`deshake`), and the card says so. | The frame-to-frame shake (phase correlation on small frames, the motion minus its smooth trend) is at least 0.25% of the picture's width |
| Balance colour and brightness | Levels per colour channel from the picture's darkest and brightest 0.5% (limited: black lifted at most 12%, white stretched from at most 70%, so a dark scene stays a dark scene), which also takes out a colour cast, then a gentle brightness (gamma) correction, measured on frames across the clip | A colour cast of 6% or more, or the picture is dark (average below 30%), too bright (above 72%) or flat (less than 55% of the range used) |
| Reduce grain | A spatial and temporal denoiser (`hqdn3d`) whose strength follows the measured grain | The grain (Immerkær's noise estimate) is 3 levels or more |
| Even out each person's volume | Each person's speech brought to the same loudness (their stretches from the transcript, with short ramps), then the whole gently leveled; without voices yet, a speech leveler | The voices differ by 6 dB or more |
| Clean up voices | The clean engine of the Sound tab (DeepFilterNet and the measured polish) on the whole recording | Background noise less than 25 dB under the speech |
| Reduce compression blockiness | ffmpeg's deblocking on the 8-pixel grid, weak or strong from the measure | The steps across 8-pixel block edges are 1.25 times the steps inside blocks or more (measured on full-size frames) |
| Even out loudness | Two-pass loudness normalization (speech to the usual loudness for spoken video, music a little louder), for speech after a leveler | The loudness range is wide (speech above 12, music above 16) or it is far from the usual loudness |
| Remove hum | Narrow notches at the mains frequency (50 or 60 Hz, whichever is there) and its harmonics | A peak at 50 or 60 Hz and its harmonics 15 dB or more above the sound around it |
| Trim silence at the ends | Where the music starts and stops (quieter than −50 dB) | Half a second or more of silence at either end |
| Remove the background | A plain background (the edges' colour) made transparent from the edges in, with soft edges; for a photo, on a Mac with macOS 14 or later, its subject cut out with Apple Vision's subject lifting (the on-screen helper's `mask` command); elsewhere a photo keeps its background and the card says why | The edges are one plain colour (90% or more) and the picture has no transparency |
| Trim empty edges | The picture cropped to what is in it, with a small margin | 8% or more empty space on a side |
| Make it larger and sharper | Twice the size (four times under 600 pixels, at most 4096) with Catmull-Rom resampling, then an unsharp mask | The longer side is under 1280 pixels |

## Steadying the camera: the comparison

Measured on two shaky clips made from a photograph (a 1280×720 camera that pans slowly across it while a hand-held
shake moves and turns it, with a second picture crossing the scene; clip A: a moderate shake, about 12 pixels and
0.8°; clip B: a walking shake, 22 pixels and 1.6°), each with its steady twin. "Shake left" is the frame-to-frame
motion minus its own smooth trend, in pixels, measured by phase correlation (MetaRush's own estimate, independent of
both stabilizers); zoom is how much the picture had to be enlarged to keep its edges filled.

| | Shake left, A | Zoom, A | Shake left, B | Zoom, B | Time (10 s) |
|---|---|---|---|---|---|
| Shaky original | 5.5 / 6.2 | 1.00 | 13.0 / 15.8 | 1.00 | |
| ffmpeg `deshake` (in every ffmpeg) | 2.2 / 2.9, turning not fixed | 1.00, edges smeared | 5.0 / 5.8 | 1.00 | 29 s |
| vid.stab, smoothing 15 frames | 0.42 / 0.40 | 1.06 | 1.01 / 0.81 | 1.14 | 12 s |
| **vid.stab, smoothing 30 frames (chosen)** | **0.18 / 0.12** | **1.07** | **0.42 / 0.53** | **1.15** | 12 s |
| Apple Vision registration + Gaussian path, free zoom | 0.17 / 0.10 | 1.12 | 0.23 / 0.13 | 1.30 (capped, edges show) | 17–31 s |
| Apple Vision + a smoothed path held to vid.stab's zoom | 0.45 / 0.64 | 1.07 | 1.21 / 1.79 | 1.15 | 16–18 s |

Apple Vision's registration is the most accurate motion estimate (against the known shake: 0.08 pixels coarse to fine,
0.47–1.1 pixels at one scale, which also stops at about 28 pixels of movement), but turning it into a steady picture
needs a camera path that keeps the edges filled, and vid.stab's path does that better: at the same picture kept it
leaves a third to a quarter of the shake. So MetaRush uses vid.stab.

## Steadying a call recording

A webcam held in a hand or propped on a wobbly laptop shakes too, so **Steady the camera** is offered for call recordings
as well, marked the same way ("The camera shakes a lot", at 0.25% of the picture's width a frame). A call's picture
switches between people (speaker view: a hard cut at every switch, found to the frame by the switches step), and
steadying the whole recording at once would carry one picture's camera path into the next: vid.stab smooths the path
over about a second on both sides, and the switch itself reads as one huge jump, so the frames before a switch would
slide with the next person's shake. So:

- **Measured between the switches.** The shake of a call is measured within each stretch between two switches and
  never across one; frames where the picture changes completely (a switch not found yet) are left out the same way.
  The card says "shake: 0.65% of the picture's width a frame between the picture's switches".
- **Steadied one stretch at a time.** Each stretch between two switches gets its own motion pass, camera path and
  zoom (vid.stab, or ffmpeg's basic steadying with the same note as for camera video). Every stretch is decoded from
  its own first frame (an accurate seek a quarter of a frame early, the filters cut at its last frame so the motion
  pass never reads into the next picture) and its frames go, in order, to one encoder as raw frames: the copy has
  exactly the original's frames, rate and timing, with every switch on its frame. A stretch under 12 frames (a sliver
  of another picture at a switch) is kept as it is. Switches found on a preview copy at another frame rate are first
  put on the original's own frame (the frame whose picture changes most nearby); a call whose switches weren't found
  yet has them found at that moment (and kept). A call with no switches (one person's own webcam) is one stretch.
- **Conversations.** A conversation's recordings are calls, and each member's own recording is steadied on its own: its
  own copy, its own job, its own stretches; the others stay as they are until someone ticks them too. Cuts pin each
  recording's copy separately (`spec.fixed`), as for any footage.

Measured (`internal/fixes`, `TestSteadyingStaysWithinEachPicture`): two shaky webcams made from the fixture photographs,
joined by a hard cut, in pairs that share one side of the switch (A|B, A|C, A2|B). Steadied stretch by stretch, the
shared side comes out the same frame for frame whatever is on the other side (at most 0.5 levels apart; the switch on
its frame, all 180 frames kept), and the shake within the stretches goes from 3.0% to 0.28% of the picture's width a
frame with vid.stab (0.45% with the basic method). Steadied as one recording, the frames before the switch differ by up
to 9.8 levels depending on what follows it. The fix steps record the stretches (`switches`, `stretches`, `switchesFrom`).

## Generate video in the drop box

Under **Choose files** in the drop box — on Home and in a project's Sources ("Add to “Pond film”") — **Generate video**
opens the same Generate panel as Sources (describe the shot; an optional starting picture, from a file or a frame of
a clip; the length; the quality; the price shown before anything is sent). From Home it asks which project the shot is
for, defaulting to the one used last (the last project a generation went to, else the last one opened): generations
are always for a project, because every shot is budgeted against a project's budget and the team's daily limit,
so the Inbox is not offered. A starting picture from a file is added to the project's resources (pictures are
resources) and the shot starts on it (`generate.shot` `from.picture`, `metarush generate shot … --from <picture>`;
MetaRush can't tell from a photo whether it shows people, so the person says it doesn't before it is sent). Claude offers the same in the panel: "make
a shot of…" becomes a priced card (`internal/assistant/generate_actions.go`), and the skills say so.

## From a terminal (and Claude Code)

```bash
metarush asset card <clip>                         # the info card (--json has everything)
metarush asset update <clip> --title "Lena's call with Maya"   # rename (a person's name is never replaced)
metarush asset update <clip> --suggested           # take MetaRush's suggestion
metarush asset names [--project <p>] [--accept-all] # better names MetaRush suggests; --accept-all uses them
metarush asset fixes <clip>                        # the fixes, what fits and why, what's ticked, the copy
metarush asset fixes <clip> --tick steady,colour [--untick grain] [--kind camera] [--wait]
metarush asset fixes <clip> --look                 # measure again
metarush cut rename <project> "Main open day video" [--branch short]   # or --suggested
metarush revision note <version> "flicker fixes and headline cards"
metarush cut use-fixed <project> <clip> [--original] # a new version that plays the fixed copy (or the original)
```

## Testing

- Go: `internal/sources` (the naming rules, a topic, renames during the first look and suggestions after it, Use all),
  `internal/fixes` (each measurement on made-up media, a call steadied stretch by stretch (`TestSteadyingStaysWithinEachPicture`,
  `TestSteadyingACallStretchByStretch`, `TestEachRecordingOfAConversationIsSteadiedOnItsOwn`), pictures, the pinned stabilizer download with a fake build and
  a local server, and the whole flow on a shaky clip and a humming voice: tick, fixed copy, before and after, a new
  version per cut, untick, saved versions unchanged), `internal/editspec` (`spec.fixed` pins and checks),
  `internal/cutsheet` (`fixed:`, `change: note`), `internal/ops` (cut names, version notes), `internal/store`
  (analysis per media), `internal/vision` (the helper's `mask` command), `internal/assistant` (the panel's cards),
  `internal/generate` (a shot that starts from a picture).
- `scripts/acceptance.sh`, the "sources" step: a name at import, a rename, steadying and hum removal measured before
  and after, the fixed copy in a new version, the saved version unchanged, the original back on untick. Give it the
  pinned archive with `METARUSH_STABILIZER_ZIP` to steady with vid.stab without a download.
- `scripts/sources-e2e.sh`: the same in a browser (the info card, renaming, Steady the camera made by the dashboard's
  worker — for a camera clip and for a call recording whose two webcams shake, steadied one stretch at a time —
  before and after, Use the steadier picture, Use all, the cut's name, a version's note, a picture's fixes,
  Generate video from Home's drop box starting from a photo, with the fake Gemini API), with screenshots in light and
  dark and on a phone.
