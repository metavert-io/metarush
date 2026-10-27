package models

import (
	"strings"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/costs"
)

// Drafts from AI apps (D248, D249): any agent a person connects — Claude Code
// in a terminal, Codex, Cursor, Claude Desktop — can be limited to proposing.
// What it proposes is saved as a draft that changes nothing until a person
// approves it on the review desk ("Awaiting your review"). A draft is one of:
//
//   - a version: a new version of a cut, checked and saved like any other but
//     not in the cut (the cut's latest version doesn't move). Approving puts it
//     in the cut (keeping both changes when someone saved meanwhile).
//   - a change: one operation and its input, never run until a person
//     approves it, then run as that person (a house rule, a spelling, the
//     brand, a clip's name…).

// Kinds of draft.
const (
	DraftVersion = "version"
	DraftChange  = "change"
)

// States of a draft.
const (
	DraftWaiting    = "waiting"     // awaiting a person's review
	DraftApproved   = "approved"    // a person approved it: it is in the cut, or the change was made
	DraftNotQuite   = "not_quite"   // a person said what should change; the agent may make a new draft
	DraftTurnedDown = "turned_down" // a person turned it down
	DraftReplaced   = "replaced"    // the agent made a newer draft in its place
	DraftApproving  = "approving"   // a person's approval is being carried out (one at a time)
	DraftInCut      = "in_cut"      // its change reached the cut another way: a person saved a change on top of it
)

// Draft is one thing an agent proposed (collection drafts, drf_…).
type Draft struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	ProjectID   string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Kind        string `bson:"kind" json:"kind" jsonschema:"enum=version,enum=change"`
	State       string `bson:"state" json:"state" jsonschema:"enum=waiting,enum=approving,enum=approved,enum=not_quite,enum=turned_down,enum=replaced,enum=in_cut"`
	// Op is the operation the agent asked for (cut.apply, content.rules.add…)
	// and Input its input as the agent gave it (a change runs exactly this).
	Op    string `bson:"op" json:"op"`
	Input string `bson:"input,omitempty" json:"input,omitempty"`
	// Title says in a few words what it does ("Add a house rule"), Changes
	// what changes, in plain words (a version: what changes in the video).
	Title   string   `bson:"title" json:"title"`
	Changes []string `bson:"changes,omitempty" json:"changes,omitempty"`
	// Note is the agent's own words: why.
	Note string `bson:"note,omitempty" json:"note,omitempty"`
	// A version draft: the version (not in the cut), its cut, and the version
	// it was written against (the cut's latest version then).
	RevisionID     string `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	RevisionNumber int    `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	Branch         string `bson:"branch,omitempty" json:"branch,omitempty"`
	BaseRevisionID string `bson:"baseRevisionId,omitempty" json:"baseRevisionId,omitempty"`
	// Warnings are what the check found that doesn't stop it, in plain words.
	Warnings []string `bson:"warnings,omitempty" json:"warnings,omitempty"`
	// Agent names the AI app that made it (codex, cursor, claude-code…), as
	// CreatedBy.Agent does; Via is how it reached MetaRush (mcp, cli).
	Agent     string    `bson:"agent" json:"agent"`
	CreatedBy Actor     `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	// Replaces is the draft this one takes the place of (after Not quite).
	Replaces   string `bson:"replaces,omitempty" json:"replaces,omitempty"`
	ReplacedBy string `bson:"replacedBy,omitempty" json:"replacedBy,omitempty"`
	// Decision is a person's: approve, Not quite (with what should change) or
	// turn down.
	Decision *DraftDecision `bson:"decision,omitempty" json:"decision,omitempty"`
	// Target is a Content Settings change's settings, resolved when the draft
	// was saved: approving changes those, whichever are the default then.
	Target *DraftTarget `bson:"target,omitempty" json:"target,omitempty"`
	// Expect is what approving was shown to do, measured when saved (a
	// brand's every part, the places a spelling corrects); approving is
	// refused when it would now do more or other.
	Expect *DraftExpect `bson:"expect,omitempty" json:"expect,omitempty"`
	// Cost is MetaRush's estimate of what approving costs at outside
	// services, made when the draft was saved (and again when approved).
	Cost *costs.Estimate `bson:"cost,omitempty" json:"cost,omitempty"`
	// ApprovingAt: a person's approval started then (state approving); an
	// approval left unfinished for minutes may be started again.
	ApprovingAt *time.Time `bson:"approvingAt,omitempty" json:"approvingAt,omitempty"`
	// Tried is the last time a person's approval couldn't be carried out
	// (the cut changed in the same places, a setting refused it): the draft
	// stays waiting and says why.
	Tried *DraftTry `bson:"tried,omitempty" json:"tried,omitempty"`
}

// DraftDecision is what a person decided about a draft.
type DraftDecision struct {
	State string    `bson:"state" json:"state" jsonschema:"enum=approved,enum=not_quite,enum=turned_down,enum=in_cut"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
	Note  string    `bson:"note,omitempty" json:"note,omitempty"`
	// What approving did: the version now in the cut (the draft's own, or a
	// new one when someone saved meanwhile and both changes were kept).
	RevisionID     string `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	RevisionNumber int    `bson:"revisionNumber,omitempty" json:"revisionNumber,omitempty"`
	// Result says in words what the change did ("Added the house rule").
	Result string `bson:"result,omitempty" json:"result,omitempty"`
	// Seen: the agent has been told (its next answer carried it).
	Seen bool `bson:"seen,omitempty" json:"seen,omitempty"`
}

// DraftTry is an approval that couldn't be carried out.
type DraftTry struct {
	By      Actor     `bson:"by" json:"by"`
	At      time.Time `bson:"at" json:"at"`
	Message string    `bson:"message" json:"message"`
	Code    string    `bson:"code,omitempty" json:"code,omitempty"`
}

// MaxAgentName is the longest agent name kept.
const MaxAgentName = 40

// AgentSlug turns what an AI app calls itself (a --agent value, an MCP
// client's name) into the short name MetaRush keeps: lower case, letters,
// digits and dashes. Well-known apps get their usual name whatever they send
// ("codex-mcp-client" is codex, "cursor-vscode" is cursor).
func AgentSlug(s string) string {
	l := strings.ToLower(strings.TrimSpace(s))
	for _, k := range knownAgents {
		if strings.Contains(l, k.match) {
			return k.slug
		}
	}
	var b strings.Builder
	dash := false
	for _, r := range l {
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case b.Len() > 0 && !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if len(out) > MaxAgentName {
		out = strings.Trim(out[:MaxAgentName], "-")
	}
	return out
}

var knownAgents = []struct{ match, slug, name string }{
	{"claude-code", "claude-code", "Claude Code"},
	{"claude code", "claude-code", "Claude Code"},
	{"claude-ai", "claude-desktop", "Claude Desktop"},
	{"claude-desktop", "claude-desktop", "Claude Desktop"},
	{"claude desktop", "claude-desktop", "Claude Desktop"},
	{"codex", "codex", "Codex"},
	{"cursor", "cursor", "Cursor"},
	{"windsurf", "windsurf", "Windsurf"},
	{"gemini", "gemini", "Gemini CLI"},
	{"visual studio code", "vscode", "VS Code"},
	{"vscode", "vscode", "VS Code"},
	{"zed", "zed", "Zed"},
	{"cline", "cline", "Cline"},
	{"goose", "goose", "Goose"},
}

// AgentName is an agent's name for people: "Codex", "Cursor", "Claude Code";
// another app's short name with a capital ("Acme-bot" → "Acme-bot").
func AgentName(slug string) string {
	if slug == "" {
		return ""
	}
	if slug == AgentClaude {
		return "Claude"
	}
	for _, k := range knownAgents {
		if k.slug == slug {
			return k.name
		}
	}
	r := []rune(slug)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

// AgentClaude is Claude in MetaRush's own Claude Code panel, or Claude Code
// driving the command line before agents had names (D188's "Claude made it").
const AgentClaude = "claude"

// DraftTarget is the Content Settings a change draft changes.
type DraftTarget struct {
	// ContentID is the settings record (empty when the project has none yet);
	// ProjectID the project whose own settings they are, Name its name.
	ContentID string `bson:"contentId,omitempty" json:"contentId,omitempty"`
	ProjectID string `bson:"projectId" json:"projectId"`
	Name      string `bson:"name" json:"name"`
	// Default: they were the default settings (what new projects use).
	Default bool `bson:"default,omitempty" json:"default,omitempty"`
}

// DraftExpect is what approving a change draft was shown to do.
type DraftExpect struct {
	// Kit is the brand as approving leaves it, part by part.
	Kit map[string]string `bson:"kit,omitempty" json:"kit,omitempty"`
	// Places are the places in existing transcripts a spelling corrects.
	Places []string `bson:"places,omitempty" json:"places,omitempty"`
	// Fixes: the draft corrects existing transcripts (Places may be none).
	Fixes bool `bson:"fixes,omitempty" json:"fixes,omitempty"`
}
