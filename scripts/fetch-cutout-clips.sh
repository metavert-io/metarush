#!/usr/bin/env bash
# fetch-cutout-clips.sh — fetch the public-domain clips with people that the cut-out measurement uses.
#
# Background removal for video (docs/background-removal.md#how-well-it-works) is measured on real footage of
# people: three clips filmed by US federal agencies (works of the US government, public domain) and a clip of a
# person filmed against a green screen, whose keyed matte is the reference the models' mattes are compared with.
# They come from Wikimedia Commons into tests/fixtures/cutouts/ (gitignored, never committed: footage of real
# people stays out of the repository), each checked against the SHA-256 it had when the numbers were measured.
# The ordinary tests need none of this.
#
# Usage:  scripts/fetch-cutout-clips.sh            # fetch what's missing
#         FORCE=1 scripts/fetch-cutout-clips.sh    # fetch everything again
# Then (on a Mac for Apple Vision; METARUSH_ORT_LIB and METARUSH_MODNET for MODNet):
#   METARUSH_MATTE_CLIPS=tests/fixtures/cutouts go test -tags matteeval -run TestCutOutEval -timeout 30m -v ./internal/matting/
#
# Requirements: curl, shasum.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
OUT="$ROOT/tests/fixtures/cutouts"
mkdir -p "$OUT"
command -v curl >/dev/null || { echo "curl not found" >&2; exit 1; }
command -v shasum >/dev/null || { echo "shasum not found" >&2; exit 1; }

C=https://upload.wikimedia.org/wikipedia/commons
# id | address on Wikimedia Commons | SHA-256 | what it is, and its licence
SOURCES="
cbp|$C/2/22/Video_Blog-_CBP_Super_Bowl_LII_Countdown_to_Kickoff_-_Day_2.webm|de729ed833e9993b96687a4e9a28a4f237178587e49e958f15914650260a4402|a man talking to the camera outdoors, 1920x1080; US Customs and Border Protection, public domain
spectro|$C/5/5f/Astrophysics_Explainer_Vertical_Video_%28SVS14801_-_What_is_Spectroscopy_-_Vertical_burned_CAPTIONED%29.webm|383ce041233b6850a72fec31df9b43174113b7af0337b5eff9024a11cf02db4d|a woman with curly hair explaining, vertical 1080x1920; NASA, public domain
artemis|$C/4/49/NASA_Interview_Opportunity-_Moonbound%21_NASA%E2%80%99s_Artemis_II_Mission_Days_From_Launch_%E2%80%94_First_Crewed_Journey_Around_the_Moon_in_More_Than_50_Years%21_%28SVS14934_-_LONGER_QUESTIONS_1%29.webm|586b204306636a9a755beba9f3160728bb2bccf49ec8bc48dc34d8d8be57dd82|interviews in a studio, 1920x1080; NASA, public domain
greenscreen|$C/2/23/All_of_this_filming_of_Anna_is_just_for_a_few_seconds_in_an_upcoming_video%21.webm|394724b38b8d32e4fa488a9a0e8bd1ebba828b4ec9ae7c9995b4a7c13fe0791b|a woman filmed against a green screen, vertical 720x1280; Voice of America, public domain
"

printf '%s\n' "$SOURCES" | while IFS='|' read -r id url sha what; do
  [ -n "$id" ] || continue
  file="$OUT/$id.webm"
  if [ "${FORCE:-}" = 1 ] || [ ! -s "$file" ]; then
    echo "fetching $id"
    curl -fsSL --retry 3 -o "$file.part" "$url"
    mv "$file.part" "$file"
  fi
  got="$(shasum -a 256 "$file" | cut -d' ' -f1)"
  if [ "$got" != "$sha" ]; then
    echo "$id: SHA-256 $got is not the one measured ($sha)" >&2
    rm -f "$file"
    exit 1
  fi
  echo "$id: ok ($what)"
done
exit 0
