package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

const archMermaid = "flowchart LR\n  cli[CLI] --> q[(Job queue)]\n  q --> w[Worker]\n  w --> s[(Media store)]\n"

// Slides from the Claude Code panel (D104–D106): asking to bring in a
// repository or a page, to make a code slide, a diagram or a repository card
// becomes a card with a plain title and the same op the CLI runs. A diagram's
// Mermaid comes from this conversation's proposals folder (never from anywhere
// else) and travels in the card; the person runs the card on the human
// channel and Claude hears the outcome.
func TestResourceRequestsBecomeCards(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	ctx := context.Background()
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Turns: []fakeclaude.Turn{
		{Match: "slides", Steps: []fakeclaude.Step{
			{Write: &fakeclaude.File{Name: "architecture.mmd", Content: archMermaid}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": `metarush resources make diagram widget --from {{proposals}}/architecture.mmd --name architecture --title "How it fits together" --project ` + projectID + ` --json`}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources make code widget --file internal/queue/queue.go --lines 46-63 --highlight 52-55 --name main-loop --project " + projectID + " --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources make stats widget --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources add https://github.com/acme/widget --project " + projectID + " --json"}}},
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush add https://acme.example/pricing --project " + projectID + " --depth 1 --json"}}},
			// A diagram file outside this conversation's proposals folder is never read.
			{Tool: &fakeclaude.ToolStep{Name: "Bash", Denied: true, Input: map[string]any{"command": "metarush resources make diagram widget --from /etc/hosts --json"}}},
			text("I asked for the slides."),
		}},
		{Match: "Done from the dashboard", Steps: []fakeclaude.Step{text("It is coming in.")}},
	}}})
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "make slides from our repo", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	cv := waitIdle(t, m, id)
	eventually(t, "a card for each request", func() bool {
		c, err := m.Get(ctx, id, true, true)
		if err != nil {
			t.Fatal(err)
		}
		cv = c
		return len(c.Actions) >= 5
	})
	if len(cv.Actions) != 5 {
		t.Fatalf("five cards (the file outside the proposals folder makes none): %+v", cv.Actions)
	}
	cards := map[string]AssistantAction{}
	for _, c := range cv.Actions {
		cards[c.Title] = c
		if strings.Contains(c.Input, "localhost") || strings.Contains(c.Input, "/etc/hosts") {
			t.Fatalf("a card read a file outside the proposals folder: %+v", c)
		}
	}
	project := m.nameOf(ctx, projectID)

	dia, ok := cards["Make the diagram “How it fits together”"]
	var mk ops.ResourceMakeIn
	if !ok || dia.Op != "resources.make" || dia.State != ActionPending || json.Unmarshal([]byte(dia.Input), &mk) != nil ||
		mk.Kind != "diagram" || mk.Source != archMermaid || mk.Format != "mermaid" || mk.Repo != "widget" || mk.Name != "architecture" || mk.ProjectID != projectID {
		t.Fatalf("diagram card: %+v %+v", dia, mk)
	}
	if d := strings.Join(dia.Details, " | "); !strings.Contains(d, "Mermaid, 4 lines") || !strings.Contains(d, "Shows: CLI, Job queue, Worker, Media store") ||
		!strings.Contains(d, "Into "+project+"'s resources") {
		t.Fatalf("diagram card details: %s", d)
	}
	code, ok := cards["Make a code slide of internal/queue/queue.go, lines 46–63"]
	mk = ops.ResourceMakeIn{}
	if !ok || code.Op != "resources.make" || json.Unmarshal([]byte(code.Input), &mk) != nil || mk.Kind != "code" || mk.File != "internal/queue/queue.go" ||
		mk.Lines != "46-63" || mk.Highlight != "52-55" || mk.Name != "main-loop" || !strings.Contains(strings.Join(code.Details, " | "), "Highlights lines 52–55") {
		t.Fatalf("code card: %+v %+v", code, mk)
	}
	if st, ok := cards["Make a card about “widget”"]; !ok || st.Op != "resources.make" || !strings.Contains(strings.Join(st.Details, " | "), "Next to the repository") {
		t.Fatalf("stats card: %+v", cards)
	}
	page, ok := cards["Take a snapshot of acme.example/pricing"]
	var fi ops.ResourceFetchIn
	if !ok || page.Op != "resources.fetch" || json.Unmarshal([]byte(page.Input), &fi) != nil || fi.Depth != 1 || fi.ProjectID != projectID ||
		!strings.Contains(strings.Join(page.Details, " | "), "5 in all") {
		t.Fatalf("page card: %+v %+v", page, fi)
	}
	repo, ok := cards["Bring in the repository acme/widget"]
	if !ok || repo.Op != "resources.fetch" || !strings.Contains(strings.Join(repo.Details, " | "), "nothing from it ever runs") {
		t.Fatalf("repository card: %+v", cards)
	}

	// The person brings the repository in from the dashboard: a fetch job on
	// this machine, the card links to the project's resources, Claude is told.
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: id, ActionID: repo.ID})
	res, err := ops.Call(ctx, &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	done := res.(*ConversationActOut).Action
	if done.State != ActionDone || !strings.Contains(done.Result, "Getting acme/widget from GitHub") || done.Link != "/p/"+projectID+"?tab=resources" {
		t.Fatalf("ran: %+v", done)
	}
	var job models.Job
	if err := a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeFetch, "payload.url": "https://github.com/acme/widget"}).Decode(&job); err != nil ||
		job.CreatedBy.Via != "dashboard" || job.Installation != a.Cfg.Installation.ID {
		t.Fatalf("the fetch job, asked from the dashboard on this machine: %+v %v", job, err)
	}
	// Claude hears when the repository is in, not when the job starts (D142).
	time.Sleep(300 * time.Millisecond)
	if cv = waitIdle(t, m, id); len(cv.Turns) != 1 || done.Work == nil || done.Work.State != WorkRunning {
		t.Fatalf("Claude was woken when the fetch only started: %+v %+v", cv.Turns, done.Work)
	}
	now := time.Now().UTC()
	if err := a.DB.Update(ctx, store.CJobs, a.WorkspaceID(), job.ID, bson.M{"$set": bson.M{"state": models.JobSucceeded, "finishedAt": now,
		"result": bson.M{"resourceIds": bson.A{"res_01jabcdefghjkmnpqrstvwxyz0"}, "names": bson.A{"widget"}, "message": "Brought in acme/widget at its current commit."}}}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "Claude to hear the repository is in", func() bool {
		c, _ := m.Get(ctx, id, false, false)
		return c != nil && len(c.Turns) == 2 && TurnTerminal(c.Turns[1].State)
	})
	cv = waitIdle(t, m, id)
	if len(cv.Turns) != 2 || !cv.Turns[1].Note || cv.Turns[1].Shown != "“widget” is in the resources" ||
		!strings.Contains(cv.Turns[1].Prompt, "resources show widget --project "+projectID) {
		t.Fatalf("Claude is told the outcome: %+v", cv.Turns)
	}
}

// Requests the dashboard can't run for Claude say what to do instead (files
// are dropped on the Resources tab, a video link is imported from the Add box,
// nothing on this computer or the local network is fetched), and the commands
// parse into their full paths (`motion check`, `resources make`).
func TestResourceRequestsThatAreNotCards(t *testing.T) {
	a := testApp(t, "")
	m := &Manager{App: a, names: map[string]string{}}
	ctx := context.Background()
	card := func(command string) *AssistantAction {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok {
			t.Fatalf("not a metarush command: %s", command)
		}
		act := &AssistantAction{Kind: call.Path}
		m.describeAction(ctx, nil, &call, act)
		return act
	}
	for cmd, title := range map[string]string{
		"metarush resources add ./logo.png --json":             "Add files",
		"metarush add https://youtu.be/aqz-KE-bpKQ --json":     "Import a video",
		"metarush resources add http://127.0.0.1:8080/ --json": "Bring in 127.0.0.1:8080",
	} {
		if act := card(cmd); act.State != ActionUnavailable || act.Op != "" || act.Title != title || act.Unavailable == "" {
			t.Fatalf("%s: %+v", cmd, act)
		}
	}
	// Not cards at all: Claude only hears the refusal and asks again properly.
	for _, cmd := range []string{
		"metarush resources make diagram widget --from /tmp/a.mmd --json", // no conversation here
		"metarush resources make code widget --lines 1-9 --json",          // no file
		"metarush resources make picture widget --json",
		"metarush resources add https://a.example https://b.example --json",
		"metarush resources make stats widget --project Tour --json", // a project by name
	} {
		if act := card(cmd); act.State != "" || act.Op != "" {
			t.Fatalf("%s became a card: %+v", cmd, act)
		}
	}
	for cmd, path := range map[string]string{
		"metarush motion check proposals/route-map --json":            "motion check",
		"metarush resources make code widget --file a.go --lines 1-9": "resources make",
		"metarush resources path widget --json":                       "resources path",
	} {
		words, _ := splitCommand(cmd)
		if call, _ := parseMetarush(words); call.Path != path {
			t.Fatalf("%s parses as %q", cmd, call.Path)
		}
	}
	if words, _ := splitCommand("metarush resources path widget --json"); !readOnlyCall(mustParse(t, words)) {
		t.Fatal("reading a repository's copy is a read")
	}
}

func mustParse(t *testing.T, words []string) cliCall {
	t.Helper()
	call, ok := parseMetarush(words)
	if !ok {
		t.Fatalf("not a metarush command: %v", words)
	}
	return call
}
