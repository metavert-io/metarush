#!/usr/bin/env bash
# The version of this checkout (D130), from VERSION (the one place the number is
# written) and git:
#
#   at the release tag v<VERSION>, nothing uncommitted   0.2.0
#   N commits after that tag (or uncommitted changes)   0.2.0-dev.N
#   no tag v<VERSION> behind this commit, or no git     0.2.0-dev
#
#   scripts/version.sh               the version
#   scripts/version.sh --commit      the short commit ("-dirty" with uncommitted changes to tracked files)
#   scripts/version.sh --ldflags     -X flags that stamp version, commit and build time into internal/version
#   scripts/version.sh --json        {"version", "release", "devCount", "commit", "date"}
#
# deploy.sh, release.sh (through goreleaser), bootstrap.sh and start.sh build with --ldflags; a plain
# `go build`/`go run`/`go test` says <VERSION>-dev instead (internal/version). scripts/bump-version.sh
# cuts a release: VERSION, CHANGELOG.md, a commit and the tag.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PKG=github.com/metavert-io/metarush/internal/version

REL="$(tr -d ' \t\r\n' <"$ROOT/VERSION")"
[[ "$REL" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] && [[ "$REL" != *dev* ]] ||
  { echo "VERSION must hold a release such as 0.2.0, not \"$REL\"" >&2; exit 2; }

git_() { git -C "$ROOT" "$@"; }
COMMIT=unknown; VERSION="$REL-dev"; COUNT=""
if git_ rev-parse --is-inside-work-tree >/dev/null 2>&1 && git_ rev-parse -q --verify HEAD >/dev/null; then
  COMMIT="$(git_ rev-parse --short=7 HEAD)"
  DIRTY=0
  [ -z "$(git_ status --porcelain --untracked-files=no)" ] || { DIRTY=1; COMMIT="$COMMIT-dirty"; }
  TAG="v$REL"
  if git_ rev-parse -q --verify "refs/tags/$TAG^{commit}" >/dev/null && git_ merge-base --is-ancestor "$TAG^{commit}" HEAD; then
    COUNT="$(git_ rev-list --count "$TAG^{commit}..HEAD")"
    if [ "$COUNT" = 0 ] && [ "$DIRTY" = 0 ]; then VERSION="$REL"; COUNT=""; else VERSION="$REL-dev.$COUNT"; fi
  fi
fi
DATE="$(date -u +%Y-%m-%dT%H:%M:%SZ)"

case "${1:-}" in
  "") echo "$VERSION" ;;
  --commit) echo "$COMMIT" ;;
  --ldflags) echo "-X $PKG.Version=$VERSION -X $PKG.Commit=$COMMIT -X $PKG.Date=$DATE" ;;
  --json) printf '{"version": "%s", "release": "%s", "devCount": %s, "commit": "%s", "date": "%s"}\n' \
    "$VERSION" "$REL" "${COUNT:-null}" "$COMMIT" "$DATE" ;;
  -h|--help) sed -n '2,17p' "$0" ;;
  *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
esac
