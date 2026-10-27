# Sound: levels, clearer voices and music

The Sound tab (and `metarush sound …`) is everything about how a cut sounds: how loud each part and each person
is, voices made clearer, and the music. Every change is one small checked plan that becomes a new version of the
cut, through the same ops the CLI and Claude Code use (`sound.show`, `sound.levels`, `sound.preview`,
`sound.enhance`, `sound.compare`, `sound.music.add`, `sound.music.set`, `sound.setup`).

## Levels
- **Parts**: camera sound (the clips' own sound), narration, music, effects, as before (`mix.buses`).
- **Each person** (`mix.voices[].gainDb`, by the name the speakers show): louder or quieter only while they speak.
  Every moment of a recording belongs to the nearest transcript segment's speaker (a pause between two people is
  split in the middle) and the level changes over 0.08 s, linear in dB, so nobody else moves.
- **Music tracks**: their own level, and whether they get quieter while someone talks (`audio[].duck`: on, off, or
  as the mix decides) and by how much (`duckDb`).
- Every level change is heard in the preview at once (`sound.preview` resolves the changed cut; nothing is saved)
  and saved together (`sound.levels`).

## Clearer voices
A voice is made clearer **without changing the recording**: MetaRush makes a clearer copy of the stretch of the
recording the cut uses and plays it instead (`mix.enhanced`: recording, where the copy starts, its settings;
`RenderPlan` `variant` on the clip's sound window or a dialogue track; renders refuse a plan whose copy is
missing, the preview falls back to the recording). A person's setting (`mix.voices[].enhance`: mode, strength,
off) applies over their speech only; a recording-wide setting covers everyone in it. Narration takes use their
existing cleaned version (`take.processed`, preset `clean 100%`). Copies are hidden assets (tag `enhanced`) in
the cut's project, kept out of every Library view, search, footage list and what project links bring in, and
deleted with their footage (also when the Trash is emptied). Timeline exports (`export timeline`) play the
copies too and put them beside the footage with `--media`; FCPXML plays such a clip's picture muted with the
copy as a connected clip.

- **Clean** (default; on this computer, fast, free): DeepFilterNet 0.5.6 removes noise and room echo, then a
  polish in ffmpeg: rumble and pops (a high-pass and a low shelf that acts only on bursts), less mud around
  320 Hz, a little presence around 4.2 kHz and air above 11 kHz, softer harsh s sounds, gentle compression and a
  slow leveler. It sets itself up the first time (about 30 MB, pinned and checked).
- **Studio** (on this computer, slower): the cleaned voice is rebuilt by Resemble Enhance for a studio sound. A
  generative model can say a word differently, so the local recognizer hears the cleaned and the rebuilt voice and
  wherever a word differs the cleaned voice plays. One-time setup by a person: about 1.8 GB (its own Python
  environment with pinned packages, and the model). Every stretch a run makes clearer, from any number of
  recordings, goes to one run of the model, so it loads once. Where it runs:
  - **Apple silicon**: on the graphics chip (PyPI's torch, Metal). Intel Macs have no build (setup says so).
  - **Linux with an NVIDIA graphics card**: setup reads the card and driver (`nvidia-smi`'s fixed query, else the
    driver's version file) and installs PyTorch's CUDA 12.8 build for cards from Volta to Blackwell (compute
    capability 7.0 and up), CUDA 12.6 for older ones (Maxwell, Pascal), with a driver of 525.60 or newer: about
    4 GB with the card's libraries. PyPI's own Linux build needs CUDA 13 (driver 580, Turing or newer), so it is not
    used.
  - **Otherwise** (no card, an older driver or card): the CPU build, without the CUDA libraries; the Sound tab says
    why it runs on the processor.
  - When it starts, the runner checks that the build has code for the card and falls back to the processor (saying
    why) when it hasn't, when the model doesn't load on the card, or when a part runs out of the card's memory (that
    part and the rest run on the processor). The CUDA path is unit-tested only: no NVIDIA card was available.
- **Cloud**: ElevenLabs voice isolation with this computer's key, 1,000 credits a minute of audio (checked
  2026-10-19), priced first and refused over the project's budget or the plan's credits before anything is sent.
  The price is an estimate in dollars: "about $0.12" at ElevenLabs' pay-as-you-go $0.12 a minute (read
  23 Oct 2026; a plan's credits may cost less), which opens to each stretch of a recording that goes to the Voice
  Isolator, its minutes and credits (`EnhanceEstimate.cost`). With a person's estimates off (Settings → Preferences)
  the Sound tab doesn't ask about the price first, and Auto runs Claude's cloud card by itself; the project's budget
  and the plan's credits still refuse what doesn't fit.
- **Strength** is how much of the clearer voice is heard (a blend with the recording); 100% by default, because
  blending brings the noise back.
- **A/B**: a person's clearer voice off and on again (saved; the copy is kept, so switching is free), and "hear
  every voice as recorded" in the preview (nothing saved).
- **Nothing is made twice**: processed voices and copies are content-addressed (`sound_cache`: the recording's
  SHA-256, the stretch, the mode and the engine's versions), across the workspace's machines; a cloud result is
  never paid for twice. Work asked on a version that moved on meanwhile is placed on the latest version.

### Proof
Each copy is measured for each person before and after, and said in plain words: the **voice clarity score**
(DNSMOS P.835, 1–5: overall, the voice itself, the background; computed in Go with onnxruntime and checked
against Microsoft's reference implementation), how far the **background** sits below the voice, and how much it
**still sounds like them** (voice fingerprints). Warnings say when it went too far: the score dropped, the voice
itself sounds processed, it may not sound like the person any more, a blend that may sound hollow, or too little
speech to judge. `metarush sound compare --details` shows the numbers; QA lists the clearer voices a render plays.

Measured on the development Mac (M2 Max), 2026-10-19, with `scripts/make-sound-fixtures.sh` (two `say` voices in
a room with pink noise and mains hum about 12 dB under them; a narrator on a hissy microphone with thumps):

| | Before | Clean (100%) | Studio |
|---|---|---|---|
| Samantha (clarity overall / voice / background) | 2.55 / 3.37 / 2.95 | 2.98 / 3.36 / 3.84 | 3.38 / 3.61 / 4.15 |
| Daniel | 2.51 / 3.38 / 2.88 | — | 3.16 / 3.39 / 4.10 |
| Narration | 2.83 / 3.54 / 3.15 | 3.17 / 3.44 / 4.10 | — |
| Background below the voice | 27–31 dB | 60 dB (no audible background) | 60 dB |
| Still sounds like them | — | 0.85–0.93 | 0.63–0.71 (a warning says so) |
| Words the recognizer still hears | 58 of 58 | 58 of 58 | 58 of 58 (unguarded, the rebuild changed about one word in five: “seedlings” → “sea lace”) |

Clean blended with the recording scores lower (two-person fixture, overall): 100% 3.15, 95% 3.09, 90% 2.97,
80% 2.86. Studio speed: 28 s of speech in 54 s (about 1.9 s of work per second, including cleaning, the word
check and scoring; the model alone measured 0.41 s per second at setup); clean: 14 s in about 5 s.

One model load per run (2026-10-20, M2 Max, the real component in a scratch home, `TestStudioSpeedAcrossRecordings`
with `-tags voiceeval`, medians of 2 rounds): 3 recordings of 8 s took 29.2 s one at a time and 16.0 s at once
(1.82×); 5 recordings of 6 s took 36.2 s and 18.0 s (2.01×): about 4.5–6.6 s saved per recording after the first.

### Clean up voices as a fix

A source's fixes ([sources](sources.md)) include **Clean up voices** for calls, screen recordings and voice
recordings. It runs the same clean engine on the whole recording and makes a **fixed copy** of the source (the
original is kept), so everything after hears the cleaner sound: the transcript and the voices are made again (names
people gave stay), and new cuts play it. The Sound tab's clearer voices stay per cut and per person, over their speech
only; both can be used, and a cut's clearer voice is made from the media its version plays. **Remove hum**, **Even
out loudness** and **Even out each person's volume** are fixes too (ffmpeg's notch filters, two-pass loudness
normalization, per-speaker gains); the Sound tab's levels and "even out voices" stay settings of a cut.

## Music
- **Fits the video** by default (`audio.fit: auto`): it starts with the video and ends with it, with a 2.5 s fade; a
  shorter track fades out early, or repeats with `loop` (repeats blend over 0.08 s).
- **Ends on a phrase**: music added or fitted gets `ending: phrase`: it ends where the music starts its next
  phrase within 4 s of the video's end, else at a bar line within 2.5 s, else on the latest beat within 1.5 s
  (`ending: beat` ends on a beat only). The Sound tab and cut sheets say which ("ends on a phrase"); `--ending
  phrase|beat` on `sound music add|set`. Tracks fitted before this have no `ending` and end on the latest beat of
  the first-15-minutes analysis (evidence `beats`), exactly as before.
- **The music grid** (evidence `beats-2`, made the first time a track with an ending is used): the whole track,
  streamed from ffmpeg, at its own tempo as it changes (8-second autocorrelation windows on a Viterbi path), beats
  by dynamic programming with the period of each moment (refined between frames), the meter (3 or 4) and bar lines
  (low end and harmony at each beat, on a path that may slip a beat at a cost), and phrases of 4 or 8 bars (texture,
  harmony, a crash, a fill at each bar line). `sound show` and the Sound tab say what it found ("Its tempo changes:
  128, then 128 to 96 beats a minute, 4 beats to the bar, phrases of 8 bars.").

Measured on generated music with known beats, bars and phrases (`go test ./internal/sound -run Grid`: five songs,
tempo changes, a waltz, a 20-minute track), against the first-15-minutes analysis (`beats-1`):

| | `beats-1` | music grid |
|---|---|---|
| Beats found (F-measure, ±70 ms) | 0.51–0.53 where the tempo changes | 0.990–0.992 |
| Tempo | one value | within 0.8–1.0% over time |
| Meter | — | right in all five |
| Bar lines / phrases (F) | — | 0.97–0.99 / 0.92–0.97 |
| Endings on a bar line | 2–27% | 97–100% |
| A 20-minute track with a tempo change at 16:00 | first 15 minutes, one tempo | read whole, both tempos, beats F 0.999 |

Real songs with rubato or no clear phrasing fall back to bar lines or beats; the numbers above come from generated
music (they haven't been measured on real songs yet).
- **Placed by hand** (no `fit`): from `in` to `out` of the track, starting `at` in the video — exactly as before;
  dragging it on the timeline's music lane, or its ends, places it by hand.
- Added music gets quieter while someone talks (`duck: on`) at −12 dB under the voices; the rights question
  (To-dos) asks where it comes from.

## In the Claude Code panel
Claude reads the sound (`sound show|compare`) and asks for changes with the ordinary commands; each becomes a card
the server builds from Claude's command and previews with nothing saved, made or spent
(`internal/assistant/sound_actions.go`, `sound.Preview*`), then runs the same op as the CLI with apply when the
person confirms it:
- **A person's level** (`sound levels --speaker Maya --gain 3`): "Make Maya louder", "Only while Maya
  speaks; nobody else changes"; a name nobody in the cut has makes no card. **Music quieter under voices**
  (`--duck on --depth 14`) and the other levels read the same way (the change in words, amounts below).
- **A clearer voice** (`sound enhance <who> --mode …`): clean says what it takes and that nothing leaves this
  computer (and that the voice cleaner sets itself up first, about 30 MB); **cloud shows its price first** ("about
  $0.12", an estimate that opens to the stretches, minutes and credits), what the project and the ElevenLabs
  plan have left, and can't be confirmed over the project's
  budget, the plan's credits or without a key (the card says why); **studio shows its one-time setup** (about 1.8
  GB) as the card's first line when it isn't set up: the card is then the person's decision (a styled
  confirmation, "Set up and make it clearer") and sets studio quality up first (`sound.setup`, people only), unless
  a setup is already running on this computer. A price only (`--estimate`) or a preview makes no card.
- **Music** (`sound music add <title>`: music already in the workspace; a file on this computer is added on the
  Sound tab first) and **fitting it** (`sound music set <track> --fit auto`, levels, quieter while someone talks,
  taken out), and **setting clearer voices up** (`sound setup`, a people-only card).

## Commands
```bash
metarush sound show --project <id> [--details]
metarush sound levels --project <id> --speaker "Maya" --gain 3      # only while she speaks
metarush sound levels --project <id> --part music --gain -4 --preview --json
metarush sound enhance Maya --project <id> [--mode clean|studio|cloud] [--strength 0.8] [--estimate]   # cloud at a terminal: the estimate, then a yes (estimates on)
metarush sound enhance Maya --project <id> --off                     # A/B: the recording again
metarush sound compare --project <id> --details
metarush sound music add song.m4a --project <id> [--fit auto | --from 12 --to 70 --at 3] [--level -12] [--duck on|off] [--loop] [--ending phrase|beat]
metarush sound music set music --project <id> --level -16 --duck off [--ending phrase]
metarush sound setup studio                                             # people only; about 1.8 GB
metarush doctor                                                         # Clearer voices: clean and studio set up or not, studio's measured speed
```
Each change prints the plan to confirm (`--confirm <token>`, or `--apply`); `--json` everywhere. `metarush doctor`
says in its Clearer voices part whether clean and studio are set up here (set up only in part, or for an earlier
MetaRush) and studio's speed measured at setup, as the estimate uses it; `--details` adds the versions and folders.

## Testing
- `go test ./internal/sound/` (MongoDB): music fit, levels and quieter-when-talking, cloud pricing, budget and
  plan refusals and caching against `internal/testutil/fakeeleven`; with `METARUSH_TEST_SOUND_HOME` (a home where
  `sound setup` ran; `METARUSH_TEST_SOUND_INSTALL=1` sets it up there) clean over one person only (the other
  person's turns stay bit-identical), the speech check, narration takes and work landing on the latest version;
  `METARUSH_TEST_STUDIO=1` adds studio. Without components: one model load across recordings (a fake runner),
  the torch build for each kind of computer and NVIDIA card (`TestPickTorch`), reading cards (`TestReadNvidiaCards`)
  and the runner's own device choice with a stub torch (`TestStudioRunnerPicksADevice`, needs python3).
- `go test -tags voiceeval -run TestStudioSpeedAcrossRecordings ./internal/sound/` with `METARUSH_TEST_SOUND_HOME`
  (`EVAL_RECORDINGS`, `EVAL_SECONDS`, `EVAL_ROUNDS`): one load per recording vs one per run, timed.
- `scripts/sound-e2e.sh`: the Sound tab in a real browser (33 checks, screenshots desktop and phone, dark and
  light); `video/render/selftest.mjs` "sound": renders play only the clearer copies, from the right place.
