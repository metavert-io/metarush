package e2e

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/worker"
)

type turnTruth struct {
	Speaker string  `json:"speaker"`
	Start   float64 `json:"start_s"`
	End     float64 `json:"end_s"`
}

// speechFixtures needs real local speech tools and the generated fixtures.
func speechFixtures(t *testing.T, ts *tools.Set) (string, []turnTruth) {
	t.Helper()
	wav := filepath.Join("..", "fixtures", "gen", "two-speakers.wav")
	if _, err := os.Stat(wav); err != nil {
		t.Skip("tests/fixtures/gen/two-speakers.wav missing (scripts/make-fixtures.sh)")
	}
	if transcribe.NewDiarizer(ts) == nil || transcribe.Select(config.Transcription{Provider: "auto"}, ts) == nil {
		t.Skip("local transcription / speaker detection not installed")
	}
	b, err := os.ReadFile(filepath.Join("..", "fixtures", "truth", "two-speakers.json"))
	if err != nil {
		t.Fatal(err)
	}
	var truth struct {
		Turns []turnTruth `json:"turns"`
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	return wav, truth.Turns
}

// cut writes [from, to) of a WAV as an m4a "video" (audio only).
func cut(t *testing.T, ts *tools.Set, src, dst string, from, to float64) string {
	t.Helper()
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-y", "-i", src,
		"-ss", ftoa(from), "-to", ftoa(to), "-c:a", "aac", "-b:a", "128k", dst}}); err != nil {
		t.Fatal(err)
	}
	return dst
}

func ftoa(f float64) string { b, _ := json.Marshal(f); return string(b) }

// process ingests a file and runs its jobs (transcription must succeed).
func process(t *testing.T, a *app.App, path, project string) string {
	t.Helper()
	r := ingest.File(context.Background(), a, path, ingest.Options{ProjectID: project, Transcribe: true})
	if r.Status != "ingested" {
		t.Fatalf("ingest %s: %s %s", path, r.Status, r.Error)
	}
	waitAll(t, a, r.JobIDs)
	return r.AssetID
}

func waitAll(t *testing.T, a *app.App, jobIDs []string) {
	t.Helper()
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	w := worker.New(a)
	w.IgnoreBattery = true
	w.Log = func(string, ...any) {}
	go func() { _ = w.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Minute)
	for {
		done := 0
		for _, id := range jobIDs {
			j, _ := a.Queue.Get(ctx, id)
			switch j.State {
			case models.JobSucceeded:
				done++
			case models.JobFailed, models.JobCanceled:
				t.Fatalf("job %s %s %s: %s", j.ID, j.Type, j.State, j.Error)
			}
		}
		if done == len(jobIDs) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("jobs did not finish")
		}
		time.Sleep(300 * time.Millisecond)
	}
}

// truthAt is who really speaks at source time t of the cut starting at offset.
func truthAt(turns []turnTruth, offset, at float64) string {
	for _, tt := range turns {
		if at+offset >= tt.Start-0.05 && at+offset <= tt.End+0.05 {
			return tt.Speaker
		}
	}
	return ""
}

// Real speech, end to end: two recordings of the same two people. Naming one
// voice per person in the first recording labels the second automatically;
// a speaker-count hint re-detects without re-transcribing; names come back.
func TestSpeakersAcrossRecordings(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	wav, turns := speechFixtures(t, ts)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("voice fingerprints need the sherpa-onnx C library (macOS/Linux)")
	}
	ctx := context.Background()
	dir := t.TempDir()
	// Recording 1 = turns 1–4, recording 2 = turns 5–8 (different sentences).
	split := (turns[3].End + turns[4].Start) / 2
	first := cut(t, ts, wav, filepath.Join(dir, "first.m4a"), 0, split)
	second := cut(t, ts, wav, filepath.Join(dir, "second.m4a"), split, turns[len(turns)-1].End+0.4)
	proj := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Two voices"})
	a1 := process(t, a, first, proj.Project.ID)
	a2 := process(t, a, second, proj.Project.ID)

	sp1 := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: a1})
	if sp1.Status == nil || sp1.Status.State != "available" || len(sp1.Speakers) != 2 || !sp1.Status.Fingerprints {
		t.Fatalf("recording 1 should have 2 fingerprinted voices: %+v (%d voices)", sp1.Status, len(sp1.Speakers))
	}
	for _, v := range sp1.Speakers {
		if len(v.Samples) == 0 || v.Samples[0].To-v.Samples[0].From < 1.5 {
			t.Fatalf("each voice needs a listenable sample: %+v", v.Samples)
		}
	}
	todos := call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{AssetID: a1})
	if len(todos.Todos) != 2 || todos.Todos[0].Kind != models.TodoSpeakerUnnamed {
		t.Fatalf("want two 'who is this?' questions, got %+v", todos.Todos)
	}

	// Name one voice per person in recording 1 (S1 speaks first).
	names := map[string]string{"S1": "Samantha", "S2": "Daniel"}
	for _, v := range sp1.Speakers {
		who := truthAt(turns, 0, (v.Samples[0].From+v.Samples[0].To)/2)
		if who == "" {
			t.Fatalf("sample %+v is not inside a reference turn", v.Samples[0])
		}
		call[ops.SpeakerRenameOut](t, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: a1, Speaker: v.Label, Name: names[who]})
	}

	// Recording 2 is labeled automatically, word by word correctly.
	sp2 := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: a2})
	for _, v := range sp2.Speakers {
		if v.Source != models.SpeakerByAuto || v.Name == "" {
			t.Fatalf("recording 2 voice %s not recognized: name %q source %q suggestions %+v", v.Label, v.Name, v.Source, v.Suggestions)
		}
	}
	tr := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: a2, Words: true})
	right, total := 0, 0
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			if who := truthAt(turns, split, (w.From+w.To)/2); who != "" {
				total++
				if names[who] == s.SpeakerName {
					right++
				}
			}
		}
	}
	if total == 0 || float64(right)/float64(total) < 0.95 {
		t.Fatalf("recording 2 words labeled correctly: %d/%d", right, total)
	}
	if open := call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{ProjectID: proj.Project.ID}); len(open.Todos) != 0 {
		t.Fatalf("no questions should remain, got %+v", open.Todos)
	}
	// Only what Daniel says.
	dan := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: a2, Speaker: "Daniel"})
	if len(dan.Segments) == 0 {
		t.Fatal("speaker filter returned nothing")
	}
	for _, s := range dan.Segments {
		if s.SpeakerName != "Daniel" {
			t.Fatalf("speaker filter leaked %q", s.SpeakerName)
		}
	}

	// A count hint re-detects voices without re-transcribing; names come back
	// from the voice profiles.
	before := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: a1})
	h := call[ops.SpeakerHintOut](t, a, "footage.speakers.hint", ops.SpeakerHintIn{AssetID: a1, Count: 2})
	waitAll(t, a, []string{h.JobID})
	after := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: a1})
	if after.Status.Hint != 2 || len(after.Speakers) != 2 || after.Status.Named != 2 {
		t.Fatalf("hint re-run: %+v", after.Status)
	}
	tr1 := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: a1})
	if tr1.Status.Words != before.Status.Words || tr1.Status.UpdatedAt != before.Status.UpdatedAt {
		t.Fatal("a speaker hint must not re-transcribe")
	}
	_ = ctx
}

// Whisper.cpp transcripts get speakers exactly like Parakeet ones.
func TestWhisperTranscriptsGetSpeakers(t *testing.T) {
	in, ts := setup(t)
	if !ts.Has(tools.Whisper) {
		t.Skip("whisper.cpp not installed")
	}
	wav, _ := speechFixtures(t, ts)
	a := in.app
	cfg := *a.Cfg
	cfg.Transcription = config.Transcription{Provider: "whispercpp", Language: "en"}
	if transcribe.Select(cfg.Transcription, ts) == nil {
		t.Skip("no whisper model installed")
	}
	a.Cfg = &cfg
	proj := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Whisper"})
	id := process(t, a, cut(t, ts, wav, filepath.Join(t.TempDir(), "w.m4a"), 0, 29), proj.Project.ID)
	sp := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: id})
	tr := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: id})
	if tr.Status.Provider != "whisper.cpp" || len(sp.Speakers) != 2 {
		t.Fatalf("whisper transcript should have 2 voices: provider %s, %d voices", tr.Status.Provider, len(sp.Speakers))
	}
	labeled := 0
	for _, s := range tr.Segments {
		if s.Speaker != "" {
			labeled++
		}
	}
	if labeled != len(tr.Segments) {
		t.Fatalf("every whisper segment needs a speaker: %d/%d", labeled, len(tr.Segments))
	}
}
