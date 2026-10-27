# Test fixtures

Synthetic, deterministic media for MetaRush tests. Media is **generated, not committed**:

```bash
scripts/make-fixtures.sh          # builds into tests/fixtures/gen/ (gitignored); no-op if up to date
FORCE=1 scripts/make-fixtures.sh  # rebuild everything
```

`gen/` holds the media. `truth/` holds the ground truth, which is small and committed. The script rewrites `truth/` on every build, so the timings in it always match the audio that was actually generated.

Requirements: macOS `say` (for speech), and `ffmpeg`/`ffprobe` with libx264 and the native `aac` encoder. The script looks for them on `PATH` first, then in `/usr/local/bin`. It does not need `drawtext` or freetype: labels are drawn with a built-in 5x7 bitmap font made of `drawbox` filters. The default voices are Samantha, Daniel, Karen and Moira. If one is missing, the script picks the next voice on its fallback list (and as a last resort the system default) and records which voice it used in the truth JSON.

Determinism: the inputs are fixed `say` voices at a fixed rate (170 wpm), lavfi sources with fixed seeds, and `-bitexact` muxing. Two consecutive `FORCE=1` builds were checked to be byte-identical on a development Mac (macOS 26 / Darwin 25.5, ffmpeg 8.0.1). Rules the script follows to keep it that way:
- Do not use the `gradients` source. Its output changes between runs even with `seed=` set.
- Do not use x264 VBV (`-maxrate`/`-bufsize`). With frame threads it is not reproducible. Use plain CRF.

A different OS release can still produce slightly different TTS audio. That is why the truth timings are computed at build time instead of being hard-coded.

Total size is about 28 MB.

| File | Format | Purpose |
|---|---|---|
| `interview-a.mov` | 1920x1080, 30 fps CFR, H.264 + AAC 48 kHz stereo, ~27 s | Main "talking head" take. One voice (Samantha) reads 6 sentences. Exactly **1.5 s of digital silence** is inserted after "...asked me one simple question." and before "What would you build if nobody was watching?" (the "vulnerable pause"). The picture is testsrc2 with a moving block and a large "TAKE A" label. Hook-worthy lines are listed in the truth JSON. |
| `interview-b.mp4` | 1280x720, **29.97 (30000/1001)** CFR, H.264 + AAC 48 kHz mono, ~20 s | A second take with a different voice (Daniel, en_GB) and a different script. Tests NTSC timebases. The picture is a Mandelbrot zoom with a "TAKE B 29.97" label. |
| `broll-c.mp4` | **1080x1920 vertical**, 30 fps, H.264 + AAC, 10 s | B-roll with **no speech**: a quiet sine pad plus faint pink noise. Transcribers must emit no words here (hallucination test). The picture is a cellular automaton. |
| `phone-rotated.mov` | 1920x1080 coded frames, **display matrix rotation = 90**, 30 fps, 6 s | Phone footage: players show it portrait. Has speech (Karen, en_AU). Check with `ffprobe -show_streams` (side_data "Display Matrix", rotation=90). |
| `vfr.mp4` | 1280x720, H.264 + AAC, 6 s, **variable frame rate** | 2 s at 30 fps, then 2 s at 12 fps, then 2 s at 24 fps, with the original timestamps kept (timescale 600, `-fps_mode passthrough`). Has speech (Moira, en_IE). How it was checked: `r_frame_rate=120/1` but `avg_frame_rate=7920/359` (about 22.06), and the packet pts deltas take three distinct values, 0.0333 s x60, 0.0833 s x24 and 0.0417 s x47 (`ffprobe -select_streams v:0 -show_entries packet=pts_time`). |
| `music-bed.m4a` | AAC 44.1 kHz stereo, 30 s | Synthesized C–Am–F–G chord bed, 2 s per chord, pulsing envelope, fades. For music-ducking and mixing tests, and as a second hallucination check. |
| `two-speakers.wav` | PCM s16le, **16 kHz mono**, ~29 s | 8 alternating turns between Samantha (S1, female US) and Daniel (S2, male UK), with 0.6 s gaps. For diarization tests. |
| `not-a-video.mp4` | plain text | A text file with a `.mp4` extension. Ingest must sniff the contents, not trust the extension (ffprobe fails on it: "moov atom not found"). |
| `long-silence.mp4` | 1280x720, 30 fps, H.264 + AAC 48 kHz stereo, 8 s | Video with a digitally silent (all-zero) audio track. Transcribers must emit nothing. |

## Ground truth (`truth/`)

- `interview-a.txt`, `interview-b.txt`, `phone-rotated.txt`, `vfr.txt`: the exact text given to `say`.
- `interview-a.json`: voice, rate, `duration_s`, `speech_start_s`/`speech_end_s`, `pause_after_word` ("question"), `pause_before_word` ("What"), `pause_seconds` (1.5) and the exact `pause_start_s`/`pause_end_s` sample boundaries of the inserted silence, plus `hooks`.
- `two-speakers.json`: per turn, `speaker` (S1/S2), `voice`, `start_s`, `end_s` and `text`. The times come from the exact sample counts of the segments that were concatenated. Each TTS segment is trimmed at -55 dBFS, so `start_s`/`end_s` are the speech bounds.

Caveat: all of this speech is clean macOS TTS with no noise, reverb or crosstalk. It is much easier than real interview audio, so treat the transcription accuracy you see here as an upper bound.
