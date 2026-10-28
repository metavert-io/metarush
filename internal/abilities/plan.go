package abilities

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
)

// Planning and starting a run (D234). The plan says, before anything runs,
// what the run uses and sends, where, with which keys, what it may bring
// back, what it is expected to cost and who decides; its fingerprint names
// all of that, so what starts is exactly what the person saw. A run is a job
// pinned to the computer that asked (its programs, its keys).

// AbilityPlanIn is a run as a person (or Claude) would start it.
type AbilityPlanIn struct {
	Ref string `json:"ref" jsonschema:"minLength=1,description=name (its newest approved version) or name@version."`
	// Inputs are the run's values by input name: words, numbers, choices,
	// yes/no; footage, pictures and sound by their ID or name.
	Inputs map[string]any `json:"inputs,omitempty"`
	// ProjectID is where what it brings back goes (default: footage to the
	// Inbox, pictures to the workspace library).
	ProjectID string `json:"projectId,omitempty"`
}

// AbilityRunIn starts the plan a person looked at.
type AbilityRunIn struct {
	AbilityPlanIn
	Plan string `json:"plan,omitempty" jsonschema:"description=The plan's fingerprint (from ability.plan): what starts is what the person saw."`
	// MaxUSD is what the person agrees a priced run may cost (at least the estimate).
	MaxUSD float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	// Input is the same run as find's run gives it (D268): the ref is the
	// catalogue entry's, everything else comes under input.
	Input *AbilityRunEntryIn `json:"input,omitempty" jsonschema:"description=The run as find's run gives it: its inputs\\, the plan's fingerprint and the rest under one field (leave the others out)."`
}

// AbilityRunEntryIn is a run's fields as find's run gives them.
type AbilityRunEntryIn struct {
	Inputs    map[string]any `json:"inputs,omitempty"`
	Plan      string         `json:"plan,omitempty"`
	ProjectID string         `json:"projectId,omitempty"`
	MaxUSD    float64        `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
}

// fromEntry takes a run given the way find's run gives it.
func (in *AbilityRunIn) fromEntry() error {
	if in.Input == nil {
		return nil
	}
	if in.Inputs != nil || in.Plan != "" || in.ProjectID != "" || in.MaxUSD != 0 {
		return apperr.New(apperr.InvalidInput, "Give the run's fields either under input or beside it, not both.")
	}
	in.Inputs, in.Plan, in.ProjectID, in.MaxUSD = in.Input.Inputs, in.Input.Plan, in.Input.ProjectID, in.Input.MaxUSD
	in.Input = nil
	return nil
}

// AbilityPlanOut is everything a person sees before a run starts.
type AbilityPlanOut struct {
	Name     string                   `json:"name"`
	Title    string                   `json:"title"`
	Version  string                   `json:"version"`
	Number   int                      `json:"number"`
	Inputs   []models.AbilityRunInput `json:"inputs"`
	Estimate costs.Estimate           `json:"estimate"`
	Hosts    []string                 `json:"hosts"`
	Network  string                   `json:"network"`
	Keys     []AbilityPageKey         `json:"keys"`
	Leaves   string                   `json:"leaves" jsonschema:"description=What leaves this computer on this run\\, in plain words."`
	Makes    string                   `json:"makes"`
	Where    string                   `json:"where" jsonschema:"description=Where what it brings back goes."`
	Decides  string                   `json:"decides" jsonschema:"enum=safe,enum=priced,enum=keyed,enum=sends,enum=outside,enum=likeness"`
	// DecidesWords says who starts it.
	DecidesWords string `json:"decidesWords"`
	Machine      string `json:"machine"`
	Isolation    string `json:"isolation,omitempty" jsonschema:"enum=,enum=macos-sandbox,enum=bubblewrap"`
	// LocalProgram is the program listening on its local service's port now
	// (D284), the one an owner agreed to; LocalWords says it.
	LocalProgram *models.AbilityLocalProgram `json:"localProgram,omitempty"`
	LocalWords   string                      `json:"localWords,omitempty"`
	// Terms are the service's terms, in the ability's words (D283).
	Terms     string `json:"terms,omitempty"`
	Apart     string `json:"apart"`
	ProjectID string `json:"projectId,omitempty"`
	// EstimatesFirst: the asker agrees to the estimate before a priced run
	// (their preference, D208); off, maxUsd isn't needed.
	EstimatesFirst bool     `json:"estimatesFirst"`
	Problems       []string `json:"problems" jsonschema:"description=What stops it from starting\\, in plain words."`
	Ready          bool     `json:"ready"`
	Fingerprint    string   `json:"fingerprint"`
	Message        string   `json:"message"`
	Next           string   `json:"next,omitempty"`
}

// AbilityRunOut says what started.
type AbilityRunOut struct {
	Message string `json:"message"`
	ID      string `json:"id"`
	Number  int    `json:"number"`
	JobID   string `json:"jobId"`
	Status  string `json:"status" jsonschema:"enum=queued"`
	Link    string `json:"link"`
}

// AbilityRunsIn lists runs, or shows one with its log and requests.
type AbilityRunsIn struct {
	Name  string `json:"name,omitempty" jsonschema:"description=An ability (default: every ability's runs)."`
	ID    string `json:"id,omitempty" jsonschema:"description=One run: its requests and what it printed too."`
	Limit int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=100"`
}

// AbilityRunsOut lists runs, newest first.
type AbilityRunsOut struct {
	Runs []AbilityRunRow `json:"runs"`
	// Run is the one asked for, with every request it made and its log.
	Run      *AbilityRunRow          `json:"run,omitempty"`
	Requests []models.AbilityRequest `json:"requests,omitempty"`
	Log      string                  `json:"log,omitempty" jsonschema:"description=The end of what it printed (keys and secrets hidden)."`
	Message  string                  `json:"message"`
}

func init() {
	ops.Def("ability.plan", "Show what a run of an ability would do before anything runs: its inputs, the estimate, the places it reaches and the keys MetaRush adds there, what leaves this computer, what it may bring back and where, who decides, and the plan's fingerprint", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AbilityPlanIn) (*AbilityPlanOut, error) {
			p, _, err := plan(ctx, a, in)
			return p, err
		})
	ops.Def("ability.run", "Start a run of an approved ability a person (or, for a free one that sends nothing but words, Claude) looked at: a job on this computer, in the sandbox, its network only through MetaRush; what it brings back is added with how it was made", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AbilityRunIn) (*AbilityRunOut, error) {
			return startRun(ctx, a, in)
		})
	ops.Def("ability.runs", "List the runs of abilities (one ability's, or every one's), or show one run with the requests it made through MetaRush and what it printed", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AbilityRunsIn) (*AbilityRunsOut, error) {
			return listRuns(ctx, a, in)
		})
}

// planned is what a plan resolved, for starting the run.
type planned struct {
	Version  *models.AbilityVersion
	Manifest *Manifest
	Values   map[string]any
	Files    map[string]fileInput
	// Send is what the run sends and whose it is; Kept the rule keeping it
	// here (What may leave, D290).
	Send leave.Send
	Kept *leave.Refusal
}

// fileInput is a clip or picture a run takes.
type fileInput struct {
	Kind, Ref, Title, SHA256, Key string
	Size                          int64
}

// plan resolves a run and says everything about it.
func plan(ctx context.Context, a *app.App, in *AbilityPlanIn) (*AbilityPlanOut, *planned, error) {
	v, err := resolve(ctx, a, in.Ref, true)
	if err != nil {
		return nil, nil, err
	}
	m, err := ParseManifest([]byte(v.Manifest))
	if err != nil {
		return nil, nil, apperr.Wrap(apperr.Integrity, err, "The stored manifest of “%s” can't be read.", v.Title)
	}
	f := v.Facts
	here, _ := hereFor(ctx, a, f, v.Name, true)
	out := &AbilityPlanOut{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number, Inputs: []models.AbilityRunInput{}, Hosts: f.Hosts, Network: networkWords(f),
		Keys: []AbilityPageKey{}, Decides: decider(f), Machine: a.Cfg.Installation.Name, Isolation: here.Isolation, Apart: here.Apart, Problems: []string{}}
	if out.Hosts == nil {
		out.Hosts = []string{}
	}
	out.DecidesWords = deciderWords(out.Decides)
	out.Terms = f.Terms
	if v.State != models.AbilityApproved {
		out.Problems = append(out.Problems, fmt.Sprintf("Version %d isn't approved (%s).", v.Number, StateWords(v.State)))
	}
	if f.Network == NetworkOpen && !v.OpenNetwork {
		out.Problems = append(out.Problems, "It asks for the whole internet, which no owner agreed to.")
	}
	ls := localCheck(ctx, a, v)
	if ls.Problem != "" {
		out.Problems = append(out.Problems, ls.Problem)
	}
	if ls.Program != nil {
		out.LocalProgram = ls.Program
		out.LocalWords = fmt.Sprintf("On port %d of this computer: %s, the program an owner agreed to.", v.Facts.Local.Port, programWords(*ls.Program))
	}
	if out.Decides == DecideLikeness {
		out.Problems = append(out.Problems, deciderWords(DecideLikeness))
	}
	out.Problems = append(out.Problems, here.Problems...)
	for i, k := range here.Keys {
		out.Keys = append(out.Keys, AbilityPageKey{Name: k.Name, Env: k.Env, Hosts: k.Hosts, Place: placeWords(f.Keys[i]), Configured: k.Configured, Masked: k.Masked})
	}
	// Where outputs go.
	projectID := ""
	var view *projectlinks.View
	if strings.TrimSpace(in.ProjectID) != "" {
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, nil, err
		}
		projectID = p.ID
		out.ProjectID = p.ID
		out.Where = "Into the project “" + p.Name + "”."
		view, _ = projectlinks.Load(ctx, a, p.ID)
	} else {
		out.Where = "Footage and sound to the Inbox; pictures, PDFs and fonts to the workspace library."
	}
	if len(f.Outputs) == 0 {
		out.Where = "It brings nothing back."
	} else if contains(f.Outputs, OutCaptions) {
		out.Where = strings.TrimSpace(strings.TrimSuffix(out.Where, ".") + ". Captions stay with the clip they were made from.")
		if len(f.Outputs) == 1 {
			out.Where = "Captions stay with the clip they were made from."
		}
	}
	out.Makes = page(ctx, a, v, here).Makes
	// Inputs.
	pl := &planned{Version: v, Manifest: m, Values: map[string]any{}, Files: map[string]fileInput{}}
	values, err := m.Values(in.Inputs, false)
	if err != nil {
		out.Problems = append(out.Problems, upperFirst(err.Error())+".")
	}
	for _, n := range m.InputNames() {
		input := m.Inputs[n]
		val, ok := values[n]
		if !ok {
			continue
		}
		ri := models.AbilityRunInput{Name: n, Kind: input.Kind}
		if _, file := fileInputSends[input.Kind]; file {
			fi, err := resolveFile(ctx, a, input.Kind, fmt.Sprint(val), projectID, view)
			if err != nil {
				out.Problems = append(out.Problems, fmt.Sprintf("%s: %s", n, apperr.From(err).Message))
				continue
			}
			ri.Ref, ri.Title, ri.SHA256 = fi.Ref, fi.Title, fi.SHA256
			pl.Files[n] = *fi
		} else {
			ri.Value = val
			pl.Values[n] = val
		}
		out.Inputs = append(out.Inputs, ri)
	}
	out.Leaves = leavesWords(f, out.Inputs)
	// What may leave (D290): the inputs it sends, and the hosts that may
	// receive them, as the Content Settings of their projects say.
	pl.Send = runSend(ctx, a, f, out.Inputs, projectID, v.Title)
	if kept, err := keptKinds(ctx, a, f, pl.Send); err != nil {
		return nil, nil, err
	} else if kept != nil {
		pl.Kept = kept
		out.Problems = append(out.Problems, kept.Message)
	} else if len(pl.Send.Kinds) > 0 && f.Network == NetworkMetaRush {
		if hosts, why := hostsKept(ctx, a, pl.Send, f.Hosts); len(hosts) == len(f.Hosts) && len(hosts) > 0 && !reachesHere(f) {
			out.Problems = append(out.Problems, why)
		} else if len(hosts) > 0 {
			out.Leaves += fmt.Sprintf(" MetaRush will refuse its requests to %s: %s", joinAnd(hosts), why)
		}
	}
	// The estimate.
	out.Estimate = estimate(v, values)
	out.EstimatesFirst = prefs.EstimatesFirstForAsker(ctx, a)
	// The fingerprint: everything above that decides what happens.
	fp := map[string]any{"version": v.Hash, "inputs": out.Inputs, "project": projectID, "machine": a.Cfg.Installation.ID, "hosts": f.Hosts, "network": f.Network,
		"estimate": out.Estimate.USD, "keys": keyPrints(a, f.Keys), "programs": here.Programs, "openNetwork": v.OpenNetwork, "local": f.Local,
		"files": filePrints(ctx, a, v.Name, f.Files), "localProgram": ls.Program}
	b, _ := json.Marshal(fp)
	sum := sha256.Sum256(b)
	out.Fingerprint = hex.EncodeToString(sum[:])
	out.Ready = len(out.Problems) == 0
	if out.Ready {
		out.Message = fmt.Sprintf("“%s” (version %d) is ready to run on %s.", v.Title, v.Number, a.Cfg.Installation.Name)
		out.Next = out.DecidesWords
	} else {
		out.Message = fmt.Sprintf("“%s” can't run on %s yet.", v.Title, a.Cfg.Installation.Name)
	}
	return out, pl, nil
}

// filePrints are the SHA-256 of the files a person set up for it here (a
// file set up again is another plan, D285).
func filePrints(ctx context.Context, a *app.App, name string, need []models.AbilityNeededFile) map[string]string {
	out := map[string]string{}
	if len(need) == 0 {
		return out
	}
	s, _ := getSetup(ctx, a, name)
	for _, fp := range setupFiles(s) {
		out[fp.Name] = fp.SHA256
	}
	return out
}

// keyPrints are short fingerprints of the keys on this computer (a changed
// key is another plan; the key itself never leaves the keychain).
func keyPrints(a *app.App, keys []models.AbilityKey) map[string]string {
	out := map[string]string{}
	for _, k := range keys {
		v, _, err := providers.Key(a.Cfg, k.Name)
		if err != nil || v == "" {
			out[k.Name] = ""
			continue
		}
		s := sha256.Sum256([]byte("metarush-ability-key\x00" + v))
		out[k.Name] = hex.EncodeToString(s[:8])
	}
	return out
}

// resolveFile finds a clip or a picture a run takes, and its stored file.
func resolveFile(ctx context.Context, a *app.App, kind, ref, projectID string, view *projectlinks.View) (*fileInput, error) {
	if kind == InPicture && !strings.HasPrefix(ref, ids.Asset+"_") {
		e, err := resources.Find(ctx, a, projectID, view, ref)
		if err != nil {
			return nil, err
		}
		r := e.Resource
		if r.Kind != "image" && r.Kind != "svg" {
			return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a picture.", r.Title)
		}
		cur := r.Version(r.Current)
		if cur == nil {
			cur = r.Latest()
		}
		if cur == nil {
			return nil, apperr.New(apperr.NotFound, "“%s” has no file yet.", r.Title)
		}
		return &fileInput{Kind: kind, Ref: r.ID, Title: r.Title, SHA256: cur.SHA256, Key: cur.StorageKey, Size: cur.Size}, nil
	}
	as, err := ops.ResolveFootage(ctx, a, ref, projectID)
	if err != nil {
		return nil, err
	}
	if as.StorageKey == "" || as.SHA256 == "" {
		return nil, apperr.New(apperr.Validation, "“%s” isn't stored yet.", as.DisplayTitle())
	}
	return &fileInput{Kind: kind, Ref: as.ID, Title: as.DisplayTitle(), SHA256: as.SHA256, Key: as.StorageKey, Size: as.Size}, nil
}

// leavesWords says what leaves this computer on this run.
func leavesWords(f models.AbilityFacts, inputs []models.AbilityRunInput) string {
	if f.Network == NetworkOff {
		return "Nothing leaves this computer."
	}
	if f.Network == NetworkMetaRush && len(f.Hosts) == 0 && f.Local != nil {
		return localLeaves(f.Local)
	}
	where := "only for " + joinAnd(f.Hosts)
	if f.Network == NetworkOpen {
		where = "to anywhere on the internet"
	}
	var parts []string
	for _, in := range inputs {
		switch {
		case in.Ref != "":
			if contains(f.Sends, fileInputSends[in.Kind]) {
				parts = append(parts, fmt.Sprintf("“%s”", in.Title))
			}
		case in.Kind == InWords && contains(f.Sends, SendsWords):
			if s, _ := in.Value.(string); s != "" {
				parts = append(parts, fmt.Sprintf("the words “%s”", clip(s, 80)))
			}
		}
	}
	if len(parts) == 0 {
		if contains(f.Sends, SendsNothing) || len(f.Sends) == 0 {
			return "It reaches " + joinAnd(f.Hosts) + ", but sends nothing you give it."
		}
		return upperFirst(joinAnd(sendsList(f.Sends))) + " may leave this computer, " + where + "."
	}
	return upperFirst(joinAnd(parts)) + " may leave this computer, " + where + "."
}

// localState is a version's service on this computer as a plan or a run
// finds it (D284).
type localState struct {
	Program *models.AbilityLocalProgram // listening on the port now
	Problem string                      // why it can't be reached ("" when it can)
}

// localCheck says whether a version's service on this computer can be
// reached: an owner agreed to exactly that port, it isn't MetaRush's own or
// reserved, and the one program listening there is the one an owner agreed
// to (set up on this computer, else at approval), by name and path.
func localCheck(ctx context.Context, a *app.App, v *models.AbilityVersion) localState {
	l := v.Facts.Local
	switch {
	case l == nil:
		return localState{}
	case v.LocalPort != l.Port:
		return localState{Problem: fmt.Sprintf("It asks to reach %s, which no owner agreed to.", localWords(l))}
	case ReservedPort(l.Port) != "" || l.Port == a.Cfg.Dashboard.Port || ownPort(a, l.Port):
		return localState{Problem: fmt.Sprintf("Port %d of this computer is MetaRush's own or reserved; an ability never reaches it.", l.Port)}
	}
	agreed := v.LocalProgram
	if s, _ := getSetup(ctx, a, v.Name); s != nil && s.LocalProgram != nil {
		agreed = s.LocalProgram
	}
	if agreed == nil {
		return localState{Problem: fmt.Sprintf("No owner agreed to a program on port %d of this computer; an owner approves it again, or sets it up here.", l.Port)}
	}
	p, why := localListener(ctx, a, l)
	switch {
	case why != "":
		return localState{Problem: why}
	case !sameProgram(p, agreed):
		return localState{Program: p, Problem: fmt.Sprintf("On port %d of this computer, %s, not what an owner agreed to; an owner sets the ability up again on this computer to agree to it.",
			l.Port, differenceWords(*p, *agreed))}
	}
	// Asked only of the program an owner agreed to.
	return localState{Program: p, Problem: probeLocal(ctx, l.Port, l.Probe)}
}

// estimate is what a run is expected to cost (D208's estimate).
func estimate(v *models.AbilityVersion, values map[string]any) costs.Estimate {
	f := v.Facts
	if f.Free || f.Price == nil {
		e := costs.Of()
		e.About = "nothing"
		return e
	}
	p := f.Price
	units := 1.0
	if p.From != "" {
		if n, ok := values[p.From].(float64); ok {
			units = n
		}
	}
	service := p.Service
	if service == "" {
		service = v.Title
	}
	part := costs.Part{Service: service, What: v.Title, Amount: units, Unit: p.Per, Units: costs.Count(units, p.Per, p.Per+"s"),
		Rate: fmt.Sprintf("%s per %s (prices of %s)", costs.Money(p.USD), p.Per, costs.Day(p.AsOf)), USD: p.USD * units}
	return costs.Of(part)
}

// startRun starts a planned run: the plan must be the one the person saw,
// and who decides decides (a person for anything but a free run that sends
// nothing but words; a priced run within what the person agreed to).
func startRun(ctx context.Context, a *app.App, in *AbilityRunIn) (*AbilityRunOut, error) {
	if err := in.fromEntry(); err != nil {
		return nil, err
	}
	// It runs with this computer's set-up (D349).
	if err := a.NeedsThisComputer(ctx, "an ability's run"); err != nil {
		return nil, err
	}
	if len(strings.TrimSpace(in.Plan)) < 16 {
		return nil, apperr.New(apperr.InvalidInput, "Say which plan to start: the fingerprint ability.plan gave for this run.").
			WithCommand("metarush ability plan " + in.Ref)
	}
	p, pl, err := plan(ctx, a, &in.AbilityPlanIn)
	if err != nil {
		return nil, err
	}
	if pl.Kept != nil { // a client's things this run would send: refused and recorded (D290)
		leave.Record(ctx, a, pl.Kept, pl.Send)
		return nil, pl.Kept.Err()
	}
	if !p.Ready {
		return nil, apperr.New(apperr.Validation, "This run can't start: %s", strings.Join(p.Problems, " ")).WithDetails(map[string]any{"problems": p.Problems})
	}
	if strings.TrimSpace(in.Plan) != p.Fingerprint {
		return nil, apperr.New(apperr.Conflict, "The plan changed since you looked at it (another version, input or key, or it is set up differently on this computer).").
			WithHint("Look at the plan again, then start it.").WithDetails(map[string]any{"plan": p.Fingerprint})
	}
	human := ops.CallerFrom(ctx).Human
	switch p.Decides {
	case DecideSends, DecideOutside, DecideKeyed, DecideLocal:
		if !human {
			return nil, apperr.New(apperr.HumanOnly, "Only a person starts this run: %s", lowerFirst(p.DecidesWords)).WithCommand("metarush ability run " + p.Name)
		}
	case DecidePriced:
		if !human {
			return nil, apperr.New(apperr.HumanOnly, "Only a person starts a priced run (it costs %s).", p.Estimate.About).WithCommand("metarush ability run " + p.Name)
		}
	}
	// A priced run is agreed to whoever else decides it (one that also sends
	// footage or reaches this computer's service is priced too).
	if p.Estimate.USD > 0 && p.EstimatesFirst && in.MaxUSD+1e-9 < p.Estimate.USD {
		return nil, apperr.New(apperr.Validation, "This run is expected to cost %s; agree to at least that first.", p.Estimate.About).
			WithDetails(map[string]any{"estimate": p.Estimate.USD, "maxUsd": in.MaxUSD})
	}
	maxUSD := in.MaxUSD
	if p.Estimate.USD > 0 && !p.EstimatesFirst && maxUSD <= 0 {
		maxUSD = p.Estimate.USD
	}
	var r *models.AbilityRun
	for attempt := 0; attempt < 5; attempt++ {
		n, err := nextRunNumber(ctx, a, p.Name)
		if err != nil {
			return nil, err
		}
		r = &models.AbilityRun{ID: ids.New(ids.AbilityRun), WorkspaceID: a.WorkspaceID(), Name: p.Name, Title: p.Title, Hash: p.Version, VersionNumber: p.Number, Number: n,
			ProjectID: p.ProjectID, Inputs: p.Inputs, Fingerprint: p.Fingerprint, EstimateUSD: p.Estimate.USD, MaxUSD: maxUSD, State: models.AbilityRunQueued,
			Installation: a.Cfg.Installation.ID, Machine: a.Cfg.Installation.Name, Isolation: p.Isolation, Network: pl.Version.Facts.Network, By: a.Actor,
			Requests: []models.AbilityRequest{}, Outputs: []models.AbilityOutput{}, CreatedAt: now(), UpdatedAt: now()}
		err = a.DB.Insert(ctx, store.CAbilityRuns, r)
		if err == nil {
			break
		}
		if !isDuplicate(err) {
			return nil, err
		}
		r = nil
	}
	if r == nil {
		return nil, apperr.New(apperr.Conflict, "MetaRush couldn't number this run; start it again.")
	}
	// Recorded: its job is queued even if the asker goes away meanwhile (a run
	// is never left waiting with nothing to run it).
	ctx = context.WithoutCancel(ctx)
	what := fmt.Sprintf("“%s” (run %d)", p.Title, r.Number)
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeAbilityRun, IdempotencyKey: "ability-run:" + r.ID, Priority: 25, MaxAttempts: 1,
		ProjectID: p.ProjectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID,
		Payload: map[string]any{"run": r.ID, "ability": p.Name, "title": "Running " + what, "what": what}})
	if err != nil {
		_ = setRun(ctx, a, r.ID, map[string]any{"state": models.AbilityRunFailed, "error": "It couldn't be queued."})
		return nil, err
	}
	_ = setRun(ctx, a, r.ID, map[string]any{"jobId": j.ID})
	a.Audit(ctx, "ability.run", "ability", docID(a.WorkspaceID(), p.Version), p.ProjectID, map[string]any{"run": r.ID, "number": r.Number, "name": p.Name, "version": p.Version,
		"inputs": p.Inputs, "estimateUsd": p.Estimate.USD, "maxUsd": maxUSD, "machine": a.Cfg.Installation.Name, "hosts": p.Hosts, "via": a.Actor.Via})
	return &AbilityRunOut{ID: r.ID, Number: r.Number, JobID: j.ID, Status: "queued", Link: links.AbilityRun(p.Name, r.ID),
		Message: fmt.Sprintf("Running %s on %s.", what, a.Cfg.Installation.Name)}, nil
}

func nextRunNumber(ctx context.Context, a *app.App, name string) (int, error) {
	var best []models.AbilityRun
	if err := a.DB.Find(ctx, store.CAbilityRuns, a.WorkspaceID(), bson.M{"name": name}, &best,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(1).SetProjection(bson.M{"number": 1})); err != nil {
		return 0, err
	}
	if len(best) == 0 {
		return 1, nil
	}
	return best[0].Number + 1, nil
}

func isDuplicate(err error) bool { return mongo.IsDuplicateKeyError(err) }

func listRuns(ctx context.Context, a *app.App, in *AbilityRunsIn) (*AbilityRunsOut, error) {
	out := &AbilityRunsOut{Runs: []AbilityRunRow{}}
	if in.ID != "" {
		r, err := getRun(ctx, a, strings.TrimSpace(in.ID))
		if err != nil {
			return nil, err
		}
		row := runRow(r)
		out.Run, out.Requests, out.Log = &row, r.Requests, r.Log
		if out.Requests == nil {
			out.Requests = []models.AbilityRequest{}
		}
		out.Runs = append(out.Runs, row)
		out.Message = fmt.Sprintf("Run %d of “%s”: %s.", r.Number, r.Title, runStateWords(r.State))
		return out, nil
	}
	limit := int64(in.Limit)
	if limit <= 0 {
		limit = 20
	}
	rs, err := runsOf(ctx, a, strings.TrimSpace(in.Name), limit)
	if err != nil {
		return nil, err
	}
	for i := range rs {
		out.Runs = append(out.Runs, runRow(&rs[i]))
	}
	out.Message = plural(len(out.Runs), "run", "runs") + "."
	return out, nil
}

// standIn is a key's stand-in for one run: what the code reads instead of
// the key (random, so a log or an answer that shows it shows nothing).
func standIn(name string) string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return "mrkey-" + name + "-" + hex.EncodeToString(b)
}

// token is a run's password for its proxy.
func token() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func sortedKeys(m map[string]fileInput) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = sandbox.NetOff
