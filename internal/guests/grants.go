package guests

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/withdrawn"
)

// Grants (D490): what a guest may see and do. An owner shares a project —
// every version of it with a review copy, now and later — or some of its
// versions, as a viewer or a commenter, until an end that is required and at
// most MaxDays away (sharing again gives more). Approvals by guests come
// later: the approver role is refused now. What may leave is asked for every
// grant (D498), and again for every version a guest opens.

// GuestsShareIn shares a project, or some of its versions, with a guest.
type GuestsShareIn struct {
	// Email is the guest's: they sign in with a link sent to it, or with
	// their organization's account when it vouches for this email.
	Email string `json:"email" jsonschema:"maxLength=254"`
	// Name is what to call them ("Dana at the client").
	Name      string `json:"name,omitempty" jsonschema:"maxLength=80"`
	ProjectID string `json:"projectId"`
	// Versions are the versions to share (rN or their IDs); none: every
	// version of the project with a review copy, now and later.
	Versions []string `json:"versions,omitempty" jsonschema:"maxItems=50"`
	// Role is viewer (watches) or commenter (watches and comments).
	Role string `json:"role" jsonschema:"enum=viewer,enum=commenter,enum=approver"`
	// Days is how long it works from now (1 to 90); Until its last day
	// instead (2026-11-30, this computer's time zone). One of them is needed.
	Days  int    `json:"days,omitempty" jsonschema:"minimum=0,maximum=90"`
	Until string `json:"until,omitempty" jsonschema:"maxLength=10"`
	// Message is a note their guest page shows.
	Message string `json:"message,omitempty" jsonschema:"maxLength=500"`
	// DryRun says what the guest would see and whether it may be shared,
	// sharing nothing (What may leave is asked quietly).
	DryRun bool `json:"dryRun,omitempty"`
}

// GrantVersion is a version a grant names, as an owner sees it.
type GrantVersion struct {
	ID     string `json:"id"`
	Number int    `json:"number"`
	// Cut is the cut's name; Ready: it has a review copy to watch.
	Cut   string `json:"cut,omitempty"`
	Ready bool   `json:"ready"`
}

// GuestsShareOut is a share made, or what one would be.
type GuestsShareOut struct {
	// May: it may be shared (a dry run); else Why, Code and Hint say what stops it.
	May  bool   `json:"may"`
	Why  string `json:"why,omitempty"`
	Code string `json:"code,omitempty"`
	Hint string `json:"hint,omitempty"`
	// Shared: it was shared (not a dry run).
	Shared      bool   `json:"shared"`
	ProjectName string `json:"projectName"`
	// Versions are the versions named (none: every version of the project).
	Versions  []GrantVersion `json:"versions"`
	Role      string         `json:"role"`
	RoleWords string         `json:"roleWords"`
	EndsAt    time.Time      `json:"endsAt"`
	Ends      string         `json:"ends"`
	// Exposes says what the guest sees; Kinds as What may leave names them.
	Exposes []string `json:"exposes"`
	Kinds   []string `json:"kinds"`
	// Guest and Grant are what was made (not on a dry run); NewGuest: they
	// weren't a guest here before.
	Guest    *models.Guest      `json:"guest,omitempty"`
	Grant    *models.GuestGrant `json:"grant,omitempty"`
	NewGuest bool               `json:"newGuest,omitempty"`
	Warnings []string           `json:"warnings"`
	Summary  string             `json:"summary"`
}

// GuestsListIn lists guests: everyone's, or those a project is shared with.
type GuestsListIn struct {
	ProjectID string `json:"projectId,omitempty"`
	// All includes grants that ended and guests taken back.
	All bool `json:"all,omitempty"`
}

// GrantView is a grant as people see it.
type GrantView struct {
	Grant       models.GuestGrant `json:"grant"`
	ProjectName string            `json:"projectName"`
	// Versions are the version numbers it names (none: every version).
	Versions []int `json:"versions"`
	// Live: it works now. StateWords and Ends say its state and time plainly.
	Live       bool   `json:"live"`
	StateWords string `json:"stateWords"`
	Ends       string `json:"ends"`
	Summary    string `json:"summary"`
}

// GuestView is a guest with their grants.
type GuestView struct {
	Guest  models.Guest `json:"guest"`
	Grants []GrantView  `json:"grants"`
	// Live is how many grants work now; SignedIn how many browsers are
	// signed in as them now.
	Live     int    `json:"live"`
	SignedIn int    `json:"signedIn"`
	Summary  string `json:"summary"`
}

// GuestsListOut is the guests, those with something shared now first.
type GuestsListOut struct {
	Guests  []GuestView `json:"guests"`
	Summary string      `json:"summary"`
}

// GuestsRevokeIn takes back one grant, or everything shared with a guest.
type GuestsRevokeIn struct {
	Grant string `json:"grant,omitempty"`
	// Guest is a guest's email or ID: everything shared with them ends and
	// they are signed out.
	Guest string `json:"guest,omitempty"`
}

// GuestsRevokeOut says what ended.
type GuestsRevokeOut struct {
	Guest     models.Guest `json:"guest"`
	Ended     int          `json:"ended"`
	SignedOut int          `json:"signedOut"`
	Summary   string       `json:"summary"`
}

func init() {
	ops.Def("guests.share", "Share a project (every version with a review copy) or some of its versions with a guest outside the workspace, as a viewer or a commenter, until a day you choose (at most 90 days; What may leave is asked); owners, people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		share)
	ops.Def("guests.list", "Guests outside the workspace and what each may see: the projects or versions, as a viewer or a commenter, until when, and whether they are signed in", ops.Flags{},
		list)
	ops.Def("guests.revoke", "Take back one thing shared with a guest, or everything shared with them: it stops at once and, with nothing left, they are signed out; owners, people only", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		revoke)
	ops.RegisterPurgeHook("guests", purge)
}

/* ---------- Sharing ---------- */

func share(ctx context.Context, a *app.App, in *GuestsShareIn) (*GuestsShareOut, error) {
	out := &GuestsShareOut{Versions: []GrantVersion{}, Exposes: []string{}, Kinds: []string{}, Warnings: []string{}}
	stop := func(e *apperr.Error) (*GuestsShareOut, error) {
		if !in.DryRun {
			return nil, e
		}
		out.May, out.Why, out.Code, out.Hint, out.Summary = false, e.Message, e.Code, e.Hint, e.Message
		return out, nil
	}
	email := strings.TrimSpace(in.Email)
	if !auth.ValidEmail(email) || strings.ContainsAny(email, "\"'`") {
		return stop(apperr.New(apperr.InvalidInput, "Give the guest's email, like dana@example.com.").WithDetails(map[string]any{"reason": "email"}))
	}
	name := oneLine(in.Name, MaxName)
	role := strings.ToLower(strings.TrimSpace(in.Role))
	switch role {
	case models.GuestViewer, models.GuestCommenter:
	case models.GuestApprover:
		return stop(apperr.New(apperr.InvalidInput, "Guests can't approve versions yet: share it with them as a viewer or a commenter.").WithDetails(map[string]any{"reason": "approver"}))
	default:
		return stop(apperr.New(apperr.InvalidInput, "Say what the guest may do: viewer (watch) or commenter (watch and comment).").WithDetails(map[string]any{"reason": "role"}))
	}
	out.Role, out.RoleWords = role, roleWords(role)
	message := words(in.Message, MaxMessage)
	p, err := ops.ResolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	out.ProjectName = p.Name
	if p.Archived || p.Purging != nil {
		return stop(apperr.New(apperr.Conflict, "“%s” is archived, so none of it is shared.", p.Name).WithHint("Restore the project first."))
	}
	if u, _ := auth.FindByEmail(ctx, a, email); u != nil && (u.State == models.UserActive || u.State == models.UserInvited) {
		return stop(apperr.New(apperr.Conflict, "%s is someone in this workspace already: they open the dashboard instead.", email).WithDetails(map[string]any{"reason": "member"}))
	}

	// Until when (the database server's time: every computer judges it by that).
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	ends, e := endOf(now, in.Days, in.Until)
	if e != nil {
		return stop(e)
	}
	out.EndsAt, out.Ends = ends, "until "+dayWords(ends)

	// What it names.
	var revs []models.Revision
	seen := map[string]bool{}
	for _, ref := range in.Versions {
		ref = strings.TrimSpace(ref)
		if ref == "" {
			continue
		}
		rev, err := ops.ResolveRevision(ctx, a, p, ref, "")
		if err != nil || rev == nil || rev.ProjectID != p.ID || rev.DraftID != "" {
			return stop(apperr.New(apperr.NotFound, "“%s” has no version %s.", p.Name, ref).WithDetails(map[string]any{"reason": "version", "version": ref}))
		}
		if !seen[rev.ID] {
			seen[rev.ID] = true
			revs = append(revs, *rev)
		}
	}
	if len(in.Versions) > 50 {
		return stop(apperr.New(apperr.InvalidInput, "Share at most 50 versions at once, or the whole project."))
	}
	if len(revs) == 0 && len(in.Versions) > 0 {
		return stop(apperr.New(apperr.InvalidInput, "Name the versions to share, or none for the whole project."))
	}
	shown := revs
	if len(revs) == 0 {
		// Every version with a review copy, now and later: What may leave is
		// asked for those there are now, and again whenever a guest opens one.
		if shown, err = projectVersions(ctx, a, p.ID, 50); err != nil {
			return nil, err
		}
		if len(shown) == 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("“%s” has no version with a review copy yet: the guest sees each one once it has.", p.Name))
		}
	}
	ready := readyVersions(ctx, a, shown)
	for _, r := range revs {
		gv := GrantVersion{ID: r.ID, Number: r.Number, Cut: cutName(p, r.Branch), Ready: ready[r.ID]}
		out.Versions = append(out.Versions, gv)
		if !gv.Ready {
			out.Warnings = append(out.Warnings, fmt.Sprintf("Version %d has no review copy yet: the guest can watch it once it has one.", r.Number))
		}
	}

	// What leaves, whose things they are, and whether they may go (D498).
	kinds := []string{models.LeaveFootage, models.LeaveVoices, models.LeaveFaces, models.LeavePictures}
	projects := map[string]bool{p.ID: true}
	for i := range shown {
		if ops.BurnsCaptionsIn(&shown[i]) && !slices.Contains(kinds, models.LeaveWords) {
			kinds = append(kinds, models.LeaveWords)
		}
		touched, err := leave.RevisionTouchesChecked(ctx, a, shown[i].ID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't check version %d just now.", shown[i].Number).WithHint("Try again in a moment.")
		}
		for _, x := range touched {
			projects[x] = true
		}
	}
	out.Kinds = ordered(kinds)
	what := fmt.Sprintf("every version of “%s” with a review copy", p.Name)
	if len(revs) > 0 {
		var nums []string
		for _, r := range revs {
			nums = append(nums, fmt.Sprint(r.Number))
		}
		what = fmt.Sprintf("version %s of “%s”", joinAnd(nums), p.Name)
		if len(revs) > 1 {
			what = "versions " + strings.TrimPrefix(what, "version ")
		}
	}
	out.Exposes = append(out.Exposes, what+": the review copy's picture and sound")
	if slices.Contains(kinds, models.LeaveWords) {
		out.Exposes = append(out.Exposes, "the captions drawn in the picture")
	}
	out.Exposes = append(out.Exposes, "the project's name and the cut's name")
	if message != "" {
		out.Exposes = append(out.Exposes, "your note")
	}
	if n := strings.TrimSpace(a.Actor.UserName); n != "" {
		out.Exposes = append(out.Exposes, "the name of who shared it ("+n+")")
	}
	if role == models.GuestCommenter {
		out.Exposes = append(out.Exposes, "the comments they leave themselves (they come back to the version)")
	}
	var pl []string
	for x := range projects {
		pl = append(pl, x)
	}
	sort.Strings(pl)
	send := leave.Send{Place: models.PlaceGuests, ToWords: "guests (" + email + ")", Kinds: out.Kinds, Projects: pl, What: what + " shared with a guest"}
	if in.DryRun {
		r, err := leave.Problem(ctx, a, send)
		if err != nil {
			return nil, err
		}
		if r != nil {
			return stop(r.Err())
		}
	} else if err := leave.Check(ctx, a, send); err != nil {
		return nil, err // recorded: a person asked, and it was kept here
	}
	// A version that plays someone else's video, anything made with a consent
	// that was withdrawn, or words changed in someone's voice whose consent is
	// gone, isn't shown to guests (D441, D227) — whatever note sent it out
	// anyway. A project's versions are asked again whenever a guest opens one.
	var gone map[string]string
	if len(revs) > 0 {
		if gone, err = withdrawn.Made(ctx, a); err != nil {
			return nil, err
		}
	}
	for i := range revs {
		why, err := heldWords(ctx, a, p, &revs[i])
		if err != nil {
			return nil, err
		}
		if why != "" {
			return stop(apperr.New(apperr.Held, "Version %d can't be shared with guests: %s.", revs[i].Number, why).
				WithHint("Make a version without it first.").WithDetails(map[string]any{"reason": ReasonHeld, "revisionId": revs[i].ID}))
		}
		who, bad, err := withdrawn.In(ctx, a, revs[i].ID, gone)
		if err != nil {
			return nil, err
		}
		if bad {
			e := apperr.New(apperr.ConsentRequired, "Version %d can't be shared with guests: it plays something made with the consent %s withdrew.", revs[i].Number, ops.Quote(who))
			if who == "" {
				e = apperr.New(apperr.ConsentRequired, "Version %d can't be shared with guests: it nests more than %s, too many to check against consents that were withdrawn.", revs[i].Number, plural(withdrawn.MaxNested, "cut"))
			}
			return stop(e.WithHint("Make a version without it first.").WithDetails(map[string]any{"reason": ReasonConsent, "revisionId": revs[i].ID}))
		}
	}
	if !On(ctx, a) {
		out.Warnings = append(out.Warnings, "Guests are off here: turn them on (Settings → People → Guests) before they open it.")
	}
	out.May = true
	who := email
	if name != "" {
		who = name + " (" + email + ")"
	}
	if in.DryRun {
		out.Summary = fmt.Sprintf("%s would %s %s, %s.", who, roleWords(role), what, out.Ends)
		return out, nil
	}

	g, isNew, err := guestFor(ctx, a, email, name)
	if err != nil {
		return nil, err
	}
	gr := models.GuestGrant{ID: ids.New(ids.GuestGrant), WorkspaceID: a.WorkspaceID(), GuestID: g.ID, ProjectID: p.ID, RevisionIDs: []string{}, Role: role,
		EndsAt: ends, Message: message, Kinds: out.Kinds, Projects: pl, Exposes: out.Exposes, CreatedBy: a.Actor, CreatedAt: now}
	for _, r := range revs {
		gr.RevisionIDs = append(gr.RevisionIDs, r.ID)
	}
	if err := a.DB.Insert(ctx, store.CGuestGrants, gr); err != nil {
		return nil, err
	}
	a.Audit(ctx, "guests.share", "guest_grant", gr.ID, p.ID, map[string]any{"guestId": g.ID, "role": role, "versions": gr.RevisionIDs, "endsAt": ends, "kinds": gr.Kinds, "newGuest": isNew})
	out.Shared, out.Guest, out.Grant, out.NewGuest = true, g, &gr, isNew
	out.Summary = fmt.Sprintf("Shared %s with %s, who can %s %s. Send them a sign-in link next.", what, who, roleWords(role), out.Ends)
	return out, nil
}

// endOf is when a grant ends: days from now, or the end of a day given as
// YYYY-MM-DD (this computer's time zone); required, at most MaxDays away.
func endOf(now time.Time, days int, until string) (time.Time, *apperr.Error) {
	until = strings.TrimSpace(until)
	switch {
	case days != 0 && until != "":
		return time.Time{}, apperr.New(apperr.InvalidInput, "Say how many days it works or its last day, not both.")
	case days < 0 || days > MaxDays:
		return time.Time{}, apperr.New(apperr.InvalidInput, "Share it for 1 to %d days.", MaxDays).WithDetails(map[string]any{"reason": "days"})
	case days > 0:
		return now.Add(time.Duration(days) * 24 * time.Hour).UTC(), nil
	case until == "":
		return time.Time{}, apperr.New(apperr.InvalidInput, "Say until when the guest may see it: a number of days (at most %d) or a last day.", MaxDays).WithDetails(map[string]any{"reason": "end"})
	}
	day, err := time.ParseInLocation("2006-01-02", until, time.Local)
	if err != nil {
		return time.Time{}, apperr.New(apperr.InvalidInput, "Give the last day like 2026-11-30.").WithDetails(map[string]any{"reason": "until"})
	}
	end := day.Add(24*time.Hour - time.Second).UTC()
	switch {
	case !end.After(now):
		return time.Time{}, apperr.New(apperr.InvalidInput, "That day has passed: give a day still to come.").WithDetails(map[string]any{"reason": "until"})
	case end.Sub(now) > time.Duration(MaxDays+1)*24*time.Hour:
		return time.Time{}, apperr.New(apperr.InvalidInput, "A guest gets at most %d days at once: give a nearer day (share again later for more).", MaxDays).WithDetails(map[string]any{"reason": "until"})
	}
	return end, nil
}

// guestFor finds the guest with an email, or makes them; a guest taken back
// before is let in again (a new share is a new decision).
func guestFor(ctx context.Context, a *app.App, email, name string) (*models.Guest, bool, error) {
	key := auth.EmailKey(email)
	var g models.Guest
	err := a.DB.C(store.CGuests).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "emailKey": key}).Decode(&g)
	switch {
	case err == nil:
		set := bson.M{}
		if name != "" && name != g.Name {
			set["name"] = name
		}
		update := bson.M{}
		if g.State != models.GuestActive {
			set["state"] = models.GuestActive
			update["$unset"] = bson.M{"revokedAt": "", "revokedBy": ""}
		}
		if len(set) > 0 {
			update["$set"] = set
		}
		if len(update) > 0 {
			if err := a.DB.Update(ctx, store.CGuests, a.WorkspaceID(), g.ID, update); err != nil {
				return nil, false, err
			}
		}
		return getGuest(ctx, a, g.ID)
	case !errors.Is(err, mongo.ErrNoDocuments):
		return nil, false, err
	}
	now := time.Now().UTC()
	g = models.Guest{ID: ids.New(ids.Guest), WorkspaceID: a.WorkspaceID(), Email: email, EmailKey: key, Name: name, State: models.GuestActive, CreatedBy: a.Actor, CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CGuests, g); err != nil {
		if store.IsDuplicateKey(err) { // made at the same moment by another share
			return guestFor(ctx, a, email, name)
		}
		return nil, false, err
	}
	return &g, true, nil
}

// getGuest reads a guest; isNew is always false (a helper for guestFor).
func getGuest(ctx context.Context, a *app.App, id string) (*models.Guest, bool, error) {
	var g models.Guest
	if err := a.DB.GetByID(ctx, store.CGuests, a.WorkspaceID(), id, &g); err != nil {
		return nil, false, err
	}
	return &g, false, nil
}

// findGuest finds a guest by ID or email.
func findGuest(ctx context.Context, a *app.App, ref string) (*models.Guest, error) {
	ref = strings.TrimSpace(ref)
	var g models.Guest
	var err error
	if ids.Valid(ids.Guest, ref) {
		err = a.DB.GetByID(ctx, store.CGuests, a.WorkspaceID(), ref, &g)
	} else {
		err = a.DB.C(store.CGuests).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "emailKey": auth.EmailKey(ref)}).Decode(&g)
	}
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no guest like that here.").WithHint("Share something with them first.").WithCommand("metarush guests list")
	}
	return &g, nil
}

// projectVersions are a project's versions with a review copy, newest first
// (never an AI app's draft nobody approved).
func projectVersions(ctx context.Context, a *app.App, projectID string, limit int) ([]models.Revision, error) {
	var idsList []string
	if err := a.DB.C(store.CRenders).Distinct(ctx, "revisionId", bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID, "quality": "review",
		"state": models.RenderAvailable, "output": bson.M{"$ne": nil}}).Decode(&idsList); err != nil {
		return nil, err
	}
	if len(idsList) == 0 {
		return nil, nil
	}
	var revs []models.Revision
	if err := a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}, "projectId": projectID, "draftId": bson.M{"$in": bson.A{nil, ""}}}, &revs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit)).SetProjection(bson.M{"spec.clips": 0, "spec.overlays": 0, "ops": 0, "transcripts": 0})); err != nil {
		return nil, err
	}
	return revs, nil
}

// readyVersions reports which versions have a review copy to watch.
func readyVersions(ctx context.Context, a *app.App, revs []models.Revision) map[string]bool {
	out := map[string]bool{}
	if len(revs) == 0 {
		return out
	}
	var list []string
	for _, r := range revs {
		list = append(list, r.ID)
	}
	var got []string
	if err := a.DB.C(store.CRenders).Distinct(ctx, "revisionId", bson.M{"workspaceId": a.WorkspaceID(), "revisionId": bson.M{"$in": list}, "quality": "review",
		"state": models.RenderAvailable, "output": bson.M{"$ne": nil}}).Decode(&got); err != nil {
		return out
	}
	for _, id := range got {
		out[id] = true
	}
	return out
}

// cutName is a cut's name as people see it ("" for the main cut without one).
func cutName(p *models.Project, branch string) string {
	if branch == "" {
		branch = "main"
	}
	if h, ok := p.Branches[branch]; ok && strings.TrimSpace(h.Title) != "" {
		return h.Title
	}
	if branch == "main" {
		return ""
	}
	return branch
}

func ordered(kinds []string) []string {
	out := []string{}
	for _, k := range models.LeaveKinds {
		if slices.Contains(kinds, k) {
			out = append(out, k)
		}
	}
	return out
}

// heldWords says why a version isn't shown to guests ("" it is): it plays
// someone else's video (a reference) or words changed in someone's voice whose
// consent is gone. Guests never see those, even where a person sent the
// version out anyway with a note. When that can't be told (the database
// didn't answer), it isn't shown: an error.
func heldWords(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision) (string, error) {
	g, err := ops.ShipGate(ctx, a, p, rev, "review", nil)
	if err != nil {
		return "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't check version %d just now.", rev.Number).WithHint("Try again in a moment.")
	}
	var why []string
	for _, h := range g.Holds {
		if h.Kind != models.HoldReference && h.Kind != models.HoldConsent {
			continue
		}
		why = append(why, strings.TrimSuffix(lowerFirst(h.Says), "."))
	}
	return joinAnd(why), nil
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && r[0] >= 'A' && r[0] <= 'Z' && !(r[1] >= 'A' && r[1] <= 'Z') {
		r[0] += 'a' - 'A'
	}
	return string(r)
}

/* ---------- Who has what ---------- */

func list(ctx context.Context, a *app.App, in *GuestsListIn) (*GuestsListOut, error) {
	now, err := a.DB.Now(ctx)
	if err != nil {
		return nil, err
	}
	gf := bson.M{}
	if in.ProjectID != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		gf["projectId"] = p.ID
	}
	var grants []models.GuestGrant
	if err := a.DB.Find(ctx, store.CGuestGrants, a.WorkspaceID(), gf, &grants, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(2000)); err != nil {
		return nil, err
	}
	byGuest := map[string][]models.GuestGrant{}
	for _, g := range grants {
		byGuest[g.GuestID] = append(byGuest[g.GuestID], g)
	}
	gq := bson.M{}
	if in.ProjectID != "" {
		idList := []string{}
		for id := range byGuest {
			idList = append(idList, id)
		}
		gq["_id"] = bson.M{"$in": idList}
	}
	var gs []models.Guest
	if err := a.DB.Find(ctx, store.CGuests, a.WorkspaceID(), gq, &gs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1000)); err != nil {
		return nil, err
	}
	signedIn := sessionsByGuest(ctx, a)
	names := projectNames(ctx, a, grants)
	numbers := versionNumbers(ctx, a, grants)
	out := &GuestsListOut{Guests: []GuestView{}}
	live := 0
	for _, g := range gs {
		v := GuestView{Guest: g, Grants: []GrantView{}, SignedIn: signedIn[g.ID]}
		for _, gr := range byGuest[g.ID] {
			gv := grantView(gr, names[gr.ProjectID], numbers, now)
			if gv.Live {
				v.Live++
			}
			if gv.Live || in.All {
				v.Grants = append(v.Grants, gv)
			}
		}
		if (g.State != models.GuestActive || v.Live == 0) && !in.All {
			continue
		}
		v.Summary = guestSummary(&g, v)
		if v.Live > 0 {
			live++
		}
		out.Guests = append(out.Guests, v)
	}
	sort.SliceStable(out.Guests, func(i, j int) bool { return out.Guests[i].Live > 0 && out.Guests[j].Live == 0 })
	switch {
	case len(out.Guests) == 0 && in.ProjectID != "":
		out.Summary = "Nothing of this project is shared with guests now."
	case len(out.Guests) == 0:
		out.Summary = "Nothing is shared with guests now."
	default:
		out.Summary = fmt.Sprintf("%s with something shared now.", plural(live, "guest"))
	}
	return out, nil
}

func grantView(gr models.GuestGrant, projectName string, numbers map[string]int, now time.Time) GrantView {
	if gr.RevisionIDs == nil {
		gr.RevisionIDs = []string{}
	}
	if gr.Kinds == nil {
		gr.Kinds = []string{}
	}
	if gr.Projects == nil {
		gr.Projects = []string{}
	}
	if gr.Exposes == nil {
		gr.Exposes = []string{}
	}
	v := GrantView{Grant: gr, ProjectName: projectName, Versions: []int{}, Live: gr.RevokedAt == nil && gr.EndsAt.After(now)}
	for _, id := range gr.RevisionIDs {
		if n := numbers[id]; n > 0 {
			v.Versions = append(v.Versions, n)
		}
	}
	sort.Ints(v.Versions)
	switch {
	case v.Live:
		v.StateWords, v.Ends = "Works", "until "+dayWords(gr.EndsAt)
	case gr.RevokedAt != nil && gr.EndReason != "":
		v.StateWords, v.Ends = "Ended", "ended "+dayWords(*gr.RevokedAt)+": "+gr.EndReason
	case gr.RevokedAt != nil:
		by := ""
		if gr.RevokedBy != nil && gr.RevokedBy.UserName != "" {
			by = " by " + gr.RevokedBy.UserName
		}
		v.StateWords, v.Ends = "Taken back", "taken back"+by+" "+dayWords(*gr.RevokedAt)
	default:
		v.StateWords, v.Ends = "Ended", "ended "+dayWords(gr.EndsAt)
	}
	what := "every version of “" + projectName + "”"
	if len(v.Versions) > 0 {
		var ns []string
		for _, n := range v.Versions {
			ns = append(ns, fmt.Sprint(n))
		}
		what = "version " + joinAnd(ns) + " of “" + projectName + "”"
		if len(ns) > 1 {
			what = "versions " + joinAnd(ns) + " of “" + projectName + "”"
		}
	}
	v.Summary = fmt.Sprintf("%s: can %s, %s.", upperFirst(what), roleWords(gr.Role), v.Ends)
	if !v.Live {
		v.Summary = fmt.Sprintf("%s: %s.", upperFirst(what), v.Ends)
	}
	return v
}

func guestSummary(g *models.Guest, v GuestView) string {
	s := who(g)
	if g.Name != "" {
		s += " (" + g.Email + ")"
	}
	switch {
	case g.State != models.GuestActive:
		return s + ": everything shared with them was taken back."
	case v.Live == 0:
		return s + ": nothing shared now."
	}
	s += ": " + plural(v.Live, "thing") + " shared"
	if v.SignedIn > 0 {
		s += ", signed in now"
	}
	return s + "."
}

func upperFirst(s string) string {
	r := []rune(s)
	if len(r) > 0 && r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}

func sessionsByGuest(ctx context.Context, a *app.App) map[string]int {
	out := map[string]int{}
	var ss []models.GuestSession
	if err := a.DB.Find(ctx, store.CGuestSessions, a.WorkspaceID(), bson.M{"$expr": bson.M{"$gt": bson.A{"$expiresAt", "$$NOW"}}}, &ss,
		options.Find().SetProjection(bson.M{"guestId": 1}).SetLimit(10000)); err != nil {
		return out
	}
	for _, s := range ss {
		out[s.GuestID]++
	}
	return out
}

func projectNames(ctx context.Context, a *app.App, grants []models.GuestGrant) map[string]string {
	out := map[string]string{}
	list := []string{}
	for _, g := range grants {
		if _, ok := out[g.ProjectID]; !ok {
			out[g.ProjectID] = "a project"
			list = append(list, g.ProjectID)
		}
	}
	var ps []models.Project
	_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}}, &ps, options.Find().SetProjection(bson.M{"name": 1}))
	for _, p := range ps {
		out[p.ID] = p.Name
	}
	return out
}

func versionNumbers(ctx context.Context, a *app.App, grants []models.GuestGrant) map[string]int {
	out := map[string]int{}
	var list []string
	for _, g := range grants {
		list = append(list, g.RevisionIDs...)
	}
	if len(list) == 0 {
		return out
	}
	var rs []models.Revision
	_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}}, &rs, options.Find().SetProjection(bson.M{"number": 1}))
	for _, r := range rs {
		out[r.ID] = r.Number
	}
	return out
}

/* ---------- Taking things back ---------- */

func revoke(ctx context.Context, a *app.App, in *GuestsRevokeIn) (*GuestsRevokeOut, error) {
	switch {
	case (in.Grant == "") == (in.Guest == ""):
		return nil, apperr.New(apperr.InvalidInput, "Say what to take back: one thing shared, or everything shared with a guest.")
	case in.Grant != "" && !ids.Valid(ids.GuestGrant, in.Grant):
		return nil, apperr.New(apperr.InvalidInput, "That isn't something shared with a guest.").WithCommand("metarush guests list")
	}
	now := time.Now().UTC()
	by := a.Actor
	out := &GuestsRevokeOut{}
	var g *models.Guest
	if in.Grant != "" {
		var gr models.GuestGrant
		if err := a.DB.GetByID(ctx, store.CGuestGrants, a.WorkspaceID(), in.Grant, &gr); err != nil {
			return nil, apperr.New(apperr.NotFound, "There's nothing like that shared with a guest here.").WithCommand("metarush guests list --all")
		}
		res, err := a.DB.C(store.CGuestGrants).UpdateOne(ctx, bson.M{"_id": gr.ID, "workspaceId": a.WorkspaceID(), "revokedAt": nil}, bson.M{"$set": bson.M{"revokedAt": now, "revokedBy": by}})
		if err != nil {
			return nil, err
		}
		out.Ended = int(res.ModifiedCount)
		var e error
		if g, _, e = getGuest(ctx, a, gr.GuestID); e != nil {
			return nil, e
		}
		a.Audit(ctx, "guests.revoke", "guest_grant", gr.ID, gr.ProjectID, map[string]any{"guestId": gr.GuestID})
	} else {
		var err error
		if g, err = findGuest(ctx, a, in.Guest); err != nil {
			return nil, err
		}
		res, err := a.DB.C(store.CGuestGrants).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guestId": g.ID, "revokedAt": nil}, bson.M{"$set": bson.M{"revokedAt": now, "revokedBy": by}})
		if err != nil {
			return nil, err
		}
		out.Ended = int(res.ModifiedCount)
		if err := a.DB.Update(ctx, store.CGuests, a.WorkspaceID(), g.ID, bson.M{"$set": bson.M{"state": models.GuestRevoked, "revokedAt": now, "revokedBy": by}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "guests.revoke", "guest", g.ID, "", map[string]any{"grants": out.Ended})
		if g, _, err = getGuest(ctx, a, g.ID); err != nil {
			return nil, err
		}
	}
	// Taking a guest back, or their last grant, signs them out at once; with
	// grants left, every request is still held to what remains.
	left, err := liveGrants(ctx, a, g.ID)
	if err != nil {
		return nil, err
	}
	if g.State != models.GuestActive || len(left) == 0 {
		out.SignedOut = EndSessions(ctx, a, g.ID)
		revokeLinks(ctx, a, g.ID)
	}
	out.Guest = *g
	switch {
	case in.Guest != "":
		out.Summary = fmt.Sprintf("Took back everything shared with %s.", who(g))
	case out.Ended == 0:
		out.Summary = fmt.Sprintf("That was taken back or had ended already; %s has %s left.", who(g), plural(len(left), "thing"))
	case len(left) == 0:
		out.Summary = fmt.Sprintf("Took it back: nothing is shared with %s now.", who(g))
	default:
		out.Summary = fmt.Sprintf("Took it back: %s has %s shared still.", who(g), plural(len(left), "thing"))
	}
	if out.SignedOut > 0 {
		out.Summary += " " + plural(out.SignedOut, "browser") + " signed out."
	}
	return out, nil
}

// liveGrants lists a guest's grants that work now (by the database server's
// clock).
func liveGrants(ctx context.Context, a *app.App, guestID string) ([]models.GuestGrant, error) {
	f := liveGrant()
	f["guestId"] = guestID
	var gs []models.GuestGrant
	if err := a.DB.Find(ctx, store.CGuestGrants, a.WorkspaceID(), f, &gs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	return gs, nil
}

// purge ends the grants of a project cleared out for good (D196): nothing
// of it is shown again; the records of what was shared stay.
func purge(ctx context.Context, a *app.App, projectID string) (int, error) {
	now := time.Now().UTC()
	res, err := a.DB.C(store.CGuestGrants).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": projectID, "revokedAt": nil},
		bson.M{"$set": bson.M{"revokedAt": now, "endReason": "its project was cleared out"}})
	if err != nil {
		return 0, err
	}
	return int(res.ModifiedCount), nil
}
