package speakers

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Previous is a clip's voices as they were before speaker detection runs
// again: the voices (merged ones too) and who said each word.
type Previous struct {
	Voices []models.AssetSpeaker
	Words  []saidBy
}

type saidBy struct {
	From, To float64
	Label    string
}

// CapturePrevious records a clip's voices before its words are labeled again
// (nil when it has none).
func CapturePrevious(ctx context.Context, a *app.App, assetID string) *Previous {
	vs, err := Voices(ctx, a, assetID, true)
	if err != nil || len(vs) == 0 {
		return nil
	}
	segs, err := loadSegments(ctx, a, assetID)
	if err != nil {
		return nil
	}
	p := &Previous{Voices: vs}
	for _, s := range segs {
		if s.Speaker == "" {
			continue
		}
		if len(s.Words) == 0 {
			p.Words = append(p.Words, saidBy{From: s.From, To: s.To, Label: s.Speaker})
			continue
		}
		for _, w := range s.Words {
			p.Words = append(p.Words, saidBy{From: w.From, To: w.To, Label: s.Speaker})
		}
	}
	return p
}

// carried is what one new voice inherits from the voices found before.
type carried struct {
	person    *models.Person
	confirmed []models.TimeSpan
	rejected  []string
	naming    *models.Naming // who gave the name (D274), carried with it
}

// Carry-over shares: a confirmed name moves to a new voice when most of that
// voice's words were the confirmed voice's and it took most of them; a "not
// this person" moves when most of the new voice's words were said by the voice
// it was said about.
const (
	carryName   = 0.6
	carryReject = 0.5
)

// carryOver decides, for each new label, the name and rejections it keeps.
func carryOver(ctx context.Context, a *app.App, prev *Previous, segs []models.TranscriptSegment) map[string]carried {
	out := map[string]carried{}
	if prev == nil || len(prev.Words) == 0 {
		return out
	}
	// Who said what, before and now, compared by time (the words are the same
	// when only the voices were found again; close when the clip was transcribed again).
	old := append([]saidBy(nil), prev.Words...)
	sort.Slice(old, func(i, j int) bool { return old[i].From < old[j].From })
	var cur []saidBy
	for _, s := range segs {
		if s.Speaker == "" {
			continue
		}
		if len(s.Words) == 0 {
			cur = append(cur, saidBy{From: s.From, To: s.To, Label: s.Speaker})
			continue
		}
		for _, w := range s.Words {
			cur = append(cur, saidBy{From: w.From, To: w.To, Label: s.Speaker})
		}
	}
	sort.Slice(cur, func(i, j int) bool { return cur[i].From < cur[j].From })
	overlap := map[string]map[string]float64{} // new → old → seconds
	newTotal, oldTotal := map[string]float64{}, map[string]float64{}
	for _, w := range old {
		oldTotal[w.Label] += w.To - w.From
	}
	i := 0
	for _, w := range cur {
		newTotal[w.Label] += w.To - w.From
		for i < len(old) && old[i].To <= w.From-2 { // old words end well before: never needed again
			i++
		}
		for k := i; k < len(old) && old[k].From < w.To; k++ {
			if d := math.Min(w.To, old[k].To) - math.Max(w.From, old[k].From); d > 0 {
				if overlap[w.Label] == nil {
					overlap[w.Label] = map[string]float64{}
				}
				overlap[w.Label][old[k].Label] += d
			}
		}
	}
	byLabel := map[string]models.AssetSpeaker{}
	for _, v := range prev.Voices {
		byLabel[v.Label] = v
	}
	for nl, olds := range overlap {
		k := carried{}
		for ol, sec := range olds {
			v, ok := byLabel[ol]
			if !ok {
				continue
			}
			if newTotal[nl] > 0 && sec/newTotal[nl] >= carryReject {
				for _, r := range v.Rejected {
					if !contains(k.rejected, r) {
						k.rejected = append(k.rejected, r)
					}
				}
			}
			if v.Source != models.SpeakerByPerson || v.PersonID == "" || newTotal[nl] == 0 || oldTotal[ol] == 0 {
				continue
			}
			if sec/newTotal[nl] >= carryName && sec/oldTotal[ol] >= carryName {
				if p, err := loadPerson(ctx, a, v.PersonID); err == nil {
					k.person, k.confirmed, k.naming = p, v.Confirmed, v.Naming
					k.rejected = without(k.rejected, p.ID)
				}
			}
		}
		out[nl] = k
	}
	return out
}

// reapplyConfirmed gives every line a person fixed back to that person after
// the voices were found again (a voice for them is made when none carried
// their name).
func reapplyConfirmed(ctx context.Context, a *app.App, as *models.Asset, prev *Previous, carried map[string]carried, ch *Changes) error {
	if prev == nil {
		return nil
	}
	type fix struct {
		span   models.TimeSpan
		person string
		naming *models.Naming
	}
	var fixes []fix
	for _, v := range prev.Voices {
		if v.Source != models.SpeakerByPerson || v.PersonID == "" {
			continue
		}
		for _, s := range v.Confirmed {
			fixes = append(fixes, fix{s, v.PersonID, v.Naming})
		}
	}
	if len(fixes) == 0 {
		return nil
	}
	segs, err := loadSegments(ctx, a, as.ID)
	if err != nil {
		return err
	}
	vs, err := Voices(ctx, a, as.ID, true)
	if err != nil {
		return err
	}
	labelOf := map[string]string{}
	for _, v := range vs {
		if v.MergedInto == "" && v.PersonID != "" && v.Source == models.SpeakerByPerson {
			labelOf[v.PersonID] = v.Label
		}
	}
	changed := false
	for _, f := range fixes {
		p, err := loadPerson(ctx, a, f.person)
		if err != nil {
			continue
		}
		label, ok := labelOf[p.ID]
		if !ok {
			l, color := nextLabel(vs)
			now := time.Now().UTC()
			nv := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Label: l, Color: color, Manual: true,
				Name: p.Name, PersonID: p.ID, Source: models.SpeakerByPerson, Naming: f.naming, Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}
			if as.Speakers != nil {
				nv.Run = as.Speakers.Run
			}
			if err := a.DB.Insert(ctx, store.CSpeakers, nv); err != nil {
				return err
			}
			vs = append(vs, nv)
			label, labelOf[p.ID] = l, l
		}
		var moved int
		segs, moved, _ = relabel(segs, f.span.From, f.span.To, label)
		if moved > 0 {
			changed = true
		}
		for i := range vs {
			if vs[i].Label == label {
				vs[i].Confirmed = addSpan(vs[i].Confirmed, f.span)
			}
		}
		ch.Person(p.ID)
	}
	for _, v := range vs {
		if len(v.Confirmed) > 0 {
			if err := a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bsonSet("confirmed", v.Confirmed)); err != nil {
				return err
			}
		}
	}
	if !changed {
		return nil
	}
	if err := replaceSegments(ctx, a, as.ID, segs); err != nil {
		return err
	}
	return restat(ctx, a, as.ID, segs, vs)
}

// restat recounts every voice of a clip from its segments; voices nobody
// speaks in any more are removed (their people are rebuilt by the caller).
func restat(ctx context.Context, a *app.App, assetID string, segs []models.TranscriptSegment, vs []models.AssetSpeaker) error {
	runs := runsOf(segs)
	for _, v := range vs {
		if v.MergedInto != "" {
			continue
		}
		sec, words, turns, first := voiceStats(runs, v.Label)
		if words == 0 {
			if _, err := a.DB.C(store.CSpeakers).DeleteOne(ctx, map[string]any{"_id": v.ID, "workspaceId": a.WorkspaceID()}); err != nil {
				return err
			}
			// Voices folded into it go with it.
			if _, err := a.DB.C(store.CSpeakers).DeleteMany(ctx, map[string]any{"workspaceId": a.WorkspaceID(), "assetId": assetID, "mergedInto": v.Label}); err != nil {
				return err
			}
			continue
		}
		if err := a.DB.Update(ctx, store.CSpeakers, a.WorkspaceID(), v.ID, bsonSetAll(map[string]any{"seconds": sec, "words": words, "turns": turns, "first": first})); err != nil {
			return err
		}
	}
	return nil
}
