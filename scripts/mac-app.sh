#!/usr/bin/env bash
# MetaRush.app (D470–D479, docs/mac-app.md): build it, sign an update, write its appcast entry. Nothing is published:
# putting the archive and appcast.xml on a release is a later step of scripts/release.sh (docs/mac-app.md).
#
#   scripts/mac-app.sh build [--arch universal|arm64|x86_64] [--out DIR] [--skip-web] [--no-dmg]
#                            [--feed-url URL] [--bundle-id ID] [--ed-public-key KEY | --key-from-keychain]
#       MetaRush.app in DIR (default dist/mac): the Swift app (Command Line Tools alone), the metarush program
#       stamped by scripts/version.sh for each architecture (lipo'd: universal is the default, as release.sh builds
#       both), Sparkle.framework, the icon, Info.plist; signed with the Developer ID in METARUSH_SIGN_IDENTITY when
#       this Mac has one (environment or release-signing.env; notarized and stapled too with METARUSH_NOTARY_PROFILE),
#       else ad hoc. Then MetaRush-<version>.zip (what Sparkle downloads) and MetaRush-<version>.dmg (the first
#       download). The update key is the EdDSA public key: given, from METARUSH_SPARKLE_PUBLIC_KEY, or read from this
#       Mac's keychain with --key-from-keychain (Sparkle's generate_keys -p); without one the app never updates. A key
#       given is checked against the keychain's when this Mac has one; the feed address must be https.
#   scripts/mac-app.sh sign-update <archive.zip> [--ed-key-file FILE|-] [--account NAME]
#       the archive's EdDSA signature, by Sparkle's sign_update: the private key from this Mac's keychain (made once
#       with `scripts/mac-app.sh tools` then generate_keys; docs/mac-app.md), or a key file (tests: made at run time)
#   scripts/mac-app.sh appcast <archive.zip> [--url URL] [--out FILE] [--ed-key-file FILE|-] [--account NAME]
#       the feed entry for the archive (default <archive's folder>/appcast.xml), with what's new from CHANGELOG.md;
#       the archive and the feed itself signed (SURequireSignedFeed), then both checked against the update key the
#       archive's app carries, so a release the app would refuse never leaves this Mac; the URL defaults to the
#       repository's release download for this version
#   scripts/mac-app.sh test       the app's Swift tests (swift-testing with the Command Line Tools)
#   scripts/mac-app.sh tools      fetch Sparkle's tools (pinned by SHA-256) into the cache and print their folder
#                                 (METARUSH_SPARKLE_TOOLS names another folder of them: the tests')
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
. "$ROOT/scripts/lib/status.sh" # an unset variable fails the script on macOS's bash 3.2 too; it ends with `exit 0`
export PATH="${METARUSH_TOOLCHAIN_PATH:+$METARUSH_TOOLCHAIN_PATH:}$PATH:/opt/homebrew/bin:/usr/local/bin" # Go and Node: PATH, else Homebrew
cd "$ROOT"

PKG="$ROOT/desktop/mac"
# Sparkle's release with its tools (sign_update, generate_keys): the same version as desktop/mac/Package.swift's
# binary target (internal/macapp TestSparkleIsPinnedOnce keeps them together).
SPARKLE_VERSION=2.10.1
SPARKLE_TOOLS_URL="https://github.com/sparkle-project/Sparkle/releases/download/$SPARKLE_VERSION/Sparkle-$SPARKLE_VERSION.tar.xz"
SPARKLE_TOOLS_SHA256=1a856d2a9edb8e628de88121d7be04cf0e2167f8641650b78fca00b14244cf27

bold() { printf '\n\033[1m== %s\033[0m\n' "$*"; }
ok() { printf '  \033[32m✓\033[0m %s\n' "$*"; }
note() { printf '  %s\n' "$*"; }
die() { printf '  \033[31m✗\033[0m %s\n' "$*" >&2; exit 1; }
help_text() { awk 'NR > 1 && /^#/ { print; next } NR > 1 { exit }' "$0"; } # the comment block above
usage() { help_text; exit 2; }

[ "$(uname -s)" = Darwin ] || die "MetaRush.app is built on a Mac"
WORK=""
trap 'rc="$(exit_status $?)"; [ -z "$WORK" ] || rm -rf "$WORK"; exit "$rc"' EXIT

# read_setting NAME: the environment's, else release-signing.env's (KEY=value lines, read as data, never run).
read_setting() {
  local name="$1" file="$ROOT/release-signing.env" line val
  if [ -n "${!name:-}" ]; then printf '%s' "${!name}"; return; fi
  [ -f "$file" ] || return 0
  while IFS= read -r line || [ -n "$line" ]; do
    case "$line" in "$name="*) val="${line#*=}"; val="${val%\"}"; val="${val#\"}"; val="${val%\'}"; val="${val#\'}"; printf '%s' "$val"; return ;; esac
  done <"$file"
}

# repo_slug: owner/name of the repository this checkout pushes to (the feed and the bundle identifier follow it).
repo_slug() {
  local u
  u="$(git -C "$ROOT" remote get-url origin 2>/dev/null || true)"
  u="${u%.git}"
  case "$u" in
  git@github.com:*) echo "${u#git@github.com:}" ;;
  https://github.com/*) echo "${u#https://github.com/}" ;;
  ssh://git@github.com/*) echo "${u#ssh://git@github.com/}" ;;
  *) echo "" ;;
  esac
}

# sparkle_tools: Sparkle's release unpacked in the cache (checked against its SHA-256 before anything is kept).
sparkle_tools() {
  if [ -n "${METARUSH_SPARKLE_TOOLS:-}" ]; then echo "$METARUSH_SPARKLE_TOOLS"; return; fi
  local cache="${XDG_CACHE_HOME:-$HOME/Library/Caches}/metarush/sparkle/$SPARKLE_VERSION" tmp
  if [ -x "$cache/bin/sign_update" ]; then echo "$cache"; return; fi
  mkdir -p "$(dirname "$cache")"
  tmp="$(mktemp -d "${TMPDIR:-/tmp}/metarush-sparkle-XXXXXX")"
  curl -fsSL -o "$tmp/sparkle.tar.xz" "$SPARKLE_TOOLS_URL" || { rm -rf "$tmp"; die "Sparkle's tools couldn't be downloaded ($SPARKLE_TOOLS_URL)"; }
  if [ "$(shasum -a 256 "$tmp/sparkle.tar.xz" | cut -d' ' -f1)" != "$SPARKLE_TOOLS_SHA256" ]; then
    rm -rf "$tmp"
    die "Sparkle's download doesn't match its pinned SHA-256: nothing kept"
  fi
  mkdir -p "$tmp/x"
  tar -xJf "$tmp/sparkle.tar.xz" -C "$tmp/x"
  rm -rf "$cache"
  mv "$tmp/x" "$cache"
  rm -rf "$tmp"
  echo "$cache"
}

# swift_test_flags: swift-testing's macros with the Command Line Tools (their plugin folder isn't on the default path).
swift_test_flags() {
  local p
  p="$(cd "$(dirname "$(xcrun -f swift)")/.." && pwd)/lib/swift/host/plugins/testing"
  if [ -d "$p" ]; then printf '%s\n' -Xswiftc -plugin-path -Xswiftc "$p"; fi
}

# sign <path> [entitlements]: Developer ID (hardened runtime, timestamp) when this Mac has one, else ad hoc.
sign() {
  local target="$1" ent="${2:-}" id said
  id="$(read_setting METARUSH_SIGN_IDENTITY)"
  if [ -n "$id" ]; then
    said="$(codesign --force --options runtime --timestamp ${ent:+--entitlements "$ent"} --sign "$id" "$target" 2>&1)" || { echo "$said" >&2; die "$(basename "$target") couldn't be signed"; }
  else
    said="$(codesign --force --sign - ${ent:+--entitlements "$ent"} "$target" 2>&1)" || { echo "$said" >&2; die "$(basename "$target") couldn't be signed"; }
  fi
}

cmd_build() {
  local arch=universal out="$ROOT/dist/mac" web=1 dmg=1 feed="" bundle_id="${METARUSH_APP_BUNDLE_ID:-}" key="" keychain=0
  while [ $# -gt 0 ]; do
    case "$1" in
    --arch) arch="${2:?}"; shift ;;
    --out) out="${2:?}"; shift ;;
    --skip-web) web=0 ;;
    --no-dmg) dmg=0 ;;
    --feed-url) feed="${2:?}"; shift ;;
    --bundle-id) bundle_id="${2:?}"; shift ;;
    --ed-public-key) key="${2:?}"; shift ;;
    --key-from-keychain) keychain=1 ;;
    *) usage ;;
    esac
    shift
  done
  local archs swift_archs
  case "$arch" in
  universal) archs="arm64 amd64"; swift_archs="--arch arm64 --arch x86_64" ;;
  arm64) archs="arm64"; swift_archs="--arch arm64" ;;
  x86_64 | amd64) archs="amd64"; swift_archs="--arch x86_64" ;;
  *) die "--arch is universal, arm64 or x86_64" ;;
  esac

  local version commit ldflags bundle_version slug
  version="$(scripts/version.sh)"
  commit="$(scripts/version.sh --commit)"
  ldflags="-s -w $(scripts/version.sh --ldflags)"
  bundle_version="$(go run ./scripts/macapp bundle-version "$version")"
  slug="$(repo_slug)"
  if [ -z "$feed" ]; then
    [ -n "$slug" ] || die "no feed address: this checkout has no GitHub remote (give --feed-url)"
    feed="https://github.com/$slug/releases/latest/download/appcast.xml"
  fi
  case "$feed" in
  https://?*) ;;
  *) die "the feed address must be https (--feed-url $feed)" ;;
  esac
  if [ -z "$bundle_id" ]; then
    [ -n "$slug" ] || die "no bundle identifier: this checkout has no GitHub remote (give --bundle-id)"
    bundle_id="io.github.$(printf '%s' "$slug" | tr '[:upper:]' '[:lower:]' | tr '/' '.' | tr -c 'a-z0-9.-' '-')"
  fi
  [ -n "$key" ] || key="$(read_setting METARUSH_SPARKLE_PUBLIC_KEY)"
  local account
  account="$(read_setting METARUSH_SPARKLE_ACCOUNT)"
  if [ -z "$key" ] && [ "$keychain" = 1 ]; then
    key="$("$(sparkle_tools)/bin/generate_keys" -p ${account:+--account "$account"})" || die "no update key in this Mac's keychain (docs/mac-app.md, the release Mac's one-time steps)"
  elif [ -n "$key" ]; then
    # A key given (a flag, the environment, release-signing.env) must be the one this Mac signs with: an app
    # built with another refuses every update signed here, for good.
    go run ./scripts/macapp check-key "$key" || die "the update key given isn't an EdDSA public key"
    local mine
    if mine="$("$(sparkle_tools)/bin/generate_keys" -p ${account:+--account "$account"} 2>/dev/null)" && [ -n "$mine" ]; then
      [ "$mine" = "$key" ] || die "the update key given isn't the one in this Mac's keychain: an app built with it would refuse every update signed here"
      ok "the update key is this Mac's (its keychain)"
    else
      note "this Mac's keychain has no update key to check it against: appcast checks every signature against the app's key before release"
    fi
  fi
  [ -n "$key" ] || key="__ED_PUBLIC_KEY__"

  bold "MetaRush.app $version ($commit), $arch"
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-app-XXXXXX")"
  local work="$WORK"

  if [ "$web" = 1 ]; then
    [ -d node_modules ] || npm ci --no-audit --no-fund
    npm run build -w web >/dev/null
    ok "dashboard built"
  fi
  local a bins=""
  for a in $archs; do
    CGO_ENABLED=0 GOOS=darwin GOARCH="$a" go build -trimpath -ldflags "$ldflags" -o "$work/metarush-$a" ./cmd/metarush
    bins="$bins $work/metarush-$a"
  done
  # shellcheck disable=SC2086 # the list of binaries
  lipo -create -output "$work/metarush" $bins
  ok "metarush $version: $(lipo -archs "$work/metarush")"

  # shellcheck disable=SC2086 # the --arch flags
  swift build -c release --package-path "$PKG" --product MetaRush $swift_archs >"$work/swift.log" 2>&1 || { tail -30 "$work/swift.log" >&2; die "the app didn't build"; }
  local bin
  # shellcheck disable=SC2086
  bin="$(swift build -c release --package-path "$PKG" --product MetaRush $swift_archs --show-bin-path)"
  swift build -c release --package-path "$PKG" --product AppHarness >>"$work/swift.log" 2>&1 || { tail -30 "$work/swift.log" >&2; die "the build tool didn't build"; }
  local harness
  harness="$(swift build -c release --package-path "$PKG" --product AppHarness --show-bin-path)/AppHarness"
  ok "the app: $(lipo -archs "$bin/MetaRush")"

  local app="$out/MetaRush.app"
  mkdir -p "$out"
  rm -rf "$app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Helpers" "$app/Contents/Frameworks" "$app/Contents/Resources"
  cp "$bin/MetaRush" "$app/Contents/MacOS/MetaRush"
  cp "$work/metarush" "$app/Contents/Helpers/metarush"
  chmod 755 "$app/Contents/MacOS/MetaRush" "$app/Contents/Helpers/metarush"
  ditto "$bin/Sparkle.framework" "$app/Contents/Frameworks/Sparkle.framework"
  # The app's own search paths only: none into the build machine's Command Line Tools.
  local rp
  for rp in $(otool -l "$app/Contents/MacOS/MetaRush" | awk '/LC_RPATH/{getline; getline; print $2}' | grep '^/Library/Developer/' || true); do
    install_name_tool -delete_rpath "$rp" "$app/Contents/MacOS/MetaRush" 2>/dev/null || true
  done
  # The template's __NAMES__ filled in by plutil, value by value (no text substitution: an address may hold & or |).
  local info="$app/Contents/Info.plist" k v
  cp "$PKG/Info.plist" "$info"
  for k in CFBundleIdentifier=bundle_id CFBundleShortVersionString=version CFBundleVersion=bundle_version MetaRushCommit=commit \
    SUFeedURL=feed SUPublicEDKey=key; do
    v="${k#*=}"
    plutil -replace "${k%%=*}" -string "${!v}" "$info"
  done
  plutil -lint "$info" >/dev/null
  ! grep -q "__[A-Z_]*__" "$info" || [ "$key" = "__ED_PUBLIC_KEY__" ] || die "Info.plist still has a name to fill in"
  "$harness" icon "$work/AppIcon.iconset" >/dev/null
  iconutil -c icns "$work/AppIcon.iconset" -o "$app/Contents/Resources/AppIcon.icns"
  cp "$PKG/THIRD_PARTY.md" "$app/Contents/Resources/THIRD_PARTY.md"
  cp LICENSE "$app/Contents/Resources/LICENSE"

  # Signed inside out: Sparkle's helpers, the framework, MetaRush's program, then the app.
  local fw="$app/Contents/Frameworks/Sparkle.framework/Versions/B" x
  for x in "$fw/XPCServices/Installer.xpc" "$fw/XPCServices/Downloader.xpc" "$fw/Autoupdate" "$fw/Updater.app"; do
    [ -e "$x" ] && sign "$x"
  done
  sign "$app/Contents/Frameworks/Sparkle.framework"
  sign "$app/Contents/Helpers/metarush" "$ROOT/scripts/macos-entitlements.plist"
  sign "$app"
  codesign --verify --deep --strict "$app" || die "the app's signature doesn't verify"
  "$app/Contents/MacOS/MetaRush" --version >/dev/null || die "the app doesn't start (MetaRush --version)"
  [ "$("$app/Contents/Helpers/metarush" version --json | python3 -I -c 'import json,sys;print(json.load(sys.stdin)["data"]["commit"])')" = "$commit" ] ||
    die "the metarush inside the app isn't this build"
  if [ -n "$(read_setting METARUSH_SIGN_IDENTITY)" ]; then ok "signed with the Developer ID (hardened runtime)"; else ok "signed ad hoc (no Developer ID on this Mac: macOS asks once before it opens)"; fi
  if [ "$key" = "__ED_PUBLIC_KEY__" ]; then
    note "no update key: this build never updates itself (docs/mac-app.md: --ed-public-key or --key-from-keychain)"
  else
    ok "updates from $feed, checked against the update key"
  fi

  local zip="$out/MetaRush-$version.zip"
  rm -f "$zip"
  ditto -c -k --sequesterRsrc --keepParent "$app" "$zip"
  local profile
  profile="$(read_setting METARUSH_NOTARY_PROFILE)"
  if [ -n "$(read_setting METARUSH_SIGN_IDENTITY)" ] && [ -n "$profile" ]; then
    note "… waiting for Apple's notarization (usually a few minutes)"
    xcrun notarytool submit "$zip" --keychain-profile "$profile" --wait --timeout 30m >"$work/notary.log" 2>&1 || { cat "$work/notary.log" >&2; die "notarization didn't go through"; }
    grep -q "status: Accepted" "$work/notary.log" || { cat "$work/notary.log" >&2; die "Apple didn't accept it for notarization"; }
    xcrun stapler staple "$app" >/dev/null
    rm -f "$zip"
    ditto -c -k --sequesterRsrc --keepParent "$app" "$zip"
    ok "notarized, the ticket stapled to the app"
  fi
  ok "$(basename "$zip") ($(du -h "$zip" | cut -f1 | tr -d ' '))"
  if [ "$dmg" = 1 ]; then
    local stage="$work/dmg" image="$out/MetaRush-$version.dmg"
    mkdir -p "$stage"
    ditto "$app" "$stage/MetaRush.app"
    ln -s /Applications "$stage/Applications"
    rm -f "$image"
    hdiutil create -quiet -volname "MetaRush $version" -srcfolder "$stage" -ov -format UDZO "$image"
    [ -n "$(read_setting METARUSH_SIGN_IDENTITY)" ] && codesign --force --sign "$(read_setting METARUSH_SIGN_IDENTITY)" "$image"
    ok "$(basename "$image") ($(du -h "$image" | cut -f1 | tr -d ' '))"
  fi
  echo
  echo "Built $app (nothing published)."
}

# zip_only <archive>: an update is the app's zip, never a disk image: for a disk image signed by the same Developer ID
# team, Sparkle falls back to its code signature even with SUVerifyUpdateBeforeExtraction (desktop/mac/Info.plist).
zip_only() {
  case "$1" in
  *.zip) ;;
  *) die "updates are the app's .zip: Sparkle would take a disk image signed by the same Developer ID team without the update key" ;;
  esac
  [ "$(head -c 2 "$1")" = "PK" ] || die "$(basename "$1") isn't a zip archive"
}

# signature <archive> [sign_update options…]: the EdDSA signature alone.
signature() {
  local archive="$1" tools
  shift
  tools="$(sparkle_tools)"
  "$tools/bin/sign_update" -p "$@" "$archive"
}

cmd_sign_update() {
  [ $# -ge 1 ] || usage
  local archive="$1" opts=()
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    --ed-key-file) opts+=(--ed-key-file "${2:?}"); shift ;;
    --account) opts+=(--account "${2:?}"); shift ;;
    *) usage ;;
    esac
    shift
  done
  [ -f "$archive" ] || die "no archive at $archive"
  zip_only "$archive"
  local sig
  sig="$(signature "$archive" ${opts[@]+"${opts[@]}"})"
  printf 'sparkle:edSignature="%s" length="%s"\n' "$sig" "$(stat -f %z "$archive")"
}

cmd_appcast() {
  [ $# -ge 1 ] || usage
  local archive="$1" url="" out="" opts=() stdin_key=""
  shift
  while [ $# -gt 0 ]; do
    case "$1" in
    --url) url="${2:?}"; shift ;;
    --out) out="${2:?}"; shift ;;
    --ed-key-file) opts+=(--ed-key-file "${2:?}"); [ "$2" != - ] || stdin_key=1; shift ;;
    --account) opts+=(--account "${2:?}"); shift ;;
    *) usage ;;
    esac
    shift
  done
  [ -f "$archive" ] || die "no archive at $archive"
  zip_only "$archive"
  # A key on the standard input signs twice (the archive, then the feed): read once, kept in memory only.
  local key_in=""
  [ -z "$stdin_key" ] || key_in="$(cat)"
  local work plist short bundle minsys slug sig pub
  WORK="$(mktemp -d "${TMPDIR:-/tmp}/metarush-appcast-XXXXXX")"
  work="$WORK"
  ditto -x -k "$archive" "$work"
  plist="$work/MetaRush.app/Contents/Info.plist"
  [ -f "$plist" ] || die "the archive doesn't hold MetaRush.app"
  short="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleShortVersionString' "$plist")"
  bundle="$(/usr/libexec/PlistBuddy -c 'Print :CFBundleVersion' "$plist")"
  minsys="$(/usr/libexec/PlistBuddy -c 'Print :LSMinimumSystemVersion' "$plist")"
  pub="$(/usr/libexec/PlistBuddy -c 'Print :SUPublicEDKey' "$plist" 2>/dev/null || true)"
  case "$pub" in "" | __ED_PUBLIC_KEY__) die "the app in this archive has no update key, so it would never update: build it with the key (docs/mac-app.md)" ;; esac
  slug="$(repo_slug)"
  if [ -z "$url" ]; then
    [ -n "$slug" ] || die "no address for the archive: give --url"
    url="https://github.com/$slug/releases/download/v$short/$(basename "$archive")"
  fi
  [ -n "$out" ] || out="$(dirname "$archive")/appcast.xml"
  local tools
  tools="$(sparkle_tools)"
  sign_with() { # sign_with <sign_update arguments…>: the key from the standard input when it came that way
    if [ -n "$stdin_key" ]; then printf '%s\n' "$key_in" | "$tools/bin/sign_update" "$@"; else "$tools/bin/sign_update" "$@"; fi
  }
  sig="$(sign_with -p ${opts[@]+"${opts[@]}"} "$archive")"
  go run ./scripts/macapp appcast --archive "$archive" --url "$url" --short-version "$short" --version "$bundle" --minimum-system "$minsys" \
    --signature "$sig" --changelog "$ROOT/CHANGELOG.md" ${slug:+--link "https://github.com/$slug"} --out "$out"
  # The feed signed too (SURequireSignedFeed): sign_update writes its signature into the file.
  sign_with ${opts[@]+"${opts[@]}"} "$out" >/dev/null
  # Both checked against the key the archive's app carries: a key mix-up stops here, before anything is published.
  go run ./scripts/macapp verify --public-key "$pub" --archive "$archive" --signature "$sig" --feed "$out" ||
    die "the signatures don't match the update key in the app: the app would refuse this release (a different key signed it)"
  ok "$out: MetaRush $short ($bundle), the archive and the feed signed and checked against the app's key, from $url"
}

cmd_test() {
  local flags=()
  while IFS= read -r f; do flags+=("$f"); done < <(swift_test_flags)
  swift test --package-path "$PKG" ${flags[@]+"${flags[@]}"}
}

case "${1:-}" in
build) shift; cmd_build "$@" ;;
sign-update) shift; cmd_sign_update "$@" ;;
appcast) shift; cmd_appcast "$@" ;;
test) shift; cmd_test "$@" ;;
tools) sparkle_tools ;;
-h | --help) help_text; exit 0 ;;
*) usage ;;
esac
exit 0
