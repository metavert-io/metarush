package recipes

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Planning by itself (D218): a recipe can plan a run when new files arrive
// where an input takes its values from (a linked folder's pictures, a
// collection's or a project's footage, a repository's folder of pictures) or
// at a weekly time, and ring the bell of the person who turned it on ("New
// footage in “Gala” for “Event recap”: the run is planned, about $1.20" — Run…).
// It never starts a run, whatever Auto or the estimate preference say: the
// plan waits on the recipe's page for a person to run it, or set it aside.
//
// The settings are the recipe's, off until a person turns them on (people
// only, recipe.triggers.set); they say whom they tell and which computer looks
// — the one they were turned on at, whose keys price the plan — so anyone can
// see them (recipe.triggers lists every recipe's), and only that person or an
// owner changes them while they are on. The daemon's loop (RunTriggers) looks
// every TriggerEvery; new files wait TriggerQuiet without more arriving (a
// folder being filled), then one plan is made, as the person, and the bell rings.

var (
	// TriggerEvery is how often the daemon looks.
	TriggerEvery = time.Minute
	// TriggerQuiet is how long new files wait without more arriving.
	TriggerQuiet = 2 * time.Minute
)

// maxSeen bounds the files a trigger remembers having seen.
const maxSeen = 2000

// triggerWake makes the daemon's loop look now (settings changed).
var triggerWake = make(chan struct{}, 1)

func nudgeTriggers() {
	select {
	case triggerWake <- struct{}{}:
	default:
	}
}

// RecipeTriggersIn picks a recipe, or every recipe that plans by itself.
type RecipeTriggersIn struct {
	Recipe string `json:"recipe,omitempty" jsonschema:"description=One recipe (its short name\\, title or ID); default every recipe that plans runs by itself."`
}

// RecipeTriggersSetIn changes when a recipe plans a run by itself.
type RecipeTriggersSetIn struct {
	Recipe string `json:"recipe" jsonschema:"minLength=1,description=Its short name\\, title or ID."`
	// NewFiles plans a run when new footage or pictures arrive where an input takes them from.
	NewFiles *bool `json:"newFiles,omitempty" jsonschema:"description=Plan a run when new footage or pictures arrive where an input takes them from (true) or stop (false)."`
	// Weekly plans a run every week ("monday 09:00"); off stops it.
	Weekly *string `json:"weekly,omitempty" jsonschema:"maxLength=40,description=Plan a run every week: a day and a time (monday 09:00\\, fri 5pm); off stops it."`
	// Dismiss sets aside the plan it made (nothing had started).
	Dismiss bool `json:"dismiss,omitempty" jsonschema:"description=Set aside the plan it made (nothing had started)."`
}

// RecipeTriggerRow is when one recipe plans a run by itself, plainly.
type RecipeTriggerRow struct {
	Recipe string `json:"recipe" jsonschema:"description=The recipe's short name."`
	Title  string `json:"title"`
	// On: it plans by itself (new files, a weekly time, or both).
	On       bool `json:"on"`
	NewFiles bool `json:"newFiles"`
	// Sources say where new files are looked for ("the collection “Gala”");
	// none: no input takes its values from anywhere new files arrive.
	Sources     []string             `json:"sources"`
	Weekly      *models.RecipeWeekly `json:"weekly,omitempty"`
	WeeklyWords string               `json:"weeklyWords,omitempty" jsonschema:"description=The weekly time in words (every Monday at 09:00)."`
	Next        *time.Time           `json:"next,omitempty" jsonschema:"description=When the weekly plan is made next."`
	// Tells is whose bell rings (who turned it on); Computer the computer that looks.
	Tells    string     `json:"tells,omitempty"`
	TellsID  string     `json:"tellsId,omitempty"`
	Computer string     `json:"computer,omitempty"`
	By       string     `json:"by,omitempty"`
	At       *time.Time `json:"at,omitempty"`
	Looked   *time.Time `json:"looked,omitempty" jsonschema:"description=When its computer last looked."`
	// Mine: it tells the person asking; CanChange: they may change it (it is
	// off, it is theirs, or they are an owner).
	Mine      bool `json:"mine"`
	CanChange bool `json:"canChange"`
	// Prepared is the plan it made, waiting for a person to run it.
	Prepared *models.RecipePrepared `json:"prepared,omitempty"`
	// Words say it all in a sentence.
	Words string `json:"words"`
	URL   string `json:"url"`
}

// RecipeTriggersOut lists when recipes plan runs by themselves.
type RecipeTriggersOut struct {
	Recipes []RecipeTriggerRow `json:"recipes"`
	Summary string             `json:"summary"`
	Message string             `json:"message,omitempty"`
}

func init() {
	ops.Def("recipe.triggers", "Say when recipes plan a run by themselves (new footage or pictures where an input takes them from, a weekly time), whom they tell, which computer looks and the plan waiting; they never start a run", ops.Flags{}, listTriggers)
	ops.Def("recipe.triggers.set", "Turn planning by itself on or off for a recipe — when new files arrive where an input takes them from, or every week at a time — or set aside the plan it made; it tells you and never starts a run — people only", ops.Flags{Mutating: true, HumanOnly: true}, setTriggers)
}

func triggersOn(t *models.RecipeTriggers) bool { return t != nil && (t.NewFiles || t.Weekly != nil) }

func listTriggers(ctx context.Context, a *app.App, in *RecipeTriggersIn) (*RecipeTriggersOut, error) {
	out := &RecipeTriggersOut{Recipes: []RecipeTriggerRow{}}
	if strings.TrimSpace(in.Recipe) != "" {
		r, err := loadRecipe(ctx, a, in.Recipe)
		if err != nil {
			return nil, err
		}
		row := triggerRow(ctx, a, r)
		out.Recipes = append(out.Recipes, row)
		out.Summary = row.Words
		return out, nil
	}
	var rs []models.Recipe
	if err := a.DB.Find(ctx, store.CRecipes, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"triggers": bson.M{"$ne": nil}}, bson.M{"prepared": bson.M{"$ne": nil}}}}, &rs,
		options.Find().SetSort(bson.D{{Key: "name", Value: 1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	for i := range rs {
		if triggersOn(rs[i].Triggers) || rs[i].Prepared != nil {
			out.Recipes = append(out.Recipes, triggerRow(ctx, a, &rs[i]))
		}
	}
	switch n := len(out.Recipes); n {
	case 0:
		out.Summary = "No recipe plans runs by itself. Turn it on on a recipe's page: it plans a run and tells you, and never starts one."
	default:
		out.Summary = plural(n, "recipe") + " plan runs by themselves; none ever starts one."
	}
	return out, nil
}

// triggerRow says when a recipe plans by itself, for the person asking.
func triggerRow(ctx context.Context, a *app.App, r *models.Recipe) RecipeTriggerRow {
	row := RecipeTriggerRow{Recipe: r.Name, Title: r.Title, Sources: []string{}, Prepared: r.Prepared, URL: ops.DashboardURL(a, links.Recipe(r.Name))}
	if v, err := loadVersion(ctx, a, r, ""); err == nil {
		for _, s := range sourcesOf(ctx, a, &v.Doc) {
			row.Sources = append(row.Sources, s.where)
		}
	}
	t := r.Triggers
	row.On = triggersOn(t)
	row.CanChange = !row.On || t.Tells.UserID == "" || t.Tells.UserID == a.Actor.UserID || ops.IsOwnerCaller(ctx)
	if t != nil && row.On {
		row.NewFiles, row.Weekly, row.Next, row.Looked = t.NewFiles, t.Weekly, t.Next, t.Looked
		if t.Weekly != nil {
			row.WeeklyWords = weeklyWords(t.Weekly)
		}
		row.Tells, row.TellsID, row.Computer, row.By = personName(t.Tells), t.Tells.UserID, t.Computer, personName(t.By)
		at := t.At
		row.At = &at
		row.Mine = t.Tells.UserID != "" && t.Tells.UserID == a.Actor.UserID
	}
	row.Words = triggerWords(r, row)
	return row
}

func personName(x models.Actor) string {
	if x.UserName != "" {
		return x.UserName
	}
	return "someone"
}

// triggerWords says in one sentence when a recipe plans by itself.
func triggerWords(r *models.Recipe, row RecipeTriggerRow) string {
	if !row.On {
		return fmt.Sprintf("“%s” doesn't plan runs by itself.", r.Title)
	}
	var when []string
	if row.NewFiles {
		where := "where its inputs take them from"
		if len(row.Sources) > 0 {
			where = "in " + joinAnd(row.Sources)
		}
		when = append(when, "when new footage or pictures arrive "+where)
	}
	if row.WeeklyWords != "" {
		when = append(when, row.WeeklyWords)
	}
	s := fmt.Sprintf("“%s” plans a run %s and tells %s", r.Title, joinAnd(when), row.Tells)
	if row.Computer != "" {
		s += fmt.Sprintf(" (its computer “%s” looks)", row.Computer)
	}
	return s + "; it never starts one."
}

func setTriggers(ctx context.Context, a *app.App, in *RecipeTriggersSetIn) (*RecipeTriggersOut, error) {
	if in.NewFiles == nil && in.Weekly == nil && !in.Dismiss {
		return nil, apperr.New(apperr.InvalidInput, "Say what to change: new files on or off, a weekly time (or off), or set its plan aside.")
	}
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	old := r.Triggers
	if triggersOn(old) && old.Tells.UserID != "" && old.Tells.UserID != a.Actor.UserID && !ops.IsOwnerCaller(ctx) {
		return nil, apperr.New(apperr.Unauthorized, "“%s” plans runs by itself for %s: only %s or an owner changes that.", r.Title, personName(old.Tells), personName(old.Tells)).
			WithDetails(map[string]any{"reason": "not_yours", "tells": old.Tells.UserID})
	}
	now := time.Now().UTC()
	t := &models.RecipeTriggers{}
	if old != nil {
		cp := *old
		t = &cp
	}
	var msgs []string
	if in.NewFiles != nil {
		if *in.NewFiles {
			v, err := loadVersion(ctx, a, r, "")
			if err != nil {
				return nil, err
			}
			srcs := sourcesOf(ctx, a, &v.Doc)
			if len(srcs) == 0 {
				return nil, apperr.New(apperr.InvalidInput, "No input of “%s” takes its footage or pictures from a folder, a collection, a project or a repository, so no new files can arrive for it.", r.Title).
					WithHint("Give an input a place to take them from (its sheet: from folder, from collection, from project), or plan it every week instead.").WithCommand("metarush recipe checkout " + r.Name)
			}
			if !t.NewFiles {
				// What is there now isn't new: only files that arrive from here on are.
				seen, err := sourceItems(ctx, a, srcs)
				if err != nil {
					return nil, err
				}
				t.Seen, t.Pending, t.PendingSince = seen, nil, nil
				msgs = append(msgs, fmt.Sprintf("When new footage or pictures arrive in %s, MetaRush plans a run and tells you.", joinAnd(sourceWheres(srcs))))
			}
		} else if t.NewFiles {
			t.Seen, t.Pending, t.PendingSince = nil, nil, nil
			msgs = append(msgs, "New files no longer plan a run.")
		}
		t.NewFiles = *in.NewFiles
	}
	if in.Weekly != nil {
		raw := strings.TrimSpace(*in.Weekly)
		if raw == "" || strings.EqualFold(raw, "off") {
			if t.Weekly != nil {
				msgs = append(msgs, "No weekly plan any more.")
			}
			t.Weekly, t.Next = nil, nil
		} else {
			w, err := ParseWeekly(raw)
			if err != nil {
				return nil, err
			}
			next := nextWeekly(w, now, time.Local)
			t.Weekly, t.Next = w, &next
			msgs = append(msgs, fmt.Sprintf("%s MetaRush plans a run and tells you (next: %s).", upperFirst(weeklyWords(w)), next.Local().Format("Mon 2 Jan 15:04")))
		}
	}
	on := triggersOn(t)
	if on && !r.Archived && r.Current == "" {
		return nil, apperr.New(apperr.Validation, "“%s” has no current version yet: a person makes one current before it can plan runs.", r.Title).WithCommand("metarush recipe publish " + r.Name)
	}
	if on && r.Archived {
		return nil, apperr.New(apperr.Conflict, "“%s” is archived; restore it first.", r.Title)
	}
	if on && !triggersOn(old) {
		// Turned on: it tells this person, and this computer looks.
		t.Tells = models.Actor{UserID: a.Actor.UserID, UserName: a.Actor.UserName}
		// …where this computer's background work runs (D349).
		host, err := a.PinWork(ctx)
		if err != nil {
			return nil, err
		}
		t.Installation, t.Computer = host.ID, host.Name
	}
	t.By, t.At = models.Actor{UserID: a.Actor.UserID, UserName: a.Actor.UserName, Via: a.Actor.Via}, now
	set, unset := bson.M{"updatedAt": now}, bson.M{}
	if on {
		set["triggers"] = t
	} else {
		unset["triggers"] = ""
	}
	if in.Dismiss {
		unset["prepared"] = ""
		if r.Prepared != nil {
			msgs = append(msgs, "The plan it made is set aside; nothing had started.")
		}
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	filter := bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}
	if old != nil {
		filter["triggers.at"] = old.At
	} else {
		filter["triggers"] = nil
	}
	res, err := a.DB.C(store.CRecipes).UpdateOne(ctx, filter, update)
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone changed when “%s” plans by itself meanwhile; look again.", r.Title)
	}
	a.Audit(ctx, "recipe.triggers.set", "recipe", r.ID, r.ProjectID, map[string]any{"newFiles": t.NewFiles, "weekly": t.Weekly != nil, "dismiss": in.Dismiss, "on": on})
	nudgeTriggers()
	if !on && triggersOn(old) {
		msgs = append(msgs, fmt.Sprintf("“%s” doesn't plan runs by itself any more.", r.Title))
	}
	fresh, err := loadRecipe(ctx, a, r.ID)
	if err != nil {
		return nil, err
	}
	row := triggerRow(ctx, a, fresh)
	out := &RecipeTriggersOut{Recipes: []RecipeTriggerRow{row}, Summary: row.Words, Message: strings.Join(msgs, " ")}
	if out.Message == "" {
		out.Message = row.Words
	}
	return out, nil
}

// ---- weekly ----

var weekdays = []string{"sunday", "monday", "tuesday", "wednesday", "thursday", "friday", "saturday"}

var weeklyPattern = regexp.MustCompile(`^(?i)(mon|tue|wed|thu|fri|sat|sun)[a-z]*\s+(?:at\s+)?(\d{1,2})(?:[:.](\d{2}))?\s*(am|pm)?$`)

// ParseWeekly reads a weekly time: a day and a time ("monday 09:00",
// "Fridays at 5pm", "sun 18.30").
func ParseWeekly(s string) (*models.RecipeWeekly, error) {
	m := weeklyPattern.FindStringSubmatch(strings.TrimSpace(s))
	bad := apperr.New(apperr.InvalidInput, "“%s” isn't a day and a time of the week; say it like monday 09:00 or fri 5pm.", s)
	if m == nil {
		return nil, bad
	}
	day := ""
	for _, d := range weekdays {
		if strings.HasPrefix(d, strings.ToLower(m[1])) {
			day = d
		}
	}
	h, _ := strconv.Atoi(m[2])
	mi := 0
	if m[3] != "" {
		mi, _ = strconv.Atoi(m[3])
	}
	switch strings.ToLower(m[4]) {
	case "am":
		if h < 1 || h > 12 {
			return nil, bad
		}
		if h == 12 {
			h = 0
		}
	case "pm":
		if h < 1 || h > 12 {
			return nil, bad
		}
		if h != 12 {
			h += 12
		}
	}
	if h > 23 || mi > 59 {
		return nil, bad
	}
	return &models.RecipeWeekly{Day: day, Time: fmt.Sprintf("%02d:%02d", h, mi)}, nil
}

// nextWeekly is the next weekly time after now on loc's clock.
func nextWeekly(w *models.RecipeWeekly, now time.Time, loc *time.Location) time.Time {
	want := 1
	for i, d := range weekdays {
		if d == w.Day {
			want = i
		}
	}
	h, mi := 9, 0
	if hh, mm, ok := strings.Cut(w.Time, ":"); ok {
		h, _ = strconv.Atoi(hh)
		mi, _ = strconv.Atoi(mm)
	}
	l := now.In(loc)
	for d := 0; d <= 7; d++ {
		day := l.AddDate(0, 0, d)
		at := time.Date(day.Year(), day.Month(), day.Day(), h, mi, 0, 0, loc)
		if int(at.Weekday()) == want && at.After(now) {
			return at.UTC()
		}
	}
	return now.Add(7 * 24 * time.Hour).UTC()
}

// weeklyWords says a weekly time ("every Monday at 09:00").
func weeklyWords(w *models.RecipeWeekly) string {
	return "every " + upperFirst(w.Day) + " at " + w.Time
}

// ---- where new files arrive ----

// trigSource is an input that takes its values from where new files arrive.
type trigSource struct {
	in models.RecipeInput
	// where says the place in a few words ("the collection “Gala”").
	where string
}

func sourceWheres(srcs []trigSource) []string {
	var out []string
	for _, s := range srcs {
		out = append(out, s.where)
	}
	return out
}

// sourcesOf lists a recipe's inputs that take footage or pictures from a
// place new files arrive in: a linked folder, a collection, a project, a
// repository's folder.
func sourcesOf(ctx context.Context, a *app.App, d *models.RecipeDoc) []trigSource {
	names := newSheetContext(ctx, a)
	var out []trigSource
	for _, in := range d.Inputs {
		f := in.From
		if f == nil || (in.Kind != models.RecipeFootage && in.Kind != models.RecipePictures) {
			continue
		}
		s := trigSource{in: in}
		switch {
		case f.Folder != "" && in.Kind == models.RecipePictures:
			s.where = "the linked folder " + quoteOr(names.title(f.Folder), "that is gone")
		case f.Collection != "" && in.Kind == models.RecipeFootage:
			s.where = "the collection " + quoteOr(names.title(f.Collection), "that is gone")
		case f.Project != "" && in.Kind == models.RecipeFootage:
			s.where = quoteOr(names.title(f.Project), "a project that was cleared out")
		case f.Repo != "" && in.Kind == models.RecipePictures:
			var r models.Resource
			if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), f.Repo, &r) != nil {
				continue
			}
			dir, err := resources.CleanPicturePath(f.Path)
			if err != nil {
				continue
			}
			s.where = repoPlace(&r, dir)
		default:
			continue
		}
		out = append(out, s)
	}
	return out
}

func quoteOr(name, none string) string {
	if strings.TrimSpace(name) == "" {
		return none
	}
	return "“" + name + "”"
}

// sourceItems are the files the sources hold now, each as ID@version (a
// picture's newer version is new art).
func sourceItems(ctx context.Context, a *app.App, srcs []trigSource) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	add := func(id, sha string) {
		k := id + "@" + sha
		if !seen[k] {
			seen[k] = true
			out = append(out, k)
		}
	}
	for _, s := range srcs {
		items, err := itemsOf(ctx, a, s)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			add(it.ID, it.SHA256)
		}
	}
	return out, nil
}

// itemsOf are the files one source holds now (newest first for footage).
func itemsOf(ctx context.Context, a *app.App, s trigSource) ([]models.RecipeRunItem, error) {
	f := s.in.From
	var out []models.RecipeRunItem
	switch {
	case f.Folder != "":
		var rs []models.Resource
		if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"folder.folderId": f.Folder, "removed": bson.M{"$ne": true},
			"kind": bson.M{"$in": bson.A{editspec.ResourceKindImage, editspec.ResourceKindSVG}}}, &rs, options.Find().SetLimit(maxSeen)); err != nil {
			return nil, err
		}
		for i := range rs {
			if it, why := pictureItem(&rs[i], ""); why == "" {
				out = append(out, it)
			}
		}
	case f.Collection != "" || f.Project != "":
		src := f.Collection
		if src == "" {
			src = f.Project
		}
		var as []models.Asset
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), sourceFootage(src), &as, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(maxSeen)); err != nil {
			return nil, err
		}
		for i := range as {
			out = append(out, clipItem(&as[i]))
		}
	case f.Repo != "":
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), f.Repo, &r) != nil {
			return nil, nil
		}
		dir, err := resources.CleanPicturePath(f.Path)
		v := r.Latest()
		if err != nil || v == nil || v.Repo == nil {
			return nil, nil
		}
		for _, p := range resources.KeptPictures(&r, v, dir) {
			out = append(out, models.RecipeRunItem{ID: p.ResourceID, SHA256: p.SHA256})
		}
	}
	return out, nil
}

// ---- the daemon's loop ----

// RunTriggers is the daemon's loop: every TriggerEvery it looks at the
// recipes that plan by themselves on this computer (TriggerLook).
// METARUSH_RECIPE_TRIGGER_SECONDS makes it look, and new files settle, that
// many seconds (browser runs).
func RunTriggers(ctx context.Context, a *app.App, logf func(string, ...any)) {
	if n, err := strconv.Atoi(os.Getenv("METARUSH_RECIPE_TRIGGER_SECONDS")); err == nil && n > 0 {
		TriggerEvery, TriggerQuiet = time.Duration(n)*time.Second, time.Duration(n)*time.Second
	}
	t := time.NewTimer(TriggerEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-triggerWake:
			if !t.Stop() {
				select {
				case <-t.C:
				default:
				}
			}
		}
		planned, err := TriggerLook(ctx, a, time.Now().UTC())
		if err != nil && ctx.Err() == nil && logf != nil {
			logf("recipes: planning by itself: %v", err)
		}
		for _, p := range planned {
			if logf != nil {
				logf("recipes: planned a run of “%s” by itself; it waits for a person", p)
			}
		}
		t.Reset(TriggerEvery)
	}
}

// TriggerLook looks once at every recipe that plans by itself on this
// computer: new files settled, or the weekly time came, make a plan and ring
// the bell. It never starts a run. It returns the recipes it planned.
func TriggerLook(ctx context.Context, a *app.App, now time.Time) ([]string, error) {
	var rs []models.Recipe
	if err := a.DB.Find(ctx, store.CRecipes, a.WorkspaceID(), bson.M{"triggers.installation": a.Cfg.Installation.ID, "archived": bson.M{"$ne": true}}, &rs,
		options.Find().SetLimit(500)); err != nil {
		return nil, err
	}
	var planned []string
	var firstErr error
	for i := range rs {
		if ctx.Err() != nil {
			break
		}
		ok, err := lookAt(ctx, a, &rs[i], now)
		if err != nil && firstErr == nil {
			firstErr = fmt.Errorf("%s: %w", rs[i].Name, err)
		}
		if ok {
			planned = append(planned, rs[i].Title)
		}
	}
	return planned, firstErr
}

// lookAt looks at one recipe's triggers; true when it made a plan.
func lookAt(ctx context.Context, a *app.App, r *models.Recipe, now time.Time) (bool, error) {
	t := r.Triggers
	if !triggersOn(t) || r.Current == "" {
		return false, nil
	}
	set := bson.M{"triggers.looked": now}
	unset := bson.M{}
	why, words := "", ""
	if t.NewFiles {
		v, err := loadVersion(ctx, a, r, "")
		if err != nil {
			return false, err
		}
		srcs := sourcesOf(ctx, a, &v.Doc)
		items, err := sourceItems(ctx, a, srcs)
		if err != nil {
			return false, err
		}
		had := map[string]bool{}
		for _, k := range t.Seen {
			had[k] = true
		}
		var fresh []string
		for _, k := range items {
			if !had[k] {
				fresh = append(fresh, k)
			}
		}
		switch {
		case len(fresh) == 0:
			if len(t.Pending) > 0 {
				unset["triggers.pending"], unset["triggers.pendingSince"] = "", ""
			}
		case !sameSet(fresh, t.Pending) || t.PendingSince == nil:
			// New files arrived (or more of them): wait for them to settle.
			set["triggers.pending"], set["triggers.pendingSince"] = fresh, now
		case now.Sub(*t.PendingSince) >= TriggerQuiet:
			why, words = models.PlannedNewFiles, newFilesWords(ctx, a, srcs, fresh)
			set["triggers.seen"] = mergeSeen(t.Seen, items)
			unset["triggers.pending"], unset["triggers.pendingSince"] = "", ""
		}
	}
	if t.Weekly != nil {
		switch {
		case t.Next == nil:
			next := nextWeekly(t.Weekly, now, time.Local)
			set["triggers.next"] = next
		case !now.Before(*t.Next):
			if why == "" {
				why, words = models.PlannedWeekly, upperFirst(weeklyWords(t.Weekly))
			}
			set["triggers.next"] = nextWeekly(t.Weekly, now, time.Local)
		}
	}
	var prepared *models.RecipePrepared
	if why != "" {
		p, err := prepare(ctx, a, r, why, words, now)
		if err != nil {
			return false, err
		}
		prepared = p
		set["prepared"] = p
	}
	update := bson.M{"$set": set}
	if len(unset) > 0 {
		update["$unset"] = unset
	}
	// Only while the settings are the ones looked at (a person may change them
	// meanwhile) and they are still this computer's (a handover may have moved
	// them to another one, which plans from then on: D346).
	res, err := a.DB.C(store.CRecipes).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "triggers.at": t.At,
		"triggers.installation": a.Cfg.Installation.ID}, update)
	if err != nil || res.MatchedCount == 0 || prepared == nil {
		return false, err
	}
	ring(ctx, a, r, prepared)
	return true, nil
}

func sameSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]string(nil), a...)
	y := append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// mergeSeen adds what the sources hold now to what was seen (the newest kept).
func mergeSeen(seen, items []string) []string {
	have := map[string]bool{}
	out := append([]string(nil), seen...)
	for _, k := range out {
		have[k] = true
	}
	for _, k := range items {
		if !have[k] {
			have[k] = true
			out = append(out, k)
		}
	}
	if len(out) > maxSeen {
		out = out[len(out)-maxSeen:]
	}
	return out
}

// newFilesWords says what arrived where ("New footage on the collection “Gala”
// (2 clips)").
func newFilesWords(ctx context.Context, a *app.App, srcs []trigSource, fresh []string) string {
	isNew := map[string]bool{}
	for _, k := range fresh {
		isNew[k] = true
	}
	var parts []string
	for _, s := range srcs {
		items, err := itemsOf(ctx, a, s)
		if err != nil {
			continue
		}
		n := 0
		for _, it := range items {
			if isNew[it.ID+"@"+it.SHA256] {
				n++
			}
		}
		if n == 0 {
			continue
		}
		what, unit, on := "New pictures", "picture", "in"
		if s.in.Kind == models.RecipeFootage {
			what, unit = "New footage", "clip"
		}
		if s.in.From.Collection != "" {
			on = "on"
		}
		parts = append(parts, fmt.Sprintf("%s %s %s (%s)", what, on, s.where, plural(n, unit)))
	}
	if len(parts) == 0 {
		return "New files where the recipe takes them from"
	}
	return strings.Join(parts, "; ")
}

// prepare plans a run as the person the triggers tell (their estimate
// preference, their budgets): nothing starts.
func prepare(ctx context.Context, a *app.App, r *models.Recipe, why, words string, now time.Time) (*models.RecipePrepared, error) {
	t := r.Triggers
	pa := *a
	pa.Actor = models.Actor{UserID: t.Tells.UserID, UserName: t.Tells.UserName, InstallationID: a.Cfg.Installation.ID, Via: models.ViaRecipe}
	p, err := makePlan(ctx, &pa, planIn{Recipe: r.ID})
	if err != nil {
		return nil, err
	}
	out := &models.RecipePrepared{Why: why, Words: words, At: now, For: models.Actor{UserID: t.Tells.UserID, UserName: t.Tells.UserName},
		Version: p.Version.Number, Inputs: p.Inputs, Estimate: p.Estimate, Ready: len(p.Problems) == 0, Fingerprint: p.Fingerprint}
	po := planOut(ctx, &pa, p)
	out.Lands = po.Lands
	for i, pr := range p.Problems {
		if i == 5 {
			break
		}
		out.Problems = append(out.Problems, pr)
	}
	return out, nil
}

// priceWords says a plan's estimate after "the run is planned".
func priceWords(p *models.RecipePrepared) string {
	switch {
	case p.Estimate.USD <= 0:
		return "nothing to pay"
	case p.Estimate.About != "":
		return p.Estimate.About
	}
	return "about " + fmt.Sprintf("$%.2f", p.Estimate.USD)
}

// ring tells the person the triggers tell: a plan waits for them to run it.
func ring(ctx context.Context, a *app.App, r *models.Recipe, p *models.RecipePrepared) {
	title := fmt.Sprintf("%s for “%s”: the run is planned, %s", p.Words, r.Title, priceWords(p))
	body := "Nothing starts until you run it."
	tone := "info"
	if !p.Ready {
		title = fmt.Sprintf("%s for “%s”, but the run can't start yet", p.Words, r.Title)
		tone = "warning"
		if len(p.Problems) > 0 {
			body = strings.TrimSuffix(p.Problems[0], ".") + "."
		}
	}
	_, _ = notify.Upsert(ctx, a, models.Notification{UserID: p.For.UserID, Kind: models.NotifyRecipePlanned, GroupKey: "recipe-planned:" + r.ID, Tone: tone,
		Title: shorten(title, 300), Body: body, Link: links.RecipePlanned(r.Name), ProjectID: r.ProjectID}, notify.Refresh)
}
