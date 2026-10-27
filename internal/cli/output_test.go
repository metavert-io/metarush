package cli

import (
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/activity"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// stdout captures what fn prints.
func stdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdout
	os.Stdout = w
	done := make(chan string)
	go func() {
		b, _ := io.ReadAll(r)
		done <- string(b)
	}()
	fn()
	w.Close()
	os.Stdout = old
	return <-done
}

// The plain output speaks to a person: no IDs, links or commands unless
// --details asks for them (the voice rule); --json carries everything.
func TestHumanOutputKeepsIDsBehindDetails(t *testing.T) {
	pct, half := 18, 0.5
	s := &activity.Summary{Headline: "Processing 1 file, 1 transcription paused: battery at 18% on studio-mac", Progress: &half,
		Items: []activity.Item{{Key: "asset:ast_123", Category: activity.CatProcessing, Title: "Garage interview", StageLabel: "Processing", State: activity.StateFailed,
			Pct: &half, Error: "Transcript: sherpa-onnx-offline failed: exit status 1", CanRetry: true, RetryJobIDs: []string{"job_abc"}, Link: "/?asset=ast_123",
			Steps: []activity.Step{{Kind: "transcribe", Label: "Transcript", State: "failed", JobID: "job_abc"}}}},
		Waiting: []activity.WaitGroup{{Code: jobs.WaitBattery, Text: "1 transcription paused: battery at 18% on studio-mac", Count: 1, CanProcessNow: true, JobIDs: []string{"job_def"}}},
		Workers: []activity.WorkerBrief{{Name: "studio-mac", BatteryPercent: &pct, OnBattery: true, Threshold: 25, Concurrency: 2, This: true}}}
	plain := stdout(t, func() { printActivity(s, false) })
	for _, banned := range []string{"job_abc", "job_def", "ast_123", "metarush ", "/?asset", "sherpa"} {
		if strings.Contains(plain, banned) {
			t.Errorf("plain activity output shows %q:\n%s", banned, plain)
		}
	}
	for _, want := range []string{"Garage interview", "Transcript failed", "1 transcription paused", "Machines", "studio-mac", "Retry or start these from Activity in the dashboard."} {
		if !strings.Contains(plain, want) {
			t.Errorf("plain activity output lacks %q:\n%s", want, plain)
		}
	}
	detailed := stdout(t, func() { printActivity(s, true) })
	for _, want := range []string{"metarush job retry job_abc", "metarush job run-now job_def", "/?asset=ast_123", "error: Transcript: sherpa"} {
		if !strings.Contains(detailed, want) {
			t.Errorf("--details output lacks %q:\n%s", want, detailed)
		}
	}

	fin := 12.0
	j := &ops.JobShowOut{Job: models.Job{ID: "job_xyz", Type: "transcribe", State: models.JobFailed, Error: "sherpa: model file not found", CreatedAt: time.Now()},
		Title: "Transcribing “Garage interview”", StartedBy: "You, in the dashboard", Machine: "studio-mac", DurationSeconds: &fin,
		Advice: "The transcriber on that machine is missing a file it needs.", CanRetry: true,
		Target: &ops.JobTarget{Label: "Go to the clip", Link: "/?asset=ast_123", Kind: "clip"}}
	plainJob := stdout(t, func() { printJob(j, false) })
	if strings.Contains(plainJob, "job_xyz") || strings.Contains(plainJob, "/?asset") || strings.Contains(plainJob, "metarush ") || strings.Contains(plainJob, "sherpa") {
		t.Errorf("plain job output shows technical detail:\n%s", plainJob)
	}
	if !strings.Contains(plainJob, "Failed, started by you, in the dashboard, on studio-mac, took 12s") || !strings.Contains(plainJob, "Go to the clip in the dashboard.") {
		t.Errorf("plain job output:\n%s", plainJob)
	}
	if d := stdout(t, func() { printJob(j, true) }); !strings.Contains(d, "retry: metarush job retry job_xyz") || !strings.Contains(d, "/?asset=ast_123") {
		t.Errorf("--details job output:\n%s", d)
	}
}

func TestDescribeDuplicateIsPlain(t *testing.T) {
	d := &models.DuplicateOf{AssetID: "ast_1", Title: "Beach", Where: "Inbox", Action: models.DuplicateKeptBoth}
	if got := describeDuplicate(d); got != "identical to “Beach” (in Inbox); kept both names, stored once" {
		t.Fatalf("kept both: %q", got)
	}
	d.Action = models.DuplicateSkipped
	if got := describeDuplicate(d); strings.Contains(got, "ast_1") || strings.Contains(got, "--") {
		t.Fatalf("skipped: %q", got)
	}
}

// Speakers and to-dos speak plainly too: IDs and commands only with --details.
func TestSpeakersAndTodosOutputKeepIDsBehindDetails(t *testing.T) {
	sp := &ops.SpeakersOut{AssetID: "ast_777", Name: "MVI_0042.MP4", Title: "Kitchen chat", Summary: "2 voices in “Kitchen chat”: Ada, plus Speaker 2 not named yet.",
		Speakers: []ops.SpeakerRow{{AssetSpeaker: models.AssetSpeaker{Label: "speaker_00", Name: "Ada", Seconds: 40, Source: models.SpeakerByPerson}, DisplayName: "Ada"},
			{AssetSpeaker: models.AssetSpeaker{Label: "speaker_01", Seconds: 12}, DisplayName: "Speaker 2"}}}
	plain := stdout(t, func() { printSpeakers(sp, false) })
	for _, banned := range []string{"ast_777", "metarush ", "speaker_01"} {
		if strings.Contains(plain, banned) {
			t.Errorf("plain speakers output shows %q:\n%s", banned, plain)
		}
	}
	if !strings.Contains(plain, "Speaker 2") || !strings.Contains(plain, "Speakers tab") {
		t.Errorf("plain speakers output:\n%s", plain)
	}
	if d := stdout(t, func() { printSpeakers(sp, true) }); !strings.Contains(d, "metarush footage speakers rename ast_777") {
		t.Errorf("--details speakers output:\n%s", d)
	}

	td := models.Todo{ID: "tdo_123", Kind: models.TodoSpeakerUnnamed, AssetID: "ast_777", Question: "Who is Speaker 2 in “Kitchen chat”?", State: models.TodoOpen, FreeText: true,
		Context:     models.TodoContext{AssetName: "MVI_0042.MP4", AssetTitle: "Kitchen chat", Samples: []models.TimeSpan{{From: 3, To: 6}}},
		Suggestions: []models.TodoSuggestion{{Label: "Bo (in this project)", Value: "Bo"}}}
	list := &ops.TodoListOut{Todos: []models.Todo{td}, Summary: "1 question waiting for you."}
	plainList := stdout(t, func() { printTodos(list, false) })
	plainOne := stdout(t, func() {
		printTodo(&ops.TodoShowOut{Todo: td, HowToAnswer: "Pick an answer: 1) Bo (in this project). Or type your own answer."}, false)
	})
	for _, out := range []string{plainList, plainOne} {
		for _, banned := range []string{"tdo_123", "ast_777", "metarush ", "MVI_0042"} {
			if strings.Contains(out, banned) {
				t.Errorf("plain to-do output shows %q:\n%s", banned, out)
			}
		}
		if !strings.Contains(out, "To-dos page") {
			t.Errorf("plain to-do output should say where to answer:\n%s", out)
		}
	}
	if !strings.Contains(plainOne, "in “Kitchen chat”") {
		t.Errorf("listening samples name the clip by its title:\n%s", plainOne)
	}
	if d := stdout(t, func() { printTodos(list, true) }); !strings.Contains(d, "metarush todos show tdo_123") {
		t.Errorf("--details to-do list:\n%s", d)
	}

	j := &ops.JobShowOut{Job: models.Job{ID: "job_9", Type: "diarize", AssetID: "ast_777", State: models.JobSucceeded, CreatedAt: time.Now()},
		Title: "Finding who's speaking in “Kitchen chat”", StartedBy: "You, in the dashboard",
		Questions: &models.OpenQuestions{Count: 2, Text: "2 questions about this clip", Link: "/todos?asset=ast_777"}}
	plainJob := stdout(t, func() { printJob(j, false) })
	if !strings.Contains(plainJob, "2 questions about this clip, waiting on the To-dos page.") || strings.Contains(plainJob, "/todos") || strings.Contains(plainJob, "ast_777") {
		t.Errorf("plain job output:\n%s", plainJob)
	}
	if d := stdout(t, func() { printJob(j, true) }); !strings.Contains(d, "/todos?asset=ast_777") {
		t.Errorf("--details job output:\n%s", d)
	}
}
