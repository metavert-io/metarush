#!/usr/bin/env bash
# Build MetaRush from this checkout and install it on this machine, or on another
# Mac (or Linux machine) over SSH:
#
#   build (version, commit and build time stamped in by scripts/version.sh: 0.2.0 at
#   the release tag, 0.2.0-dev.3 three commits after it) → replace the binary (a new
#   file, never overwritten in place: macOS kills a binary changed under its code
#   signature with exit 137) → skills install → renderer install (this version's
#   renderer) → migrate → restart the background service or the daemon → check that
#   the running daemon is the new build.
#
#   scripts/deploy.sh                       this machine (home $METARUSH_HOME or ~/.metarush, binary in ~/.local/bin)
#   scripts/deploy.sh --host render-1       another machine over SSH (its own home and ~/.local/bin)
#     --home DIR / --bin-dir DIR            this machine's installation home / binary folder
#     --remote-home DIR / --remote-bin DIR  the other machine's (default: its ~/.metarush and ~/.local/bin)
#     --service / --worker-only             install the background service if it is not installed yet
#                                           (--worker-only: the worker without the dashboard, for a Mac mini)
#     --no-skills  --no-restart  --skip-web
#     --dry-run                             print every step, change nothing (also prints the remote script)
#
# Remote steps (what --host runs, over SSH, after copying the binary for that machine's CPU to a temporary
# name in its home folder): move it into place as a new file, `metarush version` (checked against the version
# stamped in here), `skills install --user`, `renderer install`, `migrate`, then `service restart` (or `service
# install`, with --service), and print the daemon's version. The other machine needs Node.js 22+ and ffmpeg
# (Homebrew) and an initialized home (`metarush machine join <bundle>`); see docs/deployment.md.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin"   # Go and Node: PATH, else Homebrew

H="${METARUSH_HOME:-$HOME/.metarush}"; BIN_DIR="$HOME/.local/bin"
HOST=""; RHOME=""; RBIN=""; DRY=0; SKILLS=1; RESTART=1; WEB=1; SERVICE=0; WORKER_ONLY=0
while [ $# -gt 0 ]; do
  case "$1" in
    --home) H="${2:?}"; shift ;;
    --bin-dir) BIN_DIR="${2:?}"; shift ;;
    --host) HOST="${2:?}"; shift ;;
    --remote-home) RHOME="${2:?}"; shift ;;
    --remote-bin) RBIN="${2:?}"; shift ;;
    --service) SERVICE=1 ;;
    --worker-only) SERVICE=1; WORKER_ONLY=1 ;;
    --no-skills) SKILLS=0 ;;
    --no-restart) RESTART=0 ;;
    --skip-web) WEB=0 ;;
    --dry-run) DRY=1 ;;
    -h|--help) sed -n '2,25p' "$0"; exit 0 ;;
    *) echo "unknown option: $1 (see --help)" >&2; exit 2 ;;
  esac
  shift
done

bold() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
die() { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
run() { if [ "$DRY" = 1 ]; then printf '  would run: %s\n' "$*"; else "$@"; fi; }
J() { python3 -I -c "import json,sys;d=json.load(sys.stdin);print($1)"; }

cd "$ROOT"
# The version (D130): VERSION plus how far past its release tag this commit is — 0.2.0 at the tag v0.2.0
# (scripts/bump-version.sh cuts one), 0.2.0-dev.3 three commits after it — stamped in with the commit
# ("-dirty" with uncommitted changes) and the build time. A renderer is named by the release and its own
# digest, so rebuilds that leave video/ alone reuse it.
VERSION="$(scripts/version.sh)"
COMMIT="$(scripts/version.sh --commit)"
LDFLAGS="-s -w $(scripts/version.sh --ldflags)"

# build <goos> <goarch> <output>
build() {
  bold "Build $1/$2: MetaRush $VERSION ($COMMIT)"
  if [ "$WEB" = 1 ]; then
    [ -d node_modules ] || run npm ci --no-audit --no-fund
    run npm run build -w web >/dev/null
    ok "dashboard built"
  fi
  run env CGO_ENABLED=0 GOOS="$1" GOARCH="$2" go build -trimpath -ldflags "$LDFLAGS" -o "$3" ./cmd/metarush
  [ "$DRY" = 1 ] || ok "built $3 ($(file -b "$3" | cut -d, -f1))"
  ok "stamped: version $VERSION, commit $COMMIT (scripts/version.sh)"
}

native_arch() {
  local a; a="$(uname -m)"
  if [ "$(uname -s)" = Darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then a=arm64; fi
  case "$a" in arm64|aarch64) echo arm64 ;; *) echo amd64 ;; esac
}

# ---------------------------------------------------------------- another machine over SSH
if [ -n "$HOST" ]; then
  if [ "$DRY" = 1 ]; then
    UNAME="Darwin arm64"; warn "dry run: assuming $HOST is $UNAME (not contacted)"
  else
    UNAME="$(ssh -o BatchMode=yes "$HOST" 'uname -sm')" || die "cannot reach $HOST over SSH (key-based login is needed)"
  fi
  case "$UNAME" in
    "Darwin arm64") GOOS_T=darwin; GOARCH_T=arm64 ;;
    "Darwin x86_64") GOOS_T=darwin; GOARCH_T=amd64 ;;
    "Linux aarch64"|"Linux arm64") GOOS_T=linux; GOARCH_T=arm64 ;;
    "Linux x86_64") GOOS_T=linux; GOARCH_T=amd64 ;;
    *) die "unsupported machine: $UNAME" ;;
  esac
  OUT="$ROOT/bin/metarush-$GOOS_T-$GOARCH_T"
  build "$GOOS_T" "$GOARCH_T" "$OUT"
  TMPNAME=".metarush-deploy-$COMMIT-$$"
  q() { printf '%q' "$1"; }
  RH='"$HOME/.metarush"'; [ -n "$RHOME" ] && RH="$(q "$RHOME")"
  RB='"$HOME/.local/bin"'; [ -n "$RBIN" ] && RB="$(q "$RBIN")"
  SVC_INSTALL="service install"
  if [ "$WORKER_ONLY" = 1 ]; then SVC_INSTALL="service install --worker-only"; fi
  REMOTE='set -eu
export PATH="/opt/homebrew/bin:/usr/local/bin:$HOME/.local/bin:$PATH"
'"H=$RH; B=$RB; T=\"\$HOME/$TMPNAME\"; V=$(q "$VERSION")"'
mkdir -p "$B"
chmod 755 "$T"
mv -f "$T" "$B/metarush"   # a new file: the running daemon keeps its old one until it restarts
"$B/metarush" version | head -1
"$B/metarush" version --json | grep -q "\"version\": \"$V\"" || { echo "$B/metarush does not say it is $V"; exit 4; }
[ -f "$H/config.yaml" ] || { echo "no MetaRush installation at $H yet: run metarush machine join <bundle> there first"; exit 3; }
'
  if [ "$SKILLS" = 1 ]; then REMOTE="$REMOTE"'"$B/metarush" skills install --user
'; fi
  REMOTE="$REMOTE"'"$B/metarush" --home "$H" renderer install
"$B/metarush" --home "$H" migrate
'
  if [ "$RESTART" = 1 ]; then
    REMOTE="$REMOTE"'if "$B/metarush" --home "$H" service status --json | grep -q '"'"'"installed": true'"'"'; then
  "$B/metarush" --home "$H" service restart
'
    if [ "$SERVICE" = 1 ]; then REMOTE="$REMOTE"'else
  "$B/metarush" --home "$H" '"$SVC_INSTALL"'
'; fi
    REMOTE="$REMOTE"'fi
'
  fi
  REMOTE="$REMOTE"'"$B/metarush" --home "$H" service status
'
  bold "Deploy to $HOST"
  if [ "$DRY" = 1 ]; then
    printf '  would run: scp %s %s:%s\n' "$OUT" "$HOST" "$TMPNAME"
    printf '  would run on %s:\n%s\n' "$HOST" "$(sed 's/^/    /' <<<"$REMOTE")"
    printf '%s' "$REMOTE" | sh -n && ok "the remote script parses (sh -n)"
    exit 0
  fi
  scp -q "$OUT" "$HOST:$TMPNAME"
  ssh -o BatchMode=yes "$HOST" "$REMOTE"
  ok "deployed $COMMIT to $HOST"
  exit 0
fi

# ---------------------------------------------------------------- this machine
ARCH="$(native_arch)"; OS="$(uname -s | tr '[:upper:]' '[:lower:]')"
build "$OS" "$ARCH" "$ROOT/bin/metarush"
BIN="$BIN_DIR/metarush"

bold "Install $BIN"
run mkdir -p "$BIN_DIR"
# A new file moved into place: the running daemon keeps the old one until it restarts, and macOS never
# sees an executable changed in place (which it kills with exit 137).
run cp "$ROOT/bin/metarush" "$BIN_DIR/.metarush.new.$$"
run chmod 755 "$BIN_DIR/.metarush.new.$$"
run mv -f "$BIN_DIR/.metarush.new.$$" "$BIN"
if [ "$DRY" = 0 ]; then
  GOT="$("$BIN" version --json | J 'd["data"]["commit"] + " " + d["data"]["version"]')"
  [ "$GOT" = "$COMMIT $VERSION" ] || die "$BIN reports $GOT, not $COMMIT $VERSION"
  ok "$BIN is $("$BIN" version | head -1)"
fi

if [ "$DRY" = 0 ] && [ ! -f "$H/config.yaml" ]; then
  warn "no installation at $H yet: run \`metarush --home $H init\`, then this script again"
  exit 0
fi
MR() { run "$BIN" --home "$H" "$@"; }

if [ "$SKILLS" = 1 ]; then
  bold "Skills"
  run "$BIN" skills install --user
fi

bold "Renderer for this version"
MR renderer install
bold "Database"
MR migrate

if [ "$RESTART" = 0 ]; then
  ok "installed (--no-restart: the running daemon keeps the old build until it restarts)"
  exit 0
fi
bold "Restart"
if [ "$DRY" = 1 ]; then
  echo "  would run: $BIN --home $H service restart   (if the background service is installed; with --service, install it if not)"
  echo "  would run: $BIN --home $H stop && $BIN --home $H dashboard --no-open   (a daemon started by \`metarush dashboard\`)"
  exit 0
fi
SVC="$("$BIN" --home "$H" service status --json 2>/dev/null || echo '{}')"
INSTALLED="$(J 'd.get("data",{}).get("installed",False)' <<<"$SVC" 2>/dev/null || echo False)"
if [ "$INSTALLED" = True ]; then
  MR service restart
elif [ "$SERVICE" = 1 ]; then
  if [ "$WORKER_ONLY" = 1 ]; then MR service install --worker-only; else MR service install; fi
elif [ -f "$H/state/daemon.json" ] && kill -0 "$(J 'd["pid"]' <"$H/state/daemon.json")" 2>/dev/null; then
  MR stop >/dev/null
  MR dashboard --no-open >/dev/null
  ok "restarted the daemon"
else
  ok "no daemon is running (start it with \`metarush dashboard\`, or install the service with --service)"
  exit 0
fi

# Evidence: the daemon that answers is this build.
PORT="$(J 'd.get("port",0)' <"$H/state/daemon.json" 2>/dev/null || echo 0)"
MODE="$(J 'd.get("mode","full")' <"$H/state/daemon.json" 2>/dev/null || echo full)"
if [ "$MODE" = full ] && [ "$PORT" != 0 ]; then
  HEALTH="$(curl -sf "http://127.0.0.1:$PORT/api/health" || true)"
  RUNNING="$(J 'd.get("commit","") + " " + d.get("version","")' <<<"$HEALTH" 2>/dev/null || true)"
  [ "$RUNNING" = "$COMMIT $VERSION" ] || die "the daemon on port $PORT runs ${RUNNING:-?}, not $COMMIT $VERSION"
  ok "the daemon on port $PORT runs MetaRush $VERSION ($COMMIT)"
else
  ok "the worker runs (pid $(J 'd["pid"]' <"$H/state/daemon.json" 2>/dev/null || echo ?), $MODE)"
fi
