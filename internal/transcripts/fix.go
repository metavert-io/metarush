package transcripts

import (
	"context"
	"errors"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// ErrNoWords means nothing is said in the stretch someone tried to fix.
var ErrNoWords = errors.New("no words are said there")

// Space collapses runs of white space to one space.
func Space(s string) string { return strings.Join(strings.Fields(s), " ") }

// FixInput retypes what is said in a stretch of a clip.
type FixInput struct {
	AssetID string
	// From and To are the stretch (source seconds): every word whose middle is inside.
	From, To float64
	// Text is what is really said there.
	Text string
	// Expect is what the stretch says now, as the person saw it; when it
	// changed since, nothing is fixed ("" skips the check).
	Expect    string
	ProjectID string
	Source    string
	Batch     string
}

// FixWords retypes a stretch: only the words that differ change, each keeping
// the time it was said in (joined words span both, split words share it).
func FixWords(ctx context.Context, a *app.App, in FixInput) (*Change, error) {
	return Edit(ctx, a, in.AssetID, EditOptions{ProjectID: in.ProjectID, Batch: in.Batch}, func(c *Clip) error {
		return c.Retype(in)
	})
}

// Retype is FixWords on an open clip.
func (c *Clip) Retype(in FixInput) error {
	if in.To < in.From {
		in.From, in.To = in.To, in.From
	}
	i, j, ok := c.UnitsIn(in.From, in.To)
	if !ok {
		return ErrNoWords
	}
	if !c.Joinable(i, j) {
		return apperr.New(apperr.InvalidInput, "Fix one line at a time there: those lines have no times for their words.")
	}
	us := c.Units()
	now := Text(us, i, j)
	if in.Expect != "" && !sameSaid(in.Expect, now) {
		return apperr.New(apperr.Conflict, "Those words changed since you looked: they now say “%s”. Look again and fix them then.", now).
			WithDetails(map[string]any{"now": now, "expected": in.Expect})
	}
	text := Space(in.Text)
	if text == now {
		return nil
	}
	src := in.Source
	if src == "" {
		src = models.FixByHand
	}
	blocks := Retype(WordsOf(us, i, j), text)
	for k := len(blocks) - 1; k >= 0; k-- { // from the end, so earlier places keep their positions
		b := blocks[k]
		c.Replace(i+b.I, i+b.J, b.Words, models.TranscriptFix{Source: src})
	}
	return nil
}

// sameSaid compares what a person saw with what the transcript says: the same
// words, stand-alone punctuation aside (the dashboard's word list skips it).
func sameSaid(a, b string) bool {
	keep := func(s string) []string {
		var out []string
		for _, t := range strings.Fields(s) {
			if _, c, _ := Split(t); c != "" {
				out = append(out, t)
			}
		}
		return out
	}
	x, y := keep(a), keep(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// UndoResult is what taking fixes back did.
type UndoResult struct {
	Undone  []models.TranscriptFix
	Skipped []Skipped
	Changes []Change
}

// Skipped is a fix that could not be taken back, and why (plain words).
type Skipped struct {
	Fix models.TranscriptFix
	Why string
}

// Undo takes back one fix (fixID) or every fix of a batch, across clips. A
// fix whose words were changed again since stays (the later change has to
// be taken back first); a fix of who speaks goes back through the speakers.
func Undo(ctx context.Context, a *app.App, fixID, batchID string) (*UndoResult, error) {
	filter := bson.M{}
	switch {
	case fixID != "":
		filter["_id"] = fixID
	case batchID != "":
		filter["batchId"] = batchID
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say which fix to undo.")
	}
	var fixes []models.TranscriptFix
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), filter, &fixes, options.Find().SetSort(bson.D{{Key: "rev", Value: -1}, {Key: "from", Value: -1}})); err != nil {
		return nil, err
	}
	if len(fixes) == 0 {
		return nil, apperr.New(apperr.NotFound, "That fix was not found.").WithDetails(map[string]any{"fixId": fixID, "batchId": batchID})
	}
	res := &UndoResult{}
	// Words fixes of a clip go back together; each fix of who speaks on its
	// own (the speakers rewrite the clip's lines themselves).
	type group struct {
		asset string
		fixes []models.TranscriptFix
	}
	var groups []*group
	words := map[string]*group{}
	for _, f := range fixes {
		switch {
		case f.Undone != nil:
			if fixID != "" {
				res.Skipped = append(res.Skipped, Skipped{Fix: f, Why: "It was already undone."})
			}
			continue
		case f.Superseded:
			res.Skipped = append(res.Skipped, Skipped{Fix: f, Why: "The clip was transcribed again and this fix didn't carry over, so there's nothing to undo."})
			continue
		case f.Kind == models.FixSpeaker:
			groups = append(groups, &group{asset: f.AssetID, fixes: []models.TranscriptFix{f}})
		case f.Kind == models.FixWords:
			g := words[f.AssetID]
			if g == nil {
				g = &group{asset: f.AssetID}
				words[f.AssetID] = g
				groups = append(groups, g)
			}
			g.fixes = append(g.fixes, f)
		}
	}
	for _, g := range groups {
		var undone []models.TranscriptFix
		var skipped []Skipped
		ch, err := Edit(ctx, a, g.asset, EditOptions{Any: true}, func(c *Clip) error {
			undone, skipped = nil, nil
			for _, f := range g.fixes {
				why, err := c.undo(ctx, a, f)
				if err != nil {
					return err
				}
				if why != "" {
					skipped = append(skipped, Skipped{Fix: f, Why: why})
					continue
				}
				undone = append(undone, f)
			}
			return nil
		})
		if err != nil {
			var after *AfterError
			if errors.Is(err, store.ErrNotFound) && !errors.As(err, &after) {
				for _, f := range g.fixes {
					res.Skipped = append(res.Skipped, Skipped{Fix: f, Why: "The clip isn't in MetaRush any more."})
				}
				continue
			}
			return nil, err
		}
		res.Undone = append(res.Undone, undone...)
		res.Skipped = append(res.Skipped, skipped...)
		if len(undone) > 0 {
			res.Changes = append(res.Changes, *ch)
		}
	}
	return res, nil
}

// undo takes one fix back in the working copy (a fix of who speaks, through
// the speakers); it returns why it couldn't ("" when it did).
func (c *Clip) undo(ctx context.Context, a *app.App, f models.TranscriptFix) (string, error) {
	if f.Base != c.State.Base {
		return "The clip was transcribed again since this fix, so it can't be undone; fix the new words instead.", nil
	}
	if f.Kind == models.FixSpeaker {
		if f.Speaker == nil {
			return "This fix doesn't say who had the words before, so it can't be undone.", nil
		}
		// The words go back now and the fix is recorded as undone with them;
		// the voices learn again after (Clip.Then).
		_, learn, err := speakers.RestoreWords(ctx, a, f.AssetID, f.From, f.To, *f.Speaker)
		if err != nil {
			var refused *speakers.RestoreRefused
			if errors.As(err, &refused) {
				return refused.Why, nil
			}
			return "", err
		}
		c.undone = append(c.undone, f.ID)
		c.Then(learn)
		return "", nil
	}
	if len(f.After) == 0 {
		if !c.insertAt(f.Before) {
			return "Something else is said there now, so the words can't go back.", nil
		}
		c.undone = append(c.undone, f.ID)
		return "", nil
	}
	i, j, ok := c.findWords(f.After, f.Line, f.From, f.To)
	if !ok {
		return "Those words were changed again after this fix. Undo the later change first.", nil
	}
	c.replace(i, j, f.Before)
	c.undone = append(c.undone, f.ID)
	return "", nil
}

// HistoryEntry is one fix in a clip's history, with whether it can still be undone.
type HistoryEntry struct {
	models.TranscriptFix
	CanUndo bool   `json:"canUndo"`
	Why     string `json:"why,omitempty"`
}

// History is a clip's fixes, newest first.
type History struct {
	State   models.TranscriptState
	Entries []HistoryEntry
	// Original is the kept machine transcript of the clip's current transcript
	// (nil when nothing was changed yet: the transcript is the machine's).
	Original *Original
}

// LoadHistory reads a clip's fixes and works out which can still be undone.
func LoadHistory(ctx context.Context, a *app.App, as *models.Asset, limit int) (*History, error) {
	if limit <= 0 {
		limit = 500
	}
	var fixes []models.TranscriptFix
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), bson.M{"assetId": as.ID}, &fixes,
		options.Find().SetSort(bson.D{{Key: "rev", Value: -1}, {Key: "at", Value: -1}, {Key: "from", Value: -1}}).SetLimit(int64(limit))); err != nil {
		return nil, err
	}
	h := &History{Entries: make([]HistoryEntry, 0, len(fixes))}
	if as.TextFixes != nil {
		h.State = *as.TextFixes
		h.State.Lock = nil
	}
	if h.State.Base == 0 {
		h.State.Base = 1
	}
	if o, err := LoadOriginal(ctx, a, as.ID, h.State.Base); err == nil {
		o.Segments = nil // only what it is; the words are read with footage.transcript --original
		h.Original = o
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	segs, err := LoadSegments(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	c := &Clip{Asset: as, State: h.State, Segs: segs, orig: segs}
	for _, f := range fixes {
		e := HistoryEntry{TranscriptFix: f}
		switch {
		case f.Undone != nil, f.Kind != models.FixWords && f.Kind != models.FixSpeaker:
		case f.Superseded:
			e.Why = "The clip was transcribed again and this fix didn't carry over."
		case f.Base != h.State.Base:
			e.Why = "The clip was transcribed again since this fix."
		case f.Kind == models.FixSpeaker:
			if f.Speaker == nil {
				e.Why = "This fix doesn't say who had the words before."
			} else {
				e.Why = speakers.CanRestore(as, segs, f.From, f.To, *f.Speaker)
			}
			e.CanUndo = e.Why == ""
		case len(f.After) == 0:
			e.CanUndo = true
			for _, u := range c.Units() {
				if mid := (u.From + u.To) / 2; mid > f.From+0.001 && mid < f.To-0.001 {
					e.CanUndo, e.Why = false, "Something else is said there now."
				}
			}
		default:
			if _, _, ok := c.findWords(f.After, f.Line, f.From, f.To); ok {
				e.CanUndo = true
			} else {
				e.Why = "Those words were changed again later; undo that first."
			}
		}
		h.Entries = append(h.Entries, e)
	}
	return h, nil
}

// BatchCounts says how many fixes each batch has and in how many clips.
func BatchCounts(fixes []models.TranscriptFix) map[string][2]int {
	clips := map[string]map[string]bool{}
	out := map[string][2]int{}
	for _, f := range fixes {
		if clips[f.BatchID] == nil {
			clips[f.BatchID] = map[string]bool{}
		}
		clips[f.BatchID][f.AssetID] = true
		c := out[f.BatchID]
		c[0]++
		c[1] = len(clips[f.BatchID])
		out[f.BatchID] = c
	}
	return out
}

// sortFixes orders fixes as a clip says them.
func sortFixes(fs []models.TranscriptFix) {
	sort.SliceStable(fs, func(i, j int) bool {
		if fs[i].AssetID != fs[j].AssetID {
			return fs[i].AssetID < fs[j].AssetID
		}
		return fs[i].From < fs[j].From
	})
}
