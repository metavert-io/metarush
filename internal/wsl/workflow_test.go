package wsl

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// The WSL smoke test's workflow (.github/workflows/wsl.yml) reads nothing it
// needn't: only the repository's contents, the checkout keeps no credentials
// for the steps after it, and every action is pinned by its commit. It runs
// when what the smoke test exercises changes: these paths, the same for a
// push and a pull request, each still naming something in the repository.
func TestTheWSLWorkflow(t *testing.T) {
	root := filepath.Join("..", "..")
	b, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "wsl.yml"))
	if err != nil {
		t.Fatal(err)
	}
	type step struct {
		Uses string         `yaml:"uses"`
		With map[string]any `yaml:"with"`
	}
	var wf struct {
		On struct {
			Push        struct{ Paths []string } `yaml:"push"`
			PullRequest struct{ Paths []string } `yaml:"pull_request"`
		} `yaml:"on"`
		Permissions map[string]string `yaml:"permissions"`
		Jobs        map[string]struct {
			Permissions map[string]string `yaml:"permissions"`
			Steps       []step            `yaml:"steps"`
		} `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(b, &wf); err != nil {
		t.Fatal(err)
	}
	if len(wf.Permissions) != 1 || wf.Permissions["contents"] != "read" {
		t.Errorf("permissions: %v, want contents: read only", wf.Permissions)
	}
	pinned := regexp.MustCompile(`^[\w.-]+/[\w.-]+@[0-9a-f]{40}$`)
	checkouts := 0
	for name, job := range wf.Jobs {
		if len(job.Permissions) != 0 {
			t.Errorf("job %s asks for more: %v", name, job.Permissions)
		}
		for _, s := range job.Steps {
			if s.Uses == "" {
				continue
			}
			if !pinned.MatchString(s.Uses) {
				t.Errorf("%s: not pinned by its commit", s.Uses)
			}
			if strings.HasPrefix(s.Uses, "actions/checkout@") {
				checkouts++
				if v, _ := s.With["persist-credentials"].(bool); v || s.With["persist-credentials"] == nil {
					t.Errorf("the checkout keeps its credentials: %v", s.With)
				}
			}
		}
	}
	if checkouts == 0 {
		t.Error("no checkout")
	}
	want := []string{"internal/wsl/**", "internal/tools/**", "internal/cli/wsl.go", "internal/cli/daemon.go", "internal/cli/doctor.go",
		"internal/cli/setup.go", "internal/api/access.go", "internal/localdb/**", "internal/ingest/windows.go", "internal/secretpath/**",
		"internal/service/service.go", "scripts/wsl-smoke.sh", ".github/workflows/wsl.yml"}
	for _, paths := range [][]string{wf.On.Push.Paths, wf.On.PullRequest.Paths} {
		if !slices.Equal(paths, want) {
			t.Errorf("paths %q, want %q", paths, want)
		}
	}
	for _, p := range want {
		if _, err := os.Stat(filepath.Join(root, strings.TrimSuffix(p, "/**"))); err != nil {
			t.Errorf("%s names nothing: %v", p, err)
		}
	}
}
