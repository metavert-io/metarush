// Package evidence runs free, deterministic, local detectors over footage: scene
// changes, silences (pauses), black frames, freezes and loudness. These are
// observations, not opinions — editorial judgment stays with Claude Code, which
// reads this evidence alongside transcripts and contact sheets.
package evidence

import (
	"bufio"
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Version identifies the detector configuration (part of the cache digest).
// v2 adds visual changes and activity (D71); older evidence is caught up
// (internal/catchup "visual-changes").
const Version = "ffmpeg-detectors-2"

// Span is a detected interval in source seconds.
type Span struct {
	From     float64 `json:"from" bson:"from"`
	To       float64 `json:"to" bson:"to"`
	Duration float64 `json:"duration" bson:"duration"`
}

// Scene is a detected cut/scene change.
type Scene struct {
	At    float64 `json:"at" bson:"at"`
	Score float64 `json:"score" bson:"score"`
}

// Loudness is EBU R128 measurement.
type Loudness struct {
	IntegratedLUFS float64 `json:"integratedLufs" bson:"integratedLufs"`
	RangeLU        float64 `json:"rangeLu" bson:"rangeLu"`
	TruePeakDBFS   float64 `json:"truePeakDbfs" bson:"truePeakDbfs"`
	Measured       bool    `json:"measured" bson:"measured"`
}

// Report is the technical evidence for one asset.
type Report struct {
	Version  string   `json:"version" bson:"version"`
	Source   string   `json:"source" bson:"source"` // proxy | original
	Scenes   []Scene  `json:"scenes" bson:"scenes"`
	Silences []Span   `json:"silences" bson:"silences"`
	Black    []Span   `json:"black" bson:"black"`
	Freezes  []Span   `json:"freezes" bson:"freezes"`
	Loudness Loudness `json:"loudness" bson:"loudness"`
	Notes    []string `json:"notes" bson:"notes"`
	// VisualChanges are cuts, slow changes and movement (visual.go).
	VisualChanges []VisualChange `json:"visualChanges" bson:"visualChanges"`
	// Activity is how much the picture moves, per second.
	Activity *Activity `json:"activity,omitempty" bson:"activity,omitempty"`
	// Momentary is the loudness every 0.1 s (kept in its own insight, not here).
	Momentary *Series `json:"-" bson:"-"`
}

var (
	reBlack    = regexp.MustCompile(`black_start:\s*([\d.]+)\s+black_end:\s*([\d.]+)\s+black_duration:\s*([\d.]+)`)
	reSilStart = regexp.MustCompile(`silence_start:\s*(-?[\d.]+)`)
	reSilEnd   = regexp.MustCompile(`silence_end:\s*([\d.]+)\s*\|\s*silence_duration:\s*([\d.]+)`)
	reFrzStart = regexp.MustCompile(`freeze_start:\s*([\d.]+)`)
	reFrzEnd   = regexp.MustCompile(`freeze_end:\s*([\d.]+)`)
	reI        = regexp.MustCompile(`I:\s+(-?[\d.]+|-inf)\s+LUFS`)
	reLRA      = regexp.MustCompile(`LRA:\s+([\d.]+)\s+LU`)
	rePeak     = regexp.MustCompile(`Peak:\s+(-?[\d.]+|-inf)\s+dBFS`)
	rePts      = regexp.MustCompile(`pts_time:([\d.]+)`)
	reScore    = regexp.MustCompile(`lavfi\.scene_score=([\d.]+)`)
)

// Analyze runs the detectors on a local file.
func Analyze(ctx context.Context, t *tools.Set, path, source, scratch string, mi *models.MediaInfo) (*Report, error) {
	r := &Report{Version: Version, Source: source, Scenes: []Scene{}, Silences: []Span{}, Black: []Span{}, Freezes: []Span{}, Notes: []string{}, VisualChanges: []VisualChange{}}
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-i", path}
	if mi.HasVideo {
		args = append(args, "-map", "0:v:0", "-vf", "blackdetect=d=0.25:pix_th=0.10,freezedetect=n=-60dB:d=1.5")
	}
	momentary := ""
	if mi.HasAudio {
		af := "silencedetect=noise=-35dB:d=0.4,ebur128=peak=true:framelog=verbose"
		if scratch != "" && os.MkdirAll(scratch, 0o755) == nil {
			momentary = filepath.Join(scratch, fmt.Sprintf("momentary-%d.txt", time.Now().UnixNano()))
			af += ":metadata=1" + MomentaryFilter(momentary)
			defer os.Remove(momentary)
		}
		args = append(args, "-map", "0:a:0", "-af", af)
	}
	args = append(args, "-f", "null", "-")
	res, err := t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true})
	if err != nil {
		return nil, err
	}
	parseDetectors(string(res.Stderr), r, mi.Duration)
	if momentary != "" {
		if s, err := ReadMomentary(momentary); err == nil && len(s.LUFS) > 0 {
			r.Momentary = s
		}
	}
	if mi.HasVideo {
		scenes, err := detectScenes(ctx, t, path, scratch)
		if err != nil {
			r.Notes = append(r.Notes, "scene detection failed: "+err.Error())
		} else {
			r.Scenes = scenes
		}
		if mi.Duration >= 0.5 {
			if ch, act, err := detectVisual(ctx, t, path, mi, r.Scenes); err != nil {
				r.Notes = append(r.Notes, "visual change detection failed: "+err.Error())
			} else {
				r.VisualChanges, r.Activity = ch, act
			}
		}
	}
	if mi.HasAudio && r.Loudness.Measured {
		switch {
		case r.Loudness.TruePeakDBFS > -1:
			r.Notes = append(r.Notes, fmt.Sprintf("audio true peak %.1f dBFS: risk of clipping", r.Loudness.TruePeakDBFS))
		case r.Loudness.IntegratedLUFS < -30:
			r.Notes = append(r.Notes, fmt.Sprintf("audio is quiet (%.1f LUFS integrated)", r.Loudness.IntegratedLUFS))
		}
	}
	return r, nil
}

func parseDetectors(stderr string, r *Report, dur float64) {
	sc := bufio.NewScanner(strings.NewReader(stderr))
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	var silStart, frzStart = -1.0, -1.0
	section := ""
	for sc.Scan() {
		line := sc.Text()
		if m := reBlack.FindStringSubmatch(line); m != nil {
			r.Black = append(r.Black, Span{From: f(m[1]), To: f(m[2]), Duration: f(m[3])})
		}
		if m := reSilStart.FindStringSubmatch(line); m != nil {
			silStart = math.Max(0, f(m[1]))
		}
		if m := reSilEnd.FindStringSubmatch(line); m != nil && silStart >= 0 {
			r.Silences = append(r.Silences, Span{From: round(silStart), To: f(m[1]), Duration: f(m[2])})
			silStart = -1
		}
		if m := reFrzStart.FindStringSubmatch(line); m != nil {
			frzStart = f(m[1])
		}
		if m := reFrzEnd.FindStringSubmatch(line); m != nil && frzStart >= 0 {
			end := f(m[1])
			r.Freezes = append(r.Freezes, Span{From: frzStart, To: end, Duration: round(end - frzStart)})
			frzStart = -1
		}
		trim := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trim, "Integrated loudness"):
			section = "I"
		case strings.HasPrefix(trim, "Loudness range"):
			section = "LRA"
		case strings.HasPrefix(trim, "True peak"):
			section = "peak"
		}
		if m := reI.FindStringSubmatch(line); m != nil && section == "I" {
			r.Loudness.IntegratedLUFS, r.Loudness.Measured = f(m[1]), true
		}
		if m := reLRA.FindStringSubmatch(line); m != nil && section == "LRA" {
			r.Loudness.RangeLU = f(m[1])
		}
		if m := rePeak.FindStringSubmatch(line); m != nil && section == "peak" {
			r.Loudness.TruePeakDBFS = f(m[1])
		}
	}
	// Open intervals run to the end of the file.
	if silStart >= 0 && dur > silStart {
		r.Silences = append(r.Silences, Span{From: round(silStart), To: round(dur), Duration: round(dur - silStart)})
	}
	if frzStart >= 0 && dur > frzStart {
		r.Freezes = append(r.Freezes, Span{From: frzStart, To: round(dur), Duration: round(dur - frzStart)})
	}
}

func detectScenes(ctx context.Context, t *tools.Set, path, scratch string) ([]Scene, error) {
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(scratch, "scenes-*.txt")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	// metadata=print writes "frame:N pts:P pts_time:T" then "lavfi.scene_score=S".
	filter := fmt.Sprintf("select='gt(scene\\,0.30)',metadata=mode=print:file=%s", escapeFilterPath(tmp.Name()))
	if _, err := t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error", "-i", path, "-map", "0:v:0", "-vf", filter, "-f", "null", "-"}, Background: true}); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	var out []Scene
	var cur *Scene
	for _, line := range strings.Split(string(b), "\n") {
		if m := rePts.FindStringSubmatch(line); m != nil {
			if at := round(f(m[1])); at >= 0.1 { // the first frame is not a "change"
				out = append(out, Scene{At: at})
				cur = &out[len(out)-1]
			} else {
				cur = nil
			}
		} else if m := reScore.FindStringSubmatch(line); m != nil && cur != nil {
			cur.Score = round(f(m[1]))
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	if out == nil {
		out = []Scene{}
	}
	return out, nil
}

func escapeFilterPath(p string) string {
	p = filepath.ToSlash(p)
	r := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `,`, `\,`, `[`, `\[`, `]`, `\]`, `;`, `\;`)
	return r.Replace(p)
}

func f(s string) float64 {
	if s == "-inf" {
		return math.Inf(-1)
	}
	v, _ := strconv.ParseFloat(s, 64)
	return round(v)
}

func round(v float64) float64 {
	if math.IsInf(v, 0) {
		return v
	}
	return math.Round(v*1000) / 1000
}

// Stored is the technical evidence as kept in Insight.data (kind "technical")
// and shown by the dashboard. Loudness values are null when not measurable
// (e.g. -inf on silence).
type Stored struct {
	Version  string         `json:"version" bson:"version"`
	Source   string         `json:"source" bson:"source" jsonschema:"enum=proxy,enum=original"`
	Scenes   []Scene        `json:"scenes" bson:"scenes"`
	Silences []Span         `json:"silences" bson:"silences"`
	Black    []Span         `json:"black" bson:"black"`
	Freezes  []Span         `json:"freezes" bson:"freezes"`
	Loudness StoredLoudness `json:"loudness" bson:"loudness"`
	Notes    []string       `json:"notes" bson:"notes"`
	// VisualChanges are cuts, slow changes (pans, zooms, light, someone coming
	// into view) and bursts of movement, each with frame times worth looking at.
	// Absent in evidence from before version 2.
	VisualChanges []VisualChange `json:"visualChanges,omitempty" bson:"visualChanges,omitempty"`
	// Activity is how much the picture moves, per second (0 still … 1 very busy).
	Activity *Activity `json:"activity,omitempty" bson:"activity,omitempty"`
}

// StoredLoudness is Loudness with unmeasurable values as null.
type StoredLoudness struct {
	IntegratedLUFS *float64 `json:"integratedLufs" bson:"integratedLufs" jsonschema:"oneof_type=number;null"`
	RangeLU        *float64 `json:"rangeLu" bson:"rangeLu" jsonschema:"oneof_type=number;null"`
	TruePeakDBFS   *float64 `json:"truePeakDbfs" bson:"truePeakDbfs" jsonschema:"oneof_type=number;null"`
	Measured       bool     `json:"measured" bson:"measured"`
}

// Stored converts a report to its stored shape.
func (r *Report) Stored() Stored {
	clean := func(v float64) *float64 {
		if math.IsInf(v, 0) || math.IsNaN(v) {
			return nil
		}
		return &v
	}
	return Stored{Version: r.Version, Source: r.Source, Scenes: r.Scenes, Silences: r.Silences, Black: r.Black, Freezes: r.Freezes, Notes: r.Notes,
		VisualChanges: r.VisualChanges, Activity: r.Activity,
		Loudness: StoredLoudness{IntegratedLUFS: clean(r.Loudness.IntegratedLUFS), RangeLU: clean(r.Loudness.RangeLU), TruePeakDBFS: clean(r.Loudness.TruePeakDBFS), Measured: r.Loudness.Measured}}
}

// ToMap converts a report for storage in an Insight document (the Stored shape).
func (r *Report) ToMap() map[string]any {
	s := r.Stored()
	l := map[string]any{"integratedLufs": nil, "rangeLu": nil, "truePeakDbfs": nil, "measured": s.Loudness.Measured}
	for k, v := range map[string]*float64{"integratedLufs": s.Loudness.IntegratedLUFS, "rangeLu": s.Loudness.RangeLU, "truePeakDbfs": s.Loudness.TruePeakDBFS} {
		if v != nil {
			l[k] = *v
		}
	}
	m := map[string]any{"version": s.Version, "source": s.Source, "scenes": s.Scenes, "silences": s.Silences, "black": s.Black, "freezes": s.Freezes,
		"loudness": l, "notes": s.Notes}
	if s.VisualChanges != nil {
		m["visualChanges"] = s.VisualChanges
	}
	if s.Activity != nil {
		m["activity"] = s.Activity
	}
	return m
}

// LoadStored reads the latest technical evidence of assets (one query).
// Assets without evidence are absent.
func LoadStored(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]*Stored {
	out := map[string]*Stored{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID   string    `bson:"assetId"`
		CreatedAt time.Time `bson:"createdAt"`
		Data      Stored    `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = "technical"
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs, options.Find().SetSort(store.MediaSort)); err != nil {
		return out
	}
	for i := range docs { // ascending: the latest wins
		out[docs[i].AssetID] = &docs[i].Data
	}
	return out
}

// LoadLoudness reads the measured loudness of assets from their latest
// technical evidence (one query). Assets without a measurement are absent.
// Preview (revision.plan) and render (worker) both use it, so the master
// loudness gain they compute is the same.
func LoadLoudness(ctx context.Context, db *store.DB, workspaceID string, assetIDs []string, sel ...store.InsightMedia) map[string]StoredLoudness {
	out := map[string]StoredLoudness{}
	if db == nil || len(assetIDs) == 0 {
		return out
	}
	var docs []struct {
		AssetID   string    `bson:"assetId"`
		CreatedAt time.Time `bson:"createdAt"`
		Data      struct {
			Loudness StoredLoudness `bson:"loudness"`
		} `bson:"data"`
	}
	f := store.MediaFilter(assetIDs, db.MediaFor(ctx, workspaceID, assetIDs, sel))
	f["kind"] = "technical"
	if err := db.Find(ctx, store.CInsights, workspaceID, f, &docs,
		options.Find().SetSort(store.MediaSort).SetProjection(bson.M{"assetId": 1, "createdAt": 1, "media": 1, "data.loudness": 1})); err != nil {
		return out
	}
	for _, d := range docs { // ascending: the latest wins
		if l := d.Data.Loudness; l.Measured && l.IntegratedLUFS != nil {
			out[d.AssetID] = l
		}
	}
	return out
}
