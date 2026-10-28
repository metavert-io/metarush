package handover

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// HandoverExportIn makes the handover file on the old computer.
type HandoverExportIn struct {
	To string `json:"to" jsonschema:"minLength=1,maxLength=120,description=The computer taking the role over (its name)."`
	// Out is where the file goes (never over a file that is there, never in a
	// sync folder or a repository); empty: ~/.metarush-bundles.
	Out string `json:"out,omitempty" jsonschema:"maxLength=4096"`
	// NoPassphrase writes the file unencrypted: anyone who has it can read
	// the conversations.
	NoPassphrase bool `json:"noPassphrase,omitempty"`
}

// HandoverExportOut says what the file holds and what comes next.
type HandoverExportOut struct {
	Handover  HandoverRecord `json:"handover"`
	File      string         `json:"file"`
	Bytes     int64          `json:"bytes"`
	Encrypted bool           `json:"encrypted"`
	// Conversations is how many travel in it, Sessions how many with Claude
	// Code's own record, Summaries how many go on from a summary.
	Conversations int      `json:"conversations"`
	Sessions      int      `json:"sessions"`
	Summaries     int      `json:"summaries"`
	Folders       int      `json:"folders"`
	Warnings      []string `json:"warnings,omitempty"`
	Summary       string   `json:"summary"`
	Next          []string `json:"next"`
}

// NoPassphraseWarning is said whenever a file is made without a passphrase.
const NoPassphraseWarning = "This handover file isn't protected by a passphrase: anyone who gets it can read these conversations — what was said, and what Claude read. Keep it private, and delete it once it has been received."

func exportHandover(ctx context.Context, a *app.App, in *HandoverExportIn) (*HandoverExportOut, error) {
	if err := peopleOnly(a); err != nil {
		return nil, err
	}
	pass := passphraseFrom(ctx)
	if !in.NoPassphrase {
		if pass == "" {
			return nil, apperr.New(apperr.InvalidInput, "A handover file needs a passphrase: MetaRush asks for it in the terminal.").
				WithHint("Run the command in a terminal, or give the passphrase on standard input.").WithCommand("metarush machine handover export --to <computer> --passphrase-stdin")
		}
		if err := CheckPassphrase(pass); err != nil {
			return nil, err
		}
	} else {
		pass = ""
	}
	me := HandoverComputer{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}
	role, err := RoleOf(ctx, a, me.ID)
	if err != nil {
		return nil, err
	}
	if role.Role != RoleHost {
		return nil, apperr.New(apperr.Conflict, "%s", role.Says).WithDetails(map[string]any{"reason": "not_host"})
	}
	to, err := targetComputer(ctx, a, in.To)
	if err != nil {
		return nil, err
	}
	out, err := handoverPath(in.Out, to.Name, pass != "")
	if err != nil {
		return nil, err
	}
	// The record: the one in progress to the same computer goes on (an
	// export can run again), another computer's is refused.
	rec, err := activeFrom(ctx, a, me.ID)
	if err != nil {
		return nil, err
	}
	switch {
	case rec != nil && rec.To != to.ID:
		return nil, apperr.New(apperr.Conflict, "A handover from this computer to “%s” is in progress.", rec.ToName).
			WithHint("Abandon it first to hand over to another computer.").WithCommand("metarush machine handover abandon")
	case rec != nil && rec.State == StateReceived:
		return nil, apperr.New(apperr.Conflict, "“%s” has already received this computer's role.", rec.ToName).WithCommand("metarush machine handover status")
	case rec == nil:
		rec = &HandoverRecord{ID: ids.New(ids.Handover), WorkspaceID: a.WorkspaceID(), Kind: KindHandover, From: me.ID, FromName: me.Name, To: to.ID, ToName: to.Name,
			State: StateExporting, Active: true, StartedBy: a.Actor, StartedAt: time.Now().UTC()}
		if err := a.DB.Insert(ctx, store.CHandovers, rec); err != nil {
			if store.IsDuplicateKey(err) {
				return nil, apperr.New(apperr.Conflict, "Another handover from this computer has just started.").WithCommand("metarush machine handover status")
			}
			return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record the handover.")
		}
	}
	// The conversations: none Claude is working on now, then each marked as
	// on its way (from now on this computer never continues them). A message
	// a MetaRush that stopped left running ends first, as when it starts again.
	assistant.Settle(ctx, a)
	held, err := assistant.Held(ctx, a, me.ID)
	if err != nil {
		return nil, err
	}
	var busy, convIDs []string
	for _, h := range held {
		if h.Busy {
			busy = append(busy, "“"+h.Title+"”")
		}
		convIDs = append(convIDs, h.ID)
	}
	if len(busy) > 0 {
		return nil, apperr.New(apperr.Conflict, "Claude is working on %s here.", strings.Join(busy, ", ")).
			WithHint("Wait until it finishes (or stop it), then run the same command again.").WithDetails(map[string]any{"reason": "busy"})
	}
	_, busyNow, err := assistant.MarkHandingOver(ctx, a, convIDs, assistant.HandingOverMark{Handover: rec.ID, To: to.ID, ToName: to.Name})
	if err != nil {
		return nil, err
	}
	if len(busyNow) > 0 {
		return nil, apperr.New(apperr.Conflict, "A message started in %s while the handover was being made.", plural(len(busyNow), "conversation")).
			WithHint("Wait until Claude finishes, then run the same command again.").WithDetails(map[string]any{"reason": "busy"})
	}
	if held, err = assistant.Held(ctx, a, me.ID); err != nil {
		return nil, err
	}
	work, err := pinnedWork(ctx, a, me.ID, false)
	if err != nil {
		return nil, err
	}
	folders, err := foldersHere(ctx, a)
	if err != nil {
		return nil, err
	}
	m := &fileManifest{Format: FileFormat, Handover: rec.ID, Workspace: HandoverComputer{ID: a.WorkspaceID(), Name: a.Cfg.Workspace.Name}, From: me,
		To: HandoverComputer{ID: to.ID, Name: to.Name}, MadeAt: time.Now().UTC(), MadeBy: a.Actor.UserName, MetaRush: version.Current().Version,
		AssistantDir: assistant.AssistantFolder(a), Work: work, Folders: folders}
	sessions := map[string]string{}
	res := &HandoverExportOut{Encrypted: pass != "", Folders: len(folders)}
	size, err := writeFile(out, pass, m, func(p *packer) error {
		for _, h := range held {
			if h.HandingOver == nil || h.HandingOver.Handover != rec.ID {
				continue
			}
			fc := fileConversation{ID: h.ID, Title: h.Title, SessionID: h.SessionID}
			state, _ := sessionState(a, h, "")
			switch state {
			case "here":
				place, ok := assistant.FindSession(a, h.SessionID)
				if !ok {
					return fmt.Errorf("a session went missing while the file was being made")
				}
				prefix := path.Join("sessions", h.ID)
				if err := p.addFile(path.Join(prefix, h.SessionID+".jsonl"), place.File); err != nil {
					return err
				}
				if err := p.addDir(path.Join(prefix, h.SessionID), place.Dir); err != nil {
					return err
				}
				fc.Session = "in"
				sessions[h.ID] = h.SessionID
				res.Sessions++
			case "missing", "lost":
				fc.Session = state
				res.Summaries++
			default:
				fc.Session = "none"
			}
			if err := p.addDir(path.Join("conversations", h.ID), assistant.ConversationFolder(a, h.ID)); err != nil {
				return err
			}
			m.Conversations = append(m.Conversations, fc)
			res.Conversations++
		}
		return nil
	})
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		if errors.Is(err, os.ErrExist) {
			return nil, apperr.New(apperr.Conflict, "There's already a file at %s: MetaRush never writes over one.", out).WithHint("Choose another name for it.")
		}
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't write the handover file.")
	}
	now := time.Now().UTC()
	convs := make([]string, 0, len(m.Conversations))
	for _, c := range m.Conversations {
		convs = append(convs, c.ID)
	}
	if _, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{StateExporting, StateExported}}},
		bson.M{"$set": bson.M{"state": StateExported, "exportedAt": now, "conversations": convs, "sessions": sessions, "file": filepath.Base(out)}}); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record the handover.")
	}
	rec, err = getRecord(ctx, a, rec.ID)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "machine.handover.export", "handover", rec.ID, "", map[string]any{"to": to.Name, "conversations": res.Conversations, "sessions": res.Sessions,
		"summaries": res.Summaries, "encrypted": res.Encrypted, "folders": res.Folders})
	res.Handover, res.File, res.Bytes = *rec, out, size
	if !res.Encrypted {
		res.Warnings = append(res.Warnings, NoPassphraseWarning)
	}
	what := plural(res.Conversations, "conversation")
	if res.Summaries > 0 {
		what += fmt.Sprintf(" (%d to go on from a summary)", res.Summaries)
	}
	res.Summary = fmt.Sprintf("The handover file for “%s” is ready: %s, the work pinned here and %s. This computer no longer continues those conversations.",
		to.Name, what, plural(res.Folders, "folder setting"))
	res.Next = []string{"Carry the file to “" + to.Name + "” yourself (a drive, AirDrop or scp — never a shared or synced folder).",
		"On “" + to.Name + "”: metarush machine handover receive " + shellQuote(filepath.Base(out)),
		"Changed your mind? metarush machine handover abandon puts everything back here."}
	return res, nil
}

var nameSafe = regexp.MustCompile(`[^a-z0-9-]+`)

// handoverPath is where the file goes: out as given (made absolute), or a new
// name in ~/.metarush-bundles. Never in a sync folder or a repository, never
// over a file that is there.
func handoverPath(out, toName string, encrypted bool) (string, error) {
	if strings.TrimSpace(out) == "" {
		dir, err := secretpath.DefaultDir()
		if err != nil {
			return "", apperr.Wrap(apperr.Internal, err, "MetaRush couldn't find a private folder for the handover file.").WithHint("Say where the file goes.")
		}
		slug := strings.Trim(nameSafe.ReplaceAllString(strings.ToLower(toName), "-"), "-")
		if slug == "" {
			slug = "computer"
		}
		ext := ".age"
		if !encrypted {
			ext = ".tar.gz"
		}
		out = filepath.Join(dir, "metarush-handover-"+slug+"-"+time.Now().Format("20060102-1504")+ext)
	}
	abs, err := filepath.Abs(expandHome(out))
	if err != nil {
		return "", apperr.New(apperr.InvalidInput, "That isn't a place for a file.")
	}
	if st, err := os.Stat(abs); err == nil {
		if st.IsDir() {
			return "", apperr.New(apperr.InvalidInput, "%s is a folder: give the file's name too.", abs)
		}
		return "", apperr.New(apperr.Conflict, "There's already a file at %s: MetaRush never writes over one.", abs).WithHint("Choose another name.")
	}
	if st, err := os.Stat(filepath.Dir(abs)); err != nil || !st.IsDir() {
		return "", apperr.New(apperr.InvalidInput, "The folder %s isn't there.", filepath.Dir(abs))
	}
	if err := secretpath.Check(abs, "the handover file", false); err != nil {
		return "", apperr.From(err)
	}
	return abs, nil
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}
