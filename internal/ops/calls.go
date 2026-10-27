package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/visual"
)

// Call recordings (D160–D162, docs/call-recordings.md): a footage's picture
// switches found to the frame, who is on screen between them and the names a
// call app printed (footage.switches); trimming split-second flashes of the
// other picture at clip edges (cut.flashes); covering the printed names with
// the built-in name bar (cut.names).

// Footage implements editspec.SwitchFacts: a footage's switches, who is on
// screen and its printed names, read once for the spec's footage the first
// time something asks (the edit check, the name bar).
func (f *dbFacts) Footage(id string) (*editspec.FootageShots, bool) {
	if f.loadSwitches == nil {
		return nil, false
	}
	f.switchesOnce.Do(func() { f.switches = f.loadSwitches() })
	fs, ok := f.switches[id]
	return fs, ok && fs != nil
}

// switchLoader reads the spec's footage's switches when first needed.
func switchLoader(ctx context.Context, a *app.App, f *dbFacts, ids []string) func() map[string]*editspec.FootageShots {
	return func() map[string]*editspec.FootageShots {
		return calls.Footage(ctx, a.DB, a.WorkspaceID(), ids, func(id string) []editspec.TranscriptSegment { return f.tx[id] })
	}
}

// FootageSwitchesIn names a clip whose picture switches to read.
type FootageSwitchesIn struct {
	AssetID string `json:"assetId" jsonschema:"description=The clip (its title or ID)."`
}

// FootageSwitchesOut is a footage's picture switches, its shots and who is in
// them, and the names the call app printed.
type FootageSwitchesOut struct {
	AssetID string `json:"assetId"`
	Title   string `json:"title"`
	// State: ready, waiting (being found), none (no cuts, or not looked at yet).
	State     string                 `json:"state" jsonschema:"enum=ready,enum=waiting,enum=none"`
	Summary   string                 `json:"summary"`
	Switches  []calls.Switch         `json:"switches"`
	Shots     []editspec.FootageShot `json:"shots"`
	Names     *editspec.PrintedNames `json:"names,omitempty"`
	Precision float64                `json:"precision" jsonschema:"description=How far the original's switch can be from these times (seconds; 0 = exact)."`
	Source    string                 `json:"source,omitempty"`
	Next      string                 `json:"next,omitempty"`
}

// FootageSwitchesFindIn asks for a clip's switches (again).
type FootageSwitchesFindIn struct {
	AssetID string `json:"assetId" jsonschema:"description=The clip (its title or ID)."`
}

// FootageSwitchesFindOut is the work that finds them.
type FootageSwitchesFindOut struct {
	AssetID string   `json:"assetId"`
	Jobs    []string `json:"jobs"`
	Summary string   `json:"summary"`
}

func footageSwitches(ctx context.Context, a *app.App, in *FootageSwitchesIn) (*FootageSwitchesOut, error) {
	as, err := findAsset(ctx, a, in.AssetID, "")
	if err != nil {
		return nil, err
	}
	out := &FootageSwitchesOut{AssetID: as.ID, Title: as.DisplayTitle(), Switches: []calls.Switch{}, Shots: []editspec.FootageShot{}}
	st := calls.Load(ctx, a.DB, a.WorkspaceID(), as.ID)
	if st == nil {
		out.State = "none"
		if as.Analysis[calls.JobType] == "running" {
			out.State = "waiting"
		}
		if js, err := a.Queue.List(ctx, map[string]any{"assetId": as.ID, "type": calls.JobType, "state": map[string]any{"$in": []string{models.JobQueued, models.JobRunning}}}, 1); err == nil && len(js) > 0 {
			out.State = "waiting"
		}
		out.Summary = "MetaRush hasn't looked for the picture switches of " + theirsName(as.DisplayTitle()) + " yet."
		if out.State == "waiting" {
			out.Summary = "MetaRush is finding where the picture of " + theirsName(as.DisplayTitle()) + " switches."
		}
		out.Next = "metarush footage switches " + as.ID + " --find --wait"
		return out, nil
	}
	out.State, out.Switches, out.Precision, out.Source = "ready", st.Switches, st.Precision, st.Source
	tx := map[string][]editspec.TranscriptSegment{}
	if f, err := loadFacts(ctx, a, "", &editspec.Spec{Clips: []editspec.Clip{{ID: "c", AssetID: as.ID, In: 0, Out: 1}}}); err == nil {
		tx[as.ID] = f.tx[as.ID]
	}
	fs := calls.Footage(ctx, a.DB, a.WorkspaceID(), []string{as.ID}, func(id string) []editspec.TranscriptSegment { return tx[id] })[as.ID]
	if fs != nil {
		out.Shots, out.Names = fs.Shots, fs.Names
	}
	known := 0
	for _, sh := range out.Shots {
		if sh.Person != "" {
			known++
		}
	}
	out.Summary = fmt.Sprintf("%s switches its picture %s: %s, with who is on screen known in %d.", theirsName(as.DisplayTitle()), timesWord(len(st.Switches)), pluralWord(len(out.Shots), "shot"), known)
	if len(st.Switches) == 0 {
		out.Summary = theirsName(as.DisplayTitle()) + " doesn't switch its picture."
	}
	if out.Names != nil {
		out.Summary += fmt.Sprintf(" The call app printed names at the %s.", strings.ReplaceAll(out.Names.Corner, "-", " "))
	}
	return out, nil
}

func timesWord(n int) string {
	switch n {
	case 1:
		return "once"
	case 2:
		return "twice"
	}
	return fmt.Sprintf("%d times", n)
}

func theirsName(s string) string { return "“" + s + "”" }

// CutFlashesIn trims the split-second flashes at the clip edges of the latest cut.
type CutFlashesIn struct {
	CutEditCommon
}

// CutFlashesOut is the change, with the flashes it removes.
type CutFlashesOut struct {
	CutEditOut
	Flashes []editspec.EdgeFlash `json:"flashes"`
}

func cutFlashes(ctx context.Context, a *app.App, in *CutFlashesIn) (*CutFlashesOut, error) {
	t, err := loadEditTarget(ctx, a, in.CutEditCommon)
	if err != nil {
		return nil, err
	}
	flashes := editspec.EdgeFlashes(t.spec, t.facts)
	out := &CutFlashesOut{Flashes: flashes}
	if out.Flashes == nil {
		out.Flashes = []editspec.EdgeFlash{}
	}
	var ops []editspec.Op
	locked := 0
	for _, f := range flashes {
		fix := editspec.EdgeFlashFix(t.spec, f, t.facts)
		if len(fix) == 0 {
			locked++
			continue
		}
		ops = append(ops, fix...)
	}
	if len(ops) == 0 {
		msg := "The latest cut has no split-second flashes at its clips' edges."
		if locked > 0 {
			msg = fmt.Sprintf("The flashes are in locked clips (%d), so nothing changes.", locked)
		}
		out.CutEditOut = CutEditOut{ProjectID: t.project.ID, Branch: t.branch, Summary: msg, NoChange: true, Changes: []string{}, Problems: []cutsheet.Problem{}}
		raw, _ := json.Marshal(editspec.Plan{BaseRevisionID: t.rev.ID})
		out.Plan = raw
		return out, nil
	}
	n := len(flashes) - locked
	summary := "Trims the split-second flash at the exact picture switch."
	if n > 1 {
		summary = fmt.Sprintf("Trims %d split-second flashes, each at the exact picture switch.", n)
	}
	res, err := t.finish(ctx, a, in.CutEditCommon, "flashes", ops, summary, "Trim the split-second flashes at the clips' edges", &out.CutEditOut)
	if err != nil {
		return nil, err
	}
	out.CutEditOut = *res
	return out, nil
}

// CutNamesIn covers the names a call app printed with the built-in name bar.
type CutNamesIn struct {
	CutEditCommon
	// FirstNames shows only first names (default: when the project's rules say so).
	FirstNames *bool  `json:"firstNames,omitempty" jsonschema:"description=Only first names (default: what the project's rules say)."`
	Style      string `json:"style,omitempty" jsonschema:"enum=band,enum=tab,description=band (default): across the bottom; tab: only as wide as the names."`
	KeepTitles bool   `json:"keepTitles,omitempty" jsonschema:"description=Keep the person-name titles (by default the bar replaces them)."`
	ID         string `json:"id,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=The graphic's name (default names)."`
}

// CutNamesOut is the change, with what the name bar does.
type CutNamesOut struct {
	CutEditOut
	NameBar *editspec.NameBarPlan `json:"nameBar,omitempty"`
}

func cutNames(ctx context.Context, a *app.App, in *CutNamesIn) (*CutNamesOut, error) {
	t, err := loadEditTarget(ctx, a, in.CutEditCommon)
	if err != nil {
		return nil, err
	}
	first := content.HasMeasure(content.RulesFor(ctx, a, t.project), MeasureFirstNames)
	if in.FirstNames != nil {
		first = *in.FirstNames
	}
	// The house style's name bar (D199): its look, height and animation.
	house, _, _ := content.StyleFor(ctx, a, t.project.ID)
	look, height, animate := editspec.NameBarStyle(house)
	if in.Style != "" {
		look = in.Style
	}
	nb, ok := editspec.BuildNameBar(t.spec, t.facts, editspec.NameBarOptions{ID: in.ID, FirstNames: first, Style: look, KeepTitles: in.KeepTitles, Height: height, Animate: animate})
	if !ok {
		return nil, apperr.New(apperr.NotFound, "MetaRush hasn't found names printed in the picture of %s's footage.", t.project.Name).
			WithHint("It finds them in call recordings a Mac looked at (what's on screen), once it knows where their picture switches.").WithCommand("metarush footage switches <clip>")
	}
	out := &CutNamesOut{NameBar: &nb}
	var ops []editspec.Op
	if nb.Replaces {
		ops = append(ops, editspec.Op{Op: "removeOverlay", ID: nb.Overlay.ID})
	}
	for _, id := range nb.Removed {
		if id != nb.Overlay.ID {
			ops = append(ops, editspec.Op{Op: "removeOverlay", ID: id})
		}
	}
	o := nb.Overlay
	ops = append(ops, editspec.Op{Op: "addOverlay", Overlay: &o})
	who := "who is on screen"
	if len(nb.People) > 0 {
		who = joinPlainAnd(nb.People)
	}
	summary := fmt.Sprintf("Covers the names the call app printed with your own name bar for the whole video, showing %s.", who)
	if len(nb.Removed) > 0 {
		summary += fmt.Sprintf(" It replaces %s.", pluralWord(len(nb.Removed), "name title"))
	}
	res, err := t.finish(ctx, a, in.CutEditCommon, "names", ops, summary, "Cover the names printed in the video with our name bar", &out.CutEditOut)
	if err != nil {
		return nil, err
	}
	out.CutEditOut = *res
	return out, nil
}

func joinPlainAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func init() {
	Def("footage.switches", "Where a clip's picture switches (to the frame), who is on screen in each shot, and the names a call app printed in the picture", Flags{},
		func(ctx context.Context, a *app.App, in *FootageSwitchesIn) (*FootageSwitchesOut, error) {
			return footageSwitches(ctx, a, in)
		})
	Def("footage.switches.find", "Find where a clip's picture switches, to the frame (a few seconds of work on any computer; again when asked)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *FootageSwitchesFindIn) (*FootageSwitchesFindOut, error) {
			as, err := findAsset(ctx, a, in.AssetID, "")
			if err != nil {
				return nil, err
			}
			if !as.Media.HasVideo {
				return nil, apperr.New(apperr.InvalidInput, "%s has no picture.", theirsName(as.DisplayTitle()))
			}
			ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
			if ev == nil {
				return nil, apperr.New(apperr.Conflict, "%s isn't analyzed yet, so MetaRush doesn't know its cuts.", theirsName(as.DisplayTitle())).
					WithHint("Analyze it first.").WithCommand("metarush footage analyze --asset " + as.ID + " --wait")
			}
			ver := ""
			if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil && st.Index != nil {
				ver = st.Index.Version
			}
			// Asked again: a new key, so the work runs again.
			key := ver
			if calls.Load(ctx, a.DB, a.WorkspaceID(), as.ID) != nil {
				key = ver + "@" + strconv.FormatInt(time.Now().UnixNano(), 36)
			}
			id, _, err := calls.Queue(ctx, a, as, key, a.Actor)
			if err != nil {
				return nil, err
			}
			a.Audit(ctx, "footage.switches.find", "asset", as.ID, firstOr(as.ProjectIDs), map[string]any{"jobId": id})
			return &FootageSwitchesFindOut{AssetID: as.ID, Jobs: []string{id}, Summary: "MetaRush is finding where the picture of " + theirsName(as.DisplayTitle()) + " switches."}, nil
		})
	Def("cut.flashes", "Trim the split-second flashes of another picture at the edges of the latest cut's clips, at the exact switch, keeping the words; dryRun checks it and gives a confirm token", Flags{Mutating: true}, cutFlashes)
	Def("cut.names", "Cover the names a call app printed in the picture with the built-in name bar for the whole video, its name following who is on screen; dryRun checks it and gives a confirm token", Flags{Mutating: true}, cutNames)
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.switches.find", Offering{ID: "call-switches", Group: "understand", Title: "Picture switches in calls",
		Does:  "Finds where a call recording's picture switches, to the frame, and who is on screen in each shot.",
		Words: []string{"camera switch", "speaker view changes"}, Needs: Needs{Works: []string{"editing"}}})
	Offer("cut.names", Offering{ID: "cover-call-names", Group: "edit", Title: "Cover printed names",
		Does:  "Covers the names a call app printed in the picture with your own name bar for the whole video.",
		Words: []string{"name bar", "lower third over call names"}, Needs: Needs{Works: []string{"editing"}}})
	Offer("cut.flashes", Offering{ID: "trim-flashes", Group: "edit", Title: "Trim flashes",
		Does:  "Trims split-second flashes of another picture at the edges of the cut's clips, at the exact switch.",
		Words: []string{"flash frames", "glitch at the cut"}, Needs: Needs{Works: []string{"editing"}}})
}
