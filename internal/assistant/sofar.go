package assistant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// "The project so far" (D185). A conversation that starts in a project hears,
// with its first message, what happened there before it: the versions (who,
// when, their note, approved or needing changes), the renders (review copy or
// final, of which version, the quality check), earlier conversations on the
// project with what was decided on their cards (done, not now, not quite and
// the person's note), the cards still waiting and the open questions about the
// project's clips. It is built in Go, newest first, and kept short (about
// 2,500 tokens at most); the brief and the rules are already in every
// message's context, so they aren't repeated. Claude reads it again with
// `metarush project so-far <project> --json` (on its read-only list), and the
// summary request (/compact) carries it so it survives a summary.

// soFarMaxChars bounds the text (about 2,250 tokens).
const soFarMaxChars = 9000

// ProjectSoFarIn names a project.
type ProjectSoFarIn struct {
	ProjectID string `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	// Except leaves one conversation out (the one asking).
	Except string `json:"except,omitempty" jsonschema:"description=Leave this conversation out of the earlier ones (the one asking)."`
}

// ProjectSoFarOut is what happened in a project so far, newest first.
type ProjectSoFarOut struct {
	ProjectID     string              `json:"projectId"`
	ProjectName   string              `json:"projectName"`
	Versions      []SoFarVersion      `json:"versions"`
	Renders       []SoFarRender       `json:"renders"`
	Conversations []SoFarConversation `json:"conversations"`
	Todos         []SoFarTodo         `json:"todos"`
	// Text is the same in a few lines, bounded: what Claude reads when a
	// conversation starts in the project.
	Text string `json:"text" jsonschema:"description=The same in a few lines\\, newest first\\, bounded (what Claude reads when a conversation starts in the project)."`
	// More says older things were left out to keep it short.
	More bool `json:"more,omitempty"`
}

// SoFarVersion is one version of the cut.
type SoFarVersion struct {
	RevisionID string    `json:"revisionId"`
	Number     int       `json:"number"`
	By         string    `json:"by,omitempty"`
	At         time.Time `json:"at"`
	Note       string    `json:"note,omitempty" jsonschema:"description=What it changed (its note or the change asked for)."`
	FromClaude bool      `json:"fromClaude,omitempty" jsonschema:"description=Saved from a card Claude wrote."`
	Approval   string    `json:"approval,omitempty" jsonschema:"enum=,enum=approved,enum=rejected"`
	ApprovedBy string    `json:"approvedBy,omitempty"`
	// ApprovalNote is what the person said with it ("What should change?").
	ApprovalNote string `json:"approvalNote,omitempty"`
	Latest       bool   `json:"latest,omitempty"`
}

// SoFarRender is one render.
type SoFarRender struct {
	RenderID       string    `json:"renderId"`
	RevisionNumber int       `json:"revisionNumber"`
	Quality        string    `json:"quality" jsonschema:"enum=review,enum=final"`
	State          string    `json:"state"`
	At             time.Time `json:"at"`
	QA             string    `json:"qa,omitempty" jsonschema:"description=What its quality check found\\, in plain words."`
}

// SoFarConversation is an earlier Claude Code conversation on the project.
type SoFarConversation struct {
	ID        string      `json:"id"`
	Title     string      `json:"title"`
	StartedBy string      `json:"startedBy,omitempty"`
	At        time.Time   `json:"at"`
	Cards     []SoFarCard `json:"cards"`
}

// SoFarCard is a card of an earlier conversation and what became of it.
type SoFarCard struct {
	Title string     `json:"title"`
	State string     `json:"state" jsonschema:"enum=done,enum=not_now,enum=not_quite,enum=failed,enum=waiting"`
	By    string     `json:"by,omitempty"`
	Note  string     `json:"note,omitempty" jsonschema:"description=The person's note (Not quite) or answer."`
	At    *time.Time `json:"at,omitempty"`
}

// SoFarTodo is an open question about one of the project's clips.
type SoFarTodo struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

func init() {
	ops.Def("project.sofar", "What happened in a project so far, newest first: versions, renders, earlier Claude Code conversations and what was decided on their cards, open cards and questions", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ProjectSoFarIn) (*ProjectSoFarOut, error) {
			return ProjectSoFar(ctx, a, in.ProjectID, in.Except)
		})
}

// soFarText is the bounded text for a conversation's first message (empty
// when there is nothing to say yet, or it couldn't be read).
func (m *Manager) soFarText(ctx context.Context, projectID, except string) string {
	sctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
	defer cancel()
	out, err := soFarForClaude(sctx, m.App, projectID, except, m.terms())
	if err != nil {
		return ""
	}
	return out.Text
}

// soFarForClaude is the project so far as Claude's first message carries it.
// What may leave (D290): a client that keeps its words from Claude gets no
// to-do questions (they quote what was said) and no card titles or notes
// (Claude's and people's words about it) there either.
func soFarForClaude(ctx context.Context, a *app.App, projectID, except, terms string) (*ProjectSoFarOut, error) {
	out, err := ProjectSoFar(ctx, a, projectID, except)
	if err != nil {
		return nil, err
	}
	if leave.KeepsWordsFromClaude(ctx, a, projectID, terms) {
		out.Todos = []SoFarTodo{}
		for i := range out.Conversations {
			for j := range out.Conversations[i].Cards {
				c := &out.Conversations[i].Cards[j]
				c.Title, c.Note = "a card", ""
			}
		}
		out.Text, out.More = soFarLines(out, time.Now())
	}
	return out, nil
}

// ProjectSoFar reads what happened in a project so far.
func ProjectSoFar(ctx context.Context, a *app.App, projectID, except string) (*ProjectSoFarOut, error) {
	if !ids.Valid(ids.Project, projectID) {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": projectID})
	}
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), projectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that project.").WithDetails(map[string]any{"projectId": projectID})
	}
	out := &ProjectSoFarOut{ProjectID: p.ID, ProjectName: p.Name, Versions: []SoFarVersion{}, Renders: []SoFarRender{}, Conversations: []SoFarConversation{}, Todos: []SoFarTodo{}}
	ws := a.WorkspaceID()

	// Versions, newest first (what they play is left out: only who, when, why).
	var revs []models.Revision
	_ = a.DB.Find(ctx, store.CRevisions, ws, bson.M{"projectId": p.ID, "detached": bson.M{"$ne": true}}, &revs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(16).
			SetProjection(bson.M{"spec": 0, "ops": 0, "transcripts": 0, "warnings": 0}))
	head := p.Branches["main"].Head
	for _, r := range revs {
		v := SoFarVersion{RevisionID: r.ID, Number: r.Number, By: r.CreatedBy.UserName, At: r.CreatedAt, Note: firstNonEmpty(r.Note, r.Instruction),
			FromClaude: strings.HasPrefix(r.Origin, "assistant:") || r.CreatedBy.Via == models.ViaAssistantAuto, Latest: r.ID == head}
		if s := r.Approval.State; s == models.ApprovalApproved || s == models.ApprovalRejected {
			v.Approval, v.ApprovalNote = s, r.Approval.Note
			if r.Approval.By != nil {
				v.ApprovedBy = r.Approval.By.UserName
			}
		}
		out.Versions = append(out.Versions, v)
	}

	// Renders, newest first.
	var rds []models.Render
	_ = a.DB.Find(ctx, store.CRenders, ws, bson.M{"projectId": p.ID}, &rds,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(10))
	for _, r := range rds {
		x := SoFarRender{RenderID: r.ID, RevisionNumber: r.RevisionNumber, Quality: r.Quality, State: r.State, At: r.CreatedAt}
		if x.Quality == "" {
			x.Quality = "review"
		}
		if r.QA != nil {
			x.QA = qaWords(models.QACounts{Errors: r.QA.Errors, Warnings: r.QA.Warnings}, r.QA.Verdict)
		}
		out.Renders = append(out.Renders, x)
	}

	// Earlier conversations on the project and what became of their cards.
	var convs []Conversation
	filter := bson.M{"projectId": p.ID, "archived": bson.M{"$ne": true}}
	if except != "" {
		filter["_id"] = bson.M{"$ne": except}
	}
	_ = a.DB.Find(ctx, store.CAssistantConversations, ws, filter, &convs,
		options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(8).
			SetProjection(bson.M{"title": 1, "userName": 1, "createdAt": 1, "updatedAt": 1, "turns.n": 1, "turns.user": 1, "turns.userId": 1,
				"actions.title": 1, "actions.state": 1, "actions.by": 1, "actions.note": 1, "actions.result": 1, "actions.decidedAt": 1,
				"actions.question": 1, "actions.turn": 1, "actions.auto": 1, "actions.autoMode": 1, "actions.keeping": 1}))
	for _, c := range convs {
		sc := SoFarConversation{ID: c.ID, Title: c.Title, StartedBy: c.UserName, At: c.UpdatedAt, Cards: []SoFarCard{}}
		for i := len(c.Actions) - 1; i >= 0; i-- { // newest first
			x := c.Actions[i]
			card := SoFarCard{Title: strings.TrimSuffix(x.Title, "."), By: x.By, At: x.DecidedAt}
			switch x.State {
			case ActionDone:
				card.State = "done"
				if x.Question != nil {
					card.Note = x.Result // the answer
				}
				if x.Auto && card.By == "" {
					card.By = "by itself"
				}
			case ActionDismissed:
				card.State = "not_now"
			case ActionNotQuite:
				card.State, card.Note = "not_quite", x.Note
			case ActionFailed:
				card.State = "failed"
			case ActionPending:
				card.State = "waiting"
				card.By = c.sender(x.Turn).name // who it waits for
			default:
				continue
			}
			sc.Cards = append(sc.Cards, card)
		}
		out.Conversations = append(out.Conversations, sc)
	}

	// Open questions about the project and its clips.
	var todos []models.Todo
	_ = a.DB.Find(ctx, store.CTodos, ws, bson.M{"state": models.TodoOpen, "$or": bson.A{bson.M{"projectId": p.ID}, bson.M{"projectIds": p.ID}}}, &todos,
		options.Find().SetSort(bson.D{{Key: "priority", Value: 1}, {Key: "createdAt", Value: -1}}).SetLimit(10))
	for _, t := range todos {
		out.Todos = append(out.Todos, SoFarTodo{ID: t.ID, Question: clip(strings.TrimSpace(t.Question), 160)})
	}
	out.Text, out.More = soFarLines(out, time.Now())
	return out, nil
}

// soFarLines writes the record for Claude: sections of lines, newest first,
// trimmed from their oldest ends until the whole fits soFarMaxChars.
func soFarLines(o *ProjectSoFarOut, now time.Time) (string, bool) {
	type section struct {
		head  string
		lines []string
	}
	var secs []*section
	if len(o.Versions) > 0 {
		s := &section{head: "Versions of the cut:"}
		for _, v := range o.Versions {
			l := fmt.Sprintf("- version %d (%s)", v.Number, v.RevisionID)
			if v.Latest {
				l += ", the latest"
			}
			if v.By != "" {
				l += ", saved by " + v.By
			}
			if v.FromClaude {
				l += " from Claude's card"
			}
			l += ", " + whenWords(v.At, now)
			if v.Note != "" {
				l += ": " + quoteClip(v.Note, 160)
			}
			switch v.Approval {
			case models.ApprovalApproved:
				l += "; approved" + byWho(v.ApprovedBy)
			case models.ApprovalRejected:
				l += "; needs changes" + byWho(v.ApprovedBy)
				if v.ApprovalNote != "" {
					l += ": " + quoteClip(v.ApprovalNote, 300)
				}
			}
			s.lines = append(s.lines, l)
		}
		secs = append(secs, s)
	}
	if len(o.Renders) > 0 {
		s := &section{head: "Renders:"}
		for _, r := range o.Renders {
			what := "review copy"
			if r.Quality == "final" {
				what = "final render"
			}
			l := fmt.Sprintf("- %s of version %d (%s), %s, %s", what, r.RevisionNumber, r.RenderID, whenWords(r.At, now), renderStateWords(r.State))
			if r.QA != "" {
				l += "; " + strings.ToLower(r.QA[:1]) + r.QA[1:]
			}
			s.lines = append(s.lines, l)
		}
		secs = append(secs, s)
	}
	if len(o.Conversations) > 0 {
		s := &section{head: "Earlier Claude Code conversations on this project:"}
		for _, c := range o.Conversations {
			l := "- " + quoteClip(c.Title, 80)
			if c.StartedBy != "" {
				l += ", started by " + c.StartedBy
			}
			l += ", last " + whenWords(c.At, now)
			if len(c.Cards) == 0 {
				l += "; no cards"
			} else {
				var cs []string
				for i, k := range c.Cards {
					if i == 6 {
						cs = append(cs, fmt.Sprintf("%d older", len(c.Cards)-6))
						break
					}
					cs = append(cs, cardWords(k))
				}
				l += "; cards: " + strings.Join(cs, "; ")
			}
			s.lines = append(s.lines, l)
		}
		secs = append(secs, s)
	}
	if len(o.Todos) > 0 {
		s := &section{head: "Open questions about the project's clips (to-dos):"}
		for _, t := range o.Todos {
			s.lines = append(s.lines, "- "+quoteClip(t.Question, 160)+" ("+t.ID+")")
		}
		secs = append(secs, s)
	}
	render := func() string {
		var b strings.Builder
		for _, s := range secs {
			if len(s.lines) == 0 {
				continue
			}
			b.WriteString(s.head + "\n")
			for _, l := range s.lines {
				b.WriteString(l + "\n")
			}
		}
		return strings.TrimRight(b.String(), "\n")
	}
	more := false
	text := render()
	for len(text) > soFarMaxChars {
		// Drop the oldest line of the longest section.
		sort.SliceStable(secs, func(i, j int) bool {
			return len(strings.Join(secs[i].lines, "")) > len(strings.Join(secs[j].lines, ""))
		})
		if len(secs) == 0 || len(secs[0].lines) == 0 {
			break
		}
		if len(secs[0].lines) == 1 {
			secs[0].lines[0] = clip(secs[0].lines[0], 400)
		} else {
			secs[0].lines = secs[0].lines[:len(secs[0].lines)-1]
		}
		more = true
		text = render()
	}
	if text == "" {
		return "", false
	}
	if more {
		text += "\n(Older things were left out to keep this short.)"
	}
	return text, more
}

// cardWords says what became of a card: "done: Add the note (Maya)",
// "not quite: Save the cut (Theo: “shorter opening”)".
func cardWords(k SoFarCard) string {
	what := map[string]string{"done": "done", "not_now": "not now", "not_quite": "not quite", "failed": "didn't work", "waiting": "waiting"}[k.State]
	s := what + ": " + clip(k.Title, 90)
	switch {
	case k.State == "waiting" && k.By != "":
		s += " (for " + k.By + ")"
	case k.Note != "" && k.By != "":
		s += " (" + k.By + ": " + quoteClip(k.Note, 200) + ")"
	case k.Note != "":
		s += " (" + quoteClip(k.Note, 200) + ")"
	case k.By != "":
		s += " (" + k.By + ")"
	}
	return s
}

func byWho(name string) string {
	if name == "" {
		return ""
	}
	return " by " + name
}

func quoteClip(s string, n int) string {
	return "“" + clip(strings.Join(strings.Fields(s), " "), n) + "”"
}

// whenWords says when, briefly: "today 3:10 PM", "yesterday", "Oct 20".
func whenWords(t, now time.Time) string {
	if t.IsZero() {
		return "some time ago"
	}
	t, now = t.Local(), now.Local()
	y1, m1, d1 := t.Date()
	y2, m2, d2 := now.Date()
	days := int(time.Date(y2, m2, d2, 0, 0, 0, 0, time.Local).Sub(time.Date(y1, m1, d1, 0, 0, 0, 0, time.Local)).Hours() / 24)
	switch {
	case days <= 0:
		return "today " + t.Format("3:04 PM")
	case days == 1:
		return "yesterday " + t.Format("3:04 PM")
	case y1 == y2:
		return t.Format("Jan 2")
	}
	return t.Format("Jan 2, 2006")
}
