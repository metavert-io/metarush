package logbook

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/store"
)

// Filter selects log entries. Zero values match everything.
type Filter struct {
	Since          time.Time
	Until          time.Time
	Kinds          []string
	Levels         []string
	Channels       []string
	Op             string
	ProjectID      string
	AssetID        string
	JobID          string
	RenderID       string
	ConversationID string
	InstallationID string
	IDs            []string
	ParentID       string
	// Before continues a listing: entries older than this cursor (from a previous page).
	Before string
	Limit  int
	// Query searches the text people read (summaries, errors, prompts, responses).
	Query string
	// NoPrompts keeps prompt and response text out of the search (the dashboard
	// assistant may not read other conversations).
	NoPrompts bool
	// HideRoutine leaves out successful program runs (the many ffmpeg and
	// ffprobe calls behind every job) and successful requests to online
	// services (one per narration line); failures are always included.
	HideRoutine bool
	// HideInProcess leaves out the ordinary events of jobs an operation ran
	// itself (its own entry already says what was made); problems stay.
	HideInProcess bool
}

// MaxLimit is the largest page a query returns.
const MaxLimit = 1000

func (f Filter) bson(ws string) bson.M {
	m := bson.M{"workspaceId": ws}
	at := bson.M{}
	if !f.Since.IsZero() {
		at["$gte"] = f.Since.UTC()
	}
	if !f.Until.IsZero() {
		at["$lt"] = f.Until.UTC()
	}
	if len(at) > 0 {
		m["at"] = at
	}
	in := func(field string, vs []string) {
		switch len(vs) {
		case 0:
		case 1:
			m[field] = vs[0]
		default:
			m[field] = bson.M{"$in": vs}
		}
	}
	in("kind", f.Kinds)
	in("level", f.Levels)
	in("channel", f.Channels)
	in("_id", f.IDs)
	eq := func(field, v string) {
		if v != "" {
			m[field] = v
		}
	}
	eq("op.name", f.Op)
	eq("refs.projectId", f.ProjectID)
	eq("refs.assetId", f.AssetID)
	eq("refs.jobId", f.JobID)
	eq("refs.renderId", f.RenderID)
	eq("refs.conversationId", f.ConversationID)
	eq("installationId", f.InstallationID)
	eq("parentId", f.ParentID)
	if t, id, ok := parseCursor(f.Before); ok {
		m["$or"] = bson.A{bson.M{"at": bson.M{"$lt": t}}, bson.M{"at": t, "_id": bson.M{"$lt": id}}}
	}
	var nor bson.A
	if f.HideRoutine {
		nor = append(nor, bson.M{"kind": KindTool, "level": LevelInfo}, bson.M{"kind": KindProvider, "level": LevelInfo})
	}
	if f.HideInProcess {
		nor = append(nor, bson.M{"kind": KindJob, "job.inProcess": true, "level": LevelInfo})
	}
	if len(nor) > 0 {
		m["$nor"] = nor
	}
	return m
}

// Cursor continues a listing after e.
func Cursor(e Entry) string { return e.At.UTC().Format(time.RFC3339Nano) + "~" + e.ID }

func parseCursor(c string) (time.Time, string, bool) {
	ts, id, ok := strings.Cut(c, "~")
	if !ok {
		return time.Time{}, "", false
	}
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return time.Time{}, "", false
	}
	return t, id, true
}

func (f Filter) limit() int64 {
	switch {
	case f.Limit <= 0:
		return 100
	case f.Limit > MaxLimit:
		return MaxLimit
	}
	return int64(f.Limit)
}

var newestFirst = bson.D{{Key: "at", Value: -1}, {Key: "_id", Value: -1}}

// List returns entries newest first, and the cursor of the next page ("" at the end).
func List(ctx context.Context, db *store.DB, ws string, f Filter) ([]Entry, string, error) {
	if db == nil {
		return nil, "", errors.New("no database")
	}
	if strings.TrimSpace(f.Query) != "" {
		return search(ctx, db, ws, f)
	}
	lim := f.limit()
	var out []Entry
	cur, err := db.C(Collection).Find(ctx, f.bson(ws), options.Find().SetSort(newestFirst).SetLimit(lim+1))
	if err != nil {
		return nil, "", err
	}
	if err := cur.All(ctx, &out); err != nil {
		return nil, "", err
	}
	return page(out, lim)
}

func page(out []Entry, lim int64) ([]Entry, string, error) {
	if out == nil {
		out = []Entry{}
	}
	next := ""
	if int64(len(out)) > lim {
		out = out[:lim]
		next = Cursor(out[len(out)-1])
	}
	return out, next, nil
}

// search finds entries whose text matches the query: whole words through the
// text index (when prompts may be read), then parts of words in the readable
// fields, newest first.
func search(parent context.Context, db *store.DB, ws string, f Filter) ([]Entry, string, error) {
	ctx, cancel := context.WithTimeout(parent, 6*time.Second) // a search never holds the page for long
	defer cancel()
	lim := f.limit()
	q := strings.TrimSpace(f.Query)
	if len(q) > 200 {
		q = q[:200]
	}
	seen := map[string]bool{}
	var out []Entry
	if !f.NoPrompts {
		m := f.bson(ws)
		if t, _, ok := parseCursor(f.Before); ok { // $text cannot sit next to an $or of unindexed clauses
			delete(m, "$or")
			at, _ := m["at"].(bson.M)
			if at == nil {
				at = bson.M{}
			}
			at["$lt"] = t
			m["at"] = at
		}
		m["$text"] = bson.M{"$search": q}
		var hits []Entry
		cur, err := db.C(Collection).Find(ctx, m, options.Find().SetSort(newestFirst).SetLimit(lim+1))
		if err == nil && cur.All(ctx, &hits) == nil {
			for _, e := range hits {
				seen[e.ID] = true
				out = append(out, e)
			}
		}
	}
	if int64(len(out)) <= lim {
		fields := []string{"error.message", "error.code", "op.name", "tool.program", "tool.stderrTail", "event.action", "job.type", "action.title"}
		if !f.NoPrompts {
			fields = append(fields, "summary", "claude.title", "claude.prompt", "claude.response")
		}
		re := bson.M{"$regex": regexp.QuoteMeta(q), "$options": "i"}
		var or bson.A
		for _, fl := range fields {
			or = append(or, bson.M{fl: re})
		}
		if f.NoPrompts { // a Claude turn's summary quotes its prompt
			or = append(or, bson.M{"summary": re, "kind": bson.M{"$ne": KindClaude}})
		}
		m := f.bson(ws)
		if prev, ok := m["$or"]; ok { // keep the cursor condition
			delete(m, "$or")
			m["$and"] = bson.A{bson.M{"$or": prev}, bson.M{"$or": or}}
		} else {
			m["$or"] = or
		}
		var more []Entry
		cur, err := db.C(Collection).Find(ctx, m, options.Find().SetSort(newestFirst).SetLimit(lim+1))
		if err != nil {
			if len(out) == 0 {
				return nil, "", err
			}
		} else if cur.All(ctx, &more) == nil {
			for _, e := range more {
				if !seen[e.ID] {
					seen[e.ID] = true
					out = append(out, e)
				}
			}
		}
	}
	sortNewest(out)
	return page(out, lim)
}

func sortNewest(es []Entry) {
	for i := 1; i < len(es); i++ {
		for j := i; j > 0 && newer(es[j], es[j-1]); j-- {
			es[j], es[j-1] = es[j-1], es[j]
		}
	}
}

func newer(a, b Entry) bool {
	if !a.At.Equal(b.At) {
		return a.At.After(b.At)
	}
	return a.ID > b.ID
}

// Get loads one entry.
func Get(ctx context.Context, db *store.DB, ws, id string) (*Entry, error) {
	var e Entry
	err := db.C(Collection).FindOne(ctx, bson.M{"_id": id, "workspaceId": ws}).Decode(&e)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &e, nil
}

// PurgeOptions select what Purge deletes.
type PurgeOptions struct {
	Before         time.Time
	InstallationID string // only this machine's entries (retention); "" = everyone's
}

// PurgeResult says what was deleted.
type PurgeResult struct {
	Deleted int64 `json:"deleted"`
	Bodies  int   `json:"bodies" jsonschema:"description=Stored long texts deleted with them."`
}

// Purge deletes entries older than o.Before, and the long texts only they
// referred to (in the media store and in this machine's home).
func Purge(ctx context.Context, l *Logger, o PurgeOptions) (*PurgeResult, error) {
	if l == nil || l.db == nil {
		return nil, errors.New("no database")
	}
	if o.Before.IsZero() {
		return nil, errors.New("purge needs a date")
	}
	coll := l.db.C(Collection)
	filter := bson.M{"workspaceId": l.ws, "at": bson.M{"$lt": o.Before.UTC()}}
	if o.InstallationID != "" {
		filter["installationId"] = o.InstallationID
	}
	res := &PurgeResult{}
	refFields := []string{"claude.promptRef", "claude.responseRef", "tool.stderrRef"}
	for {
		// In batches: find the next entries with their body references, delete them, then the bodies.
		var batch []struct {
			ID     string `bson:"_id"`
			Claude *struct {
				PromptRef   *BodyRef `bson:"promptRef"`
				ResponseRef *BodyRef `bson:"responseRef"`
			} `bson:"claude"`
			Tool *struct {
				StderrRef *BodyRef `bson:"stderrRef"`
			} `bson:"tool"`
		}
		cur, err := coll.Find(ctx, filter, options.Find().SetLimit(1000).SetProjection(bson.M{"claude.promptRef": 1, "claude.responseRef": 1, "tool.stderrRef": 1}))
		if err != nil {
			return res, err
		}
		if err := cur.All(ctx, &batch); err != nil {
			return res, err
		}
		if len(batch) == 0 {
			return res, nil
		}
		idsToDelete := make([]string, 0, len(batch))
		var refs []*BodyRef
		for _, b := range batch {
			idsToDelete = append(idsToDelete, b.ID)
			if b.Claude != nil {
				refs = append(refs, b.Claude.PromptRef, b.Claude.ResponseRef)
			}
			if b.Tool != nil {
				refs = append(refs, b.Tool.StderrRef)
			}
		}
		dr, err := coll.DeleteMany(ctx, bson.M{"workspaceId": l.ws, "_id": bson.M{"$in": idsToDelete}})
		if err != nil {
			return res, err
		}
		res.Deleted += dr.DeletedCount
		done := map[string]bool{}
		for _, r := range refs {
			if r == nil || done[r.Store+r.Key] {
				continue
			}
			done[r.Store+r.Key] = true
			var or bson.A
			for _, fl := range refFields {
				or = append(or, bson.M{fl + ".key": r.Key})
			}
			if n, err := coll.CountDocuments(ctx, bson.M{"workspaceId": l.ws, "$or": or}); err != nil || n > 0 {
				continue // still used by a newer entry
			}
			switch {
			case r.Store == StoreLocal && r.InstallationID == l.inst:
				p := filepath.Join(l.cfg.LogBodiesDir(), filepath.FromSlash(r.Key))
				if !strings.Contains(r.Key, "..") && os.Remove(p) == nil {
					res.Bodies++
					_ = os.Remove(filepath.Dir(p)) // the day folder, when empty
				}
			case r.Store == StoreWorkspace && l.ms != nil && strings.HasPrefix(r.Key, "ws/"+l.ws+"/logs/"):
				if l.ms.Delete(ctx, r.Key) == nil {
					res.Bodies++
				}
			}
		}
		if len(batch) < 1000 {
			return res, nil
		}
	}
}

// ParseWhen reads a time given as a duration back from now ("2h", "30d",
// "90m"), a date ("2026-10-01") or an RFC 3339 time.
func ParseWhen(s string, now time.Time) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, nil
	}
	if strings.HasSuffix(s, "d") || strings.HasSuffix(s, "w") {
		var n int
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &n); err == nil && n >= 0 && fmt.Sprint(n) == s[:len(s)-1] {
			days := n
			if strings.HasSuffix(s, "w") {
				days = 7 * n
			}
			return now.AddDate(0, 0, -days), nil
		}
	}
	if d, err := time.ParseDuration(s); err == nil && d >= 0 {
		return now.Add(-d), nil
	}
	for _, layout := range []string{"2006-01-02", "2006-01-02T15:04", "2006-01-02 15:04", time.RFC3339, time.RFC3339Nano} {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("%q is not a time: use a duration like 2h or 30d, or a date like 2026-10-01", s)
}
