package remotion

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Sending an approved version's changes back to the repository it came from
// (D176): the plan says the changes in plain words and where they go; a
// person starts it; a job pinned to this computer writes one commit with
// git's plumbing on a fresh bare repository and pushes it to a new branch —
// never forced, never the default branch — and opens a pull request on GitHub.

// RemotionPushIn is a push back as a person would start it.
type RemotionPushIn struct {
	Source    string `json:"source" jsonschema:"minLength=1,description=name or name@version (default: its newest approved version)."`
	ProjectID string `json:"projectId,omitempty"`
	Branch    string `json:"branch,omitempty" jsonschema:"maxLength=200,description=The new branch (default: metarush/<name>-v<N>)."`
	NoPR      bool   `json:"noPr,omitempty" jsonschema:"description=Only the branch: no pull request (GitHub)."`
	Words     bool   `json:"words,omitempty" jsonschema:"description=Write its current word settings into the code (else the code keeps its own words)."`
}

// RemotionPushStartIn starts the plan a person looked at.
type RemotionPushStartIn struct {
	RemotionPushIn
	Plan string `json:"plan" jsonschema:"minLength=16,description=The plan's fingerprint (from remotion.push.plan)."`
}

// RemotionPushPlanOut is what a person sees before changes go back.
type RemotionPushPlanOut struct {
	Name          string                         `json:"name"`
	Title         string                         `json:"title"`
	SourceID      string                         `json:"sourceId"`
	ProjectID     string                         `json:"projectId"`
	Version       string                         `json:"version"`
	VersionNumber int                            `json:"versionNumber"`
	FromNumber    int                            `json:"fromNumber" jsonschema:"description=The version it compares with: what was brought in."`
	Base          string                         `json:"base" jsonschema:"description=The original commit."`
	Target        models.RemotionPushTarget      `json:"target"`
	Branch        string                         `json:"branch"`
	Parent        string                         `json:"parent,omitempty" jsonschema:"description=The commit the new one goes on."`
	Extends       bool                           `json:"extends" jsonschema:"description=The branch is one MetaRush made: the new commit goes on top of it."`
	PR            bool                           `json:"pr"`
	Words         bool                           `json:"words"`
	Changes       []models.RemotionFileChange    `json:"changes"`
	WordChanges   []models.RemotionWordChange    `json:"wordChanges"`
	Packages      []models.RemotionPackageChange `json:"packages"`
	Helper        string                         `json:"helper,omitempty" jsonschema:"description=What happens to MetaRush's words helper\\, in plain words."`
	Summary       string                         `json:"summary" jsonschema:"description=The changes in plain words."`
	Problems      []string                       `json:"problems"`
	Ready         bool                           `json:"ready"`
	Fingerprint   string                         `json:"fingerprint"`
	Message       string                         `json:"message"`
	Next          string                         `json:"next"`
}

// RemotionPushStartOut says what started.
type RemotionPushStartOut struct {
	Message string `json:"message"`
	ID      string `json:"id"`
	Number  int    `json:"number"`
	JobID   string `json:"jobId"`
	Status  string `json:"status" jsonschema:"enum=queued"`
}

// RemotionPushListIn lists a source's pushes.
type RemotionPushListIn struct {
	Source    string `json:"source" jsonschema:"minLength=1"`
	ProjectID string `json:"projectId,omitempty"`
}

// RemotionPushRow is one push back.
type RemotionPushRow struct {
	ID            string                      `json:"id"`
	Number        int                         `json:"number"`
	State         string                      `json:"state"`
	StateLabel    string                      `json:"stateLabel"`
	VersionNumber int                         `json:"versionNumber"`
	Version       string                      `json:"version"`
	Target        models.RemotionPushTarget   `json:"target"`
	Branch        string                      `json:"branch"`
	Commit        string                      `json:"commit,omitempty"`
	PRURL         string                      `json:"prUrl,omitempty"`
	BranchURL     string                      `json:"branchUrl,omitempty"`
	Changes       []models.RemotionFileChange `json:"changes"`
	Words         bool                        `json:"words"`
	Summary       string                      `json:"summary,omitempty"`
	Error         string                      `json:"error,omitempty"`
	By            models.Actor                `json:"by"`
	Machine       string                      `json:"machine"`
	JobID         string                      `json:"jobId,omitempty"`
	CreatedAt     time.Time                   `json:"createdAt"`
	FinishedAt    *time.Time                  `json:"finishedAt,omitempty"`
}

// RemotionPushListOut lists pushes, newest first.
type RemotionPushListOut struct {
	Name   string            `json:"name"`
	Title  string            `json:"title"`
	Pushes []RemotionPushRow `json:"pushes"`
}

func init() {
	ops.Def("remotion.push.plan", "Show what sending an approved version's changes back to the Remotion project's repository would do: the changes in plain words (files, words, packages), where they go, the new branch and the pull request", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionPushIn) (*RemotionPushPlanOut, error) {
			p, _, err := planPush(ctx, a, in)
			return p, err
		})
	ops.Def("remotion.push.start", "Send an approved version's changes back to the Remotion project's repository as a new branch, and a pull request on GitHub — people only; never forced, never the default branch", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionPushStartIn) (*RemotionPushStartOut, error) {
			return startPush(ctx, a, in)
		})
	ops.Def("remotion.push.list", "List the changes sent back to a Remotion project's repository: versions, branches, pull requests", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionPushListIn) (*RemotionPushListOut, error) {
			s, err := refreshSource(ctx, a, in.ProjectID, in.Source)
			if err != nil {
				return nil, err
			}
			ps, err := pushesOf(ctx, a, s.ID, 50)
			if err != nil {
				return nil, err
			}
			out := &RemotionPushListOut{Name: s.Name, Title: s.Title, Pushes: []RemotionPushRow{}}
			for i := range ps {
				out.Pushes = append(out.Pushes, pushRow(&ps[i]))
			}
			return out, nil
		})
}

// ---- git, for pushing ----

// pushGit runs git for a push back: no hooks, no prompts (the person's
// credential helper or SSH key answers, or it fails plainly), only https,
// ssh and — for a repository on this computer — local paths, and no
// automatic maintenance left running in the background after it returns.
func pushGit(ctx context.Context, ts *tools.Set, dir string, local bool, env []string, stdin []byte, stdout *bytes.Buffer, args ...string) error {
	cfg := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "protocol.allow=never", "-c", "protocol.https.allow=always", "-c", "protocol.ssh.allow=always",
		"-c", "core.fsmonitor=false", "-c", "core.askPass=", "-c", "credential.interactive=never", "-c", "transfer.fsckObjects=true", "-c", "commit.gpgsign=false",
		"-c", "push.followTags=false", "-c", "advice.detachedHead=false", "-c", "maintenance.auto=false", "-c", "gc.auto=0"}
	if local {
		cfg = append(cfg, "-c", "protocol.file.allow=always")
	}
	e := append([]string{"GIT_TERMINAL_PROMPT=0", "GIT_ASKPASS=" + falseCmd(), "SSH_ASKPASS=" + falseCmd(), "GCM_INTERACTIVE=never", "GIT_LFS_SKIP_SMUDGE=1",
		"GIT_SSH_COMMAND=ssh -o BatchMode=yes"}, env...)
	var in *bytes.Reader
	spec := tools.Spec{Tool: tools.Git, Dir: dir, Args: append(cfg, args...), Env: e}
	if stdin != nil {
		in = bytes.NewReader(stdin)
		spec.Stdin = in
	}
	if stdout != nil {
		spec.Stdout = stdout
	}
	_, err := ts.Run(ctx, spec)
	return err
}

func falseCmd() string {
	for _, p := range []string{"/usr/bin/false", "/bin/false"} {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "false"
}

// gitStderr is what git said when it failed.
func gitStderr(err error) string {
	var ef *tools.ErrFailed
	if errors.As(err, &ef) {
		return strings.TrimSpace(ef.Stderr)
	}
	if err != nil {
		return err.Error()
	}
	return ""
}

// ---- where it goes ----

var githubSSH = regexp.MustCompile(`^(?:ssh://)?git@github\.com[:/]([A-Za-z0-9_.-]+)/([A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// pushTarget finds where a source's changes go back to.
func pushTarget(ctx context.Context, a *app.App, s *models.RemotionSource) (models.RemotionPushTarget, bool, string) {
	o := s.Origin
	switch o.Kind {
	case models.RemotionFromGitHub:
		r, ok := resources.ParseRepoURL(o.URL)
		if !ok {
			return models.RemotionPushTarget{}, false, "MetaRush can't read the address of the repository it came from."
		}
		u := resources.CloneURL(r)
		return models.RemotionPushTarget{Kind: models.RemotionPushGitHub, Where: r.Site(), URL: u, Repo: r.Owner + "/" + r.Name}, isLocalURL(u), ""
	case models.RemotionFromFolder:
		if o.Machine != a.Cfg.Installation.Name {
			return models.RemotionPushTarget{}, false, fmt.Sprintf("Its folder is on %s: send the changes back from there.", firstNonEmpty(o.Machine, "another computer"))
		}
		dir := expandHome(o.Path)
		if o.Commit == "" || !dirExists(filepath.Join(dir, ".git")) {
			return models.RemotionPushTarget{}, false, fmt.Sprintf("The folder %s wasn't a git repository when it was brought in, so there is no repository to send changes back to.", o.Folder)
		}
		var out bytes.Buffer
		if err := pushGit(ctx, a.Tools, dir, true, nil, nil, &out, "--no-optional-locks", "config", "--get", "remote.origin.url"); err == nil {
			raw := strings.TrimSpace(out.String())
			if raw != "" {
				if u, err := url.Parse(raw); err == nil && u.User != nil {
					if _, has := u.User.Password(); has {
						return models.RemotionPushTarget{}, false, "The folder's remote has a password in its address; MetaRush won't push with it."
					}
				}
				if r, ok := githubRepo(raw); ok {
					push := raw
					if os.Getenv("METARUSH_GIT_BASE") != "" {
						push = resources.CloneURL(r)
					}
					return models.RemotionPushTarget{Kind: models.RemotionPushGitHub, Where: r.Site(), URL: push, Repo: r.Owner + "/" + r.Name}, isLocalURL(push), ""
				}
				return models.RemotionPushTarget{Kind: models.RemotionPushGit, Where: remoteWhere(raw), URL: raw}, isLocalURL(raw), ""
			}
		}
		return models.RemotionPushTarget{Kind: models.RemotionPushFolder, Where: "the folder " + o.Folder + " on " + o.Machine, URL: dir}, true, ""
	}
	return models.RemotionPushTarget{}, false, "MetaRush doesn't know where this project came from."
}

// githubRepo reads a GitHub remote (https or ssh).
func githubRepo(raw string) (resources.RepoRef, bool) {
	if m := githubSSH.FindStringSubmatch(raw); m != nil {
		return resources.RepoRef{Host: "github.com", Owner: m[1], Name: m[2]}, true
	}
	if strings.Contains(raw, "github.com") {
		return resources.ParseRepoURL(strings.TrimSuffix(raw, ".git"))
	}
	return resources.RepoRef{}, false
}

// remoteWhere says where a remote is, in a few words: its address, or for a
// repository on this computer "the repository teaser.git on this computer".
func remoteWhere(raw string) string {
	if isLocalURL(raw) {
		p := strings.TrimPrefix(raw, "file://")
		return "the repository " + filepath.Base(strings.TrimSuffix(p, "/")) + " on this computer"
	}
	return cleanRemote(raw)
}

// isLocalURL reports a repository on this computer (a path or file://).
func isLocalURL(u string) bool {
	return strings.HasPrefix(u, "/") || strings.HasPrefix(u, "file://") || strings.HasPrefix(u, "~/")
}

// reachProblem says in plain words why a remote couldn't be read: GitHub's
// advice (sign in with gh) for GitHub, what the remote said otherwise.
func reachProblem(t models.RemotionPushTarget, err error) string {
	if t.Kind == models.RemotionPushGitHub {
		if r, ok := githubRepo("https://github.com/" + t.Repo); ok {
			msg := resources.GitProblem(r, err).Error()
			return upperFirst(strings.TrimSuffix(msg, ".")) + "."
		}
	}
	why := strings.TrimSpace(strings.TrimPrefix(err.Error(), "fatal:"))
	return fmt.Sprintf("MetaRush couldn't reach %s (%s); this computer uses your own sign-in for it.", t.Where, clipText(why, 200))
}

// remoteRefs reads a remote's default branch and one branch's tip.
func remoteRefs(ctx context.Context, a *app.App, t models.RemotionPushTarget, local bool, branch string) (def, tip string, err error) {
	dir, err := os.MkdirTemp("", "metarush-push-refs-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(dir)
	var out bytes.Buffer
	if err := pushGit(ctx, a.Tools, dir, local, nil, nil, &out, "ls-remote", "--symref", "--", t.URL, "HEAD", "refs/heads/"+branch); err != nil {
		return "", "", fmt.Errorf("%s", firstLine(gitStderr(err)))
	}
	for _, line := range strings.Split(out.String(), "\n") {
		f := strings.Fields(line)
		switch {
		case len(f) == 3 && f[0] == "ref:" && f[2] == "HEAD":
			def = strings.TrimPrefix(f[1], "refs/heads/")
		case len(f) == 2 && f[1] == "refs/heads/"+branch && resources.FullSHA(f[0]):
			tip = f[0]
		}
	}
	return def, tip, nil
}

var branchName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,199}$`)

// checkBranch says why a branch name can't be used ("" = it can).
func checkBranch(b, def string) string {
	switch {
	case !branchName.MatchString(b) || strings.Contains(b, "..") || strings.Contains(b, "//") || strings.HasSuffix(b, "/") || strings.HasSuffix(b, ".lock") || strings.HasSuffix(b, "."):
		return fmt.Sprintf("“%s” can't be a branch name: use letters, digits, dashes, dots and slashes (like metarush/teaser-v4).", clipText(b, 60))
	case b == def || b == "main" || b == "master" || b == "HEAD":
		return fmt.Sprintf("MetaRush never sends changes to %s, the main branch: they go to a new branch you can look at and merge.", b)
	}
	return ""
}

// ---- what goes back ----

// latestImport is the newest version brought in from the original (what the
// original commit holds, as MetaRush took it in).
func latestImport(ctx context.Context, a *app.App, s *models.RemotionSource) (*models.RemotionVersion, error) {
	var vs []models.RemotionVersion
	if err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), bson.M{"sourceId": s.ID, "kind": models.RemotionKindImport}, &vs,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1)); err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, apperr.New(apperr.NotFound, "“%s” has no version that was brought in.", s.Title)
	}
	return &vs[0], nil
}

// pushFile is a file of what goes back: its bytes in memory (unwrapped
// words) or an object.
type pushFile struct {
	ManifestFile
	text []byte
}

// pushFiles are the files a version sends back: its own, with MetaRush's
// words helper unwrapped (and its word settings written in when asked) and its
// pictures helper unwrapped (D209: the code's own pictures go back).
func pushFiles(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, m *Manifest, words bool) ([]pushFile, string, error) {
	var out []pushFile
	if v.WordsHelper == "" && v.PicturesHelper == "" {
		for _, f := range m.Files {
			out = append(out, pushFile{ManifestFile: f})
		}
		return out, "", nil
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, "", err
	}
	defer release()
	work, done, err := workTree(ctx, a, v, m, "")
	if err != nil {
		return nil, "", err
	}
	defer done()
	opts := map[string]any{"values": map[string]string{}}
	if words {
		opts["values"] = wordValues(s, v)
	}
	of, err := os.CreateTemp(a.Cfg.Scratch.Dir, "remotion-bake-*.json")
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(of.Name())
	b, _ := json.Marshal(opts)
	_, _ = of.Write(b)
	_ = of.Close()
	var res struct {
		Type    string `json:"type"`
		Message string `json:"message"`
		Files   []struct {
			Path string `json:"path"`
			Text string `json:"text"`
		} `json:"files"`
		Removed []string `json:"removed"`
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	_, rerr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Args: []string{"render/source/analyze.mjs", "bake", "--work", work, "--options", of.Name()}, MaxLine: 64 << 20,
		OnLine: func(l string) {
			var ev struct {
				Type string `json:"type"`
			}
			if json.Unmarshal([]byte(l), &ev) == nil && (ev.Type == "bake" || ev.Type == "error") {
				_ = json.Unmarshal([]byte(l), &res)
			}
		}})
	if res.Type != "bake" {
		msg := res.Message
		if msg == "" && rerr != nil {
			msg = rerr.Error()
		}
		return nil, "", fmt.Errorf("MetaRush couldn't take its helpers out of the code: %s", firstLine(msg))
	}
	removed := map[string]bool{}
	for _, p := range res.Removed {
		removed[p] = true
	}
	changed := map[string][]byte{}
	for _, f := range res.Files {
		if !safeRel(f.Path) {
			return nil, "", fmt.Errorf("the unwrapped code names an unusual file (%s)", f.Path)
		}
		changed[f.Path] = []byte(f.Text)
	}
	for _, f := range m.Files {
		switch {
		case removed[f.Path]:
		case changed[f.Path] != nil:
			t := changed[f.Path]
			out = append(out, pushFile{ManifestFile: ManifestFile{Path: f.Path, Size: int64(len(t)), SHA256: sha256Hex(t)}, text: t})
		default:
			out = append(out, pushFile{ManifestFile: f})
		}
	}
	var notes []string
	if v.WordsHelper != "" {
		note := "MetaRush's words helper stays here: its words go back as plain code, as the code writes them."
		if words {
			note = "MetaRush's words helper stays here: its words go back as plain code, with today's word settings written in."
		}
		notes = append(notes, note)
	}
	if v.PicturesHelper != "" {
		notes = append(notes, "MetaRush's pictures helper stays here: its pictures go back as the code had them (pictures replaced in MetaRush stay in MetaRush).")
	}
	return out, strings.Join(notes, " "), nil
}

// pushPlanState is what a push needs beyond its plan.
type pushPlanState struct {
	// kept is the What may leave rule keeping it here (D290), and send what it sends.
	kept   *leave.Refusal
	send   leave.Send
	source *models.RemotionSource
	ver    *models.RemotionVersion
	base   *models.RemotionVersion
	baseM  *Manifest
	files  []pushFile
	diff   []Change
	local  bool
}

// planPush builds the plan of a push back.
func planPush(ctx context.Context, a *app.App, in *RemotionPushIn) (*RemotionPushPlanOut, *pushPlanState, error) {
	pid, err := projectIDOf(ctx, a, in.ProjectID)
	if err != nil {
		return nil, nil, err
	}
	name, _ := splitRef(in.Source)
	s, err := findSource(ctx, a, pid, name)
	if err != nil {
		return nil, nil, err
	}
	return planPushSource(ctx, a, s, in)
}

// planPushSource builds the plan for a source already found.
func planPushSource(ctx context.Context, a *app.App, s *models.RemotionSource, in *RemotionPushIn) (*RemotionPushPlanOut, *pushPlanState, error) {
	_, ver := splitRef(in.Source)
	if ver == "" {
		ver = "approved"
	}
	v, err := resolveVersion(ctx, a, s, ver)
	if err != nil {
		return nil, nil, err
	}
	out := &RemotionPushPlanOut{Name: s.Name, Title: s.Title, SourceID: s.ID, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number, PR: !in.NoPR, Words: in.Words,
		Changes: []models.RemotionFileChange{}, WordChanges: []models.RemotionWordChange{}, Packages: []models.RemotionPackageChange{}, Problems: []string{}}
	st := &pushPlanState{source: s, ver: v}
	if v.State != models.RemotionApproved {
		out.Problems = append(out.Problems, fmt.Sprintf("Version %d isn't approved; only a version a person approved goes back.", v.Number))
	}
	base, err := latestImport(ctx, a, s)
	if err != nil {
		return nil, nil, err
	}
	st.base = base
	out.FromNumber, out.Base = base.Number, s.Origin.Commit
	if s.Origin.Commit == "" {
		out.Problems = append(out.Problems, "MetaRush doesn't know which commit it came from, so it can't send changes back.")
	}
	if base.Number > v.Number {
		out.Problems = append(out.Problems, fmt.Sprintf("Version %d is older than what was brought in last (version %d).", v.Number, base.Number))
	}
	// Where it goes.
	t, local, problem := pushTarget(ctx, a, s)
	st.local = local
	if problem != "" {
		out.Problems = append(out.Problems, problem)
	}
	out.Target = t
	out.Branch = strings.TrimSpace(in.Branch)
	if out.Branch == "" {
		out.Branch = fmt.Sprintf("metarush/%s-v%d", s.Name, v.Number)
	}
	if t.Kind != models.RemotionPushGitHub {
		out.PR = false
	}
	if problem == "" && t.URL != "" {
		def, tip, err := remoteRefs(ctx, a, t, local, out.Branch)
		switch {
		case err != nil:
			out.Problems = append(out.Problems, reachProblem(t, err))
		default:
			out.Target.Default = def
			why := checkBranch(out.Branch, def)
			if why != "" {
				out.Problems = append(out.Problems, why)
			}
			out.Parent = s.Origin.Commit
			if tip != "" && why == "" {
				last := lastPushTo(ctx, a, s, t.URL, out.Branch)
				switch {
				case last == nil:
					out.Problems = append(out.Problems, fmt.Sprintf("%s already has a branch called %s that MetaRush didn't make; pick another name.", t.Where, out.Branch))
				case last.Commit != tip:
					out.Problems = append(out.Problems, fmt.Sprintf("The branch %s was changed after MetaRush sent version %d to it; MetaRush never overwrites it — pick another name.", out.Branch, last.VersionNumber))
				default:
					out.Parent, out.Extends = tip, true
				}
			}
		}
	}
	// What goes back.
	baseM, err := loadManifest(ctx, a, base)
	if err != nil {
		return nil, nil, err
	}
	vm, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, nil, err
	}
	files, note, err := pushFiles(ctx, a, s, v, vm, in.Words)
	if err != nil {
		return nil, nil, err
	}
	out.Helper = note
	var mfs []ManifestFile
	text := map[string][]byte{}
	for _, f := range files {
		mfs = append(mfs, f.ManifestFile)
		if f.text != nil {
			text[f.SHA256] = f.text
		}
	}
	target := NewManifest(mfs)
	diff := Diff(baseM, target)
	st.baseM, st.files, st.diff = baseM, files, diff
	if len(diff) == 0 {
		out.Problems = append(out.Problems, fmt.Sprintf("Version %d has nothing to send back: its files are the same as what was brought in.", v.Number))
	}
	changes, words := fileChanges(diff, func(f ManifestFile) ([]byte, bool) {
		if t, ok := text[f.SHA256]; ok {
			return t, true
		}
		if f.Size > 4<<20 {
			return nil, false
		}
		b, err := readObject(ctx, a, f.SHA256, f.Size)
		return b, err == nil
	})
	if changes != nil {
		out.Changes = changes
	}
	if words != nil {
		out.WordChanges = words
	}
	if pk := diffPackages(base.Packages, v.Packages); pk != nil {
		out.Packages = pk
	}
	// What may leave (D290): the code, words and pictures go to the
	// repository's host (nothing leaves for a folder on this computer).
	if out.Target.Kind != "folder" && !isLocalURL(out.Target.URL) {
		send := leave.RepoPush(s.ProjectID, out.Target.URL, out.Target.Where, fmt.Sprintf("version %d of “%s” sent back to its repository", v.Number, s.Title))
		if r := leave.KeptRefusal(ctx, a, send); r != nil {
			out.Problems = append(out.Problems, r.Message)
			st.kept, st.send = r, send
		}
	}
	out.Summary = pushSummary(out)
	out.Ready = len(out.Problems) == 0
	out.Fingerprint = pushFingerprint(out, diff)
	out.Message, out.Next = pushMessage(out)
	return out, st, nil
}

// pushSummary says the changes in plain words.
func pushSummary(p *RemotionPushPlanOut) string {
	if len(p.Changes) == 0 {
		return "No changes."
	}
	var b strings.Builder
	if w := changeWords(p.Changes, p.WordChanges); w != "" {
		b.WriteString(w)
	} else {
		counts := map[string]int{}
		for _, c := range p.Changes {
			counts[c.Change]++
		}
		var parts []string
		for _, k := range []string{"changed", "added", "removed"} {
			if counts[k] > 0 {
				parts = append(parts, fmt.Sprintf("%s %s", plural(counts[k], "file", "files"), k))
			}
		}
		b.WriteString(upperFirst(joinAnd(parts)) + ".")
	}
	if len(p.Packages) > 0 {
		b.WriteString(" It " + packageWords(p.Packages) + ".")
	}
	return strings.TrimSpace(b.String())
}

// pushMessage says the plan in a sentence, and what to do next.
func pushMessage(p *RemotionPushPlanOut) (string, string) {
	where := p.Target.Where
	if where == "" {
		where = "its repository"
	}
	msg := fmt.Sprintf("Sends version %d of “%s” back to %s as the branch %s", p.VersionNumber, p.Title, where, p.Branch)
	if p.Extends {
		msg = fmt.Sprintf("Adds version %d of “%s” to the branch %s on %s", p.VersionNumber, p.Title, p.Branch, where)
	}
	if p.PR {
		into := p.Target.Default
		if into == "" {
			into = "the main branch"
		}
		msg += ", with a pull request into " + into
	}
	msg += ". " + p.Summary
	if !p.Ready {
		return msg, p.Problems[0]
	}
	return msg, "A person sends it (in the dashboard, or in their own terminal); nothing is ever forced or sent to the main branch."
}

// pushFingerprint is what the person saw.
func pushFingerprint(p *RemotionPushPlanOut, diff []Change) string {
	var cs []string
	for _, c := range diff {
		cs = append(cs, c.Change+":"+c.Path+":"+c.After.SHA256)
	}
	b, _ := json.Marshal(map[string]any{"format": "metarush.remotion-push-plan/1", "source": p.SourceID, "version": p.Version, "target": p.Target.URL, "branch": p.Branch,
		"base": p.Base, "parent": p.Parent, "pr": p.PR, "words": p.Words, "changes": cs})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// lastPushTo is MetaRush's last push to a branch (nil when it never pushed there).
func lastPushTo(ctx context.Context, a *app.App, s *models.RemotionSource, target, branch string) *models.RemotionPush {
	var ps []models.RemotionPush
	if err := a.DB.Find(ctx, store.CRemotionPushes, a.WorkspaceID(), bson.M{"sourceId": s.ID, "target.url": target, "branch": branch, "state": models.RemotionRunDone}, &ps,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1)); err != nil || len(ps) == 0 {
		return nil
	}
	return &ps[0]
}

// ---- starting ----

func startPush(ctx context.Context, a *app.App, in *RemotionPushStartIn) (*RemotionPushStartOut, error) {
	p, st, err := planPush(ctx, a, &in.RemotionPushIn)
	if err != nil {
		return nil, err
	}
	if st != nil && st.kept != nil { // refused and recorded (D290)
		leave.Record(ctx, a, st.kept, st.send)
		return nil, st.kept.Err()
	}
	if !p.Ready {
		return nil, apperr.New(apperr.Validation, "These changes can't go back: %s", strings.Join(p.Problems, " ")).WithDetails(map[string]any{"problems": p.Problems})
	}
	if strings.TrimSpace(in.Plan) != p.Fingerprint {
		return nil, apperr.New(apperr.Conflict, "What would go back changed since you looked at it (another version, branch or option, or the branch changed).").
			WithHint("Look at it again, then send it.").WithDetails(map[string]any{"plan": p.Fingerprint})
	}
	var after models.RemotionSource
	now := time.Now().UTC()
	if err := a.DB.C(store.CRemotionSources).FindOneAndUpdate(ctx, bson.M{"_id": p.SourceID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"pushes": 1}, "$set": bson.M{"updatedAt": now}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&after); err != nil {
		return nil, err
	}
	rec := &models.RemotionPush{ID: ids.New(ids.RemotionPush), WorkspaceID: a.WorkspaceID(), SourceID: p.SourceID, ProjectID: p.ProjectID, Number: after.Pushes,
		Version: p.Version, VersionNumber: p.VersionNumber, Target: p.Target, Branch: p.Branch, Base: p.Base, Parent: p.Parent, PR: p.PR, Words: p.Words,
		Changes: p.Changes, Fingerprint: p.Fingerprint, State: models.RemotionRunQueued, By: a.Actor, Machine: a.Cfg.Installation.Name, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CRemotionPushes, rec); err != nil {
		return nil, err
	}
	what := fmt.Sprintf("version %d of “%s” back to %s", p.VersionNumber, p.Title, p.Target.Where)
	payload := map[string]any{"push": rec.ID, "source": p.SourceID, "title": "Sending " + what, "what": what, "versionNumber": p.VersionNumber,
		"branch": p.Branch, "pr": p.PR, "words": p.Words, "projectId": p.ProjectID}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRemotionPush, IdempotencyKey: "remotion-push:" + rec.ID, Priority: 25, MaxAttempts: 1,
		ProjectID: p.ProjectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID, Payload: payload})
	if err != nil {
		_ = setPush(ctx, a, rec.ID, bson.M{"state": models.RemotionRunFailed, "error": "It couldn't be queued."})
		return nil, err
	}
	_ = setPush(ctx, a, rec.ID, bson.M{"jobId": j.ID})
	a.Audit(ctx, "remotion.push", "remotion", p.SourceID, p.ProjectID, map[string]any{"push": rec.ID, "version": p.Version, "target": p.Target.Where, "branch": p.Branch, "pr": p.PR, "words": p.Words, "via": a.Actor.Via})
	msg := fmt.Sprintf("Sending version %d of “%s” to %s as the branch %s", p.VersionNumber, p.Title, p.Target.Where, p.Branch)
	if p.PR {
		msg += ", then opening a pull request"
	}
	return &RemotionPushStartOut{Message: msg + ".", ID: rec.ID, Number: rec.Number, JobID: j.ID, Status: "queued"}, nil
}

// ---- running ----

// RunPushJob runs a remotion-push job (the worker's handler).
func RunPushJob(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	if report == nil {
		report = func(float64, string) {}
	}
	ra := *a
	ra.Actor = j.CreatedBy
	a = &ra
	id, _ := j.Payload["push"].(string)
	rec, err := getPush(ctx, a, id)
	if err != nil {
		return nil, &Final{Err: errors.New("that push isn't recorded any more")}
	}
	fail := func(msg string) error {
		_ = setPush(context.WithoutCancel(ctx), a, rec.ID, bson.M{"state": models.RemotionRunFailed, "error": msg, "finishedAt": time.Now().UTC()})
		return &Final{Err: errors.New(msg)}
	}
	if rec.State != models.RemotionRunQueued {
		return nil, fail(fmt.Sprintf("Sending version %d stopped before it finished; nothing was forced. Look at it again and send it again if you want.", rec.VersionNumber))
	}
	_ = setPush(ctx, a, rec.ID, bson.M{"state": models.RemotionRunRunning})
	s, err := getSource(ctx, a, rec.SourceID)
	if err != nil {
		return nil, fail("The Remotion project was removed.")
	}
	report(0.05, "looking at the repository again")
	in := &RemotionPushIn{Source: s.Name + "@" + rec.Version, Branch: rec.Branch, NoPR: !rec.PR, Words: rec.Words}
	p, st, err := planPushSource(ctx, a, s, in)
	if err != nil {
		return nil, fail("MetaRush couldn't look at it again: " + apperrMessage(err))
	}
	if st != nil && st.kept != nil { // asked again before anything is sent (D290)
		leave.Record(ctx, a, st.kept, st.send)
		return nil, fail(st.kept.Message)
	}
	if !p.Ready {
		return nil, fail(strings.Join(p.Problems, " "))
	}
	if p.Fingerprint != rec.Fingerprint {
		return nil, fail("Something changed since it was started (the branch, or the repository), so nothing was sent. Look at it again, then send it.")
	}
	commit, err := writeAndPush(ctx, a, s, p, st, report)
	if err != nil {
		return nil, fail(err.Error())
	}
	set := bson.M{"commit": commit}
	msg := fmt.Sprintf("Sent version %d of “%s” to %s as the branch %s.", p.VersionNumber, s.Title, p.Target.Where, p.Branch)
	if p.PR {
		report(0.9, "opening the pull request")
		pr, err := openPR(ctx, a, s, p)
		if err != nil {
			msg += " The pull request couldn't be opened: " + err.Error()
		} else {
			set["prUrl"] = pr
			msg += " Pull request: " + pr
		}
	}
	set["state"], set["summary"], set["finishedAt"] = models.RemotionRunDone, msg, time.Now().UTC()
	_ = setPush(context.WithoutCancel(ctx), a, rec.ID, set)
	a.Audit(context.WithoutCancel(ctx), "remotion.pushed", "remotion", s.ID, s.ProjectID, map[string]any{"push": rec.ID, "commit": commit, "branch": p.Branch, "target": p.Target.Where})
	report(1, msg)
	out := map[string]any{"message": msg, "push": rec.ID, "commit": commit, "branch": p.Branch, "sourceId": s.ID, "projectId": s.ProjectID}
	if u, ok := set["prUrl"].(string); ok {
		out["prUrl"] = u
	}
	return out, nil
}

// writeAndPush writes the commit on a fresh bare repository and pushes it.
func writeAndPush(ctx context.Context, a *app.App, s *models.RemotionSource, p *RemotionPushPlanOut, st *pushPlanState, report func(float64, string)) (string, error) {
	dir, err := os.MkdirTemp("", "metarush-push-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	bare := filepath.Join(dir, "repo.git")
	g := func(stdin []byte, out *bytes.Buffer, env []string, args ...string) error {
		return pushGit(ctx, a.Tools, bare, st.local, env, stdin, out, args...)
	}
	if err := pushGit(ctx, a.Tools, dir, st.local, nil, nil, nil, "init", "--bare", "--quiet", bare); err != nil {
		return "", fmt.Errorf("MetaRush couldn't make a place to write the commit: %s", firstLine(gitStderr(err)))
	}
	report(0.2, "getting the original commit")
	if err := g(nil, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", p.Target.URL, p.Base); err != nil {
		want := firstNonEmpty(s.Origin.Branch, "HEAD")
		if err2 := g(nil, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=200", "--", p.Target.URL, want); err2 != nil {
			return "", fmt.Errorf("MetaRush couldn't get the original commit from %s: %s", p.Target.Where, firstLine(gitStderr(err2)))
		}
	}
	if err := g(nil, nil, nil, "cat-file", "-e", p.Base+"^{commit}"); err != nil {
		return "", fmt.Errorf("%s doesn't have the commit it was brought in from any more", p.Target.Where)
	}
	if p.Extends {
		if err := g(nil, nil, nil, "fetch", "--quiet", "--no-tags", "--depth=1", "--", p.Target.URL, "refs/heads/"+p.Branch); err != nil {
			return "", fmt.Errorf("MetaRush couldn't get the branch %s: %s", p.Branch, firstLine(gitStderr(err)))
		}
	}
	// The tree: the original commit's files with MetaRush's changes.
	report(0.45, "writing the commit")
	index := filepath.Join(dir, "index")
	idx := []string{"GIT_INDEX_FILE=" + index}
	if err := g(nil, nil, idx, "read-tree", p.Base); err != nil {
		return "", fmt.Errorf("MetaRush couldn't read the original commit: %s", firstLine(gitStderr(err)))
	}
	modes := map[string]string{}
	var ls bytes.Buffer
	if err := g(nil, &ls, nil, "ls-tree", "-r", "-z", "--full-tree", p.Base); err == nil {
		for _, rec := range bytes.Split(ls.Bytes(), []byte{0}) {
			if tab := bytes.IndexByte(rec, '\t'); tab > 0 {
				if f := strings.Fields(string(rec[:tab])); len(f) == 3 {
					modes[string(rec[tab+1:])] = f[0]
				}
			}
		}
	}
	text := map[string][]byte{}
	for _, f := range st.files {
		if f.text != nil {
			text[f.Path] = f.text
		}
	}
	var info bytes.Buffer
	for _, c := range st.diff {
		if c.Change == "removed" {
			fmt.Fprintf(&info, "0 %s\t%s\n", strings.Repeat("0", 40), c.Path)
			continue
		}
		var out bytes.Buffer
		if data, ok := text[c.Path]; ok {
			if err := g(data, &out, nil, "hash-object", "-w", "--no-filters", "--stdin"); err != nil {
				return "", fmt.Errorf("MetaRush couldn't write %s: %s", c.Path, firstLine(gitStderr(err)))
			}
		} else {
			// The version's own file, read by git from this computer's copy (pictures and sound can be large).
			local, err := localObject(ctx, a, c.After.SHA256, c.After.Size)
			if err != nil {
				return "", err
			}
			if err := g(nil, &out, nil, "hash-object", "-w", "--no-filters", "--", local); err != nil {
				return "", fmt.Errorf("MetaRush couldn't write %s: %s", c.Path, firstLine(gitStderr(err)))
			}
		}
		mode := modes[c.Path]
		if mode == "" || mode == "120000" || mode == "160000" {
			mode = "100644"
		}
		fmt.Fprintf(&info, "%s %s\t%s\n", mode, strings.TrimSpace(out.String()), c.Path)
	}
	if err := g(info.Bytes(), nil, idx, "update-index", "--index-info"); err != nil {
		return "", fmt.Errorf("MetaRush couldn't put the changes together: %s", firstLine(gitStderr(err)))
	}
	var tree bytes.Buffer
	if err := g(nil, &tree, idx, "write-tree"); err != nil {
		return "", fmt.Errorf("MetaRush couldn't put the changes together: %s", firstLine(gitStderr(err)))
	}
	var who []string
	var ident bytes.Buffer
	if g(nil, &ident, nil, "var", "GIT_AUTHOR_IDENT") != nil || strings.TrimSpace(ident.String()) == "" {
		name := firstNonEmpty(a.Actor.UserName, "MetaRush")
		who = []string{"GIT_AUTHOR_NAME=" + name, "GIT_AUTHOR_EMAIL=metarush@localhost", "GIT_COMMITTER_NAME=" + name, "GIT_COMMITTER_EMAIL=metarush@localhost"}
	}
	msg := pushCommitMessage(s, p)
	var commit bytes.Buffer
	if err := g([]byte(msg), &commit, who, "commit-tree", strings.TrimSpace(tree.String()), "-p", p.Parent, "-F", "-"); err != nil {
		return "", fmt.Errorf("MetaRush couldn't write the commit: %s", firstLine(gitStderr(err)))
	}
	sha := strings.TrimSpace(commit.String())
	if !resources.FullSHA(sha) {
		return "", errors.New("git didn't say which commit it wrote")
	}
	// A plain push: never forced, only the new branch (or MetaRush's own, moving forward).
	report(0.7, "sending it to "+p.Target.Where)
	if err := g(nil, nil, nil, "push", "--quiet", "--no-verify", "--", p.Target.URL, sha+":refs/heads/"+p.Branch); err != nil {
		e := gitStderr(err)
		low := strings.ToLower(e)
		switch {
		case strings.Contains(low, "rejected") || strings.Contains(low, "non-fast-forward") || strings.Contains(low, "fetch first"):
			return "", fmt.Errorf("%s turned it down: the branch %s changed in the meantime, and MetaRush never overwrites a branch", p.Target.Where, p.Branch)
		case strings.Contains(low, "permission") || strings.Contains(low, "403") || strings.Contains(low, "authentication") || strings.Contains(low, "could not read username"):
			return "", fmt.Errorf("%s didn't let this computer send it: sign in to it here (for GitHub, with gh auth login) and try again", p.Target.Where)
		}
		return "", fmt.Errorf("sending it to %s didn't work: %s", p.Target.Where, firstLine(e))
	}
	return sha, nil
}

// pushCommitMessage is the commit's message: what changed, in plain words.
func pushCommitMessage(s *models.RemotionSource, p *RemotionPushPlanOut) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Version %d of %s, from MetaRush\n\n", p.VersionNumber, s.Title)
	b.WriteString(p.Summary + "\n\n")
	n := 0
	for _, c := range p.Changes {
		if n >= 40 {
			fmt.Fprintf(&b, "… and %d more\n", len(p.Changes)-n)
			break
		}
		fmt.Fprintf(&b, "- %s %s\n", c.Change, c.Path)
		n++
	}
	if p.Helper != "" {
		b.WriteString("\n" + p.Helper + "\n")
	}
	fmt.Fprintf(&b, "\nMade with MetaRush: %s, version %d (%s).\n", s.Name, p.VersionNumber, p.Version)
	return b.String()
}

// openPR opens the pull request (or finds the one open for the branch).
func openPR(ctx context.Context, a *app.App, s *models.RemotionSource, p *RemotionPushPlanOut) (string, error) {
	if !a.Tools.Has(tools.GH) {
		return "", errors.New("GitHub's command-line tool (gh) isn't installed on this computer")
	}
	body := fmt.Sprintf("Changes made in MetaRush to “%s”, version %d (approved by a person there).\n\n%s\n", s.Title, p.VersionNumber, p.Summary)
	if p.Helper != "" {
		body += "\n" + p.Helper + "\n"
	}
	title := fmt.Sprintf("MetaRush: version %d of %s", p.VersionNumber, s.Title)
	base := firstNonEmpty(p.Target.Default, "main")
	var out bytes.Buffer
	_, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.GH, Args: []string{"pr", "create", "--repo", p.Target.Repo, "--head", p.Branch, "--base", base, "--title", title, "--body-file", "-"},
		Stdin: strings.NewReader(body), Stdout: &out, Env: []string{"GH_PROMPT_DISABLED=1", "GH_NO_UPDATE_NOTIFIER=1"}})
	if err == nil {
		if u := lastURL(out.String()); u != "" {
			return u, nil
		}
		return "", errors.New("gh didn't say where the pull request is")
	}
	if strings.Contains(strings.ToLower(gitStderr(err)), "already exists") {
		var view bytes.Buffer
		if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.GH, Args: []string{"pr", "view", p.Branch, "--repo", p.Target.Repo, "--json", "url"}, Stdout: &view,
			Env: []string{"GH_PROMPT_DISABLED=1"}}); err == nil {
			var v struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(view.Bytes(), &v) == nil && v.URL != "" {
				return v.URL, nil
			}
		}
	}
	return "", fmt.Errorf("%s", firstLine(firstNonEmpty(gitStderr(err), "gh stopped")))
}

var anyURL = regexp.MustCompile(`https://[^\s]+`)

func lastURL(s string) string {
	all := anyURL.FindAllString(s, -1)
	if len(all) == 0 {
		return ""
	}
	return all[len(all)-1]
}

// ---- records ----

func setPush(ctx context.Context, a *app.App, id string, set bson.M) error {
	set["updatedAt"] = time.Now().UTC()
	_, err := a.DB.C(store.CRemotionPushes).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set})
	return err
}

func getPush(ctx context.Context, a *app.App, id string) (*models.RemotionPush, error) {
	var p models.RemotionPush
	if err := a.DB.GetByID(ctx, store.CRemotionPushes, a.WorkspaceID(), id, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func pushesOf(ctx context.Context, a *app.App, sourceID string, limit int64) ([]models.RemotionPush, error) {
	var out []models.RemotionPush
	err := a.DB.Find(ctx, store.CRemotionPushes, a.WorkspaceID(), bson.M{"sourceId": sourceID}, &out, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(limit))
	return out, err
}

func pushRow(p *models.RemotionPush) RemotionPushRow {
	row := RemotionPushRow{ID: p.ID, Number: p.Number, State: p.State, StateLabel: runStateLabel(p.State), VersionNumber: p.VersionNumber, Version: p.Version, Target: p.Target,
		Branch: p.Branch, Commit: p.Commit, PRURL: p.PRURL, Changes: p.Changes, Words: p.Words, Summary: p.Summary, Error: p.Error, By: p.By, Machine: p.Machine,
		JobID: p.JobID, CreatedAt: p.CreatedAt, FinishedAt: p.FinishedAt}
	if row.Changes == nil {
		row.Changes = []models.RemotionFileChange{}
	}
	if p.Target.Kind == models.RemotionPushGitHub && p.Target.Repo != "" && p.State == models.RemotionRunDone {
		row.BranchURL = "https://github.com/" + p.Target.Repo + "/tree/" + p.Branch
	}
	return row
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("remotion.push.start", ops.Offering{ID: "changes-as-pull-request", Group: "deliver", Title: "Changes back as a pull request",
		Does:  "Sends an approved Remotion version's changes back to its repository as a new branch and a pull request.",
		Words: []string{"open a pr", "send changes upstream"}, Needs: ops.Needs{Programs: []string{"git", "gh"}, Leaves: "the version's changes leave this computer, for its repository"}})
}
