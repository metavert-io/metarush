package components_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// What a person removed (or left out) never sets itself up again on first
// use: the face model, the on-screen helper, the voice cleaner and the
// steadying tool each say what they need, in the same words as the
// component, with the one step — and download nothing (D325).
func TestLeftOutComponentsDontSetThemselvesUp(t *testing.T) {
	t.Setenv(tools.EnvToolDirs, t.TempDir())
	h := t.TempDir()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); http.NotFound(w, r) }))
	defer srv.Close()
	if err := choice.Set(h, map[string]bool{"faces": false, "screen-index": false, "clean-voice": false, "stabilizer": false}, "Sam"); err != nil {
		t.Fatal(err)
	}
	ts := tools.New(config.Tools{})
	ctx := context.Background()
	says := func(name string, err error) {
		t.Helper()
		want := components.Missing(name).Message
		if err == nil || apperr.From(err).Message != want {
			t.Errorf("%s left out: %v (want %q)", name, err, want)
		}
		if err == nil {
			return
		}
		if d, _ := apperr.From(err).Details.(map[string]any); d["component"] != name {
			t.Errorf("%s: details %v", name, apperr.From(err).Details)
		}
	}
	_, err := faceid.Setup{Home: h, URL: srv.URL + "/face.onnx", SHA256: "00", HTTP: srv.Client()}.Ensure(ctx)
	says("faces", err)
	if runtime.GOOS == "darwin" {
		_, _, err = vision.Ensure(ctx, ts, h)
		says("screen-index", err)
		_, err = (&fixes.Stabilizer{Home: h, Tools: ts}).Ensure(ctx, nil)
		says("stabilizer", err)
	}
	clean := &sound.Components{Home: h, Tools: ts, BaseURLs: map[string]string{"deepfilter": srv.URL + "/", "dnsmos": srv.URL + "/"}}
	if clean.CleanAvailable() {
		says("clean-voice", clean.InstallClean(ctx, func(int, int, string) error { return nil }))
	}
	if n := hits.Load(); n != 0 {
		t.Errorf("%d downloads for components a person left out", n)
	}
}
