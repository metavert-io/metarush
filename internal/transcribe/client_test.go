package transcribe

import (
	"context"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeModelServer is a model server whose process is this test binary in a
// fake mode (see TestMain): no sherpa-onnx needed.
func fakeModelServer(t *testing.T, mode string) *modelServer {
	t.Helper()
	t.Setenv(fakeServerEnv, mode)
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.Self) {
		t.Skip("cannot find the test binary")
	}
	s := &modelServer{t: ts, args: []string{ServerCommand, "--library", "lib", "--model", "model", "--threads", "1"}}
	t.Cleanup(func() {
		s.mu.Lock()
		s.stop()
		s.mu.Unlock()
	})
	return s
}

func running(s *modelServer) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.run != nil
}

// One process answers batch after batch, stops when idle and starts again for
// the next batch.
func TestModelServerStaysUpBetweenBatchesAndStopsWhenIdle(t *testing.T) {
	old := ResidentIdle
	ResidentIdle = 300 * time.Millisecond
	t.Cleanup(func() { ResidentIdle = old })
	s := fakeModelServer(t, "echo")
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		res, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}, {16, 32}})
		if err != nil || len(res) != 2 {
			t.Fatalf("batch %d: %v %v", i, res, err)
		}
	}
	if s.starts != 1 || s.batches != 3 || !running(s) {
		t.Fatalf("starts %d batches %d running %v", s.starts, s.batches, running(s))
	}
	deadline := time.Now().Add(5 * time.Second)
	for running(s) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if running(s) {
		t.Fatal("the idle server was not stopped")
	}
	if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); err != nil || s.starts != 2 {
		t.Fatalf("restart after idle: %v (starts %d)", err, s.starts)
	}
}

// A server that dies in the middle of a batch means "use the CLI" for that
// batch; after two in a row the model is not kept in memory any more.
func TestModelServerCrashMeansTheCLI(t *testing.T) {
	s := fakeModelServer(t, "crash")
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); !errors.Is(err, ErrResidentUnavailable) {
			t.Fatalf("crash %d: %v", i, err)
		}
	}
	if s.disabled == nil {
		t.Fatal("two crashes in a row should turn the resident model off")
	}
	if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); !errors.Is(err, ErrResidentUnavailable) || s.starts != 2 {
		t.Fatalf("after two crashes: %v (starts %d)", err, s.starts)
	}
}

// A server past the memory cap is restarted before its next batch.
func TestModelServerRestartsAboveTheMemoryCap(t *testing.T) {
	old := MaxServerMemory
	MaxServerMemory = 1 << 10 // the fake reports 1 MiB
	t.Cleanup(func() { MaxServerMemory = old })
	s := fakeModelServer(t, "echo")
	ctx := context.Background()
	if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); err != nil {
		t.Fatal(err)
	}
	if running(s) {
		t.Fatal("a server over the cap must stop after its batch")
	}
	if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); err != nil || s.starts != 2 {
		t.Fatalf("next batch: %v (starts %d)", err, s.starts)
	}
}

// Canceling a transcription ends the server at once (a batch cannot be
// interrupted otherwise).
func TestModelServerCanceledBatchEndsTheServer(t *testing.T) {
	s := fakeModelServer(t, "hang")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := s.decode(ctx, "audio.wav", [][2]int{{0, 16}}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("got %v", err)
	}
	if time.Since(start) > 5*time.Second || running(s) {
		t.Fatalf("the server was not ended promptly (%v, running %v)", time.Since(start), running(s))
	}
}

// With the real model installed, the resident model transcribes exactly what
// the CLI does (the full measurement is TestResidentMatchesCLI, -tags voiceeval).
func TestParakeetResidentMatchesCLI(t *testing.T) {
	wav := "../../tests/fixtures/gen/two-speakers.wav" // 16 kHz mono 16-bit (scripts/make-fixtures.sh)
	if _, err := os.Stat(wav); err != nil {
		t.Skip("fixtures not generated")
	}
	ts := tools.New(config.Tools{})
	cli, _ := newParakeet(ts, "", false).(*parakeet)
	res, _ := newParakeet(ts, "", true).(*parakeet)
	if cli == nil || res == nil {
		t.Skip("parakeet not installed")
	}
	if res.Mode() != ModeResident {
		t.Skip("the model cannot be kept in memory here (no sherpa-onnx C library of a known version)")
	}
	t.Cleanup(ReleaseResident)
	words := func(p *parakeet) ([]models.Word, string) {
		tr, err := p.Transcribe(context.Background(), wav, t.TempDir(), "en")
		if err != nil {
			t.Fatal(err)
		}
		var ws []models.Word
		for _, s := range tr.Segments {
			ws = append(ws, s.Words...)
		}
		return ws, tr.Engine
	}
	want, e1 := words(cli)
	got, e2 := words(res)
	if e1 != ModePerBatch || e2 != ModeResident {
		t.Fatalf("engines %s, %s (%s)", e1, e2, res.Fallback())
	}
	if len(want) < 50 || !reflect.DeepEqual(got, want) {
		t.Fatalf("resident %d words, CLI %d words: %s", len(got), len(want), firstDiffWords(got, want))
	}
	again, _ := words(res) // the second clip reuses the loaded model
	if !reflect.DeepEqual(again, want) {
		t.Fatal("a second transcription with the loaded model differs")
	}
	if st := ResidentStates(); len(st) != 1 || st[0].Starts != 1 || st[0].Batches != 2 {
		t.Fatalf("one server, two batches expected: %+v", st)
	}
}

func firstDiffWords(a, b []models.Word) string {
	for i := 0; i < min(len(a), len(b)); i++ {
		if a[i] != b[i] {
			return a[i].Text + " vs " + b[i].Text
		}
	}
	return "different lengths"
}
