package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/skillsinstall"
	"github.com/metavert-io/metarush/internal/tools"
)

// OneShot is a single headless Claude Code run with no shell at all (D78, the
// weekly improvement report): it may read the files in Dir, write only the
// files named in Writes, and load the MetaRush skills written into Dir. The
// same locked-down flags as the dashboard assistant apply (project and local
// settings only, which never exist there; no MCP servers; anything not
// allowed is denied), and Bash, web access and edits elsewhere are refused.
type OneShot struct {
	Dir       string   // the run folder: Claude Code's working folder, the only one it reads
	Prompt    string   // sent on standard input (never argv)
	Writes    []string // files (relative to Dir) it may write
	System    string   // appended to Claude Code's system prompt
	BudgetUSD float64
	MaxTurns  int
	Model     string
	Timeout   time.Duration
}

// OneShotResult is Claude Code's final answer.
type OneShotResult struct {
	Result   string        `json:"result"`
	CostUSD  float64       `json:"costUsd"`
	IsError  bool          `json:"isError"`
	Subtype  string        `json:"subtype"`
	Turns    int           `json:"turns"`
	Duration time.Duration `json:"duration"`
}

// oneShotTools are the only built-in tools offered (--tools).
var oneShotTools = []string{"Read", "Write", "Skill"}

// RunOneShot runs Claude Code once, headless, under the rules above.
func RunOneShot(ctx context.Context, a *app.App, o OneShot) (*OneShotResult, error) {
	m := For(a)
	pr := m.probe(ctx, false)
	if !pr.status.Available {
		return nil, apperr.New(apperr.NotConfigured, "%s", pr.status.Reason).WithHint(pr.status.Hint).WithDetails(map[string]any{"reason": "assistant_unavailable"})
	}
	dir := realPath(o.Dir)
	if !filepath.IsAbs(dir) || unsafeRuleChars.MatchString(dir) {
		return nil, apperr.New(apperr.Internal, "MetaRush can't let Claude Code use this folder: its name has characters MetaRush can't pass on.").WithDetails(map[string]any{"folder": dir})
	}
	if err := prepareRunFolder(dir); err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't get the folder for Claude Code ready.")
	}
	allowed := []string{"Skill", "Read(/" + dir + "/**)"}
	for _, w := range o.Writes {
		p := filepath.Join(dir, filepath.Clean("/"+w)) // stays inside dir
		allowed = append(allowed, "Write(/"+p+")")
	}
	budget := o.BudgetUSD
	if budget <= 0 {
		budget = 1
	}
	args := []string{"-p", "--output-format", "json", "--setting-sources", "project,local",
		"--max-budget-usd", fmt.Sprintf("%.2f", math.Max(0.01, budget))}
	if o.System != "" {
		args = append(args, "--append-system-prompt", o.System)
	}
	if o.MaxTurns > 0 {
		args = append(args, "--max-turns", strconv.Itoa(o.MaxTurns))
	}
	if pr.caps.strictMCP {
		args = append(args, "--strict-mcp-config")
	}
	if pr.caps.dontAsk {
		args = append(args, "--permission-mode", "dontAsk")
	}
	if pr.caps.tools {
		args = append(args, "--tools", strings.Join(oneShotTools, ","))
	}
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	args = append(args, "--allowedTools", strings.Join(allowed, ","),
		"--disallowedTools", "Bash,WebFetch,WebSearch,Edit,NotebookEdit,Task,Glob,Grep")
	timeout := o.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Minute
	}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	res, err := a.Tools.Run(rctx, tools.Spec{Tool: tools.Claude, Args: args, Dir: dir, Env: childEnv(os.Environ(), "", a.Cfg.Home(), dir), CleanEnv: true,
		Stdin: strings.NewReader(o.Prompt), Background: true})
	out := &OneShotResult{Duration: time.Since(start)}
	if res != nil {
		var r struct {
			Result  string  `json:"result"`
			Cost    float64 `json:"total_cost_usd"`
			IsError bool    `json:"is_error"`
			Subtype string  `json:"subtype"`
			Turns   int     `json:"num_turns"`
		}
		s := strings.TrimSpace(string(res.Stdout))
		if i := strings.LastIndex(s, "\n{"); i >= 0 {
			s = s[i+1:]
		}
		if json.Unmarshal([]byte(s), &r) == nil {
			out.Result, out.CostUSD, out.IsError, out.Subtype, out.Turns = r.Result, r.Cost, r.IsError, r.Subtype, r.Turns
		}
	}
	if err != nil {
		if rctx.Err() != nil && ctx.Err() == nil {
			return out, apperr.New(apperr.ToolFailed, "Claude Code didn't finish within %s.", minutesText(timeout))
		}
		return out, apperr.Wrap(apperr.ToolFailed, err, "Claude Code stopped before it finished.%s", saidText(err))
	}
	return out, nil
}

// minutesText says a time limit in words ("20 minutes", "90 seconds").
func minutesText(d time.Duration) string {
	if d < time.Minute {
		return pluralize(int(d/time.Second), "second")
	}
	return pluralize(int(d/time.Minute), "minute")
}

// prepareRunFolder writes the skills (permissions come only from the flags,
// so their allowed-tools lines are removed) and removes any settings file.
func prepareRunFolder(dir string) error {
	files, err := skillsinstall.Files()
	if err != nil {
		return err
	}
	for _, rel := range []string{".claude/settings.json", ".claude/settings.local.json", "CLAUDE.local.md", ".mcp.json"} {
		if err := os.RemoveAll(filepath.Join(dir, filepath.FromSlash(rel))); err != nil {
			return err
		}
	}
	for rel, b := range files {
		if strings.HasSuffix(rel, "/SKILL.md") {
			b = allowedToolsLine.ReplaceAll(b, nil)
		}
		if err := writeIfChanged(filepath.Join(dir, ".claude", "skills", filepath.FromSlash(rel)), b, 0o644); err != nil {
			return err
		}
	}
	md := "# MetaRush report run\n\nThis folder is written by MetaRush for one headless Claude Code run. Everything you need is in it.\n"
	return writeIfChanged(filepath.Join(dir, "CLAUDE.md"), []byte(md), 0o644)
}
