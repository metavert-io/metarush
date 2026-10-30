package reviewlinks

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Comments come back (D315): MetaRush reads each live link's comment slots
// with its own key — only slots the store says changed since it last read
// them — and keeps what they hold as ordinary comments on the version. A
// reviewer's words are data: cleaned to plain text, cut to MaxText, their
// name to MaxName, at most PerReviewer from one browser and PerHour a link
// takes in an hour (the rest wait in their slots for the next hour).

// seatDoc is a comment slot as a reviewer's browser writes it; each comment
// is read on its own, so one that isn't right spoils none of the others.
type seatDoc struct {
	V        int               `json:"v"`
	Writer   string            `json:"writer"`
	Name     string            `json:"name"`
	Comments []json.RawMessage `json:"comments"`
}

type seatComment struct {
	N    json.Number `json:"n"`
	At   *float64    `json:"at"`
	Text string      `json:"text"`
	Made string      `json:"made"`
}

// fromSeat is one comment read from a slot, cleaned.
type fromSeat struct {
	seat   int
	writer string
	name   string
	n      int
	at     *float64
	text   string
	made   *time.Time
}

var writerRe = regexp.MustCompile(`^[0-9a-f]{32}$`)

// readSeat reads and cleans one slot ("" why when it can't be used).
func readSeat(b []byte, seat int, l *models.ReviewLink, duration float64, now time.Time) ([]fromSeat, string) {
	if len(b) > l.SeatSize {
		return nil, "longer than its slot"
	}
	var d seatDoc
	if err := json.Unmarshal(b, &d); err != nil {
		return nil, "not a comment slot"
	}
	if !writerRe.MatchString(d.Writer) {
		return nil, "not a comment slot"
	}
	name := oneLine(d.Name, MaxName)
	if name == "" {
		name = "Someone"
	}
	var out []fromSeat
	seen := map[int]bool{}
	for i, raw := range d.Comments {
		if i >= PerReviewer*2 {
			break
		}
		var c seatComment
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.UseNumber()
		if dec.Decode(&c) != nil {
			continue
		}
		n64, err := c.N.Int64()
		if err != nil || n64 < 1 || n64 > PerReviewer || seen[int(n64)] {
			continue
		}
		text := words(c.Text, MaxText)
		if text == "" {
			continue
		}
		f := fromSeat{seat: seat, writer: d.Writer, name: name, n: int(n64), text: text}
		if c.At != nil && !math.IsNaN(*c.At) && !math.IsInf(*c.At, 0) && *c.At >= 0 && (duration <= 0 || *c.At <= duration+1) {
			at := math.Round(*c.At*1000) / 1000
			f.at = &at
		}
		if t, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(c.Made)); err == nil {
			t = t.UTC()
			if t.Before(l.CreatedAt) {
				t = l.CreatedAt
			}
			if t.After(now) {
				t = now
			}
			f.made = &t
		}
		seen[f.n] = true
		out = append(out, f)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].n < out[j].n })
	return out, ""
}

// syncNow brings in one link's comments, or every live link's, and ends
// links whose time is up (not in a restored copy whose deletions are paused).
func syncNow(ctx context.Context, a *app.App, id string) (*ShareSyncOut, error) {
	// A look someone asked for finishes even if they stop waiting (a page
	// reloaded): what came in is counted and told.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Minute)
	defer cancel()
	out := &ShareSyncOut{Lines: []string{}}
	// Whether a link's time is up is the database server's word: its end was
	// written in the server's time, and every computer looks (D360).
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	var ls []models.ReviewLink
	if id != "" {
		l, err := get(ctx, a, id)
		if err != nil {
			return nil, err
		}
		ls = []models.ReviewLink{*l}
	} else if err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"state": models.ReviewLinkLive}, bson.M{"removed": bson.M{"$ne": true}}}}, &ls,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(1000)); err != nil {
		return nil, err
	}
	names := projectNames(ctx, a, ls)
	// A restored copy whose deletions are paused shares its links' pages with
	// the original: it brings comments in, and ends nothing, deletes nothing.
	paused := a.DB.DeletionsPaused(ctx, a.WorkspaceID())
	// What was made with consents that were withdrawn: asked at every look,
	// so a link the withdrawal itself didn't end ends here.
	gone, err := withdrawn(ctx, a)
	if err != nil {
		return nil, err
	}
	for i := range ls {
		l := &ls[i]
		if l.Store != a.Media.Locator() {
			if paused {
				continue
			}
			// Kept in another storage: the computers that use it clean up;
			// here a link whose time is up is only marked as ended.
			if l.State == models.ReviewLinkLive && !now.Before(l.ExpiresAt) {
				_, _ = a.DB.C(store.CReviewLinks).UpdateOne(ctx, bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID(), "state": models.ReviewLinkLive},
					bson.M{"$set": bson.M{"state": models.ReviewLinkExpired, "endedAt": now}})
			}
			continue
		}
		switch {
		case paused && l.State == models.ReviewLinkLive:
			out.Links++
			got, _, err := syncLink(ctx, a, l, false)
			if err != nil {
				out.Lines = append(out.Lines, fmt.Sprintf("The comments on the review link to version %d of “%s” couldn't be read: %v.", l.RevisionNumber, names[l.ProjectID], err))
				continue
			}
			add(out, got, l, names[l.ProjectID])
		case paused:
			// Ended: its files stay until the copy is adopted.
		case l.State == models.ReviewLinkLive && !now.Before(l.ExpiresAt):
			// Its time is up: the last comments come in, then it goes.
			changed, st, err := endLink(ctx, a, l, models.ReviewLinkExpired, nil, "")
			if err != nil {
				return nil, err
			}
			out.Comments += st.Comments
			if changed {
				out.Ended++
				line, _ := endedWords(l, st, names[l.ProjectID], fmt.Sprintf("The review link to version %d of “%s” ended: its time was up", l.RevisionNumber, names[l.ProjectID]))
				out.Lines = append(out.Lines, line)
			}
		case l.State == models.ReviewLinkLive:
			// Its client's rules may have changed (here, or by following
			// other settings): a link they no longer allow ends.
			if r, s, err := stillAllowed(ctx, a, l); err == nil && r != nil {
				leave.Record(ctx, a, r, s)
				line, err := endWhy(ctx, a, l, nil, fmt.Sprintf("“%s”'s Content Settings keep its %s from %s now", r.SettingsName, kindOr(r.Kind), leave.PlaceWords(r.Place, "")))
				if err != nil {
					return nil, err
				}
				out.Ended++
				out.Lines = append(out.Lines, line)
				continue
			}
			// It plays what was made with a consent that was withdrawn.
			if who, bad, err := withdrawnIn(ctx, a, l.RevisionID, gone); err != nil {
				return nil, err
			} else if bad {
				line, err := endWhy(ctx, a, l, nil, withdrawnWhy(who))
				if err != nil {
					return nil, err
				}
				out.Ended++
				out.Lines = append(out.Lines, line)
				continue
			}
			out.Links++
			got, _, err := syncLink(ctx, a, l, false)
			if err != nil {
				out.Lines = append(out.Lines, fmt.Sprintf("The comments on the review link to version %d of “%s” couldn't be read: %v.", l.RevisionNumber, names[l.ProjectID], err))
				continue
			}
			add(out, got, l, names[l.ProjectID])
		default:
			// Ended: its last comments come in if they haven't, and its files
			// go (again, until its signed time is over).
			st, err := finish(ctx, a, l.ID)
			if err != nil {
				return nil, err
			}
			if st.Comments > 0 {
				out.Comments += st.Comments
				out.Lines = append(out.Lines, fmt.Sprintf("%s came in from the review link to version %d of “%s” as it ended.", plural(st.Comments, "last comment"), l.RevisionNumber, names[l.ProjectID]))
			}
		}
	}
	switch {
	case out.Comments > 0:
		out.Summary = plural(out.Comments, "new comment") + " came in from review links."
	case out.Links == 0 && out.Ended == 0:
		out.Summary = "No review links work now."
	default:
		out.Summary = "No new comments from review links."
	}
	if out.Waiting > 0 {
		out.Summary += fmt.Sprintf(" %s wait for the next hour (a link takes in at most %d an hour).", plural(out.Waiting, "more comment"), PerHour)
	}
	return out, nil
}

func add(out *ShareSyncOut, got *synced, l *models.ReviewLink, name string) {
	if got == nil {
		return
	}
	out.Comments += got.Comments
	out.Waiting += got.Waiting
	if got.Comments > 0 {
		out.Lines = append(out.Lines, fmt.Sprintf("%s on version %d of “%s” from %s.", plural(got.Comments, "comment"), l.RevisionNumber, name, joinAnd(quoted(got.Names))))
	}
}

// synced is what one look at a link brought in.
type synced struct {
	Comments int
	Waiting  int
	Names    []string
	// Overwritten are slots another browser wrote over (not taken in).
	Overwritten []int
}

// syncLink reads a link's changed slots and keeps their new comments. last:
// the link is ending, so everything its slots hold comes in now (within
// PerReviewer), whatever the hour's count. read says every slot was read
// (false: another computer held them, or the store didn't answer).
func syncLink(ctx context.Context, a *app.App, l *models.ReviewLink, last bool) (got *synced, read bool, err error) {
	// Once started, a look finishes: a page closed or a request given up
	// mid-way must never leave comments taken in but not counted, nor the
	// sender untold.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), lookTimeout)
	defer cancel()
	// The database server's time: the hour's count is started by one
	// computer and judged by the next, whose clocks may differ (D360).
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, false, err
	}
	// One computer at a time reads a link's slots: a lease that outlasts the
	// look, held under a token only this look knows — or one a move took
	// already for every link it deals with (holding). It ends syncLease
	// after the server's time and is judged by it, so two computers whose
	// clocks differ never both hold it.
	token, inherited := ctx.Value(leaseKey{}).(string)
	lease := bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID(), "syncBy": token}
	if !inherited {
		token = ids.New("lse")
		lease = leaseFree(l.ID, a.WorkspaceID())
	}
	if !last {
		lease["state"] = models.ReviewLinkLive
	}
	res, err := a.DB.C(store.CReviewLinks).UpdateOne(ctx, lease, takeLease(token, syncLease))
	if err != nil {
		return nil, false, err
	}
	if res.MatchedCount == 0 {
		return &synced{}, false, nil // another computer is reading it
	}
	held := bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID(), "syncBy": token}
	if !inherited {
		defer func() { // only this look's lease is given back (a move gives back its own)
			_, _ = a.DB.C(store.CReviewLinks).UpdateOne(context.WithoutCancel(ctx), held, bson.M{"$unset": bson.M{"syncUntil": "", "syncBy": ""}})
		}()
	}
	var cur models.ReviewLink
	if err := a.DB.GetByID(ctx, store.CReviewLinks, a.WorkspaceID(), l.ID, &cur); err != nil {
		return nil, false, err
	}
	l = &cur
	duration := 0.0
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), l.RenderID, &rd); err == nil && rd.Output != nil {
		duration = rd.Output.Duration
	}
	seen := append([]string(nil), l.Seen...)
	for len(seen) < l.Seats {
		seen = append(seen, "")
	}
	owners := append([]string(nil), l.Owners...)
	for len(owners) < l.Seats {
		owners = append(owners, "")
	}
	taken := map[string]*models.ReviewSeat{}
	for i := range l.Taken {
		taken[l.Taken[i].Writer] = &l.Taken[i]
	}
	// The hour's count (its start is the server's time, so whichever
	// computer looks next judges it the same way).
	hour, inHour := now, 0
	if l.Hour != nil && now.Sub(*l.Hour) < time.Hour {
		hour, inHour = *l.Hour, l.InHour
	}
	got = &synced{}
	prefix := prefixOf(l.WorkspaceID, l.Key)
	var fresh []fromSeat
	waitingSeat := map[int]bool{}
	for k := 0; k < l.Seats; k++ {
		key := prefix + seatObject(k)
		info, err := a.Media.Stat(ctx, key)
		if errors.Is(err, media.ErrNotFound) {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if info.ETag != "" && info.ETag == seen[k] && !last {
			continue // unchanged since it was last read
		}
		rc, _, err := a.Media.Get(ctx, key, 0, int64(l.SeatSize)+1)
		if err != nil {
			if errors.Is(err, media.ErrNotFound) {
				continue
			}
			return nil, false, err
		}
		b, err := io.ReadAll(io.LimitReader(rc, int64(l.SeatSize)+1))
		rc.Close()
		if err != nil {
			return nil, false, err
		}
		cs, _ := readSeat(b, k, l, duration, now)
		// A slot is its first browser's (D315): another browser's writing
		// over it is never taken in, so a link takes at most Seats browsers'
		// comments, PerReviewer from each, whatever writer IDs a page uses.
		if len(cs) > 0 {
			switch owners[k] {
			case "":
				owners[k] = cs[0].writer
			case cs[0].writer:
			default:
				cs = nil
				got.Overwritten = append(got.Overwritten, k)
			}
		}
		var newOnes []fromSeat
		for _, c := range cs {
			t := taken[c.writer]
			if t != nil && c.n <= t.Taken {
				continue
			}
			newOnes = append(newOnes, c)
		}
		if len(newOnes) > 0 {
			fresh = append(fresh, newOnes...)
		}
		seen[k] = info.ETag
	}
	// Oldest first (each browser's in the order it numbered them), then
	// within PerHour unless the link is ending.
	byWriter := map[string][]int{}
	for i := range fresh {
		byWriter[fresh[i].writer] = append(byWriter[fresh[i].writer], i)
	}
	when := make([]time.Time, len(fresh))
	for _, is := range byWriter {
		sort.Slice(is, func(x, y int) bool { return fresh[is[x]].n < fresh[is[y]].n })
		var prev time.Time
		for _, i := range is {
			t := madeOf(fresh[i], now)
			if t.Before(prev) {
				t = prev
			}
			when[i], prev = t, t
		}
	}
	order := make([]int, len(fresh))
	for i := range order {
		order[i] = i
	}
	sort.SliceStable(order, func(x, y int) bool {
		i, j := order[x], order[y]
		if !when[i].Equal(when[j]) {
			return when[i].Before(when[j])
		}
		if fresh[i].writer != fresh[j].writer {
			return fresh[i].writer < fresh[j].writer
		}
		return fresh[i].n < fresh[j].n
	})
	room := PerHour - inHour
	if last {
		room = len(fresh)
	}
	kept := 0
	for _, i := range order {
		c := fresh[i]
		t := taken[c.writer]
		if kept >= room {
			got.Waiting++
			waitingSeat[c.seat] = true
			continue
		}
		cm := models.Comment{ID: ids.New(ids.Comment), WorkspaceID: a.WorkspaceID(), ProjectID: l.ProjectID, RevisionID: l.RevisionID, At: c.at,
			Body: c.text, Author: models.Actor{UserName: c.name + " (outside)", InstallationID: a.Cfg.Installation.ID, Via: models.ViaOutside},
			CreatedAt: when[i], Outside: &models.CommentOutside{Name: c.name, LinkID: l.ID, Seat: c.seat, Writer: c.writer, N: c.n, Made: c.made}}
		// Already there: an earlier look took it in but didn't finish. It
		// isn't new, but it is in the link's count (counted from the
		// comments themselves below).
		err := a.DB.Insert(ctx, store.CComments, cm)
		if err != nil && !store.IsDuplicateKey(err) {
			return nil, false, err
		}
		kept++
		if err == nil {
			got.Comments++
		}
		if !contains(got.Names, c.name) {
			got.Names = append(got.Names, c.name)
		}
		if t == nil {
			l.Taken = append(l.Taken, models.ReviewSeat{Seat: c.seat, Writer: c.writer, Name: c.name, Taken: c.n})
			taken = map[string]*models.ReviewSeat{}
			for i := range l.Taken {
				taken[l.Taken[i].Writer] = &l.Taken[i]
			}
		} else {
			t.Seat, t.Name = c.seat, c.name
			if c.n > t.Taken {
				t.Taken = c.n
			}
		}
	}
	// A slot with comments still waiting is read again next time.
	for k := range waitingSeat {
		seen[k] = ""
	}
	if kept == 0 && got.Waiting == l.Waiting && slices.Equal(seen, l.Seen) && slices.Equal(owners, l.Owners) {
		return got, true, nil // nothing new: no write, so no one is told of a change
	}
	if AfterInserts != nil {
		AfterInserts(ctx, a, l.ID)
	}
	// The link's count is the comments that came through it, counted from
	// them: never twice, and one an unfinished look took in counts now.
	total, err := a.DB.C(store.CComments).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "outside.linkId": l.ID})
	if err != nil {
		return nil, false, err
	}
	set := bson.M{"taken": l.Taken, "seen": seen, "owners": owners, "syncedAt": now, "waiting": got.Waiting, "hour": hour, "inHour": inHour + kept, "comments": int(total)}
	if kept > 0 {
		set["lastCommentAt"] = now
	}
	upd := bson.M{"$set": set}
	if len(got.Names) > 0 {
		upd["$addToSet"] = bson.M{"reviewers": bson.M{"$each": got.Names}}
	}
	// Written only while this look still holds the lease.
	res, err = a.DB.C(store.CReviewLinks).UpdateOne(ctx, held, upd)
	if err != nil {
		return nil, false, err
	}
	if res.MatchedCount == 0 {
		// The look ran past its lease and another computer took over: it
		// writes the link and counts these comments (once) from what's in.
		return &synced{}, false, nil
	}
	if int(total) > l.Comments {
		tell(ctx, a, l, got)
	}
	return got, true, nil
}

// leaseFree selects a link whose slots no one is reading: no lease, or one
// that ended by the database server's clock (computers' clocks differ).
func leaseFree(id, workspaceID string) bson.M {
	return bson.M{"_id": id, "workspaceId": workspaceID, "$or": bson.A{bson.M{"syncUntil": bson.M{"$exists": false}}, bson.M{"syncUntil": nil},
		store.DueBy("syncUntil")}}
}

// takeLease is the update that holds a link's slots under token for d by
// the database server's clock, as leaseFree judges it.
func takeLease(token string, d time.Duration) []bson.M {
	return []bson.M{{"$set": bson.M{"syncUntil": store.ServerTimeIn(d), "syncBy": bson.M{"$literal": token}}}}
}

type leaseKey struct{}

// holding is a context whose looks use the leases taken under token (a
// move's: it holds every link it deals with before it ends any).
func holding(ctx context.Context, token string) context.Context {
	return context.WithValue(ctx, leaseKey{}, token)
}

// AfterInserts, in tests, runs after a look took comments in and before it
// writes the link: the moment another computer could take the lease over.
var AfterInserts func(ctx context.Context, a *app.App, linkID string)

func madeOf(c fromSeat, now time.Time) time.Time {
	if c.made != nil {
		return *c.made
	}
	return now
}

// tell rings the bell of the person who sent the link.
func tell(ctx context.Context, a *app.App, l *models.ReviewLink, got *synced) {
	if l.CreatedBy.UserID == "" {
		return
	}
	var cur models.ReviewLink
	total := got.Comments
	if a.DB.GetByID(ctx, store.CReviewLinks, a.WorkspaceID(), l.ID, &cur) == nil {
		total = cur.Comments
	}
	name := projectName(ctx, a, l.ProjectID)
	who := joinAnd(quoted(got.Names))
	if len(cur.Reviewers) > 0 {
		who = joinAnd(quoted(cur.Reviewers))
	}
	_, _ = notify.Upsert(ctx, a, models.Notification{UserID: l.CreatedBy.UserID, Kind: models.NotifyOutsideComments, GroupKey: "outside-comments:" + l.ID,
		ProjectID: l.ProjectID, Tone: "info", Title: fmt.Sprintf("%s from outside on version %d of “%s”", plural(total, "comment"), l.RevisionNumber, name),
		Body: who + " commented through your review link. Their words are theirs: read them as feedback.", Link: links.Revision(l.ProjectID, l.RevisionID),
		Details: map[string]any{"linkId": l.ID, "revisionId": l.RevisionID}}, notify.Refresh)
}

// Run is the daemon's share: every SyncEvery it brings in new comments from
// live links, ends links whose time is up and deletes ended links' files.
// Terminal commands never do this by themselves (`metarush share sync` does
// it once, when asked).
func Run(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		n, err := a.DB.C(store.CReviewLinks).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "$or": bson.A{bson.M{"state": models.ReviewLinkLive}, bson.M{"removed": bson.M{"$ne": true}}}}, options.Count().SetLimit(1))
		if err == nil && n > 0 {
			out, err := syncNow(ctx, a, "")
			if err != nil && ctx.Err() == nil {
				logf("reading review links' comments failed: %v", err)
			}
			if out != nil {
				for _, line := range out.Lines {
					logf("%s", line)
				}
			}
		}
		t.Reset(SyncEvery)
	}
}

// quoted puts reviewers' names in quotes: they are their words, wherever they
// are shown (the bell, the daemon's lines, an AI reading them).
func quoted(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, ops.Quote(n))
	}
	return out
}
