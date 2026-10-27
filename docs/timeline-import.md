# Timelines from other editors

A timeline made in another editing app can become a cut in MetaRush.

## How timelines are read

MetaRush reads timelines with its own Go readers, one per format, into one model in `internal/interchange`
(`interchange.Read`): OpenTimelineIO JSON, FCPXML 1.8–1.11 (and `.fcpxmld` bundles), FCP7 XML and CMX 3600 EDL. The
readers keep what an import must keep — dissolves, chapters, levels and exact NTSC times, and FCPXML 1.9+'s file
addresses in `media-rep` — and need no Python on the computer that imports. OpenTimelineIO (pinned by version and
SHA-256 in `scripts/interchange-requirements.txt`) is the independent check: `scripts/interchange-check.sh` has it
write each fixture in every format it writes, and the Go readers read those files back. Each reader is tested against
MetaRush's own exports (a round trip), against files OpenTimelineIO writes, and against hand-made files for each
FCPXML version. AAF is not read: it is a binary format with no open reader in Go, and the editors that write AAF also
write FCP7 XML, FCPXML or EDL.

## Importing a timeline

`metarush import timeline <file> --project <project>` (ops `timeline.read` and `timeline.import`), or drop
the file in the dashboard's Add box (anywhere on the page): a timeline exported from another editing app becomes
a **new cut** in the project, as its first version, through the normal edit check and confirm.

```bash
metarush import timeline ~/Desktop/rough-cut.fcpxml --project "Garden walk"            # asks, then saves
metarush import timeline rough-cut.otio --project "Garden walk" --read                 # only say what would come in
metarush import timeline cut.edl --project "Garden walk" --fps 25 \
  --relink "Garden 3.mov=Wide shot" --add "Logo sting.mov=/Volumes/Shoot/Logo sting.mov" --skip "Temp music.wav"
metarush import timeline cut.xml --new-project "Client review" --dry-run --json          # a confirm token for agents
```

| Format | Files |
|---|---|
| OpenTimelineIO | `.otio` (the first timeline of a collection, or `--timeline <name>`) |
| FCPXML 1.8–1.11 | `.fcpxml`, or an `.fcpxmld` bundle (its `Info.fcpxml`); the first project, or `--timeline` |
| FCP7 XML (xmeml 4 and 5) | `.xml` |
| CMX 3600 EDL | `.edl`; say `--fps` (EDLs don't say their frame rate; otherwise it is guessed from the timecodes, with a warning) |

AAF is refused with a plain message: export the timeline in one of the formats above.

### Footage

Each file the timeline plays is matched to a clip in the Library, in this order:

1. **MetaRush's own export** — a timeline MetaRush exported notes each file's clip and SHA-256 (OpenTimelineIO
   metadata, FCPXML `md` and Final Cut Pro 7 XML `metadata` keys `metarush.assetId` and `metarush.sha256`). A
   note is words anyone can write: it is believed with the clip's SHA-256, or without it only for a clip of the
   project the cut goes into — and only when the clip shows a picture where the cut does and is long enough.
2. **Name, length and timecode** — the file or clip name (case, spaces and punctuation ignored, the extension
   too), then the length; every stretch the cut plays must fit inside the clip. An EDL gives the file's own
   timecode, not where the file starts: a clip whose times only fit counting from the hour (01:00:00:00) is
   taken that way, and says so. Several equally good clips, or a name with the wrong length, are asked about.
3. Otherwise it **asks**: *“Garden 3.mov” isn't in the Library. Relink it to a clip, add the file, or leave it
   out?* Clips of the same length are offered. In a terminal the command asks; elsewhere answer with
   `--relink <footage>=<clip>`, `--add <footage>=<file>` or `--skip <footage>` (op `answers`). The dashboard's
   dialog has Use a clip, Add the file (uploaded into the project) and Leave it out. Nothing is made while a
   question is open.

**The files a timeline names are never opened or looked for** — a timeline can name any path on this computer.
Only a file a person answers with is read: it must be a video or sound file on this computer (never a folder, a
picture or a PDF; never named over the network); its fingerprint (the first and last MiB, as the Add box uses)
finds it when the Library already has it, and otherwise it is added to the project as footage — only once the
import is confirmed: the confirm token is checked against the cut as it was checked, and covers which files
(by their bytes) are added, before anything is added.
The terminal never suggests the timeline's own path.

### What comes over

| In the timeline | In the cut |
|---|---|
| The picture on every video track | one storyline: at each moment the topmost clip that shows (b-roll on a track above an interview cuts to it and back) |
| Cuts, dissolves, wipes, slides, pushes, zooms, whips | the same transition (OpenTimelineIO exports from MetaRush keep the exact kind, direction and easing); others are dissolves of the same length, said so |
| Dissolves using footage beyond the cut | the same: the clips overlap by the dissolve, so every clip stays where it was |
| Fades from and to black, dips to black or white | a fade in, a fade out, a fade through black or white |
| A clip's own sound | that clip's sound; a split edit when the sound starts up to 3 s early or carries on up to 3 s (J- and L-cuts); a clip with no sound of its own plays silent |
| Sound under other pictures, music, narration, effects | added sounds at the same times, with their level (dB) and fades; the role (music, narration, effects, dialogue) from the file's roles or track names, else dialogue, said so |
| Sound levels | a clip's level (up to +6 dB) or a sound's level; level changes inside a sound play at one level, said so |
| Constant speed changes | the clip's speed (25%–400%) |
| Compound clips, nested sequences, multicam clips (the angles in use) | opened up into the cut's own clips |
| Chapter markers (and markers named “Chapter …”) | the version's chapters in its delivery notes, the first at 0:00 |
| Other markers | notes on the clip they fall in ("0:12 (marker): Fix the jump") |
| Stretches with no picture | closed up; sounds that played through them are cut there, said so |

**What can't be carried over** is always listed, in plain words, before the cut is made: titles and
generators (with their text), effects, position/scale/crop, colour settings, speed ramps (read as one speed),
level changes inside a sound, captions and graphics MetaRush exported as markers, switched-off clips.

**Limits.** A read stops with a plain answer when a file is over 50 MB, has more than 500,000 XML elements
(or, for OpenTimelineIO, more than a million values — counted before anything is made of them), or opens up
into more than 100,000 clips, titles, transitions and markers, or two million parts, once its compound clips and
nested sequences are opened: each time one is used, everything in it counts again (its keyframes and speed points
included). A compound clip or nested sequence inside itself is listed and not opened again. A piece keeps at most
256 level keyframes and a title its first 1,000 characters; names are kept to a readable length. Every XML value
is cut as it is read (2,048 bytes for an attribute, 4,096 for text), and what each element keeps counts towards
what opening it again costs; a clip's levels and speed points are read once however many parts share them. Each
reader is tested with files built from random mixes of these shapes: reading one costs at most 50 times its size
in memory and under two seconds. Times that aren't numbers (NaN, infinity, more than about three years) are read as
missing. The first 100 things not read are listed one by one, then how many more; a cut takes the first 100
chapters and 500 markers as notes, and says so.

### The round trip

`go test ./internal/interchange -run TestReadRoundTrip` exports the fixture cut (fades, a dissolve, a fade
through black on muted b-roll, a J-cut and an L-cut, a wipe on a 2× clip, music with fades and ducking,
narration) in all three formats, reads each back and compares the plans: every clip from the same source frame
at the same timeline frame with the same transition and sound window, within one frame.
`go test ./internal/ops -run TestTimelineImportRoundTrip` does the same through the operations: export.timeline
with the footage, timeline.import of each file into the same project, `revision.plan` of both versions.
`scripts/interchange-check.sh` adds files OpenTimelineIO writes (OTIO, FCP7 XML, an EDL) read back frame by
frame (`TestReadOtherWriters`). Files from other editors can be read in bulk with
`INTERCHANGE_CORPUS=<folder> go test ./internal/interchange -run TestReadCorpus -v`.
