package sound

import (
	"context"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// cut is a revision resolved for sound work.
type cut struct {
	rev   *models.Revision
	spec  *editspec.Spec
	facts editspec.Facts
	plan  *editspec.RenderPlan
	// members are the conversations' member recordings → the member's name.
	members map[string]string
}

func loadCut(ctx context.Context, a *app.App, revisionID string) (*cut, error) {
	if revisionID == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose the version of the cut whose sound this is.")
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), revisionID, &rev); err != nil {
		return nil, apperr.New(apperr.NotFound, "That version of the cut isn't in this workspace.").WithDetails(map[string]any{"revisionId": revisionID})
	}
	spec := editspec.Clone(&rev.Spec)
	editspec.Normalize(spec)
	facts, err := ops.LoadFacts(ctx, a, rev.ProjectID, spec)
	if err != nil {
		return nil, err
	}
	plan := editspec.Resolve(editspec.Clone(spec), facts, rev.ID, rev.ProjectID, rev.SpecHash)
	return &cut{rev: &rev, spec: spec, facts: facts, plan: plan, members: memberVoices(spec)}, nil
}

// memberVoices are the conversations' member recordings by asset → the
// member's name (D101): a member's recording is their own microphone, so
// every moment of it is theirs — the people Sound lists, their levels and
// their clearer copies follow it.
func memberVoices(s *editspec.Spec) map[string]string {
	names := map[string]string{}
	keys := make([]string, 0, len(s.Conversations))
	for n := range s.Conversations {
		keys = append(keys, n)
	}
	sort.Strings(keys)
	for _, name := range keys {
		for _, m := range s.Conversations[name].Members {
			if m.UseOf() != editspec.UsePicture && strings.TrimSpace(m.Name) != "" {
				names[m.AssetID] = m.Name
			}
		}
	}
	return names
}

// transcript is a recording's transcript as Sound sees its people: a
// conversation member's recording carries the member's name on every segment.
func (c *cut) transcript(id string) []editspec.TranscriptSegment {
	segs := c.facts.Transcript(id)
	name, ok := c.members[id]
	if !ok {
		return segs
	}
	out := make([]editspec.TranscriptSegment, len(segs))
	for i, s := range segs {
		s.Speaker = name
		out[i] = s
	}
	return out
}

// asset is a loaded asset document of the cut (nil when unknown).
func (c *cut) asset(id string) *models.Asset { return ops.AssetOf(c.facts, id) }

// use is a stretch of a recording the cut plays (source seconds).
type use struct {
	asset    string
	from, to float64
	clip     string // the clip's label or ID ("" for a dialogue track)
}

// uses are the stretches of each recording heard in the cut: every clip's
// sound window (split edits and blends included) and every dialogue track.
func (c *cut) uses() []use {
	var out []use
	fps := float64(c.plan.FPS)
	sourceMuted := c.plan.Mix != nil && c.plan.Mix.Buses[editspec.BusSource].Muted
	for i, pc := range c.plan.Clips {
		if pc.Muted || !pc.HasAudio || sourceMuted || i >= len(c.spec.Clips) || editspec.IsConversation(pc.AssetID) {
			continue // a conversation's sound is its members' tracks (below)
		}
		from, dur := pc.From, pc.DurationInFrames
		if pc.Audio != nil {
			from, dur = pc.Audio.From, pc.Audio.DurationInFrames
		}
		rate := pc.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		s0 := pc.SourceIn + float64(from-pc.From)/fps*rate
		s1 := pc.SourceIn + float64(from+dur-pc.From)/fps*rate
		name := c.spec.Clips[i].Label
		if name == "" {
			name = c.spec.Clips[i].ID
		}
		out = append(out, use{asset: pc.AssetID, from: math.Max(0, s0), to: s1, clip: name})
	}
	for _, t := range c.spec.Audio {
		if t.Role == editspec.RoleDialogue {
			out = append(out, use{asset: t.AssetID, from: t.In, to: t.Out})
		}
	}
	// Conversation members: each one's own track over the stretch it plays (D101).
	for _, a := range c.plan.Audio {
		if a.Conversation == "" || a.DurationInFrames <= 0 {
			continue
		}
		rate := a.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		s0 := float64(a.TrimBefore) / fps
		out = append(out, use{asset: a.AssetID, from: s0, to: s0 + float64(a.DurationInFrames)/fps*rate, clip: a.Conversation})
	}
	return out
}

// trackTitle names an audio track the way people know it: its file's title.
func (c *cut) trackTitle(id string) string {
	for _, t := range c.spec.Audio {
		if t.ID == id {
			if as := c.asset(t.AssetID); as != nil {
				return as.DisplayTitle()
			}
		}
	}
	return id
}

// window is one stretch of a recording a copy covers (whole seconds).
type window struct {
	asset      string
	start, end float64
}

// copyPad is how much of the recording around what plays a copy covers (so
// small trims keep using it), and copyJoin how close two stretches may be to
// share one copy.
const (
	copyPad  = 2.0
	copyJoin = 10.0
)

// WindowSeconds is how much of a recording making a stretch of it clearer
// processes (and the cloud charges): the stretch padded and rounded out to
// whole seconds, within the recording — what a sample's estimate prices (D294).
func WindowSeconds(from, to, duration float64) float64 {
	s := math.Max(0, math.Floor(from-copyPad))
	e := math.Ceil(to + copyPad)
	if duration > 0 {
		e = math.Min(e, math.Ceil(duration))
	}
	return math.Max(0, e-s)
}

// windows groups each recording's uses into the stretches copies cover.
func windows(uses []use, duration func(asset string) float64) []window {
	by := map[string][]use{}
	var order []string
	for _, u := range uses {
		if _, ok := by[u.asset]; !ok {
			order = append(order, u.asset)
		}
		by[u.asset] = append(by[u.asset], u)
	}
	var out []window
	for _, asset := range order {
		us := by[asset]
		sort.Slice(us, func(i, j int) bool { return us[i].from < us[j].from })
		dur := duration(asset)
		var cur *window
		for _, u := range us {
			s := math.Max(0, math.Floor(u.from-copyPad))
			e := math.Ceil(u.to + copyPad)
			if dur > 0 {
				e = math.Min(e, math.Ceil(dur))
			}
			if cur != nil && s <= cur.end+copyJoin {
				cur.end = math.Max(cur.end, e)
				continue
			}
			if cur != nil {
				out = append(out, *cur)
			}
			cur = &window{asset: asset, start: s, end: e}
		}
		if cur != nil {
			out = append(out, *cur)
		}
	}
	return out
}

// speakers lists the people heard in the cut, by name, in the order they first
// speak, with how long each speaks (seconds of their stretches in what plays).
func (c *cut) speakers() ([]string, map[string]float64) {
	secs := map[string]float64{}
	var order []string
	for _, u := range c.uses() {
		for _, sp := range editspec.SpeakerSpans(c.transcript(u.asset), u.from, u.to) {
			if strings.TrimSpace(sp.Speaker) == "" {
				continue
			}
			if _, ok := secs[sp.Speaker]; !ok {
				order = append(order, sp.Speaker)
			}
			secs[sp.Speaker] += sp.To - sp.From
		}
	}
	return order, secs
}

// speechSeconds is how much of the transcript's speech (segments) falls in a stretch.
func speechSeconds(segs []editspec.TranscriptSegment, speaker string, from, to float64) float64 {
	var s float64
	for _, sg := range segs {
		if speaker != "" && strings.TrimSpace(sg.Speaker) != speaker {
			continue
		}
		a, b := math.Max(sg.From, from), math.Min(sg.To, to)
		if b > a {
			s += b - a
		}
	}
	return s
}
