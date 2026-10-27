# Speakers, people and to-dos

Every number below was measured in October 2026 on an Apple M2 Max Mac (macOS 26) with sherpa-onnx
1.13.8 (pyannote segmentation 3.0 + NeMo TitaNet-Small), unless a line says otherwise.

## What a person sees
- Each video lists its **voices**: "Speaker 1", "Speaker 2"… until someone names them, with how long each speaks and
  2–3 short samples. A sample is a **video tile**: the frame where it starts (`GET /media/assets/{id}/frame?t=&w=`,
  cut by ffmpeg on the daemon and cached; at most three at a time) and, on a click, exactly that stretch of the
  clip with sound from the same-origin preview copy, stopping at its end (audio-only clips keep the audio button).
  Naming a voice ("Ada") names it in the transcript, in captions (`showSpeakers`), in what Claude reads, and in
  **every other video** where Ada's voice is recognized.
- A voice named **automatically** looks different from one a person named: a dashed outline and "Recognized by
  voice, 68% match. Not confirmed yet." with "That's Ada" and a one-click **"Not Ada?"** (the label goes and the
  voice is asked about again). New automatic labels are announced once per person, grouped: "Recognized Ada's voice
  in 2 more videos" with **Check** (unread only when a new video joins; it reads itself when none are left to check).
- **Who said each line**: every transcript line (Sources → Transcript, and each voice's lines in the Speakers tab)
  has a chip with the speaker. Pick someone already named, **Someone new…**, or **Not sure**: the line's words go to
  that person (lines split where the speaker changes), the voices and their speaking time update, and "Not sure"
  turns the words into a voice that is asked about. CLI: `footage speakers assign <asset> --from <t> --to <t>
  --person "<name>" | --new-person "<name>" | --unsure`. **"There's someone else talking here"** listens again for
  one more voice than was found (names people confirmed stay with their voices; `footage speakers hint --more`).
- Naming a second voice of a clip with a name another voice there already has **asks first**: "Both voices in this
  clip will be Ada — treat them as one person?", with "No, let me pick another name" as the default and a plain
  warning when the two voices sound clearly different ("one is much lower"). Every combination, asked or automatic,
  comes with **Undo** (`footage speakers unmerge`).
- Names are typed in **one person picker** everywhere (naming a voice, a line, "Not Ada?", To-dos, combining
  people): case and accents are ignored and "lind" finds "Maya Lindqvist"; a typo close to someone offers "Did you
  mean Maya Lindqvist?" first; someone new is only ever added through the explicit "Add someone new" option. The CLI
  resolves names the same way (`internal/names`, mirrored in `web/src/lib/names.ts` and tested on one table):
  `No one called "Mya" — did you mean Maya?`; an ambiguous prefix lists the candidates.
- The **People** page shows each person's samples across videos (a wrong label stands out), where they speak
  (automatic labels marked, each with "That's them" / "Not them?"), **notes** about them, "Same as someone else"
  and **Rebuild voice**.
- "How many people talk in this video?" re-runs speaker detection with that number (the words are kept).
- Questions MetaRush cannot answer become **to-dos** (dashboard `/todos`, `metarush todos …`): who is this voice,
  is this Ada, may we use this music, what is this project for, are these two people the same, is this cut ready.
- Listening again shows in the **activity bar** ("Finding who's speaking in “Kitchen chat”"). Like a transcript it
  is a big job, so on a laptop below the battery threshold it waits until someone chooses Process now; the bell says
  when the voices were found. Job details, the activity bar and the bell say when a finished clip has open questions
  ("2 questions about this clip") and link to the To-dos page filtered to that clip (`todos.AboutClip`,
  `links.Todos`). A speaker detection job's "Go to" opens the clip's Speakers tab.
- **Search** finds people by name (the videos where they speak) and shows who said each transcript hit.

## Pipeline (worker, after transcription; also the `diarize` job)
1. **Diarize** the 16 kHz WAV with `sherpa-onnx-offline-speaker-diarization`, `--clustering.cluster-threshold=0.7`
   (MetaRush default; sherpa-onnx's default 0.5 over-splits real speakers, see below) or `--clustering.num-clusters=N`
   when the user gave a count. This runs for **every recognizer**: Parakeet and whisper.cpp transcripts get speakers
   the same way (`transcribe.AssignSpeakers`: each word takes the speaker turn it overlaps most, or the nearest
   turn edge; segments split where the speaker changes; segment-timed output takes its largest overlap).
2. **Fingerprint** (`internal/voice`): TitaNet-Small embeddings of clean speech, computed in-process through the
   sherpa-onnx C library (`libsherpa-onnx-c-api`, next to the CLI binaries) loaded at run time with
   [purego](https://github.com/ebitengine/purego) (no cgo, no headers, no extra model: diarization already ships
   TitaNet). sherpa-onnx 1.13.8 has no command-line tool that prints embeddings for a file (only a microphone
   speaker-identification demo), hence the library binding. Clean speech = each turn minus 0.15 s at both ends,
   minus every other speaker's turns, in windows of at most 3 s (≥ 1 s).
3. **Repair** (`voice.Refine`). Labels are tiered by fingerprinted speech: under 1.5 s ("weak") their turns are
   folded into the most similar confident speaker (similarity ≥ 0.30 with a 0.05 margin) or, when shorter than 1 s
   or unclear, into the speaker next to them in time; 1.5–4 s ("thin") they join a confident speaker they sound like
   (≥ 0.45); confident labels whose fingerprints are ≥ 0.55 alike are merged. A voice left with under 4 s of clean
   speech is a **brief voice** (listed, never asked about) only if it also says under 15% of everything said in the
   recording: in a 10-second clip, 2–3 seconds is one of the people in it, not crosstalk. A short line that sounds
   like a known person is not folded away. Voices are numbered by first appearance, real speakers before brief
   voices.
4. **Recognize** people (`voice.Decide`, `voice.Consolidate`). A person's profile keeps several exemplars (up to 6
   per recording, 60 in total) plus their mean, so a new video matches the closest conditions the person was heard
   in. Score = the better of mean-to-mean similarity and the mean of the 3 closest exemplars. **Automatic label** at
   ≥ 0.60 (voices with ≥ 4 s of clean speech) or ≥ 0.55 (shorter), and at least 0.20 above the runner-up;
   **suggestion** ("probably Ada, 86% match") at ≥ 0.40. **While fewer than two other people are named**, an
   automatic label needs ≥ 0.75 (`Thresholds.FewAuto`): with one or two people known there is no real runner-up, so
   the margin rule says nothing, and one person's profile is the most likely to absorb everyone who sounds roughly
   like them (with one person named, other voices can score 0.6–0.7 against her profile). Between 0.40 and
   that, the voice becomes "Is this Ada?" (`speaker_match`). Two voices of one video recognized as the same person
   are combined automatically (`mergedBy: auto`, with Undo). When every voice is a known person and there are fewer
   people than the diarizer's clusters, detection runs once more with that number and is kept if it agrees.
5. **Samples**: 2–3 snippets per voice, 2–5 s of clean non-overlapping speech, the most typical first, spread out.
   The dashboard plays them from `/media/assets/{id}/snippet?from=&to=` (cut on the daemon, cached in scratch);
   the CLI writes WAVs with `footage speakers <asset> --samples`.
6. **Voice profiles are derived**: a person's profile is rebuilt from the voices a person confirmed are them
   (named, a confirmed suggestion, an answered question, a line given to them) and from merges a person confirmed,
   never from automatic labels, so one wrong match cannot snowball. Every correction (renaming a voice away,
   "Not Ada?", splitting a person, giving a line to someone else, separating merged voices) goes through one path:
   rebuild the people involved, then re-score every automatic label and suggestion in the library, so labels the
   wrong profile produced disappear (and are asked about), and "same person?" questions whose voices no longer sound
   alike close themselves as obsolete. **Rebuild voice** (`people rebuild <person>|--all`) does the same on request
   and reports clips where two voices that sound like different people were combined.
7. **Pitch** (`internal/voice/pitch.go`): each voice's typical pitch is measured from its clean speech (autocorrelation
   F0 on 8 kHz audio, 40 ms frames, 60–400 Hz). Two voices "sound quite different" when their pitches differ by
   ≥ 60 Hz or ≥ 0.4 octave with at least 1.5 s of voiced speech each; the combine dialogs then say so.

## Fixing who said what
- **Line by line**: `footage speakers assign` moves the words in a time range to a person: segments are split at
  the range, the words become (or join) a voice of that person in the clip marked as **confirmed spans**, and only
  confirmed spans teach the profile. "Not sure" makes them a new unnamed voice (with a question).
- **Undo a combination**: merges keep the voice they absorbed (`AssetSpeaker.Before`, `TranscriptSegment.speakerFrom`
  per segment), so `footage speakers unmerge` restores both voices exactly; merges recorded before this release are
  split again by each segment's fingerprint.
- **Listening again keeps names**: when detection runs again (a count hint, "someone else is talking", a newer
  release), names carry over by time overlap (a new voice inherits a name when ≥ 60% of each overlaps the other),
  rejections ("Not Ada") carry over when ≥ 50% of the new voice was rejected, and confirmed spans are applied again.
- **Same person?** (`duplicate_person`) offers "No, not the same" first (the default and the focused button). An
  answer is final: it is remembered on the two people (`Person.Distinct`), not on a detection run, so re-detection
  never asks again; "Same as someone else" on the People page stays the way to combine them later.
- Measured on the fixture voices (`tests/e2e/speakers_fix_test.go`, a woman's and a man's synthetic voice): the
  man's voice named with her name, then her voice given the same name (combined), then fixed line by line and
  rebuilt: their profiles score **0.83 before the fix and 0.23 after** (suggestion threshold 0.40), pitch 177 Hz vs
  111 Hz, and no same-person question is left.

## Who named it
Every voice and face named as someone records who named it and when (`naming`: a person — a click in the dashboard, a
terminal, a card a person ran — an AI app through MCP or the command line, Claude's Auto, Claude, or MetaRush by
itself); combining two people names their voices and faces anew as whoever combined them, and undoing a fix gives a
voice back who had named it. Recognition is unchanged; consent checks (voice copies' samples, own-voice dubs, presenters, faces in
generated shots) count only names a person gave. A voice named before this was recorded is named again once; a face
confirmed in the dashboard before it counts.

## Hearing and seeing what a name was learned from
"These two voices sound quite different — one is much lower. Is this really Maya?" used to be a yes/no with
nothing to listen to: the person couldn't hear what this voice was being compared with, and when the cause was an
earlier mistake (say a man's voice learned as "Maya") there was no way to fix it from there. Now:

- **Is this really Maya? (voices_differ).** The dialog plays **this voice** next to **the voices Maya's voice
  was learned from** (her confirmed voices): each with its clip, a few seconds to play and how alike it is to this
  voice. The one least like the others — by fingerprint, or by pitch when one is much lower — is marked "Sounds unlike
  Maya's other voices". Every learned voice has **Not Maya**: the name picker opens to say who it is instead
  (or "Not sure"), that voice is given to them (or asked about again), Maya's voice is relearned at once, and the
  warning is worked out again in place. When the mistake was the cause, the warning goes away and **Yes, it's
  Maya** finishes the answer that started it.
- **Both voices in this clip will be Maya (same_name_in_clip).** Both voices play side by side: **Same person —
  combine** (as before, with Undo), **Different — rename the other one** (the picker names the other voice; then this
  one becomes Maya), or **Keep things as they are** (the default; nothing changes).
- **Faces work the same way.** "Not Maya?" on a face MetaRush named by its looks opens the faces her face was
  learned from (her confirmed faces, how alike each is to this face, the odd one marked, a group answer shown as one
  entry with its count); each has **Not Maya**, and this face has **This face isn't Maya** (or **Say who it is**).
  Recognition is redone at once, so faces named because of the mistake lose that name.

From a terminal: `metarush people learned "Maya" [--asset <clip> --voice "Speaker 2" | --face <face>] --json` lists the voices
and faces her name was learned from with their match scores (against the voice or face in question when given) and
marks the odd one; `metarush people unlearn "Maya" (--asset <clip> --voice "Speaker 1" | --face <face>) (--as "<who it
is>" [--new-person] | --unsure) --json` takes one back, relearns her voice and face, and says whether the voice or face in
question still sounds or looks unlike her (`still`). Ops `people.learned` and `people.unlearn`; the `E_CONFIRM_REQUIRED`
details of a naming now carry `personId`, `assetId`, `speaker` and `otherSpeaker`, which the dialog uses.

A voice is also suggested from faces and words, never named by them: an unnamed voice whose question is open
gets "Lena (on screen while this voice talks)" when faces a person confirmed are alone on screen while
it talks (≥ 6 s, ≥ 60% of that time; not in a clip where Lena was said to be nowhere on screen, or after
two "not Lena" answers there that went by her voice), and "Iris (says “I'm Iris”)" when it introduces itself
(pronouns and contractions such as "I'm" or "You're" are never read as names).

## Keeping score of the hints
Every answered question about a person records what each name it offered rested on — how the voice sounds (`voice`),
the face alone on screen while a voice talks (`voice_cue`), the looks, a name shown (`tile`, `lower`, `title`) or said
(`said`), being set aside — and whether the answer agreed. A kind of hint right less than 80% of the time over at
least 10 answers in the workspace is muted: it no longer makes MetaRush suggest a name. For voices, when going by how
voices sound is muted, "Is this Ada?" becomes "Who is this voice?" with Ada as a quick pick and a plain line saying
why. `metarush people evidence --json` lists each kind with its answers, how many were right and whether it is muted;
answers given before MetaRush kept score are scored once, from what their questions said (the catch-up step
`person-questions`). Bulk answers about faces (`todos not`, `footage faces not-here`, `todos skip-faces`) are in
[visual-index.md](visual-index.md#evidence-that-holds-and-keeping-score).

## Notes about people
Each person can carry **notes** (up to 2,000 characters): who they are, how to refer to them or title them, what to
highlight or avoid. They are edited on the People page (saved as you type) or with `people update <person> --notes
"…" | --notes-file <file> | --clear-notes` (the activity log records that the notes changed, never the text). They
appear in `people list|show`, `footage speakers`, the project folder's `footage.md` ("About Ada: …") and the
Speakers panel (the info button next to a name). The skills (`_shared/editorial.md`, `video-story-editor`,
`video-edit-revise`) read them before choosing who to feature, writing name tags and captions; the dashboard's
Claude Code receives the people in the footage or project in view with their notes in each message's context. Notes
never override the safety rules, and anything in them about MetaRush itself is ignored.

## Catching up after an upgrade
Clips imported before speaker detection existed get it without anyone asking: when the daemon starts and at the end
of `metarush migrate`, the catch-up registry (`internal/catchup`, [architecture.md](architecture.md)) queues a
`diarize` job (idempotency key `diarize:catchup@1:<asset>`, the normal battery policy, shown in the activity bar) for
every clip with audio and a finished transcript but no speaker detection yet, then checks every project for the
questions earlier versions did not ask. `footage speakers detect [<asset>…|--all] [--wait]` does it on request.

Storage: `asset_speakers` (one document per voice per detection run, fingerprint fields never sent to clients; also
`mergedBy`, `confirmed` spans, `manual`, `pitch`/`voiced` and, server side only, the `before` snapshot of a merge),
`people` (profiles derived from confirmed voices, `notes`, `distinct`), `assets.speakers` (status: raw clusters,
voices kept, brief voices, named, hint, notes), `transcript_segments.speakerName` (kept in sync with the voices) and
`speakerFrom` (server side), `todos`, `catch_ups`. Migration 4 adds their indexes; this release needs no migration.

## To-dos
One open to-do per **group key** (partial unique index on `{workspaceId, groupKey}` where `state = open`), so any
number of workers can create them idempotently; a question a person answered or dismissed is never asked again;
questions whose situation changed are closed automatically (`resolved` when the data was fixed another way,
`obsolete` when it no longer applies, e.g. voices re-detected). Answering goes through the normal operations.

| Kind | Created when | Group key | Answer applies |
|---|---|---|---|
| `speaker_unnamed` | a detection leaves a (non-brief) voice without a name | `speaker:<asset>:<label>@<run>` | names the voice (person by name, or new) |
| `speaker_match` | same, but it sounds like someone (≥ 0.40) | same key | confirm the suggestion, pick another, or type a name; a rejected suggestion is not offered again |
| `music_rights` | a cut uses a `music` track with no rights on the track or the file | `rights:<asset>` | records `asset.rights` (license/source/attribution) |
| `brief_missing` | a full-cut plan is checked for a project without goal / audience / tone | `brief:<project>:<field>` | sets that brief field |
| `duplicate_person` | two people's voices are ≥ 0.70 alike | `dup:<a>:<b>` | "No, not the same" (first, the default): remembered on both people, never asked again; "Yes, combine": merge them. Closes itself when the voices stop sounding alike |
| `revision_approval` | a new main-branch head is a draft (one per project) | `approval:<project>@<rev>` | approve/reject; **people only** (dashboard or interactive terminal) |
| `face_group` | a group of a clip's unnamed faces whose person speaks (docs/visual-index.md); "Is this …?" only from the looks or a name shown, else "Who is this?" with quick picks | `facegroup:<asset>:<group>:<person \| new:name \| who>` | names every face of the group that fits (the rest are set aside and asked about later), "no" rejects the suggestion for the group, `--same-as` joins another group of the clip; skipping skips the faces for good; many at once: `todos not`, `footage faces not-here`, `todos skip-faces` |
| `face_person` | a confirmed face looks unlike the person's other confirmed faces ("Is this really Maya?") | `face:<asset>:<from>:<person>:recheck` | yes keeps it (checked), no takes the name back |

Counts for badges: `todos.counts` (by project, by asset, by kind); the dashboard refetches on SSE events for
`todos`, `asset_speakers` and `people`. A clip's questions follow it: renaming it refreshes the speaker questions
(they name the clip by its title) and adding it to or removing it from a project updates where they count
(`asset.update`, `asset.link`).

## Evaluation on real speech

### Recordings
- **Public domain, multi-speaker:** a joint press conference of two heads of government (Brussels, 2014-06-05),
  White House video on Wikimedia Commons, 2359.8 s. Commons marks it public domain as a work of the US federal government (17 U.S.C. §105). Downloaded
  original: SHA-1 `a2fe9d2b97136b90f73f3962ae6983fb93cce511` (matches the Commons API), SHA-256
  `cbb996b9ce05faec6f45150904ec2e7b9d6a426ec1e0751863cb34ecf40d4bff`.
- **Reference turns:** the official transcript of that news conference, Daily Compilation of Presidential Documents
  DCPD-201400432, also a US government work. It labels every turn (each of the two leaders, "Q."); the four reporters (R1–R4) were told apart from context (each is
  called on by name; follow-ups carry no new introduction). Turn times come from aligning the transcript to Parakeet
  word times (word-level sequence alignment): 5,807 of 6,369 reference words matched; 30 of the 37 turns were timed.
  The other seven are short interjections the recognizer missed; their stretches are excluded from scoring. Reference speech inside a
  turn = the aligned words, joined across gaps under 0.5 s. 6 speakers in total.
- **Cross-session:** a press conference by one of the two leaders, November 2016 (White House, Commons, public
  domain), the first 15 minutes (another room, 2.5 years later).
- **Synthetic:** `tests/fixtures/gen/two-speakers.wav` (8 turns, 2 TTS voices).

Metric: frame-based diarization error rate (10 ms frames, ±0.25 s collar around reference boundaries, optimal
one-to-one mapping) = missed speech + false alarm + speaker confusion; turn accuracy = the majority voice of each
reference turn is that speaker.

### Diarization repair (full press conference, 39 min, 6 speakers)
| Clustering | Raw clusters | Raw DER (miss / FA / confusion) | After repair: speakers (+ brief) | DER (confusion) | Turns ≥ 1.5 s | Turns < 1.5 s |
|---|---|---|---|---|---|---|
| sherpa default 0.5 | 23 | 9.5% (3.1 / 4.2 / 2.2) | **6** (+3) | 7.2% (**0.0**) | 22/24 → 24/24 | 2/6 → 4/6 |
| 0.6 | 18 | 8.1% (3.1 / 4.2 / 0.8) | 6 (+2) | 7.2% (0.0) | 23/24 → 24/24 | 2/6 → 4/6 |
| **0.7 (default)** | 17 | 8.1% (3.1 / 4.2 / 0.8) | **6** (+2) | 7.2% (**0.0**) | 23/24 → 24/24 | 2/6 → 4/6 |
| 0.8 | 17 | 8.1% (3.1 / 4.2 / 0.8) | 6 (+2) | 7.2% (0.0) | 23/24 → 24/24 | 3/6 → 4/6 |
| 0.9 | 12 | 7.5% (3.1 / 4.1 / 0.2) | 6 (+2) | 7.2% (0.1) | 23/24 → 23/24 | 3/6 → 3/6 |

Halves processed separately (as two recordings, threshold 0.7): first 1,250 s (3 speakers) 5 raw clusters → 3;
the rest (5 speakers) 12 → 5 (+2 brief). What remains is missed speech and false alarms (laughter, crosstalk, room
noise; 3% and 4%), not confusion. The two short-turn misses: "Absolutely." (0.6 s by one leader inside the other's turn; the diarizer never separated it) and a 0.5 s reporter line it did not detect as speech.

### Recognizing people
Enrolled from one recording by labeling each voice with the reference (what a person does by naming one voice each):
- **Same event, the other half:** the two leaders 0.930 and 0.899 (both labeled automatically); the three
  reporters who were not enrolled scored at most 0.155, 0.229 and 0.251 (left unnamed, correctly).
- **Another event, 2.5 years later:** the same leader 0.802 (automatic); other voices at most 0.139.
- **Short pieces** (same event, enrolled on the first half, every window inside the second half's turns):

  | Window | Same person ≥ 0.55 | Same person ≥ 0.60 | Different person: p99 / max (pairs) |
  |---|---|---|---|
  | 1 s | 39% | 23% | 0.244 / 0.321 (2,278) |
  | 2 s | 85% | 77% | 0.250 / 0.341 (1,128) |
  | 3 s | 97% | 94% | 0.248 / 0.316 (752) |
  | 5 s | 99% | 99% | 0.251 / 0.309 (448) |

  No different-person pair reached 0.40 (the suggestion threshold), so automatic labels at 0.55–0.60 with a 0.20
  margin had no false accepts here; voices under ~2 s are mostly left to suggestions or to the person.
- **Synthetic fixture:** 2 of 2 speakers, DER 0.0% (collar 0.25 s); in `tests/e2e/speakers_test.go`, naming the two
  voices of the first half labels the second half automatically with ≥ 95% of words correct (Parakeet), whisper.cpp
  transcripts get the same 2 voices, and a count hint re-detects without re-transcribing.

### Limits (honest)
- One real multi-speaker event plus one cross-session check, English, podium microphones with reporters on room
  microphones. Not measured: dinner-table crosstalk, long overlapping speech, very similar voices (siblings), phone
  audio, music under speech, children.
- Overlapping speech is excluded from fingerprints; words inside overlaps take the speaker they overlap most.
  Interjections under ~1 s inside another person's turn usually stay with that person (the diarizer does not split
  them), and very short lines can be missed as speech.
- "Different people never above 0.35" comes from six speakers; similar-sounding people can score higher. That is why
  automatic labels need a 0.20 margin, only confirmed labels enroll, a person can say "not Ada" (never suggested
  again), and very alike people become a "same person?" question instead of being merged.
- Reference timing comes from recognizer alignment (±0.1–0.3 s), hence the collar.
- Pitch only warns: two people can share a pitch, and one person's pitch moves with mood and microphone, so a
  different-sounding pair is a plain warning in the dialog, never a refusal.

### Reproduce
The harness is in the repository; recordings and transcripts are not.
```bash
# words for alignment (Parakeet), then align the reference turns yourself
EVAL_WAV=pc.wav EVAL_OUT=words.json go test -tags voiceeval -run TestEvalWords ./internal/transcribe/
# repair + DER (EVAL_DIAR = sherpa-onnx diarization output lines; EVAL_TRUTH = {"turns":[{"speaker","from","to","spans"}]})
EVAL_WAV=pc.wav EVAL_DIAR=diar.txt EVAL_TRUTH=truth.json go test -tags voiceeval -run TestEvalRefine -v ./internal/voice/
# enroll on A, match B; short-window score distributions
EVAL_A_WAV=… EVAL_A_DIAR=… EVAL_A_TRUTH=… EVAL_B_WAV=… EVAL_B_DIAR=… EVAL_B_TRUTH=… go test -tags voiceeval -run TestEvalMatch -v ./internal/voice/
EVAL_WIN=3 EVAL_A_… EVAL_B_… go test -tags voiceeval -run TestEvalShortWindows -v ./internal/voice/
```
