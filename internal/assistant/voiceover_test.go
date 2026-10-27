package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

func denied(cmd string) fakeclaude.Step {
	return fakeclaude.Step{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": cmd}}}
}

// Narration and mix changes Claude asks for become cards the person confirms:
// plain titles, an AI voice priced before anything is spent, and the change put
// in the cut as a new version. Keys, voice copies and recordings never become cards.
func TestNarrationAndMixBecomeCards(t *testing.T) {
	key := "sk" + "_" + strings.Repeat("ab12", 10) + "cafe"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), key) // this machine's key comes from the environment
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	script := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"narration","ops":[{"op":"setScript","voiceover":{"beats":[
	  {"id":"line-1","text":"Out here, the light arrives slowly.","anchor":{"kind":"clip","clipId":"hook"}},
	  {"id":"line-2","text":"And then it is everywhere, in every corner of the valley, on every rooftop and on every face, all at once and all together.","anchor":{"kind":"clip","clipId":"broll"}}]}}]}`, head)
	applied, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(script)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	rev := applied.Revision.ID
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		// Reads run (no cards).
		tool("Bash", map[string]any{"command": "metarush voiceover script show --revision " + rev + " --json"}, `{"ok":true,"data":{}}`),
		tool("Bash", map[string]any{"command": "metarush voiceover fit --revision " + rev + " --json"}, `{"ok":true,"data":{"result":{"over":1}}}`),
		// Changes become cards.
		denied("metarush voiceover beat add --text 'A third line.' --at-time 0:03 --before line-2 --revision " + rev + " --json"),
		denied("metarush mix set --revision " + rev + " --ducking on --json"),
		denied("metarush voiceover tts --revision " + rev + " --beat 1 --voice voice_ada --json"),
		denied("metarush voiceover fit --revision " + rev + " --apply extend-shot --json"),
		denied("metarush vo line remove 2 --revision " + rev + " --json"),
		denied("metarush voiceover import narration.wav --revision " + rev + " --json"),
		// Never cards: keys, voice copies, the microphone.
		denied("metarush providers set elevenlabs --key-stdin"),
		denied("metarush voiceover clone --name Me --sample ast_01jabcdefghjkmnpqrstvwxyz0 --consent 'I agree'"),
		denied("metarush voiceover record --revision " + rev),
		text("I asked you to confirm those."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "narrate the cut and fix the music", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	byKind := map[string]AssistantAction{}
	var kinds []string
	for _, act := range cv.Actions {
		byKind[act.Kind] = act
		kinds = append(kinds, act.Kind)
	}
	want := []string{"voiceover beat add", "mix set", "voiceover tts", "voiceover fit", "voiceover beat remove", "voiceover import"}
	if strings.Join(kinds, "|") != strings.Join(want, "|") {
		t.Fatalf("cards %v, want %v (reads, keys, voice copies and recordings are never cards)", kinds, want)
	}
	add := byKind["voiceover beat add"]
	if add.Op != "edit.apply" || add.State != ActionPending || add.Title != "Add a narration line: “A third line.”" || add.Check == nil || !add.Check.Valid ||
		!strings.Contains(strings.Join(add.Details, "|"), "Starts at 0:03") || !strings.Contains(strings.Join(add.Details, "|"), "1 narration line added") {
		t.Fatalf("add card: %+v (check %+v)", add, add.Check)
	}
	if mx := byKind["mix set"]; mx.Title != "Make the music dip under voices" || mx.Op != "edit.apply" || !strings.Contains(strings.Join(mx.Details, "|"), "sound mix changed") {
		t.Fatalf("mix card: %+v", mx)
	}
	tts := byKind["voiceover tts"]
	// The voice was named by its ID: the card says whose voice it is.
	if tts.Op != "voiceover.tts" || !strings.HasPrefix(tts.Title, "Make the AI voice for 1 line, with Ada (") || strings.Contains(tts.Title, "credits") ||
		!strings.Contains(tts.Input, `"apply":true`) || !strings.Contains(strings.Join(tts.Details, "|"), "Puts it in the cut as a new version") {
		t.Fatalf("AI voice card (priced first): %+v", tts)
	}
	// The estimate in dollars opens to the line it reads, the voice and model,
	// characters and credits (D208).
	if e := tts.Estimate; e == nil || len(e.Parts) != 1 || e.Parts[0].Service != "ElevenLabs" || !strings.HasPrefix(e.Parts[0].Source, "Line 1: “") ||
		!strings.Contains(e.Parts[0].What, "Ada") || !strings.Contains(e.Parts[0].Units, "characters") || !strings.Contains(tts.Title, "("+e.About+")") {
		t.Fatalf("AI voice card's estimate: %+v", tts.Estimate)
	}
	if fit := byKind["voiceover fit"]; fit.Op != "edit.apply" || fit.Title != "Make the narration fit" || !strings.Contains(strings.Join(fit.Details, "|"), "Holds the shots a little longer") {
		t.Fatalf("fit card: %+v", fit)
	}
	if rm := byKind["voiceover beat remove"]; rm.Op != "edit.apply" || !strings.HasPrefix(rm.Title, "Remove narration line 2 (“And then it is everywhere") {
		t.Fatalf("remove card (alias words): %+v", rm)
	}
	if imp := byKind["voiceover import"]; imp.State != ActionUnavailable || imp.Op != "" || !strings.Contains(imp.Unavailable, "Upload") {
		t.Fatalf("a file on disk is added from the dashboard first: %+v", imp)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title}, act.Details...) {
			for _, bad := range []string{"LUFS", "dBTP", "ducking", "rev_", "ast_", "--"} {
				if strings.Contains(s, bad) {
					t.Fatalf("card text %q has %q", s, bad)
				}
			}
		}
	}

	// The person confirms them on the human channel; each lands in the cut.
	dash := *a
	dash.Actor.Via = "dashboard"
	act := func(id string) AssistantAction {
		t.Helper()
		in, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: id, Quiet: true})
		res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
		if err != nil {
			t.Fatal(err)
		}
		return res.(*ConversationActOut).Action
	}
	if done := act(add.ID); done.State != ActionDone || !strings.Contains(done.Result, "Saved as version") {
		t.Fatalf("add ran: %+v", done)
	}
	if done := act(byKind["mix set"].ID); done.State != ActionDone {
		t.Fatalf("mix ran: %+v", done)
	}
	done := act(tts.ID)
	if done.State != ActionDone || !strings.Contains(done.Result, "It is in the cut now as version") || !strings.HasPrefix(done.Link, "/p/"+projectID+"/r/rev_") ||
		strings.Contains(done.Result, "{") || strings.Contains(done.Result, "Apply the change") || strings.Contains(done.Result, "(s)") {
		t.Fatalf("AI voice ran and was put in the cut: %+v", done)
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		t.Fatal(err)
	}
	var latest models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), p.Branches["main"].Head, &latest); err != nil {
		t.Fatal(err)
	}
	vo := latest.Spec.Voiceover
	if vo == nil || len(vo.Beats) != 3 || vo.Beats[0].Take == nil || vo.Beats[0].Take.Source != models.TakeTTS || !vo.Beats[0].Take.AIVoice ||
		latest.Spec.Mix == nil || latest.Spec.Mix.Ducking == nil || latest.Spec.Mix.Ducking.Enabled == nil || !*latest.Spec.Mix.Ducking.Enabled {
		t.Fatalf("the cut after the three cards: %+v mix %+v", vo, latest.Spec.Mix)
	}
	if latest.CreatedBy.Via != "dashboard" {
		t.Fatalf("made on the human channel: %+v", latest.CreatedBy)
	}
}

// The narration voice and a project's spending limit become cards in plain
// words (voices by name, never by ID); a line placed at a point of the video
// (--to) is tied to the shot playing there.
func TestVoiceSettingsAndPlaceLineCards(t *testing.T) {
	key := "sk" + "_" + strings.Repeat("cd12", 10) + "beef"
	fake := fakeeleven.New(key)
	defer fake.Close()
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), key)
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	script := fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"setScript","voiceover":{"beats":[{"id":"line-1","text":"Hello there.","anchor":{"kind":"clip","clipId":"hook"}}]}}]}`, head)
	applied, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(script)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	rev := applied.Revision.ID
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		denied("metarush providers settings elevenlabs --voice voice_bram --project " + projectID + " --json"),
		denied("metarush providers settings elevenlabs --budget 5000 --project " + projectID + " --json"),
		denied("metarush providers settings elevenlabs --voice Nobody --json"),
		denied("metarush voiceover beat move 1 --to 0:06.5 --revision " + rev + " --json"),
		denied("metarush mix set --revision " + rev + " --bus music --gain -6 --preview --json"),
		text("Asked."),
	}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "use Bram, cap the spend and move the line", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 3 {
		t.Fatalf("cards: %+v (an unknown voice and a preview are not cards)", cv.Actions)
	}
	voice, budget, move := cv.Actions[0], cv.Actions[1], cv.Actions[2]
	if voice.Op != "provider.settings" || voice.Title != "Use Bram as the narration voice for “Assistant film”" || !strings.Contains(voice.Input, `"voice":"voice_bram"`) {
		t.Fatalf("voice card: %+v", voice)
	}
	if budget.Op != "provider.settings" || budget.Title != "Let “Assistant film” spend up to 5,000 credits on the AI voice" {
		t.Fatalf("budget card: %+v", budget)
	}
	if move.Op != "edit.apply" || !strings.HasPrefix(move.Title, "Move narration line 1 (“") || !strings.Contains(strings.Join(move.Details, "|"), "Starts 1.5 s after shot 2 starts") || move.Check == nil || !move.Check.Valid {
		t.Fatalf("move card: %+v", move)
	}
	for _, act := range cv.Actions {
		for _, s := range append([]string{act.Title}, act.Details...) {
			if strings.Contains(s, "voice_") || strings.Contains(s, "prj_") || strings.Contains(s, "--") {
				t.Fatalf("card text %q is not plain", s)
			}
		}
	}
	dash := *a
	dash.Actor.Via = "dashboard"
	for _, act := range []AssistantAction{voice, budget} {
		in, _ := json.Marshal(ConversationActIn{ID: cv.ID, ActionID: act.ID, Quiet: true})
		res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
		if err != nil || res.(*ConversationActOut).Action.State != ActionDone {
			t.Fatalf("%s: %+v %v", act.Title, res, err)
		}
	}
	st, err := ops.CallTyped[providers.StatusOut](ctx, a, "provider.status", providers.StatusIn{Name: providers.ElevenLabsName, ProjectID: projectID}, ops.Caller{})
	// The voice is the project's Content Settings' (D193), its budget the project's own.
	if err != nil || st.Settings == nil || st.Settings.VoiceName != "Bram" || st.ReadOnly || st.Budget == nil || st.Budget.LimitCredits == nil || *st.Budget.LimitCredits != 5000 {
		t.Fatalf("settings after the cards: %+v %v", st, err)
	}
}

// Without a key on this computer an AI voice card says so instead of asking to spend.
func TestAIVoiceCardWithoutAKey(t *testing.T) {
	t.Setenv(providers.EnvElevenLabsBaseURL, "http://127.0.0.1:1")
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	a := testApp(t, "")
	projectID, _, head := fixture(t, a)
	ctx := context.Background()
	script := fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"setScript","voiceover":{"beats":[{"id":"line-1","text":"Hello there.","anchor":{"kind":"clip","clipId":"hook"}}]}}]}`, head)
	applied, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(script)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		denied("metarush voiceover tts --revision " + applied.Revision.ID + " --json"), text("Asked.")}}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make the AI voice", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	cv := waitIdle(t, m, out.Conversation.ID)
	if len(cv.Actions) != 1 || cv.Actions[0].State != ActionUnavailable || cv.Actions[0].Op != "" || !strings.Contains(cv.Actions[0].Unavailable, "Settings → Integrations") {
		t.Fatalf("card without a key: %+v", cv.Actions)
	}
}

func TestNarrationCommandsParseAndRead(t *testing.T) {
	parse := func(cmd string) cliCall {
		t.Helper()
		w, err := splitCommand(cmd)
		if err != nil {
			t.Fatal(err)
		}
		c, ok := parseMetarush(w)
		if !ok {
			t.Fatalf("not parsed: %s", cmd)
		}
		return c
	}
	if c := parse("metarush vo line add --text 'Hi' --after-previous --revision rev_x --beat 1 --beat 3,4 --json"); c.Path != "voiceover beat add" || c.Flags["text"] != "Hi" ||
		strings.Join(listFlag(&c, "beat"), ",") != "1,3,4" || len(c.Args) != 0 {
		t.Fatalf("aliases, booleans and repeated flags: %+v", c)
	}
	if c := parse("metarush voiceover import ast_x --continuous --revision rev_x"); c.Path != "voiceover import" || first(c.Args) != "ast_x" || !hasBool(&c, "continuous") {
		t.Fatalf("import: %+v", c)
	}
	for cmd, read := range map[string]bool{
		"metarush voiceover script show --revision rev_x --json": true, "metarush voiceover fit --revision rev_x --json": true,
		"metarush voiceover fit --revision rev_x --apply preferred --json": false, "metarush voiceover estimate --revision rev_x": true,
		"metarush mix show --revision rev_x": true, "metarush mix set --revision rev_x --ducking on": false, "metarush providers status": true,
		"metarush providers set elevenlabs": false, "metarush voiceover voices --library": true, "metarush voiceover tts --revision rev_x": false,
	} {
		if got := readOnlyCall(parse(cmd)); got != read {
			t.Errorf("%s: read-only %v, want %v", cmd, got, read)
		}
	}
	deny := strings.Join(deniedTools, ",")
	for _, want := range []string{"Bash(metarush voiceover fit * --apply *)", "Bash(metarush voiceover fit --apply *)", "Bash(metarush voiceover fit * --apply=*)"} {
		if !strings.Contains(deny, want) {
			t.Errorf("deny rules lack %s", want)
		}
	}
	allow, err := allowedTools("/scratch", "/cache", "/conv/proposals")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Bash(metarush voiceover script show *)", "Bash(metarush mix show *)", "Bash(metarush providers status *)", "Bash(metarush voiceover estimate *)"} {
		if !strings.Contains(strings.Join(allow, ","), want) {
			t.Errorf("allowlist lacks %s", want)
		}
	}
	for _, bad := range []string{"voiceover tts", "voiceover scratch", "mix set", "providers set", "voiceover clone", "voiceover record"} {
		if strings.Contains(strings.Join(allow, ","), "Bash(metarush "+bad) {
			t.Errorf("allowlist has %s", bad)
		}
	}
	for _, want := range []string{"voiceover script show", "mix set", "voiceover fit --apply", "Settings → Integrations"} {
		if !strings.Contains(systemRules, want) {
			t.Errorf("system rules lack %q", want)
		}
	}
	p := newParser("/ws", "/as/conversations/asc_01jabcdefghjkmnpqrstvwxyz0", "/scratch", "/as", func(id string) string { return map[string]string{"rev_01jabcdefghjkmnpqrstvwxyz0": "version 3"}[id] })
	for cmd, want := range map[string]string{
		"metarush voiceover script show --revision rev_01jabcdefghjkmnpqrstvwxyz0 --json": "Read the narration script of version 3",
		"metarush voiceover fit --revision rev_x --json":                                  "Checked that every narration line fits",
		"metarush voiceover fit --revision rev_x --apply preferred --json":                "Asked to make the narration fit",
		"metarush voiceover estimate --revision rev_x --json":                             "Priced the AI voice",
		"metarush mix set --revision rev_x --ducking on":                                  "Asked to change the sound mix",
		"metarush providers status --json":                                                "Checked the AI voice setup",
	} {
		c := parse(cmd)
		if got := p.bashLabel(&c, cmd); got != want {
			t.Errorf("%s: label %q, want %q", cmd, got, want)
		}
	}
	if thousands(1234.4) != "1,234" || thousands(999) != "999" || thousands(1234567) != "1,234,567" {
		t.Error("thousands")
	}
	if mixTitle([]string{"make the music quieter (4 dB)", "turn the music down under speech"}) != "Make the music quieter, turn the music down under speech" {
		t.Error("mix title drops the amounts")
	}
}

// A cut without narration suggests writing a script; one whose lines have no
// audio yet suggests hearing them.
func TestSuggestionsOfferNarration(t *testing.T) {
	a := testApp(t, "")
	m := New(a, "")
	defer m.Close()
	ctx := context.Background()
	projectID, _, head := fixture(t, a)
	labels := func() string {
		out, err := m.Suggestions(ctx, AssistantSuggestionsIn{ProjectID: projectID})
		if err != nil {
			t.Fatal(err)
		}
		var ls []string
		for _, s := range out.Suggestions {
			ls = append(ls, s.Label)
		}
		return strings.Join(ls, "|")
	}
	if got := labels(); !strings.Contains(got, "Write a voiceover script for this cut") {
		t.Fatalf("no narration yet: %s", got)
	}
	script := fmt.Sprintf(`{"baseRevisionId":%q,"ops":[{"op":"setScript","voiceover":{"beats":[{"id":"line-1","text":"Hello there.","anchor":{"kind":"clip","clipId":"hook"}}]}}]}`, head)
	if _, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(script)}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if got := labels(); !strings.Contains(got, "Hear the narration") || strings.Contains(got, "Write a voiceover script") {
		t.Fatalf("narration without audio: %s", got)
	}
}
