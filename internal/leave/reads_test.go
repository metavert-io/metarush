package leave_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	_ "github.com/metavert-io/metarush/internal/cli" // every operation
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Every operation an AI channel can call (the panel's commands, Claude Code
// in a terminal, MCP's read tools: anything that neither changes things nor
// is people-only) says what it shows of a client's things (D290). A new read
// adds a line to leave.readsShow: "-" when it shows none of a client's
// material, else the kinds.
func TestEveryReadSaysWhatItShows(t *testing.T) {
	for _, op := range ops.All() {
		if op.Mutating || op.HumanOnly {
			continue
		}
		if !leave.Declared(op.Name) {
			t.Errorf("%s doesn't say what it shows an AI (add it to leave.readsShow)", op.Name)
		}
	}
}

// quietSkips are "-" reads this probe doesn't run: they act on this computer
// (a folder picker, Finder, programs, the keychain, downloads) rather than
// read records.
var quietSkips = map[string]bool{"watch.choose": true, "watch.reveal": true, "assistant.status": true, "assistant.models": true, "setup.status": true, "components.list": true,
	"ingest.url.tools": true, "remote.status": true, "cache.prune": true, "cache.repair": true, "cache.prefetch": true, "preview.prepare": true,
	"machine.rekeyPlan": true, "motion.new": true, "provider.test": true, "worker.settings": true, "voiceover.models": true, "voiceover.voices": true,
	"voiceover.preview": true, "storage.plan": true}

// A read that says it shows nothing of a client's ("-") quotes nothing of a
// client that keeps its words: run on Claude's channel against a client
// whose transcript, to-do question and notification carry a word found
// nowhere else, none of them may answer with it.
func TestQuietReadsQuoteNothing(t *testing.T) {
	const word = "zorblaxian"
	a, acme, _, clip := strict(t)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: a.WorkspaceID(), AssetID: clip.ID, From: 5, To: 6,
		Text: "the " + word + " plan", Words: []models.Word{{Text: "the", From: 5, To: 5.2}, {Text: word, From: 5.2, To: 5.8}, {Text: "plan", From: 5.8, To: 6}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := todos.Upsert(ctx, a, models.Todo{Kind: models.TodoSpelling, Scope: "project", ProjectID: acme.ID, ProjectIDs: []string{acme.ID}, AssetID: clip.ID,
		GroupKey: "spelling:" + word, Question: "You fixed “" + word + "” 3 times. Add it to the spelling list?", Priority: 3}); err != nil {
		t.Fatal(err)
	}
	if _, err := notify.Upsert(ctx, a, models.Notification{UserID: a.Actor.UserID, Kind: models.NotifyFixesMissed, GroupKey: "w:" + word, ProjectID: acme.ID,
		Title: "A fix of “" + word + "” was missed", Body: "“" + word + "” came back.", CreatedAt: time.Now().UTC()}, notify.Refresh); err != nil {
		t.Fatal(err)
	}
	claude := asVia(a, models.ViaAssistant, "")
	inputs := []map[string]any{{}, {"projectId": acme.ID}, {"assetId": clip.ID}, {"id": clip.ID}, {"projectId": acme.ID, "assetId": clip.ID}}
	ran := 0
	for _, op := range ops.All() {
		if op.Mutating || op.HumanOnly || quietSkips[op.Name] {
			continue
		}
		if kinds, ok := leave.ReadKinds(op.Name); !ok || len(kinds) > 0 {
			continue
		}
		for _, in := range inputs {
			raw, _ := json.Marshal(in)
			out, err := ops.Call(ctx, claude, op.Name, raw, ops.Caller{})
			if err != nil {
				continue
			}
			ran++
			b, _ := json.Marshal(out)
			if strings.Contains(strings.ToLower(string(b)), word) {
				t.Errorf("%s %s says it shows nothing of a client's, but quotes their words: %.300s", op.Name, raw, b)
				break
			}
		}
	}
	if ran < 50 {
		t.Fatalf("the probe ran only %d reads", ran)
	}
}
