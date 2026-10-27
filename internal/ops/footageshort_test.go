package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A short from any footage (D246): checked first with a confirm token, then a
// new cut of the project for phones (tall) framed as the house style says
// (faces without one) with big and bold captions, named from its first words; the next
// short takes the next free name; stretches too short, too long or outside
// the clip, and unknown looks are refused plainly.
func TestFootageShort(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	now := time.Now().UTC()
	walk := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "walk.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 120, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now,
		Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Language: "en", Segments: 1, Words: 6}})
	insert(t, a, store.CAssets, walk)
	insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: a.WorkspaceID(), AssetID: walk.ID, From: 12, To: 15,
		Text: "The first tomato of the summer.", Words: []models.Word{{Text: "The", From: 12, To: 12.2}, {Text: "first", From: 12.2, To: 12.6},
			{Text: "tomato", From: 12.6, To: 13.2}, {Text: "of", From: 13.2, To: 13.3}, {Text: "the", From: 13.3, To: 13.4}, {Text: "summer.", From: 13.4, To: 14}}})

	in := FootageShortIn{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 12, To: 41}}, Why: "The first harvest, said with joy."}
	dry := in
	dry.DryRun = true
	d := call[FootageShortOut](t, a, "footage.short", dry)
	if d.Saved || d.Check == nil || !d.Check.Valid || d.Check.ConfirmToken == "" || d.Branch != "short" || d.Seconds != 29 ||
		!strings.Contains(d.Summary, "29 s short for phones (tall)") || !strings.HasPrefix(d.Name, "Short: The first tomato of the summer.") {
		t.Fatalf("dry run: %+v %+v", d, d.Check)
	}
	if !strings.Contains(strings.Join(d.Details, "|"), "The crop keeps faces in frame") || !strings.Contains(strings.Join(d.Details, "|"), "“Big and bold”") {
		t.Fatalf("details: %v", d.Details)
	}
	// Outside the dashboard it needs the token.
	if again := call[FootageShortOut](t, a, "footage.short", in); again.Saved {
		t.Fatal("made without a confirm token")
	}
	in.ConfirmToken = d.Check.ConfirmToken
	made := call[FootageShortOut](t, a, "footage.short", in)
	if !made.Saved || made.Applied == nil || made.Link == "" {
		t.Fatalf("made: %+v", made)
	}
	s := specOf(t, a, made.Applied.Revision.ID)
	if s.Output.Preset != editspec.PresetVertical || s.Output.Framing != editspec.FramingFaces || s.Output.Platform != "shorts" ||
		len(s.Clips) != 1 || s.Clips[0].In != 12 || s.Clips[0].Out != 41 || s.Captions == nil || s.Captions.Style != editspec.CaptionBold {
		t.Fatalf("the short's cut: %+v %+v", s.Output, s.Captions)
	}
	var proj models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.Project.ID, &proj)
	if b := proj.Branches["short"]; b.Head != made.Applied.Revision.ID || b.Title != made.Name {
		t.Fatalf("the cut is named: %+v", b)
	}
	if _, ok := proj.Branches["main"]; ok {
		t.Fatal("the main cut is untouched")
	}
	// The next one is short-2, square, two stretches, no captions.
	two := call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Shape: "square", Captions: boolp(false), DryRun: true,
		Pieces: []ShortPiece{{AssetID: walk.ID, From: 50, To: 58}, {AssetID: walk.ID, From: 70, To: 75}}})
	if two.Branch != "short-2" || two.Seconds != 13 || !strings.Contains(two.Summary, "square") || !strings.Contains(strings.Join(two.Details, "|"), "No captions") {
		t.Fatalf("second: %+v", two)
	}
	for _, bad := range []FootageShortIn{
		{ProjectID: p.Project.ID},
		{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 10, To: 11.5}}},
		{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 0, To: 120}, {AssetID: walk.ID, From: 0, To: 100}}},
		{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 130, To: 140}}},
		{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 12, To: 41}}, Look: "glitter"},
		{ProjectID: p.Project.ID, Pieces: []ShortPiece{{AssetID: walk.ID, From: 12, To: 41}}, Shape: "landscape"},
	} {
		if _, err := CallTyped[FootageShortOut](ctx, a, "footage.short", bad, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("%+v: %v", bad, err)
		}
	}
	if _, err := CallTyped[FootageShortOut](ctx, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Branch: "short", Pieces: in.Pieces}, Caller{}); apperr.From(err).Code != apperr.Conflict {
		t.Errorf("a taken name: %v", err)
	}
}

// Following movement is only a person's choice (D115): a short follows only
// when asked; with nothing asked it takes the house style's framing. A
// Japanese short is named from its first words joined as Japanese writes
// them (D244).
func TestFootageShortFramingAndJapaneseName(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Mermaid"})
	now := time.Now().UTC()
	clip := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "ningyo.mov",
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 60, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now,
		Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Language: "ja", Segments: 1, Words: 6}})
	insert(t, a, store.CAssets, clip)
	insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: a.WorkspaceID(), AssetID: clip.ID, From: 2, To: 5,
		Text: "人魚は、南の方の海に", Words: []models.Word{{Text: "人魚", From: 2, To: 2.4}, {Text: "は、", From: 2.4, To: 2.6},
			{Text: "南", From: 2.8, To: 3}, {Text: "の", From: 3, To: 3.1}, {Text: "方", From: 3.1, To: 3.3}, {Text: "の", From: 3.3, To: 3.4}}})
	pieces := []ShortPiece{{AssetID: clip.ID, From: 2, To: 20}}
	d := call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Pieces: pieces, DryRun: true})
	if !strings.HasPrefix(d.Name, "Short: 人魚は、南の方の…") {
		t.Errorf("a Japanese name: %q", d.Name)
	}
	// The house style says center: a short nobody asked to follow keeps it.
	if _, err := CallTyped[ContentStyleOut](ctx, a, "content.style.set", ContentStyleSetIn{ProjectID: p.Project.ID, Framing: strp("center")}, Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	d = call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Pieces: pieces, DryRun: true})
	if !strings.Contains(strings.Join(d.Details, "|"), "keeps the middle") {
		t.Errorf("the house style's framing is said: %v", d.Details)
	}
	made := call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Pieces: pieces, ConfirmToken: d.Check.ConfirmToken})
	if s := specOf(t, a, made.Applied.Revision.ID); s.Output.Framing != editspec.FramingCenter {
		t.Fatalf("the house style's framing: %q", s.Output.Framing)
	}
	// Asked for, it follows.
	f := call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Pieces: pieces, Framing: "follow", DryRun: true})
	f = call[FootageShortOut](t, a, "footage.short", FootageShortIn{ProjectID: p.Project.ID, Pieces: pieces, Framing: "follow", ConfirmToken: f.Check.ConfirmToken})
	if s := specOf(t, a, f.Applied.Revision.ID); s.Output.Framing != editspec.FramingFollow {
		t.Fatalf("asked to follow: %q", s.Output.Framing)
	}
}

func strp(v string) *string { return &v }
