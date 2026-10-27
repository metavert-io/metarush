package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/api"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// assistantMode reports whether this process was started by the dashboard
// assistant's shim (headless Claude Code).
func assistantMode() bool { return os.Getenv("METARUSH_VIA") == models.ViaAssistant }

// assistantGuard runs before every command. In assistant mode only the read-only
// allowlist is available, output folders cannot be chosen and the installation
// home cannot be switched, whatever Claude Code's own permission settings say.
func assistantGuard(c *cobra.Command) error {
	if !assistantMode() {
		return nil
	}
	// The refusals name the command and the option behind --details and in
	// --json (details), where Claude reads them.
	cmdPath := c.CommandPath()
	if !assistant.AllowsCommand(cmdPath) {
		return failErr(c, apperr.New(apperr.Unauthorized, "That command isn't available to the dashboard assistant (it is read-only).").
			WithHint("The dashboard shows this request to the person as a card they can confirm; say what you asked for and stop.").
			WithDetails(map[string]any{"refused": cmdPath}))
	}
	for _, name := range assistant.MutatingFlags(cmdPath) {
		if f := c.Flags().Lookup(name); f != nil && f.Changed {
			return failErr(c, apperr.New(apperr.Unauthorized, "That option changes the cut, which the dashboard assistant can't do.").
				WithHint("The dashboard shows this request to the person as a card they can confirm; say what you asked for and stop.").
				WithDetails(map[string]any{"refused": cmdPath + " --" + name}))
		}
	}
	for _, name := range []string{"out", "dir"} {
		if f := c.Flags().Lookup(name); f != nil && f.Changed {
			return failErr(c, apperr.New(apperr.Unauthorized, "The dashboard assistant can't choose where files are saved.").
				WithHint("Leave that option out; the usual place can be read.").WithDetails(map[string]any{"refused": "--" + name}))
		}
	}
	if f := c.Flags().Lookup("watch"); f != nil && f.Changed {
		return failErr(c, apperr.New(apperr.Unauthorized, "The dashboard assistant can't keep following output: that never returns.").
			WithHint("Run the command once instead.").WithDetails(map[string]any{"refused": "--watch"}))
	}
	if want := os.Getenv("METARUSH_ASSISTANT_HOME"); want != "" {
		if h, err := home(); err != nil || h != want {
			return failErr(c, apperr.New(apperr.Unauthorized, "The dashboard assistant only works with its own MetaRush folder.").
				WithHint("Leave out the option that picks another folder.").WithDetails(map[string]any{"refused": "--home"}))
		}
	}
	return nil
}

// opsAnnotation names the ops a command runs (the CLI parity test reads it).
const opsAnnotation = "ops"

func forOps(cmd *cobra.Command, names ...string) *cobra.Command {
	if cmd.Annotations == nil {
		cmd.Annotations = map[string]string{}
	}
	cmd.Annotations[opsAnnotation] = strings.Join(names, ",")
	return cmd
}

func assistantCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "assistant", Short: "Claude Code conversations (local, read-only: it proposes, you apply)",
		Long: "Conversations with your local Claude Code, the same ones the dashboard's Claude Code panel shows. Claude reads your footage,\n" +
			"transcripts and cuts and proposes edit plans and changes; you apply them. Conversations are stored in the workspace\n" +
			"database (browsable from every machine); only the machine that holds a conversation's Claude Code session can continue it.\n" +
			"Commands that need the live process (ask, continue, interrupt, stop, …) go through the local dashboard daemon when it runs."}
	cmd.AddCommand(assistantStatusCmd(), assistantModelsCmd(), assistantSettingsCmd(), conversationsCmd(), assistantAskCmd(), assistantContinueCmd(),
		assistantInterruptCmd(), assistantStopCmd(), assistantDequeueCmd(), assistantRenameCmd(), assistantArchiveCmd(), assistantConfigureCmd(),
		assistantProposalsCmd(), assistantApplyCmd(), assistantActCmd(), assistantSuggestionsCmd(), assistantModeCmd(), assistantSpendingCmd())
	return cmd
}

/* ---------- Running assistant ops (in-process or through the daemon) ---------- */

// assistantSession runs assistant ops: through the local daemon when it is up
// (it holds the Claude Code processes), otherwise in this process.
type assistantSession struct {
	a      *app.App
	daemon *daemonClient
	m      *assistant.Manager
}

func openAssistant(c *cobra.Command, live bool) (*assistantSession, error) {
	a, err := openApp(c, "cli")
	if err != nil {
		return nil, err
	}
	s := &assistantSession{a: a}
	if live {
		s.daemon = localDaemon(a)
	}
	if s.daemon == nil {
		// This terminal's own manager: it never acts on the daemon's
		// conversations in the background (watching work, telling Claude).
		exe, _ := os.Executable()
		s.m = assistant.NewTerminal(a, exe)
	}
	return s, nil
}

func (s *assistantSession) close() {
	if s.m != nil {
		s.m.Close()
	}
	s.a.Close()
}

// call runs an op and decodes its output into out.
func (s *assistantSession) call(ctx context.Context, name string, in, out any) error {
	if s.daemon != nil {
		return s.daemon.op(ctx, name, in, out)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	res, err := ops.Call(ctx, s.a, name, b, ops.Caller{})
	if err != nil {
		return err
	}
	rb, _ := json.Marshal(res)
	return json.Unmarshal(rb, out)
}

// humanCall runs a people-only op after the person confirmed in this terminal.
func (s *assistantSession) humanCall(ctx context.Context, name string, in, out any) error {
	if s.daemon != nil {
		return s.daemon.op(ctx, name, in, out)
	}
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	res, err := ops.Call(ctx, s.a, name, b, ops.Caller{Human: true})
	if err != nil {
		return err
	}
	rb, _ := json.Marshal(res)
	return json.Unmarshal(rb, out)
}

// daemonClient talks to this installation's running dashboard daemon.
type daemonClient struct {
	base string
	http *http.Client
}

// localDaemon signs in to this installation's daemon (a one-time nonce, like
// `metarush dashboard`), or returns nil when none is running.
func localDaemon(a *app.App) *daemonClient {
	b, err := os.ReadFile(filepath.Join(a.Cfg.StateDir(), "daemon.json"))
	if err != nil {
		return nil
	}
	var st daemonState
	if json.Unmarshal(b, &st) != nil || st.PID <= 0 || syscall.Kill(st.PID, 0) != nil || !healthy(a.Cfg) {
		return nil
	}
	nonce, err := api.MintLaunchNonce(a.Cfg.StateDir())
	if err != nil {
		return nil
	}
	jar, _ := cookiejar.New(nil)
	d := &daemonClient{base: fmt.Sprintf("http://127.0.0.1:%d", a.Cfg.Dashboard.Port), http: &http.Client{Jar: jar, Timeout: 0}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", d.base+"/auth?t="+nonce, nil)
	res, err := d.http.Do(req)
	if err != nil {
		return nil
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return d
}

func (d *daemonClient) op(ctx context.Context, name string, in, out any) error {
	b, err := json.Marshal(in)
	if err != nil {
		return err
	}
	req, _ := http.NewRequestWithContext(ctx, "POST", d.base+"/api/ops/"+name, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-MetaRush-Client", "cli")
	res, err := d.http.Do(req)
	if err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush running in the background didn't answer.")
	}
	defer res.Body.Close()
	var env struct {
		OK    bool            `json:"ok"`
		Data  json.RawMessage `json:"data"`
		Error *apperr.Error   `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(res.Body, 64<<20)).Decode(&env); err != nil {
		return apperr.Wrap(apperr.Internal, err, "MetaRush running in the background gave an answer that couldn't be read.")
	}
	if !env.OK {
		if env.Error != nil {
			return env.Error
		}
		return apperr.New(apperr.Internal, "MetaRush running in the background refused the request.").WithDetails(map[string]any{"op": name})
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// follow reads a conversation's SSE stream until fn returns false.
func (d *daemonClient) follow(ctx context.Context, id string, after int, raw bool, fn func(kind string, data []byte) bool) error {
	u := fmt.Sprintf("%s/api/assistant/conversations/%s/events?after=%d", d.base, id, after)
	if raw {
		u += "&raw=1"
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	res, err := d.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 32<<20)
	event := ""
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			kind := event
			if kind == "" {
				kind = "event"
			}
			if !fn(kind, []byte(strings.TrimPrefix(line, "data: "))) {
				return nil
			}
		case line == "":
			event = ""
		}
	}
	return sc.Err()
}

// watch follows a conversation (through the daemon or in this process) until
// fn returns false.
func (s *assistantSession) watch(ctx context.Context, id string, after int, raw bool, fn func(kind string, data []byte) bool) error {
	if s.daemon != nil {
		return s.daemon.follow(ctx, id, after, raw, fn)
	}
	metaVer, partialVer := -1, -1
	for {
		st, err := s.m.Follow(ctx, id, after, metaVer, partialVer, raw)
		if err != nil {
			return err
		}
		for _, e := range st.Events {
			b, _ := json.Marshal(e)
			after = e.Seq
			if !fn("event", b) {
				return nil
			}
		}
		if st.Meta != nil {
			b, _ := json.Marshal(st.Meta)
			if !fn("meta", b) {
				return nil
			}
		}
		metaVer, partialVer = st.MetaVer, st.PartialVer
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-st.Wait:
		}
	}
}

/* ---------- Status, models, settings ---------- */

func assistantStatusCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Check whether Claude Code can run here (version, sign-in, billing mode, limits); no prompt is sent",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			var st assistant.AssistantStatus
			if err := s.call(c.Context(), "assistant.status", assistant.StatusIn{Refresh: refresh}, &st); err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"status": st, "workspace": s.a.Cfg.AssistantDir()}, func() {
				if st.Available {
					login := "sign-in not known"
					if st.LoggedIn != nil && *st.LoggedIn {
						login = "signed in"
						if st.AuthMethod != "" {
							login += " (" + st.AuthMethod + ")"
						}
					}
					fmt.Printf("✓ Claude Code is ready on %s, %s\n", orText(st.Machine, "this machine"), login)
				} else {
					fmt.Printf("✗ %s\n", st.Reason)
					if st.Hint != "" {
						fmt.Printf("  %s\n", st.Hint)
					}
				}
				switch {
				case st.Waiting == 1:
					fmt.Println("  1 card or question is waiting for you in your conversations here")
				case st.Waiting > 1:
					fmt.Printf("  %d cards and questions are waiting for you in your conversations here\n", st.Waiting)
				}
				fmt.Printf("  billing: %s\n", st.BillingLabel)
				if a := st.Account; a != nil {
					// Whose Claude answers here, and its terms (D239).
					fmt.Printf("  answers: %s\n", a.Who)
					fmt.Printf("  its terms: %s. %s\n", sentence(a.TermsName), a.DataUse)
				}
				fmt.Printf("  each message: up to %d steps and %d minutes; a quiet conversation pauses after %d minutes\n", st.MaxSteps, st.TimeoutMinutes, st.IdleMinutes)
				for _, l := range limitLines(st.Billing, st.TokenLimitM, st.BudgetUSD) {
					fmt.Printf("  %s\n", l)
				}
				if l := planLine(st.PlanUsage); l != "" {
					fmt.Printf("  %s\n", l)
				}
				if st.Model != "" {
					fmt.Printf("  model: %s\n", st.Model)
				}
				if detailsMode(c) {
					fmt.Printf("  Claude Code %s · machine %s · workspace folder %s\n", orText(st.ClaudeVersion, "-"), st.Machine, s.a.Cfg.AssistantDir())
				}
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", true, "re-run the checks instead of using the cached result")
	return forOps(cmd, "assistant.status")
}

// limitLines describe the per-conversation limits (D181): tokens, and with an
// API key the dollars too.
func limitLines(billing string, millions, usd float64) []string {
	out := []string{fmt.Sprintf("limit: %s tokens per conversation", assistant.Millions(int64(millions*1e6)))}
	if billing != "plan" && usd > 0 {
		out = append(out, fmt.Sprintf("spending limit: $%.2f per conversation (your API key is billed per token)", usd))
	}
	return out
}

// planLine says how much of the Claude plan is used ("" when Claude Code
// hasn't said yet).
func planLine(p *assistant.PlanUsage) string {
	if p == nil {
		return ""
	}
	var parts []string
	if w := p.SevenDay; w != nil {
		parts = append(parts, fmt.Sprintf("%.0f%% of this week's limit used (it starts again %s)", w.Used*100, w.ResetsAt.Local().Format("Mon 3:04 pm")))
	}
	if w := p.FiveHour; w != nil {
		parts = append(parts, fmt.Sprintf("%.0f%% of the five-hour window", w.Used*100))
	}
	if len(parts) == 0 {
		return ""
	}
	return "your Claude plan: " + strings.Join(parts, "; ")
}

// onOff reads an on/off flag.
func onOff(c *cobra.Command, name, v string) (*bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "on", "true", "yes":
		t := true
		return &t, nil
	case "off", "false", "no":
		f := false
		return &f, nil
	}
	return nil, fail(c, apperr.InvalidInput, "--"+name+" takes on or off", "")
}

// tokenCount reads a number of tokens: 200000, 200k, 0.2m (0 is the default).
func tokenCount(c *cobra.Command, name, v string) (*int, error) {
	t := strings.ToLower(strings.TrimSpace(v))
	mul := 1.0
	switch {
	case strings.HasSuffix(t, "k"):
		mul, t = 1e3, strings.TrimSuffix(t, "k")
	case strings.HasSuffix(t, "m"):
		mul, t = 1e6, strings.TrimSuffix(t, "m")
	}
	f, err := strconv.ParseFloat(strings.ReplaceAll(t, ",", ""), 64)
	if err != nil || f < 0 {
		return nil, fail(c, apperr.InvalidInput, "--"+name+" takes a number of tokens, like 200000 or 200k", "")
	}
	n := int(f * mul)
	return &n, nil
}

func assistantModelsCmd() *cobra.Command {
	var refresh bool
	cmd := &cobra.Command{
		Use:   "models",
		Short: "List the models and effort levels the installed Claude Code offers (no prompt is sent)",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			var out assistant.AssistantModelsOut
			if err := s.call(c.Context(), "assistant.models", assistant.ModelsIn{Refresh: refresh}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				for _, m := range out.Models {
					effort := "effort: not adjustable"
					if len(m.EffortLevels) > 0 {
						effort = "effort: " + strings.Join(m.EffortLevels, ", ")
					}
					fmt.Printf("%-22s %-24s %s\n", m.Value, m.DisplayName, effort)
				}
				for _, m := range out.Unavailable {
					fmt.Printf("%-22s %-24s %s\n", m.Value, m.DisplayName, m.Unavailable)
				}
				if out.Note != "" {
					fmt.Println(out.Note)
				}
			})
		},
	}
	cmd.Flags().BoolVar(&refresh, "refresh", false, "ask Claude Code again instead of using the cached list")
	return forOps(cmd, "assistant.models")
}

func assistantSettingsCmd() *cobra.Command {
	var model, effort, safeSteps, compact, compactAt, autoCompact, cacheHour, holdNews string
	var limit, tokenLimit float64
	cmd := &cobra.Command{
		Use:   "settings",
		Short: "Show or change the installation defaults: model, effort, the per-conversation limit, summaries and the cache",
		Example: "  metarush assistant settings\n  metarush assistant settings --model sonnet --effort medium\n" +
			"  metarush assistant settings --model default --effort default   # Claude Code's own defaults\n" +
			"  metarush assistant settings --limit 80                         # 80 million tokens per conversation\n" +
			"  metarush assistant settings --compact-at 150k --cache-hour on   # summarize sooner, keep the cache an hour\n" +
			"  metarush assistant settings --safe-steps off                   # every step Claude asks for waits for your click",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openAssistant(c, true)
			if err != nil {
				return err
			}
			defer s.close()
			var out assistant.AssistantSettingsOut
			in := assistant.AssistantSettingsIn{}
			changed := false
			if c.Flags().Changed("model") {
				in.Model, changed = &model, true
			}
			if c.Flags().Changed("effort") {
				in.Effort, changed = &effort, true
			}
			if c.Flags().Changed("safety-limit") {
				in.SafetyLimit, changed = &limit, true
			}
			if c.Flags().Changed("limit") {
				in.TokenLimitM, changed = &tokenLimit, true
			}
			for _, f := range []struct {
				name string
				v    *string
				to   **bool
			}{{"safe-steps", &safeSteps, &in.SafeSteps}, {"compact", &compact, &in.Compact}, {"cache-hour", &cacheHour, &in.CacheHour}, {"hold-news", &holdNews, &in.HoldNews}} {
				if c.Flags().Changed(f.name) {
					b, err := onOff(c, f.name, *f.v)
					if err != nil {
						return err
					}
					*f.to, changed = b, true
				}
			}
			for _, f := range []struct {
				name string
				v    *string
				to   **int
			}{{"compact-at", &compactAt, &in.CompactAtTokens}, {"autocompact", &autoCompact, &in.AutoCompactTokens}} {
				if c.Flags().Changed(f.name) {
					n, err := tokenCount(c, f.name, *f.v)
					if err != nil {
						return err
					}
					*f.to, changed = n, true
				}
			}
			name := "assistant.settings"
			var input any = ops.EmptyIn{}
			if changed {
				name, input = "assistant.settings.set", in
			}
			if err := s.call(c.Context(), name, input, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Printf("model:   %s\n", orText(out.Model, "default (Claude Code's choice)"))
				fmt.Printf("effort:  %s\n", orText(out.Effort, "default"))
				fmt.Printf("billing: %s\n", out.BillingLabel)
				fmt.Printf("each message: up to %d steps and %d minutes\n", out.MaxSteps, out.TimeoutMinutes)
				for _, l := range limitLines(out.Billing, out.TokenLimitM, out.BudgetUSD) {
					fmt.Println(l)
				}
				if out.Compact {
					fmt.Printf("summaries: when Claude's memory passes %s tokens (Claude Code's own at %s)\n", assistant.Millions(int64(out.CompactAtTokens)), assistant.Millions(int64(out.AutoCompactTokens)))
				} else {
					fmt.Printf("summaries: off (Claude Code's own at %s)\n", assistant.Millions(int64(out.AutoCompactTokens)))
				}
				if out.CacheHour {
					fmt.Println("cache: an hour (a pause doesn't make the next message read everything again)")
				} else {
					fmt.Println("cache: five minutes (Claude Code's default)")
				}
				if out.HoldNews {
					fmt.Println("your clicks on Claude's cards: told with your next message (safe steps and failures at once)")
				} else {
					fmt.Println("your clicks on Claude's cards: told at once, as a message of their own")
				}
				if out.SafeSteps {
					fmt.Println("safe steps: run by themselves (checking a graphic Claude made, a review copy, a link you typed in the conversation)")
				} else {
					fmt.Println("safe steps: wait for your click, like every other card")
				}
				if l := planLine(out.PlanUsage); l != "" {
					fmt.Println(l)
				}
				fmt.Println("Bigger models and higher effort use more of your Claude plan's usage (or cost more with an API key).")
			})
		},
	}
	cmd.Flags().StringVar(&safeSteps, "safe-steps", "", "on: checks, review copies and links you typed start by themselves (default); off: they wait for your click")
	cmd.Flags().StringVar(&model, "model", "", "default model (an alias like sonnet, a model name, or default)")
	cmd.Flags().StringVar(&effort, "effort", "", "default effort: low, medium, high, xhigh, max or default")
	cmd.Flags().Float64Var(&tokenLimit, "limit", 0, "per-conversation limit in millions of tokens (0 = the default, 50)")
	cmd.Flags().StringVar(&compact, "compact", "", "on: Claude summarizes a conversation whose memory passes --compact-at (default); off: only Claude Code's own")
	cmd.Flags().StringVar(&compactAt, "compact-at", "", "summarize when Claude's memory passes this many tokens (e.g. 200k; 0 = the default)")
	cmd.Flags().StringVar(&autoCompact, "autocompact", "", "Claude Code's own summary point inside one long message (100k–1m; 0 = the default, 300k)")
	cmd.Flags().StringVar(&cacheHour, "cache-hour", "", "on: keep the prompt cache an hour (default); off: Claude Code's five minutes")
	cmd.Flags().StringVar(&holdNews, "hold-news", "", "on: your clicks on Claude's cards go with your next message (default); off: at once")
	cmd.Flags().Float64Var(&limit, "safety-limit", 0, "per-conversation spending limit in dollars with an API key (0 = the default)")
	return forOps(cmd, "assistant.settings", "assistant.settings.set")
}

func orText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

/* ---------- Conversations ---------- */

func conversationsCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "conversations", Aliases: []string{"conversation"}, Short: "Browse conversations (every machine of the workspace)"}
	var in assistant.ConversationListIn
	list := &cobra.Command{
		Use:   "list",
		Short: "List conversations, most recent first (a project's: everyone's; otherwise yours)",
		RunE: func(c *cobra.Command, _ []string) error {
			if in.Mine && in.AllUsers {
				return fail(c, apperr.InvalidInput, "Use --mine or --all-users, not both.", "")
			}
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			var out assistant.ConversationListOut
			if err := s.call(c.Context(), "assistant.list", in, &out); err != nil {
				return failErr(c, err)
			}
			var st assistant.AssistantStatus
			_ = s.call(c.Context(), "assistant.status", assistant.StatusIn{}, &st)
			details := detailsMode(c)
			me := s.a.Actor.UserID
			return emit(c, out, func() {
				if len(out.Conversations) == 0 {
					fmt.Println("no conversations yet")
				}
				for _, cv := range out.Conversations {
					where := ""
					if !cv.Here {
						where = "  on " + cv.InstallationName
					}
					id := ""
					if details {
						id = cv.ID + "  "
					}
					fmt.Printf("%s%-24s %-16s %2d msg  %s%s%s%s\n", id, convStateWords(&cv, me), cv.UpdatedAt.Local().Format("2006-01-02 15:04"), cv.TurnCount,
						trunc(cv.Title, 48), spend(st.Billing, cv.CostUSD, false)+tokensWord(cv.Tokens.Total), where, convStarterWords(&cv, me, "  "))
				}
				switch {
				case !out.AllUsers && out.Others == 1:
					fmt.Println("(1 conversation someone else started matches too: add --all-users to see it.)")
				case !out.AllUsers && out.Others > 1:
					fmt.Printf("(%d conversations other people started match too: add --all-users to see them.)\n", out.Others)
				}
				if !details && len(out.Conversations) > 0 {
					fmt.Println("(Add --details to see each conversation's ID, to open one with conversations show.)")
				}
			})
		},
	}
	list.Flags().StringVar(&in.ProjectID, "project", "", "only this project (everyone's conversations in it, unless --mine)")
	list.Flags().StringVar(&in.Search, "search", "", "words in the title or any message")
	list.Flags().BoolVar(&in.Archived, "archived", false, "list archived conversations instead")
	list.Flags().BoolVar(&in.AllUsers, "all-users", false, "include other people's conversations (the default with --project)")
	list.Flags().BoolVar(&in.Mine, "mine", false, "only the conversations you started (the default without --project)")
	list.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 50)")
	cmd.AddCommand(forOps(list, "assistant.list"))
	var raw, noEvents bool
	show := &cobra.Command{
		Use:   "show <id>",
		Short: "Show a conversation: messages, Claude's replies, steps and proposals (--raw adds the exact prompts and tool I/O)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			events := !noEvents
			var cv assistant.Conversation
			if err := s.call(c.Context(), "assistant.show", assistant.ConversationShowIn{ID: args[0], Events: &events, Raw: raw}, &cv); err != nil {
				return failErr(c, err)
			}
			var st assistant.AssistantStatus
			_ = s.call(c.Context(), "assistant.status", assistant.StatusIn{}, &st)
			return emit(c, cv, func() { printConversation(os.Stdout, &cv, st.Billing, raw, detailsMode(c), s.a.Actor.UserID) })
		},
	}
	show.Flags().BoolVar(&raw, "raw", false, "include the exact messages sent to Claude Code, its system rules and raw tool inputs/outputs")
	show.Flags().BoolVar(&noEvents, "no-events", false, "only the conversation's metadata and turns")
	cmd.AddCommand(forOps(show, "assistant.show"))
	return cmd
}

// spend renders a cost in the billing mode's terms: dollars only for API-key
// billing; on a Claude plan only in the raw view, as an estimate.
// tokensWord is a conversation's tokens for its list line ("" before any).
func tokensWord(n int64) string {
	if n <= 0 {
		return ""
	}
	return "  " + assistant.Millions(n) + " tokens"
}

func spend(billing string, usd float64, raw bool) string {
	switch {
	case usd <= 0:
		return ""
	case billing == "plan" && raw:
		return fmt.Sprintf("  ~$%.2f API-equivalent estimate (not billed on your plan)", usd)
	case billing == "plan":
		return ""
	case billing == "api":
		return fmt.Sprintf("  $%.2f", usd)
	case raw:
		return fmt.Sprintf("  ~$%.2f (estimate)", usd)
	}
	return ""
}

/* ---------- Ask / continue (and waiting for the reply) ---------- */

type msgFlags struct {
	project, revision, asset, clip string
	budget, limit                  float64
	model, effort, title           string
	wait, raw                      bool
	attach                         []string
}

func (f *msgFlags) bind(cmd *cobra.Command, first bool) {
	cmd.Flags().StringVar(&f.project, "project", "", "project ID for context")
	cmd.Flags().StringVar(&f.revision, "revision", "", "revision ID in view")
	cmd.Flags().StringVar(&f.asset, "asset", "", "selected footage (asset ID)")
	cmd.Flags().StringVar(&f.clip, "clip", "", "selected clip ID")
	cmd.Flags().StringVar(&f.model, "model", "", "model for this conversation (alias like sonnet, a model name, or default)")
	cmd.Flags().StringVar(&f.effort, "effort", "", "effort for this conversation: low, medium, high, xhigh, max or default")
	cmd.Flags().BoolVar(&f.wait, "wait", false, "stay until Claude has answered (prints the reply; JSON lines with --json)")
	cmd.Flags().BoolVar(&f.raw, "raw", false, "with --wait: also print raw tool details")
	cmd.Flags().StringArrayVar(&f.attach, "attach", nil, "attach a picture or a PDF (Claude can look at it; MetaRush asks whether to keep it); repeat for more")
	if first {
		cmd.Flags().Float64Var(&f.limit, "limit", 0, "limit for the whole conversation in millions of tokens (default: the installation's, 50)")
		cmd.Flags().Float64Var(&f.budget, "budget", 0, "spending limit for the whole conversation in dollars (API-key sign-in only)")
		cmd.Flags().StringVar(&f.title, "title", "", "title (default: Claude names it)")
	}
}

func (f *msgFlags) message(prompt string) assistant.AssistantMessageIn {
	return assistant.AssistantMessageIn{Prompt: prompt, ProjectID: f.project, RevisionID: f.revision, AssetID: f.asset, ClipID: f.clip}
}

// attachFiles stores the files given with --attach (op assistant.attach) and
// returns them to send with the message.
func (f *msgFlags) attachFiles(c *cobra.Command, s *assistantSession) ([]models.KeepFile, error) {
	var out []models.KeepFile
	for _, p := range f.attach {
		st, err := os.Stat(p)
		if err != nil || !st.Mode().IsRegular() {
			return nil, fail(c, apperr.InvalidInput, "MetaRush can't find the file "+filepath.Base(p)+" to attach.", "")
		}
		if st.Size() > 20<<20 {
			return nil, fail(c, apperr.InvalidInput, filepath.Base(p)+" is larger than 20 MB; attach a smaller file.", "")
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return nil, fail(c, apperr.InvalidInput, "MetaRush couldn't read "+filepath.Base(p)+".", "")
		}
		var res assistant.AttachOut
		if err := s.humanCall(c.Context(), "assistant.attach", assistant.AttachIn{Name: filepath.Base(p), Data: base64.StdEncoding.EncodeToString(b)}, &res); err != nil {
			return nil, failErr(c, err)
		}
		out = append(out, res.File)
	}
	return out, nil
}

func readPrompt(args []string) (string, error) {
	p := strings.TrimSpace(strings.Join(args, " "))
	if p == "-" {
		b, err := io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
		if err != nil {
			return "", err
		}
		p = strings.TrimSpace(string(b))
	}
	return p, nil
}

func assistantAskCmd() *cobra.Command {
	var f msgFlags
	cmd := &cobra.Command{
		Use:   "ask <prompt|->",
		Short: "Start a conversation with Claude Code (it reads and proposes; you apply)",
		Example: "  metarush assistant ask \"What's in this project?\" --project prj_… --wait\n" +
			"  echo \"Tighten the b-roll\" | metarush assistant ask - --project prj_… --wait --json",
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			prompt, err := readPrompt(args)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			s, err := openAssistant(c, true)
			if err != nil {
				return err
			}
			defer s.close()
			in := assistant.AssistantAskIn{AssistantMessageIn: f.message(prompt), BudgetUSD: f.budget, TokenLimitM: f.limit, Model: f.model, Effort: f.effort, Title: f.title}
			if in.Attachments, err = f.attachFiles(c, s); err != nil {
				return err
			}
			var out assistant.ConversationSendOut
			if err := s.call(c.Context(), "assistant.ask", in, &out); err != nil {
				return failErr(c, err)
			}
			return finishSend(c, s, &out, f)
		},
	}
	f.bind(cmd, true)
	return forOps(cmd, "assistant.ask", "assistant.attach")
}

func assistantContinueCmd() *cobra.Command {
	var f msgFlags
	cmd := &cobra.Command{
		Use:   "continue <id> <prompt|->",
		Short: "Send a follow-up message (queued while Claude is working); only on the machine that holds the conversation",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			prompt, err := readPrompt(args[1:])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			s, err := openAssistant(c, true)
			if err != nil {
				return err
			}
			defer s.close()
			in := assistant.AssistantContinueIn{ID: args[0], AssistantMessageIn: f.message(prompt)}
			if in.Attachments, err = f.attachFiles(c, s); err != nil {
				return err
			}
			if c.Flags().Changed("model") {
				in.Model = &f.model
			}
			if c.Flags().Changed("effort") {
				in.Effort = &f.effort
			}
			var out assistant.ConversationSendOut
			if err := s.call(c.Context(), "assistant.continue", in, &out); err != nil {
				return failErr(c, err)
			}
			return finishSend(c, s, &out, f)
		},
	}
	f.bind(cmd, false)
	return forOps(cmd, "assistant.continue", "assistant.attach")
}

// finishSend reports a sent message, and with --wait (always when this process
// holds the conversation) follows it until Claude is done.
func finishSend(c *cobra.Command, s *assistantSession, out *assistant.ConversationSendOut, f msgFlags) error {
	cv := out.Conversation
	if !f.wait && s.daemon != nil {
		return emit(c, out, func() {
			if out.Queued {
				fmt.Printf("queued in %s (%s); Claude is still working on the previous message\n", cv.ID, out.QueueID)
			} else {
				fmt.Printf("%s  turn %d started — follow it with `metarush assistant conversations show %s` or in the dashboard\n", cv.ID, out.Turn, cv.ID)
			}
		})
	}
	if !f.wait && !jsonMode(c) {
		fmt.Fprintln(os.Stderr, "(no dashboard daemon is running, so this terminal holds the conversation until Claude answers)")
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	var st assistant.AssistantStatus
	_ = s.call(context.Background(), "assistant.status", assistant.StatusIn{}, &st)
	jsonLines := jsonMode(c)
	enc := json.NewEncoder(os.Stdout)
	last := cv
	p := newThreadPrinter(os.Stdout, cv.ID, st.Billing, f.raw, detailsMode(c))
	p.me = s.a.Actor.UserID
	after := lastSeq(cv.ID, s)
	sawEnd := false
	interactiveActs := interactive() && !jsonLines
	var pendingActs []assistant.AssistantAction
	err := s.watch(ctx, cv.ID, after, f.raw, func(kind string, data []byte) bool {
		switch kind {
		case "event":
			var e assistant.AssistantEvent
			if json.Unmarshal(data, &e) != nil {
				return true
			}
			if jsonLines {
				_ = enc.Encode(map[string]any{"event": e})
			} else {
				p.event(e)
			}
			if e.Kind == "turn_end" {
				sawEnd = true
			}
		case "meta":
			var m assistant.Conversation
			if json.Unmarshal(data, &m) != nil {
				return true
			}
			last = m
			for _, a := range m.Actions {
				if _, seen := p.actions[a.ID]; !seen && a.State == assistant.ActionPending && a.Op != "" {
					pendingActs = append(pendingActs, a)
				}
				p.actions[a.ID] = a
				if a.ToolUseID != "" {
					p.cardTools[a.ToolUseID] = true
				}
			}
			for _, t := range m.Turns {
				p.turns[t.N] = t
			}
		}
		return !(sawEnd && last.State == assistant.ConvIdle && len(last.Queue) == 0)
	})
	p.flush()
	if ctx.Err() != nil {
		// Ctrl-C: stop Claude's turn, keep the conversation.
		var iout assistant.ConversationInterruptOut
		_ = s.call(context.Background(), "assistant.interrupt", assistant.ConversationIn{ID: cv.ID}, &iout)
		return failErr(c, apperr.New(apperr.Canceled, "Interrupted. The conversation is kept.").
			WithHint("Continue it in the dashboard's Claude Code panel.").WithCommand("metarush assistant continue "+cv.ID+" …"))
	}
	if err != nil {
		return failErr(c, err)
	}
	if interactiveActs {
		for _, a := range pendingActs {
			if a.Question != nil {
				continue // answered on its card, or with assistant act --choice/--answer
			}
			ask := fmt.Sprintf("Claude asks: %s. Do it now?", a.Title)
			if a.SavesVersion {
				ask = fmt.Sprintf("Claude asks: %s. Save it and render a review copy?", a.Title)
			}
			if askYes(ask, false) {
				var ao assistant.ConversationActOut
				if err := s.humanCall(context.Background(), "assistant.act", assistant.ConversationActIn{ID: cv.ID, ActionID: a.ID, ReviewCopy: true}, &ao); err != nil {
					fmt.Fprintf(os.Stderr, "could not: %s\n", apperr.From(err).Message)
				} else {
					fmt.Printf("  %s: %s\n", upperFirstWord(cardStateWords(ao.Action)), orText(ao.Action.ShownLine(), ao.Action.Error))
				}
			}
		}
	}
	if jsonLines {
		return enc.Encode(map[string]any{"ok": true, "data": last})
	}
	return nil
}

// lastSeq is the transcript position before this message (so --wait prints
// only what follows it).
func lastSeq(id string, s *assistantSession) int {
	var cv assistant.Conversation
	events := true
	if err := s.call(context.Background(), "assistant.show", assistant.ConversationShowIn{ID: id, Events: &events}, &cv); err != nil {
		return 0
	}
	// Start at this message's own turn marker.
	for i := len(cv.Events) - 1; i >= 0; i-- {
		if cv.Events[i].Kind == "turn" {
			return cv.Events[i].Seq - 1
		}
	}
	if n := len(cv.Events); n > 0 {
		return cv.Events[n-1].Seq
	}
	return 0
}

/* ---------- Interrupt, stop, queue, rename, archive, configure ---------- */

func simpleConvCmd(use, short, op string, run func(c *cobra.Command, s *assistantSession, args []string) error) *cobra.Command {
	cmd := &cobra.Command{Use: use, Short: short, Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openAssistant(c, true)
			if err != nil {
				return err
			}
			defer s.close()
			return run(c, s, args)
		}}
	return forOps(cmd, op)
}

func assistantInterruptCmd() *cobra.Command {
	return simpleConvCmd("interrupt <id>", "Interrupt Claude's current turn (the conversation goes on; queued messages are handed back)", "assistant.interrupt",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			var out assistant.ConversationInterruptOut
			if err := s.call(c.Context(), "assistant.interrupt", assistant.ConversationIn{ID: args[0]}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Printf("interrupted %s\n", args[0])
				for _, q := range out.Requeued {
					fmt.Printf("  not sent: %s\n", q.Prompt)
				}
			})
		})
}

func assistantStopCmd() *cobra.Command {
	return simpleConvCmd("stop <id>", "End the conversation's Claude Code process (a later message resumes the session)", "assistant.stop",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			var out assistant.Conversation
			if err := s.call(c.Context(), "assistant.stop", assistant.ConversationIn{ID: args[0]}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Printf("stopped Claude Code for %s (continue it any time to resume)\n", out.ID) })
		})
}

func assistantDequeueCmd() *cobra.Command {
	cmd := simpleConvCmd("dequeue <id> <queue-id>", "Remove a queued message before it is sent", "assistant.dequeue",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			if len(args) != 2 {
				return fail(c, apperr.InvalidInput, "give the conversation and the queued message ID", "")
			}
			var out assistant.Conversation
			if err := s.call(c.Context(), "assistant.dequeue", assistant.ConversationDequeueIn{ID: args[0], QueueID: args[1]}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Printf("removed %s; %d still queued\n", args[1], len(out.Queue)) })
		})
	return cmd
}

func assistantRenameCmd() *cobra.Command {
	return simpleConvCmd("rename <id> <title>", "Rename a conversation", "assistant.rename",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			var out assistant.Conversation
			if err := s.call(c.Context(), "assistant.rename", assistant.ConversationRenameIn{ID: args[0], Title: strings.Join(args[1:], " ")}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() { fmt.Printf("%s is now “%s”\n", out.ID, out.Title) })
		})
}

func assistantArchiveCmd() *cobra.Command {
	var restore bool
	cmd := simpleConvCmd("archive <id>", "Archive a conversation (--restore brings it back)", "assistant.archive",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			v := !restore
			var out assistant.Conversation
			if err := s.call(c.Context(), "assistant.archive", assistant.ConversationArchiveIn{ID: args[0], Archived: &v}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				if out.Archived {
					fmt.Printf("archived %s\n", out.ID)
				} else {
					fmt.Printf("restored %s\n", out.ID)
				}
			})
		})
	cmd.Flags().BoolVar(&restore, "restore", false, "restore an archived conversation")
	return cmd
}

func assistantConfigureCmd() *cobra.Command {
	var model, effort string
	var budget, limit float64
	cmd := simpleConvCmd("configure <id>", "Change a conversation's model, effort or limit (from its next message on, also after a resume)", "assistant.configure",
		func(c *cobra.Command, s *assistantSession, args []string) error {
			in := assistant.ConversationConfigureIn{ID: args[0]}
			if c.Flags().Changed("model") {
				in.Model = &model
			}
			if c.Flags().Changed("effort") {
				in.Effort = &effort
			}
			if c.Flags().Changed("budget") {
				in.BudgetUSD = &budget
			}
			if c.Flags().Changed("limit") {
				in.TokenLimitM = &limit
			}
			var out assistant.Conversation
			if err := s.call(c.Context(), "assistant.configure", in, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Printf("%s: model %s, effort %s, limit %s tokens (%s used)", out.ID, orText(out.Model, "default"), orText(out.Effort, "default"),
					assistant.Millions(out.LimitTokens), assistant.Millions(out.Tokens.Total))
				if out.BudgetUSD > 0 {
					fmt.Printf(", spending limit $%.2f", out.BudgetUSD)
				}
				fmt.Println()
			})
		})
	cmd.Flags().StringVar(&model, "model", "", "model (alias, model name or default)")
	cmd.Flags().StringVar(&effort, "effort", "", "effort: low, medium, high, xhigh, max or default")
	cmd.Flags().Float64Var(&limit, "limit", 0, "limit for the conversation in millions of tokens")
	cmd.Flags().Float64Var(&budget, "budget", 0, "spending limit for the conversation in dollars (API-key sign-in only)")
	return cmd
}

/* ---------- Proposals, apply, action cards, suggestions ---------- */

func assistantProposalsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "proposals <id>",
		Short: "List a conversation's plans and render requests, checked against the current head (with confirm tokens)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			var out assistant.ConversationProposals
			if err := s.call(c.Context(), "assistant.proposals", assistant.ConversationIn{ID: args[0]}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				if !out.Here {
					fmt.Printf("the proposal files are on %s\n", out.Machine)
					return
				}
				if len(out.Turns) == 0 {
					fmt.Println("no proposals in this conversation")
				}
				for _, t := range out.Turns {
					fmt.Printf("message %d\n", t.Turn)
					for _, p := range t.Plans {
						state := "invalid"
						switch {
						case p.Applied != nil:
							state = fmt.Sprintf("saved as version %d", p.Applied.Number)
						case p.Check != nil && p.Check.Valid:
							state = "ready"
						case p.Error != nil:
							state = p.Error.Message
						}
						fmt.Printf("  plan %-28s %s — %s\n", p.Ref, trunc(orText(p.Instruction, p.File), 50), state)
						if p.Check != nil && p.Check.Diff != nil {
							for _, line := range p.Check.Diff.Summary {
								fmt.Printf("      %s\n", line)
							}
						}
						if p.Applied == nil && p.Check != nil && p.Check.Valid && p.Check.ConfirmToken != "" {
							fmt.Printf("      apply: metarush assistant apply %s %s --confirm %s\n", out.ConversationID, p.Ref, p.Check.ConfirmToken)
						}
					}
					for _, r := range t.Renders {
						fmt.Printf("  render %-26s %s\n", r.Ref, orText(r.Error, orText(r.Waiting, "ready")))
					}
					for _, pr := range t.Problems {
						fmt.Printf("  (%s)\n", pr)
					}
				}
			})
		},
	}
	return forOps(cmd, "assistant.proposals")
}

func assistantApplyCmd() *cobra.Command {
	var confirm string
	var allowUnlock bool
	cmd := &cobra.Command{
		Use:   "apply <id> <proposal> --confirm <token>",
		Short: "Apply one of a conversation's plans (the confirm token comes from `assistant proposals`, like `edit apply`)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			exe, _ := os.Executable()
			m := assistant.NewTerminal(a, exe)
			defer m.Close()
			return runOpWith(c, a, "assistant.apply", assistant.ConversationApplyIn{ID: args[0], Proposal: args[1], ConfirmToken: confirm, AllowUnlock: allowUnlock}, ops.Caller{}, func(o any) {
				r := o.(*ops.EditApplyOut)
				fmt.Printf("Saved as version %d (%.1f s long).\n%s\n", r.Revision.Number, r.Revision.Duration, r.URL)
				for _, s := range r.Diff.Summary {
					fmt.Println("  " + s)
				}
			})
		},
	}
	cmd.Flags().StringVar(&confirm, "confirm", "", "token from `assistant proposals` for this exact plan")
	cmd.Flags().BoolVar(&allowUnlock, "allow-unlock", false, "permit changes to locked items (only when you asked for that)")
	return forOps(cmd, "assistant.apply")
}

func assistantActCmd() *cobra.Command {
	var dismiss, quiet, all, saveOnly bool
	var choices []int
	var answer, notQuite, keepWhere string
	cmd := &cobra.Command{
		Use:   "act <id> [<card>…]",
		Short: "Do, answer or set aside what Claude asked for (one card, several, --all, or a question card); people only (you confirm in this terminal)",
		Example: "  metarush assistant act asc_… act_…                 # one card (a save also renders a review copy; --save-only doesn't)\n" +
			"  metarush assistant act asc_… --all                  # every card waiting: Claude hears about them in one message\n" +
			"  metarush assistant act asc_… act_… --choice 1 --choice 3   # answer a question card (--answer \"…\" in your own words)\n" +
			"  metarush assistant act asc_… act_… --not-quite \"shorter opening\"   # send it back: Claude makes a new version with that change",
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !interactive() {
				return fail(c, apperr.HumanOnly, "running Claude's requests is for people", "run this command yourself in a terminal, or use the card in the dashboard")
			}
			if len(args) == 1 && !all {
				return fail(c, apperr.InvalidInput, "say which card (its ID from conversations show --details), or --all for every card waiting", "")
			}
			s, err := openAssistant(c, true)
			if err != nil {
				return err
			}
			defer s.close()
			var cv assistant.Conversation
			events := false
			if err := s.call(c.Context(), "assistant.show", assistant.ConversationShowIn{ID: args[0], Events: &events}, &cv); err != nil {
				return failErr(c, err)
			}
			cards, err := cardsToAct(&cv, args[1:], all, len(choices) > 0 || answer != "")
			if err != nil {
				return failErr(c, err)
			}
			in := assistant.ConversationActIn{ID: args[0], Dismiss: dismiss, Quiet: quiet, ReviewCopy: !saveOnly, NotQuite: strings.TrimSpace(notQuite), KeepWhere: keepWhere}
			if notQuite != "" && in.NotQuite == "" {
				return fail(c, apperr.InvalidInput, "say what should change after --not-quite", "")
			}
			for _, a := range cards {
				in.ActionIDs = append(in.ActionIDs, a.ID)
			}
			if len(choices) > 0 || answer != "" {
				in.Answer = &assistant.QuestionAnswer{Choices: choices, Text: answer}
			}
			for _, a := range cards {
				printCardForDecision(a, in)
			}
			verb := "Do this"
			switch {
			case in.NotQuite != "":
				verb = "Send it back to Claude with your note"
			case in.Answer != nil:
				verb = "Answer this way"
			case dismiss && len(cards) > 1:
				verb = fmt.Sprintf("Set these %d aside", len(cards))
			case dismiss:
				verb = "Set this aside"
			case len(cards) > 1:
				verb = fmt.Sprintf("Do these %d", len(cards))
			}
			if !askYes(verb+"?", false) {
				return fail(c, apperr.Canceled, "not confirmed", "")
			}
			var out assistant.ConversationActOut
			if err := s.humanCall(c.Context(), "assistant.act", in, &out); err != nil {
				return failErr(c, err)
			}
			if s.daemon == nil && (in.NotQuite != "" || in.Answer != nil) && !jsonMode(c) {
				// It went to Claude as your message, and this terminal holds the
				// conversation: follow it until Claude answers (D186).
				fmt.Printf("%s: %s\n", upperFirstWord(cardStateWords(out.Action)), orText(out.Action.Note, out.Action.Result))
				return finishSend(c, s, &assistant.ConversationSendOut{Conversation: out.Conversation}, msgFlags{wait: true})
			}
			return emit(c, out, func() {
				for _, a := range out.Actions {
					fmt.Printf("%s: %s\n", upperFirstWord(cardStateWords(a)), orText(a.Result, orText(a.Error, a.Title)))
					if a.Work != nil && a.Work.State == assistant.WorkRunning {
						if s.daemon != nil {
							fmt.Printf("  Working on %s; Claude hears when it's done.\n", a.Work.What)
						} else {
							fmt.Printf("  Working on %s.\n", a.Work.What)
						}
					}
				}
				switch {
				case in.NotQuite != "":
					fmt.Println("Claude makes a new version with your change.")
				case quiet || dismiss:
				case s.daemon != nil:
					fmt.Println("Claude hears about it in the conversation.")
				default: // no dashboard running: nothing here can wake Claude once this command ends
					fmt.Println("Claude hears about it with your next message in this conversation (or as soon as the dashboard runs).")
				}
			})
		},
	}
	cmd.Flags().BoolVar(&dismiss, "dismiss", false, "set it aside (Not now) instead of doing it")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "do not tell Claude the outcome")
	cmd.Flags().BoolVar(&all, "all", false, "every card waiting in this conversation (not question cards)")
	cmd.Flags().BoolVar(&saveOnly, "save-only", false, "for a card that saves a new version: don't render a review copy of it")
	cmd.Flags().IntSliceVar(&choices, "choice", nil, "answer a question card: a choice's number (repeat it where several may be picked)")
	cmd.Flags().StringVar(&answer, "answer", "", "answer a question card in your own words")
	cmd.Flags().StringVar(&notQuite, "not-quite", "", "send it back with what should change: Claude gets it at once as your message and makes a new version")
	cmd.Flags().StringVar(&keepWhere, "keep-where", "", "for a Keep this rule card: project (just this project) or settings (every project using its settings)")
	return forOps(cmd, "assistant.act")
}

// cardsToAct finds the cards a decision is about: the ones named, or every
// card waiting (--all; question cards are answered one by one).
func cardsToAct(cv *assistant.Conversation, ids []string, all, answering bool) ([]assistant.AssistantAction, error) {
	var out []assistant.AssistantAction
	if all {
		for _, a := range cv.Actions {
			if a.State == assistant.ActionPending && a.Op != "" && a.Question == nil {
				out = append(out, a)
			}
		}
		if len(out) == 0 {
			return nil, apperr.New(apperr.NotFound, "Nothing is waiting for you in this conversation.")
		}
		return out, nil
	}
	for _, id := range ids {
		found := false
		for _, a := range cv.Actions {
			if a.ID == id {
				out, found = append(out, a), true
			}
		}
		if !found {
			return nil, apperr.New(apperr.NotFound, "That card isn't in this conversation.").
				WithDetails(map[string]any{"action": id, "command": "metarush assistant conversations show " + cv.ID + " --details"})
		}
	}
	if answering && (len(out) != 1 || out[0].Question == nil) {
		return nil, apperr.New(apperr.InvalidInput, "Only a question card takes an answer; answer one at a time.")
	}
	return out, nil
}

// printCardForDecision shows a card before the person confirms it.
func printCardForDecision(a assistant.AssistantAction, in assistant.ConversationActIn) {
	if q := a.Question; q != nil {
		fmt.Println(q.Text)
		for i, ch := range q.Choices {
			mark := " "
			if in.Answer != nil {
				for _, n := range in.Answer.Choices {
					if n == i+1 {
						mark = "x"
					}
				}
			}
			fmt.Printf("  [%s] %d. %s\n", mark, i+1, ch)
		}
		if in.Answer != nil && in.Answer.Text != "" {
			fmt.Printf("  Your words: %s\n", in.Answer.Text)
		}
		return
	}
	fmt.Println(a.Title)
	for _, d := range a.Details {
		fmt.Printf("  %s\n", d)
	}
	if a.SavesVersion && in.ReviewCopy && !in.Dismiss {
		fmt.Println("  Then renders a review copy of the new version")
	}
}

func upperFirstWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func assistantSuggestionsCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "suggestions",
		Short: "Suggest a few things to ask Claude for the current project or workspace",
		RunE: func(c *cobra.Command, _ []string) error {
			s, err := openAssistant(c, false)
			if err != nil {
				return err
			}
			defer s.close()
			var out assistant.AssistantSuggestionsOut
			if err := s.call(c.Context(), "assistant.suggestions", assistant.AssistantSuggestionsIn{ProjectID: project}, &out); err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				for _, sg := range out.Suggestions {
					fmt.Printf("• %s\n    %s\n", sg.Label, sg.Prompt)
				}
			})
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "project ID (default: the whole workspace)")
	return forOps(cmd, "assistant.suggestions")
}
