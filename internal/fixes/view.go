package fixes

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// View is a source's fixes as people see them (the info card, `asset fixes`).
type View struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	// Picture: a picture or logo (a resource; its fixes make new versions).
	Picture bool `json:"picture,omitempty"`
	// Kind is the kind of source (it decides the fixes offered); KindBy is
	// "person" when someone chose it; Kinds are the kinds it may be.
	Kind      string       `json:"kind"`
	KindWords string       `json:"kindWords"`
	KindBy    string       `json:"kindBy,omitempty"`
	Kinds     []KindChoice `json:"kinds"`
	Fixes     []Row        `json:"fixes"`
	Ticked    []string     `json:"ticked"`
	// State of making the ticked fixes' copy: "", preparing, ready, failed.
	State  string   `json:"state,omitempty"`
	Error  string   `json:"error,omitempty"`
	JobIDs []string `json:"jobIds,omitempty"`
	// Using says which media the source uses now (original | fixed) and in words.
	Using      string    `json:"using"`
	UsingWords string    `json:"usingWords"`
	Copy       *CopyView `json:"copy,omitempty"`
	// Trim is where music starts and stops when "Trim silence at the ends" is ticked.
	Trim      *models.SoundTrim `json:"trim,omitempty"`
	TrimWords string            `json:"trimWords,omitempty"`
	// Cuts whose latest version plays the source, and what each could switch to.
	Cuts     []CutUse   `json:"cuts"`
	MarkedAt *time.Time `json:"markedAt,omitempty"`
	// Measuring says why the marks aren't there yet ("" when they are).
	Measuring string `json:"measuring,omitempty"`
	Summary   string `json:"summary"`
}

// KindChoice is a kind of source a person may choose.
type KindChoice struct {
	Value string `json:"value"`
	Label string `json:"label"`
}

// Row is one fix of the source's kind.
type Row struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Result string `json:"result"`
	Ticked bool   `json:"ticked"`
	// Fits: MetaRush measured that this fix fits; Why says so ("The camera
	// shakes a lot"), Measured the measurement, Pending why it isn't measured yet.
	Fits     bool   `json:"fits"`
	Why      string `json:"why,omitempty"`
	Measured string `json:"measured,omitempty"`
	Pending  string `json:"pending,omitempty"`
	// After is the same measurement on the fixed copy ("shake: 5.5 → 0.2 pixels a frame").
	After string `json:"after,omitempty"`
}

// CopyView is the fixed copy a source uses.
type CopyView struct {
	SHA256   string              `json:"sha256"`
	Fixes    []string            `json:"fixes"`
	Words    string              `json:"words"`
	Measures []models.FixMeasure `json:"measures,omitempty"`
	Notes    []string            `json:"notes,omitempty"`
	MadeOn   string              `json:"madeOn,omitempty"`
	Took     float64             `json:"took,omitempty"`
	Size     int64               `json:"size,omitempty"`
}

// CutUse is a cut whose latest version plays the source.
type CutUse struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Branch      string `json:"branch"`
	CutName     string `json:"cutName"`
	RevisionID  string `json:"revisionId"`
	Number      int    `json:"number"`
	// Plays: original | fixed (the copy the source uses now) | other (an earlier fixed copy).
	Plays      string `json:"plays"`
	PlaysWords string `json:"playsWords"`
	// Offer is the one-click change that would bring the cut in line with the
	// source ("Use the steadier picture in “Main open day video”"), as a new
	// version; Original says it goes back to the original.
	Offer    string `json:"offer,omitempty"`
	Original bool   `json:"original,omitempty"`
}

// view builds a clip's fixes view.
func view(ctx context.Context, a *app.App, id string) (*View, error) {
	if strings.HasPrefix(id, "res_") {
		return pictureView(ctx, a, id)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that clip.").WithDetails(map[string]any{"id": id})
	}
	fx := models.SourceFixes{}
	if as.Fixes != nil {
		fx = *as.Fixes
	}
	kind := kindOf(ctx, a, &as)
	v := &View{ID: as.ID, Title: as.DisplayTitle(), Kind: kind, KindWords: models.SourceKindWords(kind), KindBy: fx.KindBy, Ticked: fx.Ticked,
		State: fx.State, Error: fx.Error, Using: "original", Trim: fx.Trim, MarkedAt: fx.MarkedAt, Fixes: []Row{}, Cuts: []CutUse{}}
	if v.Ticked == nil {
		v.Ticked = []string{}
	}
	if as.Media.HasVideo {
		v.Kinds = []KindChoice{{models.SourceCamera, "Camera video"}, {models.SourceCall, "Call or screen recording"}}
	} else {
		v.Kinds = []KindChoice{{models.SourceVoice, "Voice or audio recording"}, {models.SourceMusic, "Music"}}
	}
	if fx.JobID != "" && fx.State == models.FixPreparing {
		v.JobIDs = []string{fx.JobID}
	}
	var cp *models.SourceCopy
	if as.Fixed() {
		v.Using = "fixed"
		var c models.SourceCopy
		if a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), as.Fixes.Copy.ID, &c) == nil {
			cp = &c
			v.Copy = &CopyView{SHA256: c.SHA256, Fixes: stepsOf(&c), Words: ops.CopyWords(&c), Measures: c.Measures, Notes: c.Notes, MadeOn: installationName(ctx, a, c.MadeOn), Took: c.Took, Size: c.Size}
		}
	}
	marks := map[string]models.FixMark{}
	for _, m := range fx.Marks {
		marks[m.Fix] = m
	}
	after := map[string]string{}
	if cp != nil {
		for _, m := range cp.Measures {
			after[m.Fix] = m.Words
		}
	}
	for _, f := range models.FixesFor(kind) {
		r := Row{ID: f.ID, Label: f.Label, Result: f.Result, Ticked: slices.Contains(fx.Ticked, f.ID), After: after[f.ID]}
		if m, ok := marks[f.ID]; ok {
			r.Fits, r.Measured, r.Pending = m.Fits, m.Measured, m.Pending
			if m.Fits {
				r.Why = m.Why
				if r.Why == "" {
					r.Why = f.Mark
				}
			}
		}
		v.Fixes = append(v.Fixes, r)
	}
	if fx.Trim != nil {
		v.TrimWords = trimWords(fx.Trim)
	}
	switch {
	case v.Using == "fixed" && v.Copy != nil:
		v.UsingWords = "Uses its fixed copy: " + v.Copy.Words + ". The original is kept."
	case fx.State == models.FixPreparing:
		v.UsingWords = "Uses the original while the fixed copy is made."
	default:
		v.UsingWords = "Uses the original."
	}
	if fx.MarkedAt == nil {
		v.Measuring = "MetaRush measures it once its first look (the preview copy, the transcript, the scenes) is done."
	}
	// Cuts whose latest version plays it.
	heads, err := ops.HeadsUsing(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	cur := as.MediaSHA()
	for _, h := range heads {
		p := h.Project
		cu := CutUse{ProjectID: p.ID, ProjectName: p.Name, Branch: h.Branch, CutName: ops.CutName(ctx, a, &p, h.Branch), RevisionID: h.Revision.ID, Number: h.Revision.Number}
		switch {
		case h.Fixed == "":
			cu.Plays, cu.PlaysWords = "original", "plays the original"
		case h.Fixed == cur:
			cu.Plays, cu.PlaysWords = "fixed", "plays the fixed copy"
		case cur == "":
			cu.Plays, cu.PlaysWords = "other", "plays a fixed copy"
		default:
			cu.Plays, cu.PlaysWords = "other", "plays an earlier fixed copy"
		}
		switch {
		case cur != "" && h.Fixed != cur && v.Copy != nil:
			cu.Offer = fmt.Sprintf("Use the %s in “%s”", OfferWords(v.Copy.Fixes), cu.CutName)
		case cur == "" && h.Fixed != "" && fx.State != models.FixPreparing:
			cu.Offer, cu.Original = fmt.Sprintf("Use the original in “%s”", cu.CutName), true
		}
		if slices.Contains(fx.Ticked, models.FixEnds) && fx.Trim != nil && endsOffer(&h.Revision, as.ID, fx.Trim) {
			if cu.Offer == "" {
				cu.Offer = fmt.Sprintf("Use the trimmed music in “%s”", cu.CutName)
			}
		}
		v.Cuts = append(v.Cuts, cu)
	}
	v.Summary = summary(v)
	return v, nil
}

// OfferWords names what a fixed copy improves, for its one-click offer:
// "steadier picture", "cleaned-up voices", "fixed copy" for a mix.
func OfferWords(fixes []string) string {
	pictures, sounds := 0, 0
	for _, id := range fixes {
		f, _ := models.Fix(id)
		switch f.Affects {
		case models.AffectsPicture:
			pictures++
		case models.AffectsSound:
			sounds++
		}
	}
	switch {
	case len(fixes) == 1:
		f, _ := models.Fix(fixes[0])
		return f.Result
	case sounds == 0:
		return "fixed picture"
	case pictures == 0:
		return "fixed sound"
	}
	return "fixed copy"
}

// endsOffer: the cut's music tracks of this asset start before its first
// sound or end after its last.
func endsOffer(r *models.Revision, assetID string, tr *models.SoundTrim) bool {
	for _, t := range r.Spec.Audio {
		if t.AssetID == assetID && (t.In < tr.Start-0.05 || t.Out > tr.End+0.05) {
			return true
		}
	}
	return false
}

func trimWords(t *models.SoundTrim) string {
	end := t.Duration - t.End
	switch {
	case t.Start >= 0.05 && end >= 0.05:
		return fmt.Sprintf("Plays from its first sound (%s in) to its last (%s before the end).", secs(t.Start), secs(end))
	case t.Start >= 0.05:
		return fmt.Sprintf("Plays from its first sound, %s in.", secs(t.Start))
	case end >= 0.05:
		return fmt.Sprintf("Stops with its last sound, %s before the end.", secs(end))
	}
	return "It has no silence at the ends."
}

func secs(s float64) string {
	if s < 10 {
		return fmt.Sprintf("%.1f seconds", s)
	}
	return fmt.Sprintf("%.0f seconds", s)
}

func summary(v *View) string {
	fits := 0
	for _, r := range v.Fixes {
		if r.Fits && !r.Ticked {
			fits++
		}
	}
	switch {
	case v.State == models.FixPreparing:
		return "Making the fixed copy: " + strings.ToLower(Doing(v.Ticked)) + "."
	case v.State == models.FixFailed:
		return "The fixed copy couldn't be made. " + v.Error
	case v.Using == "fixed" && v.Copy != nil:
		return "Uses its fixed copy: " + v.Copy.Words + "."
	case fits == 1:
		return "One fix fits it."
	case fits > 1:
		return fmt.Sprintf("%d fixes fit it.", fits)
	case v.Measuring != "":
		return v.Measuring
	}
	return "Nothing needs fixing."
}

func installationName(ctx context.Context, a *app.App, id string) string {
	if id == "" {
		return ""
	}
	var inst models.Installation
	if a.DB.GetByID(ctx, store.CInstallations, a.WorkspaceID(), id, &inst) == nil {
		return inst.Name
	}
	return ""
}
