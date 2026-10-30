package generate

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Provider is the generative-video provider MetaRush uses (Google Gemini;
// more providers plug in through internal/videogen).
var Provider = videogen.GeminiName

// settings are the choices in force for a request: the team's (workspace
// providers.gemini), a project's, and the catalog's defaults under both.
type settings struct {
	info                 *videogen.ProviderInfo
	model                string
	draft, final         string
	seconds              int
	aspect               string
	dailyCap             float64
	dailyCapDefault      bool
	projectBudget        *float64 // nil: no limit
	projectBudgetDefault bool
	project              *models.Project
	// Who set the daily limit and the project's budget (D241; nil: not recorded).
	dailyCapBy, projectBudgetBy *models.SetBy
}

func loadSettings(ctx context.Context, a *app.App, projectID string) (*settings, error) {
	info, err := videogen.Info(Provider)
	if err != nil {
		return nil, err
	}
	d := info.Defaults
	st := &settings{info: info, model: d.Model, draft: d.DraftResolution, final: d.FinalResolution, seconds: d.Seconds, aspect: d.AspectRatio,
		dailyCap: d.DailyCapUSD, dailyCapDefault: true}
	var w models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &w); err == nil {
		if s, ok := w.Providers[Provider]; ok {
			if s.Model != "" {
				st.model = s.Model
			}
			if s.DraftResolution != "" {
				st.draft = s.DraftResolution
			}
			if s.FinalResolution != "" {
				st.final = s.FinalResolution
			}
			if s.DailyCapUSD != nil && *s.DailyCapUSD > 0 {
				st.dailyCap, st.dailyCapDefault, st.dailyCapBy = *s.DailyCapUSD, false, s.DailyCapBy
			}
		}
	}
	if projectID != "" {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
			return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": projectID})
		}
		st.project = &p
		b := d.ProjectBudgetUSD
		st.projectBudget, st.projectBudgetDefault = &b, true
		if s, ok := p.Providers[Provider]; ok && s.BudgetUSD != nil {
			st.projectBudgetDefault, st.projectBudgetBy = false, s.BudgetBy
			if *s.BudgetUSD > 0 {
				v := *s.BudgetUSD
				st.projectBudget = &v
			} else {
				st.projectBudget = nil // no limit for this project; the daily cap still applies
			}
		}
	}
	return st, nil
}

func keyReady(a *app.App, provider string) bool {
	k, _, err := providers.Key(a.Cfg, provider)
	return err == nil && k != ""
}

func modelViews(st *settings, now time.Time) []GenModelView {
	var out []GenModelView
	for _, m := range st.info.Models {
		v := GenModelView{ID: m.ID, Title: m.Title, Stage: m.Stage, Describe: m.Describe(), Default: m.ID == st.model, MinSeconds: m.MinSeconds, MaxSeconds: m.MaxSeconds,
			Resolutions: m.Resolutions, Upscaled: m.Upscaled, AspectRatios: m.AspectRatios, PerSecondUSD: map[string]float64{}, RetiredOn: m.RetiredOn, Watermark: m.Watermark}
		if pt, err := videogen.PriceAt(Provider, m.ID, now); err == nil {
			v.PriceTable = pt.Effective
			for res, tps := range pt.VideoTokensPerSecond {
				v.PerSecondUSD[res] = videogen.RoundUSD(float64(tps) * pt.VideoOutputPerMillion / 1e6)
			}
		}
		out = append(out, v)
	}
	return out
}

func settingsOut(ctx context.Context, a *app.App, projectID string) (*GenSettingsOut, error) {
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	now, err := Now(ctx, a) // today's spending is the server's day's (D360)
	if err != nil {
		return nil, err
	}
	out := &GenSettingsOut{Provider: Provider, ProviderTitle: st.info.Title, KeyReady: keyReady(a, Provider), Models: modelViews(st, now), Model: st.model,
		DraftResolution: st.draft, FinalResolution: st.final, Seconds: st.seconds, AspectRatio: st.aspect, DailyCapUSD: st.dailyCap, DailyCapDefault: st.dailyCapDefault,
		ProjectBudgetUSD: st.projectBudget, ProjectBudgetDefault: st.projectBudgetDefault, Budget: budgetView(ctx, a, st, projectID, now),
		DailyCapBy: st.dailyCapBy, ProjectBudgetBy: st.projectBudgetBy,
		SpendLimitsPer10Minutes: st.info.SpendLimitsPer10Minutes}
	parts := []string{fmt.Sprintf("Today: %s of %s spent", money(out.Budget.Today.SpentUSD), money(st.dailyCap))}
	if p := out.Budget.Project; p != nil {
		if p.LimitUSD != nil {
			parts = append(parts, fmt.Sprintf("this project: %s of %s", money(p.SpentUSD), money(*p.LimitUSD)))
		} else {
			parts = append(parts, fmt.Sprintf("this project: %s spent, no project limit", money(p.SpentUSD)))
		}
	}
	out.Summary = strings.Join(parts, "; ") + "."
	if !out.KeyReady {
		out.Summary = st.info.Title + " isn't set up on this computer yet. " + out.Summary
	}
	return out, nil
}

func configure(ctx context.Context, a *app.App, in *GenConfigureIn) (*GenSettingsOut, error) {
	info, err := videogen.Info(Provider)
	if err != nil {
		return nil, err
	}
	if in.Model != "" || in.DraftResolution != "" || in.FinalResolution != "" || in.DailyCapUSD != nil {
		// The team's model, quality and daily limit: owners (D191); a
		// project's budget stays open to everyone.
		if err := ops.RequireOwner(ctx, a, "generate.configure"); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	prefix := "providers." + Provider + "."
	ws, prj := bson.M{}, bson.M{}
	unsetPrj := bson.M{}
	if in.Model != "" {
		m, err := videogen.ModelInfo(Provider, in.Model)
		if err != nil {
			return nil, err
		}
		if m.Retired(now) {
			return nil, apperr.New(apperr.InvalidInput, "%s was retired on %s.", m.Title, m.RetiredOn)
		}
		ws[prefix+"model"] = m.ID
	}
	if in.DraftResolution != "" {
		ws[prefix+"draftResolution"] = in.DraftResolution
	}
	if in.FinalResolution != "" {
		ws[prefix+"finalResolution"] = in.FinalResolution
	}
	if in.DailyCapUSD != nil {
		if *in.DailyCapUSD < 1 || *in.DailyCapUSD > 1000 {
			return nil, apperr.New(apperr.InvalidInput, "The daily limit must be between $1 and $1,000.")
		}
		ws[prefix+"dailyCapUsd"] = roundCents(*in.DailyCapUSD)
		ws[prefix+"dailyCapBy"] = models.SetNow(a.Actor) // who set it (D241)
	}
	if in.BudgetUSD != nil || in.ClearBudget {
		if in.ProjectID == "" {
			return nil, apperr.New(apperr.InvalidInput, "Budgets are set for each project. Choose the project.")
		}
		switch {
		case in.ClearBudget:
			unsetPrj[prefix+"budgetUsd"], unsetPrj[prefix+"budgetBy"] = "", ""
		case *in.BudgetUSD < 0:
			return nil, apperr.New(apperr.InvalidInput, "A budget can't be less than nothing.")
		default:
			prj[prefix+"budgetUsd"] = roundCents(*in.BudgetUSD)
			prj[prefix+"budgetBy"] = models.SetNow(a.Actor) // who set it (D241)
		}
	}
	if len(ws) == 0 && len(prj) == 0 && len(unsetPrj) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say what to change: the daily limit, a project's budget, the model or the draft and final quality.")
	}
	actor := a.Actor
	if len(ws) > 0 {
		ws[prefix+"updatedAt"], ws[prefix+"updatedBy"] = now, actor
		if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": ws}); err != nil {
			return nil, apperr.New(apperr.NotFound, "MetaRush couldn't find this workspace's settings.")
		}
	}
	if len(prj) > 0 || len(unsetPrj) > 0 {
		upd := bson.M{"$set": bson.M{prefix + "updatedAt": now, prefix + "updatedBy": actor}}
		for k, v := range prj {
			upd["$set"].(bson.M)[k] = v
		}
		if len(unsetPrj) > 0 {
			upd["$unset"] = unsetPrj
		}
		if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, upd); err != nil {
			return nil, apperr.New(apperr.NotFound, "That project isn't in this workspace.").WithDetails(map[string]any{"projectId": in.ProjectID})
		}
	}
	var fields []string
	for k := range ws {
		if !strings.HasSuffix(k, "updatedAt") && !strings.HasSuffix(k, "updatedBy") {
			fields = append(fields, strings.TrimPrefix(k, prefix))
		}
	}
	for k := range prj {
		fields = append(fields, strings.TrimPrefix(k, prefix))
	}
	for k := range unsetPrj {
		fields = append(fields, "-"+strings.TrimPrefix(k, prefix))
	}
	slices.Sort(fields)
	a.Audit(ctx, "generate.configure", "provider", Provider, in.ProjectID, map[string]any{"provider": Provider, "fields": fields, "dailyCapUsd": in.DailyCapUSD, "budgetUsd": in.BudgetUSD})
	_ = info
	return settingsOut(ctx, a, in.ProjectID)
}

func roundCents(v float64) float64 { return float64(int64(v*100+0.5)) / 100 }
