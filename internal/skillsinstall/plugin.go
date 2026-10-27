package skillsinstall

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush"
	"github.com/metavert-io/metarush/skills"
)

// What the repository carries for Claude Code's plugin system and for other AI
// apps (D319–D321). Everything under these paths is generated from skills/ by
// `metarush skills package-plugin` and never edited by hand; a Go test fails
// when the committed copy differs from what the generator writes.
const (
	// PluginDir is the Claude Code plugin "metarush": the skills, MCP's
	// read-only core and an eval suite.
	PluginDir = "plugins/claude-code"
	// AgentsDir holds the same skills in the Agent Skills format and an
	// AGENTS.md block, for Codex and other apps.
	AgentsDir = "plugins/agents"
	// MarketplaceFile makes the repository a Claude Code marketplace with
	// the one plugin (`claude plugin install metarush@metarush`).
	MarketplaceFile = ".claude-plugin/marketplace.json"
	// PluginName is the plugin's and the marketplace's name.
	PluginName = "metarush"
	// PluginServer is the MCP server's name in the plugin (its tools are
	// mcp__plugin_metarush_metarush__<tool> in Claude Code).
	PluginServer = "metarush"
)

// PluginMCPArgs are the arguments the plugin starts `metarush` with: MCP's
// core, read-only (D319). Changes go through the skills' commands, which
// Claude Code asks the person about, or a server the person adds themselves.
var PluginMCPArgs = []string{"mcp", "serve"}

// generatedRoots are the folders the generator owns: files under them that it
// doesn't write are stale. Eval results are a run's output, never generated.
var generatedRoots = []string{PluginDir, AgentsDir}

func generatedPath(rel string) bool {
	for _, r := range generatedRoots {
		if strings.HasPrefix(rel, r+"/") {
			return !strings.HasPrefix(rel, PluginDir+"/evals/results/")
		}
	}
	return rel == MarketplaceFile
}

// EvalCase is one case of the plugin's eval suite (`claude plugin eval`):
// an everyday request, the skill that should take it (or others that are also
// right), the commands one of which it should run, and commands it must never
// run. The cases come from `metarush skills test --live --natural`'s, so the
// two suites ask the same things.
type EvalCase struct {
	Name     string
	Prompt   string
	Skills   []string
	Commands []string
	Never    []string
}

// Release is the version the plugin says: VERSION's (scripts/bump-version.sh
// writes the plugin's along with it).
func Release() string { return strings.TrimSpace(metarush.VersionFile) }

// Repository is every generated file, by its path in the repository.
func Repository(evals []EvalCase) (map[string][]byte, error) {
	out := map[string][]byte{}
	files, err := Files()
	if err != nil {
		return nil, err
	}
	for rel, b := range files {
		out[PluginDir+"/skills/"+rel] = b
	}
	agents, err := AgentsFiles()
	if err != nil {
		return nil, err
	}
	for rel, b := range agents {
		out[AgentsDir+"/skills/"+rel] = b
	}
	out[PluginDir+"/.claude-plugin/plugin.json"] = mustJSON(pluginManifest())
	out[PluginDir+"/.mcp.json"] = mustJSON(map[string]any{"mcpServers": map[string]mcpServer{
		PluginServer: {Command: "metarush", Args: PluginMCPArgs},
	}})
	out[MarketplaceFile] = mustJSON(marketplace{
		Name:        PluginName,
		Owner:       person{Name: "Metavert LLC"},
		Description: "MetaRush for Claude Code: the AI video studio that plays by your rules.",
		Plugins:     []marketplaceEntry{{Name: PluginName, Source: "./" + PluginDir, Description: pluginDescription, Category: "media"}},
	})
	readme, err := pluginReadme()
	if err != nil {
		return nil, err
	}
	out[PluginDir+"/README.md"] = readme
	out[AgentsDir+"/README.md"] = []byte(agentsReadme())
	out[AgentsDir+"/AGENTS.md"] = []byte(AgentsMDBlock())
	for _, c := range evals {
		for rel, b := range evalFiles(c) {
			out[PluginDir+"/evals/"+c.Name+"/"+rel] = b
		}
	}
	return out, nil
}

const pluginDescription = "Edit video by talking to Claude Code, in your style and with your sign-off: import footage, find the best moments, cut, caption, fix the sound, add graphics, check the finished file and deliver it. Needs the metarush program installed (metarush setup)."

// The manifests as Claude Code reads them, fields in the order its
// documentation lists them.
type (
	person struct {
		Name string `json:"name"`
	}
	mcpServer struct {
		Command string   `json:"command"`
		Args    []string `json:"args"`
	}
	pluginJSON struct {
		Name        string   `json:"name"`
		DisplayName string   `json:"displayName"`
		Version     string   `json:"version"`
		Description string   `json:"description"`
		Author      person   `json:"author"`
		Homepage    string   `json:"homepage"`
		Repository  string   `json:"repository"`
		License     string   `json:"license"`
		Keywords    []string `json:"keywords"`
	}
	marketplace struct {
		Name        string             `json:"name"`
		Owner       person             `json:"owner"`
		Description string             `json:"description"`
		Plugins     []marketplaceEntry `json:"plugins"`
	}
	marketplaceEntry struct {
		Name        string `json:"name"`
		Source      string `json:"source"`
		Description string `json:"description"`
		Category    string `json:"category"`
	}
)

func pluginManifest() pluginJSON {
	return pluginJSON{
		Name:        PluginName,
		DisplayName: "MetaRush",
		Version:     Release(),
		Description: pluginDescription,
		Author:      person{Name: "Metavert LLC"},
		Homepage:    "https://github.com/metavert-io/metarush",
		Repository:  "https://github.com/metavert-io/metarush",
		License:     "MIT",
		Keywords:    []string{"video", "editing", "captions", "remotion", "mcp"},
	}
}

func mustJSON(v any) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(v); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

// evalFiles is one case as `claude plugin eval` reads it: prompt.md and a
// grader per check (the skill, the command, each command it must never run).
// Claude Code counts the skill's grader only as a sign that the plugin fired
// (it can't pass without the plugin); the command graders score both arms.
func evalFiles(c EvalCase) map[string][]byte {
	out := map[string][]byte{}
	out["prompt.md"] = []byte(fmt.Sprintf(`---
description: %s
tags: [skills]
max_turns: 12
allowed_tools: [Read, Glob, Grep, Skill]
---

%s
`, yamlQuote("An everyday request the skill "+strings.Join(c.Skills, " or ")+" should take, without saying MetaRush."), c.Prompt))
	if len(c.Skills) > 0 {
		out["graders/skill.md"] = []byte(fmt.Sprintf(`---
type: tool_used
tool: Skill
input_match: '"skill"\s*:\s*"(?:[\w-]+:)?(?:%s)"'
---
`, strings.Join(c.Skills, "|")))
	}
	if len(c.Commands) > 0 {
		out["graders/command.md"] = []byte(fmt.Sprintf(`---
type: tool_used
tool: Bash
input_match: '%s'
---
`, commandMatch(c.Commands)))
	}
	for i, n := range c.Never {
		out[fmt.Sprintf("graders/never-%d.md", i+1)] = []byte(fmt.Sprintf(`---
type: tool_used
tool: Bash
input_match: '%s'
min: 0
max: 0
arm: both
---
`, commandMatch([]string{n})))
	}
	return out
}

// commandMatch is a regular expression (JavaScript's, in a single-quoted YAML
// string) for a Bash call whose command has one of these in it.
func commandMatch(cmds []string) string {
	var alts []string
	for _, c := range cmds {
		alts = append(alts, regexp.QuoteMeta(c))
	}
	return `"command"\s*:\s*"(?:[^"\\]|\\.)*?(?:` + strings.Join(alts, "|") + `)`
}

func yamlQuote(s string) string {
	q, _ := yamlString(s)
	return q
}

// pluginReadme is the plugin's README: what it is, that it needs the metarush
// program, how to install it, what is inside.
func pluginReadme() ([]byte, error) {
	var b strings.Builder
	b.WriteString(`# MetaRush for Claude Code

Edit video by talking to Claude Code: your footage, your style, your sign-off. This plugin teaches Claude Code
MetaRush's video skills and connects it to MetaRush to look things up.

**It needs the ` + "`metarush`" + ` program.** The plugin holds instructions, not MetaRush itself: every skill runs
` + "`metarush …`" + ` commands, and the connection starts ` + "`metarush mcp serve`" + `. Install MetaRush first, set it up, and keep
` + "`metarush`" + ` on your PATH:

` + "```bash" + `
brew install --cask metavert-io/metarush/metarush   # or a release archive: put metarush on your PATH
metarush setup --skills=false                        # the plugin brings the skills
metarush doctor                                      # everything ready?
` + "```" + `

Then add the plugin:

` + "```bash" + `
claude plugin marketplace add metavert-io/metarush
claude plugin install metarush@metarush
` + "```" + `

Already ran ` + "`metarush setup`" + ` with its skills? It put the same skills in ` + "`~/.claude/skills`" + `. Keep one copy: remove
those folders (` + strings.Join(skills.Names, ", ") + `) or don't install the plugin.

## What's inside

**Skills** (Claude Code picks them by what you ask; you never have to say MetaRush):

| Skill | What it covers |
|---|---|
`)
	for _, n := range skills.Names {
		raw, err := fs.ReadFile(skills.FS(), n+"/SKILL.md")
		if err != nil {
			return nil, err
		}
		keys, _, _, err := Frontmatter(raw)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", n, err)
		}
		first, _, _ := strings.Cut(keys["description"], " — ")
		first, _, _ = strings.Cut(first, ". ")
		// The descriptions speak to Claude ("the person's video"); the README speaks to that person.
		first = strings.ReplaceAll(strings.ReplaceAll(first, "the person's", "your"), "|", "/")
		fmt.Fprintf(&b, "| `%s` | %s |\n", n, strings.TrimSuffix(first, "."))
	}
	b.WriteString(`
**A read-only connection** (MCP server ` + "`" + PluginServer + "`" + `: ` + "`metarush " + strings.Join(PluginMCPArgs, " ") + "`" + `): about ten tools — find, describe and run
for any reading step, plus the everyday ones (what's happening now, projects, search, a cut sheet, its check, a
render's quality check). It changes nothing, so you can allow all of its tools. Changes go through the skills'
commands, and Claude Code asks you before each one; only people approve versions, deliver, spend money or use
someone's face or voice. To let another app propose changes as drafts for you to review, see MetaRush's
docs/review-desk.md.

## For maintainers

This folder is generated from ` + "`skills/`" + ` by ` + "`metarush skills package-plugin`" + ` (with ` + "`plugins/agents`" + ` and
` + "`.claude-plugin/marketplace.json`" + `); don't edit it by hand. ` + "`go test ./internal/cli -run TestPlugin`" + ` fails when it is out of
date, and ` + "`claude plugin validate --strict plugins/claude-code`" + ` checks it as Claude Code reads it.

` + "`evals/`" + ` is a ` + "`claude plugin eval`" + ` suite with the same everyday requests as ` + "`metarush skills test --live --natural`" + `.
Every run is a real Claude Code session on your account, so run it on purpose, never in CI:

` + "```bash" + `
claude plugin eval plugins/claude-code --allow-tools "Bash(metarush *)" --runs 1 --max-cost-usd 5
` + "```" + `
`)
	return []byte(b.String()), nil
}

func agentsReadme() string {
	return `# MetaRush's skills for Codex and other AI apps

The same video skills as MetaRush's Claude Code plugin, in the Agent Skills format that Codex reads from
` + "`.agents/skills`" + ` (and ` + "`~/.agents/skills`" + `), as do other apps that read Agent Skills; ` + "`AGENTS.md`" + ` is MetaRush's part of a
project's AGENTS.md.

**They need the ` + "`metarush`" + ` program.** Every skill runs ` + "`metarush …`" + ` commands: install MetaRush and run
` + "`metarush setup`" + ` first, and keep ` + "`metarush`" + ` on your PATH.

Install them from the program (they are the same files):

` + "```bash" + `
metarush skills install --agents --user                  # ~/.agents/skills, for every project
metarush skills install --agents --project ~/my-video    # its .agents/skills and MetaRush's part of its AGENTS.md
` + "```" + `

or copy ` + "`skills/*`" + ` into one of those folders.

Connecting an app over MCP, and holding it to proposing (every change a draft a person reviews, ` + "`--agent`" + ` naming
the app): [the review desk](../../docs/review-desk.md) and [MetaRush as an MCP server](../../docs/mcp.md).

This folder is generated from ` + "`skills/`" + ` by ` + "`metarush skills package-plugin`" + `; don't edit it by hand.
`
}

// Drift compares the generated files with a repository's: files that differ,
// files missing, and files under the generated folders that the generator
// doesn't write. Empty means the repository's copy is current.
func Drift(root string, want map[string][]byte) ([]string, error) {
	var out []string
	for rel, b := range want {
		got, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(rel)))
		switch {
		case err != nil:
			out = append(out, "missing "+rel)
		case !bytes.Equal(got, b):
			out = append(out, "differs "+rel)
		}
	}
	extra, err := extraFiles(root, want)
	if err != nil {
		return nil, err
	}
	for _, rel := range extra {
		out = append(out, "not generated "+rel)
	}
	sort.Strings(out)
	return out, nil
}

func extraFiles(root string, want map[string][]byte) ([]string, error) {
	var out []string
	for _, dir := range generatedRoots {
		base := filepath.Join(root, filepath.FromSlash(dir))
		err := filepath.WalkDir(base, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, _ := filepath.Rel(root, p)
			rel = filepath.ToSlash(rel)
			if _, ok := want[rel]; !ok && generatedPath(rel) && path.Base(rel) != ".DS_Store" {
				out = append(out, rel)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// WriteRepository writes the generated files into a repository and removes
// files under the generated folders that the generator no longer writes. It
// returns what changed.
func WriteRepository(root string, want map[string][]byte) ([]string, error) {
	changed, err := Drift(root, want)
	if err != nil {
		return nil, err
	}
	extra, err := extraFiles(root, want)
	if err != nil {
		return nil, err
	}
	for _, rel := range extra {
		if err := os.Remove(filepath.Join(root, filepath.FromSlash(rel))); err != nil {
			return nil, err
		}
	}
	for rel, b := range want {
		dst := filepath.Join(root, filepath.FromSlash(rel))
		if cur, err := os.ReadFile(dst); err == nil && bytes.Equal(cur, b) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := os.WriteFile(dst, b, 0o644); err != nil {
			return nil, err
		}
	}
	return changed, nil
}
