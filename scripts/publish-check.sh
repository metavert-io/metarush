#!/usr/bin/env bash
# publish-check.sh — the check before anything of MetaRush is published (D322, D323, docs/publish-check.md).
#
#   scripts/publish-check.sh [-r REPO] [-a] [DENYLIST ...]      (check-public.sh's arguments)
#
# First scripts/check-public.sh, the open-source-prep skill's generic check, kept verbatim (tracked files' contents
# and paths, commit messages, author and committer emails, deny-listed terms, gitleaks when installed; deny-lists in
# local/publish-denylist.txt and ~/.config/open-source-prep/denylist.txt, allowed emails in scripts/check-public.allow,
# skipped paths in scripts/check-public.skip). Then what only MetaRush checks, over the same repository's tracked
# files:
#   - work notes: pm/, local/, CLAUDE.local.md, a deny-list, and the file names local/publish-worknotes.txt lists
#     (they stay on disk, out of the repository; local.example/publish-worknotes.txt shows the shape); a .gitignore
#     line that names one of them (private notes are listed in .git/info/exclude, so their names stay out of the
#     public tree);
#   - tailnet host names (<host>.<tailnet>.ts.net), except example ones and the placeholders tail0000, tail1234,
#     tailXXXX and names with "tailnet" in them; paths in scripts/check-public.skip are left out;
#   - the repository's own secret patterns (scripts/secret-scan.sh, with its allow list and marker).
# Exit status 1 when either part found anything, 2 on a usage error.
set -uo pipefail
HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

repo=.
args=("$@")
while [ $# -gt 0 ]; do
  case "$1" in
    -r) repo="${2:?-r needs a folder}"; shift 2 ;;
    -a) shift ;;
    -h | --help) sed -n '2,18p' "$0"; exit 0 ;;
    *) break ;;
  esac
done
top="$(git -C "$repo" rev-parse --show-toplevel 2>/dev/null)" || { echo "not a git repository: $repo" >&2; exit 2; }

status=0
"$HERE/check-public.sh" ${args[@]+"${args[@]}"} || status=1

echo
echo "MetaRush's own checks:"
cd "$top" || exit 2
found=0
say() { found=$((found + 1)); printf '%s: [%s] %s\n' "$1" "$2" "$3"; }

# Tracked files, without the skipped ones (check-public.skip's globs, on the path or the file name).
skips=()
if [ -f scripts/check-public.skip ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in '' | '#'*) continue ;; esac
    skips+=("$line")
  done <scripts/check-public.skip
fi
files=()
while IFS= read -r -d '' f; do
  skip=
  for g in ${skips[@]+"${skips[@]}"}; do
    # shellcheck disable=SC2254
    case "$f" in $g) skip=1 ;; esac
    case "${f##*/}" in $g) skip=1 ;; esac
  done
  [ -z "$skip" ] && [ -f "$f" ] && files+=("$f")
done < <(git ls-files -z)

# Work notes (and local/, and any deny-list) tracked: one pass over the names. Which file names are work notes is this
# computer's to say: local/publish-worknotes.txt (one name per line, ignoring case) in the repository checked, else in
# this checkout, else the file PUBLISH_WORKNOTES names.
notes="claude.local.md publish-denylist.txt denylist.txt publish-worknotes.txt"
for list in "$top/local/publish-worknotes.txt" "$HERE/../local/publish-worknotes.txt" "${PUBLISH_WORKNOTES:-}"; do
  if [ -n "$list" ] && [ -f "$list" ]; then
    notes="$notes $(grep -v -e '^[[:space:]]*#' -e '^[[:space:]]*$' "$list" | tr '[:upper:]' '[:lower:]' | tr -d '\r' | tr '\n' ' ')"
    break
  fi
done
while IFS= read -r line; do
  [ -n "$line" ] && { found=$((found + 1)); printf '%s\n' "$line"; }
done < <(git ls-files | awk -v notes="$notes" '
  BEGIN { n = split(notes, a, " "); for (i = 1; i <= n; i++) note[a[i]] = 1 }
  { f = $0; base = f; sub(/.*\//, "", base); lb = tolower(base)
    if (f ~ /^local\.example\//) next  # the deny-list'\''s shape, with invented entries
    if (lb in note) print f ": [work-note] " base " stays on disk, out of the repository"
    else if (f ~ /^pm\//) print f ": [work-note] pm/ stays on disk, out of the repository"
    else if (f ~ /^local\//) print f ": [work-note] local/ is this computer'\''s, never tracked" }')
# A .gitignore that names a private note shows its name to everyone.
for f in ${files[@]+"${files[@]}"}; do
  [ "${f##*/}" = ".gitignore" ] || continue
  n=0
  while IFS= read -r line || [ -n "$line" ]; do
    n=$((n + 1))
    case "$line" in '' | '#'*) continue ;; esac
    name="$(printf '%s' "$line" | sed -e 's#^/##' -e 's#/$##' -e 's#^\*\*/##' | tr '[:upper:]' '[:lower:]')"
    case " pm $notes " in
      *" $name "*) say "$f:$n" "work-note" "the .gitignore names $line: list it in .git/info/exclude instead" ;;
    esac
  done <"$f"
done

# Tailnet host names, in one awk pass over the lines that have any.
tail_re='[A-Za-z0-9-]+(\.[A-Za-z0-9-]+)*\.ts\.net'
while IFS= read -r line; do
  [ -n "$line" ] && { found=$((found + 1)); printf '%s\n' "$line"; }
done < <(printf '%s\0' ${files[@]+"${files[@]}"} | xargs -0 grep -HnIE -e "$tail_re" -- 2>/dev/null |
  PC_TAIL="$tail_re" awk '
    function ok(m,   l) {
      l = tolower(m); sub(/\.ts\.net$/, "", l); sub(/.*\./, "", l)
      return (l == "example" || l ~ /^example-|-example$/ || l ~ /tailnet/ || l ~ /^tail(0+|1234|x+)$/ || l ~ /^-/)
    }
    { file = $0; sub(/:.*/, "", file); rest = substr($0, length(file) + 2); n = rest; sub(/:.*/, "", n)
      text = substr(rest, length(n) + 2); s = text
      while (match(s, ENVIRON["PC_TAIL"])) {
        m = substr(s, RSTART, RLENGTH)
        if (!ok(m)) printf "%s:%s: [tailnet-host: %s] %s\n", file, n, m, substr(text, 1, 110)
        s = substr(s, RSTART + RLENGTH)
      } }')

# The repository's own secret patterns (the secret scan, with its allow list and marker).
if ! out="$("$HERE/secret-scan.sh" "$top" 2>&1)"; then
  while IFS= read -r line; do
    case "$line" in "secret-scan: likely secret in "*) say "${line#secret-scan: likely secret in }" "secret" "a likely secret (not shown)" ;; esac
  done <<<"$out"
fi

echo
echo "$found finding(s) from MetaRush's own checks."
[ "$found" -gt 0 ] && status=1
exit "$status"
