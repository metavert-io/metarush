package handover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/store"
)

// HandoverPlanIn asks what handing this computer's role over would do.
type HandoverPlanIn struct {
	To string `json:"to,omitempty" jsonschema:"maxLength=120,description=The computer taking the role over (its name as the workspace's computers list it); empty says what would move without naming one."`
}

// HandoverPlan is the dry run (D348): everything tied to this computer and
// what happens to each, in plain words. Nothing changes.
type HandoverPlan struct {
	From HandoverComputer  `json:"from"`
	To   *HandoverComputer `json:"to,omitempty"`
	// Computers are the other computers of the workspace that could take
	// the role (joined, not removed).
	Computers []HandoverComputer `json:"computers"`
	Role      HandoverRole       `json:"role"`
	// InProgress is a handover from this computer that isn't finished.
	InProgress    *HandoverRecord        `json:"inProgress,omitempty"`
	Conversations []HandoverConversation `json:"conversations"`
	Work          []HandoverWork         `json:"work"`
	Folders       []HandoverFolder       `json:"folders"`
	Remote        HandoverRemote         `json:"remote"`
	Extensions    []HandoverItem         `json:"extensions"`
	// Keys are the keys kept on this computer, by name only: they never
	// move (each computer keeps its own).
	Keys       []HandoverItem `json:"keys"`
	Abilities  []HandoverItem `json:"abilities"`
	Background HandoverItem   `json:"background"`
	// Steps are what only a person does, with the exact command or the place
	// in Settings.
	Steps []HandoverStep `json:"steps"`
	// Blocking says what must happen before the handover file can be made.
	Blocking []string `json:"blocking,omitempty"`
	Ready    bool     `json:"ready"`
	Summary  string   `json:"summary"`
	// Next are the commands, in order (export here, receive there).
	Next []string `json:"next"`
}

// HandoverConversation is one of the panel's conversations this computer holds.
type HandoverConversation struct {
	ID          string `json:"id"`
	Title       string `json:"title"`
	ProjectName string `json:"projectName,omitempty"`
	UserName    string `json:"userName,omitempty"`
	// Session is where Claude Code's own record of it stands here: here (it
	// travels in the file), missing, lost (Claude Code had lost it), none
	// (never started) or legacy (a single request from an older version).
	Session string `json:"session" jsonschema:"enum=here,enum=missing,enum=lost,enum=none,enum=legacy"`
	Busy    bool   `json:"busy,omitempty" jsonschema:"description=Claude is working on it now."`
	Words   string `json:"words"`
}

// HandoverFolder is a watched or linked folder of this computer.
type HandoverFolder struct {
	Kind string `json:"kind" jsonschema:"enum=watched,enum=linked"`
	// ID is the linked folder's record (shared by every computer).
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Path is where it is on the old computer: the person says where it is
	// on the new one, or skips it.
	Path string `json:"path"`
	Off  bool   `json:"off,omitempty"`
	// The watched folder's own settings, kept as they were.
	ProjectID     string `json:"projectId,omitempty"`
	MoveProcessed bool   `json:"moveProcessed,omitempty"`
	StableSeconds int    `json:"stableSeconds,omitempty"`
	Words         string `json:"words"`
}

// HandoverRemote is how people reach this dashboard from other computers.
type HandoverRemote struct {
	On      bool   `json:"on"`
	Address string `json:"address,omitempty"`
	Logins  int    `json:"logins,omitempty"`
	Words   string `json:"words"`
}

// HandoverItem is one thing tied to this computer, with what happens to it.
type HandoverItem struct {
	Name  string `json:"name"`
	Words string `json:"words"`
}

// HandoverStep is a step only a person takes.
type HandoverStep struct {
	// Where: new (the computer taking the role) or old (this one).
	Where   string `json:"where" jsonschema:"enum=new,enum=old"`
	Words   string `json:"words"`
	Command string `json:"command,omitempty"`
	Click   string `json:"click,omitempty" jsonschema:"description=Where in the dashboard (Settings → …)."`
}

func planHandover(ctx context.Context, a *app.App, in *HandoverPlanIn) (*HandoverPlan, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	me := HandoverComputer{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}
	p := &HandoverPlan{From: me, Computers: []HandoverComputer{}, Conversations: []HandoverConversation{}, Work: []HandoverWork{},
		Folders: []HandoverFolder{}, Extensions: []HandoverItem{}, Keys: []HandoverItem{}, Abilities: []HandoverItem{}, Steps: []HandoverStep{}}
	role, err := RoleOf(ctx, a, me.ID)
	if err != nil {
		return nil, err
	}
	p.Role = role
	if role.Role != RoleHost {
		p.Blocking = append(p.Blocking, role.Says)
	}
	others, err := otherComputers(ctx, a)
	if err != nil {
		return nil, err
	}
	p.Computers = others
	newName := "the new computer"
	if strings.TrimSpace(in.To) != "" {
		to, err := targetComputer(ctx, a, in.To)
		if err != nil {
			return nil, err
		}
		p.To = &HandoverComputer{ID: to.ID, Name: to.Name}
		newName = "“" + to.Name + "”"
	}
	if r, err := activeFrom(ctx, a, me.ID); err != nil {
		return nil, err
	} else if r != nil {
		p.InProgress = r
		if p.To != nil && r.To != p.To.ID {
			p.Blocking = append(p.Blocking, "A handover to “"+r.ToName+"” is in progress: abandon it first to hand over to another computer.")
		}
	}
	// The panel's conversations.
	held, err := assistant.Held(ctx, a, me.ID)
	if err != nil {
		return nil, err
	}
	var busy []string
	for _, h := range held {
		c := HandoverConversation{ID: h.ID, Title: h.Title, ProjectName: h.ProjectName, UserName: h.UserName, Busy: h.Busy}
		c.Session, c.Words = sessionState(a, h, newName)
		if h.Busy {
			busy = append(busy, "“"+h.Title+"”")
			c.Words = "Claude is working on it now: wait until it finishes (or stop it), then make the handover file."
		}
		p.Conversations = append(p.Conversations, c)
	}
	if len(busy) > 0 {
		p.Blocking = append(p.Blocking, "Claude is working on "+strings.Join(busy, ", ")+" here: wait until it finishes, or stop it.")
	}
	// Work pinned here.
	if p.Work, err = pinnedWork(ctx, a, me.ID, false); err != nil {
		return nil, err
	}
	for i := range p.Work {
		if p.Work[i].Moves && p.Work[i].Why == "" {
			p.Work[i].Why = "It moves to " + newName + " and runs there."
		}
	}
	if p.Work == nil {
		p.Work = []HandoverWork{}
	}
	// Folders.
	fs, err := foldersHere(ctx, a)
	if err != nil {
		return nil, err
	}
	for _, f := range fs {
		f.Words = folderWords(f, newName)
		p.Folders = append(p.Folders, f)
	}
	// Remote access.
	rm := a.Cfg.Remote()
	p.Remote = HandoverRemote{On: rm.On(), Address: rm.PublicURL, Logins: len(rm.Logins)}
	if rm.On() {
		p.Remote.Words = "People open this dashboard from their other computers at this computer's address. It goes off here when this computer steps down; turn it on at " + newName +
			", whose address is different, so the links people kept change."
	} else {
		p.Remote.Words = "Remote access is off here: nothing to move."
	}
	// Browser extension pairings.
	for _, b := range browserext.Of(a).List() {
		who := b.UserName
		if who == "" {
			who = a.Cfg.User.Name
		}
		p.Extensions = append(p.Extensions, HandoverItem{Name: who + "’s " + orText(b.Name, "browser"),
			Words: "Paired with this computer: pair it again with " + newName + " (the extension sends to one computer)."})
	}
	// Keys, by name only.
	for _, pr := range providers.All() {
		if st := providers.StatusFor(a.Cfg, pr); st.Configured {
			p.Keys = append(p.Keys, HandoverItem{Name: pr.Title(), Words: "Kept on this computer only: set it again on " + newName + " (keys are never copied)."})
		}
	}
	if c, err := publish.LoadClient(a.Cfg, "youtube"); err == nil && c != nil {
		p.Keys = append(p.Keys, HandoverItem{Name: "YouTube posting", Words: "Set up on this computer only: set it up on " + newName + ", and each person signs in to YouTube again there."})
	}
	p.Keys = append(p.Keys, HandoverItem{Name: "Storage and database sign-in", Words: "Each computer has its own: " + newName + " keeps the one it joined with; this computer's stays here."})
	// Abilities set up here.
	var setups []models.AbilitySetup
	if err := a.DB.Find(ctx, store.CAbilitySetups, a.WorkspaceID(), bson.M{"installation": me.ID}, &setups, options.Find().SetProjection(bson.M{"name": 1})); err == nil {
		seen := map[string]bool{}
		for _, s := range setups {
			if seen[s.Name] {
				continue
			}
			seen[s.Name] = true
			p.Abilities = append(p.Abilities, HandoverItem{Name: s.Name, Words: "Set up on this computer: set it up again on " + newName + " before it runs there."})
		}
	}
	// The background role.
	p.Background = backgroundHere(a, newName)
	p.Steps = peopleSteps(a, p, newName)
	p.Ready = len(p.Blocking) == 0 && p.To != nil
	switch {
	case len(p.Blocking) > 0:
		p.Summary = "Not yet: " + strings.Join(p.Blocking, " ")
	case p.To == nil:
		p.Summary = "Choose the computer that takes this computer's role, then make the handover file."
	default:
		p.Summary = fmt.Sprintf("Ready to hand this computer's role to %s: %s, %s and %s go there.", newName,
			plural(len(p.Conversations), "conversation"), plural(countMoves(p.Work), "piece of work", "pieces of work"), plural(len(p.Folders), "folder setting"))
	}
	to := "<computer>"
	if p.To != nil {
		to = shellQuote(p.To.Name)
	}
	p.Next = []string{"metarush machine handover export --to " + to + " --out <file>", "metarush machine handover receive <file>   (on " + strings.Trim(newName, "“”") + ")"}
	return p, nil
}

// targetComputer is the computer taking the role: joined, not this one.
func targetComputer(ctx context.Context, a *app.App, name string) (*models.Installation, error) {
	to, err := findComputer(ctx, a, name)
	if err != nil {
		return nil, err
	}
	switch {
	case to.ID == a.Cfg.Installation.ID:
		return nil, apperr.New(apperr.InvalidInput, "That is this computer: name the computer that takes its role over.").WithCommand("metarush machine list")
	case to.State == models.InstallationRevoked:
		return nil, apperr.New(apperr.Conflict, "“%s” was removed from the workspace.", to.Name).WithHint("Invite it again and join it there first.")
	case to.State == models.InstallationInvited:
		return nil, apperr.New(apperr.Conflict, "“%s” is invited but hasn't joined the workspace yet.", to.Name).
			WithHint("Join it there with its join file first, then hand over.").WithCommand("metarush machine join <join file>")
	}
	return to, nil
}

func otherComputers(ctx context.Context, a *app.App) ([]HandoverComputer, error) {
	var list []models.Installation
	if err := a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"_id": bson.M{"$ne": a.Cfg.Installation.ID},
		"state": bson.M{"$nin": bson.A{models.InstallationRevoked, models.InstallationInvited}}}, &list,
		options.Find().SetProjection(bson.M{"name": 1}).SetSort(bson.D{{Key: "name", Value: 1}})); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read this workspace's computers.")
	}
	out := []HandoverComputer{}
	for _, i := range list {
		out = append(out, HandoverComputer{ID: i.ID, Name: i.Name})
	}
	return out, nil
}

// sessionState says where a conversation's session stands on this computer.
func sessionState(a *app.App, h assistant.HeldConversation, newName string) (string, string) {
	switch {
	case h.Legacy:
		return "legacy", "A single request from an older version: it moves, to be read there."
	case h.Lost:
		return "lost", "Claude Code had already lost its own record of it here: it goes on at " + newName + " from a summary of the conversation."
	case !h.Started || h.SessionID == "":
		return "none", "It moves, and goes on at " + newName + "."
	}
	if place, err := assistant.SessionPlaceFor(a, h.SessionID); err == nil {
		if _, err := os.Stat(place.File); err == nil {
			return "here", "It travels in the handover file and goes on at " + newName + " where it left off."
		}
	}
	return "missing", "Claude Code's own record of it isn't on this computer: it goes on at " + newName + " from a summary of the conversation."
}

// foldersHere are the watched folder and the linked folders of this computer.
func foldersHere(ctx context.Context, a *app.App) ([]HandoverFolder, error) {
	var out []HandoverFolder
	if w := a.Cfg.Worker.Watch; w.Dir != "" {
		out = append(out, HandoverFolder{Kind: "watched", Name: "The watched folder", Path: w.Dir, Off: !w.Enabled, ProjectID: w.ProjectID,
			MoveProcessed: w.MoveProcessed, StableSeconds: w.StableSeconds})
	}
	if len(a.Cfg.Resources.Folders) > 0 {
		idl := make([]string, 0, len(a.Cfg.Resources.Folders))
		for _, f := range a.Cfg.Resources.Folders {
			idl = append(idl, f.ID)
		}
		var recs []models.LinkedFolder
		if err := a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idl}}, &recs, options.Find().SetProjection(bson.M{"name": 1, "unlinked": 1})); err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the linked folders.")
		}
		names := map[string]string{}
		for _, r := range recs {
			if !r.Unlinked {
				names[r.ID] = r.Name
			}
		}
		for _, f := range a.Cfg.Resources.Folders {
			if n, ok := names[f.ID]; ok {
				out = append(out, HandoverFolder{Kind: "linked", ID: f.ID, Name: n, Path: f.Path, Off: f.Off})
			}
		}
	}
	return out, nil
}

// folderWords says what happens to a folder; its path stays behind details
// (the folder is named by its own name).
func folderWords(f HandoverFolder, newName string) string {
	what := "The watched folder"
	if f.Kind == "linked" {
		what = "The linked folder “" + f.Name + "”"
	}
	if base := filepath.Base(cleanPath(f.Path)); base != "" && base != "." && base != string(filepath.Separator) {
		what += " (“" + base + "” here)"
	}
	return what + ": say where it is on " + newName + " when you receive the file, or skip it."
}

func backgroundHere(a *app.App, newName string) HandoverItem {
	d, running := service.ReadDaemon(a.Cfg.Home())
	switch {
	case running && d.Mode == "worker-only":
		return HandoverItem{Name: "Background work", Words: "This computer does background work (without the dashboard). It stops here when this computer steps down; install the background service on " + newName + "."}
	case running:
		return HandoverItem{Name: "The dashboard and background work", Words: "This computer runs the dashboard and background work. They stop here when this computer steps down; install the background service on " + newName + "."}
	}
	return HandoverItem{Name: "The dashboard and background work", Words: "MetaRush isn't running in the background here now. Install the background service on " + newName + " so it does the work there."}
}

// peopleSteps are the steps only a person takes, with exact commands.
func peopleSteps(a *app.App, p *HandoverPlan, newName string) []HandoverStep {
	on := " on " + newName
	steps := []HandoverStep{
		{Where: "new", Words: "Sign in to Claude Code" + on + ": run claude there once and sign in. From then on the Claude account signed in there answers these conversations — its plan and its terms, not this computer's.",
			Command: "claude auth status", Click: "Settings → Claude Code → Whose Claude answers"},
	}
	var names []string
	for _, pr := range providers.All() {
		if st := providers.StatusFor(a.Cfg, pr); st.Configured {
			names = append(names, pr.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		steps = append(steps, HandoverStep{Where: "new", Words: "Set the " + titleOf(n) + " key" + on + " (keys are never copied).", Command: "metarush providers set " + n, Click: "Settings → Integrations"})
	}
	for _, k := range p.Keys {
		if k.Name == "YouTube posting" {
			steps = append(steps, HandoverStep{Where: "new", Words: "Set up posting to YouTube" + on + "; each person then signs in to YouTube there.", Command: "metarush publish setup youtube"})
		}
	}
	for _, ab := range p.Abilities {
		steps = append(steps, HandoverStep{Where: "new", Words: "Set up the ability “" + ab.Name + "”" + on + ".", Command: "metarush ability setup " + ab.Name})
	}
	steps = append(steps, HandoverStep{Where: "new", Words: "Install the background service" + on + ", so the dashboard and background work run there.", Command: "metarush service install"})
	if p.Remote.On {
		steps = append(steps,
			HandoverStep{Where: "new", Words: "Turn remote access on" + on + " with tailscale serve: its address differs from this computer's, so tell people the new one.", Command: "metarush remote enable --apply", Click: "Settings → People"},
			HandoverStep{Where: "old", Words: "Stop tailscale serve on this computer (MetaRush turns its remote access off when it steps down).", Command: "metarush remote disable --apply"})
	}
	if len(p.Extensions) > 0 {
		steps = append(steps, HandoverStep{Where: "new", Words: "Each person pairs their browser's extension again" + on + ".", Command: "metarush extension pair", Click: "Settings → Browser extension"})
	}
	steps = append(steps, HandoverStep{Where: "old", Words: "Once " + newName + " has received the file and you are sure all is well, remove this computer's copies of the conversations (never by itself).",
		Command: "metarush machine handover clean"})
	return steps
}

func titleOf(name string) string {
	if p, err := providers.Get(name); err == nil {
		return p.Title()
	}
	return name
}

func countMoves(ws []HandoverWork) int {
	n := 0
	for _, w := range ws {
		if w.Moves {
			n++
		}
	}
	return n
}

func plural(n int, one string, many ...string) string {
	if n == 1 {
		return "1 " + one
	}
	if len(many) > 0 {
		return fmt.Sprintf("%d %s", n, many[0])
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func orText(s, other string) string {
	if strings.TrimSpace(s) == "" {
		return other
	}
	return s
}

// shellQuote quotes a computer's name for a command line.
func shellQuote(s string) string {
	if s != "" && strings.IndexFunc(s, func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.')
	}) < 0 {
		return s
	}
	return "\"" + strings.ReplaceAll(s, "\"", "\\\"") + "\""
}
