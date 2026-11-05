package cli

import (
	"context"
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
	record := func(mode, program string) {
		t.Helper()
		d := service.Daemon{PID: os.Getpid(), Port: port, StartedAt: time.Now().UTC(), Version: "0.15.0", Mode: mode, Program: program}
		b, _ := json.Marshal(d)
		if err := os.MkdirAll(filepath.Join(h, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h, "state", "daemon.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// This copy's daemon (it recorded this program), a newer build than this
	// test's (0.16.0 against VERSION's -dev).
	record("full", service.ThisProgram())
	d := status().Daemon
	if d == nil || !d.Answering || d.Version != "0.16.0" || d.Commit != "abc1234" || d.PID != os.Getpid() || d.Port != port {
		t.Fatalf("the daemon that answers: %+v", d)
	}
	if d.SameCopy == nil || !*d.SameCopy || d.Program != service.ThisProgram() || !d.Newer {
		t.Fatalf("this copy, newer: %+v", d)
	}
	// Another copy's (D473): a program elsewhere, or a link to this one,
	// recorded as the link's target when it started, so changing the link
	// later changes nothing.
	record("full", "/opt/elsewhere/bin/metarush")
	if d := status().Daemon; d == nil || d.SameCopy == nil || *d.SameCopy {
		t.Fatalf("another copy: %+v", d)
	}
	// A build from before the record named its program: not known.
	record("full", "")
	if d := status().Daemon; d == nil || d.SameCopy != nil {
		t.Fatalf("a daemon that didn't say: %+v", d)
	}
	// A worker without the dashboard: what its record says.
	record("worker-only", "")
	if d := status().Daemon; d == nil || d.Answering || d.Version != "0.15.0" || d.Commit != "" || d.Mode != "worker-only" || d.Newer {
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

// otherService writes the background service of home h as another copy of
// MetaRush would have set it up: its program is a stand-in that says it is
// version v (`version --json`) and, with worker, runs without the dashboard.
// Nothing is loaded: the service managers never run under go test.
func otherService(t *testing.T, h, v string, worker bool) string {
	t.Helper()
	prog := filepath.Join(t.TempDir(), "other", "metarush")
	if err := os.MkdirAll(filepath.Dir(prog), 0o755); err != nil {
		t.Fatal(err)
	}
	// v "": a program that doesn't say its version.
	script := "#!/bin/sh\nif [ \"$1\" = version ]; then printf '{\"ok\": true, \"data\": {\"version\": \"" + v + "\"}}\\n'; fi\n"
	if v == "" {
		script = "#!/bin/sh\nexit 1\n"
	}
	if err := os.WriteFile(prog, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	serviceFor(t, h, prog, worker)
	return prog
}

// serviceFor writes home h's background service as starting prog (nothing is
// loaded: the service managers never run under go test).
func serviceFor(t *testing.T, h, prog string, worker bool) {
	t.Helper()
	plan, err := service.Options{Home: h, Exe: prog, WorkerOnly: worker}.InstallPlan()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(plan.File), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(plan.File, []byte(plan.Content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Another copy's background service is never suggested away (D473): setup
// asks a question of its own, its answer no, and doesn't ask at all when that
// copy is newer than this one; a worker stays a worker.
func TestSetupQuestionsWhenAnotherCopyRunsTheService(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Database = config.Database{URI: "mongodb://db.example.invalid/x", Name: "x"}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	otherService(t, h, "0.1.0", true)
	var q SetupQuestions
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &q)
	sv := q.Service
	if !sv.Installed || !sv.OtherCopy || sv.Suggested || sv.Newer || sv.OtherVersion != "0.1.0" || !sv.WorkerOnly || !strings.Contains(sv.Says, "leaves it as it is") {
		t.Fatalf("an older copy's service: %+v", sv)
	}
	otherService(t, h, "99.0.0", false)
	var newer SetupQuestions
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &newer)
	sv = newer.Service
	if !sv.OtherCopy || sv.Suggested || !sv.Newer || sv.OtherVersion != "99.0.0" || sv.WorkerOnly || !strings.Contains(sv.Says, "newer than this one") {
		t.Fatalf("a newer copy's service: %+v", sv)
	}
	for _, f := range plainwords.CheckShown(sv.Says) {
		t.Errorf("%q shows %s", sv.Says, f)
	}

	// service install --keep-newer (the app's) leaves the newer copy alone.
	r := runCLI(t, h, "service", "install", "--keep-newer", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "E_CONFLICT") || !strings.Contains(r.stdout, "newer copy (version 99.0.0)") {
		t.Fatalf("install over a newer copy: %d %s %s", r.code, r.stdout, r.stderr)
	}

	// A copy that doesn't say its version may be newer: not offered, and
	// --keep-newer leaves it alone too.
	otherService(t, h, "", false)
	var silent SetupQuestions
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &silent)
	if sv := silent.Service; !sv.OtherCopy || sv.Suggested || sv.Newer || !sv.VersionUnknown || sv.OtherVersion != "" || sv.Says != otherUnknownSays {
		t.Fatalf("a copy that doesn't say its version: %+v", sv)
	}
	r = runCLI(t, h, "service", "install", "--keep-newer", "--json")
	if r.code == 0 || !strings.Contains(r.stdout, "E_CONFLICT") || !strings.Contains(r.stdout, "doesn't say which version") {
		t.Fatalf("install over a copy of unknown version: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

// What the other copy is, from both its daemon and its program: an older copy
// opened by its dashboard can't hide a newer program the service starts; a
// program that is gone can't be newer; nothing answering isn't known (D473).
func TestOtherCopyAsksBoth(t *testing.T) {
	setupEnv(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	record := func(v string) {
		t.Helper()
		// Another copy's worker (its version from its record: no dashboard to ask).
		b, _ := json.Marshal(service.Daemon{PID: os.Getpid(), StartedAt: time.Now().UTC(), Version: v, Mode: "worker-only", Program: "/opt/elsewhere/bin/metarush"})
		if err := os.MkdirAll(filepath.Join(h, "state"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h, "state", "daemon.json"), b, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	other := func() otherCopy {
		t.Helper()
		exe, err := service.StableExecutable()
		if err != nil {
			t.Fatal(err)
		}
		return otherCopyOf(context.Background(), cfg, service.Options{Home: h, Exe: exe}.Get(context.Background()))
	}
	record("0.1.0")
	otherService(t, h, "99.0.0", false)
	if o := other(); !o.Present || !o.Newer || !o.Known || o.Version != "99.0.0" {
		t.Errorf("an older daemon, a newer program: %+v", o)
	}
	otherService(t, h, "0.1.1", false)
	if o := other(); !o.Present || o.Newer || !o.Known || o.Version != "0.1.1" || !o.mayTakeOver() {
		t.Errorf("both older: %+v", o)
	}
	record("99.0.1")
	if o := other(); !o.Newer || o.Version != "99.0.1" {
		t.Errorf("a newer daemon, an older program: %+v", o)
	}
	if err := os.Remove(filepath.Join(h, "state", "daemon.json")); err != nil {
		t.Fatal(err)
	}
	otherService(t, h, "", false)
	if o := other(); !o.Present || o.Newer || o.Known || o.mayTakeOver() {
		t.Errorf("nothing says its version: %+v", o)
	}
	serviceFor(t, h, filepath.Join(t.TempDir(), "gone", "metarush"), false)
	if o := other(); !o.Present || o.Newer || !o.Known {
		t.Errorf("the service's program is gone: %+v", o)
	}
}

// setup run from the program a link leads to (~/.local/bin/metarush → this
// one) is this copy: the service isn't another copy's, so setup suggests it
// and sets a stopped one up again rather than leaving it (D473).
func TestSetupFromTheLinksTargetIsThisCopy(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Database = config.Database{URI: "mongodb://db.example.invalid/x", Name: "x"}
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	exe, err := service.StableExecutable()
	if err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "bin", "metarush")
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(exe, link); err != nil {
		t.Fatal(err)
	}
	serviceFor(t, h, link, false)
	var q SetupQuestions
	decodeData(t, runCLI(t, h, "setup", "questions", "--json"), &q)
	if sv := q.Service; !sv.Installed || sv.OtherCopy || !sv.Suggested || sv.Running {
		t.Fatalf("a service through a link to this program: %+v", sv)
	}
	st := service.Options{Home: h, Exe: exe}.Get(context.Background())
	if got := decideService(true, false, st, otherCopy{}); got != serviceInstall {
		t.Errorf("its stopped service: %v, want it set up again", got)
	}
}

// What setup does with the background service: another copy's is taken over
// only when a person says so, never by an older copy, and this copy's running
// one is left running.
func TestDecideService(t *testing.T) {
	this := service.Status{Installed: true, Running: true, SameBinary: true}
	other := service.Status{Installed: true, Running: true, OtherCopy: true}
	none := service.Status{}
	for _, c := range []struct {
		name             string
		wanted, takeOver bool
		st               service.Status
		o                otherCopy
		want             serviceChoice
	}{
		{"not wanted", false, false, other, otherCopy{}, serviceLeave},
		{"nothing there", true, false, none, otherCopy{}, serviceInstall},
		{"this copy runs it", true, false, this, otherCopy{}, serviceRunning},
		{"this copy, stopped", true, false, service.Status{Installed: true, SameBinary: true}, otherCopy{}, serviceInstall},
		{"another copy, nobody said", true, false, other, otherCopy{Version: "0.1.0", Known: true}, serviceKeepOther},
		{"another copy, a person said yes", true, true, other, otherCopy{Version: "0.1.0", Known: true}, serviceInstall},
		{"another copy whose program is gone, yes", true, true, other, otherCopy{Present: true, Known: true}, serviceInstall},
		{"a newer copy, even with yes", true, true, other, otherCopy{Version: "9.0.0", Newer: true, Known: true}, serviceKeepNewer},
		{"a newer copy, nobody said", true, false, other, otherCopy{Version: "9.0.0", Newer: true, Known: true}, serviceKeepNewer},
		{"a version not known, even with yes", true, true, other, otherCopy{Present: true}, serviceKeepUnknown},
		{"a version not known, nobody said", true, false, other, otherCopy{Present: true}, serviceKeepUnknown},
	} {
		if got := decideService(c.wanted, c.takeOver, c.st, c.o); got != c.want {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
}

// Setting the service up again (taking another copy's place, say) keeps its
// mode: a worker's service is written with --worker-only again.
func TestSetupKeepsAWorkerAWorker(t *testing.T) {
	u := t.TempDir()
	o := service.Options{Home: filepath.Join(u, "home"), Exe: "/Applications/MetaRush.app/Contents/Helpers/metarush", UserHome: u, OS: "darwin", UID: 501}
	for _, c := range []struct {
		st     service.Status
		worker bool
	}{
		{service.Status{Installed: true, WorkerOnly: true, OtherCopy: true}, true},
		{service.Status{Installed: true, OtherCopy: true}, false},
		{service.Status{}, false},
	} {
		plan, err := sameMode(o, c.st).InstallPlan()
		if err != nil {
			t.Fatal(err)
		}
		if got := strings.Contains(plan.Content, "--worker-only"); got != c.worker {
			t.Errorf("%+v: --worker-only %v, want %v", c.st, got, c.worker)
		}
	}
}
