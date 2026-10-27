package sources

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Descriptive names (D150) from what MetaRush knows: who's in it, what it's
// about, the words on screen, a link's title, the file's tags, when it was
// recorded; the file name stays when MetaRush can't tell.
func TestNames(t *testing.T) {
	day := time.Date(2026, 10, 12, 15, 40, 0, 0, time.Local)
	for _, c := range []struct {
		name string
		l    Look
		want string
	}{
		{"a call with two people", Look{What: whatCall, People: []string{"Lena", "Maya"}, Duration: 32 * 60}, "Lena's call with Maya (32 min)"},
		{"a call with one", Look{What: whatCall, People: []string{"Lena"}, Duration: 300}, "Call with Lena (5 min)"},
		{"a call nobody named", Look{What: whatCall, Recorded: day, Duration: 600}, "Call from 12 Oct 2026 (10 min)"},
		{"a call nothing is known about", Look{What: whatCall, Duration: 600}, ""},
		{"someone talks about something", Look{What: whatVideo, People: []string{"Lena"}, Topic: "the garden", Duration: 45}, "Lena talks about the garden (45 s)"},
		{"two people talk", Look{What: whatVideo, People: []string{"Lena", "Sam"}, Duration: 45}, "Lena and Sam talking (45 s)"},
		{"a voice memo", Look{What: whatVoice, People: []string{"James"}, Duration: 125}, "James' voice recording (2 min)"},
		{"a screen recording", Look{What: whatScreen, Screen: "QUARTERLY REVIEW", Duration: 70}, "Screen recording: Quarterly Review (70 s)"},
		{"a title on screen", Look{What: whatVideo, Screen: "TAKE A", Duration: 27}, "Take A (27 s)"},
		{"what it's about", Look{What: whatVideo, Topic: "the boat", Duration: 3700}, "Video about the boat (1 h 02 min)"},
		{"a link's title", Look{What: whatVideo, Link: "how we built the shed", People: []string{"Lena"}}, "How we built the shed"},
		{"music with tags", Look{What: whatMusic, TagTitle: "Morning light", TagArtist: "The Band"}, "Morning light by The Band"},
		{"music without", Look{What: whatMusic, FileName: "track01.mp3"}, ""},
		{"a camera file with its date", Look{What: whatVideo, CameraFile: true, Recorded: day, Duration: 130}, "Video from 12 Oct 2026, 3:40 pm (2 min)"},
		{"a camera file without", Look{What: whatVideo, CameraFile: true, Duration: 130}, ""},
		{"a generated shot keeps its name", Look{What: whatVideo, Keep: true, People: []string{"Lena"}}, ""},
	} {
		if got, why, _ := name(c.l); got != c.want || (got != "" && why == "") {
			t.Errorf("%s: %q (%q), want %q", c.name, got, why, c.want)
		}
	}
	for n, want := range map[string]bool{"IMG_4821.MOV": true, "DJI_0042.mp4": true, "PXL_20261012_154012.mp4": true, "New Recording 3.m4a": true,
		"kitchen chat.mov": false, "Interview with Sam.mp4": false} {
		if cameraName(n) != want {
			t.Errorf("cameraName(%q) = %v", n, !want)
		}
	}
}

// What a transcript is about: the words said most, in the speaker's own capitals.
func TestTopic(t *testing.T) {
	segs := []string{
		"So today we're working in the garden, the garden needs a lot of work.",
		"The tomatoes in the garden are doing well this year.",
		"I think the garden will look great by summer, and the tomatoes too.",
		"Let's water the garden before the sun gets high.",
	}
	if got := Topic(segs); got != "the garden" {
		t.Fatalf("topic %q", got)
	}
	if got := Topic([]string{"um yes", "okay so"}); got != "" {
		t.Fatalf("no topic in small talk: %q", got)
	}
}

func project(t *testing.T, a *app.App) string {
	t.Helper()
	now := time.Now().UTC()
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(context.Background(), store.CProjects, models.Project{ID: pid, WorkspaceID: a.WorkspaceID(), Name: "Shed film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return pid
}

func clip(t *testing.T, a *app.App, pid, file, linkTitle string, auto bool) *models.Asset {
	t.Helper()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{pid}, Kind: "video", Name: file, Title: models.DefaultTitle(file),
		TitleBy: models.TitleByFile, NameAuto: auto, SHA256: ids.New(ids.Asset), Size: 1000, MIME: "video/mp4", StorageKey: "k", State: models.AssetAvailable,
		Media: models.MediaInfo{Container: "mp4", Duration: 95, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if linkTitle != "" {
		as.Source = &models.AssetSource{Platform: "youtube", URL: "https://video.example/watch?v=1", Title: linkTitle}
	}
	if err := a.DB.Insert(context.Background(), store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return &as
}

func get(t *testing.T, a *app.App, id string) *models.Asset {
	t.Helper()
	var as models.Asset
	if err := a.DB.GetByID(context.Background(), store.CAssets, a.WorkspaceID(), id, &as); err != nil {
		t.Fatal(err)
	}
	return &as
}

// While its first look runs, MetaRush renames a source by itself; after it,
// a better name waits as a suggestion; a name a person typed is never
// replaced, and Use all keeps it too.
func TestRefreshAndUseAll(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	pid := project(t, a)

	// First look: the link's title becomes its name, and the name settles.
	first := clip(t, a, pid, "f8d2c1.mp4", "How we built the shed", true)
	if got, err := Refresh(ctx, a, first.ID); err != nil || got != "How we built the shed" {
		t.Fatalf("first look: %q %v", got, err)
	}
	if as := get(t, a, first.ID); as.TitleBy != models.TitleByMetaRush || as.NameAuto || as.Name != "f8d2c1.mp4" {
		t.Fatalf("named by MetaRush, the file name kept, settled: %+v", as)
	}

	// After the first look: a suggestion, not a rename.
	later := clip(t, a, pid, "f8d2c2.mp4", "Painting the shed", false)
	if got, _ := Refresh(ctx, a, later.ID); got != "f8d2c2" {
		t.Fatalf("renamed after its first look: %q", got)
	}
	if as := get(t, a, later.ID); as.NameSuggestion == nil || as.NameSuggestion.Title != "Painting the shed" || as.NameSuggestion.Why == "" {
		t.Fatalf("suggestion: %+v", as.NameSuggestion)
	}

	// A person's name stays, whatever MetaRush finds later, and no other name is offered over it.
	mine := clip(t, a, pid, "f8d2c3.mp4", "", false)
	if _, err := ops.CallTyped[ops.AssetLinkOut](ctx, a, "asset.update", ops.AssetUpdateIn{ID: mine.ID, Title: "Roof day"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": mine.ID}, bson.M{"$set": bson.M{"source": models.AssetSource{Platform: "youtube", Title: "Shed roof timelapse"}, "nameAuto": true}}); err != nil {
		t.Fatal(err)
	}
	if got, _ := Refresh(ctx, a, mine.ID); got != "Roof day" {
		t.Fatalf("a person's name was replaced: %q", got)
	}
	if as := get(t, a, mine.ID); as.TitleBy != models.TitleByPerson || as.NameSuggestion != nil {
		t.Fatalf("kept, with nothing offered over it: %+v", as)
	}
	// A title from before descriptive names (nobody recorded who chose it) that isn't its file name: Use all keeps it.
	older := clip(t, a, pid, "f8d2c4.mp4", "", false)
	sug := &models.NameSuggestion{Title: "Shed roof from above (95 s)", Why: "From the words on screen at the start.", At: time.Now().UTC()}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": older.ID}, bson.M{"$set": bson.M{"title": "Roof, take 2", "titleBy": "", "nameSuggestion": sug}}); err != nil {
		t.Fatal(err)
	}

	// Use all: MetaRush's names only; the person's stays.
	list, err := ops.CallTyped[NamesOut](ctx, a, "asset.names", NamesIn{ProjectID: pid}, ops.Caller{})
	if err != nil || len(list.Names) != 2 {
		t.Fatalf("names: %+v %v", list, err)
	}
	out, err := ops.CallTyped[NamesAcceptOut](ctx, a, "asset.names.accept", NamesAcceptIn{ProjectID: pid}, ops.Caller{Human: true})
	if err != nil || len(out.Renamed) != 1 || out.Renamed[0].AssetID != later.ID || len(out.Kept) != 1 || out.Kept[0].AssetID != older.ID {
		t.Fatalf("use all: %+v %v", out, err)
	}
	if as := get(t, a, later.ID); as.Title != "Painting the shed" || as.NameSuggestion != nil {
		t.Fatalf("renamed: %+v", as)
	}
	if as := get(t, a, mine.ID); as.Title != "Roof day" {
		t.Fatalf("the person's name must stay: %q", as.Title)
	}
	if as := get(t, a, older.ID); as.Title != "Roof, take 2" {
		t.Fatalf("an older name someone may have given must stay: %q", as.Title)
	}
	// One suggestion at a time: asset.update --suggested takes it, then there is none.
	if _, err := ops.CallTyped[ops.AssetLinkOut](ctx, a, "asset.update", ops.AssetUpdateIn{ID: later.ID, Suggested: true}, ops.Caller{Human: true}); err == nil {
		t.Fatal("no suggestion left to take")
	}
}
