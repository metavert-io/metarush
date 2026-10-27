#!/usr/bin/env bash
# MetaRush bootstrap — for people working on MetaRush itself: run once after
# cloning. Installs or verifies what a development checkout needs, builds the
# binary, and runs `metarush doctor`. People who use MetaRush never need it:
# `metarush setup` and `metarush components` add what they choose (D328).
#
#   ./bootstrap.sh            interactive (shows the plan, asks before installing)
#   ./bootstrap.sh --yes      non-interactive
#   ./bootstrap.sh --dry-run  only print what would happen
#   ./bootstrap.sh --no-models / --no-mongo / --skip-build
#
# It never uses sudo silently, never deletes anything, and never creates cloud
# resources or databases. Re-running is safe (idempotent).
set -euo pipefail

ROOT="$(cd "$(dirname "$0")" && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails bootstrap on macOS's bash 3.2 too; it ends with `exit 0`
YES=0; DRY=0; MODELS=1; MONGO=1; BUILD=1
for a in "$@"; do
  case "$a" in
    --yes|-y) YES=1 ;; --dry-run) DRY=1 ;; --no-models) MODELS=0 ;; --no-mongo) MONGO=0 ;; --skip-build) BUILD=0 ;;
    -h|--help) sed -n '2,13p' "$0"; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

bold() { printf '\033[1m%s\033[0m\n' "$*"; }
info() { printf '  %s\n' "$*"; }
warn() { printf '  \033[33m!\033[0m %s\n' "$*"; }
ok()   { printf '  \033[32m✓\033[0m %s\n' "$*"; }
run()  { if [ "$DRY" = 1 ]; then printf '  would run: %s\n' "$*"; else "$@"; fi; }
confirm() { [ "$YES" = 1 ] && return 0; [ "$DRY" = 1 ] && return 1; read -r -p "  $1 [y/N] " r; [[ "$r" =~ ^[Yy] ]]; }

OS="$(uname -s)"; ARCH="$(uname -m)"
NATIVE_ARCH="$ARCH"
if [ "$OS" = Darwin ] && [ "$(sysctl -n sysctl.proc_translated 2>/dev/null || echo 0)" = 1 ]; then
  NATIVE_ARCH=arm64
  warn "this shell is running under Rosetta; MetaRush will be built for arm64 anyway"
fi
bold "MetaRush bootstrap — $OS/$NATIVE_ARCH"

# ---------------------------------------------------------------- license notice
cat <<'EOF'

  About Remotion (the renderer MetaRush uses for motion graphics and final renders):
  Remotion is NOT MIT-licensed. It is free for individuals and for organizations of
  up to 3 people; larger companies need a Remotion Company License before using it.
  See https://www.remotion.dev/docs/license — each organization that installs
  MetaRush is responsible for its own Remotion license. MetaRush itself is MIT.

EOF

# ---------------------------------------------------------------- disk space
FREE_KB=$(df -Pk "$ROOT" | awk 'NR==2{print $4}')
FREE_GB=$((FREE_KB / 1024 / 1024))
if [ "$FREE_GB" -lt 15 ]; then
  warn "only ${FREE_GB} GB free — tools and models need ~6 GB, and video work needs much more"
else
  ok "${FREE_GB} GB free disk"
fi

# ---------------------------------------------------------------- package manager
PM=""; BREW=""
if [ "$OS" = Darwin ]; then
  if [ "$NATIVE_ARCH" = arm64 ] && [ -x /opt/homebrew/bin/brew ]; then BREW=/opt/homebrew/bin/brew
  elif [ "$NATIVE_ARCH" != arm64 ] && command -v brew >/dev/null; then BREW="$(command -v brew)"
  elif command -v brew >/dev/null; then
    BREW="$(command -v brew)"
    case "$BREW" in /usr/local/*) warn "only the Intel Homebrew (/usr/local) is installed; tools from it run under Rosetta (slower). Install native Homebrew: https://brew.sh";; esac
  fi
  [ -n "$BREW" ] && PM=brew
elif [ "$OS" = Linux ]; then
  if command -v apt-get >/dev/null; then PM=apt; elif command -v dnf >/dev/null; then PM=dnf; fi
fi
[ -z "$PM" ] && warn "no supported package manager found — missing tools will be listed for manual install"

have() { command -v "$1" >/dev/null 2>&1; }
MISSING=()
pkg_for() { # tool -> package name per manager
  case "$PM:$1" in
    brew:ffmpeg|brew:ffprobe) echo ffmpeg ;; brew:node) echo node@22 ;; brew:go) echo go ;; brew:whisper-cli) echo whisper-cpp ;;
    brew:mongod) echo mongodb/brew/mongodb-community ;;
    apt:ffmpeg|apt:ffprobe) echo ffmpeg ;; apt:node) echo nodejs ;; apt:go) echo golang-go ;;
    dnf:ffmpeg|dnf:ffprobe) echo ffmpeg ;; dnf:node) echo nodejs ;; dnf:go) echo golang ;;
    *) echo "" ;;
  esac
}

bold "Checking tools"
check_tool() { # name, min-version-hint
  if have "$1"; then ok "$1 $(command -v "$1")"; else warn "$1 missing"; MISSING+=("$1"); fi
}
check_tool go; check_tool node; check_tool npm; check_tool ffmpeg; check_tool ffprobe
if have go; then
  GOV=$(GOTOOLCHAIN=local go env GOVERSION 2>/dev/null | sed "s/go//"); GOARCH_BIN=$(GOTOOLCHAIN=local go env GOHOSTARCH 2>/dev/null || echo "")
  [ "$OS" = Darwin ] && [ "$NATIVE_ARCH" = arm64 ] && [ "$GOARCH_BIN" = amd64 ] && warn "Go is the Intel build; builds will target arm64 explicitly"
  info "go $GOV"
fi
if have node; then NODEV=$(node -v | sed 's/v//;s/\..*//'); [ "$NODEV" -lt 22 ] && { warn "Node $NODEV < 22"; MISSING+=(node); }; fi
if have ffmpeg && [ "$OS" = Darwin ] && [ "$NATIVE_ARCH" = arm64 ]; then
  file "$(command -v ffmpeg)" | grep -q x86_64 && warn "ffmpeg is an Intel build (works under Rosetta, slower). Native: brew install ffmpeg from /opt/homebrew"
fi
WHISPER_OK=0; have whisper-cli && { ok "whisper-cli $(command -v whisper-cli)"; WHISPER_OK=1; } || warn "whisper-cli (local transcription) missing"
# whisper-server from the same build, next to whisper-cli, keeps the whisper model loaded between clips (D121).
if [ "$WHISPER_OK" = 1 ]; then
  [ -x "$(dirname "$(command -v whisper-cli)")/whisper-server" ] && ok "whisper-server next to whisper-cli (the whisper model stays loaded between clips)" \
    || info "whisper-server is not next to whisper-cli: whisper.cpp loads its model for every clip (copy build/bin/whisper-server from the same whisper.cpp build next to it)"
fi
MONGOD_OK=0; have mongod || [ -x "$HOME/.local/mongodb/bin/mongod" ] && MONGOD_OK=1
[ "$MONGOD_OK" = 1 ] && ok "mongod available (local development/test database)" || info "mongod not installed (only needed for local development/tests)"

if [ ${#MISSING[@]} -gt 0 ] || [ "$WHISPER_OK" = 0 ]; then
  bold "Missing: ${MISSING[*]:-} $([ "$WHISPER_OK" = 0 ] && echo whisper-cli)"
  if [ "$PM" = brew ]; then
    PKGS=(); for t in "${MISSING[@]:-}"; do [ -n "$t" ] && p=$(pkg_for "$t") && [ -n "$p" ] && PKGS+=("$p"); done
    [ "$WHISPER_OK" = 0 ] && PKGS+=(whisper-cpp)
    PKGS=($(printf '%s\n' "${PKGS[@]}" | sort -u))
    if [ ${#PKGS[@]} -gt 0 ] && confirm "Install with Homebrew: ${PKGS[*]}?"; then run "$BREW" install "${PKGS[@]}"; fi
  elif [ "$PM" = apt ] || [ "$PM" = dnf ]; then
    PKGS=(); for t in "${MISSING[@]:-}"; do [ -n "$t" ] && p=$(pkg_for "$t") && [ -n "$p" ] && PKGS+=("$p"); done
    if [ ${#PKGS[@]} -gt 0 ]; then
      info "these need root: sudo $PM install ${PKGS[*]}"
      confirm "Run that with sudo now?" && run sudo "$PM" install -y "${PKGS[@]}"
    fi
    [ "$WHISPER_OK" = 0 ] && info "whisper.cpp: build from https://github.com/ggml-org/whisper.cpp (cmake -B build && cmake --build build) and put build/bin/whisper-cli and build/bin/whisper-server together on PATH"
  else
    info "install manually: Go ≥1.26, Node 22, ffmpeg, whisper.cpp"
  fi
fi

# ---------------------------------------------------------------- link imports (optional)
# yt-dlp downloads videos from YouTube, TikTok and X links (`metarush import <link>`,
# the dashboard's "Paste a link"); YouTube also needs a JavaScript runtime (Deno).
# Optional: without it, links can still be saved with the browser extension.
bold "Link imports (optional)"
YTDLP_OK=0; have yt-dlp && YTDLP_OK=1
[ "$YTDLP_OK" = 1 ] && ok "yt-dlp $(yt-dlp --version 2>/dev/null) $(command -v yt-dlp)" || info "yt-dlp not installed (imports from YouTube, TikTok and X links)"
DENO_OK=0; have deno && DENO_OK=1
[ "$DENO_OK" = 1 ] && ok "deno $(deno --version 2>/dev/null | head -1 | awk '{print $2}') (JavaScript runtime yt-dlp uses for YouTube)"
if [ "$YTDLP_OK" = 0 ] || [ "$DENO_OK" = 0 ]; then
  if [ "$PM" = brew ]; then
    # Homebrew's yt-dlp bundles its YouTube challenge scripts and pulls in Deno.
    if confirm "Install yt-dlp and Deno with Homebrew for link imports (optional)?"; then run "$BREW" install yt-dlp deno; fi
  else
    info "for link imports: install yt-dlp (https://github.com/yt-dlp/yt-dlp#installation, e.g. pipx install \"yt-dlp[default]\") and Deno (https://deno.com)"
  fi
fi

# ---------------------------------------------------------------- transcription model
MODELS_DIR="${METARUSH_MODELS_DIR:-$HOME/.local/share/metarush-models}"
WMODEL="$MODELS_DIR/whisper/ggml-large-v3-turbo.bin"
if [ "$MODELS" = 1 ]; then
  if [ -f "$WMODEL" ]; then ok "whisper model present ($WMODEL)"
  elif confirm "Download the whisper large-v3-turbo model (~1.6 GB) for local transcription?"; then
    run mkdir -p "$MODELS_DIR/whisper"
    URL="https://huggingface.co/ggerganov/whisper.cpp/resolve/main/ggml-large-v3-turbo.bin"
    run curl -fL --progress-bar -o "$WMODEL.part" "$URL"
    if [ "$DRY" = 0 ]; then
      WANT=$(curl -fsSL "https://huggingface.co/api/models/ggerganov/whisper.cpp/tree/main" | python3 -I -c 'import json,sys;print(next((f.get("lfs",{}).get("oid","") for f in json.load(sys.stdin) if f["path"]=="ggml-large-v3-turbo.bin"),""))' 2>/dev/null || true)
      GOT=$(shasum -a 256 "$WMODEL.part" | awk '{print $1}')
      if [ -n "$WANT" ] && [ "$WANT" != "$GOT" ]; then rm -f "$WMODEL.part"; echo "checksum mismatch for whisper model" >&2; exit 1; fi
      [ -n "$WANT" ] && ok "model checksum verified" || warn "could not fetch the published checksum; model not verified"
      mv "$WMODEL.part" "$WMODEL"
    fi
  fi
fi

# ---------------------------------------------------------------- default transcriber: Parakeet v2 via sherpa-onnx
# Measured as the best English recognizer on a development Mac (whisper.cpp stays the multilingual fallback).
SHERPA_VER=1.13.8
SHERPA_DIR="$HOME/.local/opt/sherpa-onnx"
PK_DIR="$MODELS_DIR/parakeet/sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8"
DIAR_DIR="$MODELS_DIR/diarization"
gh_asset_sha() { # repo tag asset -> sha256 published as the GitHub release asset digest
  curl -fsSL "https://api.github.com/repos/$1/releases/tags/$2" | python3 -I -c "import json,sys;d=json.load(sys.stdin);print(next((a.get('digest','').replace('sha256:','') for a in d['assets'] if a['name']=='$3'),''))" 2>/dev/null || true
}
fetch_verified() { # url dest expected-sha (may be empty)
  run curl -fL --progress-bar -o "$2.part" "$1"
  [ "$DRY" = 1 ] && return 0
  local got; got=$(shasum -a 256 "$2.part" | awk '{print $1}')
  if [ -n "$3" ] && [ "$3" != "$got" ]; then rm -f "$2.part"; echo "checksum mismatch for $1" >&2; exit 1; fi
  [ -n "$3" ] && ok "verified $(basename "$2")" || warn "no published checksum for $(basename "$2")"
  mv "$2.part" "$2"
}
if [ "$MODELS" = 1 ] && [ "$OS" = Darwin ] && [ "$NATIVE_ARCH" = arm64 ]; then
  if [ -x "$SHERPA_DIR/current/bin/sherpa-onnx-offline" ] && [ -f "$PK_DIR/encoder.int8.onnx" ]; then
    ok "Parakeet v2 + sherpa-onnx present"
  elif confirm "Install the default transcriber (sherpa-onnx $SHERPA_VER + Parakeet TDT v2, ~700 MB; speaker diarization models ~46 MB)?"; then
    TMPD=$(mktemp -d); trap 'rc="$(exit_status $?)"; rm -rf "$TMPD"; exit "$rc"' EXIT
    T=sherpa-onnx-v$SHERPA_VER-osx-arm64-shared-no-tts.tar.bz2
    fetch_verified "https://github.com/k2-fsa/sherpa-onnx/releases/download/v$SHERPA_VER/$T" "$TMPD/$T" "$(gh_asset_sha k2-fsa/sherpa-onnx v$SHERPA_VER $T)"
    run mkdir -p "$SHERPA_DIR/v$SHERPA_VER" "$HOME/.local/bin"
    run tar -xjf "$TMPD/$T" -C "$SHERPA_DIR/v$SHERPA_VER" --strip-components=1
    run ln -sfn "$SHERPA_DIR/v$SHERPA_VER" "$SHERPA_DIR/current"
    for b in sherpa-onnx-offline sherpa-onnx-offline-speaker-diarization sherpa-onnx-version; do run ln -sf "$SHERPA_DIR/current/bin/$b" "$HOME/.local/bin/$b"; done
    PT=sherpa-onnx-nemo-parakeet-tdt-0.6b-v2-int8.tar.bz2
    fetch_verified "https://github.com/k2-fsa/sherpa-onnx/releases/download/asr-models/$PT" "$TMPD/$PT" "$(gh_asset_sha k2-fsa/sherpa-onnx asr-models $PT)"
    run mkdir -p "$MODELS_DIR/parakeet"
    run tar -xjf "$TMPD/$PT" -C "$MODELS_DIR/parakeet"
    ST=sherpa-onnx-pyannote-segmentation-3-0.tar.bz2
    fetch_verified "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-segmentation-models/$ST" "$TMPD/$ST" ""
    run mkdir -p "$DIAR_DIR"
    run tar -xjf "$TMPD/$ST" -C "$DIAR_DIR"
    fetch_verified "https://github.com/k2-fsa/sherpa-onnx/releases/download/speaker-recongition-models/nemo_en_titanet_small.onnx" "$DIAR_DIR/nemo_en_titanet_small.onnx" "ad4a1802485d8b34c722d2a9d04249662f2ece5d28a7a039063ca22f515a789e"
    if [ "$DRY" = 0 ]; then
      SEG=$(shasum -a 256 "$DIAR_DIR/sherpa-onnx-pyannote-segmentation-3-0/model.onnx" | awk '{print $1}')
      [ "$SEG" = 220ad67ca923bef2fa91f2390c786097bf305bceb5e261d4af67b38e938e1079 ] && ok "verified pyannote segmentation model" || { echo "pyannote model checksum mismatch" >&2; exit 1; }
    fi
    info "Parakeet TDT v2 weights are CC-BY-4.0 (attribution to NVIDIA; see docs/licensing.md)"
  fi
elif [ "$MODELS" = 1 ]; then
  info "Parakeet/sherpa-onnx auto-install is macOS arm64 only for now; whisper.cpp will be used (see docs/installation.md for Linux)"
fi

# ---------------------------------------------------------------- JS dependencies + Remotion browser
bold "JavaScript workspaces (Remotion templates + dashboard)"
if have npm; then
  run npm ci --no-audit --no-fund --prefix "$ROOT" || run npm install --no-audit --no-fund --prefix "$ROOT"
  if [ -f "$ROOT/video/package.json" ]; then
    (cd "$ROOT/video" && run npx --yes remotion browser ensure) || warn "could not install Remotion's headless browser (renders will try again on first use)"
  fi
  run npm run build -w web --prefix "$ROOT" || warn "dashboard build failed (the binary will still run; see npm output)"
fi

# ---------------------------------------------------------------- build
if [ "$BUILD" = 1 ] && have go; then
  bold "Building metarush"
  GOARCH_T=$([ "$NATIVE_ARCH" = arm64 ] || [ "$NATIVE_ARCH" = aarch64 ] && echo arm64 || echo amd64)
  # Version, commit and build time stamped in (D130): 0.2.0 at a release tag, 0.2.0-dev.3 after it.
  run env GOARCH="$GOARCH_T" go build -C "$ROOT" -trimpath -ldflags "-s -w $("$ROOT/scripts/version.sh" --ldflags)" -o "$ROOT/bin/metarush" ./cmd/metarush
  [ "$DRY" = 0 ] && ok "built $ROOT/bin/metarush ($(file -b "$ROOT/bin/metarush" | cut -d, -f1-2))"
  # The on-screen index (Apple Vision, macOS): metarush-vision, built from tools/vision.
  if [ "$(uname -s)" = Darwin ] && have swiftc; then
    run swiftc -O -swift-version 5 -o "$ROOT/bin/metarush-vision" "$ROOT/tools/vision/main.swift" \
      && { [ "$DRY" = 1 ] || shasum -a 256 "$ROOT/tools/vision/main.swift" | awk '{print $1}' > "$ROOT/bin/metarush-vision.source-sha256"; } \
      && ok "built $ROOT/bin/metarush-vision (looks at what's on screen with Apple Vision; nothing is uploaded)" \
      || warn "could not build metarush-vision (metarush doctor tries again)"
  elif [ "$(uname -s)" = Darwin ]; then
    warn "no swiftc: install the Xcode Command Line Tools (xcode-select --install) so MetaRush can look at what's on screen in clips"
  fi
fi

# ---------------------------------------------------------------- local dev database (optional)
if [ "$MONGO" = 1 ] && [ "$MONGOD_OK" = 1 ]; then
  info "start a local dev/test MongoDB any time with: scripts/dev-mongo.sh start"
elif [ "$MONGO" = 1 ]; then
  info "no local MongoDB: point METARUSH_MONGODB_URI at Atlas or another server, or install MongoDB for local development"
fi
if [ "$OS" = Darwin ] && [ "$MONGO" = 1 ]; then
  # Developers' test fixtures (docs/testing.md → What a Mac needs for the fixtures).
  have magick || info "for MetaRush's own tests: the call fixtures need ImageMagick (brew install imagemagick)"
  T="${TMPDIR:-/tmp}"
  [ -f "${T%/}/metarush-faceid-test-cache/face_recognition_sface_2021dec.onnx" ] || [ -n "${METARUSH_FACE_MODEL:-}" ] ||
    info "for MetaRush's own tests: the face tests need the face model in \$TMPDIR/metarush-faceid-test-cache (docs/testing.md)"
fi

bold "Next steps"
cat <<EOF
  export PATH="$ROOT/bin:\$PATH"        # or copy bin/metarush somewhere on your PATH
  metarush setup                        # storage, a database (on this computer, or Atlas), the dashboard, Claude Code's skills, the components
  metarush components                   # what each optional component unlocks; metarush components add <name> adds one
  metarush doctor
  # or a development home rendering from this checkout: ./start.sh
EOF
if [ -x "$ROOT/bin/metarush" ] && [ "$DRY" = 0 ]; then
  echo; bold "metarush doctor (pre-init checks)"; "$ROOT/bin/metarush" doctor || true
fi
exit 0
