package leave

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Claude and AI apps (D290): what Claude reads of a client goes to the
// Claude account of the computer that answers — in the panel, Claude Code in
// a terminal using MetaRush's skills, or an AI app through MCP. Their words
// and pictures reach it only through operations that show them (transcripts,
// captions, cut sheets, frames, contact sheets): those refuse, on an AI's
// channel, a project whose Content Settings keep them here. Names, rules,
// notes and the person's own messages are what the person tells Claude.

func init() {
	ops.AIReads = aiReads
	ops.AIOutput = aiOutput
}

// Reader is who reads through MetaRush: Claude in the panel, Claude Code in
// a terminal, or an AI app through MCP (Is false: a person).
type Reader struct {
	Is bool
	// Words names it ("Claude in the panel").
	Words string
	// Terms are the Claude account's terms when known (commercial…).
	Terms string
	// Conversation is the panel's conversation and Project its project.
	Conversation string
	Project      string
	InProject    bool
	// Terminal: a command run where Claude Code started it (its CLAUDECODE
	// variable): a courtesy, as a person can clear it.
	Terminal bool
}

// TerminalNote tells a person in a terminal started from Claude Code (a
// tmux session, say) why MetaRush took them for Claude Code, and what to do.
func TerminalNote(err error, rd Reader) error {
	if err == nil || !rd.Terminal {
		return err
	}
	e := apperr.From(err)
	e.Hint = strings.TrimSpace(e.Hint + " MetaRush took this terminal for Claude Code's, as Claude Code started it. If you are a person typing here, use a terminal window Claude Code didn't start.")
	if m, ok := e.Details.(map[string]any); ok {
		m["claudeCodeTerminal"] = true
		m["asPerson"] = "CLAUDECODE= metarush …"
	}
	return e
}

// ReaderOf says whether an AI reads what this command or call shows.
func ReaderOf(ctx context.Context, a *app.App) Reader {
	switch a.Actor.Via {
	case models.ViaAssistant:
		r := Reader{Is: true, Words: "Claude in the panel"}
		if dir := os.Getenv("METARUSH_ASSISTANT_RUN_DIR"); dir != "" {
			if base := filepath.Base(dir); ids.Prefix(base) != "" {
				r.Conversation = base
				var c struct {
					ProjectID string `bson:"projectId"`
					Account   *struct {
						Terms string `bson:"terms"`
					} `bson:"account"`
				}
				if a.DB.C(store.CAssistantConversations).FindOne(ctx, bson.M{"_id": base, "workspaceId": a.WorkspaceID()}).Decode(&c) == nil {
					r.Project, r.InProject = c.ProjectID, c.ProjectID != ""
					if c.Account != nil {
						r.Terms = c.Account.Terms
					}
				}
			}
		}
		return r
	case "mcp":
		w := "an AI app"
		if a.Actor.Agent != "" {
			w = "the AI app " + a.Actor.Agent
		}
		return Reader{Is: true, Words: w}
	case "cli":
		if os.Getenv("CLAUDECODE") == "1" {
			return Reader{Is: true, Words: "Claude Code in a terminal", Terminal: true}
		}
	}
	return Reader{}
}

// aiReads refuses, on an AI's channel, an operation that would show a
// client's things their Content Settings keep here (ops.dispatch asks it).
// Deny by default: an operation that hasn't said what it shows is refused
// while any client has rules; one that shows kept things is refused unless
// every project they touch allows them — and when its input names none,
// every project counts.
func aiReads(ctx context.Context, a *app.App, op string, in any) error {
	rd := ReaderOf(ctx, a)
	if !rd.Is || !anyRules(ctx, a) {
		return nil
	}
	kinds, declared := ReadKinds(op)
	if !declared {
		return TerminalNote(refusedUndeclared(op), rd)
	}
	if len(kinds) == 0 {
		return nil
	}
	s := Send{Place: models.PlaceClaude, To: rd.Terms, ToWords: rd.Words, Kinds: kinds, What: "what " + logbook.OpWords(op) + " shows, to " + rd.Words}
	projects := ProjectsIn(ctx, a, in)
	if len(projects) == 0 {
		// Nothing names whose things they are: every project counts (one
		// look at the settings for all of them).
		c, err := newChecker(ctx, a)
		if err != nil {
			return err
		}
		for _, pid := range append(c.g.Projects(), "") {
			s.Projects = []string{pid}
			if r := c.problem(s); r != nil {
				Record(ctx, a, r, s)
				e := r.Err()
				who := "“" + r.SettingsName + "”"
				if r.ProjectName != "" {
					who = "“" + r.ProjectName + "”"
				}
				e.Message = fmt.Sprintf("This would show %s %s of %s, whose Content Settings keep them on this computer. Ask about one project whose rules allow it instead.",
					rd.Words, KindWords(firstKept(r, kinds)), who)
				return TerminalNote(e, rd)
			}
		}
		return nil
	}
	if rd.InProject {
		t := newToucher(ctx, a)
		t.add(projects...)
		t.project(rd.Project)
		projects = t.out
	}
	s.Projects = projects
	return TerminalNote(Check(ctx, a, s), rd)
}

func refusedUndeclared(op string) error {
	return apperr.New(apperr.KeptHere, "MetaRush can't tell what %s would show, so it isn't shown to AI apps while a client's Content Settings keep things on this computer.", logbook.OpWords(op)).
		WithHint("A person can look at it in the dashboard or a terminal.").WithDetails(map[string]any{"reason": "may_not_leave", "place": models.PlaceClaude, "op": op})
}

func firstKept(r *Refusal, kinds []string) string {
	if r.Kind != "" {
		return r.Kind
	}
	return kinds[0]
}

// KeptFromClaude says, for a conversation's project ("" a workspace
// conversation: the default's), the rule keeping these kinds from Claude
// (nil: none). The panel says it, and Claude hears it.
func KeptFromClaude(ctx context.Context, a *app.App, projectID, terms string, kinds []string) *Refusal {
	r, err := Problem(ctx, a, Send{Place: models.PlaceClaude, To: terms, ToWords: "Claude", Kinds: kinds, Projects: []string{projectID}})
	if err != nil {
		return nil
	}
	return r
}

// ClaudeNotice is the panel's line for a conversation whose client keeps
// things from Claude ("" none).
func ClaudeNotice(ctx context.Context, a *app.App, projectID, terms string) string {
	var kept []string
	var r0 *Refusal
	for _, k := range []string{models.LeaveWords, models.LeavePictures, models.LeaveFaces} {
		if r := KeptFromClaude(ctx, a, projectID, terms, []string{k}); r != nil {
			if r.Kind == "" { // only some accounts may receive them
				return fmt.Sprintf("“%s”'s Content Settings let only %s read their transcripts and pictures, so Claude here can't. %s", r.SettingsName,
					joinOr(onlyWords(models.PlaceClaude, onlyOf(ctx, a, r.Settings))), setLine(r))
			}
			kept = append(kept, KindWords(k))
			if r0 == nil {
				r0 = r
			}
		}
	}
	if r0 == nil {
		return ""
	}
	return fmt.Sprintf("“%s”'s Content Settings keep its %s on this computer, so Claude can't read them here. %s", r0.SettingsName, joinAnd(kept), setLine(r0))
}

func setLine(r *Refusal) string {
	if r.SetBy == "" {
		return ""
	}
	return upperFirst(setWords(r.SetBy, r.SetAt)) + "."
}

func onlyOf(ctx context.Context, a *app.App, project string) []string {
	rec, err := content.Record(ctx, a, project)
	if err != nil || rec == nil {
		return nil
	}
	return rec.Leave.Place(models.PlaceClaude).Only
}

// ClaudeRule is what Claude hears in a conversation whose client keeps
// things from it ("" none): plain, so it doesn't try other ways.
func ClaudeRule(ctx context.Context, a *app.App, projectID, terms string) string {
	n := ClaudeNotice(ctx, a, projectID, terms)
	if n == "" {
		return ""
	}
	return n + " MetaRush refuses the commands that would show them (transcripts, captions, cut sheets, frames, contact sheets); don't look for them another way. Work from names, the project's notes and what the person tells you."
}

// AnyKeptFromClaude reports whether any client's rules keep faces, pictures
// or words from Claude (or let only some accounts read them): then Claude in
// the panel reads no shared frame or cache folder, only copies MetaRush makes
// for its conversation of what it may see (unknown: yes).
func AnyKeptFromClaude(ctx context.Context, a *app.App) bool {
	if !anyRules(ctx, a) {
		return false
	}
	var recs []models.ContentSettings
	if err := a.DB.Find(ctx, store.CContentSettings, a.WorkspaceID(), bson.M{"leave": bson.M{"$exists": true}}, &recs, options.Find().SetProjection(bson.M{"leave": 1}).SetLimit(5000)); err != nil {
		return true
	}
	for _, r := range recs {
		if r.Leave == nil {
			continue
		}
		p := r.Leave.Place(models.PlaceClaude)
		if r.Leave.Nothing || len(p.Only) > 0 {
			return true
		}
		for _, k := range []string{models.LeaveFaces, models.LeavePictures, models.LeaveWords} {
			if contains(p.Keep, k) {
				return true
			}
		}
	}
	return false
}

// KeepsWordsFromClaude reports whether any project a project's things touch
// keeps words from Claude (for text MetaRush writes into Claude's messages
// itself, outside the reads check).
func KeepsWordsFromClaude(ctx context.Context, a *app.App, projectID, terms string) bool {
	if !anyRules(ctx, a) {
		return false
	}
	r, err := Problem(ctx, a, Send{Place: models.PlaceClaude, To: terms, ToWords: "Claude", Kinds: []string{models.LeaveWords}, Projects: ProjectTouches(ctx, a, projectID)})
	return err != nil || r != nil
}
