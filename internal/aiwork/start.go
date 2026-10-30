package aiwork

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// start turns a plan into a generation record and its job: the price must
// have been agreed, consent must be there, the files sent are kept as exact
// bytes, the money is set aside, and the job is queued on this machine (the
// one with the key).
func start(ctx context.Context, a *app.App, p *plan, maxUSD *float64, dry bool, origin string) (*MakeOut, error) {
	est := p.estimate(ctx, a)
	out := &MakeOut{Estimate: est, DryRun: dry}
	if dry {
		out.Summary = est.Summary
		return out, nil
	}
	if p.adapter == nil {
		return nil, apperr.New(apperr.NotConfigured, "%s", p.problems[0])
	}
	// The keys and models checked here are this computer's (D349).
	if err := a.NeedsThisComputer(ctx, "AI work"); err != nil {
		return nil, err
	}
	if _, err := keyFor(a, p.model.Provider); err != nil {
		return nil, err
	}
	if err := readyHere(a, p.model.Provider, p.kind.Kind, p.model.ID); err != nil {
		return nil, err // a model on this computer that isn't set up here (D278)
	}
	if len(p.consent) > 0 {
		return nil, apperr.New(apperr.ConsentRequired, "%s", p.consent[0]).WithDetails(map[string]any{"problems": p.consent, "people": p.people})
	}
	if len(p.problems) > 0 {
		return nil, apperr.New(apperr.Conflict, "%s", p.problems[0]).WithDetails(map[string]any{"problems": p.problems})
	}
	// The estimate step (D208): a person who turned estimates off agrees to
	// the estimate by asking; budgets and limits below still decide.
	estimateFirst := true
	if maxUSD == nil && p.provider.Route != aiservices.RouteDirect && est.USD == 0 {
		// A model on this computer, or an ability that says it is free, costs
		// nothing: there is no price to agree (D278, D281).
		v := 0.0
		maxUSD = &v
	}
	// An ability decides who may start it, as for any run of it (D234, D281).
	if po, ok := p.adapter.(aiservices.PersonOnly); ok {
		if why := po.PersonOnly(p.model.ID); why != "" && !ops.CallerFrom(ctx).Human {
			return nil, apperr.New(apperr.HumanOnly, "Only a person asks for this: %s", lowerFirst(why))
		}
	}
	if maxUSD == nil && !prefs.EstimatesFirstForAsker(ctx, a) {
		v := est.USD
		maxUSD, estimateFirst = &v, false
	}
	if maxUSD == nil || est.USD > *maxUSD+0.0005 {
		msg := fmt.Sprintf("This costs about %s. Confirm the price to make it.", costs.Money(est.USD))
		if maxUSD != nil {
			msg = fmt.Sprintf("This costs about %s, more than the %s you agreed to.", costs.Money(est.USD), costs.Money(*maxUSD))
		}
		return nil, apperr.New(apperr.ConfirmRequired, "%s", msg).WithHint("Nothing was sent or spent.").
			WithDetails(map[string]any{"estimateUsd": est.USD, "estimate": est})
	}
	// The exact bytes of what is sent, kept by their content, so the record
	// names them and a resumed job sends the same bytes.
	refs, err := materialize(ctx, a, p)
	if err != nil {
		return nil, err
	}
	for i := range refs {
		p.req.Inputs[i].SHA256, p.req.Inputs[i].MIME, p.req.Inputs[i].Size = refs[i].SHA256, refs[i].MIME, refs[i].Size
	}
	// From here on it is one unit: a browser that goes away must not leave
	// money set aside without a record, or a record without its job.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	// The server's time (D360): the sweeps on every computer compare
	// createdAt, and the day's limit is the server's day.
	now, err := generate.Now(ctx, a)
	if err != nil {
		return nil, err
	}
	m := p.model
	genID := p.genID
	if genID == "" {
		genID = ids.New(ids.Generation)
	}
	g := models.Generation{ID: genID, WorkspaceID: a.WorkspaceID(), ProjectID: p.projectID, Kind: p.kind.Kind, Operation: models.GenMake, Take: 1,
		Provider: m.Provider, ProviderTitle: m.ProviderTitle, Model: m.ID, ModelTitle: m.Title, Stage: m.Stage, PriceTable: p.est.PriceTable,
		Prompt: p.req.Prompt, ModelVersion: aiservices.VersionOf(p.adapter, m.ID), References: refs, Params: models.GenParams{Task: p.req.Task}, RequestHash: p.req.Hash(m.Provider),
		ConsentIDs: p.consentIDs, NoPeople: p.noPeople, SourceAssets: p.sources, CreatedBy: a.Actor, Machine: a.Cfg.Installation.Name,
		Channel: generate.ChannelOf(a.Actor, origin), Origin: keptOrigin(p.feature), JobID: ids.New(ids.Job), CreatedAt: now,
		Estimate: models.GenCost{USD: p.est.USD, InputTokens: p.est.InputTokens, Unit: p.est.Unit, Amount: p.est.Amount, Basis: "estimate"},
		Terms: &models.GenTerms{Licence: m.Licence.Summary, Commercial: m.Licence.Commercial, Broadcast: m.Licence.Broadcast, Data: p.provider.Data.Summary,
			Source: m.Licence.Source, Checked: m.Licence.Checked},
		Choice: &models.GenChoice{By: p.choice.by}}
	if p.req.Options.Stock && p.provider.Presenters != nil {
		// A stock presenter's licence is the service's for its stock presenters (D275).
		st := p.provider.Presenters.Stock
		words := st.Summary
		if w := st.AdsWords(p.provider.Title); w != "" {
			words += " " + w
		}
		g.Terms = &models.GenTerms{Licence: words, Commercial: st.Commercial, Broadcast: "unstated", Data: p.provider.Data.Summary, Source: st.Source, Checked: st.Checked}
	}
	g.GroupID = g.ID
	opts := p.req.Options
	g.Options = &opts
	for _, alt := range p.alts {
		g.Choice.Alternatives = append(g.Choice.Alternatives, alt.Provider+"/"+alt.Model)
	}
	b, err := generate.ReserveWork(ctx, a, p.projectID, g.ID, p.est.USD, now, p.kind.What)
	if err != nil {
		return nil, err
	}
	g.Budget = b
	if err := a.DB.Insert(ctx, store.CGenerations, g); err != nil {
		_ = generate.Settle(context.WithoutCancel(ctx), a, &g, 0, "")
		return nil, err
	}
	_, _, err = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: g.JobID, Type: jobs.TypeGenerate, IdempotencyKey: "generate:" + g.ID, Priority: 9, MaxAttempts: 6,
		ProjectID: p.projectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID,
		Payload: map[string]any{"generationId": g.ID, "kind": g.Kind, "what": p.kind.What, "provider": g.ProviderTitle, "costUsd": g.Estimate.USD}})
	if err != nil {
		_ = generate.SetOnce(context.WithoutCancel(ctx), a, g.ID, "outcome", models.GenOutcome{State: models.GenFailed, At: a.DB.NowOrLocal(ctx), Message: "MetaRush couldn't queue it.", Actual: &models.GenCost{Basis: "none"}})
		_ = generate.Settle(context.WithoutCancel(ctx), a, &g, 0, "")
		return nil, err
	}
	action := "ai.make"
	switch {
	case p.kind.Kind == aiservices.KindVoiceCopy:
		action = "ai.voices.copy"
	case p.kind.Kind == aiservices.KindPresenterMake:
		action = "presenter.make"
	case strings.HasPrefix(origin, "presenter-test:"):
		action = "presenter.test"
	}
	a.Audit(ctx, action, "generation", g.ID, p.projectID, map[string]any{
		"kind": g.Kind, "provider": g.Provider, "model": g.Model, "estimateUsd": est.USD, "choice": p.choice.by, "consents": p.consentIDs, "likeness": p.likeness,
		"noPeople": p.noPeople, "aiGenerated": true, "channel": g.Channel, "estimateFirst": estimateFirst})
	v := views(ctx, a, []models.Generation{g})[0]
	out.Work, out.JobID = &v, g.JobID
	out.Summary = fmt.Sprintf("Asked %s for %s (about %s). %s", m.ProviderTitle, p.kind.What, costs.Money(est.USD), whereItLands(p.kind.Kind))
	if p.provider.Local() {
		out.Summary = fmt.Sprintf("Making %s on this computer with %s (free; nothing leaves it). %s", p.kind.What, m.Title, whereItLands(p.kind.Kind))
	}
	return out, nil
}

// whereItLands says where the result will be.
func whereItLands(kind string) string {
	switch kind {
	case aiservices.KindPicture:
		return "It lands in the resources library, marked as AI-generated."
	case aiservices.KindBackground:
		return "It lands in the resources library as a new picture; the original stays as it is."
	case aiservices.KindUpscale:
		return "It lands in the resources library as a new picture, marked as AI-made; the original stays as it is."
	case aiservices.KindTranscription:
		return "The clip's words are replaced when it's back; fixes people made are carried over."
	case aiservices.KindVoiceCopy:
		return "The voice shows with the narration voices when it's ready."
	case aiservices.KindPresenterMake:
		return "When it's made, test it, then approve it."
	case aiservices.KindPresenter:
		return "It lands in Sources, marked as AI-generated, with MetaRush's own sound under it."
	}
	return "It lands in Sources, marked as AI-generated."
}

// materialize keeps the exact bytes of every file the request sends.
func materialize(ctx context.Context, a *app.App, p *plan) ([]models.GenReference, error) {
	var out []models.GenReference
	for _, in := range p.inputs {
		switch {
		case in.picture != "":
			keep := generate.Picture
			if in.asIs {
				keep = generate.PictureAsIs
			}
			s, res, ref, err := keep(ctx, a, p.projectID, in.picture)
			if err != nil {
				return nil, err
			}
			out = append(out, models.GenReference{Role: in.role, Ref: ref, SHA256: s.SHA256, MIME: s.MIME, Size: s.Size, StorageKey: s.Key, ResourceID: res.ID})
		case in.assetID != "" && in.footage:
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.assetID, &as); err != nil {
				return nil, apperr.New(apperr.NotFound, "That clip isn't in this workspace.")
			}
			s, err := generate.ClipPart(ctx, a, &as, in.from, in.to) // picture and sound, as an MP4 (lip sync)
			if err != nil {
				return nil, err
			}
			out = append(out, models.GenReference{Role: in.role, Ref: fmt.Sprintf("%s@%.2f-%.2f", as.ID, in.from, in.to), SHA256: s.SHA256, MIME: s.MIME, Size: s.Size,
				StorageKey: s.Key, AssetID: as.ID, From: in.from, To: in.to})
		case in.assetID != "":
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.assetID, &as); err != nil {
				return nil, apperr.New(apperr.NotFound, "That clip isn't in this workspace.")
			}
			format := in.format
			if in.wav {
				format = "wav"
			}
			var s *generate.Stored
			var err error
			if in.clip {
				s, err = videoPart(ctx, a, &as, in.from, in.to)
			} else {
				s, err = soundOf(ctx, a, &as, in.from, in.to, in.rate, format)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, models.GenReference{Role: in.role, Ref: fmt.Sprintf("%s@%.2f-%.2f", as.ID, in.from, in.to), SHA256: s.SHA256, MIME: s.MIME, Size: s.Size,
				StorageKey: s.Key, AssetID: as.ID, From: in.from, To: in.to})
		}
	}
	return out, nil
}

// soundOf keeps [from, to) of a clip's sound as mono FLAC (or 16-bit WAV, or
// AAC in an MPEG-4 file) at a rate.
func soundOf(ctx context.Context, a *app.App, as *models.Asset, from, to float64, rate int, format string) (*generate.Stored, error) {
	src, err := generate.Original(ctx, a, as)
	if err != nil {
		return nil, err
	}
	name, codec, mime := "sound.flac", "flac", "audio/flac"
	switch format {
	case "wav":
		name, codec, mime = "sound.wav", "pcm_s16le", "audio/wav"
	case "m4a":
		name, codec, mime = "sound.m4a", "aac", "audio/mp4"
	}
	out, done, err := generate.Scratch(a, name)
	if err != nil {
		return nil, err
	}
	defer done()
	if rate <= 0 {
		rate = 44100
	}
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-ss", ff(from), "-i", src, "-t", ff(to - from), "-vn", "-map", "0:a:0",
		"-ac", "1", "-ar", strconv.Itoa(rate), "-c:a", codec}
	if codec == "aac" {
		args = append(args, "-b:a", "128k", "-movflags", "+faststart")
	}
	args = append(args, out)
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't take the sound of “%s”.", as.DisplayTitle())
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't take the sound of “%s”.", as.DisplayTitle())
	}
	return generate.Keep(ctx, a, out, mime)
}

// videoPart keeps [from, to) of a clip as an MP4 with its sound, at most 1080
// on its short side: a presenter's training take or consent reading (D273).
func videoPart(ctx context.Context, a *app.App, as *models.Asset, from, to float64) (*generate.Stored, error) {
	src, err := generate.Original(ctx, a, as)
	if err != nil {
		return nil, err
	}
	out, done, err := generate.Scratch(a, "take.mp4")
	if err != nil {
		return nil, err
	}
	defer done()
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-ss", ff(from), "-i", src, "-t", ff(to - from), "-map", "0:v:0", "-map", "0:a:0?",
		"-vf", "scale='if(gt(iw,ih),-2,min(1080,iw))':'if(gt(iw,ih),min(1080,ih),-2)',format=yuv420p", "-c:v", "libx264", "-preset", "veryfast", "-crf", "18",
		"-c:a", "aac", "-b:a", "160k", "-movflags", "+faststart", out}
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't cut that part of “%s”.", as.DisplayTitle())
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return nil, apperr.New(apperr.ToolFailed, "MetaRush couldn't cut that part of “%s”.", as.DisplayTitle())
	}
	return generate.Keep(ctx, a, out, "video/mp4")
}

func ff(v float64) string {
	if v < 0 {
		v = 0
	}
	return strconv.FormatFloat(v, 'f', 3, 64)
}
