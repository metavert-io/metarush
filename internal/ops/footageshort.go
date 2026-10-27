package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/textalign"
)

// ShortPiece is a stretch of footage a short plays (seconds into the clip).
type ShortPiece struct {
	AssetID string  `json:"assetId" jsonschema:"description=The clip (asset ID)."`
	From    float64 `json:"from" jsonschema:"minimum=0,description=Seconds into the clip."`
	To      float64 `json:"to" jsonschema:"minimum=0,description=Seconds into the clip (after From)."`
}

// FootageShortIn makes a short from any footage (D246): one or more
// stretches a person or Claude picked (footage.moments lists them), as a new
// cut of the project for phones (tall), framed as the house style says
// (following the subject only when a person asks, D115).
type FootageShortIn struct {
	ProjectID string       `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Pieces    []ShortPiece `json:"pieces" jsonschema:"minItems=1,maxItems=12,description=The stretches it plays\\, in order."`
	Name      string       `json:"name,omitempty" jsonschema:"maxLength=120,description=The short's name (default: from its first words)."`
	Shape     string       `json:"shape,omitempty" jsonschema:"enum=,enum=vertical,enum=square,description=vertical (for phones\\, tall; the default) or square."`
	Framing   string       `json:"framing,omitempty" jsonschema:"enum=,enum=follow,enum=faces,enum=center,description=How the crop is framed: follow the subject as it moves (only when a person asks)\\, keep faces in frame\\, or keep the middle. Empty: the house style's (else faces)."`
	// Look is the captions' look (default: the house style's, else big and bold).
	Look string `json:"look,omitempty" jsonschema:"enum=,enum=clean,enum=emphasis,enum=boxed,enum=bold,enum=highlight,enum=subtitle"`
	// Captions off (false); default on.
	Captions *bool `json:"captions,omitempty"`
	// Branch is the new cut's short name (default: the first free short-N).
	Branch string `json:"branch,omitempty" jsonschema:"pattern=^$|^[a-z][a-z0-9-]*$,maxLength=40"`
	// Why is why this moment makes a short (kept as the version's note).
	Why string `json:"why,omitempty" jsonschema:"maxLength=2000"`
	// Origin is written onto the version (what made it, when another feature does).
	Origin       string `json:"origin,omitempty" jsonschema:"maxLength=200"`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the short in plain words\\, warnings and a confirm token."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=The token from a dry run of exactly this short."`
}

// FootageShortOut is the short, checked (dry run) or made.
type FootageShortOut struct {
	ProjectID string  `json:"projectId"`
	Branch    string  `json:"branch"`
	Name      string  `json:"name"`
	Seconds   float64 `json:"seconds"`
	Summary   string  `json:"summary" jsonschema:"description=The short in one plain sentence."`
	// Details are plain lines: what it plays, its shape, framing and captions.
	Details []string        `json:"details"`
	Saved   bool            `json:"saved"`
	Plan    json.RawMessage `json:"plan" jsonschema:"type=object,description=The edit plan: the short's whole first version."`
	Check   *EditCheckOut   `json:"check,omitempty"`
	Applied *EditApplyOut   `json:"applied,omitempty"`
	// Link opens the short's version.
	Link string `json:"link,omitempty"`
}

var shortBranchName = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// ParseShortPiece reads a stretch as people type it: "<clip>@<from>-<to>",
// times as 12.5, 0:12.5 or 1:02:03 (`metarush footage short --piece`).
func ParseShortPiece(s string) (ShortPiece, error) {
	asset, span, ok := strings.Cut(strings.TrimSpace(s), "@")
	from, to, ok2 := strings.Cut(span, "-")
	f, okF := editspec.ParseClock(from)
	t, okT := editspec.ParseClock(to)
	if !ok || !ok2 || asset == "" || !okF || !okT {
		return ShortPiece{}, fmt.Errorf("a stretch is <clip>@<from>-<to>, like ast_…@0:12-0:41, not %q", s)
	}
	return ShortPiece{AssetID: asset, From: f, To: t}, nil
}

// ShortMaxSeconds is the longest short (places take up to three minutes).
const ShortMaxSeconds = 180

func shortChange(ctx context.Context, a *app.App, in *FootageShortIn) (*FootageShortOut, error) {
	if len(in.Pieces) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say which stretch of footage the short plays.").WithHint("The project's moments say what is said where, with times.").WithCommand("metarush footage moments <project>")
	}
	if len(in.Pieces) > 12 {
		return nil, apperr.New(apperr.InvalidInput, "A short plays at most 12 stretches.")
	}
	shape := strings.ToLower(strings.TrimSpace(in.Shape))
	preset := editspec.PresetVertical
	switch shape {
	case "", "vertical", "tall", "phone":
		shape = "vertical"
	case "square":
		preset = editspec.PresetSquare
	default:
		return nil, apperr.New(apperr.InvalidInput, "A short is vertical (for phones, tall) or square, not %q.", in.Shape)
	}
	// Framing nobody asked for is the house style's, as for any new cut
	// (NewCutDefaults); following movement only when a person asks (D115).
	framing := strings.ToLower(strings.TrimSpace(in.Framing))
	switch framing {
	case "", editspec.FramingFollow, editspec.FramingFaces, editspec.FramingCenter:
	default:
		return nil, apperr.New(apperr.InvalidInput, "Framing is follow, faces or center, not %q.", in.Framing)
	}
	look := ""
	if in.Look != "" {
		id, ok := lookID(in.Look)
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "There's no caption look %q.", in.Look).WithHint("The looks are clean, emphasis, boxed, bold, highlight and subtitle.").WithCommand("metarush captions looks")
		}
		look = id
	}
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	branch := in.Branch
	switch {
	case branch == "":
		for n := 1; ; n++ {
			b := "short"
			if n > 1 {
				b = fmt.Sprintf("short-%d", n)
			}
			if _, taken := p.Branches[b]; !taken {
				branch = b
				break
			}
		}
	case !shortBranchName.MatchString(branch) || len(branch) > 40:
		return nil, apperr.New(apperr.InvalidInput, "A cut's short name is lowercase letters, digits and dashes, starting with a letter: not %q.", branch)
	default:
		if _, taken := p.Branches[branch]; taken {
			return nil, apperr.New(apperr.Conflict, "%s already has a cut called %q.", p.Name, branch).WithHint("Leave the name out to get a free one.")
		}
	}
	fps := 30
	if h := p.Branches["main"].Head; h != "" {
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h, &r) == nil && r.Spec.Output.FPS > 0 {
			fps = r.Spec.Output.FPS
		}
	}
	out := &FootageShortOut{ProjectID: p.ID, Branch: branch}
	house, _, _ := content.StyleFor(ctx, a, p.ID)
	shown := framing // what the crop will do: the house style's when nobody asked
	if shown == "" {
		shown = editspec.FramingFaces
		if house != nil && house.Framing != "" {
			shown = house.Framing
		}
	}
	spec := editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: preset, FPS: fps, Platform: "shorts", Framing: framing}}
	if shape == "square" {
		spec.Output.Platform = "generic"
	}
	var titles []string
	var firstWords string
	for i, pc := range in.Pieces {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), pc.AssetID, &as); err != nil {
			return nil, notFound("asset", pc.AssetID)
		}
		if !as.Media.HasVideo {
			return nil, apperr.New(apperr.InvalidInput, "“%s” has no picture to make a short from.", as.DisplayTitle())
		}
		to := pc.To
		if d := as.Media.Duration; d > 0 && to > d {
			to = d
		}
		if pc.From < 0 || to-pc.From < 1 {
			return nil, apperr.New(apperr.InvalidInput, "Stretch %d of “%s” is %s to %s: give it at least a second, inside the clip.", i+1, as.DisplayTitle(), editspec.Clock(pc.From), editspec.Clock(pc.To))
		}
		out.Seconds += to - pc.From
		clip := editspec.Clip{ID: fmt.Sprintf("m%d", i+1), AssetID: as.ID, In: round3(pc.From), Out: round3(to)}
		if in.Why != "" && i == 0 {
			clip.Rationale = in.Why
		}
		spec.Clips = append(spec.Clips, clip)
		titles = append(titles, fmt.Sprintf("“%s” %s–%s", as.DisplayTitle(), editspec.Clock(pc.From), editspec.Clock(to)))
		if firstWords == "" {
			firstWords = wordsAt(ctx, a, as.ID, pc.From, to, 8)
		}
	}
	out.Seconds = round3(out.Seconds)
	if out.Seconds < 3 {
		return nil, apperr.New(apperr.InvalidInput, "A short needs at least 3 seconds; these stretches make %s.", trimSeconds(out.Seconds))
	}
	if out.Seconds > ShortMaxSeconds {
		return nil, apperr.New(apperr.InvalidInput, "A short is at most 3 minutes; these stretches make %s.", trimSeconds(out.Seconds))
	}
	captions := in.Captions == nil || *in.Captions
	if captions {
		c := &editspec.Captions{Enabled: true, Style: look}
		if look == "" && (house == nil || house.Captions == nil || house.Captions.Look == "") {
			c.Style = editspec.CaptionBold // a short is watched on a phone, often without sound
		}
		spec.Captions = c
	}
	out.Name = strings.TrimSpace(in.Name)
	if out.Name == "" {
		out.Name = "Short"
		if firstWords != "" {
			out.Name = "Short: " + firstWords
		}
	}
	if r := []rune(out.Name); len(r) > 120 {
		out.Name = string(r[:119]) + "…"
	}
	spec.Title = out.Name
	what := "for phones (tall)"
	if shape == "square" {
		what = "square"
	}
	out.Summary = fmt.Sprintf("A %s short %s, a new cut called %q.", trimSeconds(out.Seconds), what, out.Name)
	out.Details = append(out.Details, "Plays "+strings.Join(titles, ", then "))
	out.Details = append(out.Details, map[string]string{editspec.FramingFollow: "The crop follows the subject as it moves",
		editspec.FramingFaces: "The crop keeps faces in frame", editspec.FramingCenter: "The crop keeps the middle of the picture"}[shown])
	if captions {
		if spec.Captions.Style == "" {
			out.Details = append(out.Details, "Captions in the house style's look")
		} else {
			out.Details = append(out.Details, "Captions: “"+editspec.CaptionLook(spec.Captions.Style).Name+"”")
		}
	} else {
		out.Details = append(out.Details, "No captions")
	}
	if out.Seconds > 60 {
		out.Details = append(out.Details, "Longer than a minute: some places take shorter ones")
	}
	origin := in.Origin
	if origin == "" {
		origin = "short:cli"
		if a.Actor.Via == "dashboard" {
			origin = "short:studio"
		}
	}
	plan := editspec.Plan{ProjectID: p.ID, Branch: branch, Instruction: out.Summary, Rationale: in.Why, Origin: origin, Note: clipNote(out.Name), Spec: &spec}
	raw, err := json.Marshal(plan)
	if err != nil {
		return nil, err
	}
	out.Plan = raw
	return out, nil
}

// wordsAt is the first few words said in a stretch of a clip ("" without a transcript).
func wordsAt(ctx context.Context, a *app.App, assetID string, from, to float64, n int) string {
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID, "to": bson.M{"$gt": from}, "from": bson.M{"$lt": to}}, &segs,
		options.Find().SetSort(bson.D{{Key: "idx", Value: 1}})); err != nil {
		return ""
	}
	var words []string
	for _, s := range segs {
		for _, w := range s.Words {
			if w.From >= from-0.05 && w.To <= to+0.05 && len(words) < n {
				words = append(words, strings.TrimSpace(w.Text))
			}
		}
	}
	if len(words) == 0 {
		return ""
	}
	return strings.TrimRight(textalign.JoinWords(words), ",;:、。") + "…"
}

func trimSeconds(s float64) string {
	if s >= 60 {
		m := int(s) / 60
		return fmt.Sprintf("%d:%02d", m, int(math.Round(s))-60*m)
	}
	return fmt.Sprintf("%.0f s", math.Round(s))
}

func init() {
	Def("footage.short", "Make a short from any footage: one or more stretches (footage.moments lists moments) as a new cut for phones (tall), framed as the house style says unless a framing is asked for, captions on; dryRun checks it and gives a confirm token; a person approves it like any cut", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *FootageShortIn) (*FootageShortOut, error) {
		out, err := shortChange(ctx, a, in)
		if err != nil {
			return nil, err
		}
		if in.DryRun || (in.ConfirmToken == "" && a.Actor.Via != "dashboard") {
			chk, err := checkPlan(ctx, a, out.Plan, false)
			if err != nil {
				return nil, err
			}
			out.Check = chk
			if chk.Valid {
				out.Check.Next = "Show the person the short; if they agree, make the same short again with its confirm token to save it."
			}
			return out, nil
		}
		res, err := applyPlan(ctx, a, out.Plan, in.ConfirmToken, false)
		if err != nil {
			return nil, err
		}
		out.Applied, out.Saved = res, true
		// The cut is named after the short; a name that can't be kept leaves the cut's short name.
		_, _ = CallTyped[CutView](ctx, a, "cut.rename", CutRenameIn{ProjectID: out.ProjectID, Branch: out.Branch, Title: out.Name}, Caller{})
		out.Link = links.Revision(out.ProjectID, res.Revision.ID)
		return out, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.short", Offering{ID: "shorts", Group: "edit", Title: "Shorts",
		Does:  "Makes a short for phones from a stretch of any footage, framed and captioned in the house style.",
		Words: []string{"vertical video", "reels", "tiktok", "clip for social"}, Needs: Needs{Works: []string{"editing"}}})
}
