package ops

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// TranscriptIn selects transcript segments.
type TranscriptIn struct {
	AssetID string  `json:"assetId"`
	From    float64 `json:"from,omitempty"`
	To      float64 `json:"to,omitempty"`
	Words   bool    `json:"words,omitempty" jsonschema:"description=Include word-level timings."`
	// Speaker keeps only what one voice said: a name ("Ada"), "Speaker 2" or a label (speaker_01).
	Speaker string `json:"speaker,omitempty"`
	// Original reads the transcript as the computer wrote it, before anyone fixed it (D145).
	Original bool `json:"original,omitempty"`
}

// TranscriptOut is a transcript.
type TranscriptOut struct {
	AssetID  string                     `json:"assetId"`
	Name     string                     `json:"name" jsonschema:"description=Original filename."`
	Title    string                     `json:"title" jsonschema:"description=Display title."`
	Status   *models.TranscriptStatus   `json:"status"`
	Segments []models.TranscriptSegment `json:"segments"`
	// Speakers are the voices in the video (names, suggestions, samples).
	Speakers []SpeakerRow `json:"speakers"`
	// Fixes is where the transcript stands with people's fixes (nil when nobody fixed it).
	Fixes *models.TranscriptState `json:"fixes,omitempty"`
	// Original: these are the computer's own words (asked with original).
	Original bool   `json:"original,omitempty"`
	Note     string `json:"note"`
	// Said are the clip's words changed in the speaker's own voice copy for
	// a cut (D311): the words stay as they were said here; the cut says the
	// new ones.
	Said []TranscriptSaid `json:"said,omitempty"`
}

// TranscriptSaid is a stretch of a clip's words a cut says differently, in
// the speaker's own voice copy.
type TranscriptSaid struct {
	ChangeID string  `json:"changeId"`
	From     float64 `json:"from" jsonschema:"description=The words changed (source seconds)."`
	To       float64 `json:"to"`
	Before   string  `json:"before"`
	Words    string  `json:"words"`
	Who      string  `json:"who"`
	CutTitle string  `json:"cutTitle"`
	// InCut says the cut's latest version plays the new words; State is the change's.
	InCut bool   `json:"inCut"`
	State string `json:"state"`
}

// baseOf is which machine transcript a clip has (1 when nobody changed it).
func baseOf(as *models.Asset) int {
	if as.TextFixes == nil {
		return 1
	}
	return as.TextFixes.Base
}

// EvidenceIn selects technical evidence.
type EvidenceIn struct {
	AssetID string `json:"assetId"`
}

// EvidenceOut is the stored technical analysis.
type EvidenceOut struct {
	AssetID  string            `json:"assetId"`
	Name     string            `json:"name" jsonschema:"description=Original filename."`
	Title    string            `json:"title" jsonschema:"description=Display title."`
	Duration float64           `json:"duration"`
	Media    models.MediaInfo  `json:"media"`
	Analysis map[string]string `json:"analysis"`
	Insights []models.Insight  `json:"insights"`
	Note     string            `json:"note"`
}

// AnalyzeIn schedules analysis.
type AnalyzeIn struct {
	AssetID    string `json:"assetId,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
	Transcribe *bool  `json:"transcribe,omitempty"`
	// Visual looks at what's on screen too (Apple Vision on a Mac; default true when possible).
	Visual *bool `json:"visual,omitempty"`
	Force  bool  `json:"force,omitempty" jsonschema:"description=Re-run even if results exist."`
}

// AnalyzeOut lists scheduled jobs.
type AnalyzeOut struct {
	Jobs    []string `json:"jobs"`
	Skipped []string `json:"skipped"`
	Note    string   `json:"note"`
}

// MomentsIn searches footage for candidate moments.
type MomentsIn struct {
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty" jsonschema:"description=Limit to one asset instead of a whole project."`
	Query     string `json:"query,omitempty" jsonschema:"description=Words to look for in transcripts (optional)."`
	Limit     int    `json:"limit,omitempty"`
}

// Moment is a grounded, timestamped candidate.
type Moment struct {
	AssetID string  `json:"assetId"`
	Asset   string  `json:"asset" jsonschema:"description=The asset's display title."`
	From    float64 `json:"from"`
	To      float64 `json:"to"`
	// Kind: speech, a pause, a visual change (cut, slow change or movement; older
	// evidence has scene changes instead), black or frozen picture.
	Kind   string `json:"kind" jsonschema:"enum=speech,enum=pause,enum=visual,enum=scene,enum=black,enum=freeze"`
	Text   string `json:"text,omitempty"`
	Ref    string `json:"ref,omitempty"`
	Detail string `json:"detail,omitempty"`
	// Change is what the picture does (visual moments): a hard cut, a slow change
	// (pan, zoom, light, someone coming into view) or a burst of movement.
	Change string `json:"change,omitempty" jsonschema:"enum=cut,enum=gradual,enum=motion"`
	// Frames are source times worth looking at before saying anything about a
	// visual moment (footage frames --at): before and after a cut, the start,
	// middle and end of a slow change, the start, peak and end of movement.
	Frames []float64 `json:"frames,omitempty"`
}

// changeWords says what a visual change is, in plain words.
var changeWords = map[string]string{evidence.ChangeCut: "hard cut", evidence.ChangeGradual: "slow change (a pan, zoom, light, or someone coming into view)",
	evidence.ChangeMotion: "movement"}

// MomentsOut is a list of evidence, not judgments.
type MomentsOut struct {
	Moments []Moment `json:"moments"`
	Missing []string `json:"missing"`
	Note    string   `json:"note"`
}

func init() {
	Def("footage.transcript", "Read an asset's timed transcript (source seconds)", Flags{}, func(ctx context.Context, a *app.App, in *TranscriptIn) (*TranscriptOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		out := &TranscriptOut{AssetID: as.ID, Name: as.Name, Title: as.DisplayTitle(), Status: as.Transcript, Segments: []models.TranscriptSegment{}, Speakers: []SpeakerRow{}}
		if as.Transcript == nil || as.Transcript.State != "available" {
			out.Note = "No transcript yet. MetaRush makes one on this computer (nothing is uploaded): choose Analyze missing in the project's Sources tab."
			return out, nil
		}
		voices, err := speakers.Voices(ctx, a, as.ID, false)
		if err != nil {
			return nil, err
		}
		out.Speakers = speakerRows(voices, nil)
		out.Fixes = as.TextFixes
		if in.Original {
			if o, err := transcripts.LoadOriginal(ctx, a, as.ID, max(1, baseOf(&as))); err == nil {
				out.Original = true
				for _, s := range o.Segments {
					if in.To > 0 && (s.From >= in.To || s.To <= in.From) {
						continue
					}
					if !in.Words {
						s.Words = nil
					}
					out.Segments = append(out.Segments, s)
				}
				out.Note = "The transcript as the computer wrote it (" + o.Provider + ", " + o.Model + "), before anyone fixed it. Times are source seconds."
				return out, nil
			} else if !errors.Is(err, store.ErrNotFound) {
				return nil, err
			}
			out.Original = true // nobody changed it: the transcript is the computer's own
		}
		filter := bson.M{"assetId": as.ID}
		if in.To > 0 {
			filter["from"] = bson.M{"$lt": in.To}
			filter["to"] = bson.M{"$gt": in.From}
		}
		if in.Speaker != "" {
			v, err := speakers.ResolveLabel(voices, in.Speaker)
			if err != nil {
				return nil, apperr.New(apperr.NotFound, "Nobody called %q speaks in “%s”.", in.Speaker, as.DisplayTitle()).
					WithHint("Open the clip and look under Speakers to see who speaks in it.").WithCommand("metarush footage speakers list " + as.ID)
			}
			filter["speaker"] = v.Label
		}
		opts := options.Find().SetSort(bson.D{{Key: "idx", Value: 1}})
		if !in.Words {
			opts.SetProjection(bson.M{"words": 0})
		}
		if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), filter, &out.Segments, opts); err != nil {
			return nil, err
		}
		if out.Segments == nil {
			out.Segments = []models.TranscriptSegment{}
		}
		out.Note = "Times are source seconds from " + as.Transcript.Provider + " (" + as.Transcript.Model + "), " + as.Transcript.Precision + "-level timing. Machine transcription can contain errors; verify quotes before using them on screen."
		if as.TextFixes != nil && as.TextFixes.Fixes > 0 {
			out.Note += fmt.Sprintf(" People fixed it in %d places (words marked x were written by a person); transcript history lists the fixes.", as.TextFixes.Fixes)
		}
		if len(voices) > 0 {
			out.Note += " speaker is the voice label; speakerName is set once someone named the voice (or it was recognized, source auto). Unnamed voices may have suggestions — ask the person rather than guess."
		}
		out.Said = saidIn(ctx, a, as.ID)
		if len(out.Said) > 0 {
			out.Note += " said lists stretches a cut says differently, in the speaker's own voice copy (the words here are what was said)."
		}
		return out, nil
	})
	Def("footage.evidence", "Read technical evidence for an asset: scenes, pauses, black/frozen frames, loudness", Flags{}, func(ctx context.Context, a *app.App, in *EvidenceIn) (*EvidenceOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		out := &EvidenceOut{AssetID: as.ID, Name: as.Name, Title: as.DisplayTitle(), Duration: as.Media.Duration, Media: as.Media, Analysis: as.Analysis, Insights: []models.Insight{}}
		// The momentary loudness series is machine data for speech leveling, not evidence to read.
		if err := a.DB.Find(ctx, store.CInsights, a.WorkspaceID(), bson.M{"assetId": as.ID, "kind": bson.M{"$ne": evidence.InsightKind}}, &out.Insights, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}})); err != nil {
			return nil, err
		}
		if out.Insights == nil {
			out.Insights = []models.Insight{}
		}
		out.Note = "Measured from the picture and the sound, not a judgment of what's good. Pauses are places a cut could go. Changes are hard cuts, slow changes " +
			"(a pan, a zoom, the light, someone coming into view) and bursts of movement, each with frames worth looking at; activity is how much the picture moves (0 to 1). " +
			"The scene list is the raw hard-cut detection."
		return out, nil
	})
	Def("footage.analyze", "Schedule local analysis (transcript, technical evidence, and what's on screen on a Mac) for an asset or a whole project", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *AnalyzeIn) (*AnalyzeOut, error) {
		filter := bson.M{"state": models.AssetAvailable}
		switch {
		case in.AssetID != "":
			filter["_id"] = in.AssetID
		case in.ProjectID != "":
			filter["projectIds"] = in.ProjectID
		default:
			return nil, invalid("Say which clip or project to analyze.")
		}
		var assets []models.Asset
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets); err != nil {
			return nil, err
		}
		if len(assets) == 0 {
			return nil, invalid("There's no footage ready to analyze there yet.")
		}
		transcribe := in.Transcribe == nil || *in.Transcribe
		look := in.Visual == nil || *in.Visual
		possible := look && visualsPossible(ctx, a)
		out := &AnalyzeOut{Jobs: []string{}, Skipped: []string{}}
		noMac := false
		for _, as := range assets {
			for _, typ := range []string{jobs.TypeEvidence, jobs.TypeTranscribe, jobs.TypeVisual} {
				if typ == jobs.TypeTranscribe && (!transcribe || !as.Media.HasAudio) {
					continue
				}
				if typ == jobs.TypeVisual && (!look || !as.Media.HasVideo) {
					continue
				}
				if typ == jobs.TypeVisual && !possible {
					noMac = true
					continue
				}
				key := typ + ":" + as.ID
				if in.Force {
					key += ":" + strings.TrimPrefix(a.Actor.InstallationID, "ins_") + ":" + strings.ReplaceAll(nowStamp(), ":", "")
				}
				payload := map[string]any{"assetId": as.ID}
				if typ == jobs.TypeVisual && in.AssetID != "" {
					payload["requested"] = true // the person hears how it went
				}
				j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: key, Priority: 8, Heavy: typ == jobs.TypeTranscribe || typ == jobs.TypeVisual,
					AssetID: as.ID, Payload: payload, Actor: a.Actor})
				if err != nil {
					return nil, err
				}
				if created || j.State == models.JobQueued || j.State == models.JobRunning {
					out.Jobs = append(out.Jobs, j.ID)
				} else {
					out.Skipped = append(out.Skipped, as.ID+" "+typ+" ("+j.State+"; use force to re-run)")
				}
			}
		}
		out.Note = "The analysis runs in the background on a machine running MetaRush. The transcript, the pauses and what's on screen show up as each part finishes."
		if noMac {
			out.Note += " What's on screen was not looked at: that uses Apple Vision on a Mac, and no Mac in this workspace can do it yet."
		}
		return out, nil
	})
	Def("footage.moments", "List grounded, timestamped candidate moments (speech, pauses, scene changes) across a project", Flags{}, func(ctx context.Context, a *app.App, in *MomentsIn) (*MomentsOut, error) {
		var assets []models.Asset
		af := bson.M{"state": models.AssetAvailable}
		switch {
		case in.AssetID != "":
			af["_id"] = in.AssetID
		case in.ProjectID != "":
			af["projectIds"] = in.ProjectID
		default:
			return nil, invalid("Say which project or clip to look through.")
		}
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), af, &assets); err != nil {
			return nil, err
		}
		names := map[string]string{}
		multiVoice := map[string]bool{}
		var assetIDs []string
		out := &MomentsOut{Moments: []Moment{}, Missing: []string{}}
		for _, as := range assets {
			names[as.ID] = as.DisplayTitle()
			multiVoice[as.ID] = as.Transcript != nil && as.Transcript.Speakers > 1
			assetIDs = append(assetIDs, as.ID)
			if as.Media.HasAudio && (as.Transcript == nil || as.Transcript.State != "available") {
				out.Missing = append(out.Missing, as.ID+" ("+as.DisplayTitle()+"): no transcript")
			}
			if as.Analysis[jobs.TypeEvidence] != "available" {
				out.Missing = append(out.Missing, as.ID+" (“"+as.DisplayTitle()+"”): no technical evidence")
			}
		}
		if len(assetIDs) == 0 {
			out.Note = "There's no footage ready in this project yet."
			return out, nil
		}
		q := strings.TrimSpace(in.Query)
		segFilter := bson.M{"assetId": bson.M{"$in": assetIDs}}
		opts := options.Find().SetSort(bson.D{{Key: "assetId", Value: 1}, {Key: "idx", Value: 1}}).SetProjection(bson.M{"words": 0})
		if q != "" {
			segFilter["$text"] = bson.M{"$search": q}
		}
		var segs []models.TranscriptSegment
		if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), segFilter, &segs, opts); err != nil {
			return nil, err
		}
		for _, s := range segs {
			who := s.SpeakerName // who said it: a name, or "Speaker N" when there are several voices
			if who == "" && multiVoice[s.AssetID] {
				who = models.SpeakerDisplayLabel(s.Speaker)
			}
			out.Moments = append(out.Moments, Moment{AssetID: s.AssetID, Asset: names[s.AssetID], From: s.From, To: s.To, Kind: "speech", Text: strings.TrimSpace(s.Text), Ref: s.ID, Detail: who})
		}
		if q == "" {
			for id, ev := range evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), assetIDs) {
				for _, sp := range ev.Silences {
					if sp.To-sp.From >= 0.8 {
						out.Moments = append(out.Moments, Moment{AssetID: id, Asset: names[id], From: sp.From, To: sp.To, Kind: "pause", Detail: "silence " + fmtSec(sp.To-sp.From)})
					}
				}
				for _, sp := range ev.Black {
					out.Moments = append(out.Moments, Moment{AssetID: id, Asset: names[id], From: sp.From, To: sp.To, Kind: "black"})
				}
				for _, sp := range ev.Freezes {
					out.Moments = append(out.Moments, Moment{AssetID: id, Asset: names[id], From: sp.From, To: sp.To, Kind: "freeze"})
				}
				if ev.VisualChanges == nil {
					// Evidence from before visual change detection: hard cuts only.
					for _, sc := range ev.Scenes {
						out.Moments = append(out.Moments, Moment{AssetID: id, Asset: names[id], From: sc.At, To: sc.At, Kind: "scene", Detail: "score " + strconvF(sc.Score),
							Frames: []float64{math.Max(0, sc.At-0.4), sc.At + 0.4}})
					}
				}
				for _, vc := range ev.VisualChanges {
					out.Moments = append(out.Moments, Moment{AssetID: id, Asset: names[id], From: vc.From, To: vc.To, Kind: "visual", Change: vc.Kind,
						Detail: changeWords[vc.Kind], Frames: vc.Frames})
				}
			}
		}
		sort.SliceStable(out.Moments, func(i, j int) bool {
			if out.Moments[i].AssetID != out.Moments[j].AssetID {
				return out.Moments[i].AssetID < out.Moments[j].AssetID
			}
			return out.Moments[i].From < out.Moments[j].From
		})
		if in.Limit > 0 && len(out.Moments) > in.Limit {
			out.Moments = out.Moments[:in.Limit]
		}
		out.Note = "These are moments MetaRush found, not picks: choosing the strongest ones is an editing call. Picture moments are cuts, slow changes (a pan, a zoom, the light, someone coming into view) and movement; " +
			"look at their frames before saying anything about what's on screen."
		return out, nil
	})
}

func spans(v any) [][2]float64 {
	var out [][2]float64
	arr, ok := v.(bson.A)
	if !ok {
		return out
	}
	for _, x := range arr {
		if d, ok := x.(bson.D); ok {
			m := dmap(d)
			out = append(out, [2]float64{toF(m["from"]), toF(m["to"])})
		}
	}
	return out
}

func toF(v any) float64 {
	switch x := v.(type) {
	case float64:
		return x
	case int32:
		return float64(x)
	case int64:
		return float64(x)
	}
	return 0
}

func fmtSec(v float64) string {
	return strings.TrimRight(strings.TrimRight(strconvF(math.Round(v*100)/100), "0"), ".") + "s"
}

func dmap(d bson.D) map[string]any {
	m := make(map[string]any, len(d))
	for _, e := range d {
		m[e.Key] = e.Value
	}
	return m
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.analyze", Offering{ID: "transcripts", Group: "understand", Title: "Transcripts and captions",
		Does:  "Writes down what is said in your footage on this computer, with its pauses and loud parts.",
		Words: []string{"transcribe", "subtitles", "speech to text", "write down what is said"}, Needs: Needs{Works: []string{"transcripts", "editing"}}})
	Offer("footage.evidence", Offering{ID: "pauses-and-cuts", Group: "understand", Title: "Pauses, cuts and loud parts",
		Does:  "Measures a clip: its pauses, cuts and picture changes, black or frozen picture, and how loud it is.",
		Words: []string{"silences", "dead air", "scene changes"}, Needs: Needs{Works: []string{"editing"}}})
	Offer("footage.moments", Offering{ID: "best-moments", Group: "understand", Title: "Best moments",
		Does:  "Finds the moments worth a look across a project or one clip, from what was said, pauses and picture changes.",
		Words: []string{"highlights", "best takes", "hooks"}, Needs: Needs{Works: []string{"transcripts", "editing"}}})
}
