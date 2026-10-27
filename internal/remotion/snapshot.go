package remotion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// snapFile is one file a version will hold, with a local copy of exactly its
// bytes (the person's folder never changes while MetaRush reads it once;
// GitHub blobs are written to a temporary folder).
type snapFile struct {
	ManifestFile
	local string
}

// outVideo is a video found in the project's out/ folder: brought in as
// footage (D131).
type outVideo struct {
	Path  string // out/…, slash path
	local string
	Size  int64
}

// snapshot is what reading a project found.
type snapshot struct {
	files    []snapFile
	left     leftOut
	videos   []outVideo
	pubBytes int64
	bytes    int64
}

func (s *snapshot) manifest() *Manifest {
	fs := make([]ManifestFile, len(s.files))
	for i, f := range s.files {
		fs[i] = f.ManifestFile
	}
	return NewManifest(fs)
}

func (s *snapshot) leftOut() []leftOutGroup {
	var out []leftOutGroup
	for _, why := range s.left.order {
		out = append(out, leftOutGroup{why, s.left.n[why], s.left.ex[why]})
	}
	return out
}

type leftOutGroup struct {
	why      string
	count    int
	examples []string
}

// videoExts are renders worth bringing in from out/.
var videoExts = map[string]bool{".mp4": true, ".mov": true, ".webm": true, ".mkv": true, ".m4v": true}

// accept decides about one file by name and size, then by contents. It
// returns "" when the file is kept.
func (s *snapshot) accept(rel string, size int64) string {
	if why := skipFile(rel); why != "" {
		return why
	}
	if len(s.files) >= maxFiles {
		return whyOverLimit
	}
	if isPublic(rel) {
		if size > maxPublicFileBytes {
			return whyTooBig
		}
		if s.pubBytes+size > maxPublicBytes {
			return whyOverLimit
		}
		return ""
	}
	if size > maxCodeFileBytes {
		return whyTooBig
	}
	if s.bytes+size > maxCodeBytes {
		return whyOverLimit
	}
	return ""
}

func (s *snapshot) keep(rel, local string, size int64, sha string) {
	s.files = append(s.files, snapFile{ManifestFile: ManifestFile{Path: rel, Size: size, SHA256: sha}, local: local})
	if isPublic(rel) {
		s.pubBytes += size
	} else {
		s.bytes += size
	}
}

// hashLocal reads a file once: its SHA-256 and, for files that aren't media,
// whether its contents look like a secret.
func hashLocal(p, rel string) (sha string, size int64, secret string, err error) {
	f, err := os.Open(p)
	if err != nil {
		return "", 0, "", err
	}
	defer f.Close()
	return hashReader(f, rel, nil)
}

// hashReader is hashLocal for an open file, writing the bytes to also too
// (a copy) when it isn't nil.
func hashReader(f io.Reader, rel string, also io.Writer) (sha string, size int64, secret string, err error) {
	h := sha256.New()
	var head bytes.Buffer
	var w io.Writer = h
	if !isMedia(rel) {
		w = io.MultiWriter(h, &limitedBuffer{b: &head, max: 8 << 20})
	}
	if also != nil {
		w = io.MultiWriter(w, also)
	}
	n, err := io.Copy(w, f)
	if err != nil {
		return "", 0, "", err
	}
	if !isMedia(rel) {
		secret = secretIn(rel, head.Bytes())
	}
	return hex.EncodeToString(h.Sum(nil)), n, secret, nil
}

type limitedBuffer struct {
	b   *bytes.Buffer
	max int
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		if len(p) <= room {
			l.b.Write(p)
		} else {
			l.b.Write(p[:room])
		}
	}
	return len(p), nil
}

// readFolder reads a project folder: everything the rules keep, with the
// project's .gitignore files applied outside public/, and the videos in its
// out/ folder. The folder is only read.
func readFolder(ctx context.Context, root string) (*snapshot, error) {
	s := &snapshot{}
	ig := &ignorer{}
	var walk func(dir, rel string) error
	walk = func(dir, rel string) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if b, err := os.ReadFile(filepath.Join(dir, ".gitignore")); err == nil {
			ig.add(rel, b)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			return err
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, e := range entries {
			name := e.Name()
			r := name
			if rel != "" {
				r = rel + "/" + name
			}
			full := filepath.Join(dir, name)
			if e.Type()&os.ModeSymlink != 0 {
				s.left.add(r, whyLink)
				continue
			}
			if e.IsDir() {
				if why := skipDir(r); why != "" {
					if r == "out" {
						s.collectOut(full, "out", ig)
					}
					if why == whyGit && rel == "" {
						continue // the project's own git history: not worth a line
					}
					s.left.add(r+"/", why)
					continue
				}
				if !isPublic(r+"/") && r != "public" && ig.ignored(r, true) {
					s.left.add(r+"/", whyIgnored)
					continue
				}
				if err := walk(full, r); err != nil {
					return err
				}
				continue
			}
			if !e.Type().IsRegular() {
				s.left.add(r, whyName)
				continue
			}
			info, err := e.Info()
			if err != nil {
				return err
			}
			// What a file is (a secret, an assistant's instructions) says more
			// than that .gitignore leaves it out.
			if why := skipFile(r); why != "" {
				s.left.add(r, why)
				continue
			}
			if !isPublic(r) && ig.ignored(r, false) {
				s.left.add(r, whyIgnored)
				continue
			}
			if why := s.accept(r, info.Size()); why != "" {
				s.left.add(r, why)
				continue
			}
			sha, n, secret, err := hashLocal(full, r)
			if err != nil {
				return err
			}
			if secret != "" {
				s.left.add(r, whySecretText)
				continue
			}
			s.keep(r, full, n, sha)
		}
		return nil
	}
	if err := walk(root, ""); err != nil {
		return nil, err
	}
	return s, nil
}

// collectOut finds the videos in out/: every video at its top level (where
// Remotion writes renders), and in its folders unless .gitignore leaves them
// out (scratch renders).
func (s *snapshot) collectOut(dir, rel string, ig *ignorer) {
	var walk func(d, r string, depth int)
	walk = func(d, r string, depth int) {
		entries, err := os.ReadDir(d)
		if err != nil {
			return
		}
		for _, e := range entries {
			rr := r + "/" + e.Name()
			if e.Type()&os.ModeSymlink != 0 || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			if e.IsDir() {
				if depth < 2 && !ig.ignored(rr, true) {
					walk(filepath.Join(d, e.Name()), rr, depth+1)
				}
				continue
			}
			if !videoExts[strings.ToLower(path.Ext(e.Name()))] || (depth > 0 && ig.ignored(rr, false)) || len(s.videos) >= 50 {
				continue
			}
			if info, err := e.Info(); err == nil && info.Mode().IsRegular() && info.Size() > 0 {
				s.videos = append(s.videos, outVideo{Path: rr, local: filepath.Join(d, e.Name()), Size: info.Size()})
			}
		}
	}
	walk(dir, rel, 0)
}

// projectProblem says, in plain words, why a folder or snapshot isn't a
// Remotion project MetaRush can use ("" = it is).
func projectProblem(s *snapshot) string {
	has := map[string]bool{}
	for _, f := range s.files {
		has[f.Path] = true
	}
	switch {
	case !has["package.json"]:
		return "There's no package.json at the top of it, so it isn't a Node project MetaRush can read."
	case !has["package-lock.json"]:
		if has["yarn.lock"] || has["pnpm-lock.yaml"] || has["bun.lockb"] || has["bun.lock"] {
			return "It locks its packages with another package manager (yarn, pnpm or bun); MetaRush installs exactly what a package-lock.json pins, and this project has none."
		}
		return "It has no package-lock.json, so MetaRush can't install exactly the package versions it was made with."
	}
	return ""
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%d %s", n, many)
}
