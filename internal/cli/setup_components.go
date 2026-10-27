package cli

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// SetupComponentsOut is what setup did with the optional components
// (D325): what it added and what each unlocks, what was here already, what
// was left out, and what didn't work or waits for a person.
type SetupComponentsOut struct {
	// Chosen are the components this run set out to add.
	Chosen []string `json:"chosen"`
	// Added were added now; Ready were on this computer already.
	Added []components.ComponentAdded `json:"added"`
	Ready []string                    `json:"ready"`
	// LeftOut were offered and not chosen (nothing sets them up by itself).
	LeftOut []string `json:"leftOut"`
	// Failed couldn't be added; NeedsYou only a person can add here.
	Failed   []SetupComponentNote `json:"failed,omitempty"`
	NeedsYou []SetupComponentNote `json:"needsYou,omitempty"`
	// ForAPerson: Claude Code or another AI app ran setup, so nothing was
	// installed; a person adds them (D327).
	ForAPerson bool   `json:"forAPerson,omitempty"`
	Says       string `json:"says"`
}

// SetupComponentNote is one component that wasn't added, and what to do.
type SetupComponentNote struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Says    string `json:"says"`
	Command string `json:"command,omitempty"`
}

// installScope is what setup may install this run.
type installScope int

const (
	// installNothing: an AI app runs setup; a person adds the components.
	installNothing installScope = iota
	// installNamed: no person at a terminal (a script, CI): only the
	// components named with --components.
	installNamed
	// installChosen: a person at their own terminal: what they ticked, or
	// the suggested ones with --yes.
	installChosen
)

// setupInstallScope is the one place that decides what setup may install
// (D327); the first case is setup run by an AI app.
func (s *setupRun) setupInstallScope() installScope {
	switch {
	case agentAtTheKeys(s.c):
		return installNothing
	case !isInteractive():
		return installNamed
	default:
		return installChosen
	}
}

// componentPick is what setup adds this run.
type componentPick struct {
	names []string
	// decided: a person or a flag chose, so the rest are left out.
	decided bool
}

// componentsEnv is this computer as setup sees it.
func componentsEnv(cfg *config.Config, ts *tools.Set) *components.Env {
	return &components.Env{Cfg: cfg, Tools: ts}
}

// chooseComponents decides which optional components setup adds: the flags,
// else the checklist (a person at a terminal), else the suggested ones.
// Running setup again suggests what was chosen before and isn't here, and
// suggested ones nobody decided yet.
func (s *setupRun) chooseComponents(e *components.Env, again bool) error {
	o := s.o
	if o.noComponents && s.changed("components") {
		return fail(s.c, apperr.InvalidInput, "--components and --no-components can't be used together.", "")
	}
	ctx := context.Background()
	offered := e.Optional()
	if o.noComponents {
		s.pick = componentPick{decided: true}
		return nil
	}
	if s.changed("components") {
		var names []string
		for _, n := range strings.Split(o.components, ",") {
			n = strings.TrimSpace(n)
			if n == "" {
				continue
			}
			c, ok := components.Get(n)
			if !ok || c.Required {
				return failErr(s.c, apperr.New(apperr.InvalidInput, "MetaRush has no optional component called “%s”.", n).
					WithDetails(map[string]any{"components": componentNames(offered)}).WithCommand("metarush components"))
			}
			if !slices.ContainsFunc(offered, func(x *components.Component) bool { return x.Name == c.Name }) {
				s.warn(c.Title + " isn't for this computer, so setup leaves it out.")
				continue
			}
			names = append(names, c.Name)
		}
		s.pick = componentPick{names: ordered(names), decided: true}
		return nil
	}
	var want []string
	for _, c := range offered {
		ch, chosen := choice.Of(e.Home(), c.Name)
		switch {
		case !again && c.SuggestedHere(e):
			want = append(want, c.Name)
		case again && chosen && ch.Wanted:
			want = append(want, c.Name)
		case again && (!chosen || ch.Waiting) && c.SuggestedHere(e) && s.prompt:
			// Suggested and nobody decided yet: only a person's answer to the
			// checklist adds it (setup again never does by itself).
			want = append(want, c.Name)
		}
	}
	s.pick = componentPick{names: want, decided: !again}
	if !s.prompt {
		return nil
	}
	return s.componentChecklist(ctx, e, offered)
}

func componentNames(cs []*components.Component) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// ordered puts names in the registry's order (what's needed comes first anyway).
func ordered(want []string) []string {
	var out []string
	for _, c := range components.All() {
		if slices.Contains(want, c.Name) && !slices.Contains(out, c.Name) {
			out = append(out, c.Name)
		}
	}
	return out
}

// componentChecklist asks a person which components to add, with the
// suggested ones ticked.
func (s *setupRun) componentChecklist(ctx context.Context, e *components.Env, offered []*components.Component) error {
	type row struct {
		c     *components.Component
		ready bool
	}
	var rows []row
	for _, c := range offered {
		f := e.Find(ctx, c)
		if f.NotHere != "" {
			continue
		}
		rows = append(rows, row{c, f.Ready})
	}
	ticked := map[string]bool{}
	for _, n := range s.pick.names {
		ticked[n] = true
	}
	for {
		s.say("\nOptional components: each unlocks features. The suggested ones are ticked.")
		mb, mins := 0, 0
		for i, r := range rows {
			if r.ready {
				s.say("   ✓  %-2d %s — already on this computer", i+1, r.c.Title)
				continue
			}
			box := "[ ]"
			if ticked[r.c.Name] {
				box = "[x]"
				mb += r.c.DownloadMB
				mins += r.c.Minutes
			}
			s.say("  %s %-2d %s — %s (%s)", box, i+1, r.c.Title, r.c.Unlocks, r.c.Size())
		}
		s.say("The ticked ones download about %s and take about %s.", sizeWords(mb), minutesWords(mins))
		a := strings.TrimSpace(strings.ToLower(ask("Press Enter to add the ticked ones, type numbers to tick or untick (3 9), or none", "")))
		switch a {
		case "", "y", "yes":
			var want []string
			for _, r := range rows {
				if ticked[r.c.Name] && !r.ready {
					want = append(want, r.c.Name)
				}
			}
			s.pick = componentPick{names: ordered(want), decided: true}
			return nil
		case "none", "no", "n":
			s.pick = componentPick{decided: true}
			return nil
		}
		for _, f := range strings.FieldsFunc(a, func(r rune) bool { return r == ' ' || r == ',' }) {
			i, err := strconv.Atoi(f)
			if err != nil || i < 1 || i > len(rows) {
				s.say("  (%s isn't one of the numbers)", f)
				continue
			}
			n := rows[i-1].c.Name
			ticked[n] = !ticked[n]
		}
	}
}

func (s *setupRun) warn(line string) {
	s.say("! %s", line)
	s.warnings = append(s.warnings, line)
}

func sizeWords(mb int) string {
	switch {
	case mb <= 0:
		return "nothing"
	case mb >= 1000:
		return strings.TrimSuffix(fmt.Sprintf("%.1f", float64(mb)/1000), ".0") + " GB"
	default:
		return strconv.Itoa(mb) + " MB"
	}
}

func minutesWords(m int) string {
	switch {
	case m <= 1:
		return "a minute"
	default:
		return strconv.Itoa(m) + " minutes"
	}
}

// addComponents adds what was chosen and records the choices.
func (s *setupRun) addComponents(ctx context.Context, e *components.Env) SetupComponentsOut {
	out := SetupComponentsOut{Chosen: append([]string{}, s.pick.names...), Added: []components.ComponentAdded{}, Ready: []string{}, LeftOut: []string{}}
	offered := e.Optional()
	scope := s.setupInstallScope()
	var todo []string
	for _, n := range s.pick.names {
		if !e.Ready(ctx, n) {
			todo = append(todo, n)
		}
	}
	if len(todo) > 0 && (scope == installNothing || (scope == installNamed && !s.changed("components"))) {
		// Installing programs is a person's decision (D327): an AI app that
		// runs setup, or a script nobody answers, leaves them for a person
		// (a script installs only the components it names).
		out.ForAPerson = true
		for _, n := range s.pick.names {
			if c, ok := components.Get(n); ok && !slices.Contains(todo, n) {
				out.Ready = append(out.Ready, n)
			} else if ok {
				out.NeedsYou = append(out.NeedsYou, SetupComponentNote{Name: n, Title: c.Title, Says: c.Needed(), Command: "metarush components add " + n})
			}
		}
		// Waiting for a person: nothing sets them up by itself meanwhile (not
		// the daemon's first start, not a worker), and setup asks a person
		// about them when it runs again at a terminal.
		by := "setup run by an AI app"
		if scope == installNamed {
			by = "setup with nobody at a terminal"
		}
		_ = choice.SetWaiting(e.Home(), todo, by)
		out.Says = "Setup didn't add the optional components, because an AI app ran it; a person adds them in Settings → Components."
		if scope == installNamed {
			out.Says = "Setup didn't add the optional components, because nobody answered at a terminal; add them in Settings → Components, or run setup in your own terminal."
		}
		s.say("· %s", out.Says)
		return out
	}
	if len(todo) > 0 {
		s.say("\nAdding the optional components you chose; this is the longest part.")
	}
	added := map[string]bool{}
	for _, n := range s.pick.names {
		c, _ := components.Get(n)
		if e.Ready(ctx, n) {
			if !added[n] {
				out.Ready = append(out.Ready, n)
			}
			continue
		}
		res, err := e.Add(ctx, n, components.AddOptions{By: "setup", Say: func(_, msg string) {
			if msg != "" {
				s.tech("%s", msg)
			}
		}})
		if err != nil {
			ae := apperr.From(err)
			out.Failed = append(out.Failed, SetupComponentNote{Name: n, Title: c.Title, Says: ae.Message, Command: "metarush components add " + n})
			s.say("! %s", ae.Message)
			_ = choice.Set(e.Home(), map[string]bool{n: true}, "setup") // setup tries again next time
			continue
		}
		switch {
		case res.NeedsYou:
			out.NeedsYou = append(out.NeedsYou, SetupComponentNote{Name: n, Title: c.Title, Says: res.Says, Command: e.StateOf(ctx, c).Command})
			s.say("· %s: %s", c.Title, res.Says)
			_ = choice.Set(e.Home(), map[string]bool{n: true}, "setup")
		case res.Already:
			out.Ready = append(out.Ready, n)
		default:
			for _, also := range res.Also {
				added[also] = true
				if ac, ok := components.Get(also); ok {
					out.Added = append(out.Added, components.ComponentAdded{Name: also, Title: ac.Title, Done: true, Unlocks: ac.Unlocks, Licence: ac.Licence,
						Says: ac.Title + " is added: it was needed first."})
					s.say("✓ %s is added.", ac.Title)
				}
			}
			out.Added = append(out.Added, *res)
			s.say("✓ %s", res.Says)
			if res.Licence != "" {
				s.say("  %s", res.Licence)
			}
		}
	}
	if s.pick.decided {
		left := map[string]bool{}
		for _, c := range offered {
			if slices.Contains(s.pick.names, c.Name) || added[c.Name] {
				continue
			}
			if f := e.Find(ctx, c); !f.Ready && f.NotHere == "" {
				left[c.Name] = false
				out.LeftOut = append(out.LeftOut, c.Name)
			}
		}
		if len(left) > 0 {
			_ = choice.Set(e.Home(), left, "setup")
		}
	}
	switch {
	case len(out.Failed) > 0:
		out.Says = fmt.Sprintf("%d of the components you chose couldn't be added; Settings → Components can try again.", len(out.Failed))
	case len(out.Added) > 0:
		out.Says = fmt.Sprintf("Added %d optional %s.", len(out.Added), map[bool]string{true: "component", false: "components"}[len(out.Added) == 1])
	case len(s.pick.names) == 0:
		out.Says = "No optional components were added; add any of them later in Settings → Components."
	default:
		out.Says = "The optional components you chose are on this computer."
	}
	if len(out.Failed) == 0 && len(out.NeedsYou) == 0 && len(todo) == 0 && len(s.pick.names) == 0 {
		s.say("· %s", out.Says)
	}
	return out
}
