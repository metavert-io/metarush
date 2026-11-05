package ops

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Clips' cut-outs as cuts read them (D510): the records a spec pins by their
// files' SHA-256, for the check, the plan, previews and renders. The
// operations on cut-outs themselves are internal/mattes' (which this package
// can't import).

// LoadMattes reads the cut-outs whose files have these SHA-256s (a ready one
// first when two records share a file).
func LoadMattes(ctx context.Context, a *app.App, shas []string) (map[string]*models.Matte, error) {
	out := map[string]*models.Matte{}
	if len(shas) == 0 {
		return out, nil
	}
	var docs []models.Matte
	if err := a.DB.Find(ctx, store.CMattes, a.WorkspaceID(), bson.M{"file.sha256": bson.M{"$in": shas}}, &docs); err != nil {
		return nil, err
	}
	for i := range docs {
		m := &docs[i]
		if m.File == nil {
			continue
		}
		if prev, ok := out[m.File.SHA256]; ok && prev.State == models.MatteReady {
			continue
		}
		out[m.File.SHA256] = m
	}
	return out, nil
}

// MatteInfoOf is what the check and the plan need of a cut-out: its clip,
// the media it follows, the stretch it covers and where its first frame
// shows in the clip (its From: the exact time of that frame once made).
func MatteInfoOf(m *models.Matte) editspec.MatteInfo {
	return editspec.MatteInfo{AssetID: m.AssetID, Fixed: m.Fixed, From: m.From, To: m.To, Offset: m.From,
		Ready: m.State == models.MatteReady && m.File != nil, Model: m.ModelTitle}
}

func matteInfos(ms map[string]*models.Matte) map[string]editspec.MatteInfo {
	if len(ms) == 0 {
		return nil
	}
	out := make(map[string]editspec.MatteInfo, len(ms))
	for sha, m := range ms {
		out[sha] = MatteInfoOf(m)
	}
	return out
}

// Matte implements editspec.MatteFacts: a cut-out the spec pins.
func (f *dbFacts) Matte(sha string) (editspec.MatteInfo, bool) {
	m, ok := f.mattes[sha]
	return m, ok
}

// CutOutURL is where the dashboard's preview fetches a cut-out: same-origin
// and local-first (api.cutOutMedia), never a redirect.
func CutOutURL(sha string) string { return "/media/cutouts/" + sha }

// fillCutOuts gives every cut-out a plan plays (nested cuts' too) the
// preview's address; one MetaRush doesn't have gets none, so the preview
// plays its clip as recorded (a render fails for it).
func fillCutOuts(plan *editspec.RenderPlan) {
	missing := map[string]bool{}
	for _, sha := range editspec.PlanMissingCutOuts(plan) {
		missing[sha] = true
	}
	for _, sha := range editspec.PlanCutOuts(plan) {
		if missing[sha] {
			continue
		}
		if plan.CutOutSources == nil {
			plan.CutOutSources = map[string]string{}
		}
		plan.CutOutSources[sha] = CutOutURL(sha)
	}
}

// CutEditTarget is the latest version of a cut, for an editing verb that
// lives outside this package (a clip's background, D510): it finds clips and
// names them, and its Finish checks or saves the verb's ops with the same
// plain words, confirm token and locks as the verbs here.
type CutEditTarget struct{ t *editTarget }

// LoadCutEditTarget reads the latest version a verb changes.
func LoadCutEditTarget(ctx context.Context, a *app.App, c CutEditCommon) (*CutEditTarget, error) {
	t, err := loadEditTarget(ctx, a, c)
	if err != nil {
		return nil, err
	}
	return &CutEditTarget{t: t}, nil
}

// Project is the cut's project.
func (e *CutEditTarget) Project() *models.Project { return e.t.project }

// Branch is the cut (main, short-2…).
func (e *CutEditTarget) Branch() string { return e.t.branch }

// Revision is the latest version.
func (e *CutEditTarget) Revision() *models.Revision { return e.t.rev }

// Spec is the latest version's spec (never changed in place).
func (e *CutEditTarget) Spec() *editspec.Spec { return e.t.spec }

// Clip finds a clip by its name or number (1 = the first).
func (e *CutEditTarget) Clip(ref string) (int, error) { return e.t.clip(ref) }

// Who names a clip in words.
func (e *CutEditTarget) Who(i int) string { return e.t.who(i) }

// Finish checks the verb's ops (a dry run, or a terminal without its confirm
// token) or saves them as a new version.
func (e *CutEditTarget) Finish(ctx context.Context, a *app.App, c CutEditCommon, verb string, ops []editspec.Op, summary, instruction string) (*CutEditOut, error) {
	return e.t.finish(ctx, a, c, verb, ops, summary, instruction, &CutEditOut{})
}
