package localmodels_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
)

type entry struct {
	name, body string
	mode       int64
	kind       byte
}

func tarGz(t *testing.T, es []entry) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := gzip.NewWriter(&b)
	tw := tar.NewWriter(zw)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: e.mode, Size: int64(len(e.body)), Typeflag: e.kind}
		if e.kind == 0 {
			h.Typeflag = tar.TypeReg
		}
		if h.Mode == 0 {
			h.Mode = 0o644
		}
		if h.Typeflag == tar.TypeSymlink {
			h.Linkname, h.Size = e.body, 0
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			_, _ = tw.Write([]byte(e.body))
		}
	}
	_ = tw.Close()
	_ = zw.Close()
	return b.Bytes()
}

func sum(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

// serve puts archives on a server on this computer, the only place tests download from.
func serve(t *testing.T, files map[string][]byte) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(b)
	}))
	t.Cleanup(srv.Close)
	t.Setenv(localmodels.EnvBase, srv.URL)
}

func pin(archive []byte) localmodels.Pin {
	return localmodels.Pin{ID: "voice-test", Title: "the test voice", URL: "https://example.com/models/voice-test.tar.gz", SHA256: sum(archive), Bytes: int64(len(archive)),
		Root: "voice", Needs: []string{"model.onnx"}, Program: "bin/say-it"}
}

func TestSetUpChecksAndPinsTheProgram(t *testing.T) {
	archive := tarGz(t, []entry{{name: "voice/", kind: tar.TypeDir}, {name: "voice/model.onnx", body: "weights"}, {name: "voice/bin/say-it", body: "#!/bin/sh\n", mode: 0o755}})
	serve(t, map[string][]byte{"voice-test.tar.gz": archive})
	p := pin(archive)
	s := localmodels.Store{Home: t.TempDir(), Pins: []localmodels.Pin{p}}
	err := s.Ready(p)
	if ae := apperr.From(err); err == nil || ae.Code != apperr.NotConfigured || !strings.Contains(ae.Message, "isn't set up on this computer yet") {
		t.Fatalf("before setup: %v", err)
	}
	var said []string
	if err := s.Setup(context.Background(), p, func(m string) { said = append(said, m) }); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(p); err != nil {
		t.Fatalf("after setup: %v", err)
	}
	if len(said) == 0 || !strings.Contains(said[0], "downloading the test voice") {
		t.Errorf("progress %v", said)
	}
	prog := s.Path(p, p.Program)
	if st, err := os.Stat(prog); err != nil || st.Mode()&0o111 == 0 {
		t.Fatalf("program %v %v", st, err)
	}
	// A program changed afterwards is refused.
	if err := os.WriteFile(prog, []byte("#!/bin/sh\necho changed\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := s.Ready(p); err == nil || !strings.Contains(err.Error(), "was changed after it was set up") {
		t.Fatalf("changed program: %v", err)
	}
	// Setting it up again puts the pinned one back.
	if err := s.Setup(context.Background(), p, nil); err != nil || s.Ready(p) != nil {
		t.Fatalf("again: %v", err)
	}
}

func TestSetUpRefusesWhatIsntPinned(t *testing.T) {
	good := tarGz(t, []entry{{name: "voice/model.onnx", body: "weights"}, {name: "voice/bin/say-it", body: "x", mode: 0o755}})
	cases := map[string]struct {
		archive []byte
		pinned  string
		want    string
	}{
		"another checksum": {good, sum([]byte("something else")), "checksum differs"},
		"a file outside":   {tarGz(t, []entry{{name: "../escape", body: "x"}}), "", "outside its folder"},
		"a link":           {tarGz(t, []entry{{name: "voice/model.onnx", body: "/etc/passwd", kind: tar.TypeSymlink}}), "", "other than files and folders"},
		"a missing file":   {tarGz(t, []entry{{name: "voice/bin/say-it", body: "x", mode: 0o755}}), "", "lacks model.onnx"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			serve(t, map[string][]byte{"voice-test.tar.gz": c.archive})
			p := pin(c.archive)
			if c.pinned != "" {
				p.SHA256 = c.pinned
			}
			home := t.TempDir()
			s := localmodels.Store{Home: home, Pins: []localmodels.Pin{p}}
			err := s.Setup(context.Background(), p, nil)
			if err == nil || !strings.Contains(apperr.From(err).Message+err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
			if s.Ready(p) == nil {
				t.Error("ready after a refused setup")
			}
			if _, err := os.Stat(filepath.Join(home, "escape")); err == nil {
				t.Error("a file was written outside the folder")
			}
		})
	}
}

// Tests never download from the internet, and the test server must be on this computer.
func TestTestsNeverDownload(t *testing.T) {
	p := localmodels.Pin{ID: "x", Title: "x", URL: "https://example.com/x.tar.bz2", SHA256: strings.Repeat("0", 64), Bytes: 10}
	s := localmodels.Store{Home: t.TempDir(), Pins: []localmodels.Pin{p}}
	if err := s.Setup(context.Background(), p, nil); err == nil || !strings.Contains(err.Error(), "Tests never download models") {
		t.Fatalf("internet: %v", err)
	}
	t.Setenv(localmodels.EnvBase, "https://example.com")
	if err := s.Setup(context.Background(), p, nil); err == nil || !strings.Contains(err.Error(), "must be a server on this computer") {
		t.Fatalf("elsewhere: %v", err)
	}
}

func TestPinsAreWhole(t *testing.T) {
	s := localmodels.Store{Home: t.TempDir()}
	seen := map[string]bool{}
	for _, p := range s.Here() {
		seen[p.ID] = true
		if len(p.SHA256) != 64 || p.Bytes <= 0 || !strings.HasPrefix(p.URL, "https://") || p.Root == "" || p.Licence == "" || p.LicenceURL == "" {
			t.Errorf("%s: incomplete %+v", p.ID, p)
		}
	}
	for _, id := range []string{"kokoro-v1", "kitten-nano", "real-esrgan"} {
		if !seen[id] {
			t.Errorf("no pin %s", id)
		}
	}
	if got := localmodels.Size(349906910); got != "334 MB" {
		t.Errorf("size %q", got)
	}
}
