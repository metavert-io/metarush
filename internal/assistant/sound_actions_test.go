package assistant

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// Sound changes Claude asks for become cards (D111): a person's level, music
// quieter under voices, a clearer voice (on this computer; in the cloud with
// its price first and the budget kept; in studio quality with its one-time
// setup shown and done first), music added or fitted, and the setup itself
// (people only). Building them makes, spends and saves nothing.
func TestSoundRequestsBecomeCards(t *testing.T) {
	key := "sk" + "_" + strings.Repeat("cd34", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), key)
	a := testApp(t, "")
	f := callFixture(t, a)
	ctx := context.Background()
	budget := func(credits float64) {
		t.Helper()
		if _, err := ops.CallTyped[providers.SettingsOut](ctx, a, "provider.settings", providers.SettingsIn{Name: providers.ElevenLabsName, Scope: "project",
			ProjectID: f.project, BudgetCredits: &credits}, ops.Caller{Human: true}); err != nil {
			t.Fatal(err)
		}
	}
	budget(50000)
	p := " --project " + f.project
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "Fix the sound", Steps: []fakeclaude.Step{
			tool("Bash", map[string]any{"command": "metarush sound show" + p + " --json"}, `{"ok":true,"data":{}}`),
			tool("Bash", map[string]any{"command": "metarush sound compare" + p + " --json"}, `{"ok":true,"data":{}}`),
			denied("metarush sound levels" + p + " --speaker maya --gain 3 --json"),
			denied("metarush sound levels" + p + " --duck on --depth 14 --apply --json"),
			denied("metarush sound enhance Sam" + p + " --json"),
			denied("metarush sound enhance Maya" + p + " --mode cloud --json"),
			denied("metarush sound enhance Sam" + p + " --mode studio --strength 90 --json"),
			denied("metarush sound music add 'Upbeat bed'" + p + " --loop --json"),
			denied("metarush sound music add /Users/you/Music/bed.m4a" + p),
			denied("metarush sound music set music" + p + " --level -18 --json"),
			denied("metarush sound music set music" + p + " --fit auto --duck on --json"),
			denied("metarush sound setup studio"),
			// Not cards: someone who doesn't speak, a price only, a preview only.
			denied("metarush sound levels" + p + " --speaker Nobody --gain 2"),
			denied("metarush sound enhance Maya" + p + " --mode cloud --estimate"),
			denied("metarush sound levels" + p + " --part music --gain -4 --preview"),
			text("I asked you to confirm those."),
		}},
		{Match: "again", Steps: []fakeclaude.Step{
			denied("metarush sound enhance Maya" + p + " --mode cloud --json"),
			text("Asked again."),
		}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "Fix the sound", ProjectID: f.project}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	by, kinds := cardsOf(t, cv)
	want := []string{"sound levels", "sound levels", "sound enhance", "sound enhance", "sound enhance", "sound music add", "sound music add",
		"sound music set", "sound music set", "sound setup"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("cards %v, want %v", kinds, want)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title, act.Unavailable}, act.Details...) {
			if fs := plainwords.CheckShown(s); len(fs) > 0 {
				t.Errorf("card %q: %q is not plain: %v", act.Kind, s, fs)
			}
		}
	}
	louder, duck := by["sound levels"][0], by["sound levels"][1]
	if louder.Op != "sound.levels" || louder.Title != "Make Maya louder" || !strings.Contains(louder.Input, `"apply":true`) ||
		!strings.Contains(louder.Input, `"people":[{"name":"Maya","gainDb":3}]`) || louder.Check == nil || !louder.Check.Valid ||
		!strings.Contains(strings.Join(louder.Details, "|"), "Make Maya louder (+3 dB)|Only while Maya speaks; nobody else changes") {
		t.Fatalf("a person's level: %+v", louder)
	}
	if duck.Op != "sound.levels" || duck.Title != "Make the music dip under voices" || !strings.Contains(duck.Input, `"depth":14`) ||
		!strings.Contains(strings.Join(duck.Details, "|"), "Set how far the music dips under the narration (14 dB)|Set how far the music dips when someone talks on camera (12 dB)") {
		t.Fatalf("music under voices: %+v", duck)
	}
	clean, cloud, studio := by["sound enhance"][0], by["sound enhance"][1], by["sound enhance"][2]
	if clean.Op != "sound.enhance" || clean.Title != "Make Sam's voice clearer (on this computer)" || clean.HumanOnly ||
		!strings.Contains(strings.Join(clean.Details, "|"), "Free, and nothing leaves this computer") || !strings.Contains(clean.Input, `"apply":true`) {
		t.Fatalf("clean: %+v", clean)
	}
	if cloud.Op != "sound.enhance" || !strings.HasPrefix(cloud.Title, "Make Maya's voice clearer with ElevenLabs (") || !strings.HasSuffix(cloud.Title, ")") ||
		strings.Contains(cloud.Title, "credits") || !strings.Contains(strings.Join(cloud.Details, "|"), "This project has 50,000 of 50,000 credits left") ||
		!strings.Contains(strings.Join(cloud.Details, "|"), "Your ElevenLabs plan has") || !strings.Contains(cloud.Input, `"mode":"cloud"`) {
		t.Fatalf("cloud, priced first: %+v", cloud)
	}
	// The estimate opens to what it draws on (D208): ElevenLabs' Voice
	// Isolator on Maya's stretch of the recording, in minutes and credits.
	if e := cloud.Estimate; e == nil || len(e.Parts) == 0 || e.Parts[0].Service != "ElevenLabs" || e.Parts[0].Model != "Voice Isolator" ||
		!strings.Contains(e.Parts[0].Units, "credits") || !strings.HasPrefix(e.Parts[0].Source, "“") || e.Parts[0].Rate == "" || !strings.Contains(cloud.Title, e.About) {
		t.Fatalf("cloud card's estimate: %+v", cloud.Estimate)
	}
	if clean.Estimate != nil || studio.Estimate != nil {
		t.Fatalf("on this computer nothing is paid, so no estimate: %+v %+v", clean.Estimate, studio.Estimate)
	}
	if !studio.HumanOnly || strings.Join(studio.SetUp, ",") != "studio" || studio.Title != "Make Sam's voice clearer in studio quality" ||
		!strings.HasPrefix(studio.Details[0], "First sets up studio quality on this computer: a one-time download of about 1.8 GB") ||
		!strings.Contains(strings.Join(studio.Details, "|"), "You hear 90% of the clearer voice") || !strings.Contains(studio.Input, `"strength":0.9`) {
		t.Fatalf("studio, with its setup: %+v", studio)
	}
	add, file := by["sound music add"][0], by["sound music add"][1]
	if add.Op != "sound.music.add" || add.Title != "Put “Upbeat bed” under the video" || !strings.Contains(add.Input, f.music) || !strings.Contains(add.Input, `"loop":true`) ||
		!strings.Contains(strings.Join(add.Details, "|"), "Starts with the video and ends with it on a beat, with a smooth fade|Repeats until the video ends") ||
		!strings.Contains(strings.Join(add.Details, "|"), "To-dos asks where it comes from") {
		t.Fatalf("music add: %+v", add)
	}
	if file.State != ActionUnavailable || !strings.Contains(file.Unavailable, "drop the file on the Sound tab") {
		t.Fatalf("a file on this computer is added from the dashboard first: %+v", file)
	}
	level, fit := by["sound music set"][0], by["sound music set"][1]
	if level.Op != "sound.music.set" || level.Title != "Music “Upbeat bed”: set its level to -18 dB" || level.Check == nil || !level.Check.Valid {
		t.Fatalf("music level: %+v", level)
	}
	if fit.Title != "Music “Upbeat bed”: fit it to the video, make it quieter while someone talks" {
		t.Fatalf("music fit: %+v", fit)
	}
	setup := by["sound setup"][0]
	if setup.Op != "sound.setup" || !setup.HumanOnly || setup.Title != "Set up studio quality on this computer (one time, about 1.8 GB)" {
		t.Fatalf("setup: %+v", setup)
	}
	if n := fake.Calls("isolation"); n != 0 {
		t.Fatalf("building cards sent %d voices to ElevenLabs", n)
	}
	var proj models.Project
	_ = a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), f.project, &proj)
	if proj.Branches["main"].Head != f.head {
		t.Fatal("building cards saved a version")
	}
	var music models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), f.music, &music)

	// Over the budget, the cloud card says so and can't be confirmed.
	budget(100)
	if _, err := m.Continue(ctx, AssistantContinueIn{ID: cv.ID, AssistantMessageIn: AssistantMessageIn{Prompt: "Try the cloud again", ProjectID: f.project}}); err != nil {
		t.Fatal(err)
	}
	cv = waitIdle(t, m, cv.ID)
	over := cv.Actions[len(cv.Actions)-1]
	if over.State != ActionUnavailable || over.Op != "" || !strings.Contains(over.Unavailable, "over the project's budget") {
		t.Fatalf("over the budget: %+v", over)
	}

	// A person confirms; the levels land in the cut.
	if done := actAsPerson(t, a, cv.ID, louder.ID); done.State != ActionDone || !strings.HasPrefix(done.Result, "Make Maya louder (+3 dB). It is in the cut now as version") ||
		!strings.HasPrefix(done.Link, "/p/"+f.project+"/r/") {
		t.Fatalf("level ran: %+v", done)
	}
	if done := actAsPerson(t, a, cv.ID, level.ID); done.State != ActionDone || !strings.Contains(done.Result, "It is in the cut now as version") {
		t.Fatalf("music level ran: %+v", done)
	}
	// Studio set up elsewhere right now: the card's setup step refuses to start a second one.
	inst := a.Cfg.Installation.ID
	j := models.Job{ID: ids.New(ids.Job), WorkspaceID: a.WorkspaceID(), Type: jobs.TypeVoiceover, State: models.JobRunning, Payload: map[string]any{"action": sound.ActionSetup},
		IdempotencyKey: "setup-elsewhere", MaxAttempts: 2, Attempts: []models.Attempt{}, Lease: &models.Lease{Owner: inst, ExpiresAt: time.Now().Add(time.Minute)},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()}
	if err := a.DB.Insert(ctx, store.CJobs, j); err != nil {
		t.Fatal(err)
	}
	if done := actAsPerson(t, a, cv.ID, studio.ID); done.State != ActionFailed || !strings.Contains(done.Error, "being set up on this computer right now") {
		t.Fatalf("studio while it is being set up: %+v", done)
	}
}
