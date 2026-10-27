package ops

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// oneVoiceClip adds a clip of one person (synthetic voice `person`) with a
// transcript and stored voices.
func oneVoiceClip(t *testing.T, a *app.App, project, name string, person int) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	id := ids.New(ids.Asset)
	as := models.Asset{ID: id, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{project}, Kind: "video", Name: name, SHA256: ids.New("sha"), State: models.AssetAvailable,
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 12}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Segments: 1, Words: 2}, CreatedAt: now, UpdatedAt: now}
	insert(t, a, store.CAssets, as)
	insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: id, From: 0.5, To: 9, Text: "hello there",
		Speaker: "speaker_00", Words: []models.Word{{Text: "hello", From: 0.5, To: 4}, {Text: "there", From: 4.5, To: 9}}, CreatedAt: now})
	det := &speakers.Detection{Run: ids.New(ids.Run), Result: &voice.Result{Raw: 1, Clusters: []voice.Cluster{testVoice("speaker_00", person, 0.5, 9)}}, Decision: map[string]voice.Decision{}, Prints: true}
	if err := speakers.Store(ctx, a, &as, det, "test"); err != nil {
		t.Fatal(err)
	}
	return id
}

func recognizedNote(t *testing.T, a *app.App) *NotificationRow {
	t.Helper()
	list, err := CallTyped[NotificationListOut](context.Background(), a, "notification.list", NotificationListIn{}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var out *NotificationRow
	for i := range list.Notifications {
		if list.Notifications[i].Kind == models.NotifyRecognized {
			if out != nil {
				t.Fatal("recognitions of one person must be one notification")
			}
			out = &list.Notifications[i]
		}
	}
	return out
}

// Every automatic recognition is visible: one grouped notification per
// person with a link to check them; a new video marks it unread again, and
// saying "Not Maya?" turns that label back into a question.
func TestRecognitionsAreAnnouncedTogether(t *testing.T) {
	a, project, _, _ := todoApp(t)
	ctx := context.Background()
	first := oneVoiceClip(t, a, project, "IMG_2391.mov", 1)
	b := oneVoiceClip(t, a, project, "IMG_2392.mov", 1)
	oneVoiceClip(t, a, project, "IMG_2393.mov", 1)
	ren, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: first, Speaker: "Speaker 1", Name: "Maya"}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(ren.Recognized) != 2 {
		t.Fatalf("Maya should be recognized in the two other clips: %+v", ren.Recognized)
	}
	n := recognizedNote(t, a)
	if n == nil || n.Title != "Recognized Maya's voice in 2 more videos" || n.Read || n.Link != "/library/people?person="+ren.Person.ID {
		t.Fatalf("grouped notification: %+v", n)
	}
	if _, err := CallTyped[NotificationReadOut](ctx, a, "notification.read", NotificationReadIn{All: true}, Caller{}); err != nil {
		t.Fatal(err)
	}
	// A new clip is recognized: the same notification, unread again.
	oneVoiceClip(t, a, project, "IMG_2394.mov", 1)
	if _, err := CallTyped[PersonRebuildOut](ctx, a, "people.rebuild", PersonRebuildIn{ID: "Maya"}, Caller{}); err != nil {
		t.Fatal(err)
	}
	n = recognizedNote(t, a)
	if n == nil || n.Title != "Recognized Maya's voice in 3 more videos" || n.Read {
		t.Fatalf("a new recognition should mark it unread: %+v", n)
	}
	_, _ = CallTyped[NotificationReadOut](ctx, a, "notification.read", NotificationReadIn{All: true}, Caller{})
	// "Not Maya?" on one of them: back to a question; the notice updates quietly.
	sp, err := CallTyped[SpeakersOut](ctx, a, "footage.speakers.reject", SpeakerRejectIn{AssetID: b, Speaker: "Maya", PersonID: ren.Person.ID}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if sp.Speakers[0].Name != "" || len(sp.Speakers[0].Rejected) != 1 {
		t.Fatalf("the label should be gone: %+v", sp.Speakers[0])
	}
	open := listOpen(t, a, TodoListIn{AssetID: b})
	if len(open.Todos) != 1 || open.Todos[0].Kind != models.TodoSpeakerUnnamed {
		t.Fatalf("it is a question again: %+v", open.Todos)
	}
	n = recognizedNote(t, a)
	if n == nil || n.Title != "Recognized Maya's voice in 2 more videos" || !n.Read {
		t.Fatalf("checking a clip should update the notice quietly: %+v", n)
	}
}

// Notes about a person reach everything Claude reads: people list/show, the
// voices of a clip and footage.md in project folders.
func TestPeopleNotesReachWhatClaudeReads(t *testing.T) {
	a, project, _, _ := todoApp(t)
	ctx := context.Background()
	clip := oneVoiceClip(t, a, project, "talk.mov", 3)
	if _, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: clip, Speaker: "1", Name: "Maya Lindqvist"}, Caller{}); err != nil {
		t.Fatal(err)
	}
	notes := "Prefers her full name on screen. Title her “Host”. Avoid her old nickname."
	up, err := CallTyped[PersonOut](ctx, a, "people.update", PersonUpdateIn{ID: "maya", Notes: &notes}, Caller{})
	if err != nil || up.Person.Notes != notes || !strings.Contains(up.Summary, "Saved the notes for Maya Lindqvist") {
		t.Fatalf("update: %+v %v", up, err)
	}
	show, err := CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "lind"}, Caller{})
	if err != nil || show.Person.Notes != notes {
		t.Fatalf("people.show: %+v %v", show, err)
	}
	sp, err := CallTyped[SpeakersOut](ctx, a, "footage.speakers", SpeakersIn{AssetID: clip}, Caller{})
	if err != nil || len(sp.People) != 1 || sp.People[0].Notes != notes {
		t.Fatalf("footage.speakers people: %+v %v", sp.People, err)
	}
	dir := filepath.Join(t.TempDir(), "folder")
	if _, err := CallTyped[FolderOut](ctx, a, "project.checkout", ProjectCheckoutIn{ProjectID: project, Dir: dir}, Caller{}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(dir, "footage.md"))
	if err != nil || !strings.Contains(string(b), "About Maya Lindqvist: "+notes) {
		t.Fatalf("footage.md should carry the notes:\n%s (%v)", b, err)
	}
	long := strings.Repeat("x", speakers.MaxNotes+1)
	if _, err := CallTyped[PersonOut](ctx, a, "people.update", PersonUpdateIn{ID: "maya", Notes: &long}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("notes over the limit: %v", err)
	}
	cl, err := CallTyped[PersonOut](ctx, a, "people.update", PersonUpdateIn{ID: "maya", ClearNotes: true}, Caller{})
	if err != nil || cl.Person.Notes != "" {
		t.Fatalf("clear: %+v %v", cl, err)
	}
}

// Names typed anywhere resolve like the dashboard's person picker: case and
// accents ignored, a unique start is enough, a typo gets "did you mean".
func TestPeopleNamesResolveLikeThePicker(t *testing.T) {
	a, project, _, _ := todoApp(t)
	ctx := context.Background()
	c1 := oneVoiceClip(t, a, project, "a.mov", 4)
	c2 := oneVoiceClip(t, a, project, "b.mov", 5)
	c3 := oneVoiceClip(t, a, project, "c.mov", 6)
	for c, name := range map[string]string{c1: "Maya Lindqvist", c2: "Marcus Webb"} {
		if _, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: c, Speaker: "1", Name: name}, Caller{}); err != nil {
			t.Fatal(err)
		}
	}
	if p, err := CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "MAYA"}, Caller{}); err != nil || p.Person.Name != "Maya Lindqvist" {
		t.Fatalf("a unique start finds her: %+v %v", p, err)
	}
	_, err := CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "Mya"}, Caller{})
	if ae := apperr.From(err); ae.Code != apperr.NotFound || ae.Message != "No one called “Mya” — did you mean “Maya Lindqvist”?" {
		t.Fatalf("typo: %v", err)
	}
	_, err = CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "ma"}, Caller{})
	if ae := apperr.From(err); ae.Code != apperr.InvalidInput || !strings.Contains(ae.Message, "“Marcus Webb” or “Maya Lindqvist”") {
		t.Fatalf("ambiguous: %v", err)
	}
	// Naming a voice "Mya" is a slip, not someone new — unless they say so.
	_, err = CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: c3, Speaker: "1", Name: "Mya"}, Caller{})
	if ae := apperr.From(err); ae.Code != apperr.InvalidInput || !strings.Contains(ae.Message, "did you mean “Maya Lindqvist”") {
		t.Fatalf("close name: %v", err)
	}
	if r, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: c3, Speaker: "1", Name: "Mya", NewPerson: true}, Caller{}); err != nil || !r.CreatedPerson {
		t.Fatalf("someone new: %+v %v", r, err)
	}
	// "Zoe" finds "Zoë".
	if _, err := CallTyped[PersonRenameOut](ctx, a, "people.rename", PersonRenameIn{ID: "Mya", Name: "Zoë"}, Caller{}); err == nil {
		if p, err := CallTyped[PersonShowOut](ctx, a, "people.show", PersonIn{ID: "zoe"}, Caller{}); err != nil || p.Person.Name != "Zoë" {
			t.Fatalf("accents: %+v %v", p, err)
		}
	}
}

// PersonRenameOut is people.rename's output (an alias for the test above).
type PersonRenameOut = PersonOut

// footage.speakers.detect listens for voices on request: videos without a
// transcript are skipped (in plain words); a detection already waiting is reused.
func TestSpeakersDetectOp(t *testing.T) {
	a, project, video, _ := todoApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	old := ids.New(ids.Asset)
	insert(t, a, store.CAssets, models.Asset{ID: old, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{project}, Kind: "video", Name: "old.mov", SHA256: ids.New("sha"),
		State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Segments: 3}, CreatedAt: now, UpdatedAt: now})
	out, err := CallTyped[SpeakerDetectOut](ctx, a, "footage.speakers.detect", SpeakerDetectIn{AssetIDs: []string{old, video}}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 1 || out.Queued != 1 || len(out.Skipped) != 1 || !strings.Contains(out.Skipped[0], "no transcript yet") || out.Summary != "Listening for the voices in “old”." {
		t.Fatalf("detect: %+v", out)
	}
	all, err := CallTyped[SpeakerDetectOut](ctx, a, "footage.speakers.detect", SpeakerDetectIn{All: true}, Caller{})
	if err != nil || len(all.Jobs) != 1 || all.Queued != 0 || all.Jobs[0].JobID != out.Jobs[0].JobID {
		t.Fatalf("--all reuses the waiting detection: %+v %v", all, err)
	}
	if n, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": "diarize"}); n != 1 {
		t.Fatalf("jobs: %d", n)
	}
	if _, err := CallTyped[SpeakerDetectOut](ctx, a, "footage.speakers.detect", SpeakerDetectIn{}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("nothing asked: %v", err)
	}
}
