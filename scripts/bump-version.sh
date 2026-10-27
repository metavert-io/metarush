#!/usr/bin/env bash
# Cut a MetaRush release (D130): set VERSION, move CHANGELOG.md's "## Unreleased" entries under
# "## X.Y.Z — 20 Oct 2026" (today), commit both, and tag the commit vX.Y.Z. Nothing is pushed.
#
#   scripts/bump-version.sh patch|minor|major   the next release after VERSION (0.2.0 → 0.2.1 | 0.3.0 | 1.0.0)
#   scripts/bump-version.sh 0.2.0               exactly this release (also the first one: VERSION already says
#                                               0.2.0 and nothing is tagged v0.2.0 yet)
#     --dry-run                                 say what it would do, change nothing
#
# It refuses a working tree with uncommitted changes (untracked files too), a release that is not newer than
# VERSION (or VERSION itself once it is tagged), a tag that exists already, and an empty "## Unreleased".
# Afterwards: scripts/deploy.sh installs it (the build at the tag says X.Y.Z); git push origin HEAD vX.Y.Z
# shares it; scripts/release.sh vX.Y.Z publishes the archives and the Homebrew tap.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"

WANT=""; DRY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --dry-run) DRY=1 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    major|minor|patch) WANT="$1" ;;
    [0-9]*) WANT="${1#v}" ;;
    v[0-9]*) WANT="${1#v}" ;;
    *) echo "unknown argument: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done
[ -n "$WANT" ] || { sed -n '2,13p' "$0"; exit 2; }

die() { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
release_re='^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$'

git rev-parse --is-inside-work-tree >/dev/null 2>&1 || die "this is not a git checkout"
[ -z "$(git status --porcelain)" ] || die "the working tree has uncommitted changes: commit or put them aside first"
CUR="$(tr -d ' \t\r\n' <VERSION)"
[[ "$CUR" =~ $release_re ]] || die "VERSION holds \"$CUR\", not a release such as 0.2.0"
tagged() { git rev-parse -q --verify "refs/tags/v$1" >/dev/null; }

# newer A B: A is a newer release than B (semver order: 0.3.0-rc1 < 0.3.0 < 0.3.1).
newer() {
  python3 -I - "$1" "$2" <<'PY'
import re, sys
def key(v):
    m = re.fullmatch(r"(\d+)\.(\d+)\.(\d+)(?:-([0-9A-Za-z.-]+))?", v)
    core = tuple(int(x) for x in m.groups()[:3])
    if m.group(4) is None:
        return core + ((1,),)
    return core + ((0,) + tuple((0, int(p), "") if p.isdigit() else (1, 0, p) for p in m.group(4).split(".")),)
sys.exit(0 if key(sys.argv[1]) > key(sys.argv[2]) else 1)
PY
}

case "$WANT" in
  major|minor|patch)
    tagged "$CUR" || die "VERSION says $CUR, which has no tag yet: cut it as it is with scripts/bump-version.sh $CUR (or give the release you want)"
    IFS=. read -r MA MI PA <<<"${CUR%%-*}"
    case "$WANT" in
      major) NEW="$((MA + 1)).0.0" ;;
      minor) NEW="$MA.$((MI + 1)).0" ;;
      patch) NEW="$MA.$MI.$((PA + 1))" ;;
    esac ;;
  *)
    [[ "$WANT" =~ $release_re ]] || die "\"$WANT\" is not a release such as 0.2.1"
    [[ "$WANT" != *dev* ]] || die "a release can't be a dev build (\"$WANT\")"
    NEW="$WANT"
    if [ "$NEW" = "$CUR" ]; then
      tagged "$CUR" && die "$CUR is released already (tag v$CUR): give a newer one, or patch|minor|major"
    else
      newer "$NEW" "$CUR" || die "$NEW is not newer than VERSION ($CUR)"
    fi ;;
esac
tagged "$NEW" && die "the tag v$NEW exists already"

DAY="$(LC_ALL=C date +'%-d %b %Y')"
HEADING="$NEW — $DAY"
# The changelog: the entries under "## Unreleased" move under the new heading; an empty "## Unreleased" stays on top.
python3 -I - CHANGELOG.md "$HEADING" "$DRY" <<'PY' || exit 1
import re, sys
path, heading, dry = sys.argv[1], sys.argv[2], sys.argv[3] == "1"
lines = open(path, encoding="utf-8").read().split("\n")
idx = [i for i, l in enumerate(lines) if re.fullmatch(r"##\s+Unreleased\s*", l, re.I)]
if len(idx) != 1:
    sys.exit('  CHANGELOG.md needs exactly one "## Unreleased" heading (found %d)' % len(idx))
start = idx[0] + 1
end = next((i for i in range(start, len(lines)) if lines[i].startswith("## ")), len(lines))
body = lines[start:end]
while body and not body[0].strip():
    body.pop(0)
while body and not body[-1].strip():
    body.pop()
if not body:
    sys.exit('  nothing under "## Unreleased" in CHANGELOG.md: say what changed first')
entries = sum(1 for l in body if l.startswith(("- ", "* ")))
rest = lines[end:]
out = lines[:start] + ["", "## " + heading] + body + ([""] if rest else []) + rest
print("  CHANGELOG.md: %d entries move from \"Unreleased\" to \"%s\"" % (entries, heading))
if not dry:
    open(path, "w", encoding="utf-8").write("\n".join(out))
PY

# The Claude Code plugin says the release too (D320): its manifest's "version", which `metarush skills package-plugin`
# writes from VERSION and a Go test checks.
PLUGIN_JSON="plugins/claude-code/.claude-plugin/plugin.json"
if [ "$DRY" = 1 ]; then
  echo "  would write VERSION: $NEW (was $CUR)"
  [ -f "$PLUGIN_JSON" ] && echo "  would write the plugin's version in $PLUGIN_JSON: $NEW"
  echo "  would run: git commit -m 'MetaRush $NEW' (VERSION, CHANGELOG.md) as $(git config user.name) <$(git config user.email)>"
  echo "  would run: git tag -a v$NEW -m 'MetaRush $NEW'"
  echo "Dry run: nothing changed."
  exit 0
fi
printf '%s\n' "$NEW" >VERSION
git add VERSION CHANGELOG.md
if [ -f "$PLUGIN_JSON" ]; then
  python3 -I - "$PLUGIN_JSON" "$NEW" <<'PY' || exit 1
import re, sys
path, new = sys.argv[1], sys.argv[2]
text = open(path, encoding="utf-8").read()
text, n = re.subn(r'^(  "version": )"[^"]*"', lambda m: m.group(1) + '"' + new + '"', text, count=1, flags=re.M)
if n != 1:
    sys.exit("  %s has no version line" % path)
open(path, "w", encoding="utf-8").write(text)
PY
  git add "$PLUGIN_JSON"
fi
git commit -q -m "MetaRush $NEW" -m "VERSION and CHANGELOG.md for $NEW (scripts/bump-version.sh)."
git tag -a "v$NEW" -m "MetaRush $NEW"
ok "VERSION $NEW, commit $(git rev-parse --short HEAD) as $(git config user.email), tag v$NEW"
if [ "$(scripts/version.sh)" = "$NEW" ]; then ok "a build of this commit says $NEW"; else echo "  ! scripts/version.sh says $(scripts/version.sh), not $NEW" >&2; fi
echo
echo "Nothing was pushed. Next:"
printf '  %-32s %s\n' "scripts/deploy.sh" "install it on this computer (and --host <name> for another)" \
  "git push origin HEAD v$NEW" "share the commit and the tag" \
  "scripts/release.sh v$NEW" "publish the release archives and the Homebrew tap"
