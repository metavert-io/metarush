package reviewlinks

import (
	"context"
	"errors"
	"fmt"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Ending a link (D318). Every way a link ends — a person takes it back, its
// time is up, the media moves to another storage, its client's rules stop
// letting its things go to people outside the workspace, someone in it
// withdraws the consent its AI likeness relied on, owners turn links off,
// its project is cleared out — goes through endLink and finish:
//
//   - its page and its copy of the video are deleted first, so it stops
//     opening at once;
//   - its comment slots are read one last time, and deleted only once that
//     read succeeded (another computer reading them, or a store that didn't
//     answer, leaves them for the next look, which reads them first);
//   - until its signed time is over, a page still open could write a slot
//     again: the slots are deleted again at every look, never read;
//   - only a store this computer uses can be cleaned: a link whose files are
//     in another storage says so (it keeps opening until its signature ends,
//     unless a computer that uses that storage looks first), and the media
//     is never moved while links still work (they end first, ReviewLinksOnMove).
//
// What it says matches what was done: endedWords.

// endState is where an ended link's files are.
type endState struct {
	// PageGone: its page and video are deleted, so it no longer opens.
	PageGone bool
	// LastRead: its slots were read one last time; SlotsGone: then deleted.
	LastRead, SlotsGone bool
	// Elsewhere: its files are in a storage this computer doesn't use.
	Elsewhere bool
	// Comments came in with the last read.
	Comments int
}

// endLink ends a live link and cleans up what it can (changed: this call
// ended it).
func endLink(ctx context.Context, a *app.App, l *models.ReviewLink, state string, by *models.Actor, why string) (bool, *endState, error) {
	now := time.Now().UTC()
	set := bson.M{"state": state, "endedAt": now}
	if by != nil {
		set["endedBy"] = by
	}
	if why != "" {
		set["endReason"] = why
	}
	res, err := a.DB.C(store.CReviewLinks).UpdateOne(ctx, bson.M{"_id": l.ID, "workspaceId": a.WorkspaceID(), "state": models.ReviewLinkLive}, bson.M{"$set": set})
	if err != nil {
		return false, nil, err
	}
	st, err := finish(ctx, a, l.ID)
	return res.ModifiedCount == 1, st, err
}

// finish cleans up after an ended link: its page and video, then (after a
// last read) its slots; removed once everything is gone and its signed time
// is over.
func finish(ctx context.Context, a *app.App, id string) (*endState, error) {
	var l models.ReviewLink
	if err := a.DB.GetByID(ctx, store.CReviewLinks, a.WorkspaceID(), id, &l); err != nil {
		return nil, err
	}
	st := &endState{LastRead: l.LastRead}
	if l.State == models.ReviewLinkLive {
		return st, nil
	}
	if l.Store != a.Media.Locator() {
		st.Elsewhere = true
		return st, nil // a computer that uses that storage cleans it up
	}
	if l.Removed {
		st.PageGone, st.LastRead, st.SlotsGone = true, true, true
		return st, nil
	}
	p := prefixOf(l.WorkspaceID, l.Key)
	gone := func(keys ...string) bool {
		ok := true
		for _, k := range keys {
			if err := a.Media.Delete(ctx, k); err != nil {
				ok = false
				continue
			}
			// A store's yes isn't taken on trust: it is gone only once the
			// store says there's no such object.
			if _, err := a.Media.Stat(ctx, k); !errors.Is(err, media.ErrNotFound) {
				ok = false
			}
		}
		return ok
	}
	st.PageGone = gone(p+pageObject, p+videoObject)
	poster := gone(p + posterObject)
	if !l.LastRead {
		got, read, err := syncLink(ctx, a, &l, true)
		if got != nil {
			st.Comments = got.Comments
		}
		if err == nil && read {
			if err := a.DB.Update(ctx, store.CReviewLinks, a.WorkspaceID(), id, bson.M{"$set": bson.M{"lastRead": true}}); err != nil {
				return nil, err
			}
			l.LastRead, st.LastRead = true, true
		}
	}
	if l.LastRead {
		var seats []string
		for k := 0; k < max(l.Seats, Seats); k++ {
			seats = append(seats, p+seatObject(k))
		}
		st.SlotsGone = gone(seats...)
	}
	if st.PageGone && poster && st.SlotsGone {
		// Its signed time is over by the database server's clock, as every
		// computer judges the link's end (D360); until then a page still
		// open could write a slot again, so the next look deletes it again.
		over := store.DueBy("expiresAt")
		over["_id"], over["workspaceId"] = id, a.WorkspaceID()
		if _, err := a.DB.C(store.CReviewLinks).UpdateOne(ctx, over, bson.M{"$set": bson.M{"removed": true}}); err != nil {
			return nil, err
		}
	}
	return st, nil
}

// endedWords says what ending a link did, as it is (lead: "Took back the
// review link to version 3 of “Garden story”"), and what is left to do.
func endedWords(l *models.ReviewLink, st *endState, name, lead string) (string, []string) {
	warn := []string{}
	var s string
	switch {
	case st == nil:
		s = lead + "."
	case st.Elsewhere:
		s = lead + ", but its page is kept in the storage this workspace used before, which this computer doesn't use: it keeps opening until " +
			endWords(l.ExpiresAt) + ", when its signature ends, unless a computer that still uses that storage looks at it first."
		warn = append(warn, "The link's page and video weren't deleted: they are in the storage this workspace used before.")
	case !st.PageGone:
		s = lead + ", but its page couldn't be deleted yet, so the link may still open: MetaRush tries again at every look until it's gone."
		warn = append(warn, "The link's page couldn't be deleted yet.")
	case !st.LastRead:
		s = lead + ": its page and its copy of the video are deleted, so the link no longer opens. Its comment slots couldn't be read one last time yet (another computer may be reading them): MetaRush reads them at its next look, then deletes them."
		warn = append(warn, "Its last comments come in at the next look.")
	case !st.SlotsGone:
		s = lead + ": its page and its copy of the video are deleted, so the link no longer opens; its last comments are in, and its comment slots are deleted at the next look."
	default:
		s = lead + ": its page, its copy of the video and its comment slots are deleted, so the link no longer opens."
	}
	if st != nil && st.Comments > 0 {
		s += " " + plural(st.Comments, "last comment") + " came in first."
	}
	return s, warn
}

// endWhy ends a live link by itself (D318): who set off what ended it, why in
// plain words; the person who sent it hears it on the bell.
func endWhy(ctx context.Context, a *app.App, l *models.ReviewLink, by *models.Actor, why string) (string, error) {
	line, _, err := endWhyState(ctx, a, l, by, why)
	return line, err
}

// endWhyState is endWhy, with where the link's files are afterwards.
func endWhyState(ctx context.Context, a *app.App, l *models.ReviewLink, by *models.Actor, why string) (string, *endState, error) {
	changed, st, err := endLink(ctx, a, l, models.ReviewLinkRevoked, by, why)
	if err != nil {
		return "", nil, err
	}
	name := projectName(ctx, a, l.ProjectID)
	line, _ := endedWords(l, st, name, fmt.Sprintf("The review link to version %d of “%s” ended (%s)", l.RevisionNumber, name, why))
	if changed {
		a.Audit(ctx, "share.ended", "review_link", l.ID, l.ProjectID, map[string]any{"revisionId": l.RevisionID, "number": l.RevisionNumber, "why": why,
			"pageGone": st.PageGone, "lastRead": st.LastRead, "elsewhere": st.Elsewhere})
		if l.CreatedBy.UserID != "" {
			_, _ = notify.Upsert(ctx, a, models.Notification{UserID: l.CreatedBy.UserID, Kind: models.NotifyReviewLinkEnded, GroupKey: "review-link-ended:" + l.ID,
				ProjectID: l.ProjectID, Tone: "warning", Title: fmt.Sprintf("Your review link to version %d of “%s” ended", l.RevisionNumber, name),
				Body: upperFirst(why) + ".", Link: links.Revision(l.ProjectID, l.RevisionID), Details: map[string]any{"linkId": l.ID}}, notify.Once)
		}
	}
	return line, st, nil
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 && r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

// liveLinks are the links that work now (all of them, or in one store).
func liveLinks(ctx context.Context, a *app.App, filter bson.M) ([]models.ReviewLink, error) {
	f := bson.M{"state": models.ReviewLinkLive}
	for k, v := range filter {
		f[k] = v
	}
	var ls []models.ReviewLink
	err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), f, &ls, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(1000))
	return ls, err
}

// stillAllowed asks today's What may leave about a live link: the rule that
// keeps its things here now, or nil (quiet: a refusal is recorded by whoever
// acts on it).
func stillAllowed(ctx context.Context, a *app.App, l *models.ReviewLink) (*leave.Refusal, leave.Send, error) {
	what := fmt.Sprintf("version %d of “%s” through a review link", l.RevisionNumber, projectName(ctx, a, l.ProjectID))
	sends := []leave.Send{{Place: models.PlaceReviewers, ToWords: "people outside the workspace", Kinds: l.Kinds, Projects: l.Projects, What: what}}
	if kept := leave.MediaKept(a.Cfg.Media); kept.Cloud {
		sends = append(sends, leave.Send{Place: models.PlaceStorage, To: "cloud", ToWords: kept.Words, Kinds: l.Kinds, Projects: l.Projects, What: what})
	}
	for _, s := range sends {
		r, err := leave.Problem(ctx, a, s)
		if err != nil || r != nil {
			return r, s, err
		}
	}
	return nil, leave.Send{}, nil
}

// endRefused ends every live link its client's rules no longer let go to
// people outside the workspace (after the rules change, and at every look:
// rules also change by following other settings).
func endRefused(ctx context.Context, a *app.App, by *models.Actor) ([]string, error) {
	ls, err := liveLinks(ctx, a, nil)
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	var out []string
	for i := range ls {
		r, s, err := stillAllowed(ctx, a, &ls[i])
		if err != nil {
			return out, err
		}
		if r == nil {
			continue
		}
		leave.Record(ctx, a, r, s)
		line, err := endWhy(ctx, a, &ls[i], by, fmt.Sprintf("“%s”'s Content Settings keep its %s from %s now", r.SettingsName, kindOr(r.Kind), leave.PlaceWords(r.Place, "")))
		if err != nil {
			return out, err
		}
		out = append(out, line)
	}
	return out, nil
}

func kindOr(kind string) string {
	if kind == "" {
		return "things"
	}
	return leave.KindWords(kind)
}

// onMove deals with every link whose files are in the storage the media is
// leaving (from), before the switch and after the copy (D318): links that
// still work end (bell and audit, like any link that ends by itself), and
// links that ended but aren't cleaned up yet are finished there. Afterwards
// nothing here could reach them, so the move is refused unless each one's
// page is gone and its last comments are in. plan: only say what would happen.
func onMove(ctx context.Context, a *app.App, from media.Store, plan bool) ([]string, error) {
	var ls []models.ReviewLink
	err := a.DB.Find(ctx, store.CReviewLinks, a.WorkspaceID(), bson.M{"store": from.Locator(), "removed": bson.M{"$ne": true}}, &ls,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(1000))
	if err != nil || len(ls) == 0 {
		return nil, err
	}
	if plan {
		live := 0
		for _, l := range ls {
			if l.State == models.ReviewLinkLive {
				live++
			}
		}
		var out []string
		switch {
		case live == 1:
			out = append(out, "1 review link that still works ends when the media moves (its page is kept in this storage): its last comments come in, then its page is deleted.")
		case live > 1:
			out = append(out, fmt.Sprintf("%d review links that still work end when the media moves (their pages are kept in this storage): their last comments come in, then their pages are deleted.", live))
		}
		switch ended := len(ls) - live; {
		case ended == 1:
			out = append(out, "1 review link that ended still has files in this storage: they're cleaned up before the move, after its last comments come in.")
		case ended > 1:
			out = append(out, fmt.Sprintf("%d review links that ended still have files in this storage: they're cleaned up before the move, after their last comments come in.", ended))
		}
		return out, nil
	}
	// Every link's slots are held for the move before any link ends: one
	// being read right now (this computer's dashboard or another computer)
	// stops the move before it changes anything.
	token := ids.New("lse")
	defer func() {
		_, _ = a.DB.C(store.CReviewLinks).UpdateMany(context.WithoutCancel(ctx), bson.M{"workspaceId": a.WorkspaceID(), "syncBy": token},
			bson.M{"$unset": bson.M{"syncUntil": "", "syncBy": ""}})
	}()
	for i := range ls {
		l := &ls[i]
		// By the database server's clock, as a look takes and judges its
		// lease: a look on a computer whose clock is off never reads through
		// the move's hold, nor the move through a look's.
		res, err := a.DB.C(store.CReviewLinks).UpdateOne(ctx, leaseFree(l.ID, a.WorkspaceID()), takeLease(token, moveLease))
		if err != nil {
			return nil, err
		}
		if res.MatchedCount == 0 {
			return nil, apperr.New(apperr.Conflict, "The comments on the review link to version %d of “%s” are being read right now, so the media can't move yet. Nothing was changed.", l.RevisionNumber, projectName(ctx, a, l.ProjectID)).
				WithHint("Try the move again in a minute.").WithDetails(map[string]any{"reason": "review_link_reading", "linkId": l.ID})
		}
	}
	ctx = holding(ctx, token)
	b := *a
	b.Media = from
	by := a.Actor
	var out []string
	for i := range ls {
		l := &ls[i]
		name := projectName(ctx, a, l.ProjectID)
		var st *endState
		if l.State == models.ReviewLinkLive {
			line, s, err := endWhyState(ctx, &b, l, &by, "the media is being moved to another storage")
			if err != nil {
				return out, err
			}
			out = append(out, line)
			st = s
		} else if st, err = finish(ctx, &b, l.ID); err != nil {
			return out, err
		}
		if st == nil || !st.PageGone || !st.LastRead || !st.SlotsGone {
			return out, apperr.New(apperr.Conflict, "The review link to version %d of “%s” couldn't be dealt with before the move: its last comments or its files in this storage aren't cleaned up yet, and after the move nothing here could reach them.", l.RevisionNumber, name).
				WithHint("Try the move again in a minute.").WithDetails(map[string]any{"reason": "review_link_ending", "linkId": l.ID})
		}
	}
	return out, nil
}

// onConsentRevoked ends the links of versions that play what was made with
// a withdrawn consent (D227): generated shots and pictures of the person,
// their voice copy's speech, their presenter's clips, matched lips — in the
// version itself or in a cut it nests. Every look checks again
// (endWithdrawn), so a link this missed still ends.
func onConsentRevoked(ctx context.Context, a *app.App, c *models.Consent) []string {
	ls, err := liveLinks(ctx, a, nil)
	if err != nil || len(ls) == 0 {
		return nil
	}
	made, err := madeWithConsent(ctx, a, c)
	if err != nil || len(made) == 0 {
		return nil
	}
	gone := map[string]string{}
	for id := range made {
		gone[id] = c.PersonName
	}
	var out []string
	for i := range ls {
		who, bad, err := withdrawnIn(ctx, a, ls[i].RevisionID, gone)
		if err != nil || !bad {
			continue
		}
		if line, err := endWhy(ctx, a, &ls[i], nil, withdrawnWhy(who)); err == nil {
			out = append(out, line)
		}
	}
	return out
}

// withdrawnWhy says why a link ended; who "" means MetaRush couldn't look
// through everything the version plays.
func withdrawnWhy(who string) string {
	if who == "" {
		return fmt.Sprintf("it nests more than %s, too many to check against consents that were withdrawn", plural(maxNested, "cut"))
	}
	return fmt.Sprintf("%s withdrew the consent what it shows of them relied on", ops.Quote(who))
}

// withdrawnStop says why a version isn't shared (who as withdrawnWhy).
func withdrawnStop(number int, who string) *apperr.Error {
	if who == "" {
		return apperr.New(apperr.ConsentRequired, "Version %d nests more than %s, too many to check against consents that were withdrawn, so it isn't shared with anyone.", number, plural(maxNested, "cut"))
	}
	return apperr.New(apperr.ConsentRequired, "Version %d plays something made with the consent %s withdrew, so it isn't shared with anyone.", number, ops.Quote(who))
}

// withdrawn is what was made with consents that were withdrawn: each clip,
// take or picture, and whose consent it was.
func withdrawn(ctx context.Context, a *app.App) (map[string]string, error) {
	var cs []models.Consent
	if err := a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"revokedAt": bson.M{"$exists": true, "$ne": nil}}, &cs); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i := range cs {
		made, err := madeWithConsent(ctx, a, &cs[i])
		if err != nil {
			return nil, err
		}
		for id := range made {
			if _, ok := out[id]; !ok {
				out[id] = cs[i].PersonName
			}
		}
	}
	return out, nil
}

// withdrawnIn says whether a version plays anything in gone (what was made
// with withdrawn consents), and whose consent it was.
func withdrawnIn(ctx context.Context, a *app.App, revisionID string, gone map[string]string) (string, bool, error) {
	if len(gone) == 0 {
		return "", false, nil
	}
	ps, whole, err := plays(ctx, a, revisionID)
	if err != nil {
		return "", false, err
	}
	for _, id := range ps {
		if who, ok := gone[id]; ok {
			if who == "" {
				who = "someone it shows"
			}
			return who, true, nil
		}
	}
	if !whole {
		// Too many nested cuts to look through: it can't be said to be clear.
		return "", true, nil
	}
	return "", false, nil
}

// consentGone is the consent check of one version, as asked when a link is
// made or sent again: bad, and whose withdrawn consent something it plays
// was made with (as withdrawnIn).
func consentGone(ctx context.Context, a *app.App, revisionID string) (string, bool, error) {
	gone, err := withdrawn(ctx, a)
	if err != nil {
		return "", false, err
	}
	return withdrawnIn(ctx, a, revisionID, gone)
}

// plays is every clip, take and picture a version plays: its own, and (as
// What may leave counts them) those of the cuts it nests, at any depth, and
// the pictures its graphics pin. whole: every nested cut was looked through
// (false past maxNested versions).
func plays(ctx context.Context, a *app.App, revisionID string) (_ []string, whole bool, _ error) {
	var out []string
	seen := map[string]bool{}
	whole = true
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return nil
		}
		if len(seen) >= maxNested {
			whole = false
			return nil
		}
		seen[id] = true
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &rev); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		out = append(out, editspec.AssetIDs(&rev.Spec)...)
		for _, pin := range rev.Spec.Resources {
			out = append(out, pin.ID)
		}
		for _, c := range rev.Spec.Clips {
			if _, nested, ok := editspec.NestedRef(c.AssetID); ok {
				if err := walk(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(revisionID)
	return out, whole, err
}

// maxNested is how many versions plays looks through for one version.
var maxNested = 500

// madeWithConsent are the clips, takes and pictures made with a consent:
//   - AI work that relied on it (a generated picture is a resource);
//   - a presenter made with it: every clip it presents;
//   - a voice copy made with it (withdrawn with it, D227): everything said
//     or changed with that voice, whatever consent later work recorded;
//   - pictures made from any of those on this computer (made larger, its
//     background taken away…), followed through their lineage.
func madeWithConsent(ctx context.Context, a *app.App, c *models.Consent) (map[string]bool, error) {
	out := map[string]bool{}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"consentIds": c.ID}, &gs); err != nil {
		return nil, err
	}
	var presenters []models.Presenter
	if err := a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"consentId": c.ID}, &presenters); err != nil {
		return nil, err
	}
	var presenterIDs, voices bson.A
	for _, p := range presenters {
		if p.ProviderID != "" {
			presenterIDs = append(presenterIDs, p.ProviderID)
		}
	}
	for _, g := range gs {
		if g.Outcome == nil {
			continue
		}
		for _, o := range g.Outcome.Outputs {
			if o.VoiceID != "" {
				voices = append(voices, o.VoiceID)
			}
			if o.PresenterID != "" {
				presenterIDs = append(presenterIDs, o.PresenterID)
			}
		}
	}
	// What was made with its presenters and voice copies.
	var with bson.A
	if len(presenterIDs) > 0 {
		with = append(with, bson.M{"options.presenter": bson.M{"$in": presenterIDs}})
	}
	if len(voices) > 0 {
		with = append(with, bson.M{"options.voice": bson.M{"$in": voices}})
	}
	if len(with) > 0 {
		var more []models.Generation
		if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"$or": with}, &more); err != nil {
			return nil, err
		}
		gs = append(gs, more...)
	}
	var pictures []string
	for _, g := range gs {
		if g.Outcome == nil {
			continue
		}
		for _, o := range g.Outcome.Outputs {
			if o.AssetID != "" {
				out[o.AssetID] = true
			}
			if o.ResourceID != "" && !out[o.ResourceID] {
				out[o.ResourceID] = true
				pictures = append(pictures, o.ResourceID)
			}
		}
	}
	if len(voices) > 0 {
		var takes []models.VoiceTake
		if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"voiceId": bson.M{"$in": voices}}, &takes); err != nil {
			return nil, err
		}
		for _, t := range takes {
			if t.AssetID != "" {
				out[t.AssetID] = true
			}
		}
	}
	// Pictures made from them, and from those, on this computer.
	for round := 0; len(pictures) > 0 && round < 20; round++ {
		var rs []models.Resource
		if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"versions.source.lineage.resourceId": bson.M{"$in": pictures}}, &rs); err != nil {
			return nil, err
		}
		pictures = nil
		for _, r := range rs {
			if !out[r.ID] {
				out[r.ID] = true
				pictures = append(pictures, r.ID)
			}
		}
	}
	return out, nil
}

func init() {
	ops.ReviewLinksOnMove = onMove
	leave.OnRulesChanged = func(ctx context.Context, a *app.App) []string {
		by := a.Actor
		lines, _ := endRefused(ctx, a, &by)
		return lines
	}
	generate.OnConsentRevoked(onConsentRevoked)
}
