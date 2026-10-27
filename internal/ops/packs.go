package ops

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

// Layout packs (D263–D267), a Content Settings part: the title cards, lower
// thirds, end screens, quote and chapter cards and the call layouts every
// project using the settings draws with. Graphics in cuts name their role;
// checking a change fills a role from the pack (FillRoles, in prepare), new
// cuts follow it, and after a change the latest cuts are offered "Use the
// layout pack on the latest cut" (content.pack.apply, a checked change).
// Code (project motion graphics) reaches a pack only through a person: a
// pack with graphics is proposed (content.pack.propose, a draft anyone or
// Claude may make) and becomes current when a person accepts it
// (content.pack.accept, people only) once every graphic is approved.

// ContentPackIn names whose layout pack ("" : the default's).
type ContentPackIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the layout pack of the settings it uses. Empty: the default's."`
}

// PackRoleOut is one role of the pack in use.
type PackRoleOut struct {
	// Role is the role's name (title-card, lower-third:guest, picture-in-picture).
	Role    string `json:"role"`
	Words   string `json:"words" jsonschema:"description=How people say it (title card)."`
	Summary string `json:"summary"`
	// Graphic: a graphic in cuts (else a call recording's layout).
	Graphic bool `json:"graphic"`
	// Piece is what draws a graphic role (nil for layouts).
	Piece *editspec.PackPiece `json:"piece,omitempty"`
	// What says it in plain words.
	What string `json:"what"`
	// Starter: the pack leaves the role out, so MetaRush's own fills it.
	Starter bool `json:"starter,omitempty"`
	// Waiting: its motion graphic waits for a person's approval.
	Waiting bool `json:"waiting,omitempty"`
	// Templates are the vetted templates that can play it.
	Templates []string `json:"templates,omitempty"`
}

// PackWaiting is a motion graphic of a pack that waits for a person's approval.
type PackWaiting struct {
	Role  string `json:"role"`
	Words string `json:"words"`
	Name  string `json:"name"`
	Hash  string `json:"hash"`
	// Library: a shared library the role's graphic imports (D171).
	Library bool `json:"library,omitempty"`
	// State is where it stands: waiting, rejected, revoked, or unknown (never checked here).
	State  string `json:"state" jsonschema:"enum=waiting,enum=rejected,enum=revoked,enum=unknown"`
	Review string `json:"review" jsonschema:"description=Where a person reviews and approves it."`
}

// PackDraftOut is a proposed pack waiting for a person.
type PackDraftOut struct {
	Pack  editspec.LayoutPack `json:"pack"`
	Lines []string            `json:"lines"`
	// BaseVersion is the version of the pack it was made from.
	BaseVersion int       `json:"baseVersion"`
	Note        string    `json:"note,omitempty"`
	Origin      string    `json:"origin,omitempty"`
	By          string    `json:"by,omitempty"`
	At          time.Time `json:"at"`
	// Waiting are its graphics that need a person's approval first.
	Waiting []PackWaiting `json:"waiting"`
	// Stale: the pack changed since the draft was made; it can't be accepted as it is.
	Stale bool `json:"stale,omitempty"`
	// Ready: a person can accept it now.
	Ready bool `json:"ready"`
	// Why says what keeps it from being accepted ("" ready).
	Why string `json:"why,omitempty"`
}

// PackLibraryUpdate is a newer approved version of a shared library the
// pack's motion graphics use (D171): a person moves the pack to it
// (content.pack.set library).
type PackLibraryUpdate struct {
	Name string `json:"name"`
	// From is the version the pack uses, To the newest approved one.
	From string `json:"from"`
	To   string `json:"to"`
	// Version is name@short-version, what content.pack.set's library takes.
	Version string   `json:"version"`
	Roles   []string `json:"roles" jsonschema:"description=The parts whose graphics use it\\, in plain words."`
}

// PackLibraryOffer is settings whose layout pack uses an older version of a
// shared library a person just approved.
type PackLibraryOffer struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project whose own settings hold the pack."`
	Project   string `json:"project"`
}

// PackCutOffer is a project's latest cut that doesn't follow the pack yet.
type PackCutOffer struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Branch      string `json:"branch"`
	Version     int    `json:"version" jsonschema:"description=The cut's latest version number."`
	Title       string `json:"title,omitempty" jsonschema:"description=The cut's name."`
	// Changes say what following the pack changes in it.
	Changes []string `json:"changes" jsonschema:"description=What following the layout pack changes in the cut\\, in plain words."`
}

// ContentPackOut is the layout pack of a project's settings.
type ContentPackOut struct {
	// ProjectID and Project are the project whose own settings hold it.
	ProjectID string `json:"projectId,omitempty"`
	Project   string `json:"project,omitempty"`
	// Pack is the pack in use: the settings' own, else MetaRush's starter pack in their house style.
	Pack editspec.LayoutPack `json:"pack"`
	// Own: the settings have a pack of their own (false: MetaRush's own).
	Own bool `json:"own"`
	// Version counts every change to the settings' pack (going back to
	// MetaRush's own included): what a proposal or an acceptance is made against.
	Version int           `json:"version"`
	Roles   []PackRoleOut `json:"roles"`
	// Lines say the pack in plain words.
	Lines []string `json:"lines"`
	// Draft is a proposed pack waiting for a person.
	Draft *PackDraftOut `json:"draft,omitempty"`
	// ReadOnly: asked about a project that uses another's settings.
	ReadOnly bool `json:"readOnly"`
	// UsedBy counts the other projects using these settings.
	UsedBy int `json:"usedBy"`
	// LatestCuts are, after a change, the latest cuts of the projects using
	// these settings that don't follow the pack (each an offer: content.pack.apply).
	LatestCuts []PackCutOffer `json:"latestCuts,omitempty"`
	// LibraryUpdates are newer approved versions of the shared libraries the
	// pack's graphics use: each an offer a person takes (content.pack.set library).
	LibraryUpdates []PackLibraryUpdate `json:"libraryUpdates,omitempty"`
	Summary        string              `json:"summary"`
}

// ContentPackSetIn changes the layout pack of a project's own settings.
// Fields left out stay as they are. Settings without a pack start from a
// copy of MetaRush's own in their house style.
type ContentPackSetIn struct {
	ProjectID string  `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's\\, what new projects use."`
	Name      *string `json:"name,omitempty" jsonschema:"maxLength=80,description=What people call the pack."`
	Note      *string `json:"note,omitempty" jsonschema:"maxLength=500"`
	// Role picks the graphic to change: title-card, lower-third, end-screen,
	// quote-card or chapter-card (a variant after a colon: lower-third:guest).
	Role string `json:"role,omitempty" jsonschema:"description=The graphic to change: title-card\\, lower-third\\, end-screen\\, quote-card or chapter-card (a variant after a colon: lower-third:guest)."`
	// Template or Motion draws it (a motion graphic only from a person: code
	// reaches a pack only through a person's acceptance).
	Template string `json:"template,omitempty" jsonschema:"description=A vetted template for the role."`
	Motion   string `json:"motion,omitempty" jsonschema:"description=An approved project motion graphic for the role (name@version); only a person may set one."`
	// Fields replace the role's choices ({} clears them).
	Fields  json.RawMessage `json:"fields,omitempty" jsonschema:"type=object,description=The pack's choices for the role's graphic: how it looks and words it shows when a cut gives none ({} clears them)."`
	Seconds *float64        `json:"seconds,omitempty" jsonschema:"minimum=0,maximum=60,description=How long the graphic stays when a cut doesn't say (0: the template's own)."`
	Logo    *bool           `json:"logo,omitempty" jsonschema:"description=Show the brand's logo (the one made for the graphic's background)."`
	// Remove takes roles out of the pack (MetaRush's own fills them).
	Remove []string `json:"remove,omitempty" jsonschema:"description=Roles taken out of the pack (MetaRush's own fills them)."`
	// The call layouts' looks; "" goes back to MetaRush's own.
	PipCorner *string `json:"pipCorner,omitempty" jsonschema:"enum=,enum=bottom-right,enum=bottom-left,enum=top-right,enum=top-left"`
	PipSize   *string `json:"pipSize,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large"`
	PipShape  *string `json:"pipShape,omitempty" jsonschema:"enum=,enum=rounded,enum=square,enum=circle"`
	PipFrame  *string `json:"pipFrame,omitempty" jsonschema:"enum=,enum=none,enum=accent,enum=primary,enum=text"`
	Gap       *string `json:"gap,omitempty" jsonschema:"enum=,enum=none,enum=thin,enum=wide,description=Side by side: the gap between the pictures."`
	Frame     *string `json:"frame,omitempty" jsonschema:"enum=,enum=none,enum=accent,enum=primary,enum=text,description=Side by side: a line around each picture."`
	Backdrop  *string `json:"backdrop,omitempty" jsonschema:"enum=,enum=black,enum=brand,description=Side by side: what shows between the pictures."`
	Rounded   *bool   `json:"rounded,omitempty" jsonschema:"description=Side by side: rounded corners."`
	// Reset goes back to MetaRush's own pack (the settings' pack is removed).
	Reset bool `json:"reset,omitempty"`
	// Library moves every graphic of the pack that uses a shared library to
	// this approved version (name@version; only a person).
	Library string `json:"library,omitempty" jsonschema:"description=A shared library's approved version (name@version): every graphic of the pack that uses the library moves to it; only a person."`
}

// ContentPackProposeIn proposes a whole pack for a project's settings: a
// draft until a person accepts it.
type ContentPackProposeIn struct {
	ProjectID string              `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's."`
	Pack      editspec.LayoutPack `json:"pack"`
	Note      string              `json:"note,omitempty" jsonschema:"maxLength=500,description=What the pack is for."`
	// Origin is set by MetaRush (assistant:<conversation> for Claude's).
	Origin string `json:"origin,omitempty" jsonschema:"maxLength=200"`
	// DryRun only checks it and says what would wait (nothing is saved).
	DryRun bool `json:"dryRun,omitempty"`
}

// ContentPackAcceptIn makes the proposed pack current (people only).
type ContentPackAcceptIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's."`
	// Pack is a pack proposed on a card (Claude's panel): accepted at once
	// when every graphic is approved, else saved as the settings' draft.
	Pack   *editspec.LayoutPack `json:"pack,omitempty"`
	Note   string               `json:"note,omitempty" jsonschema:"maxLength=500"`
	Origin string               `json:"origin,omitempty" jsonschema:"maxLength=200"`
	// BaseVersion is the version of the settings' pack the person saw (a
	// card: when Claude proposed it): accepting after another change is refused.
	BaseVersion *int `json:"baseVersion,omitempty" jsonschema:"minimum=0,description=The settings' pack version the person saw; accepting after another change is refused."`
	// DraftAt is when the proposed pack the person saw was made: accepting
	// after another was proposed is refused.
	DraftAt *time.Time `json:"draftAt,omitempty" jsonschema:"description=When the proposed pack the person saw was made; accepting after another was proposed is refused."`
}

// ContentPackDeclineIn drops the proposed pack.
type ContentPackDeclineIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings. Empty: the default's."`
	Note      string `json:"note,omitempty" jsonschema:"maxLength=500,description=Why (kept in the activity log)."`
}

// ContentPackApplyIn makes a project's latest cut follow the layout pack.
type ContentPackApplyIn struct {
	CutEditCommon
}

// ---- reading ----

// packLook looks motion graphic versions up in the workspace.
func packLook(ctx context.Context, a *app.App) editspec.PackMotion {
	cache := map[string]*editspec.MotionInfo{}
	return func(hash string) (editspec.MotionInfo, bool) {
		if c, ok := cache[hash]; ok {
			if c == nil {
				return editspec.MotionInfo{}, false
			}
			return *c, true
		}
		m, err := motion.Get(ctx, a.DB, a.WorkspaceID(), hash)
		if err != nil || m == nil {
			cache[hash] = nil
			return editspec.MotionInfo{}, false
		}
		info := motion.InfoOf(m)
		cache[hash] = &info
		return info, true
	}
}

// packHashes are the motion graphics a pack (and the starter) draw with.
func packHashes(ps editspec.Packs) []string { return ps.MotionHashes() }

// packMotionFacts is the motion half of editspec.Facts for filling roles
// before a change's own facts are loaded (prepare).
type packMotionFacts struct {
	infos map[string]editspec.MotionInfo
}

func (packMotionFacts) Asset(string) (editspec.AssetFacts, bool)       { return editspec.AssetFacts{}, false }
func (packMotionFacts) Transcript(string) []editspec.TranscriptSegment { return nil }
func (f packMotionFacts) Motion(hash string) (editspec.MotionInfo, bool) {
	m, ok := f.infos[hash]
	return m, ok
}

// fillPackRoles fills a change's graphics from the project's layout pack
// (D264): roles without a template, every role of a new cut, the call layouts
// of conversations the change adds. It reads the pack only when the change
// needs it.
//
// It returns a fingerprint of the pack it filled from ("" when it didn't read
// it): the change's confirmation is bound to it, so a change checked against
// one pack is never saved filled from another.
func fillPackRoles(ctx context.Context, a *app.App, projectID string, s, base *editspec.Spec) string {
	if !editspec.NeedsPack(s, base) {
		return ""
	}
	ps := content.PackFor(ctx, a, projectID)
	infos, _ := motion.Infos(ctx, a.DB, a.WorkspaceID(), packHashes(ps))
	editspec.FillRoles(s, base, ps, packMotionFacts{infos: infos})
	b, _ := json.Marshal(ps)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// withPackMotion adds the pack's motion graphics to a cut's facts.
func withPackMotion(ctx context.Context, a *app.App, f *dbFacts, ps editspec.Packs) {
	infos, _ := motion.Infos(ctx, a.DB, a.WorkspaceID(), packHashes(ps))
	if f.motion == nil {
		f.motion = map[string]editspec.MotionInfo{}
	}
	for h, info := range infos {
		if _, ok := f.motion[h]; !ok {
			f.motion[h] = info
		}
	}
}

// waitingOf lists a pack's motion graphics (and the shared libraries they
// import) that need a person's approval.
func waitingOf(p *editspec.LayoutPack, look editspec.PackMotion) []PackWaiting {
	out := []PackWaiting{}
	for _, w := range p.WaitingMotion(look) {
		st := "unknown"
		if info, ok := look(w.Ref.Hash); ok {
			st = info.State
		}
		out = append(out, PackWaiting{Role: w.Role, Words: editspec.RoleWords(w.Role), Name: w.Ref.Name, Hash: w.Ref.Hash, Library: w.Library, State: st, Review: links.Motion(w.Ref.Hash)})
	}
	return out
}

// waitingWords says what a pack waits for ("approve acme-title (Library → Motion)").
func waitingWords(w []PackWaiting) string {
	var bits []string
	for _, x := range w {
		what := fmt.Sprintf("the motion graphic %s (the %s)", x.Name, x.Words)
		if x.Library {
			what = fmt.Sprintf("the shared library %s (the %s's graphic uses it)", x.Name, x.Words)
		}
		switch x.State {
		case "unknown":
			bits = append(bits, "check "+what+" so MetaRush knows it")
		case editspec.MotionRejected:
			bits = append(bits, what+" was turned down: change it and have the new version approved")
		default:
			bits = append(bits, "a person approves "+what+" under Library → Motion")
		}
	}
	return joinAnd(bits)
}

func packRoles(ps editspec.Packs, look editspec.PackMotion) []PackRoleOut {
	cur := ps.Current()
	out := []PackRoleOut{}
	keys := map[string]bool{}
	for _, r := range editspec.PackRoles {
		if r.Graphic {
			keys[r.ID] = true
		}
	}
	if cur != nil {
		for k := range cur.Pieces {
			keys[k] = true
		}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	rank := map[string]int{}
	for i, r := range editspec.PackRoles {
		rank[r.ID] = i
	}
	sort.Slice(ordered, func(i, j int) bool {
		ri, _ := editspec.SplitRole(ordered[i])
		rj, _ := editspec.SplitRole(ordered[j])
		if rank[ri] != rank[rj] {
			return rank[ri] < rank[rj]
		}
		return ordered[i] < ordered[j]
	})
	for _, k := range ordered {
		role, _ := editspec.SplitRole(k)
		r, _ := editspec.PackRoleByID(role)
		pc, inPack, ok := ps.Piece(k)
		if !ok {
			continue
		}
		piece := pc
		ro := PackRoleOut{Role: k, Words: editspec.RoleWords(k), Summary: r.Summary, Graphic: true, Piece: &piece, What: editspec.PieceWords(pc), Starter: !inPack, Templates: r.Templates}
		if pc.Motion != nil && look != nil {
			info, known := look(pc.Motion.Hash)
			ro.Waiting = !known || info.State != editspec.MotionApproved
		}
		out = append(out, ro)
	}
	ls := ps.Layouts()
	for _, r := range editspec.PackRoles {
		if r.Graphic {
			continue
		}
		what := "as MetaRush draws it"
		if ls != nil {
			st := &editspec.LayoutStyle{}
			if r.ID == editspec.PackPiP {
				st.PiP = ls.PiP
			} else {
				st.SideBySide = ls.SideBySide
			}
			if w := editspec.LayoutStyleWords(st); len(w) > 0 {
				_, what, _ = strings.Cut(w[0], ": ")
			}
		}
		out = append(out, PackRoleOut{Role: r.ID, Words: r.Words, Summary: r.Summary, What: what})
	}
	return out
}

// contentPackOut is the layout pack a project uses ("" : the default's).
func contentPackOut(ctx context.Context, a *app.App, projectID string) (*ContentPackOut, error) {
	g, r, err := content.Resolve(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	look := packLook(ctx, a)
	ps := content.PackFor(ctx, a, projectID)
	out := &ContentPackOut{ProjectID: r.Owner, Project: g.Name(r.Owner), ReadOnly: projectID != "" && r.Owner != projectID, Own: ps.Own != nil}
	out.Pack = *ps.Current()
	out.Roles = packRoles(ps, look)
	out.Lines = editspec.PackWords(ps.Current())
	if out.Lines == nil {
		out.Lines = []string{}
	}
	out.LibraryUpdates = libraryUpdates(ctx, a, ps.Own)
	if r.Owner != "" {
		rec, err := content.Record(ctx, a, r.Owner)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.PackDraft != nil {
			out.Draft = draftOut(rec, look)
		}
		out.Version = packVersionOf(rec)
		out.UsedBy = len(g.UsedBy(r.Owner))
		if projectID != "" && projectID != r.Owner {
			out.UsedBy--
		}
	}
	switch {
	case r.Owner == "":
		out.Summary = "No project's settings are used here yet, so cuts use MetaRush's own layout pack."
	case !out.Own:
		out.Summary = fmt.Sprintf("%s's settings use MetaRush's own layout pack, in their house style.", q(out.Project))
	default:
		out.Summary = fmt.Sprintf("Every project using %s's settings draws its title cards, lower thirds, end screens and call layouts with this pack.", q(out.Project))
	}
	if out.Draft != nil {
		out.Summary += " A proposed pack waits for a person."
	}
	return out, nil
}

func draftOut(rec *models.ContentSettings, look editspec.PackMotion) *PackDraftOut {
	d := rec.PackDraft
	cur := packVersionOf(rec)
	o := &PackDraftOut{Pack: d.Pack, Lines: editspec.PackWords(&d.Pack), BaseVersion: d.BaseVersion, Note: d.Note, Origin: d.Origin, At: d.CreatedAt, By: proposerName(d.CreatedBy, d.Origin)}
	if o.Lines == nil {
		o.Lines = []string{}
	}
	o.Waiting = waitingOf(&d.Pack, look)
	o.Stale = d.BaseVersion != cur
	switch {
	case o.Stale:
		o.Why = "The pack changed since this one was proposed; propose it again from the pack as it is now."
	case len(o.Waiting) > 0:
		o.Why = "First " + waitingWords(o.Waiting) + "."
	default:
		if p := d.Pack.Problem(look, true); p != "" {
			o.Why = p
		}
	}
	o.Ready = o.Why == ""
	return o
}

// proposerName says who proposed a draft: Claude for the panel's and
// Claude Code's, else the person.
func proposerName(a models.Actor, origin string) string {
	if strings.HasPrefix(origin, "assistant:") || a.Via == models.ViaAssistant || a.Via == "mcp" {
		return "Claude"
	}
	return a.UserName
}

// libraryUpdates are newer approved versions of the libraries a pack uses.
func libraryUpdates(ctx context.Context, a *app.App, p *editspec.LayoutPack) []PackLibraryUpdate {
	if p == nil {
		return nil
	}
	var out []PackLibraryUpdate
	pins := p.LibraryPins()
	names := make([]string, 0, len(pins))
	for n := range pins {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		lib, err := motion.LatestLibrary(ctx, a.DB, a.WorkspaceID(), n)
		if err != nil || lib == nil || lib.State != models.MotionApproved || lib.Hash == pins[n] {
			continue
		}
		u := PackLibraryUpdate{Name: n, From: pins[n], To: lib.Hash, Version: n + "@" + motion.Short(lib.Hash), Roles: []string{}}
		for _, key := range sortedKeysOf(p.Pieces) {
			if m := p.Pieces[key].Motion; m != nil && m.Libs[n] != "" {
				u.Roles = append(u.Roles, editspec.RoleWords(key))
			}
		}
		out = append(out, u)
	}
	return out
}

func sortedKeysOf(m map[string]editspec.PackPiece) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// PacksOnOlderLibrary lists the settings whose layout pack uses another
// version of a shared library than the one a person just approved (D171):
// approving moves cuts, and offers packs (a person moves each).
func PacksOnOlderLibrary(ctx context.Context, a *app.App, name, hash string) []PackLibraryOffer {
	var recs []models.ContentSettings
	if err := a.DB.Find(ctx, store.CContentSettings, a.WorkspaceID(), bson.M{"pack": bson.M{"$exists": true}}, &recs); err != nil {
		return nil
	}
	g, err := content.Load(ctx, a)
	if err != nil {
		return nil
	}
	var out []PackLibraryOffer
	for _, r := range recs {
		if h, ok := r.Pack.LibraryPins()[name]; ok && h != hash {
			out = append(out, PackLibraryOffer{ProjectID: r.ProjectID, Project: g.Name(r.ProjectID)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Project < out[j].Project })
	return out
}

// packVersionOf is the settings' pack version: it counts every saved change,
// going back to MetaRush's own included.
func packVersionOf(rec *models.ContentSettings) int {
	if rec == nil {
		return 0
	}
	v := rec.PackVersion
	if rec.Pack != nil && rec.Pack.Version > v {
		v = rec.Pack.Version
	}
	return v
}

// ---- changing ----

// packOffers are the latest cuts of the projects using owner's settings that
// don't follow its layout pack.
func packOffers(ctx context.Context, a *app.App, owner string) []PackCutOffer {
	users, _, err := contentUsers(ctx, a, owner)
	if err != nil {
		return nil
	}
	var out []PackCutOffer
	for _, id := range users {
		t, err := loadEditTarget(ctx, a, CutEditCommon{ProjectID: id})
		if err != nil || t.project.Archived {
			continue
		}
		ps := content.PackFor(ctx, a, id)
		withPackMotion(ctx, a, t.facts, ps)
		ops, err := editspec.PackOps(t.spec, ps, t.facts)
		if err != nil || len(ops) == 0 {
			continue
		}
		next, _, err := editspec.ApplyOps(t.spec, ops, false)
		if err != nil {
			continue
		}
		editspec.Normalize(next)
		names := cutsheet.NameFunc(t.spec, t.info, projectAssetIDs(ctx, a, t.project.ID))
		changes := cutsheet.Describe(t.spec, next, names)
		if changes == nil {
			changes = []string{}
		}
		out = append(out, PackCutOffer{ProjectID: id, ProjectName: t.project.Name, Branch: t.branch, Version: t.rev.Number, Title: t.project.Branches[t.branch].Title, Changes: changes})
	}
	return out
}

// savePack makes a pack the settings' current one (version bumped) and says
// whose latest cuts don't follow it.
func savePack(ctx context.Context, a *app.App, t *content.Target, g *content.Graph, rec *models.ContentSettings, next *editspec.LayoutPack, dropDraft bool, verb string) (*ContentPackOut, error) {
	unset := bson.M{}
	v := packVersionOf(rec) + 1
	var err error
	if next == nil {
		// Back to MetaRush's own: a draft made against the pack goes too.
		unset["pack"], unset["packDraft"] = "", ""
		err = content.Save(ctx, a, t.ProjectID, bson.M{"packVersion": v}, unset)
	} else {
		if dropDraft {
			unset["packDraft"] = ""
		}
		next.Version = v
		next.Layouts = next.Layouts.Clean()
		err = content.Save(ctx, a, t.ProjectID, bson.M{"pack": next, "packVersion": v}, unset)
	}
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, verb, "project", t.ProjectID, t.ProjectID, map[string]any{"pack": next})
	out, err := contentPackOut(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	if next == nil {
		out.Summary = fmt.Sprintf("%s's settings use MetaRush's own layout pack again, in their house style.", q(t.ProjectName))
	} else {
		out.Summary = fmt.Sprintf("Saved the layout pack of %s's settings (version %d): new cuts of every project using them draw with it.", q(t.ProjectName), next.Version)
	}
	out.LatestCuts = packOffers(ctx, a, t.ProjectID)
	if note := t.Note(g); note != "" {
		out.Summary += " " + note
	}
	if n := len(out.LatestCuts); n > 0 {
		out.Summary += fmt.Sprintf(" Saved versions don't change: %s can use the pack on %s latest cut.", pluralWord(n, "project"), map[bool]string{true: "its", false: "their"}[n == 1])
	}
	return out, nil
}

// packTarget is the settings an edit changes and their record.
func packTarget(ctx context.Context, a *app.App, projectRef string) (*content.Target, *content.Graph, *models.ContentSettings, error) {
	pid, err := projectRefID(ctx, a, projectRef)
	if err != nil {
		return nil, nil, nil, err
	}
	t, g, err := content.EditTarget(ctx, a, pid)
	if err != nil {
		return nil, nil, nil, err
	}
	rec, err := content.Record(ctx, a, t.ProjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	if rec == nil {
		rec = &models.ContentSettings{ProjectID: t.ProjectID}
	}
	return t, g, rec, nil
}

func clonePack(p *editspec.LayoutPack) *editspec.LayoutPack {
	if p == nil {
		return nil
	}
	b, _ := json.Marshal(p)
	var out editspec.LayoutPack
	_ = json.Unmarshal(b, &out)
	return &out
}

func pickLayout(dst *string, v *string) {
	if v != nil {
		*dst = strings.ToLower(strings.TrimSpace(*v))
	}
}

func packSet(ctx context.Context, a *app.App, in *ContentPackSetIn) (*ContentPackOut, error) {
	t, g, rec, err := packTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.Reset {
		if rec.Pack == nil {
			out, err := contentPackOut(ctx, a, t.ProjectID)
			if err == nil {
				out.Summary = fmt.Sprintf("%s's settings use MetaRush's own layout pack already.", q(t.ProjectName))
			}
			return out, err
		}
		return savePack(ctx, a, t, g, rec, nil, true, "content.pack.set")
	}
	style, _, _ := content.StyleFor(ctx, a, t.ProjectID)
	next := clonePack(rec.Pack)
	if next == nil {
		next = editspec.StarterPack(style) // a pack of one's own starts as MetaRush's own
		next.Name = ""
	}
	if next.Pieces == nil {
		next.Pieces = map[string]editspec.PackPiece{}
	}
	if in.Name != nil {
		next.Name = strings.TrimSpace(*in.Name)
	}
	if in.Note != nil {
		next.Note = strings.TrimSpace(*in.Note)
	}
	for _, r := range in.Remove {
		key := roleKey(r)
		if _, ok := next.Pieces[key]; !ok {
			return nil, apperr.New(apperr.NotFound, "The pack has no %s.", editspec.RoleWords(key)).WithHint("Roles in the pack: " + strings.Join(pieceWordsList(next), ", ") + ".")
		}
		delete(next.Pieces, key)
	}
	if in.Role != "" {
		key := roleKey(in.Role)
		if !editspec.GraphicRole(key) {
			return nil, apperr.New(apperr.InvalidInput, "A layout pack has a title card, lower third, end screen, quote card and chapter card, not %q.", in.Role).
				WithHint("A variant goes after a colon: lower-third:guest.")
		}
		pc, has := next.Pieces[key]
		if !has {
			// A role the pack doesn't have yet starts as what fills it now.
			ps := editspec.Packs{Own: next, Starter: editspec.StarterPack(style)}
			pc, _, _ = ps.Piece(key)
		}
		switch {
		case in.Template != "" && in.Motion != "":
			return nil, invalid("Give the %s a template or a motion graphic, not both.", editspec.RoleWords(key))
		case in.Template != "":
			if pc.Template != in.Template {
				pc.Fields = nil // the old graphic's choices may not fit the new one
			}
			pc.Template, pc.Motion = in.Template, nil
		case in.Motion != "":
			if !CallerFrom(ctx).Human {
				return nil, apperr.New(apperr.HumanOnly, "Only a person can put a motion graphic in a layout pack.").
					WithHint("Propose the pack instead; a person accepts it once its graphics are approved.").
					WithCommand("metarush content pack propose --project <project> --file pack.json")
			}
			m, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), in.Motion)
			if err != nil {
				return nil, err
			}
			ref := editspec.MotionRef{Name: m.Name, Hash: m.Hash}
			// The library versions the pack's other graphics use (one version of each per pack).
			others := &editspec.LayoutPack{Pieces: map[string]editspec.PackPiece{}}
			for k, x := range next.Pieces {
				if k != key {
					others.Pieces[k] = x
				}
			}
			pinLibs(ctx, a, &ref, m.Libraries, others.LibraryPins())
			if pc.Motion == nil || pc.Motion.Name != m.Name {
				pc.Fields = nil
			}
			pc.Template, pc.Motion = "", &ref
		}
		if len(in.Fields) > 0 {
			if string(in.Fields) == "{}" || string(in.Fields) == "null" {
				pc.Fields = nil
			} else {
				pc.Fields = in.Fields
			}
		}
		if in.Seconds != nil {
			pc.Seconds = math.Round(*in.Seconds*1000) / 1000
		}
		if in.Logo != nil {
			pc.Logo = *in.Logo
		}
		next.Pieces[key] = pc
	} else if in.Template != "" || in.Motion != "" || len(in.Fields) > 0 || in.Seconds != nil || in.Logo != nil {
		return nil, invalid("Say which part of the pack to change: title-card, lower-third, end-screen, quote-card or chapter-card.")
	}
	if in.PipCorner != nil || in.PipSize != nil || in.PipShape != nil || in.PipFrame != nil || in.Gap != nil || in.Frame != nil || in.Backdrop != nil || in.Rounded != nil {
		ls := &editspec.LayoutStyle{}
		if next.Layouts != nil {
			*ls = *next.Layouts
		}
		pip := editspec.PiPStyle{}
		if ls.PiP != nil {
			pip = *ls.PiP
		}
		sb := editspec.SideBySideStyle{}
		if ls.SideBySide != nil {
			sb = *ls.SideBySide
		}
		pickLayout(&pip.Corner, in.PipCorner)
		pickLayout(&pip.Size, in.PipSize)
		pickLayout(&pip.Shape, in.PipShape)
		pickLayout(&pip.Frame, in.PipFrame)
		pickLayout(&sb.Gap, in.Gap)
		pickLayout(&sb.Frame, in.Frame)
		pickLayout(&sb.Backdrop, in.Backdrop)
		if in.Rounded != nil {
			sb.Rounded = *in.Rounded
		}
		ls.PiP, ls.SideBySide = &pip, &sb
		next.Layouts = ls.Clean()
	}
	if in.Library != "" {
		if !CallerFrom(ctx).Human {
			return nil, apperr.New(apperr.HumanOnly, "Only a person can move a layout pack to another version of a shared library.").
				WithHint("Move it under Settings → Layout pack, or in your own terminal.")
		}
		lib, err := motion.Resolve(ctx, a.DB, a.WorkspaceID(), in.Library)
		if err != nil {
			return nil, err
		}
		if lib.Kind != models.MotionKindLibrary {
			return nil, invalid("%s isn't a shared library.", lib.Name)
		}
		moved := 0
		for key, pc := range next.Pieces {
			if pc.Motion == nil || pc.Motion.Libs[lib.Name] == "" {
				continue
			}
			ref := *pc.Motion
			libs := map[string]string{}
			for k, v := range ref.Libs {
				libs[k] = v
			}
			libs[lib.Name] = lib.Hash
			ref.Libs = libs
			pc.Motion = &ref
			next.Pieces[key] = pc
			moved++
		}
		if moved == 0 {
			return nil, apperr.New(apperr.NotFound, "No graphic of the pack uses the shared library %s.", lib.Name)
		}
	}
	look := packLook(ctx, a)
	if p := next.Problem(look, true); p != "" {
		return nil, apperr.New(apperr.InvalidInput, "%s", p)
	}
	return savePack(ctx, a, t, g, rec, next, false, "content.pack.set")
}

// roleKey reads a role the way people or the CLI write it.
func roleKey(s string) string {
	if k, ok := editspec.ParseRoleWords(s); ok {
		return k
	}
	return strings.ToLower(strings.TrimSpace(s))
}

func pieceWordsList(p *editspec.LayoutPack) []string {
	var out []string
	for k := range p.Pieces {
		out = append(out, editspec.RoleWords(k))
	}
	sort.Strings(out)
	return out
}

// pinPackLibs pins the shared libraries a proposed pack's motion graphics
// import (D171) that it doesn't pin yet: the newest approved version of each,
// else the newest checked (which then waits for a person like the graphic).
func pinPackLibs(ctx context.Context, a *app.App, p *editspec.LayoutPack) {
	pinned := p.LibraryPins() // one version of each library across the pack
	keys := make([]string, 0, len(p.Pieces))
	for k := range p.Pieces {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		pc := p.Pieces[key]
		if pc.Motion == nil {
			continue
		}
		m, err := motion.Get(ctx, a.DB, a.WorkspaceID(), pc.Motion.Hash)
		if err != nil || m == nil {
			continue
		}
		ref := *pc.Motion
		pinLibs(ctx, a, &ref, m.Libraries, pinned)
		pc.Motion = &ref
		p.Pieces[key] = pc
	}
}

// pinLibs pins each library a graphic imports that its ref doesn't: the
// version the pack already uses (pinned), else the newest approved one, else
// the newest checked (which then waits for a person); pinned learns it.
func pinLibs(ctx context.Context, a *app.App, ref *editspec.MotionRef, libraries []string, pinned map[string]string) {
	for _, l := range libraries {
		if ref.Libs[l] != "" {
			if _, ok := pinned[l]; !ok {
				pinned[l] = ref.Libs[l]
			}
			continue
		}
		h := pinned[l]
		if h == "" {
			if lib, err := motion.LatestLibrary(ctx, a.DB, a.WorkspaceID(), l); err == nil && lib != nil {
				h = lib.Hash
			}
		}
		if h == "" {
			continue
		}
		libs := map[string]string{}
		for k, v := range ref.Libs {
			libs[k] = v
		}
		libs[l] = h
		ref.Libs, pinned[l] = libs, h
	}
}

// checkProposed is what a proposed pack says about itself: its problem ("",
// none — graphics may still wait for approval) and what waits.
func checkProposed(ctx context.Context, a *app.App, p *editspec.LayoutPack) (string, []PackWaiting) {
	look := packLook(ctx, a)
	if why := p.Problem(look, false); why != "" {
		return why, nil
	}
	return "", waitingOf(p, look)
}

func cleanProposed(p editspec.LayoutPack) *editspec.LayoutPack {
	x := clonePack(&p)
	x.Name, x.Note = strings.TrimSpace(x.Name), strings.TrimSpace(x.Note)
	x.Version = 0
	x.Layouts = x.Layouts.Clean()
	if x.Pieces == nil {
		x.Pieces = map[string]editspec.PackPiece{}
	}
	return x
}

func packPropose(ctx context.Context, a *app.App, in *ContentPackProposeIn) (*ContentPackOut, error) {
	t, _, rec, err := packTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	p := cleanProposed(in.Pack)
	pinPackLibs(ctx, a, p)
	if why, _ := checkProposed(ctx, a, p); why != "" {
		return nil, apperr.New(apperr.InvalidInput, "%s", why)
	}
	base := packVersionOf(rec)
	d := &models.PackDraft{Pack: *p, BaseVersion: base, Note: strings.TrimSpace(in.Note), Origin: in.Origin, CreatedAt: time.Now().UTC(), CreatedBy: a.Actor}
	if in.DryRun {
		out, err := contentPackOut(ctx, a, t.ProjectID)
		if err != nil {
			return nil, err
		}
		r := *rec
		r.PackDraft = d
		out.Draft = draftOut(&r, packLook(ctx, a))
		out.Summary = proposedSummary(t.ProjectName, out.Draft, false)
		return out, nil
	}
	if err := content.Save(ctx, a, t.ProjectID, bson.M{"packDraft": d}, nil); err != nil {
		return nil, err
	}
	a.Audit(ctx, "content.pack.propose", "project", t.ProjectID, t.ProjectID, map[string]any{"pack": p, "origin": in.Origin})
	out, err := contentPackOut(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	out.Summary = proposedSummary(t.ProjectName, out.Draft, true)
	return out, nil
}

func proposedSummary(project string, d *PackDraftOut, saved bool) string {
	s := fmt.Sprintf("A layout pack proposed for %s's settings", q(project))
	if saved {
		s = "Saved " + lowerFirst(s) + ": it becomes the pack when a person accepts it."
	} else {
		s += ": it becomes the pack when a person accepts it."
	}
	if d != nil && len(d.Waiting) > 0 {
		s += " First " + waitingWords(d.Waiting) + "."
	}
	return s
}

func packAccept(ctx context.Context, a *app.App, in *ContentPackAcceptIn) (*ContentPackOut, error) {
	t, g, rec, err := packTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	look := packLook(ctx, a)
	cur := packVersionOf(rec)
	if in.BaseVersion != nil && *in.BaseVersion != cur {
		return nil, apperr.New(apperr.Conflict, "The layout pack of %s's settings changed since this one was proposed, so it can't be accepted as it is.", q(t.ProjectName)).
			WithHint("Look at the pack as it is now, and propose this one again if it should still replace it.").WithCommand("metarush content pack show --project " + t.ProjectID)
	}
	var p *editspec.LayoutPack
	if in.Pack != nil {
		// A pack proposed on a card: accepted now when it can be, else kept
		// as the settings' draft for the person to accept once approved.
		// Exactly the pack the card showed: its library versions were pinned
		// when the card was made, and nothing is pinned again at the click.
		p = cleanProposed(*in.Pack)
		if why, waiting := checkProposed(ctx, a, p); why != "" {
			return nil, apperr.New(apperr.InvalidInput, "%s", why)
		} else if len(waiting) > 0 {
			d := &models.PackDraft{Pack: *p, BaseVersion: cur, Note: strings.TrimSpace(in.Note), Origin: in.Origin, CreatedAt: time.Now().UTC(), CreatedBy: a.Actor}
			if err := content.Save(ctx, a, t.ProjectID, bson.M{"packDraft": d}, nil); err != nil {
				return nil, err
			}
			a.Audit(ctx, "content.pack.propose", "project", t.ProjectID, t.ProjectID, map[string]any{"pack": p, "origin": in.Origin})
			out, err := contentPackOut(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			out.Summary = fmt.Sprintf("Kept the proposed pack for %s's settings; it isn't the pack yet. First %s, then accept it under Settings → Layout pack.", q(t.ProjectName), waitingWords(waiting))
			return out, nil
		}
	} else {
		if rec.PackDraft == nil {
			return nil, apperr.New(apperr.NotFound, "No layout pack is proposed for %s's settings.", q(t.ProjectName)).
				WithCommand("metarush content pack propose --project <project> --file pack.json")
		}
		if in.DraftAt != nil && !rec.PackDraft.CreatedAt.Equal(*in.DraftAt) {
			return nil, apperr.New(apperr.Conflict, "Another layout pack was proposed for %s's settings since; look at it before accepting it.", q(t.ProjectName)).
				WithCommand("metarush content pack show --project " + t.ProjectID)
		}
		d := draftOut(rec, look)
		if d.Stale {
			return nil, apperr.New(apperr.Conflict, "%s", d.Why).WithCommand("metarush content pack show --project " + t.ProjectID)
		}
		if !d.Ready {
			return nil, apperr.New(apperr.Conflict, "The proposed pack can't be the pack yet. %s", d.Why).WithCommand("metarush motion list")
		}
		p = clonePack(&rec.PackDraft.Pack)
	}
	if why := p.Problem(look, true); why != "" {
		return nil, apperr.New(apperr.Conflict, "%s", why)
	}
	return savePack(ctx, a, t, g, rec, p, true, "content.pack.accept")
}

func packDecline(ctx context.Context, a *app.App, in *ContentPackDeclineIn) (*ContentPackOut, error) {
	t, _, rec, err := packTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if rec.PackDraft == nil {
		return nil, apperr.New(apperr.NotFound, "No layout pack is proposed for %s's settings.", q(t.ProjectName))
	}
	if err := content.Save(ctx, a, t.ProjectID, nil, bson.M{"packDraft": ""}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "content.pack.decline", "project", t.ProjectID, t.ProjectID, map[string]any{"note": in.Note, "origin": rec.PackDraft.Origin})
	out, err := contentPackOut(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	out.Summary = fmt.Sprintf("Set the proposed layout pack for %s's settings aside; the pack stays as it was.", q(t.ProjectName))
	return out, nil
}

// packApply makes a project's latest cut follow the layout pack of the
// settings it uses, as a checked change.
func packApply(ctx context.Context, a *app.App, in *ContentPackApplyIn) (*CutEditOut, error) {
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	c := in.CutEditCommon
	c.ProjectID = p.ID
	t, err := loadEditTarget(ctx, a, c)
	if err != nil {
		return nil, err
	}
	ps := content.PackFor(ctx, a, p.ID)
	withPackMotion(ctx, a, t.facts, ps)
	ops, err := editspec.PackOps(t.spec, ps, t.facts)
	if err != nil {
		return nil, err
	}
	whose := "MetaRush's own layout pack"
	if ps.Own != nil {
		whose = "the layout pack of " + q(ps.Of) + "'s settings"
	}
	if len(ops) == 0 {
		out := &CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: "The cut follows " + whose + " already.", NoChange: true, Changes: []string{}, Problems: []cutsheet.Problem{}}
		out.Duration, out.DurationBefore = round3(t.length()), round3(t.length())
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
		out.Plan = raw
		return out, nil
	}
	return t.finish(ctx, a, c, "pack", ops, "Follows "+whose+".", firstNonEmpty(c.Instruction, "Use the layout pack"), &CutEditOut{})
}

// ContentPackPreviewIn asks for previews of a layout pack's roles.
type ContentPackPreviewIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the pack of the settings it uses\\, in their brand. Empty: the default's."`
	// Role previews one role (default: all of them).
	Role string `json:"role,omitempty" jsonschema:"description=One role (title-card\\, lower-third:guest\\, picture-in-picture…); default: all."`
	// Shape is the frame: landscape, vertical or square (default: the
	// project's latest cut's, else landscape).
	Shape string `json:"shape,omitempty" jsonschema:"enum=,enum=landscape,enum=vertical,enum=square"`
	// Draft previews the proposed pack instead of the current one.
	Draft bool `json:"draft,omitempty"`
}

// PackPreview is one role drawn in the house style.
type PackPreview struct {
	Role    string `json:"role"`
	Words   string `json:"words"`
	What    string `json:"what"`
	Graphic bool   `json:"graphic"`
	// Plan plays the graphic on its own (graphic roles): the dashboard's
	// player draws it with the vetted templates, a motion graphic as the
	// stills its check drew.
	Plan *editspec.RenderPlan `json:"plan,omitempty"`
	// Tiles and Backdrop are a call layout's pictures as Go places them.
	Tiles    []editspec.PlanTile `json:"tiles,omitempty"`
	Backdrop string              `json:"backdrop,omitempty"`
	Width    int                 `json:"width"`
	Height   int                 `json:"height"`
	// Starter: MetaRush's own fills it (the pack leaves the role out).
	Starter bool `json:"starter,omitempty"`
	// Waiting: its motion graphic waits for a person's approval.
	Waiting bool `json:"waiting,omitempty"`
}

// ContentPackPreviewOut are the previews.
type ContentPackPreviewOut struct {
	ProjectID string        `json:"projectId,omitempty"`
	Project   string        `json:"project,omitempty"`
	Shape     string        `json:"shape"`
	Draft     bool          `json:"draft,omitempty"`
	Previews  []PackPreview `json:"previews"`
}

func packPreview(ctx context.Context, a *app.App, in *ContentPackPreviewIn) (*ContentPackPreviewOut, error) {
	pid, err := projectRefID(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	g, r, err := content.Resolve(ctx, a, pid)
	if err != nil {
		return nil, err
	}
	ps := content.PackFor(ctx, a, pid)
	out := &ContentPackPreviewOut{ProjectID: r.Owner, Project: g.Name(r.Owner), Previews: []PackPreview{}, Draft: in.Draft}
	if in.Draft {
		rec, err := content.Record(ctx, a, r.Owner)
		if err != nil {
			return nil, err
		}
		if rec == nil || rec.PackDraft == nil {
			return nil, apperr.New(apperr.NotFound, "No layout pack is proposed for these settings.")
		}
		ps.Own, ps.Of = clonePack(&rec.PackDraft.Pack), g.Name(r.Owner)
	}
	preset := map[string]string{"landscape": editspec.PresetLandscape, "vertical": editspec.PresetVertical, "square": editspec.PresetSquare}[in.Shape]
	if in.Shape != "" && preset == "" {
		return nil, invalid("A preview is landscape, vertical or square, not %q.", in.Shape)
	}
	if preset == "" {
		preset = editspec.PresetLandscape
		if pid != "" {
			if p, err := resolveProject(ctx, a, pid); err == nil {
				if head := p.Branches["main"].Head; head != "" {
					var rev models.Revision
					if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev) == nil && rev.Spec.Output.Preset != "" {
						preset = rev.Spec.Output.Preset
					}
				}
			}
		}
	}
	if _, ok := editspec.Presets[preset]; !ok {
		preset = editspec.PresetLandscape
	}
	out.Shape = editspec.PresetLabel(preset)
	spec := &editspec.Spec{Output: editspec.Output{Preset: preset}}
	applyKitToNewCut(ctx, a, spec, pid)
	editspec.Normalize(spec)
	look := packLook(ctx, a)
	roles := packRoles(ps, look)
	// One spec with every preview graphic, so the facts (resources, motion
	// graphics) load once.
	all := editspec.Clone(spec)
	infos, _ := motion.Infos(ctx, a.DB, a.WorkspaceID(), packHashes(ps))
	pf := packMotionFacts{infos: infos}
	overlays := map[string]editspec.Overlay{}
	want := ""
	if in.Role != "" {
		want = roleKey(in.Role)
		if k, ok := map[string]string{"pip": editspec.PackPiP, "picture in picture": editspec.PackPiP, "side by side": editspec.PackSideBySide}[strings.ToLower(strings.TrimSpace(in.Role))]; ok {
			want = k
		}
	}
	for _, ro := range roles {
		if ro.Piece == nil || (want != "" && ro.Role != want) {
			continue
		}
		o := editspec.RolePreviewOverlay(spec, ro.Role, *ro.Piece, pf)
		overlays[ro.Role] = o
		all.Overlays = append(all.Overlays, o)
	}
	facts, err := loadFacts(ctx, a, pid, all)
	if err != nil {
		return nil, err
	}
	for _, ro := range roles {
		if want != "" && ro.Role != want {
			continue
		}
		pv := PackPreview{Role: ro.Role, Words: ro.Words, What: ro.What, Graphic: ro.Graphic, Width: spec.Output.Width, Height: spec.Output.Height, Starter: ro.Starter, Waiting: ro.Waiting}
		if o, ok := overlays[ro.Role]; ok {
			pv.Plan = editspec.RolePreviewPlan(spec, o, facts)
			previewSources(pv.Plan, facts, spec)
		} else if !ro.Graphic {
			layout := editspec.LayoutPiP
			if ro.Role == editspec.PackSideBySide {
				layout = editspec.LayoutSideBySide
			}
			pv.Tiles, pv.Backdrop = editspec.LayoutPreview(layout, spec.Output.Width, spec.Output.Height, ps.Layouts(), spec.Brand)
		}
		out.Previews = append(out.Previews, pv)
	}
	if want != "" && len(out.Previews) == 0 {
		return nil, apperr.New(apperr.NotFound, "The pack has no %s.", editspec.RoleWords(want))
	}
	return out, nil
}

// PackContext is the layout pack a project's videos follow, for Claude's
// context at the start of every turn ("" when it is MetaRush's own and the
// cuts don't name roles).
func PackContext(ctx context.Context, a *app.App, projectID string) string {
	ps := content.PackFor(ctx, a, projectID)
	if ps.Own == nil {
		return "- Layout pack: MetaRush's own, in the house style. Name a graphic's part so it follows the pack (in a cut sheet: `- intro: title card at 0:00 for 3 s · with title \"…\"`; parts: title card, lower third, end screen, quote card, chapter card)."
	}
	return "- Layout pack from “" + ps.Of + "”'s settings (name a graphic's part and MetaRush draws it with the pack — `- intro: title card at 0:00 for 3 s · with title \"…\"`; `metarush content pack apply` puts the pack on the latest cut): " +
		strings.Join(editspec.PackWords(ps.Own), "; ")
}

func init() {
	Def("content.pack.show", "The layout pack of a project's Content Settings (without a project, the default's): which template or approved motion graphic draws each title card, lower third, end screen, quote card and chapter card, how call layouts look, and a proposed pack waiting for a person", Flags{},
		func(ctx context.Context, a *app.App, in *ContentPackIn) (*ContentPackOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return contentPackOut(ctx, a, pid)
		})
	Def("content.pack.set", "Change the layout pack of a project's own Content Settings (without a project, the default's): the template that draws a part and its choices, the pack's name, call layouts' looks, parts taken out, a shared library's newer version, or back to MetaRush's own; the latest cuts that don't follow it are offered", Flags{Mutating: true}, packSet)
	Def("content.pack.propose", "Propose a whole layout pack for a project's Content Settings (its graphics may be project motion graphics): a draft that becomes the pack only when a person accepts it", Flags{Mutating: true}, packPropose)
	Def("content.pack.accept", "Make the proposed layout pack the pack of a project's Content Settings (people only; every motion graphic in it must be approved)", Flags{Mutating: true, HumanOnly: true}, packAccept)
	Def("content.pack.decline", "Set the proposed layout pack of a project's Content Settings aside (the pack stays as it was)", Flags{Mutating: true}, packDecline)
	Def("content.pack.preview", "Previews of a project's layout pack in its brand and house style: each graphic on its own as the dashboard plays it (a motion graphic as its check's stills) and where call layouts place the pictures", Flags{}, packPreview)
	Def("content.pack.apply", "Make a project's latest cut follow the layout pack of the Content Settings it uses (a checked change: graphics that name a part drawn with the pack's, call layouts in its look)", Flags{Mutating: true}, packApply)
}
