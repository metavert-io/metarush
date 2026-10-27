package aiservices

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/videogen"
)

//go:embed catalog.json
var catalogJSON []byte

// Catalog is every AI service's providers, models and dated prices
// (catalog.json, D225).
type Catalog struct {
	// Defaults name each kind's usual model: "provider/model".
	Defaults  map[string]string `json:"defaults"`
	Providers []ProviderInfo    `json:"providers"`
}

// Routes: how MetaRush reaches a provider.
const (
	RouteDirect  = "direct"  // the provider's own API, with its own key
	RouteGateway = "gateway" // a service that resells other providers' models; none is built (D224)
)

// DataTerms are what a provider does with what is sent and made, as its own
// terms say, with the day they were read.
type DataTerms struct {
	Summary   string `json:"summary"`
	Training  string `json:"training"`
	Retention string `json:"retention"`
	Region    string `json:"region"`
	// TrainsOnUploads says whether the provider trains its models on what
	// it is sent (D276): no, unless-opted-out or yes; OptOut says how to opt out.
	TrainsOnUploads string `json:"trainsOnUploads,omitempty" jsonschema:"enum=,enum=no,enum=unless-opted-out,enum=yes"`
	OptOut          string `json:"optOut,omitempty"`
	Source          string `json:"source"`
	Checked         string `json:"checked"`
}

// ProviderInfo is one provider in the catalogue.
type ProviderInfo struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	// Key is the integration whose key it uses (internal/providers).
	Key     string `json:"key"`
	KeyHost string `json:"keyHost" jsonschema:"description=The only host the key is ever sent to."`
	Route   string `json:"route" jsonschema:"enum=direct,enum=gateway"`
	// Via is the gateway that serves this provider's models (route gateway).
	Via string `json:"via,omitempty"`
	// Stays: nothing sent to it leaves this computer (an ability whose
	// network is off, D281; a model on this computer always).
	Stays bool `json:"stays,omitempty"`
	// Reaches are the integrations an ability uses the key of, or whose
	// hosts it reaches ("*": any host, an open network): Content Settings
	// that rule one of them out rule the ability out too (D281).
	Reaches []string  `json:"reaches,omitempty"`
	Data    DataTerms `json:"data"`
	// ConsentScripts are the words the provider wants a speaker to read to
	// consent to a copy of their voice, by language.
	ConsentScripts map[string]string `json:"consentScripts,omitempty"`
	// Presenters are what the provider asks of a person's presenter and what
	// its terms say (D273–D277); nil when it makes no presenters.
	Presenters *PresenterTerms `json:"presenters,omitempty"`
	Models     []Model         `json:"models"`
	Prices     []Price         `json:"prices"`
}

// Limits are what a model accepts.
type Limits struct {
	Sizes          []string `json:"sizes,omitempty"`
	AspectRatios   []string `json:"aspectRatios,omitempty"`
	MaxInputs      int      `json:"maxInputs,omitempty"`
	MaxPromptChars int      `json:"maxPromptChars,omitempty"`
	// Seconds made (sound effects, music) or heard (voice change, dubbing, transcription).
	MinSeconds float64 `json:"minSeconds,omitempty"`
	MaxSeconds float64 `json:"maxSeconds,omitempty"`
	// MaxChars is the longest text a voice says in one request.
	MaxChars int `json:"maxChars,omitempty"`
	// Voices are a voice model's own voices (empty: the account's voices).
	Voices []string `json:"voices,omitempty"`
	// A voice copy's sample, in seconds.
	SampleMinSeconds float64  `json:"sampleMinSeconds,omitempty"`
	SampleMaxSeconds float64  `json:"sampleMaxSeconds,omitempty"`
	MaxSpeakers      int      `json:"maxSpeakers,omitempty"`
	MaxInputBytes    int64    `json:"maxInputBytes,omitempty"`
	Languages        []string `json:"languages,omitempty"`
	// Scales are how many times larger a model makes a picture (D279; the
	// first is the usual one).
	Scales []int `json:"scales,omitempty"`
	// MaxPixels is the largest picture a model takes, in pixels (width × height).
	MaxPixels int64 `json:"maxPixels,omitempty"`
}

// PlanTerms are a licence's terms on some of a provider's plans.
type PlanTerms struct {
	Plans string `json:"plans"`
	Terms string `json:"terms"`
}

// Licence is what a model's results may be used for, as the provider's
// terms say, with the day they were read. Commercial and Broadcast are yes,
// no, plan (it depends on the provider's plan) or unstated.
type Licence struct {
	Summary    string      `json:"summary"`
	Commercial string      `json:"commercial" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Broadcast  string      `json:"broadcast" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Plans      []PlanTerms `json:"plans,omitempty"`
	Source     string      `json:"source"`
	Checked    string      `json:"checked"`
}

// Model is one model of one kind of work at a provider.
type Model struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
	// Provider is the provider it belongs to (filled in from the catalogue).
	Provider      string   `json:"provider"`
	ProviderTitle string   `json:"providerTitle"`
	Tasks         []string `json:"tasks"`
	Stage         string   `json:"stage" jsonschema:"enum=ga,enum=preview,enum=experimental"`
	Since         string   `json:"since,omitempty"`
	RetiredOn     string   `json:"retiredOn,omitempty" jsonschema:"description=The day the provider retires it (YYYY-MM-DD); empty while it is offered."`
	// Rank is MetaRush's own order of how good a model's results are within
	// its kind (higher is better), shown with every estimate's alternatives.
	Rank      int     `json:"rank"`
	Limits    Limits  `json:"limits"`
	Watermark string  `json:"watermark,omitempty"`
	FreeTier  bool    `json:"freeTier,omitempty"`
	Licence   Licence `json:"licence"`
	// OnlyVia names the gateway a model is reachable through when it has no
	// direct route (D282): it is listed with its terms, never offered,
	// chosen or sent — MetaRush goes to providers directly (D224, D278).
	OnlyVia string   `json:"onlyVia,omitempty" jsonschema:"description=The gateway it is only reachable through (it is listed\\, never offered or sent: MetaRush uses no gateway)."`
	Docs    string   `json:"docs,omitempty"`
	Checked string   `json:"checked,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// Ref is the model as "provider/model".
func (m *Model) Ref() string { return m.Provider + "/" + m.ID }

// GatewayOnly reports whether the model is reachable only through a gateway.
func (m *Model) GatewayOnly() bool { return strings.TrimSpace(m.OnlyVia) != "" }

// GatewayWords say plainly why a gateway-only model isn't offered.
func (m *Model) GatewayWords() string {
	return fmt.Sprintf("%s is only reachable through %s, a service that resells other providers' models. MetaRush goes to each provider directly and uses no gateway, so it isn't offered.", m.Title, m.OnlyVia)
}

// Retired reports whether the model is retired at t.
func (m *Model) Retired(t time.Time) bool {
	if m.RetiredOn == "" {
		return false
	}
	d, err := time.Parse("2006-01-02", m.RetiredOn)
	return err == nil && !t.Before(d)
}

// Price is one dated price list for a model of a kind. The latest one whose
// Effective day has come is the one in force.
type Price struct {
	Model     string `json:"model"`
	Kind      string `json:"kind"`
	Effective string `json:"effective"`
	Checked   string `json:"checked,omitempty"`
	Source    string `json:"source,omitempty"`
	// Unit is what one USD is for: picture, second, minute, hour, song,
	// thousand-characters or request.
	Unit string  `json:"unit" jsonschema:"enum=picture,enum=second,enum=minute,enum=hour,enum=song,enum=thousand-characters,enum=request"`
	USD  float64 `json:"usd"`
	// ByOption prices a picture by its size (1K, 2K, 4K).
	ByOption map[string]float64 `json:"byOption,omitempty"`
	// What is sent, in tokens (Google): the price per million, a picture's
	// tokens and how many characters make a token.
	InputPerMillion    float64 `json:"inputPerMillion,omitempty"`
	InputPictureTokens int64   `json:"inputPictureTokens,omitempty"`
	CharsPerToken      int     `json:"charsPerToken,omitempty"`
	// CharsPerSecond is how many characters a voice says in a second (to
	// price speech by the second before it is made).
	CharsPerSecond float64 `json:"charsPerSecond,omitempty"`
	// RoundSeconds: each request's length is counted up to a whole number of
	// these (D-ID counts a video in 15-second steps).
	RoundSeconds float64 `json:"roundSeconds,omitempty"`
	Note         string  `json:"note,omitempty"`
}

var (
	catMu   sync.RWMutex
	catalog = loadCatalog()
	added   = map[string]bool{}
)

func loadCatalog() Catalog {
	var c Catalog
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("aiservices: catalog.json: " + err.Error())
	}
	for i := range c.Providers {
		fill(&c.Providers[i])
	}
	return c
}

func fill(p *ProviderInfo) {
	for j := range p.Models {
		p.Models[j].Provider, p.Models[j].ProviderTitle = p.Name, p.Title
	}
}

// LoadCatalog returns the catalogue (a copy).
func LoadCatalog() Catalog {
	catMu.RLock()
	defer catMu.RUnlock()
	b, _ := json.Marshal(catalog)
	var c Catalog
	_ = json.Unmarshal(b, &c)
	return c
}

// AddProvider puts a provider and its models into the catalogue while the
// program runs (an ability that provides a kind of work, or a test's fake);
// the returned function takes it out again. A provider already in the
// catalogue file can't be replaced.
func AddProvider(p ProviderInfo) (remove func(), err error) {
	catMu.Lock()
	defer catMu.Unlock()
	for _, q := range catalog.Providers {
		if q.Name == p.Name {
			return nil, apperr.New(apperr.Conflict, "MetaRush already knows a provider called “%s”.", p.Name)
		}
	}
	fill(&p)
	catalog.Providers = append(catalog.Providers, p)
	added[p.Name] = true
	return func() {
		catMu.Lock()
		defer catMu.Unlock()
		out := catalog.Providers[:0]
		for _, q := range catalog.Providers {
			if q.Name != p.Name {
				out = append(out, q)
			}
		}
		catalog.Providers = out
		delete(added, p.Name)
	}, nil
}

// ProviderOf returns a provider's catalogue entry.
func ProviderOf(name string) (*ProviderInfo, error) {
	catMu.RLock()
	defer catMu.RUnlock()
	name = strings.ToLower(strings.TrimSpace(name))
	for i := range catalog.Providers {
		if catalog.Providers[i].Name == name {
			p := catalog.Providers[i]
			return &p, nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "MetaRush has no AI service called “%s”.", name)
}

// Providers lists the catalogue's providers.
func Providers() []ProviderInfo { return LoadCatalog().Providers }

// DefaultRef is a kind's usual model, "provider/model" ("" when the catalogue names none).
func DefaultRef(kind string) string {
	catMu.RLock()
	defer catMu.RUnlock()
	return catalog.Defaults[kind]
}

// SplitRef reads "provider/model" (a model alone leaves the provider empty).
func SplitRef(ref string) (provider, model string) {
	ref = strings.TrimSpace(ref)
	if p, m, ok := strings.Cut(ref, "/"); ok {
		return strings.ToLower(strings.TrimSpace(p)), strings.TrimSpace(m)
	}
	return "", ref
}

// ModelFor returns a provider's model of a kind. An empty model is the
// kind's default when that is the provider's, otherwise the provider's best
// model of the kind still offered.
func ModelFor(provider, kind, model string) (*Model, error) {
	if _, err := KindOf(kind); err != nil {
		return nil, err
	}
	p, err := ProviderOf(provider)
	if err != nil {
		return nil, err
	}
	model = strings.TrimSpace(model)
	if model == "" {
		dp, dm := SplitRef(DefaultRef(kind))
		if dp == p.Name {
			model = dm
		} else {
			var best *Model
			for i := range p.Models {
				m := &p.Models[i]
				if m.Kind == kind && !m.Retired(time.Now()) && !m.GatewayOnly() && (best == nil || m.Rank > best.Rank) {
					best = m
				}
			}
			if best == nil {
				k, _ := KindOf(kind)
				return nil, apperr.New(apperr.NotFound, "%s has no models for %s that MetaRush knows.", p.Title, strings.ToLower(k.Title)).
					WithDetails(map[string]any{"provider": p.Name, "kind": kind})
			}
			model = best.ID
		}
	}
	var titles, ids []string
	for i := range p.Models {
		m := p.Models[i]
		if m.Kind != kind {
			continue
		}
		if m.ID == model || strings.EqualFold(m.Title, model) {
			return &m, nil
		}
		titles, ids = append(titles, m.Title), append(ids, m.ID)
	}
	k, _ := KindOf(kind)
	e := apperr.New(apperr.InvalidInput, "%s has no model “%s” for %s that MetaRush knows.", p.Title, model, strings.ToLower(k.Title)).
		WithDetails(map[string]any{"provider": p.Name, "kind": kind, "models": ids})
	if len(titles) > 0 {
		e = e.WithHint("Choose one of: " + strings.Join(titles, ", ") + ".")
	}
	return nil, e
}

// FindModel reads "provider/model", a model alone (any provider's), or ""
// (the kind's default).
func FindModel(kind, ref string) (*Model, error) {
	if strings.TrimSpace(ref) == "" {
		ref = DefaultRef(kind)
	}
	p, m := SplitRef(ref)
	if p != "" {
		return ModelFor(p, kind, m)
	}
	var found []Model
	for _, x := range ModelsOf(kind) {
		if x.ID == m || strings.EqualFold(x.Title, m) {
			found = append(found, x)
		}
	}
	switch len(found) {
	case 1:
		return &found[0], nil
	case 0:
		k, _ := KindOf(kind)
		return nil, apperr.New(apperr.InvalidInput, "MetaRush knows no model “%s” for %s.", m, strings.ToLower(k.Title)).WithDetails(map[string]any{"kind": kind})
	}
	return nil, apperr.New(apperr.InvalidInput, "More than one service has a model “%s”: say which, as provider/model.", m).WithDetails(map[string]any{"kind": kind})
}

// ModelsOf lists every provider's models of a kind, the default first, then
// by provider and rank. Generated video's models come from internal/videogen.
func ModelsOf(kind string) []Model {
	if kind == KindVideo {
		return videoModels()
	}
	c := LoadCatalog()
	var out []Model
	for _, p := range c.Providers {
		for _, m := range p.Models {
			if m.Kind == kind {
				out = append(out, m)
			}
		}
	}
	def := c.Defaults[kind]
	sort.SliceStable(out, func(i, j int) bool {
		di, dj := out[i].Ref() == def, out[j].Ref() == def
		if di != dj {
			return di
		}
		if out[i].Provider != out[j].Provider {
			return out[i].Provider < out[j].Provider
		}
		return out[i].Rank > out[j].Rank
	})
	return out
}

// videoModels lists generated video's models as catalogue models (for
// choosing; requests and prices stay in internal/videogen and internal/generate).
func videoModels() []Model {
	var out []Model
	for _, p := range videogen.LoadCatalog().Providers {
		for _, c := range p.Models {
			out = append(out, Model{ID: c.ID, Title: c.Title, Kind: KindVideo, Provider: p.Name, ProviderTitle: p.Title, Tasks: c.Tasks, Stage: c.Stage,
				Since: c.Since, RetiredOn: c.RetiredOn, Rank: 4, Watermark: c.Watermark,
				Limits: Limits{Sizes: c.Resolutions, AspectRatios: c.AspectRatios, MinSeconds: float64(c.MinSeconds), MaxSeconds: float64(c.MaxSeconds), MaxInputs: c.MaxImageRefs}})
		}
	}
	return out
}

// PriceAt returns the price in force for a model of a kind at t.
func PriceAt(provider, kind, model string, t time.Time) (*Price, error) {
	p, err := ProviderOf(provider)
	if err != nil {
		return nil, err
	}
	var best *Price
	for i := range p.Prices {
		pr := &p.Prices[i]
		if pr.Model != model || pr.Kind != kind {
			continue
		}
		d, err := time.Parse("2006-01-02", pr.Effective)
		if err != nil || t.Before(d) {
			continue
		}
		if best == nil || pr.Effective > best.Effective {
			best = pr
		}
	}
	if best == nil {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush has no prices for %s yet, so it can't say what it costs.", model).
			WithDetails(map[string]any{"provider": provider, "kind": kind, "model": model})
	}
	cp := *best
	return &cp, nil
}

// Estimate is what a request will cost, before anything is spent.
type Estimate struct {
	Provider      string  `json:"provider"`
	ProviderTitle string  `json:"providerTitle"`
	Kind          string  `json:"kind"`
	Model         string  `json:"model"`
	ModelTitle    string  `json:"modelTitle"`
	USD           float64 `json:"usd" jsonschema:"description=The estimate in US dollars (to a tenth of a cent)."`
	// What is counted: the unit, how many, and that in words ("30 seconds of sound").
	Unit   string  `json:"unit"`
	Amount float64 `json:"amount"`
	Units  string  `json:"units"`
	// The parts of USD: what is made and what is sent (Google counts the
	// words and pictures sent in tokens). Not rounded: USD is.
	OutputUSD   float64 `json:"outputUsd"`
	InputUSD    float64 `json:"inputUsd,omitempty"`
	InputTokens int64   `json:"inputTokens,omitempty"`
	// The price used: per unit, its table's day, when it was read and where.
	UnitUSD    float64 `json:"unitUsd"`
	PriceTable string  `json:"priceTable" jsonschema:"description=The price table's date (YYYY-MM-DD)."`
	Checked    string  `json:"checked,omitempty"`
	Source     string  `json:"source,omitempty"`
	// Rate says the price in words, with its date.
	Rate string `json:"rate"`
	// Notes say what to know about it (the provider picks the length, a
	// price that changes soon).
	Notes []string `json:"notes,omitempty"`
}

// Parts are the estimate as D208's parts: what is made and what is sent.
// source names what it draws on, in plain words.
func (e *Estimate) Parts(source string) []costs.Part {
	k, _ := KindOf(e.Kind)
	out := []costs.Part{{Service: e.ProviderTitle, What: k.What, Model: e.ModelTitle, Source: source, Amount: e.Amount, Unit: e.Unit, Units: e.Units,
		Rate: e.Rate, USD: e.OutputUSD}}
	if e.InputTokens > 0 {
		out = append(out, costs.Part{Service: e.ProviderTitle, What: "what is sent with it", Model: e.ModelTitle, Source: source, Amount: float64(e.InputTokens),
			Unit: "tokens", Units: costs.Count(float64(e.InputTokens), "token", "tokens"), USD: e.InputUSD,
			Rate: fmt.Sprintf("%s per million tokens (%s's prices of %s)", costs.Money(e.inputPerMillion()), e.ProviderTitle, costs.Day(e.PriceTable))})
	}
	return out
}

func (e *Estimate) inputPerMillion() float64 {
	if e.InputTokens == 0 {
		return 0
	}
	return e.InputUSD / float64(e.InputTokens) * 1e6
}

// unitWords names a price's unit for people.
var unitWords = map[string][2]string{
	"picture": {"picture", "pictures"}, "second": {"second", "seconds"}, "minute": {"minute", "minutes"}, "hour": {"hour", "hours"},
	"song": {"song", "songs"}, "thousand-characters": {"thousand characters", "thousand characters"}, "request": {"request", "requests"},
}

// EstimateFor prices a request for a model with the price in force at t.
func EstimateFor(provider string, m *Model, req *Request, at time.Time) (*Estimate, error) {
	pr, err := PriceAt(provider, m.Kind, m.ID, at)
	if err != nil {
		return nil, err
	}
	p, _ := ProviderOf(provider)
	e := &Estimate{Provider: provider, ProviderTitle: provider, Kind: m.Kind, Model: m.ID, ModelTitle: m.Title, Unit: pr.Unit, UnitUSD: pr.USD,
		PriceTable: pr.Effective, Checked: pr.Checked, Source: pr.Source}
	if p != nil {
		e.ProviderTitle = p.Title
	}
	chars := len([]rune(req.Prompt))
	unitUSD := pr.USD
	switch pr.Unit {
	case "picture":
		e.Amount = 1
		if v, ok := pr.ByOption[req.Options.Size]; ok {
			unitUSD = v
		}
		e.Units = "1 picture"
		if req.Options.Size != "" {
			e.Units += " at " + req.Options.Size
		}
	case "song", "request":
		e.Amount = 1
		e.Units = "1 " + unitWords[pr.Unit][0]
	case "thousand-characters":
		e.Amount = float64(chars) / 1000
		e.Units = costs.Count(float64(chars), "character", "characters")
	case "second", "minute", "hour":
		secs := secondsOf(m, req, pr)
		if secs == 0 && req.Options.Seconds == 0 && m.Limits.MaxSeconds > 0 && made(m.Kind) {
			secs = m.Limits.MaxSeconds
			e.Notes = append(e.Notes, fmt.Sprintf("%s chooses how long it is; priced at the longest, %s.", e.ProviderTitle, secondsWords(secs)))
		}
		if pr.RoundSeconds > 0 && secs > 0 {
			if up := math.Ceil(secs/pr.RoundSeconds-1e-9) * pr.RoundSeconds; up != secs {
				e.Notes = append(e.Notes, fmt.Sprintf("%s counts each one in steps of %s, so %s counts as %s.", e.ProviderTitle, secondsWords(pr.RoundSeconds), secondsWords(secs), secondsWords(up)))
				secs = up
			}
		}
		div := map[string]float64{"second": 1, "minute": 60, "hour": 3600}[pr.Unit]
		e.Amount = secs / div
		e.Units = secondsWords(secs) + " " + map[bool]string{true: "of sound", false: "heard"}[made(m.Kind)]
		if m.Kind == KindPresenter {
			e.Units = secondsWords(secs) + " on camera"
		}
		if m.Kind == KindLipSync {
			e.Units = secondsWords(secs) + " of footage"
		}
	default:
		return nil, apperr.New(apperr.NotConfigured, "MetaRush can't read the price of %s (it is counted per %s).", m.Title, pr.Unit)
	}
	e.OutputUSD = e.Amount * unitUSD
	e.UnitUSD = unitUSD
	// What is sent, in tokens: the words, and each picture.
	if pr.InputPerMillion > 0 {
		cpt := pr.CharsPerToken
		if cpt <= 0 {
			cpt = 4
		}
		e.InputTokens = int64((chars + cpt - 1) / cpt)
		e.InputTokens += int64(len(req.InputsOf(RolePicture))) * pr.InputPictureTokens
		e.InputUSD = float64(e.InputTokens) * pr.InputPerMillion / 1e6
	}
	// To a millionth of a dollar: a few seconds heard cost less than a cent, not nothing.
	e.USD = math.Round((e.OutputUSD+e.InputUSD)*1e6) / 1e6
	switch {
	case p.Local():
		e.Rate = localRate // nothing to pay, nothing sent (D278)
	case pr.Unit == "request" && pr.USD == 0:
		e.Rate = fmt.Sprintf("nothing charged (%s's prices of %s)", e.ProviderTitle, costs.Day(pr.Effective))
	default:
		price := costs.Money(unitUSD)
		if unitUSD < 0.01 && unitUSD > 0 {
			price = "$" + trimZeros(unitUSD)
		}
		article := "a"
		if pr.Unit == "hour" {
			article = "an"
		}
		e.Rate = fmt.Sprintf("%s %s %s (%s's prices of %s)", price, article, unitWords[pr.Unit][0], e.ProviderTitle, costs.Day(pr.Effective))
	}
	if pr.Note != "" {
		e.Notes = append(e.Notes, pr.Note)
	}
	if next := nextPrice(p, m, pr); next != nil {
		e.Notes = append(e.Notes, fmt.Sprintf("%s's price changes on %s.", m.Title, costs.Day(next.Effective)))
	}
	return e, nil
}

// made reports whether a kind's seconds are of sound it makes (not of a recording it hears).
func made(kind string) bool { return kind == KindSoundEffect || kind == KindMusic || kind == KindVoice }

// secondsOf is how many seconds a request is priced for: what it makes, what
// it hears, or (speech) what the words take to say.
func secondsOf(m *Model, req *Request, pr *Price) float64 {
	switch m.Kind {
	case KindVoice:
		cps := pr.CharsPerSecond
		if cps <= 0 {
			cps = 14
		}
		return math.Ceil(float64(len([]rune(req.Prompt))) / cps)
	case KindSoundEffect, KindMusic:
		return req.Options.Seconds
	case KindLipSync:
		if m := req.Input(RoleMedia); m != nil {
			return math.Ceil(m.Seconds) // the clip made, as long as the footage
		}
	}
	return math.Ceil(req.HeardSeconds())
}

func nextPrice(p *ProviderInfo, m *Model, cur *Price) *Price {
	if p == nil {
		return nil
	}
	var next *Price
	for i := range p.Prices {
		pr := &p.Prices[i]
		if pr.Model == m.ID && pr.Kind == m.Kind && pr.Effective > cur.Effective && (next == nil || pr.Effective < next.Effective) {
			next = pr
		}
	}
	return next
}

func trimZeros(v float64) string {
	s := fmt.Sprintf("%.6f", v)
	s = strings.TrimRight(s, "0")
	return strings.TrimSuffix(s, ".")
}

// secondsWords says a length the way people do: "30 seconds", "2 minutes", "1.5 seconds".
func secondsWords(s float64) string {
	switch {
	case s >= 120 && math.Mod(s, 60) == 0:
		return fmt.Sprintf("%.0f minutes", s/60)
	case s == 60:
		return "1 minute"
	case s == math.Trunc(s):
		return costs.Count(s, "second", "seconds")
	}
	return fmt.Sprintf("%.1f seconds", s)
}

// Check refuses a request the model can't do, in plain words.
func (m *Model) Check(req *Request, now time.Time) error {
	bad := func(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }
	k, err := KindOf(req.Kind)
	if err != nil {
		return err
	}
	if req.Kind != m.Kind {
		return bad("%s doesn't do %s.", m.Title, strings.ToLower(k.Title))
	}
	if m.GatewayOnly() {
		return apperr.New(apperr.NotConfigured, "%s", m.GatewayWords()).WithHint("Choose a model MetaRush reaches directly.").
			WithDetails(map[string]any{"reason": "gateway_only", "model": m.ID, "via": m.OnlyVia})
	}
	if m.Retired(now) {
		return apperr.New(apperr.Provider, "%s was retired on %s, so it can't be used any more.", m.Title, m.RetiredOn).
			WithHint("Choose another model.").WithDetails(map[string]any{"reason": videogen.KindRetired, "model": m.ID})
	}
	task := firstNonEmpty(req.Task, k.Tasks[0])
	if !slices.Contains(m.Tasks, task) {
		return bad("%s can't do that.", m.Title)
	}
	l := m.Limits
	prompt := strings.TrimSpace(req.Prompt)
	switch req.Kind {
	case KindPicture, KindSoundEffect, KindMusic:
		if prompt == "" {
			return bad("Say what to make, in a few words.")
		}
	case KindBackground, KindUpscale:
		if n := len(req.InputsOf(RolePicture)); n != 1 {
			return bad("Choose one picture.")
		}
		if req.Kind == KindUpscale && len(l.Scales) > 0 && req.Options.Scale != 0 && !slices.Contains(l.Scales, req.Options.Scale) {
			return bad("%s makes pictures %s times larger, not %d.", m.Title, joinInts(l.Scales), req.Options.Scale)
		}
	case KindVoice:
		if prompt == "" {
			return bad("Give the words to say.")
		}
	}
	if l.MaxPromptChars > 0 && len([]rune(req.Prompt)) > l.MaxPromptChars {
		return bad("%s takes at most %d characters of description; this has %d.", m.Title, l.MaxPromptChars, len([]rune(req.Prompt)))
	}
	if l.MaxChars > 0 && len([]rune(req.Prompt)) > l.MaxChars {
		return bad("%s says at most %d characters at a time; this has %d. Split it into shorter parts.", m.Title, l.MaxChars, len([]rune(req.Prompt)))
	}
	if l.MaxInputs > 0 && len(req.Inputs) > l.MaxInputs {
		return bad("%s takes at most %d files with a request; this has %d.", m.Title, l.MaxInputs, len(req.Inputs))
	}
	o := req.Options
	if len(l.Sizes) > 0 && o.Size != "" && !slices.Contains(l.Sizes, o.Size) {
		return bad("%s makes pictures at %s, not %s.", m.Title, strings.Join(l.Sizes, ", "), o.Size)
	}
	if len(l.AspectRatios) > 0 && o.AspectRatio != "" && !slices.Contains(l.AspectRatios, o.AspectRatio) {
		what := "pictures"
		if req.Kind == KindPresenter {
			what = "clips"
		}
		return bad("%s makes %s shaped %s, not %s.", m.Title, what, strings.Join(l.AspectRatios, ", "), o.AspectRatio)
	}
	if made(req.Kind) && req.Kind != KindVoice && o.Seconds > 0 {
		if (l.MinSeconds > 0 && o.Seconds < l.MinSeconds-0.001) || (l.MaxSeconds > 0 && o.Seconds > l.MaxSeconds+0.001) {
			return bad("%s makes %s of %s to %s.", m.Title, k.What+"s", secondsWords(l.MinSeconds), secondsWords(l.MaxSeconds))
		}
	}
	// A voice's ID (a voice copy: voice_…) passes; a name must be one of the model's own.
	if len(l.Voices) > 0 && o.Voice != "" && !slices.Contains(l.Voices, o.Voice) && !strings.ContainsAny(o.Voice, "_/") {
		return apperr.New(apperr.InvalidInput, "%s has no voice “%s”.", m.Title, o.Voice).WithDetails(map[string]any{"voices": l.Voices})
	}
	if len(l.Languages) > 0 && o.Language != "" && !slices.ContainsFunc(l.Languages, func(x string) bool { return strings.HasPrefix(strings.ToLower(o.Language), x) }) {
		return bad("%s works only in %s.", m.Title, strings.Join(l.Languages, ", "))
	}
	if l.MaxSpeakers > 0 && o.Speakers > l.MaxSpeakers {
		return bad("%s tells apart at most %d speakers.", m.Title, l.MaxSpeakers)
	}
	switch req.Kind {
	case KindVoiceChange, KindDubbing, KindTranscription:
		heard := req.HeardSeconds()
		if heard <= 0 {
			return bad("Choose the recording to use.")
		}
		if l.MinSeconds > 0 && heard < l.MinSeconds-0.001 {
			return bad("%s needs at least %s of recording.", m.Title, secondsWords(l.MinSeconds))
		}
		if l.MaxSeconds > 0 && heard > l.MaxSeconds+0.5 {
			return bad("%s takes at most %s of recording at a time; this is %s. Pick a shorter part.", m.Title, secondsWords(l.MaxSeconds), secondsWords(math.Round(heard)))
		}
	case KindPresenter:
		if req.Input(RoleAudio) == nil || req.HeardSeconds() <= 0 {
			return bad("Choose the sound the presenter reads.")
		}
		if strings.TrimSpace(req.Options.Presenter) == "" {
			return bad("Choose who reads it: a presenter.")
		}
		if heard := req.HeardSeconds(); l.MaxSeconds > 0 && heard > l.MaxSeconds+0.5 {
			return bad("%s reads at most %s at a time; this is %s. Split the lines into shorter parts.", m.Title, secondsWords(l.MaxSeconds), secondsWords(math.Round(heard)))
		}
	case KindPresenterMake:
		takes := req.InputsOf(RoleFootage)
		if len(takes) != 1 {
			return bad("Choose the training take: the person speaking to the camera.")
		}
		if l.SampleMinSeconds > 0 && takes[0].Seconds < l.SampleMinSeconds-0.001 {
			return bad("%s needs a training take of at least %s; this is %s.", m.Title, secondsWords(l.SampleMinSeconds), secondsWords(math.Round(takes[0].Seconds)))
		}
		if l.SampleMaxSeconds > 0 && takes[0].Seconds > l.SampleMaxSeconds+0.5 {
			return bad("%s takes a training take of at most %s; this is %s. Pick a shorter part.", m.Title, secondsWords(l.SampleMaxSeconds), secondsWords(math.Round(takes[0].Seconds)))
		}
	case KindLipSync:
		media, sound := req.Input(RoleMedia), req.Input(RoleAudio)
		if media == nil || sound == nil {
			return bad("Choose the clip whose lips to match and the sound to match them to.")
		}
		if l.MaxSeconds > 0 && media.Seconds > l.MaxSeconds+0.5 {
			return bad("%s takes at most %s of footage at a time; this is %s. Pick a shorter part.", m.Title, secondsWords(l.MaxSeconds), secondsWords(math.Round(media.Seconds)))
		}
		if sound.Seconds > media.Seconds+0.5 {
			return bad("The new sound is longer than the footage (%s against %s): pick a longer part of the clip, or shorter sound.", secondsWords(math.Round(sound.Seconds)), secondsWords(math.Round(media.Seconds)))
		}
	case KindVoiceCopy:
		samples := req.InputsOf(RoleSample)
		if len(samples) == 0 {
			return bad("Choose a recording of the voice to copy.")
		}
		total := 0.0
		for _, s := range samples {
			total += s.Seconds
		}
		if l.SampleMinSeconds > 0 && total < l.SampleMinSeconds-0.001 {
			return bad("%s needs at least %s of the voice.", m.Title, secondsWords(l.SampleMinSeconds))
		}
		if l.SampleMaxSeconds > 0 && total > l.SampleMaxSeconds+0.5 {
			return bad("%s takes at most %s of the voice; this is %s. Pick a shorter part.", m.Title, secondsWords(l.SampleMaxSeconds), secondsWords(math.Round(total)))
		}
	}
	if l.MaxInputBytes > 0 {
		for _, x := range req.Inputs {
			if x.Size > l.MaxInputBytes {
				return bad("%s takes files of at most %d MB.", m.Title, l.MaxInputBytes>>20)
			}
		}
	}
	return nil
}

// Alternative is another model that could do the same request: cheaper or
// better (by the catalogue's rank, shown with it; never a hidden score).
type Alternative struct {
	Why        string  `json:"why" jsonschema:"enum=cheaper,enum=better"`
	Provider   string  `json:"provider"`
	Model      string  `json:"model"`
	Title      string  `json:"title"`
	USD        float64 `json:"usd"`
	Rank       int     `json:"rank"`
	Summary    string  `json:"summary"`
	PriceTable string  `json:"priceTable"`
}

// Alternatives are up to two other models for the same request: the
// cheapest that costs less, and the best ranked that ranks higher. allowed
// says whether a provider may be offered (Content Settings may forbid some).
func Alternatives(chosen *Model, req *Request, usd float64, at time.Time, allowed func(provider string) bool) []Alternative {
	return alternatives(chosen, req, usd, at, func(m *Model) bool { return allowed == nil || allowed(m.Provider) })
}

func alternatives(chosen *Model, req *Request, usd float64, at time.Time, ok func(m *Model) bool) []Alternative {
	type cand struct {
		m Model
		e *Estimate
	}
	var cs []cand
	for _, m := range ModelsOf(chosen.Kind) {
		if m.Ref() == chosen.Ref() || m.Retired(at) || m.GatewayOnly() || (ok != nil && !ok(&m)) {
			continue
		}
		r := *req
		r.Model = m.ID
		r.Task = firstNonEmpty(req.Task, "")
		if r.Task != "" && !slices.Contains(m.Tasks, r.Task) {
			continue
		}
		if m.Check(&r, at) != nil {
			continue
		}
		e, err := EstimateFor(m.Provider, &m, &r, at)
		if err != nil {
			continue
		}
		cs = append(cs, cand{m, e})
	}
	var cheaper, better *cand
	for i := range cs {
		c := &cs[i]
		// Free is cheaper than any price, however small (D278).
		if (c.e.USD < usd-0.0005 || (c.e.USD == 0 && usd > 0)) && (cheaper == nil || c.e.USD < cheaper.e.USD || (c.e.USD == cheaper.e.USD && c.m.Rank > cheaper.m.Rank)) {
			cheaper = c
		}
		if c.m.Rank > chosen.Rank && (better == nil || c.m.Rank > better.m.Rank || (c.m.Rank == better.m.Rank && c.e.USD < better.e.USD)) {
			better = c
		}
	}
	var out []Alternative
	add := func(why string, c *cand) {
		if c == nil {
			return
		}
		s := fmt.Sprintf("%s with %s: about %s", c.m.Title, c.m.ProviderTitle, costs.Money(c.e.USD))
		if IsLocal(c.m.Provider) {
			s = c.m.Title + " on this computer: free" // D278
		}
		if why == "better" {
			s += ", ranked higher by MetaRush"
		}
		out = append(out, Alternative{Why: why, Provider: c.m.Provider, Model: c.m.ID, Title: c.m.Title, USD: c.e.USD, Rank: c.m.Rank, Summary: s, PriceTable: c.e.PriceTable})
	}
	add("cheaper", cheaper)
	if better != cheaper {
		add("better", better)
	}
	return out
}

func joinInts(xs []int) string {
	var out []string
	for _, x := range xs {
		out = append(out, fmt.Sprint(x))
	}
	if len(out) <= 1 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}
