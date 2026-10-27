package voiceover

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"os"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/tools"
)

// LocalVoice is a free system voice for scratch narration.
type LocalVoice struct {
	Name   string `json:"name"`
	Locale string `json:"locale,omitempty"`
	Sample string `json:"sample,omitempty"`
}

// localEngine returns the scratch TTS tool on this machine.
func localEngine(a *app.App) (string, error) {
	if a.Tools.Has(tools.Say) {
		return tools.Say, nil
	}
	if a.Tools.Has(tools.EspeakNG) {
		return tools.EspeakNG, nil
	}
	msg := "This machine has no free scratch voice."
	hint := "Install espeak-ng (free) to get scratch voices, then try again."
	if runtime.GOOS == "darwin" {
		hint = "The Mac's built-in voices weren't found on this machine."
	}
	return "", apperr.New(apperr.ToolMissing, "%s", msg).WithHint(hint)
}

var reSayVoice = regexp.MustCompile(`^(.+?)\s+([a-z]{2,3}[_-][A-Za-z0-9]{2,4})\s+#\s?(.*)$`)

// LocalVoices lists the system voices (macOS say, or espeak-ng).
func LocalVoices(ctx context.Context, a *app.App) ([]LocalVoice, error) {
	eng, err := localEngine(a)
	if err != nil {
		return nil, err
	}
	var out []LocalVoice
	switch eng {
	case tools.Say:
		res, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.Say, Args: []string{"-v", "?"}})
		if err != nil {
			return nil, err
		}
		for _, l := range strings.Split(string(res.Stdout), "\n") {
			if m := reSayVoice.FindStringSubmatch(strings.TrimRight(l, " ")); m != nil {
				out = append(out, LocalVoice{Name: strings.TrimSpace(m[1]), Locale: m[2], Sample: strings.TrimSpace(m[3])})
			}
		}
	case tools.EspeakNG:
		res, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.EspeakNG, Args: []string{"--voices"}})
		if err != nil {
			return nil, err
		}
		for i, l := range strings.Split(string(res.Stdout), "\n") {
			f := strings.Fields(l)
			if i == 0 || len(f) < 4 {
				continue
			}
			out = append(out, LocalVoice{Name: f[3], Locale: f[1]})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// defaultLocalVoice prefers a natural English voice.
func defaultLocalVoice(vs []LocalVoice) string {
	for _, pref := range []string{"Samantha", "Alex", "Daniel", "Karen", "Moira"} {
		for _, v := range vs {
			if v.Name == pref {
				return v.Name
			}
		}
	}
	for _, v := range vs {
		if strings.HasPrefix(v.Locale, "en_US") || strings.HasPrefix(v.Locale, "en-us") || strings.HasPrefix(v.Locale, "en") {
			return v.Name
		}
	}
	if len(vs) > 0 {
		return vs[0].Name
	}
	return ""
}

// synthLocal speaks text into a WAV file. The text goes through a file (-f),
// never the argument list, so a line can never be read as an option.
func synthLocal(ctx context.Context, a *app.App, engine, voice string, wpm float64, text, out string) error {
	txt := out + ".txt"
	if err := os.WriteFile(txt, []byte(text+"\n"), 0o600); err != nil {
		return err
	}
	defer os.Remove(txt)
	rate := fmt.Sprint(int(math.Round(wpm)))
	var args []string
	switch engine {
	case tools.Say:
		args = []string{"-r", rate, "-f", txt, "-o", out, "--data-format=LEI16@22050"}
		if voice != "" {
			args = append([]string{"-v", voice}, args...)
		}
	default:
		args = []string{"-s", rate, "-f", txt, "-w", out}
		if voice != "" {
			args = append([]string{"-v", voice}, args...)
		}
	}
	_, err := a.Tools.Run(ctx, tools.Spec{Tool: engine, Args: args})
	return err
}

// ScratchIn makes free scratch narration for lines of a revision's script.
type ScratchIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Beats      []string `json:"beats,omitempty" jsonschema:"description=Line IDs or numbers (default: every line without audio)."`
	Voice      string   `json:"voice,omitempty" jsonschema:"maxLength=80,description=System voice name (see voiceover voices --provider local)."`
	WPM        float64  `json:"wpm,omitempty" jsonschema:"minimum=80,maximum=300,description=Speaking rate (default: the script's)."`
	Replace    bool     `json:"replace,omitempty" jsonschema:"description=Also redo lines that already have scratch audio."`
	Apply      bool     `json:"apply,omitempty" jsonschema:"description=Also put the result in the cut as a new version (applies the plan it checked)."`
}

// GenerateOut is what a generation made, plus the plan that puts it in the cut.
type GenerateOut struct {
	Takes     []TakeView  `json:"takes"`
	Generated int         `json:"generated"`
	Reused    int         `json:"reused"` // unchanged lines found in the cache (free)
	Credits   float64     `json:"credits,omitempty"`
	Warnings  []string    `json:"warnings,omitempty"`
	Result    *PlanResult `json:"result,omitempty"`
	Applied   *Applied    `json:"applied,omitempty"`
	Summary   string      `json:"summary"`
	JobID     string      `json:"jobId,omitempty" jsonschema:"description=The job that recorded this work (activity, job details)."`
}

func scratch(ctx context.Context, a *app.App, in *ScratchIn) (*GenerateOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	idx, err := rc.selectBeats(in.Beats, func(b editspec.Beat) bool {
		return b.Take == nil || (in.Replace && b.Take.Source == models.TakeScratch)
	})
	if err != nil {
		return nil, err
	}
	if len(idx) == 0 {
		return &GenerateOut{Takes: []TakeView{}, Summary: "Every line already has audio, so there was nothing new to make."}, nil
	}
	eng, err := localEngine(a)
	if err != nil {
		return nil, err
	}
	voice := in.Voice
	if voice == "" {
		vs, _ := LocalVoices(ctx, a)
		voice = defaultLocalVoice(vs)
	}
	wpm := in.WPM
	if wpm <= 0 {
		wpm = rc.spec.SpeakingRate()
	}
	out := &GenerateOut{Takes: []TakeView{}}
	var eops []editspec.Op
	for k, i := range idx {
		b := rc.spec.Voiceover.Beats[i]
		if err := step(ctx, k, len(idx), fmt.Sprintf("Making the scratch voice for line %d", i+1)); err != nil {
			return nil, err
		}
		key := scratchKey(eng, voice, wpm, b.Text)
		take := findCached(ctx, a, key)
		if take != nil {
			if take, err = adoptCached(ctx, a, take, rc.rev.ProjectID, b.ID); err != nil {
				return nil, err
			}
			out.Reused++
		} else {
			path, cleanup, err := scratchFile(a, "line.wav")
			if err != nil {
				return nil, err
			}
			if err := synthLocal(ctx, a, eng, voice, wpm, b.Text, path); err != nil {
				cleanup()
				return nil, apperr.Wrap(apperr.ToolFailed, err, "The scratch voice couldn't read %s.", lineRef(i, b)).WithDetails(map[string]any{"error": err.Error()})
			}
			mi, err := probe.Probe(ctx, a.Tools, path)
			if err != nil {
				cleanup()
				return nil, err
			}
			as, err := ingestAudio(ctx, a, rc.rev.ProjectID, path, fmt.Sprintf("Narration – line %d (scratch voice).wav", i+1), []string{"scratch", "ai-voice"}, false)
			cleanup()
			if err != nil {
				return nil, err
			}
			words := estimateWords(b.Text, 0.05, math.Max(0.1, mi.Duration-0.08))
			_ = storeWords(ctx, a, as, "scratch-estimate", eng+":"+voice, "segment", words, false)
			take = &models.VoiceTake{ProjectID: rc.rev.ProjectID, BeatID: b.ID, Text: b.Text, Source: models.TakeScratch, AssetID: as.ID, In: 0, Out: round3(mi.Duration),
				Duration: round3(mi.Duration), Words: editspec.WordCount(b.Text), AIVoice: true, Provider: "local", VoiceName: voice, ModelID: eng,
				CacheKey: key, Timing: "estimated", Note: "free scratch voice for timing review"}
			if take.Duration > 0 {
				take.WPM = math.Round(float64(take.Words) / take.Duration * 60)
			}
			if err := insertTake(ctx, a, take); err != nil {
				return nil, err
			}
			out.Generated++
		}
		out.Takes = append(out.Takes, TakeView{VoiceTake: *take, Chosen: true, Summary: takeSummary(*take)})
		eops = append(eops, setOp(b.ID, map[string]any{"take": takeRef(take), "state": "scratch"}))
	}
	a.Audit(ctx, "voiceover.scratch", "revision", rc.rev.ID, rc.rev.ProjectID, map[string]any{"lines": len(idx), "generated": out.Generated, "reused": out.Reused, "voice": voice})
	res, note, err := planCheckAfterWork(ctx, a, rc.rev, fmt.Sprintf("Scratch narration for %s (free %s voice)", plural(len(idx), "line"), voice), eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	if note != "" {
		out.Warnings = append(out.Warnings, note)
	}
	out.Summary = fmt.Sprintf("Made scratch narration for %s with the free %s voice.", plural(len(idx), "line"), voice) + hintScratch
	return out, nil
}

func scratchKey(engine, voice string, wpm float64, text string) string {
	h := sha256.New()
	fmt.Fprintf(h, "local\x00%s\x00%s\x00%d\x00%s", engine, voice, int(math.Round(wpm)), strings.Join(strings.Fields(text), " "))
	return hex.EncodeToString(h.Sum(nil))
}
