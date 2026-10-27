package conversation

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Sync methods.
const (
	MethodAudio    = "audio"    // by their sound (cross-correlation)
	MethodTogether = "together" // the files start together (a provider's aligned files)
	MethodClap     = "clap"     // the sharpest loud transient in each (a clap, a slate)
	MethodMark     = "mark"     // moments a person picked in each recording
)

// defaultSearch is how far apart recordings may start (seconds either way).
const defaultSearch = 600.0

// syncRun aligns a conversation's members (in place). initial: a new
// conversation, whose clock then starts with its earliest recording;
// afterwards the reference keeps its place, so cuts' times stay meaningful.
func syncRun(ctx context.Context, a *app.App, c *models.Conversation, method string, marks map[string]float64, search float64, initial bool) error {
	t0 := time.Now()
	as, err := assets(ctx, a, c)
	if err != nil {
		return err
	}
	if search <= 0 {
		search = defaultSearch
	}
	ref := reference(c, as)
	if ref < 0 {
		return invalid("none of the recordings has sound, so they can't be lined up by sound; use --mark or nudge them")
	}
	refM := &c.Members[ref]
	switch method {
	case "", MethodAudio:
		method = MethodAudio
		err = syncAudio(ctx, a, c, as, ref, search)
	case MethodTogether:
		// A call app's files that start together are as sure as it gets;
		// files only taken to start together are a guess.
		how, sure := "together", 0.7
		if c.Source != nil && c.Source.Aligned {
			how, sure = "app", 0.95
		}
		for i := range c.Members {
			m := &c.Members[i]
			m.Start, m.Drift = refM.Start, 0
			m.Align = &models.MemberAlign{Method: how, Confidence: sure, Against: refM.ID, At: time.Now().UTC()}
		}
	case MethodClap:
		err = syncClap(ctx, a, c, as, ref)
	case MethodMark:
		err = syncMarks(c, marks)
	default:
		return invalid("line recordings up by audio, together, clap or mark, not %q", method)
	}
	if err != nil {
		return err
	}
	if c.Members[ref].Align == nil || method != MethodMark {
		c.Members[ref].Align = &models.MemberAlign{Method: "reference", Confidence: 1, At: time.Now().UTC()}
	}
	if initial {
		// The clock starts with the earliest recording.
		lo := math.Inf(1)
		for _, m := range c.Members {
			lo = math.Min(lo, m.Start)
		}
		for i := range c.Members {
			c.Members[i].Start = round3(c.Members[i].Start - lo)
		}
	}
	conf := 1.0
	for _, m := range c.Members {
		if m.Align != nil {
			conf = math.Min(conf, m.Align.Confidence)
		}
	}
	c.Length = length(c, as)
	c.Sync = &models.ConversationSync{Reference: c.Members[ref].ID, Method: method, Confidence: conf, Search: search,
		Took: math.Round(time.Since(t0).Seconds()*10) / 10, At: time.Now().UTC(), By: a.Actor}
	return nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// reference is the member the others are aligned to: a shared picture with
// sound (a call's main recording has everyone in it), else the longest
// recording with sound; -1 when nothing has sound.
func reference(c *models.Conversation, as map[string]*models.Asset) int {
	best, bestDur, bestMix := -1, 0.0, false
	for i, m := range c.Members {
		x := as[m.AssetID]
		if x == nil || !x.Media.HasAudio || m.Muted {
			continue
		}
		mix := m.UseOf() == editspec.UsePicture
		d := x.Media.Duration
		if best < 0 || (mix && !bestMix) || (mix == bestMix && d > bestDur+0.01) {
			best, bestDur, bestMix = i, d, mix
		}
	}
	return best
}

// syncAudio lines every member with sound up with the reference by their
// sound; members without sound start with the reference.
func syncAudio(ctx context.Context, a *app.App, c *models.Conversation, as map[string]*models.Asset, ref int, search float64) error {
	refM := c.Members[ref]
	isMix := refM.UseOf() == editspec.UsePicture
	snd := map[int]*sound{}
	total := len(c.Members)
	done := 0
	read := func(i int) (*sound, error) {
		if s, ok := snd[i]; ok {
			return s, nil
		}
		x := as[c.Members[i].AssetID]
		if x == nil {
			return nil, invalid("member %s's recording isn't in this workspace any more", c.Members[i].Name)
		}
		_ = voiceover.Step(ctx, done, total, "Listening to "+possessive(c.Members[i].Name)+" recording")
		p, err := local(ctx, a, x)
		if err != nil {
			return nil, err
		}
		s, err := decodeSound(ctx, a.Tools, p, 0, 0)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", x.DisplayTitle(), err)
		}
		snd[i] = s
		done++
		return s, nil
	}
	rs, err := read(ref)
	if err != nil {
		return err
	}
	rRate := refM.Rate()
	for i := range c.Members {
		if i == ref {
			continue
		}
		m := &c.Members[i]
		x := as[m.AssetID]
		if x == nil || !x.Media.HasAudio {
			m.Start, m.Drift = refM.Start, 0
			m.Align = &models.MemberAlign{Method: "together", Confidence: 0.3, Against: refM.ID, At: time.Now().UTC()}
			continue
		}
		ms, err := read(i)
		if err != nil {
			return err
		}
		_ = voiceover.Step(ctx, done, total, "Lining up "+possessive(m.Name)+" recording")
		r := align(rs, ms, syncOptions{search: search, mix: isMix})
		start, rate := place(refM.Start, rRate, r.lag, r.slope)
		al := &models.MemberAlign{Method: r.method, Confidence: r.confidence, Against: refM.ID, Delay: round3(r.delay), Windows: r.windows, Agree: r.agree,
			Residual: math.Round(r.residual*10000) / 10, At: time.Now().UTC()}
		if r.z < zSome {
			// Nothing in common to line up on (everyone wore headphones?): the
			// files' own starts, said plainly, until someone nudges or marks them.
			start, rate = refM.Start, rRate
			al.Method, al.Confidence = "together", 0.2
		}
		m.Start = round3(start)
		m.Drift = math.Round((rate-1)*1e7) / 10
		m.Align = al
	}
	return nil
}

// syncClap lines members up on the sharpest loud transient in the first
// minute of each (a clap or a slate), refined to the loudest sample.
func syncClap(ctx context.Context, a *app.App, c *models.Conversation, as map[string]*models.Asset, ref int) error {
	type clapAt struct {
		tau float64
		ok  bool
	}
	claps := make([]clapAt, len(c.Members))
	for i, m := range c.Members {
		x := as[m.AssetID]
		if x == nil || !x.Media.HasAudio {
			continue
		}
		p, err := local(ctx, a, x)
		if err != nil {
			return err
		}
		s, err := decodeSound(ctx, a.Tools, p, 0, 60)
		if err != nil {
			return err
		}
		t, ok := clapIn(s, 60)
		if !ok {
			continue
		}
		if samples, err := decodeSamples(ctx, a.Tools, p, t-0.05, 0.12); err == nil && len(samples) > 0 {
			t = refineClap(samples, math.Max(0, t-0.05))
		}
		claps[i] = clapAt{tau: t, ok: true}
	}
	if !claps[ref].ok {
		return invalid("no clap was found in the first minute of %s recording; mark a moment instead (--mark)", possessive(c.Members[ref].Name))
	}
	refM := c.Members[ref]
	at := refM.ClockAt(claps[ref].tau)
	var missing []string
	for i := range c.Members {
		if i == ref {
			continue
		}
		m := &c.Members[i]
		if !claps[i].ok {
			missing = append(missing, m.Name)
			continue
		}
		m.Start = round3(at - claps[i].tau/m.Rate())
		m.Align = &models.MemberAlign{Method: "clap", Confidence: 0.9, Against: refM.ID, At: time.Now().UTC()}
	}
	if len(missing) > 0 {
		return invalid("no clap was found in the first minute of %s; mark a moment for them instead (--mark)", joinAnd(missing))
	}
	return nil
}

// syncMarks lines members up on moments a person picked: the same moment in
// each (recording time); the first marked member keeps its place.
func syncMarks(c *models.Conversation, marks map[string]float64) error {
	if len(marks) < 2 {
		return invalid("mark the same moment in at least two recordings (like --mark sam=0:05.9,maya=0:03.2)")
	}
	var first *models.ConversationMember
	for i := range c.Members {
		if _, ok := marks[c.Members[i].ID]; ok {
			first = &c.Members[i]
			break
		}
	}
	for id := range marks {
		if m, _ := c.Member(id); m == nil {
			return invalid("there's no member %q (the members are %s)", id, memberList(c))
		}
	}
	at := first.ClockAt(marks[first.ID])
	for i := range c.Members {
		m := &c.Members[i]
		tau, ok := marks[m.ID]
		if !ok || m.ID == first.ID {
			continue
		}
		m.Start = round3(at - tau/m.Rate())
		m.Align = &models.MemberAlign{Method: "mark", Confidence: 0.9, Against: first.ID, At: time.Now().UTC()}
	}
	return nil
}

func memberList(c *models.Conversation) string {
	var ids []string
	for _, m := range c.Members {
		ids = append(ids, m.ID)
	}
	return strings.Join(ids, ", ")
}

func possessive(name string) string {
	if strings.HasSuffix(name, "s") {
		return name + "'"
	}
	return name + "'s"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}
