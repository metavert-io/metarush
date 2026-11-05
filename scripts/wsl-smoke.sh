#!/usr/bin/env bash
# wsl-smoke.sh — MetaRush inside WSL 2 on a real Windows PC (D524, docs/wsl.md): what it detects and says, and
# that Windows reaches Ubuntu's 127.0.0.1. Run it inside Ubuntu (or another distribution) in WSL; CI runs it on a
# Windows runner (.github/workflows/wsl.yml).
#
#   scripts/wsl-smoke.sh            # detection and doctor's WINDOWS checks; Windows reaching a server on 127.0.0.1
#   scripts/wsl-smoke.sh --setup    # also a throwaway MetaRush in a temporary folder: setup with the database on this
#                                   # computer (MongoDB's download, about 85 MB), the dashboard's opener (nothing
#                                   # opens), Windows asking the dashboard for its health, a C:\ path added (a dry
#                                   # run), doctor; then it is stopped and removed
#
# It uses the metarush on PATH (or METARUSH=<program>) and only its own temporary folders and settings
# (XDG_CONFIG_HOME too): never your own MetaRush. Ports 5840 (a test server) and 5841 (the dashboard).
# Exit status 1 when a check fails, 2 when it isn't run inside WSL.
set -uo pipefail

setup=0
for a in "$@"; do
  case "$a" in
    --setup) setup=1 ;;
    -h | --help) sed -n '2,15p' "$0"; exit 0 ;;
    *) echo "unknown option: $a" >&2; exit 2 ;;
  esac
done

if ! grep -qiE 'microsoft|wsl' /proc/sys/kernel/osrelease /proc/version 2>/dev/null; then
  echo "This isn't WSL: run it inside Ubuntu (or another distribution) on Windows." >&2
  exit 2
fi
MR="${METARUSH:-$(command -v metarush || true)}"
if [ -z "$MR" ] || [ ! -x "$MR" ]; then
  echo "No metarush program: put it on PATH, or set METARUSH=<program>." >&2
  exit 2
fi
command -v python3 >/dev/null || { echo "python3 is needed to read MetaRush's JSON (sudo apt install python3)." >&2; exit 2; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/metarush-wsl-smoke-XXXXXX")"
home="$tmp/home"
export XDG_CONFIG_HOME="$tmp/config" # the database port registry and the rest stay in the throwaway folder
export METARUSH_MONGODB_URI=""
unset METARUSH_HOME
server=""
winfolder=""
pass=0
fail=0
skip=0

cleanup() {
  [ -n "$server" ] && kill "$server" 2>/dev/null
  if [ "$setup" = 1 ] && [ -f "$home/config.yaml" ]; then
    "$MR" --home "$home" stop >/dev/null 2>&1 || true
  fi
  [ -n "$winfolder" ] && rm -rf "$winfolder"
  rm -rf "$tmp"
}
trap cleanup EXIT

ok() { pass=$((pass + 1)); echo "  ok    $*"; }
bad() { fail=$((fail + 1)); echo "  FAIL  $*"; }
skipped() { skip=$((skip + 1)); echo "  skip  $*"; }

# json <file> <python expression over d>: prints the value ("" when it isn't there).
json() {
  python3 - "$1" "$2" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
    v = eval(sys.argv[2], {"d": d})
except Exception:
    v = ""
print("" if v is None else (json.dumps(v) if isinstance(v, (dict, list)) else v))
PY
}

# windows_checks <doctor json>: doctor's WINDOWS checks, one "name<TAB>status<TAB>say" per line.
windows_checks() {
  python3 - "$1" <<'PY'
import json, sys
try:
    d = json.load(open(sys.argv[1]))
except Exception:
    sys.exit(0)
for c in (d.get("data") or {}).get("checks") or []:
    if c.get("area") == "windows":
        print("%s\t%s\t%s" % (c.get("name"), c.get("status"), c.get("say") or c.get("detail")))
PY
}

# powershell <command>: runs it in Windows' PowerShell through interop ("" and status 1 when it can't).
ps=""
for p in "$(command -v powershell.exe || true)" /mnt/c/Windows/System32/WindowsPowerShell/v1.0/powershell.exe; do
  if [ -n "$p" ] && [ -x "$p" ]; then ps="$p"; break; fi
done
powershell() {
  [ -n "$ps" ] || return 1
  (cd /mnt/c 2>/dev/null || true; "$ps" -NoProfile -NonInteractive -Command "$1" 2>/dev/null | tr -d '\r')
}
# from_windows <url>: the status Windows gets for url ("" when it couldn't ask).
from_windows() {
  powershell "try { (Invoke-WebRequest -UseBasicParsing -TimeoutSec 20 -Uri '$1').StatusCode } catch { if (\$_.Exception.Response) { [int]\$_.Exception.Response.StatusCode } else { 'unreachable' } }"
}

echo "MetaRush: $("$MR" version 2>/dev/null | head -1)"
echo "WSL: $(uname -r); $(. /etc/os-release && echo "$PRETTY_NAME")"
echo

echo "What MetaRush detects (doctor on an empty folder):"
"$MR" --home "$home" doctor --json >"$tmp/doctor0.json" 2>"$tmp/doctor0.err"
checks="$(windows_checks "$tmp/doctor0.json")"
if [ -z "$checks" ]; then
  bad "doctor has no WINDOWS checks (it doesn't see WSL)"
else
  echo "$checks" | sed 's/^/        /'
  wsl_line="$(echo "$checks" | awk -F'\t' '$1=="wsl"')"
  case "$wsl_line" in
    *$'\tok\t'*"(WSL 2)"*) ok "WSL 2 seen" ;;
    *) bad "WSL 2 not seen: $wsl_line" ;;
  esac
  echo "$checks" | awk -F'\t' '$1=="loopback" && $2=="ok"' | grep -q . && ok "the dashboard stays on this PC (loopback)" || bad "no loopback check"
fi

echo
echo "Windows reaching Ubuntu's 127.0.0.1:"
mkdir -p "$tmp/www" && echo "metarush-wsl-smoke" >"$tmp/www/index.html"
python3 -m http.server 5840 --bind 127.0.0.1 --directory "$tmp/www" >/dev/null 2>&1 &
server=$!
for _ in $(seq 1 50); do
  python3 -c 'import urllib.request; urllib.request.urlopen("http://127.0.0.1:5840/", timeout=1)' 2>/dev/null && break
  sleep 0.2
done
if [ -z "$ps" ]; then
  skipped "Ubuntu can't start Windows programs here (interop off): open http://127.0.0.1:5840 in Edge yourself"
else
  for host in 127.0.0.1 localhost; do
    got="$(from_windows "http://$host:5840/")"
    [ "$got" = "200" ] && ok "Windows' $host:5840 reaches Ubuntu's server" || bad "Windows' $host:5840: ${got:-no answer}"
  done
fi
kill "$server" 2>/dev/null
server=""

if [ "$setup" = 1 ]; then
  echo
  echo "A throwaway MetaRush (setup with the database on this computer):"
  if "$MR" --home "$home" setup --yes --json --database here --secrets env --service=false --skills=false --no-components \
    --port 5841 --workspace-name "WSL smoke" --user-name "Smoke test" --user-email smoke@example.com >"$tmp/setup.json" 2>"$tmp/setup.err"; then
    ok "setup: $(json "$tmp/setup.json" 'd["data"]["database"]["says"]')"
    [ "$(json "$tmp/setup.json" 'd["data"]["database"]["running"]')" = "True" ] && ok "MongoDB $(json "$tmp/setup.json" 'd["data"]["database"]["version"]') runs on WSL's kernel" || bad "the database isn't running"
    [ "$(json "$tmp/setup.json" 'd["data"]["whatWorks"]["windows"]["version"]')" = "2" ] && ok "setup's words: $(json "$tmp/setup.json" 'd["data"]["whatWorks"]["windows"]["says"]')" || bad "setup says nothing about Windows"
    echo "        service: $(json "$tmp/setup.json" 'd["data"]["service"]["says"]')"
  else
    bad "setup failed: $(tail -3 "$tmp/setup.err") $(json "$tmp/setup.json" 'd["error"]["message"]')"
  fi
  if [ -f "$home/config.yaml" ]; then
    if "$MR" --home "$home" dashboard --no-open --json >"$tmp/dash.json" 2>"$tmp/dash.err"; then
      opener="$(json "$tmp/dash.json" 'd["data"]["opensWith"]')"
      case "$opener" in
        cmd.exe | wslview) ok "the dashboard would open with $opener (Windows' browser)" ;;
        "") [ -z "$ps" ] && skipped "nothing opens the Windows browser (interop off): the link is printed" || bad "no opener, though Windows programs run" ;;
        *) bad "the dashboard would open with $opener, not Windows' browser" ;;
      esac
      if [ -n "$ps" ]; then
        for host in 127.0.0.1 localhost; do
          got="$(from_windows "http://$host:5841/api/health")"
          [ "$got" = "200" ] && ok "Windows' browser address $host:5841 reaches the dashboard" || bad "Windows' $host:5841/api/health: ${got:-no answer}"
        done
      fi
    else
      bad "the dashboard didn't start: $(tail -3 "$tmp/dash.err")"
    fi
    # A path as File Explorer copies it, from the Windows side: found, and said to be slow.
    pub="$(wslpath -u 'C:\Users\Public' 2>/dev/null || echo /mnt/c/Users/Public)"
    if [ -d "$pub" ] && mkdir -p "$pub/metarush-wsl-smoke-$$" 2>/dev/null; then
      winfolder="$pub/metarush-wsl-smoke-$$"
      python3 - "$winfolder/tone.wav" <<'PY'
import math, struct, sys, wave
w = wave.open(sys.argv[1], "wb")
w.setnchannels(1); w.setsampwidth(2); w.setframerate(16000)
w.writeframes(b"".join(struct.pack("<h", int(6000 * math.sin(2 * math.pi * 440 * i / 16000))) for i in range(16000)))
w.close()
PY
      typed="\"C:\\Users\\Public\\metarush-wsl-smoke-$$\\tone.wav\""
      "$MR" --home "$home" ingest --dry-run --json "$typed" >"$tmp/ingest.json" 2>"$tmp/ingest.err"
      path="$(json "$tmp/ingest.json" 'd["data"]["ingest"]["results"][0]["path"]')"
      status="$(json "$tmp/ingest.json" 'd["data"]["ingest"]["results"][0]["status"]')"
      slow="$(json "$tmp/ingest.json" 'd["data"]["ingest"]["slow"]')"
      [ "$status" = "would-ingest" ] && [ "$path" = "$winfolder/tone.wav" ] && ok "$typed is $path" || bad "the Windows path: $status $path $(tail -2 "$tmp/ingest.err")"
      [ -n "$slow" ] && ok "it says: $slow" || bad "no word that the Windows side is slower"
    else
      skipped "no writable C:\\Users\\Public to add a file from"
    fi
    "$MR" --home "$home" doctor --json >"$tmp/doctor1.json" 2>/dev/null
    echo "        doctor's WINDOWS checks now:"
    windows_checks "$tmp/doctor1.json" | sed 's/^/          /'
  fi
fi

echo
echo "$pass passed, $fail failed, $skip skipped."
[ "$fail" = 0 ] || exit 1
exit 0
