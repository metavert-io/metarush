package speakers

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Undoing "this part is Lena, not Maya" (D145): the stretch's words go
// back to the voices that had them, voices the fix left without words come
// back, the confirmed stretches are as they were, and the voices are learned
// again from their own speech, so nothing of the fix stays in anyone's voice.

// CanRestore says why a fix of who says a stretch can't be taken back ("" when it can).
func CanRestore(as *models.Asset, segs []models.TranscriptSegment, from, to float64, fix models.SpeakerFix) string {
	run := ""
	if as.Speakers != nil {
		run = as.Speakers.Run
	}
	if fix.Run != run {
		return "Who speaks in this clip was worked out again since this fix, so it can't be undone. Give the line to the right person instead."
	}
	for _, r := range wordRuns(segs, from, to) {
		if r.Label != fix.Label {
			return "Who says those words was changed again since. Give the line to the right person instead."
		}
	}
	return ""
}

// RestoreResult is what taking back a fix of who says a stretch changed in
// the library (filled in by its Learning).
type RestoreResult struct {
	// Relearned: the voices were fingerprinted again (false when voice
	// matching is not installed here).
	Relearned bool
	Library   *LibraryChange
}

// Restore takes back a fix of who says [from, to) of a clip.
func Restore(ctx context.Context, a *app.App, assetID string, from, to float64, fix models.SpeakerFix) (*LibraryChange, error) {
	res, learn, err := RestoreWords(ctx, a, assetID, from, to, fix)
	if err != nil {
		return nil, err
	}
	if err := learn(ctx); err != nil {
		return nil, err
	}
	return res.Library, nil
}

// RestoreWords is Restore up to the moment the words are back: the lines, the
// voices and the names on the lines are as before the fix. The Learning it
// returns learns the voices again and brings the library up to date.
func RestoreWords(ctx context.Context, a *app.App, assetID string, from, to float64, fix models.SpeakerFix) (*RestoreResult, Learning, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, nil, err
	}
	segs, err := loadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, nil, err
	}
	if why := CanRestore(&as, segs, from, to, fix); why != "" {
		return nil, nil, &RestoreRefused{Why: why}
	}
	all, err := Voices(ctx, a, as.ID, true)
	if err != nil {
		return nil, nil, err
	}
	ch := newChanges()
	ch.Asset(as.ID)
	// Voices folded into the fix's voice since, only because they then
	// sounded like the person it named (recognized from the stretch the fix
	// confirmed), come apart first: their words go back to them.
	apart, err := unmergeSince(ctx, a, all, fix, ch)
	if err != nil {
		return nil, nil, err
	}
	if len(apart) > 0 {
		if all, err = Voices(ctx, a, as.ID, true); err != nil {
			return nil, nil, err
		}
		if segs, err = loadSegments(ctx, a, as.ID); err != nil {
			return nil, nil, err
		}
	}
	now := time.Now().UTC()
	before := map[string]models.VoiceBefore{}
	for _, v := range fix.Voices {
		before[v.ID] = v
	}
	// Voices the fix left without words come back (their fingerprints are
	// learned again below).
	have := map[string]bool{}
	for _, v := range all {
		have[v.ID] = true
	}
	for _, b := range fix.Voices {
		if have[b.ID] {
			continue
		}
		used := false
		for _, r := range fix.Before {
			if r.Label == b.Label {
				used = true
			}
		}
		if !used {
			continue
		}
		nv := models.AssetSpeaker{ID: b.ID, WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Run: b.Run, Label: b.Label, Name: b.Name, PersonID: b.PersonID,
			Source: b.Source, Score: b.Score, Suggestions: b.Suggestions, Rejected: b.Rejected, Confirmed: b.Confirmed, Manual: b.Manual, Color: b.Color,
			MergedInto: b.MergedInto, Naming: b.Naming, Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CSpeakers, nv); err != nil && !store.IsDuplicateKey(err) {
			return nil, nil, err
		}
		all = append(all, nv)
	}
	// The words go back to who had them.
	newSegs := segs
	for _, r := range fix.Before {
		newSegs, _, _ = relabel(newSegs, r.From, r.To+0.0005, r.Label)
	}
	span := models.TimeSpan{From: round3(from), To: round3(to)}
	touched := map[string]bool{fix.Label: true}
	for _, r := range fix.Before {
		touched[r.Label] = true
	}
	for _, l := range apart {
		touched[l] = true
	}
	for i := range all {
		v := &all[i]
		b, known := before[v.ID]
		// Where a person confirmed each voice speaks: as it was inside the stretch.
		v.Confirmed = cutSpan(v.Confirmed, span)
		if known {
			for _, c := range b.Confirmed {
				lo, hi := max(c.From, span.From), min(c.To, span.To)
				if hi > lo {
					v.Confirmed = addSpan(v.Confirmed, models.TimeSpan{From: lo, To: hi})
				}
			}
		}
		// The voice the fix named goes back to who it was, unless someone named it since.
		if v.Label == fix.Label && known && v.Name == fix.Name && v.PersonID == fix.PersonID {
			if v.PersonID != b.PersonID {
				ch.Person(v.PersonID, b.PersonID)
			}
			v.Name, v.PersonID, v.Source, v.Score, v.Suggestions, v.Rejected, v.Naming = b.Name, b.PersonID, b.Source, b.Score, b.Suggestions, b.Rejected, b.Naming
		}
		if touched[v.Label] && v.Source == models.SpeakerByPerson {
			ch.Person(v.PersonID)
		}
	}
	for l := range touched {
		for _, v := range all {
			if v.MergedInto == l {
				touched[v.Label] = true
			}
		}
	}
	if err := replaceSegments(ctx, a, as.ID, newSegs); err != nil {
		return nil, nil, err
	}
	kept, err := settle(ctx, a, &as, all, touched, newSegs, ch, now)
	if err != nil {
		return nil, nil, err
	}
	a.Audit(ctx, "speakers.restore", "asset", as.ID, "", map[string]any{"from": from, "to": to, "label": fix.Label})
	res := &RestoreResult{}
	return res, func(ctx context.Context) error {
		var err error
		res.Relearned, res.Library, err = learn(ctx, a, &as, kept, touched, newSegs, ch)
		return err
	}, nil
}

// unmergeSince separates the voices that were combined into the fix's voice
// automatically after the fix (they were their own voices when it was made);
// merges a person confirmed, and merges from before the fix, stay.
func unmergeSince(ctx context.Context, a *app.App, all []models.AssetSpeaker, fix models.SpeakerFix, ch *Changes) ([]string, error) {
	was := map[string]models.VoiceBefore{}
	for _, b := range fix.Voices {
		was[b.ID] = b
	}
	var keep *models.AssetSpeaker
	for i := range all {
		if all[i].Label == fix.Label && all[i].MergedInto == "" {
			keep = &all[i]
		}
	}
	if keep == nil {
		return nil, nil
	}
	// The latest merge first (each restores the kept voice as it was before it).
	var drops []models.AssetSpeaker
	for _, v := range all {
		b, known := was[v.ID]
		if v.MergedInto == fix.Label && v.MergedBy == models.SpeakerByAuto && (!known || b.MergedInto == "") {
			drops = append(drops, v)
		}
	}
	sort.SliceStable(drops, func(i, j int) bool { return drops[i].UpdatedAt.After(drops[j].UpdatedAt) })
	var apart []string
	for _, d := range drops {
		k, err := unmergeOne(ctx, a, *keep, d, nil, nil, ch)
		if errors.Is(err, ErrLegacyMerge) {
			continue
		}
		if err != nil {
			return apart, err
		}
		*keep = k
		apart = append(apart, d.Label)
	}
	return apart, nil
}

// RestoreRefused says why a fix of who speaks can't be taken back.
type RestoreRefused struct{ Why string }

func (e *RestoreRefused) Error() string { return e.Why }
