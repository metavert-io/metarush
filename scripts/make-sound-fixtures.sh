#!/usr/bin/env bash
# make-sound-fixtures.sh — synthetic audio for the Sound features (clearer
# voices, music fit). Writes media into tests/fixtures/gen/sound/ (gitignored)
# and the ground truth into tests/fixtures/truth/sound-*.json (committed).
#
#   scripts/make-sound-fixtures.sh            # build if missing or out of date
#   FORCE=1 scripts/make-sound-fixtures.sh    # rebuild everything
#
# Everything is made from fixed inputs: macOS `say` voices at a fixed rate,
# ffmpeg lavfi sources with fixed seeds, and a room echo built from a seeded
# noise burst. Outputs:
#   noisy-two-speakers.wav   48 kHz mono: two people taking turns (Samantha,
#                            Daniel) in a room (reverb), with pink noise and
#                            mains hum about 12 dB under the voices
#   noisy-two-speakers.mov   the same sound in a small video (AAC stereo),
#                            for clips
#   clean-two-speakers.wav   the same voices without room, noise or hum (the
#                            reference the speech check and SNR are judged by)
#   noisy-narration.wav      48 kHz mono: one narrator (Karen) close to a
#                            hissy microphone, with low thumps on "p" words
#   music-beat.m4a           40 s at 120 bpm: a kick on every beat (the first
#                            at 0.25 s) under a chord bed, so beat times are known
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
GEN="$ROOT/tests/fixtures/gen/sound"
TRUTH="$ROOT/tests/fixtures/truth"
mkdir -p "$GEN" "$TRUTH"
FFMPEG="${FFMPEG:-$(command -v ffmpeg || true)}"; FFMPEG="${FFMPEG:-/usr/local/bin/ffmpeg}"
[ -x "$FFMPEG" ] || { echo "ffmpeg not found" >&2; exit 1; }
command -v say >/dev/null || { echo "macOS 'say' not found (speech fixtures need it)" >&2; exit 1; }
FF=("$FFMPEG" -hide_banner -loglevel error -nostdin -y)
BITEXACT=(-fflags +bitexact -flags:v +bitexact -flags:a +bitexact -map_metadata -1)
RATE_WPM=170
SR=48000

OUTPUTS="noisy-two-speakers.wav noisy-two-speakers.mov clean-two-speakers.wav noisy-narration.wav music-beat.m4a"
STAMP="$( { cat "${BASH_SOURCE[0]}"; "$FFMPEG" -version | head -1; say -v '?' | grep -E '^(Samantha|Daniel|Karen) ' ; } | shasum -a 256 | awk '{print $1}')"
if [ "${FORCE:-0}" != "1" ] && [ -f "$GEN/.stamp" ] && [ "$(cat "$GEN/.stamp")" = "$STAMP" ]; then
  missing=0; for f in $OUTPUTS; do [ -s "$GEN/$f" ] || missing=1; done
  if [ "$missing" = 0 ]; then echo "sound fixtures up to date in $GEN (FORCE=1 to rebuild)"; exit 0; fi
fi
WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-sound-fixtures.XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT

# speak <voice> <text> <out.wav>: 48 kHz mono, trimmed to the speech (-55 dBFS).
speak() {
  say -v "$1" -r "$RATE_WPM" -o "$WORK/s.aiff" "$2"
  "${FF[@]}" -i "$WORK/s.aiff" -af "aresample=$SR,silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse,silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse" -ac 1 -c:a pcm_s16le "$3"
}
dur() { "${FFMPEG%ffmpeg}ffprobe" -v error -show_entries format=duration -of csv=p=0 "$1" 2>/dev/null || ffprobe -v error -show_entries format=duration -of csv=p=0 "$1"; }

# ---- two people taking turns ------------------------------------------------
TURNS=(
  "Samantha|Welcome back to the garden show. Today we are planting tomatoes."
  "Daniel|Thanks for having me. I brought three kinds of seedlings."
  "Samantha|Which one grows best on a small balcony?"
  "Daniel|The cherry tomato, because it stays compact and fruits early."
  "Samantha|Perfect. Let us put them in the pots right now."
  "Daniel|Water them gently, and keep them in the sun."
)
GAP=0.7
LIST="$WORK/turns.txt"; : > "$LIST"
JSON="$TRUTH/sound-two-speakers.json"
printf '{\n  "file": "noisy-two-speakers.wav",\n  "gap_s": %s,\n  "turns": [\n' "$GAP" > "$JSON"
t=0; i=0
"${FF[@]}" -f lavfi -i "anullsrc=r=$SR:cl=mono" -t "$GAP" -c:a pcm_s16le "$WORK/gap.wav"
for turn in "${TURNS[@]}"; do
  voice="${turn%%|*}"; text="${turn#*|}"
  speak "$voice" "$text" "$WORK/t$i.wav"
  d="$(dur "$WORK/t$i.wav")"
  who="S1"; [ "$voice" = "Daniel" ] && who="S2"
  start="$(awk -v a="$t" -v g="$GAP" 'BEGIN{printf "%.3f", a+g}')"
  end="$(awk -v a="$start" -v b="$d" 'BEGIN{printf "%.3f", a+b}')"
  [ $i -gt 0 ] && printf ',\n' >> "$JSON"
  printf '    {"speaker": "%s", "voice": "%s", "start_s": %s, "end_s": %s, "text": "%s"}' "$who" "$voice" "$start" "$end" "$text" >> "$JSON"
  printf "file '%s'\nfile '%s'\n" "$WORK/gap.wav" "$WORK/t$i.wav" >> "$LIST"
  t="$end"; i=$((i+1))
done
printf "file '%s'\n" "$WORK/gap.wav" >> "$LIST"
printf '\n  ]\n}\n' >> "$JSON"
"${FF[@]}" -f concat -safe 0 -i "$LIST" -c:a pcm_s16le "$WORK/clean.wav"
cp "$WORK/clean.wav" "$GEN/clean-two-speakers.wav"
TOTAL="$(dur "$WORK/clean.wav")"

# A small room: an impulse response from a seeded noise burst with a 0.35 s decay.
"${FF[@]}" -f lavfi -i "anoisesrc=color=white:amplitude=0.5:seed=11:sample_rate=$SR" -t 0.6 \
  -af "aeval='val(0)*exp(-t*19.7)*if(lt(t,0.002),0,1)':c=same,adelay=1|1" -ac 1 -c:a pcm_f32le "$WORK/ir.wav"
# Room (wet 35%) + pink noise + 50/100/150 Hz hum, about 12 dB under the voices.
"${FF[@]}" -i "$WORK/clean.wav" -i "$WORK/ir.wav" \
  -f lavfi -i "anoisesrc=color=pink:amplitude=0.045:seed=5:sample_rate=$SR" \
  -f lavfi -i "sine=frequency=50:sample_rate=$SR" -f lavfi -i "sine=frequency=100:sample_rate=$SR" -f lavfi -i "sine=frequency=150:sample_rate=$SR" \
  -filter_complex "[0:a]asplit=2[dry][toir];[toir][1:a]afir=dry=10:wet=10:irgain=1[room];[dry]volume=0.75[d];[room]volume=0.35[r];[d][r]amix=inputs=2:normalize=0:duration=first[voice];[3:a]volume=0.012[h1];[4:a]volume=0.006[h2];[5:a]volume=0.003[h3];[h1][h2][h3]amix=inputs=3:normalize=0[hum];[voice][2:a][hum]amix=inputs=3:normalize=0:duration=first,atrim=0:$TOTAL,alimiter=limit=0.95:level=disabled" \
  -ac 1 -ar $SR -c:a pcm_s16le "${BITEXACT[@]}" "$GEN/noisy-two-speakers.wav"
"${FF[@]}" -f lavfi -i "testsrc2=size=640x360:rate=30" -i "$GEN/noisy-two-speakers.wav" -t "$TOTAL" \
  -c:v libx264 -preset veryfast -crf 30 -pix_fmt yuv420p -g 60 -c:a aac -b:a 160k -ac 2 -shortest "${BITEXACT[@]}" "$GEN/noisy-two-speakers.mov"

# ---- a narration take on a hissy microphone, with thumps on "p" ---------------
NARR="Pick a sunny spot, prepare the soil, and plant the peppers deep. Pat the earth down, then water it well."
speak Karen "$NARR" "$WORK/narr.wav"
ND="$(dur "$WORK/narr.wav")"
"${FF[@]}" -i "$WORK/narr.wav" -f lavfi -i "anoisesrc=color=white:amplitude=0.02:seed=9:sample_rate=$SR" \
  -f lavfi -i "sine=frequency=45:sample_rate=$SR" \
  -filter_complex "[0:a]apad=pad_dur=0.6,adelay=400|400[v];[2:a]volume='if(lt(mod(t,1.9),0.06),0.35,0)':eval=frame[thump];[v][1:a][thump]amix=inputs=3:normalize=0:duration=first,alimiter=limit=0.95:level=disabled" \
  -ac 1 -ar $SR -c:a pcm_s16le "${BITEXACT[@]}" "$GEN/noisy-narration.wav"
printf '{\n  "file": "noisy-narration.wav",\n  "voice": "Karen",\n  "text": "%s"\n}\n' "$NARR" > "$TRUTH/sound-narration.json"

# ---- music at 120 bpm: a kick on every beat from 0.25 s over chords ------------
"${FF[@]}" -f lavfi -i "aevalsrc='0.7*sin(2*PI*55*t)*exp(-mod(t-0.25+10,0.5)*14)*gte(t,0.25)':s=44100:d=40" \
  -f lavfi -i "aevalsrc='0.08*(sin(2*PI*261.6*t)+sin(2*PI*329.6*t)+sin(2*PI*392*t))':s=44100:d=40" \
  -filter_complex "[0:a][1:a]amix=inputs=2:normalize=0,afade=t=in:d=0.05,alimiter=limit=0.9:level=disabled" -ac 2 -c:a aac -b:a 160k "${BITEXACT[@]}" "$GEN/music-beat.m4a"
printf '{\n  "file": "music-beat.m4a",\n  "bpm": 120,\n  "first_beat_s": 0.25,\n  "duration_s": 40\n}\n' > "$TRUTH/sound-music.json"

echo "$STAMP" > "$GEN/.stamp"
echo "sound fixtures in $GEN"
exit 0
