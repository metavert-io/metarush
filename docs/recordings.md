# Screen recordings

**Record your screen** (the Add box on Home and in a project's Sources, or `/record`) records a
product demo or a walkthrough straight from the dashboard: the screen (a window, a tab or a whole screen), the camera
if you want it, and your microphone, with the teleprompter showing your script. The recording becomes footage in the
Library, with the screen, the camera and your voice as separate tracks lined up on one clock, so a cut can show them
with the camera in a corner or side by side, and everything is transcribed like any footage.

- [Recording](#recording)
- [The teleprompter](#the-teleprompter)
- [How the pieces get to MetaRush](#how-the-pieces-get-to-metarush)
- [One clock](#one-clock)
- [In the Library and in a project](#in-the-library-and-in-a-project)
- [From another computer](#from-another-computer)
- [When something goes wrong](#when-something-goes-wrong)
- [Commands](#commands)
- [Testing](#testing)

## Recording

1. **Screen**: *Choose a window, tab or screen…* opens the browser's picker. This dashboard's own tab is never
   offered. A tab's sound (and, where the system allows, a whole screen's) is recorded with it.
2. **Camera**: pick one, or leave it off. Its bubble shows how you look.
3. **Microphone**: the computer's default or another one, with its level ("Good", "Quiet", "Too loud"), or none.
4. **How the first cut shows them** (with a screen and a camera): *Camera in a corner* (default), *Side by side* or
   *Screen only*. They are separate tracks either way, so any cut can change it.
5. **Teleprompter**: no script, the project's narration script, or a pasted one.
6. **Record**: a 3-2-1 countdown, then everything starts together. *Pause* stops every file (nothing is recorded or
   sent while paused) and the teleprompter; *Stop* sends the rest and adds it to the Library. Keys, when the
   dashboard has focus: Space or → next line, P pause, Enter stop.

When it's in the Library the page offers **Start a cut** with each layout the recording allows, and lists recent
recordings with what to do next.

## The teleprompter

The script scrolls so the line to say sits near the top; the next one counts down its last two seconds. Lines come
one after another at an easy pace (155 words a minute, a quarter second per pause inside a line, the same estimate
narration uses), on the recording's clock, so a pause stops them too. **Next line**, Space, an arrow key or a clicker
brings the next line in early.

A pasted script is cut into lines the way the narration script is (`editspec.SplitScript`): paragraphs; without blank
lines, each row (unless the rows are one wrapped paragraph); one block of text, each sentence. A leading `(warmly)` is
how to say the line. The lines shown are kept on the recording.

**It never shows in what you record**, as long as you share a window or another tab: only what you share is
recorded. Sharing a whole screen, the page warns that the teleprompter shows if this window is on that screen, and
suggests a window or another display. In Chrome and Edge, **Float it above other windows** puts the teleprompter in a
small window that stays on top, so you read it over the app you are recording; it is part of no other window, so only
a whole-screen recording would show it.

## How the pieces get to MetaRush

The browser records each source into its own file: the screen and the camera in two-second pieces (H.264 and AAC in
MP4 where the browser can, else VP9 or VP8 and Opus in WebM), the voice as plain 16-bit samples (like narration
takes). Each piece of picture starts with a keyframe, so it plays from its own start, and is sent as soon as it ends
(in Chrome and Edge, within about half a second): a tab that closes mid-recording loses only the piece it was
recording. Every piece is kept in the browser's storage until MetaRush has it, and sent at once:

| | |
|---|---|
| `POST /api/recordings` | starts a recording: its name, project, layout, script and what each track is |
| `HEAD /api/recordings/{id}/tracks/{track}` | how much of the track MetaRush has (`Upload-Offset`) |
| `PATCH /api/recordings/{id}/tracks/{track}` | a piece at that offset; another offset is refused with MetaRush's, and sending carries on from there |
| `POST /api/recordings/{id}/clock` | the clock as measured so far (1.5 s in, every 5 s) |

The pieces wait on the computer that received them (`<home>/state/recordings/<id>/`) until **finishing** makes each
track footage: the voice becomes a WAV, each video is copied into a clean file without re-encoding (the browser's
pieces carry no length; a file cut off mid-piece still plays up to there), and each is added like any file — words for
the voice (and for a screen's own sound), the on-screen index, duplicates. The clips are tagged `screen-recording` and
`screen`, `camera` or `voice`.

## One clock

Everything is timed on the page's clock: the recorders start together (time 0); the microphone's first sample is
measured from when its blocks of sound arrived (the earliest arrivals, as live narration takes are), and its drift
from a line through them over the whole recording. Pausing drops the same stretch from every file. The recording keeps
where each track starts and its drift (`metarush recording show --details`).

The screen and camera files also carry a copy of your microphone (unless the screen has sound of its own): each plays
with your voice in the Library, and MetaRush checks the clock by that sound when it links the recording into a
project. The sound's alignment replaces the clock when it is very sure, or fairly sure and within 0.15 s of it, but
never moves a track more than 0.5 s from a clock that was measured (a bigger move is the sound matching the wrong
moment); the conversation then says "lined up by their sound", and `metarush recording show` says why the clock stayed
when it did. In the browser run the two agree within 0.04 s.

## In the Library and in a project

A recording made for a project is **linked** into it as soon as it is footage: its clips join the project as one
**conversation** (`screen` shown whole, `camera` framed on your face, `voice`), so everything conversations do works
([conversations](conversations.md)): change what shows from any moment on the timeline, nudge a track a frame, line
it up again. A recording made from Home goes to the Inbox; link it into a project from the recorder's list or with
`metarush recording link`.

**Start a cut** makes a cut whose one clip is the whole recording with one shot: the screen with the camera in a
corner (the vetted `pip` layout), side by side (`side-by-side`), or one of them full frame. Your voice plays
throughout; name tags are off. In a project without a cut it is the first cut; otherwise a new cut named after the
recording. Trim it on the timeline or in the transcript like any cut.

## From another computer

Open the dashboard on another computer through its Tailscale address ([remote access](security.md)): the recording
happens in that computer's browser and its pieces are sent to the computer that runs MetaRush as you record. Screen
recording needs a secure page, which the HTTPS address `tailscale serve` gives; on an `http` address the page says
how to get one.

## When something goes wrong

| What | What happens |
|---|---|
| The screen, camera or microphone is refused, or the computer doesn't allow the browser | its row says so and what to do (the browser's site settings; on a Mac, System Settings → Privacy & Security → Screen & System Audio Recording, Camera or Microphone) |
| A camera or microphone never answers (a virtual one whose app isn't connected) | 9 seconds after you allow it (the browser's question isn't counted), its row says which didn't answer; choose another and try again (the narration recorder does the same) |
| None found, or busy in another app | said, with Try again |
| The shared window closes, or a camera or microphone goes away | the recording stops by itself and is kept |
| The connection drops, or you're signed out | it keeps sending from where MetaRush stopped; nothing recorded is lost |
| The tab closes or the page reloads mid-recording | MetaRush keeps what arrived; the next time the recorder opens in that browser it offers **Send the rest and keep it** (or **Throw it away**); `metarush recording finish <id> --keep` keeps what arrived |
| The computer that runs MetaRush is out of disk space | said; the browser keeps the pieces and sends them again |
| A track has nothing playable (a tab closed before its first piece) | the rest is the recording, and it says what was left out |

## Whose it is, and how much is kept

A recording that isn't footage yet is its maker's: only they, or an owner, keep what arrived or throw it away, and
other members see it only once it is footage (then everyone does). Its clips are the maker's, whoever finishes it.
Finishing happens once: a second finish (another tab, the terminal) answers with the work in progress, and takes it
over only if the process doing it stopped renewing its lease for two minutes (the daemon's recovery does the same);
a recording stuck that way can also be thrown away.

What this browser keeps of a recording is the person's who recorded it: the recorder offers only their unfinished
recordings, and **signing out deletes the recorder's browser storage**, asking first when part of a recording hasn't
reached MetaRush.

On the computer that receives them (`config.yaml`):

| | default | |
|---|---|---|
| `recordings.maxGB` | 100 | the most one recording holds; past it the recording stops and what arrived is kept |
| `recordings.minFreeGB` | 2 | pieces never take the disk below this; the browser keeps them and sends them again once there's room |
| `recordings.keepDays` | 14 | pieces of a recording nobody finished are removed after this many days untouched; its maker is told on the bell two days before (a live recording, one being added and footage are never touched; tracks that already are clips stay) |

## Commands

| Command | |
|---|---|
| `metarush recording list [--project <id>] [--json]` | recordings, newest first: tracks, length, whether they are footage |
| `metarush recording show <id> [--details]` | each track, its footage and place on the clock, the projects it is in |
| `metarush recording finish <id> [--keep]` | make it footage (on the computer that received it); `--keep` after a closed tab |
| `metarush recording link <id> --project <id or name>` | its footage into a project, as one conversation |
| `metarush recording cut <id> [--layout pip\|side-by-side\|screen\|camera] [--project …] [--apply]` | start a cut from it |
| `metarush recording discard <id>` | throw away one that isn't footage yet (asks you at a terminal) |

Recording itself happens only in a browser.

## Testing

- Go: `internal/recorder` (pieces at the host's offset, finishing, keeping what arrived, the sound check, links and
  cuts) and `internal/api` (`TestRecordingPiecesOverHTTP`).
- The dashboard: `web/scripts/recorder-test.mjs` (in `npm test -w web`): the clock (the microphone's start and drift,
  the samples a pause drops), the piece queue (cut-off pieces, dropped connections, a reload, refusals), the
  teleprompter's lines, the file format chosen, how often each recorder hands its pieces over and the device
  deadline.
- The browser run: `scripts/recorder-e2e.sh` (ports 5480–5489), Chromium's fake camera and microphone (asked for by
  device ID) and a fake window. Before it closes a tab mid-recording it waits until pieces of every track have
  reached MetaRush (a piece of picture at least every 3.5 s), then checks that what's kept plays to its end and
  lacks only about the last piece.
