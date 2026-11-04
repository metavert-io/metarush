# What MetaRush sees in the picture

Claude Code does the editorial reasoning by reading what MetaRush extracts, never the video itself. This page is about
the picture: how MetaRush decides which frames are worth looking at, what it knows about each clip without anyone
looking, and where that knowledge stops.

| Layer | Runs on | Cost | Where it shows up |
|---|---|---|---|
| Visual changes: cuts, slow changes, movement, activity per second | every machine (ffmpeg) | free, part of the evidence job | `footage evidence` (`visualChanges`, `activity`), `footage moments` (kind `visual`), smart sheets |
| Smart contact sheets | every machine | free | `footage sheet`, `footage keyframes`, the Frame grid tab, skills |
| On-screen index: things, animals, people, faces, gestures, text in any language, shot type, where the eye is drawn | a Mac (Apple Vision); any other computer with **Seeing what's on screen** added (open models: faces, words, shot type, where the eye is drawn) | free, a heavy job | `footage visuals`, `footage faces`, search, the On screen tab, People, To-dos |
| Faces recognized across clips | the computer that indexes (SFace through onnxruntime) | free; a 39 MB model downloaded once | People, the On screen tab, `footage faces`, To-dos, the bell |
| Framing that follows movement | every machine, from the index | free | `framing: follow`, **Follow movement** under the player, `metarush cut framing` |
| What's on screen in renders | every machine (the plan and the index); a computer that can look also reads the rendered frames | free, part of every quality check | `qa show`, the review page, the video-qa skill |
| Written scene descriptions | where Claude Code is signed in | your Claude Code usage; off by default | `footage describe`, search, the On screen tab |

All but the last run entirely on your machine and upload nothing. The last is your own Claude Code session looking
at frames MetaRush extracted, so those frames reach Claude the way any image a skill reads does; that is why it is off
until a person turns it on.

## Visual changes
The scene detector only notices hard cuts, and phone footage is usually one continuous shot: a pan, a zoom or someone
walking in used to be invisible. The evidence job now also samples the picture six times a second at 64×64 and measures:

- **Cuts**: a sample that changes a lot (≥ 0.26) and stands out 2.5× from the samples around it. Fast movement
  changes every sample a little; a cut changes one a lot. The time snaps to ffmpeg's full-rate scene change when one
  is within 0.4 s, and full-rate scene changes ≥ 0.5 are cuts even if sampling missed them.
- **Slow changes** (`gradual`): the picture drifting away from the last keyframe without a cut: a pan, a zoom, light
  changing, someone slowly coming into view. Measured as distance from the keyframe (signature, histogram) or the share
  of the picture that changed since it, while the picture keeps moving. One that runs on past 20 s is split where each
  new view is reached.
- **Movement** (`motion`): a burst of movement well above what is normal for that shot (3× the shot's usual changed
  area, at least 3% of the picture, for at least 0.5 s). Steady motion (a bouncing ball, waves, a busy street) is that
  shot's normal and is not an event.
- **Activity**: how much the picture moves, one value (0 still … 1 very busy) per second.

Each change carries the frames worth pulling: just before and after a cut; the start, middle and end of a slow change;
the start, peak and end of movement. `footage moments` lists them as `visual` moments (`change`, `frames`), so a skill
can run `footage frames --at` on exactly those times. Clips analyzed before this (evidence version
`ffmpeg-detectors-1`) catch up by themselves: the `visual-changes` catch-up step queues the evidence job again once.

### Measured (`tests/fixtures`, `go test ./internal/evidence -run Visual`)
`visual-a.mp4` (30 s, built by `scripts/make-fixtures.sh` from public-domain images and a synthetic voice; ground
truth in `tests/fixtures/truth/visual-a.json`):

| What happens | Truth | Found |
|---|---|---|
| Talking head (sways 2 px, with grain), speech | 0–6 s | no events |
| Hard cut | 6.00 | cut at 6.00 |
| Slow pan across a beach (90 px/s) | 6–14 s | one slow change 6.00–13.33 |
| Hard cut | 14.00 | cut at 14.00 |
| A person slides in from the right and stays | 16–17.5 s | movement 15.67–17.83 |
| Hard cut, a sign reading EXIT | 21.00 | cut at 21.00, then no events |
| Hard cut, a dog | 25.00 | cut at 25.00, then no events |

4 of 4 cuts at the exact time, the pan as one event, the entrance found, no events in any still stretch. The two
steady-motion fixtures (a bouncing block, a cellular automaton) produce no events at all. Unit tests cover the same
cases on synthetic frames (static talking head, cut, pan, someone walking in, steady motion, activity).

Limits: a change slower than the drift threshold over the whole shot (a very slow sunset) is not an event; camera
shake reads as movement; a flash or strobe can look like a cut; a clip shorter than two samples has none.

## Smart contact sheets
Twelve evenly spaced frames miss short shots and everything between them. A smart sheet (the default) picks frames by
priority under a cap (20 by default, `--frames` up to 48):

1. one frame per shot (from the cuts),
2. the peaks of visual changes (the start, middle and end of a pan; the peak of movement),
3. the moments someone new starts speaking,
4. frames that keep any stretch from going unseen too long (the longest gap allowed grows with the range and the cap,
   never below 4 s).

Each tile is labeled with the source time, who is speaking and a few of their words, e.g.
`0:42.0 · Maya: “and then the bunny…”`; tiles without speech say why they were chosen ("new shot", "the
picture is changing", "movement"). `metarush footage sheet <asset> [--from 0:40 --to 1:10] [--mode smart|even]
[--details]` makes the image; `metarush footage keyframes <asset>` lists the frames and reasons without making one.
`--mode even` keeps the old evenly spaced sheet (12 frames by default). On `visual-a`, the smart sheet has 11 frames:
every one of the 5 shots, the start, middle and end of the pan, the person walking in, and no stretch longer than 4 s
unseen.

## The on-screen index
On a Mac, MetaRush looks at about one frame a second (every 2 s past an hour of footage) plus the frames at visual
changes, from the clip's proxy at 960 px wide, with Apple Vision through a small helper program,
`metarush-vision` (`tools/vision/main.swift`). For each frame it asks for:

| Apple Vision request | What MetaRush keeps |
|---|---|
| `VNClassifyImageRequest` | scene and thing labels (beach, outdoors, microphone), only where Apple says the label is precise at that confidence, or it is very sure |
| `VNRecognizeAnimalsRequest` | dogs and cats |
| `VNDetectHumanRectanglesRequest` | people (bodies), also when no face is visible |
| `VNDetectFaceRectanglesRequest` + `VNDetectFaceCaptureQualityRequest` | where faces are and how usable each one is as a picture of the face |
| `VNRecognizeTextRequest` (accurate, with language correction) | text on screen: signs, titles, captions burned in, in any language Apple Vision reads (it finds the language itself, macOS 13 and newer; earlier, every language it supports) |
| `VNDetectFaceLandmarksRequest` | five points per face (both eyes, the tip of the nose, the mouth corners), to align a face before it is fingerprinted |
| `VNGenerateAttentionBasedSaliencyImageRequest` | where the eye is drawn: the middle of what stands out and the region that stands out most, which framing that follows movement uses where no face is seen |
| `VNDetectHumanHandPoseRequest`, `VNDetectHumanBodyPoseRequest` | hand and body joints, read as gestures: pointing (and which way), a hand raised, a thumbs up, an open hand |
| `VNGenerateImageFeaturePrintRequest` | only in the face measurement below; nothing stores feature prints |

The helper speaks protocol 2 (landmarks, attention, hands, poses, languages); a protocol-1 helper built by
an older MetaRush, where Swift isn't there to build it again, still indexes clips without them. All of these exist
since macOS 11 (finding the language since macOS 13); the helper needs macOS 12 or newer and the Xcode Command Line
Tools to build.
On Linux, and on a Mac told to, open models on the computer do the same look into the same index: faces with the same
five points, the words on screen and where the eye is drawn (see **Off the Mac: the open models** below).
From the frames, MetaRush builds a compact timeline per clip (insight kind `visual`): label spans (scene, things,
animals, people), on-screen text spans (repeated readings merged; lone or garbled readings dropped; each showing keeps
where it was, so a call's name tile in speaker view and under a gallery tile are two places), face tracks (one
face followed through a shot, collection `faces`), and the **shot type** from face size: a face at least a quarter of
the picture's height is a close-up, at least a tenth a medium shot, smaller a wide shot; with no face, a body at least
60% of the picture's height is a medium shot.

`metarush footage visuals <asset> [--from 0:10 --to 0:40] [--json]` reads it out:
```text
Beach day: 1 person · microphone · eyeglasses · dog · text “EXIT”
  0:00–0:06 close-up · 1 face (Samantha) · microphone · eyeglasses · no text
  0:06–0:14 no people · outdoors, beach · no text
  0:14–0:16 no people · no text
  0:16–0:21 medium shot · 1 face · no text
  0:21–0:25 no people · text “EXIT”
  0:25–0:30 no people · dog · no text
```
That is the real readout of `visual-a`: the beach, the sign's text, the dog and both faces are where the ground truth
puts them, and the shot types are right (a close-up of the speaker, a medium shot of the person walking in);
"microphone · eyeglasses" is Apple's own reading of the speaker's photo. The second face has no name because nobody
named a voice while it was on screen.
Search (`metarush search`, the ⌘K palette) finds clips by what's in them and by text on screen ("dog", "exit sign"),
with the time it shows up. The clip's On screen tab in Sources shows the same lines.

How it runs: new clips are queued after their proxy (job `visual`, a heavy job, so the battery rule applies). While
a clip is still being prepared, the activity bar shows it as one more step of that clip ("What's on screen"); on its
own it says "Looking at what's on screen in “Kitchen chat”", and the bell says when it is done for clips someone
asked about. Older clips catch up through the `visual-index` catch-up step, which runs on any computer that can look
(a Mac, or one with Seeing what's on screen added; elsewhere it waits, without reporting a problem) and again as soon
as a computer gains that component. Re-indexing keeps every name a person gave a face. A 30-second clip took
5.5 s on an M2 Max (31 frames, including extracting them). Frames are written to the machine's scratch folder and
deleted afterwards. The index's version is `vision-2` (face fingerprints, attention, the footage's cuts,
gestures, text in any language): the `visual-index` catch-up step (version 2) looks again at clips an older index
looked at, so their faces can be recognized across clips and their crops can follow movement; names people gave
stay.

The helper's source is embedded in the `metarush` binary, so the helper always matches it. `./bootstrap.sh` builds it
into `bin/`, and `metarush doctor` or the worker (before the first clip it looks at) build it into
`<home>/tools/metarush-vision` when it is missing or built from another version (one build per source version is kept
in the user's cache folder, so homes on one Mac share it). It is an allowlisted tool like ffmpeg: fixed options and folders MetaRush wrote, never text from a model.
On a computer that can't look (Windows, or Linux without the component), `doctor` says so, `footage visuals` says
what would look at the clip (a Mac in the workspace, or adding Seeing what's on screen), and everything else works.

### Faces and people
A face gets a name from a person, and MetaRush also recognizes a face a person has named in other clips —
the way voices are recognized, never by guessing from one look.

- **Asking.** MetaRush asks once per person, not once per appearance: a clip's unnamed faces are grouped by how
  they look and by who is speaking while they are on screen, and each group that matters gets one question on the
  To-dos page and on the person's page — **"Who is this?"**, or **"Is this Maya?"** when it looks like faces a
  person confirmed are Maya or the video shows her name under it (her voice heard while the face is alone on
  screen — at least 6 s, and she is most of the speaking then — adds to that, but on its own only makes her a quick
  pick on "Who is this?") — with a few sample faces and "Seen 12 times in “Kitchen chat”". Yes names the group's faces
  in that clip ("1 face (Maya)" in the readout); No closes the question, and Maya is never suggested for those
  faces again. Details below.
- **Recognizing.** A face that looks very much like the faces a person confirmed are Maya, and clearly unlike
  everyone else's, is named automatically: the readout says "1 face (looks like Maya)", the On screen tab and
  Maya's page show it with a dashed outline, "Recognized by their face, 86% alike. Not confirmed yet.", **That's
  Maya** and **Not Maya?**. The bell says "Recognized Maya's face in 2 more clips" once per person.
- **Learning only from people.** What someone looks like is learned only from faces a person confirmed (named, a
  question answered yes), never from faces named automatically, so one wrong match can't spread. It is worked out
  again from the confirmed faces whenever anything changes — a new name, "Not Maya?", a confirmation taken back,
  two people combined — and every face in the library is decided again with it: names that no longer hold go (and
  are asked about), new ones are added.
- **Clean-up.** Once a person has three or more confirmed faces, a confirmed face that looks unlike the person's
  others (a wrong name, most likely) is asked about again — "Is this really Maya? It doesn't look like Maya's
  other faces" — and until someone answers it doesn't help recognize Maya anywhere.
- **Two at once.** Two faces on screen at the same moment are never both the same person: the closer match keeps the
  name and the other is asked about. Who is talking never holds back a name by looks: in calls the picture
  often shows the listener while someone else talks.

`metarush footage faces <asset>`, `footage faces name <face> "Maya"` and `footage faces reject <face>` (which also
takes back a name given by looks) do the same from a terminal; renaming or merging people carries their faces along.
`metarush doctor` says whether this computer recognizes faces.

#### One question per person, not per appearance
**Why.** Asking once per face *appearance* (a face track) asks about one person dozens of times in a long call:
a call's speaker view starts a new appearance at every switch between speakers. Call apps also print each
participant's name under their picture, which MetaRush already reads (the on-screen index's text).

**Groups.** The unnamed faces of a clip — not confirmed, not named by their looks, not skipped — are grouped
before anything is asked:

1. **By how they look.** Faces with fingerprints are clustered within the clip (average linkage over cosine
   similarity, joined while the average is ≥ 0.45). Within one recording the same person scores 0.6–0.9 (same camera,
   same light); different people never passed 0.49 on the faceset (p99 0.34), so 0.45 keeps people apart and one
   person's appearances together.
2. **By who is speaking while they are on screen.** For every face, MetaRush counts the seconds it is alone on screen
   while each voice talks (the same count the old question used). Two clusters that are on screen alone with the same
   voice (≥ 6 s, ≥ 60% of the talking then) and look at least a little alike (average ≥ 0.30) are one group; a face
   without a fingerprint (too small, or face recognition not set up on this computer) joins the group whose voice it is on
   screen with and whose place in the picture it shares (box overlap ≥ 0.3), otherwise it stays on its own.
3. **Groups keep their identity.** A group is an ID on its faces (`faces.group`, `fgr_…`); indexing the clip again
   carries it over like a name, so a question asked, answered or skipped stays that way. Faces that arrive later
   join the group they look like, or start their own.

One question per group: **"Who is this?"**, or **"Is this Lena?"** when there is a suggestion, with 4–6 sample faces
(the clearest first, spread over the clip) and "Seen 24 times in “Lena's call”". One answer names the whole group:
every face of the group that fits (similarity ≥ 0.40 to the rest of the group; a face without a fingerprint fits when
it was grouped by voice and place) is confirmed by that answer (`confirmedVia: group`). A face that doesn't fit well
is **set aside**: it stays unnamed, leaves the group, and is asked about later on its own ("It was left out when you
named Lena's other faces: it looks less like them"). "No, someone else" rejects the suggestion for every face of the
group; Skip skips the whole group for good (`faces.skipped`).

What the answer teaches: the faces a person confirmed through a group answer teach what Lena looks like like any
confirmed face, but a group contributes at most its six clearest faces to her profile (4 fingerprints each), so one
long call can't push every other clip out of her 80 fingerprints. The library is re-scored once per answer, not once
per face.

**Names shown or said in the video.** They are evidence, never a name by themselves:

- **Shown**: on-screen text that reads like a person's name (one to four capitalized words read clearly — the index at
  least 0.8 sure, so a misread sign such as "SHLE ERS SUHDAY" at 0.5 is not a name — or the name of someone
  already in People; app words such as "Mute", "Recording" or "Gallery view" and pronouns in brackets are left out,
  "Lena's iPhone" reads as "Lena", a lower third "Jane Doe, CEO" as "Jane Doe") that sits **under or near the
  face** and **stays while that face is on screen**: a call app's name tile (Zoom, Teams, Meet: under the face in its
  tile, or in the lower part of the picture while the face is alone on screen) on screen for ≥ 60% of the group's time
  and much less with other faces; a lower third (the lower third of the picture, ≥ 2 s while the face is there); a
  title shown just before the face first appears (≤ 5 s, nobody on screen) as weaker evidence.
- **Said**: in the transcript, "I'm Iris", "my name is Iris", "this is Iris" or "Iris here" at the start of
  a turn name the voice that says it; "Thanks, Lena", "Hi Lena", "Welcome, Lena" name the other voice (the only
  other voice in a two-person clip, otherwise the one that talks next or just before). The group the voice is on
  screen with gets the evidence.

A shown name that matches someone in People (case and accents ignored, or a unique start of their name: "Lena" is
"Lena Okoye") suggests that person; otherwise it suggests someone new with that name. **Use the names shown in the
video** (on the To-dos page, per clip; `metarush todos accept-shown <asset>`) answers every question of that clip whose
suggestion is a shown name, in one click. Until a person accepts, nothing is learned from shown or said names: no face
or voice is named by them, no profile uses them, and the readout says "maybe".

The suggestion of a group is the best supported name: a name tile (strong), how the faces look (a suggestion between
0.38 and 0.60, the group's mean fingerprint), a lower third, a name said by its own voice, a name it was called, being
set aside from someone, and — weakest — the voice heard while the faces are alone on screen (a voice a person named
1.0, one recognized automatically 0.75). Names that agree (the voice is "Lena Okoye", the tile says
"Lena") are one suggestion; a second, different name with good support is offered as a second answer, never the
voice heard alone. Where the voice says Lena but Lena's faces in the same clip — confirmed, or named by their
looks — look nothing like this group (< 0.30), the voice is not used: someone else is on screen while she talks
(a reaction shot). **"Is this Lena?" needs the looks or a name shown** (below); otherwise the question is
"Who is this?" and the names the evidence points at are its quick picks.

**Ask only what matters.** A group is asked about only when its person **speaks** — a voice is heard while its
faces are alone on screen (≥ 2 s, ≥ 60% of the talking then: enough to ask who, not to say who), or its shown name is someone whose voice is in the clip —
and it is on screen for at least a second. Faces seen for under a second are named with their group but never asked
about on their own; faces of people who never speak are not asked about (they can still be named by their looks, or
by hand). **At most one open question per person per video**: when two groups of a clip point at the same person
(the same suggestion, or the same voice), the one in the cut (or seen longest) asks and the other waits; after the
answer it is looked at again with what was learned. Groups whose faces appear in a project's latest cut come first,
also when the cut is saved after the questions were asked (saving a version updates "In your cut" and the order;
nothing is asked again).

**The To-dos page** shows face questions as a grid: each tile has the sample faces, "Seen 24 times in “Lena's
call”" (and "In your cut"), the suggestion with its evidence ("Lena is talking while this face is on screen", "The
video shows “Lena Okoye” under this face", "Someone says “Hi, I'm Iris”"), **Yes, it's Lena**, the name
picker, **Same as…** (another face question of the same clip — the two become one — or someone already in it) and
**Skip**. Keyboard: arrow keys or j/k move between tiles, 1 accepts the suggestion, n says "No, someone else", Enter
types a name, s skips. Each clip with shown names has **Use the names shown in the video**. Answers for many at once:
**Not Lena in this clip** on a tile (c), **Skip all in this clip** (S, a styled confirm), and above the grid
**None of these are Lena (3)** for a name two or more questions suggest and **Skip all in this project** when the
page shows one project; each tile can be selected (x, a checkbox, **Select all**) and the selected questions answered
or skipped together.

From a terminal (`--json` everywhere): `metarush todos groups [--project <prj>] [--asset <ast>]` lists the grouped
face questions (samples, appearances, evidence) and, per clip, the shown names one click would use; `metarush todos
answer <tdo> "Lena" | --suggestion 1 | --same-as <tdo>` answers a group; `metarush todos accept-shown <asset>`
accepts the shown names of a clip (every group is named before the library is re-scored once, so naming one person
can't take faces from another's group first); `metarush todos dismiss <tdo>` skips it; `metarush todos not "Lena"
[--project|--asset]`, `metarush footage faces not-here <asset> "Lena"` and `metarush todos skip-faces
--asset|--project` answer many at once; `metarush people evidence` shows how each kind of hint has done.

#### Evidence that holds, and keeping score
**Why.** The voice cue ("this face is alone on screen while Lena talks") is unreliable on short stretches: a call's
picture switches late and often shows the listener, so a face alone on screen for a second or two while someone talks
is often the other person, with a "share" of 1.0 all the same. From about 6 seconds on it holds. The voice also must
not stop correct names by looks, and the check that "that voice is someone else's" has to look at faces named by
their looks too, not only at faces a person confirmed in the clip.

- **The voice cue needs 6 seconds** (`faces.MinSeconds`; most of the talking then is still required, as a gate) and
  **never asks on its own**: "Is this Lena?" takes the looks (a suggestion) or her name shown (tile, lower third,
  title). Otherwise "Who is this?" offers her as a quick pick ("Lena (talking then)"), and no "maybe" name is put on
  the faces. 2 seconds of someone talking still makes a group worth asking about (`SpeaksSeconds`).
- **Faces named by their looks count** when MetaRush checks that the voice heard belongs to someone else, and who is
  talking never holds back a name by looks. A "(talking then)" answer is never offered against the looks or a name
  shown, and a group shown with a name doesn't take over the voice's question unless the voice is that person.
- **Names from the transcript**: pronouns, contractions and fillers ("I'm", "You're", "it's", "um", "well"…) are never
  names.
- **Keeping score.** Each name a question offers carries what it rests on (voice_cue, looks, tile, lower, title, said,
  voice, aside); an answer records, for each, whether the person agreed (`todos.guesses`; a "no" judges only the
  suggestion). A kind right less than 80% of the time over at least 10 answers in the workspace is **muted**: it no
  longer suggests a name (it can still be a quick pick), and the question and the To-dos page say so plainly
  ("Going by who is talking while a face is alone on screen was right 4 of 12 times here…"). Older answers are scored
  from what their questions said; a voice cue under 6 seconds from before is set aside. `metarush people evidence`.
- **Per clip.** After two "not Lena" answers in a clip that rested on her voice, her voice is no evidence for that
  clip's faces (nor a quick pick there). **Not Lena in this clip** remembers it on the clip (`notOnScreen`): faces
  found later aren't her either, names by looks go, and naming one of its faces Lena by hand takes it back.
- **After the upgrade** the catch-up step `person-questions` (version 1) scores the earlier answers and looks again at
  every clip with an open question about a person: what no longer holds closes, junk answers go ("I'm", contradicting
  voices), questions a person answered or skipped stay as they were.

**Older installations.** The catch-up step `face-questions` (version 1) groups the faces of every clip and folds
the open per-appearance questions into their groups (closed as no longer needed: "asked once for this person"), so they
become one question per person per call. "Is this really Maya?" about a confirmed face that looks
unlike her others stays a question of its own. No migration: the new fields live on the existing face records.
Clips indexed before this version took each regular frame about half a step late (ffmpeg's `fps` filter keeps the last
frame before the moment half a step later; new indexes ask for `round=up`), so the last frame of a shot could show the
next one: in a call's speaker view, the next speaker's face at the end of this speaker's, which could teach one
person's looks as another's. When faces are compared, a fingerprint less like the rest of its face's than 0.40 (`PrintKeep`, the bar for
being named with a group) is left out; older clips are not looked at again.

Measured: see [Measured](#measured) below.

#### How faces are told apart
A face is a **fingerprint** made on the machine that indexes the clip: Apple Vision's five landmarks place the face on
the 112×112 template, the same similarity transform OpenCV's `FaceRecognizerSF.alignCrop` uses
(`internal/faceid/align.go`, unit-tested to recover a known rotation, scale and shift), and **SFace** (Zhong et al.,
"SFace: Sigmoid-Constrained Hypersphere Loss for Robust Face Recognition", IEEE TIP 2021; OpenCV Zoo's
`face_recognition_sface_2021dec.onnx`, a MobileFaceNet) turns it into 128 numbers, run in-process by onnxruntime from
the sherpa-onnx install, like the voice clarity score (`internal/onnx`, purego, no cgo). Each face keeps the
fingerprints of its six most usable frames (capture quality and size; faces under 18 px between the eyes or below
0.15 capture quality get none) in the `faces` collection, never sent to clients; fingerprints also keep two people
apart inside one continuous shot (a face clearly unlike the one a track followed starts its own track), and a
fingerprint unlike the rest of its face's (< 0.40) is not used. Frames are taken at the moments they stand for (one
decode per batch with `fps=1/step:round=up`; a test checks the frame just before a cut). A person's
profile is up to four fingerprints from each confirmed face (80 in all) and their mean; a face's score against it is
the better of the similarity to the mean and the mean of the three closest fingerprints (cosine), as for voices.

| Decision | Bar (cosine) |
|---|---|
| Named automatically | ≥ 0.60 and at least 0.15 above the runner-up; ≥ 0.66 while fewer than two other people have confirmed faces |
| Asked about ("Is this …?") | ≥ 0.38 (≥ 0.41 for a face the open models found, D448) |
| A confirmed face asked about again | < 0.30 against the person's other confirmed faces (three or more) |

**The model's licence.** OpenCV Zoo's `models/face_recognition_sface` says "All files in this directory are licensed
under Apache 2.0 License" (its `LICENSE` is the Apache License 2.0), model files included; the model was contributed to
OpenCV Zoo by its author. MetaRush downloads it from OpenCV's own Hugging Face mirror at a pinned revision and checks
its SHA-256 before use (docs/installation.md); the face-alignment and matching code is MetaRush's. The training data
behind the weights is not stated by OpenCV Zoo (the SFace paper trains on CASIA-WebFace, VGGFace2 and MS1MV2); that is
noted, not resolved. InsightFace's models were not used: their weights are for non-commercial research only.

#### Measured
`tools/vision/eval/faces.py --metarush bin/metarush` fingerprints the main face of every photo in `faceset.json`
exactly as clips are fingerprinted (`metarush footage faces prints`, hidden: the helper's landmarks, the aligned face,
SFace), and plays MetaRush's rules out: some people are known (a person confirmed one, two or three of their faces),
the rest stand in for strangers, and every other face is decided as MetaRush decides it, over 300 random draws per
setting. **The faceset**: 20 people, 185 public-domain photos (U.S. government works) from Wikimedia Commons — NASA
astronauts of the same years, so similar faces, ages and lighting: official portraits and work photos on the space
station, years apart, some in profile or helmets. Each photo shows one clear face (no other face a quarter its size)
and was checked by eye against its title; 21 near-copies of another photo of the same person (a portrait cropped
twice) are left out, leaving 164 faces. Nothing is kept or committed.

| | SFace (ships) | Apple feature print (rejected) |
|---|---|---|
| Same person, median similarity | 0.580 | 0.605 (crop 1.35×) |
| Different people, median / 99th percentile / highest | 0.116 / 0.342 / 0.491 | 0.518 / – / 0.932 |
| Nearest face is the same person | 155 of 164 | 89 of 185 (near-copies kept, which flatters it) |
| Pairs: precision at 0.50 / 0.60 | 1.0 (recall 0.68) / 1.0 (recall 0.43) | best at any threshold 0.86 |

The product's rules (auto ≥ 0.60 with a 0.15 lead, ask ≥ 0.38; a quarter of the people unknown):

| A person confirmed | Named automatically: right / wrong | **Precision** | **Recall** (named automatically) | Named or asked about the right person | Strangers named | Strangers asked about |
|---|---|---|---|---|---|---|
| 1 face each | 14,248 / 0 | **1.00** | **0.44** | 0.81 | 0 of 12,292 | 3.7% |
| 2 faces each | 18,299 / 0 | **1.00** | **0.66** | 0.89 | 0 of 12,292 | 5.9% |
| 3 faces each | 17,076 / 0 | **1.00** | **0.73** | 0.92 | 0 of 12,326 | 6.8% |
| only 1 person known (2 faces) | 956 / 0 | **1.00** | **0.51** | 0.89 | 0 of 46,733 | 0.5% |
| only 2 people known (2 faces) | 1,849 / 0 | **1.00** | **0.50** | 0.89 | 0 of 44,289 | 0.9% |

No face was ever named as the wrong person or a stranger named at all; the highest score any wrong person reached was
0.517 (the bar is 0.60). Questions about the wrong person: 0.7% of known faces with one confirmed face each, 0.1%
with three. On the fixtures (`go test ./internal/visual -run TestRecognizesTheFixtureFaces`, real video through the
real helper): the same two people in other photos, recorded in `visual-b.mp4`, score 0.81, 0.80 and 0.76 against
`visual-a.mp4` (named automatically even with one person known), different people 0.22–0.27 (never asked). Apple's
general image feature print on the same faces: the nearest face was the same person only 68–89 times in 185.

Limits: still photos of 20 adults of similar backgrounds; not measured on children, twins, heavy makeup, masks,
very low light or faces under 18 px between the eyes (which get no fingerprint). Video frames are usually worse than
portraits; a face in video keeps its six best frames, which helps. Hence the margins, the questions and "Not …?".

### Automatic framing from faces
Framing follows faces only where the edit spec says so, never because footage happens to be indexed, so a revision
that doesn't ask for it renders the same over time (one that asks follows the index: a clip not indexed yet keeps the
middle until it is):

- **The cut**: `output.framing: "faces"` (the cut sheet's output settings: `framing: faces`) makes every cropped clip
  without a focus of its own keep its main face in frame. New cuts are saved with it (like speech leveling);
  a full rewrite keeps the setting of the cut it replaces. `center` says the opposite explicitly.
- **One clip**: `framing: {mode: "fill", auto: "faces"}`, spelled `· focus on faces` on the clip's line, in any cut.
- **A cut made before this existed** has no `framing` setting and keeps the middle (or each clip's own focus), whether
  or not its footage is indexed later; `TestLegacySpecsResolveIdenticallyOnceIndexed` holds its plans byte for byte to
  the frozen golden file. Turning faces on is an edit that saves a new version: **Keep faces in frame** under the
  preview of the latest version, a `framing: faces` line in its cut sheet, or a `setOutput` op in an edit plan.

Go takes the main face's position in the clip's source range from the index and resolves it into the RenderPlan's
focus point (the plan records `from: "faces"`), so the preview and the render agree. Where no face was seen (or the
clip isn't indexed yet), `focusX`/`focusY` or the middle apply, and `edit check` says so for a heavy crop. A clip shown
whole (`fit`, or the same shape as the output without zoom) has nothing to follow. The focus is one point per clip;
a crop that moves with the subject is the next section.

### Following movement
One focus point can't keep someone who walks across a landscape shot inside a vertical crop. **Follow movement** moves
the crop with the subject, and it too is only what the spec says — a person turns it on, nothing switches it on by
itself, and older versions never change:

- **The cut**: `output.framing: "follow"` (cut sheet `framing: follow`, also written `follow movement`, `follow the
  action`, `follow the subject`), for every cropped clip without a framing of its own.
- **One clip**: `framing.auto: "follow"`, spelled `· follow movement` on the clip's line, in any cut.
- **How a person turns it on**: **Follow movement** under the player of the latest version (beside Keep faces in
  frame; **Hold still** goes back to faces, **Turn off** to the middle; each saves a new version, with Undo),
  `metarush cut framing <project> follow [--clip <clip>]` (the edit op `cut.framing`: a checked `setOutput` or
  `updateClip`, with a confirm token), a cut sheet line, or the fix a quality check suggests for a face the crop cuts
  off. `cut framing … cut --clip 3` makes a clip do what the cut says again.

What it follows, at each moment the index looked at: the **main face** of the shot (the face track seen biggest over
the shot, so the crop doesn't hop between two people), and where the shot shows no face, **what draws the eye**
(Apple Vision's attention: the region that stands out most, if it stands out at all). Go turns those moments into a
virtual camera (`editspec.FollowPath`):

- a single odd reading between two that agree is dropped, and the path is smoothed (Gaussian, 0.8 s);
- the crop holds still while the subject stays within 12% of the shown width of the middle (fidgeting moves nothing),
  then closes the gap at most 0.6 shown widths a second and settles when the subject is within 3%;
- starts and stops are eased (0.25 s, forward and backward, so it never lags);
- it **jumps only at the footage's own cuts**, exactly there: each shot gets its own path (the index keeps the cuts it
  was built with), and a shot with nothing to follow holds the clip's focus.

The plan carries the path as keyframes in the clip's frames (`framing.path`: `{f, x, y}`, straight lines between,
two keys a frame apart at a cut; `from: "follow"`; `focusX`/`focusY` are its start), and the renderer and the preview
place the picture verbatim (`video/src/clips/framing.ts`, `focusAt`; `editspec.CropRect` is its Go twin, which the
quality checks use). A clip played faster covers its footage in fewer frames, and the path with it. Measured: the
`follow` self-test renders a red square sliding across a landscape clip into a vertical video, and the square sits
where Go placed it within 0.001 of the width at every checked frame, wholly in the picture; held in the middle it is
gone at the start and the end, and cut off at the edge in between, as planned. A cut that says `faces`, `center` or
nothing renders exactly as before however its footage is indexed (`TestFollowOnlyWhereTheSpecSaysSo`; the legacy
golden plans are checked with moving subjects in the facts too), and the follow paths of three cuts are frozen in
`internal/editspec/testdata/follow-plans.json`.

Limits: the subject is sampled about once a second, so very fast movement is smoothed over; a shot with two people of
the same size follows the one seen biggest overall; inside a conversation clip each member is framed by its own face,
not followed; a cut that nests another cut has never had its own clips framed by faces (`nestFacts` doesn't
pass faces through, kept so those revisions don't change), but follows movement where it says `follow`.

## What's on screen in renders
Every quality check (qa-5) now looks at what the rendered picture shows. MetaRush knows where the footage's faces and
its own text are (the on-screen index) and exactly how the plan crops and places every clip, every caption line and
every graphic, so it maps the footage into the rendered frame every half second (`editspec.CropRect`, the renderer's
placement, including a path that follows movement) and finds:

| Finding | When | The fix it suggests (an edit, a new version) |
|---|---|---|
| **A face cut off by the crop** (`face_cut_off`) | the main face of a moment shows less than 80% of itself in the picture | let the crop follow movement (`updateClip` `framing.auto: follow`); when it already follows, show the whole picture (`fit`) |
| **Captions over a face** (`faces_covered`) | a quarter of a face under the caption band while a caption shows | move the captions to the top, bottom or middle, whichever stays clear there (`setCaptions`) |
| **A graphic over a face** (`graphic_covers_face`) | a quarter of a face under a lower third or another graphic (full-screen cards excluded) | move the graphic to a moment without a face there, or crop the clip so the face sits higher |
| **Captions over the footage's own text** (`text_under_captions`) | 30% of a sign or burned-in title under the caption band while a caption shows | move the captions clear of it (`setCaptions`) |

Each finding names the clip and, where a person confirmed (or MetaRush recognized) the face, whose face ("the crop cuts
off Ada's face at the right edge"). Stretches shorter than ¾ s are passing moments and are not reported. Where the
computer can look (Apple Vision, or the open models), the check also reads the rendered frames in the middle of each
stretch the same way (at most 40): a face at the
very edge of the rendered picture, words drawn over a face, or the footage's own words read in the caption band make
the finding **observed**; otherwise it is **heuristic** (from the plan and the index). Without an index for the
footage the check says it couldn't run and why. The geometry is checked against real renders by the `qa-zones`
self-test: the captions and the lower third are drawn inside the boxes QA uses.

Measured on `visual-b.mp4` (acceptance): a vertical cut held in the middle shows the face at the far right edge cut
off and the captions over the sign at the bottom and over the face low in the picture; after **Follow movement** the
same cut renders with no face cut off. Limits: a face the index didn't see (between its frames, too small) can't be
checked; text and faces are at index precision (about a frame a second); conversation clips and nested cuts are left
out; graphics' boxes are the templates' measured layouts, a project motion graphic only its region.

## Gestures and text in any language
From Apple Vision's hand and body poses the index reads **gestures**: a hand **pointing** (index finger out, the others
curled) and which way in the picture, an arm stretched out to the side (pointing that way), a **hand raised** above
the head, a **thumbs up** and an **open hand**. Joints less sure than 0.3, or hands less sure than 0.5, say nothing;
a gesture is kept when it shows in two frames looked at (one very clear one is enough). The readout says it ("0:12–0:15
medium shot · 1 face · no text · pointing left"), search finds it (`points`, `pointing`, `thumbs up`, `hand raised`,
`gesture`), and `footage visuals --json` lists `gestures` per line. On-screen text is read in any language Apple Vision
knows (it finds the language itself on macOS 13 and newer). Gestures, labels, animals and bodies come only from Apple
Vision: an index the open models made says it didn't look for them, so nothing reads their absence as "not there".

Not built: tracking an object from frame to frame for B-roll (Apple's `VNTrackObjectRequest` needs many frames a second
around each moment; the index looks once a second). Where the eye is drawn (attention, above) already says where the
subject of a shot without a face is, which is what framing needs.

## Off the Mac: the open models
Every computer can look at what's on screen. A Mac uses Apple Vision (above); Linux, and a Mac told to, uses open
models that run on the computer through onnxruntime, the library that already fingerprints faces and scores voices
(`internal/visionmodels`, chosen by `internal/looker`). They answer in the helper's own terms, so the index is the
same `vision-2` and everything that reads it works the same: faces and recognition across clips, framing that follows
movement, the render checks, names shown on screen, moments, search, the On screen tab. The index records which looked
(`backend`: `apple-vision` or `open-models`; older indexes are Apple Vision's) and, for the open models, what they
look for (`sees`: faces, landmarks, text, attention); the insight's provider and model say the same. `footage visuals`
says it in its note and `--json` (`lookedWith`, `notLookedFor`).

| Model | What it does | Licence (read from its own repository) | Size |
|---|---|---|---|
| YuNet 2026may (OpenCV Zoo) | finds faces and their five points (eyes, nose tip, mouth corners), which align a face for SFace exactly as Apple's do | MIT | 0.2 MB |
| PP-OCRv5 mobile text detection (PaddleOCR) | finds the lines of text | Apache-2.0 | 4.8 MB |
| PP-OCRv6 small text recognition + its character list (PaddleOCR) | reads each line | Apache-2.0 | 21 MB |
| U²-Net-p (rembg's ONNX export) | where the eye is drawn when no face is in the picture | Apache-2.0 (the export MIT) | 4.6 MB |
| BiRefNet general, Swin-T (its own release) | cuts a photo's subject out (the **Cutting out a picture's subject** component) | MIT | 224 MB |

Each file is a pinned download (address and SHA-256, `internal/visionmodels` `Files`, appended to
`components/pins.go`), checked again before every use; a file changed after it was added isn't used, and doctor
says so. None is GPL or for non-commercial use only; what they were trained on is their publishers' (WIDER Face,
PaddleOCR's text sets, DUTS, DIS5K and others), noted and not resolved, as for SFace.

**Words on screen** are read in Latin script (English and the other languages written in it, accents included),
Greek, Chinese (simplified and traditional) and Japanese. Not Cyrillic, Korean, Arabic, Hebrew, Thai or the Indic
scripts: a sign in those is missed. Apple Vision reads more.

**What they don't look for**: things, places and animals (labels), bodies, hands and so gestures. Shot types still come
from faces (the index's own rule); a frame with no face is "no people" rather than a body's medium shot.

**Made to mean the same as Apple's** (D407), so no threshold changed: faces are looked for on the frame and on a copy
at half size (big faces), kept from 0.6, except a shape found at under 0.7 whose aligned face has no detail at all (a
call's blurred, enlarged background showed such shapes, which Apple Vision never takes for faces); the box is made square like Apple's (side 0.89 × YuNet's height, its middle
0.08 × the height lower: measured, Apple's side ÷ this side has a median of 1.000); the five points come in the
helper's order. How usable a face is as a picture (capture quality, which decides fingerprints from 0.15) is a
documented rule: `0.72 × √confidence × sharpness × facing the camera × size`, sharpness being the Laplacian variance
of the aligned 112 px face; it follows Apple's at a correlation of 0.64. Text uses PaddleOCR's own preparation; lines
under 2% of the picture's height are left out as Apple's are (`minimumTextHeight`), and the confidence is mapped so
the readers' cut-offs mean the same (0.55 → 0, 0.95 → 1). Where the eye is drawn is the faces when there are faces
(as Apple's attention follows them), else U²-Net-p's map: its weighted middle, spread, peak and the regions above half.

**Choosing.** `screenIndex.backend` in config.yaml: `auto` (Apple Vision on a Mac, the open models elsewhere),
`apple-vision` (refused off a Mac) or `open-models`. `metarush worker configure --screen-index open-models` and, on a Mac,
Settings → Footage → Looking at your footage (Apple Vision | Open models) write it, and a running MetaRush takes it at once;
`METARUSH_SCREEN_INDEX` overrides it for one process (measurements, tests). `metarush footage faces prints --with
open-models` fingerprints a folder of photos with either.

**Setting up.** The **Seeing what's on screen** component (`metarush components add screen-index`, setup's checklist,
Settings → Components) is the helper on a Mac and the open models (about 31 MB) elsewhere; they need FFmpeg and
Transcription on this computer (whose install brings onnxruntime). **Recognizing faces across clips** works on any
computer with both. Doctor checks each. Workers take the `visual` job wherever the component is ready, and a
computer that gains it looks at the clips nobody looked at yet (catch-up on start and whenever a component is added).

**Fills** (D445, D465): a landscape shot made vertical, square or 4:5 (or a portrait shot made landscape) over a
blurred, enlarged copy of itself shows the copy's faces too, larger and blurred. Apple Vision rarely takes them for
faces; YuNet often did, so following movement moved into the blur and one person became two. The open models now find
such a frame — the picture centred along the frame's long side, bands beside it with little detail next to it (most of
their rows, and all of one band's, so a face cam or a title laid over the other doesn't hide the fill), straight lines
where the picture ends, and bands that follow the picture enlarged and blurred (what is laid over them left out of the
comparison) — and leave out the faces mostly outside the picture that are as plain there as the copy is. A face cam
or a picture in picture laid over the copy is sharp, so its faces stay. Plain walls, studio backdrops and letterbox bars
don't follow the picture, so they aren't fills; a fill whose picture is itself plain near its edges (graphics, screen
recordings) may not be found, and then its copy's faces count, as they often do with Apple Vision. Measured on the
faceset made into fills (`TestFillsOnTheFaceset`): faces in the copy that Apple Vision didn't see went from 38 to 1
(9:16, blur 8), 12 to 0 (blur 16), 7 to 0 (1:1), 9 to 0 (4:5) and 20 to 1 (portrait made landscape), with 183–186 of
186 fills found. With something laid over the copy: a 110-pixel face cam in the top band 8 to 0, a 160-pixel one in the
bottom band 12 to 0, MetaRush's vertical picture in picture over a fill 6 to 1 — and none of the inset's faces Apple
Vision found (129, 155 and 157) was left out, where leaving out every face outside the picture would have dropped
129, 155 and 150 of them. No face Apple Vision found was left out anywhere: in the fills, the insets, a plain vertical
picture in picture, or the photos and video frames that aren't fills (as they are, cropped, blurred all over, small,
letterboxed). The garden call made 9:16: Apple Vision saw 12 faces in the copy, the open models none.

**A Mac gets the first look** (D410): a computer with the open models leaves a new clip alone for two minutes at a
time, at most five times, while a Mac in the workspace that uses Apple Vision, takes big jobs, offers to look and lacks
nothing for it was seen in the last two minutes; then it looks itself. The job's wait says why. A Mac whose person left
Seeing what's on screen out, with no helper built, doesn't offer to look, so nothing waits for it (D446). A computer
that finds it can't look after all (a helper that didn't build, a model changed after it was added) stops offering to
until its components or settings change, and hands the clip back, not counted as a try, when another computer that
looks was seen in the last day; with none, the clip fails as before, with the one step that fixes it.

**Apple Vision's indexes stay Apple Vision's** (D447, D467): a clip Apple Vision looked at is looked at again (a
person asking, or catch-up after an upgrade) only by a Mac while one that looks was seen in the last 30 days — the
computer with the open models puts it back, waiting longer each time, at most three hours. After 30 days without one
it looks itself and keeps what Apple Vision saw that the open models don't look for: the places, things, animals and
gestures (`kept` in the index, said in the readout's note); looking again later keeps what that look kept (D468). It
is the index of the media the clip plays now that counts: a fixed copy nothing looked at yet gets a first look like
any new clip (a Mac about now gets it first), even when Apple Vision looked at the original, and keeps the original's
places, things and animals but not its gestures — where a hand was in the original's picture says nothing about a
steadied copy's. The original's index stays as it was.

**Fingerprints say what found the face** (D448, `printsBy`): `apple-vision` or `open-models` (older ones are Apple
Vision's). Matching treats both alike — one library can hold both, where a workspace has a Mac and a computer without
one — except that a face the open models found is asked about ("Is this Ada?") from 0.41 rather than 0.38: at 0.38 they
asked about the wrong person more often than Apple Vision does; at 0.41 no more often. The same holds when a
correction re-scores the groups (Refresh).

**What stays on Apple Vision**: lip sync's check that every face in the part it changes is named (D279) still looks
with the helper, so on Linux it asks for a Mac. Cutting a photo's subject out uses Apple's mask on a Mac that keeps
Apple Vision and BiRefNet elsewhere (the fix **A photo's subject**, and AI work's background removal with model
`birefnet`); without either, removing a background works only for plain-colour backgrounds.

#### Measured
`go test -tags screeneval ./internal/visionmodels` (`TestCompareBackends`, `TestCompareCutOuts`; the models from `go
run ./scripts/fetchscreenmodels <folder>` in `METARUSH_SCREEN_MODELS`) looks at the same frames with both on a Mac;
`tools/vision/eval/faces.py --metarush bin/metarush --with open-models` plays the recognition rules out on the faceset
(`--mix`: both ways in one library).
Measured 3 Nov 2026 on an M5 Pro Mac mini shared with other builds (a load around 17, so the times are long for both):

| | Apple Vision | The open models |
|---|---|---|
| Faces found in the fixtures and the call (150 frames) | 148 | 148, the same ones |
| Box overlap with Apple's (IoU, median) | – | 0.895 |
| Five points, off from Apple's (of the eye distance, mean) | – | 0.114 |
| Capture quality, mean (correlation with Apple's) | 0.594 | 0.551 (0.64) |
| Words on screen the fixtures show | 32 of 32 | 32 of 32 |
| Shot type at the same moment | – | the same at 91.3% (272 of 298; the rest close-up vs medium on one call) |
| The fixtures' people: same person / others | 0.80, 0.79, 0.76 / 0.22–0.25 | 0.79, 0.77, 0.72 / 0.22–0.24 |
| Faceset, 200 draws: precision with 1 / 2 / 3 faces confirmed | **1.00 / 1.00 / 1.00** | **1.00 / 1.00 / 1.00** |
| Recall (named automatically) with 1 / 2 / 3 faces confirmed | 0.445 / 0.641 / 0.725 | 0.417 / 0.570 / 0.659 |
| Recall with only 1 / only 2 people known | 0.52 / 0.495 | 0.426 / 0.425 |
| Asked about the wrong person (of known faces), 1 / 2 / 3 confirmed | 0.73% / 0.53% / 0.27% | 0.33% / 0.28% / 0.24% (0.73% / 0.77% / 0.76% asking from 0.38) |
| Named or asked about the right person, 1 / 2 / 3 confirmed | 0.81 / 0.88 / 0.91 | 0.70 / 0.81 / 0.84 (0.71 / 0.83 / 0.85 from 0.38) |
| Strangers asked about, 1 / 2 / 3 confirmed | 3.7% / 6.7% / 7.1% | 0.9% / 2.3% / 3.1% |
| Nearest face is the same person | 152 of 163 | 150 of 167 |
| Most alike different people (the bar is 0.60) | 0.497 | 0.521 |
| Time a frame (proxy at 960 px) | 73 ms | 194 ms |
| Cut-outs (7 pictures) | – | the same answer on 7 (6 subjects, 1 none); overlap 0.99 on 5, 0.77 on 1; about 5 s a picture |

No face was named as the wrong person with either. The open models name a little less on their own (they find
slightly different faces in the faceset's photos, so a few more are asked about instead), and they read fewer
scripts and none of the labels, animals or gestures: that is why a Mac keeps Apple Vision and gets the first look.

**Both ways in one library** (`faces.py --mix`, the 161 photos fingerprinted both ways, 200 draws each): the same
photo's two fingerprints are 0.94 alike (median); different people across the two ways at most 0.488 (0.521 within
one). Precision stays 1.00 whichever way faces were confirmed and asked about:

| Confirmed / asked about | Recall, 1 / 2 / 3 confirmed | Only 1 / 2 people known | Wrong person asked, 1 / 2 / 3 | Most alike wrong person |
|---|---|---|---|---|
| Apple Vision / Apple Vision | 0.448 / 0.666 / 0.753 | 0.536 / 0.488 | 0.72% / 0.28% / 0.19% | 0.521 |
| Open models / open models | 0.415 / 0.564 / 0.653 | 0.466 / 0.422 | 0.34% / 0.30% / 0.25% | 0.536 |
| Apple Vision / open models | 0.432 / 0.622 / 0.701 | 0.529 / 0.460 | 0.28% / 0.08% / 0.17% | 0.504 |
| Open models / Apple Vision | 0.432 / 0.602 / 0.692 | 0.486 / 0.428 | 0.28% / 0.27% / 0.13% | 0.524 |
| Each face either way at random | 0.436 / 0.603 / 0.697 | 0.438 / 0.472 | 0.44% / 0.30% / 0.18% | 0.521 |

**Windows**: MetaRush itself does not build for Windows yet (process groups and free-space checks use Unix calls in
`internal/tools` and `internal/storagemove`), and `internal/onnx` reports onnxruntime as unavailable there
(`onnx_other.go`), so the clarity score, face fingerprints and the open models say so instead of failing.

## Written scene descriptions
Off by default. A person turns them on for the workspace or one project (Settings → Footage → Looking at your footage, or
`metarush footage describe settings --on [--project <id>]` in a terminal; Claude cannot turn them on). Then new
clips, or `metarush footage describe <asset> [--wait]` (or `--project <id> --all`), have **your own Claude Code on this
machine** look at the clip's smart contact sheet, frames and on-screen readout and write a sentence or two per scene.
It runs headless through the dashboard assistant's runner with the same read-only allowlist; Claude writes the
descriptions as a proposal file and MetaRush checks and saves them (insight kind `description`). Search finds clips by
them, and the On screen tab shows them. A skill that already described a clip in a session saves its text with
`metarush footage describe <asset> --save scenes.json`. Descriptions use your Claude Code usage, so MetaRush says so
where they are turned on and never shows a price.

## Skills
`skills/_shared/editorial.md` tells every editing skill to see before it says: read the smart sheet, the visual
moments and `footage visuals`, then pull frames (`footage frames --at`) at the visual events before claiming anything
visual (who is in the shot, what a sign says, whether a shot is a close-up), and to treat the index as machine guesses,
faces named by their looks as "it looks like Maya" and open questions as questions. Following movement is offered,
never switched on unasked. `video-footage-review` and `video-story-editor` read the readout and the smart sheet
before writing about the picture or choosing framing; `video-qa` reports what the render's on-screen check found
(a face cut off, captions over a face or a sign) with its fix. Live cases in `metarush skills test --live`: "find the
part where the dog shows up" and "is anything written on screen in this clip?".
