package worker

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// oneSentence is a transcriber that always hears the same sentence, and
// remembers a fingerprint of each sound it was given.
type oneSentence struct{ heard []string }

func (s *oneSentence) Name() string  { return "fake" }
func (s *oneSentence) Model() string { return "same-words" }

func (s *oneSentence) Transcribe(_ context.Context, wav, _, _ string) (*transcribe.Transcript, error) {
	b, err := os.ReadFile(wav)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(b)
	s.heard = append(s.heard, hex.EncodeToString(sum[:]))
	words := []models.Word{{Text: "We", From: 0.2, To: 0.4, Score: 0.9}, {Text: "met", From: 0.5, To: 0.8, Score: 0.9},
		{Text: "Fern", From: 1.0, To: 1.3, Score: 0.6}, {Text: "Brook", From: 1.4, To: 1.8, Score: 0.6},
		{Text: "in", From: 2.0, To: 2.2, Score: 0.9}, {Text: "the", From: 2.3, To: 2.4, Score: 0.9}, {Text: "kitchen.", From: 2.5, To: 3.0, Score: 0.9}}
	return &transcribe.Transcript{Language: "en", Segments: []transcribe.Segment{{From: 0.2, To: 3.0, Text: "We met Fern Brook in the kitchen.", Words: words}}}, nil
}

// A person's word fix survives a sound fix (D149, D152): a voice memo is
// transcribed, someone writes "Fern Brook" the way the name is spelled, the hum is
// removed, and the transcription the fix asks for hears the fixed copy and
// keeps the person's "FernBrook" (fixes.reanalyze queues the ordinary transcribe
// job, which carries fixes over with transcripts.BeforeTranscribing and
// AfterTranscribing).
func TestSoundFixRetranscriptionKeepsWordFixes(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Kitchen film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	// A voice recording with a hum (the same sound as the fixes' own hum test).
	path := filepath.Join(t.TempDir(), "kitchen-voice.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t))+0.08*sin(2*PI*60*t)+0.05*sin(2*PI*120*t)+0.03*sin(2*PI*180*t):s=48000:d=8",
		"-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	r := ingest.File(ctx, a, path, ingest.Options{ProjectID: pid, Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	load := func() *models.Asset {
		t.Helper()
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &as); err != nil {
			t.Fatal(err)
		}
		return &as
	}

	w := New(a)
	tx := &oneSentence{}
	w.Tx, w.Langs, w.Spk = tx, nil, nil // no voices needed: the words are what is checked
	run := func(j *models.Job) map[string]any {
		t.Helper()
		out, err := transcribeHandler(ctx, w, j, func(float64, string) {})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	run(&models.Job{ID: ids.New(ids.Job), Type: jobs.TypeTranscribe, AssetID: r.AssetID, Payload: map[string]any{"assetId": r.AssetID}})

	// The person writes the name the way it is spelled.
	if _, err := ops.CallTyped[ops.TranscriptFixOut](ctx, a, "transcript.fix", ops.TranscriptFixIn{AssetID: r.AssetID, From: 0.95, To: 1.85, Text: "FernBrook"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}

	// The hum is measured and removed: the clip uses its fixed copy from then on.
	ij, err := fixes.QueueInspect(ctx, a, load(), false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fixes.Inspect(ctx, a, ij, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	v, err := ops.CallTyped[fixes.View](ctx, a, "asset.fixes.set", fixes.SetIn{ID: r.AssetID, Tick: []string{models.FixHum}}, ops.Caller{Human: true})
	if err != nil || len(v.JobIDs) != 1 {
		t.Fatalf("ticking the hum fix: %+v %v", v, err)
	}
	pj, err := a.Queue.Get(ctx, v.JobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	pj.AttemptCount = 1
	if _, err := fixes.Prepare(ctx, a, pj, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	if as := load(); !as.Fixed() || as.MediaSHA() == "" {
		t.Fatalf("the clip uses its fixed copy: %+v", as.Fixes)
	}

	// The fix asked for the words to be heard again: one transcribe job.
	var again []models.Job
	if err := a.DB.Find(ctx, store.CJobs, ws, bson.M{"assetId": r.AssetID, "type": jobs.TypeTranscribe, "payload.reason": "fixed"}, &again); err != nil {
		t.Fatal(err)
	}
	if len(again) != 1 {
		t.Fatalf("one transcription of the fixed copy, got %d", len(again))
	}
	out := run(&again[0])
	if out["fixesCarried"] != 1 || out["fixesMissed"] != 0 {
		t.Fatalf("the person's fix carries over to the new words: %+v", out)
	}
	if len(tx.heard) != 2 || tx.heard[0] == tx.heard[1] {
		t.Fatalf("the second transcription hears the fixed copy, not the original: %v", tx.heard)
	}
	segs, err := transcripts.LoadSegments(ctx, a, r.AssetID)
	if err != nil {
		t.Fatal(err)
	}
	var said []string
	fixed := false
	for _, s := range segs {
		for _, wd := range s.Words {
			said = append(said, wd.Text)
			fixed = fixed || (wd.Text == "FernBrook" && wd.Fixed)
		}
	}
	if !fixed || strings.Contains(strings.Join(said, " "), "Fern Brook") {
		t.Fatalf("today's words keep the person's spelling: %v", said)
	}
}
