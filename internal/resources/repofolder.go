package resources

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/tools"
)

// One folder of a repository at one commit, for an ability a community
// catalogue lists (D485): fetched with the same rules as a repository
// brought in (D104) — git with hooks, prompts and every protocol but https
// off, into fresh bare repositories — and read raw (git cat-file), so no
// filter, hook or attribute of the repository runs and nothing is checked
// out.
//
// The commit must be the repository's own (D485): one of its branches or
// tags holds it. GitHub hands out every commit of a repository's whole
// network of copies under each copy's address, so a commit only someone's
// copy holds (or a pull request from one) would otherwise pass for the
// repository's. And only what the folder needs is fetched, each step within
// a limit: the history of the branches and tags without any folder or file
// (when the commit isn't one's tip), the commit's folders, then the folder's
// own files.

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

// What a fetch for a folder may download at most (variables only so tests
// can lower them): a repository's history without folders or files, and a
// commit's folders without files. The folder's files are held to
// FolderLimits.TotalBytes.
var (
	historyMax int64 = 256 << 20
	foldersMax int64 = 64 << 20
)

// ErrFetchTooLarge is a fetch stopped at its limit.
var ErrFetchTooLarge = errors.New("the fetch grew past its limit")

// FolderAt fetches one commit of a GitHub repository under work and reads the
// files under folder ("" for its top). The commit is named in full and must
// be the repository's own; a server that won't hand it out fails plainly.
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
	ctx = withoutLazyFetch(ctx)
	if err := ownCommit(ctx, ts, work, r, commit); err != nil {
		return nil, err
	}
	bare := filepath.Join(work, "repo.git")
	if err := partialRepo(ctx, ts, work, bare, r, "blob:none"); err != nil {
		return nil, err
	}
	// The commit and its folders, no file yet.
	if err := fetchWithin(ctx, ts, bare, foldersMax, nil, "-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet", "--no-tags", "--no-write-fetch-head",
		"--recurse-submodules=no", "--depth=1", "--filter=blob:none", "origin", commit); err != nil {
		if errors.Is(err, ErrFetchTooLarge) {
			return nil, apperr.New(apperr.InvalidInput, "%s/%s at %s has more folders than MetaRush reads for one ability (%s).", r.Owner, r.Name, commit[:12], humanBytes(foldersMax))
		}
		return nil, apperr.From(gitProblem(r, err))
	}
	var head bytes.Buffer
	if _, err := git(ctx, ts, bare, nil, &head, "rev-parse", "--verify", "--quiet", commit+"^{commit}"); err != nil || strings.TrimSpace(head.String()) != commit {
		return nil, apperr.New(apperr.NotFound, "%s/%s has no commit %s.", r.Owner, r.Name, commit[:12])
	}
	// Then the folder's own files, by their names, within the folder's limit.
	fetch := func(shas []string) error {
		max := int64(math.MaxInt64)
		if lim.TotalBytes > 0 {
			// What is here already (the folders), the files, and a little
			// more for the pack's own bytes.
			max = objectsSize(bare) + lim.TotalBytes + lim.TotalBytes/4 + 1<<20
		}
		err := fetchWithin(ctx, ts, bare, max, strings.NewReader(strings.Join(shas, "\n")+"\n"), "-c", "fetch.negotiationAlgorithm=noop", "fetch", "--quiet",
			"--no-tags", "--no-write-fetch-head", "--recurse-submodules=no", "--filter=blob:none", "--stdin", "origin")
		switch {
		case errors.Is(err, ErrFetchTooLarge):
			return apperr.New(apperr.InvalidInput, "The folder's files add up to more than %s, the most an ability may have.", humanBytes(lim.TotalBytes))
		case err != nil:
			return apperr.From(gitProblem(r, err))
		}
		return nil
	}
	return filesAt(ctx, ts, bare, commit, folder, lim, r.Owner+"/"+r.Name, fetch)
}

// ownCommit refuses a commit none of the repository's branches and tags
// holds (never its pull requests', which come from other people's copies):
// a branch's or a tag's tip is the repository's; else the history of its
// branches and tags, without any folder or file, must hold it.
func ownCommit(ctx context.Context, ts *tools.Set, work string, r RepoRef, commit string) error {
	notOwn := func() error {
		return apperr.New(apperr.Integrity, "No branch or tag of %s/%s holds the commit %s, so MetaRush won't bring it in: GitHub also hands out commits from other people's copies of a repository under its address, and only the repository's own are its.", r.Owner, r.Name, commit[:12]).
			WithHint("Ask whoever keeps the catalogue to list a commit on one of the repository's branches or tags.")
	}
	var out bytes.Buffer
	if _, err := git(ctx, ts, work, nil, &out, "ls-remote", "--heads", "--tags", "--", cloneURL(r)); err != nil {
		return apperr.From(gitProblem(r, err))
	}
	refs := 0
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		if len(f) != 2 || !fullSHA.MatchString(f[0]) || !(strings.HasPrefix(f[1], "refs/heads/") || strings.HasPrefix(f[1], "refs/tags/")) {
			continue
		}
		refs++
		if f[0] == commit {
			return nil // a branch's tip, or a tag's (peeled: refs/tags/x^{})
		}
	}
	if refs == 0 {
		return notOwn()
	}
	hist := filepath.Join(work, "history.git")
	if err := partialRepo(ctx, ts, work, hist, r, "tree:0"); err != nil {
		return err
	}
	if err := fetchWithin(ctx, ts, hist, historyMax, nil, "fetch", "--quiet", "--no-tags", "--recurse-submodules=no", "--filter=tree:0", "origin",
		"+refs/heads/*:refs/heads/*", "+refs/tags/*:refs/tags/*"); err != nil {
		if errors.Is(err, ErrFetchTooLarge) {
			return apperr.New(apperr.InvalidInput, "The history of %s/%s is larger than MetaRush reads to see whose commit %s is (%s).", r.Owner, r.Name, commit[:12], humanBytes(historyMax)).
				WithHint("Ask whoever keeps the catalogue to list a commit that is a branch's or a tag's latest.")
		}
		return apperr.From(gitProblem(r, err))
	}
	// Only what these branches and tags hold was fetched: a commit that isn't
	// here is none of theirs (and isn't fetched by itself: no lazy fetch).
	var held bytes.Buffer
	if _, err := git(ctx, ts, hist, nil, &held, "for-each-ref", "--count=1", "--format=%(refname)", "--contains", commit, "refs/heads", "refs/tags"); err != nil || strings.TrimSpace(held.String()) == "" {
		return notOwn()
	}
	return nil
}

// partialRepo makes a fresh bare repository whose origin is the repository,
// a partial one (filter: tree:0, only commits; blob:none, no files) so a
// fetch brings only what it is asked for.
func partialRepo(ctx context.Context, ts *tools.Set, work, bare string, r RepoRef, filter string) error {
	if _, err := git(ctx, ts, work, nil, nil, "init", "--bare", "--quiet", bare); err != nil {
		return fmt.Errorf("MetaRush couldn't make a place to put the repository: %w", err)
	}
	for _, kv := range [][2]string{{"core.repositoryFormatVersion", "1"}, {"remote.origin.url", cloneURL(r)}, {"remote.origin.promisor", "true"},
		{"remote.origin.partialCloneFilter", filter}, {"extensions.partialClone", "origin"}} {
		if _, err := git(ctx, ts, bare, nil, nil, "config", kv[0], kv[1]); err != nil {
			return fmt.Errorf("MetaRush couldn't make a place to put the repository: %w", err)
		}
	}
	return nil
}

// fetchWatchEvery is how often a fetch's size is looked at.
var fetchWatchEvery = 100 * time.Millisecond

// fetchWithin runs a git fetch into repo and stops it once the repository's
// objects pass max bytes (ErrFetchTooLarge): what a catalogue names can't
// make MetaRush download more than an ability may hold.
func fetchWithin(ctx context.Context, ts *tools.Set, repo string, max int64, stdin io.Reader, args ...string) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var over atomic.Bool
	stop := make(chan struct{})
	watched := make(chan struct{})
	go func() {
		defer close(watched)
		t := time.NewTicker(fetchWatchEvery)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				if objectsSize(repo) > max {
					over.Store(true)
					cancel()
					return
				}
			}
		}
	}()
	_, err := git(ctx, ts, repo, stdin, nil, args...)
	close(stop)
	<-watched
	if over.Load() || objectsSize(repo) > max {
		return ErrFetchTooLarge
	}
	return err
}

// objectsSize is how much a repository's objects take (packs being written
// too).
func objectsSize(repo string) int64 {
	var n int64
	_ = filepath.WalkDir(filepath.Join(repo, "objects"), func(_ string, d fs.DirEntry, err error) error {
		if err == nil && d.Type().IsRegular() {
			if info, err := d.Info(); err == nil {
				n += info.Size()
			}
		}
		return nil
	})
	return n
}

// filesAt reads the files under folder at a commit of a repository on this
// computer (FolderAt's partial copy, or a maintainer's own checkout). fetch,
// when there is one, brings the files' contents first (a partial copy has
// none yet); their sizes count when they are read.
func filesAt(ctx context.Context, ts *tools.Set, repo, commit, folder string, lim FolderLimits, name string, fetch func(shas []string) error) ([]FolderFile, error) {
	var out bytes.Buffer
	// --literal-pathspecs: the folder is a path, never a pattern. Sizes (-l)
	// only where the files are here already.
	args := []string{"--literal-pathspecs", "ls-tree", "-r", "-z", "--full-tree"}
	if fetch == nil {
		args = append(args, "-l")
	}
	args = append(args, commit)
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
		if len(f) != 3 && len(f) != 4 {
			continue
		}
		e := treeEntry{mode: f[0], typ: f[1], sha: f[2], path: string(rec[tab+1:])}
		if len(f) == 4 && f[3] != "-" {
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
	tooLarge := func(rel string, size int64) error {
		return apperr.New(apperr.InvalidInput, "%s is %s; a file of an ability may be at most %s.", rel, humanBytes(size), humanBytes(lim.FileBytes))
	}
	tooMuch := func() error {
		return apperr.New(apperr.InvalidInput, "The folder's files add up to more than %s, the most an ability may have.", humanBytes(lim.TotalBytes))
	}
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
				return nil, tooLarge(rel, e.size)
			}
			total += e.size
			if lim.TotalBytes > 0 && total > lim.TotalBytes {
				return nil, tooMuch()
			}
			shas = append(shas, e.sha)
		}
		if lim.Files > 0 && len(files) >= lim.Files {
			return nil, apperr.New(apperr.InvalidInput, "The folder has more than %d files, the most an ability may have.", lim.Files)
		}
		files = append(files, f)
	}
	bySha := map[string]string{}
	for _, e := range entries {
		bySha[e.path] = e.sha
	}
	fullPath := func(rel string) string {
		if folder == "" {
			return rel
		}
		return folder + "/" + rel
	}
	if fetch != nil && len(shas) > 0 {
		if err := fetch(shas); err != nil {
			return nil, err
		}
		// A partial copy's files are sized now, from git's index of them,
		// before any is read: a fetch is limited by its packed bytes, and
		// files that pack small may be large.
		sizes, err := blobSizes(ctx, ts, repo, shas)
		if err != nil {
			return nil, fmt.Errorf("MetaRush couldn't size the files of %s at %s: %w", name, commit[:12], err)
		}
		total = 0
		for i := range files {
			f := &files[i]
			if f.Link() || f.Submodule() {
				continue
			}
			size, ok := sizes[bySha[fullPath(f.Path)]]
			if !ok {
				return nil, errors.New("a file of the folder was missing from what the repository sent")
			}
			f.Size = size
			if lim.FileBytes > 0 && size > lim.FileBytes {
				return nil, tooLarge(f.Path, size)
			}
			if total += size; lim.TotalBytes > 0 && total > lim.TotalBytes {
				return nil, tooMuch()
			}
		}
	}
	blobs := map[string][]byte{}
	err := readBlobs(ctx, ts, repo, shas, BlobLimits{Each: lim.FileBytes, Total: lim.TotalBytes}, func(sha string, data []byte) error {
		blobs[sha] = data
		return nil
	})
	var limit *ErrBlobLimit
	switch {
	case errors.As(err, &limit) && limit.Total:
		return nil, tooMuch()
	case errors.As(err, &limit):
		return nil, apperr.New(apperr.InvalidInput, "A file of the folder is %s; a file of an ability may be at most %s.", humanBytes(limit.Size), humanBytes(lim.FileBytes))
	case err != nil:
		return nil, err
	}
	total = 0
	for i := range files {
		f := &files[i]
		if f.Link() || f.Submodule() {
			continue
		}
		data, ok := blobs[bySha[fullPath(f.Path)]]
		if !ok {
			return nil, errors.New("a file of the folder was missing from what the repository sent")
		}
		// Sizes again, as read.
		f.Data, f.Size = data, int64(len(data))
		if lim.FileBytes > 0 && f.Size > lim.FileBytes {
			return nil, tooLarge(f.Path, f.Size)
		}
		if total += f.Size; lim.TotalBytes > 0 && total > lim.TotalBytes {
			return nil, tooMuch()
		}
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
	return filesAt(ctx, ts, repo, commit, folder, lim, filepath.Base(repo), nil)
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
