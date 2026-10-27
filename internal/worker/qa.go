package worker

import (
	"context"
	"os"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// qaHandler decodes a finished render, checks it against the plan it was made
// from, publishes the visual evidence and stores the report.
func qaHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	start := time.Now()
	rid, _ := j.Payload["renderId"].(string)
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd); err != nil {
		return nil, permanent("render %s not found", rid)
	}
	if rd.State != models.RenderAvailable || rd.Output == nil {
		return nil, permanent("render %s is %s, not available", rd.ID, rd.State)
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rd.RevisionID, &rev); err != nil {
		return nil, permanent("revision %s not found", rd.RevisionID)
	}
	report(0.05, "getting ready")
	plan, spec, assets, facts, err := resolveForRender(ctx, a, &rd, &rev)
	if err != nil {
		return nil, err
	}
	// Speech timing: resolve transcript captions (word-level where available)
	// with the same timing code the renderer uses, whether or not the cut shows captions.
	sp := editspec.Clone(spec)
	sp.Captions = &editspec.Captions{Enabled: true, Source: "transcript", MaxWordsPerLine: 12}
	speechPlan := editspec.Resolve(sp, facts, rev.ID, rev.ProjectID, rev.SpecHash)
	speech, prec := qa.SpeechFromCaptions(speechPlan.Captions, speechPlan.FPS)
	// Every word the cut should play (clips and narration), for silence and continuity checks.
	expected, unknown, edges := editspec.ExpectedSpeech(spec, facts)
	for _, w := range expected {
		if w.Kind == "narration" {
			speech = append(speech, qa.Span{From: w.From, To: w.To})
		}
	}
	loud := map[string]qa.Loudness{}
	takeWords := map[string][]editspec.TranscriptWord{}
	for id := range facts.facts {
		if l, tp, ok := facts.Loudness(id); ok {
			loud[id] = qa.Loudness{LUFS: l, TruePeak: tp, Mono: facts.AudioChannels(id) == 1}
		}
		for _, seg := range facts.tx[id] {
			takeWords[id] = append(takeWords[id], seg.Words...)
		}
	}

	names := map[string]string{}
	source := map[string]qa.SourceEvidence{}
	credits := map[string]string{}
	generated := map[string]qa.GeneratedClip{}
	for _, as := range assets {
		names[as.ID] = as.DisplayTitle()
		switch {
		case as.Source.IsGenerated():
			// AI-generated footage is noted on its own (D127), not credited as someone's.
			gc := qa.GeneratedClip{}
			if g := as.Source.Generated; g != nil {
				gc.Provider, gc.Likeness, gc.Presenter = g.ProviderTitle, g.Likeness, g.Kind == "presenter"
			}
			generated[as.ID] = gc
		case as.Source != nil:
			credits[as.ID] = as.Source.Short()
		}
		// The evidence of the media the version plays (D152): its fixed copy's when it pins one.
		var ins models.Insight
		f := store.MediaFilter([]string{as.ID}, store.InsightMedia{as.ID: spec.FixedSHA(as.ID)})
		f["workspaceId"], f["kind"] = a.WorkspaceID(), "technical"
		err := a.DB.C(store.CInsights).FindOne(ctx, f,
			options.FindOne().SetSort(bson.D{{Key: "media", Value: -1}, {Key: "createdAt", Value: -1}})).Decode(&ins)
		if err != nil {
			source[as.ID] = qa.SourceEvidence{}
			continue
		}
		source[as.ID] = qa.SourceEvidence{Available: true, Black: insightSpans(ins.Data["black"]), Freezes: insightSpans(ins.Data["freezes"])}
	}
	// The pictures and fonts the cut shows (nested cuts' included), with where
	// they came from (D79).
	var resCredits []qa.ResourceCredit
	for _, shown := range facts.extras.ShownResources(spec) {
		r, pin := shown.Doc, shown.Pin
		rc := qa.ResourceCredit{Name: shown.Name, Title: r.Title, Kind: r.Kind, License: r.License}
		if v := r.Version(pin.SHA256); v != nil {
			rc.Origin, rc.Site = v.Source.Kind, v.Source.Site
			if rc.Site == "" {
				rc.Site = v.Source.URL
			}
			// A code slide, diagram or card made from a repository shows that
			// repository's work: its licence matters like a picture from it (D106).
			if v.Source.Kind == models.FromGenerated && v.Source.Commit != "" {
				rc.Origin, rc.Made = models.FromRepo, true
			}
			if v.Source.License != "" {
				rc.License = v.Source.License
			}
			// A picture an AI service made (D226): a promise to disclose it counts it (D300).
			rc.AI = v.Source.MadeWith != nil && v.Source.MadeWith.Kind == "ai"
		}
		resCredits = append(resCredits, rc)
	}
	var proj models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), rd.ProjectID, &proj)

	report(0.1, "getting the render")
	path, err := a.Cache.Ensure(ctx, a.Media, rd.Output.StorageKey, rd.Output.SHA256, rd.Output.Size, func(done, total int64) {
		if total > 0 {
			report(0.1+0.1*float64(done)/float64(total), "getting the render")
		}
	})
	if err != nil {
		return nil, err
	}
	scratch, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	reportID := ids.New(models.QAReportPrefix)
	outDir := qa.Dir(a.Cfg.Scratch.Dir, reportID)
	scale := 1.0
	if rd.Quality == "review" {
		scale = 0.5
	}
	report(0.25, "watching and listening to the render")
	var tx qa.Transcriber
	speechLang := ""
	if skip, _ := j.Payload["noSpeechCheck"].(bool); w.Tx != nil && !skip {
		tx = w.Tx
		// The render is heard in the language its clips are spoken in (D243).
		if w.Langs != nil {
			speechLang = cutLanguage(assets, takeWords, w.Langs.Default)
			if p := w.Langs.For(speechLang); p != nil {
				tx = p
			} else {
				tx = nil // nothing here hears that language
			}
		}
	}
	onScreen, faceNames := onScreenFacts(ctx, a, plan)
	var look qa.Looker
	if vision.Rich(ctx, a.Tools) {
		look = func(ctx context.Context, dir string) ([]vision.Frame, error) {
			var out []vision.Frame
			err := vision.Analyze(ctx, a.Tools, dir, vision.Options{}, func(f vision.Frame) { out = append(out, f) })
			return out, err
		}
	}
	// The caption check (D148): unsure words, names not on the spelling lists,
	// words fixed in the transcript since this version.
	var captionWords []qa.CaptionWord
	if issues, err := transcripts.CheckCaptions(ctx, a, plan, []string{rev.ProjectID}); err == nil {
		for _, is := range issues {
			at := 0.0
			if len(is.At) > 0 {
				at = is.At[0]
			}
			captionWords = append(captionWords, qa.CaptionWord{Kind: is.Kind, Says: is.Says, At: at, Times: is.Times})
		}
	}
	// Call footage (D161, D162): flashes at clip edges, printed names, the name bar.
	callFx := callFacts(ctx, a, spec, plan, facts, &proj)
	// The house style of the settings the project uses (D199).
	house, _, houseOf := content.StyleFor(ctx, a, proj.ID)
	// … and its layout pack (D267): graphics that name a role and don't follow it.
	pack := content.PackFor(ctx, a, proj.ID)
	packInfos, _ := motion.Infos(ctx, a.DB, a.WorkspaceID(), pack.MotionHashes())
	packDrifts := editspec.PackDrifts(spec, pack, editspec.WithPackMotion(facts, packInfos))
	// Changed words the cut plays, nested cuts' included, their consent checked now (D309, D311).
	heard, err := ops.SaidHeardIn(ctx, a, plan, spec, rev.ProjectID)
	if err != nil {
		return nil, err
	}
	said := []qa.SaidFacts{}
	for _, h := range heard {
		said = append(said, qa.SaidFacts{ChangeID: h.ChangeID, From: h.From, To: h.To, Who: h.Who, Words: h.Words, Before: h.Before, Nested: h.Nested != "",
			Withdrawn: h.Withdrawn, Mouth: h.Mouth, MouthWithdrawn: h.MouthWithdrawn})
	}
	rep, err := qa.Analyze(ctx, a.Tools, qa.Input{Path: path, Said: said, Plan: plan, Spec: spec, Scale: scale, Platforms: proj.Brief.Platforms, Calls: callFx, Rules: content.RulesFor(ctx, a, &proj),
		Style: house, StyleOf: houseOf, Notes: rev.DeliveryNotes, PackDrifts: packDrifts, PackOf: pack.Of,
		Speech: speech, SpeechPrec: prec, Source: source, AssetNames: names, RevisionID: rev.ID, Quality: rd.Quality, OutDir: outDir, Scratch: scratch,
		ExpectedSpeech: expected, UnknownSpeech: unknown, SpeechEdges: edges, Transcriber: tx, SpeechLanguage: speechLang, Loudness: loud, TakeWords: takeWords,
		Cuts: editspec.CutReport(spec, facts), Shots: editspec.Shots(spec, facts), Credits: credits, Generated: generated, CaptionWords: captionWords, Resources: resCredits, ClearerVoices: clearerVoices(ctx, a, plan),
		Conversations: conversationChecks(ctx, a, spec, plan, facts), OnScreen: onScreen, FaceNames: faceNames, Look: look})
	if err != nil {
		return nil, err
	}
	report(0.85, "saving the frames")
	pub := func(f *models.QAFrame) error {
		if f == nil || f.Path == "" {
			return nil
		}
		key, sha, size, err := publish(ctx, a, f.Path, "image/jpeg")
		if err != nil {
			return err
		}
		f.Key, f.SHA256, f.Size = key, sha, size
		return nil
	}
	if err := pub(rep.ContactSheet); err != nil {
		return nil, err
	}
	if err := pub(rep.MomentsSheet); err != nil {
		return nil, err
	}
	for i := range rep.Frames {
		if err := pub(&rep.Frames[i]); err != nil {
			return nil, err
		}
	}
	now := time.Now().UTC()
	rep.ID, rep.WorkspaceID, rep.ProjectID, rep.RevisionID, rep.RevisionNumber = reportID, a.WorkspaceID(), rd.ProjectID, rev.ID, rev.Number
	rep.RenderID, rep.RenderSHA256, rep.SpecHash, rep.Preset, rep.Quality = rd.ID, rd.Output.SHA256, rd.SpecHash, rd.Preset, rd.Quality
	rep.JobID, rep.RunBy, rep.CreatedBy, rep.CreatedAt = j.ID, a.Cfg.Installation.Name, j.CreatedBy, now
	rep.Seconds = time.Since(start).Seconds()
	if err := a.DB.Insert(ctx, store.CQAReports, rep); err != nil {
		return nil, err
	}
	sum := models.RenderQA{ReportID: rep.ID, Verdict: rep.Verdict, Errors: rep.Counts.Errors, Warnings: rep.Counts.Warnings, At: now}
	_ = a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), rd.ID, bson.M{"$set": bson.M{"qa": sum}})
	a.Audit(ctx, "qa.complete", "render", rd.ID, rd.ProjectID, map[string]any{"reportId": rep.ID, "verdict": rep.Verdict, "errors": rep.Counts.Errors, "warnings": rep.Counts.Warnings, "worker": a.Cfg.Installation.Name})
	return map[string]any{"reportId": rep.ID, "verdict": rep.Verdict, "errors": rep.Counts.Errors, "warnings": rep.Counts.Warnings, "info": rep.Counts.Info}, nil
}

// onScreenFacts is what the on-screen index knows about the footage the plan
// shows (D116): each clip's index, and the names of its faces (confirmed, or
// recognized by looks).
func onScreenFacts(ctx context.Context, a *app.App, plan *editspec.RenderPlan) (map[string]*visual.Index, map[string]map[int]string) {
	var ids []string
	seen := map[string]bool{}
	for _, c := range plan.Clips {
		if !seen[c.AssetID] && !editspec.IsNested(c.AssetID) && !editspec.IsConversation(c.AssetID) {
			seen[c.AssetID] = true
			ids = append(ids, c.AssetID)
		}
	}
	idx := map[string]*visual.Index{}
	for id, st := range visual.LoadMany(ctx, a.DB, a.WorkspaceID(), ids) {
		idx[id] = st.Index
	}
	names := map[string]map[int]string{}
	for id := range idx {
		tracks, err := faces.Load(ctx, a, id)
		if err != nil {
			continue
		}
		for t, f := range tracks {
			n := f.Name
			if n == "" && f.Recognized != nil {
				n = f.Recognized.Name
			}
			if n != "" {
				if names[id] == nil {
					names[id] = map[int]string{}
				}
				names[id][t] = n
			}
		}
	}
	return idx, names
}

// conversationChecks are the cut's conversation clips with what their records
// know: the style's limit, who stays off camera, the sound dropouts clean-up
// found (unless someone chose to keep them) and recordings lined up unsurely.
func conversationChecks(ctx context.Context, a *app.App, spec *editspec.Spec, plan *editspec.RenderPlan, facts editspec.Facts) []qa.Conversation {
	plays := editspec.ConversationPlays(spec, plan, facts)
	recs := map[string]*models.Conversation{}
	var out []qa.Conversation
	for _, p := range plays {
		cv := qa.Conversation{ConversationPlay: p}
		c, seen := recs[p.ID]
		if !seen {
			var doc models.Conversation
			if err := a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), p.ID, &doc); err == nil {
				c = &doc
			}
			recs[p.ID] = c
		}
		if c != nil {
			cv.Style, cv.MaxShot = c.Style, c.MaxShot
			if c.Interviewer != "" {
				cv.OffCamera = []string{c.Interviewer}
			}
			for _, s := range c.Cleanup {
				if s.State == "skipped" {
					continue
				}
				switch s.Kind {
				case "dropout":
					cv.Dropouts = append(cv.Dropouts, qa.Glitch{Member: s.Member, From: s.From, To: s.To})
				case "freeze":
					cv.Freezes = append(cv.Freezes, qa.Glitch{Member: s.Member, From: s.From, To: s.To})
				}
			}
			for _, m := range c.Members {
				if m.Align != nil && m.Align.Nudged == 0 && m.Align.Confidence < 0.5 && m.Align.Method != "reference" {
					cv.Unsure = append(cv.Unsure, m.Name)
				}
			}
		}
		out = append(out, cv)
	}
	return out
}

// insightSpans reads stored detector spans ({from,to,...} documents).
func insightSpans(v any) []qa.Span {
	var out []qa.Span
	arr, ok := v.(bson.A)
	if !ok {
		if xs, ok2 := v.([]any); ok2 {
			arr = xs
		}
	}
	for _, x := range arr {
		var m map[string]any
		switch d := x.(type) {
		case bson.D:
			m = map[string]any{}
			for _, e := range d {
				m[e.Key] = e.Value
			}
		case bson.M:
			m = d
		case map[string]any:
			m = d
		default:
			continue
		}
		out = append(out, qa.Span{From: num(m["from"]), To: num(m["to"])})
	}
	return out
}

func num(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	case int:
		return float64(x)
	}
	return 0
}
