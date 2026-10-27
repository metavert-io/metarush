package remotion

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// A checkout is MetaRush's branch of a source as files Claude edits (D137):
// one version's files, .metarush/remotion.json (which source and version it
// started from) and a git repository whose one commit is that version, so
// `git diff` shows what changed. `remotion check` turns the folder into the
// next version. Instructions for coding assistants never come along (the
// import left them out), and public/ files only when asked (they are big and
// rarely edited; a check keeps them as they were).

// LockFile is the checkout's record, relative to the folder.
const LockFile = ".metarush/remotion.json"

// checkoutLock is what a checkout records.
type checkoutLock struct {
	Format      string `json:"format"`
	WorkspaceID string `json:"workspaceId"`
	SourceID    string `json:"sourceId"`
	Name        string `json:"name"`
	ProjectID   string `json:"projectId"`
	Base        string `json:"base"`
	BaseNumber  int    `json:"baseNumber"`
	// Public: the public/ files were written (else a check keeps the base's).
	Public bool `json:"public"`
}

const lockFormat = "metarush.remotion-checkout/1"

func readLock(dir string) (*checkoutLock, error) {
	b, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(LockFile)))
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "That folder isn't a checkout of a Remotion project.").
			WithHint("Write one first; then edit it and check it.").WithCommand("metarush remotion checkout <name> <folder>")
	}
	var l checkoutLock
	if err := json.Unmarshal(b, &l); err != nil || l.Format != lockFormat || l.SourceID == "" || !ValidHash(l.Base) {
		return nil, apperr.New(apperr.InvalidInput, "The folder's checkout record can't be read.").WithHint("Write a fresh checkout and copy your changes into it.")
	}
	return &l, nil
}

// CheckoutResult says what was written.
type CheckoutResult struct {
	Dir     string
	Files   int
	Version *models.RemotionVersion
	Source  *models.RemotionSource
}

// checkout writes a version into a folder.
func checkout(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, dir string, public, force bool) (*CheckoutResult, error) {
	dir, err := fullPath(dir)
	if err != nil {
		return nil, err
	}
	if entries, err := os.ReadDir(dir); err == nil && len(entries) > 0 {
		l, lerr := readLock(dir)
		switch {
		case lerr == nil && l.SourceID != s.ID:
			return nil, apperr.New(apperr.Conflict, "%s is a checkout of another Remotion project.", tildePath(dir)).WithHint("Pick another folder.")
		case lerr != nil && !force:
			return nil, apperr.New(apperr.Conflict, "%s isn't empty.", tildePath(dir)).WithHint("Pick a new folder (MetaRush writes into an empty one).")
		case lerr == nil && !force:
			return nil, apperr.New(apperr.Conflict, "%s already has a checkout of “%s” (version %d).", tildePath(dir), s.Title, l.BaseNumber).
				WithHint("Check your changes there first; writing it again over them loses them.").WithCommand("metarush remotion check " + dir)
		}
		// --force over a checkout of the same source: start again.
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(dir, e.Name()))
		}
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	n := 0
	for _, f := range m.Files {
		if isPublic(f.Path) && !public {
			continue
		}
		src, err := localObject(ctx, a, f.SHA256, f.Size)
		if err != nil {
			return nil, err
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := copyFile(src, dst); err != nil {
			return nil, err
		}
		n++
	}
	l := checkoutLock{Format: lockFormat, WorkspaceID: a.WorkspaceID(), SourceID: s.ID, Name: s.Name, ProjectID: s.ProjectID, Base: v.Hash, BaseNumber: v.Number, Public: public}
	if err := writeJSON(filepath.Join(dir, filepath.FromSlash(LockFile)), l); err != nil {
		return nil, err
	}
	gitCommit(ctx, a.Tools, dir, fmt.Sprintf("Version %d of %s (MetaRush)", v.Number, s.Title))
	return &CheckoutResult{Dir: dir, Files: n, Version: v, Source: s}, nil
}

// gitCommit makes the checkout a git repository whose commit is the version
// (best effort: without git, the checkout still works).
func gitCommit(ctx context.Context, ts *tools.Set, dir, msg string) {
	if ts == nil || !ts.Has(tools.Git) {
		return
	}
	run := func(args ...string) error {
		_, err := resources.Git(ctx, ts, dir, nil, nil, args...)
		return err
	}
	if run("init", "--quiet", "--initial-branch=metarush") != nil {
		return
	}
	excl := filepath.Join(dir, ".git", "info", "exclude")
	_ = os.MkdirAll(filepath.Dir(excl), 0o755)
	_ = os.WriteFile(excl, []byte(".metarush/\nnode_modules/\n.DS_Store\n"), 0o644)
	if run("add", "--all") != nil {
		return
	}
	_ = run("-c", "user.name=MetaRush", "-c", "user.email=metarush@localhost", "-c", "commit.gpgsign=false", "commit", "--quiet", "--no-verify", "-m", msg)
}

// isCheckout reports a folder that holds a checkout.
func isCheckout(dir string) bool {
	_, err := os.Stat(filepath.Join(dir, filepath.FromSlash(LockFile)))
	return err == nil
}

func trimSlash(s string) string { return strings.TrimRight(s, "/") }
