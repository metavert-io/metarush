package guests

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Every guest request is recorded (D497): who, from which address, what,
// when, and whether it was turned away — a guest_activity record owners list
// (guests.activity, Settings → People → Guests), and an entry in the activity
// log with a plain phrase (logbook's guest phrases). A video's byte ranges
// are one record per browser and video a minute (a player asks for dozens),
// and refusals from one address that isn't signed in one a minute (a flood
// writes no more than that). Owners are told once a day per guest when a
// guest asks more than VolumePerHour times in an hour.

// Hit is one guest request to record.
type Hit struct {
	// Guest is who asked (nil: nobody signed in).
	Guest *models.Guest
	// What is what they did: guest.page, guest.signin, guest.watch…
	What       string
	ProjectID  string
	RevisionID string
	From       string
	Status     int
	Refused    bool
	Reason     string
	// Session and Media make a video's byte ranges one record a minute.
	Session string
	Media   string
}

// recent keeps what was recorded lately in this process: a video's ranges
// and refusals by address, and each guest's count this hour.
var recent = struct {
	sync.Mutex
	at    map[string]time.Time
	count map[string]*hour
}{at: map[string]time.Time{}, count: map[string]*hour{}}

type hour struct {
	start time.Time
	n     int
	told  bool
}

// quietFor says whether a record like this one was written in the last
// minute (and marks it written now).
func quietFor(key string) bool {
	recent.Lock()
	defer recent.Unlock()
	now := time.Now()
	if len(recent.at) > 50000 {
		recent.at = map[string]time.Time{}
	}
	if t, ok := recent.at[key]; ok && now.Sub(t) < time.Minute {
		return true
	}
	recent.at[key] = now
	return false
}

// countHit counts a guest's request this hour (this process's own count) and
// reports when it first passes VolumePerHour.
func countHit(guestID string) bool {
	recent.Lock()
	defer recent.Unlock()
	now := time.Now()
	h := recent.count[guestID]
	if h == nil || now.Sub(h.start) >= time.Hour {
		h = &hour{start: now}
		recent.count[guestID] = h
	}
	h.n++
	if h.n > VolumePerHour && !h.told {
		h.told = true
		return true
	}
	return false
}

// Record writes one guest request (never failing the request).
func Record(ctx context.Context, a *app.App, h Hit) {
	if h.Media != "" && quietFor("media:"+h.Session+":"+h.Media) {
		return
	}
	if h.Guest == nil && h.Refused && quietFor("refused:"+h.From+":"+h.Reason) {
		return
	}
	ctx = context.WithoutCancel(ctx)
	row := models.GuestActivity{ID: ids.New(ids.GuestActivity), WorkspaceID: a.WorkspaceID(), What: h.What, ProjectID: h.ProjectID, RevisionID: h.RevisionID,
		From: h.From, At: a.DB.NowOrLocal(ctx), Status: h.Status, Refused: h.Refused, Reason: h.Reason, Computer: a.Cfg.Installation.ID}
	actor := models.Actor{UserName: "someone not signed in", InstallationID: a.Cfg.Installation.ID, Via: models.ViaGuest}
	if h.Guest != nil {
		row.GuestID = h.Guest.ID
		actor = Actor(h.Guest, a.Cfg.Installation.ID)
	}
	if a.ReadOnly || a.Held() {
		return
	}
	if err := a.DB.Insert(ctx, store.CGuestActivity, row); err != nil {
		return
	}
	details := map[string]any{"from": h.From, "status": h.Status}
	if h.Refused {
		details["refused"], details["reason"] = true, h.Reason
	}
	if h.RevisionID != "" {
		details["revisionId"] = h.RevisionID
	}
	a.Log.Audit(ctx, models.AuditEvent{ID: row.ID, WorkspaceID: row.WorkspaceID, At: row.At, Actor: actor, Action: h.What, TargetType: "guest", TargetID: row.GuestID,
		ProjectID: h.ProjectID, Details: details})
	if h.Guest != nil && countHit(h.Guest.ID) {
		tellUnusual(ctx, a, h.Guest)
	}
}

// tellUnusual checks the hour's count in the database (every computer's
// requests) and tells the owners, once a day per guest.
func tellUnusual(ctx context.Context, a *app.App, g *models.Guest) {
	n, err := a.DB.C(store.CGuestActivity).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "guestId": g.ID,
		"$expr": bson.M{"$gt": bson.A{"$at", bson.M{"$subtract": bson.A{"$$NOW", time.Hour.Milliseconds()}}}}})
	if err != nil || n <= VolumePerHour {
		return
	}
	day := a.DB.NowOrLocal(ctx).Format("2006-01-02")
	for _, o := range owners(ctx, a) {
		_, _ = notify.Upsert(ctx, a, models.Notification{UserID: o, Kind: models.NotifyGuestVolume, GroupKey: "guest-volume:" + g.ID + ":" + day, Tone: "warning",
			Title: fmt.Sprintf("%s asked MetaRush unusually often", GuestName(g)),
			Body:  fmt.Sprintf("%s made more than %d requests in the last hour. If that isn't them watching, take back what you shared with them in Settings → People → Guests.", who(g), VolumePerHour),
			Link:  "/settings/people#guests", Details: map[string]any{"guestId": g.ID, "count": n}}, notify.Once)
	}
}

// owners are the people who hear about guests: the workspace's owners.
func owners(ctx context.Context, a *app.App) []string {
	var us []models.User
	_ = a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner, "state": models.UserActive}, &us, options.Find().SetProjection(bson.M{"_id": 1}))
	var out []string
	for _, u := range us {
		out = append(out, u.ID)
	}
	if len(out) == 0 && a.Cfg.User.ID != "" {
		out = append(out, a.Cfg.User.ID)
	}
	return out
}

/* ---------- The owners' list ---------- */

// GuestsActivityIn lists guests' requests, newest first.
type GuestsActivityIn struct {
	// Guest is one guest's (their email or ID); ProjectID one project's.
	Guest     string `json:"guest,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
	// Refused lists only requests that were turned away.
	Refused bool `json:"refused,omitempty"`
	Limit   int  `json:"limit,omitempty" jsonschema:"minimum=0,maximum=500"`
}

// ActivityRow is one request as owners read it.
type ActivityRow struct {
	Activity models.GuestActivity `json:"activity"`
	// Who is the guest ("someone not signed in" when nobody was).
	Who string `json:"who"`
	// Says it in plain words: "watched version 3 of “Spring promo”".
	Says string `json:"says"`
}

// GuestsActivityOut is the requests, and anything unusual.
type GuestsActivityOut struct {
	Rows []ActivityRow `json:"rows"`
	// Unusual says which guests asked more than usual in the last hour.
	Unusual []string `json:"unusual"`
	Summary string   `json:"summary"`
}

func init() {
	ops.Def("guests.activity", "What guests did, newest first: who, from which address, what they opened, watched or wrote, and what was turned away; and who asked unusually often (owners)", ops.Flags{OwnerOnly: true},
		activity)
}

func activity(ctx context.Context, a *app.App, in *GuestsActivityIn) (*GuestsActivityOut, error) {
	f := bson.M{}
	if in.Guest != "" {
		g, err := findGuest(ctx, a, in.Guest)
		if err != nil {
			return nil, err
		}
		f["guestId"] = g.ID
	}
	if in.ProjectID != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		f["projectId"] = p.ID
	}
	if in.Refused {
		f["refused"] = true
	}
	limit := in.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	var rows []models.GuestActivity
	if err := a.DB.Find(ctx, store.CGuestActivity, a.WorkspaceID(), f, &rows, options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(int64(limit))); err != nil {
		return nil, err
	}
	gs := map[string]*models.Guest{}
	projects := map[string]string{}
	revs := map[string]int{}
	for _, r := range rows {
		if r.GuestID != "" && gs[r.GuestID] == nil {
			if g, _, err := getGuest(ctx, a, r.GuestID); err == nil {
				gs[r.GuestID] = g
			}
		}
		if r.ProjectID != "" {
			projects[r.ProjectID] = ""
		}
		if r.RevisionID != "" {
			revs[r.RevisionID] = 0
		}
	}
	var pl, rl []string
	for k := range projects {
		pl = append(pl, k)
	}
	for k := range revs {
		rl = append(rl, k)
	}
	var ps []models.Project
	_ = a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": pl}}, &ps, options.Find().SetProjection(bson.M{"name": 1}))
	for _, p := range ps {
		projects[p.ID] = p.Name
	}
	var rs []models.Revision
	_ = a.DB.Find(ctx, store.CRevisions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": rl}}, &rs, options.Find().SetProjection(bson.M{"number": 1}))
	for _, r := range rs {
		revs[r.ID] = r.Number
	}
	out := &GuestsActivityOut{Rows: []ActivityRow{}, Unusual: []string{}}
	for _, r := range rows {
		row := ActivityRow{Activity: r, Who: "someone not signed in"}
		if g := gs[r.GuestID]; g != nil {
			row.Who = GuestName(g)
		}
		row.Says = activityWords(r, projects[r.ProjectID], revs[r.RevisionID])
		out.Rows = append(out.Rows, row)
	}
	out.Unusual = unusualNow(ctx, a)
	switch {
	case len(out.Rows) == 0:
		out.Summary = "No guest has opened anything yet."
	default:
		out.Summary = fmt.Sprintf("The last %s by guests.", plural(len(out.Rows), "request"))
	}
	if len(out.Unusual) > 0 {
		out.Summary += " Unusually many in the last hour: " + joinAnd(out.Unusual) + "."
	}
	return out, nil
}

// activityWords says one request plainly, from the activity log's phrase.
func activityWords(r models.GuestActivity, project string, number int) string {
	s := logbook.GuestPhrase(r.What)
	where := ""
	switch {
	case number > 0 && project != "":
		where = fmt.Sprintf("version %d of “%s”", number, project)
	case project != "":
		where = "“" + project + "”"
	}
	s = strings.ReplaceAll(s, "{v}", firstOf(where, "a version"))
	if r.Refused {
		s += " — turned away (" + refusalWords(r.Reason) + ")"
	}
	if r.From != "" {
		s += ", from " + r.From
	}
	return s
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// refusalWords says why a request was turned away.
func refusalWords(reason string) string {
	switch reason {
	case ReasonNotShared:
		return "not shared with them"
	case ReasonSignedOut:
		return "not signed in"
	case ReasonGuestsOff:
		return "guests are off"
	case ReasonKeptHere:
		return "a client's rules keep it here"
	case ReasonNotReady:
		return "no review copy yet"
	case ReasonHeld:
		return "it isn't shown to guests"
	case ReasonViewer:
		return "a viewer can't comment"
	case ReasonTooMany:
		return "too many comments"
	case ReasonLinkGone:
		return "the sign-in link didn't work"
	case ReasonNotTrusted, ReasonMSGuest:
		return "the provider didn't vouch for the email"
	case ReasonOtherAcct:
		return "another account than theirs"
	case ReasonNoSSO:
		return "that sign-in isn't offered"
	case ReasonThrottled:
		return "too many requests"
	case ReasonOther:
		return "another address"
	case ReasonCSRF:
		return "not from the guest page"
	}
	return "refused"
}

// unusualNow names the guests who asked more than VolumePerHour times in the
// last hour (the database server's clock).
func unusualNow(ctx context.Context, a *app.App) []string {
	cur, err := a.DB.C(store.CGuestActivity).Aggregate(ctx, []bson.M{
		{"$match": bson.M{"workspaceId": a.WorkspaceID(), "guestId": bson.M{"$exists": true, "$ne": ""},
			"$expr": bson.M{"$gt": bson.A{"$at", bson.M{"$subtract": bson.A{"$$NOW", time.Hour.Milliseconds()}}}}}},
		{"$group": bson.M{"_id": "$guestId", "n": bson.M{"$sum": 1}}},
		{"$match": bson.M{"n": bson.M{"$gt": VolumePerHour}}},
	})
	if err != nil {
		return []string{}
	}
	defer cur.Close(ctx)
	out := []string{}
	for cur.Next(ctx) {
		var r struct {
			ID string `bson:"_id"`
			N  int    `bson:"n"`
		}
		if cur.Decode(&r) != nil {
			continue
		}
		if g, _, err := getGuest(ctx, a, r.ID); err == nil {
			out = append(out, fmt.Sprintf("%s (%d)", GuestName(g), r.N))
		}
	}
	return out
}

/* ---------- Keeping it tidy ---------- */

// Run is the background MetaRush's share: every hour, guests' requests older
// than KeepActivity, sign-ins that ended and links used or ended a week ago
// go — unless the workspace is a restored copy whose deletions are paused.
func Run(ctx context.Context, a *app.App, logf func(string, ...any)) {
	t := time.NewTimer(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if n := Tidy(ctx, a); n > 0 && logf != nil {
				logf("guests: cleared %s of old guest records", plural(n, "record"))
			}
			t.Reset(time.Hour)
		}
	}
}

// Tidy clears what has had its day and says how many records went.
func Tidy(ctx context.Context, a *app.App) int {
	if a.ReadOnly || a.Held() {
		return 0
	}
	if a.DB.DeletionsPaused(ctx, a.WorkspaceID()) {
		return 0
	}
	n := 0
	del := func(coll string, f bson.M) {
		f["workspaceId"] = a.WorkspaceID()
		if res, err := a.DB.C(coll).DeleteMany(ctx, f); err == nil {
			n += int(res.DeletedCount)
		}
	}
	del(store.CGuestActivity, store.StaleBefore("at", KeepActivity))
	del(store.CGuestSessions, store.DueBy("expiresAt"))
	del(store.CGuestLinks, store.StaleBefore("expiresAt", 7*24*time.Hour))
	return n
}
