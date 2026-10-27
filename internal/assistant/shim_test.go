package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
)

// within reports whether p is root or inside it (both already real paths).
func within(p, root string) bool {
	rel, err := filepath.Rel(root, p)
	return err == nil && (rel == "." || (!strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)))
}

// rulePaths are the absolute folders a list of permission rules names for a
// tool ("Edit(//a/b/**)" → /a/b).
func rulePaths(rules []string, tool string) []string {
	var out []string
	for _, r := range rules {
		if !strings.HasPrefix(r, tool+"(/") {
			continue
		}
		p := strings.TrimSuffix(strings.TrimPrefix(r, tool+"(/"), ")")
		out = append(out, strings.TrimSuffix(p, "/**"))
	}
	return out
}

// onPath resolves a command the way a shell would, from a PATH value.
func onPath(path, name string) string {
	for _, d := range filepath.SplitList(path) {
		p := filepath.Join(d, name)
		if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() && st.Mode()&0o111 != 0 {
			return p
		}
	}
	return ""
}

// The dashboard assistant's shim lives outside Claude Code's working folder,
// next to it: Claude reaches it only as `metarush` on PATH (the allowlist
// names commands, not paths), cannot find it by looking around its working
// folder, and cannot write it (no allow rule covers its folder, an explicit
// deny does). An installation upgraded from the old layout loses the copy in
// the working folder.
func TestShimIsReachedOnlyThroughPath(t *testing.T) {
	a := testApp(t, "")
	projectID, _, _ := fixture(t, a)
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		tool("Bash", map[string]any{"command": "metarush project show " + projectID + " --json"}, `{"ok":true,"data":{}}`),
		text("Two clips."),
	}}}})
	// The layout before: the shim in the working folder's bin/.
	old := filepath.Join(m.workspaceDir(), "bin", "metarush")
	if err := os.MkdirAll(filepath.Dir(old), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old, []byte(shim(m.Exe, a.Cfg.Home())), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	out, err := m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: "What's here?", ProjectID: projectID}})
	if err != nil {
		t.Fatal(err)
	}
	id := out.Conversation.ID
	waitIdle(t, m, id)

	if _, err := os.Stat(filepath.Join(m.workspaceDir(), "bin")); !os.IsNotExist(err) {
		t.Fatalf("the old shim folder in the working folder survived: %v", err)
	}
	shimDir, cwd := realPath(m.binDir()), realPath(m.workspaceDir())
	p := filepath.Join(shimDir, ShimName)
	st, err := os.Lstat(p)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o755 {
		t.Fatalf("shim: %v %v", st, err)
	}
	if b, _ := os.ReadFile(p); string(b) != shim(m.Exe, a.Cfg.Home()) {
		t.Fatalf("shim text:\n%s", b)
	}
	if dst, err := os.Lstat(shimDir); err != nil || !dst.IsDir() || dst.Mode().Perm() != 0o700 {
		t.Fatalf("shim folder: %v %v", dst, err)
	}
	// Outside every folder Claude works in, reads or writes.
	proposals := realPath(filepath.Join(m.convDir(id), "proposals"))
	for what, root := range map[string]string{"working folder": cwd, "proposals": proposals, "conversation": realPath(m.convDir(id)),
		"scratch": realPath(a.Cfg.Scratch.Dir), "cache": realPath(a.Cfg.Cache.Dir)} {
		if within(shimDir, root) {
			t.Errorf("the shim is inside the %s (%s)", what, root)
		}
	}
	// PATH is the only way to it: first on PATH, and `metarush` resolves to it.
	env, _ := os.ReadFile(filepath.Join(m.convDir(id), "env.txt"))
	var path string
	for _, kv := range strings.Split(string(env), "\n") {
		if v, ok := strings.CutPrefix(kv, "PATH="); ok {
			path = v
		}
	}
	if first := filepath.SplitList(path)[0]; first != m.binDir() {
		t.Fatalf("PATH starts with %q, want the shim's folder %q", first, m.binDir())
	}
	if got := onPath(path, "metarush"); realPath(got) != p {
		t.Fatalf("metarush on Claude's PATH is %q, want the shim %q", got, p)
	}
	for _, d := range filepath.SplitList(path) {
		if within(realPath(d), cwd) || within(realPath(d), proposals) {
			t.Fatalf("PATH has %q, a folder Claude can look in or write", d)
		}
	}
	// The permission rules: commands by name only, nothing writable there, an explicit deny.
	argv := fakeclaude.Spawns(t, m.convDir(id))[0]
	allow, deny := strings.Split(after(argv, "--allowedTools"), ","), strings.Split(after(argv, "--disallowedTools"), ",")
	for _, r := range allow {
		if strings.HasPrefix(r, "Bash(") && !strings.HasPrefix(r, "Bash(metarush ") {
			t.Errorf("allow rule %q runs something other than metarush by name", r)
		}
	}
	for _, tool := range []string{"Edit", "Write", "Read"} {
		for _, root := range rulePaths(allow, tool) {
			if within(shimDir, root) || within(p, root) {
				t.Errorf("allow rule %s(%s) covers the shim", tool, root)
			}
		}
	}
	if !has(deny, "Edit(/"+shimDir+"/**)") {
		t.Fatalf("no deny rule for edits of the shim's folder: %q", deny)
	}
	for _, want := range deniedTools {
		if !has(deny, want) {
			t.Errorf("the deny list lost %q", want)
		}
	}
}

// Whatever happens to the shim between launches, the next launch (or resume)
// puts the real one back as a plain file in a plain folder, and never writes
// through a link someone left in its place.
func TestShimIsRestoredBeforeEveryLaunch(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "shim")
	want := shim("/opt/metarush", "/h")
	check := func(stage string) {
		t.Helper()
		if err := writeShim(dir, "/opt/metarush", "/h"); err != nil {
			t.Fatalf("%s: %v", stage, err)
		}
		dst, err := os.Lstat(dir)
		if err != nil || !dst.IsDir() || dst.Mode()&os.ModeSymlink != 0 || dst.Mode().Perm() != 0o700 {
			t.Fatalf("%s: folder %v %v", stage, dst, err)
		}
		st, err := os.Lstat(filepath.Join(dir, ShimName))
		if err != nil || !st.Mode().IsRegular() || st.Mode().Perm() != 0o755 {
			t.Fatalf("%s: shim %v %v", stage, st, err)
		}
		if b, _ := os.ReadFile(filepath.Join(dir, ShimName)); string(b) != want {
			t.Fatalf("%s: shim text %q", stage, b)
		}
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("%s: left behind %v", stage, entries)
		}
	}
	check("first launch")
	// Changed in place, and made world-writable.
	if err := os.WriteFile(filepath.Join(dir, ShimName), []byte("#!/bin/sh\nexec /bin/sh \"$@\"\n"), 0o777); err != nil {
		t.Fatal(err)
	}
	_ = os.Chmod(filepath.Join(dir, ShimName), 0o777)
	check("changed shim")
	// Replaced by a link to another file: the link goes, the file it points at is untouched.
	other := filepath.Join(base, "elsewhere")
	if err := os.WriteFile(other, []byte("not mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(filepath.Join(dir, ShimName))
	if err := os.Symlink(other, filepath.Join(dir, ShimName)); err != nil {
		t.Fatal(err)
	}
	check("linked shim")
	if b, _ := os.ReadFile(other); string(b) != "not mine" {
		t.Fatalf("writing the shim went through the link: %q", b)
	}
	// A folder where the shim goes.
	_ = os.Remove(filepath.Join(dir, ShimName))
	if err := os.MkdirAll(filepath.Join(dir, ShimName, "x"), 0o755); err != nil {
		t.Fatal(err)
	}
	check("folder in its place")
	// The folder itself replaced by a link to a folder with another metarush.
	decoy := filepath.Join(base, "decoy")
	if err := os.MkdirAll(decoy, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(decoy, ShimName), []byte("#!/bin/sh\necho decoy\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(decoy, dir); err != nil {
		t.Fatal(err)
	}
	check("linked folder")
	if b, _ := os.ReadFile(filepath.Join(decoy, ShimName)); !strings.Contains(string(b), "decoy") {
		t.Fatal("the decoy folder was written to")
	}
	// A file where the folder goes.
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	check("file in place of the folder")

	// The working folder never gets a shim, whatever it had.
	ws := filepath.Join(base, "workspace")
	if err := os.MkdirAll(filepath.Join(ws, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(ws, "bin", "metarush"), []byte(want), 0o755)
	if err := prepareWorkspace(ws); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(ws, "bin")); !os.IsNotExist(err) {
		t.Fatalf("the working folder kept a shim: %v", err)
	}
	if _, err := deniedFor("/a/sh(im"); err == nil {
		t.Fatal("a shim folder that breaks rule syntax must be refused")
	}
}
