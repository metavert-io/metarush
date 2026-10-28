package handover

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Taking over from a computer that is gone (D347): it can't make a handover
// file, so the new computer takes its role by itself — only when the gone
// computer hasn't been seen for a while (its last touch, by the database
// server's clock), no work runs there under a live lease, and a person
// confirms it is off (a computer still running would do the same work twice).
// Its conversations come from a plain copy of its folders (Claude Code's
// projects folder and MetaRush's conversations folder, with its config.yaml
// for the folder settings — never its keys) or from a handover file; any
// without a session go on from a summary. Everything pinned to it moves, the
// link imports and generated shots it had started included (they start again
// here; AI work goes on from the request it sent).

// TakeOverQuiet is how long a computer must be unseen before another may
// take its role over.
var TakeOverQuiet = 30 * time.Minute

// HandoverTakeoverIn takes over from a computer that is gone.
type HandoverTakeoverIn struct {
	From string `json:"from" jsonschema:"minLength=1,maxLength=120,description=The computer that is gone (its name)."`
	// Source is a plain copy of its folders, or a handover file it made.
	Source string `json:"source,omitempty" jsonschema:"maxLength=4096"`
	Plan   bool   `json:"plan,omitempty"`
	// ConfirmedOff is the gone computer's name, typed by a person who
	// confirms it is off for good.
	ConfirmedOff string                 `json:"confirmedOff,omitempty" jsonschema:"maxLength=120"`
	Folders      []HandoverFolderChoice `json:"folders,omitempty"`
}

// HandoverTakeoverOut says what was (or would be) taken over.
type HandoverTakeoverOut struct {
	Plan     bool             `json:"plan,omitempty"`
	Handover *HandoverRecord  `json:"handover,omitempty"`
	From     HandoverComputer `json:"from"`
	LastSeen *time.Time       `json:"lastSeen,omitempty"`
	// Allowed: it has been unseen long enough and runs nothing now; else Why.
	Allowed       bool                   `json:"allowed"`
	Why           string                 `json:"why,omitempty"`
	Conversations []HandoverConversation `json:"conversations"`
	Work          []HandoverWork         `json:"work"`
	Folders       []HandoverFolder       `json:"folders"`
	Moved         HandoverMoved          `json:"moved"`
	Steps         []HandoverStep         `json:"steps"`
	Summary       string                 `json:"summary"`
	Next          []string               `json:"next"`
}

func takeOver(ctx context.Context, a *app.App, in *HandoverTakeoverIn) (*HandoverTakeoverOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	gone, err := findComputer(ctx, a, in.From)
	if err != nil {
		return nil, err
	}
	if gone.ID == a.Cfg.Installation.ID {
		return nil, apperr.New(apperr.InvalidInput, "That is this computer: name the computer that is gone.")
	}
	out := &HandoverTakeoverOut{Plan: in.Plan, From: HandoverComputer{ID: gone.ID, Name: gone.Name}, Conversations: []HandoverConversation{}, Folders: []HandoverFolder{}}
	if !gone.LastSeenAt.IsZero() {
		t := gone.LastSeenAt
		out.LastSeen = &t
	}
	out.Allowed, out.Why, err = quietEnough(ctx, a, gone)
	if err != nil {
		return nil, err
	}
	if !in.Plan && !out.Allowed {
		return nil, apperr.New(apperr.Conflict, "%s", out.Why).WithDetails(map[string]any{"reason": "seen_recently"})
	}
	// Where its conversations' files come from.
	src := source{sessions: map[string]string{}, folders: map[string]string{}}
	oldDir := ""
	var cfgFolders []HandoverFolder
	if s := strings.TrimSpace(in.Source); s != "" {
		p, err := filepath.Abs(expandHome(s))
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s isn't a place on this computer.", s)
		}
		st, err := os.Stat(p)
		if err != nil {
			return nil, apperr.New(apperr.NotFound, "There's nothing at %s.", p)
		}
		if st.IsDir() {
			found, err := scanCopy(p)
			if err != nil {
				return nil, err
			}
			src.sessions, src.folders, cfgFolders = found.sessions, found.folders, found.config
			oldDir = found.oldDir
		} else {
			if err := file2(ctx, a, p, gone, &src, &oldDir, &cfgFolders); err != nil {
				return nil, err
			}
			defer os.RemoveAll(src.dir)
		}
	}
	if cfgFolders == nil {
		if cfgFolders, err = linkedFoldersOf(ctx, a, gone.ID); err != nil {
			return nil, err
		}
	}
	for _, f := range cfgFolders {
		f.Words = folderWords(f, "this computer")
		out.Folders = append(out.Folders, f)
	}
	if out.Work, err = pinnedWork(ctx, a, gone.ID, true); err != nil {
		return nil, err
	}
	if out.Work == nil {
		out.Work = []HandoverWork{}
	}
	choices, err := checkChoices(out.Folders, in.Folders, in.Plan)
	if err != nil {
		return nil, err
	}
	held, err := assistant.Held(ctx, a, gone.ID)
	if err != nil {
		return nil, err
	}
	rec := &HandoverRecord{ID: ids.New(ids.Handover), WorkspaceID: a.WorkspaceID(), Kind: KindTakeover, From: gone.ID, FromName: gone.Name,
		To: a.Cfg.Installation.ID, ToName: a.Cfg.Installation.Name, State: StateDone, StartedBy: a.Actor, StartedAt: time.Now().UTC()}
	plan := in.Plan
	if !plan && !strings.EqualFold(strings.TrimSpace(in.ConfirmedOff), gone.Name) {
		return nil, apperr.New(apperr.InvalidInput, "Confirm that “%s” is off for good: type its name.", gone.Name).
			WithHint("A computer that is still running would do the same work twice.").WithDetails(map[string]any{"reason": "confirm_off"})
	}
	ad := adoption{a: a, from: out.From, handover: rec.ID, oldDir: oldDir, src: src}
	for _, h := range held {
		c, err := ad.one(ctx, h.ID, h.Title, plan)
		if err != nil {
			return nil, err
		}
		out.Conversations = append(out.Conversations, c)
	}
	out.Steps = takeoverSteps(gone.Name)
	out.Next = []string{"metarush machine revoke " + shellQuote(gone.Name) + "   (last: its storage key stops working)"}
	if plan {
		switch {
		case !out.Allowed:
			out.Summary = "Not yet: " + out.Why
		default:
			out.Summary = fmt.Sprintf("Taking over from “%s” would bring %s here and move %s. Confirm it is off to go ahead.", gone.Name,
				plural(len(out.Conversations), "conversation"), plural(countMoves(out.Work), "piece of work", "pieces of work"))
		}
		return out, nil
	}
	moved, err := moveWork(ctx, a, gone.ID, a.Cfg.Installation.ID, a.Cfg.Installation.Name, true)
	if err != nil {
		return nil, err
	}
	moved.Conversations, moved.Summaries = ad.moved, ad.summaries
	if moved.Folders, err = applyFolders(ctx, a, out.Folders, choices); err != nil {
		return nil, err
	}
	// A handover from it that never finished ends: this computer has the role.
	now := time.Now().UTC()
	_, _ = a.DB.C(store.CHandovers).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "from": gone.ID, "active": true},
		bson.M{"$set": bson.M{"state": StateAbandoned, "abandonedAt": now, "abandonedBy": a.Actor}, "$unset": bson.M{"active": ""}})
	actor := a.Actor
	rec.Moved, rec.ConfirmedOff, rec.ReceivedAt, rec.ReceivedBy = moved, &actor, &now, &actor
	for _, c := range out.Conversations {
		rec.Conversations = append(rec.Conversations, c.ID)
	}
	// One write: the record, settled by the database server's clock.
	doc, err := toDoc(rec)
	if err != nil {
		return nil, err
	}
	if _, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$setOnInsert": doc, "$currentDate": bson.M{"settledAt": true, "steppedDownAt": true}}, options.UpdateOne().SetUpsert(true)); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record the take-over.")
	}
	if rec, err = getRecord(ctx, a, rec.ID); err != nil {
		return nil, err
	}
	if err := settleOvertaken(ctx, a, a.Cfg.Installation.ID, rec); err != nil {
		return nil, err
	}
	a.Audit(ctx, "machine.takeover", "handover", rec.ID, "", map[string]any{"from": gone.Name, "conversations": moved.Conversations, "summaries": moved.Summaries,
		"runs": moved.Runs, "triggers": moved.Triggers, "jobs": moved.Jobs, "folders": moved.Folders})
	out.Handover, out.Moved = rec, moved
	out.Summary = fmt.Sprintf("This computer took over from “%s”: %s, %s and %s came here.", gone.Name, plural(moved.Conversations, "conversation"),
		plural(moved.Jobs+moved.Runs+moved.Triggers, "piece of work", "pieces of work"), plural(moved.Folders, "folder"))
	if moved.Summaries > 0 {
		out.Summary += fmt.Sprintf(" %s go on from a summary.", plural(moved.Summaries, "conversation"))
	}
	out.Summary += " Last, remove it from the workspace so its storage key stops working."
	return out, nil
}

// toDoc is a record as fields to insert (its ID and workspace come from the filter).
func toDoc(rec *HandoverRecord) (bson.M, error) {
	b, err := bson.Marshal(rec)
	if err != nil {
		return nil, err
	}
	var doc bson.M
	if err := bson.Unmarshal(b, &doc); err != nil {
		return nil, err
	}
	delete(doc, "_id")
	delete(doc, "workspaceId")
	return doc, nil
}

// quietEnough: the gone computer hasn't been seen for TakeOverQuiet by the
// database server's clock, and runs nothing under a live lease.
func quietEnough(ctx context.Context, a *app.App, gone *models.Installation) (bool, string, error) {
	f := store.SeenWithin("lastSeenAt", TakeOverQuiet)
	f["_id"], f["workspaceId"] = gone.ID, a.WorkspaceID()
	n, err := a.DB.C(store.CInstallations).CountDocuments(ctx, f)
	if err != nil {
		return false, "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't read when that computer was last seen.")
	}
	if n > 0 {
		ago := "recently"
		if !gone.LastSeenAt.IsZero() {
			mins := int(time.Since(gone.LastSeenAt).Minutes())
			if mins < 1 {
				ago = "less than a minute ago"
			} else {
				ago = plural(mins, "minute") + " ago"
			}
		}
		return false, fmt.Sprintf("“%s” was seen %s, so it may still be running: hand over from it instead (metarush machine handover export there), or wait until it has been off for %d minutes.",
			gone.Name, ago, int(TakeOverQuiet.Minutes())), nil
	}
	jf := store.LiveLease()
	jf["workspaceId"], jf["state"], jf["lease.owner"] = a.WorkspaceID(), models.JobRunning, gone.ID
	if n, err := a.DB.C(store.CJobs).CountDocuments(ctx, jf); err != nil {
		return false, "", apperr.Wrap(apperr.Database, err, "MetaRush couldn't read what that computer is running.")
	} else if n > 0 {
		return false, fmt.Sprintf("“%s” is running work right now, so it isn't off.", gone.Name), nil
	}
	return true, "", nil
}

// file2 reads a handover file the gone computer made, for a take-over.
func file2(ctx context.Context, a *app.App, file string, gone *models.Installation, src *source, oldDir *string, folders *[]HandoverFolder) error {
	dir, err := stagingDir(a)
	if err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't make a private folder for the file.")
	}
	m, err := stageFile(file, passphraseFrom(ctx), dir, func(m *fileManifest) error {
		if m.Workspace.ID != a.WorkspaceID() || m.From.ID != gone.ID {
			return apperr.New(apperr.Conflict, "This handover file was made on “%s”, not on “%s”.", m.From.Name, gone.Name)
		}
		return nil
	})
	if errors.Is(err, errNeedsPassphrase) {
		os.RemoveAll(dir)
		return apperr.New(apperr.InvalidInput, "This handover file is protected by a passphrase: MetaRush asks for it in the terminal.").
			WithDetails(map[string]any{"reason": "needs_passphrase"})
	}
	if err != nil {
		os.RemoveAll(dir)
		return err
	}
	src.dir, src.manifest = dir, m
	*oldDir = m.AssistantDir
	*folders = m.Folders
	if *folders == nil {
		*folders = []HandoverFolder{}
	}
	return nil
}

// copyFound is what a plain copy of a computer's folders holds.
type copyFound struct {
	sessions map[string]string // session ID → its .jsonl
	folders  map[string]string // conversation ID → its folder
	config   []HandoverFolder  // from its config.yaml (nil: none found)
	oldDir   string            // MetaRush's folder for Claude Code there
}

// scanCopy looks through a plain copy of a gone computer's folders (links
// are never followed): Claude Code's session files, MetaRush's
// conversation folders and the first config.yaml (for folder paths only).
func scanCopy(root string) (*copyFound, error) {
	f := &copyFound{sessions: map[string]string{}, folders: map[string]string{}}
	cfgPath, cfgDepth := "", 1<<30
	n := 0
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // unreadable parts are left out
		}
		if n++; n > 500000 {
			return fs.SkipAll
		}
		depth := strings.Count(strings.TrimPrefix(p, root), string(filepath.Separator))
		if d.IsDir() {
			if depth > 10 {
				return fs.SkipDir
			}
			if assistant.ValidID(d.Name()) && filepath.Base(filepath.Dir(p)) == "conversations" {
				if _, ok := f.folders[d.Name()]; !ok {
					f.folders[d.Name()] = p
				}
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		name := d.Name()
		switch {
		case strings.HasSuffix(name, ".jsonl") && assistant.ValidSessionID(strings.TrimSuffix(name, ".jsonl")):
			sid := strings.TrimSuffix(name, ".jsonl")
			if _, ok := f.sessions[sid]; !ok {
				f.sessions[sid] = p
			}
		case name == "config.yaml" && depth < cfgDepth:
			cfgPath, cfgDepth = p, depth
		}
		return nil
	})
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't look through that folder.")
	}
	if cfgPath != "" {
		f.config = foldersFromConfig(cfgPath)
	}
	for _, sf := range f.sessions {
		if d := assistantDirOf(sf); d != "" {
			f.oldDir = d
			break
		}
	}
	return f, nil
}

// foldersFromConfig reads only the folder settings of a copied config.yaml
// (nothing else in it is read: never a key).
func foldersFromConfig(p string) []HandoverFolder {
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	var c struct {
		Worker struct {
			Watch config.Watch `yaml:"watch"`
		} `yaml:"worker"`
		Resources struct {
			Folders []config.FolderPath `yaml:"folders"`
		} `yaml:"resources"`
	}
	if yaml.Unmarshal(b, &c) != nil {
		return nil
	}
	out := []HandoverFolder{}
	if w := c.Worker.Watch; w.Dir != "" {
		out = append(out, HandoverFolder{Kind: "watched", Name: "The watched folder", Path: w.Dir, Off: !w.Enabled, ProjectID: w.ProjectID, MoveProcessed: w.MoveProcessed, StableSeconds: w.StableSeconds})
	}
	for _, f := range c.Resources.Folders {
		if f.ID != "" && f.Path != "" {
			out = append(out, HandoverFolder{Kind: "linked", ID: f.ID, Name: "A linked folder", Path: f.Path, Off: f.Off})
		}
	}
	return out
}

// linkedFoldersOf are the linked folders the workspace knows a computer had
// (their names, and where each was as that computer last said).
func linkedFoldersOf(ctx context.Context, a *app.App, inst string) ([]HandoverFolder, error) {
	var recs []models.LinkedFolder
	if err := a.DB.Find(ctx, store.CLinkedFolders, a.WorkspaceID(), bson.M{"machines." + inst: bson.M{"$exists": true}, "unlinked": bson.M{"$ne": true}}, &recs,
		options.Find().SetProjection(bson.M{"name": 1, "machines": 1})); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read the linked folders.")
	}
	out := []HandoverFolder{}
	for _, r := range recs {
		out = append(out, HandoverFolder{Kind: "linked", ID: r.ID, Name: r.Name, Path: r.Machines[inst].Display})
	}
	return out, nil
}

// assistantDirOf reads where a session's working folder was (its lines say)
// and gives MetaRush's folder for Claude Code around it.
func assistantDirOf(sessionFile string) string {
	f, err := os.Open(sessionFile)
	if err != nil {
		return ""
	}
	defer f.Close()
	r := bufio.NewReaderSize(f, 1<<20)
	for i := 0; i < 50; i++ {
		line, err := r.ReadBytes('\n')
		var l struct {
			Cwd string `json:"cwd"`
		}
		if json.Unmarshal(line, &l) == nil && l.Cwd != "" && filepath.Base(l.Cwd) == "workspace" {
			return filepath.Dir(l.Cwd)
		}
		if err != nil {
			break
		}
	}
	return ""
}

func takeoverSteps(gone string) []HandoverStep {
	return []HandoverStep{
		{Where: "new", Words: "Check Claude Code is signed in here: from now on this computer's Claude account answers these conversations.", Command: "claude auth status", Click: "Settings → Claude Code → Whose Claude answers"},
		{Where: "new", Words: "Set the keys this computer needs (they never travel).", Command: "metarush providers set <name>", Click: "Settings → Integrations"},
		{Where: "new", Words: "Install the background service here.", Command: "metarush service install"},
		{Where: "new", Words: "If people opened the dashboard on “" + gone + "” from other computers, turn remote access on here (a new address).", Command: "metarush remote enable --apply", Click: "Settings → People"},
		{Where: "new", Words: "Each person pairs their browser's extension with this computer again.", Command: "metarush extension pair", Click: "Settings → Browser extension"},
		{Where: "new", Words: "Last: remove “" + gone + "” from the workspace, so its storage key stops working.", Command: "metarush machine revoke " + shellQuote(gone)},
	}
}
