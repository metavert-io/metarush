package remotion

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
)

// folderOrigin reads where a project folder came from without changing it:
// git is asked read-only questions that read objects only (no status, which
// could run a repository's filters; optional locks off), so nothing the
// folder's git settings name ever runs.
func folderOrigin(ctx context.Context, ts *tools.Set, root, machine string, s *snapshot) models.RemotionOrigin {
	o := models.RemotionOrigin{Kind: models.RemotionFromFolder, Folder: filepath.Base(root), Path: tildePath(root), Machine: machine}
	if ts == nil || !ts.Has(tools.Git) {
		return o
	}
	if _, err := os.Stat(filepath.Join(root, ".git")); err != nil {
		return o // not the top of a git checkout (a folder inside one is read as a folder)
	}
	q := func(args ...string) string {
		var out bytes.Buffer
		if _, err := resources.Git(ctx, ts, root, nil, &out, append([]string{"--no-optional-locks"}, args...)...); err != nil {
			return ""
		}
		return strings.TrimSpace(out.String())
	}
	commit := q("rev-parse", "--verify", "-q", "HEAD^{commit}")
	if !resources.FullSHA(commit) {
		return o
	}
	o.Commit = commit
	if b := q("rev-parse", "--abbrev-ref", "HEAD"); b != "" && b != "HEAD" {
		o.Branch = b
	}
	o.Remote = cleanRemote(q("config", "--get", "remote.origin.url"))
	o.Dirty = folderDiffers(ctx, ts, root, commit, s)
	return o
}

// folderDiffers reports whether the copy differs from the commit: a file
// changed, added or left out. Git blob hashes are computed here from the
// files MetaRush read.
func folderDiffers(ctx context.Context, ts *tools.Set, root, commit string, s *snapshot) bool {
	var out bytes.Buffer
	if _, err := resources.Git(ctx, ts, root, nil, &out, "--no-optional-locks", "ls-tree", "-r", "-z", "--full-tree", commit); err != nil {
		return true
	}
	tracked := map[string]string{} // path → blob SHA-1
	for _, rec := range bytes.Split(out.Bytes(), []byte{0}) {
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) == 3 && f[1] == "blob" {
			tracked[string(rec[tab+1:])] = f[2]
		}
	}
	kept := map[string]bool{}
	for _, f := range s.files {
		kept[f.Path] = true
		want, ok := tracked[f.Path]
		if !ok {
			return true // a file the commit doesn't have
		}
		if got, err := gitBlobSHA(f.local); err != nil || got != want {
			return true
		}
	}
	for p := range tracked {
		if !kept[p] && skipFile(p) == "" && !insideSkipped(p) && !strings.HasPrefix(p, "out/") {
			if _, err := os.Lstat(filepath.Join(root, filepath.FromSlash(p))); err != nil {
				return true // deleted from the folder
			}
		}
	}
	return false
}

// insideSkipped reports a path inside a folder the rules never copy.
func insideSkipped(p string) bool {
	parts := strings.Split(p, "/")
	for i := 1; i < len(parts); i++ {
		if skipDir(strings.Join(parts[:i], "/")) != "" {
			return true
		}
	}
	return false
}

// gitBlobSHA is git's object hash of a file's bytes.
func gitBlobSHA(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return "", err
	}
	h := sha1.New() // git's object names are SHA-1 (not used for security here)
	fmt.Fprintf(h, "blob %d\x00", st.Size())
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// cleanRemote drops any user name or password from a git remote address.
func cleanRemote(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Scheme != "" && u.Host != "" {
		u.User = nil
		u.RawQuery, u.Fragment = "", ""
		return u.String()
	}
	// git@github.com:owner/repo.git
	if i := strings.Index(raw, "@"); i >= 0 && strings.Contains(raw[i:], ":") {
		return raw[i+1:]
	}
	return raw
}

// tildePath shortens the home folder to ~.
func tildePath(p string) string {
	if u, err := os.UserHomeDir(); err == nil && u != "" && (p == u || strings.HasPrefix(p, u+string(filepath.Separator))) {
		return "~" + strings.TrimPrefix(p, u)
	}
	return p
}

// expandHome turns ~/… into a full path.
func expandHome(p string) string {
	p = strings.TrimSpace(p)
	if p == "~" || strings.HasPrefix(p, "~/") {
		if u, err := os.UserHomeDir(); err == nil {
			return filepath.Join(u, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// lines splits text into lines (tests).
func lines(s string) []string {
	var out []string
	sc := bufio.NewScanner(strings.NewReader(s))
	for sc.Scan() {
		out = append(out, sc.Text())
	}
	return out
}
