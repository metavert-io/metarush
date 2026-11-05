package ops

import (
	"context"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/c2pa"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What AI did in a delivered version (D435): one walk over the version and
// every cut it plays, at any depth, conversations' members included, with
// the same rules for each — its generated clips, its narration's AI voices
// and cleaned-up lines, its clearer voices, its changed words, the pictures
// it shows, what abilities brought in as their manifests declare (D460) —
// and one answer for each (models.AIWorkOfSource, AIWorkOfMadeWith). Content
// Credentials list what it finds; the delivery's record discloses the same
// AI-made and AI-edited pieces; the quality check counts pictures by the
// same answer.

// aiPiece is one piece of AI work a delivered version plays.
type aiPiece struct {
	// What it is, for a manifest part's title: footage, picture, voice,
	// words, music, sound-effect, dubbing, presenter, lip-sync, voice-change,
	// sound (a clean-up).
	What string
	// Work is what AI did (models.AIWorkMade, AIWorkEdited, AIWorkEnhanced).
	Work string
	// Kind is the kind of AI work recorded ("" a clean-up or a narration take).
	Kind   string
	SHA256 string
	Format string
	// Where it comes from: a clip, a picture, a narration take.
	AssetID, ResourceID, TakeID string
	// Nested is the clip that plays the cut it is in ("" the version's own).
	Nested string
	// For the record only, never a manifest: its title, what made it.
	Title, Provider, Model, GenerationID string
	// Ability is the ability that made it (name@version; D460).
	Ability  string
	Likeness bool
}

// made is what made a piece, as a manifest says it ("Google Gemini, veo-3.1").
func (p aiPiece) made() string {
	return strings.Trim(strings.Join([]string{p.Provider, p.Model}, ", "), ", ")
}

// cutUse is a cut a delivered version plays: the version's own, or a cut
// it nests, with what its clips and narration takes are.
type cutUse struct {
	spec   *editspec.Spec
	nested string
	asset  func(id string) (models.Asset, bool)
	take   func(id string) (models.VoiceTake, bool)
	// matte is a clip's cut-out the cut plays (D510; nil: none known).
	matte func(sha string) (*models.Matte, bool)
}

// shownPicture is a picture a delivered version shows (its own pins and its
// nested cuts').
type shownPicture struct {
	Name, ResourceID, Title, SHA256 string
	// MadeWith says how MetaRush made it (nil: a picture someone added).
	MadeWith *models.ResourceMadeWith
	// Kind is its AI work's kind, from the generation record ("" unknown).
	Kind string
	// Nested names the nested cut's project that pins it ("" the version's own).
	Nested   string
	Likeness bool
}

// aiPieces is every piece of AI work the cuts play and the pictures show,
// each once (by its bytes), generated clips first.
func aiPieces(uses []cutUse, pics []shownPicture) []aiPiece {
	var out []aiPiece
	seen := map[string]bool{}
	add := func(p aiPiece) {
		key := p.SHA256
		if key == "" {
			key = p.AssetID + "|" + p.ResourceID + "|" + p.TakeID
		}
		if p.Work == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, p)
	}
	for _, u := range uses {
		// Clips an AI model made or changed: generated ones, and an ability's
		// as its manifest declares (D460) — their source says so.
		music := map[string]bool{}
		for _, t := range u.spec.Audio {
			if t.Role == editspec.RoleMusic {
				music[t.AssetID] = true
			}
		}
		for _, id := range editspec.AssetIDs(u.spec) {
			as, ok := u.asset(id)
			if !ok {
				continue
			}
			work := models.AIWorkOfSource(as.Source)
			if work == "" {
				continue
			}
			p := aiPiece{SHA256: as.SHA256, Format: as.MIME, AssetID: id, Nested: u.nested, Title: as.DisplayTitle(), Work: work, What: "footage"}
			switch src := as.Source; {
			case src.Generated != nil:
				g := src.Generated
				p.Kind, p.Provider, p.Model, p.GenerationID, p.Likeness = g.Kind, g.ProviderTitle, g.Model, g.GenerationID, g.Likeness
				p.What = whatAIMade(g)
			case src.IsAbility():
				p.Kind, p.Provider = "ability", src.AbilityTitle()
				if src.MadeWith != nil {
					p.Model, p.Ability = src.MadeWith.Ability, src.MadeWith.Ability
				}
				switch {
				case music[id]:
					p.What = "music"
				case as.Kind != "video":
					p.What = "sound"
				}
			}
			add(p)
		}
	}
	for _, u := range uses {
		// The narration: AI voices, and the cleaned-up copies lines play.
		narration := func(assetID, takeID string, aiVoice bool, processed *editspec.ProcessedRef, useProcessed bool) {
			if aiVoice {
				as, _ := u.asset(assetID)
				p := aiPiece{What: "voice", Work: models.AIWorkMade, Kind: "voice", SHA256: as.SHA256, Format: as.MIME, AssetID: assetID, TakeID: takeID, Nested: u.nested, Title: "AI narration"}
				if t, ok := u.take(takeID); ok {
					p.Provider, p.Model = providerTitle(t.Provider), t.ModelID
				}
				add(p)
			}
			if useProcessed && processed != nil {
				as, _ := u.asset(processed.AssetID)
				add(aiPiece{What: "sound", Work: models.AIWorkEnhanced, SHA256: as.SHA256, Format: as.MIME, AssetID: processed.AssetID, Nested: u.nested, Title: "narration made clearer"})
			}
		}
		if v := u.spec.Voiceover; v != nil {
			for _, b := range v.Beats {
				if b.Take != nil {
					narration(b.Take.AssetID, b.Take.TakeID, b.Take.AIVoice, b.Take.Processed, b.Take.UseProcessed)
				}
			}
			if c := v.Continuous; c != nil {
				narration(c.AssetID, "", c.AIVoice, c.Processed, c.UseProcessed)
			}
		}
		// Voices made clearer in the clips' sound (conversations' members too).
		if mx := u.spec.Mix; mx != nil {
			for _, e := range mx.Enhanced {
				as, _ := u.asset(e.Variant)
				add(aiPiece{What: "sound", Work: models.AIWorkEnhanced, SHA256: as.SHA256, Format: as.MIME, AssetID: e.Variant, Nested: u.nested, Title: "a voice made clearer"})
			}
		}
		// Clips whose background is replaced (D510, D511): the people, told from
		// their background by an AI model, over something else — the footage
		// enhanced, like a picture's background taken away.
		for _, c := range u.spec.Clips {
			b := c.Background
			if b == nil || b.Matte == "" {
				continue
			}
			p := aiPiece{What: "footage", Work: models.AIWorkOf(aiKindVideoBackground, false), Kind: aiKindVideoBackground, SHA256: b.Matte, Format: "video/webm",
				AssetID: c.AssetID, Nested: u.nested, Title: "the people in a clip, over a new background"}
			if as, ok := u.asset(c.AssetID); ok {
				p.Title = "the people in “" + as.DisplayTitle() + "”, over a new background"
			}
			if u.matte != nil {
				if m, ok := u.matte(b.Matte); ok {
					p.Provider, p.Model, p.GenerationID = m.ProviderTitle, m.ModelTitle, m.GenerationID
				}
			}
			add(p)
		}
	}
	for _, pic := range pics {
		// Pictures an AI service made or changed, and an ability's as its
		// manifest declares (D460).
		mw := pic.MadeWith
		work := models.AIWorkOfMadeWith(mw, pic.Kind)
		if work == "" {
			continue
		}
		p := aiPiece{What: "picture", Work: work, Kind: pic.Kind, SHA256: pic.SHA256, ResourceID: pic.ResourceID, Nested: pic.Nested, Title: pic.Title,
			Provider: mw.ProviderTitle, Model: mw.Model, GenerationID: mw.GenerationID, Likeness: pic.Likeness || mw.Likeness}
		if ab := mw.Ability; mw.Kind == "ability" && ab != nil {
			p.Kind, p.Provider, p.Model, p.Ability = "ability", firstNonEmpty(ab.Title, ab.Name), ab.Ability, ab.Ability
		}
		add(p)
	}
	return out
}

// whatAIMade is what a generated clip is, for its title.
func whatAIMade(g *models.GeneratedSource) string {
	switch g.Kind {
	case "", models.GenKindVideo:
		return "footage"
	case "voice":
		if g.Said != nil {
			return "words"
		}
		return "voice"
	case "background-removal", "upscale":
		return "picture"
	}
	return g.Kind // picture, music, sound-effect, dubbing, presenter, lip-sync, voice-change
}

// aiUsesOf is what a delivered version plays and shows, for aiPieces: its
// own cut and every cut it nests (their narration takes and the kinds of
// AI work of their pictures looked up once).
func aiUsesOf(ctx context.Context, a *app.App, it *deliveryItem) ([]cutUse, []shownPicture) {
	asset := func(id string) (models.Asset, bool) {
		if as, ok := it.assets[id]; ok {
			return as, true
		}
		if as, ok := it.extras.Asset(id); ok && as != nil {
			return *as, true
		}
		return models.Asset{}, false
	}
	nested := it.extras.NestedCuts(&it.rev.Spec)
	takes := map[string]models.VoiceTake{}
	for id, t := range it.takes {
		takes[id] = t
	}
	if a != nil {
		var ids bson.A
		for _, u := range nested {
			for _, id := range takeIDs(u.Source.Spec) {
				if _, ok := takes[id]; !ok {
					ids = append(ids, id)
				}
			}
		}
		if len(ids) > 0 {
			c, cancel := context.WithTimeout(ctx, 10*time.Second)
			var ts []models.VoiceTake
			_ = a.DB.Find(c, store.CVoiceTakes, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &ts)
			cancel()
			for _, t := range ts {
				takes[t.ID] = t
			}
		}
	}
	take := func(id string) (models.VoiceTake, bool) {
		t, ok := takes[id]
		return t, ok
	}
	// The clips' cut-outs the cuts play (D510), looked up once.
	var shas []string
	for _, s := range append([]*editspec.Spec{&it.rev.Spec}, nestedSpecs(nested)...) {
		shas = append(shas, editspec.MatteSHAs(s)...)
	}
	mattes := map[string]*models.Matte{}
	if a != nil && len(shas) > 0 {
		c, cancel := context.WithTimeout(ctx, 10*time.Second)
		if ms, err := LoadMattes(c, a, shas); err == nil {
			mattes = ms
		}
		cancel()
	}
	matte := func(sha string) (*models.Matte, bool) {
		m, ok := mattes[sha]
		return m, ok
	}
	uses := []cutUse{{spec: &it.rev.Spec, asset: asset, take: take, matte: matte}}
	for _, u := range nested {
		uses = append(uses, cutUse{spec: u.Source.Spec, nested: u.ClipID, asset: asset, take: take, matte: matte})
	}
	type genFacts struct {
		kind     string
		likeness bool
	}
	gens := map[string]genFacts{}
	var pics []shownPicture
	shown := it.extras.ShownResources(&it.rev.Spec)
	if it.extras == nil && a != nil && len(it.rev.Spec.Resources) > 0 {
		shown = pinnedResources(ctx, a, &it.rev.Spec) // no render facts: the cut's own pins
	}
	for _, shown := range shown {
		r, pin := shown.Doc, shown.Pin
		p := shownPicture{Name: shown.Name, ResourceID: r.ID, Title: firstNonEmpty(r.Title, r.Name), SHA256: pin.SHA256, Nested: shown.In}
		if v := r.Version(pin.SHA256); v != nil {
			p.MadeWith = v.Source.MadeWith
		}
		if mw := p.MadeWith; mw != nil && mw.Kind == "ai" {
			p.Kind = mw.AIKind
			if mw.GenerationID != "" && a != nil {
				// The generation record: the kind for pictures kept before it was
				// recorded on them, and a likeness made with consent.
				f, ok := gens[mw.GenerationID]
				if !ok {
					var g models.Generation
					c, cancel := context.WithTimeout(ctx, 5*time.Second)
					if a.DB.GetByID(c, store.CGenerations, a.WorkspaceID(), mw.GenerationID, &g) == nil {
						f = genFacts{kind: g.Kind, likeness: len(g.ConsentIDs) > 0}
					}
					cancel()
					gens[mw.GenerationID] = f
				}
				p.Kind = firstNonEmpty(p.Kind, f.kind)
				p.Likeness = f.likeness
			}
		}
		pics = append(pics, p)
	}
	return uses, pics
}

// aiKindVideoBackground is the kind of AI work that cuts the people out of a
// clip (aiservices.KindVideoBackground; this package doesn't import it).
const aiKindVideoBackground = "video-background"

// nestedSpecs are the specs of the cuts a version plays.
func nestedSpecs(uses []NestedCutUse) []*editspec.Spec {
	var out []*editspec.Spec
	for _, u := range uses {
		if u.Source != nil && u.Source.Spec != nil {
			out = append(out, u.Source.Spec)
		}
	}
	return out
}

// takeIDs are the narration takes a cut plays.
func takeIDs(s *editspec.Spec) []string {
	var out []string
	if s == nil || s.Voiceover == nil {
		return out
	}
	for _, b := range s.Voiceover.Beats {
		if b.Take != nil && b.Take.TakeID != "" && !slices.Contains(out, b.Take.TakeID) {
			out = append(out, b.Take.TakeID)
		}
	}
	return out
}

// manifestFrom is a delivered video's manifest: the AI work first (what it
// is and what AI did), then everything else the cuts play and show, by kind.
func manifestFrom(version string, uses []cutUse, pieces []aiPiece, sc *DeliverySidecar) c2pa.Manifest {
	m := c2pa.Manifest{Version: version}
	for _, p := range pieces {
		m.Parts = append(m.Parts, c2pa.Part{What: p.What, SHA256: p.SHA256, Format: p.Format, AI: p.Work, Made: p.made()})
	}
	for _, u := range uses {
		music := map[string]bool{}
		for _, t := range u.spec.Audio {
			if t.Role == editspec.RoleMusic {
				music[t.AssetID] = true
			}
		}
		for _, id := range editspec.AssetIDs(u.spec) {
			as, ok := u.asset(id)
			if !ok {
				continue
			}
			what := map[string]string{"video": "footage", "image": "picture"}[as.Kind]
			if what == "" {
				what = "sound"
			}
			if music[id] {
				what = "music"
			}
			m.Parts = append(m.Parts, c2pa.Part{What: what, SHA256: as.SHA256, Format: as.MIME})
		}
	}
	if sc != nil {
		for _, r := range sc.Resources {
			switch r.Kind {
			case "image", "svg":
				m.Parts = append(m.Parts, c2pa.Part{What: "picture", SHA256: r.SHA256})
			case "pdf":
				m.Parts = append(m.Parts, c2pa.Part{What: "document", SHA256: r.SHA256})
			}
		}
	}
	return m
}

// aiWorkOn is what AI did to a picture a delivered version shows ("" nothing).
func aiWorkOn(a *app.App, it *deliveryItem, resourceID, sha string) string {
	_, _, pieces := it.ai(context.Background(), a)
	for _, p := range pieces {
		if p.ResourceID == resourceID && p.SHA256 == sha {
			return p.Work
		}
	}
	return ""
}

// pinnedResources is a cut's own pinned pictures, read from the database.
func pinnedResources(ctx context.Context, a *app.App, s *editspec.Spec) []ShownResource {
	c, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	ids := bson.A{}
	for _, p := range s.Resources {
		ids = append(ids, p.ID)
	}
	var rs []models.Resource
	_ = a.DB.Find(c, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &rs)
	byID := map[string]*models.Resource{}
	for i := range rs {
		byID[rs[i].ID] = &rs[i]
	}
	var out []ShownResource
	for _, name := range editspec.ResourceNames(s) {
		pin := s.Resources[name]
		if doc := byID[pin.ID]; doc != nil {
			out = append(out, ShownResource{Name: name, Pin: pin, Doc: doc})
		}
	}
	return out
}
