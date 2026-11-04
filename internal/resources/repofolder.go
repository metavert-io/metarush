package resources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/tools"
)

// One folder of a repository at one commit, for an ability a community
// catalogue lists (D485): fetched with the same rules as a repository
// brought in (D104) — git with hooks, prompts and every protocol but https
// off, only that commit, shallow, into a fresh bare repository — and read raw
// (git cat-file), so no filter, hook or attribute of the repository runs and
// nothing is checked out.

// FolderFile is a file of a folder at a commit. Data is read only for regular
// files; a link (mode 120000) or another repository (a submodule) comes
// without it, for the caller to refuse.
type FolderFile struct {
	// Path is inside the folder.
	Path string
	Mode string
	Size int64
	Data []byte
}

// Link reports a symbolic link; Submodule another repository.
func (f FolderFile) Link() bool      { return f.Mode == "120000" }
func (f FolderFile) Submodule() bool { return f.Mode == "160000" }

// Program reports a file marked as a program (executable).
func (f FolderFile) Program() bool { return f.Mode == "100755" }

// FolderLimits cap what FolderAt reads; Skip leaves a path inside the folder
// out before anything about it counts (hidden files, like a folder's).
type FolderLimits struct {
	Files      int
	FileBytes  int64
	TotalBytes int64
	Skip       func(rel string) bool
}

// FolderAt fetches one commit of a GitHub repository into a fresh bare
// repository under work and reads the files under folder ("" for its top).
// The commit is named in full; a server that won't hand it out fails plainly.
func FolderAt(ctx context.Context, ts *tools.Set, work, repoURL, commit, folder string, lim FolderLimits) ([]FolderFile, error) {
	r, ok := ParseRepoURL(repoURL)
	if !ok || r.Ref != "" || r.Path != "" {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a GitHub repository's address.")
	}
	if !fullSHA.MatchString(commit) {
		return nil, apperr.New(apperr.InvalidInput, "Name the commit in full (40 characters).")
	}
	folder = strings.Trim(folder, "/")
	if folder != "" && !safeRepoPath(folder) {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a plain folder in a repository.")
	}
	if !ts.Has(tools.Git) {
		return nil, apperr.New(apperr.ToolMissing, "Bringing in an ability from a repository needs git, which isn't on this computer. Settings → Components says how to add it.")
	}
	if (testing.Testing() || os.Getenv("METARUSH_NO_PAID_SERVICES") == "1") && gitBase() == "" {
		return nil, apperr.New(apperr.NotConfigured, "Tests never fetch from GitHub: set METARUSH_GIT_BASE to the test repositories.")
	}
	bare := filepath.Join(work, "repo.git")
	if _, err := git(ctx, ts, work, nil, nil, "init", "--bare", "--quiet", bare); err != nil {
		return nil, fmt.Errorf("MetaRush couldn't make a place to put the repository: %w", err)
	}
	r.Ref = commit
	if _, err := git(ctx, ts, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", cloneURL(r), commit); err != nil {
		return nil, apperr.From(gitProblem(r, err))
	}
	var head bytes.Buffer
	if _, err := git(ctx, ts, bare, nil, &head, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil || strings.TrimSpace(head.String()) != commit {
		return nil, apperr.New(apperr.NotFound, "%s/%s has no commit %s.", r.Owner, r.Name, commit[:12])
	}
	return filesAt(ctx, ts, bare, commit, folder, lim, r.Owner+"/"+r.Name)
}

// filesAt reads the files under folder at a commit of a repository on this
// computer (FolderAt's bare copy, or a maintainer's own checkout).
func filesAt(ctx context.Context, ts *tools.Set, repo, commit, folder string, lim FolderLimits, name string) ([]FolderFile, error) {
	var out bytes.Buffer
	args := []string{"ls-tree", "-r", "-l", "-z", "--full-tree", commit}
	if folder != "" {
		args = append(args, "--", folder+"/")
	}
	if _, err := git(ctx, ts, repo, nil, &out, args...); err != nil {
		return nil, fmt.Errorf("MetaRush couldn't list %s at %s: %w", name, commit[:12], err)
	}
	var entries []treeEntry
	for _, rec := range bytes.Split(out.Bytes(), []byte{0}) {
		tab := bytes.IndexByte(rec, '\t')
		if len(rec) == 0 || tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) != 4 {
			continue
		}
		e := treeEntry{mode: f[0], typ: f[1], sha: f[2], path: string(rec[tab+1:])}
		if f[3] != "-" {
			fmt.Sscan(f[3], &e.size)
		}
		entries = append(entries, e)
	}
	if len(entries) == 0 {
		return nil, apperr.New(apperr.NotFound, "%s has no folder %s at %s.", name, orTop(folder), commit[:12])
	}
	var files []FolderFile
	var shas []string
	var total int64
	for _, e := range entries {
		if !safeRepoPath(e.path) {
			return nil, apperr.New(apperr.InvalidInput, "%s holds a file with a name MetaRush won't read (%s).", orTop(folder), clipName(e.path))
		}
		rel := e.path
		if folder != "" {
			if !strings.HasPrefix(e.path, folder+"/") {
				return nil, apperr.New(apperr.InvalidInput, "%s at %s is a file, not a folder.", folder, commit[:12])
			}
			rel = strings.TrimPrefix(e.path, folder+"/")
		}
		if lim.Skip != nil && lim.Skip(rel) {
			continue
		}
		f := FolderFile{Path: rel, Mode: e.mode, Size: e.size}
		switch {
		case e.typ == "commit":
			f.Mode = "160000"
		case e.typ != "blob":
			continue
		case e.mode == "120000":
		default:
			if lim.FileBytes > 0 && e.size > lim.FileBytes {
				return nil, apperr.New(apperr.InvalidInput, "%s is %s; a file of an ability may be at most %s.", rel, humanBytes(e.size), humanBytes(lim.FileBytes))
			}
			total += e.size
			if lim.TotalBytes > 0 && total > lim.TotalBytes {
				return nil, apperr.New(apperr.InvalidInput, "The folder's files add up to more than %s, the most an ability may have.", humanBytes(lim.TotalBytes))
			}
			shas = append(shas, e.sha)
		}
		if lim.Files > 0 && len(files) >= lim.Files {
			return nil, apperr.New(apperr.InvalidInput, "The folder has more than %d files, the most an ability may have.", lim.Files)
		}
		files = append(files, f)
	}
	blobs := map[string][]byte{}
	if err := readBlobs(ctx, ts, repo, shas, func(sha string, data []byte) error {
		blobs[sha] = data
		return nil
	}); err != nil {
		return nil, err
	}
	bySha := map[string]string{}
	for _, e := range entries {
		bySha[e.path] = e.sha
	}
	for i := range files {
		f := &files[i]
		if f.Link() || f.Submodule() {
			continue
		}
		full := f.Path
		if folder != "" {
			full = folder + "/" + f.Path
		}
		data, ok := blobs[bySha[full]]
		if !ok {
			return nil, errors.New("a file of the folder was missing from what the repository sent")
		}
		f.Data = data
	}
	return files, nil
}

// FolderAtHere reads the files under folder at a commit of a repository on
// this computer (a catalogue maintainer's checkout, D487): the same reading
// as FolderAt, without fetching anything.
func FolderAtHere(ctx context.Context, ts *tools.Set, repo, commit, folder string, lim FolderLimits) ([]FolderFile, error) {
	if !fullSHA.MatchString(commit) {
		return nil, apperr.New(apperr.InvalidInput, "Name the commit in full (40 characters).")
	}
	folder = strings.Trim(folder, "/")
	if folder != "" && !safeRepoPath(folder) {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a plain folder in a repository.")
	}
	return filesAt(ctx, ts, repo, commit, folder, lim, filepath.Base(repo))
}

func orTop(folder string) string {
	if folder == "" {
		return "the repository's top folder"
	}
	return folder
}

func clipName(p string) string {
	p = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, p)
	if len(p) > 80 {
		return p[:80] + "…"
	}
	return p
}
