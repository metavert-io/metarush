package conversation

import (
	"context"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Screen recordings made in the dashboard (D260) are conversations in the
// projects they are linked into: the screen, the camera and the voice are
// members lined up by the recording's clock (D259), so everything a
// conversation does (layouts, the picture switching while the voice plays,
// nudging, lining up again by sound) works on them. A cut starts from one
// layout shot over the whole recording.

// MethodClock is a recording's alignment measured on the browser's clock.
const MethodClock = "clock"

// ProviderRecorder marks a conversation made from a dashboard recording.
const ProviderRecorder = "recorder"

// RecordedMember is one track of a recording as a member.
type RecordedMember struct {
	ID      string
	Name    string
	AssetID string
	// Use: both, sound or picture.
	Use     string
	Start   float64
	Drift   float64
	Framing *editspec.Framing
}

// RecordedIn makes or updates a recording's conversation in a project.
type RecordedIn struct {
	ProjectID string
	// ConversationID is the recording's conversation there already (it is
	// lined up again rather than made twice).
	ConversationID string
	Name           string
	Members        []RecordedMember
	// Confidence is how sure the clock is (0–1).
	Confidence float64
}

// FromRecording makes (or lines up again) the conversation of a recording in
// a project. Every member's recording must be in the project.
func FromRecording(ctx context.Context, a *app.App, in RecordedIn) (*models.Conversation, error) {
	if !ids.Valid(ids.Project, in.ProjectID) {
		return nil, invalid("say which project the recording goes into")
	}
	if len(in.Members) == 0 {
		return nil, invalid("the recording has no tracks to put in a conversation")
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no project %s in this workspace.", in.ProjectID)
	}
	now := time.Now().UTC()
	sure := math.Max(0, math.Min(1, in.Confidence))
	members := make([]models.ConversationMember, 0, len(in.Members))
	lo := math.Inf(1)
	for _, m := range in.Members {
		lo = math.Min(lo, m.Start)
	}
	ref := in.Members[0].ID
	for _, m := range in.Members {
		if m.Use == editspec.UseSound {
			ref = m.ID // the voice is what the others were measured against
		}
	}
	for _, m := range in.Members {
		if !editspec.ConversationNamePattern.MatchString(m.ID) {
			return nil, invalid("%q can't be a member's short name", m.ID)
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), m.AssetID, &as); err != nil {
			return nil, apperr.New(apperr.NotFound, "The recording's %s isn't in the Library.", m.ID)
		}
		if !containsStr(as.ProjectIDs, p.ID) {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't in %s yet.", as.DisplayTitle(), p.Name).WithCommand("metarush recording link")
		}
		cm := models.ConversationMember{}
		cm.ID, cm.Name, cm.AssetID = m.ID, m.Name, m.AssetID
		cm.Use = m.Use
		if cm.Use == editspec.UseBoth {
			cm.Use = ""
		}
		cm.Start, cm.Drift, cm.Framing = round3(m.Start-lo), math.Round(m.Drift*10)/10, m.Framing
		if m.ID != "screen" {
			if pp := person(ctx, a, m.Name); pp != nil {
				cm.PersonID = pp.ID
			}
		}
		how := &models.MemberAlign{Method: MethodClock, Confidence: sure, Against: ref, At: now}
		if m.ID == ref {
			how = &models.MemberAlign{Method: "reference", Confidence: 1, At: now}
		}
		cm.Align = how
		members = append(members, cm)
	}
	var c *models.Conversation
	if in.ConversationID != "" {
		var old models.Conversation
		if err := a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), in.ConversationID, &old); err == nil {
			c = &old
		}
	}
	created := c == nil
	if created {
		c = &models.Conversation{ID: ids.New(ids.Conversation), WorkspaceID: a.WorkspaceID(), CreatedAt: now, CreatedBy: a.Actor,
			Source: &models.ConversationSource{Provider: ProviderRecorder, Aligned: true}}
	}
	if !containsStr(c.ProjectIDs, p.ID) {
		c.ProjectIDs = append(c.ProjectIDs, p.ID)
	}
	if n := strings.TrimSpace(in.Name); n != "" && (created || c.Name == "") {
		c.Name = n
	}
	if c.Name == "" {
		c.Name = "Screen recording"
	}
	c.Members = members
	as, err := assets(ctx, a, c)
	if err != nil {
		return nil, err
	}
	c.Length = length(c, as)
	c.Sync = &models.ConversationSync{Reference: ref, Method: MethodClock, Confidence: sure, At: now, By: a.Actor}
	c.UpdatedAt = now
	if created {
		if err := a.DB.Insert(ctx, store.CConversations, c); err != nil {
			return nil, err
		}
		a.Audit(ctx, "conversation.create", "conversation", c.ID, p.ID, map[string]any{"members": len(c.Members), "from": "recording"})
		return c, nil
	}
	if err := save(ctx, a, c); err != nil {
		return nil, err
	}
	a.Audit(ctx, "conversation.sync", "conversation", c.ID, p.ID, map[string]any{"method": MethodClock, "from": "recording"})
	return c, nil
}

// SoundCheckSure is how sure lining up by sound must be to replace the
// clock on its own; SoundCheckAgree is how close (seconds) a fairly sure
// result must come to the clock to replace it (each then vouches for the
// other: recordings under a minute never reach "very sure", D259).
const (
	SoundCheckSure  = 0.8
	SoundCheckFair  = 0.5
	SoundCheckAgree = 0.15
)

// SoundCheckMaxMove is how far (seconds) the sound may move a track from a
// clock that was measured, however sure it is: a browser's clock is off by
// tens of milliseconds, so a bigger move is a false match (a repeated sound).
const SoundCheckMaxMove = 0.5

// RefineBySound lines a recording's conversation up again by its sound, when
// its pictures carry the microphone (D259): the clock measured in the browser
// is good to a frame or two, the sound to the sample. The sound's alignment is
// kept when very sure, or fairly sure and within SoundCheckAgree of the
// clock; never when it moves a track more than maxMove from the clock (0: no
// limit, the clock wasn't measured). It says why in plain words either way.
// search: seconds either way (the files started within moments of each other).
func RefineBySound(ctx context.Context, a *app.App, c *models.Conversation, search, maxMove float64) (bool, string, error) {
	const byClock = "lined up by the clock it was recorded with"
	cp := *c
	cp.Members = append([]models.ConversationMember(nil), c.Members...)
	if err := syncRun(ctx, a, &cp, MethodAudio, nil, search, true); err != nil || cp.Sync == nil || cp.Sync.Confidence < SoundCheckFair {
		return false, byClock + ": the sound couldn't say surely", nil
	}
	ref, _ := c.Member(cp.Sync.Reference)
	refNow, _ := cp.Member(cp.Sync.Reference)
	if ref == nil || refNow == nil {
		return false, byClock, nil
	}
	worst, who := 0.0, ""
	for i := range cp.Members {
		was, _ := c.Member(cp.Members[i].ID)
		if was == nil {
			return false, byClock, nil
		}
		if d := math.Abs((cp.Members[i].Start - refNow.Start) - (was.Start - ref.Start)); d > worst {
			worst, who = d, cp.Members[i].Name
		}
	}
	if maxMove > 0 && worst > maxMove {
		return false, fmt.Sprintf("%s: the sound would have moved %s by %s, more than the clock can be off", byClock, who, secs(worst)), nil
	}
	why := "lined up by their sound"
	if cp.Sync.Confidence < SoundCheckSure {
		if worst > SoundCheckAgree {
			return false, byClock + ": the sound was only fairly sure and didn't agree with it", nil
		}
		// The sound and the clock agree: each vouches for the other.
		why = "lined up by their sound, which agrees with the clock"
		cp.Sync.Confidence = SoundCheckSure
		for i := range cp.Members {
			if al := cp.Members[i].Align; al != nil && al.Confidence < SoundCheckSure {
				x := *al
				x.Confidence = SoundCheckSure
				cp.Members[i].Align = &x
			}
		}
	}
	cp.Sync.Method = MethodAudio
	*c = cp
	if err := save(ctx, a, c); err != nil {
		return false, "", err
	}
	a.Audit(ctx, "conversation.sync", "conversation", c.ID, first(c.ProjectIDs), map[string]any{"method": MethodAudio, "from": "recording"})
	return true, why, nil
}

// Show is a conversation in plain words (what conversation.show answers).
func Show(ctx context.Context, a *app.App, c *models.Conversation) CallInfo { return view(ctx, a, c) }

// Load finds a conversation by ID or name.
func Load(ctx context.Context, a *app.App, ref, projectID string) (*models.Conversation, error) {
	return load(ctx, a, ref, projectID)
}

// LayoutCutIn starts a cut of a conversation that shows one thing throughout:
// a layout of some members, or one member full frame.
type LayoutCutIn struct {
	Conversation string
	Project      string
	// Branch is the cut (default: the first cut when the project has none,
	// else a new cut named from Title).
	Branch string
	// Layout is pip, side-by-side or stacked with Members in order; or empty
	// with one member in Members (shown full frame).
	Layout  string
	Members []string
	Shape   string
	Title   string
	// Instruction says what the change is (the version's instruction).
	Instruction string
	Notes       string
	Origin      string
	Apply       bool
}

// LayoutCutOut is the checked plan, with the cut it goes to.
type LayoutCutOut struct {
	CallPlanOut
	Branch string `json:"branch"`
	// NewCut: the plan starts a cut that didn't exist.
	NewCut bool `json:"newCut"`
}

// LayoutCut builds a cut whose one clip is the whole conversation with one
// shot (gate off: each recording at its level; no name tags), checks it and,
// with Apply, saves it.
func LayoutCut(ctx context.Context, a *app.App, in LayoutCutIn) (*LayoutCutOut, error) {
	c, err := load(ctx, a, in.Conversation, in.Project)
	if err != nil {
		return nil, err
	}
	pid := in.Project
	if pid == "" {
		pid = first(c.ProjectIDs)
	}
	if !containsStr(c.ProjectIDs, pid) {
		return nil, invalid("%s isn't in that project", c.Name)
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no project %s in this workspace.", pid)
	}
	shot := editspec.Shot{At: 0}
	switch in.Layout {
	case "":
		if len(in.Members) != 1 {
			return nil, invalid("name the one recording to show, or a layout")
		}
		shot.Member = in.Members[0]
	case editspec.LayoutPiP, editspec.LayoutSideBySide, editspec.LayoutStacked:
		lo, hi := editspec.LayoutCount(in.Layout)
		if len(in.Members) < lo || len(in.Members) > hi {
			return nil, invalid("%s shows %d to %d recordings, not %d", editspec.LayoutLabel(in.Layout), lo, hi, len(in.Members))
		}
		shot.Layout, shot.Members = in.Layout, in.Members
	default:
		return nil, invalid("the layout is pip, side-by-side or stacked, not %q", in.Layout)
	}
	for _, id := range in.Members {
		m, _ := c.Member(id)
		if m == nil {
			return nil, invalid("there's no %q in %s (it has %s)", id, c.Name, memberList(c))
		}
		if m.UseOf() == editspec.UseSound {
			return nil, invalid("%s is sound only, so it can't be shown", m.Name)
		}
	}
	preset, platform, err := shapeOf(in.Shape)
	if err != nil {
		return nil, err
	}
	if c.Length < 0.5 {
		return nil, invalid("%s is too short to cut (%s)", c.Name, secs(c.Length))
	}
	pin := c.Pin()
	pin.SampleAligned = true
	pin.NameTags = "off"
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: strings.TrimSpace(in.Title),
		Output:        editspec.Output{Preset: preset, FPS: projectFPS(ctx, a, pid), Platform: platform, Framing: editspec.FramingFaces},
		Conversations: map[string]editspec.ConversationPin{convName: pin},
		Clips:         []editspec.Clip{{ID: "recording", AssetID: editspec.ConvKey(convName), In: 0, Out: c.Length, Shots: []editspec.Shot{shot}}},
		Notes:         in.Notes}
	editspec.Normalize(spec)
	out := &LayoutCutOut{}
	branch := in.Branch
	if branch == "" {
		if p.Branches["main"].Head == "" {
			branch = "main"
		} else {
			branch = freeBranch(&p, in.Title)
		}
	}
	rev, err := latest(ctx, a, pid, branch)
	if err != nil {
		return nil, err
	}
	instruction := in.Instruction
	if instruction == "" {
		instruction = "Start a cut of " + c.Name
	}
	plan := editspec.Plan{ProjectID: pid, Branch: branch, Instruction: instruction, Spec: spec, Origin: in.Origin}
	if rev != nil {
		// Starting again on a cut that exists replaces what it shows.
		plan.ProjectID, plan.BaseRevisionID = "", rev.ID
	}
	out.Branch, out.NewCut = branch, rev == nil
	if out.Result, err = checkPlan(ctx, a, plan); err != nil {
		return nil, err
	}
	what := "the screen and camera in a corner"
	switch {
	case shot.Member != "":
		m, _ := c.Member(shot.Member)
		what = m.Name + " full frame"
	case shot.Layout == editspec.LayoutSideBySide:
		what = joinAnd(memberNames(c, shot.Members)) + " side by side"
	case shot.Layout == editspec.LayoutStacked:
		what = joinAnd(memberNames(c, shot.Members)) + " one above the other"
	case shot.Layout == editspec.LayoutPiP && len(shot.Members) == 2:
		m0, _ := c.Member(shot.Members[0])
		m1, _ := c.Member(shot.Members[1])
		what = fmt.Sprintf("%s with %s in a corner", m0.Name, m1.Name)
	}
	out.Changes = []string{fmt.Sprintf("a cut of %s, %s long: %s", c.Name, secs(c.Length), what)}
	out.Summary = fmt.Sprintf("Ready: a cut of %s showing %s.", c.Name, what)
	if in.Apply {
		out.Applied = applyResult(ctx, a, out.Result)
		out.Summary = settle(out.Summary, out.Applied)
		if out.Applied != nil && out.Applied.Problem == "" && strings.TrimSpace(in.Title) != "" && branch != "main" {
			_, _ = ops.CallTyped[ops.CutView](ctx, a, "cut.rename", ops.CutRenameIn{ProjectID: pid, Branch: branch, Title: strings.TrimSpace(in.Title)}, ops.Caller{})
		}
	}
	return out, nil
}

// freeBranch is an unused cut name from a title ("Product demo" → product-demo, product-demo-2…).
func freeBranch(p *models.Project, title string) string {
	base := slug(title)
	if base == "person" || base == "" {
		base = "recording"
	}
	if len(base) > 34 {
		base = strings.Trim(base[:34], "-")
	}
	name := base
	for n := 2; ; n++ {
		if _, used := p.Branches[name]; !used && name != "main" {
			return name
		}
		name = fmt.Sprintf("%s-%d", base, n)
	}
}

func memberNames(c *models.Conversation, idList []string) []string {
	var out []string
	for _, id := range idList {
		if m, _ := c.Member(id); m != nil {
			out = append(out, m.Name)
		}
	}
	return out
}
