package resources

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// GitHub repositories (D104). A repository resource is pinned to one commit:
// each version is a snapshot of the files kept at that commit (a canonical
// tar.gz in the media store, content-addressed) plus an overview (README,
// file tree, languages, recent history and contributors). Git fetches it with
// the person's own sign-in (their credential helper; gh's when it set one
// up) — MetaRush never asks for, reads or stores a token. Blobs are read raw
// (git cat-file), so no filter, hook or attribute from the repository runs;
// nothing in a repository is ever executed. Binaries, Git LFS pointers and
// large files are left out, and a snapshot stops at its size caps.

// RepoRef is a parsed GitHub address.
type RepoRef struct {
	Host, Owner, Name string
	// Ref is the branch, tag or commit in the address (/tree/<ref>, /commit/<sha>).
	Ref string
	// Path is a file or folder in the address (/blob/<ref>/<path>).
	Path string
}

// URL is the repository's canonical address.
func (r RepoRef) URL() string { return "https://" + r.Host + "/" + r.Owner + "/" + r.Name }

// Site is "github.com/acme/widget".
func (r RepoRef) Site() string { return r.Host + "/" + r.Owner + "/" + r.Name }

var (
	ghOwner = regexp.MustCompile(`^[A-Za-z0-9](?:[A-Za-z0-9-]{0,38})$`)
	ghRepo  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,100}$`)
	fullSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
	refSafe = regexp.MustCompile(`^[A-Za-z0-9._/@+-]{1,200}$`)
	// First path segments on github.com that are not accounts.
	ghReserved = map[string]bool{"orgs": true, "settings": true, "marketplace": true, "features": true, "topics": true, "collections": true,
		"sponsors": true, "about": true, "pricing": true, "login": true, "join": true, "explore": true, "notifications": true, "new": true,
		"search": true, "trending": true, "apps": true, "enterprise": true, "security": true, "site": true, "customer-stories": true}
)

// ParseRepoURL reads a GitHub repository address: github.com/<owner>/<repo>,
// optionally .git, /tree/<ref>[/<path>], /blob/<ref>/<path> or /commit/<sha>.
func ParseRepoURL(raw string) (RepoRef, bool) {
	s := strings.TrimSpace(raw)
	if !strings.Contains(s, "://") {
		s = "https://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return RepoRef{}, false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "github.com" {
		return RepoRef{}, false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	if len(parts) < 2 {
		return RepoRef{}, false
	}
	owner, name := parts[0], strings.TrimSuffix(parts[1], ".git")
	if ghReserved[strings.ToLower(owner)] || !ghOwner.MatchString(owner) || !ghRepo.MatchString(name) || name == "." || name == ".." {
		return RepoRef{}, false
	}
	r := RepoRef{Host: host, Owner: owner, Name: name}
	if len(parts) >= 4 {
		switch parts[2] {
		case "tree", "blob":
			if refSafe.MatchString(parts[3]) {
				r.Ref = parts[3]
				if len(parts) > 4 {
					r.Path = strings.Join(parts[4:], "/")
				}
			}
		case "commit", "commits":
			if fullSHA.MatchString(parts[3]) {
				r.Ref = parts[3]
			}
		case "releases":
			if len(parts) >= 5 && parts[3] == "tag" && refSafe.MatchString(parts[4]) {
				r.Ref = parts[4]
			}
		}
	}
	return r, true
}

// githubSource is the URL source for GitHub repositories (replaces part 1's placeholder).
type githubSource struct{}

func (githubSource) Name() string  { return "github" }
func (githubSource) Route() string { return LinkRepo }
func (githubSource) Match(u *url.URL) bool {
	if u == nil {
		return false
	}
	_, ok := ParseRepoURL(u.String())
	return ok
}

// Add queues the repository's fetch on this machine.
func (githubSource) Add(ctx context.Context, a *app.App, in URLAddIn) (*URLAddOut, error) {
	if a == nil {
		return nil, errors.New("no app") // part 1's readiness probe calls Add without one
	}
	r, ok := ParseRepoURL(in.URL)
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "That isn't a GitHub repository's address.")
	}
	q, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: r.URL(), Ref: r.Ref, ProjectID: in.ProjectID, Workspace: in.ProjectID == ""})
	if err != nil {
		return nil, err
	}
	return &URLAddOut{Message: q.Message, JobID: q.JobID, Status: q.Status}, nil
}

func init() { RegisterURLSource(githubSource{}) }

// ---- git ----

// gitBase maps github.com addresses to another place for tests
// (METARUSH_GIT_BASE=/path/to/fixtures: <base>/<owner>/<repo>.git, file
// protocol allowed). Never set in normal use.
func gitBase() string { return strings.TrimRight(os.Getenv("METARUSH_GIT_BASE"), "/") }

// cloneURL is where git fetches the repository from.
func cloneURL(r RepoRef) string {
	if b := gitBase(); b != "" {
		if strings.Contains(b, "://") {
			return b + "/" + r.Owner + "/" + r.Name + ".git"
		}
		return "file://" + filepath.ToSlash(b) + "/" + r.Owner + "/" + r.Name + ".git"
	}
	return r.URL() + ".git"
}

// git runs git with settings that keep it to fetching: no prompts (the
// person's credential helper answers, or the fetch fails plainly), no hooks
// (theirs or anyone's), only https (and file:// for test fixtures), and no
// automatic maintenance left running in the background after it returns.
func git(ctx context.Context, ts *tools.Set, dir string, stdin io.Reader, stdout io.Writer, args ...string) (*tools.Result, error) {
	cfg := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "protocol.allow=never", "-c", "protocol.https.allow=always",
		"-c", "core.fsmonitor=false", "-c", "core.askPass=", "-c", "credential.interactive=never", "-c", "transfer.fsckObjects=true",
		"-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	if strings.HasPrefix(gitBase(), "/") || strings.HasPrefix(gitBase(), "file://") {
		cfg = append(cfg, "-c", "protocol.file.allow=always")
	}
	env := []string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=" + falseProgram(), "SSH_ASKPASS=" + falseProgram(), "GCM_INTERACTIVE=never", "GIT_LFS_SKIP_SMUDGE=1"}
	return ts.Run(ctx, tools.Spec{Tool: tools.Git, Dir: dir, Args: append(cfg, args...), Env: env, Stdin: stdin, Stdout: stdout})
}

func falseProgram() string {
	for _, p := range []string{"/usr/bin/false", "/bin/false"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "false"
}

// gitProblem turns a git failure into advice in plain words.
func gitProblem(r RepoRef, err error) error {
	msg := err.Error()
	var fe *tools.ErrFailed
	if errors.As(err, &fe) {
		msg = fe.Stderr
	}
	low := strings.ToLower(msg)
	switch {
	case strings.Contains(low, "authentication failed") || strings.Contains(low, "could not read username") || strings.Contains(low, "terminal prompts disabled") ||
		strings.Contains(low, "repository not found") || strings.Contains(low, "403"):
		return permanentf("GitHub didn't let this computer read %s/%s. If it's private, sign in to GitHub on this computer with GitHub's own command-line tool (gh) first, then try again; MetaRush uses that sign-in and never keeps it.", r.Owner, r.Name)
	case strings.Contains(low, "couldn't find remote ref") || strings.Contains(low, "not our ref") || strings.Contains(low, "unknown revision"):
		return permanentf("%s/%s has no branch, tag or commit called %q.", r.Owner, r.Name, r.Ref)
	case strings.Contains(low, "could not resolve host") || strings.Contains(low, "network is unreachable") || strings.Contains(low, "timed out"):
		return errors.New("This computer couldn't reach GitHub; MetaRush tries again")
	}
	return fmt.Errorf("MetaRush couldn't get %s/%s from GitHub (%s)", r.Owner, r.Name, firstLine(msg))
}

// resolveCommit finds the commit a ref names (the default branch when empty).
func resolveCommit(ctx context.Context, ts *tools.Set, work string, r RepoRef) (commit, branch string, err error) {
	if fullSHA.MatchString(r.Ref) {
		return r.Ref, "", nil
	}
	var out bytes.Buffer
	args := []string{"ls-remote", "--symref", "--", cloneURL(r)}
	if r.Ref != "" {
		args = append(args, r.Ref, "refs/heads/"+r.Ref, "refs/tags/"+r.Ref, "refs/tags/"+r.Ref+"^{}")
	} else {
		args = append(args, "HEAD")
	}
	if _, err := git(ctx, ts, work, nil, &out, args...); err != nil {
		return "", "", gitProblem(r, err)
	}
	refs := map[string]string{}
	sc := bufio.NewScanner(&out)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "ref: ") {
			// ref: refs/heads/main	HEAD
			if f := strings.Fields(strings.TrimPrefix(line, "ref: ")); len(f) == 2 && f[1] == "HEAD" {
				branch = strings.TrimPrefix(f[0], "refs/heads/")
			}
			continue
		}
		f := strings.Fields(line)
		if len(f) == 2 && fullSHA.MatchString(f[0]) {
			refs[f[1]] = f[0]
		}
	}
	if r.Ref == "" {
		if c := refs["HEAD"]; c != "" {
			return c, branch, nil
		}
		return "", "", permanentf("%s/%s has no commits yet.", r.Owner, r.Name)
	}
	for _, k := range []string{"refs/tags/" + r.Ref + "^{}", "refs/tags/" + r.Ref, "refs/heads/" + r.Ref, r.Ref} {
		if c := refs[k]; c != "" {
			return c, r.Ref, nil
		}
	}
	return "", "", permanentf("%s/%s has no branch or tag called %q.", r.Owner, r.Name, r.Ref)
}

// treeEntry is one file of the commit's tree.
type treeEntry struct {
	mode, typ, sha string
	size           int64
	path           string
}

// listTree lists every entry of a commit (git ls-tree -r -l -z).
func listTree(ctx context.Context, ts *tools.Set, bare, commit string) ([]treeEntry, error) {
	var out bytes.Buffer
	if _, err := git(ctx, ts, bare, nil, &out, "ls-tree", "-r", "-l", "-z", "--full-tree", commit); err != nil {
		return nil, err
	}
	var es []treeEntry
	for _, rec := range bytes.Split(out.Bytes(), []byte{0}) {
		if len(rec) == 0 {
			continue
		}
		tab := bytes.IndexByte(rec, '\t')
		if tab < 0 {
			continue
		}
		f := strings.Fields(string(rec[:tab]))
		if len(f) != 4 {
			continue
		}
		e := treeEntry{mode: f[0], typ: f[1], sha: f[2], path: string(rec[tab+1:])}
		if f[3] != "-" {
			e.size, _ = strconv.ParseInt(f[3], 10, 64)
		}
		es = append(es, e)
	}
	sort.Slice(es, func(i, j int) bool { return es[i].path < es[j].path })
	return es, nil
}

// readBlobs streams the blobs' bytes (git cat-file --batch: raw, no filters).
func readBlobs(ctx context.Context, ts *tools.Set, bare string, shas []string, each func(sha string, data []byte) error) error {
	if len(shas) == 0 {
		return nil
	}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		br := bufio.NewReaderSize(pr, 1<<20)
		for range shas {
			head, err := br.ReadString('\n')
			if err != nil {
				errc <- fmt.Errorf("reading the snapshot: %w", err)
				_, _ = io.Copy(io.Discard, br)
				return
			}
			f := strings.Fields(head)
			if len(f) == 2 && f[1] == "missing" {
				continue
			}
			if len(f) != 3 {
				errc <- fmt.Errorf("reading the snapshot: unexpected %q", strings.TrimSpace(head))
				_, _ = io.Copy(io.Discard, br)
				return
			}
			n, _ := strconv.Atoi(f[2])
			buf := make([]byte, n+1)
			if _, err := io.ReadFull(br, buf); err != nil {
				errc <- fmt.Errorf("reading the snapshot: %w", err)
				_, _ = io.Copy(io.Discard, br)
				return
			}
			if err := each(f[0], buf[:n]); err != nil {
				errc <- err
				_, _ = io.Copy(io.Discard, br)
				return
			}
		}
		_, _ = io.Copy(io.Discard, br)
		errc <- nil
	}()
	in := strings.NewReader(strings.Join(shas, "\n") + "\n")
	_, runErr := git(ctx, ts, bare, in, pw, "cat-file", "--batch")
	_ = pw.Close()
	parseErr := <-errc
	if runErr != nil {
		return runErr
	}
	return parseErr
}

// ---- the snapshot ----

// SnapshotFile is one file a snapshot kept.
type SnapshotFile struct {
	Path     string `json:"path"`
	Bytes    int64  `json:"bytes"`
	Language string `json:"language,omitempty"`
}

// SkippedFile is one file a snapshot left out, and why.
type SkippedFile struct {
	Path string `json:"path"`
	Why  string `json:"why"`
}

// RepoOverview is the full overview stored with a version (JSON in the media store).
type RepoOverview struct {
	Repo         string                   `json:"repo"`
	Commit       string                   `json:"commit"`
	CommittedAt  time.Time                `json:"committedAt"`
	Description  string                   `json:"description,omitempty"`
	License      string                   `json:"license,omitempty"`
	Languages    []models.RepoLanguage    `json:"languages"`
	Files        []SnapshotFile           `json:"files"`
	Skipped      []SkippedFile            `json:"skipped,omitempty"`
	Readme       string                   `json:"readme,omitempty"`
	ReadmePath   string                   `json:"readmePath,omitempty"`
	Recent       []models.RepoCommit      `json:"recent,omitempty"`
	Contributors []models.RepoContributor `json:"contributors,omitempty"`
	History      string                   `json:"history,omitempty"`
}

// snapshot is what building a snapshot found.
type snapshot struct {
	tarPath  string
	files    []SnapshotFile
	skipped  []SkippedFile
	bytes    int64
	readme   string
	readmeAt string
	license  string
	langs    map[string]int64
}

// RepoLimitsFor are the snapshot limits on this machine.
type repoLimits struct {
	fileBytes, totalBytes int64
	maxFiles              int
}

// buildSnapshot reads the commit's files and writes the canonical tar.gz:
// sorted paths, one fixed time (the commit's), plain modes, no owners, so the
// same commit and limits always make the same bytes (the same hash).
func buildSnapshot(ctx context.Context, ts *tools.Set, bare, commit string, at time.Time, lim repoLimits, out string) (*snapshot, error) {
	entries, err := listTree(ctx, ts, bare, commit)
	if err != nil {
		return nil, err
	}
	s := &snapshot{langs: map[string]int64{}}
	var want []treeEntry
	var total int64
	stopped := false
	for _, e := range entries {
		switch {
		case e.typ == "commit":
			s.skipped = append(s.skipped, SkippedFile{e.path, "a submodule (another repository)"})
			continue
		case e.typ != "blob":
			continue
		case e.mode == "120000":
			s.skipped = append(s.skipped, SkippedFile{e.path, "a link to another file"})
			continue
		case !safeRepoPath(e.path):
			s.skipped = append(s.skipped, SkippedFile{e.path, "an unusual file name"})
			continue
		case isBinaryName(e.path):
			s.skipped = append(s.skipped, SkippedFile{e.path, "a picture or another file that isn't text"})
			continue
		case e.size > lim.fileBytes:
			s.skipped = append(s.skipped, SkippedFile{e.path, fmt.Sprintf("larger than %s", humanBytes(lim.fileBytes))})
			continue
		case stopped || total+e.size > lim.totalBytes || len(want) >= lim.maxFiles:
			stopped = true
			s.skipped = append(s.skipped, SkippedFile{e.path, "past the snapshot's size limit"})
			continue
		}
		total += e.size
		want = append(want, e)
	}
	f, err := os.Create(out)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gz, _ := gzip.NewWriterLevel(f, gzip.BestCompression)
	gz.Header = gzip.Header{OS: 255} // no name, no time: the same bytes every time
	tw := tar.NewWriter(gz)
	byBlob := map[string][]treeEntry{}
	var shas []string
	for _, e := range want {
		if _, ok := byBlob[e.sha]; !ok {
			shas = append(shas, e.sha)
		}
		byBlob[e.sha] = append(byBlob[e.sha], e)
	}
	blobs := map[string][]byte{}
	// Blobs arrive in request order; files are written in path order, so keep
	// them until every file is known (bounded by the snapshot's size cap).
	if err := readBlobs(ctx, ts, bare, shas, func(sha string, data []byte) error {
		blobs[sha] = data
		return nil
	}); err != nil {
		return nil, err
	}
	for _, e := range want {
		data, ok := blobs[e.sha]
		if !ok {
			s.skipped = append(s.skipped, SkippedFile{e.path, "missing from what GitHub sent"})
			continue
		}
		switch {
		case isLFSPointer(data):
			s.skipped = append(s.skipped, SkippedFile{e.path, "stored with Git LFS (not downloaded)"})
			continue
		case isBinaryData(data):
			s.skipped = append(s.skipped, SkippedFile{e.path, "a file that isn't text"})
			continue
		}
		hdr := &tar.Header{Name: e.path, Mode: 0o644, Size: int64(len(data)), ModTime: at.UTC().Truncate(time.Second), Typeflag: tar.TypeReg, Format: tar.FormatPAX}
		if e.mode == "100755" {
			hdr.Mode = 0o755
		}
		if err := tw.WriteHeader(hdr); err != nil {
			return nil, err
		}
		if _, err := tw.Write(data); err != nil {
			return nil, err
		}
		lang, _ := LanguageOf(e.path)
		s.files = append(s.files, SnapshotFile{Path: e.path, Bytes: int64(len(data)), Language: lang})
		s.bytes += int64(len(data))
		if lang != "" && !proseOrData[lang] && !vendoredDir.MatchString(e.path) {
			s.langs[lang] += int64(len(data))
		}
		if isReadme(e.path) && s.readme == "" {
			s.readme, s.readmeAt = string(data), e.path
		}
		if isLicenseFile(e.path) && s.license == "" {
			s.license = LicenseFromText(string(data))
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	s.tarPath = out
	return s, nil
}

// safeRepoPath refuses names a checkout couldn't hold safely.
func safeRepoPath(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || len(p) > 1024 {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.EqualFold(seg, ".git") {
			return false
		}
	}
	return path.Clean(p) == p
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// skippedWords sums up what a snapshot left out ("12 pictures and other
// binary files, 2 files larger than 512 KB").
func skippedWords(sk []SkippedFile) []string {
	n := map[string]int{}
	var order []string
	for _, s := range sk {
		if n[s.Why] == 0 {
			order = append(order, s.Why)
		}
		n[s.Why]++
	}
	var out []string
	for _, why := range order {
		if n[why] == 1 {
			out = append(out, "1 file: "+why)
		} else {
			out = append(out, fmt.Sprintf("%d files: %s", n[why], why))
		}
	}
	return out
}

// ---- history and what GitHub says ----

// gitHistory reads recent commits from a deeper (blobless) fetch.
func gitHistory(ctx context.Context, ts *tools.Set, bare string, r RepoRef, commit string) ([]models.RepoCommit, []models.RepoContributor) {
	_, _ = git(ctx, ts, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=40", "--filter=blob:none", "--", cloneURL(r), commit)
	var out bytes.Buffer
	if _, err := git(ctx, ts, bare, nil, &out, "log", "-n", "40", "--format=%H%x1f%an%x1f%aI%x1f%s", commit); err != nil {
		return nil, nil
	}
	var recent []models.RepoCommit
	count := map[string]int{}
	var order []string
	for _, line := range strings.Split(strings.TrimSpace(out.String()), "\n") {
		f := strings.Split(line, "\x1f")
		if len(f) != 4 {
			continue
		}
		at, _ := time.Parse(time.RFC3339, f[2])
		if len(recent) < 12 {
			recent = append(recent, models.RepoCommit{SHA: f[0], Author: clip(f[1], 80), At: at.UTC(), Subject: clip(f[3], 200)})
		}
		if count[f[1]] == 0 {
			order = append(order, f[1])
		}
		count[f[1]]++
	}
	var people []models.RepoContributor
	for _, name := range order {
		people = append(people, models.RepoContributor{Name: clip(name, 80), Commits: count[name]})
	}
	sort.SliceStable(people, func(i, j int) bool { return people[i].Commits > people[j].Commits })
	if len(people) > 10 {
		people = people[:10]
	}
	return recent, people
}

// ghFacts is what GitHub says about a repository (gh api, the person's own sign-in).
type ghFacts struct {
	ok            bool
	description   string
	defaultBranch string
	stars, forks  int
	private       bool
	license       string
	topics        []string
	languages     map[string]int64
	contributors  []models.RepoContributor
	recent        []models.RepoCommit
}

// ghJSON runs `gh api <path>` (a GET) and decodes the answer.
func ghJSON(ctx context.Context, ts *tools.Set, p string, v any) error {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var out bytes.Buffer
	_, err := ts.Run(cctx, tools.Spec{Tool: tools.GH, Args: []string{"api", "--method", "GET", "-H", "Accept: application/vnd.github+json", p}, Stdout: &out,
		Env: []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1", "NO_COLOR=1"}})
	if err != nil {
		return err
	}
	return json.Unmarshal(out.Bytes(), v)
}

// githubFacts asks GitHub (through gh) for the description, languages,
// contributors and history. ok is false when gh isn't there or can't answer.
func githubFacts(ctx context.Context, ts *tools.Set, r RepoRef, commit string) ghFacts {
	var f ghFacts
	if gitBase() != "" || !ts.Has(tools.GH) || r.Host != "github.com" {
		return f
	}
	base := "repos/" + r.Owner + "/" + r.Name
	var info struct {
		Description   string   `json:"description"`
		DefaultBranch string   `json:"default_branch"`
		Stars         int      `json:"stargazers_count"`
		Forks         int      `json:"forks_count"`
		Private       bool     `json:"private"`
		Topics        []string `json:"topics"`
		License       *struct {
			SPDX string `json:"spdx_id"`
		} `json:"license"`
	}
	if err := ghJSON(ctx, ts, base, &info); err != nil {
		return f
	}
	f.ok = true
	f.description, f.defaultBranch, f.stars, f.forks, f.private = clip(info.Description, 400), info.DefaultBranch, info.Stars, info.Forks, info.Private
	if len(info.Topics) > 12 {
		info.Topics = info.Topics[:12]
	}
	f.topics = info.Topics
	if info.License != nil && info.License.SPDX != "" && info.License.SPDX != "NOASSERTION" {
		f.license = info.License.SPDX
	}
	_ = ghJSON(ctx, ts, base+"/languages", &f.languages)
	var people []struct {
		Login         string `json:"login"`
		Contributions int    `json:"contributions"`
	}
	if ghJSON(ctx, ts, base+"/contributors?per_page=10", &people) == nil {
		for _, p := range people {
			f.contributors = append(f.contributors, models.RepoContributor{Login: clip(p.Login, 60), Commits: p.Contributions})
		}
	}
	var commits []struct {
		SHA    string `json:"sha"`
		Commit struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
		} `json:"commit"`
	}
	if ghJSON(ctx, ts, base+"/commits?per_page=12&sha="+url.QueryEscape(commit), &commits) == nil {
		for _, c := range commits {
			f.recent = append(f.recent, models.RepoCommit{SHA: c.SHA, Subject: clip(firstLine(c.Commit.Message), 200), Author: clip(c.Commit.Author.Name, 80), At: c.Commit.Author.Date.UTC()})
		}
	}
	return f
}

func clip(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// languagesOf turns byte counts into shares, largest first (top 8).
func languagesOf(m map[string]int64) []models.RepoLanguage {
	var total int64
	for _, n := range m {
		total += n
	}
	var out []models.RepoLanguage
	for name, n := range m {
		if n <= 0 {
			continue
		}
		out = append(out, models.RepoLanguage{Name: name, Bytes: n, Share: float64(n) / float64(max(total, 1))})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Bytes != out[j].Bytes {
			return out[i].Bytes > out[j].Bytes
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 8 {
		out = out[:8]
	}
	for i := range out {
		out[i].Share = float64(int(out[i].Share*1000+0.5)) / 1000
	}
	return out
}

// ---- the fetch job ----

// FetchRepository runs a repository fetch: resolve the commit, fetch it
// shallowly into a fresh bare repository, keep its text files (the snapshot),
// learn what GitHub says, and add the repository resource (or a new version
// of it). A newer commit also makes new versions of the code slides and
// stats cards made from it (cuts move to them only with cut update).
func FetchRepository(ctx context.Context, a *app.App, req FetchRequest, progress func(float64, string)) (*FetchResult, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	r, ok := ParseRepoURL(req.URL)
	if !ok {
		return nil, permanentf("%s isn't a GitHub repository's address.", req.URL)
	}
	if req.Ref != "" {
		if !refSafe.MatchString(req.Ref) || strings.HasPrefix(req.Ref, "-") {
			return nil, permanentf("%q can't be a branch, tag or commit.", req.Ref)
		}
		r.Ref = req.Ref
	}
	if !a.Tools.Has(tools.Git) {
		return nil, permanentf("Bringing in a GitHub repository needs git, which isn't on this computer. Settings → Components says how to add it.")
	}
	work, err := makeTemp(a)
	if err != nil {
		return nil, err
	}
	defer removeAll(work)
	bare := filepath.Join(work, "repo.git")
	progress(0.05, "finding the commit")
	commit, branch, err := resolveCommit(ctx, a.Tools, work, r)
	if err != nil {
		return nil, err
	}
	// Already pinned at this commit with the same limits: nothing new.
	target, err := repoTarget(ctx, a, req, r)
	if err != nil {
		return nil, err
	}
	if _, err := git(ctx, a.Tools, work, nil, nil, "init", "--bare", "--quiet", bare); err != nil {
		return nil, fmt.Errorf("MetaRush couldn't make a place to put the repository: %w", err)
	}
	progress(0.15, "fetching "+r.Owner+"/"+r.Name+" at "+models.ShortSHA(commit))
	if _, err := git(ctx, a.Tools, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", cloneURL(r), commit); err != nil {
		if fullSHA.MatchString(r.Ref) {
			return nil, gitProblem(r, err) // exactly that commit, or nothing
		}
		// A server that only hands out named refs: fetch the branch or tag
		// (the default branch when none was asked) and pin to where it is now.
		want := r.Ref
		if want == "" {
			want = "HEAD"
		}
		if _, err2 := git(ctx, a.Tools, bare, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", cloneURL(r), want); err2 != nil {
			return nil, gitProblem(r, err2)
		}
		var head bytes.Buffer
		if _, err := git(ctx, a.Tools, bare, nil, &head, "rev-parse", "FETCH_HEAD^{commit}"); err == nil && fullSHA.MatchString(strings.TrimSpace(head.String())) {
			commit = strings.TrimSpace(head.String())
		}
	}
	if req.PicturesOnly {
		// Its picture paths changed (D216): the pictures at the commit it shows.
		return keepPicturesAt(ctx, a, bare, r, req, target, commit, filepath.Join(work, "pictures"), progress)
	}
	var meta bytes.Buffer
	committedAt := time.Now().UTC()
	subject := ""
	if _, err := git(ctx, a.Tools, bare, nil, &meta, "log", "-n", "1", "--format=%cI%x1f%s", commit); err == nil {
		if f := strings.SplitN(strings.TrimSpace(meta.String()), "\x1f", 2); len(f) == 2 {
			if t, err := time.Parse(time.RFC3339, f[0]); err == nil {
				committedAt = t.UTC()
			}
			subject = clip(f[1], 200)
		}
	}
	progress(0.35, "keeping its files")
	lim := repoLimits{fileBytes: a.Cfg.Resources.Repos.FileBytes(), totalBytes: a.Cfg.Resources.Repos.TotalBytes(), maxFiles: a.Cfg.Resources.Repos.MaxFiles()}
	snap, err := buildSnapshot(ctx, a.Tools, bare, commit, committedAt, lim, filepath.Join(work, "snapshot.tar.gz"))
	if err != nil {
		return nil, gitProblem(r, err)
	}
	if len(snap.files) == 0 {
		return nil, permanentf("%s/%s at %s has no text files MetaRush can keep.", r.Owner, r.Name, models.ShortSHA(commit))
	}
	progress(0.6, "reading its history")
	gh := githubFacts(ctx, a.Tools, r, commit)
	rs := &models.RepoSnapshot{Host: r.Host, Owner: r.Owner, Name: r.Name, Ref: r.Ref, Commit: commit, CommittedAt: committedAt, Subject: subject,
		Files: len(snap.files), Bytes: snap.bytes, Skipped: len(snap.skipped), SkippedWhy: skippedWords(snap.skipped), DefaultBranch: branch}
	if snap.readme != "" {
		rs.Readme = clip(stripMarkup(snap.readme), 1500)
	}
	rs.License = snap.license
	rs.Languages = languagesOf(snap.langs)
	if gh.ok {
		rs.History = "gh"
		rs.Description, rs.Stars, rs.Forks, rs.Private, rs.Topics = gh.description, gh.stars, gh.forks, gh.private, gh.topics
		if gh.defaultBranch != "" {
			rs.DefaultBranch = gh.defaultBranch
		}
		if gh.license != "" {
			rs.License = gh.license
		}
		if len(gh.languages) > 0 {
			rs.Languages = languagesOf(gh.languages)
		}
		rs.Contributors, rs.Recent = gh.contributors, gh.recent
	}
	if len(rs.Recent) == 0 {
		rs.Recent, rs.Contributors = gitHistory(ctx, a.Tools, bare, r, commit)
		if len(rs.Recent) > 0 {
			rs.History = "git"
		}
	}
	progress(0.75, "saving the snapshot")
	ov := RepoOverview{Repo: r.Site(), Commit: commit, CommittedAt: committedAt, Description: rs.Description, License: rs.License, Languages: rs.Languages,
		Files: snap.files, Skipped: snap.skipped, Recent: rs.Recent, Contributors: rs.Contributors, History: rs.History, ReadmePath: snap.readmeAt}
	if len(ov.Skipped) > 500 {
		ov.Skipped = ov.Skipped[:500]
	}
	if snap.readme != "" {
		ov.Readme = snap.readme
		if len(ov.Readme) > 64<<10 {
			ov.Readme = ov.Readme[:64<<10]
		}
	}
	ob, _ := json.Marshal(ov)
	if obj, err := putBytes(ctx, a, work, ob, "application/json"); err == nil {
		rs.Overview = obj
	}
	res, err := storeRepoVersion(ctx, a, req, r, target, rs, snap)
	if err != nil {
		return nil, err
	}
	// The pictures under its picture paths (D216), kept with the commit.
	if err := keepPicturesOf(ctx, a, bare, r, req, res, filepath.Join(work, "pictures"), progress); err != nil {
		return nil, err
	}
	if res.Statuses[0] == StatusVersion {
		progress(0.85, "making its slides again from the newer commit")
		notes, err := RegenerateFromRepo(ctx, a, res.Resources[0])
		if err != nil {
			res.Notes = append(res.Notes, "Its slides couldn't be made again: "+apperr.From(err).Message)
		}
		res.Notes = append(res.Notes, notes...)
	}
	progress(1, res.Message)
	return res, nil
}

// repoTarget finds the repository resource a fetch updates: the one asked
// for (refresh), else the same repository in the same place.
func repoTarget(ctx context.Context, a *app.App, req FetchRequest, r RepoRef) (*models.Resource, error) {
	if req.ResourceID != "" {
		var x models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), req.ResourceID, &x); err != nil {
			return nil, permanentf("The repository to refresh was removed.")
		}
		return &x, nil
	}
	filter := bson.M{"kind": models.ResourceRepo, "removed": bson.M{"$ne": true}, "versions.repo.owner": r.Owner, "versions.repo.name": r.Name}
	if req.ProjectID == "" {
		filter["scope"] = models.ScopeWorkspace
	} else {
		filter["scope"], filter["projectId"] = models.ScopeProject, req.ProjectID
	}
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &list); err != nil || len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// storeRepoVersion stores the snapshot and adds the resource or a version.
func storeRepoVersion(ctx context.Context, a *app.App, req FetchRequest, r RepoRef, target *models.Resource, rs *models.RepoSnapshot, snap *snapshot) (*FetchResult, error) {
	sha, err := media.HashFile(snap.tarPath)
	if err != nil {
		return nil, err
	}
	st, _ := os.Stat(snap.tarPath)
	now := time.Now().UTC()
	actor := a.Actor
	out := &FetchResult{version: sha}
	if target != nil {
		if v := target.Version(sha); v != nil {
			out.Resources, out.Statuses = []*models.Resource{target}, []string{StatusSame}
			out.Message = fmt.Sprintf("%s already has this version (%s).", target.Title, models.ShortSHA(rs.Commit))
			return out, nil
		}
	}
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if err := putPrepared(ctx, a, snap.tarPath, key, sha, "application/gzip"); err != nil {
		return nil, err
	}
	_, _ = a.Cache.Adopt(snap.tarPath, sha)
	v := models.ResourceVersion{SHA256: sha, Size: st.Size(), MIME: "application/gzip", Ext: "tar.gz", StorageKey: key, Stored: true, Repo: rs, CreatedAt: now, CreatedBy: actor,
		Source: models.ResourceSource{Kind: models.FromRepo, Name: r.Owner + "/" + r.Name, URL: r.URL(), Site: r.Site(), Commit: rs.Commit, License: rs.License, Author: r.Owner,
			Machine: a.Cfg.Installation.Name, At: now}}
	v.Text = clip(strings.Join([]string{rs.Description, rs.Readme}, " "), 2000)
	tags := []models.ResourceTag{{Tag: "code", Source: "repository", Confidence: 0.9, Why: "it is a code repository"}}
	for i, l := range rs.Languages {
		if i >= 3 || l.Share < 0.1 {
			break
		}
		tags = append(tags, models.ResourceTag{Tag: strings.ToLower(l.Name), Source: "repository", Confidence: 0.8, Why: fmt.Sprintf("%.0f%% of its code is %s", l.Share*100, l.Name)})
	}
	if target != nil {
		upd := bson.M{"$push": bson.M{"versions": v}, "$set": bson.M{"current": sha, "updatedAt": now, "autoTags": mergeAuto(target.AutoTags, tags)}}
		if rs.Description != "" && target.Description == "" {
			upd["$set"].(bson.M)["description"] = rs.Description
		}
		if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": target.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": bson.M{"$ne": sha}}, upd); err != nil {
			return nil, err
		}
		_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), target.ID, target)
		a.Audit(ctx, "resource.version", "resource", target.ID, target.ProjectID, map[string]any{"sha256": sha, "commit": rs.Commit, "via": actor.Via})
		out.Resources, out.Statuses = []*models.Resource{target}, []string{StatusVersion}
		out.Message = fmt.Sprintf("Brought %s up to its newer version %s (%s). Cuts keep what they show until you update them.", target.Title, models.ShortSHA(rs.Commit), plainDate(rs.CommittedAt))
		return out, nil
	}
	scope := models.ScopeWorkspace
	if req.ProjectID != "" {
		scope = models.ScopeProject
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = Slug(r.Name)
	} else if !NamePattern.MatchString(name) {
		return nil, permanentf("%q can't be a resource's short name: use lowercase letters, digits, - and _, starting with a letter.", name)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = r.Owner + "/" + r.Name
	}
	res := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: name, Title: title, Kind: models.ResourceRepo, Scope: scope,
		Versions: []models.ResourceVersion{v}, Current: sha, AutoTags: tags, Description: rs.Description, CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
	if scope == models.ScopeProject {
		res.ProjectID = req.ProjectID
	}
	if err := insertUnique(ctx, a, &res, req.Name != ""); err != nil {
		return nil, err
	}
	a.Audit(ctx, "resource.add", "resource", res.ID, res.ProjectID, map[string]any{"name": res.Name, "kind": res.Kind, "sha256": sha, "commit": rs.Commit, "scope": scope, "via": actor.Via})
	out.Resources, out.Statuses = []*models.Resource{&res}, []string{StatusAdded}
	out.Message = fmt.Sprintf("Added %s as it is at %s (%s): %d files", title, models.ShortSHA(rs.Commit), plainDate(rs.CommittedAt), rs.Files)
	if rs.Skipped > 0 {
		out.Message += fmt.Sprintf(", %d left out (large files and files that aren't text)", rs.Skipped)
	}
	out.Message += "."
	return out, nil
}

func plainDate(t time.Time) string {
	if t.IsZero() {
		return "date unknown"
	}
	return t.UTC().Format("2 Jan 2006")
}

var (
	mdImage   = regexp.MustCompile(`!\[[^\]]*\]\([^)]*\)`)
	mdLink    = regexp.MustCompile(`\[([^\]]*)\]\([^)]*\)`)
	htmlTag   = regexp.MustCompile(`<[^>]{0,400}>`)
	mdHeading = regexp.MustCompile(`(?m)^#{1,6}\s*`)
	mdBadge   = regexp.MustCompile(`\[!\[[^\]]*\]\([^)]*\)\]\([^)]*\)`)
)

// stripMarkup turns a README's start into plain words for the overview.
func stripMarkup(s string) string {
	s = mdBadge.ReplaceAllString(s, "")
	s = mdImage.ReplaceAllString(s, "")
	s = mdLink.ReplaceAllString(s, "$1")
	s = htmlTag.ReplaceAllString(s, "")
	s = mdHeading.ReplaceAllString(s, "")
	s = strings.NewReplacer("**", "", "__", "", "`", "").Replace(s)
	return s
}
