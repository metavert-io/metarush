package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// synthetic voices: one dominant direction per person
func testVoice(label string, person int, from, to float64) voice.Cluster {
	c := voice.Cluster{Label: label, Seconds: to - from, Embedded: to - from - 0.3, Turns: 1, First: from, Samples: []voice.Span{{From: from + 0.2, To: math.Min(to, from+4)}}}
	for k := 0; k < 4; k++ {
		v := make([]float32, 32)
		for i := range v {
			v[i] = float32(0.05 * math.Sin(float64(person*97+i*13)))
		}
		v[person%32] += 1
		v[(k*7)%32] += 0.1
		voice.Normalize(v)
		c.Exemplars = append(c.Exemplars, voice.Exemplar{Span: voice.Span{From: from, To: from + 2.5}, Vec: v})
	}
	vs := [][]float32{}
	for _, e := range c.Exemplars {
		vs = append(vs, e.Vec)
	}
	c.Centroid = voice.Mean(vs, nil)
	return c
}

// Names reach what Claude reads (transcript filter by name) and what viewers
// see (captions with showSpeakers); the count hint queues a re-detection.
func TestSpeakerNamesInTranscriptAndCaptions(t *testing.T) {
	a, project, video, _ := todoApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	segs := []struct {
		from, to    float64
		label, text string
	}{{0.5, 2.5, "speaker_00", "So what did you build?"}, {3, 6, "speaker_01", "A robot that waters plants."}, {6.5, 8, "speaker_00", "Why?"}}
	for i, s := range segs {
		_ = a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: video, Index: i, From: s.from, To: s.to,
			Text: s.text, Speaker: s.label, Words: []models.Word{{Text: s.text, From: s.from, To: s.to}}, CreatedAt: now})
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), video, bsonSet("transcript", models.TranscriptStatus{State: "available", Precision: "word", Segments: 3, Words: 3}))
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), video, &as)
	det := &speakers.Detection{Run: ids.New(ids.Run), Result: &voice.Result{Raw: 2, Clusters: []voice.Cluster{testVoice("speaker_00", 1, 0.5, 8), testVoice("speaker_01", 2, 3, 6)}}, Decision: map[string]voice.Decision{}, Prints: true}
	if err := speakers.Store(ctx, a, &as, det, "test"); err != nil {
		t.Fatal(err)
	}
	ren, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: video, Speaker: "Speaker 2", Name: "Ada"}, Caller{})
	if err != nil || ren.Person.Name != "Ada" || ren.Voice.Label != "speaker_01" {
		t.Fatalf("rename: %+v %v", ren, err)
	}
	only, err := CallTyped[TranscriptOut](ctx, a, "footage.transcript", TranscriptIn{AssetID: video, Speaker: "ada"}, Caller{})
	if err != nil || len(only.Segments) != 1 || only.Segments[0].SpeakerName != "Ada" || len(only.Speakers) != 2 {
		t.Fatalf("speaker filter: %+v %v", only, err)
	}
	if _, err := CallTyped[TranscriptOut](ctx, a, "footage.transcript", TranscriptIn{AssetID: video, Speaker: "Nobody"}, Caller{}); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown speaker should be E_NOT_FOUND, got %v", err)
	}

	plan := json.RawMessage(fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},
		"clips":[{"id":"a","assetId":%q,"in":0,"out":8}],"captions":{"enabled":true,"showSpeakers":true}}}`, project, video))
	chk, err := CallTyped[EditCheckOut](ctx, a, "edit.check", EditCheckIn{Plan: plan}, Caller{})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %v %+v", err, chk)
	}
	ap, err := CallTyped[EditApplyOut](ctx, a, "edit.apply", EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	rp, err := CallTyped[editspec.RenderPlan](ctx, a, "revision.plan", RevisionPlanIn{ID: ap.Revision.ID}, Caller{})
	if err != nil || rp.Captions == nil || !rp.Captions.ShowSpeakers {
		t.Fatalf("plan captions: %+v %v", rp.Captions, err)
	}
	who := map[string]bool{}
	for _, l := range rp.Captions.Lines {
		who[l.Speaker] = true
	}
	if !who["Ada"] || !who["Speaker 1"] {
		t.Fatalf("caption lines should carry speaker names: %v", who)
	}

	// A count hint stores the number and queues a re-detection that keeps the words.
	h, err := CallTyped[SpeakerHintOut](ctx, a, "footage.speakers.hint", SpeakerHintIn{AssetID: video, Count: 3}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := a.Queue.Get(ctx, h.JobID)
	if err != nil || j.Type != jobs.TypeDiarize || j.AssetID != video {
		t.Fatalf("hint job: %+v %v", j, err)
	}
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), video, &as)
	if as.Speakers == nil || as.Speakers.Hint != 3 {
		t.Fatalf("hint not stored: %+v", as.Speakers)
	}
	if _, err := CallTyped[SpeakerHintOut](ctx, a, "footage.speakers.hint", SpeakerHintIn{AssetID: video, Count: 40}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("absurd counts are refused: %v", err)
	}
	// people show resolves names; merge needs two different people.
	ps, err := CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "ada"}, Caller{})
	if err != nil || ps.Person.Name != "Ada" || len(ps.Person.Appearances) != 1 {
		t.Fatalf("people.show: %+v %v", ps, err)
	}
	if _, err := CallTyped[PersonOut](ctx, a, "people.merge", PersonMergeIn{From: "Ada", Into: "Ada"}, Caller{}); err == nil {
		t.Fatal("merging a person into themselves must fail")
	}
}
