package keep_test

import (
	"context"
	"errors"
	"testing"

	"github.com/metavert-io/metarush/internal/keep"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestKeys(t *testing.T) {
	for _, c := range [][2]string{
		{"https://www.fernbrook.example/about/", "fernbrook.example/about"},
		{"http://FERNBROOK.example/about#team", "fernbrook.example/about"},
		{"fernbrook.example/about", "fernbrook.example/about"},
		{"https://fernbrook.example/?lang=fr", "fernbrook.example?lang=fr"},
	} {
		if got := keep.LinkKey(c[0]); got != c[1] {
			t.Errorf("LinkKey(%q) = %q, want %q", c[0], got, c[1])
		}
	}
	rule := keep.Key(models.KeepRule, "First names only on screen.", models.KeepDetail{})
	check := keep.Key(models.KeepCheck, "first names only, on screen", models.KeepDetail{})
	if rule != check {
		t.Fatalf("a rule and a check with the same words are one thing: %q %q", rule, check)
	}
	a := keep.Key(models.KeepSource, "", models.KeepDetail{URL: "https://www.fernbrook.example"})
	b := keep.Key(models.KeepSource, "fernbrook.example/", models.KeepDetail{})
	if a != b {
		t.Fatalf("the same address typed two ways is one source: %q %q", a, b)
	}
	b1 := keep.Key(models.KeepBrief, "", models.KeepDetail{Audience: "Members, neighbours", Deliverables: []models.Deliverable{{Name: "By email"}}})
	b2 := keep.Key(models.KeepBrief, "", models.KeepDetail{Audience: "members neighbours", Deliverables: []models.Deliverable{{Name: "by email"}}})
	if b1 != b2 {
		t.Fatalf("brief keys: %q %q", b1, b2)
	}
	p1 := keep.Key(models.KeepPerson, "Volunteers at FernBrook", models.KeepDetail{Person: "Lena"})
	p2 := keep.Key(models.KeepPerson, "Volunteers at FernBrook", models.KeepDetail{Person: "Maya"})
	if p1 == p2 {
		t.Fatal("the same fact about two people is two things")
	}
}

// Asked once per project and content; kept or declined stays decided.
func TestAskOnceAndDecide(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ws := a.WorkspaceID()
	it := models.KeepItem{ProjectID: "prj_1", Kind: models.KeepRule, Text: "Captions at the top", From: "claude"}
	first, err := keep.Ask(ctx, a.DB, ws, it)
	if err != nil || first.State != models.KeepAsked || first.ID == "" {
		t.Fatalf("first ask: %+v %v", first, err)
	}
	again, err := keep.Ask(ctx, a.DB, ws, models.KeepItem{ProjectID: "prj_1", Kind: models.KeepCheck, Text: "captions at the top!"})
	if !errors.Is(err, keep.ErrSeen) || again.ID != first.ID {
		t.Fatalf("asked twice: %+v %v", again, err)
	}
	// Another project is asked on its own.
	if _, err := keep.Ask(ctx, a.DB, ws, models.KeepItem{ProjectID: "prj_2", Kind: models.KeepRule, Text: "Captions at the top"}); err != nil {
		t.Fatalf("another project: %v", err)
	}
	d, err := keep.Decide(ctx, a.DB, ws, *first, models.KeepDeclined, a.Actor, "")
	if err != nil || d.State != models.KeepDeclined || d.ID != first.ID || d.DecidedAt == nil {
		t.Fatalf("declined: %+v %v", d, err)
	}
	if _, err := keep.Ask(ctx, a.DB, ws, it); !errors.Is(err, keep.ErrSeen) {
		t.Fatalf("a declined thing is never asked again: %v", err)
	}
	// Kept at a terminal without being asked: a record is made.
	k, err := keep.Decide(ctx, a.DB, ws, models.KeepItem{ProjectID: "prj_1", Kind: models.KeepSpelling, Text: "FernBrook"}, models.KeepKept, a.Actor, "On the list.")
	if err != nil || k.State != models.KeepKept || k.ID == "" || k.Outcome != "On the list." || k.From != "person" {
		t.Fatalf("kept: %+v %v", k, err)
	}
	list, err := keep.List(ctx, a.DB, ws, "prj_1", "")
	if err != nil || len(list) != 2 {
		t.Fatalf("list: %+v %v", list, err)
	}
	kept, _ := keep.List(ctx, a.DB, ws, "prj_1", models.KeepKept)
	if len(kept) != 1 || kept[0].Text != "FernBrook" {
		t.Fatalf("kept list: %+v", kept)
	}
}
