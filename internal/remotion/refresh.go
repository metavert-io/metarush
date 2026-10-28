package remotion

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
)

// Refreshing a source from its own steps (D173): the plan — every command,
// key, output and host, said before anything runs — and starting it, which
// is for people only. Running it is refresh_run.go (D174, D175).

// refreshSteps is where a refresh's keys go, in a private key's refusal.
const refreshSteps = "a refresh's steps"

// Plan limits.
const (
	maxRefreshSteps   = 12
	maxRefreshKeys    = 8
	maxRefreshOutputs = 40
	maxScanFileBytes  = 2 << 20
	maxScanBytes      = 24 << 20
)

// RemotionRefreshIn is a refresh as a person would start it. Without steps,
// the refresh steps last started are used (or none, with Fresh).
type RemotionRefreshIn struct {
	Source    string               `json:"source" jsonschema:"minLength=1,description=The source's short name (its newest version is refreshed)."`
	ProjectID string               `json:"projectId,omitempty"`
	Steps     []string             `json:"steps,omitempty" jsonschema:"maxItems=12,description=In order: npm run <script>\\, node <file> or npx --no <tool> with its arguments (default: the steps last started)."`
	Keys      []models.RemotionKey `json:"keys,omitempty" jsonschema:"maxItems=8,description=Keys from Settings → Integrations on this computer and the variable names the project reads them from (with steps given: only these)."`
	Outputs   []string             `json:"outputs,omitempty" jsonschema:"maxItems=40,description=Where the steps may write: folders (public/voice/)\\, files or patterns (public/**/*.wav). Default: public/ and the data files its code imports."`
	Fresh     bool                 `json:"fresh,omitempty" jsonschema:"description=Start from MetaRush's suggestions instead of the steps last started."`
}

// RemotionRefreshStartIn starts the plan a person looked at.
type RemotionRefreshStartIn struct {
	RemotionRefreshIn
	Plan string `json:"plan" jsonschema:"minLength=16,description=The plan's fingerprint (from remotion.refresh.plan): what starts is exactly what the person saw."`
}

// RemotionPlanStep is one step of a plan, with its words.
type RemotionPlanStep struct {
	N int `json:"n"`
	models.RemotionStep
	Words string `json:"words" jsonschema:"description=What it runs\\, in plain words."`
	// MakesVideos marks a script that renders: MetaRush makes clips itself.
	MakesVideos bool `json:"makesVideos,omitempty"`
}

// RemotionScriptRow is one script of the project's package.json.
type RemotionScriptRow struct {
	Name        string `json:"name"`
	Command     string `json:"command"`
	MakesVideos bool   `json:"makesVideos,omitempty"`
}

// RemotionPlanKey is a key the plan passes.
type RemotionPlanKey struct {
	models.RemotionKey
	Title      string `json:"title"`
	Masked     string `json:"masked,omitempty"`
	Configured bool   `json:"configured" jsonschema:"description=This computer has the key (Settings → Integrations)."`
	Read       bool   `json:"read" jsonschema:"description=The project's code reads that variable."`
}

// RemotionPlanIntegration is a provider whose key a refresh could pass.
type RemotionPlanIntegration struct {
	Name       string   `json:"name"`
	Title      string   `json:"title"`
	Configured bool     `json:"configured"`
	Masked     string   `json:"masked,omitempty"`
	Env        string   `json:"env" jsonschema:"description=The variable name MetaRush suggests (what its code reads\\, else the usual name)."`
	Read       bool     `json:"read" jsonschema:"description=Its code reads that variable."`
	Hosts      []string `json:"hosts"`
}

// RemotionEnvRead is a variable the project's code reads.
type RemotionEnvRead struct {
	Name  string   `json:"name"`
	Files []string `json:"files"`
	// Set: the plan passes it (a key); else the code keeps its own default.
	Set bool `json:"set"`
}

// RemotionRefreshRow is one refresh as lists show it.
type RemotionRefreshRow struct {
	ID            string                       `json:"id"`
	Number        int                          `json:"number"`
	State         string                       `json:"state" jsonschema:"enum=queued,enum=running,enum=done,enum=failed,enum=canceled"`
	StateLabel    string                       `json:"stateLabel"`
	Steps         []string                     `json:"steps"`
	StepRuns      []models.RemotionStepRun     `json:"stepRuns"`
	Keys          []models.RemotionKey         `json:"keys"`
	Outputs       []string                     `json:"outputs"`
	FromNumber    int                          `json:"fromNumber"`
	VersionNumber int                          `json:"versionNumber,omitempty"`
	Version       string                       `json:"version,omitempty"`
	Changes       []models.RemotionFileChange  `json:"changes"`
	LeftOut       []models.RemotionFileLeftOut `json:"leftOut"`
	Summary       string                       `json:"summary,omitempty"`
	Error         string                       `json:"error,omitempty"`
	By            models.Actor                 `json:"by"`
	Machine       string                       `json:"machine"`
	Isolation     string                       `json:"isolation,omitempty"`
	JobID         string                       `json:"jobId,omitempty"`
	HasLog        bool                         `json:"hasLog"`
	CreatedAt     time.Time                    `json:"createdAt"`
	FinishedAt    *time.Time                   `json:"finishedAt,omitempty"`
	URL           string                       `json:"url,omitempty" jsonschema:"description=The review of the version it made."`
}

// RemotionRefreshPlanOut is everything a person sees before a refresh starts.
type RemotionRefreshPlanOut struct {
	Name          string                      `json:"name"`
	Title         string                      `json:"title"`
	SourceID      string                      `json:"sourceId"`
	ProjectID     string                      `json:"projectId"`
	Version       string                      `json:"version" jsonschema:"description=The version it starts from (the newest)."`
	VersionNumber int                         `json:"versionNumber"`
	Steps         []RemotionPlanStep          `json:"steps"`
	Scripts       []RemotionScriptRow         `json:"scripts" jsonschema:"description=Every script of its package.json (steps are picked from these)."`
	Keys          []RemotionPlanKey           `json:"keys"`
	Integrations  []RemotionPlanIntegration   `json:"integrations"`
	Reads         []RemotionEnvRead           `json:"reads" jsonschema:"description=Variables its code reads."`
	Outputs       []string                    `json:"outputs"`
	Hosts         []models.RemotionHost       `json:"hosts"`
	Machine       string                      `json:"machine" jsonschema:"description=The computer it runs on (its keys)."`
	Isolation     string                      `json:"isolation,omitempty" jsonschema:"enum=,enum=macos-sandbox,enum=bubblewrap"`
	Apart         string                      `json:"apart" jsonschema:"description=How its steps are kept away from MetaRush's files and keys\\, in plain words."`
	Never         []string                    `json:"never"`
	Problems      []string                    `json:"problems" jsonschema:"description=What stops it from starting\\, in plain words."`
	Ready         bool                        `json:"ready"`
	Fingerprint   string                      `json:"fingerprint"`
	RefreshSteps  models.RemotionRefreshSteps `json:"refreshSteps"`
	Remembered    bool                        `json:"remembered" jsonschema:"description=The steps are the ones last started."`
	Running       *RemotionRefreshRow         `json:"running,omitempty" jsonschema:"description=A refresh of it that hasn't finished."`
	Message       string                      `json:"message"`
	Next          string                      `json:"next"`
}

// RemotionRefreshStartOut says what started.
type RemotionRefreshStartOut struct {
	Message string `json:"message"`
	ID      string `json:"id"`
	Number  int    `json:"number"`
	JobID   string `json:"jobId"`
	Status  string `json:"status" jsonschema:"enum=queued"`
}

// RemotionRefreshRunsIn lists a source's refreshes.
type RemotionRefreshRunsIn struct {
	Source    string `json:"source" jsonschema:"minLength=1"`
	ProjectID string `json:"projectId,omitempty"`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=100"`
}

// RemotionRefreshRunsOut lists refreshes, newest first.
type RemotionRefreshRunsOut struct {
	Name      string               `json:"name"`
	Title     string               `json:"title"`
	Refreshes []RemotionRefreshRow `json:"refreshes"`
}

// RemotionRefreshLogIn picks a refresh's log.
type RemotionRefreshLogIn struct {
	Source    string `json:"source" jsonschema:"minLength=1"`
	ProjectID string `json:"projectId,omitempty"`
	Number    int    `json:"number,omitempty" jsonschema:"minimum=1,description=Which refresh (default: the newest)."`
}

// RemotionRefreshLogOut is what a refresh's steps printed (keys hidden).
type RemotionRefreshLogOut struct {
	Number int    `json:"number"`
	State  string `json:"state"`
	Text   string `json:"text"`
}

func init() {
	ops.Def("remotion.refresh.plan", "Show what a refresh of a Remotion source would do before anything runs: its own steps (npm scripts, its files, its tools) with their commands, the keys from Settings → Integrations passed and the names they get, where the steps may write, the hosts they may reach and how they are kept away from MetaRush's files", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionRefreshIn) (*RemotionRefreshPlanOut, error) {
			s, v, err := refreshTarget(ctx, a, in.ProjectID, in.Source)
			if err != nil {
				return nil, err
			}
			return planRefresh(ctx, a, s, v, in)
		})
	ops.Def("remotion.refresh.start", "Start a refresh a person looked at (people only): the source's own steps run on this computer, outside the render jail with the network on, in a separate environment that gets only the keys picked; what they write becomes a new version that waits for a person", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *RemotionRefreshStartIn) (*RemotionRefreshStartOut, error) {
			return startRefresh(ctx, a, in)
		})
	ops.Def("remotion.refresh.runs", "List the refreshes of a Remotion source: their steps, how each went, what they changed and left out, and the version they made", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionRefreshRunsIn) (*RemotionRefreshRunsOut, error) {
			s, err := refreshSource(ctx, a, in.ProjectID, in.Source)
			if err != nil {
				return nil, err
			}
			limit := in.Limit
			if limit <= 0 {
				limit = 20
			}
			rs, err := refreshesOf(ctx, a, s.ID, int64(limit))
			if err != nil {
				return nil, err
			}
			out := &RemotionRefreshRunsOut{Name: s.Name, Title: s.Title, Refreshes: []RemotionRefreshRow{}}
			for i := range rs {
				out.Refreshes = append(out.Refreshes, refreshRow(&rs[i]))
			}
			return out, nil
		})
	ops.Def("remotion.refresh.log", "Read what a refresh's steps printed (keys and other secrets hidden)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RemotionRefreshLogIn) (*RemotionRefreshLogOut, error) {
			return refreshLogOp(ctx, a, in)
		})
}

// refreshSource resolves the source of a refresh (by name, in a project).
func refreshSource(ctx context.Context, a *app.App, projectRef, ref string) (*models.RemotionSource, error) {
	pid, err := projectIDOf(ctx, a, projectRef)
	if err != nil {
		return nil, err
	}
	name, _ := splitRef(ref)
	return findSource(ctx, a, pid, name)
}

// refreshTarget is the source and the version a refresh starts from (its newest).
func refreshTarget(ctx context.Context, a *app.App, projectRef, ref string) (*models.RemotionSource, *models.RemotionVersion, error) {
	s, err := refreshSource(ctx, a, projectRef, ref)
	if err != nil {
		return nil, nil, err
	}
	if s.Head == "" {
		return nil, nil, apperr.New(apperr.Validation, "“%s” is still being brought in.", s.Title)
	}
	v, err := getVersion(ctx, a, s.Head)
	if err != nil {
		return nil, nil, err
	}
	return s, v, nil
}

// ---- reading what the project has ----

// projectFacts are what a plan reads from a version's files: its scripts,
// the tools its packages have, the variables and hosts its code names.
type projectFacts struct {
	scripts map[string]string
	order   []string // script names as package.json lists them
	files   map[string]bool
	bins    map[string]bool
	reads   map[string][]string // variable → files
	hosts   map[string][]string // host → files
	data    []string            // JSON data files its code imports
}

var (
	envDot      = regexp.MustCompile(`\bprocess\.env\.([A-Za-z_][A-Za-z0-9_]*)`)
	envIndex    = regexp.MustCompile(`\bprocess\.env\[\s*["'\x60]([A-Za-z_][A-Za-z0-9_]*)["'\x60]\s*\]`)
	envDestruct = regexp.MustCompile(`\{([^{}]{1,600})\}\s*=\s*process\.env\b`)
	envMeta     = regexp.MustCompile(`\bimport\.meta\.env\.([A-Za-z_][A-Za-z0-9_]*)`)
	envPython   = regexp.MustCompile(`\bos\.(?:environ\.get|getenv|environ\[)\(?\s*["']([A-Za-z_][A-Za-z0-9_]*)["']`)
	envShell    = regexp.MustCompile(`\$\{?([A-Z_][A-Z0-9_]{2,})\}?`)
	urlHost     = regexp.MustCompile(`\bhttps?://([A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?(?:\.[A-Za-z0-9](?:[A-Za-z0-9-]*[A-Za-z0-9])?)*)`)
)

// namespaceHosts are addresses code names without ever reaching them (XML
// namespaces, schemas, licenses).
var namespaceHosts = map[string]bool{"w3.org": true, "www.w3.org": true, "schema.org": true, "json-schema.org": true, "www.apache.org": true, "opensource.org": true, "spdx.org": true}

// scanExts are the files a plan reads for variables and hosts.
var scanExts = map[string]bool{".js": true, ".mjs": true, ".cjs": true, ".ts": true, ".tsx": true, ".jsx": true, ".mts": true, ".cts": true, ".py": true, ".sh": true}

// readFacts reads a version's package.json, lockfile and code (as text: nothing runs).
func readFacts(ctx context.Context, a *app.App, m *Manifest) (*projectFacts, error) {
	f := &projectFacts{scripts: map[string]string{}, files: map[string]bool{}, bins: map[string]bool{}, reads: map[string][]string{}, hosts: map[string][]string{}}
	total := 0
	for _, mf := range m.Files {
		f.files[mf.Path] = true
	}
	read := func(p string) []byte {
		mf, ok := m.File(p)
		if !ok || mf.Size > maxScanFileBytes || total+int(mf.Size) > maxScanBytes {
			return nil
		}
		b, err := readObject(ctx, a, mf.SHA256, mf.Size)
		if err != nil {
			return nil
		}
		total += len(b)
		return b
	}
	if b := read("package.json"); b != nil {
		var pj struct {
			Scripts json.RawMessage `json:"scripts"`
		}
		if json.Unmarshal(b, &pj) == nil && len(pj.Scripts) > 0 {
			_ = json.Unmarshal(pj.Scripts, &f.scripts)
			f.order = orderedKeys(pj.Scripts)
		}
		for _, line := range f.scripts {
			for _, mm := range envShell.FindAllStringSubmatch(line, -1) {
				addFile(f.reads, mm[1], "package.json")
			}
			for _, mm := range urlHost.FindAllStringSubmatch(line, -1) {
				addFile(f.hosts, strings.ToLower(mm[1]), "package.json")
			}
		}
	}
	if b := read("package-lock.json"); b != nil {
		var lk struct {
			Packages map[string]struct {
				Bin json.RawMessage `json:"bin"`
			} `json:"packages"`
		}
		if json.Unmarshal(b, &lk) == nil {
			for key, p := range lk.Packages {
				if !strings.HasPrefix(key, "node_modules/") || strings.Contains(strings.TrimPrefix(key, "node_modules/"), "/node_modules/") {
					continue
				}
				var bins map[string]string
				if json.Unmarshal(p.Bin, &bins) == nil {
					for b := range bins {
						f.bins[b] = true
					}
				}
			}
		}
	}
	for _, mf := range m.Files {
		ext := strings.ToLower(path.Ext(mf.Path))
		if isPublic(mf.Path) || !scanExts[ext] {
			continue
		}
		b := read(mf.Path)
		if b == nil {
			continue
		}
		text := string(b)
		for _, re := range []*regexp.Regexp{envDot, envIndex, envMeta, envPython} {
			for _, mm := range re.FindAllStringSubmatch(text, -1) {
				addFile(f.reads, mm[1], mf.Path)
			}
		}
		for _, mm := range envDestruct.FindAllStringSubmatch(text, -1) {
			for _, part := range strings.Split(mm[1], ",") {
				name := strings.TrimSpace(part)
				if i := strings.IndexAny(name, ":="); i >= 0 {
					name = strings.TrimSpace(name[:i])
				}
				if regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`).MatchString(name) {
					addFile(f.reads, name, mf.Path)
				}
			}
		}
		for _, mm := range urlHost.FindAllStringSubmatch(text, -1) {
			h := strings.ToLower(mm[1])
			if namespaceHosts[h] || !strings.Contains(h, ".") && h != "localhost" {
				continue
			}
			addFile(f.hosts, h, mf.Path)
		}
	}
	// Data files its code imports (src/generated/voiceover.json): outputs by default.
	return f, nil
}

func addFile(m map[string][]string, key, file string) {
	for _, f := range m[key] {
		if f == file {
			return
		}
	}
	if len(m[key]) < 3 {
		m[key] = append(m[key], file)
	} else if _, ok := m[key]; !ok {
		m[key] = []string{file}
	}
}

// orderedKeys lists an object's keys in the order they are written.
func orderedKeys(raw json.RawMessage) []string {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	var out []string
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return out
		}
		k, _ := t.(string)
		out = append(out, k)
		var skip json.RawMessage
		if dec.Decode(&skip) != nil {
			return out
		}
	}
	return out
}

// makesVideos marks a script that renders (MetaRush makes clips itself).
func makesVideos(name, command string) bool {
	low := strings.ToLower(command)
	return strings.Contains(strings.ToLower(name), "render") || strings.Contains(low, "remotion render") || strings.Contains(low, "remotion still") ||
		strings.Contains(low, "rendermedia") || strings.Contains(low, "render-all")
}

// ---- steps ----

// shellWords splits a step into words the way people type them: spaces
// separate, quotes group (nothing is expanded; no shell ever runs it).
func shellWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in := false
	var quote rune
	esc := false
	for _, r := range s {
		switch {
		case esc:
			cur.WriteRune(r)
			esc = false
		case r == '\\' && quote != '\'':
			esc = true
			in = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				cur.WriteRune(r)
			}
		case r == '"' || r == '\'':
			quote = r
			in = true
		case r == ' ' || r == '\t':
			if in {
				out = append(out, cur.String())
				cur.Reset()
				in = false
			}
		case r == '\n' || r == '\r':
			return nil, fmt.Errorf("a step is one line")
		default:
			cur.WriteRune(r)
			in = true
		}
	}
	if quote != 0 || esc {
		return nil, fmt.Errorf("a quote isn't closed")
	}
	if in {
		out = append(out, cur.String())
	}
	return out, nil
}

// parseStep reads one step against what the project has.
func parseStep(run string, f *projectFacts) (models.RemotionStep, error) {
	words, err := shellWords(strings.TrimSpace(run))
	if err != nil {
		return models.RemotionStep{}, fmt.Errorf("“%s” can't be read: %s", clipText(run, 80), err)
	}
	st := models.RemotionStep{Run: strings.Join(words, " ")}
	bad := fmt.Errorf("“%s” isn't a step MetaRush runs: a step is npm run <script>, node <file> or npx --no <tool>, with its arguments", clipText(run, 80))
	if len(words) < 2 {
		return st, bad
	}
	for _, w := range words {
		if len(w) > 500 || strings.ContainsRune(w, 0) {
			return st, fmt.Errorf("“%s” has an argument MetaRush can't pass", clipText(run, 80))
		}
	}
	switch words[0] {
	case "npm":
		if len(words) < 3 || (words[1] != "run" && words[1] != "run-script") {
			return st, bad
		}
		name := words[2]
		cmd, ok := f.scripts[name]
		if !ok {
			return st, fmt.Errorf("its package.json has no script called “%s”", clipText(name, 60))
		}
		args := words[3:]
		if len(args) > 0 {
			if args[0] != "--" {
				return st, fmt.Errorf("arguments for the script “%s” go after --: npm run %s -- …", name, name)
			}
			args = args[1:]
		}
		st.Kind, st.Name, st.Args, st.Command = models.RemotionStepScript, name, args, cmd
		st.Run = strings.Join(append([]string{"npm", "run", name}, withDashes(args)...), " ")
	case "node":
		file := strings.TrimPrefix(words[1], "./")
		if strings.HasPrefix(file, "-") {
			return st, fmt.Errorf("MetaRush runs node with one of the project's files, never with options (%s)", clipText(words[1], 40))
		}
		if !safeRel(file) || !f.files[file] {
			return st, fmt.Errorf("the project has no file %s", clipText(file, 80))
		}
		ext := strings.ToLower(path.Ext(file))
		if ext != ".js" && ext != ".mjs" && ext != ".cjs" {
			return st, fmt.Errorf("node runs JavaScript files (.js, .mjs, .cjs); %s isn't one (a tool such as tsx runs it: npx --no tsx %s)", file, file)
		}
		st.Kind, st.Name, st.Args = models.RemotionStepNode, file, words[2:]
		st.Run = strings.Join(append([]string{"node", file}, words[2:]...), " ")
	case "npx":
		rest := words[1:]
		if len(rest) > 0 && (rest[0] == "--no" || rest[0] == "--no-install") {
			rest = rest[1:]
		}
		if len(rest) == 0 || strings.HasPrefix(rest[0], "-") {
			return st, fmt.Errorf("npx runs a tool the project's packages have: npx --no <tool> …")
		}
		tool := rest[0]
		if !f.bins[tool] {
			return st, fmt.Errorf("the project's packages have no tool called “%s” (MetaRush never downloads one for a refresh)", clipText(tool, 60))
		}
		st.Kind, st.Name, st.Args = models.RemotionStepNPX, tool, rest[1:]
		st.Run = strings.Join(append([]string{"npx", "--no", tool}, rest[1:]...), " ")
	default:
		return st, bad
	}
	return st, nil
}

func withDashes(args []string) []string {
	if len(args) == 0 {
		return nil
	}
	return append([]string{"--"}, args...)
}

// stepWords says what a step runs.
func stepWords(st models.RemotionStep) string {
	switch st.Kind {
	case models.RemotionStepScript:
		return fmt.Sprintf("Runs its script “%s”: %s", st.Name, clipText(st.Command, 300))
	case models.RemotionStepNode:
		return "Runs its file " + st.Name + " with Node.js"
	case models.RemotionStepNPX:
		return "Runs “" + st.Name + "”, a tool its packages have"
	}
	return st.Run
}

// ---- keys ----

// refusedEnv are variables that steer programs: a key never gets one of these names.
var refusedEnv = map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true, "TMPDIR": true, "TMP": true, "TEMP": true,
	"PWD": true, "OLDPWD": true, "LANG": true, "TERM": true, "CI": true, "ENV": true, "BASH_ENV": true, "ZDOTDIR": true, "IFS": true, "PS4": true,
	"JAVA_TOOL_OPTIONS": true, "_JAVA_OPTIONS": true, "HTTP_PROXY": true, "HTTPS_PROXY": true, "ALL_PROXY": true, "NO_PROXY": true, "FORCE_COLOR": true, "NO_COLOR": true}

var refusedEnvPrefixes = []string{"NODE_", "NPM_", "DYLD_", "LD_", "METARUSH_", "GIT_", "SSH_", "XDG_", "PLAYWRIGHT_", "BASH_", "PYTHON", "PERL", "RUBY", "LC_", "GCONV_", "MALLOC"}

var envNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]{1,63}$`)

// checkEnvName says why a key can't go under a name ("" = it can).
func checkEnvName(name string) string {
	switch {
	case !envNamePattern.MatchString(name):
		return fmt.Sprintf("“%s” can't be a variable name for a key: use capital letters, digits and _ (like ELEVENLABS_API_KEY)", clipText(name, 40))
	case refusedEnv[name]:
		return fmt.Sprintf("%s steers how programs run, so a key never goes there", name)
	}
	for _, p := range refusedEnvPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Sprintf("%s steers how programs run, so a key never goes there", name)
		}
	}
	return ""
}

// keyHints are the variable names a provider's key usually goes under.
var keyHints = map[string][]string{providers.ElevenLabsName: {"ELEVENLABS", "ELEVEN_LABS", "XI_API_KEY"}, "gemini": {"GEMINI", "GOOGLE_API_KEY", "GOOGLE_GENAI", "GENAI"}}

// keyDefault is the usual name of a provider's key.
func keyDefault(name string) string {
	return strings.ToUpper(strings.ReplaceAll(name, "-", "_")) + "_API_KEY"
}

// keyHosts are the places a provider's key is for.
func keyHosts(ctx context.Context, a *app.App, name string) []string {
	switch name {
	case providers.ElevenLabsName:
		ws, _, _, _ := providers.LoadSettings(ctx, a, providers.ElevenLabsName, "")
		region := ""
		if ws != nil {
			region = ws.Region
		}
		if base, err := providers.ElevenLabsBaseURL(region); err == nil {
			if u, err := url.Parse(base); err == nil && u.Hostname() != "" {
				return []string{u.Hostname()}
			}
		}
		return []string{"api.elevenlabs.io"}
	case "gemini":
		return []string{"generativelanguage.googleapis.com"}
	}
	return nil
}

// suggestEnv is the variable a provider's key should go under for this project.
func suggestEnv(name string, reads map[string][]string) (string, bool) {
	var hits []string
	for v := range reads {
		for _, h := range keyHints[name] {
			if strings.Contains(v, h) && checkEnvName(v) == "" {
				hits = append(hits, v)
				break
			}
		}
	}
	sort.Strings(hits)
	for _, v := range hits {
		if strings.HasSuffix(v, "_KEY") {
			return v, true
		}
	}
	if len(hits) > 0 {
		return hits[0], true
	}
	return keyDefault(name), false
}

// ---- outputs ----

// outputMatcher reads where a refresh may write: folders (public/voice/ or
// public/voice), files, or patterns with * (in one folder) and ** (any depth).
type outputMatcher struct{ res []*regexp.Regexp }

func newOutputMatcher(patterns []string) (*outputMatcher, error) {
	m := &outputMatcher{}
	for _, p := range patterns {
		p = strings.TrimSpace(strings.TrimPrefix(p, "./"))
		if p == "" {
			continue
		}
		clean := strings.TrimSuffix(p, "/")
		if clean == "" || strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || strings.Contains(p, "..") || strings.ContainsRune(p, 0) || len(p) > 300 {
			return nil, fmt.Errorf("“%s” isn't a place in the project a refresh can write", clipText(p, 60))
		}
		first := strings.ToLower(strings.Split(clean, "/")[0])
		if first == "node_modules" || first == ".git" || first == ".metarush" || first == "out" || strings.HasPrefix(first, "*") {
			return nil, fmt.Errorf("a refresh can't write %s: name a folder of the project such as public/ or src/generated/", clipText(p, 60))
		}
		var re strings.Builder
		re.WriteString("^")
		glob := strings.ContainsAny(clean, "*?")
		for i := 0; i < len(clean); i++ {
			c := clean[i]
			switch {
			case c == '*' && i+1 < len(clean) && clean[i+1] == '*':
				re.WriteString(".*")
				i++
				if i+1 < len(clean) && clean[i+1] == '/' {
					i++
					re.WriteString("/?")
				}
			case c == '*':
				re.WriteString("[^/]*")
			case c == '?':
				re.WriteString("[^/]")
			default:
				re.WriteString(regexp.QuoteMeta(string(c)))
			}
		}
		if glob {
			re.WriteString("$")
		} else {
			re.WriteString("(?:/.*)?$") // a file, or a folder and everything in it
		}
		rx, err := regexp.Compile(re.String())
		if err != nil {
			return nil, fmt.Errorf("“%s” isn't a pattern MetaRush can read", clipText(p, 60))
		}
		m.res = append(m.res, rx)
	}
	return m, nil
}

func (m *outputMatcher) match(rel string) bool {
	for _, re := range m.res {
		if re.MatchString(rel) {
			return true
		}
	}
	return false
}

// defaultOutputs are where a refresh writes unless the person says: public/
// and the JSON data files its code imports.
func defaultOutputs(v *models.RemotionVersion) []string {
	out := []string{"public/"}
	seen := map[string]bool{}
	for _, im := range v.Static.Imports {
		for _, f := range im.Files {
			if strings.HasSuffix(strings.ToLower(f), ".json") && !isPublic(f) && !seen[f] && f != "package.json" && f != "tsconfig.json" {
				seen[f] = true
				out = append(out, f)
			}
		}
	}
	sort.Strings(out[1:])
	return out
}

// ---- the plan ----

// planRefresh builds the plan for a source's newest version.
func planRefresh(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, in *RemotionRefreshIn) (*RemotionRefreshPlanOut, error) {
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return nil, err
	}
	f, err := readFacts(ctx, a, m)
	if err != nil {
		return nil, err
	}
	out := &RemotionRefreshPlanOut{Name: s.Name, Title: s.Title, SourceID: s.ID, ProjectID: s.ProjectID, Version: v.Hash, VersionNumber: v.Number,
		Steps: []RemotionPlanStep{}, Scripts: []RemotionScriptRow{}, Keys: []RemotionPlanKey{}, Integrations: []RemotionPlanIntegration{}, Reads: []RemotionEnvRead{},
		Outputs: []string{}, Hosts: []models.RemotionHost{}, Never: []string{}, Problems: []string{}, Machine: a.Cfg.Installation.Name}
	for _, name := range f.order {
		if cmd, ok := f.scripts[name]; ok {
			out.Scripts = append(out.Scripts, RemotionScriptRow{Name: name, Command: cmd, MakesVideos: makesVideos(name, cmd)})
		}
	}
	// The refresh steps: what was asked, else the ones last started.
	var picked models.RemotionRefreshSteps
	steps, keys, outputs := in.Steps, in.Keys, in.Outputs
	if len(steps) == 0 && !in.Fresh && s.Refresh != nil {
		out.Remembered = true
		for _, st := range s.Refresh.Steps {
			steps = append(steps, st.Run)
		}
		if len(keys) == 0 {
			keys = s.Refresh.Keys
		}
		if len(outputs) == 0 {
			outputs = s.Refresh.Outputs
		}
	}
	if len(steps) > maxRefreshSteps {
		out.Problems = append(out.Problems, fmt.Sprintf("A refresh runs at most %d steps.", maxRefreshSteps))
		steps = steps[:maxRefreshSteps]
	}
	for _, run := range steps {
		st, err := parseStep(run, f)
		if err != nil {
			out.Problems = append(out.Problems, upperFirst(err.Error())+".")
			continue
		}
		picked.Steps = append(picked.Steps, st)
		out.Steps = append(out.Steps, RemotionPlanStep{N: len(out.Steps) + 1, RemotionStep: st, Words: stepWords(st), MakesVideos: st.Kind == models.RemotionStepScript && makesVideos(st.Name, st.Command)})
	}
	if len(steps) == 0 {
		out.Problems = append(out.Problems, "Pick at least one of its steps to run.")
	}
	// Keys: only the ones picked, under the names given.
	set := map[string]bool{}
	if len(keys) > maxRefreshKeys {
		out.Problems = append(out.Problems, fmt.Sprintf("A refresh passes at most %d keys.", maxRefreshKeys))
		keys = keys[:maxRefreshKeys]
	}
	for _, k := range keys {
		p, err := providers.Get(k.Provider)
		if err == nil {
			err = providers.NotLent(p, refreshSteps) // a service account's private key (D282)
		}
		if err != nil {
			out.Problems = append(out.Problems, apperr.From(err).Message)
			continue
		}
		k.Provider = p.Name()
		k.Env = strings.TrimSpace(k.Env)
		if k.Env == "" {
			k.Env, _ = suggestEnv(p.Name(), f.reads)
		}
		if why := checkEnvName(k.Env); why != "" {
			out.Problems = append(out.Problems, upperFirst(why)+".")
			continue
		}
		if set[k.Env] {
			out.Problems = append(out.Problems, fmt.Sprintf("Two keys can't both go under %s.", k.Env))
			continue
		}
		set[k.Env] = true
		st := providers.StatusFor(a.Cfg, p)
		row := RemotionPlanKey{RemotionKey: k, Title: p.Title(), Masked: st.Masked, Configured: st.Configured, Read: len(f.reads[k.Env]) > 0}
		if !st.Configured {
			out.Problems = append(out.Problems, fmt.Sprintf("%s has no %s key yet: add it in Settings → Integrations on %s first.", a.Cfg.Installation.Name, p.Title(), a.Cfg.Installation.Name))
		}
		out.Keys = append(out.Keys, row)
		picked.Keys = append(picked.Keys, k)
	}
	for _, p := range providers.All() {
		st := providers.StatusFor(a.Cfg, p)
		env, read := suggestEnv(p.Name(), f.reads)
		out.Integrations = append(out.Integrations, RemotionPlanIntegration{Name: p.Name(), Title: p.Title(), Configured: st.Configured, Masked: st.Masked, Env: env, Read: read, Hosts: keyHosts(ctx, a, p.Name())})
	}
	names := make([]string, 0, len(f.reads))
	for n := range f.reads {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		out.Reads = append(out.Reads, RemotionEnvRead{Name: n, Files: f.reads[n], Set: set[n]})
	}
	// Outputs.
	if len(outputs) == 0 {
		outputs = defaultOutputs(v)
	}
	if len(outputs) > maxRefreshOutputs {
		out.Problems = append(out.Problems, fmt.Sprintf("A refresh writes in at most %d places.", maxRefreshOutputs))
		outputs = outputs[:maxRefreshOutputs]
	}
	var cleanOut []string
	for _, o := range outputs {
		o = strings.TrimSpace(strings.TrimPrefix(o, "./"))
		if o == "" {
			continue
		}
		if _, err := newOutputMatcher([]string{o}); err != nil {
			out.Problems = append(out.Problems, upperFirst(err.Error())+".")
			continue
		}
		cleanOut = append(cleanOut, o)
	}
	if len(cleanOut) == 0 {
		out.Problems = append(out.Problems, "Say where its steps may write (a folder such as public/).")
	}
	// What may leave (D290): its steps run with the network on, so the
	// source's words and pictures can go wherever they reach.
	if r := leave.KeptRefusal(ctx, a, leave.Refresh(s.ProjectID, "a refresh of “"+s.Title+"”")); r != nil {
		out.Problems = append(out.Problems, r.Message)
	}
	picked.Outputs = cleanOut
	out.Outputs = append(out.Outputs, cleanOut...)
	// Hosts: the ones its code names, and the services of the keys passed.
	hostWhy := map[string]string{}
	for h, files := range f.hosts {
		why := "named in " + joinAnd(files)
		if h == "localhost" || h == "127.0.0.1" {
			why = "this computer, named in " + joinAnd(files)
		}
		hostWhy[h] = why
	}
	for _, k := range out.Keys {
		for _, h := range keyHosts(ctx, a, k.Provider) {
			hostWhy[h] = "for the " + k.Title + " key"
		}
	}
	hosts := make([]string, 0, len(hostWhy))
	for h := range hostWhy {
		hosts = append(hosts, h)
	}
	sort.Strings(hosts)
	if len(hosts) > 30 {
		hosts = hosts[:30]
	}
	for _, h := range hosts {
		out.Hosts = append(out.Hosts, models.RemotionHost{Host: h, Why: hostWhy[h]})
	}
	// Where it runs, and how it is kept apart.
	iso := isolationHere(a)
	out.Isolation, out.Apart = iso.Kind, iso.Words
	if iso.Kind == "" {
		out.Problems = append(out.Problems, iso.Problem)
	}
	if !v.PackagesAllowed {
		out.Problems = append(out.Problems, fmt.Sprintf("Version %d changes the project's packages and nobody has allowed them yet; a person allows them first.", v.Number))
	}
	if ops.CallerFrom(ctx).Elsewhere {
		out.Never = append(out.Never, "It runs on "+a.Cfg.Installation.Name+", not on the computer you are using.")
	}
	out.Never = append(out.Never,
		"MetaRush's settings, keys and files stay out of its reach, and only the keys listed here are passed.",
		"Its .env file is never read (MetaRush never copied it).",
		"It runs with the network on: the hosts listed are the ones its code names, and MetaRush doesn't stop it from reaching others.",
		"Nothing goes into a cut: what it writes becomes a new version that waits for a person to approve it.")
	if r, err := activeRefresh(ctx, a, s.ID); err == nil && r != nil {
		row := refreshRow(r)
		out.Running = &row
		out.Problems = append(out.Problems, fmt.Sprintf("Refresh %d of “%s” hasn't finished yet.", r.Number, s.Title))
	}
	out.RefreshSteps = picked
	if out.RefreshSteps.Steps == nil {
		out.RefreshSteps.Steps = []models.RemotionStep{}
	}
	if out.RefreshSteps.Keys == nil {
		out.RefreshSteps.Keys = []models.RemotionKey{}
	}
	if out.RefreshSteps.Outputs == nil {
		out.RefreshSteps.Outputs = []string{}
	}
	out.Ready = len(out.Problems) == 0
	out.Fingerprint = planFingerprint(a, s, v, picked, out.Keys, out.Hosts, iso.Kind)
	out.Message, out.Next = planMessage(s, v, out)
	return out, nil
}

// planMessage says the plan in a sentence, and what to do next.
func planMessage(s *models.RemotionSource, v *models.RemotionVersion, p *RemotionRefreshPlanOut) (string, string) {
	if len(p.Steps) == 0 {
		return fmt.Sprintf("Refreshing “%s” runs steps from its own package.json; none are picked yet.", s.Title), "Pick its steps (and the keys they need), look at the plan, then start it."
	}
	var runs []string
	for _, st := range p.Steps {
		runs = append(runs, st.Run)
	}
	msg := fmt.Sprintf("Refreshing “%s” from version %d runs %s on %s", s.Title, v.Number, joinAnd(runs), p.Machine)
	if len(p.Keys) > 0 {
		var ks []string
		for _, k := range p.Keys {
			ks = append(ks, "the "+k.Title+" key as "+k.Env)
		}
		msg += ", with " + joinAnd(ks)
	} else {
		msg += ", with no keys"
	}
	msg += ". What it writes in " + joinAnd(p.Outputs) + " becomes a new version that waits for a person."
	if !p.Ready {
		return msg, p.Problems[0]
	}
	return msg, "A person starts it (in the dashboard, or in their own terminal); Claude never does."
}

// planFingerprint is what the person saw: the version, the steps with their
// commands, the keys (masked) and names, the outputs, the hosts, where it runs.
func planFingerprint(a *app.App, s *models.RemotionSource, v *models.RemotionVersion, r models.RemotionRefreshSteps, keys []RemotionPlanKey, hosts []models.RemotionHost, iso string) string {
	type fk struct {
		Provider, Env, Masked string
	}
	var ks []fk
	for _, k := range keys {
		ks = append(ks, fk{k.Provider, k.Env, k.Masked})
	}
	var hs []string
	for _, h := range hosts {
		hs = append(hs, h.Host)
	}
	b, _ := json.Marshal(map[string]any{"format": "metarush.remotion-refresh-plan/1", "source": s.ID, "version": v.Hash, "steps": r.Steps, "keys": ks,
		"outputs": r.Outputs, "hosts": hs, "isolation": iso, "machine": a.Cfg.Installation.ID})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ---- starting ----

func startRefresh(ctx context.Context, a *app.App, in *RemotionRefreshStartIn) (*RemotionRefreshStartOut, error) {
	// Its keys are this computer's (D349).
	if err := a.NeedsThisComputer(ctx, "a Remotion refresh"); err != nil {
		return nil, err
	}
	s, v, err := refreshTarget(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	p, err := planRefresh(ctx, a, s, v, &in.RemotionRefreshIn)
	if err != nil {
		return nil, err
	}
	if err := leave.Check(ctx, a, leave.Refresh(s.ProjectID, "a refresh of “"+s.Title+"”")); err != nil {
		return nil, err // refused and recorded (D290)
	}
	if !p.Ready {
		return nil, apperr.New(apperr.Validation, "This refresh can't start: %s", strings.Join(p.Problems, " ")).WithDetails(map[string]any{"problems": p.Problems})
	}
	if strings.TrimSpace(in.Plan) != p.Fingerprint {
		return nil, apperr.New(apperr.Conflict, "The plan changed since you looked at it (a new version of “%s”, another step or key, or a key changed on this computer).", s.Title).
			WithHint("Look at the plan again, then start it.").WithDetails(map[string]any{"plan": p.Fingerprint})
	}
	var after models.RemotionSource
	now := time.Now().UTC()
	if err := a.DB.C(store.CRemotionSources).FindOneAndUpdate(ctx, bson.M{"_id": s.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$inc": bson.M{"refreshes": 1}, "$set": bson.M{"refresh": p.RefreshSteps, "updatedAt": now}}, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&after); err != nil {
		return nil, err
	}
	r := &models.RemotionRefresh{ID: ids.New(ids.RemotionRefresh), WorkspaceID: a.WorkspaceID(), SourceID: s.ID, ProjectID: s.ProjectID, Number: after.Refreshes,
		RefreshSteps: p.RefreshSteps, Hosts: p.Hosts, Fingerprint: p.Fingerprint, From: v.Hash, FromNumber: v.Number, State: models.RemotionRunQueued, By: a.Actor,
		Machine: a.Cfg.Installation.Name, Installation: a.Cfg.Installation.ID, Isolation: p.Isolation, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CRemotionRefreshes, r); err != nil {
		return nil, err
	}
	what := fmt.Sprintf("“%s” from its own steps (refresh %d)", s.Title, r.Number)
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRemotionRefresh, IdempotencyKey: "remotion-refresh:" + r.ID, Priority: 25, Heavy: true, MaxAttempts: 1,
		ProjectID: s.ProjectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID,
		Payload: map[string]any{"refresh": r.ID, "source": s.ID, "title": "Refreshing " + what, "what": what, "versionNumber": v.Number}})
	if err != nil {
		_ = setRefresh(ctx, a, r.ID, bson.M{"state": models.RemotionRunFailed, "error": "It couldn't be queued."})
		return nil, err
	}
	_ = setRefresh(ctx, a, r.ID, bson.M{"jobId": j.ID})
	var runs []string
	for _, st := range p.Steps {
		runs = append(runs, st.Run)
	}
	a.Audit(ctx, "remotion.refresh", "remotion", s.ID, s.ProjectID, map[string]any{"refresh": r.ID, "number": r.Number, "version": v.Hash, "steps": runs,
		"keys": p.RefreshSteps.Keys, "outputs": p.RefreshSteps.Outputs, "machine": a.Cfg.Installation.Name, "via": a.Actor.Via})
	return &RemotionRefreshStartOut{ID: r.ID, Number: r.Number, JobID: j.ID, Status: "queued",
		Message: fmt.Sprintf("Refreshing “%s” on %s: %s. When it's done, what it wrote waits for you as a new version.", s.Title, a.Cfg.Installation.Name, joinAnd(runs))}, nil
}

// ---- records ----

func setRefresh(ctx context.Context, a *app.App, id string, set bson.M) error {
	set["updatedAt"] = time.Now().UTC()
	_, err := a.DB.C(store.CRemotionRefreshes).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set})
	return err
}

func getRefresh(ctx context.Context, a *app.App, id string) (*models.RemotionRefresh, error) {
	var r models.RemotionRefresh
	if err := a.DB.GetByID(ctx, store.CRemotionRefreshes, a.WorkspaceID(), id, &r); err != nil {
		return nil, err
	}
	return &r, nil
}

func refreshesOf(ctx context.Context, a *app.App, sourceID string, limit int64) ([]models.RemotionRefresh, error) {
	var out []models.RemotionRefresh
	err := a.DB.Find(ctx, store.CRemotionRefreshes, a.WorkspaceID(), bson.M{"sourceId": sourceID}, &out, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(limit))
	return out, err
}

// activeRefresh is a refresh of the source that hasn't finished (nil when none).
func activeRefresh(ctx context.Context, a *app.App, sourceID string) (*models.RemotionRefresh, error) {
	var out []models.RemotionRefresh
	if err := a.DB.Find(ctx, store.CRemotionRefreshes, a.WorkspaceID(), bson.M{"sourceId": sourceID, "state": bson.M{"$in": bson.A{models.RemotionRunQueued, models.RemotionRunRunning}}}, &out,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1)); err != nil || len(out) == 0 {
		return nil, err
	}
	r := &out[0]
	// A run whose job ended without saying so (MetaRush stopped mid-way) is over.
	if r.JobID != "" {
		if j, err := a.Queue.Get(ctx, r.JobID); err == nil && j.State != models.JobQueued && j.State != models.JobRunning {
			return nil, nil
		}
	}
	return r, nil
}

func runStateLabel(state string) string {
	switch state {
	case models.RemotionRunQueued:
		return "waiting to start"
	case models.RemotionRunRunning:
		return "running"
	case models.RemotionRunDone:
		return "done"
	case models.RemotionRunFailed:
		return "didn't finish"
	case models.RemotionRunCanceled:
		return "stopped"
	}
	return state
}

func refreshRow(r *models.RemotionRefresh) RemotionRefreshRow {
	row := RemotionRefreshRow{ID: r.ID, Number: r.Number, State: r.State, StateLabel: runStateLabel(r.State), Steps: []string{}, StepRuns: r.Steps, Keys: r.RefreshSteps.Keys,
		Outputs: r.RefreshSteps.Outputs, FromNumber: r.FromNumber, VersionNumber: r.VersionNumber, Version: r.Version, Changes: r.Changes, LeftOut: r.LeftOut, Summary: r.Summary,
		Error: r.Error, By: r.By, Machine: r.Machine, Isolation: r.Isolation, JobID: r.JobID, HasLog: r.Log != nil, CreatedAt: r.CreatedAt, FinishedAt: r.FinishedAt}
	for _, st := range r.RefreshSteps.Steps {
		row.Steps = append(row.Steps, st.Run)
	}
	if row.StepRuns == nil {
		row.StepRuns = []models.RemotionStepRun{}
	}
	if row.Keys == nil {
		row.Keys = []models.RemotionKey{}
	}
	if row.Outputs == nil {
		row.Outputs = []string{}
	}
	if row.Changes == nil {
		row.Changes = []models.RemotionFileChange{}
	}
	if row.LeftOut == nil {
		row.LeftOut = []models.RemotionFileLeftOut{}
	}
	if r.Version != "" {
		row.URL = links.Remotion(r.ProjectID, r.SourceID, r.Version)
	}
	return row
}

func refreshLogOp(ctx context.Context, a *app.App, in *RemotionRefreshLogIn) (*RemotionRefreshLogOut, error) {
	s, err := refreshSource(ctx, a, in.ProjectID, in.Source)
	if err != nil {
		return nil, err
	}
	var r *models.RemotionRefresh
	if in.Number > 0 {
		var rs []models.RemotionRefresh
		if err := a.DB.Find(ctx, store.CRemotionRefreshes, a.WorkspaceID(), bson.M{"sourceId": s.ID, "number": in.Number}, &rs, options.Find().SetLimit(1)); err != nil {
			return nil, err
		}
		if len(rs) > 0 {
			r = &rs[0]
		}
	} else if rs, err := refreshesOf(ctx, a, s.ID, 1); err == nil && len(rs) > 0 {
		r = &rs[0]
	}
	if r == nil {
		return nil, apperr.New(apperr.NotFound, "“%s” has no refresh like that.", s.Title).WithCommand("metarush remotion refresh runs " + s.Name)
	}
	out := &RemotionRefreshLogOut{Number: r.Number, State: r.State}
	if r.Log == nil {
		out.Text = ""
		return out, nil
	}
	b, err := readObject(ctx, a, r.Log.SHA256, r.Log.Size)
	if err != nil {
		return nil, err
	}
	out.Text = string(b)
	return out, nil
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("remotion.refresh.start", ops.Offering{ID: "remotion-refreshes", Group: "make", Title: "Remotion refreshes",
		Does:  "Runs a Remotion project's own steps to bring its data up to date, in a sandbox, started by a person.",
		Words: []string{"update the data", "refresh the numbers"}, Needs: ops.Needs{Works: []string{"renders"}}})
}
