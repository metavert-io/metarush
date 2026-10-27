package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/publish"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

// Exporting a workspace (D329–D333, docs/workspace-export.md): one archive
// of everything about the work, for a backup, a move to another computer or
// service, or leaving. Owners only and people only — never Auto, Claude, an
// AI app or a review-desk draft. The export only reads the database (the
// command line opens it read-only: app.App.ReadOnly). A restore is a step at
// a terminal or in setup on the computer the copy goes to, never an
// operation here: there is no workspace to call it in until it is done.

// ArchiveHolds says in plain words what an archive holds, and how to keep it.
const ArchiveHolds = "An export holds everything about the work in this workspace: projects, versions, transcripts, comments, " +
	"people and their consent records, Claude Code conversations and the activity log — and the footage itself when you " +
	"include it. Passwords, keys and each computer's own settings are never in it. Only your account on this computer " +
	"can open it, but anyone who gets a copy can read all of it: keep it as carefully as the work."

// WorkspaceExportIn asks for an export of the workspace, or its plan.
type WorkspaceExportIn struct {
	// To is a new folder, or with Tar a new file. Over the network (the
	// dashboard) it is left out: the archive goes into MetaRush's exports
	// folder on the computer it runs on.
	To string `json:"to,omitempty" jsonschema:"maxLength=4096,description=Where to write the archive: a new folder\\, or with tar a new file (on the computer MetaRush runs on). Left out in the dashboard: MetaRush's archives folder."`
	// Media: referenced (the default: content addresses only) or included
	// (every file of footage copied into the archive and checked).
	Media string `json:"media,omitempty" jsonschema:"enum=,enum=referenced,enum=included,description=referenced (the default): the archive lists the footage by content address and the copy expects it in the same storage; included: every file is copied into the archive and checked."`
	Tar   bool   `json:"tar,omitempty" jsonschema:"description=One file (.tar) instead of a folder."`
	// Plan says what would be written, with counts and sizes, and writes nothing.
	Plan bool `json:"plan,omitempty" jsonschema:"description=Only say what would be written: counts\\, sizes\\, what stays out and why."`
	// Background starts it and answers at once (the dashboard follows it
	// with workspace.exports).
	Background bool `json:"background,omitempty"`
}

// WorkspaceExportOut is an export made or started, or its plan.
type WorkspaceExportOut struct {
	Plan   *wsarchive.ExportPlan `json:"plan,omitempty"`
	Export *ExportRecord         `json:"export,omitempty"`
	// Holds says what an archive holds and how to keep it.
	Holds   string   `json:"holds"`
	Summary string   `json:"summary"`
	Next    []string `json:"next,omitempty"`
}

// WorkspaceExportsIn lists exports made on this computer.
type WorkspaceExportsIn struct{}

// WorkspaceExportsOut is the exports made on this computer, newest first.
type WorkspaceExportsOut struct {
	Exports []ExportRecord `json:"exports"`
	// Folder is where the dashboard's exports go on this computer.
	Folder string `json:"folder"`
	Holds  string `json:"holds"`
	// Restore says how the archive is restored (a terminal or setup step).
	Restore string `json:"restore"`
}

// ExportRecord is one export made on this computer, as it goes.
type ExportRecord struct {
	ID    string `json:"id"`
	State string `json:"state" jsonschema:"enum=running,enum=done,enum=failed,enum=stopped"`
	// Path is where the archive is (Details); Name its last part.
	Path       string     `json:"path"`
	Name       string     `json:"name"`
	Media      string     `json:"media" jsonschema:"enum=referenced,enum=included"`
	Tar        bool       `json:"tar"`
	Workspace  string     `json:"workspace"`
	By         string     `json:"by,omitempty"`
	StartedAt  time.Time  `json:"startedAt"`
	FinishedAt *time.Time `json:"finishedAt,omitempty"`
	UpdatedAt  time.Time  `json:"updatedAt"`
	// Progress is how far it is.
	Progress wsarchive.ArchiveProgress `json:"progress"`
	// When done: what it holds.
	Documents   int64 `json:"documents,omitempty"`
	Collections int   `json:"collections,omitempty"`
	MediaFiles  int   `json:"mediaFiles,omitempty"`
	MediaBytes  int64 `json:"mediaBytes,omitempty"`
	Bytes       int64 `json:"bytes,omitempty"`
	// Level is the database's migration level; Older says it is older than
	// this MetaRush's (the archive reads as it was; a restore brings it up).
	Level int  `json:"level,omitempty"`
	Older bool `json:"older,omitempty"`
	// LeftOut names what stayed out (credentials, computers' own settings).
	LeftOut []string `json:"leftOut,omitempty"`
	// Scrubbed says how many records had something that looked like a key
	// or a password taken out, and of which kinds.
	Scrubbed wsarchive.ScrubSummary `json:"scrubbed"`
	Notes    []string               `json:"notes,omitempty"`
	Error    string                 `json:"error,omitempty"`
	Says     string                 `json:"says"`
	// PID is the process making it (a stopped one is said as stopped).
	PID int `json:"-"`
}

// PIDJSON keeps the process in the state file (not in answers).
type exportFile struct {
	ExportRecord
	PID int `json:"pid"`
}

type exportSecretsKey struct{}

// WithExportSecrets gives an export exact values to keep out of the archive
// beyond the installation's own (the address of another database, its
// storage's key).
func WithExportSecrets(ctx context.Context, values ...string) context.Context {
	return context.WithValue(ctx, exportSecretsKey{}, values)
}

func init() {
	Def("workspace.export", "Export the whole workspace into one archive — projects, versions, transcripts, people and consent records, conversations, the log, and the footage when included — for a backup or a move (owners, people only; --plan first)",
		Flags{Mutating: true, HumanOnly: true, OwnerOnly: true}, workspaceExport)
	Def("workspace.exports", "List the exports of the workspace made on this computer and how far each one is (owners, people only)",
		Flags{HumanOnly: true, OwnerOnly: true}, func(ctx context.Context, a *app.App, _ *WorkspaceExportsIn) (*WorkspaceExportsOut, error) {
			if err := archivePeopleOnly(a); err != nil {
				return nil, err
			}
			return &WorkspaceExportsOut{Exports: listExports(a), Folder: ArchivesFolder(a), Holds: ArchiveHolds, Restore: RestoreWords}, nil
		})
}

// RestoreWords says how an archive is restored.
const RestoreWords = "A restore happens in a terminal on the computer the copy goes to, never in the dashboard: " +
	"metarush setup --restore <archive> on a new computer, or metarush workspace restore <archive> into an empty database. " +
	"It checks every file first and never deletes anything."

// archivePeopleOnly: exports are a person's, every time — never Auto, the
// dashboard's Claude, an AI app or the review desk (on top of HumanOnly).
func archivePeopleOnly(a *app.App) error {
	if a.Actor.Via == models.ViaAssistant || a.Actor.Via == "assistant-auto" || a.Actor.Agent != "" || a.ProposeOnly {
		return apperr.New(apperr.HumanOnly, "Only a person exports or restores a workspace: Claude, Auto and AI apps never do.").
			WithDetails(map[string]any{"reason": "people_only"})
	}
	return nil
}

// ArchivesFolder is where the dashboard's exports of the workspace go on
// this computer: inside MetaRush's own folder, private to this account.
func ArchivesFolder(a *app.App) string {
	if a.Cfg == nil || a.Cfg.Home() == "" {
		return ""
	}
	return filepath.Join(a.Cfg.Home(), "archives")
}

func exportsState(a *app.App) string {
	if a.Cfg == nil || a.Cfg.Home() == "" {
		return ""
	}
	return filepath.Join(a.Cfg.StateDir(), "exports")
}

var exporting struct {
	sync.Mutex
	running map[string]bool // export IDs this process is making
}

func workspaceExport(ctx context.Context, a *app.App, in *WorkspaceExportIn) (*WorkspaceExportOut, error) {
	if err := archivePeopleOnly(a); err != nil {
		return nil, err
	}
	media := strings.TrimSpace(in.Media)
	if media == "" {
		media = wsarchive.MediaReferenced
	}
	if media != wsarchive.MediaReferenced && media != wsarchive.MediaIncluded {
		return nil, invalid("The footage is either referenced or included.")
	}
	to, tar := strings.TrimSpace(in.To), in.Tar
	if CallerFrom(ctx).Remote {
		if to != "" {
			return nil, apperr.New(apperr.InvalidInput, "From the dashboard the archive goes into MetaRush's archives folder on the computer it runs on.").
				WithDetails(map[string]any{"folder": ArchivesFolder(a)})
		}
		dir := ArchivesFolder(a)
		if dir == "" {
			return nil, apperr.New(apperr.NotConfigured, "MetaRush has no folder for exports on this computer.")
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
		to, tar = freeName(dir, a.Cfg.Workspace.Name), true
	}
	if to == "" {
		return nil, invalid("Say where to write the archive (a new folder, or a file with tar).")
	}
	if strings.HasPrefix(to, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			to = filepath.Join(h, to[2:])
		}
	}
	opts := exportOptions(ctx, a, to, tar, media)
	if in.Plan {
		p, err := wsarchive.MakePlan(ctx, opts)
		if err != nil {
			return nil, err
		}
		return &WorkspaceExportOut{Plan: p, Holds: ArchiveHolds, Summary: planSummary(p, media)}, nil
	}
	dest := to
	if tar {
		dest = wsarchive.TarPath(to)
	}
	if err := wsarchive.CheckDestination(dest); err != nil {
		return nil, err
	}
	abs, _ := filepath.Abs(dest)
	now := time.Now().UTC()
	rec := &ExportRecord{ID: ids.New("exp"), State: "running", Path: abs, Name: filepath.Base(abs), Media: media, Tar: tar, Workspace: a.WorkspaceID(),
		By: a.Actor.UserName, StartedAt: now, UpdatedAt: now, PID: os.Getpid(), Says: "Starting the export."}
	busy := apperr.New(apperr.Conflict, "An export is being made already on this computer.").WithHint("Wait for it to finish.")
	for _, r := range listExports(a) { // another MetaRush on this computer making one
		if r.State == "running" {
			return nil, busy
		}
	}
	exporting.Lock()
	if exporting.running == nil {
		exporting.running = map[string]bool{}
	}
	if len(exporting.running) > 0 {
		exporting.Unlock()
		return nil, busy
	}
	exporting.running[rec.ID] = true
	exporting.Unlock()
	saveExport(a, rec)
	run := func(ctx context.Context) (*wsarchive.Result, error) {
		defer func() {
			exporting.Lock()
			delete(exporting.running, rec.ID)
			exporting.Unlock()
		}()
		last := time.Time{}
		opts.Progress = func(p wsarchive.ArchiveProgress) {
			rec.Progress = p
			rec.Says = progressWords(p)
			if time.Since(last) > 2*time.Second {
				last = time.Now()
				saveExport(a, rec)
			}
		}
		res, err := wsarchive.Export(ctx, opts)
		fin := time.Now().UTC()
		rec.FinishedAt = &fin
		if err != nil {
			rec.State, rec.Error = "failed", apperr.From(err).Message
			rec.Says = rec.Error
			saveExport(a, rec)
			return nil, err
		}
		fillDone(rec, res)
		saveExport(a, rec)
		a.Audit(ctx, "workspace.export", "workspace", a.WorkspaceID(), "", map[string]any{"archive": rec.Name, "media": media, "tar": tar,
			"documents": rec.Documents, "mediaFiles": rec.MediaFiles, "bytes": rec.Bytes, "level": rec.Level})
		return res, nil
	}
	if in.Background {
		go func() { _, _ = run(context.WithoutCancel(ctx)) }()
		return &WorkspaceExportOut{Export: rec, Holds: ArchiveHolds, Summary: "Making the archive now; Settings → Storage shows how far it is."}, nil
	}
	if _, err := run(ctx); err != nil {
		return nil, err
	}
	return &WorkspaceExportOut{Export: rec, Holds: ArchiveHolds, Summary: rec.Says, Next: []string{RestoreWords}}, nil
}

// exportOptions are the export's options for this App: its database (read
// only), its workspace, its storage and this computer's copies.
func exportOptions(ctx context.Context, a *app.App, to string, tar bool, media string) wsarchive.ExportOptions {
	// Every key this computer holds stays out: the configured ones, the
	// providers' and the abilities' (by the names the workspace's abilities
	// read them under), the publishing client's and people's sign-ins, and any
	// this process has read; the CLI adds another
	// database's address and its storage's key.
	extra, _ := ctx.Value(exportSecretsKey{}).([]string)
	extra = append(extra, logbook.KnownSecrets(a.Cfg, abilityKeyNames(ctx, a)...)...)
	extra = append(extra, publish.Secrets(a.Cfg, workspacePeople(ctx, a)...)...)
	b := version.Current()
	machine := ""
	if a.Cfg != nil {
		machine = a.Cfg.Installation.Name
	}
	if machine == "" {
		machine = app.Hostname()
	}
	o := wsarchive.ExportOptions{DB: a.DB, Workspace: a.WorkspaceID(), To: to, Tar: tar, Media: media, Store: a.Media, Cache: a.Cache,
		Scrub: wsarchive.NewScrubber(extra...), MadeBy: wsarchive.ArchiveMadeBy{MetaRush: b.Version, Commit: b.Commit, Machine: machine, Person: a.Actor.UserName}}
	if a.Media != nil && a.Cfg != nil {
		o.StoreWords = StorageWords(a.Cfg.Media)
	}
	if CallerFrom(ctx).Remote && a.Cfg != nil {
		o.Here = a.Cfg.Installation.ID // this computer's own MetaRush is the one exporting
	}
	if st := exportsState(a); st != "" {
		o.StateDir = filepath.Join(st, "copy")
	}
	if a.Cfg != nil && a.Cfg.Scratch.Dir != "" {
		o.ScratchDir = filepath.Join(a.Cfg.Scratch.Dir, "exports")
	}
	return o
}

// abilityKeyNames are the names of the keys the workspace's abilities read
// (read only).
func abilityKeyNames(ctx context.Context, a *app.App) []string {
	if a.DB == nil {
		return nil
	}
	var names []string
	if err := a.DB.C(store.CAbilities).Distinct(ctx, "facts.keys.name", bson.M{"workspaceId": a.WorkspaceID()}).Decode(&names); err != nil {
		return nil
	}
	return names
}

// workspacePeople are the IDs of the workspace's people (whose publishing
// sign-ins this computer may keep).
func workspacePeople(ctx context.Context, a *app.App) []string {
	if a.DB == nil {
		return nil
	}
	var ids []string
	if err := a.DB.C(store.CUsers).Distinct(ctx, "_id", bson.M{"workspaceId": a.WorkspaceID()}).Decode(&ids); err != nil {
		return nil
	}
	return ids
}

// freeName is a new archive's name in the exports folder: the workspace's
// name and the time, never one that is there already.
func freeName(dir, workspace string) string {
	base := slug(workspace, 40)
	if base == "" {
		base = "workspace"
	}
	base += "-" + time.Now().Format("2006-01-02-1504")
	p := filepath.Join(dir, base+".tar")
	for i := 2; ; i++ {
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Lstat(p + ".partial"); errors.Is(err, os.ErrNotExist) {
				return p
			}
		}
		p = filepath.Join(dir, fmt.Sprintf("%s-%d.tar", base, i))
	}
}

func fillDone(rec *ExportRecord, res *wsarchive.Result) {
	m := res.Manifest
	rec.State, rec.Bytes = "done", res.Bytes
	rec.Collections = len(m.Collections)
	for _, c := range m.Collections {
		rec.Documents += c.Count
	}
	rec.MediaFiles, rec.MediaBytes = m.Media.Files, m.Media.Bytes
	rec.Level, rec.Older = m.Level.Highest, m.Level.Older
	for _, l := range m.LeftOut {
		if l.Count > 0 || l.Unknown {
			rec.LeftOut = append(rec.LeftOut, l.Name)
		}
	}
	rec.Notes = m.Notes
	rec.Progress = wsarchive.ArchiveProgress{Phase: "done", Collections: len(m.Collections), CollectionsDone: len(m.Collections), Docs: rec.Documents}
	footage := "the footage listed by where it is kept"
	if m.Media.Mode == wsarchive.MediaIncluded {
		footage = fmt.Sprintf("%s of footage (%s)", countNoun(m.Media.Files, "file"), humanBytes(m.Media.Bytes))
	}
	rec.Says = fmt.Sprintf("The archive is ready: %s and %s, %s in all.", countNoun(int(rec.Documents), "record"), footage, humanBytes(rec.Bytes))
	if m.Scrubbed.Says != "" {
		rec.Scrubbed = m.Scrubbed
		rec.Says += " " + m.Scrubbed.Says
	}
}

// progressWords says how far an export is.
func progressWords(p wsarchive.ArchiveProgress) string {
	switch p.Phase {
	case "checking":
		return "Looking at the workspace."
	case "documents":
		return fmt.Sprintf("Writing the records (%d of %d kinds).", min(p.CollectionsDone+1, max(p.Collections, 1)), max(p.Collections, 1))
	case "media":
		if p.Files > 0 {
			return fmt.Sprintf("Copying the footage: %s of %s files.", thousands(p.FilesDone), thousands(p.Files))
		}
		return "Listing the footage."
	case "packing":
		if p.Bytes > 0 {
			return fmt.Sprintf("Putting it in one file: %d%%.", int(100*p.BytesDone/max(p.Bytes, 1)))
		}
		return "Putting it in one file."
	}
	return "Writing the archive."
}

func planSummary(p *wsarchive.ExportPlan, media string) string {
	var n int64
	for _, c := range p.Collections {
		n += c.Count
	}
	footage := fmt.Sprintf("the footage (%s, %s) listed by where it is kept", countNoun(p.Media.Files, "file"), humanBytes(p.Media.Bytes))
	if media == wsarchive.MediaIncluded {
		footage = fmt.Sprintf("%s of footage copied in (%s)", countNoun(p.Media.Files, "file"), humanBytes(p.Media.Bytes))
	}
	return fmt.Sprintf("The archive of “%s” would hold %s and %s: about %s.", p.Workspace.Name, countNoun(int(n), "record"), footage, humanBytes(p.Bytes))
}

// saveExport keeps the record in this computer's state (0600).
func saveExport(a *app.App, rec *ExportRecord) {
	dir := exportsState(a)
	if dir == "" {
		return
	}
	rec.UpdatedAt = time.Now().UTC()
	if os.MkdirAll(dir, 0o700) != nil {
		return
	}
	b, err := json.MarshalIndent(exportFile{ExportRecord: *rec, PID: rec.PID}, "", "  ")
	if err != nil {
		return
	}
	p := filepath.Join(dir, rec.ID+".json")
	if os.WriteFile(p+".tmp", b, 0o600) == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

// listExports reads the records, newest first; one whose process is gone is stopped.
func listExports(a *app.App) []ExportRecord {
	dir := exportsState(a)
	out := []ExportRecord{}
	if dir == "" {
		return out
	}
	files, _ := filepath.Glob(filepath.Join(dir, "exp_*.json"))
	exporting.Lock()
	mine := map[string]bool{}
	for id := range exporting.running {
		mine[id] = true
	}
	exporting.Unlock()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var ef exportFile
		if json.Unmarshal(b, &ef) != nil {
			continue
		}
		r := ef.ExportRecord
		if r.State == "running" && !mine[r.ID] && !alive(ef.PID) {
			r.State = "stopped"
			r.Says = "MetaRush stopped while making this archive. Export again: footage already copied isn't copied twice."
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].StartedAt.After(out[j].StartedAt) })
	if len(out) > 20 {
		out = out[:20]
	}
	return out
}
