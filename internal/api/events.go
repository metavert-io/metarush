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

func (b *Broker) poll(ctx context.Context, d time.Duration) {
	since := time.Now().UTC().Add(-5 * time.Second)
	end := time.Now().Add(d)
	t := time.NewTicker(3 * time.Second)
	defer t.Stop()
	for time.Now().Before(end) {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		now := time.Now().UTC()
		polls := []struct {
			coll   string
			filter bson.M
		}{
			{store.CJobs, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CAssets, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CProjects, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CIngest, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CRenders, bson.M{"$or": bson.A{bson.M{"createdAt": bson.M{"$gte": since}}, bson.M{"completedAt": bson.M{"$gte": since}}, bson.M{"state": bson.M{"$in": bson.A{"queued", "rendering", "uploading"}}}}}},
			{store.CRevisions, bson.M{"$or": bson.A{bson.M{"createdAt": bson.M{"$gte": since}}, bson.M{"approval.at": bson.M{"$gte": since}}}}},
			{store.CComments, bson.M{"createdAt": bson.M{"$gte": since}}},
			{store.CWorkers, bson.M{"lastHeartbeat": bson.M{"$gte": since}}},
			{store.CNotifications, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CQAReports, bson.M{"createdAt": bson.M{"$gte": since}}},
			{store.CTodos, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CSpeakers, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CPeople, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CResources, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CLinkedFolders, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CProjectLinks, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CCollections, bson.M{"updatedAt": bson.M{"$gte": since}}},
			// Generation records are written once, part by part (D125): each part has its own time.
			{store.CGenerations, bson.M{"$or": bson.A{bson.M{"createdAt": bson.M{"$gte": since}}, bson.M{"ticket.submittedAt": bson.M{"$gte": since}},
				bson.M{"outcome.at": bson.M{"$gte": since}}, bson.M{"forgotten.at": bson.M{"$gte": since}}}}},
			{store.CLooks, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CConsents, bson.M{"$or": bson.A{bson.M{"recordedAt": bson.M{"$gte": since}}, bson.M{"revokedAt": bson.M{"$gte": since}}}}},
			{store.CRemotionSources, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CRemotionVersions, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CRemotionRenders, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CSpelling, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CContentSettings, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CContentDefaults, bson.M{"setAt": bson.M{"$gte": since}}},
			// Recipes (D201–D204): a recipe saved or changed, a version made or made current, a run's steps.
			{store.CRecipes, bson.M{"updatedAt": bson.M{"$gte": since}}},
			{store.CRecipeVersions, bson.M{"$or": bson.A{bson.M{"at": bson.M{"$gte": since}}, bson.M{"madeCurrentAt": bson.M{"$gte": since}}}}},
			{store.CRecipeRuns, bson.M{"updatedAt": bson.M{"$gte": since}}},
			// Drafts from AI apps (D248): one saved, or a person's decision.
			{store.CDrafts, bson.M{"$or": bson.A{bson.M{"createdAt": bson.M{"$gte": since}}, bson.M{"decision.at": bson.M{"$gte": since}}, bson.M{"tried.at": bson.M{"$gte": since}}}}},
			// Review links (D314, D315): one made or ended, or comments came in.
			{store.CReviewLinks, bson.M{"$or": bson.A{bson.M{"createdAt": bson.M{"$gte": since}}, bson.M{"endedAt": bson.M{"$gte": since}}, bson.M{"lastCommentAt": bson.M{"$gte": since}}}}},
		}
		for _, pq := range polls {
			var docs []struct {
				ID        string `bson:"_id"`
				ProjectID string `bson:"projectId"`
				AssetID   string `bson:"assetId"`
			}
			_ = b.a.DB.Find(ctx, pq.coll, b.a.WorkspaceID(), pq.filter, &docs, options.Find().SetLimit(200))
			for _, d := range docs {
				b.publish(Event{Type: pq.coll, Op: "update", ID: d.ID, ProjectID: d.ProjectID, AssetID: d.AssetID})
			}
		}
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
