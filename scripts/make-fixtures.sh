#!/usr/bin/env bash
# make-fixtures.sh — generate MetaRush's deterministic synthetic test footage.
#
# Writes media into tests/fixtures/gen/ (gitignored) and ground truth into
# tests/fixtures/truth/ (committed). Everything is synthesized locally from
# fixed inputs: ffmpeg lavfi sources with fixed seeds, macOS `say` with fixed
# voices and a fixed speaking rate, and a built-in 5x7 bitmap font (drawn with
# drawbox, so no font files or drawtext/freetype support are needed).
#
# Usage:  scripts/make-fixtures.sh            # build if missing or out of date
#         FORCE=1 scripts/make-fixtures.sh    # rebuild everything
#
# Requirements: bash 3.2+, ffmpeg + ffprobe (with libx264 and the native aac
# encoder), awk, and macOS `say` (speech fixtures need it).
#
# Idempotent: the outputs are a pure function of this script, the selected
# voices, and the ffmpeg/say versions. A stamp file in gen/ records the script
# hash + voices; a rerun with the same stamp and all outputs present is a no-op.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
GEN="$ROOT/tests/fixtures/gen"
TRUTH="$ROOT/tests/fixtures/truth"
mkdir -p "$GEN" "$TRUTH"

FFMPEG="${FFMPEG:-$(command -v ffmpeg || true)}"; FFMPEG="${FFMPEG:-/usr/local/bin/ffmpeg}"
FFPROBE="${FFPROBE:-$(command -v ffprobe || true)}"; FFPROBE="${FFPROBE:-/usr/local/bin/ffprobe}"
[ -x "$FFMPEG" ] || { echo "ffmpeg not found" >&2; exit 1; }
[ -x "$FFPROBE" ] || { echo "ffprobe not found" >&2; exit 1; }
command -v say >/dev/null || { echo "macOS 'say' not found (speech fixtures need it)" >&2; exit 1; }

RATE_WPM=170          # fixed speaking rate for every voice
SR=48000              # sample rate for video fixtures
FF=("$FFMPEG" -hide_banner -loglevel error -nostdin -y)
# Strip version strings / metadata so reruns produce identical containers.
BITEXACT=(-fflags +bitexact -flags:v +bitexact -flags:a +bitexact -map_metadata -1)
# No VBV (-maxrate/-bufsize): x264 rate control with VBV + frame threads is not
# bit-reproducible across runs; plain CRF is.
X264=(-c:v libx264 -preset veryfast -crf 26 -pix_fmt yuv420p -g 60)
AAC=(-c:a aac -b:a 128k)

# ---------------------------------------------------------------- voices ----
# pick_voice <candidate>... — first installed voice, else "" (system default);
# "Samantha" also matches the "Samantha (English (US))" newer macOS lists.
. "$ROOT/scripts/lib/voices.sh"
VOICE_A="$(pick_voice Samantha "Flo (English (US))" "Shelley (English (US))" Kathy)"
VOICE_B="$(pick_voice Daniel "Reed (English (UK))" "Eddy (English (UK))" Rishi)"
VOICE_PHONE="$(pick_voice Karen Moira Tessa "Sandy (English (US))")"
VOICE_VFR="$(pick_voice Moira Tessa Karen "Grandpa (English (US))")"
# two-speakers needs two *different* voices: female US + male UK by default.
VOICE_S1="$(pick_voice Samantha "Flo (English (US))" Karen Moira)"
VOICE_S2="$(pick_voice Daniel "Reed (English (UK))" Fred Rishi)"
if [ -n "$VOICE_S1" ] && [ "$VOICE_S1" = "$VOICE_S2" ]; then VOICE_S2=""; fi
if [ -z "$VOICE_S1" ] || [ -z "$VOICE_S2" ]; then
  echo "warning: two-speakers.wav may not have two distinct voices (S1='$VOICE_S1' S2='$VOICE_S2')" >&2
fi
vlabel() { if [ -n "$1" ]; then printf '%s' "$1"; else printf 'system-default'; fi; }

# ------------------------------------------------------------ idempotence ---
OUTPUTS="interview-a.mov interview-b.mp4 broll-c.mp4 phone-rotated.mov vfr.mp4 music-bed.m4a two-speakers.wav not-a-video.mp4 long-silence.mp4 visual-a.mp4 visual-b.mp4 call-speaker-view.mp4"
IMAGES="$ROOT/tests/fixtures/images"
STAMP="$(
  { cat "${BASH_SOURCE[0]}" "$IMAGES"/*.jpg; echo "$VOICE_A|$VOICE_B|$VOICE_PHONE|$VOICE_VFR|$VOICE_S1|$VOICE_S2"; "$FFMPEG" -version | head -1; } \
  | shasum -a 256 | awk '{print $1}'
)"
if [ "${FORCE:-0}" != "1" ] && [ -f "$GEN/.stamp" ] && [ "$(cat "$GEN/.stamp")" = "$STAMP" ]; then
  missing=0; for f in $OUTPUTS; do [ -s "$GEN/$f" ] || missing=1; done
  if [ "$missing" = 0 ]; then echo "fixtures up to date in $GEN (FORCE=1 to rebuild)"; exit 0; fi
fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-fixtures.XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT

# ------------------------------------------------------------- helpers ------
# Exact duration in seconds of a PCM wav (duration_ts / sample_rate).
wav_dur() {
  "$FFPROBE" -v error -select_streams a:0 -show_entries stream=duration_ts,sample_rate \
    -of default=nw=1:nk=0 "$1" | awk -F= '/^sample_rate/{sr=$2} /^duration_ts/{d=$2} END{printf "%.6f", d/sr}'
}
fsum() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.6f", a+b}'; }

# speak <voice> <text> <out.wav> <rate> — TTS -> mono s16 wav at <rate> Hz,
# with leading/trailing silence trimmed so durations are speech bounds.
speak() {
  local voice="$1" text="$2" out="$3" rate="$4" aiff="$WORK/say.$$.$RANDOM.aiff"
  if [ -n "$voice" ]; then say -v "$voice" -r "$RATE_WPM" -o "$aiff" -- "$text"
  else say -r "$RATE_WPM" -o "$aiff" -- "$text"; fi
  "${FF[@]}" -i "$aiff" -af "aresample=$rate,aformat=sample_fmts=s16:channel_layouts=mono,\
silenceremove=start_periods=1:start_threshold=-55dB,areverse,\
silenceremove=start_periods=1:start_threshold=-55dB,areverse" \
    -c:a pcm_s16le "$out"
  rm -f "$aiff"
}

# silence <seconds> <out.wav> <rate>
silence() {
  "${FF[@]}" -f lavfi -i "aevalsrc=0:s=$3:d=$1" -af "aformat=sample_fmts=s16:channel_layouts=mono" -c:a pcm_s16le "$2"
}

# concat_wavs <out.wav> <in.wav>... (all same rate/format)
concat_wavs() {
  local out="$1"; shift
  local args=() fc="" i=0 f
  for f in "$@"; do args+=(-i "$f"); fc="$fc[$i:a]"; i=$((i+1)); done
  "${FF[@]}" "${args[@]}" -filter_complex "${fc}concat=n=$i:v=0:a=1[a]" -map "[a]" -c:a pcm_s16le "$out"
}

# 5x7 bitmap glyphs, 7 rows of 5 bits, top to bottom.
glyph() {
  case "$1" in
    A) echo "01110 10001 10001 11111 10001 10001 10001";; B) echo "11110 10001 10001 11110 10001 10001 11110";;
    C) echo "01110 10001 10000 10000 10000 10001 01110";; D) echo "11110 10001 10001 10001 10001 10001 11110";;
    E) echo "11111 10000 10000 11110 10000 10000 11111";; F) echo "11111 10000 10000 11110 10000 10000 10000";;
    G) echo "01110 10001 10000 10111 10001 10001 01111";; H) echo "10001 10001 10001 11111 10001 10001 10001";;
    I) echo "01110 00100 00100 00100 00100 00100 01110";; J) echo "00111 00010 00010 00010 00010 10010 01100";;
    K) echo "10001 10010 10100 11000 10100 10010 10001";;
    L) echo "10000 10000 10000 10000 10000 10000 11111";; M) echo "10001 11011 10101 10101 10001 10001 10001";;
    N) echo "10001 11001 10101 10011 10001 10001 10001";; O) echo "01110 10001 10001 10001 10001 10001 01110";;
    P) echo "11110 10001 10001 11110 10000 10000 10000";; R) echo "11110 10001 10001 11110 10100 10010 10001";;
    S) echo "01111 10000 10000 01110 00001 00001 11110";; T) echo "11111 00100 00100 00100 00100 00100 00100";;
    U) echo "10001 10001 10001 10001 10001 10001 01110";; V) echo "10001 10001 10001 10001 10001 01010 00100";;
    W) echo "10001 10001 10001 10101 10101 10101 01010";; Y) echo "10001 10001 01010 00100 00100 00100 00100";;
    X) echo "10001 10001 01010 00100 01010 10001 10001";;
    0) echo "01110 10001 10011 10101 11001 10001 01110";; 1) echo "00100 01100 00100 00100 00100 00100 01110";;
    2) echo "01110 10001 00001 00010 00100 01000 11111";; 3) echo "11110 00001 00001 01110 00001 00001 11110";;
    7) echo "11111 00001 00010 00100 01000 01000 01000";; 9) echo "01110 10001 10001 01111 00001 00010 01100";;
    .) echo "00000 00000 00000 00000 00000 01100 01100";; -) echo "00000 00000 00000 11111 00000 00000 00000";;
    *) echo "00000 00000 00000 00000 00000 00000 00000";;
  esac
}

# label <TEXT> <x> <y> <scale> <color> — drawbox filter chain rendering TEXT
# on a translucent plate. Horizontal runs of lit pixels are merged into one box.
label() {
  local text="$1" x0="$2" y0="$3" s="$4" color="$5"
  local n=${#text} pad=$(( s * 2 ))
  local out="drawbox=x=$((x0-pad)):y=$((y0-pad)):w=$(( n*6*s - s + 2*pad )):h=$(( 7*s + 2*pad )):color=black@0.55:t=fill"
  local i ch rows r row c start
  for ((i=0; i<n; i++)); do
    ch="${text:$i:1}"; rows=($(glyph "$ch")); r=0
    for row in "${rows[@]}"; do
      c=0
      while [ $c -lt 5 ]; do
        if [ "${row:$c:1}" = 1 ]; then
          start=$c
          while [ $c -lt 5 ] && [ "${row:$c:1}" = 1 ]; do c=$((c+1)); done
          out="$out,drawbox=x=$(( x0 + (i*6 + start)*s )):y=$(( y0 + r*s )):w=$(( (c-start)*s )):h=$s:color=$color:t=fill"
        else c=$((c+1)); fi
      done
      r=$((r+1))
    done
  done
  printf '%s' "$out"
}

json_escape() { printf '%s' "$1" | sed -e 's/\\/\\\\/g' -e 's/"/\\"/g'; }

echo "voices: A=$(vlabel "$VOICE_A") B=$(vlabel "$VOICE_B") phone=$(vlabel "$VOICE_PHONE") vfr=$(vlabel "$VOICE_VFR") s1=$(vlabel "$VOICE_S1") s2=$(vlabel "$VOICE_S2")"

# ========================================================== 1. interview-a ==
# 1920x1080 30 fps CFR, H.264 + AAC. One voice, 6 sentences, deliberate 1.5 s
# "vulnerable pause" between "...one simple question." and "What would you...".
A1="When I started building my first robot in my tiny garage, I had absolutely no idea what I was doing. I burned through three motors in a single weekend, and honestly, I almost quit. Then my neighbor leaned over the fence and asked me one simple question."
A2="What would you build if nobody was watching? That question changed everything for me. Today I teach kids how to build their own machines, and every single one of them fails first."
A_LEAD=0.50; A_PAUSE=1.50; A_TAIL=1.00
speak "$VOICE_A" "$A1" "$WORK/a1.wav" $SR
speak "$VOICE_A" "$A2" "$WORK/a2.wav" $SR
silence $A_LEAD "$WORK/a_lead.wav" $SR; silence $A_PAUSE "$WORK/a_pause.wav" $SR; silence $A_TAIL "$WORK/a_tail.wav" $SR
concat_wavs "$WORK/a.wav" "$WORK/a_lead.wav" "$WORK/a1.wav" "$WORK/a_pause.wav" "$WORK/a2.wav" "$WORK/a_tail.wav"
A1D="$(wav_dur "$WORK/a1.wav")"; A2D="$(wav_dur "$WORK/a2.wav")"; AD="$(wav_dur "$WORK/a.wav")"
A_PSTART="$(fsum $A_LEAD "$A1D")"; A_PEND="$(fsum "$A_PSTART" $A_PAUSE)"; A_SPEECH_END="$(fsum "$A_PEND" "$A2D")"
"${FF[@]}" -f lavfi -i "testsrc2=s=1920x1080:r=30" -f lavfi -i "color=c=0xFFB000:s=180x180:r=30" -i "$WORK/a.wav" \
  -filter_complex "[0:v][1:v]overlay=x='mod(t*140,W-w)':y='H*0.62+90*sin(t*0.8)':shortest=1,$(label "TAKE A" 120 90 24 white)[v]" \
  -map "[v]" -map 2:a -t "$AD" "${X264[@]}" -r 30 -fps_mode cfr -af "aresample=$SR,pan=stereo|c0=c0|c1=c0" "${AAC[@]}" \
  "${BITEXACT[@]}" "$GEN/interview-a.mov"
printf '%s %s\n' "$A1" "$A2" > "$TRUTH/interview-a.txt"
cat > "$TRUTH/interview-a.json" <<EOF
{
  "file": "interview-a.mov",
  "voice": "$(json_escape "$(vlabel "$VOICE_A")")",
  "rate_wpm": $RATE_WPM,
  "duration_s": $AD,
  "speech_start_s": $A_LEAD,
  "speech_end_s": $A_SPEECH_END,
  "pause_after_word": "question",
  "pause_before_word": "What",
  "pause_seconds": $A_PAUSE,
  "pause_start_s": $A_PSTART,
  "pause_end_s": $A_PEND,
  "hooks": [
    "What would you build if nobody was watching?",
    "Today I teach kids how to build their own machines, and every single one of them fails first.",
    "I burned through three motors in a single weekend, and honestly, I almost quit."
  ],
  "notes": "pause_start_s/pause_end_s are exact sample boundaries of inserted digital silence; TTS segments were trimmed at -55 dBFS, so the true acoustic gap is >= pause_seconds by a few ms of sub-threshold tail."
}
EOF

# ========================================================== 2. interview-b ==
# 1280x720 at 29.97 (30000/1001) CFR, different voice and script, ~20 s.
B="Most people think video editing is about cutting things out. I think it is really about finding the one moment that actually matters. The best interview I ever recorded lasted four hours, but the part everyone remembers is only nine seconds long. So listen for the pause, keep the laugh, and cut everything else."
speak "$VOICE_B" "$B" "$WORK/b1.wav" $SR
silence 0.40 "$WORK/b_lead.wav" $SR; silence 0.80 "$WORK/b_tail.wav" $SR
concat_wavs "$WORK/b.wav" "$WORK/b_lead.wav" "$WORK/b1.wav" "$WORK/b_tail.wav"
BD="$(wav_dur "$WORK/b.wav")"
# (mandelbrot, not gradients: gradients output differs run-to-run even with seed=)
"${FF[@]}" -f lavfi -i "mandelbrot=s=640x360:r=30000/1001" \
  -f lavfi -i "color=c=0x20E0FF:s=110x110:r=30000/1001" -i "$WORK/b.wav" \
  -filter_complex "[0:v]scale=1280:720:flags=bicubic[bg];[bg][1:v]overlay=x='W*0.5+380*sin(t*0.6)-w/2':y='H*0.7-60*cos(t*1.1)':shortest=1,$(label "TAKE B 29.97" 60 50 14 white)[v]" \
  -map "[v]" -map 2:a -t "$BD" "${X264[@]}" -r 30000/1001 -fps_mode cfr -af "aresample=$SR" "${AAC[@]}" \
  -movflags +faststart "${BITEXACT[@]}" "$GEN/interview-b.mp4"
printf '%s\n' "$B" > "$TRUTH/interview-b.txt"

# ============================================================== 3. broll-c ==
# Vertical 1080x1920, 30 fps, 10 s, no speech: quiet ambient pad + pink noise.
PAD="0.045*sin(2*PI*110*t)*(0.6+0.4*sin(2*PI*0.2*t))+0.035*sin(2*PI*164.81*t+0.7*sin(2*PI*0.5*t))+0.025*sin(2*PI*220.0*t)*(0.5+0.5*sin(2*PI*0.13*t))"
"${FF[@]}" -f lavfi -i "cellauto=s=270x480:rule=110:seed=42:r=30" -f lavfi -i "aevalsrc='$PAD':s=$SR:d=10" \
  -f lavfi -i "anoisesrc=color=pink:amplitude=0.006:seed=7:r=$SR:d=10" \
  -filter_complex "[0:v]scale=1080:1920:flags=neighbor,format=rgb24,colorchannelmixer=rr=0.25:gg=0.75:bb=1.0,format=yuv420p,$(label "BROLL C" 140 160 26 0xFFE070)[v];\
[1:a][2:a]amix=inputs=2:normalize=0,pan=stereo|c0=c0|c1=c0[a]" \
  -map "[v]" -map "[a]" -t 10 "${X264[@]}" -r 30 -fps_mode cfr "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" "$GEN/broll-c.mp4"

# ======================================================== 4. phone-rotated ==
# 1920x1080 coded frames + 90 degree display matrix (players show portrait).
PH="Quick test from my phone. Is this thing recording sideways?"
speak "$VOICE_PHONE" "$PH" "$WORK/ph1.wav" $SR
silence 0.50 "$WORK/ph_lead.wav" $SR
concat_wavs "$WORK/ph0.wav" "$WORK/ph_lead.wav" "$WORK/ph1.wav"
"${FF[@]}" -i "$WORK/ph0.wav" -af "apad=whole_dur=6" -c:a pcm_s16le "$WORK/ph.wav"
"${FF[@]}" -f lavfi -i "testsrc=s=1920x1080:r=30" -i "$WORK/ph.wav" \
  -filter_complex "[0:v]$(label "PHONE 90" 100 100 22 0xFF4060)[v]" \
  -map "[v]" -map 1:a -t 6 "${X264[@]}" -r 30 -fps_mode cfr -af "aresample=$SR" "${AAC[@]}" "${BITEXACT[@]}" "$WORK/phone-norot.mov"
"${FF[@]}" -display_rotation:v:0 90 -i "$WORK/phone-norot.mov" -map 0 -c copy "${BITEXACT[@]}" "$GEN/phone-rotated.mov"
printf '%s\n' "$PH" > "$TRUTH/phone-rotated.txt"

# ================================================================== 5. vfr ==
# Genuinely variable frame rate: 2 s @ 30 fps + 2 s @ 12 fps + 2 s @ 24 fps,
# concatenated with original timestamps preserved (no CFR re-timing).
VF="This clip changes its frame rate twice while I keep talking about the weather."
speak "$VOICE_VFR" "$VF" "$WORK/vf1.wav" $SR
silence 0.40 "$WORK/vf_lead.wav" $SR
concat_wavs "$WORK/vf0.wav" "$WORK/vf_lead.wav" "$WORK/vf1.wav"
"${FF[@]}" -i "$WORK/vf0.wav" -af "apad=whole_dur=6" -t 6 -c:a pcm_s16le "$WORK/vf.wav"
"${FF[@]}" -f lavfi -i "testsrc2=s=1280x720:r=30:d=2" -f lavfi -i "testsrc2=s=1280x720:r=12:d=2" \
  -f lavfi -i "testsrc2=s=1280x720:r=24:d=2" -i "$WORK/vf.wav" \
  -filter_complex "[0:v]settb=1/600[s0];[1:v]settb=1/600[s1];[2:v]settb=1/600[s2];\
[s0][s1][s2]concat=n=3:v=1:a=0,$(label "VFR" 60 60 16 0x40FF80)[v]" \
  -map "[v]" -map 3:a "${X264[@]}" -fps_mode passthrough -video_track_timescale 600 \
  -af "aresample=$SR" "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" "$GEN/vfr.mp4"
printf '%s\n' "$VF" > "$TRUTH/vfr.txt"

# ============================================================ 6. music-bed ==
# 30 s chord progression C - Am - F - G (2 s per chord) with a pulsing envelope.
C='mod(floor(t/2),4)'
R="if(eq($C,0),261.63,if(eq($C,1),220.00,if(eq($C,2),174.61,196.00)))"
T3="if(eq($C,0),329.63,if(eq($C,1),261.63,if(eq($C,2),220.00,246.94)))"
F5="if(eq($C,0),392.00,if(eq($C,1),329.63,if(eq($C,2),261.63,293.66)))"
ENV="(0.55+0.45*exp(-3*mod(t,0.5)))"
L="$ENV*(0.09*sin(2*PI*$R*t)+0.07*sin(2*PI*$T3*t)+0.06*sin(2*PI*$F5*t)+0.10*sin(PI*$R*t))"
RR="$ENV*(0.09*sin(2*PI*$R*t+0.3)+0.07*sin(2*PI*$T3*t+0.6)+0.06*sin(2*PI*$F5*t+0.9)+0.10*sin(PI*$R*t))"
"${FF[@]}" -f lavfi -i "aevalsrc='$L|$RR':s=44100:d=30" -af "afade=t=in:d=0.5,afade=t=out:st=28.5:d=1.5" \
  "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" "$GEN/music-bed.m4a"

# ========================================================= 7. two-speakers ==
# 16 kHz mono wav, 8 alternating turns between two clearly different voices.
# Turn start/end come from the exact sample lengths of the concatenated parts.
TS_SR=16000; TS_GAP=0.60; TS_LEAD=0.50
TURNS=(
  "S1|So tell me how the project actually started."
  "S2|It started in my garage with a broken drone and a lot of free time."
  "S1|And when did you realize it could become a real business?"
  "S2|Honestly, when strangers started emailing me to ask if they could buy one."
  "S1|What was the hardest part of that first year?"
  "S2|Learning to say no to good ideas so we could finish the great ones."
  "S1|That sounds like advice everyone needs to hear."
  "S2|It is the only advice I really trust."
)
silence $TS_LEAD "$WORK/ts_lead.wav" $TS_SR; silence $TS_GAP "$WORK/ts_gap.wav" $TS_SR; silence 0.80 "$WORK/ts_tail.wav" $TS_SR
parts=("$WORK/ts_lead.wav"); t="$TS_LEAD"; json_turns=""; k=0
for turn in "${TURNS[@]}"; do
  spk="${turn%%|*}"; text="${turn#*|}"
  if [ "$spk" = S1 ]; then v="$VOICE_S1"; else v="$VOICE_S2"; fi
  speak "$v" "$text" "$WORK/ts_$k.wav" $TS_SR
  d="$(wav_dur "$WORK/ts_$k.wav")"; end="$(fsum "$t" "$d")"
  [ $k -gt 0 ] && json_turns="$json_turns,"
  json_turns="$json_turns
    {\"speaker\": \"$spk\", \"voice\": \"$(json_escape "$(vlabel "$v")")\", \"start_s\": $t, \"end_s\": $end, \"text\": \"$(json_escape "$text")\"}"
  parts+=("$WORK/ts_$k.wav")
  if [ $k -lt $(( ${#TURNS[@]} - 1 )) ]; then parts+=("$WORK/ts_gap.wav"); t="$(fsum "$end" $TS_GAP)"; else t="$end"; fi
  k=$((k+1))
done
parts+=("$WORK/ts_tail.wav")
concat_wavs "$WORK/ts.wav" "${parts[@]}"
"${FF[@]}" -i "$WORK/ts.wav" -c:a pcm_s16le -ar $TS_SR -ac 1 "${BITEXACT[@]}" "$GEN/two-speakers.wav"
cat > "$TRUTH/two-speakers.json" <<EOF
{
  "file": "two-speakers.wav",
  "sample_rate": $TS_SR,
  "duration_s": $(wav_dur "$GEN/two-speakers.wav"),
  "gap_between_turns_s": $TS_GAP,
  "turns": [$json_turns
  ]
}
EOF

# =========================================================== 8. not-a-video ==
cat > "$GEN/not-a-video.mp4" <<'EOF'
This is not a video. It is a plain UTF-8 text file with an .mp4 extension,
used to test that MetaRush sniffs container contents instead of trusting
file extensions. ffprobe should fail to find any streams in it.
EOF

# ========================================================== 9. long-silence ==
# 1280x720, 8 s, video with a digitally silent (all-zero) stereo AAC track.
"${FF[@]}" -f lavfi -i "color=c=0x303848:s=1280x720:r=30" -f lavfi -i "color=c=0x8090A0:s=90x90:r=30" \
  -f lavfi -i "anullsrc=r=$SR:cl=stereo" \
  -filter_complex "[0:v][1:v]overlay=x='mod(t*100,W-w)':y='H/2-h/2':shortest=1,$(label "SILENCE" 60 60 14 white)[v]" \
  -map "[v]" -map 2:a -t 8 "${X264[@]}" -r 30 -fps_mode cfr "${AAC[@]}" -movflags +faststart "${BITEXACT[@]}" "$GEN/long-silence.mp4"

# ============================================================ 10. visual-a ==
# 1280x720, 30 fps, H.264 + AAC 48 kHz stereo, 30 s, built from the public-domain
# stills in tests/fixtures/images (SOURCES.md) for the visual-change detector and
# the on-screen index. Five shots joined by hard cuts at 6, 14, 21 and 25 s:
#   0–6   a talking head: a close-up portrait that sways two pixels, with grain, while
#         one voice speaks (no visual event may be found here);
#   6–14  a slow pan across a beach (90 px/s);
#   14–21 a still room; at 16–17.5 s a person slides in from the right and stays;
#   21–25 a green EXIT sign (on-screen text);
#   25–30 a dog.
V_SPEECH="Welcome back. Today we walk down to the beach and meet the dog."
speak "$VOICE_A" "$V_SPEECH" "$WORK/v1.wav" $SR
silence 0.50 "$WORK/v_lead.wav" $SR
concat_wavs "$WORK/v0.wav" "$WORK/v_lead.wav" "$WORK/v1.wav"
"${FF[@]}" -i "$WORK/v0.wav" -af "apad=whole_dur=30" -t 30 -c:a pcm_s16le "$WORK/v.wav"
V_SPEECH_END="$(fsum 0.50 "$(wav_dur "$WORK/v1.wav")")"
"${FF[@]}" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/portrait.jpg" \
  -loop 1 -framerate 30 -t 8 -i "$IMAGES/beach.jpg" \
  -loop 1 -framerate 30 -t 7 -i "$IMAGES/person.jpg" \
  -loop 1 -framerate 30 -t 5 -i "$IMAGES/dog.jpg" \
  -f lavfi -i "color=c=0x2b2622:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x5d6b7d:s=1280x720:r=30:d=7" \
  -f lavfi -i "color=c=0x161c16:s=1280x720:r=30:d=4" \
  -f lavfi -i "color=c=0x3a3a30:s=1280x720:r=30:d=5" \
  -i "$WORK/v.wav" \
  -filter_complex "\
[0:v]crop=500:420:0:20,scale=858:720,setsar=1[face];\
[4:v][face]overlay=x='211+2*sin(2*PI*t*0.7)':y=0:shortest=1,noise=alls=6:allf=t,format=yuv420p[s1];\
[1:v]scale=-2:720,crop=1280:720:x='200+t*90':y=0,setsar=1,noise=alls=4:allf=t,format=yuv420p[s2];\
[5:v]drawbox=x=120:y=90:w=300:h=560:color=0x404a58:t=fill,drawbox=x=880:y=140:w=260:h=200:color=0x9fb4c8:t=fill,drawbox=x=880:y=140:w=260:h=200:color=0x2a3038:t=8[room];\
[2:v]scale=-2:600,setsar=1[who];\
[room][who]overlay=x='if(lt(t,2),1280,if(lt(t,3.5),1280-(t-2)*(1280-700)/1.5,700))':y=120:shortest=1,noise=alls=4:allf=t,format=yuv420p[s3];\
[6:v]$(label "EXIT" 420 260 28 0x30FF60),noise=alls=4:allf=t,format=yuv420p[s4];\
[3:v]scale=-2:720,setsar=1[dog];\
[7:v][dog]overlay=x=(W-w)/2:y=0:shortest=1,noise=alls=4:allf=t,format=yuv420p[s5];\
[s1][s2][s3][s4][s5]concat=n=5:v=1:a=0[v]" \
  -map "[v]" -map 8:a -t 30 "${X264[@]}" -r 30 -fps_mode cfr -af "aresample=$SR,pan=stereo|c0=c0|c1=c0" "${AAC[@]}" \
  -movflags +faststart "${BITEXACT[@]}" "$GEN/visual-a.mp4"
printf '%s\n' "$V_SPEECH" > "$TRUTH/visual-a.txt"
cat > "$TRUTH/visual-a.json" <<EOF
{
  "file": "visual-a.mp4",
  "duration_s": 30,
  "voice": "$(json_escape "$(vlabel "$VOICE_A")")",
  "speech_start_s": 0.50,
  "speech_end_s": $V_SPEECH_END,
  "cuts_s": [6, 14, 21, 25],
  "pan_s": [6, 14],
  "enter_s": [16, 17.5],
  "quiet_s": [[0.3, 5.7], [18.3, 20.7], [21.3, 24.7], [25.3, 29.7]],
  "talking_head_s": [0, 6],
  "faces_s": [[0, 6], [16, 21]],
  "text": {"EXIT": [21, 25]},
  "animals": {"dog": [25, 30]},
  "images": "tests/fixtures/images (public domain / CC0, see SOURCES.md)",
  "notes": "Hard cuts at cuts_s; pan_s is a 90 px/s pan (gradual change); enter_s is when a person slides in from the right and stays; quiet_s must have no visual events (the 0-6 s talking head sways 2 px with grain)."
}
EOF

# ============================================================ 11. visual-b ==
# 1280x720, 30 fps, H.264 + AAC 48 kHz stereo, 24 s, from the public-domain
# stills in tests/fixtures/images, for recognizing faces across clips (D114),
# framing that follows movement (D115) and the on-screen quality checks (D116).
# Four shots joined by hard cuts at 8, 14 and 20 s:
#   0–8   the person of portrait.jpg (another photo of her, portrait-b.jpg)
#         walks from the left of the room to the right (90 px/s) while she talks;
#   8–14  the person of person.jpg (another photo, person-b.jpg) stands at the
#         far right edge, and a sign at the bottom reads SALE ENDS SUNDAY;
#   14–20 she again, low in the picture (her face in the bottom third);
#   20–24 someone else (portrait-c.jpg), never seen in visual-a.
VB1="Hi again. I walk from one side of the room to the other while I talk to you."
VB2="The sign at the bottom says the sale ends on Sunday."
VB3="Here my face sits low in the picture."
VB4="And this is someone else entirely."
speak "$VOICE_A" "$VB1" "$WORK/vb1.wav" $SR
speak "$VOICE_A" "$VB2" "$WORK/vb2.wav" $SR
speak "$VOICE_A" "$VB3" "$WORK/vb3.wav" $SR
speak "$VOICE_A" "$VB4" "$WORK/vb4.wav" $SR
VB_S1=0.50; VB_S2=8.50; VB_S3=14.50; VB_S4=20.50
VB_E1="$(fsum $VB_S1 "$(wav_dur "$WORK/vb1.wav")")"
VB_E2="$(fsum $VB_S2 "$(wav_dur "$WORK/vb2.wav")")"
VB_E3="$(fsum $VB_S3 "$(wav_dur "$WORK/vb3.wav")")"
VB_E4="$(fsum $VB_S4 "$(wav_dur "$WORK/vb4.wav")")"
gap() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.6f", a-b}'; }
silence $VB_S1 "$WORK/vbg0.wav" $SR
silence "$(gap $VB_S2 "$VB_E1")" "$WORK/vbg1.wav" $SR
silence "$(gap $VB_S3 "$VB_E2")" "$WORK/vbg2.wav" $SR
silence "$(gap $VB_S4 "$VB_E3")" "$WORK/vbg3.wav" $SR
concat_wavs "$WORK/vb0.wav" "$WORK/vbg0.wav" "$WORK/vb1.wav" "$WORK/vbg1.wav" "$WORK/vb2.wav" "$WORK/vbg2.wav" "$WORK/vb3.wav" "$WORK/vbg3.wav" "$WORK/vb4.wav"
"${FF[@]}" -i "$WORK/vb0.wav" -af "apad=whole_dur=24" -t 24 -c:a pcm_s16le "$WORK/vb.wav"
"${FF[@]}" \
  -loop 1 -framerate 30 -t 8 -i "$IMAGES/portrait-b.jpg" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/person-b.jpg" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/portrait-b.jpg" \
  -loop 1 -framerate 30 -t 4 -i "$IMAGES/portrait-c.jpg" \
  -f lavfi -i "color=c=0x2b2622:s=1280x720:r=30:d=8" \
  -f lavfi -i "color=c=0x5d6b7d:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x3a3a30:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x161c16:s=1280x720:r=30:d=4" \
  -i "$WORK/vb.wav" \
  -filter_complex "\
[0:v]crop=480:420:0:40,scale=-2:480,setsar=1[walker];\
[4:v][walker]overlay=x='40+t*90':y=120:shortest=1,noise=alls=5:allf=t,format=yuv420p[b1];\
[1:v]crop=480:360:0:20,scale=-2:520,setsar=1[edge];\
[5:v][edge]overlay=x=850:y=40:shortest=1,$(label "SALE ENDS SUNDAY" 352 630 6 white),noise=alls=4:allf=t,format=yuv420p[b2];\
[2:v]crop=480:420:0:40,scale=-2:360,setsar=1[low];\
[6:v][low]overlay=x=435:y=420:shortest=1,noise=alls=4:allf=t,format=yuv420p[b3];\
[3:v]crop=480:480:0:0,scale=-2:600,setsar=1[other];\
[7:v][other]overlay=x=340:y=60:shortest=1,noise=alls=4:allf=t,format=yuv420p[b4];\
[b1][b2][b3][b4]concat=n=4:v=1:a=0[v]" \
  -map "[v]" -map 8:a -t 24 "${X264[@]}" -r 30 -fps_mode cfr -af "aresample=$SR,pan=stereo|c0=c0|c1=c0" "${AAC[@]}" \
  -movflags +faststart "${BITEXACT[@]}" "$GEN/visual-b.mp4"
printf '%s\n%s\n%s\n%s\n' "$VB1" "$VB2" "$VB3" "$VB4" > "$TRUTH/visual-b.txt"
cat > "$TRUTH/visual-b.json" <<EOF
{
  "file": "visual-b.mp4",
  "duration_s": 24,
  "voice": "$(json_escape "$(vlabel "$VOICE_A")")",
  "speech_s": [[$VB_S1, $VB_E1], [$VB_S2, $VB_E2], [$VB_S3, $VB_E3], [$VB_S4, $VB_E4]],
  "cuts_s": [8, 14, 20],
  "walk_s": [0, 8],
  "walk_face_x": [0.21, 0.77],
  "same_face_as_visual_a": {"talking_head": [[0, 8], [14, 20]], "person_walking_in": [[8, 14]]},
  "stranger_s": [20, 24],
  "edge_face_x": [0.89, 0.99],
  "low_face_y": [0.65, 0.93],
  "text": {"SALE ENDS SUNDAY": [8, 14]},
  "text_box": [0.275, 0.875, 0.45, 0.06],
  "images": "tests/fixtures/images (public domain, see SOURCES.md)",
  "notes": "A face walks left to right at 0-8 s (a vertical crop must follow it); at 8-14 s a face stands at the far right edge (a middle crop cuts it off) above a sign at the bottom (under bottom captions); at 14-20 s a face sits in the bottom third (captions and lower thirds cover it); 20-24 s is a face never seen in visual-a. The faces at 0-8 and 14-20 s are the talking head of visual-a; the face at 8-14 s is the person who walks in there."
}
EOF

# ================================================== 12. call-speaker-view ==
# 1280x720, 30 fps, H.264 + AAC 48 kHz stereo, 24 s: one recording of a video
# call in "speaker view" (the picture flips to whoever talks), for the picture
# switches found to the frame, the flashes at clip edges and the names a call
# app prints in the picture (D160–D162). Four shots joined by hard cuts at 6, 12
# and 18 s: Lena (portrait-b.jpg) 0–6 and 12–18, Maya (person-b.jpg) 6–12
# and 18–24, each with the call app's name tile at the bottom left ("LENA O",
# "MAYA L") and saying one sentence (two different voices).
CA1="Thanks for having me. I help out at the garden every week."
CB1="Tell us what a usual day looks like."
CA2="Most days I water the beds and fix the fences."
CB2="That is a lot of work, and it matters."
i=0
for line in "$VOICE_S1|$CA1" "$VOICE_S2|$CB1" "$VOICE_S1|$CA2" "$VOICE_S2|$CB2"; do
  i=$((i+1))
  speak "${line%%|*}" "${line#*|}" "$WORK/cv$i.wav" $SR
  silence 0.30 "$WORK/cvl$i.wav" $SR
  concat_wavs "$WORK/cvs$i.wav" "$WORK/cvl$i.wav" "$WORK/cv$i.wav"
  "${FF[@]}" -i "$WORK/cvs$i.wav" -af "apad=whole_dur=6" -t 6 -c:a pcm_s16le "$WORK/cvp$i.wav"
done
concat_wavs "$WORK/cv.wav" "$WORK/cvp1.wav" "$WORK/cvp2.wav" "$WORK/cvp3.wav" "$WORK/cvp4.wav"
"${FF[@]}" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/portrait-b.jpg" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/person-b.jpg" \
  -f lavfi -i "color=c=0x23262b:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x2e3a2f:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x23262b:s=1280x720:r=30:d=6" \
  -f lavfi -i "color=c=0x2e3a2f:s=1280x720:r=30:d=6" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/portrait-b.jpg" \
  -loop 1 -framerate 30 -t 6 -i "$IMAGES/person-b.jpg" \
  -i "$WORK/cv.wav" \
  -filter_complex "\
[0:v]crop=480:420:0:40,scale=-2:600,setsar=1[a1];\
[2:v][a1]overlay=x=(W-w)/2:y=40:shortest=1,$(label "LENA O" 28 660 5 white),noise=alls=4:allf=t,format=yuv420p[c1];\
[1:v]crop=480:360:0:20,scale=-2:600,setsar=1[b1];\
[3:v][b1]overlay=x=(W-w)/2:y=40:shortest=1,$(label "MAYA L" 28 660 5 white),noise=alls=4:allf=t,format=yuv420p[c2];\
[6:v]crop=480:420:0:40,scale=-2:600,setsar=1[a2];\
[4:v][a2]overlay=x=(W-w)/2:y=40:shortest=1,$(label "LENA O" 28 660 5 white),noise=alls=4:allf=t,format=yuv420p[c3];\
[7:v]crop=480:360:0:20,scale=-2:600,setsar=1[b2];\
[5:v][b2]overlay=x=(W-w)/2:y=40:shortest=1,$(label "MAYA L" 28 660 5 white),noise=alls=4:allf=t,format=yuv420p[c4];\
[c1][c2][c3][c4]concat=n=4:v=1:a=0[v]" \
  -map "[v]" -map 8:a -t 24 "${X264[@]}" -r 30 -fps_mode cfr -af "aresample=$SR,pan=stereo|c0=c0|c1=c0" "${AAC[@]}" \
  -movflags +faststart "${BITEXACT[@]}" "$GEN/call-speaker-view.mp4"
printf '%s\n%s\n%s\n%s\n' "$CA1" "$CB1" "$CA2" "$CB2" > "$TRUTH/call-speaker-view.txt"
cat > "$TRUTH/call-speaker-view.json" <<EOF
{
  "file": "call-speaker-view.mp4",
  "duration_s": 24,
  "fps": 30,
  "voices": ["$(json_escape "$(vlabel "$VOICE_S1")")", "$(json_escape "$(vlabel "$VOICE_S2")")"],
  "switches_s": [6, 12, 18],
  "on_screen": [["LENA O", 0, 6], ["MAYA L", 6, 12], ["LENA O", 12, 18], ["MAYA L", 18, 24]],
  "printed_names": {"corner": "bottom-left", "box": [0.014, 0.903, 0.199, 0.076]},
  "images": "tests/fixtures/images (public domain, see SOURCES.md)",
  "notes": "Speaker view: the picture switches at exactly 6, 12 and 18 s (frames 180, 360, 540); each shot shows the call app's name tile at the bottom left. A clip from 5.8 s opens with a split-second (6 frames) of Lena before Maya appears; a clip ending at 18.25 s ends with a split-second of Maya."
}
EOF

echo "$STAMP" > "$GEN/.stamp"
echo "wrote fixtures to $GEN:"
( cd "$GEN" && ls -l $OUTPUTS | awk '{printf "  %-20s %9d bytes\n", $9, $5}' )
du -sh "$GEN" | awk '{print "total: " $1}'
exit 0
