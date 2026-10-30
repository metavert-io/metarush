package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/playback"
	"github.com/metavert-io/metarush/internal/store"
)

// Event tells dashboards what changed so they can refetch. Payloads are IDs only.
type Event struct {
	Type      string `json:"type" jsonschema:"description=Collection name (such as jobs or edit_revisions or notifications) or a local event type (upload or assistant or settings)."`
	Op        string `json:"op,omitempty"`
	ID        string `json:"id,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
}

// EventsHello is the first SSE event of /api/events.
type EventsHello struct {
	Mode string `json:"mode" jsonschema:"description=changestream or polling (empty while connecting)."`
}

// Broker fans shared-state changes out to browser tabs via Server-Sent Events.
// It uses MongoDB change streams (so an edit made from another machine shows up
// here immediately) and falls back to polling if change streams are unavailable.
type Broker struct {
	a    *app.App
	mu   sync.Mutex
	subs map[chan Event]bool
	Mode string // changestream | polling
	// told is what polling last told of each record (its stamps), read by the
	// one goroutine that polls.
	told map[string]string
}

// NewBroker creates a broker.
func NewBroker(a *app.App) *Broker { return &Broker{a: a, subs: map[chan Event]bool{}} }

var watched = []string{store.CJobs, store.CAssets, store.CRevisions, store.CRenders, store.CProjects, store.CComments, store.CWorkers, store.CIngest,
	store.CNotifications, store.CQAReports, store.CTodos, store.CSpeakers, store.CPeople, store.CResources, store.CLinkedFolders, store.CProjectLinks,
	store.CCollections, store.CGenerations, store.CLooks, store.CConsents, store.CRemotionSources, store.CRemotionVersions, store.CRemotionRenders, store.CSpelling, store.CContentSettings, store.CContentDefaults,
	store.CRecipes, store.CRecipeVersions, store.CRecipeRuns, store.CDrafts, store.CPresenters, store.CReviewLinks}

// leaseOnly reports whether an update only took or gave back a run's lease
// (store.CRecipeRuns: the engine's every look; store.CReviewLinks: a look
// at a link's comment slots, D315): nothing anyone sees changed.
func leaseOnly(updated bson.M, removed []string) bool {
	if len(updated) == 0 && len(removed) == 0 {
		return false
	}
	lease := func(k string) bool { return k == "lease" || strings.HasPrefix(k, "lease.") || k == "syncUntil" }
	for k := range updated {
		if !lease(k) {
			return false
		}
	}
	for _, k := range removed {
		if !lease(k) {
			return false
		}
	}
	return true
}

// Local publishes an in-process event.
func (b *Broker) Local(e Event) { b.publish(e) }

func (b *Broker) publish(e Event) {
	if e.Type == store.CAssets || e.Type == store.CRenders {
		// What the media endpoints remember follows the database (D180).
		playback.Changed(b.a, e.Type, e.ID)
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // slow client: drop; it will refetch on the next event
		}
	}
}

// Run watches the database until ctx ends.
func (b *Broker) Run(ctx context.Context) {
	for ctx.Err() == nil {
		err := b.changeStream(ctx)
		if ctx.Err() != nil {
			return
		}
		b.Mode = "polling"
		_ = err
		b.poll(ctx, 30*time.Second) // poll for a while, then try change streams again
	}
}

func (b *Broker) changeStream(ctx context.Context) error {
	collIn := bson.A{}
	for _, c := range watched {
		collIn = append(collIn, c)
	}
	pipeline := mongo.Pipeline{{{Key: "$match", Value: bson.M{"ns.coll": bson.M{"$in": collIn}}}}}
	cs, err := b.a.DB.DB.Watch(ctx, pipeline, options.ChangeStream().SetFullDocument(options.UpdateLookup))
	if err != nil {
		return err
	}
	defer cs.Close(context.Background())
	b.Mode = "changestream"
	for cs.Next(ctx) {
		var ev struct {
			Op string `bson:"operationType"`
			NS struct {
				Coll string `bson:"coll"`
			} `bson:"ns"`
			Key struct {
				ID string `bson:"_id"`
			} `bson:"documentKey"`
			Doc struct {
				WorkspaceID string `bson:"workspaceId"`
				ProjectID   string `bson:"projectId"`
				AssetID     string `bson:"assetId"`
			} `bson:"fullDocument"`
			Update struct {
				Updated bson.M   `bson:"updatedFields"`
				Removed []string `bson:"removedFields"`
			} `bson:"updateDescription"`
		}
		if err := cs.Decode(&ev); err != nil {
			continue
		}
		if ev.Doc.WorkspaceID != "" && ev.Doc.WorkspaceID != b.a.WorkspaceID() {
			continue
		}
		if (ev.NS.Coll == store.CRecipeRuns || ev.NS.Coll == store.CReviewLinks) && ev.Op == "update" && leaseOnly(ev.Update.Updated, ev.Update.Removed) {
			continue
		}
		b.publish(Event{Type: ev.NS.Coll, Op: ev.Op, ID: ev.Key.ID, ProjectID: ev.Doc.ProjectID, AssetID: ev.Doc.AssetID})
	}
	return cs.Err()
}

// pollMark is where a poll starts looking from: the earlier of the database
// server's time and this computer's. What another computer judges is stamped
// in the server's time (D360) and plain records with their computer's clock,
// so going by one clock alone misses the other's changes when the two differ.
// A change stamped ahead of the mark is found on every look until the mark
// passes it, however long that takes; poll remembers what it told, so it is
// told once (D368).
func (b *Broker) pollMark(ctx context.Context) time.Time {
	server, here := b.a.DB.NowOrLocal(ctx), time.Now().UTC()
	if here.Before(server) {
		return here
	}
	return server
}

// polled is what polling looks at: records whose stamps moved past the mark,
// and what else is told on every look (also: a render under way).
var polled = []struct {
	coll   string
	stamps []string
	also   bson.M
}{
	{coll: store.CJobs, stamps: []string{"updatedAt"}},
	{coll: store.CAssets, stamps: []string{"updatedAt"}},
	{coll: store.CProjects, stamps: []string{"updatedAt"}},
	{coll: store.CIngest, stamps: []string{"updatedAt"}},
	{coll: store.CRenders, stamps: []string{"createdAt", "completedAt"}},
	{coll: store.CRenders, also: bson.M{"state": bson.M{"$in": bson.A{"queued", "rendering", "uploading"}}}},
	{coll: store.CRevisions, stamps: []string{"createdAt", "approval.at"}},
	{coll: store.CComments, stamps: []string{"createdAt"}},
	{coll: store.CWorkers, stamps: []string{"lastHeartbeat"}},
	{coll: store.CNotifications, stamps: []string{"updatedAt"}},
	{coll: store.CQAReports, stamps: []string{"createdAt"}},
	{coll: store.CTodos, stamps: []string{"updatedAt"}},
	{coll: store.CSpeakers, stamps: []string{"updatedAt"}},
	{coll: store.CPeople, stamps: []string{"updatedAt"}},
	{coll: store.CResources, stamps: []string{"updatedAt"}},
	{coll: store.CLinkedFolders, stamps: []string{"updatedAt"}},
	{coll: store.CProjectLinks, stamps: []string{"updatedAt"}},
	{coll: store.CCollections, stamps: []string{"updatedAt"}},
	// Generation records are written once, part by part (D125): each part has its own time.
	{coll: store.CGenerations, stamps: []string{"createdAt", "ticket.submittedAt", "outcome.at", "forgotten.at"}},
	{coll: store.CLooks, stamps: []string{"updatedAt"}},
	{coll: store.CConsents, stamps: []string{"recordedAt", "revokedAt"}},
	{coll: store.CRemotionSources, stamps: []string{"updatedAt"}},
	{coll: store.CRemotionVersions, stamps: []string{"updatedAt"}},
	{coll: store.CRemotionRenders, stamps: []string{"updatedAt"}},
	{coll: store.CSpelling, stamps: []string{"updatedAt"}},
	{coll: store.CContentSettings, stamps: []string{"updatedAt"}},
	{coll: store.CContentDefaults, stamps: []string{"setAt"}},
	// Recipes (D201–D204): a recipe saved or changed, a version made or made current, a run's steps.
	{coll: store.CRecipes, stamps: []string{"updatedAt"}},
	{coll: store.CRecipeVersions, stamps: []string{"at", "madeCurrentAt"}},
	{coll: store.CRecipeRuns, stamps: []string{"updatedAt"}},
	// Drafts from AI apps (D248): one saved, or a person's decision.
	{coll: store.CDrafts, stamps: []string{"createdAt", "decision.at", "tried.at"}},
	// Review links (D314, D315): one made or ended, or comments came in.
	{coll: store.CReviewLinks, stamps: []string{"createdAt", "endedAt", "lastCommentAt"}},
}

// stampsOf says a record's stamps, to tell whether any moved.
func stampsOf(raw bson.Raw, stamps []string) string {
	var b strings.Builder
	for _, f := range stamps {
		b.WriteString(raw.Lookup(strings.Split(f, ".")...).String())
		b.WriteByte('|')
	}
	return b.String()
}

func (b *Broker) poll(ctx context.Context, d time.Duration) {
	since := b.pollMark(ctx).Add(-5 * time.Second)
	end := time.Now().Add(d)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for time.Now().Before(end) {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := b.pollMark(ctx)
		told := map[string]string{}
		for _, pq := range polled {
			filter := bson.M{}
			for k, v := range pq.also {
				filter[k] = v
			}
			if len(pq.stamps) > 0 {
				or := bson.A{}
				for _, f := range pq.stamps {
					or = append(or, bson.M{f: bson.M{"$gte": since}})
				}
				filter["$or"] = or
			}
			var docs []bson.Raw
			_ = b.a.DB.Find(ctx, pq.coll, b.a.WorkspaceID(), filter, &docs, options.Find().SetLimit(200))
			for _, raw := range docs {
				var d struct {
					ID        string `bson:"_id"`
					ProjectID string `bson:"projectId"`
					AssetID   string `bson:"assetId"`
				}
				if bson.Unmarshal(raw, &d) != nil {
					continue
				}
				if len(pq.stamps) > 0 {
					// Told already, and none of its stamps moved since: it is
					// only ahead of the mark.
					key, seen := pq.coll+"/"+d.ID, stampsOf(raw, pq.stamps)
					told[key] = seen
					if b.told[key] == seen {
						continue
					}
				}
				b.publish(Event{Type: pq.coll, Op: "update", ID: d.ID, ProjectID: d.ProjectID, AssetID: d.AssetID})
			}
		}
		b.told = told
		since = now.Add(-time.Second)
	}
}

func (b *Broker) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("X-Accel-Buffering", "no")
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subs[ch] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}()
	hello, _ := json.Marshal(EventsHello{Mode: b.Mode})
	fmt.Fprintf(w, "event: hello\ndata: %s\n\n", hello)
	fl.Flush()
	ping := time.NewTicker(20 * time.Second)
	defer ping.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case e := <-ch:
			bb, _ := json.Marshal(e)
			fmt.Fprintf(w, "data: %s\n\n", bb)
			fl.Flush()
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			fl.Flush()
		}
	}
}
