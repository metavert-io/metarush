package abilities

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
)

// The ability.* operations (D230–D235): one family, registered once, so the
// registry, the parity test and the schemas stay the same while abilities
// are data. The CLI (`metarush ability …`), the dashboard (Library →
// Abilities) and Claude Code call the same ones. Approving and taking back
// are for owners, by a person; setting up on a computer is for a person
// there, and adding keys for an owner.

// AbilityRow is one ability as lists and searches show it: its newest
// version, and its newest approved one when that is another.
type AbilityRow struct {
	Name       string    `json:"name"`
	Title      string    `json:"title"`
	Does       string    `json:"does"`
	Version    string    `json:"version" jsonschema:"description=The newest version (its SHA-256)."`
	Number     int       `json:"number"`
	State      string    `json:"state" jsonschema:"enum=waiting,enum=approved,enum=turned_down,enum=revoked,enum=failed"`
	StateLabel string    `json:"stateLabel"`
	CheckedAt  time.Time `json:"checkedAt"`
	// Approved is the newest approved version when it isn't the newest.
	Approved       *AbilityVersionRow `json:"approved,omitempty"`
	Cost           string             `json:"cost" jsonschema:"enum=free,enum=keyed,enum=priced"`
	CostWords      string             `json:"costWords"`
	Sends          []string           `json:"sends"`
	SendsWords     string             `json:"sendsWords"`
	Outputs        []string           `json:"outputs"`
	Network        string             `json:"network" jsonschema:"enum=off,enum=metarush,enum=open"`
	Decides        string             `json:"decides" jsonschema:"enum=safe,enum=priced,enum=keyed,enum=sends,enum=outside,enum=local,enum=likeness"`
	SetUpHere      bool               `json:"setUpHere" jsonschema:"description=A person set it up on this computer."`
	Line           string             `json:"line" jsonschema:"description=One line: what it does\\, whether it is set up here\\, its cost and what leaves."`
	Link           string             `json:"link"`
	VersionsInAll  int                `json:"versionsInAll"`
	WaitingVersion int                `json:"waitingVersion,omitempty" jsonschema:"description=A newer version that waits for an owner (its number)."`
}

// AbilityVersionRow is one version as a list shows it.
type AbilityVersionRow struct {
	Version    string    `json:"version"`
	Number     int       `json:"number"`
	State      string    `json:"state"`
	StateLabel string    `json:"stateLabel"`
	CheckedAt  time.Time `json:"checkedAt"`
	CheckOK    bool      `json:"checkOk"`
	By         string    `json:"by,omitempty"`
}

// AbilityListIn lists the workspace's abilities.
type AbilityListIn struct {
	State string `json:"state,omitempty" jsonschema:"enum=,enum=waiting,enum=approved,enum=turned_down,enum=revoked,enum=failed,description=Only abilities whose newest version is in this state."`
}

// AbilityListOut lists them by name.
type AbilityListOut struct {
	Abilities []AbilityRow `json:"abilities"`
	Waiting   int          `json:"waiting" jsonschema:"description=How many versions wait for an owner."`
	Message   string       `json:"message"`
}

// AbilityFindIn looks for abilities that do something.
type AbilityFindIn struct {
	Words string `json:"words" jsonschema:"minLength=1,maxLength=200,description=What it should do\\, in a few words (stock footage\\, translate captions)."`
	// All: versions not approved yet too (they can't run).
	All   bool `json:"all,omitempty"`
	Limit int  `json:"limit,omitempty" jsonschema:"minimum=1,maximum=8"`
}

// AbilityFindOut is at most eight matches, best first: one line each.
type AbilityFindOut struct {
	Matches []AbilityRow `json:"matches"`
	Message string       `json:"message"`
}

// AbilityRefIn names an ability: name, name@version or a version.
type AbilityRefIn struct {
	Ref string `json:"ref" jsonschema:"minLength=1,description=name\\, name@version (8 or more characters of it) or a version."`
}

// AbilityPageKey is one key on the approval page.
type AbilityPageKey struct {
	Name       string   `json:"name"`
	Env        string   `json:"env" jsonschema:"description=The variable its code reads a stand-in from."`
	Hosts      []string `json:"hosts" jsonschema:"description=The only places MetaRush puts the key in."`
	Place      string   `json:"place" jsonschema:"description=Where in a request it goes\\, in plain words (the Authorization header)."`
	Configured bool     `json:"configured" jsonschema:"description=This computer has the key."`
	Masked     string   `json:"masked,omitempty"`
}

// AbilityPage is what an owner reads before approving a version (D231):
// what it installs, the places it reaches, its keys, what leaves this
// computer, what it costs and its licence, in plain words.
type AbilityPage struct {
	Installs []string         `json:"installs"`
	Hosts    []string         `json:"hosts"`
	Network  string           `json:"network"`
	Keys     []AbilityPageKey `json:"keys"`
	Leaves   string           `json:"leaves"`
	Cost     string           `json:"cost"`
	Licence  string           `json:"licence"`
	// Terms are the service's terms, in the ability's words.
	Terms   string `json:"terms,omitempty"`
	Decides string `json:"decides" jsonschema:"description=Who starts a run."`
	Outside string `json:"outside,omitempty"`
	// Local says the service on this computer it reaches (D284).
	Local  string `json:"local,omitempty"`
	Limits string `json:"limits"`
	Makes  string `json:"makes"`
}

// AbilityShowOut is one version with everything a person or Claude needs.
type AbilityShowOut struct {
	Ability    models.AbilityVersion `json:"ability"`
	StateLabel string                `json:"stateLabel"`
	Page       AbilityPage           `json:"page"`
	Here       Here                  `json:"here"`
	// Local is its local service on this computer, with the program
	// listening there now (D284).
	Local    *HereLocal          `json:"local,omitempty"`
	SetUpOn  []string            `json:"setUpOn" jsonschema:"description=The computers a person set it up on."`
	Versions []AbilityVersionRow `json:"versions"`
	Runs     []AbilityRunRow     `json:"runs" jsonschema:"description=Its latest runs\\, newest first."`
	// CanApprove: its check passed and an owner hasn't approved it.
	CanApprove bool   `json:"canApprove"`
	Message    string `json:"message"`
	Next       string `json:"next,omitempty"`
	Link       string `json:"link"`
}

// AbilityCheckIn checks a folder on this computer.
type AbilityCheckIn struct {
	Path string `json:"path,omitempty" jsonschema:"maxLength=2000,description=The ability's folder on this computer (the one with ability.yaml)."`
	// Version checks a version MetaRush keeps instead of a folder: one
	// brought in from another workspace's ability file (D292).
	Version string `json:"version,omitempty" jsonschema:"maxLength=200,description=name@version: a version MetaRush keeps (one brought in from an ability file) instead of a folder."`
}

// AbilityCheckOut says what the check found.
type AbilityCheckOut struct {
	OK         bool                   `json:"ok"`
	Recorded   bool                   `json:"recorded" jsonschema:"description=The version is kept (it shows under Library → Abilities)."`
	New        bool                   `json:"new" jsonschema:"description=A version MetaRush hadn't seen."`
	Name       string                 `json:"name,omitempty"`
	Title      string                 `json:"title,omitempty"`
	Version    string                 `json:"version,omitempty"`
	Number     int                    `json:"number,omitempty"`
	State      string                 `json:"state,omitempty"`
	StateLabel string                 `json:"stateLabel,omitempty"`
	Problems   []Problem              `json:"problems"`
	Warnings   []string               `json:"warnings"`
	Test       *models.AbilityTestRun `json:"test,omitempty"`
	Message    string                 `json:"message"`
	Next       string                 `json:"next,omitempty"`
	Link       string                 `json:"link,omitempty"`
}

// AbilityApproveIn approves (or turns down) a version.
type AbilityApproveIn struct {
	Ref    string `json:"ref" jsonschema:"minLength=1"`
	Reject bool   `json:"reject,omitempty" jsonschema:"description=Turn it down instead."`
	Note   string `json:"note,omitempty" jsonschema:"maxLength=1000"`
	// OpenNetwork agrees to the computer's whole network for a version whose
	// manifest asks for it (D235).
	OpenNetwork bool `json:"openNetwork,omitempty"`
	// LocalService agrees to the one service on the computer a run happens on
	// that the version's manifest names (D284); LocalProgram is the program
	// the owner saw listening there (ability.show's local.now), refused when
	// another listens by the time they agree.
	LocalService bool                        `json:"localService,omitempty"`
	LocalProgram *models.AbilityLocalProgram `json:"localProgram,omitempty"`
}

// AbilityRevokeIn takes an approval back.
type AbilityRevokeIn struct {
	Ref  string `json:"ref" jsonschema:"minLength=1"`
	Note string `json:"note,omitempty" jsonschema:"maxLength=1000"`
}

// AbilityDecisionOut says what was decided.
type AbilityDecisionOut struct {
	Name       string `json:"name"`
	Title      string `json:"title"`
	Version    string `json:"version"`
	Number     int    `json:"number"`
	State      string `json:"state"`
	StateLabel string `json:"stateLabel"`
	Message    string `json:"message"`
	Link       string `json:"link"`
}

// AbilitySetupIn sets an ability up on this computer: the programs it runs
// (found on this computer, or at the paths given) and, for owners, its keys.
type AbilitySetupIn struct {
	Ref string `json:"ref" jsonschema:"minLength=1"`
	// Programs: a program's name → its full path (default: where this
	// computer finds it).
	Programs map[string]string `json:"programs,omitempty"`
	// Keys: a key's name → the key (owners only; kept in this computer's
	// keychain, never in the database, a log or an answer).
	Keys map[string]string `json:"keys,omitempty"`
	// Files: a needed file's name → its full path on this computer (D285);
	// files not given keep what was set up before.
	Files map[string]string `json:"files,omitempty"`
	// Forget takes the setup off this computer (its keys stay).
	Forget bool `json:"forget,omitempty"`
	// Look only says what each file given is — the path it really has and
	// what MetaRush read it to be — and sets nothing up (D285).
	Look bool `json:"look,omitempty"`
	// AgreeLocal agrees, on this computer, to the program listening on the
	// version's local service now (owners, D284): LocalProgram is the one the
	// owner saw (ability.show's local.now). Without it, setting up again
	// keeps what was agreed.
	AgreeLocal   bool                        `json:"agreeLocal,omitempty"`
	LocalProgram *models.AbilityLocalProgram `json:"localProgram,omitempty"`
}

// HereLocal is an ability's one local service as this computer has it now
// (D284): the program listening there, the one an owner agreed to, and
// whether they are the same.
type HereLocal struct {
	Port    int    `json:"port"`
	Service string `json:"service"`
	// Now is the program listening there now; NowWords says it in full (its
	// paths, the person's own, quoted).
	Now      *models.AbilityLocalProgram `json:"now,omitempty"`
	NowWords string                      `json:"nowWords,omitempty"`
	// Agreed is the one an owner agreed to (set up on this computer, else at
	// approval).
	Agreed      *models.AbilityLocalProgram `json:"agreed,omitempty"`
	AgreedWords string                      `json:"agreedWords,omitempty"`
	Same        bool                        `json:"same"`
	Problem     string                      `json:"problem,omitempty" jsonschema:"description=Why it can't be reached or agreed to now\\, in plain words."`
}

// AbilityFileLook is a file a person is about to give an ability, as
// MetaRush finds it: the ability's words for what it asks, and MetaRush's
// own for what the file is and where it really is.
type AbilityFileLook struct {
	Name  string `json:"name"`
	Says  string `json:"says" jsonschema:"description=What the ability asks for (its words)."`
	Path  string `json:"path" jsonschema:"description=Where the file really is (links followed)."`
	Size  int64  `json:"size"`
	About string `json:"about" jsonschema:"description=What MetaRush read the file to be."`
}

// AbilitySetupOut is what this computer has for it now.
type AbilitySetupOut struct {
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Here    Here     `json:"here"`
	Changed []string `json:"changed"`
	// Files are what the files given are (with look; nothing set up).
	Files []AbilityFileLook `json:"files,omitempty"`
	// Local is its local service on this computer (D284).
	Local   *HereLocal `json:"local,omitempty"`
	Message string     `json:"message"`
}

func init() {
	ops.Def("ability.list", "List the workspace's abilities (capabilities added without a release): what each does, its newest version and whether an owner approved it, whether it is set up on this computer, its cost and what leaves this computer", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AbilityListIn) (*AbilityListOut, error) {
			return listAbilities(ctx, a, in)
		})
	ops.Def("ability.find", "Find abilities for a task in a few words: at most eight, one line each (what it does, whether it is set up here, its cost, what leaves this computer); no AI involved", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AbilityFindIn) (*AbilityFindOut, error) {
			return findAbilities(ctx, a, in)
		})
	ops.Def("ability.show", "Show an ability: its manifest, inputs and notes, the page an owner approves it from (what it installs, the places it reaches, its keys, what leaves this computer, its cost and licence), its versions, what this computer has for it and its latest runs", ops.Flags{},
		func(ctx context.Context, a *app.App, in *AbilityRefIn) (*AbilityShowOut, error) {
			return showAbility(ctx, a, in.Ref)
		})
	ops.Def("ability.check", "Check an ability's folder on this computer: its manifest, no keys or oversized files, and its test run once in the sandbox with the network off; a version that passes waits for an owner to approve it", ops.Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityCheckIn) (*AbilityCheckOut, error) {
			return checkOp(ctx, a, in)
		})
	ops.Def("ability.approve", "Approve a version of an ability for the workspace, or turn it down (owners, people only): approved, members may run it on computers it is set up on", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityApproveIn) (*AbilityDecisionOut, error) {
			return approve(ctx, a, in)
		})
	ops.Def("ability.revoke", "Take an ability's approval back (owners, people only): nothing runs that version again until an owner approves it", ops.Flags{Mutating: true, HumanOnly: true, OwnerOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityRevokeIn) (*AbilityDecisionOut, error) {
			return revoke(ctx, a, in)
		})
	ops.Def("ability.setup", "Set an ability up on this computer (people only, on this computer): the programs it runs, pinned by their SHA-256, and its files and keys (owners)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *AbilitySetupIn) (*AbilitySetupOut, error) {
			return setup(ctx, a, in)
		})
}

// row is a version as lists show it.
func row(ctx context.Context, a *app.App, v *models.AbilityVersion, approved *models.AbilityVersion, setUp map[string]bool) AbilityRow {
	f := v.Facts
	use := v
	if approved != nil {
		use = approved
		f = approved.Facts
	}
	r := AbilityRow{Name: v.Name, Title: use.Title, Does: use.Does, Version: v.Hash, Number: v.Number, State: v.State, StateLabel: StateWords(v.State), CheckedAt: v.Check.At,
		Cost: costClass(f), CostWords: costWords(f), Sends: f.Sends, SendsWords: sendsWords(f), Outputs: f.Outputs, Network: f.Network, Decides: decider(f),
		SetUpHere: setUp[v.Name], Link: links.Ability(v.Name, "")}
	if approved != nil && approved.Hash != v.Hash {
		r.Approved = &AbilityVersionRow{Version: approved.Hash, Number: approved.Number, State: approved.State, StateLabel: StateWords(approved.State), CheckedAt: approved.Check.At, CheckOK: approved.Check.OK}
		if v.State == models.AbilityWaiting {
			r.WaitingVersion = v.Number
		}
	}
	here := "not set up on this computer"
	if len(f.Programs) == 0 {
		here = "needs nothing set up"
	} else if r.SetUpHere {
		here = "set up here"
	}
	cost := "free"
	switch r.Cost {
	case "priced":
		cost = strings.ToLower(r.CostWords[:1]) + r.CostWords[1:]
	case "keyed":
		cost = "the service its key is for may charge"
	}
	r.Line = fmt.Sprintf("%s — %s (%s; %s; %s; %s)", use.Title, strings.TrimSuffix(use.Does, "."), StateWords(stateOf(v, approved)), here, cost, strings.TrimSuffix(lowerFirst(r.SendsWords), "."))
	return r
}

func stateOf(v, approved *models.AbilityVersion) string {
	if approved != nil {
		return approved.State
	}
	return v.State
}

// lowerFirst starts a sentence's words mid-sentence, leaving a name that
// keeps its capitals ("MetaRush's", "ElevenLabs", "API") as it is.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	first, _, _ := strings.Cut(s, " ")
	if len(first) > 1 && strings.IndexFunc(first[1:], unicode.IsUpper) >= 0 {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// setUpHere is which abilities a person set up on this computer.
func setUpHere(ctx context.Context, a *app.App) map[string]bool {
	var ss []models.AbilitySetup
	_ = a.DB.Find(ctx, store.CAbilitySetups, a.WorkspaceID(), bson.M{"installation": a.Cfg.Installation.ID}, &ss)
	out := map[string]bool{}
	for _, s := range ss {
		out[s.Name] = true
	}
	return out
}

func listAbilities(ctx context.Context, a *app.App, in *AbilityListIn) (*AbilityListOut, error) {
	vs, err := allVersions(ctx, a)
	if err != nil {
		return nil, err
	}
	newest, approved, order := latest(vs)
	count := map[string]int{}
	waiting := 0
	for _, v := range vs {
		count[v.Name]++
		if v.State == models.AbilityWaiting {
			waiting++
		}
	}
	setUp := setUpHere(ctx, a)
	out := &AbilityListOut{Abilities: []AbilityRow{}, Waiting: waiting}
	for _, name := range order {
		v := newest[name]
		if in.State != "" && v.State != in.State {
			continue
		}
		r := row(ctx, a, v, approved[name], setUp)
		r.VersionsInAll = count[name]
		out.Abilities = append(out.Abilities, r)
	}
	switch n := len(out.Abilities); {
	case n == 0 && len(order) == 0:
		out.Message = "No abilities yet. Check an ability's folder to add one: metarush ability check <folder>."
	case n == 0:
		out.Message = "No abilities match."
	case n == 1:
		out.Message = "1 ability."
	default:
		out.Message = fmt.Sprintf("%d abilities.", n)
	}
	if waiting > 0 {
		out.Message += fmt.Sprintf(" %d %s for an owner.", waiting, map[bool]string{true: "version waits", false: "versions wait"}[waiting == 1])
	}
	return out, nil
}

// stopWords aren't searched for.
var stopWords = map[string]bool{"a": true, "an": true, "the": true, "of": true, "for": true, "to": true, "and": true, "with": true, "in": true, "on": true, "my": true,
	"me": true, "some": true, "it": true, "that": true, "this": true, "from": true, "by": true, "into": true, "i": true, "we": true, "our": true, "or": true}

// searchWords splits text into lowercase words, without stop words, plural s
// and short endings dropped so "photos" finds "photo".
func searchWords(s string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		if stopWords[w] || w == "" {
			continue
		}
		out = append(out, stem(w))
	}
	return out
}

func stem(w string) string {
	switch {
	case len(w) > 4 && strings.HasSuffix(w, "ies"):
		return w[:len(w)-3] + "y"
	case len(w) > 3 && strings.HasSuffix(w, "s") && !strings.HasSuffix(w, "ss"):
		return w[:len(w)-1]
	}
	return w
}

// score is how well a version matches the words: its name, title and search
// words count most, what it does less.
func score(v *models.AbilityVersion, words []string) int {
	name := searchWords(strings.ReplaceAll(v.Name, "-", " "))
	title := searchWords(v.Title)
	tags := searchWords(strings.Join(v.Search, " "))
	does := searchWords(v.Does)
	has := func(list []string, w string) bool {
		for _, x := range list {
			if x == w || (len(w) >= 4 && strings.HasPrefix(x, w)) {
				return true
			}
		}
		return false
	}
	n := 0
	for _, w := range words {
		switch {
		case has(name, w) || has(title, w) || has(tags, w):
			n += 3
		case has(does, w):
			n++
		}
	}
	return n
}

func findAbilities(ctx context.Context, a *app.App, in *AbilityFindIn) (*AbilityFindOut, error) {
	words := searchWords(in.Words)
	if len(words) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say in a few words what the ability should do.")
	}
	limit := in.Limit
	if limit <= 0 || limit > 8 {
		limit = 8
	}
	vs, err := allVersions(ctx, a)
	if err != nil {
		return nil, err
	}
	newest, approved, order := latest(vs)
	setUp := setUpHere(ctx, a)
	type hit struct {
		r AbilityRow
		n int
	}
	var hits []hit
	for _, name := range order {
		ap := approved[name]
		v := newest[name]
		if ap == nil && !in.All {
			continue
		}
		base := v
		if ap != nil {
			base = ap
		}
		if n := score(base, words); n > 0 {
			hits = append(hits, hit{row(ctx, a, v, ap, setUp), n})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool {
		if hits[i].n != hits[j].n {
			return hits[i].n > hits[j].n
		}
		return hits[i].r.Title < hits[j].r.Title
	})
	out := &AbilityFindOut{Matches: []AbilityRow{}}
	for i, h := range hits {
		if i == limit {
			break
		}
		out.Matches = append(out.Matches, h.r)
	}
	switch n := len(out.Matches); n {
	case 0:
		out.Message = "No approved ability does that."
		if !in.All {
			out.Message += " Versions that wait for an owner aren't counted (--all counts them)."
		}
	case 1:
		out.Message = "1 ability might do it."
	default:
		out.Message = fmt.Sprintf("%d abilities might do it, best first.", n)
	}
	return out, nil
}

// page is the approval page of a version.
func page(ctx context.Context, a *app.App, v *models.AbilityVersion, here Here) AbilityPage {
	f := v.Facts
	p := AbilityPage{Installs: []string{}, Hosts: f.Hosts, Network: networkWords(f), Keys: []AbilityPageKey{}, Leaves: sendsWords(f), Cost: costWords(f),
		Licence: f.Licence, Terms: f.Terms, Decides: deciderWords(decider(f)), Limits: fmt.Sprintf("A run may take up to %d minutes and bring back up to %d MB.", f.Minutes, f.MB)}
	if p.Hosts == nil {
		p.Hosts = []string{}
	}
	var size int64
	programs := 0
	for _, fl := range v.Files {
		size += fl.Size
		if fl.Program {
			programs++
		}
	}
	p.Installs = append(p.Installs, fmt.Sprintf("It runs %s, in this computer's sandbox.", f.Runs))
	if len(f.Programs) > 0 {
		p.Installs = append(p.Installs, fmt.Sprintf("It needs %s on each computer it runs on, set up there by a person (MetaRush pins each program by its fingerprint).", joinAnd(f.Programs)))
	} else {
		p.Installs = append(p.Installs, "It needs no program of this computer's.")
	}
	for _, nf := range f.Files {
		p.Installs = append(p.Installs, fmt.Sprintf("It reads a %s file a person sets up on each computer (%s); MetaRush copies it into each run, read-only.", nf.Name, lowerFirst(strings.TrimSuffix(nf.Says, "."))))
	}
	files := fmt.Sprintf("%d %s, %s", len(v.Files), map[bool]string{true: "file", false: "files"}[len(v.Files) == 1], sizeWords(size))
	if programs > 0 {
		files += fmt.Sprintf(", %d of them %s", programs, map[bool]string{true: "a program", false: "programs"}[programs == 1])
	}
	p.Installs = append(p.Installs, files+".")
	hk := map[string]HereKey{}
	for _, k := range here.Keys {
		hk[k.Name] = k
	}
	for _, k := range f.Keys {
		h := hk[k.Name]
		p.Keys = append(p.Keys, AbilityPageKey{Name: k.Name, Env: k.Env, Hosts: k.Hosts, Place: placeWords(k), Configured: h.Configured, Masked: h.Masked})
	}
	if f.Outside {
		p.Outside = "It changes something outside MetaRush (it publishes, uploads or drives another app)."
	}
	if l := f.Local; l != nil {
		p.Local = fmt.Sprintf("It reaches %s, a program outside MetaRush's sandbox, over plain HTTP: whatever %s can do on this computer, a run may ask it to. Nothing else on this computer or a local network.", localWords(l), l.Service)
		if v.LocalProgram != nil {
			p.Local += fmt.Sprintf(" When it was approved, %s listened there; it reaches only that program.", programWords(*v.LocalProgram))
		} else {
			p.Local += " Approving it notes the program listening there now; it reaches only that program."
		}
	}
	if len(f.Outputs) == 0 {
		p.Makes = "It brings nothing back into MetaRush."
	} else {
		var ks []string
		for _, o := range f.Outputs {
			ks = append(ks, kindWords(o))
		}
		p.Makes = "It may bring back " + joinAnd(ks) + ", each added like a file you add, with how it was made."
		if contains(f.Outputs, OutCaptions) {
			p.Makes = "It may bring back " + joinAnd(ks) + ", each with how it was made; captions stay with the clip they were made from."
		}
	}
	return p
}

// sawProgram refuses an agreement to a program the owner didn't see: the
// one listening now must be the one they were shown (D284).
func sawProgram(l *models.AbilityLocal, now, saw *models.AbilityLocalProgram) error {
	if saw == nil {
		return apperr.New(apperr.Validation, "Say which program on port %d you agree to: look at the ability first. Listening now: %s.", l.Port, identityWords(*now)).
			WithDetails(map[string]any{"listening": now})
	}
	saw.PID = 0
	if !sameProgram(now, saw) {
		return apperr.New(apperr.Conflict, "The program on port %d of this computer isn't the one you looked at: %s. Look again before agreeing.", l.Port, differenceWords(*now, *saw)).
			WithDetails(map[string]any{"listening": now})
	}
	return nil
}

// localHere is a version's local service on this computer now (nil when it
// has none): what listens, what an owner agreed to, whether they match.
func localHere(ctx context.Context, a *app.App, v *models.AbilityVersion) *HereLocal {
	l := v.Facts.Local
	if l == nil {
		return nil
	}
	h := &HereLocal{Port: l.Port, Service: l.Service, Agreed: v.LocalProgram}
	if s, _ := getSetup(ctx, a, v.Name); s != nil && s.LocalProgram != nil {
		h.Agreed = s.LocalProgram
	}
	now, why := localListener(ctx, a, l)
	h.Now, h.Problem = now, why
	if now != nil {
		h.NowWords = identityWords(*now)
		if h.Problem == "" {
			h.Problem = identifiable(now, l)
		}
		if h.Problem == "" {
			h.Problem = probeLocal(ctx, l.Port, l.Probe)
		}
	}
	if h.Agreed != nil {
		h.AgreedWords = identityWords(*h.Agreed)
	}
	h.Same = now != nil && sameProgram(now, h.Agreed)
	return h
}

// placeWords says where in a request a key goes.
func placeWords(k models.AbilityKey) string {
	if k.Query != "" {
		return "the address's " + k.Query + " part"
	}
	return "the " + keyHeader(k) + " header"
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", n>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

func versionRows(vs []models.AbilityVersion) []AbilityVersionRow {
	out := []AbilityVersionRow{}
	for _, v := range vs {
		out = append(out, AbilityVersionRow{Version: v.Hash, Number: v.Number, State: v.State, StateLabel: StateWords(v.State), CheckedAt: v.Check.At, CheckOK: v.Check.OK,
			By: v.By.UserName})
	}
	return out
}

func showAbility(ctx context.Context, a *app.App, ref string) (*AbilityShowOut, error) {
	v, err := resolve(ctx, a, ref, false)
	if err != nil {
		return nil, err
	}
	here, _ := hereFor(ctx, a, v.Facts, v.Name, true)
	vs, err := versionsOf(ctx, a, v.Name)
	if err != nil {
		return nil, err
	}
	out := &AbilityShowOut{Ability: *v, StateLabel: StateWords(v.State), Here: here, SetUpOn: []string{}, Versions: versionRows(vs), Runs: []AbilityRunRow{},
		Link: links.Ability(v.Name, v.Hash)}
	out.Page = page(ctx, a, v, here)
	out.Local = localHere(ctx, a, v)
	if ss, err := setupsOf(ctx, a, v.Name); err == nil {
		for _, s := range ss {
			out.SetUpOn = append(out.SetUpOn, s.Machine)
		}
	}
	if rs, err := runsOf(ctx, a, v.Name, 10); err == nil {
		for i := range rs {
			out.Runs = append(out.Runs, runRow(&rs[i]))
		}
	}
	out.CanApprove = v.Check.OK && v.State != models.AbilityApproved && v.State != models.AbilityFailed
	out.Message = fmt.Sprintf("“%s”, version %d (%s): %s", v.Title, v.Number, StateWords(v.State), v.Does)
	switch {
	case v.State == models.AbilityFailed:
		out.Next = "Fix what its check found, then check its folder again."
	case v.State != models.AbilityApproved:
		out.Next = "An owner reads what it may do, then approves it: metarush ability approve " + v.Name + "@" + Short(v.Hash) + "."
	case !here.Ready:
		out.Next = "To run it here: " + strings.Join(here.Problems, " ")
	default:
		out.Next = "Ready to run here: metarush ability plan " + v.Name + " shows what a run does first."
	}
	return out, nil
}

func checkOp(ctx context.Context, a *app.App, in *AbilityCheckIn) (*AbilityCheckOut, error) {
	var res *checkResult
	var err error
	switch {
	case strings.TrimSpace(in.Path) != "" && strings.TrimSpace(in.Version) != "":
		return nil, apperr.New(apperr.InvalidInput, "Check a folder or a version MetaRush keeps, not both.")
	case strings.TrimSpace(in.Version) != "":
		v, err := resolve(ctx, a, in.Version, false)
		if err != nil {
			return nil, err
		}
		arc, err := loadArchive(ctx, a, v)
		if err != nil {
			return nil, err
		}
		res, err = checkArchive(ctx, a, arc)
		if err != nil {
			return nil, err
		}
	case strings.TrimSpace(in.Path) == "":
		return nil, apperr.New(apperr.InvalidInput, "Say which ability to check: its folder, or a version MetaRush keeps.")
	default:
		if res, err = checkFolder(ctx, a, abs(in.Path)); err != nil {
			return nil, err
		}
	}
	out := &AbilityCheckOut{Problems: res.Problems, Warnings: res.Warnings, Test: res.Test}
	if out.Problems == nil {
		out.Problems = []Problem{}
	}
	if res.Manifest != nil {
		out.Name, out.Title = res.Manifest.Name, res.Manifest.Title
	}
	// A version is kept once its manifest is sound and its test ran (passed
	// or not); manifest problems are only said.
	if res.Manifest == nil || res.Test == nil {
		out.Message = fmt.Sprintf("The ability's folder has %d %s to fix; nothing was kept.", len(out.Problems), map[bool]string{true: "problem", false: "problems"}[len(out.Problems) == 1])
		out.Next = "Fix them and check the folder again."
		return out, nil
	}
	v, isNew, err := record(ctx, a, res)
	if err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx) // kept: the owners are asked whatever the asker does next
	out.Recorded, out.New, out.Version, out.Number, out.State, out.StateLabel, out.Link = true, isNew, v.Hash, v.Number, v.State, StateWords(v.State), links.Ability(v.Name, v.Hash)
	out.OK = v.Check.OK
	a.Audit(ctx, "ability.check", "ability", v.ID, "", map[string]any{"name": v.Name, "version": v.Hash, "number": v.Number, "ok": out.OK, "new": isNew, "machine": a.Cfg.Installation.Name})
	remindApproval(ctx, a, v)
	switch {
	case !out.OK:
		out.Message = fmt.Sprintf("Version %d of “%s” didn't pass its check, so nobody can approve it.", v.Number, v.Title)
		out.Next = "Fix what it found and check the folder again."
	case v.State == models.AbilityApproved:
		out.Message = fmt.Sprintf("Version %d of “%s” passed its check again; it is already approved.", v.Number, v.Title)
	case isNew:
		out.Message = fmt.Sprintf("Version %d of “%s” passed its check and waits for an owner to approve it.", v.Number, v.Title)
		out.Next = "An owner reads what it may do under Library → Abilities and approves it there (or: metarush ability approve " + v.Name + "@" + Short(v.Hash) + ")."
	default:
		out.Message = fmt.Sprintf("Version %d of “%s” passed its check again (%s).", v.Number, v.Title, StateWords(v.State))
	}
	return out, nil
}

// decide records an owner's decision about a version.
func decide(ctx context.Context, a *app.App, v *models.AbilityVersion, state, note string, openNetwork bool, localPort int, prog *models.AbilityLocalProgram) (*models.AbilityVersion, error) {
	d := models.AbilityDecision{State: state, By: a.Actor, At: now(), Note: strings.TrimSpace(note), OpenNetwork: openNetwork, LocalPort: localPort, LocalProgram: prog}
	set := bson.M{"state": state, "updatedAt": now()}
	if state == models.AbilityApproved {
		set["openNetwork"] = openNetwork
		set["localPort"] = localPort
		set["localProgram"] = prog
	}
	res, err := a.DB.C(store.CAbilities).UpdateOne(ctx, bson.M{"_id": v.ID, "workspaceId": a.WorkspaceID(), "state": v.State},
		bson.M{"$set": set, "$push": bson.M{"decisions": d}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone decided about version %d of “%s” at the same time; look at it again.", v.Number, v.Title)
	}
	// Decided: what goes with it (the to-do, the audit) is done even if the
	// person's browser goes away meanwhile.
	ctx = context.WithoutCancel(ctx)
	nv, err := getVersion(ctx, a, v.Hash)
	if err != nil {
		return nil, err
	}
	decided(ctx, a, nv)
	a.Audit(ctx, "ability."+map[string]string{models.AbilityApproved: "approve", models.AbilityTurnedDown: "turn-down", models.AbilityRevoked: "revoke"}[state], "ability", v.ID, "",
		map[string]any{"name": v.Name, "version": v.Hash, "number": v.Number, "note": d.Note, "openNetwork": openNetwork, "localPort": localPort, "localProgram": prog})
	return nv, nil
}

func decisionOut(v *models.AbilityVersion, msg string) *AbilityDecisionOut {
	return &AbilityDecisionOut{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number, State: v.State, StateLabel: StateWords(v.State), Message: msg, Link: links.Ability(v.Name, v.Hash)}
}

func approve(ctx context.Context, a *app.App, in *AbilityApproveIn) (*AbilityDecisionOut, error) {
	v, err := resolve(ctx, a, in.Ref, false)
	if err != nil {
		return nil, err
	}
	if a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto {
		return nil, apperr.New(apperr.Unauthorized, "Claude can't approve an ability; an owner does.")
	}
	if !in.Reject && catalog.IsFirstPartyID(v.Name) {
		return nil, apperr.New(apperr.Validation, "“%s” has the name of one of MetaRush's own abilities (%s), so it can't be approved: a name means one thing everywhere.", v.Title, v.Name).
			WithHint("Ask for a version with another name.")
	}
	if in.Reject {
		if v.State == models.AbilityTurnedDown {
			return decisionOut(v, fmt.Sprintf("Version %d of “%s” is already turned down.", v.Number, v.Title)), nil
		}
		nv, err := decide(ctx, a, v, models.AbilityTurnedDown, in.Note, false, 0, nil)
		if err != nil {
			return nil, err
		}
		return decisionOut(nv, fmt.Sprintf("Version %d of “%s” is turned down: nobody can run it.", nv.Number, nv.Title)), nil
	}
	switch {
	case v.State == models.AbilityApproved:
		return decisionOut(v, fmt.Sprintf("Version %d of “%s” is already approved.", v.Number, v.Title)), nil
	case v.State == models.AbilityImported:
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” came from another workspace and hasn't been checked on a computer here yet, so it can't be approved.", v.Number, v.Title).
			WithHint("Check it here first: its test runs in the sandbox with the network off.").WithCommand("metarush ability check --version " + v.Name + "@" + Short(v.Hash))
	case v.State == models.AbilityFailed || !v.Check.OK:
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” didn't pass its check, so it can't be approved.", v.Number, v.Title).
			WithHint("Fix what the check found and check the folder again.").WithDetails(map[string]any{"problems": v.Check.Problems})
	case v.Facts.Network == NetworkOpen && !in.OpenNetwork:
		return nil, apperr.New(apperr.Validation, "“%s” asks to reach any public place on the internet, not only the ones it names, so approving it also means agreeing to that.", v.Title).
			WithHint("Approve it with the open network agreed to, or ask for a version that goes through MetaRush.").WithCommand("metarush ability approve " + v.Name + "@" + Short(v.Hash) + " --open-network")
	case v.Facts.Local != nil && !in.LocalService:
		return nil, apperr.New(apperr.Validation, "“%s” asks to reach %s, a program outside MetaRush's sandbox, so approving it also means agreeing to that.", v.Title, localWords(v.Facts.Local)).
			WithHint("Approve it with that service agreed to, or turn it down.").WithCommand("metarush ability approve " + v.Name + "@" + Short(v.Hash) + " --local-service")
	}
	localPort := 0
	var prog *models.AbilityLocalProgram
	if l := v.Facts.Local; l != nil {
		if why := ReservedPort(l.Port); why != "" {
			return nil, apperr.New(apperr.Validation, "“%s” asks for port %d of this computer: %s.", v.Title, l.Port, why)
		}
		if ownPort(a, l.Port) || l.Port == a.Cfg.Dashboard.Port {
			return nil, apperr.New(apperr.Validation, "“%s” asks for port %d of this computer, which is MetaRush's own.", v.Title, l.Port)
		}
		// What the owner agrees to is the program listening there now, the
		// one they saw (D284).
		p, why := localListener(ctx, a, l)
		if why != "" {
			return nil, apperr.New(apperr.Validation, "%s MetaRush notes which program it is when an owner approves, so start it first.", why)
		}
		if why := identifiable(p, l); why != "" {
			return nil, apperr.New(apperr.Validation, "%s", why)
		}
		if err := sawProgram(l, p, in.LocalProgram); err != nil {
			return nil, err
		}
		if why := probeLocal(ctx, l.Port, l.Probe); why != "" {
			return nil, apperr.New(apperr.Validation, "%s", why)
		}
		localPort, prog = l.Port, p
	}
	nv, err := decide(ctx, a, v, models.AbilityApproved, in.Note, v.Facts.Network == NetworkOpen && in.OpenNetwork, localPort, prog)
	if err != nil {
		return nil, err
	}
	msg := fmt.Sprintf("Version %d of “%s” is approved: members may run it on computers it is set up on.", nv.Number, nv.Title)
	if prog != nil {
		msg += fmt.Sprintf(" On port %d it may reach only %s.", localPort, programWords(*prog))
	}
	return decisionOut(nv, msg), nil
}

func revoke(ctx context.Context, a *app.App, in *AbilityRevokeIn) (*AbilityDecisionOut, error) {
	v, err := resolve(ctx, a, in.Ref, true)
	if err != nil {
		return nil, err
	}
	if v.State != models.AbilityApproved {
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” isn't approved, so there is nothing to take back.", v.Number, v.Title)
	}
	nv, err := decide(ctx, a, v, models.AbilityRevoked, in.Note, false, 0, nil)
	if err != nil {
		return nil, err
	}
	return decisionOut(nv, fmt.Sprintf("The approval of version %d of “%s” is taken back: nothing runs it until an owner approves it again.", nv.Number, nv.Title)), nil
}

func setup(ctx context.Context, a *app.App, in *AbilitySetupIn) (*AbilitySetupOut, error) {
	v, err := resolve(ctx, a, in.Ref, false)
	if err != nil {
		return nil, err
	}
	if ops.CallerFrom(ctx).Elsewhere {
		return nil, apperr.New(apperr.Unauthorized, "An ability is set up on the computer it runs on, not from another one.").
			WithHint("Open the dashboard on that computer, or set it up in a terminal there.").WithCommand("metarush ability setup " + v.Name)
	}
	out := &AbilitySetupOut{Name: v.Name, Title: v.Title, Changed: []string{}}
	id := setupID(a.WorkspaceID(), a.Cfg.Installation.ID, v.Name)
	if in.Forget {
		res, err := a.DB.C(store.CAbilitySetups).DeleteOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()})
		if err != nil {
			return nil, err
		}
		if res.DeletedCount > 0 {
			out.Changed = append(out.Changed, "forgot the setup")
			a.Audit(ctx, "ability.setup", "ability", v.ID, "", map[string]any{"name": v.Name, "forget": true, "machine": a.Cfg.Installation.Name})
		}
		out.Here, _ = hereFor(ctx, a, v.Facts, v.Name, true)
		out.Message = fmt.Sprintf("“%s” isn't set up on %s any more (its keys stay in Settings → Integrations).", v.Title, a.Cfg.Installation.Name)
		return out, nil
	}
	// Files (D285): only ones it needs, chosen by an owner; with look, only
	// what each is (before anything, keys included, is set up).
	known := map[string]bool{}
	for _, nf := range v.Facts.Files {
		known[nf.Name] = true
	}
	for name := range in.Files {
		if !known[name] {
			return nil, apperr.New(apperr.InvalidInput, "“%s” doesn't need a file called %s.", v.Title, name)
		}
	}
	if len(in.Files) > 0 {
		// A file goes into every run of an approved ability: like a key, an
		// owner chooses it (D285).
		if err := ops.RequireOwner(ctx, a, "ability.setup"); err != nil {
			return nil, err
		}
	}
	if in.Look {
		for _, nf := range v.Facts.Files {
			path := strings.TrimSpace(in.Files[nf.Name])
			if path == "" {
				continue
			}
			fp, err := pinFile(a, nf, path)
			if err != nil {
				return nil, err
			}
			out.Files = append(out.Files, AbilityFileLook{Name: nf.Name, Says: nf.Says, Path: fp.Path, Size: fp.Size, About: fp.About})
		}
		out.Here, _ = hereFor(ctx, a, v.Facts, v.Name, true)
		out.Message = "Nothing is set up yet: this is what each file is and where it really is."
		return out, nil
	}
	// Keys first: owners only, never anywhere but this computer's keychain.
	if len(in.Keys) > 0 {
		if err := ops.RequireOwner(ctx, a, "ability.setup"); err != nil {
			return nil, err
		}
		known := map[string]bool{}
		for _, k := range v.Facts.Keys {
			known[k.Name] = true
		}
		names := make([]string, 0, len(in.Keys))
		for k := range in.Keys {
			names = append(names, k)
		}
		sort.Strings(names)
		for _, name := range names {
			if !known[name] {
				return nil, apperr.New(apperr.InvalidInput, "“%s” doesn't use a key called %s.", v.Title, name)
			}
			val := strings.TrimSpace(in.Keys[name])
			a.Log.Secret(val)
			if _, err := providers.StoreKey(a.Cfg, name, val, false); err != nil {
				return nil, err
			}
			out.Changed = append(out.Changed, "kept the "+name+" key")
		}
	}
	for name := range in.Programs {
		if !contains(v.Facts.Programs, name) {
			return nil, apperr.New(apperr.InvalidInput, "“%s” doesn't run a program called %s.", v.Title, name)
		}
	}
	var progs []models.AbilityProgram
	for _, name := range v.Facts.Programs {
		p, err := pinProgram(name, strings.TrimSpace(in.Programs[name]))
		if err != nil {
			return nil, err
		}
		progs = append(progs, p)
	}
	if progs == nil {
		progs = []models.AbilityProgram{}
	}
	// Files (D285): the ones given now, pinned; the rest as set up before.
	before, _ := getSetup(ctx, a, v.Name)
	var files []models.AbilityFilePin
	for _, nf := range v.Facts.Files {
		if path := strings.TrimSpace(in.Files[nf.Name]); path != "" {
			fp, err := pinFile(a, nf, path)
			if err != nil {
				return nil, err
			}
			files = append(files, fp)
			continue
		}
		if before != nil {
			for _, fp := range before.Files {
				if fp.Name == nf.Name {
					files = append(files, fp)
				}
			}
		}
	}
	// The program on this computer's port for its local service (D284):
	// kept as agreed, unless an owner agrees here to the one listening now,
	// which they saw.
	var localProg *models.AbilityLocalProgram
	if before != nil {
		localProg = before.LocalProgram
	}
	if in.AgreeLocal {
		l := v.Facts.Local
		if l == nil {
			return nil, apperr.New(apperr.InvalidInput, "“%s” reaches no service on this computer.", v.Title)
		}
		if err := ops.RequireOwner(ctx, a, "ability.setup"); err != nil {
			return nil, err
		}
		p, why := localListener(ctx, a, l)
		if why != "" {
			return nil, apperr.New(apperr.Validation, "%s", why)
		}
		if why := identifiable(p, l); why != "" {
			return nil, apperr.New(apperr.Validation, "%s", why)
		}
		if err := sawProgram(l, p, in.LocalProgram); err != nil {
			return nil, err
		}
		if why := probeLocal(ctx, l.Port, l.Probe); why != "" {
			return nil, apperr.New(apperr.Validation, "%s", why)
		}
		if !sameProgram(localProg, p) {
			out.Changed = append(out.Changed, fmt.Sprintf("agreed to %s on port %d of this computer", programWords(*p), l.Port))
		}
		localProg = p
	}
	s := models.AbilitySetup{ID: id, WorkspaceID: a.WorkspaceID(), Name: v.Name, Installation: a.Cfg.Installation.ID, Machine: a.Cfg.Installation.Name,
		Programs: progs, Files: files, LocalProgram: localProg, By: a.Actor, At: now()}
	if _, err := a.DB.C(store.CAbilitySetups).ReplaceOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, s, optionsUpsert()); err != nil {
		return nil, err
	}
	ctx = context.WithoutCancel(ctx) // set up: the audit is written whatever the asker does next
	for _, p := range progs {
		out.Changed = append(out.Changed, fmt.Sprintf("set up %s (%s)", p.Name, p.Path))
	}
	for _, fp := range files {
		if in.Files[fp.Name] != "" {
			out.Changed = append(out.Changed, fmt.Sprintf("set up the %s file (%s, %s)", fp.Name, fp.Path, sizeWords(fp.Size)))
		}
	}
	var keys []string
	for k := range in.Keys {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	a.Audit(ctx, "ability.setup", "ability", v.ID, "", map[string]any{"name": v.Name, "programs": progs, "files": files, "keys": keys, "machine": a.Cfg.Installation.Name})
	out.Here, _ = hereFor(ctx, a, v.Facts, v.Name, true)
	out.Local = localHere(ctx, a, v)
	if out.Here.Ready {
		out.Message = fmt.Sprintf("“%s” is set up on %s.", v.Title, a.Cfg.Installation.Name)
	} else {
		out.Message = fmt.Sprintf("“%s” is set up on %s, but a run still needs this: %s", v.Title, a.Cfg.Installation.Name, strings.Join(out.Here.Problems, " "))
	}
	return out, nil
}
