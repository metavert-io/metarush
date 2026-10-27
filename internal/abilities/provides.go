package abilities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Abilities that provide a kind of AI work (D281): an ability whose manifest
// says `provides: [upscale]` is, once an owner approved it, a provider of
// that kind like any adapter — in the same estimate (with its price and the
// alternatives), held to Content Settings, consent, budgets and who may
// start it, recorded as AI work. A request becomes a run of the approved
// version in the sandbox (the same room, proxy and records as ability.run,
// D232–D234): its words and files go in under fixed input names, and what
// it brings back goes to the AI work, which keeps it through the classifier
// with how it was made.

// ProviderPrefix starts the catalogue name of an ability that provides a
// kind of work ("ability-<name>"), so it never collides with a built-in one.
const ProviderPrefix = "ability-"

// ProviderName is an ability's provider name in the AI services catalogue.
func ProviderName(ability string) string { return ProviderPrefix + ability }

// provideRule is what a kind of work hands an ability and takes back: the
// inputs it fills (by name, with the kinds each may be) and the output kind.
type provideRule struct {
	needs  map[string][]string
	may    map[string][]string
	output string
}

var provideRules = map[string]provideRule{
	aiservices.KindPicture: {needs: map[string][]string{"prompt": {InWords}},
		may: map[string][]string{"picture": {InPicture}, "size": {InChoice, InWords}, "aspect": {InChoice, InWords}}, output: OutPicture},
	aiservices.KindVoice: {needs: map[string][]string{"prompt": {InWords}},
		may: map[string][]string{"voice": {InChoice, InWords}, "language": {InChoice, InWords}, "style": {InWords}}, output: OutSound},
	aiservices.KindVoiceChange: {needs: map[string][]string{"audio": {InSound}}, may: map[string][]string{"voice": {InChoice, InWords}}, output: OutSound},
	aiservices.KindSoundEffect: {needs: map[string][]string{"prompt": {InWords}}, may: map[string][]string{"seconds": {InNumber}}, output: OutSound},
	aiservices.KindMusic: {needs: map[string][]string{"prompt": {InWords}},
		may: map[string][]string{"seconds": {InNumber}, "instrumental": {InYesNo}}, output: OutSound},
	aiservices.KindBackground: {needs: map[string][]string{"picture": {InPicture}}, output: OutPicture},
	aiservices.KindUpscale:    {needs: map[string][]string{"picture": {InPicture}}, may: map[string][]string{"scale": {InNumber, InChoice}}, output: OutPicture},
}

// whyNotProvided says why a kind can't be provided by an ability yet.
var whyNotProvided = map[string]string{
	aiservices.KindVideo:         "generated video has its own steps",
	aiservices.KindVoiceCopy:     "a voice copy is a person's likeness, which abilities can't make yet",
	aiservices.KindDubbing:       "a dub can copy the speakers' voices, a likeness abilities can't make yet",
	aiservices.KindTranscription: "an ability can't bring words back yet (its documents are PDFs)",
}

// ProvidableKinds are the kinds an ability may provide.
func ProvidableKinds() []string {
	var out []string
	for k := range provideRules {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// providesProblems checks a manifest's provides: kinds an ability may
// provide, the inputs each kind fills (other inputs need a default or must
// be optional: nothing else is given), the output each brings back, and no
// likeness.
func providesProblems(m *Manifest) []string {
	var out []string
	seen := map[string]bool{}
	for _, k := range m.Provides {
		if seen[k] {
			out = append(out, fmt.Sprintf("%s is listed twice", k))
			continue
		}
		seen[k] = true
		rule, ok := provideRules[k]
		if !ok {
			if why := whyNotProvided[k]; why != "" {
				out = append(out, fmt.Sprintf("%s can't be provided by an ability: %s", k, why))
			} else {
				out = append(out, fmt.Sprintf("%s isn't a kind of AI work an ability provides (%s)", k, strings.Join(ProvidableKinds(), ", ")))
			}
			continue
		}
		if bool(m.Likeness) {
			out = append(out, "an ability that makes someone's likeness can't provide AI work yet")
		}
		for name, kinds := range rule.needs {
			in, ok := m.Inputs[name]
			if !ok || !contains(kinds, in.Kind) {
				out = append(out, fmt.Sprintf("providing %s needs an input %q (%s)", k, name, strings.Join(kinds, " or ")))
			}
		}
		for name, in := range m.Inputs {
			if kinds, ok := rule.needs[name]; ok && contains(kinds, in.Kind) {
				continue
			}
			if kinds, ok := rule.may[name]; ok && contains(kinds, in.Kind) {
				continue
			}
			if in.Default == nil && !in.Optional {
				out = append(out, fmt.Sprintf("providing %s gives only %s: give %q a default or make it optional", k, strings.Join(ruleNames(rule), ", "), name))
			}
		}
		if !contains(m.Outputs, rule.output) {
			out = append(out, fmt.Sprintf("providing %s brings back %s: list it in outputs", k, rule.output))
		}
	}
	sort.Strings(out)
	return out
}

func ruleNames(r provideRule) []string {
	var out []string
	for n := range r.needs {
		out = append(out, n)
	}
	for n := range r.may {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() { aiservices.RegisterSource(syncProviders) }

var (
	syncMu  sync.Mutex
	synced  = map[string]string{} // provider → what was registered (version and kinds)
	removes = map[string]func(){}
)

// syncProviders puts each approved ability that provides a kind of work into
// the AI services catalogue and registry (its newest approved version), and
// takes out those no longer approved.
func syncProviders(ctx context.Context, x any) error {
	a, ok := x.(*app.App)
	if !ok || a == nil || a.DB == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var vs []models.AbilityVersion
	if err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{"state": models.AbilityApproved, "facts.provides.0": bson.M{"$exists": true}}, &vs,
		options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(200)); err != nil {
		return err
	}
	newest := map[string]*models.AbilityVersion{}
	for i := range vs {
		if _, ok := newest[vs[i].Name]; !ok {
			newest[vs[i].Name] = &vs[i]
		}
	}
	syncMu.Lock()
	defer syncMu.Unlock()
	want := map[string]bool{}
	for name, v := range newest {
		pn := ProviderName(name)
		want[pn] = true
		sig := v.Hash + "|" + strings.Join(v.Facts.Provides, ",")
		if synced[pn] == sig {
			continue
		}
		drop(pn)
		rm, err := aiservices.ReplaceProvider(providerInfo(v))
		if err != nil {
			continue
		}
		for _, k := range v.Facts.Provides {
			if _, ok := provideRules[k]; ok {
				aiservices.Register(&provider{name: v.Name, title: v.Title, kind: k, hash: v.Hash, facts: v.Facts})
			}
		}
		synced[pn], removes[pn] = sig, func() {
			for _, k := range v.Facts.Provides {
				aiservices.Unregister(pn, k)
			}
			rm()
		}
	}
	for pn := range synced {
		if !want[pn] {
			drop(pn)
		}
	}
	return nil
}

func drop(pn string) {
	if rm := removes[pn]; rm != nil {
		rm()
	}
	delete(removes, pn)
	delete(synced, pn)
}

// providerInfo is an ability as a provider in the catalogue: its models (one
// per kind, named by the ability), its price and what its network means for
// what leaves this computer.
func providerInfo(v *models.AbilityVersion) aiservices.ProviderInfo {
	f := v.Facts
	day := v.CreatedAt.UTC().Format("2006-01-02")
	for _, d := range v.Decisions {
		if d.State == models.AbilityApproved {
			day = d.At.UTC().Format("2006-01-02") // the day an owner approved it
		}
	}
	p := aiservices.ProviderInfo{Name: ProviderName(v.Name), Title: v.Title, Route: aiservices.RouteAbility, Data: dataTerms(f, day), Stays: f.Network == NetworkOff,
		Reaches: reaches(f)}
	lic := aiservices.Licence{Summary: fmt.Sprintf("The ability's own licence is %s; what it makes is as the services it reaches say (MetaRush doesn't know their terms).", f.Licence),
		Commercial: "unstated", Broadcast: "unstated", Source: "ability " + v.Name + "@" + Short(v.Hash), Checked: day}
	for _, k := range f.Provides {
		info, err := aiservices.KindOf(k)
		if err != nil {
			continue
		}
		tasks := []string{info.Tasks[0]}
		if k == aiservices.KindPicture && hasInput(f, "picture", InPicture) {
			tasks = append(tasks, aiservices.TaskEdit)
		}
		m := aiservices.Model{ID: v.Name, Title: v.Title, Kind: k, Tasks: tasks, Stage: "ga", Rank: 2, Licence: lic, Docs: "ability " + v.Name, Checked: day,
			Notes: []string{fmt.Sprintf("An ability approved by an owner (version %d), run in MetaRush's sandbox on this computer.", v.Number)}}
		if k == aiservices.KindUpscale {
			m.Limits.Scales = []int{2, 4}
		}
		p.Models = append(p.Models, m)
		pr := aiservices.Price{Model: v.Name, Kind: k, Effective: day, Checked: day, Source: "the ability's manifest", Unit: "request"}
		if !f.Free && f.Price != nil {
			pr.USD, pr.Unit = f.Price.USD, unitOf(f.Price.Per)
			if f.Price.AsOf != "" {
				pr.Effective, pr.Checked = f.Price.AsOf, f.Price.AsOf
			}
		}
		p.Prices = append(p.Prices, pr)
	}
	return p
}

// reaches are the integrations an ability's runs reach: those whose key it
// uses, and those whose hosts it calls; an open network reaches any ("*").
func reaches(f models.AbilityFacts) []string {
	var out []string
	add := func(n string) {
		if n != "" && !contains(out, n) {
			out = append(out, n)
		}
	}
	if f.Network == NetworkOff {
		return nil
	}
	if f.Network == NetworkOpen {
		add("*")
	}
	for _, k := range f.Keys {
		if _, integration, _ := providers.AbilityKeyHosts(k.Name); integration {
			add(k.Name)
		}
	}
	for _, h := range f.Hosts {
		for _, n := range providers.IntegrationsFor(h) {
			add(n)
		}
	}
	sort.Strings(out)
	return out
}

// unitOf maps an ability's unit to the catalogue's (a run is a request).
func unitOf(per string) string {
	switch strings.ToLower(strings.TrimSpace(per)) {
	case "picture", "image":
		return "picture"
	case "second", "minute", "hour", "song":
		return strings.ToLower(per)
	}
	return "request"
}

func hasInput(f models.AbilityFacts, name, kind string) bool {
	for _, in := range f.Inputs {
		if in.Name == name && in.Kind == kind {
			return true
		}
	}
	return false
}

// dataTerms say what leaves this computer when the ability runs.
func dataTerms(f models.AbilityFacts, day string) aiservices.DataTerms {
	d := aiservices.DataTerms{Source: "the ability's manifest", Checked: day, Region: "This computer"}
	switch f.Network {
	case NetworkOff:
		d.Summary = "It runs on this computer in MetaRush's sandbox with its network off: nothing leaves this computer."
		d.Training, d.Retention = "None: nothing is sent anywhere.", "Only what MetaRush keeps."
	case NetworkOpen:
		d.Summary = "It runs on this computer in MetaRush's sandbox and may reach any public host, through MetaRush; what those services keep is up to them."
		d.Training, d.Retention, d.Region = "As the services it reaches say; MetaRush doesn't know.", "As the services it reaches say.", "This computer, and wherever those services are"
	default:
		places := append([]string{}, f.Hosts...)
		if f.Local != nil {
			places = append(places, localWords(f.Local)) // a service here, outside the sandbox (D284)
		}
		if len(f.Hosts) == 0 && f.Local != nil {
			d.Summary = "It runs on this computer in MetaRush's sandbox. " + localLeaves(f.Local)
			d.Training, d.Retention = "As "+f.Local.Service+" does; MetaRush doesn't know.", "As "+f.Local.Service+" keeps it."
			return d
		}
		d.Summary = fmt.Sprintf("It runs on this computer in MetaRush's sandbox and reaches only %s, through MetaRush (%s may leave); what they keep is up to them.",
			joinAnd(places), joinAnd(sendsList(f.Sends)))
		d.Training, d.Retention = "As "+joinAnd(places)+" say; MetaRush doesn't know.", "As "+joinAnd(places)+" say."
		d.Region = "This computer, and wherever " + joinAnd(f.Hosts) + " keep data"
	}
	return d
}

// provider is one approved ability doing one kind of work.
type provider struct {
	name, title, kind, hash string
	facts                   models.AbilityFacts
}

func (p *provider) Provider() string { return ProviderName(p.name) }
func (p *provider) Kind() string     { return p.kind }

func (p *provider) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(p.Provider(), p.kind, model)
}

// Estimate is the ability's own estimate (its dated price, by the number
// input its manifest names), in the catalogue's shape.
func (p *provider) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	v := &models.AbilityVersion{Name: p.name, Title: p.title, Hash: p.hash, Facts: p.facts}
	values, _ := requestValues(p.facts, req)
	e := estimate(v, values)
	out := &aiservices.Estimate{Provider: p.Provider(), ProviderTitle: p.title, Kind: p.kind, Model: p.name, ModelTitle: p.title, USD: e.USD, OutputUSD: e.USD,
		Unit: "request", Amount: 1, Units: "1 run", Rate: "nothing charged (the ability says it is free)", Source: "the ability's manifest", PriceTable: at.UTC().Format("2006-01-02")}
	if len(e.Parts) > 0 {
		pt := e.Parts[0]
		out.Unit, out.Amount, out.Units, out.Rate = pt.Unit, pt.Amount, pt.Units, pt.Rate
		if p.facts.Price != nil {
			out.UnitUSD, out.PriceTable, out.Checked = p.facts.Price.USD, p.facts.Price.AsOf, p.facts.Price.AsOf
		}
	}
	return out, nil
}

// Version is the approved version it runs: a plan records it, and work
// planned with another version isn't run with this one.
func (p *provider) Version(string) string { return p.hash }

// PersonOnly says when only a person may start it (D234's decider).
func (p *provider) PersonOnly(string) string {
	switch d := decider(p.facts); d {
	case DecideSafe:
		return ""
	default:
		return deciderWords(d)
	}
}

// Ready says whether it can run on this computer now: still approved, and
// set up here (its programs and keys).
func (p *provider) Ready(e aiservices.Env, _ string) error {
	a, ok := e.App.(*app.App)
	if !ok || a == nil {
		return apperr.New(apperr.NotConfigured, "MetaRush didn't say which computer runs “%s”.", p.title)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v, err := getVersion(ctx, a, p.hash)
	if err != nil || v.State != models.AbilityApproved {
		return apperr.New(apperr.Validation, "“%s” isn't approved any more.", p.title).WithCommand("metarush ability show " + p.name)
	}
	if ls := localCheck(ctx, a, v); ls.Problem != "" {
		return apperr.New(apperr.NotConfigured, "“%s” can't run on this computer now: %s", p.title, ls.Problem).WithCommand("metarush ability setup " + p.name)
	}
	here, _ := hereFor(ctx, a, v.Facts, v.Name, true)
	if !here.Ready {
		return apperr.New(apperr.NotConfigured, "“%s” can't run on this computer yet: %s", p.title, strings.Join(here.Problems, " ")).
			WithCommand("metarush ability setup " + p.name)
	}
	return nil
}

// Submit runs the approved version for the request, now, in the sandbox.
func (p *provider) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	e, ok := aiservices.EnvFrom(ctx)
	a, _ := e.App.(*app.App)
	if !ok || a == nil {
		return nil, apperr.New(apperr.Internal, "MetaRush didn't say which computer runs “%s”.", p.title)
	}
	return runFor(ctx, a, p, req)
}

func (p *provider) Poll(_ context.Context, _ string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: p.Provider(), Message: "The ability's run is over; what it made was kept, or not, when it ended."}
}

func (p *provider) Fetch(_ context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) == 0 {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: p.Provider(), Message: "The ability brought nothing back to keep."}
	}
	n, err := dst.Write(out.Data)
	return int64(n), err
}

func (p *provider) Cancel(context.Context, string, *aiservices.Ticket) error { return nil }

// Forget has nothing of MetaRush's own to delete: the run's room is gone;
// what the services it reached keep is said in its data terms.
func (p *provider) Forget(context.Context, string, *aiservices.Ticket, []aiservices.Output) error {
	return nil
}

// requestValues are the ability's inputs for a request: words and numbers by
// their fixed names, files by their roles (paths; their bytes as given).
func requestValues(f models.AbilityFacts, req *aiservices.Request) (map[string]any, map[string]string) {
	values, files := map[string]any{}, map[string]string{}
	has := func(name string) (models.AbilityInput, bool) {
		for _, in := range f.Inputs {
			if in.Name == name {
				return in, true
			}
		}
		return models.AbilityInput{}, false
	}
	o := req.Options
	set := func(name string, v any, empty bool) {
		if _, ok := has(name); ok && !empty {
			values[name] = v
		}
	}
	set("prompt", req.Prompt, strings.TrimSpace(req.Prompt) == "")
	set("voice", o.Voice, o.Voice == "")
	set("language", o.Language, o.Language == "")
	set("style", o.Style, o.Style == "")
	set("seconds", o.Seconds, o.Seconds == 0)
	set("instrumental", o.Instrumental, !o.Instrumental)
	set("size", o.Size, o.Size == "")
	set("aspect", o.AspectRatio, o.AspectRatio == "")
	set("scale", float64(o.Scale), o.Scale == 0)
	for name, role := range map[string]string{"picture": aiservices.RolePicture, "audio": aiservices.RoleAudio} {
		if in := req.Input(role); in != nil {
			if _, ok := has(name); ok {
				files[name] = in.Path
				values[name] = in.SHA256 // the file's place among the values; the room takes the path
			}
		}
	}
	return values, files
}

// runFor runs an approved version for one request of AI work, recorded as an
// ability run of that work (D281).
func runFor(ctx context.Context, a *app.App, p *provider, req *aiservices.Request) (*aiservices.Ticket, error) {
	refuse := func(msg string) error {
		return &videogen.Error{Kind: videogen.KindInvalid, Provider: p.Provider(), Message: msg}
	}
	v, err := getVersion(ctx, a, p.hash)
	if err != nil || v.State != models.AbilityApproved {
		return nil, refuse(fmt.Sprintf("“%s” isn't approved any more.", p.title))
	}
	if !contains(v.Facts.Provides, req.Kind) {
		return nil, refuse(fmt.Sprintf("“%s” doesn't do that kind of work.", p.title))
	}
	rule := provideRules[req.Kind]
	f := v.Facts
	here, paths := hereFor(ctx, a, f, v.Name, true)
	if !here.Ready {
		return nil, refuse(fmt.Sprintf("“%s” can't run on this computer yet: %s", p.title, strings.Join(here.Problems, " ")))
	}
	arc, err := loadArchive(ctx, a, v)
	if err != nil {
		return nil, refuse(apperr.From(err).Message)
	}
	m, err := arc.Manifest()
	if err != nil {
		return nil, refuse("Its manifest can't be read.")
	}
	given, files := requestValues(f, req)
	values, err := m.Values(given, false)
	if err != nil {
		return nil, refuse(fmt.Sprintf("“%s” can't take this request: %s.", p.title, err))
	}
	for name := range files {
		delete(values, name) // files go in by path, not as values
	}
	var inputs []models.AbilityRunInput
	for _, n := range m.InputNames() {
		in := m.Inputs[n]
		ri := models.AbilityRunInput{Name: n, Kind: in.Kind}
		if path, ok := files[n]; ok {
			ri.Ref, ri.Title, ri.SHA256 = "work:"+req.Tag, "the "+n+" sent with the AI work", given[n].(string)
			_ = path
		} else if val, ok := values[n]; ok {
			ri.Value = val
		} else {
			continue
		}
		inputs = append(inputs, ri)
	}
	est := estimate(v, values)
	// The work's project: what may leave (D290) and the receipt's places
	// (D291) are its Content Settings'.
	var work struct {
		ProjectID string `bson:"projectId"`
	}
	_ = a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"_id": req.Tag, "workspaceId": a.WorkspaceID()}).Decode(&work)
	fp, _ := json.Marshal(map[string]any{"version": v.Hash, "inputs": inputs, "work": req.Tag})
	sum := sha256.Sum256(fp)
	var r *models.AbilityRun
	for attempt := 0; attempt < 5; attempt++ {
		n, err := nextRunNumber(ctx, a, v.Name)
		if err != nil {
			return nil, err
		}
		started := now()
		r = &models.AbilityRun{ID: ids.New(ids.AbilityRun), WorkspaceID: a.WorkspaceID(), Name: v.Name, Title: v.Title, Hash: v.Hash, VersionNumber: v.Number, Number: n,
			Inputs: inputs, ProjectID: work.ProjectID, Fingerprint: hex.EncodeToString(sum[:]), EstimateUSD: est.USD, MaxUSD: est.USD, State: models.AbilityRunRunning, StartedAt: &started,
			Installation: a.Cfg.Installation.ID, Machine: a.Cfg.Installation.Name, Isolation: here.Isolation, Network: f.Network, By: a.Actor, Work: req.Tag,
			Requests: []models.AbilityRequest{}, Outputs: []models.AbilityOutput{}, CreatedAt: started, UpdatedAt: started}
		if err = a.DB.Insert(ctx, store.CAbilityRuns, r); err == nil {
			break
		} else if !isDuplicate(err) {
			return nil, err
		}
		r = nil
	}
	if r == nil {
		return nil, refuse("MetaRush couldn't number the ability's run.")
	}
	res, msg, canceled := sandboxRun(ctx, a, r, v, arc, m, paths, values, files, func(float64, string) {})
	if res != nil {
		defer res.Close()
	}
	switch {
	case canceled:
		finish(ctx, a, r, models.AbilityRunCanceled, "", "It was stopped.", nil)
		return nil, ctx.Err()
	case msg != "" && res == nil:
		// It never ran: nothing was sent or spent.
		finish(ctx, a, r, models.AbilityRunFailed, "", msg, nil)
		return nil, refuse(fmt.Sprintf("“%s” couldn't run: %s", p.title, msg))
	case msg != "":
		// It ran and failed: what it reached may have charged (never run again).
		finish(ctx, a, r, models.AbilityRunFailed, "", msg, res)
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: p.Provider(), Message: fmt.Sprintf("“%s” didn't finish: %s", p.title, msg),
			Hint: "Its run's log says more (Library → Abilities)."}
	}
	st := &aiservices.Status{State: aiservices.StateDone}
	for _, o := range res.Outputs {
		ao := models.AbilityOutput{Path: o.Rel, Kind: o.Kind, Size: o.Size, SHA256: o.SHA256, Title: o.Title}
		switch {
		case !o.OK:
			ao.Status, ao.Why = "left out", o.Why
		case o.Kind != rule.output || len(st.Outputs) > 0:
			ao.Status, ao.Why = "left out", "the AI work takes one "+rule.output
		default:
			b, err := os.ReadFile(o.Abs)
			if err != nil {
				ao.Status, ao.Why = "left out", "it couldn't be read"
				break
			}
			st.Outputs = append(st.Outputs, aiservices.Output{MIME: aiservices.Sniff(b), Data: b})
			ao.Status = "to the AI work"
		}
		r.Outputs = append(r.Outputs, ao)
	}
	if len(st.Outputs) == 0 {
		finish(ctx, a, r, models.AbilityRunFailed, "", "It brought back no "+rule.output+".", res)
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: p.Provider(), Message: fmt.Sprintf("“%s” brought back no %s.", p.title, rule.output)}
	}
	finish(ctx, a, r, models.AbilityRunDone, fmt.Sprintf("Run %d of “%s” made %s for AI work.", r.Number, r.Title, kindWhat(req.Kind)), "", res)
	return &aiservices.Ticket{ID: r.ID, Extra: map[string]string{"abilityRun": r.ID}, SubmittedAt: now(), Ready: st}, nil
}

func kindWhat(kind string) string {
	if k, err := aiservices.KindOf(kind); err == nil {
		return k.What
	}
	return kind
}

// estimateOf is a version's estimate for given values (tests).
func estimateOf(v *models.AbilityVersion, values map[string]any) costs.Estimate {
	return estimate(v, values)
}
