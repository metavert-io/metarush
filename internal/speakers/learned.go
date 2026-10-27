package speakers

import (
	"context"
	"math"
	"sort"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// What a person's voice is learned from (D158): the voices a person confirmed
// are them, each with a few seconds to hear and how alike it is to the others
// and to the voice in question, so a wrong one — a man's voice once
// learned as "Maya" — can be seen, heard and taken back where the warning
// shows up.

// LearnedVoice is one voice a person's voice is learned from.
type LearnedVoice struct {
	AssetID    string           `json:"assetId"`
	AssetTitle string           `json:"assetTitle"`
	Label      string           `json:"label"`
	Speaker    string           `json:"speaker" jsonschema:"description=How the voice is shown in its clip: Speaker N."`
	Sample     *models.TimeSpan `json:"sample,omitempty" jsonschema:"description=A few seconds to listen to (source seconds)."`
	HasVideo   bool             `json:"hasVideo"`
	Seconds    float64          `json:"seconds" jsonschema:"description=How long it speaks in that clip."`
	// Alike is how alike it sounds to the person's other learned voices and the
	// voice in question (0–1; absent when it has no fingerprint).
	Alike *float64 `json:"alike,omitempty"`
	// LikeThis is how alike it sounds to the voice in question.
	LikeThis *float64 `json:"likeThis,omitempty"`
	Pitch    float64  `json:"pitch,omitempty"`
	// Odd marks the one least like the others (a wrong name, most likely);
	// Why says how, in plain words.
	Odd bool   `json:"odd,omitempty"`
	Why string `json:"why,omitempty"`
}

// VoiceInQuestion is the voice being named, compared with what the person's
// voice is learned from.
type VoiceInQuestion struct {
	AssetID  string           `json:"assetId"`
	Label    string           `json:"label"`
	Speaker  string           `json:"speaker"`
	Sample   *models.TimeSpan `json:"sample,omitempty"`
	HasVideo bool             `json:"hasVideo"`
	// Score is how alike it sounds to the person's learned voice (0–1).
	Score       float64 `json:"score"`
	Pitch       float64 `json:"pitch,omitempty"`
	PersonPitch float64 `json:"personPitch,omitempty"`
	// Differ: its pitch is clearly unlike the person's (one much lower): the
	// "sound quite different" warning still holds.
	Differ bool `json:"differ"`
}

// Learned lists the voices personID's voice is learned from (their confirmed
// voices), compared with the voice in question when one is given (assetID and
// label, e.g. a voice being named as them), and marks the one least like the
// others.
func Learned(ctx context.Context, a *app.App, personID, assetID, label string) ([]LearnedVoice, *VoiceInQuestion, error) {
	p, err := loadPerson(ctx, a, personID)
	if err != nil {
		return nil, nil, err
	}
	all, err := confirmedVoices(ctx, a, p.ID)
	if err != nil {
		return nil, nil, err
	}
	var target *models.AssetSpeaker
	if assetID != "" && label != "" {
		vs, err := Voices(ctx, a, assetID, false)
		if err != nil {
			return nil, nil, err
		}
		v, err := ResolveLabel(vs, label)
		if err != nil {
			return nil, nil, err
		}
		target = v
	}
	var vs []models.AssetSpeaker
	for _, v := range all {
		if v.MergedInto != "" || (target != nil && v.ID == target.ID) {
			continue
		}
		vs = append(vs, v)
	}
	sort.SliceStable(vs, func(i, j int) bool { return vs[i].UpdatedAt.Before(vs[j].UpdatedAt) })
	// Pitches, measured where they aren't known yet.
	measure := append([]models.AssetSpeaker(nil), vs...)
	if target != nil {
		measure = append(measure, *target)
	}
	measure = EnsurePitch(ctx, a, measure, 6)
	for i := range vs {
		vs[i].Pitch, vs[i].Voiced = measure[i].Pitch, measure[i].Voiced
	}
	if target != nil {
		t := measure[len(measure)-1]
		target.Pitch, target.Voiced = t.Pitch, t.Voiced
	}
	withTarget := append([]models.AssetSpeaker(nil), vs...)
	if target != nil {
		withTarget = append(withTarget, *target)
	}
	titles := assetTitles(ctx, a, withTarget)
	video := hasVideo(ctx, a, vs, target)
	out := make([]LearnedVoice, 0, len(vs))
	for _, v := range vs {
		lv := LearnedVoice{AssetID: v.AssetID, AssetTitle: titles[v.AssetID], Label: v.Label, Speaker: models.SpeakerDisplayLabel(v.Label), HasVideo: video[v.AssetID],
			Seconds: v.Seconds, Pitch: v.Pitch}
		if len(v.Samples) > 0 {
			s := v.Samples[0]
			lv.Sample = &s
		}
		out = append(out, lv)
	}
	// How alike each is to the others (and to the voice in question).
	for i, v := range vs {
		if len(v.Centroid) == 0 {
			continue
		}
		sum, n := 0.0, 0
		for j, o := range vs {
			if j == i || len(o.Centroid) == 0 {
				continue
			}
			sum += voice.Cosine(v.Centroid, o.Centroid)
			n++
		}
		if target != nil && len(target.Centroid) > 0 {
			s := round3(voice.Cosine(v.Centroid, target.Centroid))
			out[i].LikeThis = &s
			sum += s
			n++
		}
		if n > 0 {
			s := round3(sum / float64(n))
			out[i].Alike = &s
		}
	}
	markOdd(out, vs, target, p.Name)
	var q *VoiceInQuestion
	if target != nil {
		q = &VoiceInQuestion{AssetID: target.AssetID, Label: target.Label, Speaker: models.SpeakerDisplayLabel(target.Label), HasVideo: video[target.AssetID], Pitch: target.Pitch}
		if len(target.Samples) > 0 {
			s := target.Samples[0]
			q.Sample = &s
		}
		pr := voice.Profile{ID: p.ID, Name: p.Name}
		for _, v := range vs {
			for i, x := range v.Exemplars {
				if i >= voice.MaxExemplarsPerSource {
					break
				}
				pr.Exemplars = append(pr.Exemplars, voice.ProfileExemplar{Source: sourceOf(v), Vec: x, Seconds: 2})
			}
		}
		pr = pr.Forget("")
		if len(target.Centroid) > 0 && len(pr.Exemplars) > 0 {
			q.Score = voice.Score(voice.Cluster{Centroid: target.Centroid}, pr).Score
		}
		pp := personPitch(vs)
		q.PersonPitch = pp.Hz
		q.Differ = voice.PitchDiffers(pitchOf(*target), pp)
	}
	return out, q, nil
}

// markOdd marks the learned voice least like the others: by fingerprint (below
// ConflictThreshold on average), or by pitch clearly apart from the rest.
func markOdd(out []LearnedVoice, vs []models.AssetSpeaker, target *models.AssetSpeaker, name string) {
	odd, worst := -1, math.Inf(1)
	for i := range out {
		if out[i].Alike != nil && *out[i].Alike < worst {
			odd, worst = i, *out[i].Alike
		}
	}
	others := len(vs) - 1
	if target != nil {
		others++
	}
	if odd >= 0 && worst < ConflictThreshold && others >= 1 {
		out[odd].Odd = true
		if len(vs) == 1 {
			out[odd].Why = "Sounds unlike this voice."
		} else {
			out[odd].Why = "Sounds unlike " + name + "'s other voices."
		}
		return
	}
	// Pitch: one voice clearly apart from all the others together.
	for i, v := range vs {
		var rest []voice.Pitch
		for j, o := range vs {
			if j != i {
				rest = append(rest, pitchOf(o))
			}
		}
		if target != nil {
			rest = append(rest, pitchOf(*target))
		}
		if len(rest) == 0 {
			continue
		}
		rp := voice.CombinePitch(rest)
		if voice.PitchDiffers(pitchOf(v), rp) {
			out[i].Odd = true
			if v.Pitch < rp.Hz {
				out[i].Why = "Much lower than " + name + "'s other voices."
			} else {
				out[i].Why = "Much higher than " + name + "'s other voices."
			}
			if len(vs) == 1 {
				out[i].Why = "Much lower than this voice."
				if v.Pitch > rp.Hz {
					out[i].Why = "Much higher than this voice."
				}
			}
			return
		}
	}
}

// hasVideo says which of the clips have a picture.
func hasVideo(ctx context.Context, a *app.App, vs []models.AssetSpeaker, target *models.AssetSpeaker) map[string]bool {
	ids := []string{}
	for _, v := range vs {
		ids = append(ids, v.AssetID)
	}
	if target != nil {
		ids = append(ids, target.AssetID)
	}
	var as []models.Asset
	_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &as)
	out := map[string]bool{}
	for _, x := range as {
		out[x.ID] = x.Media.HasVideo
	}
	return out
}
