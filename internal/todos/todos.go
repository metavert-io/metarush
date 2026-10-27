// Package todos keeps the questions MetaRush cannot answer by itself — who is
// speaking, may this music be used, what is this project for — as to-dos a
// person answers. The system creates them idempotently (one open to-do per
// group key, enforced by a partial unique index), refreshes them when the
// situation changes and closes them when the data is fixed another way.
// Answering is done by the ops layer, through the normal operations.
package todos

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Upsert creates the to-do for t.GroupKey, or refreshes the open one (question,
// context, suggestions, kind may change as evidence changes). It never reopens a
// question a person already answered or dismissed. It reports whether a new
// to-do was created.
func Upsert(ctx context.Context, a *app.App, t models.Todo) (bool, error) {
	c := a.DB.C(store.CTodos)
	ws := a.WorkspaceID()
	closed, err := c.CountDocuments(ctx, bson.M{"workspaceId": ws, "groupKey": t.GroupKey, "state": bson.M{"$in": bson.A{models.TodoAnswered, models.TodoDismissed}},
		"resolution": bson.M{"$in": bson.A{models.ResolvedAnswered, models.ResolvedDismissed}}})
	if err != nil {
		return false, err
	}
	if closed > 0 {
		return false, nil
	}
	now := time.Now().UTC()
	if t.Suggestions == nil {
		t.Suggestions = []models.TodoSuggestion{}
	}
	if t.ProjectIDs == nil {
		t.ProjectIDs = []string{}
	}
	sort.Strings(t.ProjectIDs)
	if t.Priority == 0 {
		t.Priority = 2
	}
	set := bson.M{"kind": t.Kind, "scope": t.Scope, "assetId": t.AssetID, "projectId": t.ProjectID, "revisionId": t.RevisionID, "projectIds": t.ProjectIDs,
		"question": t.Question, "detail": t.Detail, "context": t.Context, "suggestions": t.Suggestions, "freeText": t.FreeText, "humanOnly": t.HumanOnly,
		"priority": t.Priority}
	res, err := c.UpdateOne(ctx, bson.M{"workspaceId": ws, "groupKey": t.GroupKey, "state": models.TodoOpen}, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	if res.MatchedCount > 0 {
		if res.ModifiedCount > 0 {
			_, _ = c.UpdateOne(ctx, bson.M{"workspaceId": ws, "groupKey": t.GroupKey, "state": models.TodoOpen}, bson.M{"$set": bson.M{"updatedAt": now}})
		}
		return false, nil
	}
	t.ID, t.WorkspaceID, t.State, t.CreatedAt, t.UpdatedAt = ids.New(ids.Todo), ws, models.TodoOpen, now, now
	if err := a.DB.Insert(ctx, store.CTodos, t); err != nil {
		if store.IsDuplicateKey(err) { // another worker created it a moment ago
			_, err = c.UpdateOne(ctx, bson.M{"workspaceId": ws, "groupKey": t.GroupKey, "state": models.TodoOpen}, bson.M{"$set": set})
			return false, err
		}
		return false, err
	}
	return true, nil
}

// Close marks the open to-do for key as resolved without an answer from a
// person (resolution is ResolvedElsewhere or ResolvedObsolete). It reports
// whether one was open.
func Close(ctx context.Context, a *app.App, key, resolution, note string) (bool, error) {
	now := time.Now().UTC()
	state := models.TodoAnswered
	if resolution == models.ResolvedObsolete {
		state = models.TodoDismissed
	}
	set := bson.M{"state": state, "resolution": resolution, "updatedAt": now, "answeredAt": now}
	if note != "" {
		set["answer"] = note
	}
	res, err := a.DB.C(store.CTodos).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupKey": key, "state": models.TodoOpen}, bson.M{"$set": set})
	if err != nil {
		return false, err
	}
	return res.ModifiedCount > 0, nil
}

// CloseStale closes open to-dos whose key starts with prefix but is not in
// keep (the question no longer applies: e.g. the voices were re-detected).
func CloseStale(ctx context.Context, a *app.App, prefix string, keep []string) (int, error) {
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "groupKey": bson.M{"$regex": "^" + regexQuote(prefix)}}, &open,
		options.Find().SetProjection(bson.M{"groupKey": 1})); err != nil {
		return 0, err
	}
	kept := map[string]bool{}
	for _, k := range keep {
		kept[k] = true
	}
	n := 0
	for _, t := range open {
		if kept[t.GroupKey] {
			continue
		}
		if ok, err := Close(ctx, a, t.GroupKey, models.ResolvedObsolete, ""); err != nil {
			return n, err
		} else if ok {
			n++
		}
	}
	return n, nil
}

// Finish records a person's answer (or dismissal) on a to-do.
func Finish(ctx context.Context, a *app.App, id, state, answer string) (*models.Todo, error) {
	now := time.Now().UTC()
	actor := a.Actor
	resolution := models.ResolvedAnswered
	if state == models.TodoDismissed {
		resolution = models.ResolvedDismissed
	}
	if err := a.DB.Update(ctx, store.CTodos, a.WorkspaceID(), id, bson.M{"$set": bson.M{"state": state, "resolution": resolution, "answer": answer,
		"answeredBy": actor, "answeredAt": now, "updatedAt": now}}); err != nil {
		return nil, err
	}
	var t models.Todo
	if err := a.DB.GetByID(ctx, store.CTodos, a.WorkspaceID(), id, &t); err != nil {
		return nil, err
	}
	return &t, nil
}

// Counts are open to-do totals.
type Counts struct {
	Total     int            `json:"total"`
	ByKind    map[string]int `json:"byKind"`
	ByProject map[string]int `json:"byProject"`
	ByAsset   map[string]int `json:"byAsset"`
}

// Count tallies open to-dos for badges.
func Count(ctx context.Context, a *app.App) (*Counts, error) {
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen}, &open,
		options.Find().SetProjection(bson.M{"kind": 1, "projectIds": 1, "assetId": 1})); err != nil {
		return nil, err
	}
	out := &Counts{ByKind: map[string]int{}, ByProject: map[string]int{}, ByAsset: map[string]int{}}
	for _, t := range open {
		out.Total++
		out.ByKind[t.Kind]++
		for _, p := range t.ProjectIDs {
			out.ByProject[p]++
		}
		if t.AssetID != "" {
			out.ByAsset[t.AssetID]++
		}
	}
	return out, nil
}

// SetAssetProjects makes the open questions about a clip show up in the clip's
// current projects (plus the project a question is about, if any), so project
// badges and the To-dos project filter follow a clip that was added to or
// removed from a project after its questions were asked.
func SetAssetProjects(ctx context.Context, a *app.App, assetID string, projectIDs []string) error {
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "assetId": assetID}, &open,
		options.Find().SetProjection(bson.M{"projectId": 1, "projectIds": 1})); err != nil {
		return err
	}
	for _, t := range open {
		seen := map[string]bool{}
		want := []string{}
		for _, p := range append(append([]string{}, projectIDs...), t.ProjectID) {
			if p != "" && !seen[p] {
				seen[p] = true
				want = append(want, p)
			}
		}
		sort.Strings(want)
		if strings.Join(want, ",") == strings.Join(t.ProjectIDs, ",") {
			continue
		}
		if _, err := a.DB.C(store.CTodos).UpdateOne(ctx, bson.M{"_id": t.ID, "workspaceId": a.WorkspaceID(), "state": models.TodoOpen},
			bson.M{"$set": bson.M{"projectIds": want, "updatedAt": time.Now().UTC()}}); err != nil {
			return err
		}
	}
	return nil
}

// OpenByAsset counts open to-dos per clip for the given clips.
func OpenByAsset(ctx context.Context, a *app.App, assetIDs []string) (map[string]int, error) {
	out := map[string]int{}
	if len(assetIDs) == 0 {
		return out, nil
	}
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "assetId": bson.M{"$in": assetIDs}}, &open,
		options.Find().SetProjection(bson.M{"assetId": 1})); err != nil {
		return nil, err
	}
	for _, t := range open {
		out[t.AssetID]++
	}
	return out, nil
}

// AboutClip says that n questions about a clip are open and links to them (nil
// when there are none). Job details, the activity bar and notifications use it.
func AboutClip(assetID string, n int) *models.OpenQuestions {
	if n <= 0 || assetID == "" {
		return nil
	}
	text := "1 question about this clip"
	if n > 1 {
		text = fmt.Sprintf("%d questions about this clip", n)
	}
	return &models.OpenQuestions{Count: n, Text: text, Link: links.Todos(assetID)}
}

// ForClip is AboutClip for one clip, counting its open to-dos.
func ForClip(ctx context.Context, a *app.App, assetID string) *models.OpenQuestions {
	if assetID == "" {
		return nil
	}
	n, err := OpenByAsset(ctx, a, []string{assetID})
	if err != nil {
		return nil
	}
	return AboutClip(assetID, n[assetID])
}

func regexQuote(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`\.+*?()|[]{}^$`, r) {
			b.WriteRune('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}
