package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakebrew"
	"github.com/metavert-io/metarush/internal/testutil/fakenode"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/worker"
)

// bareComputer is a computer with none of MetaRush's optional programs: only
// what a test puts in the returned folders (the stand-in Homebrew's).
func bareComputer(t *testing.T) (brew, bin string) {
	t.Helper()
	brew, bin, _ = fakebrew.Write(t)
	t.Setenv(tools.EnvToolDirs, bin)
	t.Setenv(components.EnvBase, "")
	t.Setenv("CLAUDECODE", "")
	os.Unsetenv("CLAUDECODE")
	t.Setenv("METARUSH_AGENT", "")
	return brew, bin
}

func setupArgs(t *testing.T, db string, extra ...string) []string {
	return append([]string{"setup", "--yes", "--json", "--database", "elsewhere", "--db-uri", testutil.URI(), "--db-name", db, "--test-db",
		"--secrets", "env", "--service=false", "--skills=false", "--port", strconv.Itoa(freeTCPPort(t)), "--workspace-name", "Garden club",
		"--user-name", "Sam", "--user-email", "sam@example.com"}, extra...)
}

// MetaRush starts and its dashboard opens with only the required
// components (D324): setup with no optional components, then the dashboard,
// Getting started and Settings → Components say what is missing and offer
// to add it, a person adds one from the dashboard, and the features that
// need what is missing say so plainly instead of failing.
func TestStartsWithOnlyTheRequiredComponents(t *testing.T) {
	setupEnv(t)
	brew, _ := bareComputer(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, setupArgs(t, db.Name, "--no-components")...)
	var res SetupResult
	decodeData(t, r, &res)
	if len(res.Components.Chosen) != 0 || len(res.Components.Added) != 0 || res.Components.ForAPerson {
		t.Fatalf("components: %+v", res.Components)
	}
	for _, n := range []string{"ffmpeg", "renderer", "transcription", "link-import"} {
		if !slices.Contains(res.Components.LeftOut, n) || !choice.Declined(h, n) {
			t.Errorf("%s isn't recorded as left out: %v", n, res.Components.LeftOut)
		}
	}
	works := map[string]firstrun.WorksItem{}
	for _, w := range res.WhatWorks.NoKeys {
		works[w.Key] = w
	}
	if w := works["renders"]; w.Ready || w.Component != "renderer" || !strings.Contains(w.Next, "Settings → Components") {
		t.Errorf("renders: %+v", w)
	}
	if w := works["transcripts"]; w.Ready || w.Component != "transcription" {
		t.Errorf("transcripts: %+v", w)
	}
	if w := works["editing"]; w.Ready || w.Component != "ffmpeg" {
		t.Errorf("editing: %+v", w)
	}

	// The dashboard, the way the daemon serves it.
	if err := config.SetField(h, []string{"tools", "brew"}, brew); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatal(err)
	}
	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)
	u, _ := url.Parse(hs.URL)
	cfg.Dashboard.Port, _ = strconv.Atoi(u.Port())
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	if applied, err := a.DB.Migrate(ctx); err != nil {
		t.Fatal(applied, err)
	}
	srv := api.New(a)
	t.Cleanup(srv.Assistant.Close)
	hs.Config.Handler = srv.Handler()
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar}
	res0, err := client.Get(hs.URL + "/api/health")
	if err != nil || res0.StatusCode != http.StatusOK {
		t.Fatalf("health: %v %v", err, res0)
	}
	res0.Body.Close()
	nonce, err := api.MintLaunchNonce(cfg.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	signin, err := client.Get(hs.URL + "/auth?t=" + nonce + "&next=%2F%3Fwelcome%3D1")
	if err != nil || signin.StatusCode != http.StatusOK {
		t.Fatalf("the dashboard didn't open: %v %v", err, signin)
	}
	signin.Body.Close()
	op := func(name string, in, out any) (int, string) {
		t.Helper()
		b, _ := json.Marshal(in)
		req, _ := http.NewRequest(http.MethodPost, hs.URL+"/api/ops/"+name, bytes.NewReader(b))
		req.Header.Set("X-MetaRush-Client", "dashboard")
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		_ = json.Unmarshal(body, &env)
		if out != nil && resp.StatusCode == http.StatusOK {
			if err := json.Unmarshal(env.Data, out); err != nil {
				t.Fatalf("%s: %v %s", name, err, body)
			}
		}
		return resp.StatusCode, string(body)
	}
	var st firstrun.SetupStatusOut
	if code, body := op("setup.status", map[string]any{}, &st); code != http.StatusOK {
		t.Fatalf("Getting started: %d %s", code, body)
	}
	if !st.FirstRun || len(st.Components) == 0 || !st.Components[0].Suggested {
		t.Fatalf("Getting started offers no components: first run %v, %d", st.FirstRun, len(st.Components))
	}
	var list components.ComponentsOut
	if code, body := op("components.list", map[string]any{}, &list); code != http.StatusOK {
		t.Fatalf("Settings → Components: %d %s", code, body)
	}
	for _, s := range list.Components {
		switch {
		case s.Required && s.Name != "database" && !s.Ready:
			t.Errorf("required %s isn't ready: %+v", s.Name, s)
		case !s.Required && s.Ready && s.Name != "sandbox":
			t.Errorf("%s is ready on a bare computer: %+v", s.Name, s)
		}
		for _, f := range plainwords.Check(s.Says) {
			t.Errorf("%s says: %s", s.Name, f)
		}
	}
	// A person adds PDF pages from the dashboard (Homebrew's stand-in).
	var added components.ComponentAddOut
	if code, body := op("components.add", components.ComponentAddIn{Name: "pdf-pages"}, &added); code != http.StatusOK || !added.Component.Ready {
		t.Fatalf("Add it: %d %s", code, body)
	}

	// The features that need what is missing say so, with the one step.
	rs := renderer.New(a.Cfg, a.Tools).Check(false)
	if !strings.HasPrefix(rs.Problem, "Rendering needs") || !strings.Contains(rs.Command(), "metarush components add") {
		t.Errorf("rendering: %q %q", rs.Problem, rs.Command())
	}
	if msg := (&tools.ErrMissing{Name: tools.FFprobe}).Error(); !strings.Contains(msg, "FFmpeg") || !strings.Contains(msg, "Settings → Components") {
		t.Errorf("bringing in footage: %q", msg)
	}
	w := worker.New(a)
	dev := models.WorkerDevice{ID: a.Cfg.Installation.ID, Name: "studio-mac", JobTypes: w.JobTypes(), Lacks: w.Lacks(), LastHeartbeat: time.Now()}
	for _, typ := range []string{jobs.TypeTranscribe, jobs.TypeProxy, jobs.TypeRender} {
		why := jobs.Why(models.Job{Type: typ, State: models.JobQueued}, []models.WorkerDevice{dev}, time.Now())
		if why == nil || why.Code != jobs.WaitComponent || why.Component == "" || !strings.Contains(why.Text, "Settings → Components") {
			t.Errorf("a waiting %s job: %+v", typ, why)
		}
	}

	// The command line says the same.
	r = runCLI(t, h, "components")
	if r.code != 0 || !strings.Contains(r.stdout, "You can add:") || !strings.Contains(r.stdout, "What MetaRush needs to run:") {
		t.Fatalf("metarush components: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

// --components adds exactly the ones named (and what they need), --yes the
// suggested ones; --json says what each unlocks; running setup again adds
// what was chosen and is missing, and leaves the rest alone.
func TestSetupComponentFlags(t *testing.T) {
	setupEnv(t)
	brew, _ := bareComputer(t)
	t.Setenv("METARUSH_BREW", brew)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, setupArgs(t, db.Name, "--components", "pdf-pages,link-import")...)
	var res SetupResult
	decodeData(t, r, &res)
	c := res.Components
	if !slices.Equal(c.Chosen, []string{"link-import", "pdf-pages"}) || len(c.Added) != 2 || len(c.Failed) != 0 {
		t.Fatalf("components: %+v", c)
	}
	for _, a := range c.Added {
		if !a.Done || a.Unlocks == "" || a.How != components.HowHomebrew {
			t.Errorf("added: %+v", a)
		}
	}
	if !slices.Contains(c.LeftOut, "renderer") || !choice.Declined(h, "renderer") {
		t.Errorf("the renderer isn't left out: %v", c.LeftOut)
	}
	// Both flags at once are refused; an unknown name says which there are.
	if r := runCLI(t, filepath.Join(t.TempDir(), "x"), setupArgs(t, db.Name, "--components", "ffmpeg", "--no-components")...); r.code == 0 {
		t.Error("--components with --no-components")
	}
	if r := runCLI(t, filepath.Join(t.TempDir(), "y"), setupArgs(t, db.Name, "--components", "teleporter")...); r.code == 0 || !strings.Contains(r.stdout+r.stderr, "teleporter") {
		t.Errorf("unknown: %d %s %s", r.code, r.stdout, r.stderr)
	}

	// Again with the suggested answers: what was chosen stays, what was left
	// out stays out, nothing is installed twice.
	r = runCLI(t, h, "setup", "--yes", "--json", "--service=false", "--skills=false")
	var again SetupResult
	decodeData(t, r, &again)
	if len(again.Components.Added) != 0 || len(again.Components.Failed) != 0 || !slices.Contains(again.Components.Ready, "pdf-pages") {
		t.Fatalf("again: %+v", again.Components)
	}
	// Again naming one more: it is added.
	r = runCLI(t, h, "setup", "--yes", "--json", "--service=false", "--skills=false", "--components", "google-drive")
	decodeData(t, r, &again)
	if len(again.Components.Added) != 1 || again.Components.Added[0].Name != "google-drive" {
		t.Fatalf("again with google-drive: %+v", again.Components)
	}
}

// The suggested ones under --yes; an AI app running setup installs nothing
// and leaves them for a person (D327); the renderer's licence is said when
// it is added.
func TestSetupSuggestedAndAgents(t *testing.T) {
	setupEnv(t)
	brew, _ := bareComputer(t)
	t.Setenv("METARUSH_BREW", brew)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	t.Setenv("CLAUDECODE", "1")
	r := runCLI(t, h, setupArgs(t, db.Name)...)
	var res SetupResult
	decodeData(t, r, &res)
	if !res.Components.ForAPerson || len(res.Components.Added) != 0 || len(res.Components.NeedsYou) == 0 {
		t.Fatalf("an AI app ran setup: %+v", res.Components)
	}
	if !slices.Contains(res.Components.Chosen, "renderer") || !slices.Contains(res.Components.Chosen, "transcription") || slices.Contains(res.Components.Chosen, "other-languages") {
		t.Errorf("suggested: %v", res.Components.Chosen)
	}
	if !slices.Contains(res.Next, "metarush components add ffmpeg") {
		t.Errorf("next: %v", res.Next)
	}
	if ch, _ := choice.Of(h, "renderer"); ch.Wanted || !ch.Waiting {
		t.Errorf("nobody decided, so the renderer waits for a person (not left out): %+v", ch)
	}
	// Nor in a terminal Claude Code runs.
	if r := runCLI(t, h, "components", "add", "pdf-pages"); r.code == 0 || !strings.Contains(r.stdout+r.stderr, "Only a person") {
		t.Errorf("components add by Claude Code: %d %s %s", r.code, r.stdout, r.stderr)
	}
}

// The checklist a person answers at a terminal: Enter takes the ticked
// (suggested) ones, numbers tick or untick, none takes none.
func TestSetupComponentChecklist(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	run := func(answers string) componentPick {
		t.Helper()
		cmd := setupCommand()
		if err := cmd.ParseFlags(nil); err != nil {
			t.Fatal(err)
		}
		o := &setupOpts{}
		old := stdin
		stdin = bufio.NewReader(strings.NewReader(answers))
		defer func() { stdin = old }()
		h := filepath.Join(t.TempDir(), "home")
		s := &setupRun{c: cmd, o: o, h: h, prompt: true}
		e := componentsEnv(config.Default(h), tools.New(config.Tools{}))
		if err := s.chooseComponents(e, false); err != nil {
			t.Fatal(err)
		}
		return s.pick
	}
	all := run("\n")
	if !all.decided || !slices.Contains(all.names, "renderer") || slices.Contains(all.names, "other-languages") {
		t.Fatalf("Enter: %+v", all)
	}
	// The offered rows, numbered as the checklist shows them.
	e := componentsEnv(config.Default(t.TempDir()), tools.New(config.Tools{}))
	var rows []string
	for _, c := range e.Optional() {
		if e.Find(context.Background(), c).NotHere == "" {
			rows = append(rows, c.Name)
		}
	}
	num := func(name string) string { return strconv.Itoa(slices.Index(rows, name) + 1) }
	p := run(num("renderer") + " " + num("other-languages") + "\n\n")
	if slices.Contains(p.names, "renderer") || !slices.Contains(p.names, "other-languages") || !slices.Contains(p.names, "ffmpeg") {
		t.Fatalf("untick the renderer, tick other languages: %+v", p.names)
	}
	if none := run("none\n"); len(none.names) != 0 || !none.decided {
		t.Fatalf("none: %+v", none)
	}
}

// Doctor reads the components' detection: on a bare computer a missing
// FFmpeg or Node.js is a warning that names the component and its one
// step, never ./bootstrap.sh, and nothing optional fails the check.
func TestDoctorPointsAtComponents(t *testing.T) {
	setupEnv(t)
	bareComputer(t)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, setupArgs(t, db.Name, "--no-components")...)
	if r.code != 0 {
		t.Fatalf("setup: %s %s", r.stdout, r.stderr)
	}
	r = runCLI(t, h, "doctor", "--json")
	var d struct {
		Checks []Check `json:"checks"`
	}
	decodeData(t, r, &d)
	fixes := map[string]Check{}
	for _, c := range d.Checks {
		if strings.Contains(c.Fix, "bootstrap") || strings.Contains(c.Detail, "bootstrap") {
			t.Errorf("%s/%s points at bootstrap: %q", c.Area, c.Name, c.Fix)
		}
		if c.Status == "fail" && c.Area != "dashboard" {
			t.Errorf("%s/%s fails on a computer with only the required components: %s", c.Area, c.Name, c.Detail)
		}
		fixes[c.Area+"/"+c.Name] = c
	}
	for key, want := range map[string]string{"tools/ffmpeg": "metarush components add ffmpeg", "tools/node": "metarush components add node",
		"tools/transcription": "metarush components add transcription", "tools/PDF pages": "metarush components add pdf-pages", "components/optional": "metarush components"} {
		if c, ok := fixes[key]; !ok || c.Fix != want || c.Status == "ok" {
			t.Errorf("%s: %+v", key, c)
		}
	}
	plain := runCLI(t, h, "doctor")
	if !strings.Contains(plain.stdout, "Settings → Components") || !strings.Contains(plain.stdout, "COMPONENTS") {
		t.Errorf("doctor's plain words: %s", plain.stdout)
	}
	for _, f := range plainAllow.Filter(plainwords.Check(plain.stdout)) {
		t.Errorf("doctor: %s", f)
	}
}

// Without a person at a terminal setup installs only the components named
// with --components: the suggested ones are listed for a person, nothing is
// recorded as left out, and running setup again never adds a component
// nobody chose (D327). At a person's terminal --yes takes the suggested ones.
func TestSetupWithoutAPersonInstallsOnlyWhatIsNamed(t *testing.T) {
	setupEnv(t)
	brew, _ := bareComputer(t)
	t.Setenv("METARUSH_BREW", brew)
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, setupArgs(t, db.Name)...)
	var res SetupResult
	decodeData(t, r, &res)
	c := res.Components
	if len(c.Added) != 0 || len(c.Failed) != 0 || !c.ForAPerson || len(c.NeedsYou) == 0 {
		t.Fatalf("a script with the suggested answers installed something: %+v", c)
	}
	if ch, _ := choice.Of(h, "renderer"); len(c.LeftOut) != 0 || ch.Wanted || !ch.Waiting {
		t.Errorf("nobody decided, so the renderer waits for a person (not left out): %v %+v", c.LeftOut, ch)
	}
	// Again, still without a person: nothing is added.
	r = runCLI(t, h, "setup", "--yes", "--json", "--service=false", "--skills=false")
	var again SetupResult
	decodeData(t, r, &again)
	if len(again.Components.Added) != 0 || len(again.Components.Failed) != 0 {
		t.Fatalf("setup again added what nobody chose: %+v", again.Components)
	}
	// At a person's terminal --yes takes the suggested ones (here: what the
	// stand-in Homebrew can add).
	old := isInteractive
	isInteractive = func() bool { return true }
	defer func() { isInteractive = old }()
	h2 := filepath.Join(t.TempDir(), "home2")
	r = runCLI(t, h2, setupArgs(t, testutil.DB(t).Name)...)
	var person SetupResult
	decodeData(t, r, &person)
	added := map[string]bool{}
	for _, a := range person.Components.Added {
		added[a.Name] = true
	}
	if person.Components.ForAPerson || !added["pdf-pages"] || !added["ffmpeg"] {
		t.Fatalf("a person's --yes: %+v", person.Components)
	}
}

// Setup run by an AI app (or a script) leaves the suggested components
// waiting for a person, and nothing sets them up by itself afterwards: not
// the daemon's first start (FirstUse), not `metarush worker start` (D327).
func TestSetupByAnAIAppLeavesComponentsWaiting(t *testing.T) {
	setupEnv(t)
	brew, _ := bareComputer(t)
	t.Setenv("METARUSH_BREW", brew)
	t.Setenv("CLAUDECODE", "1")
	db := testutil.DB(t)
	h := filepath.Join(t.TempDir(), "home")
	r := runCLI(t, h, setupArgs(t, db.Name)...)
	var res SetupResult
	decodeData(t, r, &res)
	if !res.Components.ForAPerson {
		t.Fatalf("components: %+v", res.Components)
	}
	ch, ok := choice.Of(h, renderer.Component)
	if !ok || ch.Wanted || !ch.Waiting {
		t.Fatalf("the renderer isn't waiting for a person: %+v %v", ch, ok)
	}
	pkg, _ := renderer.Embedded()
	fake := fakenode.Write(t, pkg.Remotion())
	cfg, err := config.Load(h)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Tools.Node, cfg.Tools.NPM = fake.Node, fake.NPM // Node.js already on this computer
	env := renderer.New(cfg, tools.New(cfg.Tools))
	if st := env.Check(false); st.State != renderer.StateLeftOut {
		t.Errorf("state: %s", st.State)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	env.FirstUse(ctx, t.Logf)
	if npmRan(fake) {
		t.Fatal("after setup by an AI app, the daemon's first start set the renderer up")
	}
}
