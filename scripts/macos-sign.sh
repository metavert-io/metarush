#!/usr/bin/env bash
# Signs and notarizes one macOS release binary (D257). goreleaser runs it after building each binary, before the
# archives are made (.goreleaser.yaml, builds.hooks.post); other systems' binaries pass through untouched.
#
#   scripts/macos-sign.sh <binary> <os>         sign (and notarize) when this computer is set up for it
#   scripts/macos-sign.sh --check               say what would happen, change nothing (scripts/release.sh uses it)
#
# Set up once on the computer that makes releases (docs/installation.md, "Signing and notarizing releases"):
#   METARUSH_SIGN_IDENTITY   the Developer ID Application certificate's name in the keychain
#                            ("Developer ID Application: <name> (<team>)"; security find-identity -v -p codesigning)
#   METARUSH_NOTARY_PROFILE  the notarytool profile kept in the keychain (xcrun notarytool store-credentials <profile>)
# in the environment, or in release-signing.env at the top of the checkout (git ignores it; KEY=value lines). There
# are no defaults and no identities in the repository. Without an identity nothing is signed and the build goes on
# with a plain line; with an identity and no profile the binary is signed but not notarized.
#
# Signing: codesign with the hardened runtime, a secure timestamp and scripts/macos-entitlements.plist (MetaRush loads
# the speech and face models' libraries, so library validation is off). Notarizing: notarytool submits a zip of the
# binary and waits for Apple's answer; anything but Accepted fails the build, with Apple's log. A program on its own
# can't carry its notarization ticket (stapler takes apps, disk images and installer packages only), so nothing is
# stapled: macOS checks the ticket online the first time the program runs.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`

# Read release-signing.env as data (KEY=value lines), never as a script; the environment wins.
read_setting() {
  local name="$1" file="$ROOT/release-signing.env" line val
  if [ -n "${!name:-}" ]; then printf '%s' "${!name}"; return; fi
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in "$name="*) val="${line#*=}"; val="${val%\"}"; val="${val#\"}"; val="${val%\'}"; val="${val#\'}"; printf '%s' "$val"; return ;; esac
  done <"$file"
}
IDENTITY="$(read_setting METARUSH_SIGN_IDENTITY)"
PROFILE="$(read_setting METARUSH_NOTARY_PROFILE)"

if [ "${1:-}" = "--check" ]; then
  # One word for scripts/release.sh: none | signed | notarized.
  if [ -z "$IDENTITY" ]; then echo none; elif [ -z "$PROFILE" ]; then echo signed; else echo notarized; fi
  exit 0
fi

BIN="${1:?usage: scripts/macos-sign.sh <binary> <os>}"
OS="${2:?usage: scripts/macos-sign.sh <binary> <os>}"
[ "$OS" = darwin ] || exit 0
NAME="$(basename "$(dirname "$BIN")")"   # e.g. metarush_darwin_arm64_v8.0

if [ -z "$IDENTITY" ]; then
  echo "  macOS build $NAME: not signed (no Developer ID is set up on this computer; see docs/installation.md)"
  exit 0
fi
[ "$(uname -s)" = Darwin ] || { echo "  ✗ signing a macOS build needs a Mac (METARUSH_SIGN_IDENTITY is set)" >&2; exit 1; }
command -v codesign >/dev/null || { echo "  ✗ codesign is missing (xcode-select --install)" >&2; exit 1; }

codesign --force --options runtime --timestamp --entitlements "$ROOT/scripts/macos-entitlements.plist" --sign "$IDENTITY" "$BIN"
codesign --verify --strict --verbose=2 "$BIN" 2>/dev/null || { echo "  ✗ $NAME: the signature doesn't verify" >&2; exit 1; }
INFO="$(codesign -dvv "$BIN" 2>&1)"
grep -q '^Authority=Developer ID Application' <<<"$INFO" || { echo "  ✗ $NAME: not signed with a Developer ID Application certificate" >&2; exit 1; }
grep -q 'flags=.*runtime' <<<"$INFO" || { echo "  ✗ $NAME: signed without the hardened runtime" >&2; exit 1; }
echo "  ✓ macOS build $NAME: signed (hardened runtime, timestamped)"

if [ -z "$PROFILE" ]; then
  echo "  macOS build $NAME: not notarized (no notarization profile is set up on this computer; see docs/installation.md)"
  exit 0
fi
command -v xcrun >/dev/null || { echo "  ✗ xcrun is missing (xcode-select --install)" >&2; exit 1; }
WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-notary-XXXXXX")"
trap 'rc="$(exit_status $?)"; rm -rf "$WORK"; exit "$rc"' EXIT
ditto -c -k --keepParent "$BIN" "$WORK/metarush.zip"
echo "  … macOS build $NAME: waiting for Apple's notarization (usually a few minutes)"
if ! xcrun notarytool submit "$WORK/metarush.zip" --keychain-profile "$PROFILE" --wait --timeout 30m --output-format json >"$WORK/submit.json" 2>"$WORK/submit.err"; then
  echo "  ✗ $NAME: notarization couldn't be submitted: $(tail -n 3 "$WORK/submit.err")" >&2
  exit 1
fi
STATUS="$(python3 -I -c 'import json,sys;print(json.load(sys.stdin).get("status",""))' <"$WORK/submit.json")"
ID="$(python3 -I -c 'import json,sys;print(json.load(sys.stdin).get("id",""))' <"$WORK/submit.json")"
if [ "$STATUS" != Accepted ]; then
  echo "  ✗ $NAME: Apple didn't accept it for notarization ($STATUS). Apple's log:" >&2
  [ -n "$ID" ] && xcrun notarytool log "$ID" --keychain-profile "$PROFILE" >&2 || true
  exit 1
fi
echo "  ✓ macOS build $NAME: notarized (submission $ID; a program on its own can't carry the ticket, so macOS checks it online)"
exit 0
