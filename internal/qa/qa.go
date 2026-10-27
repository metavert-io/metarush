// Package qa checks an actual rendered MP4 against the RenderPlan it was made
// from: conformance (duration, size, frame rate, codecs, audio presence), black
// and frozen frames (separating planned fades, dips and end cards from defects),
// loudness against delivery conventions, silence where the plan expects speech,
// and layout risks derived from plan geometry. It also extracts labeled stills
// so Claude Code can look at the render itself.
//
// Everything here is local and deterministic (ffmpeg/ffprobe). Findings say
// whether they were observed on the decoded render or inferred (heuristic).
package qa

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
)

// Version identifies the check set and thresholds (part of the job idempotency key).
const Version = "qa-7" // qa-2: narration checks and speech continuity; qa-3: transitions, split edits and loudness at cuts; qa-4: conversations; qa-5: what's on screen (faces cut off, covered, text under captions); qa-6: call footage (flashes at clip edges, printed names, the name bar) and the project's rules; qa-7: promises made to the client (D300)

// Detector thresholds. They are recorded in each report's checks.
const (
	BlackPixelTh     = 0.10 // a pixel is "black" below 10% of the luma range (blackdetect pix_th)
	BlackPictureTh   = 0.98 // a frame is black when 98% of its pixels are (blackdetect pic_th)
	FlashFrames      = 3    // unexplained black shorter than this is a flash-frame warning; longer is an error
	FreezeNoiseDB    = -60  // freezedetect noise tolerance
	FreezeMinSeconds = 1.0  // shortest frozen span reported
	FreezeErrSeconds = 2.0  // frozen span this long, where the source moves, is an error
	SilenceNoiseDB   = -50  // silencedetect threshold
	SilenceMinSec    = 0.3  // shortest silence considered
	DialogueGapWord  = 0.75 // silence inside word-timed speech that is flagged (seconds)
	DialogueGapSeg   = 1.2  // same, when only segment-level timing exists
	ExplainTolFrames = 2    // planned dark/static spans are widened by this many frames each side
)

// Dir is where a report's sheets and frames live on a machine (under its scratch folder).
func Dir(scratch, reportID string) string { return filepath.Join(scratch, "qa", reportID) }

// Span is an interval in seconds.
type Span struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
}

// SourceEvidence is what footage evidence knows about one source asset (source seconds).
type SourceEvidence struct {
	Available bool
	Black     []Span
	Freezes   []Span
}

// Input is everything a QA run needs. Only Path and Plan are required.
type Input struct {
	Path       string               // local MP4 to decode
	Plan       *editspec.RenderPlan // the plan the render was made from (full-resolution geometry)
	Spec       *editspec.Spec       // the normalized spec that was rendered (for edit-plan fix suggestions)
	Scale      float64              // render scale (review copies are 0.5)
	Platforms  []string             // the project's target platforms (brief), if any
	Speech     []Span               // timeline speech spans (word-level when available)
	SpeechPrec string               // word | segment | "" (falls back to plan.DialogueRanges)
	Source     map[string]SourceEvidence
	AssetNames map[string]string
	RevisionID string
	Quality    string
	OutDir     string // where frames and sheets are written
	Scratch    string
	MaxFrames  int // cap on moment frames (default 48)

	// Speech continuity: the words the plan expects (editspec.ExpectedSpeech),
	// spans with speech but no word timing, cut points, and the local
	// transcriber that listens to the render (nil skips the check).
	ExpectedSpeech []editspec.SpeechWord
	UnknownSpeech  []editspec.SpeechSpan
	SpeechEdges    []float64
	Transcriber    Transcriber
	// SpeechLanguage is the language the cut's clips are spoken in (D243;
	// "" lets the transcriber decide).
	SpeechLanguage string
	// Narration checks: measured loudness of the sources and the word timing
	// of narration takes (by asset).
	Loudness  map[string]Loudness
	TakeWords map[string][]editspec.TranscriptWord
	// Cuts and shots as resolved (editspec.CutReport, editspec.Shots): splits
	// short of footage, cut words, voices at once, flashes.
	Cuts  []editspec.CutInfo
	Shots []editspec.ShotInfo
	// Credits names the outside footage (clips imported from links) by asset:
	// "@handle on YouTube". QA lists it for information only.
	Credits map[string]string
	// Generated are the AI-generated clips by asset (D127). QA notes them for
	// information: platforms ask people to say so when they post.
	Generated map[string]GeneratedClip
	// Said are the changed words the cut plays (its nested cuts' included),
	// each with its consent checked now (D309, D311). Nil: the cut's own, as
	// its mix says.
	Said []SaidFacts
	// CaptionWords are the caption check's words (D148): words the computer
	// wasn't sure of, names not on the spelling lists, words fixed since.
	CaptionWords []CaptionWord
	// ClearerVoices says which voices the render plays made clearer and what
	// that did, in plain words (internal/sound). QA lists it for information.
	ClearerVoices []string
	// Resources are the pictures and fonts the cut shows (D79), with where
	// they came from; QA notes web pictures whose licence isn't recorded.
	Resources []ResourceCredit
	// Conversations are the cut's conversation clips (D99) with what their
	// records know: style, who stays off camera, sound dropouts, sync.
	Conversations []Conversation
	// OnScreen is the on-screen index of the footage the cut shows (by asset):
	// where faces and on-screen text are (D116). FaceNames names its faces
	// (asset → face track → a confirmed or recognized name).
	OnScreen  map[string]*visual.Index
	FaceNames map[string]map[int]string
	// Look reads rendered frames with the on-screen helper (a Mac), so what
	// the geometry says is confirmed in the picture itself; nil skips that.
	Look Looker
	// Calls is what the worker worked out about call footage (D161, D162):
	// flashes at clip edges, printed names, the name bar against who is on screen.
	Calls *CallFacts
	// Rules are the project's rules (D166) and the house rules of the
	// settings it uses (D193, marked House): the check measures what it can.
	Rules []models.ProjectRule
	// Style is the house style of the settings the project uses (D199; nil:
	// none), StyleOf whose settings hold it: the check measures what it can.
	Style   *editspec.HouseStyle
	StyleOf string
	// Notes are the version's delivery notes (D237): a promise that every
	// AI-generated part is disclosed reads its description (D300).
	Notes *models.DeliveryNotes
	// PackDrifts are the graphics that name a layout pack role and don't
	// follow the pack (D267: editspec.PackDrifts); PackOf names whose settings
	// hold the pack ("" MetaRush's own).
	PackDrifts []editspec.PackDrift
	PackOf     string
}

// ResourceCredit is what QA knows about a resource a cut shows.
type ResourceCredit struct {
	Name  string // its short name in the cut
	Title string
	Kind  string // image | svg | pdf | font
	// Origin is where it came from: file, folder, url, repo, project or generated.
	Origin  string
	Site    string // the web site or repository, for url and repo
	License string
	// Made: MetaRush made it from the repository (a code slide, a diagram, a card).
	Made bool
	// AI: an AI service made it (D226): a promise to disclose it counts it (D300).
	AI bool
}

// Analyze decodes the render and returns a report (IDs and document metadata are
// left to the caller). Frame and sheet paths point into in.OutDir.
func Analyze(ctx context.Context, t *tools.Set, in Input) (*models.QAReport, error) {
	if in.Plan == nil || in.Plan.FPS <= 0 {
		return nil, fmt.Errorf("qa: a render plan is required")
	}
	if in.Scale <= 0 {
		in.Scale = 1
	}
	if in.MaxFrames <= 0 {
		in.MaxFrames = 48
	}
	mi, err := probe.Probe(ctx, t, in.Path)
	if err != nil {
		return nil, fmt.Errorf("render is not decodable: %w", err)
	}
	r := &run{in: in, plan: in.Plan, fps: float64(in.Plan.FPS), mi: mi, t: t}
	r.rep = &models.QAReport{Version: Version, Findings: []models.QAFinding{}, Checks: []models.QACheck{}, Frames: []models.QAFrame{}, Limitations: []string{},
		Tools: map[string]string{"ffmpeg": t.Version(ctx, tools.FFmpeg)}}
	r.rep.Measurements.Black, r.rep.Measurements.Frozen, r.rep.Measurements.Silences = []models.QASpan{}, []models.QASpan{}, []models.QASpan{}
	r.frameCount(ctx)
	r.conformance()
	if err := r.detect(ctx); err != nil {
		return nil, err
	}
	r.blackFrames()
	r.frozenFrames()
	r.audio()
	r.narration()
	r.cuts()
	r.callFootage()
	r.houseStyle()
	r.cutLoudness()
	r.conversations()
	r.speechContinuity(ctx)
	r.layout()
	r.onScreen(ctx)
	r.outsideFootage()
	r.aiGenerated()
	r.changedWords()
	r.captionWords()
	r.clearerVoices()
	r.outsideResources()
	r.projectRules() // after the checks whose findings its measures read (faces, flashes)
	r.promises()
	r.finish()
	r.linkPromises()
	if err := r.visual(ctx); err != nil {
		return nil, err
	}
	for _, f := range r.pend {
		r.rep.Findings = append(r.rep.Findings, *f)
	}
	return r.rep, nil
}

type run struct {
	in   Input
	plan *editspec.RenderPlan
	fps  float64
	mi   *models.MediaInfo
	t    *tools.Set
	rep  *models.QAReport
	det  *evidence.Report
	dur  float64 // decoded video duration
	pend []*models.QAFinding
	// promiseFindings are the findings each promise made (nil: kept), in the
	// order of rep.Promises (D300).
	promiseFindings [][]*models.QAFinding
}

func (r *run) total() float64 { return float64(r.plan.DurationInFrames) / r.fps }

func (r *run) add(f *models.QAFinding) *models.QAFinding {
	if f.Basis == "" {
		f.Basis = models.QAObserved
	}
	f.From, f.To = round3(f.From), round3(f.To)
	f.Timecode = timecode(f.From, f.To)
	r.pend = append(r.pend, f)
	return f
}

func (r *run) check(name, basis, threshold string, kinds ...string) {
	st := "pass"
	var found []*models.QAFinding
	for _, f := range r.pend {
		for _, k := range kinds {
			if f.Kind != k {
				continue
			}
			found = append(found, f)
			switch {
			case f.Severity == models.QAError:
				st = "fail"
			case f.Severity == models.QAWarning && st != "fail":
				st = "warning"
			}
		}
	}
	r.rep.Checks = append(r.rep.Checks, models.QACheck{Name: name, Status: st, Basis: basis, Detail: checkDetail(name, found), Threshold: threshold})
}

// skip records a check that could not run, and why, in plain words.
func (r *run) skip(name, basis, why string) {
	r.rep.Checks = append(r.rep.Checks, models.QACheck{Name: name, Status: "skipped", Basis: basis, Detail: sentence(why)})
}

// skipErr is skip for a check a program failed to run: the program's error
// stays with the check's technical detail (--details, --json), not in its words.
func (r *run) skipErr(name, basis, why string, err error) {
	r.rep.Checks = append(r.rep.Checks, models.QACheck{Name: name, Status: "skipped", Basis: basis, Detail: sentence(why), Threshold: "error: " + err.Error()})
}

func (r *run) frameCount(ctx context.Context) {
	res, err := r.t.Run(ctx, tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-select_streams", "v:0", "-count_packets",
		"-show_entries", "stream=nb_read_packets,duration", "-of", "default=nw=1", "--", r.in.Path}})
	if err != nil {
		return
	}
	for _, l := range strings.Split(string(res.Stdout), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(l), "=")
		switch k {
		case "nb_read_packets":
			r.rep.Measurements.Frames, _ = strconv.Atoi(v)
		case "duration":
			r.dur, _ = strconv.ParseFloat(v, 64)
		}
	}
}

// conformance compares the decoded file with what the plan promised.
func (r *run) conformance() {
	m := &r.rep.Measurements
	p := r.plan
	m.ExpectedFrames, m.ExpectedFPS = p.DurationInFrames, p.FPS
	m.ExpectedDuration = round3(r.total())
	m.ExpectedWidth, m.ExpectedHeight = even(float64(p.Width)*r.in.Scale), even(float64(p.Height)*r.in.Scale)
	m.Width, m.Height, m.FPS = r.mi.Width, r.mi.Height, round3(r.mi.FPS)
	if r.dur <= 0 {
		r.dur = r.mi.Duration
	}
	if m.Frames > 0 {
		m.Duration = round3(float64(m.Frames) / r.fps)
	} else {
		m.Duration = round3(r.dur)
	}
	var audioDur float64
	for _, s := range r.mi.Streams {
		switch s.Type {
		case "video":
			if m.VideoCodec == "" {
				m.VideoCodec, m.PixelFormat = s.Codec, s.PixFmt
			}
		case "audio":
			if m.AudioCodec == "" {
				m.AudioCodec, audioDur = s.Codec, s.Duration
			}
		}
	}
	m.HasAudio = r.mi.HasAudio
	m.AudioDuration = round3(audioDur)
	m.ExpectsAudio = planExpectsAudio(p, r.in.Spec)
	tc := timecode(0, r.total())
	if !r.mi.HasVideo {
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "no_video", To: r.total(), Message: "The render has no picture at all.",
			Fix: &models.QAFix{Summary: "Render it again: a video without a picture means the render went wrong."}})
	}
	frameDiff := m.Frames - p.DurationInFrames
	if m.Frames == 0 {
		frameDiff = int(math.Round((m.Duration - r.total()) * r.fps))
	}
	if absI(frameDiff) > 1 {
		way := "shorter"
		if frameDiff > 0 {
			way = "longer"
		}
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "duration_mismatch", From: math.Min(m.Duration, r.total()), To: math.Max(m.Duration, r.total()),
			Message: fmt.Sprintf("The render is %s (%s) %s than the cut: it lasts %s instead of %s.", seconds(float64(frameDiff)/r.fps), frames(absI(frameDiff)), way, seconds(m.Duration), seconds(r.total())),
			Fix:     &models.QAFix{Summary: "Render it again. If it happens again, a clip may ask for more footage than its file has: check where each clip starts and ends."}})
	}
	if r.mi.HasVideo && (absI(m.Width-m.ExpectedWidth) > 2 || absI(m.Height-m.ExpectedHeight) > 2) {
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "dimension_mismatch", To: r.total(),
			Message: fmt.Sprintf("The render's picture is %s, not the %s this render should have.", sizeWords(m.Width, m.Height), sizeWords(m.ExpectedWidth, m.ExpectedHeight)),
			Fix:     &models.QAFix{Summary: "Render it again in the shape you want (vertical, landscape or square)."}})
	}
	if r.mi.HasVideo && math.Abs(r.mi.FPS-r.fps) > 0.05 {
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "fps_mismatch", To: r.total(),
			Message: fmt.Sprintf("The render plays %s frames a second, but the cut is made for %d.", strings.TrimSuffix(strconv.FormatFloat(r.mi.FPS, 'f', 2, 64), ".00"), p.FPS),
			Fix:     &models.QAFix{Summary: "Render it again: its frame rate should match the cut's."}})
	}
	if r.mi.VFR {
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "variable_frame_rate", To: r.total(), Message: "The render changes its frame rate while it plays, and some sites change the timing of such videos."})
	}
	if r.mi.HasVideo && m.VideoCodec != "h264" {
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "codec", To: r.total(), Message: "The render isn't in the file format almost every site and player takes (H.264), so it may not play everywhere."})
	}
	if r.mi.HasVideo && m.PixelFormat != "" && m.PixelFormat != "yuv420p" {
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "pixel_format", To: r.total(), Message: "The render stores its colours in a way many players and sites can't show, so it may not play everywhere."})
	}
	if r.mi.Rotation != 0 {
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rotation_metadata", To: r.total(), Message: fmt.Sprintf("The render is marked to be turned %d°, so some players may show it sideways.", r.mi.Rotation)})
	}
	switch {
	case m.ExpectsAudio && !m.HasAudio:
		r.add(&models.QAFinding{Severity: models.QAError, Kind: "audio_missing", To: r.total(), Message: "The render has no sound, although the cut has clips or tracks that should be heard.",
			Fix: &models.QAFix{Summary: "Render it again. If it happens again, check that the clips you use have sound of their own."}})
	case m.HasAudio && audioDur > 0 && r.mi.HasVideo && math.Abs(audioDur-m.Duration) > 0.2:
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "av_length_mismatch", From: math.Min(audioDur, m.Duration), To: math.Max(audioDur, m.Duration),
			Message: fmt.Sprintf("The sound runs %s and the picture %s, so one of them stops early.", seconds(audioDur), seconds(m.Duration))})
	}
	r.check("conformance", models.QAObserved, fmt.Sprintf("duration ±1 frame of %d; size ±2px of %dx%d; fps %d; h264/yuv420p; audio present when planned (%s)", p.DurationInFrames, m.ExpectedWidth, m.ExpectedHeight, p.FPS, tc),
		"no_video", "duration_mismatch", "dimension_mismatch", "fps_mismatch", "variable_frame_rate", "codec", "pixel_format", "rotation_metadata", "audio_missing", "av_length_mismatch")
}

// planExpectsAudio reports whether anything in the plan should be audible.
func planExpectsAudio(p *editspec.RenderPlan, s *editspec.Spec) bool {
	if len(p.Audio) > 0 {
		return true
	}
	for _, c := range p.Clips {
		if !c.Muted && c.HasAudio && c.Volume > 0 {
			return true
		}
	}
	return false
}

// detect runs blackdetect, freezedetect, silencedetect and ebur128 in one decode.
func (r *run) detect(ctx context.Context) error {
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-i", r.in.Path}
	if r.mi.HasVideo {
		args = append(args, "-map", "0:v:0", "-vf", fmt.Sprintf("blackdetect=d=0:pix_th=%.2f:pic_th=%.2f,freezedetect=n=%ddB:d=%.2f", BlackPixelTh, BlackPictureTh, FreezeNoiseDB, FreezeMinSeconds))
	}
	momentary := ""
	if r.mi.HasAudio {
		af := fmt.Sprintf("silencedetect=noise=%ddB:d=%.2f,ebur128=peak=true:framelog=verbose", SilenceNoiseDB, SilenceMinSec)
		if dir := r.in.Scratch; dir != "" && os.MkdirAll(dir, 0o755) == nil {
			momentary = filepath.Join(dir, "qa-momentary.txt")
			af += ":metadata=1" + evidence.MomentaryFilter(momentary)
			defer os.Remove(momentary)
		}
		args = append(args, "-map", "0:a:0", "-af", af)
	}
	args = append(args, "-f", "null", "-")
	res, err := r.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true})
	if err != nil {
		return fmt.Errorf("decoding the render failed: %w", err)
	}
	dur := r.rep.Measurements.Duration
	if dur <= 0 {
		dur = r.mi.Duration
	}
	r.det = evidence.Parse(string(res.Stderr), dur)
	if momentary != "" {
		if s, err := evidence.ReadMomentary(momentary); err == nil && len(s.LUFS) > 0 {
			r.det.Momentary = s
		}
	}
	return nil
}

// finish sorts findings, numbers them and computes the verdict.
func (r *run) finish() {
	sev := map[string]int{models.QAError: 0, models.QAWarning: 1, models.QAInfo: 2}
	sort.SliceStable(r.pend, func(i, j int) bool {
		a, b := r.pend[i], r.pend[j]
		if sev[a.Severity] != sev[b.Severity] {
			return sev[a.Severity] < sev[b.Severity]
		}
		return a.From < b.From
	})
	for i, f := range r.pend {
		f.ID = fmt.Sprintf("f%d", i+1)
		switch f.Severity {
		case models.QAError:
			r.rep.Counts.Errors++
		case models.QAWarning:
			r.rep.Counts.Warnings++
		default:
			r.rep.Counts.Info++
		}
	}
	switch {
	case r.rep.Counts.Errors > 0:
		r.rep.Verdict = models.QAFail
	case r.rep.Counts.Warnings > 0:
		r.rep.Verdict = models.QAWarnings
	default:
		r.rep.Verdict = models.QAPass
	}
	r.rep.Limitations = append(r.rep.Limitations,
		"Dark and still pictures are found by their brightness and movement, so the check can't tell whether a dark or still shot the cut doesn't plan is meant that way. Planned fades and full-screen cards are left out.",
		"Silence is found by comparing the sound with the transcripts' timing: music or background sound can hide a gap, and machine transcripts can be wrong.",
		"Where captions and graphics sit is estimated from the plan, not seen in the picture: look at the frames before judging framing, faces under captions or small text.",
		"The loudness the check aims for is the usual level for each kind of video, not a rule: sites even out the volume themselves when they play videos.")
	if r.in.Scale < 1 {
		size := fmt.Sprintf("%.0f%%-size", r.in.Scale*100)
		if math.Abs(r.in.Scale-0.5) < 0.01 {
			size = "half-size"
		}
		r.rep.Limitations = append(r.rep.Limitations, fmt.Sprintf("This is a %s review copy: check the final render before you export it, because sharpness and small text look different there.", size))
	}
}

func (r *run) finding(id string) *models.QAFinding {
	for _, f := range r.pend {
		if f.ID == id {
			return f
		}
	}
	return nil
}

func round3(v float64) float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return 0
	}
	return math.Round(v*1000) / 1000
}

func even(v float64) int {
	n := int(math.Round(v))
	if n%2 != 0 {
		n++
	}
	return n
}

func absI(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func timecode(from, to float64) string {
	if to <= from {
		return derive.Timecode(from)
	}
	return derive.Timecode(from) + "–" + derive.Timecode(to)
}

func finite(v float64) *float64 {
	if math.IsInf(v, 0) || math.IsNaN(v) {
		return nil
	}
	x := round3(v)
	return &x
}
