package api

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/wsl"
)

// Inside WSL (D519) the dashboard keeps listening on 127.0.0.1 only, mirrored
// networking or not, and Windows' browser reaches it through WSL's localhost
// forwarding (or mirrored mode's shared loopback): from loopback, with the
// Host it typed, 127.0.0.1 or localhost. The Host check takes those as this
// computer's own browser; WSL's own address and Windows' names for Ubuntu
// stay refused.
func TestWindowsBrowserReachesTheDashboardOnLoopback(t *testing.T) {
	t.Cleanup(wsl.Pretend(wsl.Info{Version: 2, Distro: "Ubuntu", DistroKnown: true, Interop: true, DriveRoot: "/mnt/"}))
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	if cfg.Dashboard.Host != "127.0.0.1" {
		t.Fatalf("the dashboard listens on %q", cfg.Dashboard.Host)
	}
	cfg.Dashboard.Port = 4845
	s := &Server{App: &app.App{Cfg: cfg}}
	guarded := s.hostGuard(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for _, c := range []struct {
		host, from string
		want       int
	}{
		{"127.0.0.1:4845", "127.0.0.1:53211", http.StatusNoContent}, // Edge on Windows, NAT: WSL's relay connects from loopback
		{"localhost:4845", "127.0.0.1:53212", http.StatusNoContent},
		{"[::1]:4845", "[::1]:53213", http.StatusNoContent},
		{"localhost:4845", "[::ffff:127.0.0.1]:53214", http.StatusNoContent},
		{"172.25.112.1:4845", "127.0.0.1:53215", http.StatusForbidden}, // WSL's own address
		{"ubuntu.mshome.net:4845", "127.0.0.1:53216", http.StatusForbidden},
		{"wsl.localhost:4845", "127.0.0.1:53217", http.StatusForbidden},
		{"localhost:4846", "127.0.0.1:53218", http.StatusForbidden},
	} {
		req := httptest.NewRequest("GET", "/api/health", nil)
		req.Host, req.RemoteAddr = c.host, c.from
		req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/141.0.0.0 Safari/537.36 Edg/141.0.0.0")
		rec := httptest.NewRecorder()
		guarded.ServeHTTP(rec, req)
		if rec.Code != c.want {
			t.Errorf("Host %s from %s: %d, want %d", c.host, c.from, rec.Code, c.want)
		}
	}
}
