package sound

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/voice"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// EnhanceIn makes voices clearer: a person's, the narration's, a whole
// recording's or everyone's. It changes the voice settings, makes the processed
// copies those settings call for (cached: unchanged stretches cost nothing) and
// checks the plan that puts them in the cut.
type EnhanceIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Target     string   `json:"target" jsonschema:"minLength=1,maxLength=200,description=Who: a person's name as the speakers show it (Maya\\, Speaker 2)\\, narration\\, everyone (every recording's own sound)\\, or a recording (its title or ID)."`
	Mode       string   `json:"mode,omitempty" jsonschema:"enum=clean,enum=studio,enum=cloud,description=clean (default: on this computer\\, fast\\, free); studio (on this computer\\, slower\\, stronger); cloud (ElevenLabs voice isolation\\, paid per minute)."`
	Strength   *float64 `json:"strength,omitempty" jsonschema:"minimum=0,maximum=1,description=How much of the processed voice is heard (0–1; default 1: all of it; lower keeps some of the room)."`
	Off        *bool    `json:"off,omitempty" jsonschema:"description=A/B: true plays the original voice again (settings and processed audio are kept); false the clearer one."`
	Remove     bool     `json:"remove,omitempty" jsonschema:"description=No clearer voice for them (the original plays; a level change stays)."`
	Estimate   bool     `json:"estimate,omitempty" jsonschema:"description=Only say what it would take — how much speech\\, about how long\\, and for cloud what it costs. Nothing is made or spent."`
	Apply      bool     `json:"apply,omitempty" jsonschema:"description=Also put the result in the cut as a new version (applies the plan it checked)."`
}

// EnhanceEstimate is what making the voices clearer takes, before anything is made.
type EnhanceEstimate struct {
	SpeechSeconds float64                 `json:"speechSeconds" jsonschema:"description=Seconds of sound to process now (not made before)."`
	CachedSeconds float64                 `json:"cachedSeconds" jsonschema:"description=Seconds already processed before with the same settings (free)."`
	WorkSeconds   float64                 `json:"workSeconds" jsonschema:"description=About how long it takes on this computer."`
	Credits       float64                 `json:"credits,omitempty" jsonschema:"description=Cloud: ElevenLabs credits it costs (1\\,000 a minute of audio)."`
	Budget        *providers.BudgetStatus `json:"budget,omitempty"`
	// AccountRemaining is what the ElevenLabs plan has left this period.
	AccountRemaining *int64 `json:"accountRemaining,omitempty"`
	Allowed          bool   `json:"allowed" jsonschema:"description=It can be made now (set up here\\, within the budget and plan)."`
	Problem          string `json:"problem,omitempty" jsonschema:"description=Why it cannot be made now\\, in plain words."`
	// SetsUp: the voice cleaner and the clarity score are set up first (one
	// time; they are small and pinned, so this needs no separate step).
	SetsUp   string   `json:"setsUp,omitempty" jsonschema:"enum=clean,description=clean: the voice cleaner and the voice clarity score are set up first (one time\\, about 30 MB)."`
	Warnings []string `json:"warnings,omitempty"`
	Summary  string   `json:"summary"`
	// Cost is the cloud's estimate in dollars, stretch by stretch (D208):
	// absent when nothing goes to the cloud (clean and studio are free).
	Cost *costs.Estimate `json:"cost,omitempty"`

	refusal  string // why not allowed: setup | budget | plan | key | kept
	keptHint string
	kept     *leave.Refusal
	keptSend leave.Send
}

// recordKept records a What may leave refusal of work that was asked for
// (an estimate only says it).
func (est EnhanceEstimate) recordKept(ctx context.Context, a *app.App) {
	if est.kept != nil {
		leave.Record(ctx, a, est.kept, est.keptSend)
	}
}

// refused is the error when the estimate says no (code and hint by reason).
func (est EnhanceEstimate) refused() error {
	code, hint, cmd := apperr.NotConfigured, "A person sets up studio quality once: choose Set up on the Sound tab.", "metarush sound setup studio"
	switch est.refusal {
	case "budget":
		code, hint, cmd = apperr.Budget, "Raise the project's budget in Settings → Integrations, or use clean, which is free and runs on this computer.", "metarush providers settings elevenlabs --project <project> --budget <credits>"
	case "plan":
		code, hint, cmd = apperr.Budget, "Your ElevenLabs plan has too few credits left this period. Use clean or studio instead: both are free and run on this computer.", ""
	case "key":
		hint, cmd = "Add your ElevenLabs key on this computer in Settings → Integrations, or use clean instead.", "metarush providers set elevenlabs"
	case "nobuild":
		hint, cmd = "Use the cloud (ElevenLabs) instead.", ""
	case "kept":
		code, hint, cmd = apperr.KeptHere, est.keptHint+" Clean and studio run on this computer.", ""
	}
	e := apperr.New(code, "%s", est.Problem).WithDetails(detailsOf(est)).WithHint(hint)
	if cmd != "" {
		e = e.WithCommand(cmd)
	}
	return e
}

// detailsOf is an output as error details: the same fields, as a map, so the
// command kept with WithCommand stays beside them.
func detailsOf(v any) map[string]any {
	m := map[string]any{}
	if b, err := json.Marshal(v); err == nil {
		_ = json.Unmarshal(b, &m)
	}
	return m
}

// CopyView is one processed copy of a recording.
type CopyView struct {
	Recording string        `json:"recording" jsonschema:"description=The recording's title."`
	AssetID   string        `json:"assetId"`
	From      float64       `json:"from"`
	To        float64       `json:"to"`
	Variant   string        `json:"variant" jsonschema:"description=The processed copy (an audio asset)."`
	Reused    bool          `json:"reused" jsonschema:"description=Made before with the same settings (no work\\, no cost)."`
	Scores    []PersonScore `json:"scores"`
}

// EnhanceOut is what was done (or, with estimate, what it would take).
type EnhanceOut struct {
	Target   string                `json:"target" jsonschema:"description=Who\\, in words."`
	Mode     string                `json:"mode,omitempty"`
	Strength float64               `json:"strength,omitempty"`
	Off      bool                  `json:"off,omitempty"`
	Estimate EnhanceEstimate       `json:"estimate"`
	Copies   []CopyView            `json:"copies"`
	Scores   []PersonScore         `json:"scores" jsonschema:"description=Their voice before and after (one per person; narration: all lines together)."`
	Warnings []string              `json:"warnings,omitempty"`
	Result   *voiceover.PlanResult `json:"result,omitempty"`
	Applied  *voiceover.Applied    `json:"applied,omitempty"`
	Summary  string                `json:"summary"`
	JobID    string                `json:"jobId,omitempty"`

	stale bool // made on a version that is no longer the latest
}

// SetJobID and JobResult make enhancement tracked work (voiceover.TrackWork).
func (o *EnhanceOut) SetJobID(id string) { o.JobID = id }

// JobResult is what the job records.
func (o *EnhanceOut) JobResult() map[string]any {
	r := map[string]any{"summary": o.Summary, "lines": len(o.Copies), "target": o.Target, "mode": o.Mode}
	if o.Applied != nil && o.Applied.Problem == "" && o.Applied.RevisionID != "" {
		r["appliedRevisionId"], r["appliedNumber"] = o.Applied.RevisionID, o.Applied.Number
	}
	return r
}

// target kinds
const (
	targetPerson    = "person"
	targetRecording = "recording"
	targetEveryone  = "everyone"
	targetNarration = "narration"
)

type target struct {
	kind  string
	name  string // person
	asset string // recording
	words string // in plain words
}

func resolveTarget(c *cut, raw string) (target, error) {
	t := strings.TrimSpace(raw)
	low := strings.ToLower(t)
	switch low {
	case "narration", "narrator", "voiceover", "the narration", "the narrator":
		return target{kind: targetNarration, words: "the narration"}, nil
	case "everyone", "all", "everybody", "all voices":
		return target{kind: targetEveryone, words: "everyone on camera"}, nil
	}
	ids := editspec.AssetIDs(c.spec)
	for _, id := range ids {
		as := c.asset(id)
		if as == nil {
			continue
		}
		if id == t || strings.EqualFold(as.DisplayTitle(), t) || strings.EqualFold(as.Name, t) {
			return target{kind: targetRecording, asset: id, words: quote(as.DisplayTitle())}, nil
		}
	}
	names, _ := c.speakers()
	for _, n := range names {
		if strings.EqualFold(strings.Join(strings.Fields(n), " "), strings.Join(strings.Fields(t), " ")) {
			return target{kind: targetPerson, name: n, words: n}, nil
		}
	}
	hint := "Nobody speaks in this cut yet."
	if len(names) > 0 {
		hint = "People in this cut: " + strings.Join(names, ", ") + ". You can also choose the narration, everyone, or a recording by its title."
	}
	return target{}, apperr.New(apperr.NotFound, "Nobody called “%s” speaks in this cut.", t).WithHint(hint)
}

func quote(s string) string { return "“" + s + "”" }

// changeVoice applies the request to one voice setting (nil: a new one); it
// returns nil when nothing is left (no level change, no enhancement).
func changeVoice(v *editspec.VoiceSound, in *EnhanceIn) *editspec.VoiceSound {
	out := editspec.VoiceSound{}
	if v != nil {
		out = *v
		if v.Enhance != nil {
			e := *v.Enhance
			out.Enhance = &e
		}
	}
	switch {
	case in.Remove:
		out.Enhance = nil
	default:
		if out.Enhance == nil {
			out.Enhance = &editspec.Enhance{Mode: editspec.EnhanceClean}
		}
		if in.Mode != "" {
			if in.Mode != out.Enhance.Mode && in.Strength == nil {
				out.Enhance.Strength = nil // the new mode's own default
			}
			out.Enhance.Mode = in.Mode
		}
		if in.Strength != nil {
			s := math.Round(math.Max(0, math.Min(1, *in.Strength))*100) / 100
			out.Enhance.Strength = &s
		}
		if in.Off != nil {
			out.Enhance.Off = *in.Off
		} else if !in.Estimate && in.Mode != "" {
			out.Enhance.Off = false // asking for a mode means hearing it
		}
	}
	if out.GainDB == nil && out.Enhance == nil {
		return nil
	}
	return &out
}

// withVoice returns the mix with one voice setting replaced (or removed): a
// person's (name) or a whole recording's (asset).
func withVoice(m *editspec.Mix, name, asset string, v *editspec.VoiceSound) *editspec.Mix {
	out := cloneMix(m)
	if v != nil {
		vv := *v
		if name != "" {
			vv.Name, vv.AssetID = name, ""
		} else {
			vv.Name, vv.AssetID = "", asset
		}
		v = &vv
	}
	var kept []editspec.VoiceSound
	done := false
	for _, x := range out.Voices {
		match := (name != "" && x.Name != "" && strings.EqualFold(strings.Join(strings.Fields(x.Name), " "), strings.Join(strings.Fields(name), " "))) ||
			(name == "" && asset != "" && x.Name == "" && x.AssetID == asset)
		if match {
			if v != nil && !done {
				kept = append(kept, *v)
			}
			done = true
			continue
		}
		kept = append(kept, x)
	}
	if !done && v != nil {
		kept = append(kept, *v)
	}
	out.Voices = kept
	return out
}

func cloneMix(m *editspec.Mix) *editspec.Mix {
	if m == nil {
		return &editspec.Mix{}
	}
	return editspec.Clone(&editspec.Spec{Mix: m}).Mix
}

// wetJob is a processed voice a copy needs.
type wetJob struct {
	key      string
	asset    *models.Asset
	mode     string
	from, to float64
	cached   bool
}

// copyJob is one copy to make (or reuse).
type copyJob struct {
	win      window
	asset    *models.Asset
	channels int
	pieces   []Piece
	key      string
	settings string
	speakers []string
	cached   bool
	beats    []string // narration: the lines whose take this is
}

// wetPad is how much processed voice is made around each stretch (so the
// crossfades at its edges have processed sound to blend).
const wetPad = 0.5

// planCopies works out the copies a mix calls for over the given windows.
func planCopies(ctx context.Context, a *app.App, eng *Engine, c *cut, m *editspec.Mix, wins []window) ([]*copyJob, map[string]*wetJob, error) {
	wets := map[string]*wetJob{}
	var jobs []*copyJob
	versions := map[string]string{}
	version := func(mode string) string {
		if v, ok := versions[mode]; ok {
			return v
		}
		versions[mode] = eng.EngineVersion(ctx, mode)
		return versions[mode]
	}
	for _, w := range wins {
		as := c.asset(w.asset)
		if as == nil {
			continue
		}
		segs := c.transcript(w.asset)
		dur := as.Media.Duration
		spans := editspec.SpeakerSpans(segs, 0, math.Max(dur, w.end))
		j := &copyJob{win: w, asset: as, channels: min(2, max(1, ops.AudioChannelsOf(as)))}
		active := false
		for _, sp := range spans {
			a0, b0 := math.Max(sp.From, w.start), math.Min(sp.To, w.end)
			if b0 <= a0 {
				continue
			}
			p := Piece{From: a0, To: b0, Speaker: sp.Speaker}
			if v := m.VoiceFor(sp.Speaker); v != nil && v.Enhance != nil {
				if v.Enhance.Active() {
					p.Mode, p.Strength = v.Enhance.Mode, v.Enhance.StrengthOf()
				}
			} else if rv := m.RecordingVoice(w.asset); rv != nil && rv.Enhance.Active() {
				p.Mode, p.Strength = rv.Enhance.Mode, rv.Enhance.StrengthOf()
			}
			if p.Mode != "" {
				wf, wt := math.Max(0, a0-wetPad), b0+wetPad
				if dur > 0 {
					wt = math.Min(wt, dur)
				}
				p.Wet = WetKey(as.SHA256, p.Mode, version(p.Mode), wf, wt)
				if _, ok := wets[p.Wet]; !ok {
					wets[p.Wet] = &wetJob{key: p.Wet, asset: as, mode: p.Mode, from: wf, to: wt}
				}
				active = true
			}
			j.pieces = append(j.pieces, p)
		}
		j.speakers = editspec.SpeakersIn(segs, w.start, w.end)
		j.settings = m.VoiceSettingsFor(w.asset, j.speakers)
		if !active {
			continue // nothing to make clearer here: the recording plays as it is
		}
		j.key = CopyKey(as.SHA256, w.start, w.end, j.channels, j.pieces)
		if r, _ := cachedCopy(ctx, a, j.key); r != nil {
			j.cached = true
		}
		jobs = append(jobs, j)
	}
	// Processed voices made before cost nothing.
	for _, wj := range wets {
		if r, _ := findRecord(ctx, a, "wet", wj.key); r != nil {
			wj.cached = true
		}
	}
	// A copy found in the cache needs none of its processed voices.
	needed := map[string]bool{}
	for _, j := range jobs {
		if j.cached {
			continue
		}
		for _, p := range j.pieces {
			if p.Wet != "" {
				needed[p.Wet] = true
			}
		}
	}
	for k := range wets {
		if !needed[k] {
			delete(wets, k)
		}
	}
	return jobs, wets, nil
}

// estimate prices and times the work (what is cached is free).
func estimate(ctx context.Context, a *app.App, eng *Engine, projectID string, jobs []*copyJob, wets map[string]*wetJob) EnhanceEstimate {
	est := EnhanceEstimate{Allowed: true}
	modes := map[string]bool{}
	for _, w := range wets {
		secs := w.to - w.from
		if w.cached {
			est.CachedSeconds += secs
			continue
		}
		modes[w.mode] = true
		est.SpeechSeconds += secs
		switch w.mode {
		case "clean":
			est.WorkSeconds += secs * 0.08
		case "studio":
			speed := 0.5
			if st := eng.Comp.studioStatus(); st.Speed > 0 {
				speed = st.Speed
			}
			// The cleaned voice first, the rebuild, then each word checked:
			// measured about 1.9 s a second of speech on an M-series Mac whose
			// setup measured the rebuild alone at 0.41.
			est.WorkSeconds += secs*(2*speed+0.8) + 8
		case "cloud":
			est.Credits += CloudCredits(secs)
			est.WorkSeconds += secs*0.3 + 3
		}
	}
	for _, j := range jobs {
		if j.cached {
			continue
		}
		est.WorkSeconds += (j.win.end - j.win.start) * 0.05
	}
	// The voice cleaner and the clarity score (small, pinned) are set up the
	// first time clean is used; studio quality is a separate, deliberate setup
	// (which brings them too). Cloud never downloads anything: without them its
	// results are measured by the background level only.
	if (modes["clean"] || modes["studio"]) && !eng.Comp.CleanInstalled() { // studio starts from the cleaned voice
		if eng.Comp.CleanAvailable() {
			est.SetsUp = ComponentClean
			est.WorkSeconds += 15
		} else {
			est.Allowed, est.Problem, est.refusal = false, "The voice cleaner has no build for this kind of computer; the cloud can still make voices clearer.", "nobuild"
		}
	}
	est.Cost = cloudCost(wets)
	est.WorkSeconds = math.Round(est.WorkSeconds)
	est.SpeechSeconds = math.Round(est.SpeechSeconds*10) / 10
	est.CachedSeconds = math.Round(est.CachedSeconds*10) / 10
	for m := range modes {
		if m == "clean" {
			continue
		}
		if ok, why := eng.Comp.Ready(m); !ok {
			est.Allowed, est.Problem, est.refusal = false, why, "setup"
		}
	}
	if est.Credits > 0 {
		// Voices go to ElevenLabs: What may leave (D290), for the cut's project
		// and every project a clip sent belongs to. Said here (and recorded
		// when it was asked for, not estimated); the cloud step asks again.
		whose := []string{projectID}
		for _, w := range wets {
			if w.mode == editspec.EnhanceCloud && !w.cached && w.asset != nil {
				whose = append(whose, leave.AssetProjects(w.asset)...)
			}
		}
		eng.Whose = whose
		send := leave.AI(providers.ElevenLabsName, "ElevenLabs", []string{models.LeaveVoices}, whose, "voices for clearer sound")
		if r, err := leave.Problem(ctx, a, send); err == nil && r != nil {
			est.Allowed, est.Problem, est.refusal, est.keptHint = false, r.Message, "kept", r.Hint
			est.kept, est.keptSend = r, send
			est.Summary = estimateWords(est)
			return est
		}
		if b, err := providers.ProjectBudget(ctx, a, projectID); err == nil {
			est.Budget = b
			if b.LimitCredits != nil && b.UsedCredits+est.Credits > *b.LimitCredits {
				est.Allowed, est.refusal = false, "budget"
				est.Problem = fmt.Sprintf("This would go over the project's budget: %s credits needed, %s left.",
					providers.Commas(int64(est.Credits)), providers.Commas(int64(math.Max(0, *b.LimitCredits-b.UsedCredits))))
			}
		}
		if client, err := providers.ElevenClientFor(ctx, a); err != nil {
			est.Allowed, est.Problem, est.refusal = false, apperr.From(err).Message, "key"
		} else if sub, err := client.Subscription(ctx); err == nil && sub.Remaining() >= 0 {
			r := sub.Remaining()
			est.AccountRemaining = &r
			if int64(est.Credits) > r {
				est.Allowed, est.refusal = false, "plan"
				est.Problem = fmt.Sprintf("Your ElevenLabs plan has %s credits left this period; this needs %s.", providers.Commas(r), providers.Commas(int64(est.Credits)))
			}
		}
	}
	est.Summary = estimateWords(est)
	return est
}

// cloudCost is what the cloud's clearer voices cost (D208): a part for each
// stretch of a recording sent to ElevenLabs' voice isolation, by its minutes
// (stretches made before are free). Nil when nothing goes to the cloud.
func cloudCost(wets map[string]*wetJob) *costs.Estimate {
	var cloud []*wetJob
	for _, w := range wets {
		if w.mode == editspec.EnhanceCloud {
			cloud = append(cloud, w)
		}
	}
	if len(cloud) == 0 {
		return nil
	}
	sort.Slice(cloud, func(i, j int) bool {
		if a, b := cloud[i].asset.DisplayTitle(), cloud[j].asset.DisplayTitle(); a != b {
			return a < b
		}
		return cloud[i].from < cloud[j].from
	})
	e := costs.Of()
	for _, w := range cloud {
		secs := w.to - w.from
		credits := CloudCredits(secs)
		e.Append(costs.Part{Service: providers.ElevenLabsTitle, What: "a clearer voice", Model: providers.IsolationModel,
			Source: fmt.Sprintf("“%s” from %s to %s", w.asset.DisplayTitle(), minSec(w.from), minSec(w.to)), Amount: credits, Unit: "credits",
			Units: fmt.Sprintf("%s of sound (%s)", duration(secs), costs.Count(credits, "credit", "credits")), Rate: providers.IsolationRate(),
			USD: providers.IsolationUSD(credits), Free: w.cached})
	}
	if !e.AllFree() {
		e.Note(providers.PlanCreditsNote)
	}
	return &e
}

// WorkWords says how much sound there is to process and about how long it
// takes: "20 seconds of sound to process, about 9 seconds" (empty when there
// is nothing to process).
func WorkWords(est EnhanceEstimate) string {
	if est.SpeechSeconds == 0 {
		return ""
	}
	return fmt.Sprintf("%s of sound to process, about %s", duration(est.SpeechSeconds), duration(math.Max(1, est.WorkSeconds)))
}

// minSec says a time in a recording: 0:12, 1:24, 1:02:05.
func minSec(t float64) string {
	s := int(math.Round(math.Max(0, t)))
	if s >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
	}
	return fmt.Sprintf("%d:%02d", s/60, s%60)
}

func estimateWords(est EnhanceEstimate) string {
	if est.SpeechSeconds == 0 {
		if est.CachedSeconds > 0 {
			return "Made before with the same settings: no work and no cost."
		}
		return "Nothing to process."
	}
	s := WorkWords(est)
	if est.Credits > 0 {
		s += fmt.Sprintf(", %s ElevenLabs credits", providers.Commas(int64(est.Credits)))
		if est.Cost != nil {
			s += " (" + est.Cost.About + ")"
		}
	}
	if est.CachedSeconds > 0 {
		s += fmt.Sprintf(" (%s made before reused for free)", duration(est.CachedSeconds))
	}
	s += "."
	if est.SetsUp == ComponentClean {
		s += " First the voice cleaner is set up on this computer (one time, about 30 MB)."
	}
	return s
}

// setUpFirst sets up the voice cleaner and the clarity score when the work
// needs them (estimate.SetsUp), as steps of the same tracked work.
func setUpFirst(ctx context.Context, a *app.App, comp *Components, est EnhanceEstimate) error {
	if est.SetsUp != ComponentClean || comp.CleanInstalled() {
		return nil
	}
	if err := comp.InstallClean(ctx, func(d, t int, msg string) error { return voiceover.Step(ctx, d, t, msg+" (one time)") }); err != nil {
		if apperr.Is(err, apperr.ToolMissing) {
			return err // left out by a person: its own words and the one step (D325)
		}
		return plainFailure(err, "MetaRush couldn't set up the voice cleaner")
	}
	a.Audit(ctx, "sound.setup", "installation", a.Cfg.Installation.ID, "", map[string]any{"component": ComponentClean, "firstUse": true})
	return nil
}

// duration says seconds plainly: "45 seconds", "2 minutes", "1 hour 5 minutes".
func duration(sec float64) string {
	switch {
	case sec < 1.5:
		return "1 second"
	case sec < 90:
		return fmt.Sprintf("%d seconds", int(math.Round(sec)))
	case sec < 3600:
		m := int(math.Round(sec / 60))
		if m == 1 {
			return "1 minute"
		}
		return fmt.Sprintf("%d minutes", m)
	}
	h := int(sec / 3600)
	m := int(math.Round((sec - float64(h)*3600) / 60))
	return fmt.Sprintf("%d hour%s %d minutes", h, map[bool]string{true: "", false: "s"}[h == 1], m)
}

// newEngine builds the engine for this installation (cloud through this
// machine's ElevenLabs key, charged to the project's budget).
func newEngine(a *app.App, projectID string) *Engine {
	comp := &Components{Home: a.Cfg.Home(), Tools: a.Tools}
	comp.Status() // points the tools at installed components
	e := &Engine{Tools: a.Tools, Comp: comp, Scratch: soundScratch(a)}
	if p := transcribe.Select(a.Cfg.Transcription, a.Tools); p != nil {
		e.Hear, e.HearID = hearWith(p, filepath.Join(e.Scratch, "hear"), a.Cfg.Transcription.Language), p.Name()+"/"+filepath.Base(p.Model())
	}
	e.Cloud = func(ctx context.Context, path string, seconds float64) ([]byte, error) {
		whose := e.Whose
		if len(whose) == 0 {
			whose = []string{projectID}
		}
		if err := leave.Check(ctx, a, leave.AI(providers.ElevenLabsName, "ElevenLabs", []string{models.LeaveVoices}, whose, "voices for clearer sound")); err != nil {
			return nil, err // What may leave (D290)
		}
		client, err := providers.ElevenClientFor(ctx, a)
		if err != nil {
			return nil, err
		}
		credits := providers.IsolationCredits(seconds)
		body, err := client.Isolate(ctx, path, credits)
		if err != nil {
			return nil, err
		}
		providers.ChargeProject(ctx, a, projectID, float64(credits))
		a.Audit(ctx, "sound.isolate", "project", projectID, projectID, map[string]any{"seconds": math.Round(seconds*10) / 10, "credits": credits, "provider": providers.ElevenLabsName})
		return body, nil
	}
	return e
}

// newQuality opens the clarity score and the voice fingerprints when installed.
func newQuality(a *app.App, comp *Components) *Quality {
	q := &Quality{}
	sherpa := a.Tools.Path(tools.Sherpa)
	if lib, m := onnx.Library(sherpa), comp.ScoreModel(); lib != "" && m != "" {
		if s, err := OpenScorer(lib, m); err == nil {
			q.Scorer = s
		}
	}
	if lib := voice.Library(sherpa); lib != "" {
		model := filepath.Join(a.Tools.ModelsDir(), "diarization", voice.ModelFile)
		if _, err := os.Stat(model); err == nil {
			q.Embed = FingerprintFunc(lib, model)
		}
	}
	return q
}

// makeAll processes what is not cached and assembles the copies.
func makeAll(ctx context.Context, a *app.App, eng *Engine, q *Quality, c *cut, jobs []*copyJob, wets map[string]*wetJob, narration bool, describe func(*copyJob) string) ([]CopyView, error) {
	total := len(wets) + len(jobs)
	done := 0
	made := map[string]*Wet{}
	keys := make([]string, 0, len(wets))
	for k := range wets {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		wi, wj := wets[keys[i]], wets[keys[j]]
		if wi.asset.ID != wj.asset.ID {
			return wi.asset.ID < wj.asset.ID
		}
		return wi.from < wj.from
	})
	// Studio loads its model once for everything this run makes: every
	// recording's stretches go to one run of the studio voice (D121).
	var studio []string // wet keys, by recording then time
	recordings := map[string]bool{}
	for _, k := range keys {
		if wj := wets[k]; wj.mode == "studio" {
			if w, _, err := cachedWet(ctx, a, k); err == nil && w != nil {
				made[k] = w
				continue
			}
			studio = append(studio, k)
			recordings[wj.asset.ID] = true
		}
	}
	if len(studio) > 0 {
		say := func(n int) string {
			parts := fmt.Sprintf("%d %s", len(studio), map[bool]string{true: "part", false: "parts"}[len(studio) == 1])
			if len(recordings) > 1 {
				parts += fmt.Sprintf(" of %d recordings", len(recordings))
			}
			if n > 0 {
				return fmt.Sprintf("Making the voice clearer (studio quality, %d of %s done)", n, parts)
			}
			return "Making the voice clearer (studio quality, " + parts + ")"
		}
		if err := voiceover.Step(ctx, done, total, say(0)); err != nil {
			return nil, err
		}
		parts := make([]StudioPart, len(studio))
		srcs := map[string]string{}
		for i, k := range studio {
			wj := wets[k]
			src := srcs[wj.asset.ID]
			if src == "" {
				var err error
				if src, err = a.Cache.Ensure(ctx, a.Media, wj.asset.StorageKey, wj.asset.SHA256, wj.asset.Size, nil); err != nil {
					return nil, err
				}
				srcs[wj.asset.ID] = src
			}
			parts[i] = StudioPart{Src: src, From: wj.from, To: wj.to}
		}
		ws, err := eng.StudioMany(ctx, parts, func(n int) {
			if n < len(studio) {
				_ = voiceover.Step(ctx, done+n, total, say(n))
			}
		})
		if err != nil {
			return nil, plainFailure(err, "Making the voice clearer didn't work")
		}
		for i, k := range studio {
			wj, w := wets[k], ws[i]
			w.From = wj.from
			rec := &record{Key: k, Mode: wj.mode, Engine: eng.EngineVersion(ctx, wj.mode), SourceAssetID: wj.asset.ID, SourceSHA: wj.asset.SHA256, From: wj.from, To: wj.to}
			if err := storeWet(ctx, a, eng.Scratch, rec, w); err != nil {
				return nil, err
			}
			made[k] = w
			done++
		}
	}
	for _, k := range keys {
		wj := wets[k]
		if made[k] != nil {
			if wj.mode != "studio" {
				done++
			}
			continue
		}
		if err := voiceover.Step(ctx, done, total, fmt.Sprintf("Making the voice clearer (%s, part %d of %d)", modeWords(wj.mode), done+1, len(keys))); err != nil {
			return nil, err
		}
		done++
		if w, _, err := cachedWet(ctx, a, k); err == nil && w != nil {
			made[k] = w
			continue
		}
		src, err := a.Cache.Ensure(ctx, a.Media, wj.asset.StorageKey, wj.asset.SHA256, wj.asset.Size, nil)
		if err != nil {
			return nil, err
		}
		if wj.mode == "cloud" && wj.to-wj.from > CloudMaxSeconds {
			return nil, apperr.New(apperr.InvalidInput, "One stretch of speech is longer than an hour, more than ElevenLabs takes at once.")
		}
		w, err := eng.Process(ctx, wj.mode, src, wj.from, wj.to)
		if err != nil {
			return nil, plainFailure(err, "Making the voice clearer didn't work")
		}
		rec := &record{Key: k, Mode: wj.mode, Engine: eng.EngineVersion(ctx, wj.mode), SourceAssetID: wj.asset.ID, SourceSHA: wj.asset.SHA256, From: wj.from, To: wj.to}
		if err := storeWet(ctx, a, eng.Scratch, rec, w); err != nil {
			return nil, err
		}
		made[k] = w
	}
	var views []CopyView
	for _, j := range jobs {
		if err := voiceover.Step(ctx, done, total, "Putting the clearer voice in place and measuring it"); err != nil {
			return nil, err
		}
		done++
		v := CopyView{Recording: j.asset.DisplayTitle(), AssetID: j.asset.ID, From: j.win.start, To: j.win.end}
		if r, as := cachedCopy(ctx, a, j.key); r != nil {
			v.Variant, v.Reused, v.Scores = as.ID, true, r.Scores
			views = append(views, v)
			continue
		}
		src, err := a.Cache.Ensure(ctx, a.Media, j.asset.StorageKey, j.asset.SHA256, j.asset.Size, nil)
		if err != nil {
			return nil, err
		}
		dry, err := eng.Extract(ctx, src, j.win.start, j.win.end, WetRate, j.channels)
		if err != nil {
			return nil, err
		}
		// Exactly the window's length (whole seconds), so the copy lines up at every frame.
		want := int(math.Round((j.win.end - j.win.start) * WetRate))
		if n := dry.Frames(); n != want {
			data := make([]float32, want*dry.Channels)
			copy(data, dry.Data)
			dry.Data = data
		}
		dryMono := dry.Mono()
		aligned := map[string]*AlignedWet{}
		coherence := map[string]float64{}
		for _, p := range j.pieces {
			if p.Wet == "" || aligned[p.Wet] != nil {
				continue
			}
			w := made[p.Wet]
			if w == nil {
				return nil, fmt.Errorf("a processed voice went missing")
			}
			aw := alignWet(dryMono, j.win.start, w, p.From, p.To)
			aligned[p.Wet], coherence[p.Wet] = aw, aw.Coherence
		}
		out := Assemble(dry, j.win.start, j.pieces, aligned)
		outMono := out.Mono()
		// Scores per person whose voice changed.
		type who struct {
			spans         [][2]float64
			mode          string
			strength, coh float64
		}
		people := map[string]*who{}
		var order []string
		for _, p := range j.pieces {
			if p.Mode == "" {
				continue
			}
			x := people[p.Speaker]
			if x == nil {
				x = &who{mode: p.Mode, strength: p.Strength, coh: coherence[p.Wet]}
				people[p.Speaker] = x
				order = append(order, p.Speaker)
			}
			x.spans = append(x.spans, [2]float64{p.From, p.To})
		}
		for _, sp := range order {
			x := people[sp]
			// Studio's word check on the processed voices this person's stretches use.
			var guards []*Guard
			seen := map[string]bool{}
			for _, p := range j.pieces {
				if p.Speaker == sp && p.Wet != "" && !seen[p.Wet] {
					seen[p.Wet] = true
					if w := made[p.Wet]; w != nil && w.Guard != nil {
						guards = append(guards, w.Guard)
					}
				}
			}
			// Speech only: the transcript's segments inside the person's stretches.
			var speech [][2]float64
			for _, sg := range c.transcript(j.asset.ID) {
				for _, r := range x.spans {
					s0, s1 := math.Max(sg.From, r[0]), math.Min(sg.To, r[1])
					if s1 > s0 && (strings.TrimSpace(sg.Speaker) == sp || sp == "") {
						speech = append(speech, [2]float64{s0, s1})
					}
				}
			}
			if len(speech) == 0 {
				speech = x.spans
			}
			name := sp
			if narration {
				name = "Narration"
			}
			ps := q.ComparePerson(name, dryMono, outMono, j.win.start, speech, x.spans, x.mode, x.strength, x.coh)
			if len(guards) > 0 {
				ps.guarded(guards)
			}
			v.Scores = append(v.Scores, ps)
		}
		rec := &record{Key: j.key, SourceAssetID: j.asset.ID, SourceSHA: j.asset.SHA256, From: j.win.start, To: j.win.end, Settings: j.settings, Scores: v.Scores, Wets: wetKeysOf(j.pieces)}
		as, err := storeCopy(ctx, a, eng.Scratch, c.rev.ProjectID, j.asset, rec, out, narration)
		if err != nil {
			return nil, err
		}
		v.Variant = as.ID
		views = append(views, v)
	}
	return views, nil
}

func modeWords(mode string) string {
	switch mode {
	case "studio":
		return "studio quality"
	case "cloud":
		return "in the cloud"
	}
	return "clean"
}

// mergeScores combines a person's scores from several copies (weighted by seconds).
func mergeScores(copies []CopyView) []PersonScore {
	type acc struct {
		ps                     PersonScore
		w                      float64
		bo, ao, bs, as, bb, ab float64
		cw                     float64
		bn, an                 float64
		nw                     float64
		likes                  []float64
		warns                  map[string]bool
	}
	by := map[string]*acc{}
	var order []string
	for _, c := range copies {
		for _, s := range c.Scores {
			x := by[s.Speaker]
			if x == nil {
				x = &acc{ps: PersonScore{Speaker: s.Speaker, Mode: s.Mode, Strength: s.Strength}, warns: map[string]bool{}}
				by[s.Speaker] = x
				order = append(order, s.Speaker)
			}
			w := math.Max(0.1, s.Seconds)
			x.ps.Seconds += s.Seconds
			if s.Before.Clarity != nil && s.After.Clarity != nil {
				x.bo += w * s.Before.Clarity.Overall
				x.ao += w * s.After.Clarity.Overall
				x.bs += w * s.Before.Clarity.Speech
				x.as += w * s.After.Clarity.Speech
				x.bb += w * s.Before.Clarity.Background
				x.ab += w * s.After.Clarity.Background
				x.cw += w
			}
			if s.Before.Noise != nil && s.After.Noise != nil {
				x.bn += w * s.Before.Noise.SNR
				x.an += w * s.After.Noise.SNR
				x.nw += w
			}
			if s.Likeness != nil {
				x.likes = append(x.likes, *s.Likeness)
			}
			x.ps.Kept += s.Kept
			x.ps.SafeOnly = x.ps.SafeOnly || s.SafeOnly
			for _, kw := range s.KeptWords {
				if !contains(x.ps.KeptWords, kw) && len(x.ps.KeptWords) < 8 {
					x.ps.KeptWords = append(x.ps.KeptWords, kw)
				}
			}
			for _, wr := range s.Warnings {
				if !x.warns[wr] {
					x.warns[wr] = true
					x.ps.Warnings = append(x.ps.Warnings, wr)
				}
			}
		}
	}
	var out []PersonScore
	r2 := func(v float64) float64 { return math.Round(v*100) / 100 }
	for _, k := range order {
		x := by[k]
		ps := x.ps
		ps.Seconds = math.Round(ps.Seconds*10) / 10
		if x.cw > 0 {
			ps.Before.Clarity = &Clarity{Overall: r2(x.bo / x.cw), Speech: r2(x.bs / x.cw), Background: r2(x.bb / x.cw), Seconds: ps.Seconds}
			ps.After.Clarity = &Clarity{Overall: r2(x.ao / x.cw), Speech: r2(x.as / x.cw), Background: r2(x.ab / x.cw), Seconds: ps.Seconds}
		}
		if x.nw > 0 {
			ps.Before.Noise = &Noise{SNR: math.Round(x.bn / x.nw)}
			ps.After.Noise = &Noise{SNR: math.Round(x.an / x.nw)}
		}
		if len(x.likes) > 0 {
			m := 1.0
			for _, l := range x.likes {
				m = math.Min(m, l)
			}
			ps.Likeness = &m
		}
		ps.Summary = summarize(ps)
		out = append(out, ps)
	}
	return out
}

// presetRe reads a narration take's enhancement back from its cleaned
// version's preset ("clean 80%").
var presetRe = regexp.MustCompile(`^(clean|studio|cloud) (\d{1,3})%$`)

// PresetFor names a narration take's enhancement.
func PresetFor(mode string, strength float64) string {
	return mode + " " + strconv.Itoa(int(math.Round(strength*100))) + "%"
}

// ParsePreset reads PresetFor's words (ok false for other clean-ups).
func ParsePreset(p string) (mode string, strength float64, ok bool) {
	m := presetRe.FindStringSubmatch(p)
	if m == nil {
		return "", 0, false
	}
	n, _ := strconv.Atoi(m[2])
	return m[1], float64(n) / 100, true
}

func jsonSet(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
