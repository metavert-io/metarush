#!/usr/bin/env bash
# Builds the fixture repository acme/widget as a bare repository under <dest>,
# for tests that bring in "https://github.com/acme/widget" with
# METARUSH_GIT_BASE=<dest> (never the network). Its files come from
# tests/fixtures/repo; a picture, a file larger than the snapshot's limit and a
# link are made here, so a snapshot shows what it leaves out. Fixed authors and
# dates make the same commits on every machine. With "second" it adds a commit
# that moves the worker loop down one line (refresh tests). With "art" it adds
# a commit with character art under art/characters (pictures kept from a
# repository's folders, D216: tests/fixtures/repo-art, and files that show what
# is left out and why); "art-second" then changes alice.png, removes bob.jpg and
# adds frank.png.
#   scripts/make-fixture-repo.sh <dest> [second | art | art-second]
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
DEST="${1:?usage: make-fixture-repo.sh <dest> [second | art | art-second]}"
STEP="${2:-}"
BARE="$DEST/acme/widget.git"
W="$(mktemp -d)"
trap 'rc="$(exit_status $?)"; rm -rf "$W"; exit "$rc"' EXIT
mkdir -p "$W/home"
# A clean identity and no global settings (signing, hooks) from whoever runs this.
export HOME="$W/home" GIT_CONFIG_NOSYSTEM=1
export GIT_AUTHOR_NAME="Ada Park" GIT_AUTHOR_EMAIL="ada@example.invalid" GIT_COMMITTER_NAME="Ada Park" GIT_COMMITTER_EMAIL="ada@example.invalid"
# No automatic maintenance: newer git runs it in the background after a commit, and it
# would still be writing in $W/src/.git when the trap removes the folder.
g() { git -c init.defaultBranch=main -c commit.gpgsign=false -c core.hooksPath=/dev/null -c protocol.file.allow=always \
  -c maintenance.auto=false -c gc.auto=0 "$@"; }
at() { export GIT_AUTHOR_DATE="$1" GIT_COMMITTER_DATE="$1"; }

if [ "$STEP" = "second" ]; then
  g clone -q "$BARE" "$W/src"
  cd "$W/src"
  { printf '// Copyright 2026 The Widget authors.\n'; cat internal/queue/queue.go; } > "$W/queue.go"
  mv "$W/queue.go" internal/queue/queue.go
  at "2026-10-10T09:00:00Z"
  g commit -qam "Name the authors in the queue"
  g push -q origin main
  echo "added a commit to $BARE"
  exit 0
fi

ART="$ROOT/tests/fixtures/repo-art"
if [ "$STEP" = "art" ]; then
  g clone -q "$BARE" "$W/src"
  cd "$W/src"
  C=art/characters
  mkdir -p "$C/more"
  cp "$ART/alice.png" "$ART/bob.jpg" "$ART/carol.svg" "$ART/blink.gif" "$C/"
  cp "$ART/dot.gif" "$C/more/dot.gif"
  cp "$ART/alice.png" "$C/hero"                       # a picture without an extension: told by its bytes
  printf 'not a picture\n' > "$C/notes.png"            # named like a picture, but text
  printf 'version https://git-lfs.github.com/spec/v1\noid sha256:%064d\nsize 2048\n' 0 > "$C/eve.png"   # a Git LFS pointer
  printf 'The cast of the teaser.\n' > "$C/README.md"  # text: the snapshot's, not a picture
  head -c 1200000 /dev/zero > "$C/big.png"             # larger than a 1 MB limit
  ln -s alice.png "$C/alias.png"                      # a link
  g add -A
  at "2026-10-12T09:00:00Z"
  g commit -qm "Add the character art"
  g push -q origin main
  echo "added the art to $BARE"
  exit 0
fi
if [ "$STEP" = "art-second" ]; then
  g clone -q "$BARE" "$W/src"
  cd "$W/src"
  C=art/characters
  cp "$ART/alice-2.png" "$C/alice.png"
  cp "$ART/frank.png" "$C/frank.png"
  g rm -q "$C/bob.jpg"
  g add -A
  at "2026-10-14T09:00:00Z"
  g commit -qm "New art for Alice; Frank joins; Bob leaves"
  g push -q origin main
  echo "added new art to $BARE"
  exit 0
fi

rm -rf "$BARE"
mkdir -p "$W/src"
cp -R "$ROOT/tests/fixtures/repo/." "$W/src/"
cd "$W/src"
g init -q
g add -A
at "2026-10-01T10:00:00Z"
g commit -qm "Widget: the queue and the worker loop"
# What a snapshot leaves out: a picture, a large file and a link.
mkdir -p assets testdata
printf '\x89PNG\r\n\x1a\n\x00\x00\x00\rIHDR\x00\x00\x00\x01\x00\x00\x00\x01\x08\x02\x00\x00\x00' > assets/logo.png
head -c 600000 /dev/zero | tr '\0' 'a' > testdata/big.txt
ln -s README.md docs-link.md
g add -A
at "2026-10-02T10:00:00Z"
g commit -qm "Add the logo and some test data"
mkdir -p "$(dirname "$BARE")"
g clone -q --bare "$W/src" "$BARE"
echo "made $BARE"
exit 0
