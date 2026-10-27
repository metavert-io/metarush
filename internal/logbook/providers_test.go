package logbook

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeKey builds a key at run time that matches no credential pattern, so only
// the exact-value redaction (from the providers registry) can hide it.
func fakeKey(tag string) string {
	return "elv-" + tag + "-" + strings.Repeat("Qx7", 8) + "-zz9k"
}

// Keys the providers registry reports reach every logger of the process —
// open ones and later ones — and the hook's fingerprints; the daemon asks the
// secret sources for keys nobody has read yet.
func TestProcessSecretsReachEveryLogger(t *testing.T) {
	e := newEnv(t, false)
	before := e.open(t, nil)
	early := fakeKey("early")
	RememberSecret(early)
	after := e.open(t, nil)
	for i, l := range []*Logger{before, after} {
		if got := l.Redactor().String("key " + early + " here"); strings.Contains(got, early) {
			t.Fatalf("logger %d did not learn the key: %q", i, got)
		}
	}
	// A secret source (the providers registry) is asked by the daemon.
	sourced := fakeKey("sourced")
	cfg := e.cfg
	AddSecretSource(func(c *config.Config) []string {
		if c == cfg {
			return []string{sourced}
		}
		return nil
	})
	if got := after.Redactor().String(sourced); got != sourced {
		t.Fatal("a source must not be asked before the daemon starts")
	}
	ctx, cancel := context.WithCancel(context.Background())
	if err := os.MkdirAll(filepath.Join(e.cfg.ClaudeHooksDir(), "sessions"), 0o700); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() { after.RunDaemon(ctx, nil); close(done) }()
	deadline := time.Now().Add(5 * time.Second)
	for after.Redactor().String(sourced) == sourced && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if got := before.Redactor().String(sourced); got == sourced {
		t.Fatal("the daemon's secret sources did not reach the other logger")
	}
	// The hook (which cannot read the keychain) hides both through fingerprints.
	for time.Now().Before(deadline) {
		if fp := LoadFingerprints(e.cfg.ClaudeHooksDir()); fp != nil && !strings.Contains(fp.Redact(early+" "+sourced), "elv-") {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if fp := LoadFingerprints(e.cfg.ClaudeHooksDir()); fp == nil || strings.Contains(fp.Redact(early+" "+sourced), "elv-") {
		t.Fatalf("fingerprints do not hide the provider keys")
	}
	cancel()
	<-done
}

// A key given to an operation (saving or testing it) is hidden in everything
// that call logs — also the requests it made before its own entry was written.
func TestSecretInputsAreHiddenInTheCallsRequests(t *testing.T) {
	e := newEnv(t, true)
	l := e.open(t, e.ms)
	key := fakeKey("candidate")
	raw, _ := json.Marshal(map[string]any{"name": "elevenlabs", "key": key, "skipTest": false})
	if got := SecretInputs(raw); len(got) != 1 || got[0] != key {
		t.Fatalf("SecretInputs = %v", got)
	}
	if got := SecretInputs(json.RawMessage(`{"name":"elevenlabs","query":"a long search phrase"}`)); len(got) != 0 {
		t.Fatalf("no secret fields, got %v", got)
	}
	ctx := context.Background()
	rec := l.StartOp(ctx, "provider.test", raw, models.Actor{Via: "cli"}, true, false)
	octx := rec.Context(ctx)
	ProviderDone(octx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "check the plan", Method: "GET", Endpoint: "/v1/user/subscription", Status: 401},
		apperr.New(apperr.NotConfigured, "ElevenLabs did not accept the key %s", key))
	rec.End(false, nil, errors.New("rejected "+key))
	l.Flush(ctx)
	es := e.entries(t, nil)
	if len(es) != 2 {
		t.Fatalf("want the request and the call, got %d", len(es))
	}
	if hits := scanFor(t, e.db, nil, []string{key}); len(hits) > 0 {
		t.Fatalf("the candidate key leaked:\n%s", strings.Join(hits, "\n"))
	}
}

// Requests to online services and jobs an operation runs itself: the request
// belongs to the job (See log on the job shows it), the job events keep the
// operation's channel, successful dashboard reads are not listed, and the rows
// read plainly.
func TestProviderCallsAndInProcessJobs(t *testing.T) {
	e := newEnv(t, true)
	l := e.open(t, e.ms)
	ctx := context.Background()
	rec := l.StartOp(ctx, "voiceover.tts", json.RawMessage(`{"revisionId":"rev_01h00000000000000000000000"}`), models.Actor{Via: "cli"}, true, false)
	octx := rec.Context(ctx)
	job := models.Job{ID: ids.New(ids.Job), Type: "voiceover", State: models.JobRunning, ProjectID: "prj_01h00000000000000000000000",
		Payload: map[string]any{"action": "tts", "lines": 2, "revisionNumber": 3}, CreatedAt: time.Now()}
	l.JobEvent(octx, jobs.Event{Kind: jobs.EventLeased, Job: job, WorkerID: e.cfg.Installation.ID, WorkerName: "studio-mac"})
	ProviderDone(octx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "make speech", Method: "POST", Endpoint: "/v1/text-to-speech/voice_ada/with-timestamps",
		Status: 200, Duration: 900 * time.Millisecond, Characters: 35, Bytes: 4096, RequestID: "req-1"}, nil)
	l.ToolRun(octx, tools.RunInfo{Tool: "ffmpeg", Args: []string{"-i", "take.mp3"}, Duration: time.Second})
	job.State = models.JobSucceeded
	job.Result = map[string]any{"generated": 2, "credits": 61.0}
	l.JobEvent(octx, jobs.Event{Kind: jobs.EventSucceeded, Job: job, WorkerID: e.cfg.Installation.ID})
	// After the job: a request is the operation's own again.
	ProviderDone(octx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "check the plan", Method: "GET", Endpoint: "/v1/user/subscription", Status: 200}, nil)
	rec.End(true, map[string]any{"jobId": job.ID}, nil)
	// The dashboard: a successful read is not listed, a failed one is.
	drec := l.StartOp(ctx, "voiceover.voices", nil, models.Actor{Via: "dashboard"}, true, true)
	dctx := drec.Context(ctx)
	ProviderDone(dctx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "list voices", Method: "GET", Endpoint: "/v2/voices", Status: 200}, nil)
	ProviderDone(dctx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "list voices", Method: "GET", Endpoint: "/v2/voices", Status: 429},
		apperr.New(apperr.Provider, "ElevenLabs is busy (too many requests); try again in a minute"))
	drec.End(false, nil, nil)
	l.Flush(ctx)

	provider := e.entries(t, bson.M{"kind": KindProvider})
	if len(provider) != 3 {
		t.Fatalf("want 3 provider entries (speech, plan check, failed dashboard read), got %d: %+v", len(provider), provider)
	}
	byWhat := map[string]Entry{}
	for _, p := range provider {
		byWhat[p.Provider.What] = p
	}
	speech := byWhat["make speech"]
	if speech.Refs.JobID != job.ID || speech.ParentID == "" || speech.Provider.Characters != 35 || speech.Channel != ChannelCLI || speech.Provider.RequestID != "req-1" {
		t.Fatalf("speech request: %+v", speech)
	}
	if plan := byWhat["check the plan"]; plan.Refs.JobID != "" {
		t.Fatalf("a request after the job is not part of it: %+v", plan.Refs)
	}
	if busy := byWhat["list voices"]; busy.Level != LevelError || busy.Channel != ChannelDashboard || busy.Provider.Status != 429 {
		t.Fatalf("failed dashboard read: %+v", busy)
	}
	tool := e.entries(t, bson.M{"kind": KindTool})
	if len(tool) != 1 || tool[0].Refs.JobID != job.ID {
		t.Fatalf("a program run inside the job belongs to it: %+v", tool)
	}
	jobEntries := e.entries(t, bson.M{"kind": KindJob})
	if len(jobEntries) != 2 {
		t.Fatalf("job events: %d", len(jobEntries))
	}
	names := &Names{Projects: map[string]string{job.ProjectID: "Film"}}
	for _, j := range jobEntries {
		if j.Channel != ChannelCLI || !j.Job.InProcess {
			t.Fatalf("a job the command ran itself keeps its channel and says so: %s %v", j.Channel, j.Job.InProcess)
		}
		got := Describe(&j, names, "")
		want := map[string]string{JobLeased: "Started making the AI voice for “Film”, version 3", JobSucceeded: "Made the AI voice for “Film”, version 3"}[j.Job.Event]
		if !strings.HasPrefix(got, strings.ReplaceAll(want, "“Film”", "Film")) && !strings.HasPrefix(got, want) {
			t.Fatalf("job %s reads %q, want %q", j.Job.Event, got, want)
		}
		if j.Job.Event == JobSucceeded && (numOf(j.Job.Detail["credits"]) != 61 || numOf(j.Job.Detail["generated"]) != 2) {
			t.Fatalf("narration cost is kept: %+v", j.Job.Detail)
		}
	}
	if got := Describe(&speech, names, ""); got != "ElevenLabs made speech (35 characters)" {
		t.Fatalf("speech row: %q", got)
	}
	busy := byWhat["list voices"]
	if got := Describe(&busy, names, ""); got != "ElevenLabs couldn't list voices: is busy (too many requests); try again in a minute" {
		t.Fatalf("failed request row: %q", got)
	}
	// Successful requests are routine: hidden from the default view like program runs.
	es, _, err := List(context.Background(), e.db, e.cfg.Workspace.ID, Filter{HideRoutine: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, x := range es {
		if (x.Kind == KindProvider || x.Kind == KindTool) && x.Level == LevelInfo {
			t.Fatalf("routine entry shown: %s %s", x.Kind, x.Summary)
		}
	}
	// The everyday view tells the narration once: by the command's own entry.
	es, _, err = List(context.Background(), e.db, e.cfg.Workspace.ID, Filter{HideRoutine: true, HideInProcess: true, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	ops := 0
	for _, x := range es {
		if x.Kind == KindJob {
			t.Fatalf("an in-process job event is shown next to its command: %s", x.Summary)
		}
		if x.Kind == KindOp && x.Op.Name == "voiceover.tts" {
			ops++
		}
	}
	if ops != 1 {
		t.Fatalf("the command's entry is shown once, got %d", ops)
	}
}

// log.prompts: local keeps texts only when "this machine only" is true: not
// in a home that a repository would commit (or a sync folder would upload).
func TestLocalPromptsNeedASafeHome(t *testing.T) {
	e := newEnv(t, true)
	if err := os.Mkdir(filepath.Join(e.home, ".git"), 0o755); err != nil { // the home is inside a code repository
		t.Fatal(err)
	}
	e.cfg.Log.Prompts = config.PromptsLocal
	l := e.open(t, e.ms)
	if p := l.Status().Problem; !strings.Contains(p, "code repository") {
		t.Fatalf("problem: %q", p)
	}
	ctx := context.Background()
	l.Claude(ctx, Turn{Source: SourceAssistant, ConversationID: "asc_01h00000000000000000000000", Turn: 1, Prompt: "make it shorter",
		Response: strings.Repeat("Here is a shorter cut. ", 400), StartedAt: time.Now()})
	l.Flush(ctx)
	es := e.entries(t, bson.M{"kind": KindClaude})
	if len(es) != 1 || es[0].Claude.Privacy != PrivacyOff || es[0].Claude.Prompt != "" || es[0].Claude.PromptRef != nil || es[0].Claude.ResponseRef != nil {
		t.Fatalf("texts kept in an unsafe home: %+v", es)
	}
	if _, err := os.Stat(e.cfg.LogBodiesDir()); err == nil {
		t.Fatal("nothing may be written under the log-bodies folder")
	}
	// A plain local home keeps them.
	ok := newEnv(t, true)
	ok.cfg.Log.Prompts = config.PromptsLocal
	l2 := ok.open(t, ok.ms)
	if p := l2.Status().Problem; p != "" {
		t.Fatalf("unexpected problem: %q", p)
	}
	l2.Claude(ctx, Turn{Source: SourceAssistant, ConversationID: "asc_01h00000000000000000000001", Turn: 1, Prompt: "make it shorter", Response: "Done.", StartedAt: time.Now()})
	l2.Flush(ctx)
	if es := ok.entries(t, bson.M{"kind": KindClaude}); len(es) != 1 || es[0].Claude.Privacy != PrivacyLocal || es[0].Claude.PromptRef == nil {
		t.Fatalf("local texts: %+v", es)
	}
}

// Exports and bug reports never go into a repository (even an ignored path), a
// sync folder or a project folder.
func TestCheckOutputUsesSecretpath(t *testing.T) {
	home := t.TempDir()
	prev := inspector
	inspector = secretpath.Inspector{Home: home, Git: "-"}
	t.Cleanup(func() { inspector = prev })
	for _, dir := range []string{filepath.Join(home, "Dropbox", "shared"), filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs")} {
		err := CheckOutput(filepath.Join(dir, "log.jsonl"))
		if !apperr.Is(err, apperr.InvalidInput) || !strings.Contains(apperr.From(err).Message, "uploads everything") {
			t.Fatalf("%s: %v", dir, err)
		}
	}
	repo := filepath.Join(home, "code", "film")
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := CheckOutput(filepath.Join(repo, "ignored", "bundle.zip")); !apperr.Is(err, apperr.InvalidInput) || apperr.From(err).Hint == "" {
		t.Fatalf("repository: %v", err)
	}
	if err := CheckOutput(filepath.Join(home, "Downloads", "bundle.zip")); err != nil {
		t.Fatalf("Downloads refused: %v", err)
	}
}
