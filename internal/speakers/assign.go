package speakers

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// ErrNoWords means nothing is said in the stretch someone tried to reassign.
var ErrNoWords = errors.New("no words are said there")

// AssignInput says who really says a stretch of a clip (fixing who said a line).
type AssignInput struct {
	AssetID  string
	From, To float64
	// The person: an ID, or a name (an existing person, or with NewPerson a new one).
	PersonID  string
	Name      string
	NewPerson bool
	// Unsure ("Not sure"): the words go to a voice nobody has named yet, which
	// becomes a "who is this?" question.
	Unsure bool
}

// AssignResult is what reassigning changed.
type AssignResult struct {
	Voice      models.AssetSpeaker `json:"voice"`
	Person     *models.Person      `json:"person,omitempty"`
	CreatedNew bool                `json:"createdPerson"`
	// Moved counts the words that changed speaker; From names who had them.
	Moved int      `json:"moved"`
	From  []string `json:"from"`
	// Relearned: the voices were fingerprinted again from the speech a person
	// confirmed (false when voice matching is not installed on this machine).
	Relearned bool           `json:"relearned"`
	Library   *LibraryChange `json:"-"`
	// Before is who had the stretch's words, and Voices the clip's voices as
	// they were (what undoing the fix puts back); Run is the detection the
	// voices come from.
	Before []models.SpeakerRun  `json:"-"`
	Voices []models.VoiceBefore `json:"-"`
	Run    string               `json:"-"`
}

// Learning is what is left of a change of who says what once its words have
// moved (AssignWords, RestoreWords): the voices it touched are fingerprinted
// again from their own speech and the library catches up — the profiles
// involved rebuilt, automatic labels and suggestions everywhere re-scored,
// questions brought up to date. It is the slow part of the change, so whoever
// records the change (a clip's transcript history, D145) records it first:
// the record never lags behind the words people see.
type Learning func(ctx context.Context) error

// Assign gives the words said in [from, to) of a clip to a person (or, when
// unsure, to a voice nobody named yet), splitting transcript lines where the
// speaker changes. The span is remembered as confirmed for that voice (it
// survives detection running again). The voices that changed are fingerprinted
// again from their own speech, so a person's voice profile only learns from
// what a person confirmed; the profiles involved are rebuilt and the library
// re-scored.
func Assign(ctx context.Context, a *app.App, in AssignInput) (*AssignResult, error) {
	res, learn, err := AssignWords(ctx, a, in)
	if err != nil {
		return nil, err
	}
	if err := learn(ctx); err != nil {
		return nil, err
	}
	return res, nil
}

// AssignWords is Assign up to the moment the words have moved: the lines, the
// voices and the names on the lines are as the person said. The Learning it
// returns does the rest and fills in the result's Relearned, Library and Voice.
func AssignWords(ctx context.Context, a *app.App, in AssignInput) (*AssignResult, Learning, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, nil, err
	}
	if in.To <= in.From {
		return nil, nil, errors.New("the end must be after the start")
	}
	all, err := Voices(ctx, a, as.ID, true)
	if err != nil {
		return nil, nil, err
	}
	res := &AssignResult{From: []string{}, Voices: voicesBefore(all)}
	if as.Speakers != nil {
		res.Run = as.Speakers.Run
	}
	var person *models.Person
	if !in.Unsure {
		p, created, err := personFor(ctx, a, in.Name, in.PersonID, in.NewPerson)
		if err != nil {
			return nil, nil, err
		}
		person, res.CreatedNew = p, created
		res.Person = p
	}
	// The voice the words go to.
	var target *models.AssetSpeaker
	for i := range all {
		v := &all[i]
		if v.MergedInto != "" {
			continue
		}
		if person != nil && v.PersonID == person.ID && v.Source == models.SpeakerByPerson {
			target = v
			break
		}
		if person == nil && v.Manual && v.Name == "" {
			target = v
		}
	}
	if target == nil && person != nil {
		// An automatic label for that person in this clip becomes a confirmed one.
		for i := range all {
			if v := &all[i]; v.MergedInto == "" && v.PersonID == person.ID {
				target = v
				break
			}
		}
	}
	now := time.Now().UTC()
	if target == nil {
		label, color := nextLabel(all)
		nv := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Label: label, Color: color, Manual: true,
			Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}
		if as.Speakers != nil {
			nv.Run = as.Speakers.Run
		}
		if err := a.DB.Insert(ctx, store.CSpeakers, nv); err != nil {
			return nil, nil, err
		}
		all = append(all, nv)
		target = &all[len(all)-1]
	}
	if person != nil {
		target.Name, target.PersonID, target.Source, target.Score, target.Suggestions = person.Name, person.ID, models.SpeakerByPerson, 0, nil
		target.Naming = naming.Record(ctx, a.Actor) // who said it: only a person's name counts for consent (D274)
		target.Rejected = without(target.Rejected, person.ID)
	}
	segs, err := loadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, nil, err
	}
	res.Before = wordRuns(segs, in.From, in.To)
	newSegs, moved, fromLabels := relabel(segs, in.From, in.To, target.Label)
	if moved == 0 {
		// Nothing to move: the words already are this voice's (still a confirmation).
		inside := false
		for _, r := range runsOf(segs) {
			if r.To > in.From && r.From < in.To {
				inside = true
				break
			}
		}
		if !inside {
			return nil, nil, ErrNoWords
		}
	}
	res.Moved = moved
	byLabel := map[string]*models.AssetSpeaker{}
	for i := range all {
		byLabel[all[i].Label] = &all[i]
	}
	ch := newChanges()
	ch.Asset(as.ID)
	if person != nil {
		ch.Person(person.ID)
	}
	span := models.TimeSpan{From: round3(in.From), To: round3(in.To)}
	for _, l := range fromLabels {
		if v := byLabel[l]; v != nil {
			res.From = append(res.From, v.DisplayName())
			if v.Source == models.SpeakerByPerson {
				ch.Person(v.PersonID)
			}
		}
	}
	for i := range all {
		v := &all[i]
		if v.Label == target.Label {
			v.Confirmed = addSpan(v.Confirmed, span)
		} else {
			v.Confirmed = cutSpan(v.Confirmed, span)
		}
	}
	if err := replaceSegments(ctx, a, as.ID, newSegs); err != nil {
		return nil, nil, err
	}
	// The voices that changed are learned again, from their own speech only.
	touched := map[string]bool{target.Label: true}
	for _, l := range fromLabels {
		touched[l] = true
		for _, v := range all {
			if v.MergedInto == l { // voices folded into a voice that lost words
				touched[v.Label] = true
			}
		}
	}
	kept, err := settle(ctx, a, &as, all, touched, newSegs, ch, now)
	if err != nil {
		return nil, nil, err
	}
	for _, v := range kept {
		if v.Label == target.Label && v.MergedInto == "" {
			res.Voice = v
		}
	}
	who := "someone not named yet"
	if person != nil {
		who = person.ID
	}
	a.Audit(ctx, "speakers.assign", "asset", as.ID, "", map[string]any{"from": in.From, "to": in.To, "person": who, "label": target.Label, "moved": moved})
	return res, func(ctx context.Context) error {
		var err error
		if res.Relearned, res.Library, err = learn(ctx, a, &as, kept, touched, newSegs, ch); err != nil {
			return err
		}
		vs, _ := Voices(ctx, a, as.ID, false)
		for _, v := range vs {
			if v.Label == target.Label {
				res.Voice = v
			}
		}
		return nil
	}, nil
}

// settle brings a clip's voices in line with its lines at once: the touched
// voices are counted again, voices nobody speaks as any more go, the voices
// are saved and their names put on the lines — who says what, as people see
// it and as a version saved now would keep it. It returns the voices that
// remain; learn, the slow part, follows.
func settle(ctx context.Context, a *app.App, as *models.Asset, all []models.AssetSpeaker, touched map[string]bool, segs []models.TranscriptSegment,
	ch *Changes, now time.Time) ([]models.AssetSpeaker, error) {
	runs := runsOf(segs)
	kept := make([]models.AssetSpeaker, 0, len(all))
	for _, v := range all {
		if touched[v.Label] && v.MergedInto == "" {
			v.Seconds, v.Words, v.Turns, v.First = voiceStats(runs, v.Label)
			if v.Words == 0 {
				// Nobody speaks as this voice any more.
				ch.Person(v.PersonID)
				if _, err := a.DB.C(store.CSpeakers).DeleteOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID()}); err != nil {
					return nil, err
				}
				continue
			}
		}
		v.UpdatedAt = now
		if v.Samples == nil {
			v.Samples = []models.TimeSpan{}
		}
		if err := saveVoice(ctx, a, v); err != nil {
			return nil, err
		}
		kept = append(kept, v)
	}
	return kept, SyncNames(ctx, a, as.ID)
}

// learn fingerprints the touched voices of a clip again from their own speech
// (and voices folded into them), so a person's voice only ever learns from
// what a person confirmed, and applies what changed to the library. relearned
// is false when voice matching isn't installed here.
func learn(ctx context.Context, a *app.App, as *models.Asset, kept []models.AssetSpeaker, touched map[string]bool, segs []models.TranscriptSegment,
	ch *Changes) (bool, *LibraryChange, error) {
	emb := embedFor(a)
	var pcm []float32
	if emb != nil && as.Media.HasAudio {
		if p, err := pcmFor(ctx, a, as); err == nil {
			pcm = p
		}
	}
	runs := runsOf(segs)
	for i := range kept {
		v := &kept[i]
		if !touched[v.Label] || pcm == nil {
			continue
		}
		own, others := spansFor(runs, *v)
		c, err := voice.Describe(v.Label, own, others, pcm, emb, voice.DefaultParams)
		if err != nil {
			return false, nil, err
		}
		v.Fingerprinted, v.Centroid, v.Pitch, v.Voiced = c.Embedded, c.Centroid, c.Pitch, c.Voiced
		v.Exemplars, v.ExSeconds = nil, nil
		for _, x := range c.Exemplars {
			v.Exemplars = append(v.Exemplars, x.Vec)
			v.ExSeconds = append(v.ExSeconds, x.Len())
		}
		if len(c.Centroid) > 0 {
			v.Model = voice.ModelName
		}
		if len(c.Samples) > 0 {
			v.Samples = nil
			for _, s := range c.Samples {
				v.Samples = append(v.Samples, models.TimeSpan{From: s.From, To: s.To})
			}
		}
		if err := saveVoice(ctx, a, *v); err != nil {
			return false, nil, err
		}
	}
	lib, err := ch.Apply(ctx, a)
	if err != nil {
		return false, nil, err
	}
	return pcm != nil, lib, nil
}

// voicesBefore keeps what undoing a fix needs of each voice (no fingerprints).
func voicesBefore(all []models.AssetSpeaker) []models.VoiceBefore {
	out := make([]models.VoiceBefore, 0, len(all))
	for _, v := range all {
		out = append(out, models.VoiceBefore{ID: v.ID, Label: v.Label, Name: v.Name, PersonID: v.PersonID, Source: v.Source, Score: v.Score,
			Suggestions: v.Suggestions, Rejected: v.Rejected, Confirmed: v.Confirmed, Manual: v.Manual, Color: v.Color, Run: v.Run, MergedInto: v.MergedInto, Naming: v.Naming})
	}
	return out
}

// wordRuns lists who says the words in [from, to) (by their middle), as runs.
func wordRuns(segs []models.TranscriptSegment, from, to float64) []models.SpeakerRun {
	var out []models.SpeakerRun
	add := func(f, t float64, label, name string) {
		if n := len(out); n > 0 && out[n-1].Label == label {
			out[n-1].To = round3(t)
			return
		}
		out = append(out, models.SpeakerRun{From: round3(f), To: round3(t), Label: label, Name: name})
	}
	for _, s := range segs {
		if len(s.Words) == 0 {
			if m := (s.From + s.To) / 2; m >= from && m < to {
				add(s.From, s.To, s.Speaker, s.SpeakerName)
			}
			continue
		}
		for _, w := range s.Words {
			if m := (w.From + w.To) / 2; m >= from && m < to {
				add(w.From, w.To, s.Speaker, s.SpeakerName)
			}
		}
	}
	return out
}

// Describe says in plain words what an assignment did.
func (r *AssignResult) Describe() string {
	who := "someone you haven't named yet"
	if r.Person != nil {
		who = r.Person.Name
	}
	if r.Moved == 0 {
		return fmt.Sprintf("Those words were already %s's.", who)
	}
	if r.Person == nil {
		return "Got it: those words are someone you haven't named yet. We'll ask who it is."
	}
	return fmt.Sprintf("Got it: those words are %s's now.", who)
}

func bsonSet(k string, v any) bson.M {
	return bson.M{"$set": bson.M{k: v, "updatedAt": time.Now().UTC()}}
}

func bsonSetAll(m map[string]any) bson.M {
	set := bson.M{"updatedAt": time.Now().UTC()}
	for k, v := range m {
		set[k] = v
	}
	return bson.M{"$set": set}
}
