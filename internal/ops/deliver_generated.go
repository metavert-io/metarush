package ops

import (
	"context"
	"math"
	"sort"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// generatedIn lists the AI-generated clips a delivered cut shows (its own
// clips and those of the cuts it nests), how long they are on screen, and
// what each place asks people to disclose (D127). Nil when there are none.
func generatedIn(it *deliveryItem, order []string, used map[string][]string) *DeliveryGenerated {
	seconds := map[string]float64{}
	for _, c := range it.rev.Spec.Clips {
		if editspec.IsNested(c.AssetID) {
			continue
		}
		speed := c.Speed
		if speed <= 0 {
			speed = 1
		}
		seconds[c.AssetID] += math.Max(0, c.Out-c.In) / speed
	}
	g := &DeliveryGenerated{Clips: []DeliveryGeneratedClip{}}
	add := func(as models.Asset, usedAs []string, secs float64) {
		s := as.Source
		if !s.IsGenerated() {
			return
		}
		c := DeliveryGeneratedClip{AssetID: as.ID, Title: as.DisplayTitle(), SHA256: as.SHA256, UsedAs: usedAs, Seconds: math.Round(secs*10) / 10}
		if usedAs == nil {
			c.UsedAs = []string{}
		}
		if gs := s.Generated; gs != nil {
			c.GenerationID, c.Provider, c.Model, c.Look, c.Likeness, c.Kind = gs.GenerationID, gs.ProviderTitle, gs.Model, gs.Look, gs.Likeness, gs.Kind
			if gs.Watermark != "" {
				g.Watermark = gs.Watermark
			}
			if gs.Kind == "presenter" {
				c.Presenter, c.Consent, c.PresenterWatermark = firstNonEmpty(gs.PresenterTitle, "a presenter"), gs.Consent, firstNonEmpty(gs.Watermark, "none")
				g.Presenter = true
			}
		}
		g.Likeness = g.Likeness || c.Likeness
		g.Seconds += c.Seconds
		g.Clips = append(g.Clips, c)
	}
	seen := map[string]bool{}
	for _, id := range order {
		seen[id] = true
		add(it.assets[id], used[id], seconds[id])
	}
	if it.extras != nil {
		for _, u := range it.extras.NestedCuts(&it.rev.Spec) {
			for _, id := range editspec.AssetIDs(u.Source.Spec) {
				if seen[id] {
					continue
				}
				seen[id] = true
				if as, ok := it.extras.Asset(id); ok && as != nil {
					add(*as, []string{"nested cut:" + u.ClipID}, 0)
				}
			}
		}
	}
	if len(g.Clips) == 0 {
		return nil
	}
	g.Seconds = math.Round(g.Seconds*10) / 10
	g.Notice = models.DisclosureNotice(g.Likeness)
	if g.Presenter {
		g.Notice = models.PresenterNotice(g.Likeness)
	}
	g.Checklist = models.DisclosureChecklist(g.Likeness)
	return g
}

// aiPictures adds the pictures an AI service made that the cut pins (D226):
// they are disclosed like generated clips. The notice says "material" when
// nothing generated is video.
func aiPictures(a *app.App, it *deliveryItem, g *DeliveryGenerated) *DeliveryGenerated {
	pins := it.rev.Spec.Resources
	if len(pins) > 0 {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		ids := bson.A{}
		for _, p := range pins {
			ids = append(ids, p.ID)
		}
		var rs []models.Resource
		_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &rs)
		byID := map[string]*models.Resource{}
		for i := range rs {
			byID[rs[i].ID] = &rs[i]
		}
		names := make([]string, 0, len(pins))
		for n := range pins {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			pin := pins[n]
			r := byID[pin.ID]
			if r == nil {
				continue
			}
			v := r.Version(pin.SHA256)
			if v == nil || v.Source.MadeWith == nil || v.Source.MadeWith.Kind != "ai" {
				continue
			}
			mw := v.Source.MadeWith
			if g == nil {
				g = &DeliveryGenerated{Clips: []DeliveryGeneratedClip{}}
			}
			// A real person's likeness, made with their consent: said on the picture or on its record.
			likeness := mw.Likeness
			if !likeness && mw.GenerationID != "" {
				var rec models.Generation
				if a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), mw.GenerationID, &rec) == nil {
					likeness = len(rec.ConsentIDs) > 0
				}
			}
			g.Clips = append(g.Clips, DeliveryGeneratedClip{Kind: "picture", ResourceID: r.ID, Title: firstNonEmpty(r.Title, r.Name), GenerationID: mw.GenerationID,
				Provider: mw.ProviderTitle, Model: mw.Model, SHA256: pin.SHA256, UsedAs: []string{"picture:" + n}, Likeness: likeness})
			g.Likeness = g.Likeness || likeness
			if mw.Watermark != "" {
				g.Watermark = mw.Watermark
			}
		}
	}
	if g == nil {
		return nil
	}
	video := false
	for _, c := range g.Clips {
		video = video || c.Kind == "" || c.Kind == models.GenKindVideo || c.Kind == "presenter"
	}
	g.Notice = models.DisclosureNotice(g.Likeness)
	if g.Presenter {
		g.Notice = models.PresenterNotice(g.Likeness)
	}
	if !video {
		g.Notice = "This video contains AI-generated material (pictures or sound)."
		if g.Likeness {
			g.Notice = "This video contains AI-generated material (pictures or sound), including a real person's likeness or voice made with their consent."
		}
	}
	g.Checklist = models.DisclosureChecklist(g.Likeness)
	return g
}

// aiTerms are the licence terms a clip made by an AI service carries, and
// what made it ("ElevenLabs, Eleven Music v2"); nil when it isn't one.
func aiTerms(a *app.App, as models.Asset) (*models.GenTerms, string) {
	s := as.Source
	if s == nil || s.Generated == nil || s.Generated.GenerationID == "" {
		return nil, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var g models.Generation
	if err := a.DB.GetByID(ctx, store.CGenerations, a.WorkspaceID(), s.Generated.GenerationID, &g); err != nil || g.Terms == nil {
		return nil, ""
	}
	return g.Terms, g.ProviderTitle + ", " + firstNonEmpty(g.ModelTitle, g.Model)
}

// changedWordsIn lists the words a delivered cut says in a speaker's voice
// copy in place of what they said (D311): its own changes and those of the
// cuts it nests, each where it is heard in the video; changes its mix keeps
// but no clip plays aren't heard, so they aren't listed.
func changedWordsIn(a *app.App, it *deliveryItem) []DeliveryChangedWords {
	heard := it.heard(a)
	clips, _ := editspec.Timeline(&it.rev.Spec)
	fps := float64(it.rev.Spec.Output.FPS)
	own := map[string]editspec.SaidSound{}
	if m := it.rev.Spec.Mix; m != nil {
		for _, c := range m.Said {
			own[c.ID] = c
		}
	}
	var out []DeliveryChangedWords
	for _, h := range heard {
		d := DeliveryChangedWords{ChangeID: h.ChangeID, Who: h.Who, Before: h.Before, Words: h.Words, At: math.Round(h.From*100) / 100, Withdrawn: h.Withdrawn, In: h.Nested != ""}
		if h.Mouth {
			d.MouthWithdrawn = h.MouthWithdrawn
			d.Mouth = true
		}
		if c := h.Change; c != nil {
			d.AssetID, d.Sound = c.AssetID, c.Sound
			if c.Fit != nil {
				d.From, d.To = c.Fit.From, c.Fit.To
			}
		}
		if c, ok := own[h.ChangeID]; ok && h.Nested == "" {
			d.AssetID, d.From, d.To, d.Sound = c.AssetID, c.From, c.To, c.Sound
			// Where the new words themselves are heard (the sound starts a little before them).
			for _, pc := range clips {
				if pc.AssetID == c.AssetID && pc.SourceIn <= c.WordsFrom && c.WordsFrom < pc.SourceOut && fps > 0 && pc.PlaybackRate > 0 {
					d.At = math.Round((float64(pc.From)/fps+(c.WordsFrom-pc.SourceIn)/pc.PlaybackRate)*100) / 100
					break
				}
			}
		}
		as, ok := it.assets[d.Sound]
		if !ok {
			if x, found := it.extras.Asset(d.Sound); found && x != nil {
				as, ok = *x, true
			}
		}
		if ok {
			d.SHA256 = as.SHA256
			if g := as.Source; g != nil && g.Generated != nil {
				d.GenerationID, d.Provider, d.Model = g.Generated.GenerationID, g.Generated.ProviderTitle, g.Generated.Model
			}
		}
		out = append(out, d)
	}
	return out
}

// heard is the changed words the item's version plays, checked now (the
// version's own as its mix says, when they couldn't be looked up).
func (it *deliveryItem) heard(a *app.App) []SaidHeard {
	if it.saidDone {
		return it.said
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	h, err := saidHeardInRevision(ctx, a, &it.rev)
	if err != nil && it.rev.Spec.Mix != nil {
		h = nil
		for _, c := range it.rev.Spec.Mix.Said {
			h = append(h, SaidHeard{SaidPlay: editspec.SaidPlay{ChangeID: c.ID, From: -1}, Who: c.Who, Words: c.Words, Before: c.Before})
		}
	}
	it.said, it.saidDone = h, true
	return h
}

// withChangedWords discloses changed words with the AI-generated material:
// each new sound is listed (a likeness: a real person's voice, with their
// consent) and the checklist says what each place asks.
func withChangedWords(it *deliveryItem, g *DeliveryGenerated, cw []DeliveryChangedWords) *DeliveryGenerated {
	if len(cw) == 0 {
		return g
	}
	if g == nil {
		g = &DeliveryGenerated{Clips: []DeliveryGeneratedClip{}}
	}
	video := false
	for _, c := range g.Clips {
		video = video || c.Kind == "" || c.Kind == models.GenKindVideo
	}
	for _, w := range cw {
		title := "new words"
		if as, ok := it.assets[w.Sound]; ok {
			title = as.DisplayTitle()
		}
		g.Clips = append(g.Clips, DeliveryGeneratedClip{AssetID: w.Sound, Kind: "voice", Title: title, GenerationID: w.GenerationID, Provider: w.Provider, Model: w.Model,
			Likeness: true, SHA256: w.SHA256, UsedAs: []string{"said:" + w.ChangeID}, Seconds: math.Round((w.To-w.From)*10) / 10})
	}
	g.Likeness = true
	g.Notice = models.DisclosureNotice(true)
	if !video {
		g.Notice = "This video contains words changed with AI in a real person's own voice, with their consent."
	}
	g.Checklist = models.DisclosureChecklist(true)
	return g
}
