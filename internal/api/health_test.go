package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/version"
)

// /api/health says which build answers, with the same fields as `metarush
// version --json` (D130); deploy.sh checks its commit and version.
func TestHealthSaysWhichBuild(t *testing.T) {
	a := testapp.New(t)
	srv := New(a)
	t.Cleanup(srv.Assistant.Close)
	req := httptest.NewRequest(http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/api/health", a.Cfg.Dashboard.Port), nil)
	req.RemoteAddr = "127.0.0.1:50123"
	rec := httptest.NewRecorder()
	srv.Handler().ServeHTTP(rec, req)
	var h struct {
		OK           bool   `json:"ok"`
		Installation string `json:"installation"`
		version.Info
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil || rec.Code != http.StatusOK || !h.OK {
		t.Fatalf("health: %d %v %s", rec.Code, err, rec.Body)
	}
	b := version.Current()
	if h.Version != b.Version || h.Release != b.Release || h.Dev != b.Dev || h.Commit != b.Commit || h.Date != b.Date || h.Installation != a.Cfg.Installation.Name {
		t.Errorf("health says %s, want %+v", rec.Body, b)
	}
}
