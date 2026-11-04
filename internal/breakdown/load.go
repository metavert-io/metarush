package breakdown

import (
	"context"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/visual"
)

// looks are MetaRush's first looks at a clip the card reads, in plain words
// (the jobs that make them).
var looks = []struct{ job, words string }{
	{jobs.TypeImport, "bringing it in"},
	{jobs.TypeProxy, "its preview copy"},
	{jobs.TypeTranscribe, "what is said"},
	{jobs.TypeDiarize, "who speaks"},
	{jobs.TypeEvidence, "its cuts, pauses and loudness"},
	{jobs.TypeVisual, "what's on screen"},
}

// LookTypes are the jobs whose results the card reads.
func LookTypes() []string {
	out := make([]string, len(looks))
	for i, l := range looks {
		out[i] = l.job
	}
	return out
}

// Pending lists the looks still running for a clip, in plain words.
func Pending(ctx context.Context, a *app.App, assetID string) []string {
	var js []models.Job
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": assetID, "type": bson.M{"$in": LookTypes()},
		"state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}}, &js)
	running := map[string]bool{}
	for _, j := range js {
		running[j.Type] = true
	}
	out := []string{}
	for _, l := range looks {
		if running[l.job] {
			out = append(out, l.words)
		}
	}
	return out
}

// Load gathers what a clip's card is made from: its transcript and voices,
// its technical evidence and loudness, its on-screen index, and the looks
// still running or that no computer here can make.
func Load(ctx context.Context, a *app.App, as *models.Asset) (Input, error) {
	in := Input{Duration: as.Media.Duration, Width: as.Media.Width, Height: as.Media.Height, HasVideo: as.Media.HasVideo, HasAudio: as.Media.HasAudio, Names: map[string]string{}}
	segs, err := transcripts.LoadSegments(ctx, a, as.ID)
	if err != nil {
		return in, err
	}
	in.Heard = as.Transcript != nil && (as.Transcript.State == "available" || as.Transcript.State == "none")
	for _, sg := range segs {
		if sg.SpeakerName != "" {
			in.Names[sg.Speaker] = sg.SpeakerName
		}
		if len(sg.Words) > 0 {
			for _, w := range sg.Words {
				in.Words = append(in.Words, Word{Text: w.Text, From: w.From, To: w.To, Speaker: sg.Speaker})
			}
			continue
		}
		// A segment without word times: its words spread over it.
		ws := strings.Fields(sg.Text)
		if len(ws) == 0 || sg.To <= sg.From {
			continue
		}
		step := (sg.To - sg.From) / float64(len(ws))
		for i, w := range ws {
			in.Words = append(in.Words, Word{Text: w, From: sg.From + step*float64(i), To: sg.From + step*float64(i+1) - 0.02, Speaker: sg.Speaker})
		}
	}
	if ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]; ev != nil {
		in.Measured = true
		in.Cuts = ops.CutsOf(ev)
		for _, s := range ev.Silences {
			in.Silences = append(in.Silences, Span{s.From, s.To})
		}
		for _, s := range ev.Black {
			in.Black = append(in.Black, Span{s.From, s.To})
		}
	}
	if s := evidence.LoadSeries(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]; s != nil {
		in.Loudness = &Loudness{Step: s.Step, Values: s.LUFS}
	}
	if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil && st.Index != nil {
		in.Looked = true
		for _, t := range st.Index.Text {
			in.Text = append(in.Text, Text{Text: t.Text, From: t.From, To: t.To, Box: [4]float64(t.Box), Changing: t.Changing})
		}
		for _, sh := range st.Index.Shots {
			in.Shots = append(in.Shots, Shot{Type: sh.Type, From: sh.From, To: sh.To, Faces: sh.Faces, People: sh.People})
		}
		sort.SliceStable(in.Text, func(i, j int) bool { return in.Text[i].From < in.Text[j].From })
	}
	in.Waiting = Pending(ctx, a, as.ID)
	in.CantHere = []string{}
	if as.Media.HasVideo && !in.Looked && !contains(in.Waiting, "what's on screen") && !ops.VisualsPossible(ctx, a) {
		in.CantHere = append(in.CantHere, "what's on screen (that needs MetaRush on a Mac)")
	}
	if as.Media.HasAudio && !in.Heard && !contains(in.Waiting, "what is said") {
		why := "what is said (not written down yet)"
		if as.Transcript != nil && as.Transcript.State == "failed" {
			why = "what is said (writing it down didn't work)"
		}
		in.CantHere = append(in.CantHere, why)
	}
	return in, nil
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
