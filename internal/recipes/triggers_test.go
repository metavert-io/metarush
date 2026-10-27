package recipes

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// fromCollection makes a version of the teaser whose footage comes from a
// collection holding the interview (a person makes it current).
func (f *fx) fromCollection() string {
	f.t.Helper()
	must[ops.LibraryActionOut](f.t, f.a, "collection.add", ops.CollectionItemsIn{Collection: "Gala", AssetIDs: []string{f.interview}, Create: true}, true)
	var c models.Collection
	if err := f.a.DB.C(store.CCollections).FindOne(f.ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "name": "Gala"}).Decode(&c); err != nil {
		f.t.Fatal(err)
	}
	r, _ := loadRecipe(f.ctx, f.a, "app-teaser")
	cur, _ := loadVersion(f.ctx, f.a, r, "")
	d := Clone(&cur.Doc)
	inputByName(d)["footage"].From = &models.RecipeSource{Collection: c.ID}
	v, _, err := saveVersion(f.ctx, f.a, r, d, cur.Hash, "footage from the Gala collection", checkDoc(f.ctx, f.a, d))
	if err != nil {
		f.t.Fatal(err)
	}
	if err := makeCurrent(f.ctx, f.a, r, v); err != nil {
		f.t.Fatal(err)
	}
	return c.ID
}

func (f *fx) recipe() *models.Recipe {
	f.t.Helper()
	r, err := loadRecipe(f.ctx, f.a, "app-teaser")
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fx) runCount() int64 {
	n, _ := f.a.DB.C(store.CRecipeRuns).CountDocuments(f.ctx, bson.M{"workspaceId": f.a.WorkspaceID()})
	return n
}

// New footage on a recipe's collection makes a plan once it settles and rings
// the bell of the person who turned it on — "the run is planned" with its
// estimate and a link that opens Run with… — and never starts a run, though
// that person has Auto on and estimates off. A weekly time does the same. Only
// people turn it on; someone else's is theirs (or an owner's) to change; the
// computer that turned it on looks; a run that starts uses the plan up.
func TestTriggersPlanButNeverStart(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	now := time.Now().UTC()
	me := f.a.Cfg.User.ID
	if err := f.a.DB.Insert(f.ctx, store.CUsers, models.User{ID: me, WorkspaceID: f.a.WorkspaceID(), Name: "Ada", Email: "ada@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// Auto on (D186), as the assistant keeps it: a trigger never asks it anything.
	if _, err := f.a.DB.C(store.CUsers).UpdateOne(f.ctx, bson.M{"_id": me}, bson.M{"$set": bson.M{"assistantMode": "auto"}}); err != nil {
		t.Fatal(err)
	}
	off := false
	must[prefs.PreferencesOut](t, f.a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, true)

	// Without an input that takes new files from anywhere, only a weekly time can plan.
	on := true
	if _, err := call[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &on}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("no input takes new files: %v", err)
	}
	f.fromCollection()
	if _, err := call[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &on}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("only people turn it on: %v", err)
	}
	set := must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &on}, true)
	row := set.Recipes[0]
	if !row.On || !row.NewFiles || row.Tells != "Ada" || row.Computer != "studio" || !row.Mine || !row.CanChange ||
		strings.Join(row.Sources, ",") != "the collection “Gala”" || !strings.Contains(set.Message, "When new footage or pictures arrive in the collection “Gala”") {
		t.Fatalf("turned on: %+v %q", row, set.Message)
	}
	if !strings.Contains(row.Words, "tells Ada") || !strings.Contains(row.Words, "never starts one") {
		t.Fatalf("its words: %q", row.Words)
	}
	if got := must[RecipeListOut](t, f.a, "recipe.list", RecipeListIn{}, false); !got.Recipes[0].PlansByItself || got.Recipes[0].Planned {
		t.Fatalf("the card says it plans by itself: %+v", got.Recipes[0])
	}

	// What is on the collection already isn't new.
	if planned, err := TriggerLook(f.ctx, f.a, now.Add(time.Minute)); err != nil || len(planned) != 0 {
		t.Fatalf("nothing new: %v %v", planned, err)
	}
	// New footage arrives: it settles first, then the run is planned.
	must[ops.LibraryActionOut](t, f.a, "collection.add", ops.CollectionItemsIn{Collection: "Gala", AssetIDs: []string{f.interview2}}, true)
	if planned, _ := TriggerLook(f.ctx, f.a, now.Add(2*time.Minute)); len(planned) != 0 {
		t.Fatalf("new files settle first: %v", planned)
	}
	if r := f.recipe(); len(r.Triggers.Pending) != 1 || r.Triggers.PendingSince == nil {
		t.Fatalf("pending: %+v", r.Triggers)
	}
	planned, err := TriggerLook(f.ctx, f.a, now.Add(2*time.Minute+TriggerQuiet))
	if err != nil || len(planned) != 1 || planned[0] != "App teaser" {
		t.Fatalf("planned: %v %v", planned, err)
	}
	r := f.recipe()
	p := r.Prepared
	if p == nil || p.Why != models.PlannedNewFiles || p.Words != "New footage on the collection “Gala” (1 clip)" || !p.Ready || p.For.UserID != me || p.Version != 2 || p.Fingerprint == "" {
		t.Fatalf("the plan: %+v", p)
	}
	took := ""
	for _, in := range p.Inputs {
		if in.Name == "footage" && len(in.Items) == 1 {
			took = in.Items[0].ID
		}
	}
	if took != f.interview2 {
		t.Fatalf("the plan takes the new footage: %+v", p.Inputs)
	}
	if n := f.runCount(); n != 0 {
		t.Fatalf("a trigger never starts a run (Auto on, estimates off): %d runs", n)
	}
	var bell models.Notification
	if err := f.a.DB.C(store.CNotifications).FindOne(f.ctx, bson.M{"groupKey": "recipe-planned:" + r.ID}).Decode(&bell); err != nil {
		t.Fatalf("the bell: %v", err)
	}
	if bell.Kind != models.NotifyRecipePlanned || bell.UserID != me || bell.Tone != "info" || bell.Link != "/library/recipes?planned=1&recipe=app-teaser" ||
		!strings.HasPrefix(bell.Title, "New footage on the collection “Gala” (1 clip) for “App teaser”: the run is planned, ") || bell.Body != "Nothing starts until you run it." {
		t.Fatalf("the bell: %+v", bell)
	}
	// Looking again: nothing new, no second plan.
	if planned, _ := TriggerLook(f.ctx, f.a, now.Add(10*time.Minute)); len(planned) != 0 {
		t.Fatalf("nothing new again: %v", planned)
	}
	list := must[RecipeTriggersOut](t, f.a, "recipe.triggers", RecipeTriggersIn{}, false)
	if len(list.Recipes) != 1 || list.Recipes[0].Prepared == nil || !strings.Contains(list.Summary, "none ever starts one") {
		t.Fatalf("every recipe that plans: %+v", list)
	}

	// Another computer never looks at it.
	other := *f.a
	cfg := *f.a.Cfg
	cfg.Installation.ID = ids.New(ids.Installation)
	other.Cfg = &cfg
	must[ops.LibraryActionOut](t, f.a, "collection.add", ops.CollectionItemsIn{Collection: "Gala", AssetIDs: []string{f.intro}}, true)
	if planned, _ := TriggerLook(f.ctx, &other, now.Add(time.Hour)); len(planned) != 0 {
		t.Fatalf("another computer: %v", planned)
	}
	if r2 := f.recipe(); r2.Triggers.Looked == nil || !r2.Triggers.Looked.Before(now.Add(30*time.Minute)) {
		t.Fatalf("only its own computer looked: %+v", r2.Triggers.Looked)
	}

	// Someone else, not an owner, can't change it; an owner can.
	member := ids.New(ids.User)
	ma := *f.a
	ma.Actor = models.Actor{UserID: member, UserName: "Max", Via: "dashboard"}
	mctx := ops.OverHTTP(f.ctx, func() bool { return false })
	if _, err := ops.CallTyped[RecipeTriggersOut](mctx, &ma, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &off}, ops.Caller{Human: true, Remote: true}); codeOf(err) != apperr.Unauthorized {
		t.Fatalf("someone else's: %v", err)
	}
	seen, err := ops.CallTyped[RecipeTriggersOut](mctx, &ma, "recipe.triggers", RecipeTriggersIn{Recipe: "app-teaser"}, ops.Caller{Remote: true})
	if err != nil || seen.Recipes[0].Mine || seen.Recipes[0].CanChange || seen.Recipes[0].Tells != "Ada" {
		t.Fatalf("anyone sees whom it tells: %+v %v", seen, err)
	}

	// A weekly time: once it comes, a plan and the bell; the next one a week on.
	week := must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", Weekly: ptrString("Mondays at 9am")}, true)
	if w := week.Recipes[0]; w.Weekly == nil || w.Weekly.Day != "monday" || w.Weekly.Time != "09:00" || w.WeeklyWords != "every Monday at 09:00" || w.Next == nil || !w.Next.After(time.Now()) {
		t.Fatalf("weekly: %+v", w)
	}
	past := now.Add(-time.Minute)
	if _, err := f.a.DB.C(store.CRecipes).UpdateOne(f.ctx, bson.M{"_id": r.ID}, bson.M{"$set": bson.M{"triggers.next": past}}); err != nil {
		t.Fatal(err)
	}
	if planned, err := TriggerLook(f.ctx, f.a, now); len(planned) != 1 {
		t.Fatalf("the weekly time came: %v %v", planned, err)
	}
	r = f.recipe()
	if r.Prepared == nil || r.Prepared.Why != models.PlannedWeekly || r.Triggers.Next == nil || !r.Triggers.Next.After(now) || r.Triggers.Next.Weekday() != time.Monday && r.Triggers.Next.Local().Weekday() != time.Monday {
		t.Fatalf("weekly plan: %+v next %v", r.Prepared, r.Triggers.Next)
	}
	if n := f.runCount(); n != 0 {
		t.Fatalf("still no run: %d", n)
	}

	// Set aside; then a run that starts uses a plan up.
	aside := must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", Dismiss: true}, true)
	if aside.Recipes[0].Prepared != nil || !strings.Contains(aside.Message, "set aside") {
		t.Fatalf("set aside: %+v %q", aside.Recipes[0].Prepared, aside.Message)
	}
	if _, err := f.a.DB.C(store.CRecipes).UpdateOne(f.ctx, bson.M{"_id": r.ID}, bson.M{"$set": bson.M{"triggers.next": past}}); err != nil {
		t.Fatal(err)
	}
	if planned, _ := TriggerLook(f.ctx, f.a, now); len(planned) != 1 || f.recipe().Prepared == nil {
		t.Fatalf("planned again: %v", planned)
	}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, true)
	must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true)
	if f.recipe().Prepared != nil {
		t.Fatal("a run that starts uses the plan up")
	}
	// Off: both.
	none := must[RecipeTriggersOut](t, f.a, "recipe.triggers.set", RecipeTriggersSetIn{Recipe: "app-teaser", NewFiles: &off, Weekly: ptrString("off")}, true)
	if none.Recipes[0].On || f.recipe().Triggers != nil || !strings.Contains(none.Message, "doesn't plan runs by itself any more") {
		t.Fatalf("off: %+v %q", none.Recipes[0], none.Message)
	}
}

func ptrString(s string) *string { return &s }

// Weekly times read the way people write them; the next one is on that day.
func TestWeeklyTimes(t *testing.T) {
	for in, want := range map[string]string{"monday 09:00": "monday 09:00", "Fridays at 5pm": "friday 17:00", "sun 18.30": "sunday 18:30", "wed 12am": "wednesday 00:00", "Tue 7": "tuesday 07:00"} {
		w, err := ParseWeekly(in)
		if err != nil || w.Day+" "+w.Time != want {
			t.Errorf("%q: %+v %v, want %s", in, w, err, want)
		}
	}
	for _, bad := range []string{"someday 09:00", "monday 25:00", "monday 13pm", "09:00", ""} {
		if _, err := ParseWeekly(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
	loc := time.FixedZone("here", -4*3600)
	now := time.Date(2026, 10, 24, 10, 0, 0, 0, loc) // a Saturday
	for day, want := range map[string]string{"saturday 11:00": "2026-10-24 11:00", "saturday 09:00": "2026-10-31 09:00", "monday 09:00": "2026-10-26 09:00"} {
		w, _ := ParseWeekly(day)
		if got := nextWeekly(w, now, loc).In(loc).Format("2006-01-02 15:04"); got != want {
			t.Errorf("%s after %s: %s, want %s", day, now, got, want)
		}
	}
}

// A footage input that takes a project's newest footage takes clips with a
// picture: never the narration made for the project's cuts (the AI voice's
// takes are the newest files there), which can't be a clip.
func TestFootageFromAProjectIsClipsWithAPicture(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	r := f.recipe()
	cur, _ := loadVersion(f.ctx, f.a, r, "")
	d := Clone(&cur.Doc)
	inputByName(d)["footage"].From = &models.RecipeSource{Project: f.teaser}
	chk := checkDoc(f.ctx, f.a, d)
	if !chk.OK {
		t.Fatalf("the check takes the project's newest clip with a picture: %+v", chk.Problems)
	}
	v, _, err := saveVersion(f.ctx, f.a, r, d, cur.Hash, "footage from the project", chk)
	if err != nil {
		t.Fatal(err)
	}
	if err := makeCurrent(f.ctx, f.a, r, v); err != nil {
		t.Fatal(err)
	}
	var takes int64
	takes, _ = f.a.DB.C(store.CAssets).CountDocuments(f.ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "projectIds": f.teaser, "tags": "voiceover"})
	if takes == 0 {
		t.Fatal("the fixture's narration should be in the project")
	}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	for _, in := range plan.Inputs {
		if in.Name == "footage" && (len(in.Items) != 1 || in.Items[0].ID != f.interview2) {
			t.Fatalf("the newest clip with a picture: %+v", in.Items)
		}
	}
	if !plan.Ready {
		t.Fatalf("plan: %v", plan.Problems)
	}
}
