package improve

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/logbook/logops"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// maxScan is the most entries one signals pass reads (newest first).
const maxScan = 20000

// SignalsIn chooses what to analyze.
type SignalsIn struct {
	Since   string          `json:"since,omitempty" jsonschema:"description=How far back (30d by default): a duration or a date."`
	Until   string          `json:"until,omitempty"`
	Entries []logbook.Entry `json:"entries,omitempty" jsonschema:"description=Analyze these entries (an exported log) instead of the workspace's."`
}

// SignalsOut are the signals found, most frequent first.
type SignalsOut struct {
	Since   time.Time `json:"since"`
	Until   time.Time `json:"until"`
	Scanned int       `json:"scanned" jsonschema:"description=Entries read."`
	Signals []Signal  `json:"signals"`
	Notes   []string  `json:"notes" jsonschema:"description=What the pass could not see (for example conversations whose words are not recorded)."`
}

// SaveIn is a report written in the improvement report format.
type SaveIn struct {
	Markdown string `json:"markdown" jsonschema:"minLength=1,maxLength=262144"`
}

// SaveOut is what saving a report did.
type SaveOut struct {
	Report   Report   `json:"report"`
	Items    []Item   `json:"items"`
	Created  int      `json:"created"`
	Updated  int      `json:"updated"`
	Warnings []string `json:"warnings"`
}

// ListIn filters improvement items.
type ListIn struct {
	State string `json:"state,omitempty" jsonschema:"enum=,enum=new,enum=kept,enum=dismissed,enum=all,description=Default: new and kept."`
	Limit int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=500"`
}

// ReportSummary is a saved report without its text.
type ReportSummary struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	ItemCount int       `json:"itemCount"`
	CreatedAt time.Time `json:"createdAt"`
	By        string    `json:"by,omitempty"`
}

// ListOut are the items (best first) and the reports they came from.
type ListOut struct {
	Items   []Item          `json:"items"`
	Reports []ReportSummary `json:"reports"`
	Counts  StateCounts     `json:"counts"`
}

// StateCounts count items per state.
type StateCounts struct {
	New       int `json:"new"`
	Kept      int `json:"kept"`
	Dismissed int `json:"dismissed"`
}

// ShowIn names an item (imp_…) or a report (imr_…).
type ShowIn struct {
	ID string `json:"id"`
}

// ShowOut is an item with its evidence, or a report with its items.
type ShowOut struct {
	Item     *Item           `json:"item,omitempty"`
	Report   *Report         `json:"report,omitempty"`
	Evidence []logops.LogRow `json:"evidence" jsonschema:"description=The log entries the item points at."`
	Items    []Item          `json:"items" jsonschema:"description=A report's items."`
}

// DecideIn keeps or dismisses items.
type DecideIn struct {
	IDs []string `json:"ids" jsonschema:"minItems=1"`
}

// DecideOut are the items after the decision.
type DecideOut struct {
	Items   []Item `json:"items"`
	Changed int    `json:"changed"`
}

// AddIn is one improvement idea from a conversation (D166): something
// MetaRush couldn't do that the person needed.
type AddIn struct {
	Title       string `json:"title" jsonschema:"minLength=3,maxLength=160,description=What MetaRush couldn't do\\, in a few words (Steady shaky footage)."`
	Description string `json:"description,omitempty" jsonschema:"maxLength=4000,description=What the person needed it for."`
	Kind        string `json:"kind,omitempty" jsonschema:"enum=,enum=feature-gap,enum=friction,enum=bug,description=Default feature-gap."`
	Impact      string `json:"impact,omitempty" jsonschema:"enum=,enum=high,enum=medium,enum=low,description=Default medium."`
	From        string `json:"from,omitempty" jsonschema:"maxLength=200,description=Where it came up (a conversation)."`
}

// AddOut is the idea as kept (an idea already on the list is seen once more).
type AddOut struct {
	Item    Item   `json:"item"`
	Created bool   `json:"created"`
	Summary string `json:"summary"`
}

// Add puts one idea on the improvement list: the same title again is the same
// idea, seen once more (its state stays what a person decided).
func Add(ctx context.Context, a *app.App, in AddIn) (*AddOut, error) {
	title := strings.TrimSpace(in.Title)
	if len(title) < 3 {
		return nil, apperr.New(apperr.InvalidInput, "Say what MetaRush couldn't do, in a few words.")
	}
	kind, impact := in.Kind, in.Impact
	if kind == "" {
		kind = "feature-gap"
	}
	if impact == "" {
		impact = ImpactMedium
	}
	desc := a.Log.Redactor().String(strings.TrimSpace(in.Description))
	if in.From != "" {
		desc = strings.TrimSpace(desc + "\n\nCame up in " + in.From + ".")
	}
	now := time.Now().UTC()
	key := Slug(title)
	coll := a.DB.C(store.CImprovementItems)
	var cur Item
	err := coll.FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": key}).Decode(&cur)
	switch {
	case errors.Is(err, mongo.ErrNoDocuments):
		it := Item{ID: ids.New(ItemPrefix), WorkspaceID: a.WorkspaceID(), Key: key, Title: title, Kind: kind, Impact: impact, Frequency: 1,
			Score: Score(1, impact), Description: desc, Evidence: []string{}, State: StateNew, ReportIDs: []string{}, Seen: 1, CreatedAt: now, UpdatedAt: now, LastSeenAt: now}
		if _, err := coll.InsertOne(ctx, it); err != nil {
			return nil, err
		}
		a.Audit(ctx, "improve.add", "improvement_item", it.ID, "", map[string]any{"key": key})
		return &AddOut{Item: it, Created: true, Summary: "Added “" + title + "” to MetaRush's improvement list."}, nil
	case err != nil:
		return nil, err
	}
	if _, err := coll.UpdateOne(ctx, bson.M{"_id": cur.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$set": bson.M{"updatedAt": now, "lastSeenAt": now}, "$inc": bson.M{"seen": 1, "frequency": 1}}); err != nil {
		return nil, err
	}
	_ = coll.FindOne(ctx, bson.M{"_id": cur.ID}).Decode(&cur)
	return &AddOut{Item: cur, Summary: "“" + cur.Title + "” is already on MetaRush's improvement list; it came up once more."}, nil
}

// ExportIn chooses what to export.
type ExportIn struct {
	State string `json:"state,omitempty" jsonschema:"enum=,enum=new,enum=kept,enum=dismissed,enum=all,description=Default: kept and new."`
}

// ExportOut is a Markdown document of improvement items.
type ExportOut struct {
	Markdown string `json:"markdown"`
	Filename string `json:"filename"`
	Count    int    `json:"count"`
}

func init() {
	ops.Def("improve.signals", "Find signals in the activity log: errors, retries, refused or ignored changes, corrections, Claude saying MetaRush cannot do something, long tool chains, slow operations", ops.Flags{},
		func(ctx context.Context, a *app.App, in *SignalsIn) (*SignalsOut, error) {
			if a.Actor.Via == models.ViaAssistant {
				return nil, apperr.New(apperr.Unauthorized, "Claude in the dashboard can't read other conversations, so it can't look for improvement ideas.").
					WithHint("Ask Claude Code in a terminal for an improvement report instead.").WithDetails(map[string]any{"reason": "assistant_conversations"}).
					WithCommand("metarush improve signals")
			}
			return Signals(ctx, a, in)
		})
	ops.Def("improve.save", "Save an improvement report (Markdown: one ## section per item with kind, impact, frequency and evidence lines) as improvement items", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SaveIn) (*SaveOut, error) {
			return Save(ctx, a, in.Markdown)
		})
	ops.Def("improve.list", "List improvement ideas found in the log (best first) and the reports they came from", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ListIn) (*ListOut, error) {
			return list(ctx, a, in)
		})
	ops.Def("improve.show", "Show an improvement idea with its evidence, or a saved report", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ShowIn) (*ShowOut, error) {
			return show(ctx, a, in.ID)
		})
	ops.Def("improve.add", "Put one idea on MetaRush's improvement list: something it couldn't do that the person needed (the same title again is the same idea)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AddIn) (*AddOut, error) {
			return Add(ctx, a, *in)
		})
	ops.Def("improve.keep", "Keep improvement ideas (worth building)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *DecideIn) (*DecideOut, error) {
			return decide(ctx, a, in.IDs, StateKept)
		})
	ops.Def("improve.dismiss", "Dismiss improvement ideas (not worth building); a later report finding them again leaves them dismissed", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *DecideIn) (*DecideOut, error) {
			return decide(ctx, a, in.IDs, StateDismissed)
		})
	ops.Def("improve.export", "Export improvement ideas as a Markdown document (kept and new by default)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ExportIn) (*ExportOut, error) {
			l, err := list(ctx, a, &ListIn{State: in.State, Limit: 500})
			if err != nil {
				return nil, err
			}
			now := time.Now().UTC()
			return &ExportOut{Markdown: Markdown("MetaRush improvement ideas", l.Items, now), Filename: "metarush-improvements-" + now.Format("2006-01-02") + ".md", Count: len(l.Items)}, nil
		})
}

// Signals runs the deterministic pass over the workspace's log (or given entries).
func Signals(ctx context.Context, a *app.App, in *SignalsIn) (*SignalsOut, error) {
	now := time.Now().UTC()
	since := in.Since
	if since == "" {
		since = "30d"
	}
	from, err := logbook.ParseWhen(since, now)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	until := now
	if in.Until != "" {
		if until, err = logbook.ParseWhen(in.Until, now); err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
		}
	}
	out := &SignalsOut{Since: from, Until: until, Notes: []string{}}
	entries := in.Entries
	if entries == nil {
		f := logbook.Filter{Since: from, Until: until, Kinds: []string{logbook.KindOp, logbook.KindJob, logbook.KindTool, logbook.KindProvider, logbook.KindClaude, logbook.KindAction}, Limit: logbook.MaxLimit}
		for len(entries) < maxScan {
			page, next, err := logbook.List(ctx, a.DB, a.WorkspaceID(), f)
			if err != nil {
				return nil, err
			}
			entries = append(entries, page...)
			if next == "" {
				break
			}
			f.Before = next
		}
		if len(entries) >= maxScan {
			out.Notes = append(out.Notes, fmt.Sprintf("Only the newest %d entries were read; use a shorter period to see everything.", maxScan))
		}
	} else {
		var kept []logbook.Entry // an export may hold any period: keep the asked one
		for _, e := range entries {
			if (in.Since == "" || !e.At.Before(from)) && !e.At.After(until) {
				kept = append(kept, e)
			}
		}
		entries = kept
		if in.Since == "" && len(entries) > 0 {
			out.Since = entries[len(entries)-1].At
		}
	}
	out.Scanned = len(entries)
	red := a.Log.Redactor()
	reads := 0
	texts := func(e *logbook.Entry) (string, string, bool) {
		c := e.Claude
		p, r := c.Prompt, c.Response
		local := false
		for _, ref := range []*logbook.BodyRef{c.PromptRef, c.ResponseRef} {
			if ref != nil && ref.Store == logbook.StoreLocal && ref.InstallationID != a.Cfg.Installation.ID {
				local = true
			}
		}
		if c.Privacy == logbook.PrivacyOff || local {
			return "", "", false
		}
		if c.PromptRef != nil && p == "" && reads < 300 {
			reads++
			p, _ = a.Log.ReadBody(ctx, c.PromptRef)
		}
		if c.ResponseRef != nil && r == "" && reads < 300 {
			reads++
			r, _ = a.Log.ReadBody(ctx, c.ResponseRef)
		}
		return p, r, p != "" || r != ""
	}
	sigs, unreadable := Extract(entries, texts, red.String, now)
	out.Signals = sigs
	if unreadable > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%d Claude turns could not be read (their words are not recorded or are kept on another machine), so corrections and missing features in them are not counted.", unreadable))
	}
	return out, nil
}

// Save parses a report and stores it with its items.
func Save(ctx context.Context, a *app.App, md string) (*SaveOut, error) {
	md = strings.TrimSpace(md)
	if md == "" {
		return nil, apperr.New(apperr.InvalidInput, "the report is empty")
	}
	if len(md) > 256<<10 {
		return nil, apperr.New(apperr.InvalidInput, "the report is longer than 256 KB")
	}
	md = a.Log.Redactor().String(md)
	pr, err := ParseReport(md)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
	}
	// Stamped by the database server's clock (D360): the weekly schedule on
	// another computer asks whether a report was made since its slot.
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	rep := Report{ID: ids.New(ReportPrefix), WorkspaceID: a.WorkspaceID(), Title: firstNonEmpty(pr.Title, "Improvement report "+now.Format("2006-01-02")),
		Markdown: md, ItemIDs: []string{}, Warnings: pr.Warnings, CreatedAt: now, CreatedBy: a.Actor}
	out := &SaveOut{Items: []Item{}, Warnings: append([]string{}, pr.Warnings...)}
	// Evidence must point at real entries of this workspace.
	var all []string
	for _, it := range pr.Items {
		all = append(all, it.Evidence...)
	}
	known := map[string]bool{}
	if len(all) > 0 {
		cur, err := a.DB.C(store.CLogEntries).Find(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": all}}, options.Find().SetProjection(bson.M{"_id": 1}))
		if err == nil {
			var docs []struct {
				ID string `bson:"_id"`
			}
			if cur.All(ctx, &docs) == nil {
				for _, d := range docs {
					known[d.ID] = true
				}
			}
		}
	}
	coll := a.DB.C(store.CImprovementItems)
	for _, p := range pr.Items {
		var ev []string
		for _, id := range p.Evidence {
			if known[id] {
				ev = append(ev, id)
			} else {
				out.Warnings = append(out.Warnings, fmt.Sprintf("%q: %s is not in this workspace's log", p.Title, id))
			}
		}
		if ev == nil {
			ev = []string{}
		}
		var cur Item
		err := coll.FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "key": p.Key}).Decode(&cur)
		switch {
		case errors.Is(err, mongo.ErrNoDocuments):
			it := Item{ID: ids.New(ItemPrefix), WorkspaceID: a.WorkspaceID(), Key: p.Key, Title: p.Title, Kind: p.Kind, Impact: p.Impact, Frequency: p.Frequency,
				Score: Score(p.Frequency, p.Impact), Description: p.Description, Evidence: ev, State: StateNew, ReportIDs: []string{rep.ID}, Seen: 1,
				CreatedAt: now, UpdatedAt: now, LastSeenAt: now}
			if _, err := coll.InsertOne(ctx, it); err != nil {
				return nil, err
			}
			out.Items = append(out.Items, it)
			out.Created++
		case err != nil:
			return nil, err
		default:
			merged := mergeEvidence(cur.Evidence, ev)
			set := bson.M{"title": p.Title, "kind": p.Kind, "impact": p.Impact, "frequency": p.Frequency, "score": Score(p.Frequency, p.Impact),
				"description": p.Description, "evidence": merged, "updatedAt": now, "lastSeenAt": now}
			if _, err := coll.UpdateOne(ctx, bson.M{"_id": cur.ID, "workspaceId": a.WorkspaceID()},
				bson.M{"$set": set, "$inc": bson.M{"seen": 1}, "$addToSet": bson.M{"reportIds": rep.ID}}); err != nil {
				return nil, err
			}
			_ = coll.FindOne(ctx, bson.M{"_id": cur.ID}).Decode(&cur)
			out.Items = append(out.Items, cur)
			out.Updated++
		}
		rep.ItemIDs = append(rep.ItemIDs, out.Items[len(out.Items)-1].ID)
	}
	rep.ItemCount = len(rep.ItemIDs)
	if _, err := a.DB.C(store.CImprovementReports).InsertOne(ctx, rep); err != nil {
		return nil, err
	}
	a.Audit(ctx, "improve.save", "improvement_report", rep.ID, "", map[string]any{"items": rep.ItemCount, "created": out.Created, "updated": out.Updated})
	out.Report = rep
	return out, nil
}

func mergeEvidence(a, b []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, x := range append(append([]string{}, b...), a...) { // newest first
		if !seen[x] && len(out) < 50 {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func list(ctx context.Context, a *app.App, in *ListIn) (*ListOut, error) {
	filter := bson.M{}
	switch in.State {
	case "", "default":
		filter["state"] = bson.M{"$in": bson.A{StateNew, StateKept}}
	case "all":
	case StateNew, StateKept, StateDismissed:
		filter["state"] = in.State
	default:
		return nil, apperr.New(apperr.InvalidInput, "state must be new, kept, dismissed or all")
	}
	lim := int64(in.Limit)
	if lim <= 0 || lim > 500 {
		lim = 200
	}
	out := &ListOut{Items: []Item{}, Reports: []ReportSummary{}}
	if err := a.DB.Find(ctx, store.CImprovementItems, a.WorkspaceID(), filter, &out.Items,
		options.Find().SetSort(bson.D{{Key: "score", Value: -1}, {Key: "lastSeenAt", Value: -1}}).SetLimit(lim)); err != nil {
		return nil, err
	}
	if out.Items == nil {
		out.Items = []Item{}
	}
	var reps []Report
	if err := a.DB.Find(ctx, store.CImprovementReports, a.WorkspaceID(), nil, &reps,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(50).SetProjection(bson.M{"markdown": 0})); err != nil {
		return nil, err
	}
	for _, r := range reps {
		out.Reports = append(out.Reports, ReportSummary{ID: r.ID, Title: r.Title, ItemCount: r.ItemCount, CreatedAt: r.CreatedAt, By: r.CreatedBy.UserName})
	}
	var counts []struct {
		ID string `bson:"_id"`
		N  int    `bson:"n"`
	}
	if cur, err := a.DB.C(store.CImprovementItems).Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: bson.M{"workspaceId": a.WorkspaceID()}}},
		{{Key: "$group", Value: bson.M{"_id": "$state", "n": bson.M{"$sum": 1}}}},
	}); err == nil && cur.All(ctx, &counts) == nil {
		for _, c := range counts {
			switch c.ID {
			case StateNew:
				out.Counts.New = c.N
			case StateKept:
				out.Counts.Kept = c.N
			case StateDismissed:
				out.Counts.Dismissed = c.N
			}
		}
	}
	return out, nil
}

func show(ctx context.Context, a *app.App, id string) (*ShowOut, error) {
	out := &ShowOut{Evidence: []logops.LogRow{}, Items: []Item{}}
	switch ids.Prefix(id) {
	case ItemPrefix:
		var it Item
		if err := a.DB.GetByID(ctx, store.CImprovementItems, a.WorkspaceID(), id, &it); err != nil {
			return nil, apperr.New(apperr.NotFound, "That improvement idea wasn't found.").WithDetails(map[string]any{"id": id})
		}
		out.Item = &it
		if len(it.Evidence) > 0 {
			es, _, err := logbook.List(ctx, a.DB, a.WorkspaceID(), logbook.Filter{IDs: it.Evidence, Limit: 50})
			if err == nil {
				out.Evidence = logops.Rows(ctx, a, es)
			}
		}
	case ReportPrefix:
		var r Report
		if err := a.DB.GetByID(ctx, store.CImprovementReports, a.WorkspaceID(), id, &r); err != nil {
			return nil, apperr.New(apperr.NotFound, "That improvement report wasn't found.").WithDetails(map[string]any{"id": id})
		}
		out.Report = &r
		if len(r.ItemIDs) > 0 {
			_ = a.DB.Find(ctx, store.CImprovementItems, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": r.ItemIDs}}, &out.Items)
			sort.Slice(out.Items, func(i, j int) bool { return out.Items[i].Score > out.Items[j].Score })
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "%q is not an improvement idea or report", id)
	}
	if out.Items == nil {
		out.Items = []Item{}
	}
	return out, nil
}

func decide(ctx context.Context, a *app.App, idsIn []string, state string) (*DecideOut, error) {
	if len(idsIn) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "say which ideas")
	}
	now := time.Now().UTC()
	res, err := a.DB.C(store.CImprovementItems).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": idsIn}, "state": bson.M{"$ne": state}},
		bson.M{"$set": bson.M{"state": state, "decidedBy": a.Actor.UserName, "decidedAt": now, "updatedAt": now}})
	if err != nil {
		return nil, err
	}
	out := &DecideOut{Items: []Item{}, Changed: int(res.ModifiedCount)}
	if err := a.DB.Find(ctx, store.CImprovementItems, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsIn}}, &out.Items); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, apperr.New(apperr.NotFound, "no such improvement ideas")
	}
	a.Audit(ctx, "improve."+map[string]string{StateKept: "keep", StateDismissed: "dismiss"}[state], "improvement_item", strings.Join(idsIn, ","), "",
		map[string]any{"changed": out.Changed})
	return out, nil
}
