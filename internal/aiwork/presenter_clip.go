package aiwork

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// A presenter clip (D273): a presenter — a person's own, approved, or a
// service's stock presenter whose licence covers commercial and client work
// (D275) — reads MetaRush's own sound: a clip (AI speech in the person's
// voice copy, a recording of them) or a narration line of a cut. The sound
// sent is kept as AAC; when the clip comes back its picture is kept and the
// sound under it is MetaRush's own, at full quality.

// StockPrefix marks a stock presenter in MakeIn.Presenter ("stock:<id>").
const StockPrefix = "stock:"

var colourRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// GreenScreen is the colour a "green" background means (for keying out later).
const GreenScreen = "#00b140"

// planPresenter works out a presenter clip; test allows a presenter not yet
// approved (its test clips, people only).
func planPresenter(ctx context.Context, a *app.App, in *MakeIn, test bool) (*plan, error) {
	k, _ := aiservices.KindOf(aiservices.KindPresenter)
	p := &plan{kind: k, projectID: in.ProjectID, now: nowFn()}
	// The sound it reads.
	var as *models.Asset
	var from, to float64
	voice := ""
	var err error
	switch {
	case in.Line > 0:
		if in.Clip != "" {
			return nil, apperr.New(apperr.InvalidInput, "Choose a clip or a narration line to read, not both.")
		}
		as, from, to, voice, err = narrationLine(ctx, a, in.ProjectID, in.Branch, in.Line)
	default:
		as, from, to, err = clipPart(ctx, a, in.Clip, in.From, in.To)
	}
	if err != nil {
		return nil, err
	}
	if p.projectID == "" && len(as.ProjectIDs) > 0 {
		p.projectID = as.ProjectIDs[0]
	}
	// Who reads it.
	ref := strings.TrimSpace(in.Presenter)
	if ref == "" {
		ref = presenterOfDub(ctx, a, as)
	}
	if ref == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose who reads it: a presenter.").WithCommand("metarush presenter list")
	}
	var pr *models.Presenter
	stockID := ""
	if s, ok := strings.CutPrefix(ref, StockPrefix); ok {
		stockID = strings.TrimSpace(s)
		if stockID == "" {
			return nil, apperr.New(apperr.InvalidInput, "Say which of the service's stock presenters reads it.").WithCommand("metarush presenter stock")
		}
	} else if pr, err = findPresenter(ctx, a, ref); err != nil {
		return nil, err
	}
	asked := in.Model
	if pr != nil {
		if asked != "" {
			if m, err := aiservices.FindModel(aiservices.KindPresenter, asked); err != nil || m.Provider != pr.Provider {
				return nil, apperr.New(apperr.InvalidInput, "“%s” is a presenter at %s: it reads only there.", pr.Title, pr.ProviderTitle)
			}
		} else {
			m, err := aiservices.ModelFor(pr.Provider, aiservices.KindPresenter, "")
			if err != nil {
				return nil, err
			}
			asked = m.Ref() // the service keeps the presenter: it reads only there
		}
	}
	if err := p.base(ctx, a, asked); err != nil {
		return nil, err
	}
	_, terms, err := presenterTerms(p.model.Provider)
	if err != nil {
		return nil, err
	}
	o := models.GenOptions{AspectRatio: in.Options.AspectRatio}
	bg := strings.TrimSpace(in.Options.Background)
	if strings.EqualFold(bg, "green") {
		bg = GreenScreen
	}
	if bg != "" && !colourRe.MatchString(bg) {
		return nil, apperr.New(apperr.InvalidInput, "Say the colour behind the presenter as #rrggbb, or green.")
	}
	if pr != nil {
		switch {
		case pr.State == models.PresenterRetired:
			return nil, apperr.New(apperr.ConsentRequired, "“%s” was retired, so nothing more is made with it.", pr.Title)
		case !pr.Usable(test):
			msg := fmt.Sprintf("“%s” isn't approved yet: %s", pr.Title, lowerFirst(stateWords(pr)))
			return nil, apperr.New(apperr.Conflict, "%s", msg).WithCommand("metarush presenter show " + pr.Name)
		}
		var look models.GenLook
		if err := a.DB.GetByID(ctx, store.CLooks, a.WorkspaceID(), pr.LookID, &look); err != nil || look.State == models.LookRevoked {
			p.consent = append(p.consent, fmt.Sprintf("The look “%s” behind “%s” was withdrawn, so it doesn't read lines any more.", pr.LookName, pr.Title))
		}
		if c, why := presenterConsent(ctx, a, pr, pr.ConsentID, p.projectID, p.now); c != nil {
			p.addConsent(c)
		} else {
			p.consent = append(p.consent, why)
		}
		o.Presenter = pr.ProviderID
		if bg != "" && !pr.GreenScreen {
			return nil, apperr.New(apperr.InvalidInput, "A colour behind “%s” needs its training take filmed against a green screen; it wasn't.", pr.Title)
		}
		p.notes = append(p.notes, fmt.Sprintf("Read by “%s”, %s's presenter at %s.", pr.Title, pr.PersonName, pr.ProviderTitle))
	} else {
		if !terms.Stock.Offered() {
			return nil, apperr.New(apperr.Conflict, "%s's terms don't say its stock presenters may be used commercially and for clients, so MetaRush doesn't offer them.", p.model.ProviderTitle).
				WithDetails(map[string]any{"reason": "stock_licence", "provider": p.model.Provider})
		}
		o.Presenter, o.Stock = stockID, true
		p.notes = append(p.notes, fmt.Sprintf("Read by %s's stock presenter %s.", p.model.ProviderTitle, stockID), "The stock presenter's licence: "+terms.Stock.Summary)
		if w := terms.Stock.AdsWords(p.model.ProviderTitle); w != "" {
			p.notes = append(p.notes, w)
		}
	}
	o.Background = bg
	// Whose voice it carries; a person's face or voice goes to a service that
	// trains on what it is sent only as trainingGate says (finish; D276).
	p.presenterVoice(ctx, a, as, from, to, pr, voice)
	p.inputs = append(p.inputs, input{role: aiservices.RoleAudio, assetID: as.ID, title: as.DisplayTitle(), from: from, to: to, rate: 44100, format: "m4a"})
	p.touch(as)
	p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleAudio, MIME: "audio/mp4", Seconds: to - from})
	p.req.Options = o
	p.sources = append(p.sources, as.ID)
	p.words = append(p.words, "the sound of "+partWords(as, from, to))
	if err := generate.RulesAllow(ctx, a, p.model.Provider, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := kindsAllow(ctx, a, aiservices.KindPresenter, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := p.mayLeave(ctx, a); err != nil { // What may leave (D290): the sound, and a person's face
		return nil, err
	}
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	p.alts = nil // a presenter reads only at the service that keeps it
	return p, nil
}

// presenterOfDub is the presenter of the clip a dub was made from, so a
// dubbed presenter clip is read again by the same presenter ("" none).
func presenterOfDub(ctx context.Context, a *app.App, as *models.Asset) string {
	src := as.Source
	if src == nil || src.Generated == nil || src.Generated.Kind != aiservices.KindDubbing || src.Generated.GenerationID == "" {
		return ""
	}
	g, err := loadGen(ctx, a, src.Generated.GenerationID)
	if err != nil {
		return ""
	}
	for _, id := range g.SourceAssets {
		var orig models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &orig) != nil || orig.Source == nil || orig.Source.Generated == nil {
			continue
		}
		if og := orig.Source.Generated; og.Kind == aiservices.KindPresenter && og.Presenter != "" {
			return og.Presenter
		}
	}
	return ""
}

// narrationLine is the sound of a narration line of a project's cut (1 is
// the first): its chosen take, and the voice that said it when it is AI speech.
func narrationLine(ctx context.Context, a *app.App, projectID, branch string, n int) (*models.Asset, float64, float64, string, error) {
	if projectID == "" {
		return nil, 0, 0, "", apperr.New(apperr.InvalidInput, "Say which project's narration line to read.")
	}
	rev, err := latestCut(ctx, a, projectID, branch)
	if err != nil {
		return nil, 0, 0, "", err
	}
	if rev == nil || rev.Spec.Voiceover == nil || len(rev.Spec.Voiceover.Beats) == 0 {
		return nil, 0, 0, "", apperr.New(apperr.NotFound, "The cut has no narration lines yet.")
	}
	beats := rev.Spec.Voiceover.Beats
	if n > len(beats) {
		return nil, 0, 0, "", apperr.New(apperr.InvalidInput, "The cut's narration has %d lines.", len(beats))
	}
	b := beats[n-1]
	if b.Take == nil || b.Take.AssetID == "" {
		return nil, 0, 0, "", apperr.New(apperr.Conflict, "Line %d has no sound yet: make its AI voice or record it first.", n)
	}
	from, to := b.Take.In, b.Take.Out
	as, from, to, err := clipPart(ctx, a, b.Take.AssetID, &from, &to)
	if err != nil {
		return nil, 0, 0, "", err
	}
	voice := ""
	if b.Take.TakeID != "" {
		var tk models.VoiceTake
		if a.DB.GetByID(ctx, store.CVoiceTakes, a.WorkspaceID(), b.Take.TakeID, &tk) == nil && tk.AIVoice {
			voice = tk.VoiceID
		}
	}
	return as, from, to, voice, nil
}

// latestCut is the newest version of a project's cut (nil: none yet).
func latestCut(ctx context.Context, a *app.App, projectID, branch string) (*models.Revision, error) {
	if branch == "" {
		branch = "main"
	}
	var pj models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &pj); err != nil {
		return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.")
	}
	h := pj.Branches[branch].Head
	if h == "" {
		return nil, nil
	}
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

// withOwnSound puts MetaRush's own sound — what was sent — under the
// picture the service made (the service may have resampled it).
func (r *runner) withOwnSound(ctx context.Context, video string) (string, error) {
	var ref *models.GenReference
	for i := range r.g.References {
		if r.g.References[i].Role == aiservices.RoleAudio {
			ref = &r.g.References[i]
		}
	}
	if ref == nil {
		return video, nil
	}
	sound, err := generate.Local(ctx, r.a, ref.StorageKey, ref.SHA256, ref.Size)
	if err != nil {
		return video, nil // the service's own sound stays
	}
	out := strings.TrimSuffix(video, ".mp4") + "-own.mp4"
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-i", video, "-i", sound, "-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy",
		"-c:a", "copy", "-shortest", "-movflags", "+faststart", out}
	if _, err := r.a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return video, nil
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return video, nil
	}
	return out, nil
}
