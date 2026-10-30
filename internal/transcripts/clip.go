package transcripts

import (
	"context"
	"errors"
	"math"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Clip is a clip's transcript opened for a change: the segments as they are
// (a working copy every change edits), the fixes that describe the changes,
// and the clip's fix state. Open one with Edit; nothing is written until the
// function given to Edit returns without an error.
type Clip struct {
	Asset *models.Asset
	State models.TranscriptState
	Segs  []models.TranscriptSegment

	orig    []models.TranscriptSegment // as loaded (for the kept machine transcript and the write)
	units   []Unit
	fixes   []models.TranscriptFix
	undone  []string // fixes taken back by this change
	batch   string
	by      models.Actor
	project string
	// carried marks the change as carrying fixes over to a new transcript (no
	// new revision of its own beyond the one Edit makes).
	extra func(ctx context.Context) error
	// then runs once the change is written (Then).
	then []func(ctx context.Context) error
}

// Then has fn run once the change is written (or, when nothing needed writing,
// once fn of Edit returned), still under the clip's lease. A fix of who says a
// stretch moves the words at once and learns the voices again after (D145,
// D60): that learning is slow, so it comes here, after the fix is recorded —
// the history and the fixes waiting for a new version never lag behind the
// words people see.
func (c *Clip) Then(fn func(ctx context.Context) error) { c.then = append(c.then, fn) }

// AfterError is a change that was made and recorded, after which what follows
// it (Then: the voices learning from a fix of who speaks) failed.
type AfterError struct{ Err error }

func (e *AfterError) Error() string {
	return "the change was saved, but what follows it failed: " + e.Err.Error()
}

func (e *AfterError) Unwrap() error { return e.Err }

// Change is what a call to Edit wrote.
type Change struct {
	AssetID string
	Rev     int
	Fixes   []models.TranscriptFix
	Undone  []string
}

// ErrNoTranscript means the clip has no transcript to fix yet.
var ErrNoTranscript = errors.New("no transcript")

const lockFor = 30 * time.Second

// lock takes the clip's short lease (waiting up to ~15 s for another change
// to finish) and returns the asset as it is now. The lease ends and is looked
// at by the database server's clock (D360): a computer whose clock runs
// ahead would otherwise take a lease another still holds, and one behind
// would write a lease that has already ended.
func lock(ctx context.Context, a *app.App, assetID string) (*models.Asset, string, error) {
	token := ids.New(ids.FixBatch)
	deadline := time.Now().Add(15 * time.Second)
	for {
		var as models.Asset
		err := a.DB.C(store.CAssets).FindOneAndUpdate(ctx,
			bson.M{"_id": assetID, "workspaceId": a.WorkspaceID(), "$or": bson.A{
				bson.M{"textFixes.lock": bson.M{"$exists": false}}, bson.M{"textFixes.lock": nil}, store.StaleBefore("textFixes.lock.until", 0)}},
			bson.A{bson.M{"$set": bson.M{"textFixes.lock": bson.M{"token": bson.M{"$literal": token}, "until": store.ServerTimeIn(lockFor)}}}},
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&as)
		if err == nil {
			return &as, token, nil
		}
		if !errors.Is(err, mongo.ErrNoDocuments) {
			return nil, "", err
		}
		if n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"_id": assetID, "workspaceId": a.WorkspaceID()}); n == 0 {
			return nil, "", store.ErrNotFound
		}
		if time.Now().After(deadline) {
			return nil, "", apperr.New(apperr.Conflict, "Someone else is fixing this clip's transcript right now. Try again in a moment.")
		}
		select {
		case <-ctx.Done():
			return nil, "", ctx.Err()
		case <-time.After(150 * time.Millisecond):
		}
	}
}

// Hold keeps fixes off a clip's transcript while something else rewrites its
// lines (a new transcript being stored, voices found again); release gives it
// back. It waits like a fix does.
func Hold(ctx context.Context, a *app.App, assetID string) (release func(), err error) {
	_, token, err := lock(ctx, a, assetID)
	if err != nil {
		return func() {}, err
	}
	return func() { unlock(ctx, a, assetID, token) }, nil
}

func unlock(ctx context.Context, a *app.App, assetID, token string) {
	_, _ = a.DB.C(store.CAssets).UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": assetID, "workspaceId": a.WorkspaceID(), "textFixes.lock.token": token},
		bson.M{"$unset": bson.M{"textFixes.lock": ""}})
}

// LoadSegments reads a clip's transcript segments in order.
func LoadSegments(ctx context.Context, a *app.App, assetID string) ([]models.TranscriptSegment, error) {
	var segs []models.TranscriptSegment
	err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}))
	return segs, err
}

// EditOptions say who makes a change and from where.
type EditOptions struct {
	// Batch groups this change with others (a find and replace over several
	// clips); "" starts a new batch.
	Batch     string
	ProjectID string
	// Any lets a change open a clip whose transcript isn't ready (carrying
	// fixes over while transcription finishes).
	Any bool
}

// Edit opens a clip's transcript for a change: it locks the clip, loads its
// segments and calls fn with them. When fn returns nil and recorded fixes (or
// undid some), the segments are written, the machine's transcript is kept
// first if this is the first change to it, the fixes are recorded with who
// and when, and the clip's transcript revision goes up by one. What fn left
// for after (Clip.Then) runs next, before the clip is unlocked; when that
// fails the change stays written and the error is an *AfterError.
func Edit(ctx context.Context, a *app.App, assetID string, o EditOptions, fn func(c *Clip) error) (*Change, error) {
	// Once a change is under way it is finished: the words and the record of
	// the fix are written together even if whoever asked goes away.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
	defer cancel()
	as, token, err := lock(ctx, a, assetID)
	if err != nil {
		return nil, err
	}
	defer unlock(ctx, a, assetID, token)
	if !o.Any && (as.Transcript == nil || as.Transcript.State != "available") {
		return nil, ErrNoTranscript
	}
	segs, err := LoadSegments(ctx, a, assetID)
	if err != nil {
		return nil, err
	}
	c := &Clip{Asset: as, Segs: cloneSegs(segs), orig: segs, batch: o.Batch, by: a.Actor, project: o.ProjectID}
	if as.TextFixes != nil {
		c.State = *as.TextFixes
		c.State.Lock = nil
	}
	if c.State.Base == 0 {
		c.State.Base = 1
	}
	if c.batch == "" {
		c.batch = ids.New(ids.FixBatch)
	}
	if err := fn(c); err != nil {
		return nil, err
	}
	ch := &Change{AssetID: assetID, Rev: c.State.Rev}
	if len(c.fixes) > 0 || len(c.undone) > 0 || c.extra != nil {
		if ch, err = c.commit(ctx, a); err != nil {
			return nil, err
		}
	}
	for _, then := range c.then {
		if err := then(ctx); err != nil {
			return ch, &AfterError{Err: err}
		}
	}
	return ch, nil
}

func cloneSegs(segs []models.TranscriptSegment) []models.TranscriptSegment {
	out := make([]models.TranscriptSegment, len(segs))
	for i, s := range segs {
		s.Words = append([]models.Word(nil), s.Words...)
		out[i] = s
	}
	return out
}

// Units are the clip's words as the working copy has them now.
func (c *Clip) Units() []Unit {
	if c.units == nil {
		c.units = Units(c.Segs)
	}
	return c.units
}

// Batch is the batch this change's fixes belong to.
func (c *Clip) Batch() string { return c.batch }

// UnitsIn returns the indices of the words of a stretch (see Range), or
// ok=false when there are none.
func (c *Clip) UnitsIn(from, to float64) (i, j int, ok bool) {
	return Range(c.Units(), from, to)
}

// Range returns the words of a stretch: those whose middle is inside
// [from, to] (a millisecond of slack at each end). A moment (from == to,
// `--at`) is the word said then, or in a short pause the nearest word.
func Range(us []Unit, from, to float64) (i, j int, ok bool) {
	if to-from < 0.0005 {
		best, dist := -1, math.Inf(1)
		for k, u := range us {
			d := 0.0
			if from < u.From {
				d = u.From - from
			} else if from > u.To {
				d = from - u.To
			}
			if d < dist-1e-9 || (math.Abs(d-dist) <= 1e-9 && math.Abs(from-(u.From+u.To)/2) < math.Abs(from-(us[best].From+us[best].To)/2)) {
				best, dist = k, d
			}
		}
		if best < 0 || dist > maxGap {
			return -1, -1, false
		}
		return best, best, true
	}
	i, j = -1, -1
	for k, u := range us {
		mid := (u.From + u.To) / 2
		if mid >= from-0.001 && mid <= to+0.001 {
			if i < 0 {
				i = k
			}
			j = k
		}
	}
	return i, j, i >= 0
}

// maxGap is how far from a word a moment may be and still mean it.
const maxGap = 0.25

// Joinable reports whether units i..j can change together (they never span a
// line without word timing and another line).
func (c *Clip) Joinable(i, j int) bool {
	us := c.Units()
	for k := i + 1; k <= j; k++ {
		if !joinable(us[k-1], us[k]) {
			return false
		}
	}
	return true
}

// Replace writes new words over units i..j (inclusive) and records the fix.
// The words of a line without word timing only change its text.
func (c *Clip) Replace(i, j int, words []models.Word, f models.TranscriptFix) models.TranscriptFix {
	us := c.Units()
	before := WordsOf(us, i, j)
	f.Kind = models.FixWords
	f.From, f.To = round3(us[i].From), round3(us[j].To)
	f.Before, f.After = before, words
	f.BeforeText, f.AfterText = JoinWords(before), JoinWords(words)
	f.Line = us[i].Word < 0
	c.replace(i, j, words)
	return c.record(f)
}

// record adds a fix to the change (its revision is set when it is written).
func (c *Clip) record(f models.TranscriptFix) models.TranscriptFix {
	f.ID = ids.New(ids.TranscriptFix)
	f.WorkspaceID, f.AssetID, f.Base, f.BatchID, f.By = c.Asset.WorkspaceID, c.Asset.ID, c.State.Base, c.batch, c.by
	if f.ProjectID == "" {
		f.ProjectID = c.project
	}
	if f.Source == "" {
		f.Source = models.FixByHand
	}
	f.At = time.Now().UTC()
	c.fixes = append(c.fixes, f)
	return f
}

// replace edits the working copy: units i..j become words. Timed words can
// span lines (the words after them stay on their own line; a line left
// without words goes); a line without word timing changes only its text.
func (c *Clip) replace(i, j int, words []models.Word) {
	us := c.Units()
	first, last := us[i], us[j]
	defer func() { c.units = nil }()
	if first.Word < 0 {
		s := &c.Segs[first.Seg]
		toks := strings.Fields(s.Text)
		news := make([]string, 0, len(words))
		for _, w := range words {
			news = append(news, strings.TrimSpace(w.Text))
		}
		out := append(append(append([]string{}, toks[:first.WordInText]...), news...), toks[last.WordInText+1:]...)
		s.Text = strings.Join(out, " ")
		return
	}
	si, sj := first.Seg, last.Seg
	a := &c.Segs[si]
	oldLen := len(a.Words)
	head := append([]models.Word(nil), a.Words[:first.Word]...)
	var tail []models.Word
	if si == sj {
		tail = append(tail, a.Words[last.Word+1:]...)
	}
	a.Words = append(append(head, words...), tail...)
	firstChanged := first.Word == 0
	lastChanged := si != sj || last.Word == oldLen-1
	settle(a, firstChanged, lastChanged)
	if si == sj {
		return
	}
	for k := si + 1; k < sj; k++ {
		c.Segs[k].Words = nil
		c.Segs[k].Text = ""
	}
	b := &c.Segs[sj]
	b.Words = append([]models.Word(nil), b.Words[last.Word+1:]...)
	settle(b, true, len(b.Words) == 0)
}

// settle fits a timed line to its words after a change: its text, and where it
// starts or ends when its first or last word changed.
func settle(s *models.TranscriptSegment, firstChanged, lastChanged bool) {
	s.Text = JoinWords(s.Words)
	if len(s.Words) == 0 {
		return
	}
	if firstChanged {
		s.From = s.Words[0].From
	}
	if lastChanged {
		s.To = s.Words[len(s.Words)-1].To
	}
	if s.To < s.From {
		s.To = s.From
	}
}

// insertAt puts words back where nothing is said now (undoing a fix that took
// words out): into the line whose words end just before them, else the line
// that starts just after them.
func (c *Clip) insertAt(words []models.Word) bool {
	if len(words) == 0 {
		return true
	}
	from, to := words[0].From, words[len(words)-1].To
	us := c.Units()
	for _, u := range us {
		mid := (u.From + u.To) / 2
		if mid > from+0.001 && mid < to-0.001 {
			return false // something is said there now
		}
	}
	// The last timed word that ends by `from`.
	prev := -1
	for k, u := range us {
		if u.Word >= 0 && u.To <= from+0.001 {
			prev = k
		}
	}
	defer func() { c.units = nil }()
	if prev >= 0 {
		u := us[prev]
		s := &c.Segs[u.Seg]
		lastChanged := u.Word == len(s.Words)-1
		s.Words = append(append(append([]models.Word(nil), s.Words[:u.Word+1]...), words...), s.Words[u.Word+1:]...)
		settle(s, false, lastChanged)
		return true
	}
	for _, u := range us {
		if u.Word >= 0 && u.From >= to-0.001 {
			s := &c.Segs[u.Seg]
			s.Words = append(append([]models.Word(nil), words...), s.Words...)
			settle(s, true, false)
			return true
		}
	}
	return false // no timed words left in the clip to sit beside
}

// commit writes the change: the kept machine transcript first (once per
// machine transcript), the segments that changed, the fixes, then the clip's
// new state.
func (c *Clip) commit(ctx context.Context, a *app.App) (*Change, error) {
	if err := keepOriginal(ctx, a, c.Asset, c.State.Base, c.orig); err != nil {
		return nil, err
	}
	if err := writeSegments(ctx, a, c.orig, c.Segs); err != nil {
		return nil, err
	}
	rev := c.State.Rev + 1
	now := time.Now().UTC()
	if len(c.fixes) > 0 {
		docs := make([]any, len(c.fixes))
		for k := range c.fixes {
			c.fixes[k].Rev = rev
			docs[k] = c.fixes[k]
		}
		if _, err := a.DB.C(store.CTranscriptFixes).InsertMany(ctx, docs); err != nil {
			return nil, err
		}
	}
	if len(c.undone) > 0 {
		if _, err := a.DB.C(store.CTranscriptFixes).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": c.undone}, "undone": nil},
			bson.M{"$set": bson.M{"undone": models.FixUndone{Rev: rev, By: c.by, At: now}}}); err != nil {
			return nil, err
		}
	}
	if c.extra != nil {
		if err := c.extra(ctx); err != nil {
			return nil, err
		}
	}
	active, err := activeFixes(ctx, a, c.Asset.ID, c.State.Base)
	if err != nil {
		return nil, err
	}
	st := c.State
	st.Rev, st.Fixes, st.UpdatedAt = rev, active, now
	set := bson.M{"textFixes.rev": st.Rev, "textFixes.base": st.Base, "textFixes.fixes": st.Fixes, "textFixes.updatedAt": now, "updatedAt": now}
	if st.Carry != nil {
		set["textFixes.carry"] = st.Carry
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), c.Asset.ID, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	return &Change{AssetID: c.Asset.ID, Rev: rev, Fixes: c.fixes, Undone: c.undone}, nil
}

// activeFixes counts the fixes in force on a clip's current machine transcript.
func activeFixes(ctx context.Context, a *app.App, assetID string, base int) (int, error) {
	n, err := a.DB.C(store.CTranscriptFixes).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "base": base,
		"kind": bson.M{"$in": bson.A{models.FixWords, models.FixSpeaker}}, "undone": nil, "superseded": bson.M{"$ne": true}})
	return int(n), err
}

// writeSegments writes the lines that changed (their words, text and times
// only: who speaks is the speakers' business) and removes lines left empty.
// A line someone else rewrote meanwhile is a conflict.
func writeSegments(ctx context.Context, a *app.App, before, after []models.TranscriptSegment) error {
	coll := a.DB.C(store.CSegments)
	for k := range after {
		s, o := after[k], before[k]
		if sameLine(s, o) {
			continue
		}
		filter := bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID(), "text": o.Text}
		if strings.TrimSpace(s.Text) == "" && len(s.Words) == 0 {
			res, err := coll.DeleteOne(ctx, filter)
			if err != nil {
				return err
			}
			if res.DeletedCount == 0 {
				return changedMeanwhile()
			}
			continue
		}
		set := bson.M{"text": s.Text, "from": s.From, "to": s.To}
		if len(s.Words) > 0 || len(o.Words) > 0 {
			set["words"] = s.Words
		}
		res, err := coll.UpdateOne(ctx, filter, bson.M{"$set": set})
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			return changedMeanwhile()
		}
	}
	return nil
}

func changedMeanwhile() error {
	return apperr.New(apperr.Conflict, "This clip's transcript changed while the fix was being saved (who speaks was found again, perhaps). Look at it again and fix it then.")
}

func sameLine(a, b models.TranscriptSegment) bool {
	if a.Text != b.Text || a.From != b.From || a.To != b.To || len(a.Words) != len(b.Words) {
		return false
	}
	for i := range a.Words {
		if a.Words[i] != b.Words[i] {
			return false
		}
	}
	return true
}

// near reports two times as the same (a millisecond and a half apart at most).
func near(x, y float64) bool { return math.Abs(x-y) < 0.0015 }

// findWords finds where words are said now: a run of units with the same
// text and times (or, in a line without word timing, the same words in a line
// around that time).
func (c *Clip) findWords(ws []models.Word, line bool, from, to float64) (int, int, bool) {
	us := c.Units()
	if len(ws) == 0 {
		return 0, 0, false
	}
	for i := range us {
		if i+len(ws) > len(us) {
			break
		}
		ok := true
		for k, w := range ws {
			u := us[i+k]
			if u.Text != strings.TrimSpace(w.Text) || (u.Word < 0) != line {
				ok = false
				break
			}
			if !line && (!near(u.From, w.From) || !near(u.To, w.To)) {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if line {
			s := c.Segs[us[i].Seg]
			if us[i+len(ws)-1].Seg != us[i].Seg || s.To < from-0.5 || s.From > to+0.5 {
				continue
			}
		}
		return i, i + len(ws) - 1, true
	}
	return 0, 0, false
}
