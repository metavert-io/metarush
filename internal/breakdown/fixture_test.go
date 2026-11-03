package breakdown_test

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/breakdown"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/worker"
)

// referenceTruth is tests/fixtures/truth/reference-short.json.
type referenceTruth struct {
	Duration float64 `json:"duration_s"`
	Lines    []struct {
		Start float64 `json:"start_s"`
		End   float64 `json:"end_s"`
		Text  string  `json:"text"`
	} `json:"lines"`
	Cuts     []float64 `json:"cuts_s"`
	Sections []struct {
		Kind string  `json:"kind"`
		Name string  `json:"name"`
		From float64 `json:"from_s"`
		To   float64 `json:"to_s"`
	} `json:"sections"`
	Title struct {
		Text string `json:"text"`
	} `json:"title"`
	EndCard struct {
		Text string `json:"text"`
	} `json:"end_card"`
}

func fixturePath(t *testing.T, name string) string {
	t.Helper()
	p := filepath.Join("..", "..", "tests", "fixtures", "gen", name)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s not generated (scripts/make-fixtures.sh)", name)
	}
	return p
}

// The card of tests/fixtures/gen/reference-short.mp4 — a short in the usual
// shape — from MetaRush's own evidence job on the file (cuts, pauses and
// loudness measured by ffmpeg), its words as the fixture says them and, on a
// Mac that can build the on-screen helper, what Apple Vision reads in it: the
// seven sections at the truth's times, the pacing, the voice, the music under
// it, and the title, the burned-in captions and the end card's words.
func TestCardOfTheReferenceFixture(t *testing.T) {
	src := fixturePath(t, "reference-short.mp4")
	if !tools.New(config.Tools{}).Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	var truth referenceTruth
	b, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", "truth", "reference-short.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &truth); err != nil {
		t.Fatal(err)
	}
	a := testapp.New(t)
	ctx := context.Background()
	as := addFixture(t, a, src)
	seedWords(t, a, as.ID, truth)
	looked := false
	if ts, ok := visionHelper(t); ok {
		idx, err := visual.Run(ctx, ts, src, truth.Duration, nil, truth.Cuts, "original", t.TempDir(), nil, visual.RunOptions{})
		if err != nil {
			t.Fatal(err)
		}
		ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: "apple-vision", Model: "test",
			Data: visual.ToMap(idx), CreatedAt: time.Now().UTC()}
		if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
			t.Fatal(err)
		}
		looked = true
	}
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, as)
	out, err := breakdown.CardOf(ctx, a, as)
	if err != nil {
		t.Fatal(err)
	}
	c := out.Card
	for _, l := range c.Lines {
		t.Log(l)
	}
	if c.Shape != "vertical" || math.Abs(c.Length-truth.Duration) > 0.2 || c.Width != 720 || c.Height != 1280 {
		t.Errorf("length and shape: %v %s %dx%d", c.Length, c.Shape, c.Width, c.Height)
	}
	if len(c.Sections) != len(truth.Sections) {
		t.Fatalf("%d sections, want %d: %+v", len(c.Sections), len(truth.Sections), c.Sections)
	}
	for i, w := range truth.Sections {
		s := c.Sections[i]
		if s.Kind != w.Kind || s.Name != w.Name || math.Abs(s.From-w.From) > 0.35 || math.Abs(s.To-w.To) > 0.35 {
			t.Errorf("section %d: %s %s %.2f–%.2f, want %s %s %.2f–%.2f (%s)", i, s.Kind, s.Name, s.From, s.To, w.Kind, w.Name, w.From, w.To, s.Found)
		}
	}
	if s := c.Sections[0]; !s.Question {
		t.Errorf("the hook asks a question: %+v", s)
	}
	if s := c.Sections[1]; s.Does != "says what the video will show" {
		t.Errorf("the intro says what comes: %q", s.Does)
	}
	// Pacing: seven shots (the evidence finds the six cuts), 37 words.
	p := c.Pacing
	if p.Shots != 7 || p.Words != 37 || p.WordsPerMinute < 150 || p.WordsPerMinute > 220 || p.Pauses != 5 {
		t.Errorf("pacing: %+v", p)
	}
	if len(c.Speakers) != 1 {
		t.Errorf("one voice: %+v", c.Speakers)
	}
	// The chord bed plays under the voice and on over the end card.
	if s := c.Sound; s.Bed != breakdown.BedSound || s.Below < 8 || s.Below > 30 || !s.Throughout {
		t.Errorf("music under the voice: %+v", s)
	}
	if e := c.End; e == nil || math.Abs(e.From-22) > 0.35 || !e.Sound {
		t.Errorf("the end card: %+v", e)
	}
	if !looked {
		if c.Captions.Looked || len(c.Graphics) != 0 {
			t.Errorf("nothing looked at, nothing said about the screen: %+v %+v", c.Captions, c.Graphics)
		}
		return
	}
	// What Apple Vision reads: the captions, low, about three words in capitals;
	// the title at the top over the hook; the end card's words.
	if cp := c.Captions; !cp.Burned || cp.Position != "lower" || !cp.Capitals || cp.WordsAtATime < 2 || cp.WordsAtATime > 3 || cp.Covers < 0.6 || cp.Look != "bold" {
		t.Errorf("captions: %+v", cp)
	}
	// OCR may read a line in part (the 5×7 letters' last word): the first word counts.
	read := func(kind, text string) bool {
		first := strings.Fields(text)[0]
		for _, g := range c.Graphics {
			if g.Kind == kind && strings.HasPrefix(strings.ToUpper(g.Text), first) {
				return true
			}
		}
		return false
	}
	if !read(breakdown.GraphicTitle, truth.Title.Text) || !read(breakdown.GraphicEndCard, truth.EndCard.Text) {
		t.Errorf("the title and the end card's words: %+v", c.Graphics)
	}
	if s := c.Sections[0]; !strings.HasPrefix(s.Does, "a question to camera") {
		t.Errorf("the hook is a question to camera: %q", s.Does)
	}
}

// addFixture adds a file like a person's drop and runs MetaRush's evidence job
// on it (ffmpeg: cuts, pauses, loudness).
func addFixture(t *testing.T, a *app.App, src string) *models.Asset {
	t.Helper()
	ctx := context.Background()
	res := ingest.File(ctx, a, src, ingest.Options{Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("add %s: %+v", src, res)
	}
	var js []models.Job
	if err := a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), bson.M{"assetId": res.AssetID, "type": jobs.TypeEvidence}, &js); err != nil || len(js) == 0 {
		t.Fatalf("no evidence job: %v", err)
	}
	w := worker.New(a)
	w.OnlyJobID = js[0].ID
	w.Log = func(string, ...any) {}
	wctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	if err := w.Run(wctx); err != nil {
		t.Fatalf("evidence: %v", err)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), res.AssetID, &as); err != nil {
		t.Fatal(err)
	}
	if as.Analysis["evidence"] != "available" {
		t.Fatalf("the evidence job didn't finish: %+v", as.Analysis)
	}
	return &as
}

// seedWords writes the fixture's lines as its transcript, each line's words
// spread over it (the recognizer isn't part of this test).
func seedWords(t *testing.T, a *app.App, assetID string, truth referenceTruth) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	words := 0
	for i, l := range truth.Lines {
		ts := strings.Fields(l.Text)
		step := (l.End - l.Start) / float64(len(ts))
		var ws []models.Word
		for k, w := range ts {
			f := l.Start + step*float64(k)
			ws = append(ws, models.Word{Text: w, From: math.Round(f*1000) / 1000, To: math.Round((f+step-0.05)*1000) / 1000})
		}
		words += len(ws)
		seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Index: i, From: l.Start, To: l.End, Text: l.Text,
			Speaker: "speaker_0", Words: ws, Provider: "test", Model: "truth", CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: len(truth.Lines), Words: words, Speakers: 1, UpdatedAt: now}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		t.Fatal(err)
	}
	// The transcript job the drop queued is done (this test wrote the words).
	_, _ = a.DB.C(store.CJobs).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}}},
		bson.M{"$set": bson.M{"state": models.JobSucceeded}})
}

// visionHelper builds the on-screen helper on a Mac with swiftc (or uses
// METARUSH_VISION); elsewhere the test goes on without it.
func visionHelper(t *testing.T) (*tools.Set, bool) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return nil, false
	}
	cfg := config.Tools{Vision: os.Getenv("METARUSH_VISION")}
	ts := tools.New(cfg)
	if cfg.Vision != "" {
		return ts, true
	}
	if !ts.Has(tools.Swiftc) {
		return nil, false
	}
	dir := t.TempDir()
	if _, err := vision.Build(context.Background(), ts, dir); err != nil {
		t.Logf("the on-screen helper didn't build here: %v", err)
		return nil, false
	}
	return ts, true
}
