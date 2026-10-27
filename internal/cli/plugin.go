package cli

import (
	"fmt"
	"path"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/mcp"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/skills"
)

// The plugin's eval cases are the natural live cases (everyday requests that
// never say MetaRush), about a project of this name, with home folders as "~".
const pluginEvalProject = "Garden tour"

// pluginEvals is the plugin's `claude plugin eval` suite (D319): the same
// requests as `metarush skills test --live --natural`.
func pluginEvals() []skillsinstall.EvalCase {
	var out []skillsinstall.EvalCase
	for _, tc := range naturalCases(pluginEvalProject, "~") {
		c := skillsinstall.EvalCase{Name: tc.Name, Prompt: tc.Prompt, Commands: tc.WantCmd, Never: tc.ForbidCmds}
		if tc.WantSkill != "" && tc.WantSkill != "-" {
			c.Skills = append([]string{tc.WantSkill}, tc.AltSkills...)
		}
		out = append(out, c)
	}
	return out
}

// pluginRepository is everything `metarush skills package-plugin` writes.
func pluginRepository() (map[string][]byte, error) {
	return skillsinstall.Repository(pluginEvals())
}

// pluginChecks are `metarush skills test`'s rows for the plugin and the skills
// other apps read (D319–D321): the plugin's MCP server is the read-only core
// of a command this binary has, every skill in the Agent Skills format is one
// Codex takes, and the eval cases name skills that exist.
func pluginChecks() []staticResult {
	var out []staticResult
	if n, err := pluginMCPTools(); err != nil {
		out = append(out, staticResult{"(the plugin's connection)", false, err.Error()})
	} else {
		out = append(out, staticResult{"(the plugin's connection)", true, fmt.Sprintf("metarush %s: the read-only core, %d tools", strings.Join(skillsinstall.PluginMCPArgs, " "), n)})
	}
	files, err := skillsinstall.AgentsFiles()
	var probs []string
	if err != nil {
		probs = append(probs, err.Error())
	}
	n := 0
	for rel, b := range files {
		if path.Base(rel) != "SKILL.md" {
			continue
		}
		n++
		for _, p := range skillsinstall.AgentsSkillProblems(path.Dir(rel), b) {
			probs = append(probs, path.Dir(rel)+": "+p)
		}
	}
	slices.Sort(probs)
	if len(probs) > 0 {
		out = append(out, staticResult{"(skills for other apps)", false, strings.Join(probs, "; ")})
	} else {
		out = append(out, staticResult{"(skills for other apps)", true, fmt.Sprintf("%d skills in the Agent Skills format", n)})
	}
	probs = nil
	for _, c := range pluginEvals() {
		for _, s := range c.Skills {
			if !slices.Contains(skills.Names, s) {
				probs = append(probs, c.Name+" names a skill that doesn't exist: "+s)
			}
		}
		if len(c.Commands) == 0 {
			probs = append(probs, c.Name+" names no command")
		}
	}
	if len(probs) > 0 {
		out = append(out, staticResult{"(the plugin's evals)", false, strings.Join(probs, "; ")})
	} else {
		out = append(out, staticResult{"(the plugin's evals)", true, fmt.Sprintf("%d cases", len(pluginEvals()))})
	}
	return out
}

// pluginMCPTools checks the plugin's MCP arguments against this binary's own
// `mcp serve`: a real command, read-only, the core toolset; it returns how
// many tools that server offers.
func pluginMCPTools() (int, error) {
	root := &cobra.Command{Use: "metarush"}
	root.PersistentFlags().String("agent", "", "")
	root.PersistentFlags().Bool("propose", false, "")
	root.AddCommand(mcpCmd())
	cmd, rest, err := root.Find(skillsinstall.PluginMCPArgs)
	if err != nil || cmd.CommandPath() != "metarush mcp serve" {
		return 0, fmt.Errorf("metarush %s is not the MCP server command", strings.Join(skillsinstall.PluginMCPArgs, " "))
	}
	if err := cmd.ParseFlags(rest); err != nil {
		return 0, fmt.Errorf("metarush %s: %v", strings.Join(skillsinstall.PluginMCPArgs, " "), err)
	}
	if v, _ := cmd.Flags().GetBool("allow-changes"); v {
		return 0, fmt.Errorf("the plugin's connection must be read-only, not --allow-changes")
	}
	if v, _ := cmd.Flags().GetBool("propose"); v {
		return 0, fmt.Errorf("the plugin's connection must be read-only, not --propose")
	}
	if v, _ := cmd.Flags().GetString("toolset"); v != mcp.ToolsetCore {
		return 0, fmt.Errorf("the plugin's connection must offer the core, not --toolset %s", v)
	}
	list, err := mcp.ToolListFor(mcp.ToolsetCore, mcp.ReadOnly)
	if err != nil {
		return 0, err
	}
	if len(list) > mcp.MaxCoreTools {
		return 0, fmt.Errorf("the core offers %d tools, more than %d", len(list), mcp.MaxCoreTools)
	}
	return len(list), nil
}
