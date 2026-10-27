package recorder

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func init() {
	ops.Def("recording.list", "List the screen recordings made in the dashboard (in a project, or all of them): their tracks (screen, camera, voice), length, and whether they are footage yet", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RecordingListIn) (*RecordingListOut, error) {
			return list(ctx, a, in)
		})
	ops.Def("recording.show", "Show a screen recording: each track (screen, camera, voice), where it starts on the recording's clock, the footage it became, the projects it is linked into and what is left to do", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RecordingRefIn) (*RecordingInfo, error) {
			r, err := get(ctx, a, in.Recording)
			if err != nil {
				return nil, err
			}
			if !visible(ctx, a, r) {
				return nil, apperr.New(apperr.NotFound, "There's no recording %s in this workspace.", r.ID)
			}
			v := view(ctx, a, r)
			return &v, nil
		})
	ops.Def("recording.finish", "Make a screen recording footage: its voice, screen and camera become clips in the Library, lined up on its clock and linked into its project (keep: what arrived, after a closed tab or a lost connection)", ops.Flags{Mutating: true}, Finish)
	ops.Def("recording.link", "Link a screen recording into a project: its footage joins the project as one conversation (screen, camera and voice lined up), so a cut can lay them out together", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RecordingLinkIn) (*RecordingInfo, error) {
			r, err := get(ctx, a, in.Recording)
			if err != nil {
				return nil, err
			}
			p, err := ops.ResolveProject(ctx, a, in.Project)
			if err != nil {
				return nil, err
			}
			if _, err := link(ctx, a, r, p.ID); err != nil {
				return nil, err
			}
			v := view(ctx, a, r)
			v.Text = "Linked into " + p.Name + ". " + v.Text
			return &v, nil
		})
	ops.Def("recording.cut", "Start a cut from a screen recording: the screen with the camera in a corner, side by side, the screen only or the camera only, the voice playing throughout (checks the plan; apply makes it a version)", ops.Flags{Mutating: true}, cut)
	ops.Def("recording.discard", "Throw away a screen recording that isn't footage yet: the pieces that arrived are deleted (people only; footage already in the Library goes to the Trash there)", ops.Flags{Mutating: true, HumanOnly: true}, discard)
}

// RecordingRefIn names a recording.
type RecordingRefIn struct {
	Recording string `json:"recording" jsonschema:"minLength=1,description=The recording (its ID)."`
}

// RecordingListIn lists recordings.
type RecordingListIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=Only the recordings made for or linked into this project."`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=0,maximum=500"`
	// Discarded includes the ones thrown away.
	Discarded bool `json:"discarded,omitempty"`
}

// RecordingListOut lists recordings, newest first.
type RecordingListOut struct {
	Recordings []RecordingInfo `json:"recordings"`
}

// RecordingLinkIn links a recording into a project.
type RecordingLinkIn struct {
	Recording string `json:"recording" jsonschema:"minLength=1"`
	Project   string `json:"project" jsonschema:"minLength=1,description=The project (ID or name)."`
}

// RecordingCutIn starts a cut from a recording.
type RecordingCutIn struct {
	Recording string `json:"recording" jsonschema:"minLength=1"`
	Project   string `json:"project,omitempty" jsonschema:"description=The project (ID or name; default: the one it was recorded for). It is linked there first when it isn't."`
	// Layout: pip (the screen with the camera in a corner), side-by-side,
	// screen or camera (one full frame). Default: what the person chose
	// before recording, else pip with a camera, else the screen.
	Layout string `json:"layout,omitempty" jsonschema:"enum=pip,enum=side-by-side,enum=screen,enum=camera"`
	Shape  string `json:"shape,omitempty" jsonschema:"enum=landscape,enum=vertical,enum=square,description=Default landscape (16:9)."`
	// Branch is the cut to start (default: the project's first cut when it
	// has none, else a new cut named after the recording).
	Branch string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$,maxLength=40"`
	Apply  bool   `json:"apply,omitempty"`
}

// RecordingCutOut is the checked plan for the new cut.
type RecordingCutOut struct {
	conversation.LayoutCutOut
	ProjectID string `json:"projectId"`
	Layout    string `json:"layout"`
}

// RecordingInfo is a recording in plain words.
type RecordingInfo struct {
	Recording models.Recording `json:"recording"`
	// Text says it in a line: its name, length, tracks and where it is.
	Text string `json:"text"`
	// Tracks say each track plainly ("Screen: a window, “Docs”, 4:12").
	Tracks []RecordingTrackInfo `json:"tracks"`
	// Interrupted: pieces stopped arriving while it was being recorded (a
	// closed tab, a lost connection).
	Interrupted bool `json:"interrupted"`
	// Here: its pieces are on this computer (only here can it be finished).
	Here bool `json:"here"`
	// Next says what to do next, when anything.
	Next string `json:"next,omitempty"`
	// Links are the projects it is in, with their conversation.
	Links []RecordingLinkInfo `json:"links"`
}

// SetJobID implements voiceover.Trackable (the tracked work of finishing).
func (v *RecordingInfo) SetJobID(string) {}

// JobResult implements voiceover.Trackable.
func (v *RecordingInfo) JobResult() map[string]any {
	return map[string]any{"recordingId": v.Recording.ID, "summary": v.Text}
}

// RecordingTrackInfo is one track in plain words.
type RecordingTrackInfo struct {
	ID      string `json:"id"`
	Text    string `json:"text"`
	AssetID string `json:"assetId,omitempty"`
	Title   string `json:"title,omitempty"`
}

// RecordingLinkInfo is a project a recording is in.
type RecordingLinkInfo struct {
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
	ConversationID string `json:"conversationId"`
	// Sync says how the tracks line up there, plainly.
	Sync string `json:"sync,omitempty"`
}

func view(ctx context.Context, a *app.App, r *models.Recording) RecordingInfo {
	v := RecordingInfo{Recording: *r, Tracks: []RecordingTrackInfo{}, Links: []RecordingLinkInfo{}, Here: r.InstallationID == a.Cfg.Installation.ID}
	v.Interrupted = r.State == models.RecordingRecording && time.Since(r.LastPieceAt) > QuietAfter
	var assetIDs []string
	for _, t := range r.Tracks {
		if t.AssetID != "" {
			assetIDs = append(assetIDs, t.AssetID)
		}
	}
	titles := map[string]string{}
	if len(assetIDs) > 0 {
		var as []models.Asset
		_ = a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": assetIDs}}, &as, options.Find().SetProjection(bson.M{"title": 1, "name": 1}))
		for i := range as {
			titles[as[i].ID] = as[i].DisplayTitle()
		}
	}
	var kinds []string
	for _, t := range r.Tracks {
		kinds = append(kinds, trackWords(t.ID))
		ti := RecordingTrackInfo{ID: t.ID, AssetID: t.AssetID, Title: titles[t.AssetID], Text: trackText(t)}
		v.Tracks = append(v.Tracks, ti)
	}
	for _, l := range r.Links {
		li := RecordingLinkInfo{ProjectID: l.ProjectID, ConversationID: l.ConversationID}
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), l.ProjectID, &p) == nil {
			li.ProjectName = p.Name
		}
		var c models.Conversation
		if a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), l.ConversationID, &c) == nil && c.Sync != nil {
			li.Sync = syncWords(&c)
		}
		v.Links = append(v.Links, li)
	}
	length := r.Length
	where := stateWords(r.State)
	switch {
	case v.Interrupted:
		where = fmt.Sprintf("stopped arriving %s ago (the tab may have closed)", ago(r.LastPieceAt))
		if v.Here {
			v.Next = "Open the recorder in the browser that made it to send the rest, or keep what arrived."
		} else {
			v.Next = "Open the recorder in the browser that made it to send the rest, or keep what arrived on " + machine(r) + "."
		}
	case r.State == models.RecordingFailed:
		v.Next = r.Error
	case r.State == models.RecordingReady && len(r.Links) == 0:
		v.Next = "Link it into a project to cut it, with the screen and camera together."
	case r.State == models.RecordingReady && r.Track(models.TrackScreen) != nil && r.Track(models.TrackScreen).AssetID != "" && r.Track(models.TrackCamera) != nil && r.Track(models.TrackCamera).AssetID != "":
		v.Next = "Start a cut from it: the screen with the camera in a corner, or side by side."
	case r.State == models.RecordingReady:
		v.Next = "Start a cut from it."
	}
	if r.State == models.RecordingReady && r.Error != "" {
		v.Next = r.Error
	}
	v.Text = fmt.Sprintf("“%s”: %s, the %s; %s.", r.Name, clockText(length), joinAnd(kinds), where)
	return v
}

func trackText(t models.RecordingTrack) string {
	var parts []string
	switch t.ID {
	case models.TrackScreen:
		what := map[string]string{"monitor": "the whole screen", "window": "a window", "browser": "a browser tab"}[t.Surface]
		if what == "" {
			what = "the screen"
		}
		parts = append(parts, what)
		switch t.Sound {
		case "own":
			parts = append(parts, "with its sound")
		case "mic":
			parts = append(parts, "with the microphone for listening")
		}
	case models.TrackCamera:
		parts = append(parts, "the camera")
	case models.TrackVoice:
		parts = append(parts, "the microphone")
	}
	if t.Label != "" {
		parts = append(parts, "“"+t.Label+"”")
	}
	if t.Width > 0 && t.Height > 0 {
		parts = append(parts, fmt.Sprintf("%d×%d", t.Width, t.Height))
	}
	switch t.State {
	case "failed":
		parts = append(parts, "couldn't be added: "+strings.TrimRight(t.Error, "."))
	case "ready":
		parts = append(parts, "in the Library")
	case "receiving":
		parts = append(parts, sizeWords(t.Bytes)+" received")
	}
	return strings.Join(parts, ", ")
}

func syncWords(c *models.Conversation) string {
	s := c.Sync
	how := "lined up by the clock it was recorded with"
	if s.Method == conversation.MethodAudio {
		how = "lined up by their sound"
	}
	return how + " (" + conversation.Sure(s.Confidence) + ")"
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.0f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", n>>10)
}

// clockText is a length as m:ss.
func clockText(sec float64) string {
	if sec <= 0 {
		return "no length yet"
	}
	s := int(math.Round(sec))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func ago(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < 2*time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < 2*time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	}
	return fmt.Sprintf("%d days", int(d.Hours()/24))
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// visible: a recording that isn't footage yet is its maker's (and owners');
// footage is everyone's.
func visible(ctx context.Context, a *app.App, r *models.Recording) bool {
	return r.State == models.RecordingReady || r.CreatedBy.UserID == "" || r.CreatedBy.UserID == a.Actor.UserID || ops.IsOwnerCaller(ctx)
}

// liveProject loads a project a recording goes into, refusing an archived one.
func liveProject(ctx context.Context, a *app.App, id string) (*models.Project, error) {
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no project %s in this workspace.", id)
	}
	if p.Archived {
		return nil, invalid("%s is archived: restore it first, or choose another project.", p.Name)
	}
	return &p, nil
}

func list(ctx context.Context, a *app.App, in *RecordingListIn) (*RecordingListOut, error) {
	var and bson.A
	if in.ProjectID != "" {
		and = append(and, bson.M{"$or": bson.A{bson.M{"projectId": in.ProjectID}, bson.M{"links.projectId": in.ProjectID}}})
	}
	if !in.Discarded {
		and = append(and, bson.M{"state": bson.M{"$ne": models.RecordingDiscarded}})
	}
	if !ops.IsOwnerCaller(ctx) {
		// A member sees their own recordings, and others' once they are footage.
		and = append(and, bson.M{"$or": bson.A{bson.M{"createdBy.userId": a.Actor.UserID}, bson.M{"state": models.RecordingReady}}})
	}
	filter := bson.M{}
	if len(and) > 0 {
		filter["$and"] = and
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 100
	}
	var all []models.Recording
	if err := a.DB.Find(ctx, store.CRecordings, a.WorkspaceID(), filter, &all, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(int64(limit))); err != nil {
		return nil, err
	}
	out := &RecordingListOut{Recordings: []RecordingInfo{}}
	for i := range all {
		out.Recordings = append(out.Recordings, view(ctx, a, &all[i]))
	}
	return out, nil
}

// presenter is the person recorded: who made the recording.
func presenter(r *models.Recording) string {
	if n := strings.TrimSpace(r.CreatedBy.UserName); n != "" {
		return n
	}
	return "Presenter"
}

// link puts a recording's footage in a project and makes (or lines up again)
// its conversation there.
func link(ctx context.Context, a *app.App, r *models.Recording, projectID string) (*models.Conversation, error) {
	if r.State != models.RecordingReady {
		return nil, apperr.New(apperr.Conflict, "“%s” isn't footage yet (%s).", r.Name, stateWords(r.State)).WithCommand("metarush recording finish " + r.ID)
	}
	if _, err := liveProject(ctx, a, projectID); err != nil {
		return nil, err
	}
	var members []conversation.RecordedMember
	who := presenter(r)
	for _, t := range r.Tracks {
		if t.AssetID == "" {
			continue
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), t.AssetID, &as); err != nil {
			return nil, apperr.New(apperr.NotFound, "The recording's %s isn't in the Library any more.", trackWords(t.ID))
		}
		if !contains(as.ProjectIDs, projectID) {
			if _, err := ops.CallTyped[ops.AssetLinkOut](ctx, a, "asset.link", ops.AssetLinkIn{AssetID: t.AssetID, ProjectID: projectID}, ops.Caller{}); err != nil {
				return nil, err
			}
		}
		m := conversation.RecordedMember{ID: t.ID, AssetID: t.AssetID, Start: t.Start, Drift: t.Drift}
		switch t.ID {
		case models.TrackScreen:
			m.Name, m.Use = "Screen", editspec.UsePicture
			if t.Sound == "own" {
				m.Use = editspec.UseBoth
			}
			m.Framing = &editspec.Framing{Mode: "fit"} // the whole screen, never cropped
		case models.TrackCamera:
			m.Name, m.Use = who, editspec.UsePicture
			m.Framing = &editspec.Framing{Auto: editspec.FramingFaces}
		case models.TrackVoice:
			m.Name, m.Use = who, editspec.UseSound
		}
		members = append(members, m)
	}
	sure := 0.9
	if r.Clock == nil {
		sure = 0.5 // no measurement arrived: the files are taken to start together
	} else if r.Clock.Spread > 40 {
		sure = 0.6
	}
	in := conversation.RecordedIn{ProjectID: projectID, Name: r.Name, Members: members, Confidence: sure}
	if l := r.LinkIn(projectID); l != nil {
		in.ConversationID = l.ConversationID
	}
	c, err := conversation.FromRecording(ctx, a, in)
	if err != nil {
		return nil, err
	}
	r.Lined = "lined up by the clock it was recorded with"
	if soundCheckable(r) {
		// The pictures carry the microphone: their sound says to the sample where
		// the voice belongs; the clock stays when it can't tell surely, or when
		// a clock that was measured says the sound matched the wrong moment.
		maxMove := conversation.SoundCheckMaxMove
		if r.Clock == nil {
			maxMove = 0 // no clock arrived: nothing to hold the sound to
		}
		_, why, err := conversation.RefineBySound(ctx, a, c, 2, maxMove)
		if err != nil {
			return nil, err
		}
		r.Lined = why
	}
	if l := r.LinkIn(projectID); l != nil {
		l.ConversationID, l.At = c.ID, time.Now().UTC()
	} else {
		r.Links = append(r.Links, models.RecordingLink{ProjectID: projectID, ConversationID: c.ID, At: time.Now().UTC()})
	}
	if _, err := a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": bson.M{"links": r.Links, "lined": r.Lined, "updatedAt": time.Now().UTC()}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recording.link", "recording", r.ID, projectID, map[string]any{"conversation": c.ID})
	return c, nil
}

// soundCheckable: the recording has a voice and a picture that carries a
// copy of the microphone, and no picture with sound of its own (which would
// match nothing).
func soundCheckable(r *models.Recording) bool {
	voice, mic := false, false
	for _, t := range r.Tracks {
		if t.AssetID == "" {
			continue
		}
		switch {
		case t.ID == models.TrackVoice:
			voice = true
		case t.Sound == "own":
			return false
		case t.Sound == "mic":
			mic = true
		}
	}
	return voice && mic
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func cut(ctx context.Context, a *app.App, in *RecordingCutIn) (*RecordingCutOut, error) {
	r, err := get(ctx, a, in.Recording)
	if err != nil {
		return nil, err
	}
	pid := ""
	switch {
	case in.Project != "":
		p, err := ops.ResolveProject(ctx, a, in.Project)
		if err != nil {
			return nil, err
		}
		pid = p.ID
	case r.ProjectID != "":
		pid = r.ProjectID
	case len(r.Links) > 0:
		pid = r.Links[0].ProjectID
	default:
		return nil, invalid("“%s” is in the Inbox: say which project the cut is for.", r.Name)
	}
	if _, err := liveProject(ctx, a, pid); err != nil {
		return nil, err
	}
	l := r.LinkIn(pid)
	if l == nil {
		if _, err := link(ctx, a, r, pid); err != nil {
			return nil, err
		}
		l = r.LinkIn(pid)
	}
	has := map[string]bool{}
	for _, t := range r.Tracks {
		if t.AssetID != "" {
			has[t.ID] = true
		}
	}
	layout := in.Layout
	if layout == "" {
		layout = r.Layout
	}
	if layout == "" || ((layout == "pip" || layout == "side-by-side") && !(has["screen"] && has["camera"])) {
		switch {
		case has["screen"] && has["camera"]:
			layout = "pip"
		case has["screen"]:
			layout = "screen"
		default:
			layout = "camera"
		}
		if in.Layout != "" && in.Layout != layout {
			return nil, invalid("“%s” has no %s, so it can't be shown %s.", r.Name, missingFor(in.Layout, has), layoutWords(in.Layout))
		}
	}
	lc := conversation.LayoutCutIn{Conversation: l.ConversationID, Project: pid, Branch: in.Branch, Shape: in.Shape, Title: r.Name,
		Instruction: fmt.Sprintf("Start a cut from the recording %s: %s", r.Name, layoutWords(layout)), Origin: "recording:" + r.ID, Apply: in.Apply,
		Notes: fmt.Sprintf("Made from the screen recording “%s” (%s). Trim it on the timeline or in the transcript; the voice plays throughout.", r.Name, layoutWords(layout))}
	switch layout {
	case "pip":
		lc.Layout, lc.Members = editspec.LayoutPiP, []string{"screen", "camera"}
	case "side-by-side":
		lc.Layout, lc.Members = editspec.LayoutSideBySide, []string{"screen", "camera"}
	case "screen", "camera":
		if !has[layout] {
			return nil, invalid("“%s” has no %s track.", r.Name, layout)
		}
		lc.Members = []string{layout}
	default:
		return nil, invalid("the layout is pip, side-by-side, screen or camera, not %q", layout)
	}
	out, err := conversation.LayoutCut(ctx, a, lc)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "recording.cut", "recording", r.ID, pid, map[string]any{"layout": layout, "applied": out.Applied != nil && out.Applied.Problem == ""})
	return &RecordingCutOut{LayoutCutOut: *out, ProjectID: pid, Layout: layout}, nil
}

func missingFor(layout string, has map[string]bool) string {
	if !has["camera"] {
		return "camera"
	}
	if !has["screen"] {
		return "screen"
	}
	_ = layout
	return "picture"
}

// layoutWords names a recording layout.
func layoutWords(l string) string {
	switch l {
	case "pip":
		return "the screen with the camera in a corner"
	case "side-by-side":
		return "the screen and the camera side by side"
	case "screen":
		return "the screen only"
	case "camera":
		return "the camera only"
	}
	return l
}

func discard(ctx context.Context, a *app.App, in *RecordingRefIn) (*RecordingInfo, error) {
	r, err := get(ctx, a, in.Recording)
	if err != nil {
		return nil, err
	}
	if err := mayEnd(ctx, a, r, "throw away"); err != nil {
		return nil, err
	}
	for _, t := range r.Tracks {
		if t.AssetID != "" {
			return nil, apperr.New(apperr.Conflict, "Part of “%s” is already footage in the Library.", r.Name).
				WithHint("Move its clips to the Trash in the Library instead.")
		}
	}
	switch {
	case r.State == models.RecordingDiscarded:
		v := view(ctx, a, r)
		return &v, nil
	case r.State == models.RecordingAdding && !leaseStale(r, time.Now()):
		return nil, apperr.New(apperr.Conflict, "“%s” is being added to the Library right now.", r.Name)
	}
	if r.InstallationID != a.Cfg.Installation.ID {
		return nil, apperr.New(apperr.Conflict, "The recording's pieces are on %s, so it can be thrown away only there.", machine(r)).WithCommand("metarush recording discard " + r.ID)
	}
	if err := throwAway(ctx, a, r, "", "", nil); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recording.discard", "recording", r.ID, r.ProjectID, nil)
	v := view(ctx, a, r)
	return &v, nil
}

// throwAway marks a recording that isn't footage discarded (only if it is
// still recording or failed, or its finishing lease went stale, and matches
// guard: the sweep's "unchanged since I read it") and deletes its pieces.
func throwAway(ctx context.Context, a *app.App, r *models.Recording, ended, why string, guard bson.M) error {
	now := time.Now().UTC()
	filter := bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "tracks.assetId": bson.M{"$not": bson.M{"$gt": ""}}, "$or": bson.A{
		bson.M{"state": bson.M{"$in": bson.A{models.RecordingRecording, models.RecordingFailed}}},
		bson.M{"state": models.RecordingAdding, "addingAt": bson.M{"$lt": now.Add(-LeaseStale)}},
		bson.M{"state": models.RecordingAdding, "addingAt": bson.M{"$exists": false}},
	}}
	for k, v := range guard {
		filter[k] = v
	}
	set := bson.M{"state": models.RecordingDiscarded, "updatedAt": now}
	if ended != "" {
		set["ended"] = ended
	}
	if why != "" {
		set["error"] = why
	}
	res, err := a.DB.C(store.CRecordings).UpdateOne(ctx, filter, bson.M{"$set": set, "$unset": bson.M{"addingAt": "", "addingBy": ""}})
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return apperr.New(apperr.Conflict, "“%s” is being added to the Library right now.", r.Name)
	}
	_ = os.RemoveAll(dir(a, r.ID))
	r.State, r.AddingAt, r.AddingBy, r.UpdatedAt = models.RecordingDiscarded, nil, "", now
	if ended != "" {
		r.Ended = ended
	}
	if why != "" {
		r.Error = why
	}
	return nil
}

// ValidID reports whether id looks like a recording's.
func ValidID(id string) bool { return ids.Valid(ids.Recording, id) }
