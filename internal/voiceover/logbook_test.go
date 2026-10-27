package voiceover

import (
	"bytes"
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// The activity log through real narration work against the fake ElevenLabs:
// the key — refused once, saved, then used for speech — never reaches the
// database, the media store, the spool or the hook's folder, even when a person
// pastes it into a Claude prompt; the requests are recorded with what they did
// and what they cost (never the text sent); the narration job says what it made.
func TestNarrationLogKeepsTheKeyOut(t *testing.T) {
	f := setup(t)
	// Keys built at run time that match no credential pattern: only the
	// exact values the providers registry reports can hide them.
	key := "elv-" + strings.Repeat("Zq4", 9) + "-k3y"
	wrong := "elv-" + strings.Repeat("Wr0", 9) + "-bad"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	lines := []string{"Out here, the light arrives slowly.", "And then it is everywhere."}
	rev := f.cut(lines...)
	ctx := context.Background()

	if res, err := call[providers.TestOut](t, f.a, "provider.test", providers.TestIn{Name: providers.ElevenLabsName, Key: wrong}); err != nil || res.Result.OK {
		t.Fatalf("a wrong key is refused: %+v %v", res, err)
	}
	if _, err := call[providers.Status](t, f.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: key}); err != nil {
		t.Fatal(err)
	}
	if _, err := call[providers.SettingsOut](t, f.a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Voice: "Ada"}); err != nil {
		t.Fatal(err)
	}
	out, err := call[GenerateOut](t, f.a, "voiceover.tts", TTSIn{RevisionID: rev})
	if err != nil || out.Generated != 2 || out.JobID == "" {
		t.Fatalf("generate: %+v %v", out, err)
	}
	f.a.Log.Claude(ctx, logbook.Turn{Source: logbook.SourceAssistant, ConversationID: "asc_01h00000000000000000000000", Turn: 1,
		Prompt: "my ElevenLabs key is " + key + " — set it up", Response: "Keys are added in Settings → Integrations; I cannot use them.", StartedAt: time.Now()})
	f.a.Log.Flush(ctx)

	// The scanner finds the key where it belongs (this env-mode home's .env)…
	if hits := scan(t, f, []string{f.a.Cfg.Home()}, key); len(hits) == 0 {
		t.Fatal("scanner control: the saved key should be in the installation's .env")
	}
	// …and nowhere the log writes.
	dirs := []string{f.a.Cfg.Media.Filesystem.Root, f.a.Cfg.LogSpoolDir(), f.a.Cfg.LogBodiesDir(), f.a.Cfg.ClaudeHooksDir()}
	if hits := scan(t, f, dirs, key, wrong); len(hits) > 0 {
		t.Fatalf("an ElevenLabs key was left behind:\n%s", strings.Join(hits, "\n"))
	}

	var es []logbook.Entry
	if err := f.a.DB.Find(ctx, logbook.Collection, f.a.WorkspaceID(), bson.M{}, &es); err != nil {
		t.Fatal(err)
	}
	var speech, refused, ok []logbook.Entry
	var jobEvents []string
	for _, e := range es {
		switch {
		case e.Kind == logbook.KindProvider && e.Provider.What == "make speech":
			speech = append(speech, e)
		case e.Kind == logbook.KindProvider && !e.Provider.OK:
			refused = append(refused, e)
		case e.Kind == logbook.KindProvider:
			ok = append(ok, e)
		case e.Kind == logbook.KindJob && e.Refs.JobID == out.JobID:
			jobEvents = append(jobEvents, e.Job.Event)
			if e.Job.Event == logbook.JobSucceeded && e.Job.Detail["credits"] == nil {
				t.Fatalf("the narration job keeps what it cost: %+v", e.Job.Detail)
			}
		case e.Kind == logbook.KindOp && e.Op.Name == "provider.set":
			if e.Op.Input["key"] != logbook.Redacted {
				t.Fatalf("the saved key shows in the operation input: %+v", e.Op.Input)
			}
		}
	}
	if len(speech) != 2 {
		t.Fatalf("want one speech request per line, got %d", len(speech))
	}
	for i, e := range speech {
		if e.Refs.JobID != out.JobID || e.Provider.Characters == 0 || e.Provider.Status != 200 || e.ParentID == "" {
			t.Fatalf("speech request %d: %+v %+v", i, e.Refs, e.Provider)
		}
		raw, _ := bson.Marshal(e)
		for _, l := range lines {
			if bytes.Contains(raw, []byte(l)) {
				t.Fatalf("the narration text is not part of a request entry: %s", l)
			}
		}
	}
	if len(refused) != 1 || refused[0].Provider.Status != 401 || refused[0].Error == nil {
		t.Fatalf("the refused key check: %+v", refused)
	}
	if len(ok) == 0 {
		t.Fatal("the key check when saving it is recorded")
	}
	if got := strings.Join(jobEvents, ","); !strings.Contains(got, logbook.JobLeased) || !strings.Contains(got, logbook.JobSucceeded) {
		t.Fatalf("narration job events: %s", got)
	}
}

// scan reports where a secret appears: every document of every collection,
// every file under dirs.
func scan(t *testing.T, f *fx, dirs []string, secrets ...string) []string {
	t.Helper()
	ctx := context.Background()
	var hits []string
	check := func(where string, b []byte) {
		for _, s := range secrets {
			if bytes.Contains(b, []byte(s)) {
				hits = append(hits, where)
			}
		}
	}
	names, err := f.a.DB.DB.ListCollectionNames(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range names {
		cur, err := f.a.DB.C(n).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		for cur.Next(ctx) {
			check("db "+n, cur.Current)
		}
		_ = cur.Close(ctx)
	}
	for _, d := range dirs {
		_ = filepath.WalkDir(d, func(p string, de fs.DirEntry, err error) error {
			if err != nil || de.IsDir() {
				return nil
			}
			if b, err := os.ReadFile(p); err == nil {
				check("file "+p, b)
			}
			return nil
		})
	}
	return hits
}
