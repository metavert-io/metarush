package conversation

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"golang.org/x/text/unicode/norm"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Conversations are records in the workspace (collection conversations);
// cuts pin what they render with. This file loads, finds and saves them.

func invalid(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }

// load finds a conversation by ID or name (within a project when given).
func load(ctx context.Context, a *app.App, ref, projectID string) (*models.Conversation, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, invalid("name a conversation")
	}
	var c models.Conversation
	if ids.Valid(ids.Conversation, ref) {
		if err := a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), ref, &c); err != nil {
			return nil, apperr.New(apperr.NotFound, "There's no conversation %s in this workspace.", ref)
		}
		return &c, nil
	}
	filter := bson.M{}
	if projectID != "" {
		filter["projectIds"] = projectID
	}
	var all []models.Conversation
	if err := a.DB.Find(ctx, store.CConversations, a.WorkspaceID(), filter, &all, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	var hits []models.Conversation
	for _, x := range all {
		if strings.EqualFold(strings.TrimSpace(x.Name), ref) || slug(x.Name) == slug(ref) {
			hits = append(hits, x)
		}
	}
	switch len(hits) {
	case 1:
		return &hits[0], nil
	case 0:
		return nil, apperr.New(apperr.NotFound, "There's no conversation called %q.", ref).WithHint("Each project's Conversation tab lists its conversations.").WithCommand("metarush conversation list")
	}
	return nil, apperr.New(apperr.InvalidInput, "%d conversations are called %q.", len(hits), ref).WithHint("Pick it on its project's Conversation tab, or give one of them another name.").WithCommand("metarush conversation list --json")
}

// save writes a conversation back, if nobody changed it meanwhile.
func save(ctx context.Context, a *app.App, c *models.Conversation) error {
	prev := c.Version
	c.Version++
	c.UpdatedAt = time.Now().UTC()
	res, err := a.DB.C(store.CConversations).ReplaceOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "version": prev}, c)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		c.Version = prev
		return apperr.New(apperr.Conflict, "Someone changed this conversation meanwhile.").WithHint("Run it again.")
	}
	return nil
}

// slug makes a short name from a person's name: "Maya K." → "maya-k".
func slug(s string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(s)) {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’':
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			if b.Len() == 0 && r >= '0' && r <= '9' {
				b.WriteString("p")
			}
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > 40 {
		out = strings.Trim(out[:40], "-")
	}
	if out == "" {
		out = "person"
	}
	return out
}

// memberID is a free short name for a member.
func memberID(c *models.Conversation, name string) string {
	base := slug(name)
	id := base
	for n := 2; ; n++ {
		if m, _ := c.Member(id); m == nil {
			return id
		}
		id = fmt.Sprintf("%s-%d", base, n)
	}
}

// titleName turns a file's title into a person's name: "maya_cam" → "Maya Cam".
func titleName(title string) string {
	f := strings.FieldsFunc(title, func(r rune) bool { return r == '_' || r == '-' || r == '.' || unicode.IsSpace(r) })
	for i, w := range f {
		r := []rune(w)
		r[0] = unicode.ToUpper(r[0])
		f[i] = string(r)
	}
	n := strings.Join(f, " ")
	if n == "" {
		return "Someone"
	}
	if len([]rune(n)) > 80 {
		n = string([]rune(n)[:80])
	}
	return n
}

// person finds someone in People by name (nil when nobody is called that).
func person(ctx context.Context, a *app.App, name string) *models.Person {
	key := strings.ToLower(strings.Join(strings.Fields(name), " "))
	if key == "" {
		return nil
	}
	var p models.Person
	if err := a.DB.C(store.CPeople).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "nameKey": key, "mergedInto": bson.M{"$in": bson.A{nil, ""}}}).Decode(&p); err != nil {
		return nil
	}
	return &p
}

// tagFrom is a name tag's second line from a person's notes: their first
// sentence, if it is short ("Host of The Garden Show").
func tagFrom(notes string) string {
	n := strings.TrimSpace(notes)
	if n == "" {
		return ""
	}
	if i := strings.IndexAny(n, ".\n;"); i > 0 {
		n = n[:i]
	}
	n = strings.TrimSpace(n)
	if len([]rune(n)) > 60 {
		return ""
	}
	return n
}

// assets loads the members' recordings.
func assets(ctx context.Context, a *app.App, c *models.Conversation) (map[string]*models.Asset, error) {
	var idList []string
	for _, m := range c.Members {
		idList = append(idList, m.AssetID)
	}
	var list []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idList}}, &list); err != nil {
		return nil, err
	}
	out := map[string]*models.Asset{}
	for i := range list {
		out[list[i].ID] = &list[i]
	}
	return out, nil
}

// length is the conversation's length on its clock: where the last recording ends.
func length(c *models.Conversation, as map[string]*models.Asset) float64 {
	end := 0.0
	for _, m := range c.Members {
		if x := as[m.AssetID]; x != nil && x.Media.Duration > 0 {
			end = math.Max(end, m.ClockAt(x.Media.Duration))
		}
	}
	return math.Round(end*1000) / 1000
}

// local is a recording's original on this machine (fetched once and cached).
func local(ctx context.Context, a *app.App, as *models.Asset) (string, error) {
	if as.State != models.AssetAvailable {
		return "", invalid("%s isn't ready yet (%s)", as.DisplayTitle(), as.State)
	}
	return a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
}

// projectFPS is the frame rate of a project's latest cut (30 without one).
func projectFPS(ctx context.Context, a *app.App, projectID string) int {
	rev, err := latest(ctx, a, projectID, "")
	if err != nil || rev == nil || rev.Spec.Output.FPS == 0 {
		return 30
	}
	return rev.Spec.Output.FPS
}

// latest is a project's latest version on a branch (main by default); nil without a cut.
func latest(ctx context.Context, a *app.App, projectID, branch string) (*models.Revision, error) {
	if branch == "" {
		branch = "main"
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no project %s in this workspace.", projectID)
	}
	h := p.Branches[branch].Head
	if h == "" {
		return nil, nil
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// resolveMembers turns the people asked for into members: their recordings,
// names (given, else from People by the recording's speakers, else the file's
// title), short names, uses and People links.
func resolveMembers(ctx context.Context, a *app.App, c *models.Conversation, projectID string, in []CallMemberIn) error {
	if len(in) == 0 {
		return invalid("a conversation needs at least one recording (members)")
	}
	if len(in) > 16 {
		return invalid("%d recordings is too many for one conversation (16 at most)", len(in))
	}
	seen := map[string]bool{}
	for _, mi := range in {
		as, err := ops.ResolveFootage(ctx, a, mi.AssetID, projectID)
		if err != nil {
			return err
		}
		if seen[as.ID] {
			return invalid("%s is in the conversation twice", as.DisplayTitle())
		}
		seen[as.ID] = true
		if projectID != "" && !containsStr(as.ProjectIDs, projectID) {
			return apperr.New(apperr.InvalidInput, "%s isn't in this project yet.", as.DisplayTitle()).WithHint("Add it to the project first, or make the conversation from the project's footage.").WithCommand("metarush asset link")
		}
		m := models.ConversationMember{}
		m.AssetID = as.ID
		m.Use = mi.Use
		switch m.Use {
		case "", editspec.UseBoth, editspec.UseSound, editspec.UsePicture:
		default:
			return invalid("use is both, sound or picture, not %q", m.Use)
		}
		if m.Use == "" {
			switch {
			case !as.Media.HasVideo:
				m.Use = editspec.UseSound
			case !as.Media.HasAudio:
				m.Use = editspec.UsePicture
			}
		}
		if m.Use == editspec.UseBoth {
			m.Use = "" // the default
		}
		name := strings.TrimSpace(mi.Name)
		if name == "" && m.Use != editspec.UsePicture {
			name = speakerName(ctx, a, as.ID)
		}
		if name == "" {
			name = titleName(as.DisplayTitle())
			if m.Use == editspec.UsePicture {
				name = as.DisplayTitle()
			}
		}
		if len([]rune(name)) > 80 {
			name = string([]rune(name)[:80])
		}
		m.Name = name
		m.ID = strings.TrimSpace(mi.ID)
		if m.ID != "" && !editspec.ConversationNamePattern.MatchString(m.ID) {
			return invalid("%q can't be a member's short name (lowercase letters, digits, - and _, starting with a letter)", m.ID)
		}
		if m.ID == "" || func() bool { x, _ := c.Member(m.ID); return x != nil }() {
			m.ID = memberID(c, name)
		}
		if m.Use != editspec.UsePicture {
			if p := person(ctx, a, name); p != nil {
				m.PersonID = p.ID
				m.Tag = tagFrom(p.Notes)
			}
			m.Framing = &editspec.Framing{Auto: editspec.FramingFaces}
		}
		c.Members = append(c.Members, m)
	}
	return nil
}

// speakerName is the one named voice in a recording ("" unless exactly one
// voice is named and it speaks most of it).
func speakerName(ctx context.Context, a *app.App, assetID string) string {
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetProjection(bson.M{"speakerName": 1, "from": 1, "to": 1})); err != nil {
		return ""
	}
	secs := map[string]float64{}
	total := 0.0
	for _, s := range segs {
		d := s.To - s.From
		total += d
		if s.SpeakerName != "" {
			secs[s.SpeakerName] += d
		}
	}
	best, bt := "", 0.0
	for n, d := range secs {
		if d > bt {
			best, bt = n, d
		}
	}
	if best == "" || bt < 0.6*total {
		return ""
	}
	return best
}

func containsStr(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// cutsUsing lists the latest cuts (main branch of each project) that pin a
// conversation, and whether they use its current alignment.
func cutsUsing(ctx context.Context, a *app.App, c *models.Conversation) []CallCutUse {
	var out []CallCutUse
	for _, pid := range c.ProjectIDs {
		rev, err := latest(ctx, a, pid, "")
		if err != nil || rev == nil {
			continue
		}
		for name, pin := range rev.Spec.Conversations {
			if pin.ID != c.ID {
				continue
			}
			out = append(out, CallCutUse{ProjectID: pid, RevisionID: rev.ID, Number: rev.Number, Name: name, Current: sameAlignment(pin, c.Pin())})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ProjectID < out[j].ProjectID })
	return out
}

// sameAlignment reports whether a cut's pin has the conversation's members at
// the same places on the clock (the picture settings may differ).
func sameAlignment(p, q editspec.ConversationPin) bool {
	if len(p.Members) != len(q.Members) {
		return false
	}
	at := map[string]editspec.ConversationMember{}
	for _, m := range q.Members {
		at[m.AssetID] = m
	}
	for _, m := range p.Members {
		o, ok := at[m.AssetID]
		if !ok || math.Abs(m.Start-o.Start) > 0.0005 || math.Abs(m.Drift-o.Drift) > 0.05 {
			return false
		}
	}
	return true
}
