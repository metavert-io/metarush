#!/usr/bin/env bash
# fetch-speech.sh — fetch the openly licensed speech the multilingual transcription measurement uses.
#
# Downloads public-domain LibriVox readings (archive.org) and a public-domain recording from Wikimedia
# Commons into tests/fixtures/speech/ (gitignored, never committed), checks each file against the SHA-1 its
# publisher gives, and converts it to the 16 kHz mono 16-bit PCM WAV both recognizers expect (ffmpeg -ac 1
# -ar 16000 -c:a pcm_s16le). The reference texts are small, public domain and committed in
# tests/fixtures/truth/speech/ (where each text came from: SOURCES.md there); where each recording came from, and
# its licence, is in SOURCES below. The ordinary tests need none of this.
#
# Usage:  scripts/fetch-speech.sh            # fetch what's missing
#         FORCE=1 scripts/fetch-speech.sh    # fetch and convert everything again
# Then:   EVAL_SPEECH=tests/fixtures/speech EVAL_LANGS=es go test -tags voiceeval -run TestMultilingual -timeout 60m ./internal/transcribe/
#
# Requirements: curl, ffmpeg, shasum.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/tests/fixtures/speech"
RAW="$OUT/raw"
mkdir -p "$RAW"

FFMPEG="${FFMPEG:-$(command -v ffmpeg || true)}"; FFMPEG="${FFMPEG:-/usr/local/bin/ffmpeg}"
[ -x "$FFMPEG" ] || { echo "ffmpeg not found" >&2; exit 1; }
command -v curl >/dev/null || { echo "curl not found" >&2; exit 1; }
command -v shasum >/dev/null || { echo "shasum not found" >&2; exit 1; }

# id | language | URL (archive.org: <item>/<file>; else the full address) | SHA-1 its publisher gives | licence of the recording
# The English ones are only listened to for language detection (an English clip must stay English).
SOURCES='
es-tito|es|tito_1602_librivox/tito_01_rva_64kb.mp3|d7e8e5ea37832dd2216ae6e1ea19e66db5651136|Public Domain Mark 1.0
fr-ruth|fr|ruth_lsg_librivox/ruth_bible_lsg-1910_64kb.mp3|a82b41dbcbb17e63d20d4c38602409b103009787|public domain (LibriVox)
de-galerie|de|kafka_galerie_librivox/auf_der_galerie_kafka_64kb.mp3|18bc48e67aa0a1c85a926cab403a5d9ba64c92a0|public domain (LibriVox)
de-laerm|de|kafka_laerm_librivox/Kafka_Grosser_Laerm_64kb.mp3|69c4dc57fb55faecb5d79e478e77f4dd6b36527e|public domain (LibriVox)
de-haewelmann|de|haewelmann_librivox/der_kleine_haewelmann_storm_64kb.mp3|69add045ef6258d682d519b80cbe88b68e59a350|public domain (LibriVox)
pt-apologo|pt|contos_brasileiros_0807_librivox/contos_brasileiros_14_64kb.mp3|f915bf2d78b6caada084498a5254f153fe8b6711|public domain (LibriVox)
pt-filha|pt|contos_brasileiros_0807_librivox/contos_brasileiros_01_64kb.mp3|754c6aa0267a847ebce425a1573243bdc4826b59|public domain (LibriVox)
ja-ningyo1|ja|akai_rosoku_um_librivox/akairosokutoningyo_01_ogawa_64kb.mp3|dc2de721c6ee96a60a4e300091e48220ec3bf9d8|CC0 1.0
ja-ningyo2|ja|akai_rosoku_um_librivox/akairosokutoningyo_02_ogawa_64kb.mp3|31dd6344b3ee214cc6a100d3c11f3154a941fb33|CC0 1.0
en-gb1|en|https://upload.wikimedia.org/wikipedia/commons/1/1f/George_W_Bush_Columbia_FINAL.ogg|469ba9f0fce0603605d3ef88e5bb893141458a40|public domain (a work of the US federal government; Wikimedia Commons)
en-telltale|en|stories_001_librivox/telltale_heart_poe_dm_64kb.mp3|a89fc43a2aca9735e700993e3ae3df75a37c0a39|public domain (LibriVox)
en-nicepeople|en|stories_001_librivox/nice_people_bunner_wsc_64kb.mp3|f9193eb00cae16dd7344158d1eac8de39ccec076|public domain (LibriVox)
'

manifest="$OUT/manifest.tsv"
printf 'id\tlanguage\tsource\tsha256_wav\tlicence\n' > "$manifest.tmp"
printf '%s\n' "$SOURCES" | while IFS='|' read -r id lang src sha1 licence; do
  [ -n "$id" ] || continue
  mp3="$RAW/$id.${src##*.}"
  wav="$OUT/$id.wav"
  case "$src" in https://*) url="$src" ;; *) url="https://archive.org/download/$src" ;; esac
  if [ "${FORCE:-}" = 1 ] || [ ! -s "$mp3" ]; then
    echo "fetching $id ($url)"
    curl -fsSL --retry 3 -o "$mp3.part" "$url"
    mv "$mp3.part" "$mp3"
  fi
  got="$(shasum -a 1 "$mp3" | cut -d' ' -f1)"
  if [ "$got" != "$sha1" ]; then
    echo "$id: SHA-1 $got does not match the publisher's $sha1" >&2
    rm -f "$mp3"
    exit 1
  fi
  if [ "${FORCE:-}" = 1 ] || [ ! -s "$wav" ] || [ "$mp3" -nt "$wav" ]; then
    "$FFMPEG" -nostdin -hide_banner -loglevel error -y -i "$mp3" -map 0:a:0 -vn -ac 1 -ar 16000 -c:a pcm_s16le \
      -fflags +bitexact -flags:a +bitexact -map_metadata -1 "$wav"
  fi
  printf '%s\t%s\t%s\t%s\t%s\n' "$id" "$lang" "$url" "$(shasum -a 256 "$wav" | cut -d' ' -f1)" "$licence" >> "$manifest.tmp"
done
mv "$manifest.tmp" "$manifest"
echo "speech ready in $OUT ($(($(wc -l < "$manifest") - 1)) recordings; manifest.tsv lists them)"
