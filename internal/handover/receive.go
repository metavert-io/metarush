package handover

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
)

// HandoverFolderChoice says where one of the old computer's folders is on
// this computer, or that it is skipped.
type HandoverFolderChoice struct {
	// Path is where the folder was on the old computer (as the plan lists it).
	Path string `json:"path" jsonschema:"minLength=1,maxLength=4096"`
	// To is where it is on this computer; Skip leaves it unset here.
	To   string `json:"to,omitempty" jsonschema:"maxLength=4096"`
	Skip bool   `json:"skip,omitempty"`
}

// HandoverReceiveIn receives a handover file on the new computer.
type HandoverReceiveIn struct {
	File string `json:"file" jsonschema:"minLength=1,maxLength=4096,description=The handover file made on the old computer."`
	// Plan says what receiving would do and which folders need a place here
	// (nothing changes).
	Plan    bool                   `json:"plan,omitempty"`
	Folders []HandoverFolderChoice `json:"folders,omitempty"`
}

// HandoverReceiveOut says what came to this computer.
type HandoverReceiveOut struct {
	Plan     bool             `json:"plan,omitempty"`
	Handover *HandoverRecord  `json:"handover,omitempty"`
	From     HandoverComputer `json:"from"`
	// Conversations are what came (or would come), each with what happens.
	Conversations []HandoverConversation `json:"conversations"`
	Folders       []HandoverFolder       `json:"folders"`
	Work          []HandoverWork         `json:"work"`
	Moved         HandoverMoved          `json:"moved"`
	// Waiting is the work that finishes on the old computer before it steps down.
	Waiting []HandoverWork `json:"waiting"`
	// Steps are what only a person does now, here and there.
	Steps   []HandoverStep `json:"steps"`
	Summary string         `json:"summary"`
}

// stagingDir is a private folder for a file being received.
func stagingDir(a *app.App) (string, error) {
	base := filepath.Join(a.Cfg.StateDir(), "handover")
	if err := os.MkdirAll(base, 0o700); err != nil {
		return "", err
	}
	return os.MkdirTemp(base, "receive-")
}

func receiveHandover(ctx context.Context, a *app.App, in *HandoverReceiveIn) (*HandoverReceiveOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	file, err := filepath.Abs(expandHome(strings.TrimSpace(in.File)))
	if err != nil || in.File == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which handover file to receive.")
	}
	// A private file in a sync folder has been uploaded already.
	if err := secretpath.CheckUse(file, "the handover file"); err != nil {
		return nil, apperr.From(err)
	}
	dir, err := stagingDir(a)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't make a private folder to receive the file in.")
	}
	defer os.RemoveAll(dir)
	// Whose file it is, and for which computer, is checked before anything in it is unpacked.
	m, err := stageFile(file, passphraseFrom(ctx), dir, func(m *fileManifest) error {
		if m.Workspace.ID != a.WorkspaceID() {
			return apperr.New(apperr.Conflict, "This handover file is from another workspace (“%s”).", m.Workspace.Name).WithDetails(map[string]any{"reason": "other_workspace"})
		}
		if m.To.ID != a.Cfg.Installation.ID {
			return apperr.New(apperr.Conflict, "This handover file is for “%s”, not this computer.", m.To.Name).
				WithHint("Receive it on “" + m.To.Name + "”, or make a new one for this computer there.").WithDetails(map[string]any{"reason": "other_computer"})
		}
		return nil
	})
	if errors.Is(err, errNeedsPassphrase) {
		return nil, apperr.New(apperr.InvalidInput, "This handover file is protected by a passphrase: MetaRush asks for it in the terminal.").
			WithDetails(map[string]any{"reason": "needs_passphrase"}).WithCommand("metarush machine handover receive <file> --passphrase-stdin")
	}
	if err != nil {
		return nil, err
	}
	rec, err := getRecord(ctx, a, m.Handover)
	if err != nil {
		return nil, err
	}
	if rec.From != m.From.ID || rec.To != m.To.ID || rec.Kind != KindHandover {
		return nil, apperr.New(apperr.InvalidInput, "This handover file doesn't match the handover it names.").WithDetails(map[string]any{"reason": "damaged"})
	}
	switch rec.State {
	case StateAbandoned:
		return nil, apperr.New(apperr.Conflict, "This handover was abandoned on “%s”: its conversations went on there.", rec.FromName).
			WithHint("Make a new handover file there if you still want to hand its role over.").WithDetails(map[string]any{"reason": "abandoned"})
	case StateExporting:
		return nil, apperr.New(apperr.Conflict, "This handover file wasn't finished on “%s”.", rec.FromName).WithHint("Make it again there.")
	case StateDone:
		return nil, apperr.New(apperr.Conflict, "This handover was received already, and “%s” has stepped down.", rec.FromName).WithCommand("metarush machine handover status")
	}
	// Received already: it only says so (nothing moves twice, and the role
	// isn't taken again after it may have been handed back).
	again := rec.State == StateReceived
	src := source{dir: dir, manifest: m}
	out := &HandoverReceiveOut{Plan: in.Plan, From: m.From, Work: m.Work, Folders: m.Folders, Conversations: []HandoverConversation{}}
	if out.Work == nil {
		out.Work = []HandoverWork{}
	}
	if out.Folders == nil {
		out.Folders = []HandoverFolder{}
	}
	choices, err := checkChoices(m.Folders, in.Folders, in.Plan)
	if err != nil {
		return nil, err
	}
	// This computer is receiving it from now on: it can't be abandoned
	// meanwhile (a receive that stops part way runs again).
	if !in.Plan && !again {
		r, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{StateExported, StateReceiving}}},
			bson.M{"$set": bson.M{"state": StateReceiving}})
		if err != nil {
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record that the handover is being received.")
		}
		if r.MatchedCount == 0 {
			return nil, changedMeanwhile(ctx, a, rec.ID)
		}
	}
	adopt := adoption{a: a, from: m.From, handover: rec.ID, marked: true, oldDir: m.AssistantDir, src: src}
	for _, fc := range m.Conversations {
		c, err := adopt.one(ctx, fc.ID, fc.Title, in.Plan || again)
		if err != nil {
			return nil, err
		}
		out.Conversations = append(out.Conversations, c)
	}
	if in.Plan {
		out.Summary = fmt.Sprintf("Receiving this file would bring %s from “%s” to this computer, move the work pinned there and set %s here.",
			plural(len(out.Conversations), "conversation"), m.From.Name, plural(len(m.Folders), "folder"))
		if again {
			out.Summary = "This computer received this handover already: receiving it again changes nothing."
		}
		out.Steps = receivedSteps(a, m, rec)
		return out, nil
	}
	if again {
		out.Handover = rec
		if out.Waiting, err = stillThere(ctx, a, m.From.ID); err != nil {
			return nil, err
		}
		if out.Waiting == nil {
			out.Waiting = []HandoverWork{}
		}
		out.Steps = receivedSteps(a, m, rec)
		out.Summary = fmt.Sprintf("This computer received this handover from “%s” already: nothing more moved.", m.From.Name)
		return out, nil
	}
	if receiveHook != nil {
		receiveHook(rec.ID)
	}
	moved, err := moveWork(ctx, a, m.From.ID, a.Cfg.Installation.ID, a.Cfg.Installation.Name, false)
	if err != nil {
		return nil, err
	}
	moved.Conversations, moved.Summaries = adopt.moved, adopt.summaries
	nf, err := applyFolders(ctx, a, m.Folders, choices)
	if err != nil {
		return nil, err
	}
	moved.Folders = nf
	now := time.Now().UTC()
	r, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": StateReceiving},
		bson.M{"$set": bson.M{"state": StateReceived, "receivedAt": now, "receivedBy": a.Actor, "moved": addMoved(rec.Moved, moved)},
			"$currentDate": bson.M{"settledAt": true}})
	if err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record that the handover was received.").
			WithHint("Receive the same file again: what came already stays, and the rest comes.")
	}
	if r.MatchedCount == 0 {
		// Another receive here may have just finished it; anything else ended
		// it meanwhile, and what came here stays here (each conversation is
		// held by one computer all the same).
		if again, gerr := getRecord(ctx, a, rec.ID); gerr != nil || again.State != StateReceived {
			return nil, changedMeanwhile(ctx, a, rec.ID)
		}
	}
	if rec, err = getRecord(ctx, a, rec.ID); err != nil {
		return nil, err
	}
	a.Audit(ctx, "machine.handover.receive", "handover", rec.ID, "", map[string]any{"from": m.From.Name, "conversations": moved.Conversations,
		"summaries": moved.Summaries, "runs": moved.Runs, "triggers": moved.Triggers, "jobs": moved.Jobs, "folders": moved.Folders})
	out.Handover, out.Moved = rec, moved
	if out.Waiting, err = stillThere(ctx, a, m.From.ID); err != nil {
		return nil, err
	}
	if out.Waiting == nil {
		out.Waiting = []HandoverWork{}
	}
	out.Steps = receivedSteps(a, m, rec)
	out.Summary = fmt.Sprintf("This computer now has “%s”'s role: %s, %s and %s came here.", m.From.Name,
		plural(moved.Conversations, "conversation"), plural(moved.Jobs+moved.Runs+moved.Triggers, "piece of work", "pieces of work"), plural(moved.Folders, "folder"))
	if moved.Summaries > 0 {
		out.Summary += fmt.Sprintf(" %s go on from a summary: Claude Code's own record didn't come along.", plural(moved.Summaries, "conversation"))
	}
	if len(out.Waiting) > 0 {
		out.Summary += fmt.Sprintf(" “%s” finishes %s first, then steps down by itself.", m.From.Name, plural(len(out.Waiting), "piece of work", "pieces of work"))
	} else {
		out.Summary += fmt.Sprintf(" “%s” steps down by itself the next time MetaRush there looks.", m.From.Name)
	}
	return out, nil
}

// receiveHook runs (in tests) once the conversations came, before the
// handover is recorded as received.
var receiveHook func(handover string)

// changedMeanwhile says, plainly, that the handover was ended while this
// computer was receiving it.
func changedMeanwhile(ctx context.Context, a *app.App, id string) error {
	rec, err := getRecord(ctx, a, id)
	if err != nil {
		return err
	}
	switch rec.State {
	case StateAbandoned:
		return apperr.New(apperr.Conflict, "This handover was abandoned on “%s” while this computer was receiving it.", rec.FromName).
			WithHint("Anything that came here already stays here. Run status to see where each computer stands.").
			WithCommand("metarush machine handover status").WithDetails(map[string]any{"reason": "abandoned"})
	case StateDone:
		return apperr.New(apperr.Conflict, "This handover was received already, and “%s” has stepped down.", rec.FromName).WithCommand("metarush machine handover status")
	}
	return apperr.New(apperr.Conflict, "This handover changed while this computer was receiving it.").
		WithHint("Run status to see where each computer stands, then receive the file again.").
		WithCommand("metarush machine handover status").WithDetails(map[string]any{"reason": "changed"})
}

// checkChoices says where each folder goes; every folder needs a place or a
// skip (a plan only lists them).
func checkChoices(folders []HandoverFolder, choices []HandoverFolderChoice, plan bool) (map[string]HandoverFolderChoice, error) {
	by := map[string]HandoverFolderChoice{}
	for _, c := range choices {
		c.Path = cleanPath(c.Path)
		by[c.Path] = c
	}
	var missing []string
	for _, f := range folders {
		c, ok := by[cleanPath(f.Path)]
		if !ok || (!c.Skip && strings.TrimSpace(c.To) == "") {
			missing = append(missing, f.Path)
			continue
		}
		if c.Skip {
			continue
		}
		to, err := filepath.Abs(expandHome(c.To))
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't a folder's place.", c.To)
		}
		if st, err := os.Stat(to); err != nil || !st.IsDir() {
			return nil, apperr.New(apperr.InvalidInput, "There's no folder at %s on this computer.", to).
				WithHint("Make it, or copy the folder there first; or skip it.").WithDetails(map[string]any{"folder": f.Path})
		}
		c.To = to
		by[cleanPath(f.Path)] = c
	}
	for p := range by {
		found := false
		for _, f := range folders {
			found = found || cleanPath(f.Path) == p
		}
		if !found {
			return nil, apperr.New(apperr.InvalidInput, "The handover has no folder at %s.", p)
		}
	}
	if len(missing) > 0 && !plan {
		return nil, apperr.New(apperr.InvalidInput, "Say where %s %s on this computer, or skip %s.", plural(len(missing), "folder"), isAre(len(missing)), itThem(len(missing))).
			WithDetails(map[string]any{"reason": "folders", "folders": missing}).WithCommand("metarush machine handover receive <file> --folder OLD=NEW | --skip-folder OLD")
	}
	return by, nil
}

// cleanPath is a folder's path as written, without a trailing slash or dots.
func cleanPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return p
	}
	return filepath.Clean(p)
}

func isAre(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func itThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

// applyFolders sets the folders here through their own operations (the
// watched folder takes only new files: the old computer added the rest).
func applyFolders(ctx context.Context, a *app.App, folders []HandoverFolder, choices map[string]HandoverFolderChoice) (int, error) {
	n := 0
	caller := ops.CallerFrom(ctx)
	for _, f := range folders {
		c, ok := choices[cleanPath(f.Path)]
		if !ok || c.Skip {
			continue
		}
		var err error
		switch f.Kind {
		case "watched":
			enabled := !f.Off
			in := map[string]any{"dir": c.To, "enabled": enabled, "existing": "skip", "moveProcessed": f.MoveProcessed}
			if f.ProjectID != "" {
				in["projectId"] = f.ProjectID
			}
			if f.StableSeconds >= 2 {
				in["stableSeconds"] = f.StableSeconds
			}
			b, _ := json.Marshal(in)
			_, err = ops.Call(ctx, a, "watch.set", b, caller)
		case "linked":
			b, _ := json.Marshal(map[string]any{"folder": f.ID, "path": c.To})
			_, err = ops.Call(ctx, a, "resources.folder", b, caller)
		}
		if err != nil {
			return n, apperr.From(err)
		}
		n++
	}
	return n, nil
}

// receivedSteps are what a person does now that this computer has the role.
func receivedSteps(a *app.App, m *fileManifest, rec *HandoverRecord) []HandoverStep {
	old := "“" + m.From.Name + "”"
	steps := []HandoverStep{
		{Where: "new", Words: "Check Claude Code is signed in here: from now on this computer's Claude account answers these conversations.", Command: "claude auth status", Click: "Settings → Claude Code → Whose Claude answers"},
		{Where: "new", Words: "Set the keys this computer needs (they never travel).", Command: "metarush providers set <name>", Click: "Settings → Integrations"},
		{Where: "new", Words: "Install the background service here, so the dashboard and background work run on this computer.", Command: "metarush service install"},
		{Where: "new", Words: "If people opened the dashboard on " + old + " from other computers, turn remote access on here (a new address).", Command: "metarush remote enable --apply", Click: "Settings → People"},
		{Where: "new", Words: "Each person pairs their browser's extension with this computer again.", Command: "metarush extension pair", Click: "Settings → Browser extension"},
		{Where: "old", Words: "On " + old + ": stop tailscale serve if it was on (remote access there goes off by itself).", Command: "metarush remote disable --apply"},
		{Where: "old", Words: "On " + old + ", once you're sure all is well: remove its copies of the conversations (never by itself).", Command: "metarush machine handover clean"},
	}
	return steps
}

/* ---------- Bringing conversations here (receive and take-over) ---------- */

// source is where conversations' files come from: a staged handover file,
// or a plain copied folder (a take-over).
type source struct {
	dir      string
	manifest *fileManifest
	// sessions and folders found in a plain copy (by session and
	// conversation); empty for a handover file.
	sessions map[string]string
	folders  map[string]string
}

// sessionFile is the session file for a conversation (and its own folder).
func (s source) sessionFile(convID, sessionID string) (file, dir string) {
	if s.manifest != nil {
		base := filepath.Join(s.dir, "sessions", convID, sessionID)
		if _, err := os.Stat(base + ".jsonl"); err == nil {
			return base + ".jsonl", base
		}
		return "", ""
	}
	if f, ok := s.sessions[sessionID]; ok {
		return f, strings.TrimSuffix(f, ".jsonl")
	}
	return "", ""
}

func (s source) conversationFolder(convID string) string {
	if s.manifest != nil {
		d := filepath.Join(s.dir, "conversations", convID)
		if st, err := os.Stat(d); err == nil && st.IsDir() {
			return d
		}
		return ""
	}
	return s.folders[convID]
}

// adoption brings conversations from one computer to this one.
type adoption struct {
	a        *app.App
	from     HandoverComputer
	handover string
	marked   bool
	oldDir   string
	src      source
	// counts
	moved, summaries int
	// what each computer holds, read once
	here, there map[string]assistant.HeldConversation
}

// held reads, once, the conversations this computer and the old one hold.
func (ad *adoption) held(ctx context.Context) error {
	if ad.here != nil {
		return nil
	}
	mine, err := assistant.Held(ctx, ad.a, ad.a.Cfg.Installation.ID)
	if err != nil {
		return err
	}
	theirs, err := assistant.Held(ctx, ad.a, ad.from.ID)
	if err != nil {
		return err
	}
	ad.here, ad.there = map[string]assistant.HeldConversation{}, map[string]assistant.HeldConversation{}
	for _, h := range mine {
		ad.here[h.ID] = h
	}
	for _, h := range theirs {
		ad.there[h.ID] = h
	}
	return nil
}

// one brings one conversation here (plan: only says what would happen):
// its session where Claude Code looks, its folder under this home, then the
// record re-pinned by check and set.
func (ad *adoption) one(ctx context.Context, id, title string, plan bool) (HandoverConversation, error) {
	a := ad.a
	if err := ad.held(ctx); err != nil {
		return HandoverConversation{}, err
	}
	if h, ok := ad.here[id]; ok && h.Moved != nil && h.Moved.Handover == ad.handover {
		return HandoverConversation{ID: id, Title: h.Title, Session: "here", Words: "Already here."}, nil
	}
	hc, ok := ad.there[id]
	h := &hc
	if !ok {
		return HandoverConversation{ID: id, Title: title, Session: "none", Words: "It isn't held by “" + ad.from.Name + "” any more, so it stays where it is."}, nil
	}
	c := HandoverConversation{ID: id, Title: h.Title, ProjectName: h.ProjectName, UserName: h.UserName}
	if ad.marked && (h.HandingOver == nil || h.HandingOver.Handover != ad.handover) {
		// Not marked by this handover: it may have gone on there after the
		// file was made, so it stays (one history).
		c.Session, c.Words = "none", "It isn't on its way here (it may have gone on at “"+ad.from.Name+"”), so it stays there."
		return c, nil
	}
	file, sdir := "", ""
	if h.Started && h.SessionID != "" && !h.Legacy {
		file, sdir = ad.src.sessionFile(id, h.SessionID)
	}
	summary, why := false, ""
	switch {
	case h.Legacy:
		c.Session, c.Words = "legacy", "A single request from an older version: it comes here to be read."
	case !h.Started || h.SessionID == "":
		c.Session, c.Words = "none", "It comes here and goes on here."
	case file == "":
		c.Session, c.Words, summary, why = "missing", "Claude Code's own record of it didn't come along: it goes on here from a summary of the conversation.", true, "missing"
	case !readableSession(file):
		c.Session, c.Words, summary, why = "missing", "Claude Code's own record of it can't be read: it goes on here from a summary of the conversation.", true, "unreadable"
	default:
		c.Session, c.Words = "here", "It goes on here where it left off."
	}
	if plan {
		return c, nil
	}
	if c.Session == "here" {
		place, err := assistant.SessionPlaceFor(a, h.SessionID)
		if err != nil {
			return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't find where Claude Code keeps its conversations on this computer.")
		}
		if !sameFile(file, place.File) {
			if err := setAside(place.File); err != nil {
				return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't set an older copy aside.")
			}
			if err := copyFile(file, place.File); err != nil {
				return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't put the conversation where Claude Code looks for it.")
			}
		}
		if st, err := os.Stat(sdir); err == nil && st.IsDir() {
			if _, err := os.Lstat(place.Dir); err == nil {
				if err := setAside(place.Dir); err != nil {
					return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't set an older copy aside.")
				}
			}
			if err := copyTree(sdir, place.Dir); err != nil {
				return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't put the conversation's files where Claude Code looks for them.")
			}
		}
	}
	if folder := ad.src.conversationFolder(id); folder != "" {
		dst := assistant.ConversationFolder(a, id)
		if err := setAside(dst); err != nil {
			return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't set an older copy aside.")
		}
		if err := copyTree(folder, dst); err != nil {
			return c, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't copy the conversation's folder here.")
		}
	}
	if _, err := assistant.AdoptConversation(ctx, a, assistant.Adopt{ID: id, Handover: ad.handover, From: ad.from.ID, FromName: ad.from.Name,
		Marked: ad.marked, OldDir: ad.oldDir, Summary: summary, Why: why}); err != nil {
		return c, err
	}
	ad.moved++
	if summary {
		ad.summaries++
	}
	return c, nil
}
