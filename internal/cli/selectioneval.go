package cli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/mcp"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/skillsinstall"
)

// The tool-selection eval (D223): about 150 plain requests, each with the
// operation that should be chosen (internal/catalog/selection.json).
// `metarush skills eval` scores find on it, deterministically and with no
// model (go test gates the same score); --live runs each request through
// real, headless Claude Code — with the skills and the command line, or with
// an MCP toolset — and records the first command or tool it chose and how
// many tokens it carried before its first call. Live mode uses the person's
// Claude account, is never part of the build checks and refuses to run in CI.

type selectionEvalOut struct {
	Cases      int                     `json:"cases"`
	Operations int                     `json:"operations"`
	Baseline   catalog.SelectionResult `json:"baseline" jsonschema:"description=Plain BM25 over names and summaries (about what a client's own tool search does)."`
	Find       catalog.SelectionResult `json:"find"`
	Padded     catalog.SelectionResult `json:"padded" jsonschema:"description=find with made-up entries beside the operations (what abilities will add)."`
	MadeUp     int                     `json:"madeUp"`
	Holds      bool                    `json:"holds" jsonschema:"description=find meets the gate go test holds it to."`
	Live       *liveSelectionOut       `json:"live,omitempty"`
}

type liveSelectionOut struct {
	// Surface is what Claude Code had: "skills" (the skills and the command
	// line) or "mcp:<toolset>".
	Surface string `json:"surface"`
	Cases   int    `json:"cases"`
	Right   int    `json:"right"`
	// TokensBefore is the median context before Claude's first call.
	TokensBefore int                 `json:"tokensBefore"`
	CostUSD      float64             `json:"costUsd"`
	Results      []liveSelectionCase `json:"results"`
	Logs         string              `json:"logs"`
}

type liveSelectionCase struct {
	N    int      `json:"n"`
	Ask  string   `json:"ask"`
	Want []string `json:"want"`
	// Chose is the command or tool call Claude chose (looking around first
	// doesn't count: see chosenCall), and Op the operation it runs.
	Chose        string   `json:"chose"`
	Op           string   `json:"op"`
	Pass         bool     `json:"pass"`
	Calls        []string `json:"calls"`
	TokensBefore int      `json:"tokensBefore"`
	CostUSD      float64  `json:"costUsd"`
	Seconds      float64  `json:"seconds"`
	Reason       string   `json:"reason,omitempty"`
}

type liveSelectionOpts struct {
	surface  string // "skills" or an MCP toolset
	budget   float64
	model    string
	limit    int
	only     int
	holdout  bool
	names    map[string]string
	exe      string
	home     string
	claude   string
	logDir   string
	workRoot string
}

func skillsEvalCmd() *cobra.Command {
	var live, holdout bool
	var surface, model, projectName, clipName string
	var budget float64
	var limit, only int
	cmd := &cobra.Command{
		Use:   "eval [--live [--mcp core|review|edit|library|admin] [--limit N] [--case N]]",
		Short: "Score how well MetaRush's tool search chooses what does a request, on about 150 plain requests; --live asks real Claude Code (uses your Claude account)",
		Long: `Scores find on the tool-selection eval: about 150 plain requests, each with the operation that should be
chosen. Without --live nothing is sent anywhere: the score, plain BM25's next to it, and the score with 500
made-up entries in the catalogue; go test holds find to the same gate.

--live runs each request through real, headless Claude Code (your Claude account; a few cents a request)
with the skills and the command line, or with --mcp an MCP toolset instead, and records the first command
or tool it chose (looking around first doesn't count) and how much it carried before its first call. Each
session stops at that choice. --limit N runs N requests spread over the eval, --case N one. Never in CI.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			sel, err := catalog.LoadSelection()
			if err != nil {
				return failErr(c, err)
			}
			es, err := catalog.EvalEntries(c.Context())
			if err != nil {
				return failErr(c, err)
			}
			out := selectionEvalOut{Cases: len(sel.Cases), Operations: len(es), MadeUp: catalog.MadeUpEntries,
				Baseline: catalog.ScoreSelection(es, sel, catalog.BaselineRanking),
				Find:     catalog.ScoreSelection(es, sel, catalog.DefaultRanking),
				Padded:   catalog.ScoreSelection(append(append([]catalog.Entry(nil), es...), catalog.MadeUp(catalog.MadeUpEntries)...), sel, catalog.DefaultRanking)}
			out.Holds = out.Find.Holds(out.Padded)
			if live {
				o := liveSelectionOpts{surface: "skills", budget: budget, model: model, limit: limit, only: only, holdout: holdout}
				if surface != "" {
					o.surface = surface
					if _, err := mcp.ToolList(surface, false); err != nil {
						return failErr(c, err)
					}
				}
				lo, err := liveSelection(c, sel, o, projectName, clipName)
				if err != nil {
					return failErr(c, err)
				}
				out.Live = lo
			}
			return emit(c, out, func() { printSelectionEval(out, detailsMode(c)) })
		},
	}
	cmd.Flags().BoolVar(&live, "live", false, "run each request through real headless Claude Code (uses your Claude account)")
	cmd.Flags().StringVar(&surface, "mcp", "", "with --live: give Claude Code this MCP toolset instead of the skills and the command line")
	cmd.Flags().Float64Var(&budget, "budget-usd", 0.40, "with --live: most to spend on one request")
	cmd.Flags().StringVar(&model, "model", "", "with --live: the model (default: Claude Code's)")
	cmd.Flags().IntVar(&limit, "limit", 0, "with --live: run this many requests, spread over the eval (default: all)")
	cmd.Flags().IntVar(&only, "case", 0, "with --live: run only the request with this number")
	cmd.Flags().BoolVar(&holdout, "held-out", false, "with --live: only the requests find was not tuned on")
	cmd.Flags().StringVar(&projectName, "project", "", "with --live: the project the requests name (default: the first)")
	cmd.Flags().StringVar(&clipName, "clip", "", "with --live: the clip the requests name (default: the project's first)")
	return cmd
}

func printSelectionEval(o selectionEvalOut, details bool) {
	line := func(what string, r catalog.SelectionResult) {
		fmt.Printf("%-40s right first %3.0f%%, in the first three %3.0f%% (requests it wasn't tuned on: %3.0f%%, %3.0f%%)\n", what,
			100*r.All.Top1Rate(), 100*r.All.Top3Rate(), 100*r.Holdout.Top1Rate(), 100*r.Holdout.Top3Rate())
	}
	fmt.Printf("%d plain requests, %d things MetaRush can do.\n", o.Cases, o.Operations)
	line("Plain word matching:", o.Baseline)
	line("MetaRush's search (find):", o.Find)
	line(fmt.Sprintf("With %d made-up entries more:", o.MadeUp), o.Padded)
	if o.Holds {
		fmt.Println("It is as good as the build checks ask.")
	} else {
		fmt.Println("It is worse than the build checks ask.")
	}
	if details {
		for _, m := range o.Find.Misses {
			fmt.Printf("  #%d %q: want %s, got %s\n", m.Case.N, m.Case.Ask, strings.Join(append([]string{m.Case.Op}, m.Case.Alt...), " or "), strings.Join(m.Got, ", "))
		}
	}
	if l := o.Live; l != nil {
		fmt.Printf("\nWith real Claude Code (%s): %d of %d chosen right first; about %d tokens before its first call; $%.2f.\n",
			l.Surface, l.Right, l.Cases, l.TokensBefore, l.CostUSD)
		for _, r := range l.Results {
			if r.Pass && !details {
				continue
			}
			mark := map[bool]string{true: "✓", false: "✗"}[r.Pass]
			fmt.Printf("%s #%d %q: %s\n", mark, r.N, r.Ask, r.Reason)
			if details {
				fmt.Printf("    chose %s (%s); calls: %s\n", r.Chose, r.Op, strings.Join(r.Calls, " · "))
			}
		}
		if details {
			fmt.Println("logs: " + l.Logs)
		}
	}
}

// liveSelection runs the requests through real Claude Code.
func liveSelection(c *cobra.Command, sel *catalog.Selection, o liveSelectionOpts, projectName, clipName string) (*liveSelectionOut, error) {
	if inCI() {
		return nil, apperr.New(apperr.InvalidInput, "The live eval never runs in CI: it uses a person's Claude account.")
	}
	claude, err := exec.LookPath("claude")
	if err != nil {
		return nil, apperr.New(apperr.NotFound, "Claude Code (claude) isn't on this computer's PATH.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return nil, err
	}
	defer a.Close()
	// The requests name a project and a clip: the person's own, so Claude
	// can find them.
	if projectName == "" || clipName == "" {
		if lo, err := ops.CallTyped[ops.ProjectListOut](c.Context(), a, "project.list", ops.ProjectListIn{}, ops.Caller{}); err == nil && len(lo.Projects) > 0 {
			if projectName == "" {
				projectName = lo.Projects[0].Name
			}
			if clipName == "" {
				if al, err := ops.CallTyped[ops.AssetListOut](c.Context(), a, "asset.list", ops.AssetListIn{ProjectID: lo.Projects[0].ID, Limit: 1}, ops.Caller{}); err == nil && len(al.Assets) > 0 {
					clipName = al.Assets[0].Name
					if al.Assets[0].Title != "" {
						clipName = al.Assets[0].Title // the name people see
					}
				}
			}
		}
	}
	if projectName == "" {
		return nil, apperr.New(apperr.InvalidInput, "The live eval needs a project with footage to ask about.")
	}
	names := map[string]string{}
	for k, v := range sel.Names {
		names[k] = v
	}
	names["project"] = projectName
	if clipName != "" {
		names["clip"] = clipName
	}
	h, _ := home()
	exe, _ := os.Executable()
	o.names, o.exe, o.home, o.claude = names, exe, h, claude
	o.logDir = filepath.Join("tests", "out", "selection-eval", time.Now().Format("20060102-150405"))
	return runLiveSelection(c.Context(), sel, o, func(r liveSelectionCase) {
		if !jsonMode(c) {
			fmt.Fprintf(os.Stderr, "  %s #%d (%.0fs)\n", map[bool]string{true: "right", false: "WRONG"}[r.Pass], r.N, r.Seconds)
		}
	})
}

func inCI() bool { return os.Getenv("CI") != "" || os.Getenv("GITHUB_ACTIONS") != "" }

// liveCases picks the requests a live run asks: one, the held-out ones, or a
// limit spread evenly over the eval.
func liveCases(sel *catalog.Selection, o liveSelectionOpts) []catalog.SelectionCase {
	var cs []catalog.SelectionCase
	for _, c := range sel.Cases {
		if (o.only == 0 || c.N == o.only) && (!o.holdout || c.Holdout) {
			cs = append(cs, c)
		}
	}
	if o.limit > 0 && o.limit < len(cs) {
		picked := make([]catalog.SelectionCase, 0, o.limit)
		for i := 0; i < o.limit; i++ {
			picked = append(picked, cs[i*len(cs)/o.limit])
		}
		cs = picked
	}
	return cs
}

func runLiveSelection(ctx context.Context, sel *catalog.Selection, o liveSelectionOpts, each func(liveSelectionCase)) (*liveSelectionOut, error) {
	root := o.workRoot
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, "video-selection-eval-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	allowed := []string{"Read", "Glob", "Grep"}
	var extra []string
	mcpConfig := ""
	if o.surface == "skills" {
		if _, err := skillsinstall.Install(skillsinstall.Options{Dir: filepath.Join(dir, ".claude", "skills"), ProjectRoot: dir}); err != nil {
			return nil, err
		}
		allowed = append(append(allowed, "Skill"), skillsinstall.Permissions()...)
		bin, err := evalShim(dir, o.exe, o.home)
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(bin)
		extra = append(extra, "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	} else {
		cfg := map[string]any{"mcpServers": map[string]any{"metarush": map[string]any{"command": o.exe, "args": []string{"--home", o.home, "mcp", "serve", "--toolset", o.surface}}}}
		b, _ := json.Marshal(cfg)
		path := filepath.Join(dir, "mcp.json")
		if err := os.WriteFile(path, b, 0o600); err != nil {
			return nil, err
		}
		allowed = append(allowed, "mcp__metarush")
		o.surface = "mcp:" + o.surface
		mcpConfig = path
	}
	if err := os.MkdirAll(o.logDir, 0o755); err != nil {
		return nil, err
	}
	out := &liveSelectionOut{Surface: o.surface, Results: []liveSelectionCase{}, Logs: o.logDir}
	var tokens []int
	for _, sc := range liveCases(sel, o) {
		r := runLiveCase(ctx, dir, sc, o, allowed, extra, mcpConfig)
		out.Results = append(out.Results, r)
		out.Cases++
		if r.Pass {
			out.Right++
		}
		out.CostUSD += r.CostUSD
		if r.TokensBefore > 0 {
			tokens = append(tokens, r.TokensBefore)
		}
		if each != nil {
			each(r)
		}
	}
	if len(tokens) > 0 {
		sort.Ints(tokens)
		out.TokensBefore = tokens[len(tokens)/2]
	}
	return out, nil
}

// runLiveCase asks Claude Code one request and stops it once it chose.
func runLiveCase(ctx context.Context, dir string, sc catalog.SelectionCase, o liveSelectionOpts, allowed, env []string, mcpConfig string) liveSelectionCase {
	r := liveSelectionCase{N: sc.N, Ask: sc.Request(o.names), Want: append([]string{sc.Op}, sc.Alt...), Calls: []string{}}
	args := []string{"-p", r.Ask, "--output-format", "stream-json", "--verbose", "--setting-sources", "project,local", "--strict-mcp-config",
		"--no-session-persistence", "--max-budget-usd", fmt.Sprint(o.budget), "--allowedTools", strings.Join(allowed, ",")}
	if mcpConfig != "" {
		// MCP only: the command line is not offered.
		args = append(args, "--mcp-config", mcpConfig, "--disallowedTools", "Bash")
	}
	if o.model != "" {
		args = append(args, "--model", o.model)
	}
	cctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(cctx, o.claude, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), env...)
	cmd.Env = append(cmd.Env, "METARUSH_LOG_CHANNEL=skill-eval", fmt.Sprintf("METARUSH_LOG_CONVERSATION=selection-eval/%d", sc.N))
	start := time.Now()
	logf, _ := os.Create(filepath.Join(o.logDir, fmt.Sprintf("%03d.jsonl", sc.N)))
	if logf != nil {
		defer logf.Close()
	}
	stdout, err := cmd.StdoutPipe()
	if err == nil {
		err = cmd.Start()
	}
	if err != nil {
		r.Reason = "Claude Code didn't start: " + err.Error()
		return r
	}
	toolset := strings.TrimPrefix(o.surface, "mcp:")
	sc2 := bufio.NewScanner(stdout)
	sc2.Buffer(make([]byte, 1<<20), 16<<20)
	chosen := false
	for sc2.Scan() {
		line := sc2.Bytes()
		if logf != nil {
			_, _ = logf.Write(append(append([]byte(nil), line...), '\n'))
		}
		ev := streamEvent{}
		if json.Unmarshal(line, &ev) != nil {
			continue
		}
		switch ev.Type {
		case "result":
			r.CostUSD = ev.TotalCost
		case "assistant":
			if r.TokensBefore == 0 {
				u := ev.Message.Usage
				r.TokensBefore = u.Input + u.CacheCreation + u.CacheRead
			}
			for _, b := range ev.Message.Content {
				if b.Type != "tool_use" || chosen {
					continue
				}
				call, op := callOp(b.Name, b.Input, toolset)
				if call == "" {
					continue
				}
				r.Calls = append(r.Calls, call)
				if lookingAround(op, sc) {
					continue
				}
				chosen, r.Chose, r.Op = true, call, op
			}
		}
		if chosen || len(r.Calls) >= 12 {
			cancel() // it chose (or wandered): no need to let it go on
			break
		}
	}
	_ = cmd.Wait()
	r.Seconds = time.Since(start).Seconds()
	r.Pass, r.Reason = judgeChoice(sc, r)
	return r
}

type streamEvent struct {
	Type      string  `json:"type"`
	TotalCost float64 `json:"total_cost_usd"`
	Message   struct {
		Usage struct {
			Input         int `json:"input_tokens"`
			CacheCreation int `json:"cache_creation_input_tokens"`
			CacheRead     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
		Content []struct {
			Type  string         `json:"type"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	} `json:"message"`
}

// callOp names a tool call and the operation it runs ("" when it runs
// none): a metarush command line, or a MetaRush MCP tool.
func callOp(tool string, in map[string]any, toolset string) (call, op string) {
	switch {
	case tool == "Bash":
		cmd, _ := in["command"].(string)
		ops := commandOps(cmd)
		if len(ops) == 0 {
			return "", ""
		}
		return strings.TrimSpace(cmd), strings.Join(ops, "|")
	case strings.HasPrefix(tool, "mcp__metarush__"):
		t := strings.TrimPrefix(tool, "mcp__metarush__")
		b, _ := json.Marshal(in)
		return t + " " + string(b), mcp.OpOf(toolset, t, in)
	}
	return "", ""
}

// commandOps are the operations a metarush command line runs (several when
// one command runs them: metarush add).
func commandOps(line string) []string {
	for _, sep := range []string{"|", "&&", ";", "\n"} {
		line, _, _ = strings.Cut(line, sep)
	}
	words := strings.Fields(line)
	i := 0
	for i < len(words) && !strings.HasSuffix(words[i], "metarush") {
		i++ // FOO=bar, a path to the program
	}
	if i == len(words) {
		return nil
	}
	var path []string
	for j := i + 1; j < len(words); j++ {
		w := words[j]
		if w == "--home" {
			j++
			continue
		}
		if strings.HasPrefix(w, "-") {
			if len(path) > 0 {
				break
			}
			continue // global flags before the command
		}
		path = append(path, w)
	}
	byCommand := commandIndexByPath()
	for n := len(path); n > 0; n-- {
		if found := byCommand["metarush "+strings.Join(path[:n], " ")]; len(found) > 0 {
			return found
		}
	}
	return nil
}

func commandIndexByPath() map[string][]string {
	out := map[string][]string{}
	for _, op := range ops.All() {
		if c, ok := catalog.CommandFor(op.Name); ok {
			out[c.Command] = append(out[c.Command], op.Name)
		}
	}
	return out
}

// orientation are the operations an agent uses to look around before it
// chooses: they never count as its choice (unless they are what the request
// asks for).
var orientation = map[string]bool{"project.list": true, "project.show": true, "asset.list": true, "revision.list": true,
	"revision.show": true, "ops.find": true, "ops.describe": true, "ops.list": true}

// lookingAround says whether a call is looking around rather than the
// choice the request asks for: an operation that orients, or, when the
// request asks for a change, one that changes nothing.
func lookingAround(op string, sc catalog.SelectionCase) bool {
	if op == "" {
		return true
	}
	runs := strings.Split(op, "|")
	for _, o := range runs {
		if sc.Right(o) {
			return false
		}
	}
	for _, o := range runs {
		if orientation[o] {
			return true
		}
	}
	if want, ok := ops.Get(sc.Op); ok && want.Mutating {
		for _, o := range runs {
			if x, ok := ops.Get(o); ok && x.Mutating {
				return false
			}
		}
		return true
	}
	return false
}

func judgeChoice(sc catalog.SelectionCase, r liveSelectionCase) (bool, string) {
	if r.Chose == "" {
		if len(r.Calls) == 0 {
			return false, "it chose nothing"
		}
		return false, "it only looked around"
	}
	for _, o := range strings.Split(r.Op, "|") {
		if sc.Right(o) {
			return true, ""
		}
	}
	return false, fmt.Sprintf("it chose %s, not %s", r.Op, strings.Join(r.Want, " or "))
}
