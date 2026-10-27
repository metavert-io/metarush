#!/usr/bin/env bash
# make-conversation-fixtures.sh — a synthetic online call for conversations
# (D99–D103): two people recorded on separate machines, without headphones.
# Writes media into tests/fixtures/gen/conversation/ (gitignored) and the
# ground truth into tests/fixtures/truth/conversation.json (committed).
#
#   scripts/make-conversation-fixtures.sh            # build if missing or out of date
#   FORCE=1 scripts/make-conversation-fixtures.sh    # rebuild everything
#
# Maya (Samantha's voice) hosts; Sam (Daniel's voice) answers. Each person's
# microphone picks up the other from their speakers (22 dB down, 0.18 s late:
# the call's delay), over its own room noise. Outputs:
#   maya.mov      1280×720 webcam (warm, bright) with her microphone (PCM),
#                 starting at the conversation's 0:00
#   sam.mov       1280×720 webcam (cool, dark, a name label in the corner) with
#                 his microphone; it starts 1.234 s into the conversation and
#                 its clock runs 200 ppm fast; his picture freezes for 2 s and
#                 his sound drops out for 0.35 s (a bad connection)
#   gallery.mp4   a gallery view (both tiles, name labels, a recording banner)
#                 with the call's mixed sound as heard at Maya's machine
#                 (AAC), starting 0.5 s into the conversation
#   zoom/<date time topic>/   a Zoom local recording with separate audio per
#                 participant: video<n>.mp4, audio<n>.m4a, recording.conf and
#                 Audio Record/audio<Name><i><n>.m4a, all starting together
#   riverside/    riverside_<name>_raw-video-cfr_garden_show_000N.mp4 per person
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
GEN="$ROOT/tests/fixtures/gen/conversation"
TRUTH="$ROOT/tests/fixtures/truth"
mkdir -p "$GEN" "$TRUTH"
FFMPEG="${FFMPEG:-$(command -v ffmpeg || true)}"; FFMPEG="${FFMPEG:-/usr/local/bin/ffmpeg}"
[ -x "$FFMPEG" ] || { echo "ffmpeg not found" >&2; exit 1; }
command -v say >/dev/null || { echo "macOS 'say' not found (speech fixtures need it)" >&2; exit 1; }
MAGICK="${MAGICK:-$(command -v magick || true)}"
if [ -z "$MAGICK" ] || [ ! -x "$MAGICK" ]; then
  echo "ImageMagick isn't installed, and the call's pictures are drawn with it: without it the two people and their" >&2
  echo "name labels would be plain boxes, and the checks of faces and names in the call would find nothing." >&2
  echo "Install it (brew install imagemagick) and run this again." >&2
  exit 1
fi
FF=("$FFMPEG" -hide_banner -loglevel error -nostdin -y)
BITEXACT=(-fflags +bitexact -flags:v +bitexact -flags:a +bitexact -map_metadata -1)
RATE_WPM=175
SR=48000
SAM_START=1.234
SAM_RATE=48010       # his machine's 48 kHz runs at 48010: his clock is 208.333 ppm fast
SAM_DRIFT=208.333
GALLERY_START=0.5
DELAY=0.18           # the call's delay each way
BLEED=0.08           # the other voice in each microphone (-22 dB)
ZOOM_DIR="2026-10-19 10.00.00 Garden Show"
ZOOM_N=1123456789

OUTPUTS="maya.mov sam.mov gallery.mp4 zoom/$ZOOM_DIR/video$ZOOM_N.mp4 zoom/$ZOOM_DIR/Audio Record/audioSam2$ZOOM_N.m4a riverside/riverside_sam_raw-video-cfr_garden_show_0002.mp4"
STAMP="$( { cat "${BASH_SOURCE[0]}"; "$FFMPEG" -version | head -1; say -v '?' | grep -E '^(Samantha|Daniel) ' ; { [ -n "$MAGICK" ] && echo magick; } || true; } | shasum -a 256 | awk '{print $1}')"
if [ "${FORCE:-0}" != "1" ] && [ -f "$GEN/.stamp" ] && [ "$(cat "$GEN/.stamp")" = "$STAMP" ]; then
  missing=0; for f in maya.mov sam.mov gallery.mp4 long/sam.wav; do [ -s "$GEN/$f" ] || missing=1; done
  [ -s "$GEN/zoom/$ZOOM_DIR/Audio Record/audioSam2$ZOOM_N.m4a" ] || missing=1
  if [ "$missing" = 0 ]; then echo "conversation fixtures up to date in $GEN (FORCE=1 to rebuild)"; exit 0; fi
fi
rm -rf "$GEN/zoom" "$GEN/riverside"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-conversation-fixtures.XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT

speak() {
  say -v "$1" -r "$RATE_WPM" -o "$WORK/s.aiff" "$2"
  "${FF[@]}" -i "$WORK/s.aiff" -af "aresample=$SR,silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse,silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse" -ac 1 -c:a pcm_s16le "$3"
}
dur() { ffprobe -v error -show_entries format=duration -of csv=p=0 "$1"; }
add() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.3f", a+b}'; }

# ---- the dialogue on the conversation's clock -------------------------------
# kind: say (a turn), over (said while the other talks, at the given offset into
# the previous turn), with (at the same moment as the previous line), gap (silence).
LINES=(
  "say|M|0.6|Hi Sam, can you hear me okay?"
  "say|S|0.5|Yes, I can hear you now. Sorry, I was on mute."
  "say|M|0.7|Great. Welcome to the garden show. Today we are talking about tomatoes on a balcony."
  "say|S|0.6|Thanks for having me. I have been growing them on my balcony for ten years. The trick is a deep pot, good soil, and a lot of sun. Most people water too much, and the roots never grow deep."
  "over|M|6.0|Mm-hmm."
  "say|M|0.6|What surprised you the most?"
  "say|S|0.5|That cherry tomatoes grow almost anywhere, even in a bucket."
  "say|M|0.3|Ha ha ha."
  "with|S|0|Ha ha ha."
  "say|M|3.8|Let us look at your setup."
  "say|S|0.6|Sure. This is my small garden on the roof. I built the boxes myself from old pallets."
  "say|M|0.6|That is wonderful. Thanks for joining us today."
  "say|S|0.5|Thank you. It was a pleasure."
)
JSON="$TRUTH/conversation.json"
T=0; PREV_START=0; PREV_END=0; i=0
TURNS_JSON=""
M_INPUTS=(); M_FILTER=""; S_INPUTS=(); S_FILTER=""; nm=0; ns=0
LAST_END=0
for line in "${LINES[@]}"; do
  IFS='|' read -r kind who gap text <<< "$line"
  voice=Samantha; [ "$who" = S ] && voice=Daniel
  speak "$voice" "$text" "$WORK/l$i.wav"
  d="$(dur "$WORK/l$i.wav")"
  case "$kind" in
    say)  start="$(add "$LAST_END" "$gap")" ;;
    over) start="$(add "$PREV_START" "$gap")" ;;
    with) start="$PREV_START" ;;
  esac
  end="$(add "$start" "$d")"
  name=Maya; [ "$who" = S ] && name=Sam
  [ -n "$TURNS_JSON" ] && TURNS_JSON="$TURNS_JSON,"$'\n'
  TURNS_JSON="$TURNS_JSON    {\"speaker\": \"$name\", \"kind\": \"$kind\", \"start_s\": $start, \"end_s\": $end, \"text\": \"$text\"}"
  ms="$(awk -v s="$start" 'BEGIN{printf "%d", s*1000+0.5}')"
  if [ "$who" = M ]; then
    M_INPUTS+=(-i "$WORK/l$i.wav"); M_FILTER="$M_FILTER[$nm:a]adelay=$ms|$ms[m$nm];"; nm=$((nm+1))
  else
    S_INPUTS+=(-i "$WORK/l$i.wav"); S_FILTER="$S_FILTER[$ns:a]adelay=$ms|$ms[s$ns];"; ns=$((ns+1))
  fi
  if [ "$kind" = say ]; then PREV_START="$start"; fi
  LAST_END="$(awk -v a="$LAST_END" -v b="$end" 'BEGIN{print (b>a)?b:a}')"
  i=$((i+1))
done
TOTAL="$(add "$LAST_END" 1.5)"
mixlist() { local n=$1 p=$2 out=""; for ((k=0;k<n;k++)); do out="$out[$p$k]"; done; echo "$out"; }
"${FF[@]}" "${M_INPUTS[@]}" -filter_complex "${M_FILTER}$(mixlist $nm m)amix=inputs=$nm:normalize=0:duration=longest,apad=whole_dur=$TOTAL,atrim=0:$TOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/m_own.wav"
"${FF[@]}" "${S_INPUTS[@]}" -filter_complex "${S_FILTER}$(mixlist $ns s)amix=inputs=$ns:normalize=0:duration=longest,apad=whole_dur=$TOTAL,atrim=0:$TOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/s_own.wav"

# Where things happen (conversation clock), for the clean-up checks.
DROP_AT="$(echo "$TURNS_JSON" | awk -F'"start_s": |, "end_s": ' '/"Sam".*boxes/{printf "%.3f", $2+2.0}')"
FREEZE_AT=30.0
DELAY_MS="$(awk -v d="$DELAY" 'BEGIN{printf "%d", d*1000}')"

# Each microphone: its own voice, the other's from the speakers, room noise.
mic() { # <own> <other> <seed> <out> [dropout-at]
  local drop=""
  # (sample-exact: a volume filter's enable switches per audio frame)
  if [ -n "${5:-}" ]; then drop=",aeval=exprs='if(between(t,$5,$(add "$5" 0.35)),0,val(0))':c=same"; fi
  "${FF[@]}" -i "$1" -i "$2" -f lavfi -i "anoisesrc=color=pink:amplitude=0.004:seed=$3:sample_rate=$SR" \
    -filter_complex "[1:a]adelay=$DELAY_MS|$DELAY_MS,volume=$BLEED,lowpass=f=5000[bleed];[0:a][bleed][2:a]amix=inputs=3:normalize=0:duration=first$drop,atrim=0:$TOTAL" \
    -ac 1 -ar $SR -c:a pcm_s16le "$4"
}
mic "$WORK/m_own.wav" "$WORK/s_own.wav" 3 "$WORK/m_mic.wav"
mic "$WORK/s_own.wav" "$WORK/m_own.wav" 4 "$WORK/s_mic.wav" "$DROP_AT"
# Sam's recording: from 1.234 s on, on a clock 208.333 ppm fast (stretched
# exactly: resampled to 48010 Hz and read back as 48000).
STRETCH="$(awk -v r="$SAM_RATE" -v s="$SR" 'BEGIN{printf "%.9f", r/s}')"
samfile() { "${FF[@]}" -i "$1" -af "atrim=start=$SAM_START,asetpts=PTS-STARTPTS,aresample=$SAM_RATE,asetrate=$SR" -ac 1 -c:a pcm_s16le "$2"; }
samfile "$WORK/s_mic.wav" "$WORK/sam_file.wav"
# The call as heard at Maya's machine: her voice as she spoke, Sam's late.
"${FF[@]}" -i "$WORK/m_own.wav" -i "$WORK/s_own.wav" -f lavfi -i "anoisesrc=color=pink:amplitude=0.002:seed=6:sample_rate=$SR" \
  -filter_complex "[1:a]adelay=$DELAY_MS|$DELAY_MS[s];[0:a][s][2:a]amix=inputs=3:normalize=0:duration=first,atrim=0:$TOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/mix.wav"

# ---- a long version for drift: the dialogue six times, paced differently -----
LONG="$GEN/long"; mkdir -p "$LONG"
LM_IN=(); LM_F=""; LS_IN=(); LS_F=""; lm=0; ls=0; LEND=0
for scale in 1.0 1.35 0.8 1.2 0.9 1.5; do
  PSTART=0; i=0
  base="$(add "$LEND" 2.0)"; LE="$base"
  for line in "${LINES[@]}"; do
    IFS='|' read -r kind who gap text <<< "$line"
    d="$(dur "$WORK/l$i.wav")"
    g="$(awk -v g="$gap" -v s="$scale" 'BEGIN{printf "%.3f", g*s}')"
    case "$kind" in
      say)  st="$(add "$LE" "$g")" ;;
      over) st="$(add "$PSTART" "$gap")" ;;
      with) st="$PSTART" ;;
    esac
    en="$(add "$st" "$d")"
    ms="$(awk -v s="$st" 'BEGIN{printf "%d", s*1000+0.5}')"
    if [ "$who" = M ]; then LM_IN+=(-i "$WORK/l$i.wav"); LM_F="$LM_F[$lm:a]adelay=$ms|$ms[m$lm];"; lm=$((lm+1)); else LS_IN+=(-i "$WORK/l$i.wav"); LS_F="$LS_F[$ls:a]adelay=$ms|$ms[s$ls];"; ls=$((ls+1)); fi
    if [ "$kind" = say ]; then PSTART="$st"; fi
    LE="$(awk -v a="$LE" -v b="$en" 'BEGIN{print (b>a)?b:a}')"
    i=$((i+1))
  done
  LEND="$LE"
done
LTOTAL="$(add "$LEND" 1.5)"
"${FF[@]}" "${LM_IN[@]}" -filter_complex "${LM_F}$(mixlist $lm m)amix=inputs=$lm:normalize=0:duration=longest,apad=whole_dur=$LTOTAL,atrim=0:$LTOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/lm_own.wav"
"${FF[@]}" "${LS_IN[@]}" -filter_complex "${LS_F}$(mixlist $ls s)amix=inputs=$ls:normalize=0:duration=longest,apad=whole_dur=$LTOTAL,atrim=0:$LTOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/ls_own.wav"
TOTAL_SHORT="$TOTAL"; TOTAL="$LTOTAL"
mic "$WORK/lm_own.wav" "$WORK/ls_own.wav" 13 "$LONG/maya.wav"
mic "$WORK/ls_own.wav" "$WORK/lm_own.wav" 14 "$WORK/ls_mic.wav"
samfile "$WORK/ls_mic.wav" "$LONG/sam.wav"
TOTAL="$TOTAL_SHORT"

# ---- pictures -------------------------------------------------------------
# A head-and-shoulders silhouette and the app's chrome, drawn with ImageMagick.
person() { # <skin> <shirt> <out>
  "$MAGICK" -size 1280x720 xc:none -fill "$2" -draw "ellipse 640,760 300,230 0,360" -fill "$1" -draw "ellipse 640,330 125,160 0,360" \
    -fill '#2a1a10' -draw "ellipse 595,310 14,9 0,360" -draw "ellipse 685,310 14,9 0,360" -draw "ellipse 640,410 40,12 0,180" "$3"
}
label() { # <text> <w> <h> <out>
  local font=()
  for f in "/System/Library/Fonts/Supplemental/Arial Bold.ttf" /System/Library/Fonts/Geneva.ttf; do
    if [ -f "$f" ]; then font=(-font "$f"); break; fi
  done
  "$MAGICK" -size "${2}x${3}" xc:'#000000a0' -fill white "${font[@]}" -pointsize "$(( $3 * 6 / 10 ))" -gravity west -annotate +14+0 "$1" "$4"
}
person '#e8b48e' '#7a3b2e' "$WORK/maya.png"
person '#c99a7a' '#2e4a7a' "$WORK/sam.png"
label "Sam" 160 44 "$WORK/label-sam.png"
label "Maya" 200 40 "$WORK/label-maya.png"
label "Sam" 160 40 "$WORK/label-sam2.png"
label "● Recording..." 300 44 "$WORK/rec.png"
JD="$TOTAL"
SD="$(dur "$WORK/sam_file.wav")"
# The moving card shows the picture is live; it stops while Sam's picture freezes.
SAM_FREEZE="$(awk -v t="$FREEZE_AT" -v s="$SAM_START" -v r="$STRETCH" 'BEGIN{printf "%.3f", (t-s)*r}')"
SAM_FREEZE_END="$(add "$SAM_FREEZE" 2.0)"
# Live pictures carry sensor noise; the freeze holds one frame for 2 s.
SAM_FREEZE_F0="$(awk -v t="$SAM_FREEZE" 'BEGIN{printf "%d", t*30+0.5}')"
SAM_FREEZE_F1="$(awk -v t="$SAM_FREEZE_END" 'BEGIN{printf "%d", t*30+0.5-1}')"
"${FF[@]}" -f lavfi -i "color=c=#d9a066:s=1280x720:r=30" -loop 1 -i "$WORK/maya.png" -f lavfi -i "color=c=#f4e3c8:s=80x60:r=30" -i "$WORK/m_mic.wav" \
  -filter_complex "[0:v][1:v]overlay=x='6*sin(t*2.1)':y='4*sin(t*1.6)'[a];[a][2:v]overlay=x='60+40*sin(t*1.3)':y=40,noise=alls=6:allf=t,format=yuv420p[v]" -map "[v]" -map 3:a -t "$JD" \
  -c:v libx264 -preset veryfast -crf 26 -g 60 -c:a pcm_s16le -ar $SR -ac 1 "${BITEXACT[@]}" "$GEN/maya.mov"
"${FF[@]}" -f lavfi -i "color=c=#3b5068:s=1280x720:r=30" -loop 1 -i "$WORK/sam.png" -f lavfi -i "color=c=#9ab0c8:s=80x60:r=30" -loop 1 -i "$WORK/label-sam.png" -i "$WORK/sam_file.wav" \
  -filter_complex "[0:v][1:v]overlay=x='6*sin(t*1.8)':y='4*sin(t*2.3)'[a];[a][2:v]overlay=x='if(between(t,$SAM_FREEZE,$SAM_FREEZE_END),1100+40*sin($SAM_FREEZE*1.7),1100+40*sin(t*1.7))':y=40[b];[b][3:v]overlay=24:652,eq=brightness=-0.06:saturation=0.8,noise=alls=6:allf=t,split[n1][n2];[n1][n2]freezeframes=first=$SAM_FREEZE_F0:last=$SAM_FREEZE_F1:replace=$SAM_FREEZE_F0,format=yuv420p[v]" \
  -map "[v]" -map 4:a -t "$SD" -c:v libx264 -preset veryfast -crf 26 -g 60 -c:a pcm_s16le -ar $SR -ac 1 "${BITEXACT[@]}" "$GEN/sam.mov"
# Gallery view: two tiles, name labels, the recording banner; the mix from 0.5 s on.
"${FF[@]}" -f lavfi -i "color=c=#1b1b1f:s=1280x720:r=30" -loop 1 -i "$WORK/maya.png" -loop 1 -i "$WORK/sam.png" -loop 1 -i "$WORK/label-maya.png" -loop 1 -i "$WORK/label-sam2.png" -loop 1 -i "$WORK/rec.png" -i "$WORK/mix.wav" \
  -filter_complex "[1:v]scale=632:356[j];[2:v]scale=632:356[s];[0:v]drawbox=x=4:y=182:w=632:h=356:color=#d9a066:t=fill,drawbox=x=644:y=182:w=632:h=356:color=#3b5068:t=fill[bg];[bg][j]overlay=x='4+4*sin(t*2.1)':y=182[a];[a][s]overlay=x='644+4*sin(t*1.8)':y=182[b];[b][3:v]overlay=12:494[c];[c][4:v]overlay=652:494[d];[d][5:v]overlay=16:16,noise=alls=6:allf=t,format=yuv420p[v];[6:a]atrim=start=$GALLERY_START,asetpts=PTS-STARTPTS[au]" \
  -map "[v]" -map "[au]" -t "$(add "$TOTAL" -$GALLERY_START)" -c:v libx264 -preset veryfast -crf 28 -g 60 -c:a aac -b:a 128k -ac 1 "${BITEXACT[@]}" "$GEN/gallery.mp4"

# ---- a Zoom local recording with separate audio per participant ------------
Z="$GEN/zoom/$ZOOM_DIR"
mkdir -p "$Z/Audio Record"
"${FF[@]}" -i "$GEN/gallery.mp4" -i "$WORK/mix.wav" -map 0:v -map 1:a -c:v copy -c:a aac -b:a 128k -ac 1 -t "$TOTAL" "${BITEXACT[@]}" "$Z/video$ZOOM_N.mp4" 2>/dev/null \
  || "${FF[@]}" -i "$GEN/gallery.mp4" -i "$WORK/mix.wav" -map 0:v -map 1:a -c:v libx264 -preset veryfast -crf 28 -c:a aac -b:a 128k -ac 1 -t "$TOTAL" "${BITEXACT[@]}" "$Z/video$ZOOM_N.mp4"
"${FF[@]}" -i "$WORK/mix.wav" -c:a aac -b:a 128k -ac 1 "${BITEXACT[@]}" "$Z/audio$ZOOM_N.m4a"
# Zoom's per-participant files are the sound each sent (Sam's as it arrived), padded to start together.
"${FF[@]}" -i "$WORK/m_own.wav" -f lavfi -i "anoisesrc=color=pink:amplitude=0.003:seed=7:sample_rate=$SR" -filter_complex "[0:a][1:a]amix=inputs=2:normalize=0:duration=first" -c:a aac -b:a 128k -ac 1 "${BITEXACT[@]}" "$Z/Audio Record/audioMaya1$ZOOM_N.m4a"
"${FF[@]}" -i "$WORK/s_own.wav" -f lavfi -i "anoisesrc=color=pink:amplitude=0.003:seed=8:sample_rate=$SR" -filter_complex "[0:a]adelay=$DELAY_MS|$DELAY_MS,atrim=0:$TOTAL[s];[s][1:a]amix=inputs=2:normalize=0:duration=first" -c:a aac -b:a 128k -ac 1 "${BITEXACT[@]}" "$Z/Audio Record/audioSam2$ZOOM_N.m4a"
printf '{"magic_number":"123456789","items":[{"process":100,"audio":"audio%s.m4a","prefix":"","video":"video%s.mp4"}]}\n' "$ZOOM_N" "$ZOOM_N" > "$Z/recording.conf"
: > "$Z/chat.txt"

# ---- a Riverside download: one raw video track per person ---------------------
R="$GEN/riverside"
mkdir -p "$R"
"${FF[@]}" -i "$GEN/maya.mov" -c:v copy -c:a aac -b:a 192k "${BITEXACT[@]}" "$R/riverside_maya_raw-video-cfr_garden_show_0001.mp4"
"${FF[@]}" -i "$GEN/sam.mov" -c:v copy -c:a aac -b:a 192k "${BITEXACT[@]}" "$R/riverside_sam_raw-video-cfr_garden_show_0002.mp4"

# ---- the ground truth ---------------------------------------------------------
cat > "$JSON" <<EOF
{
  "made_by": "scripts/make-conversation-fixtures.sh",
  "length_s": $TOTAL,
  "delay_s": $DELAY,
  "bleed_gain": $BLEED,
  "members": [
    {"file": "maya.mov", "name": "Maya", "start_s": 0, "drift_ppm": 0},
    {"file": "sam.mov", "name": "Sam", "start_s": $SAM_START, "drift_ppm": $SAM_DRIFT},
    {"file": "gallery.mp4", "name": "Gallery view", "start_s": $GALLERY_START, "drift_ppm": 0, "use": "picture"}
  ],
  "zoom": {"folder": "zoom/$ZOOM_DIR", "starts_together": true, "sam_delay_s": $DELAY},
  "long": {"files": ["long/maya.wav", "long/sam.wav"], "length_s": $LTOTAL, "sam_start_s": $SAM_START, "sam_drift_ppm": $SAM_DRIFT},
  "sam_dropout_s": [$DROP_AT, $(add "$DROP_AT" 0.35)],
  "sam_freeze_s": [$FREEZE_AT, $(add "$FREEZE_AT" 2.0)],
  "turns": [
$TURNS_JSON
  ]
}
EOF
echo "$STAMP" > "$GEN/.stamp"
echo "conversation fixtures in $GEN"
exit 0
