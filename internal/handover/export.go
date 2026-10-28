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
	case rec != nil && rec.State == StateReceiving:
		return nil, apperr.New(apperr.Conflict, "“%s” is receiving this computer's role now.", rec.ToName).
			WithHint("If it stopped part-way and is gone, abandon the handover here once it has been quiet for " + quietWords() + ", or take it over.").
			WithCommand("metarush machine handover abandon")
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
	// From here on, every way this stops takes this run's marks off again
	// (putBack): a conversation is never left "moving" without a handover file.
	_, busyNow, err := assistant.MarkHandingOver(ctx, a, convIDs, assistant.HandingOverMark{Handover: rec.ID, To: to.ID, ToName: to.Name})
	if err != nil {
		putBack(ctx, a, rec.ID, me.ID)
		return nil, err
	}
	if len(busyNow) > 0 {
		back := putBack(ctx, a, rec.ID, me.ID)
		return nil, apperr.New(apperr.Conflict, "A message started in %s while the handover was being made.%s", plural(len(busyNow), "conversation"), back).
			WithHint("Wait until Claude finishes, then run the same command again.").WithDetails(map[string]any{"reason": "busy"})
	}
	if markedHook != nil {
		markedHook(rec.ID)
	}
	// Marked only while the handover is still being made: one abandoned (or
	// being received) meanwhile takes its marks off at once — whichever comes
	// last, the abandon or this look, sees the other.
	if cur, err := getRecord(ctx, a, rec.ID); err != nil {
		putBack(ctx, a, rec.ID, me.ID)
		return nil, err
	} else if cur.State != StateExporting && cur.State != StateExported {
		back := putBack(ctx, a, rec.ID, me.ID)
		if cur.State == StateAbandoned {
			return nil, apperr.New(apperr.Conflict, "The handover was abandoned while its file was being made.%s", back).
				WithDetails(map[string]any{"reason": "abandoned"})
		}
		return nil, apperr.New(apperr.Conflict, "“%s” is receiving this computer's role now.", cur.ToName).
			WithCommand("metarush machine handover status").WithDetails(map[string]any{"reason": "receiving"})
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
		// No file was made: the conversations go on here (one written
		// before keeps its file and the conversations it carries).
		back := putBack(ctx, a, rec.ID, me.ID)
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, err
		}
		if errors.Is(err, os.ErrExist) {
			return nil, apperr.New(apperr.Conflict, "There's already a file at %s: MetaRush never writes over one.%s", out, back).WithHint("Choose another name for it.")
		}
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't write the handover file.%s", back)
	}
	now := time.Now().UTC()
	convs := make([]string, 0, len(m.Conversations))
	for _, c := range m.Conversations {
		convs = append(convs, c.ID)
	}
	if r, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": rec.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$in": bson.A{StateExporting, StateExported}}},
		bson.M{"$set": bson.M{"state": StateExported, "exportedAt": now, "conversations": convs, "sessions": sessions, "file": filepath.Base(out)}}); err != nil {
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't record the handover.")
	} else if r.MatchedCount == 0 {
		// Abandoned (or taken over) while the file was being made: it can't
		// be received, so it goes, and so do the marks.
		_ = os.Remove(out)
		back := putBack(ctx, a, rec.ID, me.ID)
		return nil, apperr.New(apperr.Conflict, "The handover was ended while its file was being made, so the file was removed.%s", back).
			WithCommand("metarush machine handover status").WithDetails(map[string]any{"reason": "changed"})
	}
	rec, err = getRecord(ctx, a, rec.ID)
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "machine.handover.export", "handover", rec.ID, "", map[string]any{"to": to.Name, "conversations": res.Conversations, "sessions": res.Sessions,
		"summaries": res.Summaries, "encrypted": res.Encrypted, "folders": res.Folders})
	res.Handover, res.File, res.Bytes = *rec, out, size
	rememberFile(a, rec.ID, out, size) // for clean, on this computer only
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

// markedHook runs (in tests) once an export marked its conversations.
var markedHook func(handover string)

// putBack takes an export's marks off after it stopped: a handover never
// written out is abandoned and all its conversations go on here; one whose
// file was written before keeps the conversations that file carries, and the
// rest go on here. It says so in a few words ("" when nothing was put back).
func putBack(ctx context.Context, a *app.App, id, me string) string {
	ctx = context.WithoutCancel(ctx)
	cur, err := getRecord(ctx, a, id)
	if err != nil {
		return ""
	}
	if cur.State == StateExporting {
		if _, err := a.DB.C(store.CHandovers).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "state": StateExporting},
			bson.M{"$set": bson.M{"state": StateAbandoned, "abandonedAt": time.Now().UTC(), "abandonedBy": a.Actor}, "$unset": bson.M{"active": ""}}); err != nil {
			return ""
		}
		if cur, err = getRecord(ctx, a, id); err != nil {
			return ""
		}
	}
	if cur.State == StateAbandoned {
		if _, err := assistant.UnmarkHandingOver(ctx, a, id, me); err != nil {
			return ""
		}
		return " Nothing moved: the conversations go on here."
	}
	n, err := assistant.UnmarkHandingOverExcept(ctx, a, id, me, cur.Conversations)
	if err != nil || n == 0 {
		return ""
	}
	return fmt.Sprintf(" %s that the handover file made before doesn't carry %s on here.", upperFirst(plural(n, "conversation")), goWord(n))
}

func goWord(n int) string {
	if n == 1 {
		return "goes"
	}
	return "go"
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// quietWords is how long a computer must be quiet, in words.
func quietWords() string { return plural(int(TakeOverQuiet.Minutes()), "minute") }

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
	// Checked before anything is marked, so a drive that can't take the file
	// leaves everything as it was.
	if err := writable(filepath.Dir(abs)); err != nil {
		return "", apperr.New(apperr.Conflict, "MetaRush can't write a file in %s.", filepath.Dir(abs)).
			WithHint("Check the drive is plugged in and not read-only, or choose another place.").WithDetails(map[string]any{"reason": "not_writable"})
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
