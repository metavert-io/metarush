package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

// Auto (D186). Each person chooses how Claude's cards for them run: "Ask
// first" (the default: safe steps run by themselves, everything else waits
// for a click) or "Auto" (every card that can be undone also runs by itself,
// as them: saving a version with its review copy, review copies, sound
// settings, transcript and footage fixes, bringing in links, motion checks,
// making resources). Auto still asks for anything that costs money (unless
// the person turned estimates off, D208: then a priced change runs by itself
// within their budgets and limits), final renders, approving a version or a
// graphic, consent and looks, deleting, anything that needs setting up,
// questions, "Keep this?" cards and every people-only decision. The choice
// is the person's (their users record, so
// it follows them to every browser and the command line); in a shared
// conversation a card follows the mode of the person it is for. The
// computer's own setting (assistant.safeSteps) is the owner's master switch:
// off, nothing runs by itself and Auto isn't offered. A change that costs
// money runs by itself only where a person set the project's budget and the
// team's daily limit with every service it spends with (D241, amending D208:
// defaults don't count); it is recorded for the receipt and the bell.

// The modes.
const (
	ModeAsk  = "ask"
	ModeAuto = "auto"
)

// AssistantModeIn reads the asking person's mode.
type AssistantModeIn struct{}

// AssistantModeSetIn chooses it.
type AssistantModeSetIn struct {
	Mode string `json:"mode" jsonschema:"enum=ask,enum=auto,description=ask: changes wait for your OK (safe steps run by themselves); auto: changes that can be undone run by themselves too."`
}

// AssistantModeOut is a person's mode and whether Auto can be chosen here.
type AssistantModeOut struct {
	Mode string `json:"mode" jsonschema:"enum=ask,enum=auto"`
	// Offered: this computer lets steps run by themselves (the owner's
	// master switch, assistant.safeSteps); off, nothing runs by itself.
	Offered bool `json:"offered" jsonschema:"description=Auto can be chosen: this computer lets Claude's steps run by themselves (Settings → Claude Code)."`
	// EstimatesOff: the person turned estimates off (D208), so under Auto
	// changes that cost money run by themselves too, within budgets and limits.
	EstimatesOff bool   `json:"estimatesOff,omitempty" jsonschema:"description=Your estimates are off (Settings → Preferences): under Auto\\, changes that cost money run by themselves too\\, within your budgets and limits."`
	Summary      string `json:"summary" jsonschema:"description=What the mode does\\, in plain words."`
}

func init() {
	ops.Def("assistant.mode", "Show whether Claude's changes for you wait for your OK (ask) or run by themselves when they can be undone (auto)", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *AssistantModeIn) (*AssistantModeOut, error) {
			return For(a).Mode(AsPerson(ctx, a))
		})
	ops.Def("assistant.mode.set", "Choose whether Claude's changes for you wait for your OK (ask) or run by themselves when they can be undone (auto); people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *AssistantModeSetIn) (*AssistantModeOut, error) {
			return For(a).SetMode(AsPerson(ctx, a), in.Mode)
		})
}

// modeOut describes a mode; estimatesOff: the person turned estimates off (D208).
func (m *Manager) modeOut(mode string, estimatesOff bool) *AssistantModeOut {
	out := &AssistantModeOut{Mode: mode, Offered: m.safeStepsOn(), EstimatesOff: estimatesOff}
	switch {
	case !out.Offered:
		out.Mode = ModeAsk
		out.Summary = "Every change waits for your OK: steps don't run by themselves on this computer (Settings → Claude Code)."
	case mode == ModeAuto && estimatesOff:
		out.Summary = "Auto: changes that can be undone run by themselves, and with your estimates off so do changes that cost money — only where a person set the project's budget and the team's daily limit with that service, and within them. Final renders, approvals and deleting still ask you."
	case mode == ModeAuto:
		out.Summary = "Auto: changes that can be undone run by themselves. Anything that costs money, final renders, approvals and deleting still ask you."
	default:
		out.Summary = "Ask first: changes wait for your OK. Safe steps (checking a graphic, a review copy, a link you typed) run by themselves."
	}
	return out
}

// Mode is the asking person's mode.
func (m *Manager) Mode(ctx context.Context) (*AssistantModeOut, error) {
	uid, _ := m.person(ctx)
	return m.modeOut(m.personMode(ctx, uid), !prefs.EstimatesFirst(ctx, m.App, uid)), nil
}

// SetMode chooses the asking person's mode.
func (m *Manager) SetMode(ctx context.Context, mode string) (*AssistantModeOut, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	if mode != ModeAsk && mode != ModeAuto {
		return nil, apperr.New(apperr.InvalidInput, "Choose ask or auto.")
	}
	if mode == ModeAuto && !m.safeStepsOn() {
		return nil, apperr.New(apperr.Conflict, "Steps can't run by themselves on this computer, so Auto isn't available.").
			WithHint("An owner can allow it in Settings → Claude Code.").WithDetails(map[string]any{"reason": "safe_steps_off"})
	}
	uid, _ := m.person(ctx)
	if uid == "" {
		return nil, apperr.New(apperr.Unauthorized, "MetaRush doesn't know who you are, so it can't keep your choice.")
	}
	now := time.Now().UTC()
	if err := m.App.DB.Update(ctx, store.CUsers, m.App.WorkspaceID(), uid, bson.M{"$set": bson.M{"assistantMode": mode, "updatedAt": now}}); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "MetaRush can't find your account, so it can't keep your choice.").WithDetails(map[string]any{"userId": uid})
		}
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't save your choice.")
	}
	m.auditAs(ctx, "assistant.mode", "user", uid, "", map[string]any{"mode": mode})
	return m.modeOut(mode, !prefs.EstimatesFirst(ctx, m.App, uid)), nil
}

// personMode is a person's mode (ask when they never chose, or can't be read).
func (m *Manager) personMode(ctx context.Context, uid string) string {
	if uid == "" {
		return ModeAsk
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	var u struct {
		Mode string `bson:"assistantMode"`
	}
	err := m.App.DB.C(store.CUsers).FindOne(ctx, bson.M{"_id": uid, "workspaceId": m.App.WorkspaceID()},
		options.FindOne().SetProjection(bson.M{"assistantMode": 1})).Decode(&u)
	if err != nil || u.Mode != ModeAuto {
		return ModeAsk
	}
	return ModeAuto
}

// autoOps are the changes Auto runs by itself: each can be undone (a new
// version can be set back, a fix undone, a resource or a clip removed), costs
// nothing and needs no setup. Anything not here asks.
var autoOps = map[string]bool{
	// Saving a version (with its review copy).
	"edit.apply": true, "cut.apply": true, "cut.transition": true, "cut.captions": true, "footage.short": true, "cut.add": true, "cut.move": true, "cut.trim": true, "cut.split": true,
	"cut.delete": true, "cut.deleteRange": true, "cut.removeWords": true, "cut.pauses": true, "cut.flashes": true, "cut.names": true, "cut.useFixed": true,
	"conversation.cut": true, "conversation.decide": true, "conversation.angle": true, "conversation.short": true, "conversation.cleanup": true,
	"voiceover.scratch": true, "voiceover.place": true, "voiceover.use": true, "voiceover.fitted": true, "voiceover.align": true,
	"transcript.pending.apply": true,
	// Review copies and checks.
	"render.submit": true, "qa.run": true, "motion.check": true,
	// Sound settings (a cloud voice is priced: see autoStep).
	"sound.levels": true, "sound.enhance": true, "sound.music.add": true, "sound.music.set": true,
	// Transcript fixes (each undoable) and spelling lists.
	"transcript.replace": true, "transcript.fix": true, "transcript.speaker": true, "transcript.undo": true,
	"transcript.spelling.add": true, "transcript.spelling.remove": true, "transcript.spelling.apply": true,
	// Footage fixes (the original is kept) and names.
	"asset.fixes.set": true, "asset.fixes.look": true, "asset.names.accept": true, "asset.update": true, "cut.rename": true, "revision.note": true,
	// Bringing links in, making resources.
	"resources.fetch": true, "ingest.url": true, "resources.make": true, "resources.refresh": true, "resources.pictures.set": true,
	"footage.switches.find": true, "comment.add": true,
	// What goes out with a version: its delivery notes (D237).
	"delivery.notes.set": true,
	// Captions in other languages (D238): kept line by line, taken off again.
	"captions.translate": true,
}

// pricedOps are changes that use a paid outside AI service (D208). Auto runs
// them by themselves only for a person who turned estimates off (and whom no
// owner asks for estimates): each still reserves against the project's budget
// and today's limit, and is undone like any other change, but its money is
// spent. Everyone else sees the card with its estimate and decides.
var pricedOps = map[string]bool{"generate.shot": true, "generate.again": true, "generate.extend": true, "generate.edit": true, "voiceover.tts": true, "sound.enhance": true,
	"ai.make": true}

// pricedStep reports whether a card is a priced change Auto may run once the
// person's estimates are off: it carries its estimate, needs no setup, asks no
// question and isn't a people-only decision.
func (c *conv) pricedStep(a *AssistantAction) bool {
	if a.State != ActionPending || a.Op == "" || a.HumanOnly || len(a.SetUp) > 0 || a.Question != nil || a.Keeping != nil || !pricedOps[a.Op] || a.Estimate == nil {
		return false
	}
	if a.Op == "sound.enhance" {
		var in struct {
			Mode string `json:"mode"`
		}
		return json.Unmarshal([]byte(a.Input), &in) == nil && in.Mode == "cloud" // clean and studio are autoOps
	}
	return true
}

// autoStep reports whether a card may run by itself under Auto.
func (c *conv) autoStep(a *AssistantAction) bool {
	if a.State != ActionPending || a.Op == "" || a.HumanOnly || len(a.SetUp) > 0 || a.Question != nil || a.Keeping != nil || !autoOps[a.Op] {
		return false
	}
	if (a.Check != nil && !a.Check.Valid) || (a.SheetCheck != nil && (!a.SheetCheck.Valid || a.SheetCheck.NoChanges)) {
		return false // it can't be saved as it stands: the person sees why
	}
	switch a.Op {
	case "render.submit":
		var in ops.RenderSubmitIn
		return json.Unmarshal([]byte(a.Input), &in) == nil && (in.Quality == "" || in.Quality == "review") // a final render asks
	case "sound.enhance":
		var in struct {
			Mode string `json:"mode"`
		}
		return json.Unmarshal([]byte(a.Input), &in) == nil && in.Mode != "cloud" // the cloud is paid
	case "ingest.url":
		var in linkimport.ImportIn
		return json.Unmarshal([]byte(a.Input), &in) == nil && in.Playlist == 0 && in.BrowserSignIn == "" && !in.DryRun // the browser's sign-in asks
	case "footage.short":
		// Following movement is only ever a person's choice (D115, D246): a
		// short that asks for it waits for a click.
		var in ops.FootageShortIn
		return json.Unmarshal([]byte(a.Input), &in) == nil && in.Framing != editspec.FramingFollow
	}
	return true
}

// startAuto marks a new card as started by itself when the master switch
// allows it: a safe step for anyone (D141), or any change that can be undone
// for a person with Auto on (D186). The caller adds the card, then runs it
// with runAuto.
func (m *Manager) startAuto(c *conv, a *AssistantAction) bool {
	if m.terminal || !m.safeStepsOn() || !c.here(m) {
		return false // in a terminal command, cards wait for a click
	}
	if recipeCard(a) { // Recipes: every card waits for a person; Auto never starts a run (D204, D211)
		return false
	}
	if c.safeStep(a) {
		a.Auto, a.State, a.Batch = true, ActionRunning, shortID("b")
		return true
	}
	priced := false
	if !c.autoStep(a) {
		if !c.pricedStep(a) {
			return false
		}
		priced = true
	}
	c.mu.Lock()
	p := c.data.sender(a.Turn)
	c.mu.Unlock()
	if m.personMode(context.Background(), p.id) != ModeAuto {
		return false
	}
	if priced && prefs.EstimatesFirst(context.Background(), m.App, p.id) {
		return false // the estimate waits for the person (D208)
	}
	if priced {
		// Only within limits a person set (D241): the project's budget and the
		// team's daily limit with every service it spends with.
		if ok, why := m.autoMaySpend(context.Background(), c, a); !ok {
			a.AutoWaits = "Auto didn't start it: " + why + "."
			return false
		}
	}
	// Auto runs only what can be undone (D186): never taking something off,
	// nor writing over what a person wrote themselves.
	if ok, why := m.autoKeepsPeoplesWork(a); !ok {
		a.AutoWaits = "Auto didn't start it: " + why + "."
		return false
	}
	a.Auto, a.AutoMode, a.AutoPriced, a.State, a.Batch = true, true, priced, ActionRunning, shortID("b")
	return true
}

// autoMaySpend reports whether Auto may run a change that costs money by
// itself (D241, amending D208): for every outside service it spends with, a
// person set the project's budget and the team's daily limit. Defaults don't
// count, nor does "no limit"; a service that doesn't say its limits asks.
func (m *Manager) autoMaySpend(ctx context.Context, c *conv, a *AssistantAction) (bool, string) {
	if a.Estimate == nil {
		return false, "its estimate is missing"
	}
	pid := c.projectID()
	var services []string
	seen := map[string]bool{}
	for _, p := range a.Estimate.Parts {
		// By whose limits cover it: AI services share generated video's (D226).
		key := p.LimitsKey()
		if p.Free || p.USD <= 0 || seen[key] {
			continue
		}
		seen[key] = true
		services = append(services, key)
	}
	if len(services) == 0 {
		return true, "" // nothing to pay
	}
	switch {
	case pid == "":
		return false, "this conversation isn't about a project, so no project's budget covers it"
	case a.SpendsIn == "":
		return false, "MetaRush can't tell which project's budget it is paid from"
	case a.SpendsIn != pid:
		// It would be paid from another project than the one whose limits
		// this conversation is about: a person decides.
		return false, "it is paid from another project's budget than this conversation's"
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	for _, s := range services {
		l, ok := ops.SpendingLimitsFor(ctx, m.App, s, a.SpendsIn)
		if !ok {
			return false, "MetaRush doesn't know the limits for " + s
		}
		if may, why := l.AutoMay(); !may {
			return false, why
		}
	}
	return true, ""
}

// autoKeepsPeoplesWork reports whether a card that writes words leaves what
// people wrote alone: delivery notes taken off or written over a person's
// own, and a person's translated lines written over with machine ones, ask.
func (m *Manager) autoKeepsPeoplesWork(a *AssistantAction) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	switch a.Op {
	case "delivery.notes.set":
		var in ops.DeliveryNotesSetIn
		if json.Unmarshal([]byte(a.Input), &in) != nil {
			return false, "MetaRush can't read what it would keep"
		}
		return ops.NotesAutoSafe(ctx, m.App, in)
	case "captions.translate":
		var in ops.CaptionsTranslateIn
		if json.Unmarshal([]byte(a.Input), &in) != nil {
			return false, "MetaRush can't read what it would keep"
		}
		return ops.TranslationAutoSafe(ctx, m.App, in)
	}
	return true, ""
}
