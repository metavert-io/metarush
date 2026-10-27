// Package abilities is MetaRush's way to add a capability without a release
// (D230–D235): an ability is a folder with an ability.yaml that says what it
// does, what it needs and what it may reach, plus the script or program it
// runs. Its version is the SHA-256 of a canonical archive of the folder
// (D230); `ability check` reads the manifest, refuses secrets and oversized
// files and runs its test in the operating system's sandbox with the network
// off (D233); an owner approves each version after a page that says what it
// installs, which hosts and keys it uses, what leaves the computer, what it
// costs and its licence, and a person sets it up on each computer (D231);
// runs are jobs pinned to the computer that asked, in the sandbox, their
// network only through MetaRush's proxy, which adds keys the code never sees,
// and only the outputs it declared come back, through the one classifier,
// with how they were made (D232, D234).
//
// The manifest is a contract MetaRush enforces at run time, not a promise:
// hosts, keys, the network, the outputs and the limits are what the sandbox
// and the proxy allow, whatever the code does.
package abilities

import (
	"bytes"
	"fmt"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/providers"
	"net"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/invopop/jsonschema"
	"gopkg.in/yaml.v3"
)

// ManifestFile is the manifest's name in an ability's folder; NotesFile the
// optional notes on how to use it well; TestFolder its test inputs.
const (
	ManifestFile = "ability.yaml"
	NotesFile    = "notes.md"
	TestFolder   = "test"
	// FormatVersion is the manifest format this MetaRush reads (ability: 1).
	FormatVersion = 1
	NameMax       = 40
	// NotesMax is how much of notes.md `ability show` returns.
	NotesMax = 8 << 10
)

// Kinds of input.
const (
	InWords   = "words"
	InNumber  = "number"
	InChoice  = "choice"
	InYesNo   = "yes-no"
	InFootage = "footage"
	InPicture = "picture"
	InSound   = "sound"
)

// Kinds of output: each goes through the one classifier (D80).
const (
	OutFootage  = "footage"
	OutSound    = "sound"
	OutPicture  = "picture"
	OutDocument = "document"
	OutFont     = "font"
	// OutCaptions is a captions file (SRT or WebVTT) kept with the clip the
	// run was given (D285).
	OutCaptions = "captions"
)

// What can leave this computer (sends).
const (
	SendsNothing  = "nothing"
	SendsWords    = "words"
	SendsPictures = "pictures"
	SendsSound    = "sound"
	SendsFootage  = "footage"
)

// Network modes, as the manifest says them.
const (
	NetworkOff      = "off"
	NetworkMetaRush = "metarush" // HTTP(S) only through MetaRush, to the hosts it declared
	NetworkOpen     = "open"     // any public host, still through MetaRush, no keys: a separate, people-only approval (D235)
)

// OSes.
const (
	OSMac   = "mac"
	OSLinux = "linux"
)

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	inputRe   = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	keyNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	envRe     = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,63}$`)
	programRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	hostRe    = regexp.MustCompile(`^(\*\.)?([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z][a-z0-9-]{0,62}$`)
	wordRe    = regexp.MustCompile(`^[\p{L}\p{N}][\p{L}\p{N} +&'-]{0,39}$`)
	headerRe  = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9-]{0,63}$`)
	queryRe   = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
)

// ValidName reports whether n can name an ability.
func ValidName(n string) bool { return len(n) <= NameMax && nameRe.MatchString(n) }

// YesNo is a yes-or-no setting a manifest may write as yes/no or true/false.
type YesNo bool

// UnmarshalYAML reads yes, no, true, false (any case).
func (y *YesNo) UnmarshalYAML(n *yaml.Node) error {
	switch strings.ToLower(strings.TrimSpace(n.Value)) {
	case "yes", "true", "on":
		*y = true
	case "no", "false", "off", "":
		*y = false
	default:
		return fmt.Errorf("line %d: say yes or no, not %q", n.Line, n.Value)
	}
	return nil
}

// JSONSchema says yes/no or true/false.
func (YesNo) JSONSchema() *jsonschema.Schema {
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: "boolean"}, {Type: "string", Enum: []any{"yes", "no"}}}}
}

// Input is one thing a run takes.
type Input struct {
	Kind string `yaml:"kind" json:"kind" jsonschema:"enum=words,enum=number,enum=choice,enum=yes-no,enum=footage,enum=picture,enum=sound"`
	// Says is what to put in, in plain words (the run dialog's label).
	Says    string   `yaml:"says,omitempty" json:"says,omitempty" jsonschema:"maxLength=200"`
	Min     *float64 `yaml:"min,omitempty" json:"min,omitempty"`
	Max     *float64 `yaml:"max,omitempty" json:"max,omitempty" jsonschema:"description=Numbers: the largest value. Words: the most characters."`
	Default any      `yaml:"default,omitempty" json:"default,omitempty"`
	// Options are a choice's values.
	Options []string `yaml:"options,omitempty" json:"options,omitempty" jsonschema:"maxItems=40"`
	// Optional: a run may leave it out (an input without a default is
	// required otherwise).
	Optional bool `yaml:"optional,omitempty" json:"optional,omitempty"`
}

// Cost is what a run costs: free, or a dated price per unit.
type Cost struct {
	Free bool `yaml:"-" json:"free,omitempty"`
	// Service is who charges (the outside service).
	Service string  `yaml:"service,omitempty" json:"service,omitempty" jsonschema:"maxLength=80"`
	USD     float64 `yaml:"usd,omitempty" json:"usd,omitempty" jsonschema:"minimum=0,description=The price of one unit in US dollars."`
	// Per names the unit (picture, second, request, run).
	Per string `yaml:"per,omitempty" json:"per,omitempty" jsonschema:"maxLength=40"`
	// From is the number input that says how many units a run uses; without
	// it a run is one unit.
	From string `yaml:"from,omitempty" json:"from,omitempty"`
	// AsOf is the day the price was read (YYYY-MM-DD).
	AsOf string `yaml:"as-of,omitempty" json:"asOf,omitempty" jsonschema:"pattern=^\\d{4}-\\d{2}-\\d{2}$"`
}

// UnmarshalYAML reads "free" or a priced mapping.
func (c *Cost) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		if strings.EqualFold(strings.TrimSpace(n.Value), "free") {
			*c = Cost{Free: true}
			return nil
		}
		return fmt.Errorf("line %d: cost is free, or a price (usd, per, as-of)", n.Line)
	}
	type plain Cost
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*c = Cost(p)
	return nil
}

// JSONSchema says "free" or a price.
func (Cost) JSONSchema() *jsonschema.Schema {
	price := &jsonschema.Schema{Type: "object", AdditionalProperties: jsonschema.FalseSchema, Properties: jsonschema.NewProperties(), Required: []string{"usd", "per", "as-of"}}
	price.Properties.Set("service", &jsonschema.Schema{Type: "string", MaxLength: u64(80)})
	price.Properties.Set("usd", &jsonschema.Schema{Type: "number", Minimum: "0"})
	price.Properties.Set("per", &jsonschema.Schema{Type: "string", MaxLength: u64(40)})
	price.Properties.Set("from", &jsonschema.Schema{Type: "string"})
	price.Properties.Set("as-of", &jsonschema.Schema{Type: "string", Pattern: `^\d{4}-\d{2}-\d{2}$`})
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: "string", Enum: []any{"free"}}, price}}
}

func u64(n uint64) *uint64 { return &n }

// Key is a key a run uses: kept on each computer (Settings → Integrations, or
// `metarush ability setup`), never in the code's reach. The code reads a
// stand-in from Env and puts it in one place — a header (Authorization
// unless it says), or a query parameter; MetaRush's proxy puts the real key
// there, only on requests to Hosts, and refuses the stand-in anywhere else.
type Key struct {
	Name string `yaml:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	// Env is the variable the code reads it from (default NAME_API_KEY).
	Env string `yaml:"env,omitempty" json:"env,omitempty" jsonschema:"pattern=^[A-Z][A-Z0-9_]{0\\,63}$"`
	// Hosts it goes to (default: every host the ability declares).
	Hosts []string `yaml:"hosts,omitempty" json:"hosts,omitempty"`
	// Header is the request header it goes in (default Authorization);
	// Query the query parameter instead.
	Header string `yaml:"header,omitempty" json:"header,omitempty"`
	Query  string `yaml:"query,omitempty" json:"query,omitempty"`
}

// UnmarshalYAML reads a key's name alone or the full form.
func (k *Key) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		*k = Key{Name: strings.TrimSpace(n.Value)}
		return nil
	}
	type plain Key
	var p plain
	if err := n.Decode(&p); err != nil {
		return err
	}
	*k = Key(p)
	return nil
}

// JSONSchema says a key's name alone or the full form.
func (Key) JSONSchema() *jsonschema.Schema {
	full := &jsonschema.Schema{Type: "object", AdditionalProperties: jsonschema.FalseSchema, Properties: jsonschema.NewProperties(), Required: []string{"name"}}
	full.Properties.Set("name", &jsonschema.Schema{Type: "string", Pattern: `^[a-z][a-z0-9-]{0,39}$`})
	full.Properties.Set("env", &jsonschema.Schema{Type: "string", Pattern: `^[A-Z][A-Z0-9_]{0,63}$`, Description: "The variable the code reads the key's stand-in from (default NAME_API_KEY)."})
	full.Properties.Set("hosts", &jsonschema.Schema{Type: "array", Items: &jsonschema.Schema{Type: "string"}, Description: "The hosts it goes to (default: every host)."})
	full.Properties.Set("header", &jsonschema.Schema{Type: "string", Pattern: headerRe.String(), Description: "The request header it goes in (default Authorization)."})
	full.Properties.Set("query", &jsonschema.Schema{Type: "string", Pattern: queryRe.String(), Description: "The query parameter it goes in, instead of a header."})
	return &jsonschema.Schema{OneOf: []*jsonschema.Schema{{Type: "string", Pattern: `^[a-z][a-z0-9-]{0,39}$`}, full}}
}

// EnvName is the variable the code reads the key's stand-in from.
func (k Key) EnvName() string {
	if k.Env != "" {
		return k.Env
	}
	return strings.ToUpper(strings.ReplaceAll(k.Name, "-", "_")) + "_API_KEY"
}

// Needs is what a computer must have.
type Needs struct {
	// Programs it runs (python3, node, …): a person sets each up per
	// computer, pinned by SHA-256 (D231).
	Programs []string `yaml:"programs,omitempty" json:"programs,omitempty" jsonschema:"maxItems=8"`
	OS       []string `yaml:"os,omitempty" json:"os,omitempty" jsonschema:"maxItems=2"`
	// Files it reads that a person sets up on each computer (D285): a
	// model, for instance. Pinned by SHA-256 and copied into the run's room,
	// read-only, for each run.
	Files []NeededFile `yaml:"files,omitempty" json:"files,omitempty" jsonschema:"maxItems=4"`
}

// NeededFile is a file a person sets up for an ability on each computer
// (D285); the run reads its copy at $METARUSH_FILE_<NAME>.
type NeededFile struct {
	Name string `yaml:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	// Says what file to give, in plain words.
	Says string `yaml:"says" json:"says" jsonschema:"minLength=1,maxLength=200"`
	// Kind is a kind of file MetaRush checks by its first bytes at setup
	// (whisper-model; whisper-translator: one that can translate).
	Kind string `yaml:"kind,omitempty" json:"kind,omitempty" jsonschema:"enum=,enum=whisper-model,enum=whisper-translator"`
}

// FileEnv is the variable a needed file's copy is at: METARUSH_FILE_WHISPER_MODEL.
func FileEnv(name string) string {
	return "METARUSH_FILE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))
}

// Limits bound one run.
type Limits struct {
	Minutes int `yaml:"minutes,omitempty" json:"minutes,omitempty" jsonschema:"minimum=1,maximum=60,description=How long a run may take (default 10)."`
	MB      int `yaml:"mb,omitempty" json:"mb,omitempty" jsonschema:"minimum=1,maximum=20000,description=How much it may bring back in all (default 2000)."`
}

// LocalService is the one program on the computer a run happens on that an
// ability may reach (D284): plain HTTP to one port of this computer's
// loopback (ComfyUI on 8188), through the run's proxy like every request,
// only once an owner agreed to it when approving the version. Nothing else on
// this computer, or on a local network, is ever reachable.
type LocalService struct {
	Port int `yaml:"port" json:"port" jsonschema:"minimum=1024,maximum=32767"`
	// Service is what listens there, in a few words (ComfyUI).
	Service string `yaml:"service" json:"service" jsonschema:"minLength=1,maxLength=60"`
	// Probe is a request that tells the service apart from anything else
	// on the port: MetaRush asks it when an owner approves and before each
	// run.
	Probe *LocalProbe `yaml:"probe,omitempty" json:"probe,omitempty"`
}

// LocalProbe is a GET of Path that must answer 200 in JSON with each Expect
// field (dotted: system.comfyui_version).
type LocalProbe struct {
	Path   string   `yaml:"path" json:"path" jsonschema:"pattern=^/[A-Za-z0-9._~/?=&%-]{0\\,199}$"`
	Expect []string `yaml:"expect" json:"expect" jsonschema:"minItems=1,maxItems=8"`
}

var probeKeyRe = regexp.MustCompile(`^[A-Za-z0-9_-]+(\.[A-Za-z0-9_-]+)*$`)

// ReservedPort says why a port of this computer is never an ability's
// local service ("" when it may be): MetaRush's own dashboard and daemon
// ports and the databases' (dashboard.port is checked again at run time).
func ReservedPort(port int) string {
	switch {
	case port < 1024 || port > 65535:
		return "use a port from 1024 to 65535"
	case port >= 32768:
		return "ports from 32768 up are handed out for a moment to whatever asks (MetaRush's own media and speech servers among them); a service of yours keeps a lower port"
	case port >= 4840 && port <= 4899:
		return "that is MetaRush's own dashboard or daemon, or another installation's"
	case port >= 3000 && port <= 3100:
		return "that is where MetaRush's video renderer serves its pages"
	case port >= 27017 && port <= 27019, port >= 27150 && port <= 27199:
		return "that is a database's port"
	case controlPorts[port] != "":
		return "that is usually " + controlPorts[port] + ", which would hand over this computer or its data"
	}
	return ""
}

// controlPorts are well-known ports of services that control this computer
// or hold data: never an ability's local service, whatever its manifest
// calls it.
var controlPorts = map[int]string{
	2375: "Docker's control port", 2376: "Docker's control port", 2379: "etcd", 2380: "etcd", 3306: "MySQL", 5432: "PostgreSQL",
	5984: "CouchDB", 6379: "Redis", 6443: "Kubernetes' control port", 8200: "Vault", 8500: "Consul", 9200: "Elasticsearch",
	9222: "a browser's remote control", 9229: "Node.js's debugger", 10250: "Kubernetes' node agent", 11211: "memcached",
}

// Test is how `ability check` runs it once, with the network off.
type Test struct {
	Inputs map[string]any `yaml:"inputs,omitempty" json:"inputs,omitempty"`
}

// Manifest is ability.yaml (D230). Its JSON Schema is generated from this
// struct (schema/ability.schema.json).
type Manifest struct {
	Ability int    `yaml:"ability" json:"ability" jsonschema:"enum=1,description=The manifest's format (1)."`
	Name    string `yaml:"name" json:"name" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$"`
	Title   string `yaml:"title" json:"title" jsonschema:"minLength=1,maxLength=80"`
	// Does says what it does for the video, in plain words (one sentence).
	Does string `yaml:"does" json:"does" jsonschema:"minLength=1,maxLength=300"`
	// Runs is the program and its fixed arguments: "python3 run.py", "node
	// run.mjs" or "./tool" (a file in the folder). No shell.
	Runs     string           `yaml:"runs" json:"runs" jsonschema:"minLength=1,maxLength=300"`
	Inputs   map[string]Input `yaml:"inputs,omitempty" json:"inputs,omitempty"`
	Outputs  []string         `yaml:"outputs,omitempty" json:"outputs,omitempty" jsonschema:"uniqueItems=true"`
	Provides []string         `yaml:"provides,omitempty" json:"provides,omitempty" jsonschema:"maxItems=8,description=Kinds of AI work it does once approved: picture\\, voice\\, voice-change\\, sound-effect\\, music\\, background-removal\\, upscale."`
	Cost     Cost             `yaml:"cost" json:"cost"`
	Keys     []Key            `yaml:"keys,omitempty" json:"keys,omitempty" jsonschema:"maxItems=8"`
	Hosts    []string         `yaml:"hosts,omitempty" json:"hosts,omitempty" jsonschema:"maxItems=32"`
	// Network: off (default without hosts), metarush (default with hosts:
	// HTTP(S) only through MetaRush to the hosts), open (HTTP(S) to any public
	// host, still through MetaRush, never this computer or a local network;
	// people only, approved on its own).
	Network string `yaml:"network,omitempty" json:"network,omitempty" jsonschema:"enum=off,enum=metarush,enum=open"`
	// Local is the one service on this computer it may reach (D284), once an
	// owner agrees.
	Local *LocalService `yaml:"local,omitempty" json:"local,omitempty"`
	// Sends is what leaves this computer.
	Sends          []string `yaml:"sends,omitempty" json:"sends,omitempty" jsonschema:"uniqueItems=true"`
	ChangesOutside YesNo    `yaml:"changes-outside,omitempty" json:"changesOutside,omitempty" jsonschema:"description=It publishes\\, uploads or drives another app: people only."`
	Likeness       YesNo    `yaml:"likeness,omitempty" json:"likeness,omitempty" jsonschema:"description=It makes someone's likeness (needs consent)."`
	Needs          Needs    `yaml:"needs,omitempty" json:"needs,omitempty"`
	Licence        string   `yaml:"licence" json:"licence" jsonschema:"minLength=1,maxLength=80"`
	// Terms are a service's terms a person should know before using it (its
	// API free only for non-commercial use, for instance): shown on the
	// approval page, in the approval question and on every plan (D283).
	Terms  string   `yaml:"terms,omitempty" json:"terms,omitempty" jsonschema:"maxLength=300"`
	Search []string `yaml:"search,omitempty" json:"search,omitempty" jsonschema:"maxItems=20"`
	Limits Limits   `yaml:"limits,omitempty" json:"limits,omitempty"`
	Test   *Test    `yaml:"test,omitempty" json:"test,omitempty"`
}

// Item kinds, in the order they are listed.
var (
	InputKinds  = []string{InWords, InNumber, InChoice, InYesNo, InFootage, InPicture, InSound}
	OutputKinds = []string{OutFootage, OutSound, OutPicture, OutDocument, OutFont, OutCaptions}
	SendsKinds  = []string{SendsNothing, SendsWords, SendsPictures, SendsSound, SendsFootage}
)

// A file input's kind and what it sends when it leaves.
var fileInputSends = map[string]string{InFootage: SendsFootage, InPicture: SendsPictures, InSound: SendsSound}

// A key named after one of MetaRush's own integrations goes only to that
// integration's hosts, as the integration itself declares them
// (providers.AbilityKeyHosts): a private key (Google Cloud's) or an
// integration that names no hosts is never lent (D235, D282).

// Problem is something wrong with a manifest or a folder, in plain words.
type Problem struct {
	Field string `json:"field,omitempty" jsonschema:"description=Where (a manifest field or a file)."`
	Text  string `json:"text"`
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.Text
	}
	return p.Field + ": " + p.Text
}

// ParseManifest reads ability.yaml strictly: unknown fields are refused.
func ParseManifest(b []byte) (*Manifest, error) {
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	var m Manifest
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s isn't a manifest MetaRush can read: %s", ManifestFile, yamlWords(err))
	}
	return &m, nil
}

func yamlWords(err error) string {
	s := strings.TrimPrefix(err.Error(), "yaml: ")
	s = strings.ReplaceAll(s, "unmarshal errors:\n  ", "")
	s = strings.ReplaceAll(s, " not found in type abilities.plain", " isn't a field a manifest has")
	s = strings.ReplaceAll(s, " not found in type abilities.Manifest", " isn't a field a manifest has")
	return strings.TrimSpace(s)
}

// NetworkMode is the manifest's network, with its default.
func (m *Manifest) NetworkMode() string {
	if m.Network != "" {
		return m.Network
	}
	if len(m.Hosts) > 0 || m.Local != nil {
		return NetworkMetaRush
	}
	return NetworkOff
}

// LeavesComputer reports a run that may send something off this computer
// (declared hosts or an open network; a service on this computer alone isn't).
func (m *Manifest) LeavesComputer() bool {
	return m.NetworkMode() == NetworkOpen || (m.NetworkMode() == NetworkMetaRush && len(m.Hosts) > 0)
}

// Sending is what leaves this computer, with its default (nothing when the
// network is off).
func (m *Manifest) Sending() []string {
	if m.NetworkMode() == NetworkOff {
		return []string{SendsNothing}
	}
	if len(m.Sends) == 0 {
		return []string{SendsNothing}
	}
	return m.Sends
}

// SendsAway reports whether anything other than words leaves the computer.
func (m *Manifest) SendsAway() bool {
	for _, s := range m.Sending() {
		if s == SendsPictures || s == SendsSound || s == SendsFootage {
			return true
		}
	}
	return false
}

// MinutesLimit and BytesLimit are a run's limits, with their defaults.
func (m *Manifest) MinutesLimit() int {
	if m.Limits.Minutes > 0 {
		return m.Limits.Minutes
	}
	return 10
}

func (m *Manifest) BytesLimit() int64 {
	mb := m.Limits.MB
	if mb <= 0 {
		mb = 2000
	}
	return int64(mb) << 20
}

// InputNames are the inputs in a stable order.
func (m *Manifest) InputNames() []string {
	names := make([]string, 0, len(m.Inputs))
	for n := range m.Inputs {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Argv splits Runs into words (no shell: spaces separate, double quotes
// group).
func (m *Manifest) Argv() ([]string, error) { return splitWords(m.Runs) }

func splitWords(s string) ([]string, error) {
	var out []string
	var cur strings.Builder
	in, has := false, false
	for _, r := range s {
		switch {
		case r == '"':
			in, has = !in, true
		case (r == ' ' || r == '\t') && !in:
			if has {
				out = append(out, cur.String())
				cur.Reset()
				has = false
			}
		case r == '\n' || r == '\r':
			return nil, fmt.Errorf("runs is one line")
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if in {
		return nil, fmt.Errorf("a quote in runs isn't closed")
	}
	if has {
		out = append(out, cur.String())
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("runs is empty")
	}
	return out, nil
}

// Program is the program a run starts: a program set up on the computer
// (python3), or a file of the folder (./tool) — then Local.
func (m *Manifest) Program() (name string, local bool, args []string, err error) {
	argv, err := m.Argv()
	if err != nil {
		return "", false, nil, err
	}
	first := argv[0]
	if strings.HasPrefix(first, "./") {
		return strings.TrimPrefix(first, "./"), true, argv[1:], nil
	}
	return first, false, argv[1:], nil
}

// Validate checks the manifest on its own; files says which files the folder
// has (path → executable) for the checks that look at them.
func (m *Manifest) Validate(files map[string]bool) []Problem {
	var ps []Problem
	add := func(field, f string, a ...any) { ps = append(ps, Problem{Field: field, Text: fmt.Sprintf(f, a...)}) }
	if m.Ability != FormatVersion {
		add("ability", "say ability: %d (the manifest's format)", FormatVersion)
	}
	if !ValidName(m.Name) {
		add("name", "use lowercase letters, digits and dashes, starting with a letter, at most %d characters (like stock-footage)", NameMax)
	}
	if t := strings.TrimSpace(m.Title); t == "" || len(t) > 80 {
		add("title", "give it a short title (at most 80 characters)")
	}
	if d := strings.TrimSpace(m.Does); d == "" || len(d) > 300 {
		add("does", "say in one sentence what it does for the video (at most 300 characters)")
	}
	if strings.TrimSpace(m.Licence) == "" {
		add("licence", "say its licence (MIT, Apache-2.0, proprietary…)")
	}
	if len(m.Terms) > 300 || strings.ContainsAny(m.Terms, "\n\r") {
		add("terms", "say a service's terms in one line of at most 300 characters")
	}
	// What it runs.
	name, local, args, err := m.Program()
	switch {
	case err != nil:
		add("runs", "%s", err)
	case local:
		if !programRe.MatchString(strings.ReplaceAll(name, "/", "_")) || strings.Contains(name, "..") {
			add("runs", "the program ./%s isn't a plain file of the folder", name)
		} else if exec, ok := files[name]; files != nil && !ok {
			add("runs", "the folder has no %s", name)
		} else if files != nil && !exec {
			add("runs", "%s isn't marked as a program (chmod +x)", name)
		}
	default:
		if !programRe.MatchString(name) || strings.Contains(name, "/") {
			add("runs", "%q isn't a program MetaRush can set up: name one like python3 or node, or a file of the folder (./tool)", name)
		} else if !contains(m.Needs.Programs, name) {
			add("runs", "it runs %s, so needs.programs must list it", name)
		}
		for _, a := range args {
			if strings.HasPrefix(a, "-") && (name == "python3" || name == "node") && (a == "-c" || a == "-e" || a == "--eval" || a == "-p" || a == "--print") {
				add("runs", "run a file of the folder, not code given on the command line (%s)", a)
			}
		}
	}
	for _, p := range m.Needs.Programs {
		if !programRe.MatchString(p) {
			add("needs.programs", "%q isn't a program's name", p)
		}
	}
	for _, o := range m.Needs.OS {
		if o != OSMac && o != OSLinux {
			add("needs.os", "say mac or linux, not %q", o)
		}
	}
	seenFile := map[string]bool{}
	for i, nf := range m.Needs.Files {
		f := fmt.Sprintf("needs.files[%d]", i)
		switch {
		case !keyNameRe.MatchString(nf.Name):
			add(f, "name files with lowercase letters, digits and dashes (like whisper-model)")
		case seenFile[nf.Name]:
			add(f, "%s twice", nf.Name)
		case strings.TrimSpace(nf.Says) == "" || len(nf.Says) > 200:
			add(f, "say what file to give for %s, in a few words", nf.Name)
		case nf.Kind != "" && !contains(FileKinds, nf.Kind):
			add(f, "kind is %s, or leave it out", strings.Join(FileKinds, " or "))
		}
		seenFile[nf.Name] = true
	}
	if len(m.Needs.Files) > 4 {
		add("needs.files", "at most 4 files")
	}
	// Inputs.
	if len(m.Inputs) > 20 {
		add("inputs", "at most 20 inputs")
	}
	for _, n := range m.InputNames() {
		in := m.Inputs[n]
		f := "inputs." + n
		if !inputRe.MatchString(n) {
			add(f, "name inputs with lowercase letters, digits and underscores (like words or max_count)")
		}
		if !contains(InputKinds, in.Kind) {
			add(f, "kind is one of %s", strings.Join(InputKinds, ", "))
			continue
		}
		if in.Kind == InChoice && len(in.Options) == 0 {
			add(f, "a choice needs its options")
		}
		if in.Min != nil && in.Max != nil && *in.Min > *in.Max {
			add(f, "min is larger than max")
		}
		if in.Default != nil {
			if _, err := in.Coerce(in.Default); err != nil {
				add(f, "its default: %s", err)
			}
		}
		if s, ok := fileInputSends[in.Kind]; ok && m.LeavesComputer() && !contains(m.Sends, s) {
			add(f, "it takes %s and reaches the network, so sends must say whether %s leave this computer (add %s to sends; MetaRush refuses to send more than a little text otherwise)", in.Kind, s, s)
		}
	}
	// Outputs.
	for _, o := range m.Outputs {
		if !contains(OutputKinds, o) {
			add("outputs", "%q isn't something MetaRush takes in: say %s", o, strings.Join(OutputKinds, ", "))
		}
	}
	if contains(m.Outputs, OutCaptions) {
		clips := 0
		for _, in := range m.Inputs {
			if in.Kind == InFootage || in.Kind == InSound {
				clips++
			}
		}
		if clips != 1 {
			add("outputs", "captions are kept with the clip they were made from, so it takes exactly one footage or sound input")
		}
	}
	// Cost.
	if !m.Cost.Free {
		switch {
		case m.Cost.USD <= 0 && m.Cost.Per == "":
			add("cost", "say free, or a price (usd, per, as-of)")
		case m.Cost.Per == "" || m.Cost.AsOf == "":
			add("cost", "a price says what it is per and the day it was read (per, as-of)")
		default:
			if _, err := time.Parse("2006-01-02", m.Cost.AsOf); err != nil {
				add("cost.as-of", "write the day as YYYY-MM-DD")
			}
		}
		if m.Cost.From != "" {
			if in, ok := m.Inputs[m.Cost.From]; !ok || in.Kind != InNumber {
				add("cost.from", "%q isn't a number input", m.Cost.From)
			}
		}
		if m.NetworkMode() == NetworkOff {
			add("cost", "it is priced but reaches no network: who would charge for it?")
		}
	}
	// The network, hosts and keys.
	switch m.Network {
	case "", NetworkOff, NetworkMetaRush, NetworkOpen:
	default:
		add("network", "say off, metarush or open")
	}
	if m.Network == NetworkOff && len(m.Hosts) > 0 {
		add("network", "its network is off, so it can't reach hosts")
	}
	if m.Network == NetworkMetaRush && len(m.Hosts) == 0 && m.Local == nil {
		add("hosts", "say which hosts it reaches through MetaRush")
	}
	if l := m.Local; l != nil {
		switch {
		case ReservedPort(l.Port) != "":
			add("local.port", "%d: %s", l.Port, ReservedPort(l.Port))
		case strings.TrimSpace(l.Service) == "" || len(l.Service) > 60:
			add("local.service", "say what listens on port %d, in a few words (like ComfyUI)", l.Port)
		}
		if m.Network == NetworkOff {
			add("local", "its network is off, so it can't reach a service on this computer")
		}
		if pr := l.Probe; pr != nil {
			if !strings.HasPrefix(pr.Path, "/") || len(pr.Path) > 200 || strings.Contains(pr.Path, "..") || strings.ContainsAny(pr.Path, " #\n\r") {
				add("local.probe.path", "a path on the service, starting with / (like /system_stats)")
			}
			if len(pr.Expect) == 0 || len(pr.Expect) > 8 {
				add("local.probe.expect", "name 1 to 8 fields its answer has (like system.comfyui_version)")
			}
			for _, k := range pr.Expect {
				if !probeKeyRe.MatchString(k) {
					add("local.probe.expect", "%q isn't a field's name (dotted, like system.comfyui_version)", k)
				}
			}
		}
		if m.NetworkMode() == NetworkOpen {
			add("local", "an open network never reaches this computer; use network: metarush")
		}
	}
	for _, h := range m.Hosts {
		if why := hostProblem(h); why != "" {
			add("hosts", "%s: %s", h, why)
		}
	}
	if m.NetworkMode() == NetworkOpen && len(m.Keys) > 0 {
		add("keys", "with an open network MetaRush can't keep keys out of the code's reach; use network: metarush")
	}
	seenKey, seenEnv := map[string]bool{}, map[string]bool{}
	for i, k := range m.Keys {
		f := fmt.Sprintf("keys[%d]", i)
		if !keyNameRe.MatchString(k.Name) {
			add(f, "name keys with lowercase letters, digits and dashes (like pexels)")
			continue
		}
		if seenKey[k.Name] {
			add(f, "%s twice", k.Name)
		}
		seenKey[k.Name] = true
		if k.Env != "" && !envRe.MatchString(k.Env) {
			add(f, "env is a variable's name in capitals (like PEXELS_API_KEY)")
		}
		if steersProgram(k.EnvName()) {
			add(f, "%s steers programs (MetaRush sets it itself); read the key from another name", k.EnvName())
		}
		if seenEnv[k.EnvName()] {
			add(f, "two keys under %s", k.EnvName())
		}
		seenEnv[k.EnvName()] = true
		if m.NetworkMode() == NetworkOff {
			add(f, "a key is only for the hosts it reaches, and its network is off")
		}
		for _, h := range k.Hosts {
			if !contains(m.Hosts, h) {
				add(f, "%s isn't one of its hosts", h)
			}
		}
		switch {
		case k.Header != "" && k.Query != "":
			add(f, "a key goes in one place: a header or a query parameter, not both")
		case k.Header != "" && (!headerRe.MatchString(k.Header) || headersNotForKeys[http.CanonicalHeaderKey(k.Header)]):
			add(f, "%q can't carry a key; name a header like Authorization or X-Api-Key", k.Header)
		case k.Query != "" && !queryRe.MatchString(k.Query):
			add(f, "%q isn't a query parameter's name", k.Query)
		}
		allowed, integration, err := providers.AbilityKeyHosts(k.Name)
		switch {
		case err != nil:
			add(f, "%s", lowerFirst(strings.TrimSuffix(apperr.From(err).Message, ".")))
		case integration:
			for _, h := range k.HostsOr(m.Hosts) {
				if !contains(allowed, h) {
					add(f, "MetaRush's %s key goes only to its own service, not to %s", k.Name, h)
				}
			}
		}
	}
	// What leaves.
	for _, s := range m.Sends {
		if !contains(SendsKinds, s) {
			add("sends", "%q: say %s", s, strings.Join(SendsKinds, ", "))
		}
	}
	if m.NetworkMode() != NetworkOff && len(m.Sends) == 0 {
		add("sends", "it reaches the network, so say what leaves this computer (nothing, words, pictures, sound, footage)")
	}
	if contains(m.Sends, SendsNothing) && len(m.Sends) > 1 {
		add("sends", "nothing can't go with other things")
	}
	for _, w := range m.Search {
		if !wordRe.MatchString(w) {
			add("search", "%q: search words are short words", w)
		}
	}
	if m.Limits.Minutes < 0 || m.Limits.Minutes > 60 {
		add("limits.minutes", "a run may take 1 to 60 minutes")
	}
	if m.Limits.MB < 0 || m.Limits.MB > 20000 {
		add("limits.mb", "a run may bring back 1 to 20,000 MB")
	}
	for _, pr := range providesProblems(m) {
		add("provides", "%s", pr)
	}
	if m.Test == nil {
		add("test", "give the inputs MetaRush runs it with once, with the network off, before anyone approves it (test: {inputs: …})")
	} else {
		if _, err := m.Values(m.Test.Inputs, true); err != nil {
			add("test.inputs", "%s", err)
		}
	}
	return ps
}

// Place is where a key goes: header (canonical) or query.
func (k Key) Place() (header, query string) {
	if k.Query != "" {
		return "", k.Query
	}
	if k.Header == "" {
		return "Authorization", ""
	}
	return http.CanonicalHeaderKey(k.Header), ""
}

// headersNotForKeys are headers a key can't go in: they steer the request
// itself, or carry a session.
var headersNotForKeys = map[string]bool{"Host": true, "Content-Length": true, "Content-Type": true, "Transfer-Encoding": true, "Connection": true,
	"Upgrade": true, "Te": true, "Trailer": true, "Proxy-Authorization": true, "Proxy-Connection": true, "Cookie": true, "Accept-Encoding": true,
	"X-Metarush-Proxy": true}

// HostsOr is the hosts a key goes to: its own, else all of them.
func (k Key) HostsOr(all []string) []string {
	if len(k.Hosts) > 0 {
		return k.Hosts
	}
	return all
}

// hostProblem says what is wrong with a declared host ("" when nothing).
func hostProblem(h string) string {
	host, port := h, ""
	if hp, p, err := net.SplitHostPort(h); err == nil {
		host, port = hp, p
	}
	if host != strings.ToLower(host) {
		return "write hosts in lowercase"
	}
	if net.ParseIP(strings.Trim(host, "[]")) != nil {
		return "name a host, not an address"
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return "an ability never reaches this computer or a local network"
	}
	if !hostRe.MatchString(host) {
		return "write a host name like api.example.com (or *.example.com for every name under it)"
	}
	if port != "" {
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return "its port isn't a number from 1 to 65535"
		}
	}
	return ""
}

// steersProgram reports variable names MetaRush sets itself or that change
// how programs run (a key can't go under them).
func steersProgram(env string) bool {
	switch env {
	case "PATH", "HOME", "TMPDIR", "SHELL", "USER", "LOGNAME", "LANG", "NODE_OPTIONS", "NODE_PATH", "NODE_EXTRA_CA_CERTS", "PYTHONPATH", "PYTHONHOME", "PYTHONSTARTUP",
		"LD_PRELOAD", "LD_LIBRARY_PATH", "DYLD_INSERT_LIBRARIES", "DYLD_LIBRARY_PATH", "SSL_CERT_FILE", "SSL_CERT_DIR", "REQUESTS_CA_BUNDLE", "CURL_CA_BUNDLE":
		return true
	}
	return strings.HasPrefix(env, "METARUSH_") || strings.HasSuffix(env, "_PROXY") || strings.HasPrefix(env, "DYLD_") || strings.HasPrefix(env, "LD_")
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// Coerce turns a value given for an input into its kind ("3" → 3 for a
// number): numbers in range, words within their length, a choice among its
// options; footage, pictures and sound by their ID.
func (in Input) Coerce(v any) (any, error) {
	switch in.Kind {
	case InWords:
		s, ok := v.(string)
		if !ok {
			s = fmt.Sprint(v)
		}
		s = strings.TrimSpace(s)
		max := 2000.0
		if in.Max != nil {
			max = *in.Max
		}
		if float64(len([]rune(s))) > max {
			return nil, fmt.Errorf("at most %d characters", int(max))
		}
		return s, nil
	case InNumber:
		var f float64
		switch x := v.(type) {
		case int:
			f = float64(x)
		case int64:
			f = float64(x)
		case float64:
			f = x
		case string:
			p, err := strconv.ParseFloat(strings.TrimSpace(x), 64)
			if err != nil {
				return nil, fmt.Errorf("%q isn't a number", x)
			}
			f = p
		default:
			return nil, fmt.Errorf("%v isn't a number", v)
		}
		if in.Min != nil && f < *in.Min {
			return nil, fmt.Errorf("at least %s", num(*in.Min))
		}
		if in.Max != nil && f > *in.Max {
			return nil, fmt.Errorf("at most %s", num(*in.Max))
		}
		return f, nil
	case InChoice:
		s := strings.TrimSpace(fmt.Sprint(v))
		if !contains(in.Options, s) {
			return nil, fmt.Errorf("one of %s", strings.Join(in.Options, ", "))
		}
		return s, nil
	case InYesNo:
		switch x := v.(type) {
		case bool:
			return x, nil
		case string:
			switch strings.ToLower(strings.TrimSpace(x)) {
			case "yes", "true":
				return true, nil
			case "no", "false":
				return false, nil
			}
		}
		return nil, fmt.Errorf("yes or no")
	case InFootage, InPicture, InSound:
		s, ok := v.(string)
		if !ok || strings.TrimSpace(s) == "" {
			return nil, fmt.Errorf("the %s to use (its ID or name)", in.Kind)
		}
		return strings.TrimSpace(s), nil
	}
	return nil, fmt.Errorf("an input of kind %q", in.Kind)
}

func num(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

// Values checks the values given for a run (or its test) against the
// inputs: unknown names refused, defaults filled in, required ones present.
// files leaves footage, pictures and sound as given (their IDs).
func (m *Manifest) Values(given map[string]any, test bool) (map[string]any, error) {
	out := map[string]any{}
	for k := range given {
		if _, ok := m.Inputs[k]; !ok {
			return nil, fmt.Errorf("it has no input called %q", k)
		}
	}
	for _, n := range m.InputNames() {
		in := m.Inputs[n]
		v, ok := given[n]
		if !ok || v == nil {
			if in.Default != nil {
				v = in.Default
			} else if in.Optional {
				continue
			} else {
				return nil, fmt.Errorf("%s is needed", n)
			}
		}
		if test {
			if _, file := fileInputSends[in.Kind]; file {
				return nil, fmt.Errorf("%s: a test can't take %s yet; give the ability a default it can do without, or make the input optional", n, in.Kind)
			}
		}
		c, err := in.Coerce(v)
		if err != nil {
			return nil, fmt.Errorf("%s: %s", n, err)
		}
		out[n] = c
	}
	return out, nil
}
