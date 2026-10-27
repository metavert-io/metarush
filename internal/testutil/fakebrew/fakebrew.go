// Package fakebrew writes a stand-in Homebrew for tests and browser runs:
// `brew install --formula <names>` (and `--cask`) puts small stand-in
// programs for each formula into one folder (the one METARUSH_TOOL_DIRS
// names), records every call in a log, and fails for a formula whose name
// starts with "fail-" or that it doesn't know. Nothing is downloaded and the
// real Homebrew is never touched.
package fakebrew

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Programs are the stand-in programs each formula or cask puts in place.
var Programs = map[string][]string{
	"ffmpeg": {"ffmpeg", "ffprobe"}, "poppler": {"pdftoppm", "pdfinfo"}, "yt-dlp": {"yt-dlp", "deno"}, "node": {"node"},
	"whisper-cpp": {"whisper-cli"}, "rclone": {"rclone"}, "d2": {"d2"}, "gh": {"gh"}, "espeak-ng": {"espeak-ng"}, "claude-code": {"claude"},
}

// Script is the stand-in Homebrew: its programs go into bin, its calls into log.
func Script(bin, log string) string {
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	var cases strings.Builder
	for f, progs := range Programs {
		cases.WriteString("    " + f + ") progs=" + q(strings.Join(progs, " ")) + " ;;\n")
	}
	return `#!/bin/sh
# A stand-in Homebrew (internal/testutil/fakebrew): nothing is downloaded.
BIN=` + q(bin) + `
LOG=` + q(log) + `
echo "$*" >> "$LOG"
case "$1" in
--prefix) dirname "$BIN"; exit 0 ;;
--version) echo "Homebrew 4.5.0 (stand-in)"; exit 0 ;;
install) ;;
*) exit 0 ;;
esac
shift
while [ "${1#--}" != "$1" ]; do shift; done
mkdir -p "$BIN"
for f in "$@"; do
  progs=""
  case "$f" in
` + cases.String() + `    fail-*) echo "Error: $f could not be installed (stand-in)" >&2; exit 1 ;;
    *) echo "Error: No available formula with the name \"$f\"." >&2; exit 1 ;;
  esac
  for p in $progs; do
    if [ "$p" = node ]; then printf '#!/bin/sh\necho v22.14.0\n' > "$BIN/$p"
    else printf '#!/bin/sh\necho "%s 1.0 (stand-in)"\n' "$p" > "$BIN/$p"; fi
    chmod +x "$BIN/$p"
  done
  echo "==> Installed $f (stand-in)"
done
`
}

// Write puts a stand-in brew in a temporary folder and returns its path,
// the folder its programs go into and its log.
func Write(t testing.TB) (brew, bin, log string) {
	t.Helper()
	d := t.TempDir()
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	bin, log = filepath.Join(d, "prefix", "bin"), filepath.Join(d, "brew.log")
	brew = filepath.Join(d, "brew")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(brew, []byte(Script(bin, log)), 0o755); err != nil {
		t.Fatal(err)
	}
	return brew, bin, log
}

// Calls are the brew calls the log holds, one per line.
func Calls(t testing.TB, log string) []string {
	t.Helper()
	b, err := os.ReadFile(log)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}
