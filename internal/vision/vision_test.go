package vision

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeHelper writes a stand-in metarush-vision that reports the given protocol
// and prints one canned frame for `analyze`.
func fakeHelper(t *testing.T, protocol string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "metarush-vision")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo 'metarush-vision " + protocol + " macOS 26.5';;\n" +
		"  analyze) echo '{\"file\":\"f1.jpg\",\"w\":1280,\"h\":720,\"labels\":[{\"id\":\"dog\",\"c\":0.9,\"p\":true}],\"animals\":[],\"humans\":[],\"faces\":[{\"c\":1,\"box\":[0.1,0.2,0.3,0.4],\"q\":0.7}],\"text\":[{\"s\":\"EXIT\",\"c\":1,\"box\":[0.4,0.4,0.2,0.1]}]}';;\n" +
		"  *) exit 2;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestCheckReportsTheHelper(t *testing.T) {
	if runtime.GOOS != "darwin" {
		st := Check(context.Background(), tools.New(config.Tools{}))
		if st.Supported || st.Ready || st.Text == "" {
			t.Fatalf("elsewhere: plainly not available: %+v", st)
		}
		return
	}
	ts := tools.New(config.Tools{Vision: fakeHelper(t, Protocol)})
	st := Check(context.Background(), ts)
	if !st.Supported || !st.Ready || st.Stale || st.Protocol != Protocol {
		t.Fatalf("ready helper: %+v", st)
	}
	// Built from another source: still usable, rebuilt when possible.
	if err := os.WriteFile(sidecar(ts.Path(tools.Vision)), []byte("0000\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if st := Check(context.Background(), ts); !st.Ready || !st.Stale {
		t.Fatalf("stale helper: %+v", st)
	}
	// Another protocol: not used.
	old := tools.New(config.Tools{Vision: fakeHelper(t, "0")})
	if st := Check(context.Background(), old); st.Ready || !st.Stale {
		t.Fatalf("other protocol: %+v", st)
	}
}

func TestAnalyzeReadsFrames(t *testing.T) {
	if runtime.GOOS != "darwin" {
		if err := Analyze(context.Background(), tools.New(config.Tools{}), t.TempDir(), Options{}, func(Frame) {}); !errors.Is(err, ErrNotMac) {
			t.Fatalf("not a Mac: %v", err)
		}
		return
	}
	ts := tools.New(config.Tools{Vision: fakeHelper(t, Protocol)})
	var got []Frame
	if err := Analyze(context.Background(), ts, t.TempDir(), Options{}, func(f Frame) { got = append(got, f) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Labels[0].ID != "dog" || got[0].Faces[0].Box[3] != 0.4 || *got[0].Faces[0].Quality != 0.7 || got[0].Text[0].S != "EXIT" {
		t.Fatalf("frame: %+v", got)
	}
	// Only fixed options and folders MetaRush wrote.
	if err := Analyze(context.Background(), ts, "relative/dir", Options{}, func(Frame) {}); err == nil {
		t.Fatal("a relative folder must be refused")
	}
	if err := Analyze(context.Background(), ts, t.TempDir(), Options{Text: "--evil"}, func(Frame) {}); err == nil {
		t.Fatal("an unknown text mode must be refused")
	}
	missing := tools.New(config.Tools{Vision: filepath.Join(t.TempDir(), "nope")})
	if missing.Has(tools.Vision) && missing.Path(tools.Vision) == filepath.Join(t.TempDir(), "nope") {
		t.Fatal("a missing helper must not resolve")
	}
}

// echoHelper reports the arguments it was given as the frame's file name.
func echoHelper(t *testing.T, protocol string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "metarush-vision")
	script := "#!/bin/sh\ncase \"$1\" in\n  --version) echo 'metarush-vision " + protocol + " macOS 26.5';;\n" +
		"  analyze) echo \"{\\\"file\\\":\\\"$*\\\",\\\"w\\\":1,\\\"h\\\":1,\\\"labels\\\":[],\\\"animals\\\":[],\\\"humans\\\":[],\\\"faces\\\":[],\\\"text\\\":[]}\";;\n" +
		"  *) exit 2;;\nesac\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

// A protocol 2 helper is asked for landmarks, attention, hands, poses and
// text in any language; an older one (Swift missing to build it again) still
// works, asked only what it knows (D114–D117).
func TestNewRequestsOnlyForNewHelpers(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("Apple Vision runs on a Mac")
	}
	all := Options{AnyLanguage: true, Landmarks: true, Attention: true, Hands: true, Pose: true}
	args := func(protocol string) string {
		ts := tools.New(config.Tools{Vision: echoHelper(t, protocol)})
		var got string
		if err := Analyze(context.Background(), ts, t.TempDir(), all, func(f Frame) { got = f.File }); err != nil {
			t.Fatal(err)
		}
		return got
	}
	if a := args(Protocol); !strings.Contains(a, "--landmarks") || !strings.Contains(a, "--saliency") || !strings.Contains(a, "--hands") || !strings.Contains(a, "--pose") || !strings.Contains(a, "--languages auto") {
		t.Fatalf("protocol 2: %q", a)
	}
	if a := args("1"); strings.Contains(a, "--landmarks") || strings.Contains(a, "--languages") {
		t.Fatalf("protocol 1 is not asked for what it can't do: %q", a)
	}
	old := tools.New(config.Tools{Vision: echoHelper(t, "1")})
	if st := Check(context.Background(), old); !st.Ready || !st.Stale || Rich(context.Background(), old) {
		t.Fatalf("an older helper works, and is rebuilt when it can be: %+v", st)
	}
}

func TestSourceHashIsStable(t *testing.T) {
	if a, b := SourceHash(), SourceHash(); a != b || len(a) != 64 {
		t.Fatalf("hash: %q %q", a, b)
	}
}

// TestCopyFileAtOnce: the worker's visual jobs used to put the helper in
// place at the same moment with one temporary name between them, and all but
// the first failed ("rename …metarush-vision.<pid>.tmp: no such file").
func TestCopyFileAtOnce(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "built")
	if err := os.WriteFile(src, []byte("#!/bin/sh\necho helper\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(dir, "tools", "metarush-vision")
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func(i int) { defer wg.Done(); errs[i] = copyFile(src, dst) }(i)
	}
	wg.Wait()
	for i, err := range errs {
		if err != nil {
			t.Fatalf("copy %d: %v", i, err)
		}
	}
	if !fileOK(dst) {
		t.Fatal("the helper is not in place as a program")
	}
	left, _ := filepath.Glob(dst + ".*.tmp")
	if len(left) > 0 {
		t.Fatalf("temporary files left behind: %v", left)
	}
}
