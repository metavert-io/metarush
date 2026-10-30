package generate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Looks (D126): how a person or a fictional character appears in generated
// video — 2 or 3 stills and up to three clips of 3 seconds or less taken from
// real footage (the faces index finds them), a fixed description of who they
// are and how they look, defaults and a test sheet. Only people create,
// approve or revoke a look. A person's look relies on their consent; a
// character's may not be made from a real person's face.

// LookStillIn is a still for a look: a frame of footage, or a picture.
type LookStillIn struct {
	AssetID  string  `json:"assetId,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$"`
	At       float64 `json:"at,omitempty" jsonschema:"minimum=0"`
	Resource string  `json:"resource,omitempty" jsonschema:"maxLength=200,description=A picture from the resources library (short name or name@version)."`
}

// LookClipIn is a short part of a clip for a look (3 seconds or less).
type LookClipIn struct {
	AssetID string  `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	From    float64 `json:"from" jsonschema:"minimum=0"`
	To      float64 `json:"to" jsonschema:"minimum=0"`
}

// LookCreateIn makes a look. People only.
type LookCreateIn struct {
	Title       string        `json:"title" jsonschema:"minLength=1,maxLength=80,description=How you name it: Theo\\, Captain Blue."`
	Name        string        `json:"name,omitempty" jsonschema:"maxLength=40,description=Short name (default: from the title)."`
	Person      string        `json:"person,omitempty" jsonschema:"maxLength=120,description=Whose look it is (a person on the People page). Leave empty for a fictional character."`
	Style       string        `json:"style,omitempty" jsonschema:"enum=,enum=realistic,enum=stylized"`
	StyleNote   string        `json:"styleNote,omitempty" jsonschema:"maxLength=300,description=For a stylized look: the style\\, e.g. a watercolor illustration."`
	Description string        `json:"description" jsonschema:"minLength=10,maxLength=1500,description=Who they are and how they look\\, sent with every shot."`
	Stills      []LookStillIn `json:"stills" jsonschema:"minItems=1,maxItems=3"`
	Clips       []LookClipIn  `json:"clips,omitempty" jsonschema:"maxItems=3"`
	AspectRatio string        `json:"aspectRatio,omitempty" jsonschema:"enum=,enum=16:9,enum=9:16"`
	Seconds     int           `json:"seconds,omitempty" jsonschema:"minimum=0,maximum=60"`
	Quality     string        `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
	// NoRealPeople: for a character made from frames or pictures MetaRush can't check, you say they show no real person.
	NoRealPeople bool `json:"noRealPeople,omitempty"`
}

// LookUpdateIn changes a look. Changing what it shows (stills, clips,
// description, style) makes a new version that needs approving again. People only.
type LookUpdateIn struct {
	Look         string        `json:"look" jsonschema:"minLength=1,maxLength=80,description=Its short name or ID."`
	Title        string        `json:"title,omitempty" jsonschema:"maxLength=80"`
	Style        string        `json:"style,omitempty" jsonschema:"enum=,enum=realistic,enum=stylized"`
	StyleNote    *string       `json:"styleNote,omitempty" jsonschema:"maxLength=300"`
	Description  string        `json:"description,omitempty" jsonschema:"maxLength=1500"`
	Stills       []LookStillIn `json:"stills,omitempty" jsonschema:"maxItems=3,description=Replaces the stills."`
	Clips        []LookClipIn  `json:"clips,omitempty" jsonschema:"maxItems=3,description=Replaces the clips."`
	ClearClips   bool          `json:"clearClips,omitempty"`
	AspectRatio  string        `json:"aspectRatio,omitempty" jsonschema:"enum=,enum=16:9,enum=9:16"`
	Seconds      int           `json:"seconds,omitempty" jsonschema:"minimum=0,maximum=60"`
	Quality      string        `json:"quality,omitempty" jsonschema:"enum=,enum=draft,enum=final"`
	NoRealPeople bool          `json:"noRealPeople,omitempty"`
}

// LookRefIn names a look.
type LookRefIn struct {
	Look string `json:"look" jsonschema:"minLength=1,maxLength=80,description=Its short name or ID."`
}

// LookDecideIn approves or revokes a look. People only.
type LookDecideIn struct {
	Look string `json:"look" jsonschema:"minLength=1,maxLength=80"`
	Note string `json:"note,omitempty" jsonschema:"maxLength=500"`
}

// LookListIn lists looks.
type LookListIn struct {
	Person string `json:"person,omitempty" jsonschema:"maxLength=120,description=One person's looks (name or ID)."`
	All    bool   `json:"all,omitempty" jsonschema:"description=Revoked looks too."`
}

// LookView is a look with what people read about it.
type LookView struct {
	models.GenLook
	// Usable: it can be used for new shots now; Problems say why not.
	Usable   bool         `json:"usable"`
	Problems []string     `json:"problems,omitempty"`
	Consent  *ConsentView `json:"consent,omitempty"`
	Ref      string       `json:"ref" jsonschema:"description=name@version."`
	Summary  string       `json:"summary"`
	// TestShots are the takes of its test sheet (on show).
	TestShots []GenView `json:"testShots,omitempty"`
}

// LookListOut lists looks.
type LookListOut struct {
	Looks   []LookView `json:"looks"`
	Summary string     `json:"summary"`
}

// LookOut is one look after a change.
type LookOut struct {
	Look    LookView `json:"look"`
	Summary string   `json:"summary"`
}

var lookNameRe = regexp.MustCompile(`[^a-z0-9]+`)

func lookName(s string) string {
	n := strings.Trim(lookNameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	return n
}

func findLook(ctx context.Context, a *app.App, ref string) (*models.GenLook, error) {
	ref = strings.TrimSpace(ref)
	var l models.GenLook
	if ids.Valid(ids.Look, ref) {
		if err := a.DB.GetByID(ctx, store.CLooks, a.WorkspaceID(), ref, &l); err == nil {
			return &l, nil
		}
	} else if err := a.DB.C(store.CLooks).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": lookName(strings.SplitN(ref, "@", 2)[0])}).Decode(&l); err == nil {
		return &l, nil
	}
	return nil, apperr.New(apperr.NotFound, "There's no look called “%s”.", ref).WithHint("Looks are on the People page and in Generate a shot.").WithCommand("metarush looks list")
}

// namedFace is a face the index shows, named by a person.
type namedFace struct {
	PersonID, Name, FaceID string
	// By says who named it when it wasn't a person (D274).
	By string
}

// Faces is who the on-screen index shows in part of a clip.
type Faces struct {
	// Named are the people a person named.
	Named []NamedFace
	// Unnamed counts the faces no person named; Resemble are whom they look
	// like (recognized by looks only, D114: never a name that consent can
	// cover); AppNamed are those an AI app, Auto or Claude named, which don't
	// count either (D274).
	Unnamed  int
	Resemble []NamedFace
	AppNamed []NamedFace
	// Known is false when the clip hasn't been looked at (MetaRush can't tell).
	Known bool
}

// FacesSeen says who the on-screen index shows in [from, to] of a clip.
func FacesSeen(ctx context.Context, a *app.App, as *models.Asset, from, to float64) Faces {
	var out Faces
	if as.Analysis["visual"] != "available" {
		return out
	}
	out.Known = true
	var fs []models.FaceTrack
	_ = a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), bson.M{"assetId": as.ID, "from": bson.M{"$lte": to + 0.25}, "to": bson.M{"$gte": from - 0.25}}, &fs)
	seen, alike := map[string]bool{}, map[string]bool{}
	for _, f := range fs {
		if f.PersonID != "" && !f.NamedByPerson() {
			out.Unnamed++
			out.AppNamed = append(out.AppNamed, namedFace{PersonID: f.PersonID, Name: f.Name, FaceID: f.ID, By: f.Naming.Words()})
			continue
		}
		if f.PersonID == "" {
			out.Unnamed++
			if r := f.Recognized; r != nil && r.Name != "" && !alike[r.PersonID] {
				alike[r.PersonID] = true
				out.Resemble = append(out.Resemble, namedFace{PersonID: r.PersonID, Name: r.Name, FaceID: f.ID})
			}
			continue
		}
		if !seen[f.PersonID] {
			seen[f.PersonID] = true
			out.Named = append(out.Named, namedFace{PersonID: f.PersonID, Name: f.Name, FaceID: f.ID})
		}
	}
	return out
}

// facesIn says who the on-screen index shows in [from, to] of a clip: the
// people named, how many faces nobody named, and whom those look like (a hint
// for the message). known is false when the clip hasn't been looked at.
func facesIn(ctx context.Context, a *app.App, as *models.Asset, from, to float64) (named []namedFace, unnamed int, resembles []string, known bool) {
	f := FacesSeen(ctx, a, as, from, to)
	for _, r := range f.Resemble {
		resembles = append(resembles, r.Name)
	}
	return f.Named, f.Unnamed, resembles, f.Known
}

func lookVersion(l *models.GenLook) string {
	canon := struct {
		Subject, PersonID, Style, StyleNote, Description string
		Stills, Clips                                    []string
	}{Subject: l.Subject, PersonID: l.PersonID, Style: l.Style, StyleNote: l.StyleNote, Description: l.Description}
	for _, s := range l.Stills {
		canon.Stills = append(canon.Stills, s.SHA256)
	}
	for _, c := range l.Clips {
		canon.Clips = append(canon.Clips, c.SHA256)
	}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:12]
}

// lookStills takes and keeps a look's stills, checking who they show.
func lookStills(ctx context.Context, a *app.App, l *models.GenLook, in []LookStillIn, noRealPeople bool) ([]models.LookStill, error) {
	if len(in) == 0 || len(in) > 3 {
		return nil, apperr.New(apperr.InvalidInput, "A look has 1 to 3 stills (2 or 3 to approve it).")
	}
	var out []models.LookStill
	for i, s := range in {
		var st *stored
		ls := models.LookStill{Ref: fmt.Sprintf("still-%d", i+1)}
		switch {
		case s.AssetID != "":
			as, err := loadVideo(ctx, a, s.AssetID)
			if err != nil {
				return nil, err
			}
			if err := checkLookFrames(ctx, a, l, as, s.At, s.At, noRealPeople, &ls.FaceID); err != nil {
				return nil, err
			}
			if st, err = still(ctx, a, as, s.At); err != nil {
				return nil, err
			}
			ls.AssetID, ls.At = as.ID, s.At
		case s.Resource != "":
			if l.Subject == models.LookCharacter && !noRealPeople {
				return nil, consentRequired("Say that the picture “%s” shows no real person: a character's look can't be made from someone's face.", s.Resource).
					WithDetails(map[string]any{"reason": "no_real_people"})
			}
			var r *models.Resource
			var err error
			if st, r, _, err = picture(ctx, a, "", s.Resource); err != nil {
				return nil, err
			}
			ls.ResourceID = r.ID
		default:
			return nil, apperr.New(apperr.InvalidInput, "Each still is a frame of a clip or a picture.")
		}
		ls.SHA256, ls.StorageKey, ls.Size, ls.Width, ls.Height = st.SHA256, st.Key, st.Size, st.Width, st.Height
		out = append(out, ls)
	}
	return out, nil
}

// checkLookFrames: a person's look shows that person (not someone else); a
// character's shows no real person.
func checkLookFrames(ctx context.Context, a *app.App, l *models.GenLook, as *models.Asset, from, to float64, noRealPeople bool, faceID *string) error {
	named, unnamed, _, known := facesIn(ctx, a, as, from, to)
	if l.Subject == models.LookCharacter {
		switch {
		case len(named) > 0:
			return consentRequired("That moment of “%s” shows %s. A character's look can't be made from a real person's face; make a look for them instead, with their consent.", as.DisplayTitle(), named[0].Name).
				WithDetails(map[string]any{"reason": "real_person"})
		case unnamed > 0:
			return consentRequired("That moment of “%s” shows someone's face. A character's look can't be made from a real person.", as.DisplayTitle()).
				WithDetails(map[string]any{"reason": "real_person"})
		case !known && !noRealPeople:
			return consentRequired("MetaRush can't tell whether that moment of “%s” shows a real person. Say that it doesn't, or pick a picture of the character.", as.DisplayTitle()).
				WithDetails(map[string]any{"reason": "no_real_people"})
		}
		return nil
	}
	// A face named as someone else counts against it whoever named it; only a
	// person's name counts for it (D274).
	for _, n := range append(append([]namedFace{}, named...), FacesSeen(ctx, a, as, from, to).AppNamed...) {
		if n.PersonID != l.PersonID {
			return consentRequired("That moment of “%s” shows %s, not %s. Pick a moment where only %s is on screen.", as.DisplayTitle(), n.Name, l.PersonName, l.PersonName).
				WithDetails(map[string]any{"reason": "someone_else"})
		}
		if n.By != "" {
			continue
		}
		if faceID != nil {
			*faceID = n.FaceID
		}
	}
	return nil
}

func lookClips(ctx context.Context, a *app.App, l *models.GenLook, in []LookClipIn, noRealPeople bool) ([]models.LookClip, error) {
	if len(in) > 3 {
		return nil, apperr.New(apperr.InvalidInput, "A look has at most 3 clips.")
	}
	var out []models.LookClip
	for i, c := range in {
		if d := c.To - c.From; d <= 0.5 || d > 3.0001 {
			return nil, apperr.New(apperr.InvalidInput, "Each clip of a look is between half a second and 3 seconds long.")
		}
		as, err := loadVideo(ctx, a, c.AssetID)
		if err != nil {
			return nil, err
		}
		if err := checkLookFrames(ctx, a, l, as, c.From, c.To, noRealPeople, nil); err != nil {
			return nil, err
		}
		st, err := part(ctx, a, as, c.From, c.To, true)
		if err != nil {
			return nil, err
		}
		out = append(out, models.LookClip{Ref: fmt.Sprintf("clip-%d", i+1), AssetID: as.ID, From: c.From, To: c.To, SHA256: st.SHA256, StorageKey: st.Key, Size: st.Size,
			Seconds: st.Seconds})
	}
	return out, nil
}

func createLook(ctx context.Context, a *app.App, in *LookCreateIn) (*LookOut, error) {
	now, err := Now(ctx, a) // consent in force by the server's clock (D360)
	if err != nil {
		return nil, err
	}
	l := &models.GenLook{ID: ids.New(ids.Look), WorkspaceID: a.WorkspaceID(), Title: strings.TrimSpace(in.Title), Style: in.Style, StyleNote: strings.TrimSpace(in.StyleNote),
		Description: strings.TrimSpace(in.Description), State: models.LookDraft, CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now,
		Defaults: models.LookDefaults{AspectRatio: in.AspectRatio, Seconds: in.Seconds, Quality: in.Quality}, Clips: []models.LookClip{}}
	if l.Style == "" {
		l.Style = models.LookRealistic
	}
	if l.Style == models.LookStylized && l.StyleNote == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what the style is, for example a watercolor illustration or a 3D animated character.")
	}
	l.Name = lookName(firstNonEmpty(in.Name, in.Title))
	if l.Name == "" {
		return nil, apperr.New(apperr.InvalidInput, "Give the look a name with letters or numbers.")
	}
	if len([]rune(l.Description)) < 10 {
		return nil, apperr.New(apperr.InvalidInput, "Describe who they are and how they look in a sentence or two; it goes with every shot.")
	}
	if strings.TrimSpace(in.Person) == "" {
		l.Subject = models.LookCharacter
	} else {
		l.Subject = models.LookPerson
		p, err := personFor(ctx, a, in.Person, false)
		if err != nil {
			return nil, err
		}
		l.PersonID, l.PersonName = p.ID, p.Name
		c, err := activeConsentFor(ctx, a, p.ID, "", "", now) // a look serves shots and presenters alike (D274)
		if err != nil {
			return nil, consentRequired("A look of %s needs their consent first. Record it on the People page: your own statement if it's you, or their recorded consent.", p.Name).
				WithDetails(map[string]any{"reason": "consent", "personId": p.ID}).WithCommand("metarush consent record --person " + quoteArg(p.Name))
		}
		l.ConsentID = c.ID
	}
	if l.Stills, err = lookStills(ctx, a, l, in.Stills, in.NoRealPeople); err != nil {
		return nil, err
	}
	if l.Clips, err = lookClips(ctx, a, l, in.Clips, in.NoRealPeople); err != nil {
		return nil, err
	}
	if l.Clips == nil {
		l.Clips = []models.LookClip{}
	}
	l.Version = lookVersion(l)
	if err := a.DB.Insert(ctx, store.CLooks, l); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, apperr.New(apperr.Conflict, "There's already a look called “%s”. Give this one another name.", l.Name)
		}
		return nil, err
	}
	a.Audit(ctx, "looks.create", "look", l.ID, "", map[string]any{"name": l.Name, "subject": l.Subject, "personId": l.PersonID, "consentId": l.ConsentID,
		"stills": len(l.Stills), "clips": len(l.Clips), "version": l.Version, "noRealPeople": in.NoRealPeople})
	v := lookView(ctx, a, l, false)
	return &LookOut{Look: v, Summary: fmt.Sprintf("Made the look “%s”. %s", l.Title, v.Summary)}, nil
}

func quoteArg(s string) string {
	if strings.ContainsAny(s, " '\"") {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	return s
}

func updateLook(ctx context.Context, a *app.App, in *LookUpdateIn) (*LookOut, error) {
	l, err := findLook(ctx, a, in.Look)
	if err != nil {
		return nil, err
	}
	if l.State == models.LookRevoked {
		return nil, apperr.New(apperr.Conflict, "The look “%s” was withdrawn; make a new one instead.", l.Title)
	}
	before := l.Version
	if t := strings.TrimSpace(in.Title); t != "" {
		l.Title = t
	}
	if in.Style != "" {
		l.Style = in.Style
	}
	if in.StyleNote != nil {
		l.StyleNote = strings.TrimSpace(*in.StyleNote)
	}
	if d := strings.TrimSpace(in.Description); d != "" {
		l.Description = d
	}
	if len(in.Stills) > 0 {
		if l.Stills, err = lookStills(ctx, a, l, in.Stills, in.NoRealPeople); err != nil {
			return nil, err
		}
	}
	switch {
	case in.ClearClips:
		l.Clips = []models.LookClip{}
	case len(in.Clips) > 0:
		if l.Clips, err = lookClips(ctx, a, l, in.Clips, in.NoRealPeople); err != nil {
			return nil, err
		}
	}
	if in.AspectRatio != "" {
		l.Defaults.AspectRatio = in.AspectRatio
	}
	if in.Seconds > 0 {
		l.Defaults.Seconds = in.Seconds
	}
	if in.Quality != "" {
		l.Defaults.Quality = in.Quality
	}
	if l.Style == models.LookStylized && l.StyleNote == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what the style is, for example a watercolor illustration.")
	}
	l.Version = lookVersion(l)
	set := bson.M{"title": l.Title, "style": l.Style, "styleNote": l.StyleNote, "description": l.Description, "stills": l.Stills, "clips": l.Clips,
		"defaults": l.Defaults, "version": l.Version, "updatedAt": time.Now().UTC()}
	unset := bson.M{}
	changed := l.Version != before
	if changed && l.State == models.LookApproved {
		// What it shows changed: a person approves the new version again.
		set["state"] = models.LookDraft
		unset["approvedAt"], unset["approvedBy"] = "", ""
		l.State, l.ApprovedAt, l.ApprovedBy = models.LookDraft, nil, nil
	}
	upd := bson.M{"$set": set}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	if err := a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, upd); err != nil {
		return nil, err
	}
	a.Audit(ctx, "looks.update", "look", l.ID, "", map[string]any{"name": l.Name, "version": l.Version, "previous": before})
	v := lookView(ctx, a, l, false)
	s := fmt.Sprintf("Changed the look “%s”.", l.Title)
	if changed {
		s += " What it shows changed, so it needs approving again before it's used."
	}
	return &LookOut{Look: v, Summary: s}, nil
}

func decideLook(ctx context.Context, a *app.App, in *LookDecideIn, approve bool) (*LookOut, error) {
	l, err := findLook(ctx, a, in.Look)
	if err != nil {
		return nil, err
	}
	now, err := Now(ctx, a) // consent in force by the server's clock (D360)
	if err != nil {
		return nil, err
	}
	actor := a.Actor
	if approve {
		if l.State == models.LookRevoked {
			return nil, apperr.New(apperr.Conflict, "The look “%s” was withdrawn; make a new one instead.", l.Title)
		}
		if n := len(l.Stills); n < 2 {
			return nil, apperr.New(apperr.InvalidInput, "A look needs 2 or 3 stills before it's approved; “%s” has %d.", l.Title, n)
		}
		if l.Subject == models.LookPerson {
			c, err := activeConsentFor(ctx, a, l.PersonID, "", "", now) // a look serves shots and presenters alike (D274)
			if err != nil {
				return nil, consentRequired("%s's consent isn't in force, so the look can't be approved.", l.PersonName).WithDetails(map[string]any{"reason": "consent"})
			}
			if c.ID != l.ConsentID {
				_ = a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, bson.M{"$set": bson.M{"consentId": c.ID}})
				l.ConsentID = c.ID
			}
		}
		if err := a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, bson.M{"$set": bson.M{"state": models.LookApproved, "approvedAt": now, "approvedBy": actor, "updatedAt": now}}); err != nil {
			return nil, err
		}
		l.State, l.ApprovedAt, l.ApprovedBy = models.LookApproved, &now, &actor
		a.Audit(ctx, "looks.approve", "look", l.ID, "", map[string]any{"name": l.Name, "version": l.Version, "note": in.Note})
		return &LookOut{Look: lookView(ctx, a, l, false), Summary: fmt.Sprintf("Approved the look “%s” (version %s). It can be used for new shots.", l.Title, l.Version[:6])}, nil
	}
	if l.State == models.LookRevoked {
		return &LookOut{Look: lookView(ctx, a, l, false), Summary: "It was withdrawn already."}, nil
	}
	if err := a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, bson.M{"$set": bson.M{"state": models.LookRevoked, "revokedAt": now, "revokedBy": actor,
		"revokeNote": strings.TrimSpace(in.Note), "updatedAt": now}}); err != nil {
		return nil, err
	}
	l.State, l.RevokedAt, l.RevokedBy = models.LookRevoked, &now, &actor
	a.Audit(ctx, "looks.revoke", "look", l.ID, "", map[string]any{"name": l.Name, "version": l.Version, "note": in.Note})
	return &LookOut{Look: lookView(ctx, a, l, false), Summary: fmt.Sprintf("Withdrew the look “%s”. Nothing new is made with it; clips already made stay.", l.Title)}, nil
}

// usable says whether a look can be used for a project now, and why not.
func usable(ctx context.Context, a *app.App, l *models.GenLook, projectID string, now time.Time) (*models.Consent, []string) {
	var why []string
	switch l.State {
	case models.LookRevoked:
		why = append(why, fmt.Sprintf("The look “%s” was withdrawn.", l.Title))
	case models.LookDraft:
		why = append(why, fmt.Sprintf("The look “%s” isn't approved yet. A person approves it on the People page after checking its test shots.", l.Title))
	}
	var c *models.Consent
	if l.Subject == models.LookPerson {
		var err error
		if c, err = activeConsent(ctx, a, l.PersonID, projectID, now); err != nil {
			why = append(why, fmt.Sprintf("%s's consent doesn't cover this project, or isn't in force any more.", l.PersonName))
		}
	}
	return c, why
}

func lookView(ctx context.Context, a *app.App, l *models.GenLook, withTests bool) LookView {
	now := time.Now().UTC()
	v := LookView{GenLook: *l, Ref: l.Name + "@" + l.Version}
	// Projects are checked when a shot asks; here it's whether a consent is in force at all.
	_, why := usable(ctx, a, l, "", now)
	v.Usable, v.Problems = len(why) == 0, why
	if l.ConsentID != "" {
		if cc, err := loadConsent(ctx, a, l.ConsentID); err == nil {
			cv := consentView(ctx, a, *cc)
			v.Consent = &cv
		}
	}
	who := "a fictional character"
	if l.Subject == models.LookPerson {
		who = l.PersonName
	}
	style := "realistic"
	if l.Style == models.LookStylized {
		style = "stylized (" + l.StyleNote + ")"
	}
	state := map[string]string{models.LookDraft: "not approved yet", models.LookApproved: "approved", models.LookRevoked: "withdrawn"}[l.State]
	v.Summary = fmt.Sprintf("%s: %s, %s, %s and %s; %s.", l.Title, who, style, plural(len(l.Stills), "still"), plural(len(l.Clips), "clip"), state)
	if withTests && len(l.Tests) > 0 {
		var gids bson.A
		for _, t := range l.Tests {
			gids = append(gids, t.GenerationID)
		}
		var gs []models.Generation
		_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": gids}}, &gs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}))
		v.TestShots = views(ctx, a, gs)
	}
	return v
}

func listLooks(ctx context.Context, a *app.App, in *LookListIn) (*LookListOut, error) {
	filter := bson.M{}
	if !in.All {
		filter["state"] = bson.M{"$ne": models.LookRevoked}
	}
	if in.Person != "" {
		p, err := personFor(ctx, a, in.Person, false)
		if err != nil {
			return nil, err
		}
		filter["personId"] = p.ID
	}
	var ls []models.GenLook
	if err := a.DB.Find(ctx, store.CLooks, a.WorkspaceID(), filter, &ls, options.Find().SetSort(bson.D{{Key: "title", Value: 1}})); err != nil {
		return nil, err
	}
	out := &LookListOut{Looks: []LookView{}}
	ready := 0
	for i := range ls {
		v := lookView(ctx, a, &ls[i], false)
		if v.Usable {
			ready++
		}
		out.Looks = append(out.Looks, v)
	}
	out.Summary = fmt.Sprintf("%s, %d ready to use.", plural(len(ls), "look"), ready)
	if len(ls) == 0 {
		out.Summary = "No looks yet."
	}
	return out, nil
}
