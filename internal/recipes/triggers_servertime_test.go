package recipes

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// Planning by itself goes by the database server's time (D360): the next
// weekly time is worked out from it wherever a person sets it, a computer
// whose clock is ten minutes fast doesn't plan before the time comes, one ten
// minutes slow doesn't plan late, new files settle by it, and two looks at
// the same time with different clocks plan once.
func TestTriggersGoByTheServersTime(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	me := f.a.Cfg.User.ID
	if err := f.a.DB.Insert(f.ctx, store.CUsers, models.User{ID: me, WorkspaceID: f.a.WorkspaceID(), Name: "Ada", Email: "ada@example.com", CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	server := func() time.Time {
		t.Helper()
		now, err := f.a.DB.ServerNow(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		return now
	}
	on := func(skew time.Duration, fn func()) {
		t.Helper()
		restore := testutil.SkewClock(t, skew)
		defer restore()
		fn()
	}
	look := func(skew time.Duration) (planned []string) {
		t.Helper()
		on(skew, func() {
			var err error
			if planned, err = triggerLookNow(f.ctx, f.a); err != nil {
				t.Fatal(err)
			}
		})
		return planned
	}
	setNext := func(at time.Time) {
		t.Helper()
		if _, err := f.a.DB.C(store.CRecipes).UpdateOne(f.ctx, bson.M{"name": "app-teaser"}, bson.M{"$set": bson.M{"triggers.next": at.Truncate(time.Millisecond)},
			"$unset": bson.M{"prepared": ""}}); err != nil {
			t.Fatal(err)
		}
	}

	// Set on a computer ten minutes slow for a weekly time five minutes ago
	// by the server's clock: next week's, not one that plans at once.
	ago := server().Add(-5 * time.Minute).In(time.Local)
	weekly := fmt.Sprintf("%s %02d:%02d", strings.ToLower(ago.Weekday().String()), ago.Hour(), ago.Minute())
	var set *RecipeTriggersOut
	on(-10*time.Minute, func() {
		set = must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", Weekly: &weekly}, true)
	})
	if n := set.Recipes[0].Next; n == nil || n.Sub(server()) < 6*24*time.Hour {
		t.Fatalf("set on a slow computer, the next weekly time is %v (the server's now %v)", n, server())
	}
	if planned := look(-10 * time.Minute); len(planned) != 0 {
		t.Fatalf("planned at once for a time that had passed when it was set: %v", planned)
	}

	// Due in five minutes by the server's time: a computer ten minutes fast
	// doesn't plan yet.
	setNext(server().Add(5 * time.Minute))
	if planned := look(10 * time.Minute); len(planned) != 0 {
		t.Fatalf("a computer ten minutes fast planned five minutes early: %v", planned)
	}
	// Due a minute ago: a computer ten minutes slow plans now.
	setNext(server().Add(-time.Minute))
	if planned := look(-10 * time.Minute); len(planned) != 1 {
		t.Fatalf("a computer ten minutes slow didn't plan a time that came a minute ago: %v", planned)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	if r.Prepared == nil || r.Triggers.Next == nil || r.Triggers.Next.Sub(server()) < 6*24*time.Hour {
		t.Fatalf("planned, next a week on: %+v next %v", r.Prepared, r.Triggers.Next)
	}

	// Two looks at once, on computers whose clocks differ by twenty minutes,
	// read the recipe while the weekly time was due: it is planned once.
	setNext(server().Add(-time.Minute))
	stale, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	stale2, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	var first, second bool
	on(-10*time.Minute, func() {
		var err error
		if first, err = lookAt(f.ctx, f.a, stale, f.a.DB.NowOrLocal(f.ctx)); err != nil {
			t.Fatal(err)
		}
	})
	on(10*time.Minute, func() {
		var err error
		if second, err = lookAt(f.ctx, f.a, stale2, f.a.DB.NowOrLocal(f.ctx)); err != nil {
			t.Fatal(err)
		}
	})
	if !first || second {
		t.Fatalf("two looks at the same weekly time: planned %v and %v, want once", first, second)
	}

	// New files settle by the server's time: arrived (by the server's clock)
	// thirty seconds short of settling, a computer ten minutes fast waits; a
	// second past it, a computer ten minutes slow plans.
	f.fromCollection()
	yes, off := true, "off"
	must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &yes, Weekly: &off}, true)
	must[ops.LibraryActionOut](t, f.a, "collection.add", ops.CollectionItemsIn{Collection: "Gala", AssetIDs: []string{f.interview2}}, true)
	if planned := look(0); len(planned) != 0 {
		t.Fatalf("new files settle first: %v", planned)
	}
	settle := func(since time.Time) {
		t.Helper()
		if _, err := f.a.DB.C(store.CRecipes).UpdateOne(f.ctx, bson.M{"name": "app-teaser"}, bson.M{"$set": bson.M{"triggers.pendingSince": since.Truncate(time.Millisecond)}}); err != nil {
			t.Fatal(err)
		}
	}
	settle(server().Add(-TriggerQuiet + 30*time.Second))
	if planned := look(10 * time.Minute); len(planned) != 0 {
		t.Fatalf("a computer ten minutes fast planned before new files settled: %v", planned)
	}
	settle(server().Add(-TriggerQuiet - time.Second))
	if planned := look(-10 * time.Minute); len(planned) != 1 {
		t.Fatalf("a computer ten minutes slow didn't plan for new files that settled: %v", planned)
	}
}

// A run's review copy waits qaGrace after the render finished for its
// quality check, by the database server's time (the render's completion is
// written in it on the computer that rendered it: D360).
func TestTheQualityCheckGraceGoesByTheServersTime(t *testing.T) {
	f := setup(t)
	e := &engine{a: f.a}
	checked := func(skew, ago time.Duration) renderCheck {
		t.Helper()
		now, err := f.a.DB.ServerNow(f.ctx)
		if err != nil {
			t.Fatal(err)
		}
		done := now.Add(-ago)
		r := models.Render{ID: "rnd_" + strings.ReplaceAll(ago.String(), ".", "_"), WorkspaceID: f.a.WorkspaceID(), State: models.RenderAvailable, CreatedAt: done, CompletedAt: &done}
		if err := f.a.DB.Insert(f.ctx, store.CRenders, r); err != nil {
			t.Fatal(err)
		}
		restore := testutil.SkewClock(t, skew)
		defer restore()
		rc, err := e.renderChecked(f.ctx, &models.RecipeStep{}, r.ID)
		if err != nil {
			t.Fatal(err)
		}
		return rc
	}
	if rc := checked(10*time.Minute, qaGrace-30*time.Second); rc.done {
		t.Fatalf("a computer ten minutes fast stopped waiting for the quality check early: %+v", rc)
	}
	if rc := checked(-10*time.Minute, qaGrace+30*time.Second); !rc.done || rc.qa != "didn't run" {
		t.Fatalf("a computer ten minutes slow waited on for a quality check that never came: %+v", rc)
	}
}
