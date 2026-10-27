package ops

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/service"
	"github.com/metavert-io/metarush/internal/storagemove"
	"github.com/metavert-io/metarush/internal/store"
)

// Moving the workspace's media to another store (D94): `metarush storage
// migrate --to <file>`. storage.plan is the dry run; storage.migrate copies
// everything (internal/storagemove: resumable, every file checked by SHA-256),
// switches this machine's config.yaml and the workspace's record only once all
// of it is verified, checks the switch the way MetaRush will load it (and puts
// the old settings back if it does not work), then copies anything the
// running dashboard added to the old store before it restarted. Nothing is
// ever deleted, and the old store is only read.

func init() {
	Def("storage.plan", "Check what moving this workspace's media to another storage would copy (a dry run: it lists both storages and changes nothing)",
		Flags{LocalOnly: true}, StoragePlan)
	Def("storage.migrate", "Move this workspace's media to another storage (S3-compatible, Google Cloud Storage or a folder): copy every file resumably, each checked by SHA-256, then switch this machine and the workspace to it; the old storage is left as it was; owners",
		Flags{Mutating: true, HumanOnly: true, LocalOnly: true, OwnerOnly: true}, StorageMigrate)
}

// StoragePlanIn names the new storage.
type StoragePlanIn struct {
	To string `json:"to" jsonschema:"description=A YAML or JSON file describing the new storage: the keys of config.yaml's media section (provider: filesystem\\, s3 or gcs\\, then that section)\\, at the top level or under media:. Secrets only as ${VARIABLE} or keychain:<name>."`
}

// StorageMigrateIn moves the media.
type StorageMigrateIn struct {
	To       string `json:"to" jsonschema:"description=A YAML or JSON file describing the new storage (see storage.plan)."`
	ReadBack bool   `json:"readBack,omitempty" jsonschema:"description=Also download every file from the new storage and check its SHA-256 (slower; the new storage may charge for the downloads)."`
}

// StorageSide is one storage in a move.
type StorageSide struct {
	Name     string `json:"name" jsonschema:"description=The storage in words (Google Cloud Storage bucket media)."`
	Provider string `json:"provider" jsonschema:"enum=filesystem,enum=s3,enum=gcs"`
	Locator  string `json:"locator" jsonschema:"description=Where it is\\, without secrets (gs://bucket/prefix\\, s3://bucket/prefix@host\\, fs:/folder)."`
	Shared   bool   `json:"shared" jsonschema:"description=Every machine in the workspace can reach it."`
}

// StoragePlanOut is what a move would do.
type StoragePlanOut struct {
	From StorageSide `json:"from"`
	To   StorageSide `json:"to"`
	// CatchUp: this machine already uses the new storage; a move copies only
	// what was added to the old one since it switched.
	CatchUp bool `json:"catchUp"`
	// Nothing: this machine already uses the new storage and there is no
	// earlier storage to catch up from.
	Nothing      bool     `json:"nothing"`
	Files        int      `json:"files" jsonschema:"description=Files in the old storage."`
	Bytes        int64    `json:"bytes"`
	Present      int      `json:"present" jsonschema:"description=Already in the new storage (same size)."`
	PresentBytes int64    `json:"presentBytes"`
	ToCopy       int      `json:"toCopy"`
	ToCopyBytes  int64    `json:"toCopyBytes"`
	Local        int      `json:"local" jsonschema:"description=Of the files to copy\\, those already on this machine (not downloaded)."`
	LocalBytes   int64    `json:"localBytes"`
	Download     int64    `json:"download" jsonschema:"description=Bytes to download from the old storage."`
	Differ       []string `json:"differ" jsonschema:"description=Keys the new storage already holds with another size (never overwritten; the move cannot finish until they are removed there)."`
	Machines     []string `json:"machines" jsonschema:"description=Other machines in this workspace: each switches with its own run of the move."`
	Overrides    []string `json:"overrides" jsonschema:"description=Environment variables that would override the new settings in config.yaml."`
	Notes        []string `json:"notes"`
	Summary      string   `json:"summary"`
}

// StorageProblem is a file the move could not take across.
type StorageProblem struct {
	Key    string `json:"key"`
	Reason string `json:"reason" jsonschema:"enum=damaged,enum=differs,enum=failed"`
	Detail string `json:"detail,omitempty"`
}

// StorageMigrateOut is what a move did.
type StorageMigrateOut struct {
	From         StorageSide      `json:"from"`
	To           StorageSide      `json:"to"`
	CatchUp      bool             `json:"catchUp"`
	Nothing      bool             `json:"nothing"`
	Files        int              `json:"files"`
	Bytes        int64            `json:"bytes"`
	Copied       int              `json:"copied" jsonschema:"description=Copied and checked by this move (this run and earlier runs of it)."`
	CopiedBytes  int64            `json:"copiedBytes"`
	CopiedNow    int              `json:"copiedNow"`
	Present      int              `json:"present" jsonschema:"description=Already in the new storage before the move."`
	PresentBytes int64            `json:"presentBytes"`
	ReadBack     int              `json:"readBack"`
	Downloaded   int64            `json:"downloaded"`
	Problems     []StorageProblem `json:"problems"`
	Verified     bool             `json:"verified" jsonschema:"description=Every file of the old storage is in the new one\\, checked."`
	Switched     bool             `json:"switched" jsonschema:"description=This machine now uses the new storage."`
	Workspace    bool             `json:"workspace" jsonschema:"description=The workspace names the new storage (joining machines check it)."`
	Restarted    bool             `json:"restarted" jsonschema:"description=The dashboard running on this machine restarted to use the new storage."`
	JobID        string           `json:"jobId,omitempty"`
	Seconds      float64          `json:"seconds"`
	// ReviewLinks are the review links that ended because their pages were
	// kept in the storage the media left (D318).
	ReviewLinks []string `json:"reviewLinks"`
	Next        []string `json:"next"`
	Summary     string   `json:"summary"`
}

// storageTarget is the new storage, loaded from its description file.
type storageTarget struct {
	file  *config.MediaFile
	store media.Store
	side  StorageSide
}

func loadStorageTarget(a *app.App, path string, dryRun bool) (*storageTarget, error) {
	if strings.TrimSpace(path) == "" {
		return nil, invalid("Say which storage to move to: a file that describes it (--to).")
	}
	if strings.HasPrefix(path, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(h, path[2:])
		}
	}
	f, err := config.LoadMediaFile(a.Cfg.Home(), path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, apperr.New(apperr.InvalidInput, "There is no storage description at %s.", path)
		}
		return nil, apperr.New(apperr.InvalidInput, "The storage description cannot be used: %v.", err).
			WithHint("The deployment guide (Moving to another storage) has examples.")
	}
	var st media.Store
	if dryRun && f.Media.Provider == "filesystem" {
		// A dry run creates nothing, not even the new folder (a missing one lists as empty).
		root, err := filepath.Abs(f.Media.Filesystem.Root)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "The storage description cannot be used: %v.", err)
		}
		st = &media.FS{Root: root, Shared: f.Media.Filesystem.Shared}
	} else if st, err = app.NewMediaStoreFor(f.Media); err != nil {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush cannot use the new storage: %v.", err)
	}
	if _, ok := st.(media.Lister); !ok {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush can't list the files in that storage (%s).", st.Name())
	}
	return &storageTarget{file: f, store: st, side: storageSideOf(f.Media, st)}, nil
}

// storageSideOf describes a store for people.
func storageSideOf(m config.Media, st media.Store) StorageSide {
	return StorageSide{Name: StorageWords(m), Provider: st.Name(), Locator: st.Locator(), Shared: st.Capabilities().Shared}
}

// StorageWords names a store in plain words: "Google Cloud Storage bucket media".
func StorageWords(m config.Media) string {
	switch m.Provider {
	case "s3":
		svc := map[string]string{"tigris": "Tigris", "r2": "Cloudflare R2", "b2": "Backblaze B2", "aws": "Amazon S3", "gcs": "Google Cloud Storage"}[m.S3.Preset]
		if svc == "" {
			host := m.S3.Endpoint
			if u, err := url.Parse(m.S3.Endpoint); err == nil && u.Host != "" {
				host = u.Host
			}
			switch {
			case strings.Contains(host, "tigris.dev"):
				svc = "Tigris"
			case strings.Contains(host, "r2.cloudflarestorage.com"):
				svc = "Cloudflare R2"
			case strings.Contains(host, "backblazeb2.com"):
				svc = "Backblaze B2"
			case host == "" || strings.Contains(host, "amazonaws.com"):
				svc = "Amazon S3"
			case strings.Contains(host, "storage.googleapis.com"):
				svc = "Google Cloud Storage"
			default:
				svc = "S3 storage"
			}
		}
		return svc + " bucket " + m.S3.Bucket
	case "gcs":
		return "Google Cloud Storage bucket " + m.GCS.Bucket
	}
	name := filepath.Base(filepath.Clean(m.Filesystem.Root))
	if m.Filesystem.Shared {
		return "the shared folder " + name
	}
	return "the folder " + name + " on this computer"
}

// moveRecord remembers a finished switch, so a later run on this machine
// (after the dashboard restarted, or to pick up stragglers) can still read the
// storage it used before.
type moveRecord struct {
	From StorageSide `json:"from"`
	// FromNode is the old media section as written in config.yaml (secrets
	// only as references; kept only when it has no secret in it).
	FromNode string      `json:"fromNode,omitempty"`
	To       StorageSide `json:"to"`
	Switched time.Time   `json:"switched"`
}

func moveDir(a *app.App) string { return filepath.Join(a.Cfg.StateDir(), "storage-move") }

func readLastMove(a *app.App) (moveRecord, bool) {
	var r moveRecord
	b, err := os.ReadFile(filepath.Join(moveDir(a), "last.json"))
	if err != nil || json.Unmarshal(b, &r) != nil {
		return r, false
	}
	return r, true
}

func writeLastMove(a *app.App, r moveRecord) {
	_ = os.MkdirAll(moveDir(a), 0o700)
	b, _ := json.MarshalIndent(r, "", "  ")
	p := filepath.Join(moveDir(a), "last.json")
	if os.WriteFile(p+".tmp", b, 0o600) == nil {
		_ = os.Rename(p+".tmp", p)
	}
}

// moveSource is where the media comes from: the storage this machine uses;
// or, when it already uses the new one, the storage it used before (nil when
// there is none).
func moveSource(a *app.App, t *storageTarget) (media.Store, StorageSide, bool, error) {
	if a.Media.Locator() != t.store.Locator() {
		return a.Media, storageSideOf(a.Cfg.Media, a.Media), false, nil
	}
	rec, ok := readLastMove(a)
	if !ok || rec.To.Locator != t.store.Locator() || rec.FromNode == "" {
		return nil, StorageSide{}, false, nil
	}
	m, err := config.ParseMediaNode(a.Cfg.Home(), rec.FromNode)
	if err != nil {
		return nil, StorageSide{}, false, apperr.New(apperr.NotConfigured, "MetaRush cannot reach the storage it used before: %v.", err)
	}
	st, err := app.NewMediaStoreFor(m)
	if err != nil {
		return nil, StorageSide{}, false, apperr.New(apperr.NotConfigured, "MetaRush cannot reach the storage it used before: %v.", err)
	}
	if _, ok := st.(media.Lister); !ok {
		return nil, StorageSide{}, false, apperr.New(apperr.NotConfigured, "MetaRush can't list the files in the storage it used before (%s).", st.Name())
	}
	return st, storageSideOf(m, st), true, nil
}

// otherMachines names the workspace's other machines that are not removed.
func otherMachines(ctx context.Context, a *app.App) []string {
	var insts []models.Installation
	_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"_id": bson.M{"$ne": a.Cfg.Installation.ID}, "state": bson.M{"$ne": models.InstallationRevoked}}, &insts)
	out := []string{}
	for _, in := range insts {
		out = append(out, firstNonEmpty(in.Name, in.Hostname, in.ID))
	}
	return out
}

func moveOptions(a *app.App, src media.Store, t *storageTarget, readBack bool) storagemove.Options {
	id := storagemove.ID(a.WorkspaceID(), src.Locator(), t.store.Locator())
	return storagemove.Options{Workspace: a.WorkspaceID(), From: src, To: t.store, Cache: a.Cache,
		StateDir: filepath.Join(moveDir(a), id), ScratchDir: filepath.Join(a.Cfg.Scratch.Dir, "storage-move", id), ReadBack: readBack}
}

// StoragePlan is storage.plan.
func StoragePlan(ctx context.Context, a *app.App, in *StoragePlanIn) (*StoragePlanOut, error) {
	t, err := loadStorageTarget(a, in.To, true)
	if err != nil {
		return nil, err
	}
	src, side, catchUp, err := moveSource(a, t)
	if err != nil {
		return nil, err
	}
	if LeaveStorage != nil { // a client whose things may not be in cloud storage (D290)
		if err := LeaveStorage(ctx, a, t.file.Media); err != nil {
			return nil, err
		}
	}
	out := &StoragePlanOut{To: t.side, Differ: []string{}, Machines: otherMachines(ctx, a), Overrides: config.MediaOverrides(t.file.Media), Notes: []string{}}
	if src == nil {
		out.From, out.Nothing = t.side, true
		out.Summary = "MetaRush already uses " + t.side.Name + ". There is nothing to move."
		return out, nil
	}
	out.From, out.CatchUp = side, catchUp
	s, err := storagemove.Plan(ctx, moveOptions(a, src, t, false))
	if err != nil {
		return nil, apperr.New(apperr.Storage, "MetaRush could not look at both storages: %v.", err)
	}
	out.Files, out.Bytes, out.Present, out.PresentBytes = s.Files, s.Bytes, s.Present, s.PresentBytes
	out.ToCopy, out.ToCopyBytes, out.Local, out.LocalBytes = s.ToCopy, s.ToCopyBytes, s.Local, s.LocalBytes
	out.Download, out.Differ = s.ToCopyBytes-s.LocalBytes, s.Differ
	switch {
	case catchUp && s.ToCopy == 0:
		out.Summary = fmt.Sprintf("This computer already uses %s, and nothing was added to %s since.", t.side.Name, side.Name)
	case catchUp:
		out.Summary = fmt.Sprintf("This computer already uses %s. Moving would copy %s (%s) added to %s since.", t.side.Name, countNoun(s.ToCopy, "file"), humanBytes(s.ToCopyBytes), side.Name)
	case s.ToCopy == 0:
		out.Summary = fmt.Sprintf("Everything is in %s already (%s). Moving would only check it and switch.", t.side.Name, countNoun(s.Files, "file"))
	default:
		out.Summary = fmt.Sprintf("Moving to %s would copy %s (%s).", t.side.Name, countNoun(s.ToCopy, "file"), humanBytes(s.ToCopyBytes))
	}
	if s.Present > 0 && s.ToCopy > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%s there already.", isAre(s.Present)))
	}
	if s.Local > 0 && side.Provider != "filesystem" {
		out.Notes = append(out.Notes, fmt.Sprintf("%s on this computer already, so only %s is downloaded from %s.", isAre(s.Local), humanBytes(out.Download), side.Name))
	}
	if len(s.Differ) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("%s in %s with different contents. MetaRush never overwrites them, so the move cannot finish until they are removed there.", isAre(len(s.Differ)), t.side.Name))
	}
	if len(out.Overrides) > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf("Settings in this computer's environment (%s) override config.yaml, so MetaRush could not switch to the new storage. Remove them first.", strings.Join(out.Overrides, ", ")))
	}
	if len(out.Machines) > 0 && !catchUp {
		out.Notes = append(out.Notes, fmt.Sprintf("Other machines in this workspace (%s) keep using %s until each one runs this move too, with its own keys.", strings.Join(out.Machines, ", "), side.Name))
	}
	if ReviewLinksOnMove != nil && !catchUp && a.Media.Locator() != t.store.Locator() { // D318
		if lines, err := ReviewLinksOnMove(ctx, a, a.Media, true); err == nil {
			out.Notes = append(out.Notes, lines...)
		}
	}
	return out, nil
}

// isAre is "1 file is" / "3 files are".
func isAre(n int) string {
	if n == 1 {
		return "1 file is"
	}
	return fmt.Sprintf("%s files are", thousands(n))
}

// Plural is "1 file" / "1,204 files".
func countNoun(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return thousands(n) + " " + noun + "s"
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// StorageMoveProgress says how far a move is, in words.
func StorageMoveProgress(p storagemove.Progress) string {
	switch p.Phase {
	case "listing":
		return "Looking at both storages"
	case "checking":
		return "Checking the new storage"
	}
	return fmt.Sprintf("Copying %s of %s files", thousands(min(p.FilesDone+1, p.Files)), thousands(p.Files))
}

// StorageMigrate is storage.migrate.
func StorageMigrate(ctx context.Context, a *app.App, in *StorageMigrateIn) (*StorageMigrateOut, error) {
	started := time.Now()
	t, err := loadStorageTarget(a, in.To, false)
	if err != nil {
		return nil, err
	}
	src, side, catchUp, err := moveSource(a, t)
	if err != nil {
		return nil, err
	}
	if LeaveStorage != nil { // a client whose things may not be in cloud storage (D290)
		if err := LeaveStorage(ctx, a, t.file.Media); err != nil {
			return nil, err
		}
	}
	out := &StorageMigrateOut{To: t.side, Problems: []StorageProblem{}, Next: []string{}, ReviewLinks: []string{}}
	if src == nil {
		// Nothing was moved here, so the workspace's record is left as it is.
		out.From, out.Nothing, out.Switched = t.side, true, true
		out.Workspace = workspaceNames(ctx, a, t.store)
		out.Summary = "MetaRush already uses " + t.side.Name + ". There is nothing to move."
		return out, nil
	}
	out.From, out.CatchUp = side, catchUp
	// Settings in the environment beat config.yaml: the switch would not
	// hold. (A different S3 secret is checked by the switch itself.)
	if ov := blockingOverrides(t.file.Media); len(ov) > 0 {
		return nil, apperr.New(apperr.Conflict, "Settings in this computer's environment (%s) override config.yaml, so MetaRush could not switch to the new storage.", strings.Join(ov, ", ")).
			WithHint("Remove them from your shell or from the .env file in MetaRush's folder, then run the move again.")
	}
	unlock, err := lockMove(a)
	if err != nil {
		return nil, err
	}
	defer unlock()
	mctx, cancel := context.WithCancel(ctx)
	defer cancel()
	job := startMoveJob(mctx, a, side, t.side, in.ReadBack, cancel)
	progress := progressFrom(ctx)
	opts := moveOptions(a, src, t, in.ReadBack)
	opts.Progress = func(p storagemove.Progress) {
		job.update(p)
		progress(moveLabel(p, false), p.BytesDone, p.Bytes)
	}
	res, err := storagemove.Run(mctx, opts)
	if res != nil {
		fillMove(out, res)
	}
	if err != nil {
		err = moveError(err, job.canceled.Load())
		job.finish(ctx, nil, err)
		return nil, err
	}
	if !res.Verified {
		err := unverified(out)
		job.finish(ctx, nil, err)
		return nil, err
	}
	// Review links whose files are in the storage being left are dealt with
	// last, once nothing else can stop the move (D318): afterwards nothing
	// here could take them back or read them.
	if ReviewLinksOnMove != nil {
		lines, err := ReviewLinksOnMove(ctx, a, src, false)
		out.ReviewLinks = append(out.ReviewLinks, lines...)
		if err != nil {
			job.finish(ctx, nil, err)
			return nil, err
		}
	}
	switchedAt := time.Now().UTC()
	if !catchUp {
		old, err := switchStorage(ctx, a, t, res.Sample)
		if err != nil {
			job.finish(ctx, nil, err)
			return nil, err
		}
		rec := moveRecord{From: side, To: t.side, Switched: switchedAt}
		if old != nil && config.RefsOnly(old) {
			rec.FromNode, _ = config.NodeYAML(old)
		}
		writeLastMove(a, rec)
		a.Audit(ctx, "storage.migrate", "workspace", a.WorkspaceID(), "", map[string]any{"from": side.Locator, "to": t.side.Locator, "files": res.Files, "copied": res.Copied, "readBack": in.ReadBack})
	}
	out.Switched = true
	out.Workspace = ensureWorkspaceStore(ctx, a, t.store)
	daemonStuck := false
	if !catchUp {
		// The dashboard follows config.yaml and starts again with the new
		// storage; then copy what it added to the old one before that.
		running, restarted := waitForDaemonRestart(ctx, a.Cfg.Home(), switchedAt)
		out.Restarted, daemonStuck = restarted, running && !restarted
		// A review link made in the old storage after its links were dealt
		// with (a dashboard that hadn't switched yet) ends too.
		if ReviewLinksOnMove != nil {
			lines, err := ReviewLinksOnMove(ctx, a, src, false)
			out.ReviewLinks = append(out.ReviewLinks, lines...)
			if err != nil {
				out.Next = append(out.Next, "A review link made during the move couldn't be cleaned up in "+side.Name+": "+apperr.From(err).Message+
					" It may keep opening until its signature ends, at most 7 days.")
			}
		}
		opts.Progress = func(p storagemove.Progress) {
			job.update(p)
			progress(moveLabel(p, true), p.BytesDone, p.Bytes)
		}
		more, err := storagemove.Run(mctx, opts)
		if more != nil {
			out.Files, out.Bytes = more.Files, more.Bytes
			out.Copied, out.CopiedBytes, out.CopiedNow = more.Copied, more.CopiedBytes, out.CopiedNow+more.CopiedNow
			out.Downloaded += more.Downloaded
			out.Problems = problemsOf(more.Problems)
			out.Verified = more.Verified
		}
		if err == nil && !more.Verified {
			err = unverified(out)
		}
		if err != nil {
			// Switched, and all that was there before is in the new storage:
			// only what arrived meanwhile is missing. Running it again copies it.
			out.Next = append(out.Next, "Run the same move again: it copies what was added to "+side.Name+" while MetaRush switched.")
		}
	}
	out.Seconds = time.Since(started).Seconds()
	out.Summary, out.Next = moveSummary(out, in.ReadBack), append(out.Next, moveNext(out, otherMachines(ctx, a), daemonStuck)...)
	if n := len(out.ReviewLinks); n > 0 {
		out.Summary += fmt.Sprintf(" %s ended, as their pages were kept in %s.", plural(n, "review link"), side.Name)
	}
	job.finish(ctx, map[string]any{"summary": out.Summary, "copied": out.CopiedNow, "storage": t.side.Name}, nil)
	out.JobID = job.id()
	return out, nil
}

func fillMove(out *StorageMigrateOut, r *storagemove.Result) {
	out.Files, out.Bytes, out.Copied, out.CopiedBytes, out.CopiedNow = r.Files, r.Bytes, r.Copied, r.CopiedBytes, r.CopiedNow
	out.Present, out.PresentBytes, out.ReadBack, out.Downloaded, out.Verified = r.Present, r.PresentBytes, r.ReadBack, r.Downloaded, r.Verified
	out.Problems = problemsOf(r.Problems)
}

func problemsOf(ps []storagemove.Problem) []StorageProblem {
	out := []StorageProblem{}
	for _, p := range ps {
		out = append(out, StorageProblem{Key: p.Key, Reason: p.Reason, Detail: p.Detail})
	}
	return out
}

func moveLabel(p storagemove.Progress, after bool) string {
	switch {
	case p.Phase == "listing":
		return "Looking at both storages"
	case p.Phase == "checking":
		return "Checking the new storage"
	case after:
		return "Copying what was added meanwhile"
	}
	return fmt.Sprintf("Copying %s", countNoun(p.Files, "file"))
}

// blockingOverrides are environment settings that would point MetaRush
// elsewhere after the switch (the S3 secret is checked by the switch).
func blockingOverrides(m config.Media) []string {
	var out []string
	for _, v := range config.MediaOverrides(m) {
		if v != config.EnvPrefix+"S3_SECRET_ACCESS_KEY" {
			out = append(out, v)
		}
	}
	return out
}

func moveError(err error, canceled bool) error {
	if canceled || errors.Is(err, context.Canceled) {
		return apperr.New(apperr.Canceled, "Stopped. Run the same move again to continue where it stopped; the old storage is unchanged.")
	}
	if ae := apperr.From(err); ae.Code != apperr.Internal {
		return err
	}
	return apperr.New(apperr.Storage, "The move stopped: %v. Run it again to continue where it stopped; the old storage is unchanged.", err)
}

// unverified is the error of a move that copied what it could but found
// files it could not take across: nothing switches.
func unverified(out *StorageMigrateOut) error {
	damaged, differs, failed := 0, 0, 0
	for _, p := range out.Problems {
		switch p.Reason {
		case storagemove.Damaged:
			damaged++
		case storagemove.Differs:
			differs++
		default:
			failed++
		}
	}
	var parts []string
	if damaged > 0 {
		parts = append(parts, fmt.Sprintf("%s damaged in %s", isAre(damaged), out.From.Name))
	}
	if differs > 0 {
		parts = append(parts, fmt.Sprintf("%s in %s with different contents", isAre(differs), out.To.Name))
	}
	if failed > 0 {
		parts = append(parts, fmt.Sprintf("%s could not be copied", countNoun(failed, "file")))
	}
	code := apperr.Storage
	if damaged+differs > 0 {
		code = apperr.Integrity
	}
	hint := "Run the same move again: it continues where it stopped."
	if damaged+differs > 0 {
		hint = "See the problems with --details. MetaRush never overwrites or deletes files, so remove the differing ones from the new storage yourself."
	}
	return apperr.New(code, "MetaRush did not switch: %s. Everything else was copied and checked, and the old storage is unchanged.", strings.Join(parts, ", ")).
		WithHint(hint).WithDetails(map[string]any{"problems": out.Problems, "copied": out.Copied, "files": out.Files})
}

func moveSummary(out *StorageMigrateOut, readBack bool) string {
	check := "each one checked"
	if readBack {
		check = "each one checked and read back"
	}
	var s string
	switch {
	case out.CatchUp && out.CopiedNow == 0:
		s = fmt.Sprintf("Nothing new in %s: everything is in %s.", out.From.Name, out.To.Name)
	case out.CatchUp:
		s = fmt.Sprintf("Copied %s added to %s since the switch, %s.", countNoun(out.CopiedNow, "file"), out.From.Name, check)
	case out.Copied == 0:
		s = fmt.Sprintf("Everything was in %s already (%s), checked.", out.To.Name, countNoun(out.Files, "file"))
	default:
		s = fmt.Sprintf("Moved %s (%s) to %s, %s.", countNoun(out.Copied, "file"), humanBytes(out.CopiedBytes), out.To.Name, check)
	}
	if !out.CatchUp {
		s += " This computer and the workspace now use it. The old storage is unchanged; nothing was deleted."
	}
	return s
}

func moveNext(out *StorageMigrateOut, machines []string, daemonStuck bool) []string {
	var next []string
	if daemonStuck {
		next = append(next, "MetaRush is open on this computer and did not restart yet: quit it and start it again, then run the same move once more to copy anything it added meanwhile.")
	}
	if len(machines) > 0 {
		next = append(next, fmt.Sprintf("On each other machine (%s), run the same move with its own keys: it copies anything new and switches that machine.", strings.Join(machines, ", ")))
	}
	next = append(next, "Once every machine uses "+out.To.Name+" and everything looks right, you can remove "+out.From.Name+" yourself. MetaRush never deletes it.")
	return next
}

// workspaceNames reports whether the workspace's record names this store.
func workspaceNames(ctx context.Context, a *app.App, st media.Store) bool {
	var ws models.Workspace
	return a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws) == nil && ws.MediaStore.Locator == st.Locator()
}

// ensureWorkspaceStore makes the workspace name the new storage (joining
// machines and doctor check it). It reports whether the workspace names it now.
func ensureWorkspaceStore(ctx context.Context, a *app.App, st media.Store) bool {
	id := models.StoreIdentity{Provider: st.Name(), Locator: st.Locator(), Shared: st.Capabilities().Shared}
	var ws models.Workspace
	if err := a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws); err != nil {
		return false
	}
	if ws.MediaStore == id {
		return true
	}
	if err := a.DB.Update(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), bson.M{"$set": bson.M{"mediaStore": id}}); err != nil {
		return false
	}
	return true
}

// switchStorage points this installation's config.yaml at the new storage and
// checks it the way MetaRush will load it; when that fails, the old settings
// are put back. It returns the old media section as written.
func switchStorage(ctx context.Context, a *app.App, t *storageTarget, sample string) (*yaml.Node, error) {
	home := a.Cfg.Home()
	old, err := config.GetField(home, []string{"media"})
	if err != nil {
		return nil, err
	}
	if t.file.SecretVar != "" && a.Cfg.SecretMode() == "keychain" {
		// This installation keeps secrets in the keychain: so does the new storage's.
		name := "s3-secret-access-key-" + storagemove.ID(a.WorkspaceID(), "", t.store.Locator())[:8]
		ref, err := config.StoreSecret(home, name, t.file.Media.S3.SecretAccessKey)
		if err != nil {
			return nil, apperr.New(apperr.NotConfigured, "MetaRush could not keep the new storage's secret in the keychain: %v. Nothing switched.", err)
		}
		t.file.SetSecretRef(ref)
	}
	if err := config.SetField(home, []string{"media"}, t.file.Node); err != nil {
		return nil, err
	}
	if err := checkSwitched(ctx, home, t.store.Locator(), sample); err != nil {
		if old != nil {
			_ = config.SetField(home, []string{"media"}, old)
		}
		return nil, err
	}
	return old, nil
}

func checkSwitched(ctx context.Context, home, want, sample string) error {
	cfg, err := config.Load(home)
	if err != nil {
		return apperr.New(apperr.NotConfigured, "With the new storage, MetaRush's settings would not load (%v), so it kept the old storage.", err)
	}
	st, err := app.NewMediaStore(cfg)
	if err != nil {
		return apperr.New(apperr.NotConfigured, "With the new settings MetaRush could not open the new storage (%v), so it kept the old storage.", err)
	}
	if st.Locator() != want {
		return apperr.New(apperr.Conflict, "Settings in this computer's environment override config.yaml (%s), so MetaRush kept the old storage.", strings.Join(config.MediaOverrides(cfg.Media), ", ")).
			WithHint("Remove them, then run the move again: everything copied so far stays in the new storage.")
	}
	if sample != "" {
		if _, err := st.Stat(ctx, sample); err != nil {
			return apperr.New(apperr.Storage, "With the settings it would use, MetaRush could not read the new storage (%v), so it kept the old storage.", err).
				WithHint("Check the new storage's keys (an environment variable such as METARUSH_S3_SECRET_ACCESS_KEY overrides the one in the file), then run the move again.")
		}
	}
	return nil
}

// DaemonRestartWait is how long a move waits for this machine's running
// dashboard to start again with the new storage.
var DaemonRestartWait = 45 * time.Second

// waitForDaemonRestart waits for the dashboard running on this machine, if
// any, to start again with the new storage (it follows config.yaml).
func waitForDaemonRestart(ctx context.Context, home string, since time.Time) (running, restarted bool) {
	if _, ok := service.ReadDaemon(home); !ok {
		return false, false
	}
	deadline := time.Now().Add(DaemonRestartWait)
	for time.Now().Before(deadline) && ctx.Err() == nil {
		if d, ok := service.ReadDaemon(home); ok && d.StartedAt.After(since) {
			return true, true
		}
		time.Sleep(500 * time.Millisecond)
	}
	return true, false
}

// lockMove allows one move at a time on this machine.
func lockMove(a *app.App) (func(), error) {
	if err := os.MkdirAll(moveDir(a), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(moveDir(a), "lock"), os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		return nil, apperr.New(apperr.Conflict, "Another move of the media is running on this computer.").WithHint("Wait for it to finish, or stop it from the activity bar.")
	}
	return func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }, nil
}

/* ---------- the job the activity bar shows ---------- */

// moveJob records a move as a running job leased to this machine, so the
// activity bar shows "Copying 120 of 4,000 files" and a person can stop it.
type moveJob struct {
	a        *app.App
	job      *models.Job
	cancel   context.CancelFunc
	stop     chan struct{}
	mu       sync.Mutex
	prog     models.Progress
	lastBeat time.Time
	canceled atomic.Bool
	lost     atomic.Bool
}

func startMoveJob(ctx context.Context, a *app.App, from, to StorageSide, readBack bool, cancel context.CancelFunc) *moveJob {
	m := &moveJob{a: a, cancel: cancel, stop: make(chan struct{})}
	closeInterruptedMoves(ctx, a)
	w := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name}
	// Two attempts: this process is the first; if it stops, this machine's
	// worker takes the second only to close the job plainly.
	j, err := a.Queue.Start(ctx, jobs.EnqueueOptions{Type: jobs.TypeStorageMove, Actor: a.Actor, MaxAttempts: 2, Installation: a.Cfg.Installation.ID,
		Payload: map[string]any{"storage": to.Name, "from": from.Name, "readBack": readBack}}, w)
	if err != nil {
		return m // best effort: the move goes on without a record
	}
	m.job = j
	go func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-m.stop:
				return
			case <-tick.C:
				m.beat(ctx, true)
			}
		}
	}()
	return m
}

// closeInterruptedMoves closes this machine's moves whose process stopped
// (normally the worker does; this covers a machine without a running one).
func closeInterruptedMoves(ctx context.Context, a *app.App) {
	stale, err := a.Queue.List(ctx, bson.M{"type": jobs.TypeStorageMove, "state": models.JobRunning, "installation": a.Cfg.Installation.ID,
		"$expr": bson.M{"$lt": bson.A{"$lease.expiresAt", "$$NOW"}}}, 20) // the server's time, as leases are written
	if err != nil {
		return
	}
	w := jobs.Worker{ID: a.Cfg.Installation.ID, Name: a.Cfg.Installation.Name, JobTypes: []string{jobs.TypeStorageMove}}
	for _, s := range stale {
		if j, err := a.Queue.LeaseByID(ctx, w, s.ID); err == nil && j != nil {
			_ = a.Queue.Fail(ctx, j, w.ID, errors.New(storagemove.Interrupted), false)
		}
	}
}

func (m *moveJob) id() string {
	if m.job == nil {
		return ""
	}
	return m.job.ID
}

func (m *moveJob) update(p storagemove.Progress) {
	if m.job == nil {
		return
	}
	pct := 0.0
	if p.Bytes > 0 {
		pct = math.Max(0, math.Min(1, float64(p.BytesDone)/float64(p.Bytes)))
	}
	m.mu.Lock()
	newPhase := m.prog.Stage != p.Phase // a new phase shows at once
	m.prog = models.Progress{Pct: pct, Message: StorageMoveProgress(p), Stage: p.Phase, Done: p.BytesDone, Total: p.Bytes, Rate: p.Rate}
	m.mu.Unlock()
	m.beat(context.Background(), newPhase)
}

// beat keeps the lease alive and publishes progress (at most once a second unless forced).
func (m *moveJob) beat(ctx context.Context, force bool) {
	m.mu.Lock()
	if m.job == nil || (!force && time.Since(m.lastBeat) < time.Second) {
		m.mu.Unlock()
		return
	}
	m.lastBeat = time.Now()
	j, p := m.job, m.prog
	m.mu.Unlock()
	c, err := m.a.Queue.Heartbeat(context.WithoutCancel(ctx), j, m.a.Cfg.Installation.ID, p)
	switch {
	case errors.Is(err, jobs.ErrLostLease):
		m.lost.Store(true)
	case c:
		m.canceled.Store(true)
		m.cancel()
	}
}

func (m *moveJob) finish(ctx context.Context, result map[string]any, err error) {
	if m.job == nil {
		return
	}
	close(m.stop)
	if m.lost.Load() {
		return
	}
	a, owner, fctx := m.a, m.a.Cfg.Installation.ID, context.WithoutCancel(ctx)
	ae := apperr.From(err)
	switch {
	case err != nil && (m.canceled.Load() || ae.Code == apperr.Canceled):
		_ = a.Queue.Canceled(fctx, m.job, owner)
	case err != nil:
		_ = a.DB.Update(fctx, store.CJobs, a.WorkspaceID(), m.job.ID, bson.M{"$set": bson.M{"payload.errorCode": ae.Code}})
		_ = a.Queue.Fail(fctx, m.job, owner, errors.New(ae.Message), false)
	default:
		_ = a.Queue.Succeed(fctx, m.job, owner, result)
	}
}
