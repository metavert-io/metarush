package cli

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/skills"
)

const repoRoot = "../.."

// The Claude Code plugin (plugins/claude-code), the skills other AI apps read
// (plugins/agents) and the marketplace entry are generated from skills/ (D320):
// the repository's copy must be exactly what `metarush skills package-plugin`
// writes, with nothing left over. METARUSH_UPDATE_PLUGIN=1 writes it.
func TestPluginIsCurrent(t *testing.T) {
	want, err := pluginRepository()
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("METARUSH_UPDATE_PLUGIN") == "1" {
		changed, err := skillsinstall.WriteRepository(repoRoot, want)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %d files", len(changed))
		return
	}
	drift, err := skillsinstall.Drift(repoRoot, want)
	if err != nil {
		t.Fatal(err)
	}
	if len(drift) > 0 {
		t.Fatalf("the plugin is out of date with skills/ (%d files: %s): run\n  go run ./cmd/metarush skills package-plugin\n(or METARUSH_UPDATE_PLUGIN=1 go test ./internal/cli -run TestPluginIsCurrent) and commit plugins/ and .claude-plugin/", len(drift), strings.Join(drift[:min(len(drift), 8)], ", "))
	}
}

// Every skill folder is one the binary carries, so none is left out of the
// plugin (or of `metarush skills install`).
func TestPluginHasEverySkill(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join(repoRoot, "skills"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.IsDir() && !strings.HasPrefix(e.Name(), "_") && !slices.Contains(skills.Names, e.Name()) {
			t.Errorf("skills/%s is not in skills.Names (and skills/embed.go): the plugin and installs leave it out", e.Name())
		}
	}
}

// The plugin as Claude Code reads it: the manifest's name and version (VERSION's),
// the marketplace entry pointing at it, and an MCP server that starts this
// binary's read-only core by the program's name.
func TestPluginManifests(t *testing.T) {
	read := func(rel string, v any) {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(repoRoot, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
	}
	var manifest struct{ Name, Version, Description, License string }
	read(skillsinstall.PluginDir+"/.claude-plugin/plugin.json", &manifest)
	ver, _ := os.ReadFile(filepath.Join(repoRoot, "VERSION"))
	if manifest.Name != skillsinstall.PluginName || manifest.Version != strings.TrimSpace(string(ver)) || manifest.License != "MIT" {
		t.Errorf("plugin.json: %+v (VERSION %s)", manifest, ver)
	}
	if !strings.Contains(manifest.Description, "metarush program") {
		t.Error("the plugin's description doesn't say it needs the metarush program")
	}
	var market struct {
		Name    string
		Plugins []struct{ Name, Source string }
	}
	read(skillsinstall.MarketplaceFile, &market)
	if market.Name != skillsinstall.PluginName || len(market.Plugins) != 1 || market.Plugins[0].Name != manifest.Name ||
		market.Plugins[0].Source != "./"+skillsinstall.PluginDir {
		t.Errorf("marketplace.json: %+v", market)
	}
	var mcpCfg struct {
		MCPServers map[string]struct {
			Command string
			Args    []string
			Env     map[string]string
		} `json:"mcpServers"`
	}
	read(skillsinstall.PluginDir+"/.mcp.json", &mcpCfg)
	srv, ok := mcpCfg.MCPServers[skillsinstall.PluginServer]
	if len(mcpCfg.MCPServers) != 1 || !ok || srv.Command != "metarush" || !slices.Equal(srv.Args, skillsinstall.PluginMCPArgs) || len(srv.Env) > 0 {
		t.Fatalf(".mcp.json: %+v", mcpCfg)
	}
	for _, a := range srv.Args {
		if a == "--allow-changes" || a == "--propose" || a == "--toolset" {
			t.Errorf("the plugin's connection must be the read-only core: %v", srv.Args)
		}
	}
	if n, err := pluginMCPTools(); err != nil || n == 0 {
		t.Errorf("the plugin's MCP server: %d tools, %v", n, err)
	}
	for _, r := range pluginChecks() {
		if !r.OK {
			t.Errorf("%s: %s", r.Name, r.Detail)
		}
	}
	readme, _ := os.ReadFile(filepath.Join(repoRoot, skillsinstall.PluginDir, "README.md"))
	if !strings.Contains(string(readme), "**It needs the `metarush` program.**") {
		t.Error("the plugin's README doesn't say plainly that it needs the metarush program")
	}
}

// The skills for other apps are the plugin's skills in the Agent Skills format:
// the same bodies and references, a name and a description only, and their
// README's links resolve (the review desk and MCP pages, not copies of them).
func TestAgentsSkillsMatchThePlugin(t *testing.T) {
	agents := filepath.Join(repoRoot, skillsinstall.AgentsDir, "skills")
	plugin := filepath.Join(repoRoot, skillsinstall.PluginDir, "skills")
	n := 0
	err := filepath.WalkDir(agents, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		rel, _ := filepath.Rel(agents, p)
		got, _ := os.ReadFile(p)
		src, err := os.ReadFile(filepath.Join(plugin, rel))
		if err != nil {
			t.Errorf("%s has no counterpart in the plugin", rel)
			return nil
		}
		if filepath.Base(rel) != "SKILL.md" {
			if string(got) != string(src) {
				t.Errorf("%s differs from the plugin's", rel)
			}
			return nil
		}
		n++
		if probs := skillsinstall.AgentsSkillProblems(filepath.Base(filepath.Dir(rel)), got); len(probs) > 0 {
			t.Errorf("%s: %v", rel, probs)
		}
		ka, _, bodyA, errA := skillsinstall.Frontmatter(got)
		kc, _, bodyC, errC := skillsinstall.Frontmatter(src)
		if errA != nil || errC != nil || string(bodyA) != string(bodyC) || !strings.HasPrefix(ka["description"], kc["description"]) ||
			!strings.Contains(ka["description"], kc["when_to_use"]) {
			t.Errorf("%s is not the plugin's skill in the Agent Skills format", rel)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n != len(skills.Names) {
		t.Errorf("%d skills for other apps, want %d", n, len(skills.Names))
	}
	readme, err := os.ReadFile(filepath.Join(repoRoot, skillsinstall.AgentsDir, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	links := regexp.MustCompile(`\]\(([^)#]+)\)`).FindAllStringSubmatch(string(readme), -1)
	var seen []string
	for _, l := range links {
		seen = append(seen, path.Base(l[1]))
		if _, err := os.Stat(filepath.Join(repoRoot, skillsinstall.AgentsDir, filepath.FromSlash(l[1]))); err != nil {
			t.Errorf("plugins/agents/README.md links %s, which doesn't exist", l[1])
		}
	}
	if !slices.Contains(seen, "review-desk.md") {
		t.Error("plugins/agents/README.md doesn't link the review desk")
	}
	block, _ := os.ReadFile(filepath.Join(repoRoot, skillsinstall.AgentsDir, "AGENTS.md"))
	for _, name := range skills.Names {
		if !strings.Contains(string(block), name) {
			t.Errorf("AGENTS.md doesn't name the skill %s", name)
		}
	}
}
