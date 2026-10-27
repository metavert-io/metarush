package intake_test

import (
	"context"
	"image/color"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// "Where it's used" for a resource looks through every cut version, also
// versions that pin no resources at all (their spec has no resources field):
// it lists the cut that pins it instead of failing.
func TestResourceWhereAmongCutsThatPinNothing(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Tour")
	clip := footage(t, a, "intro.mov", p.ID)
	added, err := resources.AddFile(ctx, a, logoPNG(t, t.TempDir(), "card.png", color.RGBA{30, 120, 220, 255}), resources.AddOptions{ProjectID: p.ID, FileName: "card.png", Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	card := added.Resource
	plain := cut(editspec.Clip{ID: "intro", AssetID: clip.ID, In: 0, Out: 4})
	r1 := saveCut(t, a, p.ID, "", plain)
	pinned := cut(editspec.Clip{ID: "intro", AssetID: clip.ID, In: 0, Out: 4})
	pinned.Resources = map[string]editspec.ResourcePin{"card": {ID: card.ID, SHA256: card.Current}}
	saveCut(t, a, p.ID, r1, pinned)

	show := call[ops.ResourceShowOut](t, a, "resources.show", ops.ResourceRefIn{Ref: "card", ProjectID: p.ID})
	if len(show.UsedIn) != 1 || show.UsedIn[0].Kind != ops.UseCut || !strings.Contains(show.UsedIn[0].Text, "Tour's cut (version 2") {
		t.Fatalf("used in: %+v", show.UsedIn)
	}
	where := call[ops.ResourceWhereOut](t, a, "resources.where", ops.ResourceRefIn{Ref: "card", ProjectID: p.ID})
	if len(where.Uses) != 1 {
		t.Fatalf("where: %+v", where.Uses)
	}
}
