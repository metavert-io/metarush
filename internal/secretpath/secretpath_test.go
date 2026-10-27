package secretpath

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func gitAvailable(t *testing.T) string {
	t.Helper()
	p, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not installed")
	}
	return p
}

func gitInit(t *testing.T, dir string) {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, out)
	}
}

func TestSyncFolders(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	in := Inspector{Home: home, Git: "-", GOOS: "darwin"}
	cases := map[string]string{
		filepath.Join(home, "Library", "Mobile Documents", "com~apple~CloudDocs", "mr", ".env"):   "iCloud Drive",
		filepath.Join(home, "Library", "CloudStorage", "GoogleDrive-ada@example.com", "My Drive"): "Google Drive",
		filepath.Join(home, "Library", "CloudStorage", "OneDrive-Contoso", "x.json"):              "OneDrive",
		filepath.Join(home, "Library", "CloudStorage", "Dropbox", "bundle.json"):                  "Dropbox",
		filepath.Join(home, "Library", "CloudStorage", "Box-Box", "a"):                            "Box",
		filepath.Join(home, "Dropbox", "metarush", ".env"):                                        "Dropbox",
		filepath.Join(home, "Dropbox (Team)", "f"):                                                "Dropbox",
		filepath.Join(home, "Google Drive", "f"):                                                  "Google Drive",
		filepath.Join(home, "OneDrive - Contoso", "f"):                                            "OneDrive",
		filepath.Join(home, "OneDrive", "f"):                                                      "OneDrive",
		filepath.Join(home, "Box Sync", "f"):                                                      "Box",
		filepath.Join(home, "box", "f"):                                                           "Box", // case-insensitive on macOS
		"/Volumes/GoogleDrive/My Drive/x":                                                         "Google Drive",
	}
	for p, want := range cases {
		v := in.Inspect(p)
		if v.Kind != KindSync || v.Service != want {
			t.Errorf("%s: got kind=%q service=%q, want sync %q", p, v.Kind, v.Service, want)
		}
		if err := in.Check(p, "the bundle", true); err == nil {
			t.Errorf("%s: sync folders must be refused even with the escape hatch", p)
		}
		if err := in.CheckUse(p, ".env"); err == nil {
			t.Errorf("%s: using a secret file in a sync folder must be refused", p)
		}
	}
	for _, p := range []string{
		filepath.Join(home, ".metarush", ".env"),
		filepath.Join(home, ".metarush-bundles", "metarush-join-x.json"),
		filepath.Join(home, "Documents", "Dropbox-notes", "x"), // not a sync root
		filepath.Join(home, "Library", "Application Support", "x"),
		"/tmp/metarush/.env",
	} {
		if v := in.Inspect(p); v.Unsafe() {
			t.Errorf("%s should be safe, got %+v", p, v)
		}
		if err := in.Check(p, "x", false); err != nil {
			t.Errorf("%s: %v", p, err)
		}
	}
	// Linux is case-sensitive: ~/box is not Box's folder there.
	lin := Inspector{Home: home, Git: "-", GOOS: "linux"}
	if v := lin.Inspect(filepath.Join(home, "box", "f")); v.Unsafe() {
		t.Errorf("linux ~/box flagged: %+v", v)
	}
	if v := lin.Inspect(filepath.Join(home, "Dropbox", "f")); v.Kind != KindSync {
		t.Errorf("linux ~/Dropbox not flagged: %+v", v)
	}
}

func TestSymlinkIntoSyncFolder(t *testing.T) {
	home := t.TempDir()
	real := filepath.Join(home, "Library", "CloudStorage", "Dropbox", "work")
	if err := os.MkdirAll(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(home, "work")
	if err := os.Symlink(real, link); err != nil {
		t.Skip("symlinks unavailable")
	}
	home, _ = filepath.EvalSymlinks(home) // macOS temp dirs live behind /private
	in := Inspector{Home: home, Git: "-", GOOS: "darwin"}
	if v := in.Inspect(filepath.Join(link, "mr", ".env")); v.Kind != KindSync || v.Service != "Dropbox" {
		t.Fatalf("symlinked sync folder not detected: %+v", v)
	}
}

func TestGitWorkTree(t *testing.T) {
	git := gitAvailable(t)
	repo := t.TempDir()
	gitInit(t, repo)
	in := Inspector{Home: t.TempDir(), Git: git}
	envFile := filepath.Join(repo, "mrhome", ".env")
	v := in.Inspect(envFile)
	if v.Kind != KindGit || v.Ignored || !v.GitChecked {
		t.Fatalf("not-ignored file in a repo: %+v", v)
	}
	var ue *UnsafeError
	if err := in.Check(envFile, ".env", true); !errors.As(err, &ue) || ue.EscapeHatch {
		t.Fatalf("a path git would commit must be refused even with the escape hatch: %v", err)
	}
	if err := in.CheckUse(envFile, ".env"); err == nil {
		t.Fatal("using a secret file git would commit must be refused")
	}
	// Ignore it (as every MetaRush home's own .gitignore does).
	if err := os.MkdirAll(filepath.Dir(envFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "mrhome", ".gitignore"), []byte("*\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	v = in.Inspect(envFile)
	if v.Kind != KindGit || !v.Ignored {
		t.Fatalf("ignored file: %+v", v)
	}
	if err := in.Check(envFile, ".env", false); !errors.As(err, &ue) || !ue.EscapeHatch {
		t.Fatalf("git-ignored path: refused by default with the escape hatch offered, got %v", err)
	}
	if !strings.Contains(err2s(in.Check(envFile, ".env", false)), "--allow-unsafe-secret-path") && !strings.Contains(ue.Hint, "--allow-unsafe-secret-path") {
		t.Fatal("hint must name the escape hatch")
	}
	if err := in.Check(envFile, ".env", true); err != nil {
		t.Fatalf("escape hatch on an ignored path: %v", err)
	}
	if err := in.CheckUse(envFile, ".env"); err != nil {
		t.Fatalf("an ignored secret file may be used: %v", err)
	}
	// A tracked file is never "ignored", whatever .gitignore says.
	tracked := filepath.Join(repo, "tracked.env")
	if err := os.WriteFile(tracked, []byte("A=1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	add := exec.Command(git, "-C", repo, "add", "tracked.env")
	if out, err := add.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v %s", err, out)
	}
	if err := os.WriteFile(filepath.Join(repo, ".gitignore"), []byte("*.env\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := in.Inspect(tracked); v.Ignored {
		t.Fatalf("tracked file reported ignored: %+v", v)
	}
	// Outside the repository: safe.
	if v := in.Inspect(filepath.Join(t.TempDir(), "x.env")); v.Unsafe() {
		t.Fatalf("outside: %+v", v)
	}
}

func TestGitWithoutGitBinary(t *testing.T) {
	repo := t.TempDir()
	if err := os.Mkdir(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	in := Inspector{Home: t.TempDir(), Git: "-"}
	v := in.Inspect(filepath.Join(repo, "a", "b.json"))
	if v.Kind != KindGit || v.Ignored || v.GitChecked {
		t.Fatalf("walk-up without git: %+v", v)
	}
	if err := in.Check(v.Path, "bundle", true); err == nil {
		t.Fatal("unconfirmed ignore status must be treated as not ignored")
	}
	// A .git file (worktree / submodule) also marks a work tree.
	wt := t.TempDir()
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if v := in.Inspect(filepath.Join(wt, "x")); v.Kind != KindGit {
		t.Fatalf(".git file not detected: %+v", v)
	}
}

func TestFindStray(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, body string) string {
		p := filepath.Join(dir, rel)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	bundle := write("metarush-join-mini.json", "{}")
	ci := write("sub/metarush-ci-s3.env", "X=1")
	dotenv := write("app/.env", "METARUSH_MONGODB_URI=mongodb://127.0.0.1:27018/x\n")
	write("app/.env.example", "METARUSH_MONGODB_URI=mongodb://user:pass@host\n") // secret-scan: allow (placeholder)
	write("other/.env", "PORT=3000\nMETARUSH_MONGODB_URI=\n")                    // name only, no value
	write("node_modules/x/metarush-join-a.json", "{}")                           // skipped dir
	write("a/b/c/d/e/metarush-join-deep.json", "{}")                             // too deep
	own := write("home/.env", "METARUSH_ELEVENLABS_API_KEY=x\n")
	got := FindStray([]string{dir}, 3, 10000, own)
	want := []string{dotenv, bundle, ci}
	if strings.Join(got, "|") != strings.Join(sortStrings(want), "|") {
		t.Fatalf("stray files:\n got %v\nwant %v", got, sortStrings(want))
	}
}

// A project folder is found from any file inside it (existing or not), and
// only through its marker file.
func TestProjectFolder(t *testing.T) {
	root := t.TempDir()
	proj := filepath.Join(root, "cuts", "spring")
	if err := os.MkdirAll(filepath.Join(proj, filepath.Dir(ProjectFolderMarker)), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(proj, "cut.md"), filepath.Join(proj, "notes", "later", "x.zip")} {
		if got := ProjectFolder(p); got != "" {
			t.Fatalf("no marker yet, but %s is in project folder %q", p, got)
		}
	}
	if err := os.WriteFile(filepath.Join(proj, ProjectFolderMarker), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{filepath.Join(proj, "cut.md"), filepath.Join(proj, "notes", "later", "x.zip")} {
		if got := ProjectFolder(p); got != proj {
			t.Fatalf("project folder of %s: %q, want %q", p, got, proj)
		}
	}
	if got := ProjectFolder(filepath.Join(root, "cuts", "other.md")); got != "" {
		t.Fatalf("a file next to the project folder is not in it: %q", got)
	}
}

func sortStrings(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

func err2s(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestSecretEnvNames(t *testing.T) {
	for name, want := range map[string]bool{
		"METARUSH_MONGODB_URI": true, "METARUSH_MONGODB_ADMIN_URI": true, "METARUSH_TEST_MONGODB_URI": true, "METARUSH_S3_SECRET_ACCESS_KEY": true,
		"METARUSH_ELEVENLABS_API_KEY": true, "METARUSH_GEMINI_API_KEY": true, "METARUSH_SOMETHING_TOKEN": true, "METARUSH_X_PASSWORD": true,
		"METARUSH_TAILSCALE": false, "METARUSH_HOME": false, "METARUSH_S3_ACCESS_KEY_ID": false, "PATH": false, "ANTHROPIC_API_KEY": false,
		// Video platforms' clients and people's sign-ins (D305), whatever comes after the prefix.
		"METARUSH_PUBLISH_YOUTUBE_CLIENT": true, "METARUSH_PUBLISH_YOUTUBE_PERSON_USR_ABC": true, "METARUSH_PUBLISH_VIMEO_PERSON_OWNER": true,
		"METARUSH_YOUTUBE_BASE": false, "METARUSH_RCLONE": false,
	} {
		if IsSecretEnvName(name) != want {
			t.Errorf("IsSecretEnvName(%s) = %v", name, !want)
		}
	}
	got := WithoutSecrets([]string{"PATH=/bin", "METARUSH_MONGODB_URI=mongodb://u:p@h/", "METARUSH_TAILSCALE=/x", "METARUSH_GEMINI_API_KEY=k"}) // secret-scan: allow (a made-up test address)
	if strings.Join(got, ",") != "PATH=/bin,METARUSH_TAILSCALE=/x" {
		t.Errorf("WithoutSecrets: %v", got)
	}
	// No program MetaRush starts inherits a sign-in or the administrator's address.
	in := []string{"PATH=/bin", "METARUSH_PUBLISH_YOUTUBE_CLIENT={}", "METARUSH_PUBLISH_YOUTUBE_PERSON_USR_A={}", "METARUSH_MONGODB_ADMIN_URI=x", "METARUSH_TAILSCALE=/x"}
	if got := Inheritable(in); strings.Join(got, ",") != "PATH=/bin,METARUSH_TAILSCALE=/x" {
		t.Errorf("Inheritable: %v", got)
	}
}
