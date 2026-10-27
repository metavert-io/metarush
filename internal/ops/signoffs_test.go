package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Sign-offs (D166): a checklist on the cut, a reminder when a version is
// approved (never a lock), ticked for the latest version, undone, removed.
func TestSignoffsRemind(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Awareness"})
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	rev := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":1,"out":9}]}}`,
		p.Project.ID, as.ID))

	so := call[SignoffsOut](t, a, "cut.signoffs.add", SignoffAddIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Text: "Lena OKs the final version"})
	call[SignoffsOut](t, a, "cut.signoffs.add", SignoffAddIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Text: "Maya OKs the final version"})
	again := call[SignoffsOut](t, a, "cut.signoffs.add", SignoffAddIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Text: "lena OKs the final version."})
	if len(so.Signoffs) != 1 || again.Open != 2 || len(again.Signoffs) != 2 {
		t.Fatalf("sign-offs: %+v / %+v", so, again)
	}

	ok, err := CallTyped[RevisionSummary](context.Background(), a, "revision.approve", RevisionApproveIn{ID: rev.Revision.ID}, Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(ok.Waiting) != 2 || !strings.HasPrefix(ok.Reminder, "Still waiting for 2 sign-offs: “Lena OKs the final version”") {
		t.Fatalf("the approval should remind: %+v", ok)
	}

	done := call[SignoffsOut](t, a, "cut.signoffs.done", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Signoff: "1", Note: "by email"})
	s := done.Signoffs[0]
	if done.Open != 1 || !s.Done || s.DoneBy == nil || s.Version != rev.Revision.Number || s.Note != "by email" {
		t.Fatalf("ticked: %+v", done)
	}
	if r := SignoffReminder(OpenSignoffs(context.Background(), a, mustProject(t, a, p.Project.ID), "")); r != "Still waiting for a sign-off: “Maya OKs the final version”." {
		t.Fatalf("reminder: %q", r)
	}
	undo := call[SignoffsOut](t, a, "cut.signoffs.undo", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Signoff: "Lena OKs the final version"})
	if undo.Open != 2 || undo.Signoffs[0].DoneBy != nil {
		t.Fatalf("unticked: %+v", undo)
	}
	gone := call[SignoffsOut](t, a, "cut.signoffs.remove", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Signoff: "2"})
	if len(gone.Signoffs) != 1 || gone.Signoffs[0].Text != "Lena OKs the final version" {
		t.Fatalf("removed: %+v", gone)
	}
	if _, err := CallTyped[SignoffsOut](context.Background(), a, "cut.signoffs.done", SignoffDecideIn{SignoffsIn: SignoffsIn{ProjectID: p.Project.ID}, Signoff: "7"}, Caller{}); err == nil {
		t.Fatal("a sign-off that isn't there")
	}
}

func mustProject(t *testing.T, a *app.App, id string) *models.Project {
	t.Helper()
	var p models.Project
	if err := a.DB.GetByID(context.Background(), store.CProjects, a.WorkspaceID(), id, &p); err != nil {
		t.Fatal(err)
	}
	return &p
}

// Where the video goes carries its shape (project update), and rules come
// and go by their number or words.
func TestDeliverablesAndRules(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Awareness"})
	b := models.Brief{Goal: "Awareness", Deliverables: []models.Deliverable{{Name: "On the website"}, {Name: "Instagram reels"}, {Name: "A square post"}}}
	up := call[ProjectOut](t, a, "project.update", ProjectUpdateIn{ID: p.Project.ID, Brief: &b})
	ds := up.Project.Brief.Deliverables
	if len(ds) != 3 || ds[0].Preset != "landscape-1080p" || ds[1].Preset != "vertical-1080p" || ds[1].Platform != "reels" || ds[2].Preset != "square-1080" {
		t.Fatalf("deliverables: %+v", ds)
	}
	for _, x := range []struct{ name, preset, platform string }{
		{"by email", "landscape-1080p", "generic"}, {"social shorts", "vertical-1080p", "shorts"}, {"TikTok", "vertical-1080p", "tiktok"},
		{"YouTube", "landscape-1080p", "youtube"}, {"for phones", "vertical-1080p", "shorts"},
	} {
		if pr, pl := DeliverableShape(x.name); pr != x.preset || pl != x.platform {
			t.Errorf("DeliverableShape(%q) = %s %s", x.name, pr, pl)
		}
	}
	call[RuleAddOut](t, a, "project.rules.add", RuleAddIn{ProjectID: p.Project.ID, Text: "First names only on screen"})
	r := call[RuleAddOut](t, a, "project.rules.add", RuleAddIn{ProjectID: p.Project.ID, Text: "Under 4 minutes"})
	if r.Rule.Measure != "max-length:240" || len(r.Rules) != 2 {
		t.Fatalf("measured rule: %+v", r)
	}
	out := call[RulesOut](t, a, "project.rules.remove", RuleRemoveIn{ProjectID: p.Project.ID, Rule: "1"})
	if len(out.Rules) != 1 || out.Rules[0].Text != "Under 4 minutes" {
		t.Fatalf("removed: %+v", out)
	}
}
