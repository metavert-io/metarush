package abilities

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
)

// Checking an ability (D233): its folder becomes a canonical archive (its
// version), the manifest is read strictly and checked, credentials and
// oversized files are refused, and its test runs once in the sandbox with
// the network off. A version whose manifest has problems isn't recorded;
// one whose test failed is recorded as failed (nobody can approve it); the
// rest wait for an owner (D231), who is asked on To-dos and the bell.

// testMinutes caps the check's run.
const testMinutes = 5

// checkResult is what a check found.
type checkResult struct {
	Archive  *Archive
	Manifest *Manifest
	Problems []Problem
	Warnings []string
	Test     *models.AbilityTestRun
}

// checkFolder builds, validates and test-runs a folder. Manifest problems
// stop it before anything runs.
func checkFolder(ctx context.Context, a *app.App, dir string) (*checkResult, error) {
	arc, err := Build(dir)
	if err != nil {
		return nil, err
	}
	return checkArchive(ctx, a, arc)
}

func checkArchive(ctx context.Context, a *app.App, arc *Archive) (*checkResult, error) {
	res := &checkResult{Archive: arc, Warnings: []string{}}
	m, err := arc.Manifest()
	if err != nil {
		res.Problems = append(res.Problems, Problem{Field: ManifestFile, Text: err.Error()})
		return res, nil
	}
	res.Manifest = m
	res.Problems = m.Validate(arc.Programs())
	if catalog.IsFirstPartyID(m.Name) {
		// A name means one thing in find, describe, run and the menu (D268).
		res.Problems = append(res.Problems, Problem{Field: "name", Text: fmt.Sprintf("%s is the name of one of MetaRush's own abilities; give this one another name", m.Name)})
	}
	if _, ok := arc.File(NotesFile); !ok {
		res.Warnings = append(res.Warnings, "It has no notes.md: Claude learns how to use it well from one.")
	}
	if len(arc.Ignored) > 0 {
		res.Warnings = append(res.Warnings, fmt.Sprintf("%d hidden files or folders were left out of the version (%s).", len(arc.Ignored), strings.Join(firstN(arc.Ignored, 3), ", ")))
	}
	if len(res.Problems) > 0 {
		return res, nil
	}
	f := factsOf(m)
	here, paths := hereFor(ctx, a, f, m.Name, false)
	if !here.OS {
		res.Problems = append(res.Problems, Problem{Field: "needs.os", Text: "It runs only on " + joinAnd(osWords(f.OS)) + "; check it on such a computer."})
		return res, nil
	}
	if here.Isolation == "" {
		res.Problems = append(res.Problems, Problem{Text: sandbox.Here(a.Tools).Problem})
		return res, nil
	}
	for _, p := range here.Programs {
		if !p.OK {
			res.Problems = append(res.Problems, Problem{Field: "needs.programs", Text: p.Says})
		}
	}
	if len(res.Problems) > 0 {
		return res, nil
	}
	values, err := m.Values(m.Test.Inputs, true)
	if err != nil {
		res.Problems = append(res.Problems, Problem{Field: "test.inputs", Text: err.Error()})
		return res, nil
	}
	standIns := map[string]string{}
	for _, k := range f.Keys {
		standIns[k.Env] = "mrkey-test-" + k.Name
	}
	self, _ := os.Executable()
	r, err := runInRoom(ctx, a, roomSpec{Archive: arc, Manifest: m, Programs: paths, Values: values, Net: sandbox.NetOff, StandIns: standIns, Test: true,
		Timeout: time.Duration(min(m.MinutesLimit(), testMinutes)) * time.Minute, MaxBytes: m.BytesLimit(), Self: self, Isolation: sandbox.Here(a.Tools)})
	if err != nil {
		return nil, err
	}
	defer r.Close()
	t := &models.AbilityTestRun{Seconds: r.Seconds, Exit: r.Exit, Isolation: here.Isolation, Outputs: []models.AbilityTestOutput{}, Log: tailLines(r.Log, 64<<10)}
	t.OK = r.Exit == 0 && !r.TimedOut
	for _, o := range r.Outputs {
		t.Outputs = append(t.Outputs, models.AbilityTestOutput{Path: o.Rel, Kind: o.Kind, Size: o.Size, OK: o.OK})
		if !o.OK {
			t.OK = false
			res.Problems = append(res.Problems, Problem{Field: "test", Text: fmt.Sprintf("It wrote %s, which MetaRush wouldn't take: %s.", o.Rel, o.Why)})
		}
	}
	switch {
	case r.TimedOut:
		res.Problems = append(res.Problems, Problem{Field: "test", Text: fmt.Sprintf("Its test run took longer than %d minutes and was stopped.", min(m.MinutesLimit(), testMinutes))})
	case r.Exit != 0:
		res.Problems = append(res.Problems, Problem{Field: "test", Text: fmt.Sprintf("Its test run stopped with an error (status %d); what it printed is in the check's log.", r.Exit)})
	}
	res.Test = t
	return res, nil
}

func firstN(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), "…")
}

// tailLines keeps the end of a log, from a line's start.
func tailLines(s string, max int) string {
	if len(s) <= max {
		return s
	}
	s = s[len(s)-max:]
	if i := strings.IndexByte(s, '\n'); i >= 0 && i < 400 {
		s = s[i+1:]
	}
	return "…\n" + s
}

// record keeps a checked version: its archive in the media store and its
// record; a version people already decided about keeps their decisions.
func record(ctx context.Context, a *app.App, res *checkResult) (*models.AbilityVersion, bool, error) {
	arc, m := res.Archive, res.Manifest
	tmp, err := os.CreateTemp("", "metarush-ability-archive-")
	if err != nil {
		return nil, false, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(arc.Bytes); err != nil {
		tmp.Close()
		return nil, false, err
	}
	if err := tmp.Close(); err != nil {
		return nil, false, err
	}
	key := media.ObjectKey(a.WorkspaceID(), arc.Hash)
	info, err := a.Media.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "application/json", SHA256: arc.Hash}, media.Resume{}, nil)
	if err != nil {
		return nil, false, apperr.Wrap(apperr.Storage, err, "save the ability in the media store")
	}
	files := make([]models.AbilityFile, 0, len(arc.Files))
	for _, f := range arc.Files {
		b := f.Bytes()
		files = append(files, models.AbilityFile{Path: f.Path, Size: int64(len(b)), SHA256: sum(b), Program: f.Exec})
	}
	ok := len(res.Problems) == 0 && res.Test != nil && res.Test.OK
	problems := []string{}
	for _, p := range res.Problems {
		problems = append(problems, p.String())
	}
	check := models.AbilityCheck{At: now(), OK: ok, Problems: problems, Warnings: res.Warnings, Test: res.Test, By: a.Actor, Machine: a.Cfg.Installation.Name}
	manifest, _ := arc.File(ManifestFile)
	search := m.Search
	if search == nil {
		search = []string{}
	}
	set := bson.M{"title": m.Title, "does": m.Does, "search": search, "facts": factsOf(m), "manifest": string(manifest), "notes": arc.Notes(), "files": files,
		"ignored": arc.Ignored, "archive": models.AbilityObject{Key: key, SHA256: arc.Hash, Size: info.Size}, "check": check, "updatedAt": now()}
	id := docID(a.WorkspaceID(), arc.Hash)
	// A version already recorded keeps its number, state and decisions; a
	// failed one that now passes waits for an owner.
	var old models.AbilityVersion
	err = a.DB.GetByID(ctx, store.CAbilities, a.WorkspaceID(), id, &old)
	switch {
	case err == nil:
		switch {
		case (old.State == models.AbilityFailed || old.State == models.AbilityImported) && ok:
			set["state"] = models.AbilityWaiting // checked here: an owner decides (D292)
		case old.State == models.AbilityImported:
			set["state"] = models.AbilityFailed
		}
		if _, err := a.DB.C(store.CAbilities).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set}); err != nil {
			return nil, false, err
		}
		v, err := getVersion(ctx, a, arc.Hash)
		return v, false, err
	case !errors.Is(err, store.ErrNotFound):
		return nil, false, err
	}
	state := models.AbilityWaiting
	if !ok {
		state = models.AbilityFailed
	}
	for attempt := 0; attempt < 5; attempt++ {
		n, err := nextNumber(ctx, a, m.Name)
		if err != nil {
			return nil, false, err
		}
		v := &models.AbilityVersion{ID: id, WorkspaceID: a.WorkspaceID(), Name: m.Name, Hash: arc.Hash, Number: n, State: state, Decisions: []models.AbilityDecision{},
			By: a.Actor, CreatedAt: now()}
		doc := bson.M{"_id": v.ID, "workspaceId": v.WorkspaceID, "name": v.Name, "hash": v.Hash, "number": v.Number, "state": v.State, "decisions": v.Decisions,
			"by": v.By, "createdAt": v.CreatedAt}
		for k, x := range set {
			doc[k] = x
		}
		_, err = a.DB.C(store.CAbilities).InsertOne(ctx, doc)
		if mongo.IsDuplicateKeyError(err) {
			if got, gerr := getVersion(ctx, a, arc.Hash); gerr == nil {
				return got, false, nil // checked at the same time elsewhere
			}
			continue // another version took the number
		}
		if err != nil {
			return nil, false, err
		}
		got, err := getVersion(ctx, a, arc.Hash)
		return got, true, err
	}
	return nil, false, apperr.New(apperr.Conflict, "MetaRush couldn't number this version; check it again.")
}

// nextNumber is the number a new version of a name gets.
func nextNumber(ctx context.Context, a *app.App, name string) (int, error) {
	var vs []models.AbilityVersion
	if err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{"name": name}, &vs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1).SetProjection(bson.M{"number": 1, "name": 1})); err != nil {
		return 0, err
	}
	if len(vs) == 0 {
		return 1, nil
	}
	return vs[0].Number + 1, nil
}

// abs makes a folder's path absolute (~ is the home folder).
func abs(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	if a, err := filepath.Abs(p); err == nil {
		return a
	}
	return p
}
