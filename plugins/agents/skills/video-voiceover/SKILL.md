---
name: video-voiceover
description: "Narration and the sound of the person's video — write or fit a script, place lines against the edit, check they fit, free scratch audio, priced AI narration, a recorded read; music, levels, clearer voices, the camera sound. Routes to `metarush voiceover`, `metarush sound`, `metarush mix`. Works whether or not they say MetaRush (the `metarush` CLI). Use it for: \"add a voiceover\", \"write a script\", \"fit my script to the video\", \"the music is too loud\", \"turn off the camera sound\", \"make her voice clearer\", \"add music\"."
---

# Voiceover

**What this covers.** Add or change the narration (voiceover) and the sound mix of one of the person's videos — write a script from the footage and brief or take their text, place each line against the edit (when a shot starts or ends, at a moment in the footage, after the previous line, or at a clock time), check that every line fits, make free scratch audio to judge timing, price and generate ElevenLabs narration, import or record a real read, clean it up and choose takes; and the sound: levels of music, narration, camera sound and each person, clearer voices, music fitted to the video, music that dips under speech, the camera sound on or off. For a task this skill doesn't name, `metarush ops find "<the task>" --details` says which command does it.

Talk to the person as [reference/voice.md](reference/voice.md) describes (plain words, no IDs or flags).

Narration is part of the cut: a script of lines, each placed by a **start point** that MetaRush turns into exact
frames. Lines tied to a shot or a moment move with it when the cut is re-edited; that is why you place lines by
shots and moments, and use a clock time only when the person names one. Every change is a new revision that the
person sees first, like any edit. The narration and the mix also read as text in the cut sheet (`## Voiceover`,
`mix:`; see [reference/cut-sheets.md](reference/cut-sheets.md)): handy to show the person the script with where each
line starts, or to reword and re-place several lines at once. Read [reference/edit-plans.md](reference/edit-plans.md) (voiceover and mix parts),
[reference/cli.md](reference/cli.md) and [reference/safety.md](reference/safety.md).

Talk to the person as an amateur editor: "line 2 starts when the drone shot begins", "the music gets quieter whenever
someone talks", "this line is too long for its spot: 28 words need about 9.5 seconds and it has 4". No IDs, flags,
JSON or decibel jargon unless they ask; you read the `--json` output, they get plain words.

## 1. Ground yourself
`metarush project show <prj> --json` (brief, head revision) and `metarush revision show <head> --json` (clips with
their IDs, labels and timing; any existing `voiceover` and `mix`). `metarush voiceover script show --revision <head>
--json` lists the current lines with where each starts, whether it fits and what audio it has. Look at transcripts,
`footage moments` and frames you Read when you need to know what is on screen.

## 2. The script
- **User's words**: keep them. **Write one** only when asked: from the footage, the brief and the target length.
  Narration runs about 155 words a minute (2.5 words a second); leave room to breathe and do not talk over
  on-camera speech unless the person wants that.
- Write plain text, one paragraph per line. Optional markup at the start of a paragraph places it:
  `[@broll]` (when that shot starts), `[@broll+0.5]`, `[@broll:end]`, `[0:12.4]` (clock time),
  `[after+0.8]` (0.8 s after the previous line), `(warm, slower)` (delivery notes, never spoken). A script is
  used **as-is**: unplaced paragraphs follow the previous line and no timing is guessed — the person's live
  recording sets it (`--spread` puts unplaced lines on the shots in order instead, as earlier versions did). The
  script exactly as given is kept with the cut (its original; never changed afterwards).
- `metarush voiceover script import <file> --revision <head> --json` (add `--append` to keep existing lines) checks
  the result and prints a confirm token and fit problems. Show the person each line and where it starts; apply only
  after they agree, with the same command plus `--confirm <token>` (or `metarush edit apply --plan <file> --confirm
  <token>` with the plan file it printed).
- **Narration for one shot** (the person asked about one clip, or the request names one): add `--clip <shot>` to
  `script import`. The first line starts with that shot (0.3 s in) unless the script places it, and the new lines
  replace only the lines that started over that shot; the rest of the script stays.
- One line at a time: `metarush voiceover beat add --text "…" --at-clip <clip> [--offset 0.5]` (or `--at-moment
  <asset>@<seconds> --quote "right after she laughs"`, `--after-previous [--gap 0.5]`, `--at-time 0:12.4`),
  `beat update <line> --text …`, `beat move <line> --at-clip …`, `beat remove <line>`; each prints the check first.
- "Put line 2 at 0:12" → `beat move <line> --to 0:12` ties the line to what plays there (the shot under that point,
  or the moment or line it already follows), so it moves with later edits; add `--exact` only when the person wants
  that clock time whatever happens to the shots. `beat add --text "…" --to 0:08` works the same way.
- The person can do all of this themselves on the **Voiceover** tab of the studio (write or paste lines, drag them
  along the timeline's narration lane, record with a teleprompter, pick an AI voice). How loud everything is — the
  music, the narration, the camera sound, each person — and clearer voices and music are on the **Sound** tab. Point
  them there when they would rather click than ask.

## 2b. "Fit my script to the video"
When the person asks you to fit **their** script to the video (tie lines to the right moments, make them fit):
1. Read the cut and the lines: `voiceover script show --revision <rev> --json` (each line, its start, the pace),
   `revision show <rev> --json` (shots, timing, labels), transcripts, and frames or sheets where you need to see
   what is on screen.
2. For each of their lines, in order, decide where it starts (an anchor: `{"kind":"clip","clipId":"…","offset":0.4}`
   or a moment `{"kind":"moment","assetId":"…","at":12.3,"quote":"…"}`, or `"at": "0:09.4"` — tied to the shot
   there), and its words: trimmed or extended only as much as the time before the next line needs at their
   pace, references matched to what is on screen. **Keep their voice and style; add nothing their script does
   not say unless the flow needs it.** Give a one-sentence plain reason for every change.
3. Write `{"lines":[{"id":"line-1","text":"…","anchor":{…},"reason":"…"}],"note":"…"}` (one entry per line, the
   ids from script show, same order; keep the text when only the start changes) to a file — in the dashboard's
   Claude Code panel, your proposals folder — and run `metarush voiceover script fit --propose <file> --revision
   <rev> --json`. It compares both versions line by line and checks the change.
4. Show the person, per changed line, their words, yours and why. Use it only after they agree: the same command
   with `--confirm <token>`; `--keep 3,5` keeps their own words on those lines. Their original is never changed:
   `voiceover script use original|fitted` switches back and forth; `voiceover script fit --keep N|--use N`
   changes a line later. In the dashboard's panel the command becomes a card they review line by line.
   Do not run `voiceover script fit` without `--propose` yourself: that starts a separate Claude run for people
   working outside a Claude session.

## 3. Does it fit?
`metarush voiceover fit --revision <rev> --json` lists lines that run into the next one or past the end, with the
time each needs. Offer the fixes in plain words: shorten the line (you rewrite it), let the shot run longer, start
the line a little earlier, move the next line later, or (AI voices only) speak up to 8% faster.
`voiceover fit --apply <preferred|extend-shot|speed-up|start-earlier|move-next>` builds the fix as a plan to check
and apply like any edit. The project's `fitPolicy` says whether to fix automatically, ask, or shorten.

## 4. Audio for the lines
- **Scratch voice (free, robotic)** to judge timing before anyone records or pays:
  `metarush voiceover scratch --revision <rev> --json`, then apply the printed plan. Say it is a placeholder.
- **ElevenLabs** (natural AI voice, paid by the person):
  1. `metarush providers status elevenlabs --json`. Not set up → tell the person, in plain words, to add **their
     own** ElevenLabs key under Settings → Integrations in the dashboard. Don't show them a command; only if they
     ask how to do it from a terminal, mention `metarush providers set elevenlabs` (they run it themselves).
     Never ask for a key in the chat, never read one from files, never use a key you find.
  2. Voice and model: `metarush voiceover voices [--search calm] --json`, `metarush voiceover models --json`;
     the person can play samples and pick a default in Settings → Integrations.
  3. **Always price it first**: `metarush voiceover estimate --revision <rev> --json` (credits per line, lines
     reused for free, the project's spending limit and the plan's allowance). Generate only after the person agrees:
     `metarush voiceover tts --revision <rev> --json`, then apply the printed plan. Unchanged lines cost nothing.
  4. AI narration is marked as AI in the delivery files; mention it if the video goes to a client.
- **Their own recording**: `metarush voiceover import <file> --revision <rev> --json` stores it, transcribes it,
  matches it to the script and places each piece at its line (`--continuous` keeps it as one piece). It measures
  their speaking pace, so later fit checks use it. Recording is something the person does themselves: on the
  Voiceover tab's recorder (live by default: they watch the cut and speak, and the take lands exactly where they
  said each line, cut only in the pauses; or one line at a time), or `metarush voiceover record --live --from
  0:00 --revision <rev>` in their own terminal (`record` without `--live` goes line by line). A live take moves
  each line's start point to where it was said; `voiceover take place <take> --planned --revision <rev>` puts
  the lines at their planned spots and `--at-recorded` back where they were said (both print a change to
  confirm). Re-recording one line (`--until`, or the recorder's "Re-record only line N") replaces only it.
- **Clean-up**: `metarush voiceover cleanup --revision <rev> --json` makes a cleaned version (less noise and
  rumble, softer harsh s sounds, even level) and keeps the original; `voiceover takes list` and `takes choose --beat
  <line> --original|--processed|--take <id>` switch between them (A/B).
- **A voice copy (cloning)** is for the person's **own** voice, made by them (`metarush voiceover clone` is for
  people only). Anyone else's voice needs that person's recorded consent; never suggest copying a third party.

## 5. The sound
`metarush sound show --revision <rev> --json` explains it all: each part's level, each person's level over their
own speech, clearer voices (and how much they helped), the automatic help, the music, and which ways to make a
voice clearer work on this computer (`mix show` has the levels alone). Each command takes `--revision <rev>` (or
`--project <id>` for its latest cut); every change prints a plan to confirm (`--confirm <token>`; `--apply` when
the person already said yes):
- **One person** louder or quieter, only while they speak: `metarush sound levels --speaker "Maya" --gain 3`.
  Parts: `--part narration|music|camera|effects --gain -3` (`--mute`, `--solo`); `--duck on --depth 14`.
- **Clearer voices** (the recordings stay as they are; a clearer copy plays): `metarush sound enhance "Maya"
  --mode clean` (on this computer, fast, free; the default), `--mode studio` (slower, rebuilds the voice; every
  word is checked), `--mode cloud` (ElevenLabs, paid: always `--estimate` first and ask). Targets: a person's
  name, `narration`, `everyone`, or a recording. `--strength 0.7` keeps some of the room; `--off`/`--on` is the
  A/B; `--remove` goes back to the recording. `metarush sound compare --details` says how much clearer each
  voice got; warn the person when it says a voice may sound processed or not quite like them. Studio needs a
  one-time setup the person does themselves (`metarush sound setup studio` in their terminal, or the Sound tab).
- **Music**: `metarush sound music add <file|title>` fits it to the video (starts with it, ends with it on a
  beat, fades out); `--from 12 --to 70 --at 3` places it by hand; `--loop` repeats a short track;
  `--level -16 --duck on|off`. `metarush sound music set <track> …` changes it (`--remove` takes it out).
  Ask where the music comes from (To-dos asks too): platforms mute unlicensed music.

Levels the older way (prefer `sound levels` above; the person's own place for levels is the Sound tab):
`metarush mix show --revision <rev> --json` explains the levels. With narration, music and the clips' own sound get
quieter automatically while the voice speaks (and music under on-camera speech), and the overall level is set for
the platform. Changes go through `metarush mix set … --json` (check first, then `--confirm <token>`):
`--bus music --gain -3` (quieter), `--bus voiceover --gain 2`, `--mute-source [--clip <clip>]` (turn off the
camera sound), `--music-under-voice -18` (music drops more under the voice), `--ducking off`, `--loudness -14`
(`--loudness-auto` goes back to the platform's usual level). `--preview --json` resolves the change into the plan
the cut would play and saves nothing, for checking a level before proposing it.
Tell the person what they will hear, not the settings: "the music now dips further under your voice", "the whole
video is a little louder, about right for Shorts", "the loudest moments won't distort". LUFS, true peak, dBTP and
ducking, de-essing and DNSMOS are words for the flags and `--json`, never for them ("the voice clarity score",
"less hiss", "softer harsh s sounds" are); give a number only if they ask for one.
Never quote these commands to the person. When a change needs their OK, ask about what they will hear ("Shall I
turn the music down by about a third?"), never "approve the `metarush mix set` command" or "the command needs your
permission". If a change is refused because you can only look (for example in the dashboard's Claude panel), say
"I've put that change on a card for you to confirm" and describe what it does; on their own they can make it on the
Sound tab.

## 6. Hear it
Offer a review render (`video-render` workflow) and a quality check (`video-qa`): it flags narration over on-camera speech,
music or camera sound too loud under the voice, clipping, recordings that do not match the script, lines that
start before their spot, and listens to the render for repeated or missing words. Do not call the narration good
until it has been rendered and checked.

Never approve revisions, never print secrets, never spend money without the estimate and the person's yes.
