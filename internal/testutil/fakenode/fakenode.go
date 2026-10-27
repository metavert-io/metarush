// Package fakenode writes a stand-in `node` for tests of the renderer setup
// (internal/renderer) and of renders. It answers --version and fakes three
// programs, recording each run and the folder it ran in:
//
//   - npm-cli.js ci …     lays down the packages a renderer needs (fails while
//     the file "npm-fail" exists in the fake's folder);
//   - render/browser.mjs  the setup check: a "browser" and the versions line;
//   - render/render.mjs   an ffmpeg test pattern of the size and length in the
//     files "render-size" and "render-seconds" (540x960, 2 s by default), and
//     a copy of the render.mjs it was run with.
package fakenode

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Fake is one fake toolchain.
type Fake struct {
	Dir  string // the fake's folder (switch files, the run log)
	Node string // the fake node (tools.node)
	NPM  string // its npm-cli.js (tools.npm)
}

// Write creates a fake that reports the given Remotion version.
func Write(t testing.TB, remotion string) *Fake {
	t.Helper()
	d := t.TempDir()
	if r, err := filepath.EvalSymlinks(d); err == nil {
		d = r
	}
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := `#!/bin/sh
D=` + q(d) + `
R=` + q(remotion) + `
log() { printf '%s %s\n' "$1" "$(pwd -P)" >> "$D/runs.log"; }
case "$1" in
--version|-v) echo v22.14.0; exit 0 ;;
esac
s="$1"; shift
case "$s" in
*npm-cli.js)
  log npm
  if [ -f "$D/npm-fail" ]; then echo "npm ERR! code ECONNREFUSED (fake)" >&2; exit 1; fi
  mkdir -p node_modules/remotion node_modules/@remotion/renderer node_modules/@remotion/bundler node_modules/@metarush || exit 1
  printf '{"name":"remotion","version":"%s"}\n' "$R" > node_modules/remotion/package.json
  printf '{"name":"@remotion/renderer"}\n' > node_modules/@remotion/renderer/package.json
  printf '{"name":"@remotion/bundler"}\n' > node_modules/@remotion/bundler/package.json
  ln -sfn ../../schema node_modules/@metarush/schema
  ;;
render/browser.mjs)
  log browser
  mkdir -p node_modules/.remotion/chrome-headless-shell/fake || exit 1
  printf '{"type":"versions","remotion":"%s","templates":{}}\n' "$R"
  printf '{"type":"progress","stage":"browser","progress":1}\n'
  printf '{"type":"done","downloaded":true}\n'
  ;;
render/render.mjs)
  log render
  cp render/render.mjs "$D/last-render.mjs"
  out=""
  while [ $# -gt 0 ]; do [ "$1" = "--out" ] && out="$2"; shift; done
  size=$(cat "$D/render-size" 2>/dev/null || echo 540x960)
  secs=$(cat "$D/render-seconds" 2>/dev/null || echo 2)
  printf '{"type":"versions","remotion":"%s","templates":{"title.card":"1"}}\n' "$R"
  ffmpeg -hide_banner -loglevel error -y -f lavfi -i "color=c=gray:s=$size:r=30:d=$secs" -f lavfi -i "anullsrc=r=48000:cl=stereo" \
    -t "$secs" -c:v libx264 -pix_fmt yuv420p -c:a aac "$out" >&2 || { printf '{"type":"error","message":"fake render failed"}\n'; exit 1; }
  printf '{"type":"done","output":"%s"}\n' "$out"
  ;;
*) echo "fake node: unexpected $s" >&2; exit 1 ;;
esac
`
	f := &Fake{Dir: d, Node: filepath.Join(d, "node"), NPM: filepath.Join(d, "npm-cli.js")}
	if err := os.WriteFile(f.Node, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.NPM, []byte("// fake npm\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return f
}

// Runs are the recorded runs ("npm <dir>", "browser <dir>", "render <dir>").
func (f *Fake) Runs(t testing.TB) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, "runs.log"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

// Count is how many runs of one kind were recorded.
func (f *Fake) Count(t testing.TB, kind string) int {
	n := 0
	for _, r := range f.Runs(t) {
		if strings.HasPrefix(r, kind+" ") {
			n++
		}
	}
	return n
}

// LastRenderScript is the render/render.mjs the last render ran with.
func (f *Fake) LastRenderScript(t testing.TB) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(f.Dir, "last-render.mjs"))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// FailNPM makes `npm ci` fail (true) or work again (false).
func (f *Fake) FailNPM(t testing.TB, fail bool) {
	t.Helper()
	p := filepath.Join(f.Dir, "npm-fail")
	if fail {
		if err := os.WriteFile(p, nil, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	_ = os.Remove(p)
}
