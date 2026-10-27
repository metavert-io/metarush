package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Action card states.
const (
	ActionPending     = "pending"
	ActionRunning     = "running" // a safe step that started by itself and is still running (D141)
	ActionDone        = "done"
	ActionDismissed   = "dismissed"
	ActionFailed      = "failed"
	ActionUnavailable = "unavailable"
	// ActionNotQuite: the person wants it changed first (D186): their note
	// went to Claude at once as their message, asking for a new version.
	ActionNotQuite = "not_quite"
)

// AssistantAction is a change Claude Code asked for but may not make itself: its
// permissions stay read-only, so the command was refused. The dashboard shows it
// as a card; a person runs it through the ordinary ops endpoint (the human
// channel, exactly like a plan card's Apply) or dismisses it. Claude is told
// the outcome as the next message of the conversation.
type AssistantAction struct {
	ID        string            `bson:"id" json:"id"`
	Turn      int               `bson:"turn" json:"turn"`
	ToolUseID string            `bson:"toolUseId,omitempty" json:"toolUseId,omitempty"`
	Kind      string            `bson:"kind" json:"kind" jsonschema:"description=The metarush command (e.g. render submit)."`
	Title     string            `bson:"title" json:"title" jsonschema:"description=What will happen, in plain words."`
	Details   []string          `bson:"details,omitempty" json:"details,omitempty"`
	Command   string            `bson:"command,omitempty" json:"command,omitempty" jsonschema:"description=The exact command Claude asked to run (raw view only)."`
	Op        string            `bson:"op,omitempty" json:"op,omitempty" jsonschema:"description=The operation the dashboard runs when the person confirms."`
	Input     string            `bson:"input,omitempty" json:"-"`
	HumanOnly bool              `bson:"humanOnly,omitempty" json:"humanOnly,omitempty" jsonschema:"description=A decision only people make (e.g. approving a cut)."`
	Check     *ops.EditCheckOut `bson:"check,omitempty" json:"check,omitempty"`
	// SheetCheck and SheetLines are set on cards that save a cut sheet: the
	// check (changes in plain words, problems by line) and the changed lines.
	SheetCheck  *ops.CutCheckOut    `bson:"sheetCheck,omitempty" json:"sheetCheck,omitempty"`
	SheetLines  []cutsheet.DiffLine `bson:"sheetLines,omitempty" json:"sheetLines,omitempty"`
	Proposal    string              `bson:"proposal,omitempty" json:"proposal,omitempty" jsonschema:"description=The conversation's plan this card applies (t<turn>/<file>); the dashboard shows this card instead of a second plan card."`
	Rationale   string              `bson:"rationale,omitempty" json:"rationale,omitempty" jsonschema:"description=Why Claude proposes the change (from the plan)."`
	State       string              `bson:"state" json:"state" jsonschema:"enum=pending,enum=running,enum=done,enum=dismissed,enum=failed,enum=unavailable,enum=not_quite"`
	Unavailable string              `bson:"unavailable,omitempty" json:"unavailable,omitempty" jsonschema:"description=Why the dashboard cannot do this one."`
	By          string              `bson:"by,omitempty" json:"by,omitempty" jsonschema:"description=Who decided it (their name)."`
	ByID        string              `bson:"byId,omitempty" json:"byId,omitempty" jsonschema:"description=Who decided it (absent on cards decided by older versions and on safe steps that started by themselves)."`
	Result      string              `bson:"result,omitempty" json:"result,omitempty"`
	// Note is what the person said should change ("Not quite…", D186).
	Note  string `bson:"note,omitempty" json:"note,omitempty" jsonschema:"description=What the person said should change (Not quite)."`
	Error string `bson:"error,omitempty" json:"error,omitempty"`
	Link  string `bson:"link,omitempty" json:"link,omitempty" jsonschema:"description=Dashboard route to what the action produced."`
	// ForID and ForName are who the card is for (D179): the person whose
	// message Claude was answering when it asked. Computed when shown.
	ForID   string `bson:"-" json:"forId,omitempty" jsonschema:"description=Who it is for: the person whose message Claude was answering when it asked (anyone in the conversation may decide it)."`
	ForName string `bson:"-" json:"forName,omitempty"`
	// Review and Image are set on cards about a project motion graphic: where
	// the person reviews it (source, frames, preview) and its contact sheet.
	Review string `bson:"review,omitempty" json:"review,omitempty" jsonschema:"description=Dashboard route to review what the card decides about (before deciding)."`
	// Fit is set on a card that uses Claude's fitted script (D86): both
	// versions side by side, with the reason for each change.
	Fit *voiceover.FittedOut `bson:"fit,omitempty" json:"fit,omitempty"`
	// SetUp is what this computer sets up first when the card runs (studio
	// quality for a clearer voice: people only, a one-time download).
	SetUp []string `bson:"setUp,omitempty" json:"setUp,omitempty" jsonschema:"description=What this computer sets up first when the card runs (studio: studio quality\\, a one-time download of about 1.8 GB)."`
	Image string   `bson:"image,omitempty" json:"image,omitempty" jsonschema:"description=A picture of it (same-origin media URL)."`
	// Frames and Preview are a motion graphic's test frames and its preview
	// video (same-origin media URLs), shown large on click.
	Frames  []CardPicture `bson:"frames,omitempty" json:"frames,omitempty" jsonschema:"description=Pictures to look at before deciding (a motion graphic's test frames)."`
	Preview string        `bson:"preview,omitempty" json:"preview,omitempty" jsonschema:"description=A short preview video (same-origin media URL)."`
	// Auto: a safe step that started by itself, as the person's setting
	// allows (D141): a check of Claude's graphic, a review copy, a link the
	// person gave.
	Auto bool `bson:"auto,omitempty" json:"auto,omitempty" jsonschema:"description=It started by itself (a safe step\\, under the person's setting)."`
	// AutoMode: it started by itself because the person it is for has Auto
	// on (D186): a change that can be undone, not only a safe step.
	AutoMode bool `bson:"autoMode,omitempty" json:"autoMode,omitempty" jsonschema:"description=It started by itself because Auto is on for the person it is for (a change that can be undone)."`
	// AutoPriced: it costs money and started by itself because the person it
	// is for has Auto on and estimates off (D208), within their budgets.
	AutoPriced bool `bson:"autoPriced,omitempty" json:"autoPriced,omitempty" jsonschema:"description=It uses a paid outside service and started by itself: Auto is on and estimates are off for the person it is for (within budgets and limits)."`
	// AutoWaits says why Auto didn't start a change that costs money by
	// itself (D241): a project's budget or the team's daily limit nobody set.
	// SpendsIn is the project whose budget a priced card spends from, as its
	// operation works it out (a take's parent's project, a clip's own): Auto
	// checks that project's limits, and records what it spent there (D241).
	SpendsIn  string `bson:"spendsIn,omitempty" json:"spendsIn,omitempty" jsonschema:"description=The project whose budget it spends from."`
	AutoWaits string `bson:"autoWaits,omitempty" json:"autoWaits,omitempty" jsonschema:"description=Why Auto didn't start this change that costs money by itself: a budget or daily limit nobody set."`
	// Estimate is what a card that uses paid outside AI services is expected
	// to cost, part by part (D208): the dashboard shows "about $0.40" that
	// opens to the services, models, sources and each part's estimate.
	Estimate *costs.Estimate `bson:"estimate,omitempty" json:"estimate,omitempty"`
	// SavesVersion: running it saves a new version of the cut, so the card
	// offers to render a review copy in the same step (D144).
	SavesVersion bool `bson:"savesVersion,omitempty" json:"savesVersion,omitempty" jsonschema:"description=It saves a new version of the cut (the card offers Save and render a review copy)."`
	// Work is what the card started that takes a while; Claude hears when it ends (D142).
	Work *ActionWork `bson:"work,omitempty" json:"work,omitempty"`
	// Keeping makes this a "Keep this?" card (D166): kept with Keep, declined
	// for good with Not now.
	Keeping *KeepCard `bson:"keeping,omitempty" json:"keeping,omitempty"`
	// Question makes this a question card (D143); Answer is the person's answer.
	Question *QuestionCard   `bson:"question,omitempty" json:"question,omitempty"`
	Answer   *QuestionAnswer `bson:"answer,omitempty" json:"answer,omitempty"`
	// The Recipes cards (D211, recipe_actions.go): a run's plan, a save's proposal, a draft's changes.
	Recipe *RecipeCardView `bson:"recipe,omitempty" json:"recipe,omitempty" jsonschema:"description=Recipes: a run's plan with its steps\\, what saving a cut would vary and keep\\, or what a draft or a version made current changes."`
	// Told: Claude was told this card's outcome. ForClaude is what Claude needs
	// to carry on (IDs, versions, exact problems): only ever in its notes.
	Told      bool       `bson:"told,omitempty" json:"-"`
	ForClaude string     `bson:"forClaude,omitempty" json:"-"`
	Batch     string     `bson:"batch,omitempty" json:"-"` // cards decided in one go are told together
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
	DecidedAt *time.Time `bson:"decidedAt,omitempty" json:"decidedAt,omitempty"`
}

// CardPicture is one picture on a card.
type CardPicture struct {
	URL   string `bson:"url" json:"url" jsonschema:"description=Same-origin media URL."`
	Label string `bson:"label,omitempty" json:"label,omitempty"`
}

// ConversationActIn runs (or dismisses) one or several of a conversation's
// cards, or answers a question card. People only.
type ConversationActIn struct {
	ID       string `json:"id" jsonschema:"description=Conversation ID."`
	ActionID string `json:"actionId,omitempty"`
	// ActionIDs decides several cards in one go (Approve all): Claude hears
	// about all of them in one message.
	ActionIDs []string `json:"actionIds,omitempty" jsonschema:"maxItems=30,description=Several cards at once (Approve all); Claude hears about them in one message."`
	Dismiss   bool     `json:"dismiss,omitempty"`
	// NotQuite sends the card back to Claude with what should change (D186):
	// the card is marked "not quite" and the note goes at once as the
	// person's message, asking for a new version.
	NotQuite string `json:"notQuite,omitempty" jsonschema:"maxLength=2000,description=Not quite: what should change. Claude gets it at once as your message and makes a new version."`
	Quiet    bool   `json:"quiet,omitempty" jsonschema:"description=Do not tell Claude the outcome."`
	// ReviewCopy also renders a review copy of the version a card saves.
	ReviewCopy bool `json:"reviewCopy,omitempty" jsonschema:"description=For a card that saves a new version: also render a review copy of it (Claude hears once\\, when the copy and its quality check are done)."`
	// Answer answers a question card.
	Answer *QuestionAnswer `json:"answer,omitempty"`
	// Keep is for a card that uses Claude's fitted script: the lines (IDs or
	// numbers) where the person keeps their own words and start.
	Keep []string `json:"keep,omitempty" jsonschema:"maxItems=300,description=For Claude's fitted script: the lines where you keep your own words."`
	// KeepWhere is where a "Keep this rule?" card keeps it (D193): just this
	// project (project, the default) or every project using its settings.
	KeepWhere string `json:"keepWhere,omitempty" jsonschema:"enum=,enum=project,enum=settings,description=For a Keep this rule card: just this project (project) or every project using its settings (settings)."`
}

// ConversationActOut is the card after it ran (Actions: every card decided).
type ConversationActOut struct {
	Action       AssistantAction   `json:"action"`
	Actions      []AssistantAction `json:"actions"`
	Conversation Conversation      `json:"conversation"`
}

// actionFor turns a refused metarush command into an action card. It returns
// nil for commands that are not changes (read-only commands refused for other
// reasons, such as shell syntax).
func (m *Manager) actionFor(ctx context.Context, c *conv, turn int, d deniedCall) *AssistantAction {
	if d.Name != "Bash" {
		return nil
	}
	var in struct {
		Command string `json:"command"`
	}
	_ = json.Unmarshal(d.Input, &in)
	words, err := splitCommand(in.Command)
	if err != nil {
		return nil
	}
	call, ok := parseMetarush(words)
	if !ok || readOnlyCall(call) {
		return nil
	}
	a := &AssistantAction{ID: shortID("act"), Turn: turn, ToolUseID: d.ToolUseID, Kind: call.Path, Command: clip(strings.Join(strings.Fields(in.Command), " "), 2000),
		State: ActionPending, CreatedAt: time.Now().UTC()}
	m.describeAction(ctx, c, &call, a)
	if a.State == "" {
		return nil
	}
	a.SavesVersion = savesVersion(a.Op)
	return a
}

func setInput(a *AssistantAction, op string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		a.State, a.Unavailable = ActionUnavailable, "MetaRush could not prepare this action."
		return
	}
	a.Op, a.Input = op, string(b)
}

// unavailable marks a change the dashboard cannot run for Claude; the card
// tells the person what to do instead (e.g. drop files to import them).
func unavailable(a *AssistantAction, title, why string) {
	a.Title, a.State, a.Unavailable = title, ActionUnavailable, why
}

// incomplete marks a request the dashboard could not turn into a card (an
// unknown command, a missing or invalid ID, a plan outside this conversation):
// no card is shown; Claude only sees the refusal and can ask again properly.
func incomplete(a *AssistantAction, title, why string) {
	a.Title, a.State, a.Unavailable = title, "", why
}

// describeAction fills the card: a plain-language title, details and the op +
// input the dashboard runs. Inputs are built here (never taken from the browser).
func (m *Manager) describeAction(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	arg := first(call.Args)
	switch call.Path {
	case "render submit":
		rev := call.Flags["revision"]
		if !ids.Valid(ids.Revision, rev) {
			incomplete(a, "Render a revision", "Claude did not name a valid revision.")
			return
		}
		quality := call.Flags["quality"]
		if quality == "" {
			quality = "review"
		}
		if quality != "review" && quality != "final" {
			incomplete(a, "Render "+m.nameOf(ctx, rev), "Unknown quality "+strconv.Quote(quality)+".")
			return
		}
		preset := call.Flags["preset"]
		if preset != "" && !knownPreset(preset) {
			incomplete(a, "Render "+m.nameOf(ctx, rev), "Unknown preset "+strconv.Quote(preset)+".")
			return
		}
		name := m.nameOf(ctx, rev)
		if quality == "review" {
			a.Title = "Render a review copy of " + name
			a.Details = append(a.Details, "Half resolution, fast")
		} else {
			a.Title = "Make the final render of " + name
			a.Details = append(a.Details, "Full quality from the original footage")
		}
		if preset != "" {
			a.Details = append(a.Details, presetLabel(preset))
		}
		setInput(a, "render.submit", ops.RenderSubmitIn{RevisionID: rev, Quality: quality, Preset: preset})
	case "edit apply":
		m.describeApply(ctx, c, call, a)
	case "cut apply":
		m.describeCutApply(ctx, c, call, a)
	case "cut transition":
		m.describeCutTransition(ctx, call, a)
	case "cut captions":
		m.describeCutCaptions(ctx, call, a)
	case "footage short":
		m.describeFootageShort(ctx, call, a)
	case "cut add", "cut move", "cut trim", "cut split", "cut delete", "cut delete-range", "cut remove-words", "cut pauses", "cut flashes", "cut names", "cut use-fixed", "content pack apply":
		m.describeCutEdit(ctx, call, a)
	case "qa run":
		if !ids.Valid(ids.Render, arg) && !ids.Valid(ids.Revision, arg) {
			incomplete(a, "Check a render for problems", "Claude did not name a render or revision.")
			return
		}
		a.Title = "Check the render for problems"
		if name := m.lookupName(ctx, arg); name != "" {
			a.Title = "Check the render of " + name + " for problems"
		}
		a.Details = []string{"Watches the whole video for black or frozen frames, silence and layout problems, and checks how loud it is"}
		setInput(a, "qa.run", ops.QARunIn{ID: arg, Force: hasBool(call, "force")})
	case "revision approve":
		if !ids.Valid(ids.Revision, arg) {
			incomplete(a, "Approve a revision", "Claude did not name a valid revision.")
			return
		}
		reject := hasBool(call, "reject")
		a.HumanOnly = true
		name := m.nameOf(ctx, arg)
		a.Title = "Approve " + name
		if reject {
			a.Title = "Reject " + name
		}
		a.Details = []string{"Only people approve cuts. Claude suggested it; the decision is yours."}
		if n := call.Flags["note"]; n != "" {
			a.Details = append(a.Details, "Note: "+clip(n, 300))
		}
		setInput(a, "revision.approve", ops.RevisionApproveIn{ID: arg, Reject: reject, Note: clip(call.Flags["note"], 2000)})
	case "project create":
		name := strings.TrimSpace(strings.Join(call.Args, " "))
		if name == "" {
			incomplete(a, "Create a project", "Claude did not give the project a name.")
			return
		}
		a.Title = "Create the project “" + clip(name, 80) + "”"
		brief := models.Brief{Goal: call.Flags["goal"], Audience: call.Flags["audience"], Tone: call.Flags["tone"]}
		if p := call.Flags["platform"]; p != "" {
			brief.Platforms = []string{p}
		}
		if t, err := strconv.ParseFloat(call.Flags["target-seconds"], 64); err == nil && t > 0 {
			brief.TargetSecs = t
		}
		if brief.Goal != "" {
			a.Details = append(a.Details, "Goal: "+clip(brief.Goal, 200))
		}
		setInput(a, "project.create", ops.ProjectCreateIn{Name: clip(name, 120), Brief: brief})
	case "footage analyze":
		in := ops.AnalyzeIn{ProjectID: call.Flags["project"], Force: hasBool(call, "force")}
		if ids.Valid(ids.Asset, arg) {
			in.AssetID = arg
		} else if ids.Valid(ids.Project, arg) && in.ProjectID == "" {
			in.ProjectID = arg
		}
		if in.ProjectID == "" && in.AssetID == "" {
			incomplete(a, "Analyze footage", "Claude did not say which footage.")
			return
		}
		if in.AssetID != "" {
			a.Title = "Analyze " + m.nameOf(ctx, in.AssetID)
		} else {
			a.Title = "Analyze the footage in " + m.nameOf(ctx, in.ProjectID)
		}
		a.Details = []string{"Transcripts, pauses, scenes and loudness, on this machine"}
		setInput(a, "footage.analyze", in)
	case "comment add":
		body := strings.TrimSpace(strings.Join(call.Args, " "))
		prj := call.Flags["project"]
		if body == "" || !ids.Valid(ids.Project, prj) {
			incomplete(a, "Add a note", "Claude did not give the note or its project.")
			return
		}
		in := ops.CommentAddIn{ProjectID: prj, RevisionID: call.Flags["revision"], AssetID: call.Flags["asset"], Body: clip(body, 4000)}
		if at, err := strconv.ParseFloat(call.Flags["at"], 64); err == nil && at >= 0 {
			in.At = &at
		}
		a.Title = "Add a note to " + m.nameOf(ctx, prj)
		a.Details = []string{"“" + clip(body, 300) + "”"}
		setInput(a, "comment.add", in)
	case "asset link":
		prj := call.Flags["project"]
		if !ids.Valid(ids.Asset, arg) || !ids.Valid(ids.Project, prj) {
			incomplete(a, "Add footage to a project", "Claude did not name the footage and the project.")
			return
		}
		a.Title = "Add " + m.nameOf(ctx, arg) + " to " + m.nameOf(ctx, prj)
		if hasBool(call, "unlink") {
			a.Title = "Take " + m.nameOf(ctx, arg) + " out of " + m.nameOf(ctx, prj)
		}
		setInput(a, "asset.link", ops.AssetLinkIn{AssetID: arg, ProjectID: prj, Unlink: hasBool(call, "unlink")})
	case "render cancel":
		if !ids.Valid(ids.Render, arg) {
			incomplete(a, "Cancel a render", "Claude did not name a render.")
			return
		}
		a.Title = "Cancel this render"
		if name := m.lookupName(ctx, arg); name != "" {
			a.Title = "Cancel the render of " + name
		}
		setInput(a, "render.cancel", ops.RenderIDIn{ID: arg})
	case "footage speakers rename", "footage speakers reject", "footage speakers hint", "people rename", "people merge", "people split",
		"todos answer", "todos dismiss":
		m.describeVoicesAndQuestions(ctx, call, a)
	case "footage faces name":
		face, name := first(call.Args), clip(strings.TrimSpace(strings.Join(call.Args[min(1, len(call.Args)):], " ")), 80)
		if !ids.Valid(ids.Face, face) || name == "" {
			incomplete(a, "Name a face", "Claude did not name the face and who it is.")
			return
		}
		a.Title = fmt.Sprintf("That face is “%s”", name)
		a.Details = []string{"Its name shows when that face is on screen in its clip"}
		setInput(a, "footage.faces.name", ops.FaceNameIn{FaceID: face, Name: name, NewPerson: hasBool(call, "new-person")})
	case "footage faces reject":
		face, name := first(call.Args), clip(strings.TrimSpace(strings.Join(call.Args[min(1, len(call.Args)):], " ")), 80)
		if !ids.Valid(ids.Face, face) {
			incomplete(a, "Correct a face", "Claude did not name the face.")
			return
		}
		a.Title = "That face is not " + orDefaultName(name, "who it says")
		setInput(a, "footage.faces.reject", ops.FaceRejectIn{FaceID: face, Person: name})
	case "footage describe":
		if !ids.Valid(ids.Asset, arg) || hasBool(call, "all") || call.Flags["save"] != "" {
			incomplete(a, "Describe the scenes", "Claude did not name one clip.")
			return
		}
		a.Title = "Describe the scenes of " + m.nameOf(ctx, arg)
		a.Details = []string{"Claude Code writes a sentence or two per scene; it uses your Claude Code usage"}
		setInput(a, "footage.describe", DescribeIn{AssetID: arg, Force: hasBool(call, "force")})
	case "ingest":
		unavailable(a, "Import footage", "Add footage from the dashboard: drop the files anywhere on the page or use Upload.")
	case "import":
		m.describeLinkImport(ctx, call, a)
	case "voiceover script set", "voiceover script import", "voiceover beat add", "voiceover beat update", "voiceover beat move", "voiceover beat remove",
		"voiceover fit", "voiceover scratch", "voiceover tts", "voiceover import", "voiceover cleanup", "voiceover takes choose", "mix set",
		"providers set", "providers remove", "providers test", "providers settings", "voiceover clone", "voiceover record", "voiceover takes place":
		m.describeVoiceover(ctx, c, call, a)
	case "voiceover script fit", "voiceover script use":
		m.describeFit(ctx, c, call, a)
	case "deliver":
		unavailable(a, "Export approved cuts", "Exporting saves files into a folder on this machine, so it isn't started from here. Download the finished video from Renders instead, or export it from a terminal.")
	case "motion check":
		m.describeMotionCheck(ctx, c, call, a)
	case "motion approve", "motion revoke":
		m.describeMotionDecision(ctx, call, a)
	case "motion new", "motion studio":
		unavailable(a, "Start a motion graphic", "Claude writes a motion graphic's files itself, in this conversation, and asks to check them from a card.")
	default:
		if generateCommands[call.Path] { // generated video, looks and consent (generate_actions.go)
			m.describeGenerate(ctx, c, call, a)
			return
		}
		if sayCommands[call.Path] { // words changed in a speaker's own voice (say_actions.go, D308)
			m.describeSay(ctx, c, call, a)
			return
		}
		if aiCommands[call.Path] { // AI services: pictures, speech, music, dubs… (ai_actions.go)
			m.describeAI(ctx, c, call, a)
			return
		}
		if libraryCommands[call.Path] { // filing footage away (library_actions.go)
			m.describeLibrary(ctx, call, a)
			return
		}
		if resourceCommands[call.Path] { // repositories, pages and slide pictures (resource_actions.go)
			m.describeResource(ctx, c, call, a)
			return
		}
		if conversationCommands[call.Path] { // conversations from calls (conversation_actions.go)
			m.describeConversation(ctx, call, a)
			return
		}
		if soundCommands[call.Path] { // levels, clearer voices, music (sound_actions.go)
			m.describeSound(ctx, call, a)
			return
		}
		if transcriptCommands[call.Path] { // fixing transcripts, spelling lists, text fixes waiting (transcript_actions.go)
			m.describeTranscript(ctx, call, a)
			return
		}
		if keepCommands[call.Path] != "" { // "Keep this?" cards (keep.go, D166)
			m.describeKeep(ctx, c, call, a)
			return
		}
		if memoryCommands[call.Path] { // rules, sign-offs, picture switches (keep.go)
			m.describeMemory(ctx, c, call, a)
			return
		}
		if sourceCommands[call.Path] { // names, version notes and fixes (source_actions.go)
			m.describeSources(ctx, call, a)
			return
		}
		if packCommands[call.Path] { // layout packs: a pack Claude proposes (pack_actions.go, D266)
			m.describePack(ctx, c, call, a)
			return
		}
		if deliveryCommands[call.Path] { // delivery notes and captions in other languages (delivery_actions.go, D237, D238)
			m.describeDelivery(ctx, c, call, a)
			return
		}
		if recipeCommands[call.Path] { // Recipes: run, again, save, update, publish (recipe_actions.go, D211)
			m.describeRecipe(ctx, c, call, a) // Recipes
			return
		}
		incomplete(a, "Run metarush "+call.Path, "The dashboard cannot run this one for Claude.")
	}
}

// describeVoicesAndQuestions covers naming voices, people and answering the
// questions MetaRush asks (to-dos): the person confirms each change.
func (m *Manager) describeVoicesAndQuestions(ctx context.Context, call *cliCall, a *AssistantAction) {
	args := call.Args
	argN := func(i int) string {
		if i < len(args) {
			return strings.TrimSpace(args[i])
		}
		return ""
	}
	// The voice commands take <asset-id> <voice> "<name>"; Claude sometimes writes
	// them with flags (--asset, --speaker/--voice, --name) or passes a voice ID.
	voiceArgs := func() (asset, voice, name string) {
		asset, voice, name = argN(0), argN(1), clip(argN(2), 80)
		if asset == "" {
			asset = strings.TrimSpace(call.Flags["asset"])
		}
		if voice == "" {
			voice = strings.TrimSpace(call.Flags["speaker"])
		}
		if voice == "" {
			voice = strings.TrimSpace(call.Flags["voice"])
		}
		if name == "" {
			name = clip(strings.TrimSpace(call.Flags["name"]), 80)
		}
		if ids.Valid(ids.Speaker, voice) {
			var sp models.AssetSpeaker
			ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
			err := m.App.DB.GetByID(ctx2, store.CSpeakers, m.App.WorkspaceID(), voice, &sp)
			cancel()
			if err == nil && (asset == "" || asset == sp.AssetID) {
				asset, voice = sp.AssetID, sp.Label
			}
		}
		return asset, voice, name
	}
	switch call.Path {
	case "footage speakers rename":
		asset, voice, name := voiceArgs()
		if !ids.Valid(ids.Asset, asset) || voice == "" || name == "" || ids.Valid(ids.Speaker, voice) {
			incomplete(a, "Name a voice", "Claude did not name the video, the voice and the name.")
			return
		}
		a.Title = fmt.Sprintf("Name %s in %s “%s”", voiceLabel(voice), m.nameOf(ctx, asset), name)
		a.Details = []string{"The same person is then recognized in other videos"}
		setInput(a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: asset, Speaker: voice, Name: name, PersonID: call.Flags["person"], NewPerson: hasBool(call, "new-person"), SamePerson: hasBool(call, "same-person")})
	case "footage speakers reject":
		asset, voice, name := voiceArgs()
		if !ids.Valid(ids.Asset, asset) || voice == "" || name == "" || ids.Valid(ids.Speaker, voice) {
			incomplete(a, "Correct a voice", "Claude did not name the video, the voice and the name.")
			return
		}
		a.Title = fmt.Sprintf("%s in %s is not “%s”", voiceLabel(voice), m.nameOf(ctx, asset), name)
		a.Details = []string{"Removes that suggestion or automatic label"}
		setInput(a, "footage.speakers.reject", ops.SpeakerRejectIn{AssetID: asset, Speaker: voice, Name: name})
	case "footage speakers hint":
		asset, _, _ := voiceArgs()
		count, err := strconv.Atoi(call.Flags["count"])
		if !ids.Valid(ids.Asset, asset) || err != nil || count < 0 || count > 12 {
			incomplete(a, "Find the voices again", "Claude did not say which video and how many people speak.")
			return
		}
		a.Title = "Find the voices in " + m.nameOf(ctx, asset) + " again"
		if count > 0 {
			a.Title = fmt.Sprintf("Listen again for %s in %s", pluralize(count, "voice"), m.nameOf(ctx, asset))
		}
		a.Details = []string{"Keeps the words; works out again who speaks when"}
		setInput(a, "footage.speakers.hint", ops.SpeakerHintIn{AssetID: asset, Count: count})
	case "people rename":
		from, to := clip(argN(0), 80), clip(argN(1), 80)
		if from == "" || to == "" {
			incomplete(a, "Rename a person", "Claude did not give both names.")
			return
		}
		a.Title = fmt.Sprintf("Rename “%s” to “%s” in every video", from, to)
		setInput(a, "people.rename", ops.PersonRenameIn{ID: from, Name: to})
	case "people merge":
		from, into := clip(argN(0), 80), clip(argN(1), 80)
		if from == "" || into == "" {
			incomplete(a, "Combine two people", "Claude did not give both names.")
			return
		}
		a.Title = fmt.Sprintf("“%s” and “%s” are the same person", from, into)
		a.Details = []string{fmt.Sprintf("Everything said by “%s” is credited to “%s”", from, into)}
		setInput(a, "people.merge", ops.PersonMergeIn{From: from, Into: into})
	case "people split":
		who, asset, as := clip(argN(0), 80), call.Flags["asset"], clip(strings.TrimSpace(call.Flags["as"]), 80)
		if who == "" || !ids.Valid(ids.Asset, asset) || as == "" {
			incomplete(a, "Correct a person in one video", "Claude did not name the person, the video and who it really is.")
			return
		}
		a.Title = fmt.Sprintf("In %s, “%s” is really “%s”", m.nameOf(ctx, asset), who, as)
		setInput(a, "people.split", ops.PersonSplitIn{ID: who, AssetID: asset, Speaker: call.Flags["voice"], Name: as})
	case "todos answer", "todos dismiss":
		id := argN(0)
		if !ids.Valid(ids.Todo, id) {
			incomplete(a, "Answer a question", "Claude did not name the question.")
			return
		}
		var t models.Todo
		ctx2, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		err := m.App.DB.GetByID(ctx2, store.CTodos, m.App.WorkspaceID(), id, &t)
		cancel()
		if err != nil || t.State != models.TodoOpen {
			incomplete(a, "Answer a question", "That question is not open.")
			return
		}
		question := clip(strings.TrimSpace(t.Question), 160)
		if call.Path == "todos dismiss" {
			a.Title = "Skip the question: " + question
			a.Details = []string{"It will not be asked again"}
			setInput(a, "todos.dismiss", ops.TodoDismissIn{ID: id, Note: clip(call.Flags["note"], 300)})
			return
		}
		answer := clip(argN(1), 500)
		suggestion, _ := strconv.Atoi(call.Flags["suggestion"])
		if answer == "" && suggestion <= 0 {
			incomplete(a, "Answer a question", "Claude did not give an answer.")
			return
		}
		a.Title = "Answer: " + question
		if answer != "" {
			a.Details = []string{"“" + answer + "”"}
		} else {
			a.Details = []string{fmt.Sprintf("Suggested answer %d", suggestion)}
		}
		a.HumanOnly = t.Kind == models.TodoRevisionApproval || t.Kind == models.TodoRemotionApproval || t.Kind == models.TodoRecipeRun || t.Kind == models.TodoAbilityApproval || t.Kind == models.TodoShipGate
		setInput(a, "todos.answer", ops.TodoAnswerIn{ID: id, Answer: answer, Suggestion: suggestion})
	}
}

// voiceLabel names a voice the way the dashboard does: "speaker_01" and "2" are
// "Speaker 2"; "Speaker 2" or a name stay as they are.
func voiceLabel(v string) string {
	v = strings.TrimSpace(v)
	if n, err := strconv.Atoi(v); err == nil && n > 0 {
		return fmt.Sprintf("Speaker %d", n)
	}
	if rest, ok := strings.CutPrefix(strings.ToLower(v), "speaker_"); ok {
		if n, err := strconv.Atoi(rest); err == nil && n >= 0 {
			return fmt.Sprintf("Speaker %d", n+1)
		}
	}
	if strings.HasPrefix(strings.ToLower(v), "speaker ") {
		return "Speaker " + strings.TrimSpace(v[len("speaker "):])
	}
	return "“" + v + "”"
}

// describeApply prepares an "apply this plan" card: the plan file must be one
// of this conversation's proposals; it is checked now so the card shows the diff.
func (m *Manager) describeApply(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	path := call.Flags["plan"]
	if path == "" || path == "-" {
		incomplete(a, "Apply a plan", "Claude did not name a plan file.")
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceDir(), path)
	}
	real := func(p string) string {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return p
	}
	root := real(filepath.Join(m.convDir(c.id), "proposals"))
	rel, err := filepath.Rel(root, real(path))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		incomplete(a, "Apply a plan", "The plan is not in this conversation's proposals folder.")
		return
	}
	ref := filepath.ToSlash(rel)
	dir, file := filepath.Split(filepath.Join(root, rel))
	raw, err := readProposal(filepath.Clean(dir), file)
	if err != nil {
		incomplete(a, "Apply plan "+planName(file), "MetaRush cannot read the plan: "+err.Error())
		return
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(raw, &obj) != nil || obj == nil {
		incomplete(a, "Apply plan "+planName(file), "The plan is not a JSON object.")
		return
	}
	origin := Origin(c.id, ref)
	obj["origin"], _ = json.Marshal(origin)
	plan, _ := json.Marshal(obj)
	var peek struct {
		Instruction string `json:"instruction"`
		Rationale   string `json:"rationale"`
	}
	_ = json.Unmarshal(plan, &peek)
	a.Proposal, a.Rationale = ref, clip(strings.TrimSpace(peek.Rationale), 1000)
	title := strings.TrimSpace(peek.Instruction)
	if title == "" {
		title = upperFirst(planName(file))
	}
	a.Title = "Apply the change: " + clip(title, 120)
	allowUnlock := hasBool(call, "allow-unlock")
	if allowUnlock {
		a.HumanOnly = true
		a.Details = append(a.Details, "Includes changes to locked parts of the cut")
	}
	in, _ := json.Marshal(ops.EditCheckIn{Plan: plan, AllowUnlock: allowUnlock})
	if res, err := ops.Call(ctx, m.App, "edit.check", in, ops.Caller{}); err == nil {
		co := *res.(*ops.EditCheckOut)
		co.ConfirmToken, co.ExpiresAt, co.Next = "", nil, ""
		a.Check = &co
		if d := co.Diff; d != nil {
			a.Details = append(a.Details, diffLine(d))
		}
		if !co.Valid {
			a.Details = append(a.Details, pluralize(len(co.Errors), "problem")+" to fix first")
		} else if len(co.Warnings) > 0 {
			a.Details = append(a.Details, pluralize(len(co.Warnings), "note"))
		}
	} else {
		a.Details = append(a.Details, "Could not check it: "+firstLine(apperr.From(err).Message))
	}
	setInput(a, "edit.apply", ops.EditApplyIn{Plan: plan, AllowUnlock: allowUnlock})
}

// diffLine says what a plan does in plain words, worded like the dashboard's
// plan cards: "4.8s longer (now 20.0s), 1 clip changed".
func diffLine(d *editspec.Diff) string {
	var parts []string
	delta := d.DurationAfter - d.DurationBefore
	switch {
	case delta < -0.05:
		parts = append(parts, fmt.Sprintf("%.1fs shorter (now %.1fs)", -delta, d.DurationAfter))
	case delta > 0.05:
		parts = append(parts, fmt.Sprintf("%.1fs longer (now %.1fs)", delta, d.DurationAfter))
	default:
		parts = append(parts, fmt.Sprintf("Same length (%.1fs)", d.DurationAfter))
	}
	if n := len(d.ClipsChanged) + len(d.ClipsAdded) + len(d.ClipsRemoved); n > 0 {
		parts = append(parts, pluralize(n, "clip")+" changed")
	}
	if n := len(d.OverlaysAdded) + len(d.OverlaysChanged) + len(d.OverlaysRemoved); n > 0 {
		parts = append(parts, pluralize(n, "graphic")+" changed")
	}
	if d.Reordered {
		parts = append(parts, "shots reordered")
	}
	if n := len(d.BeatsAdded); n > 0 {
		parts = append(parts, pluralize(n, "narration line")+" added")
	}
	if n := len(d.BeatsRemoved); n > 0 {
		parts = append(parts, pluralize(n, "narration line")+" removed")
	}
	if n := len(d.BeatsChanged); n > 0 {
		parts = append(parts, pluralize(n, "narration line")+" changed")
	}
	if d.BeatsReordered {
		parts = append(parts, "narration reordered")
	}
	if d.MixChanged {
		parts = append(parts, "sound mix changed")
	}
	return strings.Join(parts, ", ")
}

func hasBool(c *cliCall, name string) bool {
	v, ok := c.Flags[name]
	return ok && (v == "" || v == "true")
}

func presetLabel(p string) string {
	switch p {
	case "vertical-1080p":
		return "Vertical 9:16"
	case "landscape-1080p":
		return "Landscape 16:9"
	case "square-1080":
		return "Square 1:1"
	}
	return p
}

// versionWords names a version of the cut ("version 4").
func versionWords(n int) string {
	if n <= 0 {
		return "a new version"
	}
	return "version " + strconv.Itoa(n)
}

// savedAs is a change's summary followed by the version it was saved as.
func savedAs(summary string, n int) string {
	s := strings.TrimSpace(summary)
	if s != "" && !strings.HasSuffix(s, ".") {
		s += "."
	}
	return strings.TrimSpace(s + " Saved as " + versionWords(n))
}

// renderStateWords says where a render is, in plain words.
func renderStateWords(state string) string {
	switch state {
	case models.RenderQueued:
		return "waiting to start"
	case models.RenderRendering:
		return "rendering now"
	case models.RenderUploading:
		return "almost done"
	case models.RenderAvailable:
		return "ready to watch"
	case models.RenderFailed:
		return "it failed"
	case models.RenderCanceled:
		return "canceled"
	}
	return state
}

// decidedAlready says why a card can't be used again.
func decidedAlready(state string) string {
	switch state {
	case ActionDone:
		return "This card was already used."
	case ActionDismissed:
		return "This card was set aside with Not now."
	case ActionNotQuite:
		return "This card went back to Claude to change (Not quite)."
	case ActionFailed:
		return "This card was already tried and didn't work. Ask Claude again."
	case ActionUnavailable:
		return "The dashboard can't do this one for Claude."
	}
	return "This card was already decided."
}

func plainSummary(s string) string {
	s = strings.TrimSpace(firstLine(s))
	if s == "" {
		return "Done"
	}
	return clip(strings.TrimRight(s, "."), 300)
}
