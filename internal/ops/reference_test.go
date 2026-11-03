package ops

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
)

// A reference is someone else's video, kept to learn from (D398): nobody in it
// is asked about. Marking a clip closes the questions MetaRush asked about its
// voices and faces, a look that finishes later asks none, and a person saying
// it's theirs gives the clip its questions back.
func TestAReferenceAsksAboutNobodyInIt(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "their-tips.mp4", State: models.AssetAvailable,
		Media:    models.MediaInfo{Duration: 26, HasVideo: true, HasAudio: true, Width: 720, Height: 1280, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	open := func() []models.Todo {
		t.Helper()
		var ts []models.Todo
		if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": as.ID, "state": models.TodoOpen}, &ts); err != nil {
			t.Fatal(err)
		}
		return ts
	}
	for _, q := range []models.Todo{
		{Kind: models.TodoSpeakerUnnamed, Scope: "asset", AssetID: as.ID, GroupKey: "speaker:" + as.ID + ":speaker_0@r1", Question: "Who is the first voice in “their-tips”?"},
		{Kind: models.TodoFaceGroup, Scope: "asset", AssetID: as.ID, GroupKey: faces.GroupKeyPrefix(as.ID) + "fgr_one", Question: "Who is this?"},
	} {
		if _, err := todos.Upsert(ctx, a, q); err != nil {
			t.Fatal(err)
		}
	}
	if n := len(open()); n != 2 {
		t.Fatalf("two questions before: %d", n)
	}
	out := call[FootageReferenceOut](t, a, "footage.reference", FootageReferenceIn{AssetID: as.ID})
	if !out.Reference || !out.Changed {
		t.Fatalf("marked: %+v", out)
	}
	if ts := open(); len(ts) != 0 {
		t.Fatalf("someone else's video asks about nobody in it: %+v", ts)
	}
	// A look that finishes later (its voices, then its faces) asks nothing either.
	if err := speakers.Reconcile(ctx, a, as.ID); err != nil {
		t.Fatal(err)
	}
	if ts := open(); len(ts) != 0 {
		t.Fatalf("asked again after a look: %+v", ts)
	}
	// A person says it's theirs: cuts can play it, and its questions come back as its looks say.
	mine, err := CallTyped[FootageReferenceOut](ctx, a, "footage.reference.mine", FootageReferenceIn{AssetID: as.ID}, Caller{Human: true})
	if err != nil || !mine.Changed || mine.Clip.Reference != nil {
		t.Fatalf("theirs: %+v %v", mine, err)
	}
}
