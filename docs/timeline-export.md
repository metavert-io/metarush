# Timelines for other editing apps

`metarush export timeline <revision> --format otio|fcpxml|xmeml --out <file> [--media <dir>] [--link]`
(op `export.timeline`) writes a version of a cut as a timeline another editing app opens. In the dashboard:
**Send to editing app** on a finished render or the Renders tab; it picks a new folder under
`~/Movies/MetaRush exports` (`~/Videos/…` on Linux, `METARUSH_EXPORTS_DIR` to change it) and offers **Show in Finder**.

| `--format` | File | Made for | Also opens it | Import with |
|---|---|---|---|---|
| `otio` | OpenTimelineIO JSON (`.otio`, the 0.14 schemas) | DaVinci Resolve | Any OpenTimelineIO tool | Resolve: File > Import > Timeline… |
| `fcpxml` | FCPXML 1.8 (`.fcpxml`) | Final Cut Pro | DaVinci Resolve | Final Cut Pro: File > Import > XML… |
| `xmeml` | Final Cut Pro 7 XML, version 5 (`.xml`) | Premiere Pro | DaVinci Resolve | Premiere Pro: File > Import… |

The timeline is built from the version's resolved RenderPlan, the same frame-accurate plan the renderer draws, so
every clip, transition and sound sits where MetaRush plays it. Captions are also written as an `.srt` next to the
timeline (`--no-captions` to skip).

## The footage

With `--media <dir>` the originals go into that folder under their own readable names (`Interview.mov`; a second
file with the same name becomes `Interview 2.mov`) and the timeline points there, so the folder can be moved to
another computer. Copies are instant APFS clones on the same Mac disk, else copies checked by SHA-256; `--link` makes
symbolic links instead. Footage comes from this machine's cache, or is downloaded from the media store first. A
manifest (`.metarush-media.json`) remembers what an export placed, so exporting another format into the same folder
reuses the files. Without `--media` the timeline points at MetaRush's own copies in this machine's cache, which only
this machine can open. Nothing is ever overwritten: an existing timeline is refused (`E_CONFLICT`) unless
`--replace`, and footage files are never replaced.

## What each format keeps

| | OpenTimelineIO | FCPXML 1.8 | Final Cut Pro 7 XML |
|---|---|---|---|
| Clips, source in and out points | yes | yes | yes |
| Split edits (sound leading or trailing the picture) | separate picture and sound items on their own tracks | `audioStart`/`audioDuration` on the clip (an expanded split edit) | separate linked picture and sound clipitems |
| Crossfades and dissolves | `SMPTE_Dissolve` transitions | Cross Dissolve on the edit point | Cross Dissolve transition items centered on the cut |
| Fades through black or white | dissolve transitions (named) | opacity keyframes on both clips (white fades through black) | Dip to Color Dissolve (white: pick the color) |
| Wipes, slides, pushes, zooms, whips | dissolves of the same length, named | Cross Dissolves of the same length | Cross Dissolves of the same length |
| Narration, music and effects tracks | audio tracks | connected audio clips in lanes, with roles (dialogue, music, effects) | stereo track pairs |
| Clearer voices (Sound tab) | the sound item plays the clearer copy (a file beside the footage); the picture keeps the original | the same | the same |
| Levels, fades, ducking | as notes in each clip's metadata (OpenTimelineIO has no volume) | `adjust-volume`: level, fade in/out, keyframes for ducking | Audio Levels filter with keyframes |
| Captions, graphics, narration lines not recorded yet | markers (blue, purple, orange) with their text | markers | markers |
| Nested cuts | a nested Stack | a compound clip (`media` + `ref-clip`) | a nested sequence |
| Constant speed changes | `LinearTimeWarp` | `timeMap` | Time Remap |

## What no format carries

The export lists these with each file, in plain words:

- **Reframing** (the crop and zoom that fit footage to the frame, including face-following framing) is not
  written: set position and scale in the editing app.
- **Graphics MetaRush draws** (titles, lower thirds, motion graphics) become markers with their text: rebuild them
  with the app's titles.
- **Captions** are markers plus the `.srt` file, which the apps import as subtitles.
- **Speed** is written as a constant speed; ramps are not.
- **Transitions** without an equivalent are dissolves of the same length (the note says where).
- **Fades through black** shorter than planned: where a clip has less footage before or after the cut than the
  fade needs, the fade is shortened and the note says so.

## How it was validated

`scripts/interchange-check.sh` builds a fixture cut (fades, a crossfade, a fade through black on muted b-roll, a
J-cut and an L-cut, a wipe on a 2× clip, music with fades and ducking under speech, narration, captions, a lower
third and a title, and a nested cut) in all three formats and checks them with the official OpenTimelineIO library
in a throwaway virtual environment (never a MetaRush dependency):

- `.otio` read by `opentimelineio` (otio_json), compared clip by clip with the layout the Go test expects, written
  back and read again, and converted down to the 0.14 schemas;
- FCPXML read by the `otio-fcpx-xml-adapter` (names and durations) and checked from the XML itself (spine offsets
  and durations), and validated with `xmllint` against the FCPXML 1.8 DTD;
- Final Cut Pro 7 XML read by the `fcp_xml` adapter (clips, in points, transitions) and validated against the
  xmeml version 5 DTD.

Result at the time of writing: 151 checks pass for the fixture, the nested cut and a real export, and both XML
files are valid against their DTDs. Two adapter limits are worked around in the checker, not in the files: the
`fcp_xml` adapter ignores speed when it computes in points under transitions (clips with a speed change are
compared by duration only), and the `fcpx_xml` adapter rounds 29.97 to 29 when it adds up mixed rates (offsets are
checked from the XML). The apps themselves (Resolve, Final Cut Pro, Premiere Pro) were not driven by the tests:
the files follow their documented formats and the reference readers.

```bash
scripts/interchange-check.sh                    # the fixture
scripts/interchange-check.sh ~/exports/cut.otio # also check files you exported
```
