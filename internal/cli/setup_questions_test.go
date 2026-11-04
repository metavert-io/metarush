package cli

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/version"
)

// `setup questions` is setup's interview with its suggested answers, for the
// Mac app's panels and for agents (D473): the storage folder, the database
// on this computer, a free port, the service, the skills (only with Claude
// Code), the components checklist with the suggested ones (the renderer
// among them, with Remotion's licence) ticked — and nothing is set up, made
// or started.
func TestSetupQuestionsChangeNothing(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, "setup", "questions", "--json")
	var q SetupQuestions
	decodeData(t, r, &q)
	if q.SetUp || q.Home != h {
		t.Fatalf("questions: %+v", q)
	}
	if q.Storage.Suggested != "folder" || q.Storage.Folder != filepath.Join(h, "media") || q.Storage.Says == "" {
		t.Errorf("storage: %+v", q.Storage)
	}
	switch d := q.Database; {
	case d.Here && (d.Suggested != "here" || d.DownloadMB <= 0):
		t.Errorf("database here: %+v", d)
	case !d.Here && d.Suggested != "elsewhere":
		t.Errorf("database: %+v", d)
	}
	if q.Port < 4845 || q.Port >= 4900 {
		t.Errorf("port %d", q.Port)
	}
	if !q.Service.Suggested || q.Service.Installed || q.Service.OtherCopy {
		t.Errorf("service: %+v", q.Service)
	}
	if q.Skills.ClaudeInstalled || q.Skills.Suggested || !strings.Contains(q.Skills.Says, "isn't installed") {
		t.Errorf("skills without Claude Code: %+v", q.Skills)
	}
	byName := map[string]SetupComponentQuestion{}
	for _, c := range q.Components {
		byName[c.Name] = c
		if c.Required || c.Title == "" || c.Unlocks == "" || c.Size == "" || c.Says == "" {
			t.Errorf("component row: %+v", c)
		}
		if c.Ticked != c.Suggested {
			t.Errorf("%s: ticked %v, suggested %v", c.Name, c.Ticked, c.Suggested)
		}
	}
	rend, ok := byName["renderer"]
	if !ok || !rend.Ticked || rend.Licence == "" || rend.DownloadMB <= 0 || rend.Minutes <= 0 {
		t.Errorf("the renderer row: %+v", rend)
	}
	if !byName["ffmpeg"].Ticked {
		t.Errorf("ffmpeg isn't ticked: %+v", byName["ffmpeg"])
	}
	if _, err := os.Stat(h); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("questions made the home: %v", err)
	}
	for _, s := range []string{q.Storage.Says, q.Database.Says, q.Database.Note, q.Service.Says, q.Skills.Says} {
		for _, f := range plainwords.CheckShown(s) {
			t.Errorf("%q shows %s", s, f)
		}
	}
	plain := runCLI(t, h, "setup", "questions")
	setupPlain(t, "setup questions", plain.stdout)
	if !strings.Contains(plain.stdout, "suggested answers") {
		t.Errorf("plain: %s", plain.stdout)
	}

	// A database address in the terminal is the one setup uses.
	t.Setenv("METARUSH_MONGODB_URI", "mongodb://db.example.invalid/x")
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &q)
	if q.Database.Suggested != "elsewhere" {
		t.Errorf("with an address in the terminal: %+v", q.Database)
	}
}

// On a home set up already the questions say what is kept and tick what a
// person chose before, plus suggested components nobody decided on.
func TestSetupQuestionsOnAHomeSetUp(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Dashboard.Port = 4871
	cfg.Database = config.Database{URI: "mongodb://db.example.invalid/x", Name: "x"}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "footage")}}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	if err := choice.Set(h, map[string]bool{"renderer": true, "ffmpeg": false}, "setup"); err != nil {
		t.Fatal(err)
	}
	var q SetupQuestions
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &q)
	if !q.SetUp || q.Port != 4871 || q.Storage.Folder != filepath.Join(h, "footage") || q.Database.Suggested != "elsewhere" || q.Database.Here {
		t.Fatalf("questions on a home set up: %+v", q)
	}
	ticked := map[string]bool{}
	for _, c := range q.Components {
		ticked[c.Name] = c.Ticked
	}
	if !ticked["renderer"] || ticked["ffmpeg"] || !ticked["transcription"] {
		t.Errorf("ticked: %v", ticked)
	}
}

// `service status --json` says which MetaRush runs for the home and what
// build answers (D474): the Mac app checks it after an update.
func TestServiceStatusSaysWhatRuns(t *testing.T) {
	setupEnv(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	hs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/health" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(api.Health{OK: true, Info: version.Describe("0.16.0", "abc1234", "2026-11-03T00:00:00Z"), Installation: "studio"})
	}))
	t.Cleanup(hs.Close)
	u, _ := url.Parse(hs.URL)
	port, _ := strconv.Atoi(u.Port())
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	status := func() service.Status {
		t.Helper()
		var st service.Status
		decodeData(t, runCLI(t, h, "service", "status", "--json"), &st)
		return st
	}
	if st := status(); st.Daemon != nil || st.Installed {
		t.Fatalf("nothing runs yet: %+v", st)
	}
	record := func(mode string) {
		t.Helper()
		d := service.Daemon{PID: os.Getpid(), Port: port, StartedAt: time.Now().UTC(), Version: "0.15.0", Mode: mode}
		b, _ := json.Marshal(d)
		if err := os.MkdirAll(filepath.Join(h, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h, "state", "daemon.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	record("full")
	d := status().Daemon
	if d == nil || !d.Answering || d.Version != "0.16.0" || d.Commit != "abc1234" || d.PID != os.Getpid() || d.Port != port {
		t.Fatalf("the daemon that answers: %+v", d)
	}
	// A worker without the dashboard: what its record says.
	record("worker-only")
	if d := status().Daemon; d == nil || d.Answering || d.Version != "0.15.0" || d.Commit != "" || d.Mode != "worker-only" {
		t.Fatalf("a worker: %+v", d)
	}
	plain := runCLI(t, h, "service", "status", "--details")
	if !strings.Contains(plain.stdout, "MetaRush 0.15.0") {
		t.Errorf("details: %s", plain.stdout)
	}
}

// doctor --json carries each check's plain words (D474): a problem always has
// a line a person can read.
func TestDoctorJSONSaysItPlainly(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, "doctor", "--json")
	var out struct {
		OK     bool    `json:"ok"`
		Checks []Check `json:"checks"`
	}
	decodeData(t, r, &out)
	if out.OK || r.code != 1 {
		t.Fatalf("doctor on a home not set up: ok=%v code=%d", out.OK, r.code)
	}
	found := false
	for _, ch := range out.Checks {
		if (ch.Status == "warn" || ch.Status == "fail") && ch.Say == "" {
			t.Errorf("a problem without plain words: %+v", ch)
		}
		if ch.Area == "config" && ch.Status == "fail" {
			found = ch.Say == "MetaRush isn't set up on this machine yet" && ch.Do == "Set it up first."
		}
	}
	if !found {
		t.Errorf("no plain words for the home not set up: %+v", out.Checks)
	}
}
