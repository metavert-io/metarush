package components

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// The open models are the screen-index component's pinned downloads where
// they are the computer's way of looking (D406, D409): each pin is the
// model file's own (address, SHA-256, size, licence) into the models folder;
// under go test nothing is fetched from the publishers; from a stand-in
// server every file is checked and placed, detection says the component is
// ready, and removing it takes them away.
func TestTheOpenModelsArePinnedDownloads(t *testing.T) {
	byID := map[string]Pin{}
	for _, p := range pins {
		byID[p.ID] = p
	}
	for _, f := range visionmodels.Files {
		p, ok := byID[f.ID]
		if !ok || p.URL != f.URL || p.SHA256 != f.SHA256 || p.Bytes != f.Bytes || p.To != "models/"+visionmodels.Folder+"/"+f.Name || p.Licence != f.Licence || p.Take != "" {
			t.Errorf("%s isn't pinned as its file says: %+v", f.ID, p)
		}
	}
	e, bin := testEnv(t)
	e.GOOS = "linux"
	ctx := context.Background()
	if err := e.fetch(ctx, byID["screen-faces"], func(string) {}); err == nil || !strings.Contains(err.Error(), "Tests never download") {
		t.Fatalf("a real download under go test: %v", err)
	}
	// Stand-in files at a stand-in server, pinned by their own checksums.
	serve := map[string][]byte{}
	e.Pins = nil
	for _, f := range visionmodels.Files {
		body := []byte("stand-in " + f.Name)
		sum := sha256.Sum256(body)
		serve["/"+f.Name] = body
		e.Pins = append(e.Pins, Pin{ID: f.ID, Title: f.Title, URL: "https://models.example/" + f.Name, SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(body)),
			To: "models/" + visionmodels.Folder + "/" + f.Name, Licence: f.Licence})
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if b, ok := serve[r.URL.Path]; ok {
			_, _ = w.Write(b)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	t.Setenv(EnvBase, srv.URL)
	// The pinned files the models are checked against are the stand-ins'.
	old := visionmodels.Files
	defer func() { visionmodels.Files = old }()
	var files []visionmodels.File
	for _, f := range old {
		sum := sha256.Sum256(serve["/"+f.Name])
		f.SHA256 = hex.EncodeToString(sum[:])
		files = append(files, f)
	}
	visionmodels.Files = files
	// FFmpeg, and a transcriber's folder with onnxruntime beside it.
	program(t, bin, "ffmpeg", "ffmpeg version 7")
	program(t, bin, "ffprobe", "ffprobe version 7")
	sherpa := t.TempDir()
	for _, d := range []string{"bin", "lib"} {
		_ = os.MkdirAll(filepath.Join(sherpa, d), 0o755)
	}
	program(t, filepath.Join(sherpa, "bin"), tools.Sherpa, "help")
	lib := "libonnxruntime.so"
	if runtime.GOOS == "darwin" {
		lib = "libonnxruntime.dylib"
	}
	_ = os.WriteFile(filepath.Join(sherpa, "lib", lib), []byte("library"), 0o644)
	e.Cfg.Tools.SherpaDir = sherpa
	e.Tools.Rescan(e.Cfg.Tools)
	si, _ := Get(looker.Component)
	if st := e.StateOf(ctx, si); st.Status != StatusMissing || st.How != HowDownload {
		t.Fatalf("before: %+v", st)
	}
	if err := e.run(ctx, si, e.wayFor(si), func(string) {}); err != nil {
		t.Fatal(err)
	}
	st := e.StateOf(ctx, si)
	if !st.Ready || !st.CanRemove {
		t.Fatalf("after: %+v", st)
	}
	for _, f := range files {
		if f.Part != visionmodels.PartIndex {
			continue
		}
		if _, err := os.Stat(filepath.Join(e.Tools.ModelsDir(), visionmodels.Folder, f.Name)); err != nil {
			t.Fatalf("%s wasn't placed: %v", f.Name, err)
		}
	}
	// A changed file isn't used: detection says so.
	f, _ := visionmodels.Store{}.ByID("screen-text-read")
	_ = os.WriteFile(filepath.Join(e.Tools.ModelsDir(), visionmodels.Folder, f.Name), []byte("changed"), 0o644)
	if st := e.StateOf(ctx, si); st.Ready || !strings.Contains(st.Says, "changed") {
		t.Fatalf("a changed model: %+v", st)
	}
	rm, err := e.Remove(ctx, looker.Component, "Sam")
	if err != nil || !rm.Removed {
		t.Fatalf("remove: %+v %v", rm, err)
	}
	if visionmodels.StoreIn(e.Tools.ModelsDir()).Present(visionmodels.PartIndex) {
		t.Fatal("the open models are still there")
	}
	// The cut-out model is its own component, added the same way.
	co, _ := Get(looker.CutOutComponent)
	if err := e.run(ctx, co, e.wayFor(co), func(string) {}); err != nil {
		t.Fatal(err)
	}
	if st := e.StateOf(ctx, co); !st.Ready {
		t.Fatalf("cut-outs: %+v", st)
	}
	if _, err := e.Add(ctx, looker.CutOutComponent, AddOptions{}); err != nil && !apperr.Is(err, apperr.NotConfigured) {
		t.Fatalf("adding it again: %v", err)
	}
}

// CI's onnxruntime (scripts/ci-onnxruntime.sh, D413) is the transcriber's
// build this component list downloads on Linux, so the Go tests on Linux run
// the stand-in models through the library people get.
func TestCIOnnxruntimeIsThePinnedOne(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "scripts", "ci-onnxruntime.sh"))
	if err != nil {
		t.Fatal(err)
	}
	script := string(b)
	var found bool
	for _, p := range pins {
		if p.ID != "sherpa-onnx" || !p.For("linux", "amd64") {
			continue
		}
		found = true
		for _, want := range []string{`URL="` + p.URL + `"`, `SHA256="` + p.SHA256 + `"`, `TAKE="` + p.Take + `"`} {
			if !strings.Contains(script, want) {
				t.Errorf("scripts/ci-onnxruntime.sh doesn't say %s", want)
			}
		}
	}
	if !found {
		t.Fatal("no Linux build of the transcriber is pinned")
	}
}
