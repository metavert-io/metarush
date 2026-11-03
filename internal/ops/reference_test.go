package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
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

// A clip can't become a reference while any latest version plays it, wherever
// it plays it (D440): a clip, a conversation's recording or cutaway, a line's
// take, the narration recording, or inside a cut it nests. An older version
// that plays it doesn't hold it back.
func TestAClipALatestVersionPlaysAnywhereStaysFootage(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clipOf := func(name string) models.Asset {
		as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name, State: models.AssetAvailable,
			Media:    models.MediaInfo{Duration: 26, HasVideo: true, HasAudio: true, Width: 720, Height: 1280, FPS: 30, Streams: []models.Stream{}},
			Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
		insert(t, a, store.CAssets, as)
		return as
	}
	other := clipOf("other.mp4")
	// version saves a version of the project's main cut playing spec, as its latest when head is true.
	numbers := map[string]int{}
	version := func(projectID string, spec editspec.Spec, head bool) models.Revision {
		numbers[projectID]++
		r := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: projectID, Branch: "main", Number: numbers[projectID], Spec: spec, SpecHash: "h", CreatedAt: now}
		insert(t, a, store.CRevisions, r)
		if head {
			if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": projectID}, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: r.ID, UpdatedAt: now}}}); err != nil {
				t.Fatal(err)
			}
		}
		return r
	}
	plays := func(clip string) editspec.Spec {
		return editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "a", AssetID: clip, In: 0, Out: 2}}}
	}
	refused := func(what, assetID string) {
		t.Helper()
		_, err := CallTyped[FootageReferenceOut](ctx, a, "footage.reference", FootageReferenceIn{AssetID: assetID}, Caller{})
		if ae := apperr.From(err); ae == nil || ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "can't be a reference while a cut uses it") {
			t.Errorf("%s: marking it is refused while a latest version plays it: %v", what, err)
		}
	}
	for _, c := range []struct {
		what string
		spec func(id string) editspec.Spec
	}{
		{"a conversation's recording", func(id string) editspec.Spec {
			return editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey("call"), In: 0, Out: 2}},
				Conversations: map[string]editspec.ConversationPin{"call": {Members: []editspec.ConversationMember{{ID: "x", Name: "X", AssetID: id}}}}}
		}},
		{"a cutaway", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Clips[0].Shots = []editspec.Shot{{At: 0, BRoll: &editspec.ShotBRoll{AssetID: id}}}
			return s
		}},
		{"a line's take", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "l1", Text: "Hi.", Take: &editspec.TakeRef{AssetID: id, In: 0, Out: 1}}}}
			return s
		}},
		{"the narration recording", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Continuous: &editspec.ContinuousVO{AssetID: id, In: 0, Out: 5}}
			return s
		}},
	} {
		p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Uses " + c.what}).Project
		x := clipOf(c.what + ".mp4")
		version(p.ID, c.spec(x.ID), true)
		if hs, err := HeadsUsing(ctx, a, x.ID); err != nil || len(hs) != 1 || hs[0].Project.ID != p.ID {
			t.Errorf("%s: the cut whose latest version plays it: %+v %v", c.what, hs, err)
		}
		refused(c.what, x.ID)
	}

	// Inside a nested cut: the inner version is not its own cut's latest, the outer cut's latest nests it.
	inner := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Inner"}).Project
	outer := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Outer"}).Project
	x := clipOf("nested.mp4")
	old := version(inner.ID, plays(x.ID), false)
	version(inner.ID, plays(other.ID), true)
	version(outer.ID, plays(editspec.NestedKey(inner.ID, old.ID)), true)
	if hs, _ := HeadsUsing(ctx, a, x.ID); len(hs) != 0 {
		t.Errorf("no latest version plays it itself: %+v", hs)
	}
	if hs, err := HeadsPlaying(ctx, a, x.ID); err != nil || len(hs) != 1 || hs[0].Project.ID != outer.ID {
		t.Errorf("the outer cut nests a version that plays it: %+v %v", hs, err)
	}
	refused("inside a nested cut", x.ID)

	// An older version that played it doesn't hold it back.
	free := clipOf("free.mp4")
	q := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Moved on"}).Project
	version(q.ID, plays(free.ID), false)
	version(q.ID, plays(other.ID), true)
	if out, err := CallTyped[FootageReferenceOut](ctx, a, "footage.reference", FootageReferenceIn{AssetID: free.ID}, Caller{}); err != nil || !out.Reference {
		t.Errorf("only an older version played it: %+v %v", out, err)
	}
}
