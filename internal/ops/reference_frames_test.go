package ops_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// A frame taken from a reference is treated like the reference itself (D457).
// footage frames writes where it was taken into the file, and Resources keeps
// that when the frame is added: the picture says it came from the clip and,
// once the clip is a reference, that it is someone else's video. The clip
// can't be marked while a latest version shows a frame of it; once marked, the
// cut check refuses the picture, a version that shows it is held from going
// out (the ship gate off too), and adding the frame again says so. A person
// saying the video is theirs makes it a picture like any other.
func TestAFrameTakenFromAReferenceIsTreatedLikeIt(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) || !ts.Has(tools.FFprobe) {
		t.Skip("ffmpeg not installed")
	}
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	src := filepath.Join(dir, "their-hook.mp4")
	if out, err := exec.Command(ts.Path(tools.FFmpeg), "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=size=320x180:rate=10:duration=2",
		"-pix_fmt", "yuv420p", src).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	in := ingest.File(ctx, a, src, ingest.Options{Via: "cli"})
	if in.Status != "ingested" || in.AssetID == "" {
		t.Fatalf("import %+v", in)
	}
	clipID := in.AssetID
	theirs := reload(t, a, clipID)
	title := theirs.DisplayTitle()
	p := project(t, a, "Hooks")
	mine := clip(t, a, "Our take", p.ID)

	// A frame of it, taken while it is still the person's own, kept as a picture.
	fr := do[ops.FramesOut](t, a, "footage.frames", ops.FramesIn{AssetID: clipID, At: []float64{1.5}, OutDir: dir})
	if len(fr.Frames) != 1 {
		t.Fatalf("frames %+v", fr)
	}
	added, err := resources.AddFile(ctx, a, fr.Frames[0].Path, resources.AddOptions{ProjectID: p.ID, Name: "hook-still", Title: "Hook still", Via: "cli"})
	if err != nil || added.Resource == nil {
		t.Fatalf("add the frame: %+v %v", added, err)
	}
	pic := added.Resource
	if f := pic.Latest().Source.FrameOf; f == nil || f.AssetID != clipID || f.At != 1.5 || f.Title != title {
		t.Fatalf("the picture keeps where it was taken: %+v", f)
	}
	if len(added.Notes) != 0 {
		t.Fatalf("a frame of the person's own footage says nothing more: %v", added.Notes)
	}
	show := do[ops.ResourceShowOut](t, a, "resources.show", ops.ResourceRefIn{Ref: "hook-still", ProjectID: p.ID})
	if show.Row.Provenance != "a frame of “"+title+"” at 0:01" || show.Row.Reference != "" {
		t.Fatalf("where it came from: %q / %q", show.Row.Provenance, show.Row.Reference)
	}

	// The latest version of a cut shows it: the clip can't become a reference.
	now := time.Now().UTC()
	pin := map[string]editspec.ResourcePin{"hook": {ID: pic.ID, SHA256: pic.Current}}
	shows := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 1, SpecHash: "h1", CreatedAt: now,
		Spec: editspec.Spec{SchemaVersion: 1, Clips: []editspec.Clip{{ID: "a", AssetID: mine.ID, In: 0, Out: 2}}, Resources: pin}}
	if err := a.DB.Insert(ctx, store.CRevisions, shows); err != nil {
		t.Fatal(err)
	}
	head := func(id string) {
		t.Helper()
		if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID}, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: id, UpdatedAt: now}}}); err != nil {
			t.Fatal(err)
		}
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.ID, &p); err != nil {
			t.Fatal(err)
		}
	}
	head(shows.ID)
	err = doErr(a, "footage.reference", ops.FootageReferenceIn{AssetID: clipID}, person)
	if ae := apperr.From(err); ae == nil || ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "a picture taken from it") {
		t.Fatalf("marked while a latest version shows a frame of it: %v", err)
	}
	// A newer version without it: the clip can be marked now.
	plain := shows
	plain.ID, plain.Number, plain.SpecHash, plain.Spec.Resources = ids.New(ids.Revision), 2, "h2", nil
	if err := a.DB.Insert(ctx, store.CRevisions, plain); err != nil {
		t.Fatal(err)
	}
	head(plain.ID)
	do[ops.FootageReferenceOut](t, a, "footage.reference", ops.FootageReferenceIn{AssetID: clipID})

	// The picture says so in the library, and adding the frame again says so too.
	show = do[ops.ResourceShowOut](t, a, "resources.show", ops.ResourceRefIn{Ref: "hook-still", ProjectID: p.ID})
	if !strings.Contains(show.Row.Reference, "Taken from “"+title+"”, a reference: someone else's video") {
		t.Fatalf("the picture says it came from a reference: %q", show.Row.Reference)
	}
	list := do[ops.ResourceListOut](t, a, "resources.list", ops.ResourceListIn{ProjectID: p.ID})
	if len(list.Resources) != 1 || list.Resources[0].Reference == "" {
		t.Fatalf("the list says it too: %+v", list.Resources)
	}
	again, err := resources.AddFile(ctx, a, fr.Frames[0].Path, resources.AddOptions{ProjectID: p.ID, Via: "cli"})
	if err != nil || again.Resource == nil || again.Resource.ID != pic.ID || len(again.Notes) != 1 || !strings.Contains(again.Notes[0], "a reference: someone else's video") {
		t.Fatalf("the frame again: %+v %v", again, err)
	}

	// No cut shows it: the cut check refuses it, with how to say the video is yours.
	planJSON := func() json.RawMessage {
		return json.RawMessage(fmt.Sprintf(`{"projectId":%q,"baseRevisionId":%q,"spec":{"schemaVersion":1,"title":"Cut","output":{"preset":"vertical-1080p","fps":30},
			"clips":[{"id":"a","assetId":%q,"in":0,"out":2}],"resources":{"hook":{"id":%q,"sha256":%q}}}}`, p.ID, plain.ID, mine.ID, pic.ID, pic.Current))
	}
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: planJSON()})
	var refused *editspec.Issue
	for i, is := range chk.Errors {
		if is.Code == "reference_footage" && is.Path == "/resources/hook" {
			refused = &chk.Errors[i]
		}
	}
	if chk.Valid || refused == nil || !strings.Contains(refused.Message, "The picture “Hook still” is a frame of “"+title+"”, which is a reference") ||
		refused.Command != "metarush footage reference "+clipID+" --mine" {
		t.Fatalf("the cut check: %+v", chk.Errors)
	}
	// The version that shows it is held from going out, with the gate off.
	g, err := ops.ShipGate(ctx, a, &p, &shows, "final", nil)
	if err != nil {
		t.Fatal(err)
	}
	var hold *ops.GateHold
	for i := range g.Holds {
		if g.Holds[i].Kind == models.HoldReference {
			hold = &g.Holds[i]
		}
	}
	if g.On || hold == nil || !hold.Holding || hold.Key != "reference:"+clipID+"@"+shows.ID ||
		!strings.Contains(hold.Says, "Version 1 shows “Hook still”, taken from “"+title+"”, a reference") || g.Ready {
		t.Fatalf("the ship gate: %+v %+v", hold, g)
	}
	if !strings.Contains(g.Why, "Even so, a reference holds this version") {
		t.Fatalf("with the gate off, it says what still holds it: %q", g.Why)
	}

	// The person says it's their video: the picture is theirs to use.
	do[ops.FootageReferenceOut](t, a, "footage.reference.mine", ops.FootageReferenceIn{AssetID: clipID})
	if chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: planJSON()}); !chk.Valid {
		t.Fatalf("theirs: the cut check: %+v", chk.Errors)
	}
	g, err = ops.ShipGate(ctx, a, &p, &shows, "final", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, h := range g.Holds {
		if h.Kind == models.HoldReference {
			t.Fatalf("theirs: no reference hold: %+v", h)
		}
	}
	if show := do[ops.ResourceShowOut](t, a, "resources.show", ops.ResourceRefIn{Ref: "hook-still", ProjectID: p.ID}); show.Row.Reference != "" {
		t.Fatalf("theirs: %q", show.Row.Reference)
	}
}
