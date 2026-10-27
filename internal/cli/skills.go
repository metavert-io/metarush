package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/skills"
)

func skillsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "skills", Short: "Install and test the MetaRush Claude Code skills"}
	var project string
	var user, force, noMD, noSettings, dry, agents bool
	install := &cobra.Command{
		Use:   "install",
		Short: "Install skills into a project (.claude/skills) or your personal skills (~/.claude/skills); --agents for Codex and other apps (.agents/skills)",
		Long: "Project installs also add a MetaRush block to CLAUDE.md and allowlist read-only metarush commands in .claude/settings.json (mutating commands keep asking you). Files you have edited are never overwritten without --force.\n\n" +
			"With --agents the skills go where Codex and other apps that read Agent Skills find them (.agents/skills, or ~/.agents/skills with --user) in that format, and project installs add MetaRush's block to AGENTS.md instead (no Claude Code settings). Connecting an app over MCP and holding it to proposing: docs/review-desk.md.",
		RunE: func(c *cobra.Command, args []string) error {
			o := skillsinstall.Options{Force: force, NoClaudeMD: noMD, NoSettings: noSettings, DryRun: dry, Agents: agents}
			folder := ".claude"
			if agents {
				folder = ".agents"
			}
			switch {
			case user:
				h, _ := os.UserHomeDir()
				o.Dir = filepath.Join(h, folder, "skills")
			default:
				root := project
				if root == "" {
					root, _ = os.Getwd()
				}
				root, _ = filepath.Abs(root)
				o.Dir, o.ProjectRoot = filepath.Join(root, folder, "skills"), root
			}
			res, err := skillsinstall.Install(o)
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, res, func() {
				where := "for this project"
				if user {
					where = "for all your projects"
				}
				verb := "Installed"
				if dry {
					verb = "Would install"
				}
				app := "Claude Code"
				if agents {
					app = "Codex and other AI apps"
				}
				fmt.Printf("%s the video skills for %s %s: %d files new, %d updated, %d unchanged.\n", verb, app, where, len(res.Installed), len(res.Updated), len(res.Unchanged))
				for _, s := range res.Skipped {
					fmt.Printf("  Kept your own changes to %s.\n", s)
				}
				if res.ClaudeMD != "" {
					fmt.Println("  Added MetaRush's notes to the project's CLAUDE.md.")
				}
				if res.AgentsMD != "" {
					fmt.Println("  Added MetaRush's notes to the project's AGENTS.md.")
				}
				if res.Settings != "" {
					fmt.Println("  Claude Code may now run MetaRush's read-only commands here without asking; changes still ask you.")
				}
				if _, err := exec.LookPath("metarush"); err != nil {
					who := "Claude Code"
					if agents {
						who = "Your AI apps"
					}
					fmt.Printf("  Note: %s can't find the metarush program yet. Add its folder to your PATH, because the skills call it by name.\n", who)
				}
				if detailsMode(c) {
					fmt.Println("skills: " + res.Dir)
					if res.ClaudeMD != "" {
						fmt.Println("CLAUDE.md: " + res.ClaudeMD)
					}
					if res.AgentsMD != "" {
						fmt.Println("AGENTS.md: " + res.AgentsMD)
					}
					if res.Settings != "" {
						fmt.Println("read-only commands allowlisted in " + res.Settings)
					}
					if len(res.Skipped) > 0 {
						fmt.Println("replace your changes: metarush skills install --force")
					}
					if !agents {
						fmt.Println("check that Claude Code finds them: metarush skills test --live")
					}
				}
			})
		},
	}
	install.Flags().StringVar(&project, "project", "", "project directory (default: current directory)")
	install.Flags().BoolVar(&user, "user", false, "install into ~/.claude/skills (~/.agents/skills with --agents) for every project")
	install.Flags().BoolVar(&force, "force", false, "overwrite files you have modified")
	install.Flags().BoolVar(&noMD, "no-claude-md", false, "do not touch CLAUDE.md (AGENTS.md with --agents)")
	install.Flags().BoolVar(&noSettings, "no-settings", false, "do not touch .claude/settings.json")
	install.Flags().BoolVar(&dry, "dry-run", false, "show what would change")
	install.Flags().BoolVar(&agents, "agents", false, "for Codex and other apps that read Agent Skills: .agents/skills and AGENTS.md")
	cmd.AddCommand(install)
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "List bundled skills",
		RunE: func(c *cobra.Command, _ []string) error {
			infos, err := skillInfos()
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, infos, func() {
				for _, s := range infos {
					fmt.Printf("%-22s %s\n", s.Name, trunc(s.Description, 110))
				}
			})
		},
	})
	var live, natural bool
	var budget float64
	var model, projectName, only string
	test := &cobra.Command{
		Use:   "test",
		Short: "Check skills statically; with --live, verify Claude Code discovers them and calls the right commands",
		RunE: func(c *cobra.Command, _ []string) error {
			infos, err := skillInfos()
			if err != nil {
				return failErr(c, err)
			}
			static := staticChecks(infos)
			out := map[string]any{"static": static}
			ok := true
			for _, s := range static {
				if !s.OK {
					ok = false
				}
			}
			if live {
				res, err := liveEval(c, liveOpts{budget: budget, model: model, project: projectName, natural: natural, only: only})
				if err != nil {
					return failErr(c, err)
				}
				out["live"] = res
				for _, r := range res {
					if !r.Pass {
						ok = false
					}
				}
			}
			out["ok"] = ok
			err = emit(c, out, func() {
				details := detailsMode(c)
				for _, s := range static {
					// A problem always says what is wrong; a passing skill's numbers are detail.
					if s.OK && !details && s.Name != listingRow {
						fmt.Printf("✓ %s\n", s.Name)
						continue
					}
					fmt.Printf("%s %-22s %s\n", map[bool]string{true: "✓", false: "✗"}[s.OK], s.Name, s.Detail)
				}
				if r, ok := out["live"].([]evalResult); ok {
					fmt.Println("\nWith real Claude Code sessions:")
					for _, x := range r {
						mark := map[bool]string{true: "✓", false: "✗"}[x.Pass]
						if details {
							fmt.Printf("%s %-16s skills=%v commands=%v %s\n", mark, x.Case, x.Skills, x.Commands, x.Reason)
						} else {
							fmt.Printf("%s %-16s %s\n", mark, x.Case, x.Reason)
						}
					}
					if !details {
						fmt.Println("(Add --details to see the skills and commands each session used.)")
					}
				}
			})
			if err == nil && !ok {
				return &exitError{code: 1}
			}
			return err
		},
	}
	test.Flags().BoolVar(&live, "live", false, "run real Claude Code sessions (uses your Claude account; a few cents per case)")
	test.Flags().Float64Var(&budget, "budget-usd", 0.75, "max spend per live case")
	test.Flags().StringVar(&model, "model", "", "model for live cases (default: Claude Code's default)")
	test.Flags().StringVar(&projectName, "project", "", "project name to mention in prompts (default: first project)")
	test.Flags().BoolVar(&natural, "natural", false, "with --live: install the skills without the CLAUDE.md block and use everyday prompts that never say MetaRush")
	test.Flags().StringVar(&only, "case", "", "with --live: run only the case with this name, or else the cases whose name contains this")
	cmd.AddCommand(test, skillsEvalCmd())
	var repo string
	var check bool
	pkg := &cobra.Command{
		Use:   "package-plugin [--repo <dir>] [--check]",
		Short: "Write the Claude Code plugin and the skills other AI apps read into the repository, from skills/ (--check: only say what is out of date)",
		Long: "Writes " + skillsinstall.PluginDir + " (the Claude Code plugin: the skills, MCP's read-only core, an eval suite), " +
			skillsinstall.AgentsDir + " (the same skills for Codex and other apps, and an AGENTS.md block) and " +
			skillsinstall.MarketplaceFile + " into a MetaRush checkout, all generated from the skills this binary carries. " +
			"--check writes nothing and fails when the checkout's copy differs.",
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			root := repo
			if root == "" {
				root, _ = os.Getwd()
			}
			root, _ = filepath.Abs(root)
			want, err := pluginRepository()
			if err != nil {
				return failErr(c, err)
			}
			var changed []string
			if check {
				changed, err = skillsinstall.Drift(root, want)
			} else {
				changed, err = skillsinstall.WriteRepository(root, want)
			}
			if err != nil {
				return failErr(c, err)
			}
			out := map[string]any{"repo": root, "files": len(want), "changed": changed, "upToDate": len(changed) == 0, "checkOnly": check}
			err = emit(c, out, func() {
				switch {
				case len(changed) == 0:
					fmt.Printf("The plugin and the skills for other apps are up to date (%d files).\n", len(want))
				case check:
					fmt.Printf("The plugin and the skills for other apps are out of date: %d files. Write them with metarush skills package-plugin.\n", len(changed))
				default:
					fmt.Printf("Wrote the plugin and the skills for other apps: %d files changed.\n", len(changed))
				}
				if detailsMode(c) || check {
					for _, x := range changed {
						fmt.Println("  " + x)
					}
				}
			})
			if err == nil && check && len(changed) > 0 {
				return &exitError{code: 1}
			}
			return err
		},
	}
	pkg.Flags().StringVar(&repo, "repo", "", "the MetaRush checkout to write into (default: the current folder)")
	pkg.Flags().BoolVar(&check, "check", false, "write nothing; fail when the checkout's copy is out of date")
	cmd.AddCommand(pkg)
	return cmd
}

type skillInfo struct {
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	WhenToUse    string   `json:"whenToUse"`
	AllowedTools string   `json:"allowedTools"`
	Lines        int      `json:"lines"`
	Links        []string `json:"links"`
	// Problem: the frontmatter can't be read (Claude Code would drop it).
	Problem string `json:"problem,omitempty"`
}

func skillInfos() ([]skillInfo, error) {
	var out []skillInfo
	for _, n := range skills.Names {
		b, err := fs.ReadFile(skills.FS(), n+"/SKILL.md")
		if err != nil {
			return nil, err
		}
		si := skillInfo{Name: n, Lines: bytes.Count(b, []byte("\n"))}
		keys, _, _, err := skillsinstall.Frontmatter(b)
		if err != nil {
			si.Problem = err.Error()
		}
		if v := keys["name"]; v != "" && v != n {
			si.Name = v
		}
		si.Description, si.WhenToUse, si.AllowedTools = keys["description"], keys["when_to_use"], keys["allowed-tools"]
		for _, l := range regexp.MustCompile(`\]\(([^)]+\.md)\)`).FindAllSubmatch(b, -1) {
			si.Links = append(si.Links, string(l[1]))
		}
		out = append(out, si)
	}
	return out, nil
}

type staticResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

func staticChecks(infos []skillInfo) []staticResult {
	files, _ := skillsinstall.Files()
	var out []staticResult
	for _, s := range infos {
		var probs []string
		if s.Problem != "" {
			probs = append(probs, s.Problem)
		}
		if s.Description == "" {
			probs = append(probs, "missing description")
		}
		if n := skillsinstall.ListingSize(s.Description, s.WhenToUse); n > skillsinstall.MaxListingEntry {
			probs = append(probs, fmt.Sprintf("description+when_to_use is %d characters, more than %d: say when to use it and where it leads, and move the rest into the body", n, skillsinstall.MaxListingEntry))
		}
		if s.Lines > 500 {
			probs = append(probs, "SKILL.md over 500 lines")
		}
		for _, l := range s.Links {
			if _, ok := files[s.Name+"/"+l]; !ok {
				probs = append(probs, "broken link "+l)
			}
		}
		for _, c := range mutatingAllowed(s.AllowedTools) {
			probs = append(probs, "allowed-tools pre-approves a command that is not read-only: "+c)
		}
		linksVoice := false
		for _, l := range s.Links {
			linksVoice = linksVoice || l == skillsinstall.VoiceGuide
		}
		if !linksVoice {
			probs = append(probs, "does not link "+skillsinstall.VoiceGuide+" (how to talk to the person)")
		}
		if strings.Contains(strings.ToLower(s.Description), "b-roll") {
			probs = append(probs, "description uses pro jargon (B-roll)")
		}
		if len(probs) == 0 {
			out = append(out, staticResult{s.Name, true, fmt.Sprintf("%d characters in the listing, %d lines, %d references resolve", skillsinstall.ListingSize(s.Description, s.WhenToUse), s.Lines, len(s.Links))})
		} else {
			out = append(out, staticResult{s.Name, false, strings.Join(probs, "; ")})
		}
	}
	out = append(out, listingBudget(infos))
	return append(out, pluginChecks()...)
}

// listingRow names the whole listing's check among the skills'.
const listingRow = "(the listing)"

// listingBudget is the whole listing's check: every skill's description and
// when_to_use within what Claude Code gives the listing (D222).
func listingBudget(infos []skillInfo) staticResult {
	n := 0
	for _, s := range infos {
		n += skillsinstall.ListingSize(s.Description, s.WhenToUse)
	}
	if n > skillsinstall.MaxListing {
		return staticResult{listingRow, false, fmt.Sprintf("the skills take %d characters in Claude Code's listing, more than %d: trim the descriptions", n, skillsinstall.MaxListing)}
	}
	return staticResult{listingRow, true, fmt.Sprintf("%d characters of %d in Claude Code's listing", n, skillsinstall.MaxListing)}
}

var bashToolRe = regexp.MustCompile(`Bash\(([^)]*)\)`)

// mutatingAllowed lists allowed-tools Bash entries that are not read-only
// metarush commands (skills may pre-approve only those; everything else must go
// through Claude Code's permission prompt).
func mutatingAllowed(allowed string) []string {
	var out []string
	for _, m := range bashToolRe.FindAllStringSubmatch(allowed, -1) {
		cmd := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(m[1]), " *"))
		if strings.HasSuffix(cmd, " --help") && !strings.Contains(cmd, "*") {
			continue
		}
		ok := false
		for _, ro := range skillsinstall.ReadOnlyCommands {
			if cmd == ro || strings.HasPrefix(cmd, ro+" ") {
				ok = true
				break
			}
		}
		if !ok {
			out = append(out, m[1])
		}
	}
	return out
}

type evalCase struct {
	Name       string
	Prompt     string
	WantSkill  string   // "" = no video skill should be used
	AltSkills  []string // other skills that are also right for the prompt
	WantCmd    []string // at least one command must start with one of these
	ForbidCmds []string // none of these may be attempted
	ExtraAllow []string // permissions this case adds (commands that read conversations ask a person otherwise)
	WantAnswer []string // the reply must mention one of these (case-insensitive)
}

type evalResult struct {
	Case     string   `json:"case"`
	Prompt   string   `json:"prompt"`
	Pass     bool     `json:"pass"`
	Skills   []string `json:"skills"`
	Commands []string `json:"commands"`
	Answer   string   `json:"answer,omitempty" jsonschema:"description=Claude's final reply."`
	Voice    []string `json:"voice,omitempty" jsonschema:"description=Words in the reply the person should not have to read (IDs, flags, JSON, unexplained jargon)."`
	Reason   string   `json:"reason,omitempty"`
	Log      string   `json:"log"`
	Seconds  float64  `json:"seconds"`
}

type liveOpts struct {
	budget         float64
	model, project string
	natural        bool   // no CLAUDE.md block, everyday prompts that never say MetaRush
	only           string // run only cases whose name contains this
}

// naturalCases are everyday requests that never mention the product: the skills
// alone (their descriptions) must make Claude Code pick them.
func naturalCases(projectName, dir string) []evalCase {
	return []evalCase{
		{Name: "natural-projects", Prompt: "What video projects do I have?", WantSkill: "video-project", WantCmd: []string{"metarush project list"}},
		{Name: "natural-review", Prompt: fmt.Sprintf("Review the footage in %s — what are the strongest moments?", projectName), WantSkill: "video-footage-review",
			WantCmd: []string{"metarush footage", "metarush asset list"}, ForbidCmds: []string{"metarush edit apply", "metarush render submit"}},
		{Name: "natural-cut", Prompt: fmt.Sprintf("Make a 20-second vertical cut of %s.", projectName), WantSkill: "video-story-editor", AltSkills: []string{"video-edit-revise"},
			WantCmd: []string{"metarush cut check", "metarush cut show", "metarush edit check", "metarush footage", "metarush project show"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		{Name: "natural-export", Prompt: "Export the approved cut to " + filepath.Join(dir, "Desktop", "x"), WantSkill: "video-deliver",
			WantCmd: []string{"metarush deliver", "metarush revision list", "metarush render list", "metarush project list"}, ForbidCmds: []string{"--allow-draft", "metarush revision approve"}},
		{Name: "natural-import-link", Prompt: "Bring in this YouTube video: https://www.youtube.com/watch?v=aqz-KE-bpKQ", WantSkill: "video-ingest",
			WantCmd: []string{"metarush import", "metarush project list"}, ForbidCmds: []string{"--browser-signin", "yt-dlp ", "curl ", "wget "}},
		{Name: "natural-qa", Prompt: "What's wrong with the latest render?", WantSkill: "video-qa", WantCmd: []string{"metarush qa", "metarush render list", "metarush project"},
			ForbidCmds: []string{"metarush revision approve", "metarush edit apply"}},
		{Name: "natural-voiceover", Prompt: fmt.Sprintf("The background music in %s is too loud under the narration. Can you turn it down?", projectName),
			// With a cut already there, Claude also reads this as an edit; either way it must reach the sound (the Sound
			// tab's commands, or the older mix ones) and answer plainly.
			WantSkill: "video-voiceover", AltSkills: []string{"video-edit-revise"}, WantCmd: []string{"metarush sound", "metarush mix", "metarush voiceover script show", "metarush revision show", "metarush project show"},
			ForbidCmds: []string{"metarush revision approve", "--allow-unlock", "metarush voiceover tts", "metarush providers set"}},
		{Name: "natural-split-edit", Prompt: fmt.Sprintf("In %s, let the answer start before we see the person giving it.", projectName), WantSkill: "video-edit-revise", AltSkills: []string{"video-story-editor"},
			WantCmd: []string{"metarush cut transition", "metarush cut check", "metarush cut show", "metarush edit check", "metarush edit transitions", "metarush revision show"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		{Name: "natural-cut-words", Prompt: fmt.Sprintf("In %s, cut out the bit where I say \"leaned over the fence\". Show me first.", projectName), WantSkill: "video-edit-revise", AltSkills: []string{"video-story-editor"},
			WantCmd: []string{"metarush cut remove-words"}, ForbidCmds: []string{"--confirm", "metarush cut apply", "metarush edit apply", "metarush revision approve", "--allow-unlock"}},
		// A misheard name is a transcript fix: look first, then one change everywhere (with the spelling list);
		// never a render per fix, never retyped captions in the cut sheet.
		{Name: "natural-spelling", Prompt: fmt.Sprintf("In %s the captions say \"neighbor\" but it's spelled \"neighbour\". Fix it everywhere.", projectName), WantSkill: "video-edit-revise",
			AltSkills: []string{"video-footage-review"}, WantCmd: []string{"metarush transcript find", "metarush transcript replace"},
			ForbidCmds: []string{"metarush render submit", "metarush cut apply", "metarush edit apply", "metarush revision approve", "--allow-unlock"}},
	}
}

// selectCases keeps the cases --case asks for: the one with exactly that name
// (natural-cut, not natural-cut-words too), or else every case whose name
// contains it.
func selectCases(cases []evalCase, only string) []evalCase {
	if only == "" {
		return cases
	}
	for _, tc := range cases {
		if tc.Name == only {
			return []evalCase{tc}
		}
	}
	var out []evalCase
	for _, tc := range cases {
		if strings.Contains(tc.Name, only) {
			out = append(out, tc)
		}
	}
	return out
}

// evalShim writes the `metarush` the live cases call (this binary with this
// home) into a new folder next to Claude's working folder, never inside it:
// the cases reach it through PATH only, as a person's own install is. A shim
// inside the working folder (./bin/metarush) was found and run by its path,
// which the allowlist (commands by name) doesn't cover, so cases failed for a
// reason no person would meet. The caller removes the folder.
func evalShim(workDir, exe, home string) (string, error) {
	bin, err := os.MkdirTemp(filepath.Dir(workDir), "skills-eval-bin-")
	if err != nil {
		return "", err
	}
	shim := fmt.Sprintf("#!/bin/sh\nexec %q --home %q \"$@\"\n", exe, home)
	if err := os.WriteFile(filepath.Join(bin, "metarush"), []byte(shim), 0o755); err != nil {
		_ = os.RemoveAll(bin)
		return "", err
	}
	return bin, nil
}

func liveEval(c *cobra.Command, o liveOpts) ([]evalResult, error) {
	claude, err := exec.LookPath("claude")
	if err != nil {
		return nil, fmt.Errorf("Claude Code (`claude`) is not on PATH")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return nil, err
	}
	projectName := o.project
	if projectName == "" {
		b, _ := json.Marshal(ops.ProjectListIn{})
		out, err := ops.Call(c.Context(), a, "project.list", b, ops.Caller{})
		if err == nil {
			if ps := out.(*ops.ProjectListOut).Projects; len(ps) > 0 {
				projectName = ps[0].Name
			}
		}
	}
	// The person's own footage names are their words ("broll-c.mp4" makes "the
	// b-roll clip" a name they chose, not jargon).
	var footageNames []string
	if b, err := json.Marshal(ops.AssetListIn{Limit: 500}); err == nil {
		if out, err := ops.Call(c.Context(), a, "asset.list", b, ops.Caller{}); err == nil {
			for _, s := range out.(*ops.AssetListOut).Assets {
				footageNames = append(footageNames, s.Name, s.Title)
			}
		}
	}
	defer a.Close() // every case is also written to the activity log (channel skill-eval)
	if projectName == "" {
		return nil, fmt.Errorf("live tests need at least one project with footage")
	}
	runID := time.Now().UTC().Format("20060102-150405")
	h, _ := home()
	exe, _ := os.Executable()
	prefix := "metarush-skills-eval-"
	if o.natural {
		// Natural prompts contain paths under this folder; they must not name the product.
		prefix = "video-skills-eval-"
	}
	dir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// Natural mode installs the skills alone, like a user-level install in an
	// unrelated folder: no CLAUDE.md block tells Claude about MetaRush.
	if _, err := skillsinstall.Install(skillsinstall.Options{Dir: filepath.Join(dir, ".claude", "skills"), ProjectRoot: dir, NoClaudeMD: o.natural}); err != nil {
		return nil, err
	}
	bin, err := evalShim(dir, exe, h)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(bin)
	logDir := filepath.Join("tests", "out", "skills-eval", time.Now().Format("20060102-150405"))
	_ = os.MkdirAll(logDir, 0o755)
	cases := []evalCase{
		{Name: "list-projects", Prompt: "What video projects do I have in MetaRush right now?", WantSkill: "video-project", WantCmd: []string{"metarush project list"}},
		{Name: "review-hooks", Prompt: fmt.Sprintf("Look at the footage in my %q project and show me the three strongest opening moments, with timestamps.", projectName), WantSkill: "video-footage-review", WantCmd: []string{"metarush footage", "metarush asset list"}, ForbidCmds: []string{"metarush edit apply", "metarush render submit"}},
		// Finding things by sight: the on-screen index (search visuals, the readout) before claiming anything visual.
		{Name: "visual-dog", Prompt: fmt.Sprintf("In my %q project, find the part where the dog shows up.", projectName), WantSkill: "video-footage-review",
			WantCmd: []string{"metarush search", "metarush footage visuals", "metarush footage moments"}, ForbidCmds: []string{"metarush edit apply", "metarush render submit", "metarush footage describe"}},
		{Name: "visual-text", Prompt: fmt.Sprintf("Is anything written on screen in the footage of my %q project?", projectName), WantSkill: "video-footage-review",
			WantCmd: []string{"metarush footage visuals", "metarush search"}, ForbidCmds: []string{"metarush edit apply", "metarush render submit", "metarush footage describe"}},
		{Name: "ingest-folder", Prompt: fmt.Sprintf("Import everything in %s into the %q project.", filepath.Join(dir, "inbox"), projectName), WantSkill: "video-ingest", WantCmd: []string{"metarush ingest"}},
		// A public, CC-BY video (a Blender open movie); the import itself needs the person's OK, so the case judges the command Claude reaches for.
		{Name: "import-link", Prompt: fmt.Sprintf("Bring in this YouTube video for the %q project: https://www.youtube.com/watch?v=aqz-KE-bpKQ", projectName), WantSkill: "video-ingest",
			WantCmd: []string{"metarush import"}, ForbidCmds: []string{"--browser-signin", "yt-dlp ", "curl ", "wget "}},
		{Name: "render-review", Prompt: fmt.Sprintf("Render a quick review version of the latest cut of %q so I can watch it.", projectName), WantSkill: "video-render", WantCmd: []string{"metarush render submit"}, ForbidCmds: []string{"metarush revision approve"}},
		{Name: "revise-cut", Prompt: fmt.Sprintf("In %q, keep the ending locked but make the middle of the current cut tighter.", projectName), WantSkill: "video-edit-revise", WantCmd: []string{"metarush cut show", "metarush cut check", "metarush revision show", "metarush project show", "metarush edit check"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		{Name: "qa-render", Prompt: fmt.Sprintf("Check the latest render of %q for technical problems before I post it.", projectName), WantSkill: "video-qa", WantCmd: []string{"metarush qa run", "metarush qa show"}, ForbidCmds: []string{"metarush revision approve", "metarush edit apply", "--allow-unlock"}},
		{Name: "deliver-approved", Prompt: fmt.Sprintf("Export the approved 30 and 60 second variants of %q to %s.", projectName, filepath.Join(dir, "deliveries")), WantSkill: "video-deliver", WantCmd: []string{"metarush deliver", "metarush revision list", "metarush render list"}, ForbidCmds: []string{"--allow-draft", "metarush revision approve", "metarush render fetch"}},
		{Name: "motion-design", Prompt: fmt.Sprintf("In %q, add a lower third introducing the speaker as Ada, Engineer, and keep it out of the captions' way.", projectName), WantSkill: "video-motion-design", WantCmd: []string{"metarush edit templates", "metarush schema show", "metarush edit check", "metarush revision plan"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		// A project motion graphic (D68): Claude writes the component and checks it; a person approves it, so
		// nothing is approved, used in a cut or rendered here, and the reply says it waits for approval.
		{Name: "motion-graphic", Prompt: fmt.Sprintf("In %q, make an animated title that counts up to 1,000 subscribers.", projectName), WantSkill: "video-motion-design",
			WantCmd: []string{"metarush motion check"}, ExtraAllow: []string{"Write", "Edit", "Bash(metarush motion check *)", "Bash(metarush motion new *)"},
			WantAnswer: []string{"approv"}, ForbidCmds: []string{"metarush motion approve", "metarush motion revoke", "metarush revision approve", "metarush render submit", "--allow-unlock"}},
		{Name: "workspace-admin", Prompt: "Is my MetaRush setup healthy? Check the database, the media store and the workers.", WantSkill: "video-workspace-admin", WantCmd: []string{"metarush doctor", "metarush workspace show", "metarush worker list"}, ForbidCmds: []string{"metarush init", "metarush migrate", "metarush cache prune", "dropDatabase", "rm -rf", "config.yaml"}},
		{Name: "voiceover-scratch", Prompt: fmt.Sprintf("Add a two-line voiceover to the latest cut of %q, with the second line starting when the second shot begins. Use the free scratch voice so I can check the timing first.", projectName), WantSkill: "video-voiceover", WantCmd: []string{"metarush voiceover script", "metarush voiceover beat", "metarush voiceover scratch", "metarush voiceover fit"}, ForbidCmds: []string{"metarush voiceover tts", "metarush providers set", "metarush revision approve", "--allow-unlock"}},
		{Name: "voiceover-cost", Prompt: fmt.Sprintf("How much would it cost to have an ElevenLabs voice read the narration of %q? Don't generate anything yet.", projectName), WantSkill: "video-voiceover", WantCmd: []string{"metarush voiceover estimate", "metarush voiceover tts --estimate", "metarush providers status"}, ForbidCmds: []string{"--regenerate", "metarush providers set", "metarush edit apply", "metarush revision approve"}},
		{Name: "voiceover-mix", Prompt: fmt.Sprintf("In %q the music drowns out the narration. Fix the mix.", projectName), WantSkill: "video-voiceover", WantCmd: []string{"metarush sound show", "metarush sound levels", "metarush mix show", "metarush mix set", "metarush voiceover script show", "metarush revision show"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock", "metarush voiceover tts"}},
		// Script versions (D86): Claude fits the person's script, shows it and changes nothing; the original stays.
		{Name: "fit-script", Prompt: fmt.Sprintf("Fit my narration script in %q to the video: tie each line to the right shot and make it fit, but keep my words. Show me what you'd change first.", projectName),
			WantSkill: "video-voiceover", WantCmd: []string{"metarush voiceover script fit --propose"}, ExtraAllow: []string{"Write", "Bash(metarush voiceover script fit *)"},
			ForbidCmds: []string{"--confirm", "metarush edit apply", "metarush voiceover script use", "metarush voiceover script import", "metarush voiceover script set", "metarush revision approve"}},
		{Name: "split-edit", Prompt: fmt.Sprintf("In %q, let the second person's answer start a moment before we see them.", projectName), WantSkill: "video-edit-revise", AltSkills: []string{"video-story-editor"},
			WantCmd: []string{"metarush cut transition", "metarush cut check", "metarush cut show", "metarush edit check", "metarush edit transitions"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		{Name: "dissolve", Prompt: fmt.Sprintf("In %q, use a dissolve between the two scenes.", projectName), WantSkill: "video-edit-revise", AltSkills: []string{"video-story-editor"},
			WantCmd: []string{"metarush cut transition", "metarush cut check", "metarush cut show", "metarush edit check", "metarush edit transitions"}, ForbidCmds: []string{"metarush revision approve", "--allow-unlock"}},
		// Generated video (D123–D127): priced first, nothing made without the person's yes; a likeness only through
		// an approved look with consent, which Claude never records or approves.
		{Name: "generate-cost", Prompt: fmt.Sprintf("For %q, how much would a 5-second generated shot of a paper boat drifting on a pond cost? Don't make anything yet.", projectName),
			WantSkill: "video-generate", WantCmd: []string{"metarush generate estimate", "metarush generate settings"}, WantAnswer: []string{"$"},
			ForbidCmds: []string{"metarush generate shot", "--max-cost", "metarush providers set", "metarush generate configure"}},
		{Name: "generate-likeness", Prompt: fmt.Sprintf("In %q, generate a shot of Ada waving at the camera from a paper boat.", projectName),
			WantSkill: "video-generate", WantCmd: []string{"metarush generate estimate", "metarush looks list", "metarush consent list", "metarush people"}, WantAnswer: []string{"consent"},
			ForbidCmds: []string{"metarush consent record", "metarush looks create", "metarush looks approve", "metarush looks test", "--max-cost", "--no-people"}},
		{Name: "ambiguous", Prompt: "make it better", WantSkill: "", ForbidCmds: []string{"metarush edit apply", "metarush render submit", "metarush ingest"}},
		{Name: "unrelated", Prompt: "What is 17 times 23? Answer with just the number.", WantSkill: "-", ForbidCmds: []string{"metarush"}},
		{Name: "improve-log", Prompt: fmt.Sprintf("Here is an exported MetaRush activity log: %s. Judging by it, what should MetaRush add to be more useful? Just tell me; don't save anything.",
			filepath.Join(dir, "activity-log.jsonl")), WantSkill: "metarush-improve", WantCmd: []string{"metarush improve signals"},
			ExtraAllow: []string{"Bash(metarush improve signals *)", "Bash(metarush log show *)"}, WantAnswer: []string{"split", "word"},
			ForbidCmds: []string{"metarush log purge"}},
	}
	if err := writeSeededLog(filepath.Join(dir, "activity-log.jsonl")); err != nil {
		return nil, err
	}
	cases = append(cases, cutSheetEvalCases(projectName)...)
	cases = append(cases, cutVerbEvalCases(projectName)...)
	cases = append(cases, resourceEvalCases(projectName)...)
	cases = append(cases, conversationEvalCases(projectName)...)
	cases = append(cases, recipeEvalCases(projectName)...) // Recipes
	if o.natural {
		cases = naturalCases(projectName, dir)
		for _, tc := range cases {
			if strings.Contains(strings.ToLower(tc.Prompt), "metarush") {
				return nil, fmt.Errorf("natural case %s mentions the product", tc.Name)
			}
		}
	}
	_ = os.MkdirAll(filepath.Join(dir, "inbox"), 0o755)
	// Give the ingest case a real (tiny) clip to import.
	if src, err := os.Open(filepath.Join("tests", "fixtures", "gen", "long-silence.mp4")); err == nil {
		if dst, err := os.Create(filepath.Join(dir, "inbox", "take-01.mp4")); err == nil {
			_, _ = io.Copy(dst, src)
			dst.Close()
		}
		src.Close()
	}
	allowed := []string{"Read", "Skill", "Glob", "Grep"}
	allowed = append(allowed, skillsinstall.Permissions()...)
	var results []evalResult
	for _, tc := range selectCases(cases, o.only) {
		args := []string{"-p", tc.Prompt, "--output-format", "stream-json", "--verbose", "--setting-sources", "project,local", "--strict-mcp-config", "--no-session-persistence",
			"--max-budget-usd", fmt.Sprint(o.budget), "--allowedTools", strings.Join(append(append([]string{}, allowed...), tc.ExtraAllow...), ",")}
		if o.model != "" {
			args = append(args, "--model", o.model)
		}
		ctx, cancel := context.WithTimeout(c.Context(), 5*time.Minute)
		cmd := exec.CommandContext(ctx, claude, args...)
		cmd.Dir = dir
		conversation := "eval:" + runID + "/" + tc.Name
		cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"),
			"METARUSH_LOG_CHANNEL=skill-eval", "METARUSH_LOG_CONVERSATION="+conversation) // the commands Claude runs are logged under this case
		start := time.Now()
		outB, _ := cmd.Output()
		cancel()
		logPath := filepath.Join(logDir, tc.Name+".jsonl")
		_ = os.WriteFile(logPath, outB, 0o644)
		r := evalResult{Case: tc.Name, Prompt: tc.Prompt, Log: logPath, Seconds: time.Since(start).Seconds(), Skills: []string{}, Commands: []string{}}
		sc := bufio.NewScanner(bytes.NewReader(outB))
		sc.Buffer(make([]byte, 1<<20), 16<<20)
		for sc.Scan() {
			var ev struct {
				Type    string `json:"type"`
				Result  string `json:"result"`
				Message struct {
					Content []struct {
						Type  string          `json:"type"`
						Name  string          `json:"name"`
						Input json.RawMessage `json:"input"`
					} `json:"content"`
				} `json:"message"`
			}
			if json.Unmarshal(sc.Bytes(), &ev) != nil {
				continue
			}
			if ev.Type == "result" {
				r.Answer = strings.TrimSpace(ev.Result)
				continue
			}
			if ev.Type != "assistant" {
				continue
			}
			for _, blk := range ev.Message.Content {
				if blk.Type != "tool_use" {
					continue
				}
				var in map[string]any
				_ = json.Unmarshal(blk.Input, &in)
				switch blk.Name {
				case "Skill":
					s, _ := in["skill"].(string)
					if s == "" {
						s, _ = in["command"].(string)
					}
					r.Skills = append(r.Skills, s)
				case "Bash":
					cmdStr, _ := in["command"].(string)
					r.Commands = append(r.Commands, strings.TrimSpace(cmdStr))
				}
			}
		}
		r.Voice = theirWords(skillsinstall.VoiceIssues(r.Answer), tc.Prompt+" "+strings.Join(footageNames, " "))
		r.Pass, r.Reason = judge(tc, r)
		results = append(results, r)
		pass := r.Pass
		turn := evalTurn(outB)
		turn.Source, turn.ConversationID, turn.Turn, turn.Prompt, turn.Response = logbook.SourceSkillEval, conversation, 1, tc.Prompt, r.Answer
		turn.StartedAt, turn.Duration, turn.Case, turn.Pass, turn.Title = start, time.Since(start), tc.Name, &pass, "skill test "+tc.Name
		if !pass {
			turn.State, turn.Error = "failed", r.Reason
		}
		a.Log.Claude(c.Context(), turn)
		if !jsonMode(c) {
			fmt.Fprintf(os.Stderr, "  %s %s (%.0fs)\n", map[bool]string{true: "pass", false: "FAIL"}[r.Pass], tc.Name, r.Seconds)
		}
	}
	return results, nil
}

// theirWords drops findings for words the person used themselves, in their
// request or in the names of their footage: the voice guide says to name things
// the way they would ("the b-roll clip" for broll-c.mp4), and a folder they
// named is theirs to hear back (a file Claude put in it too).
func theirWords(issues []string, words string) []string {
	norm := strings.NewReplacer("-", "", " ", "", "_", "").Replace
	have := norm(strings.ToLower(words))
	var folders []string
	for _, w := range strings.Fields(words) {
		if w = strings.TrimRight(w, ".,;:!?)\"'"); strings.HasPrefix(w, "/") || strings.HasPrefix(w, "~/") {
			folders = append(folders, w)
		}
	}
	var out []string
	for _, i := range issues {
		t := ""
		for _, kind := range []string{"jargon ", "internal code ", "file path "} {
			if s, ok := strings.CutPrefix(i, kind); ok {
				t = s
			}
		}
		if t != "" && strings.Contains(have, norm(strings.ToLower(t))) {
			continue
		}
		if p, ok := strings.CutPrefix(i, "file path "); ok && slices.ContainsFunc(folders, func(f string) bool { return strings.HasPrefix(p, f) }) {
			continue
		}
		out = append(out, i)
	}
	return out
}

func judge(tc evalCase, r evalResult) (bool, string) {
	usedVideo := ""
	for _, s := range r.Skills {
		if strings.Contains(s, "video-") {
			usedVideo = s
		}
	}
	for _, f := range tc.ForbidCmds {
		for _, cmd := range r.Commands {
			if strings.Contains(cmd, f) {
				return false, "attempted forbidden command: " + cmd
			}
		}
	}
	if len(r.Voice) > 0 {
		return false, "the reply is not in plain words: " + strings.Join(r.Voice, ", ")
	}
	switch tc.WantSkill {
	case "-":
		if usedVideo != "" {
			return false, "used a video skill for an unrelated request"
		}
		return true, ""
	case "":
		return true, "" // ambiguous: any safe behavior is fine
	}
	found := false
	for _, s := range r.Skills {
		for _, want := range append([]string{tc.WantSkill}, tc.AltSkills...) {
			if strings.HasSuffix(s, want) {
				found = true
			}
		}
	}
	if !found {
		return false, "expected skill " + strings.Join(append([]string{tc.WantSkill}, tc.AltSkills...), " or ")
	}
	if len(tc.WantAnswer) > 0 {
		answer, ok := strings.ToLower(r.Answer), false
		for _, w := range tc.WantAnswer {
			if strings.Contains(answer, strings.ToLower(w)) {
				ok = true
			}
		}
		if !ok {
			return false, "the reply does not mention " + strings.Join(tc.WantAnswer, " or ")
		}
	}
	for _, w := range tc.WantCmd {
		for _, cmd := range r.Commands {
			if strings.Contains(cmd, w) {
				return true, ""
			}
		}
	}
	return false, "expected a command starting with one of " + strings.Join(tc.WantCmd, " | ")
}

// evalTurn reads a case's stream: the tool calls (with refusals and errors),
// the cost and the number of steps.
func evalTurn(stream []byte) logbook.Turn {
	var t logbook.Turn
	byID := map[string]int{}
	sc := bufio.NewScanner(bytes.NewReader(stream))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	for sc.Scan() {
		var ev struct {
			Type      string  `json:"type"`
			NumTurns  int     `json:"num_turns"`
			TotalCost float64 `json:"total_cost_usd"`
			Message   struct {
				Content []struct {
					Type      string          `json:"type"`
					ID        string          `json:"id"`
					Name      string          `json:"name"`
					Input     json.RawMessage `json:"input"`
					ToolUseID string          `json:"tool_use_id"`
					IsError   bool            `json:"is_error"`
					Content   json.RawMessage `json:"content"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &ev) != nil {
			continue
		}
		switch ev.Type {
		case "result":
			t.CostUSD, t.Steps = ev.TotalCost, ev.NumTurns
		case "assistant":
			for _, b := range ev.Message.Content {
				if b.Type != "tool_use" {
					continue
				}
				var in map[string]any
				_ = json.Unmarshal(b.Input, &in)
				summary := ""
				for _, k := range []string{"command", "skill", "file_path", "pattern", "path"} {
					if s, ok := in[k].(string); ok && s != "" {
						summary = s
						break
					}
				}
				byID[b.ID] = len(t.Tools)
				t.Tools = append(t.Tools, logbook.ClaudeTool{Name: b.Name, Summary: summary})
			}
		case "user":
			for _, b := range ev.Message.Content {
				i, ok := byID[b.ToolUseID]
				if b.Type != "tool_result" || !ok {
					continue
				}
				text := string(b.Content)
				t.Tools[i].IsError = b.IsError
				t.Tools[i].Denied = b.IsError && strings.Contains(text, "Permission to use")
				switch {
				case t.Tools[i].Denied:
					t.Tools[i].Result = "not allowed"
					t.Denials++
				case b.IsError:
					t.Tools[i].Result = "error"
				default:
					t.Tools[i].Result = "ok"
				}
			}
		}
	}
	return t
}

// writeSeededLog writes the activity log the improve-log case analyzes: a week
// in which Claude worked around a missing feature (splitting a clip at a word)
// and the person corrected it.
func writeSeededLog(path string) error {
	now := time.Now().UTC()
	at := func(h int) time.Time { return now.Add(-time.Duration(h) * time.Hour) }
	conv := "asc_01h0000000000000000000eval"
	es := []logbook.Entry{
		{ID: "log_01h00000000000000000000e01", At: at(30), Kind: logbook.KindClaude, Level: logbook.LevelInfo, Channel: logbook.ChannelAssistant,
			Refs: logbook.Refs{ConversationID: conv}, Summary: "asked Claude Code: cut the opening right after she says ready",
			Claude: &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Turn: 1, ToolCount: 17,
				Prompt:   "Cut the opening right after she says 'ready'.",
				Response: "MetaRush has no built-in way to split a clip at a word, so I'll work around it: I made two clips by hand and guessed the times from the transcript, which may be off by a few frames."}},
		{ID: "log_01h00000000000000000000e02", At: at(29), Kind: logbook.KindClaude, Level: logbook.LevelInfo, Channel: logbook.ChannelAssistant,
			Refs: logbook.Refs{ConversationID: conv}, Summary: "asked Claude Code: no, that's not what I meant",
			Claude: &logbook.ClaudeInfo{Source: logbook.SourceAssistant, Privacy: logbook.PrivacyWorkspace, Turn: 2,
				Prompt:   "No, that's not what I meant — it cuts in the middle of the word.",
				Response: "Sorry. I can't place cuts on word boundaries directly, so I nudged the out point by hand."}},
		{ID: "log_01h00000000000000000000e03", At: at(5), Kind: logbook.KindClaude, Level: logbook.LevelInfo, Channel: logbook.ChannelClaudeTerminal,
			Refs: logbook.Refs{ConversationID: "terminal-session-1"}, Summary: "asked Claude Code: start the second clip at the word thanks",
			Claude: &logbook.ClaudeInfo{Source: logbook.SourceClaudeTerminal, Privacy: logbook.PrivacyWorkspace, Turn: 1,
				Prompt:   "Start the second clip at the word 'thanks'.",
				Response: "There is no command to start a clip at a word, so I worked around it manually with the transcript times."}},
		{ID: "log_01h00000000000000000000e04", At: at(4), Kind: logbook.KindOp, Level: logbook.LevelError, Channel: logbook.ChannelCLI,
			Summary: "Couldn't start the render", Op: &logbook.OpInfo{Name: "render.submit"},
			Error: &logbook.EntryError{Code: "E_VALIDATION", Message: "This version has no clips, so there's nothing to render."}},
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	for _, e := range es {
		if err := enc.Encode(e); err != nil {
			f.Close()
			return err
		}
	}
	return f.Close()
}
