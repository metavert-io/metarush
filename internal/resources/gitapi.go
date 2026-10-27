package resources

import (
	"context"
	"io"

	"github.com/metavert-io/metarush/internal/tools"
)

// The hardened git of D104 for other packages (Remotion sources, D131): no
// prompts, no hooks, https only (file:// for test fixtures through
// METARUSH_GIT_BASE), blobs read raw so no filter or attribute of a
// repository runs.

// Git runs git with the fetch-only settings.
func Git(ctx context.Context, ts *tools.Set, dir string, stdin io.Reader, stdout io.Writer, args ...string) (*tools.Result, error) {
	return git(ctx, ts, dir, stdin, stdout, args...)
}

// CloneURL is where git fetches a repository from (the test base when set).
func CloneURL(r RepoRef) string { return cloneURL(r) }

// ResolveCommit finds the commit a ref names (the default branch when empty)
// and the branch it is on.
func ResolveCommit(ctx context.Context, ts *tools.Set, work string, r RepoRef) (commit, branch string, err error) {
	return resolveCommit(ctx, ts, work, r)
}

// GitProblem turns a git failure into advice in plain words.
func GitProblem(r RepoRef, err error) error { return gitProblem(r, err) }

// TreeEntry is one entry of a commit's tree.
type TreeEntry struct {
	Mode, Type, SHA string
	Size            int64
	Path            string
}

// ListTree lists every entry of a commit (recursively).
func ListTree(ctx context.Context, ts *tools.Set, bare, commit string) ([]TreeEntry, error) {
	es, err := listTree(ctx, ts, bare, commit)
	if err != nil {
		return nil, err
	}
	out := make([]TreeEntry, len(es))
	for i, e := range es {
		out[i] = TreeEntry{Mode: e.mode, Type: e.typ, SHA: e.sha, Size: e.size, Path: e.path}
	}
	return out, nil
}

// ReadBlobs streams blobs' raw bytes (git cat-file --batch) in request order.
func ReadBlobs(ctx context.Context, ts *tools.Set, bare string, shas []string, each func(sha string, data []byte) error) error {
	return readBlobs(ctx, ts, bare, shas, each)
}

// IsLFSPointer reports a Git LFS pointer file (the real file isn't in git).
func IsLFSPointer(b []byte) bool { return isLFSPointer(b) }

// FullSHA reports a full 40-character commit hash.
func FullSHA(s string) bool { return fullSHA.MatchString(s) }

// SafeRef reports a branch, tag or commit name git may be given.
func SafeRef(s string) bool { return refSafe.MatchString(s) && (len(s) == 0 || s[0] != '-') }
