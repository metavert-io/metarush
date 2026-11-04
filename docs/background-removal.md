# Background removal for video

MetaRush can put something else behind the people in a clip: a colour, a blur of the clip itself, a picture from the
resources library or another clip. It finds the people frame by frame once, keeps them as the clip's **cut-out**, and
every cut that replaces the clip's background plays that cut-out over what goes behind — the same in the dashboard's
preview, the review copy and the final render.

(For a photo's background, see "Remove the background" in [sources.md](sources.md); for a cut-out picture made with
AI, [ai-services.md](ai-services.md).)

## Using it

**In the dashboard**: in a project's **Edit** tab, select a clip and choose **Replace the background…** beside the
player. Pick **Blur**, **Colour**, **Picture** or **Another clip**, and how soft the people's edges are; a still of the
middle of the clip shows the result, made on this computer. **Replace the background** makes the clip's cut-out first
when it has none — its progress shows in the dialog, with **Stop** — and then saves the change as a new version, with
Undo. A clip with a new background shows **Background: …** and **Take the background out**.

**From a terminal** (every command takes `--json`):

```bash
metarush footage cut-out <clip> [--range 0:12-0:40] [--model local/modnet] [--wait] [--dry-run]
metarush footage cut-outs <clip>                         # the clip's cut-outs: the part, how far along, the model, where made
metarush footage cut-out-preview <clip> --at 0:03 --with colour --colour '#00b140' [--out still.jpg]
metarush cut background <project> --clip <name or number> --with blur|colour|picture|footage \
    [--colour '#00b140'] [--blur 28] [--picture <name>] [--footage <clip> --footage-at 0:04] [--edges 2] [--wait]
metarush cut background <project> --clip 2 --off        # the clip as recorded again
```

`cut background` is a checked change like every editing verb: it shows the change and asks in a terminal, or gives a
confirm token (`--confirm <token>` saves it). When the clip has no cut-out yet it starts one and says so; `--wait`
waits for it here and then checks (or saves) the change.

**In Claude's panel**, Claude's `metarush cut background …` and `metarush footage cut-out …` become cards a person
runs. Auto runs them by itself only when the cut-out is made on a computer of the workspace (never a service
elsewhere, never a price). `metarush footage cut-outs` is a read Claude may run.

## What a cut-out is

A cut-out is the clip's own picture with its background see-through: a WebM file with VP9's alpha channel, one
frame for every frame of the clip (or of the part asked for), at most 1920 pixels on its long side, its colours
described as the clip's. It is derived media, like a fixed copy:

- **made once**: its fingerprint names the media it was made from (the original, a fixed copy, or the steady copy a
  renderer plays where the frame rate varies), the stretch, and the model; asking again for any part it covers uses
  it again. Cuts pin it by its file's SHA-256 (`spec.clips[].background.matte`), so a saved version always plays what
  it was saved with.
- **made on any computer of the workspace that can**: a `matte` job (heavy, one at a time) that a Mac takes with
  Apple Vision, and any computer with the **Replacing a video's background** component (`metarush components add
  video-cut-outs`) takes with MODNet. A job asked for with a model this computer lacks waits (up to a few minutes,
  then less often) for a computer that has it, and says where it waits; stopped from the dialog, `metarush job cancel`
  or the activity bar, it says so.
- **steady**: the matte is smoothed over time (each frame's edge follows the last one where the picture holds still,
  and follows the picture at once where it moves or cuts), so edges don't shimmer.
- **never of someone else's video**: a reference (a video kept to learn from) is never cut out, and never shown behind
  people; neither is a picture taken from one.

`metarush footage cut-outs <clip>` lists them; Settings → Storage's export keeps them (`mattes`), clearing a
project out leaves them with the footage, and deleting the footage for good deletes them too.

## In the cut

A clip says `background` in the edit spec:

```json
{"id": "hook", "assetId": "ast_…", "in": 1.2, "out": 6.4,
 "background": {"with": "picture", "picture": "beach", "feather": 3, "matte": "<sha256 of the cut-out>"}}
```

`with` is `colour` (with `colour`, `#rrggbb`), `blur` (with `blur`, pixels on a 1080 picture, default 28), `picture`
(one of the cut's pinned resources by its short name; `name#3` for a PDF's page) or `footage` (with `footage:
{assetId, in}`: another clip of the project, playing from `in` at its own speed). `feather` is how soft the people's
edges are (pixels on a 1080 picture, default 2). Cut sheets keep it in the clip's `{background: …}` block.

The check refuses a background it can't draw: a cut-out that's missing, not made yet, another clip's, or that
doesn't cover the clip (`background_matte_missing`, `background_matte_other`, `background_matte_short`, with the
command that fixes it), a colour that isn't one, a picture the cut doesn't pin, another clip that's too short or not
in the project, and a conversation or a nested cut (their footage is where to replace it). A cut-out made from other
media than the cut plays for the clip (a fixed copy made since) is a warning: it plays as saved.

**How it is drawn** (`video/src/clips/background.tsx`): the clip itself plays at the bottom (its sound, and for a
blur the picture, blurred a little larger than the frame so the blur's rim falls outside it), then what goes behind
(the colour, the picture filling the frame, or the other clip filling it), then the cut-out — placed exactly like the
clip (the same framing, focus and zoom) and played from the same moment (Go gives the cut-out's trim in the cut's
frames, a fraction of a frame when the cut-out starts between them) — its edges softened by an SVG filter that
blurs the cut-out's own alpha and keeps the cut-out only where that is (so edges fade inward, never into a dark rim).
Renders read the cut-out with Remotion's `OffthreadVideo transparent` (frames as PNG, alpha kept), which the
renderer refuses without the cut-out's file (`checkMedia`); the preview plays it in the browser's own video, which
shows VP9's alpha in Chrome, Edge and Firefox. **Safari plays VP9 without its alpha**, so there the preview plays the
clip as recorded (the dialog says so); the review copy and the final render always show the new background. The
render self-test's `background` variant draws a moving square's cut-out over a colour, another clip and a picture,
and checks the square is where the clip has it and the clip's own background shows nowhere.

## Models

| Model | Where | Licence | How it was picked |
|---|---|---|---|
| Apple Vision person segmentation (`local/apple-vision`, accurate) | a Mac (macOS 12+), through MetaRush's own helper (`metarush-vision segment`, protocol 3: frames in, mattes out, its sequence handler kept across frames) | part of macOS | the default on a Mac: fast and steady, every person in the picture |
| MODNet portrait matting (`local/modnet`) | any computer with the **Replacing a video's background** component (26 MB, pinned by SHA-256; run through onnxruntime) | Apache-2.0, code and models ([MODNet's README](https://github.com/ZHKKKe/MODNet/blob/28165a451e4610c9d77cfdf925a94610bb2810fb/README.md#license); the ONNX export is Xenova's, Apache-2.0) | the open model where Apple Vision isn't: a true alpha matte (hair stays soft), small, portrait-trained |

Considered and left out: **RobustVideoMatting** (GPL-3.0); **BiRefNet** (MIT, MetaRush's picture cut-outs off the
Mac — about 4 s a frame, far too slow for video); **PP-HumanSeg** (Apache-2.0, fast but a coarse 192 × 192 mask with
hard edges); **BackgroundMattingV2** (MIT, but it needs a clean plate: a shot of the background without the people).
Every model's licence was read from its own repository; none is GPL or non-commercial.

Which model makes a cut-out follows the other kinds of AI work ([ai-services.md](ai-services.md)): one asked for
(`--model`), else the project's Content Settings (`ai rules set --pin video-background=local/modnet`, or forbidding
`local`), else the usual one where it can run — on a computer of the workspace first. `metarush ai models --kind
video-background` and `metarush ai local --kind video-background` list them.

## Services elsewhere

None of the AI services MetaRush works with (Google Gemini, ElevenLabs, D-ID, HeyGen, sync.) removes the background
of video, so no service elsewhere is offered. The kind of work is ready for one: `video-background`, whose request
sends the clip's part as a picture-only file (role `clip`: footage and the faces in it, never its sound) and whose
answer is a matte or a video with alpha, priced by the second of footage. An adapter for a service that does this
needs: its price per second and models in `catalog.json`; the part sent once (What may leave asks first for footage
and faces, the project's AI services rules decide, a person agrees to the price); the answer kept by the cut-out's
keeper (`matte:` work), which makes the WebM from it on a computer of the workspace and records the cost on the
generation; deleting what it keeps where its API allows; and passing `aiconformance.Run` against a fake of it
(`fakeai` stands in today). Candidates to look at, none checked against its current terms: services that sell
background removal for video through an API, and hosted open models whose licences allow commercial use (most
hosted video matting is RobustVideoMatting: GPL, out).

## Saying so

A replaced background is footage changed with AI, as a photo's background taken away is: Content Credentials list the
cut-out as an ingredient whose source is algorithmically enhanced (`models.AIWorkOf("video-background")` =
`enhanced`), the delivery's record lists it with what found the people, and the quality check (qa-8) notes where the
background is replaced and what made it (`background_replaced`, information), so people can say so when a scene looks
real. It also finds a cut-out that is gone, unmade or too short (`background_matte`, an error) or made from other media
than the cut plays (`background_matte_stale`, a warning).

## How well it works

Measured with `scripts/fetch-cutout-clips.sh` and `go test -tags matteeval -run TestCutOutEval ./internal/matting/`
(public-domain clips of real people, kept out of the repository; on a Mac mini shared with other builds):

**Real footage** (four seconds of one shot each; "edge flicker" is how much the matte's edges change from frame to
frame where the picture holds still, 0–255, before and after smoothing):

| Footage | Model | Matte | Per frame | Frames with someone | Edge flicker |
|---|---|---|---|---|---|
| a man talking outdoors, 1080p | Apple Vision | 1280 × 720 | 34 ms | 100% | 32.8 → 16.5 |
| | MODNet | 896 × 512 | 210 ms | 100% | 9.1 → 4.7 |
| a woman with curly hair, vertical 1080p | Apple Vision | 720 × 1280 | 36 ms | 100% | 40.6 → 15.2 |
| | MODNet | 512 × 896 | 210 ms | 100% | 64.0 → 22.8 |
| several people in a busy room, 1080p | Apple Vision | 1280 × 720 | 35 ms | 100% | 18.2 → 8.9 |
| | MODNet | 896 × 512 | 212 ms | 100% | 81.8 → 31.0 |
| an astronaut before a busy background, 1080p | Apple Vision | 1280 × 720 | 35 ms | 100% | 16.3 → 8.0 |
| | MODNet | 896 × 512 | 217 ms | 100% | 25.7 → 11.2 |
| an animation with nobody in it, 1080p | Apple Vision | 1280 × 720 | 35 ms | 30% | 65.5 → 23.9 |
| | MODNet | 896 × 512 | 210 ms | 100% | 41.2 → 17.6 |

**Against a known matte**: a woman filmed against a green screen (keyed for the reference: her upper body, 360 × 460)
placed over the outdoor clip, four seconds:

| Model | Mean error | Overlap at half (IoU) | Error along the edges | Per frame |
|---|---|---|---|---|
| Apple Vision | 1.3% | 0.968 | 13.5% | 35 ms |
| MODNet | 1.0% | 0.977 | 9.9% | 153 ms |

**A longer stretch, made whole** (the matte, then the cut-out file a person waits for; the outdoor clip):

| Model | Stretch | The matte | The cut-out file | In all, per minute of footage |
|---|---|---|---|---|
| Apple Vision | 49.7 s, 1,490 frames | 61 s | 51 s | 2.3 minutes |
| MODNet | 20.1 s, 601 frames | 134 s | 18 s | 7.6 minutes |

So a ten-minute clip takes about 23 minutes on a Mac with Apple Vision and over an hour with MODNet on a busy
computer; the job says how far along it is, a part of a clip is made on its own, and a cut-out is made only once.

What this says: **Apple Vision is the better default where it runs** — six times faster, steady with several people
and busy backgrounds, and it finds nobody where nobody is most of the time (its 30% on the animation is shapes it took
for people). **MODNet's mattes are the most exact** where it sees one person (closest to the keyed reference, softer
along hair), but it was made to cut out a portrait: it always keeps something, and with several people or a crowded
picture its edges flicker more. Smoothing halves the flicker or better everywhere. (Measured on 4 November 2026 on a
Mac mini, M-series, shared with other builds: load average 10–18; Apple Vision at its accurate setting, MODNet on 4
threads of onnxruntime.)

## Tests

`go test ./internal/matting/ ./internal/mattes/ ./internal/editspec/ ./internal/qa/` (stand-in models: no download,
nothing real sent), the render self-test's `background` variant (`cd video && npm run render:selftest -- --only
background`), and the browser run `scripts/background-e2e.sh` (ports 5830–5839): a clip's background replaced from the
dashboard, its cut-out made by a stand-in model, the preview and a review copy showing the new background.
