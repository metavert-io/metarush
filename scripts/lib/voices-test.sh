#!/usr/bin/env bash
# scripts/lib/voices-test.sh — checks scripts/lib/voices.sh with a stand-in `say` that lists
# voices the way older macOS does ("Samantha   en_US   # …") and the way newer macOS does
# ("Samantha (English (US))   en_US   # …"): both pick the same voices. No real `say` needed.
#
#   scripts/lib/voices-test.sh
set -uo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
WORK="$(mktemp -d "${TMPDIR:-/tmp}/voices-test-XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
failures=0
check() { if [ "$2" = "$3" ]; then echo "PASS  $1"; else echo "FAIL  $1: got '$2', want '$3'"; failures=$((failures + 1)); fi; }

stand_in() { # $1 = the listing
  mkdir -p "$WORK/bin"
  printf '%s\n' "$1" > "$WORK/voices.txt"
  printf '#!/bin/sh\n[ "$1" = "-v" ] && [ "$2" = "?" ] && cat "%s"\n' "$WORK/voices.txt" > "$WORK/bin/say"
  chmod +x "$WORK/bin/say"
}
pick() { (PATH="$WORK/bin:$PATH"; . "$ROOT/scripts/lib/voices.sh"; pick_voice "$@"); }

stand_in 'Daniel              en_GB    # Hello! My name is Daniel.
Flo (English (US))  en_US    # Hello! My name is Flo.
Karen               en_AU    # Hello! My name is Karen.
Samantha            en_US    # Hello! My name is Samantha.'
check "older macOS: a bare name" "$(pick Samantha "Flo (English (US))")" "Samantha"
check "older macOS: a name with its language" "$(pick Kathy "Flo (English (US))")" "Flo (English (US))"
check "older macOS: none installed" "$(pick Kathy Fred)" ""

stand_in 'Daniel (English (UK)) en_GB    # Hello! My name is Daniel.
Eddy (German (Germany)) de_DE    # Hallo! Ich heiße Eddy.
Flo (English (US))    en_US    # Hello! My name is Flo.
Karen                 en_AU    # Hello! My name is Karen.
Samantha (English (US)) en_US    # Hello! My name is Samantha.
Samantha (English (US)) en_US    # Hello! My name is Samantha.'
check "newer macOS: a bare name listed with its language" "$(pick Samantha "Flo (English (US))")" "Samantha"
check "newer macOS: the same second voice" "$(pick Daniel "Reed (English (UK))")" "Daniel"
check "newer macOS: a voice still listed bare" "$(pick Karen Moira)" "Karen"
check "newer macOS: another language's voice isn't English" "$(pick Eddy Kathy)" ""
check "newer macOS: a prefix of another name doesn't count" "$(pick Sam Dan)" ""

[ "$failures" = 0 ] || { echo "$failures failed"; exit 1; }
echo "voices: all passed"
