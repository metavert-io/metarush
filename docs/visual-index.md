# What MetaRush sees in the picture

Claude Code does the editorial reasoning by reading what MetaRush extracts, never the video itself. This page is about
the picture: how MetaRush decides which frames are worth looking at, what it knows about each clip without anyone
looking, and where that knowledge stops.

| Layer | Runs on | Cost | Where it shows up |
|---|---|---|---|
| Visual changes: cuts, slow changes, movement, activity per second | every machine (ffmpeg) | free, part of the evidence job | `footage evidence` (`visualChanges`, `activity`), `footage moments` (kind `visual`), smart sheets |
| Smart contact sheets | every machine | free | `footage sheet`, `footage keyframes`, the Frame grid tab, skills |
| On-screen index: things, animals, people, faces, gestures, text in any language, shot type, where the eye is drawn | a Mac (Apple Vision) | free, a heavy job | `footage visuals`, `footage faces`, search, the On screen tab, People, To-dos |
| Faces recognized across clips | the Mac that indexes (SFace through onnxruntime) | free; a 39 MB model downloaded once | People, the On screen tab, `footage faces`, To-dos, the bell |
| Framing that follows movement | every machine, from the index | free | `framing: follow`, **Follow movement** under the player, `metarush cut framing` |
| What's on screen in renders | every machine (the plan and the index); a Mac also reads the rendered frames | free, part of every quality check | `qa show`, the review page, the video-qa skill |
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
asked about. Older clips catch up through the `visual-index` catch-up step, which runs only on a Mac (elsewhere it
waits for one, without reporting a problem). Re-indexing keeps every name a person gave a face. A 30-second clip took
5.5 s on an M2 Max (31 frames, including extracting them). Frames are written to the machine's scratch folder and
deleted afterwards. The index's version is `vision-2` (face fingerprints, attention, the footage's cuts,
gestures, text in any language): the `visual-index` catch-up step (version 2) looks again at clips an older index
looked at, so their faces can be recognized across clips and their crops can follow movement; names people gave
stay.

The helper's source is embedded in the `metarush` binary, so the helper always matches it. `./bootstrap.sh` builds it
into `bin/`, and `metarush doctor` or the worker (before the first clip it looks at) build it into
`<home>/tools/metarush-vision` when it is missing or built from another version (one build per source version is kept
in the user's cache folder, so homes on one Mac share it). It is an allowlisted tool like ffmpeg: fixed options and folders MetaRush wrote, never text from a model.
On Linux or Windows the index is simply not there: `doctor` says so, `footage visuals` says a Mac in the workspace can
index the clips, and everything else works.

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
`metarush doctor` says whether this Mac recognizes faces.

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
   without a fingerprint (too small, or face recognition not set up on this Mac) joins the group whose voice it is on
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
| Asked about ("Is this …?") | ≥ 0.38 |
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
off Ada's face at the right edge"). Stretches shorter than ¾ s are passing moments and are not reported. On a Mac the
check also reads the rendered frames in the middle of each stretch with the same helper (at most 40): a face at the
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
knows (it finds the language itself on macOS 13 and newer).

Not built: tracking an object from frame to frame for B-roll (Apple's `VNTrackObjectRequest` needs many frames a second
around each moment; the index looks once a second). Where the eye is drawn (attention, above) already says where the
subject of a shot without a face is, which is what framing needs.

## On Linux and Windows
The on-screen index stays Mac-only: Apple Vision is what finds faces and their landmarks, reads text and labels
things. What is already cross-platform: the face fingerprint (SFace through onnxruntime, the same model and code on
macOS and Linux; `internal/onnx`, `internal/faceid`, and their tests run wherever onnxruntime is installed), framing
that follows movement and the render checks (both Go, from whatever index the workspace has), and everything else
MetaRush does. Recognition, following and the checks work for clips a Mac indexed, from any machine.

Finding faces and their landmarks, reading text, labelling things and places, and attention need Apple Vision today;
elsewhere the `visual` job says so (`catchup.ErrNotHere`).

MetaRush itself does not build for Windows (process groups and free-space checks use Unix calls in `internal/tools`
and `internal/storagemove`), and `internal/onnx` reports
onnxruntime as unavailable there (`onnx_other.go`), so the clarity score and face fingerprints say so instead of
failing.

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
