package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// visualsPossible says whether clips can be looked at: this Mac can, or a Mac
// in the workspace said in the last 30 days that it can (its worker runs the
// visual job type).
func visualsPossible(ctx context.Context, a *app.App) bool {
	if vision.CanRun(a.Tools) {
		return true
	}
	filter := store.SeenWithin("lastHeartbeat", 30*24*time.Hour) // the server's time, as heartbeats are written
	filter["workspaceId"], filter["jobTypes"] = a.WorkspaceID(), jobs.TypeVisual
	n, err := a.DB.C(store.CWorkers).CountDocuments(ctx, filter)
	return err == nil && n > 0
}

// cutsOf are a clip's hard cuts from its technical evidence.
func cutsOf(ev *evidence.Stored) []float64 {
	var cuts []float64
	if ev == nil {
		return cuts
	}
	if ev.VisualChanges != nil {
		for _, c := range ev.VisualChanges {
			if c.Kind == evidence.ChangeCut {
				cuts = append(cuts, c.At)
			}
		}
		return cuts
	}
	for _, s := range ev.Scenes {
		cuts = append(cuts, s.At)
	}
	return cuts
}

// FaceRow is a face of a clip: when it shows, where, and whose it is.
type FaceRow struct {
	ID      string    `json:"id"`
	AssetID string    `json:"assetId"`
	Track   int       `json:"track"`
	From    float64   `json:"from"`
	To      float64   `json:"to"`
	At      float64   `json:"at" jsonschema:"description=The frame that shows the face best (source seconds)."`
	Box     []float64 `json:"box" jsonschema:"description=Where the face is in that frame: x, y, width, height from the top-left, 0 to 1."`
	Quality float64   `json:"quality"`
	// Name is whose face it is, as a person confirmed ("" when nobody said).
	Name     string `json:"name,omitempty"`
	PersonID string `json:"personId,omitempty"`
	// Recognized is who MetaRush named it after by its looks (not confirmed:
	// confirm it with footage faces name, or say "not them" with footage faces reject).
	Recognized *models.FaceRecognized `json:"recognized,omitempty"`
	// Maybe is who it might be (a question is open about it, or about its
	// group of faces).
	Maybe    *models.FaceCandidate `json:"maybe,omitempty"`
	Question string                `json:"question,omitempty" jsonschema:"description=The open to-do that asks about it (answer it with todos answer)."`
	// Group is its question group in the clip: one person's unnamed faces,
	// asked about once (D155).
	Group string `json:"group,omitempty"`
	// Appearances: on a person's page, a question about a group of faces says
	// how many times they are seen.
	Appearances int `json:"appearances,omitempty"`
	// AssetTitle and ProjectID say which clip (lists across clips, such as a person's faces).
	AssetTitle string `json:"assetTitle,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
}

func faceRow(f models.FaceTrack) FaceRow {
	r := FaceRow{ID: f.ID, AssetID: f.AssetID, Track: f.Track, From: f.From, To: f.To, At: f.At, Box: f.Box, Quality: f.Quality, Name: f.Name, PersonID: f.PersonID,
		Recognized: f.Recognized, Maybe: f.Candidate}
	if !f.Confirmed() && f.Recognized == nil && !f.Skipped {
		r.Group = f.Group
	}
	return r
}

// groupQuestions finds the open questions about groups of faces (D155) by group.
func groupQuestions(ctx context.Context, a *app.App, filter bson.M) map[string]models.Todo {
	var ts []models.Todo
	f := bson.M{"kind": models.TodoFaceGroup, "state": models.TodoOpen}
	for k, v := range filter {
		f[k] = v
	}
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), f, &ts, options.Find().SetProjection(bson.M{"assetId": 1, "context.groupId": 1, "context.faceId": 1,
		"context.personId": 1, "context.appearances": 1}))
	out := map[string]models.Todo{}
	for _, t := range ts {
		if t.Context.GroupID != "" {
			out[t.Context.GroupID] = t
		}
	}
	return out
}

// questionIDs finds the open face questions by face.
func questionIDs(ctx context.Context, a *app.App, filter bson.M) map[string]string {
	var ts []models.Todo
	f := bson.M{"kind": models.TodoFacePerson, "state": models.TodoOpen}
	for k, v := range filter {
		f[k] = v
	}
	_ = a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), f, &ts, options.Find().SetProjection(bson.M{"context.faceId": 1, "context.personId": 1}))
	out := map[string]string{}
	for _, t := range ts {
		out[t.Context.FaceID+"/"+t.Context.PersonID] = t.ID
	}
	return out
}

// VisualsIn reads what's on screen in a clip.
type VisualsIn struct {
	AssetID string  `json:"assetId"`
	From    float64 `json:"from,omitempty"`
	To      float64 `json:"to,omitempty" jsonschema:"description=End of the range (default: the end of the clip)."`
}

// VisualsOut is the readout.
type VisualsOut struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	// State: available (indexed), queued or running, missing (not looked at yet),
	// unavailable (no Mac in this workspace can look), none (the clip has no picture).
	State   string            `json:"state" jsonschema:"enum=available,enum=queued,enum=running,enum=failed,enum=missing,enum=unavailable,enum=none"`
	Summary string            `json:"summary"`
	Lines   []visual.Line     `json:"lines"`
	Faces   []FaceRow         `json:"faces"`
	Text    []visual.TextSpan `json:"text"`
	// Labels are what the picture shows, merged per label over the range (longest
	// first; very general ones such as "people" or "liquid" are left out).
	Labels    []VisualLabel     `json:"labels"`
	Shots     []visual.ShotSpan `json:"shots"`
	IndexedAt *time.Time        `json:"indexedAt,omitempty"`
	Frames    int               `json:"frames" jsonschema:"description=Frames looked at for the whole clip."`
	// Described are written scene descriptions in the range (Claude Code; absent when none).
	Described *Descriptions `json:"described,omitempty"`
	Note      string        `json:"note"`
}

// VisualLabel is one thing seen in the range and for how long.
type VisualLabel struct {
	Label   string  `json:"label"`
	Kind    string  `json:"kind" jsonschema:"enum=scene,enum=thing,enum=animal,enum=person"`
	Seconds float64 `json:"seconds"`
	First   float64 `json:"first"`
}

func visualState(ctx context.Context, a *app.App, as *models.Asset, st *visual.Stored) (string, string) {
	if !as.Media.HasVideo {
		return "none", "This clip has no picture."
	}
	if st != nil && as.Analysis[jobs.TypeVisual] != "running" {
		return "available", ""
	}
	if js, err := a.Queue.List(ctx, bson.M{"assetId": as.ID, "type": jobs.TypeVisual}, 1); err == nil && len(js) > 0 {
		switch js[0].State {
		case models.JobQueued:
			return "queued", "Waiting to look at what's on screen (it runs on a Mac; big jobs wait while the battery is low)."
		case models.JobRunning:
			return "running", "Looking at what's on screen now."
		case models.JobFailed:
			return "failed", "Looking at what's on screen didn't work this time. Try again; the job's details say why."
		}
	}
	if !visualsPossible(ctx, a) {
		return "unavailable", "Not looked at: seeing what's on screen uses Apple Vision on a Mac, and no Mac in this workspace can do it yet."
	}
	return "missing", "Not looked at yet. Open the clip and choose Look now under On screen (it runs on a Mac; nothing is uploaded)."
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return s
}

// FacesIn lists a clip's faces.
type FacesIn struct {
	AssetID string `json:"assetId"`
}

// FacesOut lists them.
type FacesOut struct {
	AssetID string    `json:"assetId"`
	Title   string    `json:"title"`
	Faces   []FaceRow `json:"faces"`
	Summary string    `json:"summary"`
}

// FaceNameIn says whose face it is.
type FaceNameIn struct {
	FaceID string `json:"faceId"`
	// Name or PersonID: who it is (an existing person with that name is reused).
	Name      string `json:"name,omitempty" jsonschema:"maxLength=80"`
	PersonID  string `json:"personId,omitempty"`
	NewPerson bool   `json:"newPerson,omitempty" jsonschema:"description=Add the name as someone new even when it is close to an existing name."`
}

// FaceRejectIn says a face is not someone.
type FaceRejectIn struct {
	FaceID string `json:"faceId"`
	// Person is who it is not (a name or person ID); default: the name it has now, else the open question's person.
	Person string `json:"person,omitempty"`
}

// FaceOut is a face after a change.
type FaceOut struct {
	Face FaceRow `json:"face"`
	Done string  `json:"done"`
}

func faceErr(err error) error {
	if errors.Is(err, faces.ErrNotFound) {
		return apperr.New(apperr.NotFound, "That face was not found. It may belong to a clip that was deleted.").
			WithHint("A clip's faces are listed under On screen when you open it.").WithCommand("metarush footage faces <clip>")
	}
	return err
}

func init() {
	Def("footage.visuals", "Read what's on screen in a clip, in plain lines (shot type, faces and whose, setting, things, on-screen text), from the on-device index", Flags{}, func(ctx context.Context, a *app.App, in *VisualsIn) (*VisualsOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		out := &VisualsOut{AssetID: as.ID, Title: as.DisplayTitle(), Lines: []visual.Line{}, Faces: []FaceRow{}, Text: []visual.TextSpan{}, Labels: []VisualLabel{}, Shots: []visual.ShotSpan{}}
		st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID)
		out.State, out.Note = visualState(ctx, a, &as, st)
		if d := LoadDescriptions(ctx, a, as.ID); d != nil {
			keep := []SceneDescription{}
			for _, s := range d.Scenes {
				if (in.To <= 0 || s.From < in.To) && s.To >= in.From {
					keep = append(keep, s)
				}
			}
			d.Scenes = keep
			out.Described = d
		}
		if st == nil {
			out.Summary = out.Note
			return out, nil
		}
		idx := st.Index
		from, to := in.From, in.To
		if to <= 0 || to > idx.Duration {
			to = idx.Duration
		}
		if from < 0 || from >= to {
			return nil, invalid("The start (%.1f s) must come before the end (%.1f s), inside the clip.", from, to)
		}
		t := st.CreatedAt
		out.IndexedAt, out.Frames, out.Summary = &t, idx.Frames, idx.Summary
		tracks, err := faces.Load(ctx, a, as.ID)
		if err != nil {
			return nil, err
		}
		ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
		cuts := cutsOf(ev)
		// Call footage (D160): shots and on-screen text start and end on the
		// picture switches found to the frame, not on the index's second.
		if cs := calls.Load(ctx, a.DB, a.WorkspaceID(), as.ID); cs != nil {
			idx, cuts = calls.Refine(idx, cuts, cs)
		}
		out.Lines = visual.Readout(idx, cuts, from, to, faces.Namer(tracks))
		qs := questionIDs(ctx, a, bson.M{"assetId": as.ID})
		gq := groupQuestions(ctx, a, bson.M{"assetId": as.ID})
		var ids []int
		for id := range tracks {
			ids = append(ids, id)
		}
		sort.Ints(ids)
		for _, id := range ids {
			f := tracks[id]
			if f.To <= from || f.From >= to {
				continue
			}
			r := faceRow(f)
			switch {
			case r.Group != "" && gq[r.Group].ID != "":
				r.Question = gq[r.Group].ID
			case f.Candidate != nil:
				r.Question = qs[f.ID+"/"+f.Candidate.PersonID]
			}
			out.Faces = append(out.Faces, r)
		}
		for _, x := range idx.Text {
			if x.To > from && x.From < to {
				out.Text = append(out.Text, x)
			}
		}
		for _, s := range idx.Shots {
			if s.To > from && s.From < to {
				out.Shots = append(out.Shots, s)
			}
		}
		byLabel := map[string]*VisualLabel{}
		for _, l := range idx.Labels {
			ov := math.Min(to, l.To) - math.Max(from, l.From)
			if ov <= 0 || visual.Generic(l.Label) {
				continue
			}
			v := byLabel[l.Label]
			if v == nil {
				v = &VisualLabel{Label: l.Label, Kind: l.Kind, First: math.Max(from, l.From)}
				byLabel[l.Label] = v
			}
			v.Seconds += ov
		}
		for _, v := range byLabel {
			v.Seconds = math.Round(v.Seconds*10) / 10
			out.Labels = append(out.Labels, *v)
		}
		sort.SliceStable(out.Labels, func(i, j int) bool {
			if out.Labels[i].Seconds != out.Labels[j].Seconds {
				return out.Labels[i].Seconds > out.Labels[j].Seconds
			}
			return out.Labels[i].Label < out.Labels[j].Label
		})
		out.Note = "From Apple Vision on this workspace's Mac, about one frame a second plus the moments the picture changes: labels are machine guesses, " +
			"text is read from the picture (check spelling before quoting it), and a face has a name where a person confirmed it; \"looks like\" names were recognized by the face and are not confirmed yet " +
			"(\"maybe\" = a question is open). Look at frames (footage frames --at) before claiming anything visual."
		return out, nil
	})
	Def("footage.faces", "List the faces seen in a clip: when each shows, where, and whose it is (named by a person, or recognized by the face and not confirmed yet)", Flags{}, func(ctx context.Context, a *app.App, in *FacesIn) (*FacesOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		fs, err := faces.List(ctx, a, as.ID)
		if err != nil {
			return nil, err
		}
		out := &FacesOut{AssetID: as.ID, Title: as.DisplayTitle(), Faces: []FaceRow{}}
		qs := questionIDs(ctx, a, bson.M{"assetId": as.ID})
		gq := groupQuestions(ctx, a, bson.M{"assetId": as.ID})
		named, recognized, asked := 0, 0, 0
		for _, f := range fs {
			r := faceRow(f)
			switch {
			case r.Group != "" && gq[r.Group].ID != "":
				r.Question = gq[r.Group].ID
				asked++
			case f.Candidate != nil && qs[f.ID+"/"+f.Candidate.PersonID] != "":
				r.Question = qs[f.ID+"/"+f.Candidate.PersonID]
				asked++
			}
			switch {
			case f.Name != "":
				named++
			case f.Recognized != nil:
				recognized++
			}
			out.Faces = append(out.Faces, r)
		}
		switch {
		case len(fs) == 0 && visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID) == nil:
			_, out.Summary = visualState(ctx, a, &as, nil)
		case len(fs) == 0:
			out.Summary = fmt.Sprintf("No faces seen in “%s”.", as.DisplayTitle())
		case recognized > 0:
			out.Summary = fmt.Sprintf("%s in “%s”: %d with a name, %d recognized by the face (not confirmed yet), %d asked about.", count(len(fs), "face", "faces"), as.DisplayTitle(), named, recognized, asked)
		default:
			out.Summary = fmt.Sprintf("%s in “%s”: %d with a name, %d asked about.", count(len(fs), "face", "faces"), as.DisplayTitle(), named, asked)
		}
		return out, nil
	})
	Def("footage.faces.name", "Say whose face it is in that clip (confirming a face also teaches MetaRush what they look like, to recognize them in other clips)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *FaceNameIn) (*FaceOut, error) {
		f, err := faces.Get(ctx, a, in.FaceID)
		if err != nil {
			return nil, faceErr(err)
		}
		if strings.TrimSpace(in.Name) == "" && in.PersonID == "" {
			return nil, invalid("say who it is: a name or a person")
		}
		p, _, err := speakers.PersonFor(ctx, a, in.Name, in.PersonID, in.NewPerson)
		if err != nil {
			return nil, speakerErr(err)
		}
		nf, err := faces.Confirm(ctx, a, f.ID, *p)
		if err != nil {
			return nil, faceErr(err)
		}
		afterFacesNamed(ctx, a, nf.AssetID) // its voice questions, and a named face names the clip better (D150)
		done := fmt.Sprintf("That face is %s now.", p.Name)
		if _, rec, _, err := faces.OfPerson(ctx, a, p.ID); err == nil {
			clips := map[string]bool{}
			for _, r := range rec {
				if r.AssetID != nf.AssetID {
					clips[r.AssetID] = true
				}
			}
			if len(clips) > 0 {
				done += fmt.Sprintf(" %s's face was also recognized in %s (not confirmed yet).", p.Name, count(len(clips), "other clip", "other clips"))
			}
		}
		return &FaceOut{Face: faceRow(*nf), Done: done}, nil
	})
	Def("footage.faces.reject", "Say a face is not someone (its name goes, and that person is not suggested for it again)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *FaceRejectIn) (*FaceOut, error) {
		f, err := faces.Get(ctx, a, in.FaceID)
		if err != nil {
			return nil, faceErr(err)
		}
		pid := ""
		switch {
		case in.Person != "":
			if pid, err = resolvePersonID(ctx, a, in.Person); err != nil {
				return nil, err
			}
		case f.PersonID != "":
			pid = f.PersonID
		case f.Recognized != nil:
			pid = f.Recognized.PersonID
		case f.Candidate != nil:
			pid = f.Candidate.PersonID
		default:
			return nil, invalid("this face has no name and no question about it; say who it is not")
		}
		name := ""
		var p models.Person
		if a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), pid, &p) == nil {
			name = p.Name
		}
		nf, err := faces.Reject(ctx, a, f.ID, pid)
		if err != nil {
			return nil, faceErr(err)
		}
		return &FaceOut{Face: faceRow(*nf), Done: fmt.Sprintf("Noted: that face is not %s.", orName(name, "them"))}, nil
	})
}

// capFirst upper-cases the first letter.
func capFirst(s string) string {
	for i, r := range s {
		return strings.ToUpper(string(r)) + s[i+len(string(r)):]
	}
	return s
}

// VisualStatusIn has no fields.
type VisualStatusIn struct{}

// VisionHere is this machine's on-screen helper.
type VisionHere struct {
	Supported bool   `json:"supported" jsonschema:"description=This is a Mac (Apple Vision exists here)."`
	Ready     bool   `json:"ready"`
	CanBuild  bool   `json:"canBuild" jsonschema:"description=The Xcode Command Line Tools can build the helper here."`
	Text      string `json:"text"`
	Fix       string `json:"fix,omitempty"`
}

// VisualStatusOut says whether clips are looked at, and how far along that is.
type VisualStatusOut struct {
	Here     VisionHere `json:"here"`
	Possible bool       `json:"possible" jsonschema:"description=This Mac or another Mac in the workspace can look at clips."`
	Clips    int        `json:"clips" jsonschema:"description=Clips with a picture."`
	Indexed  int        `json:"indexed" jsonschema:"description=Clips whose picture was looked at."`
	Waiting  int        `json:"waiting" jsonschema:"description=Clips queued or being looked at now."`
	Summary  string     `json:"summary"`
}

func init() {
	Def("footage.visuals.status", "Say whether clips get looked at (Apple Vision on a Mac), on this machine and in the workspace, and how many are done", Flags{}, func(ctx context.Context, a *app.App, _ *VisualStatusIn) (*VisualStatusOut, error) {
		st := vision.Check(ctx, a.Tools)
		out := &VisualStatusOut{Here: VisionHere{Supported: st.Supported, Ready: st.Ready, CanBuild: st.CanBuild, Text: st.Text, Fix: st.Fix}, Possible: visualsPossible(ctx, a)}
		if st.Supported && !st.Ready && st.CanBuild {
			out.Here.Text = "Ready to set up: the helper is built the first time a clip is looked at."
			out.Here.Fix = ""
		}
		ws := a.WorkspaceID()
		base := bson.M{"workspaceId": ws, "state": models.AssetAvailable, "media.hasVideo": true}
		n, _ := a.DB.C(store.CAssets).CountDocuments(ctx, base)
		out.Clips = int(n)
		done := bson.M{"workspaceId": ws, "state": models.AssetAvailable, "media.hasVideo": true, "analysis.visual": "available"}
		n, _ = a.DB.C(store.CAssets).CountDocuments(ctx, done)
		out.Indexed = int(n)
		n, _ = a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": ws, "type": jobs.TypeVisual, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}})
		out.Waiting = int(n)
		switch {
		case !out.Possible:
			out.Summary = "Not looking at what's on screen: that uses Apple Vision on a Mac, and no Mac in this workspace can do it yet."
		case out.Clips == 0:
			out.Summary = "Clips are looked at on a Mac as they come in."
		default:
			out.Summary = fmt.Sprintf("Looked at %d of %s.", out.Indexed, count(out.Clips, "clip", "clips"))
			if out.Waiting > 0 {
				out.Summary += fmt.Sprintf(" %d waiting.", out.Waiting)
			}
		}
		return out, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.visuals", Offering{ID: "whats-on-screen", Group: "understand", Title: "What's on screen",
		Does:  "Says what's on screen in a clip, line by line: the kind of shot, faces, the setting, things and words shown.",
		Words: []string{"shot type", "text on screen", "what can be seen"}, Needs: Needs{OS: []string{"mac"}, Setup: "on-screen-helper"}})
	Offer("footage.faces", Offering{ID: "faces", Group: "understand", Title: "Faces across clips",
		Does:  "Finds the faces in your footage and recognizes the people you named, across every clip.",
		Words: []string{"who appears", "who is on screen", "recognize people"}, Needs: Needs{OS: []string{"mac"}, Setup: "on-screen-helper"}})
	// The on-screen index runs where vision.CanRun says: a Mac (the OS need
	// says so elsewhere), with FFmpeg and the helper or what builds it.
	RegisterReady("on-screen-helper", func(_ context.Context, a *app.App) (bool, string) {
		switch {
		case runtime.GOOS != "darwin" || vision.CanRun(a.Tools):
			return true, ""
		case !a.Tools.Has(tools.FFmpeg):
			return false, "FFmpeg"
		}
		return false, "Apple's command line developer tools, to build the helper that looks at the picture"
	})
}
