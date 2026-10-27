package linkimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// toolState is this machine's record of downloader update checks
// (<home>/state/import-tools.json).
type toolState struct {
	LastCheck   *time.Time `json:"lastCheck,omitempty"`
	LastResult  string     `json:"lastResult,omitempty"`
	FromVersion string     `json:"fromVersion,omitempty"`
	ToVersion   string     `json:"toVersion,omitempty"`
	// Waiting is a newer yt-dlp the weekly update left for a person, because
	// Homebrew could also change Others (Python, Deno, …) to install it.
	Waiting string   `json:"waiting,omitempty"`
	Others  []string `json:"others,omitempty"`
}

func toolStatePath(a *app.App) string { return filepath.Join(a.Cfg.StateDir(), "import-tools.json") }

func loadToolState(a *app.App) (toolState, bool) {
	var s toolState
	b, err := os.ReadFile(toolStatePath(a))
	if err != nil || json.Unmarshal(b, &s) != nil {
		return s, false
	}
	return s, true
}

func saveToolState(a *app.App, s toolState) {
	b, _ := json.Marshal(s)
	_ = os.MkdirAll(a.Cfg.StateDir(), 0o700)
	tmp := toolStatePath(a) + ".tmp"
	if os.WriteFile(tmp, b, 0o600) == nil {
		_ = os.Rename(tmp, toolStatePath(a))
	}
}

// ImportToolsUpdate says what an update did.
type ImportToolsUpdate struct {
	Updated   bool   `json:"updated"`
	From      string `json:"from,omitempty"`
	To        string `json:"to,omitempty"`
	ManagedBy string `json:"managedBy"`
	// How is what MetaRush ran ("brew upgrade yt-dlp", "yt-dlp -U"), or what the person should run.
	How     string `json:"how"`
	Message string `json:"message" jsonschema:"description=The outcome in plain words."`
	Output  string `json:"output,omitempty" jsonschema:"description=The end of the updater's own output (Details)."`
	// Waiting is a newer yt-dlp the weekly (background) update left for a
	// person because Homebrew could also change Others to install it.
	Waiting string   `json:"waiting,omitempty" jsonschema:"description=A newer yt-dlp the weekly update left for a person (installing it could also update the Homebrew packages in others)."`
	Others  []string `json:"others,omitempty" jsonschema:"description=Other Homebrew packages (Python\\, Deno\\, …) a yt-dlp upgrade could install or update."`
}

// ErrSelfUpdateUnsupported means MetaRush can't update this installation of yt-dlp itself.
var ErrSelfUpdateUnsupported = errors.New("this yt-dlp can't be updated by MetaRush")

// brewEnv keeps Homebrew's output plain and leaves its cleanup to the person.
var brewEnv = []string{"HOMEBREW_NO_ENV_HINTS=1", "HOMEBREW_NO_INSTALL_CLEANUP=1"}

// Update brings yt-dlp up to date because a person asked (`metarush import
// --update-tools`, or Update now in Settings), the way it was installed: `brew
// upgrade yt-dlp` for Homebrew, which may also update what yt-dlp needs
// (Python, Deno), or `yt-dlp -U` for the official self-contained build. A pip
// install is left to the person (MetaRush doesn't run pip).
func Update(ctx context.Context, a *app.App) (*ImportToolsUpdate, error) {
	return update(ctx, a, false)
}

// BackgroundUpdate is the daemon's weekly update. It changes yt-dlp and nothing
// else: with Homebrew it upgrades yt-dlp only when nothing it relies on is
// outdated or missing (a Homebrew Python or Deno upgrade can break other tools
// on the machine); otherwise it leaves the newer yt-dlp waiting for a person,
// and doctor and Settings say so.
func BackgroundUpdate(ctx context.Context, a *app.App) (*ImportToolsUpdate, error) {
	return update(ctx, a, true)
}

func update(ctx context.Context, a *app.App, background bool) (*ImportToolsUpdate, error) {
	st := Probe(ctx, a)
	if !st.Installed {
		return nil, errors.New("MetaRush's downloader (yt-dlp) isn't installed on this computer")
	}
	res := &ImportToolsUpdate{From: st.Version, ManagedBy: st.ManagedBy}
	now := time.Now().UTC()
	var out *tools.Result
	var err error
	switch st.ManagedBy {
	case "homebrew":
		res.How = "brew upgrade yt-dlp"
		prefix := brewPrefix(st.Path)
		if !a.Tools.Has(tools.Brew) || (prefix != "" && !strings.HasPrefix(a.Tools.Path(tools.Brew), prefix+"/")) {
			res.Message = "The downloader came from Homebrew, but MetaRush couldn't find that Homebrew, so it can't update it. Update it with Homebrew yourself."
			return res, ErrSelfUpdateUnsupported
		}
		ctx2, cancel := context.WithTimeout(ctx, 10*time.Minute)
		defer cancel()
		if !background {
			out, err = a.Tools.Run(ctx2, tools.Spec{Tool: tools.Brew, Args: []string{"upgrade", "yt-dlp"}, Env: brewEnv})
			break
		}
		plan, perr := checkBrew(ctx2, a)
		switch {
		case perr != nil:
			res.Message = "MetaRush couldn't ask Homebrew whether a newer downloader is out. It asks again next week."
			saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: st.Version})
			return res, perr
		case !plan.Outdated:
			res.To, res.Message = st.Version, "The downloader is already up to date ("+st.Version+")."
		case plan.Pinned:
			res.To, res.Message = st.Version, "The downloader is pinned in Homebrew, so MetaRush leaves it at version "+st.Version+"."
		case !plan.Upgrade:
			res.To, res.Waiting, res.Others = st.Version, plan.Latest, plan.Others
			res.Message = waitingMessage(plan.Latest, plan.Others)
		}
		if res.Message != "" {
			saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: st.Version, ToVersion: res.To, Waiting: res.Waiting, Others: res.Others})
			return res, nil
		}
		// Only yt-dlp changes: nothing it relies on is outdated or missing, and
		// packages that rely on it are left alone.
		res.How = "brew upgrade --formula yt-dlp"
		out, err = a.Tools.Run(ctx2, tools.Spec{Tool: tools.Brew, Args: []string{"upgrade", "--formula", "yt-dlp"},
			Env: append(append([]string{}, brewEnv...), "HOMEBREW_NO_AUTO_UPDATE=1", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK=1")})
	case "self":
		res.How = "yt-dlp -U"
		ctx2, cancel := context.WithTimeout(ctx, 5*time.Minute)
		defer cancel()
		out, err = a.Tools.Run(ctx2, tools.Spec{Tool: tools.YtDlp, Args: []string{"--ignore-config", "-U"}})
		if err == nil {
			// MetaRush updated the program it downloaded: its checksum is
			// written down again (D326), so the new one runs.
			tools.Recorded(a.Tools.Path(tools.YtDlp))
		}
	case "pip":
		res.How = `python3 -m pip install -U "yt-dlp[default]"`
		res.Message = "The downloader was installed with pip, so MetaRush leaves updating it to you (or install it with Homebrew instead, and MetaRush keeps it up to date)."
		saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: st.Version})
		return res, ErrSelfUpdateUnsupported
	default:
		res.How = "brew install yt-dlp"
		res.Message = "MetaRush can't tell how the downloader was installed, so it can't update it. Update it the way you installed it, or install it with Homebrew instead."
		saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: st.Version})
		return res, ErrSelfUpdateUnsupported
	}
	if out != nil {
		res.Output = tailText(string(out.Stdout)+"\n"+string(out.Stderr), 1500)
	}
	a.Tools.ForgetVersion(tools.YtDlp)
	res.To = a.Tools.Version(context.WithoutCancel(ctx), tools.YtDlp)
	if err != nil {
		res.Message = "The update didn't work. Try again later; Details has what the updater said."
		if res.Output == "" {
			res.Output = tailText(err.Error(), 1500)
		}
		saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: res.From, ToVersion: res.To})
		return res, err
	}
	res.Updated = res.To != "" && res.To != res.From
	if res.Updated {
		res.Message = fmt.Sprintf("Updated the downloader from %s to %s.", res.From, res.To)
	} else {
		res.Message = "The downloader is already up to date (" + res.To + ")."
	}
	saveToolState(a, toolState{LastCheck: &now, LastResult: res.Message, FromVersion: res.From, ToVersion: res.To})
	a.Audit(ctx, "import.tools.update", "installation", a.Cfg.Installation.ID, "", map[string]any{"from": res.From, "to": res.To, "how": res.How, "updated": res.Updated})
	return res, nil
}

func tailText(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// brewOutdated is one formula in `brew outdated --json=v2`.
type brewOutdated struct {
	Name    string `json:"name"`
	Current string `json:"current_version"`
	Pinned  bool   `json:"pinned"`
}

// brewPlan is what the weekly update may do with a Homebrew yt-dlp.
type brewPlan struct {
	Outdated bool     // Homebrew has a newer yt-dlp
	Latest   string   // its version
	Pinned   bool     // the person pinned yt-dlp: left as it is
	Others   []string // what installing it could also change (outdated or missing dependencies)
	Upgrade  bool     // upgrading changes yt-dlp and nothing else
}

// planBrewUpgrade decides from Homebrew's answers: the weekly update upgrades
// yt-dlp only when every package it relies on (deps, recursively) is installed
// and current, so Homebrew has nothing else to change.
func planBrewUpgrade(outdated []brewOutdated, deps, installed []string) brewPlan {
	var p brewPlan
	old := map[string]bool{}
	for _, o := range outdated {
		old[o.Name] = true
		if o.Name == "yt-dlp" {
			p.Outdated, p.Latest, p.Pinned = true, o.Current, o.Pinned
		}
	}
	if !p.Outdated || p.Pinned {
		return p
	}
	have := map[string]bool{}
	for _, n := range installed {
		have[n] = true
	}
	for _, d := range deps {
		if d != "yt-dlp" && (old[d] || !have[d]) {
			p.Others = append(p.Others, d)
		}
	}
	p.Upgrade = len(p.Others) == 0
	return p
}

// checkBrew refreshes Homebrew's package list (`brew update` changes no
// installed package) and asks whether yt-dlp is outdated and what it relies on.
func checkBrew(ctx context.Context, a *app.App) (brewPlan, error) {
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.Brew, Args: []string{"update", "--quiet"}, Env: brewEnv}); err != nil {
		return brewPlan{}, fmt.Errorf("brew update: %w", err)
	}
	env := append(append([]string{}, brewEnv...), "HOMEBREW_NO_AUTO_UPDATE=1")
	run := func(args ...string) ([]byte, error) {
		res, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.Brew, Args: args, Env: env})
		if err != nil {
			return nil, fmt.Errorf("brew %s: %w", args[0], err)
		}
		return res.Stdout, nil
	}
	lines := func(b []byte) []string {
		var out []string
		for _, l := range strings.Split(string(b), "\n") {
			if l = strings.TrimSpace(l); l != "" && !strings.ContainsAny(l, " :") {
				out = append(out, l)
			}
		}
		return out
	}
	b, err := run("outdated", "--json=v2", "--formula")
	if err != nil {
		return brewPlan{}, err
	}
	var od struct {
		Formulae []brewOutdated `json:"formulae"`
	}
	if err := json.Unmarshal(b, &od); err != nil {
		return brewPlan{}, fmt.Errorf("brew outdated: %w", err)
	}
	deps, err := run("deps", "--formula", "yt-dlp")
	if err != nil {
		return brewPlan{}, err
	}
	installed, err := run("list", "--formula")
	if err != nil {
		return brewPlan{}, err
	}
	return planBrewUpgrade(od.Formulae, lines(deps), lines(installed)), nil
}

// waitingMessage says why a newer yt-dlp waits for the person.
func waitingMessage(latest string, others []string) string {
	names := others
	more := ""
	if len(names) > 3 {
		names, more = names[:3], fmt.Sprintf(" and %d more", len(others)-3)
	}
	return fmt.Sprintf("A newer downloader (yt-dlp %s) is ready, but installing it could also update %s%s from Homebrew, which MetaRush never does by itself. "+
		"Update it when it suits you with Update now in Settings > Link imports.", latest, strings.Join(names, ", "), more)
}

// AutoUpdateEvery is how often the daemon looks for a newer downloader.
const AutoUpdateEvery = 7 * 24 * time.Hour

// RunAutoUpdate keeps yt-dlp current while the daemon runs: about once a week,
// only while no import is running on this machine, unless imports.autoUpdate
// is false. It updates yt-dlp only (BackgroundUpdate); Python, Deno and other
// Homebrew packages change only when a person runs the update. Every run is in
// the activity log (program runs).
func RunAutoUpdate(ctx context.Context, a *app.App, logf func(string, ...any)) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(10 * time.Minute): // not while the daemon is starting up
	}
	t := time.NewTicker(time.Hour)
	defer t.Stop()
	for {
		if due(a) && idle(ctx, a) {
			res, err := BackgroundUpdate(ctx, a)
			switch {
			case err == nil && (res.Updated || res.Waiting != ""):
				logf("link imports: %s", res.Message)
			case err != nil && !errors.Is(err, ErrSelfUpdateUnsupported):
				logf("link imports: downloader update failed: %v", err)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func due(a *app.App) bool {
	if !a.Cfg.Imports.AutoUpdateTools() || !a.Tools.Has(tools.YtDlp) {
		return false
	}
	s, ok := loadToolState(a)
	return !ok || s.LastCheck == nil || time.Since(*s.LastCheck) > AutoUpdateEvery
}

func idle(ctx context.Context, a *app.App) bool {
	n, err := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeImport,
		"installation": a.Cfg.Installation.ID, "state": bson.M{"$in": bson.A{models.JobRunning, models.JobQueued}}})
	return err == nil && n == 0
}
