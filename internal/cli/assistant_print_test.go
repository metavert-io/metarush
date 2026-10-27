package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// A conversation like the one that showed the problems (D140): grouped steps,
// cards that started by themselves and wait for work, a question card, notes
// with their plain line and one from before notes had one. Plain output reads
// like the dashboard: no IDs, commands, picture paths, file names or version
// codes; --details adds them back.
func TestConversationPrintsPlainly(t *testing.T) {
	now := time.Now()
	ctx := &assistant.TurnContext{ProjectName: "fernbrook", HeadRevisionNumber: 1}
	img := []assistant.ImageRef{{Root: "scratch", Path: "frames/ast_01m5a226jfhy6wj9mh78v75rgh/13m11.800.jpg"}}
	still := func(id string) *assistant.ToolCall {
		return &assistant.ToolCall{ID: id, Name: "Read", Label: "Looked at a still from “video1763049187”", Group: "still:“video1763049187”",
			Many: "Looked at %d stills from “video1763049187”", Images: img}
	}
	events := []assistant.AssistantEvent{
		{Seq: 1, Turn: 1, Kind: "turn", Text: "Make a fresh cut of the two longer videos", User: "Theo", Context: ctx},
		{Seq: 2, Turn: 1, Kind: "tool", Tool: &assistant.ToolCall{ID: "t1", Name: "Read", Label: "Read the rest of a long result", Group: "spill", Many: "Read the rest of %d long results"}},
		{Seq: 3, Turn: 1, Kind: "tool", Tool: &assistant.ToolCall{ID: "t2", Name: "Read", Label: "Read the rest of a long result", Group: "spill", Many: "Read the rest of %d long results"}},
		{Seq: 4, Turn: 1, Kind: "tool", Tool: &assistant.ToolCall{ID: "t3", Name: "Write", Label: "Wrote the graphic “name-bar”", Group: "graphic:name-bar", Many: "Wrote the graphic “name-bar”"}},
		{Seq: 5, Turn: 1, Kind: "tool", Tool: &assistant.ToolCall{ID: "t4", Name: "Write", Label: "Wrote the graphic “name-bar”", Group: "graphic:name-bar", Many: "Wrote the graphic “name-bar”"}},
		{Seq: 6, Turn: 1, Kind: "tool", Tool: &assistant.ToolCall{ID: "t5", Name: "Bash", Label: "Asked to check a graphic it made"}},
		{Seq: 7, Turn: 1, Kind: "action", ActionID: "act_check"},
		{Seq: 8, Turn: 1, Kind: "tool_result", Tool: &assistant.ToolCall{ID: "t5", Denied: true}},
		{Seq: 9, Turn: 1, Kind: "tool", Tool: still("t6")},
		{Seq: 10, Turn: 1, Kind: "tool", Tool: still("t7")},
		{Seq: 11, Turn: 1, Kind: "tool", Tool: still("t8")},
		{Seq: 12, Turn: 1, Kind: "text", Text: "I made two graphics and asked for a review copy."},
		{Seq: 13, Turn: 1, Kind: "action", ActionID: "act_render"},
		{Seq: 14, Turn: 1, Kind: "action", ActionID: "act_q"},
		{Seq: 15, Turn: 1, Kind: "proposals", Count: 1},
		{Seq: 16, Turn: 1, Kind: "turn_end", State: assistant.TurnSucceeded, Result: &assistant.TurnResult{DurationMs: 207200, Steps: 33}},
		{Seq: 17, Turn: 2, Kind: "turn", State: "note", Text: "“name-bar” passed the check\nThe review copy of version 1 is ready (6 min 23 s). The quality check found nothing wrong.", Context: ctx},
		{Seq: 18, Turn: 2, Kind: "turn_end", State: assistant.TurnSucceeded, Result: &assistant.TurnResult{DurationMs: 7800, Steps: 2}},
		{Seq: 19, Turn: 3, Kind: "turn", State: "note", Context: ctx,
			Text: "Done from the dashboard: Save the cut: Fix the flicker. Result: Saved the cut sheet as version 2 (rev_01m5d3tqz2zw4dcggss5xj1mn9)."},
	}
	actions := []assistant.AssistantAction{
		{ID: "act_check", Turn: 1, ToolUseID: "t5", Kind: "motion check", Op: "motion.check", Title: "Check the graphic “name-bar” Claude made", Auto: true,
			State: assistant.ActionDone, Details: []string{"Draws test frames and a short preview, with no internet access"}, Result: "“name-bar” passed the check",
			Frames: []assistant.CardPicture{{URL: "/media/motion/aaaa/frame-0", Label: "Start"}}},
		{ID: "act_render", Turn: 1, Kind: "render submit", Op: "render.submit", Title: "Render a review copy of version 1", Auto: true, State: assistant.ActionDone,
			Result: "The review copy of version 1 is waiting to start", Work: &assistant.ActionWork{Kind: assistant.WorkRender, RenderID: "rnd_01m5d3vv6nrdg37zj00b5x4b1q",
				What: "the review copy of version 1", State: assistant.WorkFinished, Outcome: "The review copy of version 1 is ready (6 min 23 s)."}},
		{ID: "act_q", Turn: 1, Kind: "question", Title: "Which of the five shorts should I set up?", State: assistant.ActionPending,
			Question: &assistant.QuestionCard{Text: "Which of the five shorts should I set up?", Choices: []string{"The garden in spring", "Lessons learned"}, Multiple: true}},
	}
	turns := []assistant.Turn{{N: 1, Prompt: "Make a fresh cut"}, {N: 2, Note: true, Shown: "“name-bar” passed the check"}, {N: 3, Note: true}}
	cv := &assistant.Conversation{ID: "asc_01m5d1xjxmqtqc2aynrsxy7c0v", Title: "Garden club open day cut and shorts", InstallationName: "studio-mac", ProjectName: "fernbrook",
		State: assistant.ConvIdle, PendingActions: 1, Events: events, Actions: actions, Turns: turns, CreatedAt: now, UpdatedAt: now}

	var plain bytes.Buffer
	printConversation(&plain, cv, "plan", false, false, "")
	out := plain.String()
	if fs := (plainwords.Allow{"flag:--details"}).Filter(plainwords.CheckPanel(out)); len(fs) > 0 {
		t.Errorf("plain conversation shows %v:\n%s", fs, out)
	}
	for _, want := range []string{
		"on studio-mac · “fernbrook” · 1 thing waiting for you",
		"  · Read the rest of 2 long results",
		"  · Wrote the graphic “name-bar”\n",
		"  · Looked at 3 stills from “video1763049187”",
		"  ▸ Claude asks: Check the graphic “name-bar” Claude made — done",
		"      Started by itself (your setting for safe steps)",
		"      The review copy of version 1 is ready (6 min 23 s).",
		"  ? Claude asks you: Which of the five shorts should I set up? — waiting for you",
		"      2. Lessons learned",
		"  ✎ Claude wrote a change for you to look at",
		"── From the dashboard  (“fernbrook”, version 1)\n“name-bar” passed the check\nThe review copy",
		"Saved the cut sheet as version 2",
		"— 3 min 27 s · 33 steps",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("plain conversation lacks %q:\n%s", want, out)
		}
	}
	for _, not := range []string{"Asked to check a graphic it made", "asc_", "rnd_", "rev_", "picture:", "run it:", "metarush assistant"} {
		if strings.Contains(out, not) {
			t.Errorf("plain conversation shows %q:\n%s", not, out)
		}
	}
	var detailed bytes.Buffer
	printConversation(&detailed, cv, "plan", false, true, "")
	for _, want := range []string{"asc_01m5d1xjxmqtqc2aynrsxy7c0v", "picture: scratch/frames/", "answer it: metarush assistant act asc_01m5d1xjxmqtqc2aynrsxy7c0v act_q",
		"metarush assistant proposals asc_01m5d1xjxmqtqc2aynrsxy7c0v", "picture: /media/motion/aaaa/frame-0"} {
		if !strings.Contains(detailed.String(), want) {
			t.Errorf("--details lacks %q:\n%s", want, detailed.String())
		}
	}
}

// A conversation two people share (D179), printed for each of them: someone
// else's says who started it, each message who wrote it, each card whom it
// waits for and who decided it, in plain words.
func TestConversationSaysWhose(t *testing.T) {
	now := time.Now()
	events := []assistant.AssistantEvent{
		{Seq: 1, Turn: 1, Kind: "turn", Text: "A slower opening, please", User: "Theo", UserID: "usr_theo"},
		{Seq: 2, Turn: 1, Kind: "action", ActionID: "act_theo"},
		{Seq: 3, Turn: 1, Kind: "turn_end", State: assistant.TurnSucceeded},
		{Seq: 4, Turn: 2, Kind: "turn", Text: "Keep the kitchen shot", User: "Maya", UserID: "usr_maya"},
		{Seq: 5, Turn: 2, Kind: "action", ActionID: "act_maya"},
		{Seq: 6, Turn: 2, Kind: "action", ActionID: "act_q"},
		{Seq: 7, Turn: 2, Kind: "turn_end", State: assistant.TurnSucceeded},
	}
	actions := []assistant.AssistantAction{
		{ID: "act_theo", Turn: 1, Kind: "comment add", Op: "comment.add", Title: "Add a note to “fernbrook”", State: assistant.ActionPending, ForID: "usr_theo", ForName: "Theo"},
		{ID: "act_maya", Turn: 2, Kind: "comment add", Op: "comment.add", Title: "Add another note to “fernbrook”", State: assistant.ActionDone, Result: "Added the note",
			ForID: "usr_maya", ForName: "Maya", By: "Maya", ByID: "usr_maya"},
		{ID: "act_q", Turn: 2, Kind: "question", Title: "Which music?", State: assistant.ActionDone, Result: "Calm", ForID: "usr_maya", ForName: "Maya",
			By: "Theo", ByID: "usr_theo", Question: &assistant.QuestionCard{Text: "Which music?", Choices: []string{"Calm", "Bright"}}},
	}
	cv := &assistant.Conversation{ID: "asc_01m5d1xjxmqtqc2aynrsxy7c0v", Title: "Opening", InstallationName: "studio-mac", ProjectName: "fernbrook", UserID: "usr_theo",
		UserName: "Theo", State: assistant.ConvIdle, PendingActions: 1, WaitingFor: []assistant.WaitingCount{{UserID: "usr_theo", UserName: "Theo", Count: 1}},
		Events: events, Actions: actions, Turns: []assistant.Turn{{N: 1}, {N: 2}}, CreatedAt: now, UpdatedAt: now}

	print := func(me string) string {
		var b bytes.Buffer
		printConversation(&b, cv, "plan", false, false, me)
		if fs := (plainwords.Allow{"flag:--details"}).Filter(plainwords.CheckPanel(b.String())); len(fs) > 0 {
			t.Errorf("as %s the conversation shows %v:\n%s", me, fs, b.String())
		}
		return b.String()
	}
	asMaya := print("usr_maya")
	for _, want := range []string{
		"on studio-mac · “fernbrook” · started by Theo · 1 thing waiting for Theo",
		"── Theo\nA slower opening, please",
		"── Maya\nKeep the kitchen shot",
		"  ▸ Claude asks: Add a note to “fernbrook” — waiting for Theo",
		"  ▸ Claude asks: Add another note to “fernbrook” — done\n",
		"  ? Claude asks you: Which music? — answered by Theo",
		"      Theo's answer: Calm",
	} {
		if !strings.Contains(asMaya, want) {
			t.Errorf("as Maya, lacks %q:\n%s", want, asMaya)
		}
	}
	asTheo := print("usr_theo")
	for _, want := range []string{
		"on studio-mac · “fernbrook” · 1 thing waiting for you",
		"  ▸ Claude asks: Add a note to “fernbrook” — waiting for you",
		"  ▸ Claude asks: Add another note to “fernbrook” — done by Maya",
		"  ? Claude asks Maya: Which music? — answered",
		"      Your answer: Calm",
	} {
		if !strings.Contains(asTheo, want) {
			t.Errorf("as Theo, lacks %q:\n%s", want, asTheo)
		}
	}
	if strings.Contains(asTheo, "started by") {
		t.Errorf("his own conversation says who started it:\n%s", asTheo)
	}
	// The list's words for the same conversation.
	if got := convStateWords(cv, "usr_maya"); got != "1 thing waiting for Theo" {
		t.Errorf("list state for Maya: %q", got)
	}
	if got := convStarterWords(cv, "usr_maya", "  "); got != "  started by Theo" {
		t.Errorf("list starter for Maya: %q", got)
	}
	if got := convStarterWords(cv, "usr_theo", "  "); got != "" {
		t.Errorf("list starter for Theo: %q", got)
	}
}
