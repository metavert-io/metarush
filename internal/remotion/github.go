package remotion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// maxBlobBytes caps one file read from a repository (blobs are held in
// memory while they are written out).
const maxBlobBytes = 512 << 20

// readRepo fetches one commit of a GitHub repository into a fresh bare
// repository in work (shallow, hooks and prompts off, the person's own
// sign-in) and keeps its files by the same rules as a folder, writing each
// kept blob into work/files. Videos tracked in out/ come back as renders.
func readRepo(ctx context.Context, ts *tools.Set, work string, r resources.RepoRef, progress func(string)) (*snapshot, models.RemotionOrigin, error) {
	o := models.RemotionOrigin{Kind: models.RemotionFromGitHub, URL: r.URL(), Repo: r.Owner + "/" + r.Name, Ref: r.Ref}
	if ts == nil || !ts.Has(tools.Git) {
		return nil, o, errors.New("bringing in a GitHub repository needs git, which isn't installed on this computer")
	}
	progress("finding the commit")
	commit, branch, err := resources.ResolveCommit(ctx, ts, work, r)
	if err != nil {
		return nil, o, err
	}
	o.Commit, o.Branch = commit, branch
	bare := filepath.Join(work, "repo.git")
	if _, err := resources.Git(ctx, ts, work, nil, nil, "init", "--bare", "--quiet", bare); err != nil {
		return nil, o, fmt.Errorf("MetaRush couldn't make a place to put the repository: %w", err)
	}
	progress("fetching " + o.Repo + " at " + models.ShortSHA(commit))
	if _, err := resources.Git(ctx, ts, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", resources.CloneURL(r), commit); err != nil {
		if resources.FullSHA(r.Ref) {
			return nil, o, resources.GitProblem(r, err)
		}
		want := r.Ref
		if want == "" {
			want = "HEAD"
		}
		if _, err2 := resources.Git(ctx, ts, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", resources.CloneURL(r), want); err2 != nil {
			return nil, o, resources.GitProblem(r, err2)
		}
		var head bytes.Buffer
		if _, err := resources.Git(ctx, ts, bare, nil, &head, "rev-parse", "FETCH_HEAD^{commit}"); err == nil && resources.FullSHA(strings.TrimSpace(head.String())) {
			o.Commit = strings.TrimSpace(head.String())
			commit = o.Commit
		}
	}
	progress("reading its files")
	entries, err := resources.ListTree(ctx, ts, bare, commit)
	if err != nil {
		return nil, o, resources.GitProblem(r, err)
	}
	s := &snapshot{}
	files := filepath.Join(work, "files")
	if err := os.MkdirAll(files, 0o700); err != nil {
		return nil, o, err
	}
	type want struct {
		e   resources.TreeEntry
		out bool // a video in out/
	}
	var wants []want
	seenDir := map[string]bool{}
	for _, e := range entries {
		switch {
		case e.Type == "commit":
			s.left.add(e.Path, whySubmodule)
			continue
		case e.Type != "blob":
			continue
		case e.Mode == "120000":
			s.left.add(e.Path, whyLink)
			continue
		case !safeRel(e.Path):
			s.left.add(e.Path, whyName)
			continue
		}
		if strings.HasPrefix(e.Path, "out/") {
			if videoExts[strings.ToLower(path.Ext(e.Path))] && strings.Count(e.Path, "/") <= 3 && e.Size > 0 && e.Size <= maxBlobBytes && len(s.videos) < 50 {
				wants = append(wants, want{e: e, out: true})
			}
			continue
		}
		if dir := path.Dir(e.Path); dir != "." {
			if why := skippedDirOf(e.Path); why != "" {
				if d := topSkipped(e.Path); !seenDir[d] {
					seenDir[d] = true
					s.left.add(d+"/", why)
				}
				continue
			}
		}
		if e.Size > maxBlobBytes {
			s.left.add(e.Path, whyTooBig)
			continue
		}
		if why := s.accept(e.Path, e.Size); why != "" {
			s.left.add(e.Path, why)
			continue
		}
		s.keep(e.Path, "", e.Size, "") // placeholder: filled once the blob is read
		wants = append(wants, want{e: e})
	}
	// Placeholders hold their place in s.files; drop them and add the real
	// files as their blobs arrive.
	s.files, s.bytes, s.pubBytes = nil, 0, 0
	byBlob := map[string][]want{}
	var shas []string
	for _, w := range wants {
		if _, ok := byBlob[w.e.SHA]; !ok {
			shas = append(shas, w.e.SHA)
		}
		byBlob[w.e.SHA] = append(byBlob[w.e.SHA], w)
	}
	progress(fmt.Sprintf("keeping %d files", len(wants)))
	err = resources.ReadBlobs(ctx, ts, bare, shas, func(blob string, data []byte) error {
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		local := filepath.Join(files, sha)
		if _, err := os.Stat(local); err != nil {
			if err := os.WriteFile(local, data, 0o600); err != nil {
				return err
			}
		}
		for _, w := range byBlob[blob] {
			switch {
			case w.out:
				s.videos = append(s.videos, outVideo{Path: w.e.Path, local: local, Size: int64(len(data))})
			case resources.IsLFSPointer(data):
				s.left.add(w.e.Path, whyLFS)
			default:
				if what := secretIn(w.e.Path, data); what != "" {
					s.left.add(w.e.Path, whySecretText)
					continue
				}
				s.keep(w.e.Path, local, int64(len(data)), sha)
			}
		}
		return nil
	})
	if err != nil {
		return nil, o, resources.GitProblem(r, err)
	}
	return s, o, nil
}

// skippedDirOf says why a file's folder is never copied ("" = it is).
func skippedDirOf(p string) string {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		if why := skipDir(strings.Join(parts[:i], "/")); why != "" {
			return why
		}
	}
	return ""
}

// topSkipped is the outermost skipped folder of a path.
func topSkipped(p string) string {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		d := strings.Join(parts[:i], "/")
		if skipDir(d) != "" {
			return d
		}
	}
	return path.Dir(p)
}
