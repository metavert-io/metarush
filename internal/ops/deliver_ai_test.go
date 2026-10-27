package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// What AI services made is disclosed in delivery like generated clips (D226):
// a picture the cut pins is listed, and the notice says "material" when
// nothing generated is video; AI music carries its licence (D225).
func TestDeliveryDisclosesAIPicturesAndMusicLicence(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	sha := strings.Repeat("ab", 32)
	res := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "kite", Title: "Kite", Kind: models.ResourceImage,
		Versions: []models.ResourceVersion{{SHA256: sha, Source: models.ResourceSource{Kind: "generated", At: now,
			MadeWith: &models.ResourceMadeWith{Kind: "ai", GenerationID: "gen_x", Provider: "gemini", ProviderTitle: "Google Gemini", Model: "gemini-nano-banana-2.1", Watermark: "SynthID"}}}},
		CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CResources, res); err != nil {
		t.Fatal(err)
	}
	it := &deliveryItem{rev: models.Revision{Spec: editspec.Spec{Resources: map[string]editspec.ResourcePin{"kite": {ID: res.ID, SHA256: sha}}}}}
	g := aiPictures(a, it, nil)
	if g == nil || len(g.Clips) != 1 || g.Clips[0].Kind != "picture" || g.Clips[0].ResourceID != res.ID || g.Watermark != "SynthID" || !strings.Contains(g.Notice, "material") {
		t.Fatalf("disclosure %+v", g)
	}
	// A picture of a real person, made with their consent: disclosed as a likeness.
	pg := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), Kind: "picture", ConsentIDs: []string{"cst_x"}, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CGenerations, pg); err != nil {
		t.Fatal(err)
	}
	sha2 := strings.Repeat("cd", 32)
	ada := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "ada", Title: "Ada waving", Kind: models.ResourceImage,
		Versions: []models.ResourceVersion{{SHA256: sha2, Source: models.ResourceSource{Kind: "generated", At: now,
			MadeWith: &models.ResourceMadeWith{Kind: "ai", GenerationID: pg.ID, Provider: "gemini", ProviderTitle: "Google Gemini", Model: "gemini-nano-banana-2.1"}}}},
		CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CResources, ada); err != nil {
		t.Fatal(err)
	}
	it.rev.Spec.Resources["ada"] = editspec.ResourcePin{ID: ada.ID, SHA256: sha2}
	g = aiPictures(a, it, nil)
	if g == nil || len(g.Clips) != 2 || !g.Likeness || !strings.Contains(g.Notice, "likeness") {
		t.Fatalf("likeness %+v", g)
	}
	for _, c := range g.Clips {
		if c.ResourceID == ada.ID && !c.Likeness {
			t.Errorf("the picture of a person isn't marked: %+v", c)
		}
	}
	gen := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), Kind: "music", Provider: "elevenlabs", ProviderTitle: "ElevenLabs", Model: "music_v2",
		ModelTitle: "Eleven Music v2", Terms: &models.GenTerms{Licence: "Individuals only on Starter.", Commercial: "plan", Broadcast: "plan"}, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CGenerations, gen); err != nil {
		t.Fatal(err)
	}
	as := models.Asset{Source: &models.AssetSource{Platform: models.PlatformGenerated, Generated: &models.GeneratedSource{GenerationID: gen.ID, Kind: "music"}}}
	terms, made := aiTerms(a, as)
	if terms == nil || terms.Broadcast != "plan" || made != "ElevenLabs, Eleven Music v2" {
		t.Fatalf("terms %+v %q", terms, made)
	}
}

// A presenter clip is disclosed as one (D277): who reads, the consent it
// relies on, no watermark, and a notice that says an AI presenter.
func TestDeliveryDisclosesPresenters(t *testing.T) {
	as := models.Asset{ID: ids.New(ids.Asset), Name: "ada.mp4", Title: "Ada reads line 1", SHA256: strings.Repeat("cd", 32),
		Source: &models.AssetSource{Platform: models.PlatformGenerated, Method: models.SourceGenerated, Generated: &models.GeneratedSource{GenerationID: "gen_p",
			Kind: "presenter", Provider: "d-id", ProviderTitle: "D-ID", Model: "express", Likeness: true, Presenter: "prs_x", PresenterTitle: "Ada Lovelace", Consent: "cst_x"}}}
	it := &deliveryItem{assets: map[string]models.Asset{as.ID: as}, rev: models.Revision{Spec: editspec.Spec{Clips: []editspec.Clip{{ID: "c1", AssetID: as.ID, In: 0, Out: 4}}}}}
	g := generatedIn(it, []string{as.ID}, map[string][]string{as.ID: {"clip:c1"}})
	if g == nil || !g.Presenter || !g.Likeness || len(g.Clips) != 1 || !strings.Contains(g.Notice, "presenter made with AI from a real person's likeness") {
		t.Fatalf("%+v", g)
	}
	c := g.Clips[0]
	if c.Presenter != "Ada Lovelace" || c.Consent != "cst_x" || c.PresenterWatermark != "none" || c.Kind != "presenter" || c.Seconds != 4 {
		t.Fatalf("%+v", c)
	}
}
