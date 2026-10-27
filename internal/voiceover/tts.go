package voiceover

import (
	"context"
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/providers"
)

// TTSIn generates (or, with estimate ops, prices) ElevenLabs narration.
type TTSIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Beats      []string `json:"beats,omitempty" jsonschema:"description=Line IDs or numbers (default: every line without AI or recorded audio)."`
	Voice      string   `json:"voice,omitempty" jsonschema:"maxLength=120,description=Voice ID for this run (overrides the project/team voice)."`
	ModelID    string   `json:"modelId,omitempty" jsonschema:"maxLength=120"`
	Speed      *float64 `json:"speed,omitempty" jsonschema:"minimum=0.7,maximum=1.2"`
	Stability  *float64 `json:"stability,omitempty" jsonschema:"minimum=0,maximum=1"`
	Regenerate bool     `json:"regenerate,omitempty" jsonschema:"description=Generate again even when an identical take exists (bills again)."`
	Apply      bool     `json:"apply,omitempty" jsonschema:"description=voiceover.tts only: also put the result in the cut as a new version (applies the plan it checked)."`
}

// LineEstimate prices one line.
type LineEstimate struct {
	BeatID     string  `json:"beatId"`
	Line       int     `json:"line"`
	Characters int     `json:"characters"`
	Credits    float64 `json:"credits"`
	Cached     bool    `json:"cached"` // an identical take exists: free
	VoiceName  string  `json:"voiceName,omitempty"`
	ModelName  string  `json:"modelName,omitempty"`
}

// EstimateOut is the cost of generating narration, before anything is spent.
type EstimateOut struct {
	Lines            []LineEstimate          `json:"lines"`
	Characters       int                     `json:"characters"`
	Credits          float64                 `json:"credits"`
	CachedLines      int                     `json:"cachedLines"`
	Budget           *providers.BudgetStatus `json:"budget,omitempty"`
	AccountRemaining *int64                  `json:"accountRemaining,omitempty"`
	Voice            providers.ResolvedVoice `json:"voice"`
	VoiceNames       []string                `json:"voiceNames,omitempty" jsonschema:"description=The voices the lines use\\, by name (in line order)."`
	Warnings         []string                `json:"warnings,omitempty"`
	Allowed          bool                    `json:"allowed"`
	Summary          string                  `json:"summary"`
	// Cost is the estimate in dollars, line by line (D208): the voice, the
	// model, the line's words, characters and credits, each line's price;
	// lines made before are free.
	Cost costs.Estimate `json:"cost"`
}

// speechLine is one narration line's part of the AI voice's estimate.
func speechLine(j ttsJob) costs.Part {
	what := "the AI voice"
	if n := j.voice.VoiceName; n != "" {
		what += " (" + n + ")"
	}
	model := j.voice.ModelName
	if model == "" && j.model != nil {
		model = j.model.Name
	}
	units := costs.Count(float64(j.chars), "character", "characters")
	if math.Abs(j.credit-float64(j.chars)) > 0.05 {
		units += " (" + costs.Count(math.Round(j.credit*10)/10, "credit", "credits") + ")"
	} else {
		units += " (as many credits)"
	}
	return costs.Part{Service: providers.ElevenLabsTitle, What: what, Model: model, Source: fmt.Sprintf("Line %d: “%s”", j.idx+1, truncate(strings.TrimSpace(j.beat.Text), 60)),
		Amount: math.Round(j.credit*10) / 10, Unit: "credits", Units: units, Rate: providers.SpeechRate(), USD: providers.SpeechUSD(j.credit), Free: j.cached != nil}
}

type ttsJob struct {
	idx    int
	beat   editspec.Beat
	voice  providers.ResolvedVoice
	model  *providers.Model
	key    string
	cached *models.VoiceTake
	chars  int
	credit float64
}

// planTTS resolves voices, validates every line against the live model list,
// looks up the cache and prices the run — before any money is spent.
func planTTS(ctx context.Context, a *app.App, rc *revCtx, in *TTSIn) ([]ttsJob, *EstimateOut, *providers.ElevenClient, error) {
	idx, err := rc.selectBeats(in.Beats, func(b editspec.Beat) bool {
		return b.Take == nil || b.Take.Source == models.TakeScratch
	})
	if err != nil {
		return nil, nil, nil, err
	}
	client, err := providers.ElevenClientForProject(ctx, a, rc.rev.ProjectID)
	if err != nil {
		return nil, nil, nil, err
	}
	modelsList, err := client.Models(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	est := &EstimateOut{Lines: []LineEstimate{}, Allowed: true}
	var jobs []ttsJob
	warned := map[string]bool{}
	// Voices chosen by ID (for this run, or on a line saved without its name)
	// are named from the account, so estimates and price cards say whose voice
	// it is. One lookup per voice.
	names := map[string]string{}
	nameOf := func(id string) string {
		if n, ok := names[id]; ok {
			return n
		}
		n := ""
		if page, err := client.Voices(ctx, "", "", 100, []string{id}); err == nil {
			for _, v := range page.Voices {
				if v.VoiceID == id {
					n = v.Name
				}
			}
		}
		names[id] = n
		return n
	}
	for _, i := range idx {
		b := rc.spec.Voiceover.Beats[i]
		rv, err := providers.ResolveFor(ctx, a, rc.rev.ProjectID, b.Voice)
		if err != nil {
			return nil, nil, nil, err
		}
		if in.Voice != "" {
			rv.VoiceID, rv.VoiceName, rv.From["voice"] = in.Voice, "", "request"
		}
		if rv.VoiceID != "" && rv.VoiceName == "" {
			rv.VoiceName = nameOf(rv.VoiceID)
		}
		if in.ModelID != "" {
			rv.ModelID, rv.ModelName, rv.From["model"] = in.ModelID, "", "request"
		}
		if in.Speed != nil {
			v := *in.Speed
			rv.Settings.Speed = &v
		}
		if in.Stability != nil {
			v := *in.Stability
			rv.Settings.Stability = &v
		}
		rv.ApplyDefaultModel(modelsList)
		warns, err := providers.ValidateVoice(rv, modelsList, b.Text)
		if err != nil {
			ae := apperr.From(err)
			// Say which line ("This line has 600 characters…" → "Narration line 3 (“…”) has…").
			if rest, ok := strings.CutPrefix(ae.Message, "This line "); ok {
				ae.Message = beatName(i, b) + " " + rest
			}
			return nil, nil, nil, ae.WithDetails(map[string]any{"line": i + 1})
		}
		for _, w := range warns {
			if !warned[w] {
				warned[w] = true
				est.Warnings = append(est.Warnings, w)
			}
		}
		m := providers.FindModel(modelsList, rv.ModelID)
		if rv.ModelName == "" && m != nil {
			rv.ModelName = m.Name
		}
		j := ttsJob{idx: i, beat: b, voice: rv, model: m, key: providers.CacheKey(rv, b.Text), chars: len([]rune(strings.TrimSpace(b.Text)))}
		j.credit = providers.Credits(b.Text, m)
		if !in.Regenerate {
			j.cached = findCached(ctx, a, j.key)
		}
		le := LineEstimate{BeatID: b.ID, Line: i + 1, Characters: j.chars, Credits: math.Round(j.credit*10) / 10, Cached: j.cached != nil, VoiceName: rv.VoiceName, ModelName: rv.ModelName}
		est.Lines = append(est.Lines, le)
		if j.cached != nil {
			est.CachedLines++
		} else {
			est.Characters += j.chars
			est.Credits += j.credit
		}
		est.Voice = rv
		if n := rv.VoiceName; n != "" && !contains(est.VoiceNames, n) {
			est.VoiceNames = append(est.VoiceNames, n)
		}
		jobs = append(jobs, j)
	}
	est.Credits = math.Round(est.Credits*10) / 10
	est.Cost = costs.Of()
	for _, j := range jobs {
		est.Cost.Append(speechLine(j))
	}
	if !est.Cost.Empty() && !est.Cost.AllFree() {
		est.Cost.Note(providers.PlanCreditsNote)
	}
	if b, err := providers.ProjectBudget(ctx, a, rc.rev.ProjectID); err == nil {
		est.Budget = b
		if b.LimitCredits != nil && b.UsedCredits+est.Credits > *b.LimitCredits {
			est.Allowed = false
			est.Warnings = append(est.Warnings, fmt.Sprintf("This would go over the project's budget: %.0f credits needed, %.0f left.", est.Credits, math.Max(0, *b.LimitCredits-b.UsedCredits)))
		}
	}
	if est.Characters > 0 { // the lines' words go to ElevenLabs: What may leave (D290)
		if err := leave.Check(ctx, a, leave.AI("elevenlabs", "ElevenLabs", []string{models.LeaveWords}, []string{rc.rev.ProjectID}, "narration lines for the AI voice")); err != nil {
			if !leave.IsQuiet(ctx) {
				return nil, nil, nil, err
			}
			est.Allowed = false
			est.Warnings = append(est.Warnings, apperr.From(err).Message)
		}
	}
	if sub, err := client.Subscription(ctx); err == nil && sub.Remaining() >= 0 {
		r := sub.Remaining()
		est.AccountRemaining = &r
		if int64(est.Characters) > r {
			est.Allowed = false
			est.Warnings = append(est.Warnings, fmt.Sprintf("Your ElevenLabs plan has %d characters left this period; this needs %d.", r, est.Characters))
		}
	}
	switch {
	case len(jobs) == 0:
		est.Summary = "Nothing to generate: every line already has an AI voice or a recording."
	case est.Characters == 0:
		est.Summary = fmt.Sprintf("All %s were generated before with the same voice and settings — reusing them costs nothing.", plural(len(jobs), "line"))
		if len(jobs) == 1 {
			est.Summary = "The line was generated before with the same voice and settings — reusing it costs nothing."
		}
	default:
		est.Summary = fmt.Sprintf("%s, %d characters ≈ %.0f ElevenLabs credits, %s", plural(len(jobs)-est.CachedLines, "line"), est.Characters, est.Credits, est.Cost.About)
		if est.CachedLines > 0 {
			est.Summary += fmt.Sprintf(" (%s free)", plural(est.CachedLines, "unchanged line"))
		}
		est.Summary += "."
	}
	return jobs, est, client, nil
}

func estimate(ctx context.Context, a *app.App, in *TTSIn) (*EstimateOut, error) {
	ctx = leave.Quiet(ctx) // an estimate: a refusal is said, not recorded
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	_, est, _, err := planTTS(ctx, a, rc, in)
	return est, err
}

func generateTTS(ctx context.Context, a *app.App, in *TTSIn) (*GenerateOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	jobs, est, client, err := planTTS(ctx, a, rc, in)
	if err != nil {
		return nil, err
	}
	if !est.Allowed {
		return nil, apperr.New(apperr.Budget, "%s", strings.Join(est.Warnings, " ")).WithDetails(detailsOf(est)).
			WithHint("Raise the project's budget in Settings → Integrations, shorten the script, or use the free scratch voice.").
			WithCommand(fmt.Sprintf("metarush providers settings elevenlabs --project %s --budget <credits>", rc.rev.ProjectID))
	}
	out := &GenerateOut{Takes: []TakeView{}, Warnings: est.Warnings}
	var eops []editspec.Op
	beats := rc.spec.Voiceover.Beats
	for k, j := range jobs {
		msg := fmt.Sprintf("Making the AI voice for line %d", j.idx+1)
		if j.cached != nil {
			msg = fmt.Sprintf("Reusing the AI voice for line %d (no charge)", j.idx+1)
		}
		if err := step(ctx, k, len(jobs), msg); err != nil {
			return nil, err
		}
		take := j.cached
		if take != nil {
			if take, err = adoptCached(ctx, a, take, rc.rev.ProjectID, j.beat.ID); err != nil {
				return nil, err
			}
			out.Reused++
		} else {
			req := providers.TTSRequest{VoiceID: j.voice.VoiceID, ModelID: j.voice.ModelID, Text: strings.TrimSpace(j.beat.Text),
				Settings: providers.ToAPI(j.voice.Settings, j.model), OutputFormat: j.voice.OutputFormat}
			if j.idx > 0 {
				req.PreviousText = beats[j.idx-1].Text
			}
			if j.idx+1 < len(beats) {
				req.NextText = beats[j.idx+1].Text
			}
			res, err := client.TTS(ctx, req)
			if err != nil {
				return nil, apperr.From(err).WithDetails(map[string]any{"line": j.idx + 1, "generatedSoFar": out.Generated})
			}
			providers.ChargeProject(ctx, a, rc.rev.ProjectID, j.credit)
			out.Credits += j.credit
			path, cleanup, err := scratchFile(a, "line."+res.Ext)
			if err != nil {
				return nil, err
			}
			if err := os.WriteFile(path, res.Audio, 0o600); err != nil {
				cleanup()
				return nil, err
			}
			mi, perr := probe.Probe(ctx, a.Tools, path)
			if perr != nil {
				cleanup()
				return nil, apperr.Wrap(apperr.Provider, perr, "ElevenLabs sent back audio MetaRush can't read. Try again in a moment.")
			}
			name := fmt.Sprintf("Narration – line %d (%s, AI voice).%s", j.idx+1, firstNonEmpty(j.voice.VoiceName, "ElevenLabs"), res.Ext)
			as, err := ingestAudio(ctx, a, rc.rev.ProjectID, path, name, []string{"tts", "ai-voice"}, false)
			cleanup()
			if err != nil {
				return nil, err
			}
			timing := "word"
			var words []models.Word
			if res.Aligned {
				for _, w := range res.Words {
					words = append(words, models.Word{Text: w.Text, From: w.From, To: w.To})
				}
				_ = storeWords(ctx, a, as, "elevenlabs-alignment", j.voice.ModelID, "word", words, true)
			} else {
				timing = "estimated"
				_ = storeWords(ctx, a, as, "tts-estimate", j.voice.ModelID, "segment", estimateWords(j.beat.Text, 0.05, math.Max(0.1, mi.Duration-0.08)), false)
				out.Warnings = append(out.Warnings, fmt.Sprintf("ElevenLabs sent no word timing for %s, so its captions use estimated timing.", lineRef(j.idx, j.beat)))
			}
			take = &models.VoiceTake{ProjectID: rc.rev.ProjectID, BeatID: j.beat.ID, Text: j.beat.Text, Source: models.TakeTTS, AssetID: as.ID, In: 0, Out: round3(mi.Duration),
				Duration: round3(mi.Duration), Words: editspec.WordCount(j.beat.Text), AIVoice: true, Provider: providers.ElevenLabsName,
				VoiceID: j.voice.VoiceID, VoiceName: j.voice.VoiceName, ModelID: j.voice.ModelID, SettingsKey: j.voice.SettingsKey(), CacheKey: j.key,
				Characters: j.chars, Credits: j.credit, Timing: timing}
			if mi.Duration > 0 {
				take.WPM = math.Round(float64(take.Words) / mi.Duration * 60)
			}
			if err := insertTake(ctx, a, take); err != nil {
				return nil, err
			}
			out.Generated++
			a.Audit(ctx, "voiceover.tts", "take", take.ID, rc.rev.ProjectID, map[string]any{"line": j.idx + 1, "beat": j.beat.ID, "characters": j.chars,
				"credits": j.credit, "provider": providers.ElevenLabsName, "voice": j.voice.VoiceName, "model": j.voice.ModelID, "aiVoice": true})
		}
		out.Takes = append(out.Takes, TakeView{VoiceTake: *take, Chosen: true, Summary: takeSummary(*take)})
		ref := takeRef(take)
		eops = append(eops, setOp(j.beat.ID, map[string]any{"take": ref, "state": "generated"}))
	}
	out.Credits = math.Round(out.Credits*10) / 10
	res, note, err := planCheckAfterWork(ctx, a, rc.rev, fmt.Sprintf("AI narration (ElevenLabs) for %s", plural(len(jobs), "line")), eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	if note != "" {
		out.Warnings = append(out.Warnings, note)
	}
	switch {
	case len(jobs) == 0:
		out.Summary = est.Summary
	case out.Generated == 0:
		out.Summary = fmt.Sprintf("Reused %s at no cost.", plural(out.Reused, "unchanged line")) + hintApply
	default:
		out.Summary = fmt.Sprintf("Generated %s with ElevenLabs (about %.0f credits)", plural(out.Generated, "line"), out.Credits)
		if out.Reused > 0 {
			out.Summary += fmt.Sprintf(" and reused %s for free", plural(out.Reused, "unchanged line"))
		}
		out.Summary += ". This narration is an AI voice." + hintApply
	}
	return out, nil
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}
