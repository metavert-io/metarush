package store

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
)

// CContentSettings holds projects' own Content Settings (D192): the brand,
// narration voice choices and house rules (the spelling list is the project's
// spelling_entries). One per project, made when a project gets its own.
const CContentSettings = "content_settings"

// CContentDefaults holds, per workspace (keyed by it), the project whose
// Content Settings new projects use (its "Use for new projects").
const CContentDefaults = "content_defaults"

// voiceFields are the narration voice choices in provider settings (D193):
// they move into Content Settings; budgets, credits used and limits stay.
var voiceFields = []string{"voiceId", "voiceName", "modelId", "modelName", "voice", "region", "favorites"}

// brandFields are the parts of a brand kit that make it more than empty.
var brandFields = []string{"logo", "logoDark", "fontResource", "font", "primary", "accent", "text", "background"}

// migrateContentSettings adds Content Settings (D192, D193) and moves today's
// data into them, so nothing anyone sees changes: the workspace's brand kit,
// spelling list and voice choices become the Content Settings of the most
// recently active project that used them, which becomes the default; projects
// with a kit, spellings or voice choices of their own get their own settings
// holding exactly what they used (their kit or the workspace's, the workspace's
// spellings and theirs, the workspace's voice with theirs over it); every
// other project follows the default. Project rules stay project rules.
func migrateContentSettings(ctx context.Context, d *DB) error {
	if err := createIndexes(ctx, d, CContentSettings,
		idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "projectId", Value: 1}}, options.Index().SetUnique(true))); err != nil {
		return fmt.Errorf("%s indexes: %w", CContentSettings, err)
	}
	if err := createIndexes(ctx, d, CProjects, idx(bson.D{{Key: "workspaceId", Value: 1}, {Key: "content.project", Value: 1}},
		options.Index().SetPartialFilterExpression(bson.M{"content.project": bson.M{"$exists": true}}))); err != nil {
		return fmt.Errorf("%s indexes: %w", CProjects, err)
	}
	cur, err := d.C(CWorkspaces).Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"_id": 1}))
	if err != nil {
		return err
	}
	var wss []struct {
		ID string `bson:"_id"`
	}
	if err := cur.All(ctx, &wss); err != nil {
		return err
	}
	for _, w := range wss {
		if _, err := MoveContentSettings(ctx, d, w.ID); err != nil {
			return fmt.Errorf("workspace %s: %w", w.ID, err)
		}
	}
	return nil
}

type mvWorkspace struct {
	ID        string            `bson:"_id"`
	BrandKit  bson.M            `bson:"brandKit,omitempty"`
	Providers map[string]bson.M `bson:"providers,omitempty"`
}

// ContentDefault is the project whose Content Settings new projects use ("" none).
func ContentDefault(ctx context.Context, d *DB, workspaceID string) string {
	var doc struct {
		ProjectID string `bson:"projectId"`
	}
	if err := d.C(CContentDefaults).FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&doc); err != nil {
		return ""
	}
	return doc.ProjectID
}

type mvProject struct {
	ID        string    `bson:"_id"`
	Archived  bool      `bson:"archived"`
	CreatedAt time.Time `bson:"createdAt"`
	UpdatedAt time.Time `bson:"updatedAt"`
	Branches  map[string]struct {
		UpdatedAt time.Time `bson:"updatedAt"`
	} `bson:"branches"`
	BrandKit  bson.M            `bson:"brandKit,omitempty"`
	Providers map[string]bson.M `bson:"providers,omitempty"`
	Content   *struct {
		Uses string `bson:"uses"`
	} `bson:"content,omitempty"`
}

// activity is when a project was last worked on: its newest change or version.
func (p mvProject) activity() time.Time {
	t := p.UpdatedAt
	if p.CreatedAt.After(t) {
		t = p.CreatedAt
	}
	for _, b := range p.Branches {
		if b.UpdatedAt.After(t) {
			t = b.UpdatedAt
		}
	}
	return t
}

func kitSet(k bson.M) bool {
	for _, f := range brandFields {
		if s, _ := k[f].(string); s != "" {
			return true
		}
	}
	return false
}

// voiceOf is a provider entry's voice choices (nil: none).
func voiceOf(ps bson.M) bson.M {
	out := bson.M{}
	for _, f := range voiceFields {
		v, ok := ps[f]
		if !ok || v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if x == "" {
				continue
			}
		case bson.A:
			if len(x) == 0 {
				continue
			}
		case bson.M:
			if len(x) == 0 {
				continue
			}
		case bson.D:
			if len(x) == 0 {
				continue
			}
		}
		out[f] = v
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func voicesOf(providers map[string]bson.M) map[string]bson.M {
	out := map[string]bson.M{}
	for name, ps := range providers {
		if v := voiceOf(ps); v != nil {
			out[name] = v
		}
	}
	return out
}

// asM reads a sub-document as a map.
func asM(v any) bson.M {
	switch x := v.(type) {
	case bson.M:
		return x
	case bson.D:
		m := bson.M{}
		for _, e := range x {
			m[e.Key] = e.Value
		}
		return m
	case map[string]any:
		return bson.M(x)
	}
	return nil
}

// overlayVoice is what a project's voice was: the workspace's choices with the
// project's over them, the way voices resolved before (voice and model each
// with its name, every voice setting on its own; region and favourites the
// workspace's).
func overlayVoice(ws, proj bson.M) bson.M {
	out := bson.M{}
	for k, v := range ws {
		out[k] = v
	}
	if proj == nil {
		return out
	}
	if s, _ := proj["voiceId"].(string); s != "" {
		out["voiceId"], out["voiceName"] = s, proj["voiceName"]
	}
	if s, _ := proj["modelId"].(string); s != "" {
		out["modelId"], out["modelName"] = s, proj["modelName"]
	}
	if pv := asM(proj["voice"]); len(pv) > 0 {
		merged := bson.M{}
		for k, v := range asM(out["voice"]) {
			merged[k] = v
		}
		for k, v := range pv {
			if v != nil && v != "" {
				merged[k] = v
			}
		}
		out["voice"] = merged
	}
	for k, v := range out {
		if v == nil {
			delete(out, k)
		}
	}
	return out
}

// MoveContentSettings gives a workspace Content Settings from what its
// projects used before them (migration 36, and for the first project of a
// workspace that had none). Every step can run again: a project that has its
// own settings keeps them, the default is chosen once, and the old fields are
// cleared only after their values are in the new place. It returns the
// default project ("" when the workspace has no project yet).
func MoveContentSettings(ctx context.Context, d *DB, workspaceID string) (string, error) {
	w := mvWorkspace{ID: workspaceID}
	hasWS := true
	if err := d.C(CWorkspaces).FindOne(ctx, bson.M{"_id": workspaceID}).Decode(&w); err != nil {
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return "", err
		}
		hasWS = false // no workspace record (tests): nothing kept there before
	}
	var projects []mvProject
	cur, err := d.C(CProjects).Find(ctx, bson.M{"workspaceId": workspaceID},
		options.Find().SetProjection(bson.M{"archived": 1, "createdAt": 1, "updatedAt": 1, "branches": 1, "brandKit": 1, "providers": 1, "content": 1}))
	if err != nil {
		return "", err
	}
	if err := cur.All(ctx, &projects); err != nil {
		return "", err
	}
	if len(projects) == 0 {
		return "", nil // the first project takes the workspace's settings when it is made
	}
	byID := map[string]*mvProject{}
	for i := range projects {
		byID[projects[i].ID] = &projects[i]
	}
	// Projects that have their own settings already.
	hasRec := map[string]bool{}
	{
		c, err := d.C(CContentSettings).Find(ctx, bson.M{"workspaceId": workspaceID}, options.Find().SetProjection(bson.M{"projectId": 1}))
		if err != nil {
			return "", err
		}
		var recs []struct {
			ProjectID string `bson:"projectId"`
		}
		if err := c.All(ctx, &recs); err != nil {
			return "", err
		}
		for _, r := range recs {
			hasRec[r.ProjectID] = true
		}
	}
	// Spelling entries: the workspace's list, and how many each project has.
	type entry struct {
		ID        string `bson:"_id"`
		ProjectID string `bson:"projectId,omitempty"`
		Key       string `bson:"key"`
	}
	var entries []entry
	{
		c, err := d.C(CSpelling).Find(ctx, bson.M{"workspaceId": workspaceID}, options.Find().SetProjection(bson.M{"projectId": 1, "key": 1}))
		if err != nil {
			return "", err
		}
		if err := c.All(ctx, &entries); err != nil {
			return "", err
		}
	}
	var wsEntries []string
	keysOf := map[string]map[string]bool{}
	for _, e := range entries {
		if e.ProjectID == "" {
			wsEntries = append(wsEntries, e.ID)
			continue
		}
		if keysOf[e.ProjectID] == nil {
			keysOf[e.ProjectID] = map[string]bool{}
		}
		keysOf[e.ProjectID][e.Key] = true
	}
	legacyOwn := func(p *mvProject) bool {
		return kitSet(p.BrandKit) || len(keysOf[p.ID]) > 0 || len(voicesOf(p.Providers)) > 0
	}
	own := func(p *mvProject) bool {
		return hasRec[p.ID] || (p.Content != nil && p.Content.Uses == "own") || legacyOwn(p)
	}

	// The default: chosen once — the most recently active project that used
	// the workspace's settings (open projects first), else the most recently
	// active project of all.
	def := ""
	if cur := ContentDefault(ctx, d, workspaceID); cur != "" && byID[cur] != nil {
		def = cur
	} else {
		order := make([]*mvProject, 0, len(projects))
		for i := range projects {
			order = append(order, &projects[i])
		}
		sort.SliceStable(order, func(i, j int) bool {
			a, b := order[i], order[j]
			if oa, ob := own(a), own(b); oa != ob {
				return !oa
			}
			if a.Archived != b.Archived {
				return !a.Archived
			}
			if ta, tb := a.activity(), b.activity(); !ta.Equal(tb) {
				return ta.After(tb)
			}
			return a.ID < b.ID
		})
		def = order[0].ID
		// Set once (a default that is gone is replaced); another machine
		// choosing at the same moment wins, and its choice counts.
		prev := ContentDefault(ctx, d, workspaceID)
		filter := bson.M{"_id": workspaceID}
		if prev != "" {
			filter["projectId"] = prev
		}
		if _, err := d.C(CContentDefaults).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"workspaceId": workspaceID, "projectId": def, "setAt": time.Now().UTC()}},
			options.UpdateOne().SetUpsert(true)); err != nil && !IsDuplicateKey(err) {
			return "", err
		}
		if again := ContentDefault(ctx, d, workspaceID); again != "" {
			def = again
		}
	}

	wsVoice := voicesOf(w.Providers)
	now := time.Now().UTC()
	migrator := bson.M{"userId": "", "installationId": "", "via": "migration"}
	// Settings of their own for the default and every project that had a kit,
	// spellings or voice choices of its own: exactly what each used.
	for i := range projects {
		p := &projects[i]
		if hasRec[p.ID] || (p.ID != def && !legacyOwn(p)) {
			continue
		}
		// The workspace's spellings: copied first into a project with a list
		// of its own (a record is never there without them), moved to the
		// default below.
		if p.ID != def && len(wsEntries) > 0 {
			if err := copySpellings(ctx, d, workspaceID, p.ID, keysOf[p.ID]); err != nil {
				return "", err
			}
		}
		rec := bson.M{"_id": ids.New(ids.ContentSettings), "workspaceId": workspaceID, "projectId": p.ID, "createdAt": now, "createdBy": migrator, "updatedAt": now}
		switch {
		case kitSet(p.BrandKit):
			rec["brandKit"] = p.BrandKit
		case kitSet(w.BrandKit):
			rec["brandKit"] = w.BrandKit
		}
		pv := voicesOf(p.Providers)
		voice := bson.M{}
		for name, v := range wsVoice {
			voice[name] = overlayVoice(v, pv[name])
		}
		for name, v := range pv {
			if _, ok := voice[name]; !ok {
				voice[name] = overlayVoice(nil, v)
			}
		}
		if len(voice) > 0 {
			rec["voice"] = voice
		}
		if _, err := d.C(CContentSettings).UpdateOne(ctx, bson.M{"workspaceId": workspaceID, "projectId": p.ID}, bson.M{"$setOnInsert": rec},
			options.UpdateOne().SetUpsert(true)); err != nil && !IsDuplicateKey(err) {
			return "", err
		}
		hasRec[p.ID] = true
	}
	// The default holds the workspace's kit when it had none of its own.
	wsKitUsed := hasRec[def] && byID[def] != nil && !kitSet(byID[def].BrandKit)
	// The workspace's spelling list becomes the default's.
	for _, id := range wsEntries {
		if _, err := d.C(CSpelling).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": workspaceID, "projectId": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"projectId": def}}); err != nil && !IsDuplicateKey(err) {
			return "", err // a duplicate: the default has that word already (the entry stays, unread)
		}
	}
	// Which settings each project uses: its own, or the default (nothing to write).
	for id := range hasRec {
		if _, err := d.C(CProjects).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": workspaceID, "content": bson.M{"$exists": false}},
			bson.M{"$set": bson.M{"content": bson.M{"uses": "own", "since": now}}}); err != nil {
			return "", err
		}
	}
	// Last, the old places: their values are in the new ones now.
	for i := range projects {
		p := &projects[i]
		if !hasRec[p.ID] {
			continue
		}
		unset := bson.M{}
		if p.BrandKit != nil {
			unset["brandKit"] = ""
		}
		for name := range voicesOf(p.Providers) {
			for _, f := range voiceFields {
				unset["providers."+name+"."+f] = ""
			}
		}
		if len(unset) > 0 {
			if _, err := d.C(CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": workspaceID}, bson.M{"$unset": unset}); err != nil {
				return "", err
			}
		}
	}
	unset := bson.M{}
	if w.BrandKit != nil && (wsKitUsed || !kitSet(w.BrandKit)) {
		unset["brandKit"] = ""
	}
	if hasRec[def] {
		for name := range wsVoice {
			for _, f := range voiceFields {
				unset["providers."+name+"."+f] = ""
			}
		}
	}
	if len(unset) > 0 && hasWS {
		if _, err := d.C(CWorkspaces).UpdateOne(ctx, bson.M{"_id": workspaceID}, bson.M{"$unset": unset}); err != nil {
			return "", err
		}
	}
	return def, nil
}

// copySpellings copies the workspace's spelling list into a project's (the
// words it has already stay as they are).
func copySpellings(ctx context.Context, d *DB, workspaceID, projectID string, have map[string]bool) error {
	cur, err := d.C(CSpelling).Find(ctx, bson.M{"workspaceId": workspaceID, "projectId": bson.M{"$exists": false}})
	if err != nil {
		return err
	}
	var list []bson.M
	if err := cur.All(ctx, &list); err != nil {
		return err
	}
	for _, e := range list {
		key, _ := e["key"].(string)
		if have[key] {
			continue
		}
		e["_id"] = ids.New(ids.Spelling)
		e["projectId"] = projectID
		if _, err := d.C(CSpelling).InsertOne(ctx, e); err != nil && !IsDuplicateKey(err) {
			return err
		}
	}
	return nil
}
