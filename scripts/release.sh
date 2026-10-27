#!/usr/bin/env bash
# MetaRush release builds (D77): goreleaser archives for macOS (arm64, amd64) and
# Linux (amd64, arm64) with SHA-256 checksums. Each archive carries the binary,
# the renderer package (also embedded in the binary, D75), the skills and the docs.
# Publishing is gated on a passing dry run.
#
#   scripts/release.sh --snapshot         dry run only: build every archive from this commit and verify them
#                                         (versioned like any build: scripts/version.sh, e.g. 0.2.0-dev.3)
#   scripts/release.sh v0.2.0             the dry run, then push the tag, build from it, verify again, publish the
#                                         GitHub release with gh (a pre-release when the version has a suffix)
#                                         and update the Homebrew tap (cask, formula, README)
#     --no-tap          leave the Homebrew tap alone
#     --tap-only        only rewrite the tap for this already published release (nothing is built or published)
#     --tap-repo R      the tap repository (default <owner>/homebrew-<repo>; created when missing)
#     --plan            after the dry run, print the publishing steps instead of running them
#
# The release is cut first, by scripts/bump-version.sh 0.2.0 (D130): VERSION says 0.2.0, CHANGELOG.md has its
# "## 0.2.0 — <day>" section, and the tag v0.2.0 is on this commit. release.sh publishes exactly that.
# Needs goreleaser, go, node/npm, python3, gh (signed in, allowed to push to the repository) and a clean tree.
# macOS builds are signed and notarized (D257, scripts/macos-sign.sh) when this computer is set up for it: a Developer
# ID and a notarization profile in the environment or the git-ignored release-signing.env (docs/installation.md,
# "Signing and notarizing releases"). Without them the builds are not signed, one line says so, and everything else
# runs the same.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew

MODE=""; VERSION=""; TAP=1; TAP_REPO=""; PLAN=0; TAP_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --snapshot) MODE=snapshot ;;
    --no-tap) TAP=0 ;;
    --tap-only) TAP_ONLY=1 ;;
    --plan) PLAN=1 ;;
    --tap-repo) TAP_REPO="${2:?--tap-repo needs owner/name}"; shift ;;
    --tap-repo=*) TAP_REPO="${1#*=}" ;;
    v[0-9]*) MODE=publish; VERSION="$1" ;;
    -h|--help) sed -n '2,20p' "$0"; exit 0 ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$MODE" ] || { sed -n '2,20p' "$0"; exit 2; }

bold() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
die() { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
need() { command -v "$1" >/dev/null 2>&1 || die "$1 is not installed ($2)"; }
need goreleaser "brew install goreleaser"
need go "https://go.dev/dl"
need npm "Node.js 22"
need python3 "python3"
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }
# What this computer does with macOS builds: none | signed | notarized (scripts/macos-sign.sh --check).
SIGNING="$("$ROOT/scripts/macos-sign.sh" --check)"
if [ "$SIGNING" != none ] && [ "$(uname -s)" != Darwin ]; then die "signing macOS builds needs a Mac (METARUSH_SIGN_IDENTITY is set)"; fi

# verify <version>: the archives, their checksums, their contents, and the native binary.
verify() {
  local want="$1" d="$ROOT/dist" os arch a top list f n
  (cd "$d" && shasum -a 256 -c checksums.txt >/dev/null) || die "checksums.txt does not match the archives"
  n=$(find "$d" -maxdepth 1 -name 'metarush_*.tar.gz' | wc -l | tr -d ' ')
  [ "$n" = 4 ] || die "expected 4 archives, found $n"
  for os in darwin linux; do
    for arch in arm64 amd64; do
      top="metarush_${want}_${os}_${arch}"
      a="$d/$top.tar.gz"
      [ -f "$a" ] || die "missing $a"
      list="$(tar -tzf "$a")"
      for f in metarush README.md LICENSE CHANGELOG.md docs/installation.md docs/deployment.md skills/video-render/SKILL.md \
        skills/metarush-improve/SKILL.md renderer/package.json renderer/package-lock.json renderer/web/package.json \
        renderer/video/render/render.mjs renderer/video/render/browser.mjs renderer/schema/templates.json; do
        grep -qx "$top/$f" <<<"$list" || die "$top.tar.gz lacks $f"
      done
      if grep -q '/node_modules/' <<<"$list"; then die "$top.tar.gz contains node_modules"; fi
      case "$os/$arch" in
        darwin/arm64) want_file="Mach-O 64-bit executable arm64" ;;
        darwin/amd64) want_file="Mach-O 64-bit executable x86_64" ;;
        linux/arm64) want_file="ELF 64-bit LSB executable, ARM aarch64" ;;
        linux/amd64) want_file="ELF 64-bit LSB executable, x86-64" ;;
      esac
      bin_dir="$(find "$d" -maxdepth 1 -type d -name "metarush_${os}_${arch}*" | head -1)"
      file -b "$bin_dir/metarush" | grep -q "$want_file" || die "$os/$arch binary is not $want_file"
      if [ "$os" = darwin ] && [ "$SIGNING" != none ]; then
        # The archive's binary is the signed one (signing happens before archiving).
        tar -xzf "$a" -C "$d" "$top/metarush"
        codesign --verify --strict "$d/$top/metarush" 2>/dev/null || die "$top.tar.gz: the macOS binary isn't signed"
        codesign -dvv "$d/$top/metarush" 2>&1 | grep -q 'flags=.*runtime' || die "$top.tar.gz: signed without the hardened runtime"
        rm -rf "${d:?}/$top"
      fi
    done
  done
  # The native binary runs, reports the version, and carries exactly the archive's renderer package.
  local goos goarch x bin got
  goos="$(go env GOOS)"; goarch="$(go env GOARCH)"
  x="$(mktemp -d)"
  tar -xzf "$d/metarush_${want}_${goos}_${goarch}.tar.gz" -C "$x"
  bin="$x/metarush_${want}_${goos}_${goarch}/metarush"
  got="$("$bin" version --json | J 'd["data"]["version"]')"
  [ "$got" = "$want" ] || die "the binary says it is $got, not $want"
  "$bin" renderer export --dir "$x/export" --json >"$x/export.json"
  diff -r "$x/export" "$(dirname "$bin")/renderer" >/dev/null || die "the archive's renderer folder differs from the binary's own renderer package"
  ok "4 archives, checksums verified, version $want, renderer package $(J 'd["data"]["id"]' <"$x/export.json") (Remotion $(J 'd["data"]["remotion"]' <"$x/export.json"))"
  case "$SIGNING" in
    notarized) ok "macOS builds signed (hardened runtime) and notarized" ;;
    signed) ok "macOS builds signed (hardened runtime); not notarized: no notarization profile on this computer" ;;
    *) echo "  macOS builds not signed: no Developer ID is set up on this computer (docs/installation.md)" ;;
  esac
  rm -rf "$x"
}

dry_run() {
  # A snapshot is versioned like every build (D130, .goreleaser.yaml's snapshot.version_template):
  # scripts/version.sh — 0.2.0 at the tag v0.2.0, 0.2.0-dev.3 three commits after it.
  METARUSH_VERSION="$(scripts/version.sh)"
  export METARUSH_VERSION
  bold "Dry run: goreleaser release --snapshot --clean (MetaRush $METARUSH_VERSION)"
  goreleaser release --snapshot --clean
  local got
  got="$(J 'd["version"]' <"$ROOT/dist/metadata.json")"
  [ "$got" = "$METARUSH_VERSION" ] || die "goreleaser built $got, not $METARUSH_VERSION"
  verify "$got"
}

if [ "$MODE" = snapshot ]; then
  dry_run
  echo; echo "Dry run passed. Nothing was tagged or published."
  exit 0
fi

# ---------------------------------------------------------------- publishing
[[ "$VERSION" =~ ^v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]] || die "the version must look like v0.1.0 or v0.1.0-rc1"
need gh "brew install gh, then gh auth login"
gh auth status >/dev/null 2>&1 || die "gh is not signed in (gh auth login)"
REPO="$(gh repo view --json nameWithOwner -q .nameWithOwner)"
[ "$(gh api "repos/$REPO" -q .permissions.push)" = true ] || die "this GitHub account cannot publish to $REPO"
[ -n "$(git config user.email)" ] || die "git user.email is not set"
TAP_REPO="${TAP_REPO:-${REPO%%/*}/homebrew-${REPO##*/}}"

# update_tap: the cask, the formula and the README of the tap, from the published release's asset IDs and
# its checksums.txt, committed through the GitHub contents API as this git identity.
update_tap() {
  bold "Homebrew tap $TAP_REPO"
  if ! gh repo view "$TAP_REPO" >/dev/null 2>&1; then
    gh repo create "$TAP_REPO" --public --description "Homebrew tap for MetaRush"
    ok "created the tap repository"
  fi
  local x
  x="$(mktemp -d)"
  gh api "repos/$REPO/releases/tags/$VERSION" >"$x/release.json"
  gh release download "$VERSION" -R "$REPO" -p checksums.txt -D "$x"
  python3 -I "$ROOT/scripts/brew-formula.py" "$REPO" "$VERSION" "$x/release.json" "$x/checksums.txt" "$x/tap" "$TAP_REPO"
  put_file Casks/metarush.rb "$x/tap/Casks/metarush.rb" "metarush ${VERSION#v} (cask)"
  put_file Formula/metarush.rb "$x/tap/Formula/metarush.rb" "metarush ${VERSION#v}"
  put_file README.md "$x/tap/README.md" "How to install MetaRush from this tap"
  ok "Casks/metarush.rb and Formula/metarush.rb are for ${VERSION#v}"
  rm -rf "$x"
}
# put_file <path in the tap> <local file> <commit message>
put_file() {
  local sha="" args content
  content="$(base64 <"$2" | tr -d '\n')"
  # (gh prints the error body on stdout for a 404, so test the exit status first)
  if gh api "repos/$TAP_REPO/contents/$1" >/dev/null 2>&1; then
    sha="$(gh api "repos/$TAP_REPO/contents/$1" -q .sha)"
    if [ "$(gh api "repos/$TAP_REPO/contents/$1" -q .content | tr -d '\n')" = "$content" ]; then
      echo "  $1: unchanged"
      return 0
    fi
  fi
  args=(-X PUT "repos/$TAP_REPO/contents/$1" -f "message=$3" -f "content=$content"
    -f "committer[name]=$(git config user.name)" -f "committer[email]=$(git config user.email)"
    -f "author[name]=$(git config user.name)" -f "author[email]=$(git config user.email)")
  [ -n "$sha" ] && args+=(-f "sha=$sha")
  echo "  $1: $(gh api "${args[@]}" -q '.commit.sha[0:7]')"
}

if [ "$TAP_ONLY" = 1 ]; then
  gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1 || die "there is no release $VERSION to point the tap at"
  update_tap
  exit 0
fi

[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes"
# The release was cut by scripts/bump-version.sh (D130): VERSION, its changelog section and the tag, on this commit.
CUT="scripts/bump-version.sh ${VERSION#v}"
[ "$(tr -d ' \t\r\n' <VERSION)" = "${VERSION#v}" ] || die "VERSION says $(tr -d ' \t\r\n' <VERSION), not ${VERSION#v}: cut the release first ($CUT)"
grep -q "^## ${VERSION#v} " CHANGELOG.md || die "CHANGELOG.md has no \"## ${VERSION#v} — <day>\" section: cut the release first ($CUT)"
git rev-parse -q --verify "refs/tags/$VERSION" >/dev/null || die "there is no tag $VERSION: cut the release first ($CUT)"
[ "$(git rev-parse "$VERSION^{commit}")" = "$(git rev-parse HEAD)" ] || die "the tag $VERSION is not on this commit: check it out first (git checkout $VERSION)"
[ "$(scripts/version.sh)" = "${VERSION#v}" ] || die "a build of this commit says $(scripts/version.sh), not ${VERSION#v}"
gh release view "$VERSION" -R "$REPO" >/dev/null 2>&1 && die "the release $VERSION exists already"
PRE=""; case "$VERSION" in *-*) PRE="--prerelease" ;; esac

dry_run

if [ "$PLAN" = 1 ]; then
  bold "Publishing plan (--plan: nothing done)"
  echo "  git push origin refs/tags/$VERSION   (the tag scripts/bump-version.sh made)"
  echo "  goreleaser release --clean  (from the tag) and verify"
  echo "  gh release create $VERSION -R $REPO --verify-tag $PRE dist/metarush_*.tar.gz dist/checksums.txt"
  [ "$TAP" = 1 ] && echo "  update $TAP_REPO: Casks/metarush.rb, Formula/metarush.rb, README.md (created when missing)"
  exit 0
fi

bold "Push the tag $VERSION ($(git rev-parse --short HEAD), $(git config user.email))"
git push origin "refs/tags/$VERSION"
ok "pushed the tag"

bold "Build from the tag"
GITHUB_TOKEN="${GITHUB_TOKEN:-$(gh auth token)}" goreleaser release --clean
verify "${VERSION#v}"

bold "Publish the GitHub release"
NOTES="$ROOT/dist/release-notes.md"
if [ "$SIGNING" = notarized ]; then
  SIGN_NOTE="The macOS builds are signed with a Developer ID and notarized by Apple."
else
  SIGN_NOTE='**Not notarized** (no Developer ID set up for this release). A binary downloaded with a browser is blocked by macOS
the first time: run `xattr -d com.apple.quarantine metarush` once, or allow it in System Settings → Privacy & Security.
Homebrew installs are not affected.'
fi
cat >"$NOTES" <<EOF
MetaRush ${VERSION#v}$([ -n "$PRE" ] && echo " (pre-release)").

Archives for macOS (Apple Silicon and Intel) and Linux (arm64 and x86-64). Each holds the \`metarush\` binary, the
renderer package it sets up on first use (\`metarush renderer install\`), the Claude Code skills and the docs.
\`checksums.txt\` lists their SHA-256.

**Install**: \`brew install ${TAP_REPO%%/*}/${TAP_REPO#*/homebrew-}/metarush\` (see docs/installation.md), or download an archive, check it with \`shasum -a 256 -c checksums.txt\`,
and put \`metarush\` on your PATH. Then \`metarush setup\` sets it up in a few minutes (storage, a database on this computer,
the dashboard, Claude Code's skills).

$SIGN_NOTE

What changed: CHANGELOG.md in the archive.
EOF
gh release create "$VERSION" -R "$REPO" --verify-tag $PRE --title "MetaRush ${VERSION#v}" --notes-file "$NOTES" \
  "$ROOT"/dist/metarush_*.tar.gz "$ROOT/dist/checksums.txt"
# Check what was published against what was built.
X="$(mktemp -d)"
gh release download "$VERSION" -R "$REPO" -p checksums.txt -D "$X"
cmp -s "$X/checksums.txt" "$ROOT/dist/checksums.txt" || die "the published checksums.txt differs from the built one"
N="$(gh release view "$VERSION" -R "$REPO" --json assets -q '.assets | length')"
[ "$N" = 5 ] || die "the release has $N files, expected 5"
ok "published $VERSION to $REPO: 4 archives and checksums.txt"
rm -rf "$X"

if [ "$TAP" = 0 ]; then exit 0; fi

update_tap
echo
echo "Released $VERSION. Install with: brew tap ${TAP_REPO%%/*}/${TAP_REPO#*/homebrew-} https://github.com/$TAP_REPO && brew install --cask ${TAP_REPO%%/*}/${TAP_REPO#*/homebrew-}/metarush"
