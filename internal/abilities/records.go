package abilities

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
)

// docID is a version's document ID: one per workspace and hash.
func docID(ws, hash string) string { return ws + "/" + hash }

// setupID is a setup's document ID: one per workspace, computer and ability.
func setupID(ws, installation, name string) string { return ws + "/" + installation + "/" + name }

// getVersion loads one version by its full hash.
func getVersion(ctx context.Context, a *app.App, hash string) (*models.AbilityVersion, error) {
	var v models.AbilityVersion
	if err := a.DB.GetByID(ctx, store.CAbilities, a.WorkspaceID(), docID(a.WorkspaceID(), hash), &v); err != nil {
		return nil, err
	}
	return &v, nil
}

// versionsOf lists a name's versions, newest first.
func versionsOf(ctx context.Context, a *app.App, name string) ([]models.AbilityVersion, error) {
	var vs []models.AbilityVersion
	err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{"name": name}, &vs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(500))
	return vs, err
}

// allVersions lists every version in the workspace, newest check first.
func allVersions(ctx context.Context, a *app.App) ([]models.AbilityVersion, error) {
	var vs []models.AbilityVersion
	err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), bson.M{}, &vs, options.Find().SetSort(bson.D{{Key: "name", Value: 1}, {Key: "number", Value: -1}}).SetLimit(5000))
	return vs, err
}

// resolve finds one version: name@prefix, a bare prefix, or a name — its
// newest version, or with approved its newest approved one.
func resolve(ctx context.Context, a *app.App, ref string, approved bool) (*models.AbilityVersion, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	filter := bson.M{}
	if r.Name != "" {
		filter["name"] = r.Name
	}
	if r.Hash != "" {
		if ValidHash(r.Hash) {
			filter["hash"] = r.Hash
		} else {
			filter["hash"] = bson.M{"$gte": r.Hash, "$lt": r.Hash + "g"}
		}
	} else if approved {
		filter["state"] = models.AbilityApproved
	}
	var vs []models.AbilityVersion
	if err := a.DB.Find(ctx, store.CAbilities, a.WorkspaceID(), filter, &vs, options.Find().SetSort(bson.D{{Key: "number", Value: -1}}).SetLimit(50)); err != nil {
		return nil, err
	}
	switch {
	case len(vs) == 0 && approved && r.Hash == "":
		if any, _ := versionsOf(ctx, a, r.Name); len(any) > 0 {
			return nil, apperr.New(apperr.Validation, "No version of the ability “%s” is approved yet.", any[0].Title).
				WithHint("An owner looks at it under Library → Abilities and approves it first.").WithCommand("metarush ability show " + r.Name)
		}
		fallthrough
	case len(vs) == 0:
		return nil, apperr.New(apperr.NotFound, "MetaRush doesn't know the ability %s.", r).
			WithHint("Check its folder first; then it shows up under Library → Abilities for an owner to approve.").WithCommand("metarush ability check <folder>")
	case len(vs) > 1 && r.Hash != "":
		var names []string
		for _, v := range vs {
			names = append(names, v.Name+"@"+Short(v.Hash))
		}
		return nil, apperr.New(apperr.Conflict, "More than one version matches %s.", r).WithHint("Give more of the version.").WithDetails(map[string]any{"matches": names})
	}
	return &vs[0], nil
}

// latest keeps each name's newest version, and its newest approved one.
func latest(vs []models.AbilityVersion) (newest, approved map[string]*models.AbilityVersion, order []string) {
	newest, approved = map[string]*models.AbilityVersion{}, map[string]*models.AbilityVersion{}
	for i := range vs {
		v := &vs[i]
		if cur, ok := newest[v.Name]; !ok || v.Number > cur.Number {
			if !ok {
				order = append(order, v.Name)
			}
			newest[v.Name] = v
		}
		if v.State == models.AbilityApproved {
			if cur, ok := approved[v.Name]; !ok || v.Number > cur.Number {
				approved[v.Name] = v
			}
		}
	}
	sort.Strings(order)
	return newest, approved, order
}

// factsOf is what a manifest declares, as MetaRush enforces it.
func factsOf(m *Manifest) models.AbilityFacts {
	f := models.AbilityFacts{Runs: m.Runs, Inputs: []models.AbilityInput{}, Outputs: append([]string{}, m.Outputs...), Free: m.Cost.Free, Keys: []models.AbilityKey{},
		Hosts: append([]string{}, m.Hosts...), Network: m.NetworkMode(), Sends: m.Sending(), Outside: bool(m.ChangesOutside), Likeness: bool(m.Likeness),
		Programs: append([]string{}, m.Needs.Programs...), OS: m.Needs.OS, Licence: m.Licence, Terms: strings.TrimSpace(m.Terms), Minutes: m.MinutesLimit(), MB: int(m.BytesLimit() >> 20),
		Provides: append([]string(nil), m.Provides...)}
	for _, nf := range m.Needs.Files {
		f.Files = append(f.Files, models.AbilityNeededFile{Name: nf.Name, Says: strings.TrimSpace(nf.Says), Kind: nf.Kind, Env: FileEnv(nf.Name)})
	}
	if l := m.Local; l != nil {
		f.Local = &models.AbilityLocal{Port: l.Port, Service: strings.TrimSpace(l.Service)}
		if pr := l.Probe; pr != nil {
			f.Local.Probe = &models.AbilityLocalProbe{Path: pr.Path, Expect: append([]string{}, pr.Expect...)}
		}
	}
	if !m.Cost.Free {
		f.Price = &models.AbilityPrice{Service: m.Cost.Service, USD: m.Cost.USD, Per: m.Cost.Per, From: m.Cost.From, AsOf: m.Cost.AsOf}
	}
	for _, n := range m.InputNames() {
		in := m.Inputs[n]
		f.Inputs = append(f.Inputs, models.AbilityInput{Name: n, Kind: in.Kind, Says: in.Says, Min: in.Min, Max: in.Max, Default: in.Default, Options: in.Options, Optional: in.Optional})
	}
	for _, k := range m.Keys {
		h, q := k.Place()
		f.Keys = append(f.Keys, models.AbilityKey{Name: k.Name, Env: k.EnvName(), Hosts: k.HostsOr(m.Hosts), Header: h, Query: q})
	}
	if f.Outputs == nil {
		f.Outputs = []string{}
	}
	if f.Programs == nil {
		f.Programs = []string{}
	}
	return f
}

// ---- plain words ----

// StateWords says a version's state.
func StateWords(state string) string {
	switch state {
	case models.AbilityWaiting:
		return "waits for an owner"
	case models.AbilityApproved:
		return "approved"
	case models.AbilityTurnedDown:
		return "turned down"
	case models.AbilityRevoked:
		return "approval taken back"
	case models.AbilityFailed:
		return "its check didn't pass"
	case models.AbilityImported:
		return "brought in from another workspace; not checked here yet"
	}
	return state
}

// joinAnd joins words: a, b and c.
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// sendsWords says what leaves this computer, and where.
func sendsWords(f models.AbilityFacts) string {
	if f.Network == NetworkMetaRush && len(f.Hosts) == 0 && f.Local != nil {
		return localLeaves(f.Local)
	}
	if f.Network == NetworkOpen {
		return "It may reach any public place on the internet: what leaves this computer isn't limited to the places it names (" + joinAnd(sendsList(f.Sends)) + ")."
	}
	what := sendsList(f.Sends)
	if len(what) == 0 || f.Network == NetworkOff {
		return "Nothing leaves this computer."
	}
	return upperFirst(joinAnd(what)) + " you give it leave this computer, only for " + joinAnd(f.Hosts) + "."
}

func sendsList(sends []string) []string {
	var out []string
	for _, s := range sends {
		switch s {
		case SendsWords:
			out = append(out, "words")
		case SendsPictures:
			out = append(out, "pictures")
		case SendsSound:
			out = append(out, "sound")
		case SendsFootage:
			out = append(out, "footage")
		}
	}
	return out
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// localWords names a service on this computer: "ComfyUI on this computer
// (port 8188)".
func localWords(l *models.AbilityLocal) string {
	return fmt.Sprintf("%s on this computer (port %d)", l.Service, l.Port)
}

// localLeaves says what leaves for an ability that reaches only a service on
// this computer: nothing through MetaRush, but the service runs outside the
// sandbox and may reach further itself.
func localLeaves(l *models.AbilityLocal) string {
	return fmt.Sprintf("Nothing leaves this computer through MetaRush; what you give it goes to %s, which runs outside MetaRush's sandbox and may send things on by itself.", localWords(l))
}

// costWords says what a run costs: free only with no price and no key (a
// key to a service may cost money there, whatever the manifest says).
func costWords(f models.AbilityFacts) string {
	if (f.Free || f.Price == nil) && len(f.Keys) > 0 {
		return "No price of its own, but the service its key is for may charge"
	}
	if f.Free || f.Price == nil {
		return "Free"
	}
	p := f.Price
	s := fmt.Sprintf("%s per %s", costs.Money(p.USD), p.Per)
	if p.Service != "" {
		s += " at " + p.Service
	}
	return s + " (prices of " + costs.Day(p.AsOf) + ")"
}

// costClass is the one-word cost: free (no price, no key), keyed (no price,
// but a key to a service, which may charge) or priced.
func costClass(f models.AbilityFacts) string {
	switch {
	case f.Free || f.Price == nil:
		if len(f.Keys) > 0 {
			return "keyed"
		}
		return "free"
	}
	return "priced"
}

// Who decides a run (§ the manifest's declarations, checked at run time).
const (
	DecideSafe     = "safe"     // free, no key, nothing but words leave, nothing outside changes: anyone, Auto too
	DecidePriced   = "priced"   // priced: a person agrees to the estimate
	DecideKeyed    = "keyed"    // it uses a key: whatever its manifest says it costs, a person starts each run
	DecideSends    = "sends"    // pictures, sound or footage leave: a person each time
	DecideOutside  = "outside"  // it changes something outside MetaRush: people only
	DecideLocal    = "local"    // it drives a program on this computer outside the sandbox (D284): a person each time
	DecideLikeness = "likeness" // it makes someone's likeness: not run yet
)

// decider says who decides a run of a version with these facts.
func decider(f models.AbilityFacts) string {
	switch {
	case f.Likeness:
		return DecideLikeness
	case f.Outside:
		return DecideOutside
	case f.Network == NetworkOpen:
		return DecideOutside
	case f.Local != nil:
		return DecideLocal
	case contains(f.Sends, SendsPictures) || contains(f.Sends, SendsSound) || contains(f.Sends, SendsFootage):
		return DecideSends
	case !f.Free && f.Price != nil:
		return DecidePriced
	case len(f.Keys) > 0:
		// "Free" is the manifest's word; a key to a service may cost money
		// there, so a person decides (review 4).
		return DecideKeyed
	}
	return DecideSafe
}

// deciderWords says it to a person.
func deciderWords(d string) string {
	switch d {
	case DecideSafe:
		return "Anyone may run it, and Claude may run it as a safe step: it is free, uses no key and sends nothing but words."
	case DecidePriced:
		return "Each run is priced first; a person agrees to the estimate before it starts."
	case DecideKeyed:
		return "A person starts each run: it uses a key to a service, which may charge for what it does."
	case DecideSends:
		return "A person starts each run: it sends pictures, sound or footage away from this computer."
	case DecideOutside:
		return "Only a person starts a run: it changes something outside MetaRush or reaches the whole internet."
	case DecideLikeness:
		return "MetaRush doesn't run abilities that make someone's likeness yet."
	case DecideLocal:
		return "A person starts each run: it drives a program on this computer that runs outside MetaRush's sandbox."
	}
	return ""
}

func networkWords(f models.AbilityFacts) string {
	switch f.Network {
	case NetworkOff:
		return "No network: it reaches nothing outside this computer."
	case NetworkOpen:
		return "Any public place on the internet, through MetaRush, which records every request; never this computer or a local network (an owner agrees to that separately)."
	}
	if f.Local != nil {
		local := localWords(f.Local) + ", through MetaRush, which records every request; nothing else on this computer or a local network"
		if len(f.Hosts) == 0 {
			return "Only " + local + "."
		}
		return "Only " + joinAnd(f.Hosts) + " and " + local + "."
	}
	return "Only " + joinAnd(f.Hosts) + ", through MetaRush, which adds the keys and records every request."
}

// ---- this computer ----

// HereProgram is a program an ability needs, as this computer has it.
type HereProgram struct {
	Name   string `json:"name"`
	Path   string `json:"path,omitempty"`
	SHA256 string `json:"sha256,omitempty"`
	SetUp  bool   `json:"setUp" jsonschema:"description=A person set it up for this ability on this computer."`
	OK     bool   `json:"ok" jsonschema:"description=It is there and unchanged since it was set up."`
	Says   string `json:"says"`
}

// HereFile is a file an ability needs (D285), as this computer has it.
type HereFile struct {
	Name  string `json:"name"`
	Says  string `json:"says" jsonschema:"description=What file to give."`
	Path  string `json:"path,omitempty"`
	Size  int64  `json:"size,omitempty"`
	About string `json:"about,omitempty" jsonschema:"description=What MetaRush read the file to be."`
	SetUp bool   `json:"setUp"`
	OK    bool   `json:"ok" jsonschema:"description=It is there and unchanged since it was set up."`
	State string `json:"state" jsonschema:"description=In plain words."`
}

// HereKey is a key an ability needs, as this computer has it.
type HereKey struct {
	Name       string   `json:"name"`
	Env        string   `json:"env"`
	Hosts      []string `json:"hosts"`
	Configured bool     `json:"configured"`
	Masked     string   `json:"masked,omitempty"`
}

// Here is what this computer has for an ability.
type Here struct {
	Machine   string        `json:"machine"`
	OS        bool          `json:"os" jsonschema:"description=It runs on this computer's system."`
	SetUp     bool          `json:"setUp" jsonschema:"description=A person set it up on this computer."`
	Programs  []HereProgram `json:"programs"`
	Files     []HereFile    `json:"files"`
	Keys      []HereKey     `json:"keys"`
	Isolation string        `json:"isolation,omitempty" jsonschema:"enum=,enum=macos-sandbox,enum=bubblewrap"`
	Apart     string        `json:"apart" jsonschema:"description=How a run is kept apart here\\, in plain words."`
	// Ready: everything a run needs is here (the version's approval aside).
	Ready    bool     `json:"ready"`
	Problems []string `json:"problems" jsonschema:"description=What a run needs that this computer doesn't have\\, in plain words."`
	Fix      string   `json:"fix,omitempty" jsonschema:"description=What an administrator of this computer does about the sandbox (Details)."`
}

// getSetup loads this computer's setup of an ability (nil when none).
func getSetup(ctx context.Context, a *app.App, name string) (*models.AbilitySetup, error) {
	var s models.AbilitySetup
	err := a.DB.GetByID(ctx, store.CAbilitySetups, a.WorkspaceID(), setupID(a.WorkspaceID(), a.Cfg.Installation.ID, name), &s)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// setupsOf lists every computer's setup of an ability.
func setupsOf(ctx context.Context, a *app.App, name string) ([]models.AbilitySetup, error) {
	var out []models.AbilitySetup
	err := a.DB.Find(ctx, store.CAbilitySetups, a.WorkspaceID(), bson.M{"name": name}, &out, options.Find().SetSort(bson.D{{Key: "machine", Value: 1}}))
	return out, err
}

// lookPath finds a program on this computer's PATH (for a check before a
// person set the ability up; runs use only what a person set up).
func lookPath(name string) string {
	p, err := exec.LookPath(name)
	if err != nil {
		return ""
	}
	if abs, err := filepath.Abs(p); err == nil {
		p = abs
	}
	return p
}

// hereFor says what this computer has for a version. With pinned, programs
// count only as a person set them up (runs); without, a program on PATH is
// enough (checks, before anyone set it up).
func hereFor(ctx context.Context, a *app.App, f models.AbilityFacts, name string, pinned bool) (Here, map[string]string) {
	h := Here{Machine: a.Cfg.Installation.Name, Programs: []HereProgram{}, Files: []HereFile{}, Keys: []HereKey{}, Problems: []string{}}
	paths := map[string]string{}
	h.OS = len(f.OS) == 0 || contains(f.OS, thisOS())
	if !h.OS {
		h.Problems = append(h.Problems, "It runs only on "+joinAnd(osWords(f.OS))+".")
	}
	iso := sandbox.Here(a.Tools)
	h.Isolation, h.Apart = iso.Kind, iso.Words
	if !iso.OK() {
		h.Problems = append(h.Problems, iso.Problem)
		h.Fix = iso.Fix
	}
	setup, _ := getSetup(ctx, a, name)
	h.SetUp = setup != nil
	pins := map[string]models.AbilityProgram{}
	if setup != nil {
		for _, p := range setup.Programs {
			pins[p.Name] = p
		}
	}
	for _, p := range f.Programs {
		hp := HereProgram{Name: p}
		if pin, ok := pins[p]; ok {
			hp.SetUp, hp.Path, hp.SHA256 = true, pin.Path, pin.SHA256
			sha, err := fileSHA(pin.Path)
			switch {
			case err != nil:
				hp.Says = fmt.Sprintf("%s was set up at %s, which isn't there any more.", p, pin.Path)
			case sha != pin.SHA256:
				hp.Says = fmt.Sprintf("%s changed since it was set up (an update?); a person sets it up again.", p)
			default:
				hp.OK, hp.Says = true, fmt.Sprintf("%s, set up and unchanged.", p)
				paths[p] = pin.Path
			}
		} else if found := lookPath(p); found != "" && hiddenProgram(found, realPath(found)) {
			hp.Says = fmt.Sprintf("%s is inside your home folder, which an ability never reads; install it for the whole computer (Homebrew or your system's packages).", p)
		} else if found != "" && !pinned {
			hp.Path, hp.OK, hp.Says = found, true, fmt.Sprintf("%s is on this computer (not set up for this ability yet).", p)
			paths[p] = found
		} else if found != "" {
			hp.Path, hp.Says = found, fmt.Sprintf("%s is on this computer.", p)
			if setup != nil {
				hp.Says = fmt.Sprintf("%s is on this computer, but it wasn't set up for this ability here (set it up again).", p)
			}
		} else {
			hp.Says = fmt.Sprintf("%s isn't on this computer.", p)
		}
		// A program found but not set up is said once, by the line about
		// setting the ability up (below).
		if !hp.OK && !(pinned && setup == nil && hp.Path != "") {
			h.Problems = append(h.Problems, hp.Says)
		}
		h.Programs = append(h.Programs, hp)
	}
	files := map[string]models.AbilityFilePin{}
	if setup != nil {
		for _, fp := range setup.Files {
			files[fp.Name] = fp
		}
	}
	for _, nf := range f.Files {
		hf := HereFile{Name: nf.Name, Says: nf.Says}
		if pin, ok := files[nf.Name]; ok {
			hf.SetUp, hf.Path, hf.Size, hf.About = true, pin.Path, pin.Size, pin.About
			st, err := os.Stat(pin.Path)
			switch {
			case err != nil:
				hf.State = fmt.Sprintf("The %s file set up at %s isn't there any more.", nf.Name, pin.Path)
			case st.Size() != pin.Size || !st.ModTime().UTC().Truncate(time.Millisecond).Equal(pin.ModTime.UTC().Truncate(time.Millisecond)):
				hf.State = fmt.Sprintf("The %s file changed since it was set up; a person sets it up again.", nf.Name)
			default:
				hf.OK, hf.State = true, fmt.Sprintf("The %s file, set up and unchanged.", nf.Name)
			}
		} else {
			hf.State = fmt.Sprintf("No %s file is set up on this computer yet (%s).", nf.Name, lowerFirst(strings.TrimSuffix(nf.Says, ".")))
		}
		if pinned && !hf.OK {
			h.Problems = append(h.Problems, hf.State)
		}
		h.Files = append(h.Files, hf)
	}
	for _, k := range f.Keys {
		hk := HereKey{Name: k.Name, Env: k.Env, Hosts: k.Hosts}
		if v, _, err := providers.Key(a.Cfg, k.Name); err == nil && v != "" {
			hk.Configured, hk.Masked = true, providers.MaskFor(k.Name, v)
		} else {
			h.Problems = append(h.Problems, fmt.Sprintf("This computer has no %s key yet.", k.Name))
		}
		h.Keys = append(h.Keys, hk)
	}
	if pinned && (len(f.Programs) > 0 || len(f.Files) > 0) && setup == nil {
		h.Problems = append([]string{"A person hasn't set it up on this computer yet."}, h.Problems...)
	}
	h.Ready = len(h.Problems) == 0
	return h, paths
}

func osWords(os []string) []string {
	var out []string
	for _, o := range os {
		switch o {
		case OSMac:
			out = append(out, "a Mac")
		case OSLinux:
			out = append(out, "Linux")
		default:
			out = append(out, o)
		}
	}
	return out
}

// hiddenProgram reports a program inside a folder an ability never sees (the
// person's home, MetaRush's folders): it couldn't run there, and reading its
// folder would open the rest.
func hiddenProgram(paths ...string) bool {
	hide := sandbox.HiddenFor(nil)
	for _, p := range paths {
		if p != "" && sandbox.Hidden(p, hide) {
			return true
		}
	}
	return false
}

func inHomeProblem(name string) error {
	return apperr.New(apperr.InvalidInput, "%s is inside your home folder, which an ability never reads.", name).
		WithHint("Use a copy installed for the whole computer (Homebrew or your system's packages), or name another one.")
}

// pinProgram reads a program a person sets up: a full path to a regular,
// executable file (links followed), and its SHA-256.
func pinProgram(name, path string) (models.AbilityProgram, error) {
	if path == "" {
		path = lookPath(name)
	}
	if path == "" {
		return models.AbilityProgram{}, apperr.New(apperr.NotFound, "%s isn't on this computer.", name).WithHint("Install it, or give its full path.")
	}
	if !filepath.IsAbs(path) {
		return models.AbilityProgram{}, apperr.New(apperr.InvalidInput, "Give %s's full path.", name)
	}
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return models.AbilityProgram{}, apperr.New(apperr.NotFound, "There's no program at %s.", path)
	}
	if hiddenProgram(path, real) {
		return models.AbilityProgram{}, inHomeProblem(name)
	}
	st, err := os.Stat(real)
	if err != nil || !st.Mode().IsRegular() || st.Mode()&0o111 == 0 {
		return models.AbilityProgram{}, apperr.New(apperr.InvalidInput, "%s isn't a program MetaRush can run.", path)
	}
	sha, err := fileSHA(real)
	if err != nil {
		return models.AbilityProgram{}, err
	}
	return models.AbilityProgram{Name: name, Path: path, SHA256: sha, Size: st.Size()}, nil
}

// ---- the bell's people ----

// owners are the people who approve abilities: the workspace's owners (with
// accounts), else the person given (one person on one computer).
func owners(ctx context.Context, a *app.App, fallback string) []string {
	var us []models.User
	_ = a.DB.Find(ctx, store.CUsers, a.WorkspaceID(), bson.M{"role": models.RoleOwner, "state": bson.M{"$ne": models.UserDisabled}}, &us)
	var out []string
	for _, u := range us {
		out = append(out, u.ID)
	}
	if len(out) == 0 && fallback != "" {
		out = []string{fallback}
	}
	return out
}

func now() time.Time { return time.Now().UTC() }

func realPath(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}
