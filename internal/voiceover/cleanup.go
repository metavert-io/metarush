package voiceover

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Preset is one kind of voice clean-up (an ffmpeg filter chain). Cleanup never
// changes the original: it makes a second file the line can switch to (A/B).
type Preset struct {
	Name    string `json:"name"`
	Label   string `json:"label"`   // plain name
	Summary string `json:"summary"` // what it does, in plain words
	filters func(rnnoise string) []string
	trim    bool // trims leading silence (the processed file is shifted)
}

// Presets are the kinds of clean-up, gentlest last.
var Presets = []Preset{
	{Name: "voice-clean", Label: "Clean up (recommended)", Summary: "Removes rumble and background hiss, evens out loud and quiet words, softens harsh “s” sounds and sets a steady level.",
		filters: func(rn string) []string {
			return []string{"highpass=f=80", denoise(rn, 12), "acompressor=threshold=-20dB:ratio=3:attack=5:release=80:makeup=2", "deesser=i=0.4:m=0.5:f=0.5:s=o", "loudnorm=I=-18:TP=-2:LRA=11"}
		}, trim: true},
	{Name: "voice-broadcast", Label: "Radio voice", Summary: "Stronger cleanup and evening-out for a close, broadcast-style voice.",
		filters: func(rn string) []string {
			return []string{"highpass=f=90", denoise(rn, 18), "acompressor=threshold=-24dB:ratio=4:attack=3:release=60:makeup=3", "deesser=i=0.6:m=0.5:f=0.5:s=o", "loudnorm=I=-16:TP=-1.5:LRA=8"}
		}, trim: true},
	{Name: "voice-gentle", Label: "Gentle", Summary: "A light touch: removes rumble and a little hiss and sets the level.",
		filters: func(rn string) []string {
			return []string{"highpass=f=70", denoise("", 6), "acompressor=threshold=-18dB:ratio=2:attack=10:release=120", "loudnorm=I=-18:TP=-2:LRA=14"}
		}},
	{Name: "denoise", Label: "Reduce background noise only", Summary: "Only reduces steady background noise (air conditioning, hiss).",
		filters: func(rn string) []string { return []string{denoise(rn, 12)} }},
	{Name: "level", Label: "Even out volume only", Summary: "Only sets a steady, comfortable level.",
		filters: func(string) []string { return []string{"loudnorm=I=-18:TP=-2:LRA=11"} }},
}

// PresetByName finds a preset.
func PresetByName(name string) (*Preset, error) {
	if name == "" {
		name = "voice-clean"
	}
	for i := range Presets {
		if Presets[i].Name == name {
			return &Presets[i], nil
		}
	}
	names, labels := []string{}, []string{}
	for _, p := range Presets {
		names, labels = append(names, p.Name), append(labels, p.Label)
	}
	return nil, apperr.New(apperr.InvalidInput, "There is no voice cleanup called “%s”.", name).
		WithHint("Choose one of: " + strings.Join(labels, ", ") + ".").WithDetails(map[string]any{"presets": names})
}

// denoise uses RNNoise (arnndn) when a model file is installed, else FFT denoise.
func denoise(rnnoise string, strength int) string {
	if rnnoise != "" {
		return "arnndn=m=" + escapeFilterPath(rnnoise)
	}
	return fmt.Sprintf("afftdn=nr=%d:nf=-50:tn=1", strength)
}

func escapeFilterPath(p string) string {
	return strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `,`, `\,`, `[`, `\[`, `]`, `\]`).Replace(p)
}

// rnnoiseModel finds an RNNoise model (<models>/rnnoise/*.rnnn), if any.
func rnnoiseModel(a *app.App) string {
	ms, _ := filepath.Glob(filepath.Join(a.Tools.ModelsDir(), "rnnoise", "*.rnnn"))
	sort.Strings(ms)
	if len(ms) > 0 {
		return ms[0]
	}
	return ""
}

// CleanupIn processes the audio of lines (or one asset) with a preset.
type CleanupIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Beats      []string `json:"beats,omitempty" jsonschema:"description=Line IDs or numbers (default: every line with a recording)."`
	Preset     string   `json:"preset,omitempty" jsonschema:"enum=voice-clean,enum=voice-broadcast,enum=voice-gentle,enum=denoise,enum=level"`
	Use        *bool    `json:"use,omitempty" jsonschema:"description=Switch the lines to the cleaned-up version (default true); false keeps the original playing."`
	Apply      bool     `json:"apply,omitempty" jsonschema:"description=Also put the result in the cut as a new version (applies the plan it checked)."`
}

// CleanupLine is one cleaned-up line.
type CleanupLine struct {
	BeatID  string  `json:"beatId"`
	Line    int     `json:"line"`
	Preset  string  `json:"preset"`
	AssetID string  `json:"assetId"`
	Offset  float64 `json:"offset"`
	Reused  bool    `json:"reused"`
	Summary string  `json:"summary"`
}

// CleanupOut is the cleaned-up versions and the plan that switches to them.
type CleanupOut struct {
	Preset  Preset        `json:"preset"`
	Lines   []CleanupLine `json:"lines"`
	Result  *PlanResult   `json:"result,omitempty"`
	Applied *Applied      `json:"applied,omitempty"`
	Summary string        `json:"summary"`
	JobID   string        `json:"jobId,omitempty"`
}

var reSilenceEnd = regexp.MustCompile(`silence_end:\s*([\d.]+)`)

func cleanup(ctx context.Context, a *app.App, in *CleanupIn) (*CleanupOut, error) {
	preset, err := PresetByName(in.Preset)
	if err != nil {
		return nil, err
	}
	if !a.Tools.Has(tools.FFmpeg) {
		return nil, &tools.ErrMissing{Name: tools.FFmpeg}
	}
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	idx, err := rc.selectBeats(in.Beats, func(b editspec.Beat) bool {
		return b.Take != nil && (b.Take.Source == models.TakeRecorded || b.Take.Source == models.TakeImported)
	})
	if err != nil {
		return nil, err
	}
	use := in.Use == nil || *in.Use
	out := &CleanupOut{Preset: *preset, Lines: []CleanupLine{}}
	done := map[string]*models.Cleanup{} // one processed file per source asset
	var eops []editspec.Op
	rn := rnnoiseModel(a)
	for k, i := range idx {
		b := rc.spec.Voiceover.Beats[i]
		if b.Take == nil {
			return nil, apperr.New(apperr.InvalidInput, "%s has no audio to clean up.", beatName(i, b))
		}
		if err := step(ctx, k, len(idx), fmt.Sprintf("Cleaning up line %d", i+1)); err != nil {
			return nil, err
		}
		src := b.Take.AssetID
		cl, ok := done[src]
		reused := ok
		if !ok {
			if cl = existingCleanup(ctx, a, src, preset.Name); cl != nil {
				reused = true
			} else if cl, err = runCleanup(ctx, a, rc.rev.ProjectID, src, preset, rn); err != nil {
				return nil, err
			}
			done[src] = cl
		}
		t := *b.Take
		t.Processed = &editspec.ProcessedRef{AssetID: cl.AssetID, Preset: preset.Name, Offset: cl.Offset}
		t.UseProcessed = use
		eops = append(eops, setOp(b.ID, map[string]any{"take": t}))
		out.Lines = append(out.Lines, CleanupLine{BeatID: b.ID, Line: i + 1, Preset: preset.Name, AssetID: cl.AssetID, Offset: cl.Offset, Reused: reused,
			Summary: fmt.Sprintf("%s: %s", beatName(i, b), preset.Label)})
	}
	if len(eops) == 0 {
		out.Summary = "No recorded lines to clean up."
		return out, nil
	}
	res, note, err := planCheckAfterWork(ctx, a, rc.rev, fmt.Sprintf("Voice cleanup (%s) on %s", preset.Label, plural(len(eops), "line")), eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	if note != "" {
		out.Summary = note
		return out, nil
	}
	out.Summary = fmt.Sprintf("Cleaned up %s with “%s”. The original recordings are kept; you can switch any line back.", plural(len(eops), "line"), preset.Label)
	return out, nil
}

func existingCleanup(ctx context.Context, a *app.App, assetID, preset string) *models.Cleanup {
	var t models.VoiceTake
	err := a.DB.C(store.CVoiceTakes).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "cleanups.preset": preset}).Decode(&t)
	if err != nil {
		return nil
	}
	for _, c := range t.Cleanups {
		if c.Preset == preset {
			var as models.Asset
			if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.AssetID, &as) == nil && as.State == models.AssetAvailable {
				cc := c
				return &cc
			}
		}
	}
	return nil
}

// runCleanup processes one source asset into a new asset with the preset.
func runCleanup(ctx context.Context, a *app.App, projectID, assetID string, preset *Preset, rnnoise string) (*models.Cleanup, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "A recording to clean up isn't in this workspace any more.").WithDetails(map[string]any{"assetId": assetID})
	}
	srcPath, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return nil, err
	}
	out, done, err := scratchFile(a, "clean.wav")
	if err != nil {
		return nil, err
	}
	defer done()
	offset := 0.0
	if preset.trim {
		// Measure the leading silence; trimming it shifts the file, so record the shift.
		res, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-i", srcPath, "-af", "silencedetect=noise=-50dB:d=0.15", "-f", "null", "-"}})
		if err == nil {
			first := strings.Index(string(res.Stderr), "silence_start: 0")
			if m := reSilenceEnd.FindStringSubmatch(string(res.Stderr)); m != nil && first >= 0 {
				if v, err := strconv.ParseFloat(m[1], 64); err == nil && v > 0.25 {
					offset = -round3(v - 0.12) // keep a little air before the first word
				}
			}
		}
	}
	chain := preset.filters(rnnoise)
	if offset < 0 {
		chain = append([]string{fmt.Sprintf("atrim=start=%.3f", -offset), "asetpts=PTS-STARTPTS"}, chain...)
	}
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", srcPath, "-vn", "-af", strings.Join(chain, ","), "-ar", "48000", "-ac", "1", "-c:a", "pcm_s16le", out}
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true}); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "The voice cleanup didn't work on %s.", as.Name).WithDetails(map[string]any{"error": err.Error()})
	}
	name := strings.TrimSuffix(as.Name, filepath.Ext(as.Name)) + " (" + strings.ToLower(preset.Label) + ").wav"
	clean, err := ingestAudio(ctx, a, projectID, out, name, []string{"cleanup:" + preset.Name}, false)
	if err != nil {
		return nil, err
	}
	// Word timing carries over, shifted by the trim.
	if words, err := transcriptWords(ctx, a, as.ID); err == nil && len(words) > 0 {
		var ws []models.Word
		for _, w := range words {
			if f := w.From + offset; f >= 0 {
				ws = append(ws, models.Word{Text: w.Text, From: round3(f), To: round3(w.To + offset)})
			}
		}
		prec := "word"
		if as.Transcript != nil && as.Transcript.Precision != "" {
			prec = as.Transcript.Precision
		}
		_ = storeWords(ctx, a, clean, "cleanup:"+preset.Name, "", prec, ws, false)
	}
	cl := &models.Cleanup{Preset: preset.Name, AssetID: clean.ID, Offset: offset, Filters: strings.Join(chain, ","), CreatedAt: time.Now().UTC()}
	_, _ = a.DB.C(store.CVoiceTakes).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID}, bson.M{"$push": bson.M{"cleanups": cl}})
	a.Audit(ctx, "voiceover.cleanup", "asset", as.ID, projectID, map[string]any{"preset": preset.Name, "result": clean.ID, "offset": offset})
	return cl, nil
}

func jsonRaw(v any) (json.RawMessage, error) {
	b, err := json.Marshal(v)
	return b, err
}
