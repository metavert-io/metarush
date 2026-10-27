#!/bin/sh
# install-c2patool.sh — install c2patool, pinned, for the content-credentials ability.
#
# A person runs this, never MetaRush or Claude. It downloads c2patool 0.28.3 (MIT or
# Apache-2.0, from its project's releases on GitHub), checks the download's SHA-256
# against the pin below and refuses anything else, and puts the program in a folder for
# the whole computer: an ability never runs a program inside a home folder.
#
#   sh install-c2patool.sh                         # into /usr/local/lib/c2patool-0.28.3
#   sh install-c2patool.sh --prefix /opt/c2patool  # anywhere outside your home folder
#
# /usr/local/lib may need an administrator: run it with sudo, or give --prefix. Then set
# the ability up with the path it prints. To undo: delete that folder.
#
# C2PATOOL_BASE_URL and C2PATOOL_TEST_PIN are for tests: a download from this computer
# (http://127.0.0.1:…) checked against another pin. Any other address keeps the real pins.
set -eu

VERSION=0.28.3
BASE="${C2PATOOL_BASE_URL:-https://github.com/contentauth/c2patool/releases/download/v$VERSION}"
PREFIX="/usr/local/lib/c2patool-$VERSION"

while [ $# -gt 0 ]; do
  case "$1" in
    --prefix) PREFIX="${2:-}"; shift 2 ;;
    --prefix=*) PREFIX="${1#--prefix=}"; shift ;;
    -h|--help) sed -n '2,19p' "$0"; exit 0 ;;
    *) echo "install-c2patool.sh: unknown option $1" >&2; exit 2 ;;
  esac
done

case "$(uname -s)-$(uname -m)" in
  Darwin-*)
    NAME="v$VERSION-universal-apple-darwin.zip"
    PIN=12db3a9b454e2ce23f8701b74bca88ebd3dd4f0b9e5468f4dfda1810ee1d2787 ;;
  Linux-x86_64)
    NAME="v$VERSION-x86_64-unknown-linux-gnu.tar.gz"
    PIN=74f9d5c599b05345fdd0de8c7b3ee347960f1d24352d030d36a2a1de33c99600 ;;
  *)
    echo "c2patool $VERSION has no download for $(uname -s) $(uname -m). Build it from its source instead (cargo install c2patool --version $VERSION)." >&2
    exit 1 ;;
esac

PROTO='=https'
# A test download from this computer: exactly http://127.0.0.1:<port>[/path] or
# http://localhost:<port>[/path] (no user part, nothing that names another host).
if printf '%s' "$BASE" | grep -Eq '^http://(127\.0\.0\.1|localhost):[0-9]{1,5}(/[A-Za-z0-9._~-]+)*/?$'; then
  PROTO='=http'
fi
if [ -n "${C2PATOOL_TEST_PIN:-}" ]; then
  [ "$PROTO" = '=http' ] || { echo "C2PATOOL_TEST_PIN is only for a test download from this computer; nothing was installed." >&2; exit 1; }
  PIN="$C2PATOOL_TEST_PIN"
fi

case "$PREFIX" in
  "") echo "Say where to put it: --prefix /some/folder" >&2; exit 2 ;;
  /*) ;;
  *) echo "Give the folder's full path (--prefix /opt/c2patool)." >&2; exit 2 ;;
esac
case "$PREFIX/" in
  "$HOME"/*) echo "$PREFIX is inside your home folder, which an ability never reads; choose a folder for the whole computer." >&2; exit 1 ;;
esac

WORK="$(mktemp -d "${TMPDIR:-/tmp}/c2patool-install.XXXXXX")"
fail() { rm -rf "$WORK"; echo "$1" >&2; exit 1; }

echo "Downloading c2patool $VERSION ($NAME)…"
curl -fsSL --proto "$PROTO" --proto-redir '=https' -o "$WORK/$NAME" "$BASE/$NAME" || fail "The download didn't work; nothing was installed."

if command -v shasum >/dev/null 2>&1; then
  GOT="$(shasum -a 256 "$WORK/$NAME" | cut -d' ' -f1)"
else
  GOT="$(sha256sum "$WORK/$NAME" | cut -d' ' -f1)"
fi
[ "$GOT" = "$PIN" ] || fail "The download isn't the c2patool $VERSION this script pins (its SHA-256 is $GOT, not $PIN); nothing was installed."

mkdir "$WORK/x"
case "$NAME" in
  *.zip) unzip -q "$WORK/$NAME" -d "$WORK/x" || fail "The download couldn't be unpacked; nothing was installed." ;;
  *.tar.gz) tar -xzf "$WORK/$NAME" -C "$WORK/x" || fail "The download couldn't be unpacked; nothing was installed." ;;
esac
BIN="$(find "$WORK/x" -type f -name c2patool | head -n 1)"
[ -n "$BIN" ] || fail "The download has no c2patool program in it; nothing was installed."

mkdir -p "$PREFIX" 2>/dev/null || fail "This script can't write to $PREFIX: run it again with sudo, or give --prefix."
# Never through a link someone left in the way: the folder must be a folder,
# and the program is written to a new file there, then renamed into place
# (a rename replaces a link; it never writes through it).
[ -d "$PREFIX" ] && [ ! -L "$PREFIX" ] || fail "$PREFIX isn't a plain folder (a link?); nothing was installed."
NEW="$(mktemp "$PREFIX/.c2patool.XXXXXX" 2>/dev/null)" || fail "This script can't write to $PREFIX: run it again with sudo, or give --prefix."
cat "$BIN" > "$NEW" && chmod 0755 "$NEW" && mv -f "$NEW" "$PREFIX/c2patool" || { rm -f "$NEW"; fail "This script couldn't put c2patool in $PREFIX; nothing was installed."; }
rm -rf "$WORK"

"$PREFIX/c2patool" --version 2>/dev/null || true
echo "Installed: $PREFIX/c2patool"
echo "Set it up for the ability on this computer: metarush ability setup content-credentials --program c2patool=$PREFIX/c2patool"
exit 0
