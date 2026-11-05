package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A clip's cut-out put back for another computer (no room for it here, D505)
// reads the same in the dashboard's row — its stage, its wait line, the
// waiting group, all from the summary — and in the CLI's activity lines: its
// own reason and when it looks again, never "it ran into a problem".
func TestACutOutPutBackReadsTheSameInTheDashboardAndTheCLI(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Garden film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "kitchen.mov", Title: "Kitchen chat", State: models.AssetAvailable,
		ProjectIDs: []string{p.ID}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeMatte, Payload: map[string]any{"matteId": ids.New(ids.Matte), "assetId": clip.ID},
		Heavy: true, MaxAttempts: 2, ProjectID: p.ID, AssetID: clip.ID})
	if err != nil {
		t.Fatal(err)
	}
	w := jobs.Worker{ID: a.Cfg.Installation.ID, Name: "studio-mac", JobTypes: []string{jobs.TypeMatte}, AcceptsHeavy: true}
	leased, err := a.Queue.LeaseByID(ctx, w, j.ID)
	if err != nil || leased == nil {
		t.Fatalf("lease %v %v", leased, err)
	}
	reason := "Waiting for a computer of this workspace with room to make the cut-out; this one hasn't."
	if err := a.Queue.Postpone(ctx, leased, w.ID, 4*time.Minute, reason); err != nil { // as the worker puts it back (mattes.Wait)
		t.Fatal(err)
	}

	s, err := activity.Build(ctx, a, &activity.SummaryIn{})
	if err != nil {
		t.Fatal(err)
	}
	var it *activity.Item
	for i := range s.Items {
		if s.Items[i].Key == "job:"+j.ID {
			it = &s.Items[i]
		}
	}
	// The dashboard's row: its title, stage (with where) and wait line; the waiting group.
	if it == nil || it.Title != "Cutting the people out of “Kitchen chat”" || it.Subtitle != "Garden film" || it.StageLabel != "Waiting for another computer" ||
		it.Waiting == nil || !strings.HasPrefix(it.Waiting.Text, strings.TrimSuffix(reason, ".")+". It looks again in about ") {
		t.Fatalf("the row: %+v", it)
	}
	if len(s.Waiting) != 1 || s.Waiting[0].Text != "1 cut-out waiting for another computer" {
		t.Fatalf("the waiting group: %+v", s.Waiting)
	}
	// The CLI's lines say the same words.
	out := stdout(t, func() { printActivity(s, false) })
	for _, want := range []string{"Cutting the people out of", "Waiting for another computer", "      " + it.Waiting.Text, "  1 cut-out waiting for another computer"} {
		if !strings.Contains(out, want) {
			t.Errorf("the CLI's activity lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "problem") || strings.Contains(s.Headline, "problem") {
		t.Errorf("a cut-out put back isn't a problem:\n%s", out)
	}
}
