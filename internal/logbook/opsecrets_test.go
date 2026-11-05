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
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/secretfields"
	"github.com/metavert-io/metarush/internal/tools"
)

// A value in a secret-named field of what a call is sent is hidden in
// everything that call logs — its own entry, the requests it makes, the
// programs it runs, the calls it makes — and nowhere else: the call didn't
// take it, so it is nobody's secret, and one made up to equal an ordinary word
// (here a person's email) must not blank that word out of later entries.
// Before, every such value was hidden in everything the process logged from
// then on.
func TestSecretInputsStayInTheirCall(t *testing.T) {
	e := newEnv(t, true)
	l := e.open(t, e.ms)
	ctx := context.Background()
	const word = "maya.lindqvist@example.com"
	raw, _ := json.Marshal(map[string]any{"name": "elevenlabs", "key": word})
	rec := l.StartOp(ctx, "provider.test", raw, models.Actor{Via: "cli"}, true, false)
	octx := rec.Context(ctx)
	ProviderDone(octx, ProviderCall{Provider: "elevenlabs", Title: "ElevenLabs", What: "check the plan", Method: "GET", Endpoint: "/v1/user/subscription", Status: 401},
		apperr.New(apperr.NotConfigured, "ElevenLabs did not accept the key %s", word))
	l.ToolRun(octx, tools.RunInfo{Tool: "ffmpeg", Args: []string{"-metadata", "comment=" + word}, Duration: time.Second})
	inner := l.StartOp(octx, "project.show", json.RawMessage(`{"id":"prj_01h00000000000000000000000"}`), models.Actor{Via: "cli"}, true, false)
	inner.End(false, nil, errors.New("no project for "+word))
	rec.End(false, nil, errors.New("rejected "+word))

	// Later calls say the same words: a project named after the person.
	later := l.StartOp(ctx, "project.create", json.RawMessage(`{"name":"Interview with `+word+`"}`), models.Actor{Via: "cli"}, true, false)
	later.End(true, map[string]any{}, errors.New("a project called "+word+" is there already"))
	l.Flush(ctx)

	theCall := map[string]bool{rec.id: true, inner.id: true}
	var own, after int
	for _, en := range e.entries(t, nil) {
		b, _ := bson.MarshalExtJSON(en, false, false)
		switch {
		case theCall[en.ID] || theCall[en.ParentID]:
			own++
			if strings.Contains(string(b), word) {
				t.Fatalf("the call's own entry shows what it was sent: %s", b)
			}
		case en.ID == later.id:
			after++
			if en.Op.Input["name"] != "Interview with "+word || !strings.Contains(en.Error.Message, word) {
				t.Fatalf("a later call lost the words: %s", b)
			}
		}
	}
	if own != 4 || after != 1 {
		t.Fatalf("want the call's four entries (its own, the request, the program, the call it made) and the later one, got %d and %d", own, after)
	}
	if got := l.Redactor().String(word); got != word {
		t.Fatalf("the process hides a value no call took: %q", got)
	}
	for _, v := range KnownSecrets(e.cfg) {
		if v == word {
			t.Fatal("a value no call took is among the secrets exports leave out")
		}
	}
}

// An operation's own secret fields (internal/secretfields: account.password's
// current and new password) are hidden like secret-named ones: by name in its
// input, and their values anywhere else in its call. Another operation's
// fields of the same names are not.
func TestOperationSecretFields(t *testing.T) {
	e := newEnv(t, true)
	l := e.open(t, e.ms)
	ctx := context.Background()
	const current, next = "tomato garden at dusk", "rosemary under the window"
	raw, _ := json.Marshal(map[string]any{"current": current, "new": next, "note": "keep"})
	if got := secretInputs(raw, secretfields.Of("account.password")); len(got) != 2 {
		t.Fatalf("an operation's own secret fields: %v", got)
	}
	if got := SecretInputs(raw); len(got) != 0 {
		t.Fatalf("other operations' fields with these names aren't secrets: %v", got)
	}
	rec := l.StartOp(ctx, "account.password", raw, models.Actor{Via: "cli"}, true, false)
	rec.End(true, nil, errors.New("couldn't take "+next))
	other := l.StartOp(ctx, "test.other", raw, models.Actor{Via: "cli"}, true, false)
	other.End(true, map[string]any{}, nil)
	l.Flush(ctx)
	for _, en := range e.entries(t, bson.M{"kind": KindOp}) {
		b, _ := bson.MarshalExtJSON(en, false, false)
		switch en.Op.Name {
		case "account.password":
			if en.Op.Input["current"] != Redacted || en.Op.Input["new"] != Redacted || en.Op.Input["note"] != "keep" ||
				strings.Contains(string(b), current) || strings.Contains(string(b), next) {
				t.Fatalf("the operation's secret fields show: %s", b)
			}
		case "test.other":
			if en.Op.Input["current"] != current {
				t.Fatalf("another operation's field of the same name is hidden: %s", b)
			}
		}
	}
}

// An entry an older version spooled with an operation's secret fields as
// typed is cleaned when a newer one writes it to the database.
func TestSpooledSecretFieldsAreCleaned(t *testing.T) {
	e := newEnv(t, true)
	l := e.open(t, e.ms)
	ctx := context.Background()
	const next = "rosemary under the window"
	old := &spoolRecord{Entry: Entry{ID: newID(), WorkspaceID: e.cfg.Workspace.ID, At: time.Now().UTC(), Kind: KindOp, Level: LevelError, Channel: ChannelDashboard,
		Summary: "account.password failed: " + next + " is too common", Error: &EntryError{Message: next + " is too common"},
		Op: &OpInfo{Name: "account.password", Mutating: true, Input: map[string]any{"current": "tomato garden at dusk", "new": next}}}}
	if err := os.MkdirAll(l.spoolDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeSpoolFile(filepath.Join(l.spoolDir(), "1-1-old.jsonl"), []*spoolRecord{old}); err != nil {
		t.Fatal(err)
	}
	if n, err := l.FlushSpool(ctx, 0); err != nil || n != 1 {
		t.Fatalf("flush: %d %v", n, err)
	}
	es := e.entries(t, bson.M{"_id": old.Entry.ID})
	if len(es) != 1 {
		t.Fatal("the spooled entry wasn't written")
	}
	b, _ := bson.MarshalExtJSON(es[0], false, false)
	if es[0].Op.Input["current"] != Redacted || es[0].Op.Input["new"] != Redacted || strings.Contains(string(b), next) || strings.Contains(string(b), "tomato garden") {
		t.Fatalf("a spooled password reached the database: %s", b)
	}
}
