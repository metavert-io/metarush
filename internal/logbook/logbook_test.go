package logbook

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

// env is one test installation: home, config, test database, filesystem media store.
type env struct {
	home string
	cfg  *config.Config
	db   *store.DB
	ms   media.Store
}

func newEnv(t *testing.T, withDB bool) *env {
	t.Helper()
	// Tests run the way a person's terminal would, even under Claude Code.
	for _, k := range []string{"CLAUDECODE", "CLAUDE_CODE_SESSION_ID", "METARUSH_LOG_CHANNEL", "METARUSH_LOG_CONVERSATION", "METARUSH_ASSISTANT_RUN_DIR", "METARUSH_VIA"} {
		t.Setenv(k, "")
	}
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "Log test"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio-mac"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Tester"}
	ms, err := media.NewFS(filepath.Join(h, "media"), false)
	if err != nil {
		t.Fatal(err)
	}
	e := &env{home: h, cfg: cfg, ms: ms}
	if withDB {
		e.db = testutil.DB(t)
	}
	return e
}

func (e *env) open(t *testing.T, ms media.Store) *Logger {
	t.Helper()
	l := Open(Options{Cfg: e.cfg, DB: e.db, Media: ms, Actor: models.Actor{UserID: e.cfg.User.ID, UserName: e.cfg.User.Name, InstallationID: e.cfg.Installation.ID, Via: "cli"}})
	t.Cleanup(l.Close)
	return l
}

func (e *env) entries(t *testing.T, filter bson.M) []Entry {
	t.Helper()
	if filter == nil {
		filter = bson.M{}
	}
	filter["workspaceId"] = e.cfg.Workspace.ID
	var out []Entry
	cur, err := e.db.C(Collection).Find(context.Background(), filter)
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(context.Background(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// Secrets used by the redaction scenario. They look like real ones on purpose,
// so they are put together at run time (the repository's secret scan rightly
// flags such literals).
var (
	secretPassword = "Hunter2-very-secret-pw" // gitleaks:allow (made up for the test)
	secretS3Key    = "wJalrXUtnFEMI/K7MDENG/bPxRfiCYEXAMPLEKEY"
	secretEleven   = "sk" + "_9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822c"
	secretAWSID    = "AKIA" + "IOSFODNN7EXAMPLE"
	secretTigrisID = "tid" + "_AbCdEfGhIjKlMnOpQrStUv"
	secretTigris   = "tsec" + "_ZyXwVuTsRqPoNmLkJiHgFeDcBa0123456789"
	secretBearer   = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U" // gitleaks:allow (made up for the test)
	secretCookie   = "c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00c0ffee00"
	secretPEM      = "-----BEGIN " + "PRIVATE KEY-----\nMIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7\n-----END " + "PRIVATE KEY-----"
	secretEnvToken = "envtoken-1234567890-abcdefghij"
)

var allSecrets = []string{secretPassword, secretS3Key, secretEleven, secretAWSID, secretTigrisID, secretTigris, secretBearer, secretCookie, // gitleaks:allow (made up for the test)
	"MIIEvQIBADANBgkqhkiG9w0BAQEFAASCBKcwggSjAgEAAoIBAQC7", secretEnvToken} // gitleaks:allow (made up for the test)

// scanFor reports where any secret appears: every document of every
// collection in the database, every file under the given folders (gzip
// bodies are read uncompressed).
func scanFor(t *testing.T, db *store.DB, dirs []string, secrets []string) []string {
	t.Helper()
	ctx := context.Background()
	var hits []string
	check := func(where string, text []byte) {
		for _, s := range secrets {
			if bytes.Contains(text, []byte(s)) {
				hits = append(hits, where+" contains "+s[:6]+"…")
			}
		}
	}
	if db != nil {
		names, err := db.DB.ListCollectionNames(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range names {
			cur, err := db.C(name).Find(ctx, bson.M{})
			if err != nil {
				t.Fatal(err)
			}
			for cur.Next(ctx) {
				check("db "+name, []byte(cur.Current.String()))
			}
			cur.Close(ctx)
		}
	}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			b, err := os.ReadFile(p)
			if err != nil {
				return nil
			}
			if zr, err := gzip.NewReader(bytes.NewReader(b)); err == nil {
				if u, err := io.ReadAll(zr); err == nil {
					b = append(b, u...)
				}
			}
			check("file "+p, b)
			return nil
		})
	}
	return hits
}

// The redaction scenario: configured secrets, environment secrets, runtime
// cookies and well-known credential shapes go through every kind of entry —
// operation input, program arguments and error output, job errors, a Claude
// turn (a long reply stored in the media store), an audit event — and the spool
// (written while the database is down). Afterwards neither the database, nor
// the media store, nor the spool holds any of them.
func TestRedactionLeavesNoSecretAnywhere(t *testing.T) {
	e := newEnv(t, true)
	e.cfg.Database.URI = "mongodb+srv://editor:" + secretPassword + "@cluster0.example.net/?retryWrites=true"
	e.cfg.Media.S3.SecretAccessKey = secretS3Key
	t.Setenv("METARUSH_ELEVENLABS_API_KEY", secretEleven)
	t.Setenv("SOME_SERVICE_TOKEN", secretEnvToken)
	if err := os.MkdirAll(e.cfg.StateDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	hmacKey := []byte("0123456789abcdef0123456789abcdef")
	if err := os.WriteFile(filepath.Join(e.cfg.StateDir(), "confirm.key"), hmacKey, 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	l := e.open(t, e.ms)
	l.Secret(secretCookie)
	text := "connect with mongodb+srv://editor:" + secretPassword + "@cluster0.example.net and key " + secretAWSID + " " + secretTigrisID + " " +
		secretTigris + "; ElevenLabs " + secretEleven + "; Authorization: Bearer " + secretBearer + "; cookie mr_session=" + secretCookie + "; " +
		secretPEM + "; env " + secretEnvToken + "; s3 " + secretS3Key + "; hmac " + string(hmacKey)
	// An operation whose input holds secrets, in free text and in secret-named fields.
	raw, _ := json.Marshal(map[string]any{"query": text, "apiKey": secretEleven, "storageKey": "ws/x/objects/ab/abc", "nested": map[string]any{"password": secretPassword}})
	rec := l.StartOp(ctx, "search", raw, models.Actor{UserID: "usr_1", Via: "cli"}, false, false)
	rec.End(false, map[string]any{"note": text}, errors.New("failed: "+text))
	// A program run with secrets in its arguments and error output.
	l.ToolRun(ctx, tools.RunInfo{Tool: "ffmpeg", Args: []string{"-i", "in.mov", "--token", secretEnvToken, "--password=" + secretPassword, "AWS_SECRET_ACCESS_KEY=" + secretS3Key, text},
		Duration: time.Second, ExitCode: 1, Err: &tools.ErrFailed{Tool: "ffmpeg", Err: errors.New("exit status 1"), Stderr: text}, Stderr: []byte(strings.Repeat(text+"\n", 30))})
	// A job that failed with secrets in its error.
	l.JobEvent(ctx, jobs.Event{Kind: jobs.EventFailed, Job: models.Job{ID: ids.New(ids.Job), Type: "proxy", State: models.JobFailed, Error: text}, Error: text})
	// A Claude turn: short prompt, long reply (media store), tools and cards.
	l.Claude(ctx, Turn{Source: SourceAssistant, ConversationID: "asc_01h00000000000000000000000", Turn: 1, Prompt: "please use " + text,
		Response: strings.Repeat("Here is what I found: "+text+"\n", 40), Tools: []ClaudeTool{{Name: "Bash", Summary: "metarush search " + text, Result: text}},
		Actions: []ActionInfo{{ID: "act_1", Title: "Render with " + secretEleven, Decision: DecisionProposed}}, StartedAt: time.Now()})
	// An audited change outside any operation.
	l.Audit(ctx, models.AuditEvent{Action: "asset.ingest", Actor: models.Actor{Via: "watch"}, Details: map[string]any{"name": text, "token": secretBearer}})
	l.Flush(ctx)
	// The spool: a second logger whose database is down writes there.
	down := Open(Options{Cfg: e.cfg, Media: e.ms, Actor: models.Actor{Via: "cli"}})
	down.Secret(secretCookie)
	down.Claude(ctx, Turn{Source: SourceSkillEval, Prompt: text, Response: strings.Repeat(text, 20), StartedAt: time.Now()})
	down.ToolRun(ctx, tools.RunInfo{Tool: "node", Args: []string{"--secret", secretS3Key}, Err: errors.New(text), ExitCode: 1, Stderr: []byte(text)})
	down.Close()
	if st := (&Logger{cfg: e.cfg}).Spooled(); st.Entries < 2 {
		t.Fatalf("expected spooled entries, got %+v", st)
	}
	if n := len(e.entries(t, nil)); n < 5 {
		t.Fatalf("expected at least 5 entries in the database, got %d", n)
	}
	dirs := []string{filepath.Join(e.home, "media"), e.cfg.LogSpoolDir(), e.cfg.LogBodiesDir(), e.cfg.ClaudeHooksDir()}
	secrets := append(append([]string{}, allSecrets...), string(hmacKey), hex.EncodeToString(hmacKey))
	// The scanner itself finds what it looks for (a control outside the scanned places).
	control := t.TempDir()
	_ = os.WriteFile(filepath.Join(control, "x.gz"), gzipped("prefix "+secretEleven), 0o600)
	if hits := scanFor(t, nil, []string{control}, secrets); len(hits) != 1 {
		t.Fatalf("scanner control: %v", hits)
	}
	if hits := scanFor(t, e.db, dirs, secrets); len(hits) > 0 {
		t.Fatalf("secrets left behind:\n%s", strings.Join(hits, "\n"))
	}
	// Flushing the spool into the database keeps it clean too.
	l2 := e.open(t, e.ms)
	if n, err := l2.FlushSpool(ctx, 0); err != nil || n < 2 {
		t.Fatalf("flush spool: %d %v", n, err)
	}
	if hits := scanFor(t, e.db, dirs, secrets); len(hits) > 0 {
		t.Fatalf("secrets left behind after the spool flush:\n%s", strings.Join(hits, "\n"))
	}
	// Useful information survived: identifiers and plain words.
	es := e.entries(t, bson.M{"kind": KindOp})
	if len(es) != 1 || es[0].Op.Input["storageKey"] != "ws/x/objects/ab/abc" || !strings.Contains(es[0].Error.Message, "connect with") {
		t.Fatalf("redaction removed too much: %+v", es)
	}
}

func gzipped(s string) []byte {
	var b bytes.Buffer
	w := gzip.NewWriter(&b)
	_, _ = w.Write([]byte(s))
	_ = w.Close()
	return b.Bytes()
}

func TestSecretNames(t *testing.T) {
	for name, secret := range map[string]bool{"apiKey": true, "secretAccessKey": true, "key": true, "password": true, "Authorization": true,
		"X-Api-Key": true, "cookie": true, "elevenlabsKey": true, "token": true, "confirmToken": true, "storageKey": false, "groupKey": false,
		"idempotencyKey": false, "keyId": false, "contextTokens": false, "name": false, "keyScope": false, "monkey": false} {
		if SecretName(name) != secret {
			t.Errorf("SecretName(%q) = %v, want %v", name, !secret, secret)
		}
	}
	for name, secret := range map[string]bool{"ANTHROPIC_API_KEY": true, "GITHUB_TOKEN": true, "AWS_SECRET_ACCESS_KEY": true, "CLAUDE_CODE_SESSION_ID": false,
		"GIT_AUTHOR_NAME": false, "SSH_AUTH_SOCK": false, "PATH": false, "METARUSH_S3_ACCESS_KEY_ID": false} {
		if SecretEnvName(name) != secret {
			t.Errorf("SecretEnvName(%q) = %v, want %v", name, !secret, secret)
		}
	}
	r := NewRedactor()
	got := r.Args([]string{"--key-scope", "bucket", "--token", "abc12345", "--password=hunter22", "TOKEN=xyz12345", "plain"})
	if strings.Join(got, " ") != "--key-scope bucket --token [redacted] --password=[redacted] TOKEN=[redacted] plain" {
		t.Fatalf("args: %v", got)
	}
}

// Fingerprints let the hook hide configured secrets without knowing them.
func TestFingerprintsHideConfiguredSecrets(t *testing.T) {
	r := NewRedactor()
	r.addConfigured("s3cr3t-value-123", "abcd")
	r.Add("runtime-only-value") // not configured: the hook does not need it
	fp := r.Fingerprints([]byte("0123456789abcdef"))
	if len(fp.Items) < 2 {
		t.Fatalf("fingerprints: %+v", fp)
	}
	b, _ := json.Marshal(fp)
	if bytes.Contains(b, []byte("s3cr3t")) {
		t.Fatal("a fingerprint file contains the secret")
	}
	got := fp.Redact("x s3cr3t-value-123 y abcd z runtime-only-value")
	if strings.Contains(got, "s3cr3t-value-123") || strings.Contains(got, " abcd ") || !strings.Contains(got, "runtime-only-value") {
		t.Fatalf("redact: %q", got)
	}
}

type failingStore struct{ media.Store }

func (failingStore) Stat(context.Context, string) (media.ObjectInfo, error) {
	return media.ObjectInfo{}, errors.New("store unreachable")
}
func (failingStore) PutFile(context.Context, string, string, media.PutOptions, media.Resume, media.Progress) (media.ObjectInfo, error) {
	return media.ObjectInfo{}, errors.New("store unreachable")
}

// When the database or the media store is unreachable, entries wait in the
// spool and a later process writes them, once.
func TestSpoolAndFlush(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	// 1. No database: everything is spooled.
	db := e.db
	e.db = nil
	off := e.open(t, e.ms)
	for i := 0; i < 3; i++ {
		rec := off.StartOp(ctx, "project.create", json.RawMessage(`{"name":"P"}`), models.Actor{Via: "cli"}, false, false)
		rec.End(true, map[string]any{"project": map[string]any{"id": ids.New(ids.Project)}}, nil)
	}
	off.Close()
	// 2. A database but no media store: a long Claude reply waits with its entry.
	e.db = db
	nostore := e.open(t, failingStore{e.ms})
	long := strings.Repeat("a long answer ", 600)
	nostore.Claude(ctx, Turn{Source: SourceAssistant, Prompt: "hi", Response: long, StartedAt: time.Now()})
	nostore.Flush(ctx)
	st := nostore.Spooled()
	if st.Entries != 4 || st.Files != 2 {
		t.Fatalf("spool: %+v", st)
	}
	if n := len(e.entries(t, nil)); n != 0 {
		t.Fatalf("%d entries reached the database early", n)
	}
	// 3. A working process flushes the spool: four entries, the long reply in the store.
	l := e.open(t, e.ms)
	// Keep a copy of a spool file to replay it later.
	files, _ := filepath.Glob(filepath.Join(e.cfg.LogSpoolDir(), "*.jsonl"))
	replay, _ := os.ReadFile(files[0])
	n, err := l.FlushSpool(ctx, 0)
	if err != nil || n != 4 {
		t.Fatalf("flush: %d %v", n, err)
	}
	if st := l.Spooled(); st.Files != 0 {
		t.Fatalf("spool not empty: %+v", st)
	}
	turns := e.entries(t, bson.M{"kind": KindClaude})
	if len(turns) != 1 || turns[0].Claude.ResponseRef == nil {
		t.Fatalf("claude entry after flush: %+v", turns)
	}
	if body, err := l.ReadBody(ctx, turns[0].Claude.ResponseRef); err != nil || body != long {
		t.Fatalf("long reply: %v", err)
	}
	// 4. Replaying the same spool file writes nothing twice.
	if err := os.WriteFile(files[0], replay, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := l.FlushSpool(ctx, 0); err != nil {
		t.Fatalf("replay: %v", err)
	}
	if n := len(e.entries(t, nil)); n != 4 {
		t.Fatalf("after replay: %d entries", n)
	}
}

// Logging never blocks: a full queue drops entries (and says so) instead of waiting.
func TestQueueNeverBlocks(t *testing.T) {
	e := newEnv(t, false)
	l := &Logger{cfg: e.cfg, red: NewRedactor(), agg: map[aggKey]*CountInfo{}, jobPct: map[string]int{}, now: func() time.Time { return time.Now().UTC() },
		done: make(chan struct{})}
	start := time.Now()
	for i := 0; i < maxQueue+500; i++ {
		l.add(&pending{e: Entry{Kind: KindEvent, Summary: "x"}})
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("adding entries took too long")
	}
	q := l.take()
	if len(q) != maxQueue+1 || q[len(q)-1].e.Event.Action != "log.dropped" {
		t.Fatalf("queue: %d entries, last %+v", len(q), q[len(q)-1].e)
	}
}

// Successful dashboard reads are counted, not logged one by one; changes and
// errors from the dashboard are logged individually.
func TestDashboardReadsAreCounted(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	l := e.open(t, e.ms)
	dash := models.Actor{UserID: e.cfg.User.ID, Via: "dashboard"}
	for i := 0; i < 25; i++ {
		l.StartOp(ctx, "activity.summary", nil, dash, true, true).End(false, map[string]any{}, nil)
	}
	for i := 0; i < 5; i++ {
		l.StartOp(ctx, "notification.list", nil, dash, true, true).End(false, map[string]any{}, nil)
	}
	l.StartOp(ctx, "project.create", json.RawMessage(`{"name":"X"}`), dash, true, true).End(true, map[string]any{}, nil)
	l.StartOp(ctx, "project.show", json.RawMessage(`{"id":"prj_x"}`), dash, true, true).End(false, nil, errors.New("not found"))
	l.Flush(ctx)
	if es := e.entries(t, bson.M{"kind": KindOp}); len(es) != 2 {
		t.Fatalf("individual dashboard entries: %d", len(es))
	}
	sums := e.entries(t, bson.M{"kind": KindOpSummary})
	got := map[string]int{}
	for _, s := range sums {
		got[s.Counts.Op] = s.Counts.Count
		if s.Channel != ChannelDashboard {
			t.Fatalf("summary channel %q", s.Channel)
		}
	}
	if got["activity.summary"] != 25 || got["notification.list"] != 5 || len(got) != 2 {
		t.Fatalf("counts: %v", got)
	}
	// CLI reads are always logged one by one.
	for i := 0; i < 3; i++ {
		l.StartOp(ctx, "project.list", nil, models.Actor{Via: "cli"}, false, false).End(false, map[string]any{}, nil)
	}
	l.Flush(ctx)
	if es := e.entries(t, bson.M{"op.name": "project.list", "kind": KindOp}); len(es) != 3 {
		t.Fatalf("cli reads: %d", len(es))
	}
}

// Retention is off by default: nothing is ever removed automatically.
func TestRetentionOffByDefault(t *testing.T) {
	e := newEnv(t, true)
	ctx := context.Background()
	if e.cfg.Log.RetentionDays != 0 {
		t.Fatalf("default retention %d", e.cfg.Log.RetentionDays)
	}
	l := e.open(t, e.ms)
	old := time.Now().UTC().AddDate(-2, 0, 0)
	l.add(&pending{e: Entry{At: old, Kind: KindEvent, Summary: "two years ago"}})
	other := Entry{ID: ids.New(EntryPrefix), WorkspaceID: e.cfg.Workspace.ID, InstallationID: ids.New(ids.Installation), At: old, Kind: KindEvent, Summary: "another machine"}
	if _, err := e.db.C(Collection).InsertOne(ctx, other); err != nil {
		t.Fatal(err)
	}
	l.Flush(ctx)
	l.retention(ctx, nil)
	if n := len(e.entries(t, nil)); n != 2 {
		t.Fatalf("default retention removed entries: %d left", n)
	}
	// With log.retentionDays set, this machine's old entries go; others' stay.
	l.SetSettings(config.Log{RetentionDays: 30})
	l.retention(ctx, nil)
	left := e.entries(t, nil)
	if len(left) != 1 || left[0].Summary != "another machine" {
		t.Fatalf("after retention: %+v", left)
	}
}

func TestParseWhen(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{"2h": now.Add(-2 * time.Hour), "30d": now.AddDate(0, 0, -30), "2w": now.AddDate(0, 0, -14), "90m": now.Add(-90 * time.Minute)} {
		got, err := ParseWhen(in, now)
		if err != nil || !got.Equal(want) {
			t.Errorf("ParseWhen(%q) = %v, %v", in, got, err)
		}
	}
	if _, err := ParseWhen("yesterday-ish", now); err == nil {
		t.Error("nonsense parsed")
	}
	if got, err := ParseWhen("2026-10-01", now); err != nil || got.IsZero() {
		t.Errorf("date: %v %v", got, err)
	}
}

func TestCheckOutputRefusesRepositories(t *testing.T) {
	d := t.TempDir()
	repo := filepath.Join(d, "repo", "sub")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(d, "repo", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if CheckOutput(filepath.Join(repo, "log.jsonl")) == nil {
		t.Fatal("wrote into a git repository")
	}
	proj := filepath.Join(d, "film")
	if err := os.MkdirAll(filepath.Join(proj, ".metarush"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(proj, ".metarush", "lock.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if CheckOutput(filepath.Join(proj, "bundle.zip")) == nil {
		t.Fatal("wrote into a project folder")
	}
	if err := CheckOutput(filepath.Join(d, "elsewhere", "bundle.zip")); err != nil {
		t.Fatalf("refused an ordinary folder: %v", err)
	}
}
