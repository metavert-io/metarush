#!/usr/bin/env bash
# make-call-fixtures.sh — a synthetic online call recorded in speaker view, for
# asking who people are once per person (D155–D157): the picture follows
# whoever talks (half a second late, a hard cut at every switch, so every
# appearance is a new face on screen), the call app prints each person's name
# under their picture, three people with distinct voices and faces, a stretch
# in gallery view (all three tiles, a name under each), a title card, people
# saying their names ("Hi everyone, I'm Ada") and greeting each other
# ("Thanks, Ada").
#
# Writes tests/fixtures/gen/call/garden-call.mp4 (gitignored) and the ground
# truth tests/fixtures/truth/call.json (committed).
#
#   scripts/make-call-fixtures.sh            # build if missing or out of date
#   FORCE=1 scripts/make-call-fixtures.sh    # rebuild
#
# The faces are the public-domain photos in tests/fixtures/images (SOURCES.md);
# the names on screen are invented and name none of the people photographed.
# Needs ffmpeg, ImageMagick (`magick`) and macOS `say`.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
GEN="${CALL_GEN:-$ROOT/tests/fixtures/gen/call}"
TRUTH="$ROOT/tests/fixtures/truth"
IMAGES="$ROOT/tests/fixtures/images"
mkdir -p "$GEN" "$TRUTH"
FFMPEG="${FFMPEG:-$(command -v ffmpeg || true)}"; FFMPEG="${FFMPEG:-/usr/local/bin/ffmpeg}"
FFPROBE="${FFPROBE:-$(command -v ffprobe || true)}"; FFPROBE="${FFPROBE:-/usr/local/bin/ffprobe}"
MAGICK="${MAGICK:-$(command -v magick || true)}"; MAGICK="${MAGICK:-/usr/local/bin/magick}"
[ -x "$FFMPEG" ] || { echo "ffmpeg not found" >&2; exit 1; }
if [ ! -x "$MAGICK" ]; then
  echo "ImageMagick isn't installed, and the call's pictures are drawn with it: the gallery, the people's tiles and" >&2
  echo "the names the call app prints. Install it (brew install imagemagick) and run this again." >&2
  exit 1
fi
command -v say >/dev/null || { echo "macOS 'say' not found (speech fixtures need it)" >&2; exit 1; }
FF=("$FFMPEG" -hide_banner -loglevel error -nostdin -y)
BITEXACT=(-fflags +bitexact -flags:v +bitexact -flags:a +bitexact -map_metadata -1)
X264=(-c:v libx264 -preset veryfast -crf 24 -pix_fmt yuv420p -g 60 -r 30)
RATE_WPM=175
SR=48000
LAG=0.5   # the call switches the picture this long after someone starts talking
GAP=0.35  # silence between turns

. "$ROOT/scripts/lib/voices.sh" # pick_voice: "Moira" also matches "Moira (English (Ireland))"
# Three voices not used by the other fixtures, so nobody named elsewhere sounds like them.
VOICE_ADA="${CALL_VOICE_ADA:-$(pick_voice Tessa Moira Karen)}"
VOICE_BEN="${CALL_VOICE_BEN:-$(pick_voice Rishi Fred Daniel)}"
VOICE_CLEO="${CALL_VOICE_CLEO:-$(pick_voice Moira Tara Kathy)}"
# Synthetic voices of one kind can sound alike to speaker detection: a voice can
# be pitched (1.0 = as spoken; 0.85 = lower, the same length).
PITCH_ADA="${CALL_PITCH_ADA:-1.0}"; PITCH_BEN="${CALL_PITCH_BEN:-1.0}"; PITCH_CLEO="${CALL_PITCH_CLEO:-1.0}"
# Everyone in a call has their own microphone and room: Ada a good headset, Ben a
# small room, Cleo a laptop microphone (thin, compressed).
MIC_ADA="${CALL_MIC_ADA:-anull}"
MIC_BEN="${CALL_MIC_BEN:-aecho=0.8:0.6:35:0.22,highpass=f=90}"
MIC_CLEO="${CALL_MIC_CLEO:-highpass=f=340,lowpass=f=3400,acompressor=threshold=-22dB:ratio=4:attack=5:release=60,volume=1.5}"
NAME_ADA="Ada Park"; NAME_BEN="Ben Okafor"; NAME_CLEO="Cleo Marsh"
PHOTO_ADA="$IMAGES/portrait.jpg"; PHOTO_BEN="$IMAGES/person.jpg"; PHOTO_CLEO="$IMAGES/portrait-c.jpg"

OUT="$GEN/garden-call.mp4"
STAMP="$( { cat "${BASH_SOURCE[0]}" "$PHOTO_ADA" "$PHOTO_BEN" "$PHOTO_CLEO"; echo "$VOICE_ADA|$VOICE_BEN|$VOICE_CLEO|$PITCH_ADA|$PITCH_BEN|$PITCH_CLEO|$MIC_ADA|$MIC_BEN|$MIC_CLEO"; "$FFMPEG" -version | head -1; "$MAGICK" -version | head -1; } | shasum -a 256 | awk '{print $1}')"
if [ "${FORCE:-0}" != "1" ] && [ -f "$GEN/.stamp" ] && [ "$(cat "$GEN/.stamp")" = "$STAMP" ] && [ -s "$OUT" ]; then
  echo "call fixture up to date in $GEN (FORCE=1 to rebuild)"; exit 0
fi
WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-call-fixtures.XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT

dur() { "$FFPROBE" -v error -show_entries format=duration -of csv=p=0 "$1"; }
add() { awk -v a="$1" -v b="$2" 'BEGIN{printf "%.3f", a+b}'; }
speak() { # <voice> <text> <out.wav> [pitch] [microphone filter]
  local pitch="${4:-1.0}" mic="${5:-anull}" shift=""
  say -v "$1" -r "$RATE_WPM" -o "$WORK/s.aiff" "$2"
  if [ "$pitch" != "1.0" ]; then
    shift="asetrate=$(awk -v p="$pitch" -v r="$SR" 'BEGIN{printf "%d", r*p}'),aresample=$SR,atempo=$(awk -v p="$pitch" 'BEGIN{printf "%.4f", 1/p}'),"
  fi
  "${FF[@]}" -i "$WORK/s.aiff" -af "aresample=$SR,${shift}silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse,silenceremove=start_periods=1:start_threshold=-55dB:start_silence=0,areverse,$mic" -ac 1 -c:a pcm_s16le "$3"
}
FONT=()
for f in "/System/Library/Fonts/Supplemental/Arial Bold.ttf" /System/Library/Fonts/Helvetica.ttc /System/Library/Fonts/Geneva.ttf; do
  if [ -f "$f" ]; then FONT=(-font "$f"); break; fi
done
label() { # <text> <w> <h> <out>: a call app's name tile (white on translucent black)
  "$MAGICK" -size "${2}x${3}" xc:'#000000a8' -fill white "${FONT[@]}" -pointsize "$(( $3 * 6 / 10 ))" -gravity west -annotate +12+0 "$1" "$4"
}
label "● Recording..." 300 44 "$WORK/rec.png"

# ---- who says what -------------------------------------------------------------
# who|text ; who: A (Ada, hosts), B (Ben), C (Cleo, joins late); G marks the turns shown in gallery view.
LINES=(
  "A|Hi everyone, I'm Ada. Thanks for joining the garden club call today."
  "B|Thanks, Ada. Happy to be here. I brought photos of my tomatoes."
  "A|Great. Ben, how did the balcony pots do this summer?"
  "B|Much better than last year. Deep pots and less water made all the difference."
  "C|Hi, I'm Cleo, sorry I'm late. My connection dropped."
  "A|No problem, Cleo. We were just talking about tomatoes."
  "C|Mine grew in a bucket on the porch. Cherry tomatoes are easy."
  "B|That's what I keep telling people. Start small."
  "A|G|Let's see everyone for a moment. What about the herbs?"
  "C|G|Basil loves the heat. Mint takes over everything."
  "B|G|Mint is a weed with good manners."
  "A|Okay. Does anyone grow peppers?"
  "B|Yes, in the sunniest corner. They need patience."
  "C|Mine never turned red, only green."
  "A|Any last tips before we finish?"
  "C|Water in the morning, not at night."
  "B|And talk to your plants. They listen."
  "A|Thanks, Ben. Thanks, Cleo. See you next week."
  "C|Bye everyone."
)
TITLE_END=2.0
T="$TITLE_END"
TURNS_JSON=""; VIEWS_JSON=""
A_IN=(); A_F=""; n=0
VIEW_WHO=(); VIEW_FROM=(); VIEW_TO=()
i=0
for line in "${LINES[@]}"; do
  IFS='|' read -r who g text <<< "$line"
  if [ -z "$text" ]; then text="$g"; g=""; fi
  case "$who" in A) voice="$VOICE_ADA"; name="$NAME_ADA"; pitch="$PITCH_ADA"; mic="$MIC_ADA" ;; B) voice="$VOICE_BEN"; name="$NAME_BEN"; pitch="$PITCH_BEN"; mic="$MIC_BEN" ;; C) voice="$VOICE_CLEO"; name="$NAME_CLEO"; pitch="$PITCH_CLEO"; mic="$MIC_CLEO" ;; esac
  speak "$voice" "$text" "$WORK/l$i.wav" "$pitch" "$mic"
  d="$(dur "$WORK/l$i.wav")"
  start="$T"; end="$(add "$start" "$d")"
  [ -n "$TURNS_JSON" ] && TURNS_JSON="$TURNS_JSON,"$'\n'
  TURNS_JSON="$TURNS_JSON    {\"speaker\": \"$name\", \"start_s\": $start, \"end_s\": $end, \"text\": \"$text\"}"
  ms="$(awk -v s="$start" 'BEGIN{printf "%d", s*1000+0.5}')"
  A_IN+=(-i "$WORK/l$i.wav"); A_F="$A_F[$n:a]adelay=$ms|$ms[a$n];"; n=$((n+1))
  # The picture: this speaker (or the gallery) from LAG after they start.
  view="$who"; [ "$g" = G ] && view=G
  sw="$(add "$start" "$LAG")"; [ "$i" = 0 ] && sw="$TITLE_END"
  last=$(( ${#VIEW_WHO[@]} - 1 ))
  if [ "$last" -ge 0 ] && [ "${VIEW_WHO[$last]}" = "$view" ]; then
    : # the same view goes on
  else
    if [ "$last" -ge 0 ]; then VIEW_TO[$last]="$sw"; fi
    VIEW_WHO+=("$view"); VIEW_FROM+=("$sw"); VIEW_TO+=("")
  fi
  T="$(add "$end" "$GAP")"
  i=$((i+1))
done
TOTAL="$(add "$T" 1.0)"
VIEW_TO[$(( ${#VIEW_WHO[@]} - 1 ))]="$TOTAL"
mixlist() { local out=""; for ((k=0;k<n;k++)); do out="$out[a$k]"; done; echo "$out"; }
"${FF[@]}" "${A_IN[@]}" -f lavfi -i "anoisesrc=color=pink:amplitude=0.003:seed=11:sample_rate=$SR" \
  -filter_complex "${A_F}$(mixlist)[$n:a]amix=inputs=$((n+1)):normalize=0:duration=longest,apad=whole_dur=$TOTAL,atrim=0:$TOTAL" -ac 1 -ar $SR -c:a pcm_s16le "$WORK/call.wav"

# ---- pictures ------------------------------------------------------------------
# A person's webcam in speaker view, filling the picture: their room (their own
# photo, blurred and darkened, behind them) and them in the middle (a little
# bigger than the frame, so it can sway), their name at the bottom left, the
# recording banner at the top left. Each appearance is framed a little
# differently (another moment of the same camera).
cam() { # <photo> <name> <variant> <out>
  local h=$(( 640 + ($3 % 4) * 14 )) dx=$(( ($3 % 5) * 12 - 24 ))
  label "$2" 240 44 "$WORK/tile.png"
  "$MAGICK" \( "$1" -resize 1300x732^ -gravity center -extent 1300x732 -blur 0x18 -modulate 70 \) \
    \( "$1" -resize "x$h" -modulate "$(( 96 + ($3 % 3) * 4 ))" \) -gravity center -geometry "+$dx+0" -composite \
    "$WORK/tile.png" -gravity northwest -geometry +34+656 -composite "$WORK/rec.png" -gravity northwest -geometry +26+22 -composite "$4"
}
gallery() { # <out>: three tiles in a two-by-two grid, each person's name at the bottom left of their tile
  "$MAGICK" -size 1300x732 xc:'#1c1c1f' "$WORK/g-bg.png"
  for spec in "$PHOTO_ADA|$NAME_ADA|22|14" "$PHOTO_BEN|$NAME_BEN|654|14" "$PHOTO_CLEO|$NAME_CLEO|22|369"; do
    IFS='|' read -r photo name x y <<< "$spec"
    "$MAGICK" \( "$photo" -resize 624x349^ -gravity center -extent 624x349 -blur 0x12 -modulate 70 \) \( "$photo" -resize x343 \) -gravity center -composite "$WORK/g-tile.png"
    label "$name" 190 34 "$WORK/g-name.png"
    "$MAGICK" "$WORK/g-tile.png" "$WORK/g-name.png" -gravity southwest -geometry +8+8 -composite "$WORK/g-tile2.png"
    "$MAGICK" "$WORK/g-bg.png" "$WORK/g-tile2.png" -gravity northwest -geometry "+$x+$y" -composite "$WORK/g-bg.png"
  done
  "$MAGICK" "$WORK/g-bg.png" "$WORK/rec.png" -gravity northeast -geometry +36+22 -composite "$1"
}
"$MAGICK" -size 1300x732 xc:'#1c1c1f' -fill white "${FONT[@]}" -pointsize 46 -gravity center -annotate +0+0 "Garden club · weekly call" "$WORK/title.png"
gallery "$WORK/gallery.png"

# One video segment per view (a hard cut at every switch), then one file.
: > "$WORK/list.txt"
seg() { # <png> <seconds> <seed> <out>
  "${FF[@]}" -loop 1 -i "$1" -t "$2" -filter_complex "[0:v]crop=1280:720:x='10+6*sin(t*1.7)':y='6+4*sin(t*2.1)',noise=alls=5:allf=t+u:all_seed=$3,format=yuv420p[v]" -map "[v]" \
    "${X264[@]}" "${BITEXACT[@]}" "$4"
}
seg "$WORK/title.png" "$TITLE_END" 1 "$WORK/seg-title.mp4"
echo "file '$WORK/seg-title.mp4'" >> "$WORK/list.txt"
VIEWS_JSON="    {\"view\": \"title\", \"from_s\": 0, \"to_s\": $TITLE_END}"
for k in "${!VIEW_WHO[@]}"; do
  who="${VIEW_WHO[$k]}"; from="${VIEW_FROM[$k]}"; to="${VIEW_TO[$k]}"
  len="$(add "$to" "-$from")"
  case "$who" in
    A) cam "$PHOTO_ADA" "$NAME_ADA" "$k" "$WORK/v$k.png"; vname="$NAME_ADA" ;;
    B) cam "$PHOTO_BEN" "$NAME_BEN" "$k" "$WORK/v$k.png"; vname="$NAME_BEN" ;;
    C) cam "$PHOTO_CLEO" "$NAME_CLEO" "$k" "$WORK/v$k.png"; vname="$NAME_CLEO" ;;
    G) cp "$WORK/gallery.png" "$WORK/v$k.png"; vname="gallery" ;;
  esac
  seg "$WORK/v$k.png" "$len" "$((k+2))" "$WORK/seg$k.mp4"
  echo "file '$WORK/seg$k.mp4'" >> "$WORK/list.txt"
  VIEWS_JSON="$VIEWS_JSON,"$'\n'"    {\"view\": \"$vname\", \"from_s\": $from, \"to_s\": $to}"
done
"${FF[@]}" -f concat -safe 0 -i "$WORK/list.txt" -c copy "${BITEXACT[@]}" "$WORK/picture.mp4"
"${FF[@]}" -i "$WORK/picture.mp4" -i "$WORK/call.wav" -map 0:v -map 1:a -c:v copy -c:a aac -b:a 128k -ac 1 -t "$TOTAL" "${BITEXACT[@]}" "$OUT"

APPEARANCES="$(printf '%s\n' "${VIEW_WHO[@]}" | grep -cv '^G$' || true)"
cat > "$TRUTH/call.json" <<EOF
{
  "made_by": "scripts/make-call-fixtures.sh",
  "file": "call/garden-call.mp4",
  "length_s": $TOTAL,
  "switch_lag_s": $LAG,
  "people": [
    {"name": "$NAME_ADA", "voice": "$VOICE_ADA", "photo": "portrait.jpg"},
    {"name": "$NAME_BEN", "voice": "$VOICE_BEN", "photo": "person.jpg"},
    {"name": "$NAME_CLEO", "voice": "$VOICE_CLEO", "photo": "portrait-c.jpg"}
  ],
  "speaker_view_appearances": $APPEARANCES,
  "views": [
$VIEWS_JSON
  ],
  "turns": [
$TURNS_JSON
  ]
}
EOF
echo "$STAMP" > "$GEN/.stamp"
echo "call fixture in $GEN: $(basename "$OUT"), ${TOTAL}s, $APPEARANCES speaker-view appearances"
exit 0
