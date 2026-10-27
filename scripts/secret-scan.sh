#!/usr/bin/env bash
# secret-scan.sh — fail when a tracked file looks like it contains a real secret.
#
# Scans every file git tracks (plus files staged for commit) for likely
# credentials: MongoDB URIs with user:password@, Tigris access keys and secrets
# (tid_… / tsec_…), AWS access keys and secret assignments, sk_… API keys
# (ElevenLabs, Stripe and similar), xi-api-key assignments, Google API keys
# (AIza…, Gemini) and x-goog-api-key assignments, and PEM private keys.
#
# Documented placeholders and test fixtures are allowed by
#   - scripts/secret-scan.allow: one extended regex per line, matched against
#     "path:line:text" of each hit (comments start with #), or
#   - a "secret-scan: allow" marker on the same line (for test fixtures).
#
# No dependencies beyond git, grep and bash. Run it locally before pushing; CI runs it.
#   scripts/secret-scan.sh            # scan the repository this script lives in
#   scripts/secret-scan.sh <dir>      # scan another checkout (tests)
set -euo pipefail
ROOT="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
cd "$ROOT"
ALLOW="$ROOT/scripts/secret-scan.allow"

PATTERNS=(
  'mongodb(\+srv)?://[^:/?#@[:space:]"'"'"'<>]+:[^@/[:space:]"'"'"'<>]+@'
  '(^|[^A-Za-z0-9_])tid_[A-Za-z0-9_-]{16,}'
  '(^|[^A-Za-z0-9_])tsec_[A-Za-z0-9_+/=-]{16,}'
  '(^|[^A-Z0-9])(AKIA|ASIA)[0-9A-Z]{16}([^A-Z0-9]|$)'
  '[Aa][Ww][Ss]_?[Ss][Ee][Cc][Rr][Ee][Tt]_?[Aa][Cc][Cc][Ee][Ss][Ss]_?[Kk][Ee][Yy][[:space:]"'"'"']*[=:][[:space:]"'"'"']*[A-Za-z0-9/+=]{40}'
  '(^|[^A-Za-z0-9_])sk_(live_|test_)?[A-Za-z0-9]{20,}'
  '[Xx][Ii]-[Aa][Pp][Ii]-[Kk][Ee][Yy]["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9_]{24,}'
  '(^|[^A-Za-z0-9_])AIza[0-9A-Za-z_-]{35}'
  '[Xx]-[Gg][Oo][Oo][Gg]-[Aa][Pp][Ii]-[Kk][Ee][Yy]["'"'"']?[[:space:]]*[:=][[:space:]]*["'"'"']?[A-Za-z0-9_-]{30,}'
  '-----BEGIN ([A-Z0-9]+ )*PRIVATE KEY-----'
)
args=()
for p in "${PATTERNS[@]}"; do args+=(-e "$p"); done

# Tracked files plus anything staged (so a pre-commit run sees new files).
files=()
while IFS= read -r -d '' f; do
  case "$f" in scripts/secret-scan.sh|scripts/secret-scan.allow) continue ;; esac # the scanner's own patterns
  [ -f "$f" ] && files+=("$f")
done < <( { git ls-files -z; git diff --cached --name-only -z --diff-filter=ACMR; } | sort -zu )
[ "${#files[@]}" -eq 0 ] && { echo "secret-scan: no tracked files"; exit 0; }

allow_re=()
if [ -f "$ALLOW" ]; then
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in ''|'#'*) continue ;; esac
    allow_re+=("$line")
  done < "$ALLOW"
fi

hits=0
while IFS= read -r hit; do
  case "$hit" in *"secret-scan: allow"*) continue ;; esac
  allowed=0
  for re in ${allow_re[@]+"${allow_re[@]}"}; do
    if printf '%s\n' "$hit" | grep -qE -- "$re"; then allowed=1; break; fi
  done
  [ "$allowed" -eq 1 ] && continue
  # Never echo the secret itself: show the location and a masked excerpt.
  loc="${hit%%:*}"; rest="${hit#*:}"; lineno="${rest%%:*}"
  echo "secret-scan: likely secret in $loc:$lineno"
  hits=$((hits + 1))
done < <(printf '%s\0' "${files[@]}" | xargs -0 grep -nIE "${args[@]}" -- 2>/dev/null || true)

if [ "$hits" -gt 0 ]; then
  echo "secret-scan: $hits finding(s). Remove the secret (and rotate it if it was ever pushed), or, for a documented"
  echo "placeholder or test fixture, add a regex to scripts/secret-scan.allow or a 'secret-scan: allow' comment."
  exit 1
fi
echo "secret-scan: ${#files[@]} tracked files, no likely secrets"
