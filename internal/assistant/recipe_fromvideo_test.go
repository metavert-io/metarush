package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// Claude asking to start a recipe from a video someone liked (D402) is a card
// the server builds: for a clip here it names the clip, for a link its site.
// A browser's sign-in, a file on this computer and saying a video is yours
// never become cards; an address on this computer can't run. Auto never runs
// one. Run by a person, the clip becomes a reference (no cut plays it) and the
// draft that follows is the card's work: Claude hears once it is saved, with
// how to read its sheet. Marking a clip as a reference is a card too.
func TestStartFromAVideoCard(t *testing.T) {
	a := testApp(t, "")
	on, off := true, false
	recipeFixture(t, a) // the recipe "film" stands in for the draft
	ctx := context.Background()
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: "Tester", Email: "tester@example.com", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	clipOf := func(name, sha string) models.Asset {
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name,
			SHA256: strings.Repeat(sha, 32), Size: 1000, MIME: "video/mp4", StorageKey: "k-" + sha, State: models.AssetAvailable,
			Media:    models.MediaInfo{Container: "mp4", Duration: 26, HasVideo: true, HasAudio: true, Width: 720, Height: 1280, FPS: 30, Streams: []models.Stream{}},
			Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as
	}
	liked, other := clipOf("liked.mp4", "e1"), clipOf("their-tips.mp4", "e2")
	link := "https://www.youtube.com/watch?v=abcdefghijk"
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "make one like this", Steps: []fakeclaude.Step{
			// Reading what a video does is no card.
			tool("Bash", map[string]any{"command": "metarush footage what-it-does " + liked.ID + " --json"}, `{"ok":true,"data":{"card":{"ready":false}}}`),
			denied(`metarush recipe from-video ` + liked.ID + ` --reference --json`),
			denied(`metarush recipe from-video '` + link + `' --name tips --json`), // quoted, as a shell needs it
			denied(`metarush recipe from-video http://127.0.0.1:8080/their.mp4 --json`),
			denied(`metarush footage reference ` + other.ID + ` --json`),
			// Never cards: a browser's sign-in, a file here, saying a video is yours.
			denied(`metarush recipe from-video '` + link + `' --browser-signin chrome --json`),
			denied(`metarush recipe from-video /Users/someone/Downloads/their-video.mp4 --json`),
			denied(`metarush footage reference --mine ` + other.ID + ` --json`),
			text("Starting from that video is on a card for you."),
		}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make one like this video"}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	if len(cv.Actions) != 4 {
		t.Fatalf("four cards (a browser's sign-in, a file and saying a video is yours never are; reading is none): %+v", cv.Actions)
	}
	for _, x := range cv.Actions {
		for _, line := range append([]string{x.Title, x.Unavailable}, x.Details...) {
			if f := plainwords.CheckPanel(line); len(f) > 0 {
				t.Errorf("not plain on a card: %q %+v", line, f)
			}
		}
	}
	fromClip, fromLink, local := cardOfKind(t, cv, "recipe from-video", 0), cardOfKind(t, cv, "recipe from-video", 1), cardOfKind(t, cv, "recipe from-video", 2)
	var clipIn, linkIn recipeFromVideoIn
	_ = json.Unmarshal([]byte(fromClip.Input), &clipIn)
	_ = json.Unmarshal([]byte(fromLink.Input), &linkIn)
	if fromClip.Op != "recipe.fromVideo" || fromClip.HumanOnly || fromClip.State != ActionPending || fromClip.Recipe == nil || fromClip.Recipe.Kind != "fromvideo" ||
		fromClip.Recipe.Video != "liked" || fromClip.Title != "Start a recipe from “liked”" || clipIn.From != liked.ID || !clipIn.Reference ||
		!strings.Contains(strings.Join(fromClip.Details, " | "), "It becomes a reference") || !strings.Contains(strings.Join(fromClip.Details, " | "), "Nothing of the video goes into the draft") {
		t.Fatalf("the clip's card: %+v %+v %+v", fromClip, fromClip.Recipe, clipIn)
	}
	if fromLink.Op != "recipe.fromVideo" || fromLink.State != ActionPending || fromLink.Recipe == nil || fromLink.Recipe.VideoFrom != link || fromLink.Recipe.Video != "" ||
		fromLink.Title != "Start a recipe from the video at youtube.com" || linkIn.From != link || linkIn.Name != "tips" ||
		!strings.Contains(strings.Join(fromLink.Details, " | "), "It comes in as a reference") {
		t.Fatalf("the link's card: %+v %+v %+v", fromLink, fromLink.Recipe, linkIn)
	}
	if local.State != ActionUnavailable || local.Op != "" || !strings.Contains(local.Unavailable, "on this computer or a local network") {
		t.Fatalf("an address here can't run: %+v", local)
	}
	mark := cardOfKind(t, cv, "footage reference", 0)
	if mark.Op != "footage.reference" || mark.State != ActionPending || !strings.HasPrefix(mark.Title, "Mark ") || !strings.HasSuffix(mark.Title, " as a reference") {
		t.Fatalf("marking a reference: %+v", mark)
	}
	// Auto never runs one, with estimates off.
	a.Cfg.Assistant.SafeSteps = &on
	if _, err := ops.CallTyped[AssistantModeOut](ctx, a, "assistant.mode.set", AssistantModeSetIn{Mode: ModeAuto}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[prefs.PreferencesOut](ctx, a, "account.preferences.set", prefs.PreferencesSetIn{Estimates: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	c, _ := m.live(ctx, id, false)
	for _, x := range []AssistantAction{fromClip, fromLink} {
		x := x
		if m.startAuto(c, &x) || x.State != ActionPending || x.Auto {
			t.Errorf("Auto never runs it: %s %+v", x.Title, x)
		}
	}
	a.Cfg.Assistant.SafeSteps = &off
	// A person runs the clip's card: it becomes a reference, and the draft follows once MetaRush has looked at it.
	ran, err := decide(t, a, ConversationActIn{ID: id, ActionID: fromClip.ID})
	if err != nil {
		t.Fatal(err)
	}
	d := ran.Action
	if d.State != ActionDone || d.Work == nil || d.Work.Kind != WorkDraft || d.Work.State != WorkRunning || d.Work.AssetID != liked.ID || !strings.Contains(d.Link, "/library/recipes") {
		t.Fatalf("the card ran: %+v %+v", d, d.Work)
	}
	var got models.Asset
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"_id": liked.ID}).Decode(&got); err != nil || !got.IsReference() || got.StartFrom == nil || got.StartFrom.State != models.StartFromWaiting {
		t.Fatalf("a reference with a draft asked for: %+v %+v %v", got.Reference, got.StartFrom, err)
	}
	// The draft is saved (as the hook does once the looks are done): the card's work ends, and Claude hears it.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": liked.ID}, bson.M{"$set": bson.M{"startFrom.state": models.StartFromMade, "startFrom.recipe": "film", "startFrom.version": 1}}); err != nil {
		t.Fatal(err)
	}
	var w *ActionWork
	eventually(t, "the draft to end the card's work", func() bool {
		cv, _ = m.Get(ctx, id, false, false)
		w = cardOfKind(t, cv, "recipe from-video", 0).Work
		return w != nil && w.State != WorkRunning
	})
	if w.State != WorkFinished || w.Outcome != "The draft “Assistant film” is saved, made from what “liked” does" || !strings.Contains(w.Link, "recipe=film&version=1") {
		t.Fatalf("the work's outcome: %+v", w)
	}
	c.mu.Lock()
	forClaude := ""
	for _, x := range c.data.Actions {
		if x.ID == fromClip.ID && x.Work != nil {
			forClaude = x.Work.ForClaude
		}
	}
	c.mu.Unlock()
	if !strings.Contains(forClaude, "metarush recipe checkout film -") || !strings.Contains(forClaude, "Only a person makes it current") {
		t.Fatalf("what Claude is told: %q", forClaude)
	}
	// Marking the other clip: a reference no cut plays.
	marked, err := decide(t, a, ConversationActIn{ID: id, ActionID: mark.ID})
	if err != nil || marked.Action.State != ActionDone {
		t.Fatalf("marked: %+v %v", marked, err)
	}
	got = models.Asset{}
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"_id": other.ID}).Decode(&got); err != nil || !got.IsReference() || got.StartFrom != nil {
		t.Fatalf("the other clip is a reference: %+v %v", got.Reference, err)
	}
}
