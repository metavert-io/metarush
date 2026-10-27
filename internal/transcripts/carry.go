package transcripts

import (
	"context"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/textalign"
)

// When a clip is transcribed again (re-runs, catch-up, and later "Clean up
// voices", which transcribes the cleaned sound), the person's fixes carry over
// to the new transcript: each fix is looked for where it was, by time and by
// the words the machine had there. Fixes whose words the new transcript has
// differently are reported, not guessed (D149).

// Previous is a clip's transcript as it was before transcribing it again.
type Previous struct {
	had    bool
	state  models.TranscriptState
	active []models.TranscriptFix
}

// BeforeTranscribing remembers what a clip's transcript was, and keeps it (the
// versions saved before any fix read it), before a new transcript replaces it.
func BeforeTranscribing(ctx context.Context, a *app.App, as *models.Asset) (*Previous, error) {
	p := &Previous{}
	if as.TextFixes != nil {
		p.state = *as.TextFixes
		p.state.Lock = nil
	}
	if p.state.Base == 0 {
		p.state.Base = 1
	}
	segs, err := LoadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	p.had = len(segs) > 0 || (as.Transcript != nil && as.Transcript.State == "available")
	if !p.had {
		return p, nil
	}
	if len(segs) > 0 {
		if err := keepOriginal(ctx, a, as, p.state.Base, segs); err != nil {
			return nil, err
		}
	}
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), bson.M{"assetId": as.ID, "base": p.state.Base, "kind": models.FixWords, "undone": nil,
		"superseded": bson.M{"$ne": true}}, &p.active, options.Find().SetSort(bson.D{{Key: "rev", Value: 1}, {Key: "from", Value: 1}})); err != nil {
		return nil, err
	}
	return p, nil
}

// AfterResult is what happened to a clip's fixes and its spelling list when
// it got a new transcript.
type AfterResult struct {
	Carry   *models.CarryReport
	Change  *Change
	Spelled int
}

// AfterTranscribing carries the person's fixes over to the clip's new
// transcript and corrects it by its spelling lists (in that order: a person's
// own fix wins), as one change; the old fixes that couldn't be carried are
// marked superseded and reported.
func AfterTranscribing(ctx context.Context, a *app.App, as *models.Asset, prev *Previous, entries []models.SpellingEntry) (*AfterResult, error) {
	if prev == nil {
		prev = &Previous{}
	}
	res := &AfterResult{}
	var missed []string
	ch, err := Edit(ctx, a, as.ID, EditOptions{Any: true}, func(c *Clip) error {
		if prev.had {
			c.State.Base = prev.state.Base + 1
			rep := &models.CarryReport{At: time.Now().UTC(), Missed: []models.CarryMiss{}}
			for _, f := range prev.active {
				switch c.carry(f) {
				case carried:
					rep.Carried++
				case already:
					rep.Already++
				default:
					rep.Missed = append(rep.Missed, models.CarryMiss{FixID: f.ID, From: f.From, To: f.To, AfterText: f.AfterText, Heard: c.heardAround(f.From, f.To)})
					missed = append(missed, f.ID)
				}
			}
			c.State.Carry = rep
			res.Carry = rep
			// The history says the clip was transcribed again.
			c.record(models.TranscriptFix{Kind: models.FixTranscribed, Source: models.FixByMachine, From: 0, To: lastTime(c.Segs),
				BeforeText: "", AfterText: ""})
		}
		if len(entries) > 0 {
			res.Spelled = len(c.ApplySpelling(entries, nil))
		}
		if len(missed) > 0 {
			c.extra = func(ctx context.Context) error {
				_, err := a.DB.C(store.CTranscriptFixes).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": missed}},
					bson.M{"$set": bson.M{"superseded": true}})
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	res.Change = ch
	return res, nil
}

func lastTime(segs []models.TranscriptSegment) float64 {
	t := 0.0
	for _, s := range segs {
		t = max(t, s.To)
	}
	return round3(t)
}

type carryOutcome int

const (
	missed carryOutcome = iota
	carried
	already
)

// carry looks for a fix in the new words: where it was said, the words the
// machine had there before (or close to them), or already the person's words.
func (c *Clip) carry(f models.TranscriptFix) carryOutcome {
	us := c.Units()
	lo, hi := -1, -1
	for k, u := range us {
		mid := (u.From + u.To) / 2
		if mid >= f.From-0.6 && mid <= f.To+0.6 {
			if lo < 0 {
				lo = k
			}
			hi = k
		}
	}
	if lo < 0 {
		return missed
	}
	before := Norm(f.BeforeText)
	afterWords := Space(f.AfterText)
	afterCores := cores(strings.Fields(afterWords))
	type cand struct {
		i, j    int
		overlap float64
		exact   bool
	}
	var best *cand
	for i := lo; i <= hi; i++ {
		for j := i; j <= hi && j-i < 12; j++ {
			if !c.Joinable(i, j) {
				break
			}
			ov := overlap(us[i].From, us[j].To, f.From, f.To)
			if ov <= 0 {
				continue
			}
			texts := make([]string, 0, j-i+1)
			got := ""
			for k := i; k <= j; k++ {
				texts = append(texts, us[k].Text)
				got += Norm(us[k].Text)
			}
			if afterCores != "" && cores(texts) == afterCores {
				return already // the new transcript already writes it the person's way
			}
			exact := got == before
			if !exact && !(len([]rune(before)) >= 4 && textalign.Similar(got, before)) {
				continue
			}
			if best == nil || (exact && !best.exact) || (exact == best.exact && ov > best.overlap) {
				best = &cand{i, j, ov, exact}
			}
		}
	}
	if best == nil || before == "" {
		return missed
	}
	// The person's words over the new machine words there.
	var words []models.Word
	if afterWords != "" {
		words = RetypeWords(WordsOf(us, best.i, best.j), afterWords)
	}
	c.Replace(best.i, best.j, words, models.TranscriptFix{Source: models.FixCarried, CarriedFrom: f.ID, Query: f.Query})
	return carried
}

// cores joins words without the punctuation around each one.
func cores(toks []string) string {
	out := make([]string, 0, len(toks))
	for _, t := range toks {
		if _, c, _ := Split(t); c != "" {
			out = append(out, c)
		}
	}
	return strings.Join(out, " ")
}

// overlap is how long two stretches share (a point inside a stretch counts a
// millisecond).
func overlap(a0, a1, b0, b1 float64) float64 {
	lo, hi := max(a0, b0), min(a1, b1)
	if hi <= lo {
		if b1 <= b0 && a0 <= b0 && b0 <= a1 {
			return 0.001
		}
		return 0
	}
	return hi - lo
}

// heardAround is what the transcript says around a stretch now.
func (c *Clip) heardAround(from, to float64) string {
	us := c.Units()
	var got []string
	for _, u := range us {
		mid := (u.From + u.To) / 2
		if mid >= from-0.3 && mid <= to+0.3 {
			got = append(got, u.Text)
		}
	}
	return strings.Join(got, " ")
}
