package ops

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Every place a cut plays a clip (editspec.AssetIDs) is found by asset.where,
// the delete check (asset.delete, emptying the Trash) and HeadsUsing, and said
// in plain words: a clip shown only behind the people of another clip (D510),
// a cutaway, a conversation's recording, the narration's takes and copies, the
// mix's copies. Before, a clip behind people went unseen by all three, and
// cutaways, conversations' recordings and the mix's copies by asset.where and
// the delete check, so a clip a latest version played there could be deleted
// for good.
func TestEveryPlaceACutPlaysAClipIsFound(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Uses"}).Project
	clipOf := func(name string) models.Asset {
		as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name, State: models.AssetAvailable,
			ProjectIDs: []string{p.ID},
			Media:      models.MediaInfo{Duration: 26, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
			Variants:   map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
		insert(t, a, store.CAssets, as)
		return as
	}
	other := clipOf("other.mp4")
	plays := func(clip string) editspec.Spec {
		return editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "a", AssetID: clip, In: 0, Out: 2}}}
	}
	n := 0
	for _, c := range []struct {
		what string
		spec func(id string) editspec.Spec
		says string // how asset.where says it ("" for one clip of the cut)
	}{
		{"a clip", plays, ""},
		{"the cut's sound", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Audio = []editspec.AudioTrack{{ID: "bed", AssetID: id, Role: "music", In: 0, Out: 2}}
			return s
		}, ""},
		{"behind the people", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Clips[0].Background = &editspec.ClipBackground{With: editspec.BackgroundFootage, Footage: &editspec.BackgroundClip{AssetID: id, In: 3},
				Matte: strings.Repeat("a", 64)}
			return s
		}, ", behind the people in a clip)"},
		{"a cutaway", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Clips[0].Shots = []editspec.Shot{{At: 0, BRoll: &editspec.ShotBRoll{AssetID: id}}}
			return s
		}, ", as a cutaway)"},
		{"a conversation's recording", func(id string) editspec.Spec {
			return editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey("call"), In: 0, Out: 2}},
				Conversations: map[string]editspec.ConversationPin{"call": {Members: []editspec.ConversationMember{{ID: "x", Name: "X", AssetID: id}}}}}
		}, ", in a conversation)"},
		{"a line's take", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "l1", Text: "Hi.", Take: &editspec.TakeRef{AssetID: id, In: 0, Out: 1}}}}
			return s
		}, ", in the narration)"},
		{"a line's cleaned-up take", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{{ID: "l1", Text: "Hi.", Take: &editspec.TakeRef{AssetID: other.ID, In: 0, Out: 1,
				Processed: &editspec.ProcessedRef{AssetID: id, Preset: "clean"}}}}}
			return s
		}, ", in the narration)"},
		{"the narration recording", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Continuous: &editspec.ContinuousVO{AssetID: id, In: 0, Out: 5}}
			return s
		}, ", in the narration)"},
		{"the narration's cleaned-up copy", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Voiceover = &editspec.Voiceover{Continuous: &editspec.ContinuousVO{AssetID: other.ID, In: 0, Out: 5,
				Processed: &editspec.ProcessedRef{AssetID: id, Preset: "clean"}}}
			return s
		}, ", in the narration)"},
		{"a clearer voice", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Mix = &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: other.ID, Variant: id, Settings: "s"}}}
			return s
		}, ", in the sound)"},
		{"changed words' sound", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Mix = &editspec.Mix{Said: []editspec.SaidSound{{ID: ids.New(ids.SpokenChange), AssetID: other.ID, From: 1, To: 2, Sound: id, Words: "hi"}}}
			return s
		}, ", in the sound)"},
		{"changed words' picture", func(id string) editspec.Spec {
			s := plays(other.ID)
			s.Mix = &editspec.Mix{Said: []editspec.SaidSound{{ID: ids.New(ids.SpokenChange), AssetID: other.ID, From: 1, To: 2, Sound: other.ID, Picture: id, Words: "hi"}}}
			return s
		}, ", in the sound)"},
	} {
		x := clipOf(c.what + ".mp4")
		spec := c.spec(x.ID)
		if !slices.Contains(editspec.AssetIDs(&spec), x.ID) {
			t.Fatalf("%s: not a place editspec.AssetIDs reads", c.what)
		}
		n++
		r := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: n, Spec: spec, SpecHash: "h", CreatedAt: now}
		insert(t, a, store.CRevisions, r)
		if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: r.ID, UpdatedAt: now}}}); err != nil {
			t.Fatal(err)
		}

		if hs, err := HeadsUsing(ctx, a, x.ID); err != nil || len(hs) != 1 || hs[0].Revision.ID != r.ID {
			t.Errorf("%s: HeadsUsing finds the latest version: %+v %v", c.what, hs, err)
		}
		where := call[AssetWhereOut](t, a, "asset.where", AssetRefIn{AssetID: x.ID})
		var cut *UseRow
		for i := range where.Uses {
			if where.Uses[i].Kind == UseCut {
				cut = &where.Uses[i]
			}
		}
		switch {
		case cut == nil:
			t.Errorf("%s: asset.where lists no cut: %+v", c.what, where.Uses)
		case !cut.Current || cut.RevisionID != r.ID:
			t.Errorf("%s: asset.where's cut is the latest version: %+v", c.what, *cut)
		case !strings.HasSuffix(cut.Text, c.says) || (c.says == "" && strings.Contains(cut.Text, ",")):
			t.Errorf("%s: asset.where says %q, want it to end %q", c.what, cut.Text, c.says)
		}
		del, err := CallTyped[AssetDeleteOut](ctx, a, "asset.delete", AssetDeleteIn{AssetID: x.ID, ProjectID: p.ID, DryRun: true}, Caller{Human: true})
		if err != nil || del.Allowed || len(del.Uses) != 1 || del.Uses[0].RevisionID != r.ID {
			t.Errorf("%s: deleting it for good is refused while the cut plays it: %+v %v", c.what, del, err)
		}
	}
}
