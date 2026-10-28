package handover_test

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/handover"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"

	_ "github.com/metavert-io/metarush/internal/abilities" // ability.run
	_ "github.com/metavert-io/metarush/internal/remotion"  // remotion.import, refresh, push
)

// Work asked for on a computer that handed its role over (D349): what needs
// nothing of its own goes to the computer that has its role now (that
// computer's worker runs it; this one runs none), and what needs its own
// keys, sign-ins, set-up or files is refused in plain words naming where to
// start it. Followed through a chain of handovers.
func TestWorkAskedForOnAComputerThatHandedItsRoleOver(t *testing.T) {
	w := newWorkspace(t)
	old, nu := w.computer(t, "laptop"), w.computer(t, "desk")
	ctx := context.Background()
	pctx := handover.WithPassphrase(ctx, pass)
	ex := call[handover.HandoverExportOut](t, pctx, old.a, "machine.handover.export", handover.HandoverExportIn{To: "desk", Out: filepath.Join(t.TempDir(), "h.age")})
	call[handover.HandoverReceiveOut](t, pctx, nu.a, "machine.handover.receive", handover.HandoverReceiveIn{File: ex.File})
	if role, _, err := handover.Look(ctx, old.a); err != nil || role.Role != handover.RoleClient {
		t.Fatalf("stepped down: %+v %v", role, err)
	}
	desk := nu.a.Cfg.Installation.ID
	if h, err := old.a.WorkHost(ctx); err != nil || h.ID != desk || h.Name != "desk" || h.Here {
		t.Fatalf("the laptop's work runs on the desk: %+v %v", h, err)
	}
	if h, _ := nu.a.WorkHost(ctx); !h.Here {
		t.Fatalf("the desk does its own: %+v", h)
	}
	pinned := func(id string) string {
		t.Helper()
		var j models.Job
		if err := w.db.GetByID(ctx, store.CJobs, w.id, id, &j); err != nil {
			t.Fatal(err)
		}
		return j.Installation
	}

	// Goes to the desk: a link import, a web page, a scene description.
	imp := call[linkimport.ImportOut](t, ctx, old.a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}})
	if len(imp.JobIDs) != 1 || pinned(imp.JobIDs[0]) != desk || imp.Machine != "desk" {
		t.Fatalf("link import %+v", imp)
	}
	page := call[ops.ResourceFetchOut](t, ctx, old.a, "resources.fetch", ops.ResourceFetchIn{URL: "https://example.com/about", Workspace: true, As: "page"})
	if page.JobID == "" || pinned(page.JobID) != desk {
		t.Fatalf("web page %+v", page)
	}
	if _, err := w.db.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": w.id}, bson.M{"$set": bson.M{"describeScenes": true}}); err != nil {
		t.Fatal(err)
	}
	clip := ids.New(ids.Asset)
	if err := w.db.Insert(ctx, store.CAssets, models.Asset{ID: clip, WorkspaceID: w.id, State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true}, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	desc := call[assistant.DescribeOut](t, ctx, old.a, "footage.describe", assistant.DescribeIn{AssetID: clip})
	if len(desc.Jobs) != 1 || pinned(desc.Jobs[0]) != desk {
		t.Fatalf("scene description %+v", desc)
	}
	// The desk's worker takes them; the laptop's never would.
	for _, id := range append([]string{imp.JobIDs[0], page.JobID}, desc.Jobs...) {
		if j, err := old.a.Queue.LeaseAmong(ctx, jobs.Worker{ID: old.a.Cfg.Installation.ID, Name: "laptop", JobTypes: []string{jobs.TypeImport, jobs.TypeFetch, jobs.TypeDescribe}}, []string{id}); err != nil || j != nil {
			t.Fatalf("the laptop took %s: %v", id, err)
		}
	}

	// Refused, naming the desk: what needs the laptop's own sign-ins, keys,
	// set-up or files.
	for name, in := range map[string]any{
		"ingest.url":             map[string]any{"urls": []string{"https://www.youtube.com/watch?v=9bZkp7q19f0"}, "browserSignIn": "chrome"},
		"ability.run":            map[string]any{"ref": "captions-helper", "plan": "0123456789abcdef0123456789abcdef"},
		"remotion.import":        map[string]any{"from": t.TempDir(), "projectId": "Teaser"},
		"remotion.refresh.start": map[string]any{"source": "intro", "plan": "0123456789abcdef0123456789abcdef"},
		"remotion.push.start":    map[string]any{"source": "intro", "plan": "0123456789abcdef0123456789abcdef"},
		"publish.send":           map[string]any{"projectId": "Teaser", "platform": "youtube"},
	} {
		raw, _ := json.Marshal(in)
		_, err := ops.Call(ctx, old.a, name, raw, person())
		refused(t, err, apperr.Conflict, "handed_over")
		if d, _ := apperr.From(err).Details.(map[string]any); d["host"] != "desk" {
			t.Errorf("%s doesn't say where: %v", name, err)
		}
	}
	// On the desk none of that is refused for where it is.
	raw, _ := json.Marshal(map[string]any{"ref": "captions-helper", "plan": "0123456789abcdef0123456789abcdef"})
	if _, err := ops.Call(ctx, nu.a, "ability.run", raw, person()); err != nil {
		if d, _ := apperr.From(err).Details.(map[string]any); d != nil && d["reason"] == "handed_over" {
			t.Fatalf("the desk refused it for where it is: %v", err)
		}
	}

	// A chain: the desk hands its role on to a third computer; the laptop's
	// work goes there.
	third := w.computer(t, "studio")
	ex2 := call[handover.HandoverExportOut](t, pctx, nu.a, "machine.handover.export", handover.HandoverExportIn{To: "studio", Out: filepath.Join(t.TempDir(), "h2.age")})
	call[handover.HandoverReceiveOut](t, pctx, third.a, "machine.handover.receive", handover.HandoverReceiveIn{File: ex2.File})
	if h, err := old.a.WorkHost(ctx); err != nil || h.ID != third.a.Cfg.Installation.ID || h.Name != "studio" {
		t.Fatalf("the laptop's work follows the role: %+v %v", h, err)
	}
}
