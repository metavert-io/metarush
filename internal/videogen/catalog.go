package videogen

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
)

//go:embed catalog.json
var catalogJSON []byte

// Catalog is every provider's models, defaults and price tables (catalog.json).
type Catalog struct {
	Providers []ProviderInfo `json:"providers"`
}

// ProviderInfo is one provider in the catalog.
type ProviderInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	KeyHost string `json:"keyHost" jsonschema:"description=The only host the key is ever sent to."`
	// Region is where a provider in a cloud project runs its models (Vertex
	// AI: us-central1, D282); empty for one global API.
	Region string `json:"region,omitempty"`
	// Data says what the provider keeps of what is sent and made, with the
	// day it was read (D282; Gemini's are the AI services catalogue's).
	Data string `json:"data,omitempty"`
	// SpendLimitsPer10Minutes are the provider's own spending limits by usage tier (USD per rolling 10 minutes).
	SpendLimitsPer10Minutes []float64      `json:"spendLimitsPer10Minutes,omitempty"`
	Defaults                Defaults       `json:"defaults"`
	Models                  []Capabilities `json:"models"`
	Prices                  []PriceTable   `json:"prices"`
}

// Defaults are a provider's starting settings.
type Defaults struct {
	Model            string  `json:"model"`
	Seconds          int     `json:"seconds"`
	AspectRatio      string  `json:"aspectRatio"`
	DraftResolution  string  `json:"draftResolution"`
	FinalResolution  string  `json:"finalResolution"`
	ProjectBudgetUSD float64 `json:"projectBudgetUsd"`
	DailyCapUSD      float64 `json:"dailyCapUsd"`
}

// Capabilities is what a model can do.
type Capabilities struct {
	ID         string   `json:"id"`
	Title      string   `json:"title"`
	Stage      string   `json:"stage" jsonschema:"enum=ga,enum=preview,enum=experimental"`
	Since      string   `json:"since,omitempty"`
	RetiredOn  string   `json:"retiredOn,omitempty" jsonschema:"description=The day the provider retires it (YYYY-MM-DD); empty while it is offered."`
	Tasks      []string `json:"tasks"`
	MinSeconds int      `json:"minSeconds"`
	MaxSeconds int      `json:"maxSeconds"`
	// SecondsAllowed are the only lengths a model makes (Veo: 4, 6 or 8),
	// when it doesn't take any length between the two.
	SecondsAllowed       []int    `json:"secondsAllowed,omitempty"`
	ExtendTotalSeconds   int      `json:"extendTotalSeconds"`
	InputVideoMaxSeconds int      `json:"inputVideoMaxSeconds"`
	Resolutions          []string `json:"resolutions"`
	Upscaled             []string `json:"upscaled,omitempty" jsonschema:"description=Resolutions the model reaches by upscaling."`
	AspectRatios         []string `json:"aspectRatios"`
	FPS                  int      `json:"fps"`
	Audio                string   `json:"audio" jsonschema:"enum=always,enum=optional,enum=never"`
	MaxImageRefs         int      `json:"maxImageRefs"`
	MaxVideoRefs         int      `json:"maxVideoRefs"`
	VideoRefMaxSeconds   float64  `json:"videoRefMaxSeconds"`
	Seed                 bool     `json:"seed"`
	NegativePrompt       bool     `json:"negativePrompt"`
	Watermark            string   `json:"watermark,omitempty"`
	InlineMaxBytes       int64    `json:"inlineMaxBytes,omitempty"`
}

// PriceTable is one dated price list for a model. The latest table whose
// Effective day has come is the one in force.
type PriceTable struct {
	Model                     string           `json:"model"`
	Effective                 string           `json:"effective"`
	Checked                   string           `json:"checked,omitempty"`
	Source                    string           `json:"source,omitempty"`
	VideoOutputPerMillion     float64          `json:"videoOutputPerMillion"`
	TextOutputPerMillion      float64          `json:"textOutputPerMillion"`
	InputPerMillion           float64          `json:"inputPerMillion"`
	VideoTokensPerSecond      map[string]int64 `json:"videoTokensPerSecond"`
	ImageInputTokens          int64            `json:"imageInputTokens"`
	VideoInputTokensPerSecond int64            `json:"videoInputTokensPerSecond"`
	PromptCharsPerToken       int              `json:"promptCharsPerToken"`
	ThinkingTokensAllowance   int64            `json:"thinkingTokensAllowance"`
	// PerSecondUSD prices video by the second at each resolution, instead of
	// by tokens (Veo on Vertex AI, D282).
	PerSecondUSD map[string]float64 `json:"perSecondUsd,omitempty"`
	Note         string             `json:"note,omitempty"`
}

// Estimate is what a request will cost, before anything is spent.
type Estimate struct {
	USD            float64 `json:"usd"`
	VideoTokens    int64   `json:"videoTokens"`
	InputTokens    int64   `json:"inputTokens"`
	ThinkingTokens int64   `json:"thinkingTokens" jsonschema:"description=An allowance for the model's thinking (billed like text)."`
	PerSecondUSD   float64 `json:"perSecondUsd" jsonschema:"description=What one second of video costs at this resolution."`
	PriceTable     string  `json:"priceTable" jsonschema:"description=The price table's date (YYYY-MM-DD)."`
	Model          string  `json:"model"`
	// The parts of USD (D208): the video made, what is sent (the prompt,
	// pictures and clips) and the thinking allowance. Not rounded: USD is.
	VideoUSD    float64 `json:"videoUsd"`
	InputUSD    float64 `json:"inputUsd"`
	ThinkingUSD float64 `json:"thinkingUsd"`
	// The table's prices per million tokens.
	VideoPerMillion float64 `json:"videoPerMillion"`
	InputPerMillion float64 `json:"inputPerMillion"`
	TextPerMillion  float64 `json:"textPerMillion"`
}

// Cost is money and tokens for a finished request.
type Cost struct {
	USD            float64 `json:"usd"`
	VideoTokens    int64   `json:"videoTokens"`
	InputTokens    int64   `json:"inputTokens"`
	ThinkingTokens int64   `json:"thinkingTokens"`
	TextTokens     int64   `json:"textTokens"`
}

var catalog = func() Catalog {
	var c Catalog
	if err := json.Unmarshal(catalogJSON, &c); err != nil {
		panic("videogen: catalog.json: " + err.Error())
	}
	return c
}()

// LoadCatalog returns the catalog (a copy).
func LoadCatalog() Catalog {
	var c Catalog
	_ = json.Unmarshal(catalogJSON, &c)
	return c
}

// Info returns a provider's catalog entry.
func Info(provider string) (*ProviderInfo, error) {
	for i := range catalog.Providers {
		if catalog.Providers[i].Name == provider {
			p := catalog.Providers[i]
			return &p, nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "MetaRush has no models for “%s”.", provider)
}

// ModelInfo returns a model's capabilities ("" = the provider's default model).
func ModelInfo(provider, model string) (*Capabilities, error) {
	p, err := Info(provider)
	if err != nil {
		return nil, err
	}
	if model == "" {
		model = p.Defaults.Model
	}
	for i := range p.Models {
		if p.Models[i].ID == model {
			m := p.Models[i]
			return &m, nil
		}
	}
	var ids []string
	for _, m := range p.Models {
		ids = append(ids, m.Title)
	}
	return nil, apperr.New(apperr.InvalidInput, "%s has no video model “%s” that MetaRush knows.", p.Title, model).
		WithHint("Choose one of: " + strings.Join(ids, ", ") + ".").WithDetails(map[string]any{"models": modelIDs(p)})
}

func modelIDs(p *ProviderInfo) []string {
	out := []string{}
	for _, m := range p.Models {
		out = append(out, m.ID)
	}
	return out
}

// Retired reports whether the model is retired at t.
func (c *Capabilities) Retired(t time.Time) bool {
	if c.RetiredOn == "" {
		return false
	}
	d, err := time.Parse("2006-01-02", c.RetiredOn)
	return err == nil && !t.Before(d)
}

// PriceAt returns the price table in force for a model at t.
func PriceAt(provider, model string, t time.Time) (*PriceTable, error) {
	p, err := Info(provider)
	if err != nil {
		return nil, err
	}
	var best *PriceTable
	for i := range p.Prices {
		pt := &p.Prices[i]
		if pt.Model != model {
			continue
		}
		d, err := time.Parse("2006-01-02", pt.Effective)
		if err != nil || t.Before(d) {
			continue
		}
		if best == nil || pt.Effective > best.Effective {
			best = pt
		}
	}
	if best == nil {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush has no prices for %s yet, so it can't say what a video costs.", model).
			WithDetails(map[string]any{"provider": provider, "model": model})
	}
	cp := *best
	return &cp, nil
}

func usd(tokens int64, perMillion float64) float64 { return float64(tokens) * perMillion / 1e6 }

// RoundUSD rounds money to a tenth of a cent.
func RoundUSD(v float64) float64 { return math.Round(v*1000) / 1000 }

// Estimate prices a request: video seconds at its resolution, the pictures,
// clips and words sent, and an allowance for the model's thinking.
func (pt *PriceTable) Estimate(req *Request) (*Estimate, error) {
	if len(pt.PerSecondUSD) > 0 {
		ps, ok := pt.PerSecondUSD[req.Resolution]
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "There's no price for %s video.", req.Resolution)
		}
		e := &Estimate{Model: pt.Model, PriceTable: pt.Effective, VideoUSD: ps * float64(req.Seconds), PerSecondUSD: ps}
		e.USD = RoundUSD(e.VideoUSD)
		return e, nil
	}
	tps, ok := pt.VideoTokensPerSecond[req.Resolution]
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "There's no price for %s video.", req.Resolution)
	}
	e := &Estimate{Model: pt.Model, PriceTable: pt.Effective, VideoTokens: tps * int64(req.Seconds), ThinkingTokens: pt.ThinkingTokensAllowance}
	cpt := pt.PromptCharsPerToken
	if cpt <= 0 {
		cpt = 4
	}
	e.InputTokens = int64(len([]rune(req.Prompt))+cpt-1) / int64(cpt)
	e.InputTokens += int64(len(req.Images)) * pt.ImageInputTokens
	for _, v := range req.Videos {
		e.InputTokens += int64(math.Ceil(v.Seconds)) * pt.VideoInputTokensPerSecond
	}
	e.VideoUSD, e.ThinkingUSD, e.InputUSD = usd(e.VideoTokens, pt.VideoOutputPerMillion), usd(e.ThinkingTokens, pt.TextOutputPerMillion), usd(e.InputTokens, pt.InputPerMillion)
	e.VideoPerMillion, e.TextPerMillion, e.InputPerMillion = pt.VideoOutputPerMillion, pt.TextOutputPerMillion, pt.InputPerMillion
	e.USD = RoundUSD(e.VideoUSD + e.ThinkingUSD + e.InputUSD)
	e.PerSecondUSD = RoundUSD(usd(tps, pt.VideoOutputPerMillion))
	return e, nil
}

// Actual is what a finished request cost, from the usage the provider reported.
func (pt *PriceTable) Actual(u Usage) Cost {
	if len(pt.PerSecondUSD) > 0 {
		return Cost{} // priced by the second: what was asked for is what is paid (the estimate)
	}
	c := Cost{VideoTokens: u.VideoTokens, InputTokens: u.InputTokens, ThinkingTokens: u.ThoughtTokens, TextTokens: u.TextTokens}
	if c.VideoTokens == 0 && c.TextTokens == 0 && u.OutputTokens > 0 {
		c.VideoTokens = u.OutputTokens // no split by kind: count it all as video (the higher price)
	}
	c.USD = RoundUSD(usd(c.VideoTokens, pt.VideoOutputPerMillion) + usd(c.TextTokens+c.ThinkingTokens, pt.TextOutputPerMillion) + usd(c.InputTokens, pt.InputPerMillion))
	return c
}

// Check refuses a request the model can't do, in plain words. extendFrom is
// the length of the clip being extended (0 otherwise).
func (c *Capabilities) Check(req *Request, extendFrom float64, now time.Time) error {
	bad := func(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }
	if c.Retired(now) {
		return apperr.New(apperr.Provider, "%s was retired on %s, so it can't make videos any more.", c.Title, c.RetiredOn).
			WithHint("Choose another model in Settings, Integrations.").WithDetails(map[string]any{"reason": KindRetired, "model": c.ID})
	}
	if !slices.Contains(c.Tasks, req.Task) {
		return bad("%s can't do that kind of shot.", c.Title)
	}
	if req.Seconds < c.MinSeconds || req.Seconds > c.MaxSeconds {
		return bad("%s makes shots of %d to %d seconds.", c.Title, c.MinSeconds, c.MaxSeconds)
	}
	if len(c.SecondsAllowed) > 0 && !slices.Contains(c.SecondsAllowed, req.Seconds) {
		return bad("%s makes shots of %s seconds.", c.Title, joinOr(c.SecondsAllowed))
	}
	if !slices.Contains(c.Resolutions, req.Resolution) {
		return bad("%s makes video at %s, not %s.", c.Title, strings.Join(c.Resolutions, ", "), req.Resolution)
	}
	if !slices.Contains(c.AspectRatios, req.AspectRatio) {
		return bad("%s makes wide (16:9) or tall (9:16) video, not %s.", c.Title, req.AspectRatio)
	}
	if len(req.Images) > c.MaxImageRefs {
		return bad("A shot can use at most %d pictures; this one has %d.", c.MaxImageRefs, len(req.Images))
	}
	if strings.TrimSpace(req.Prompt) == "" {
		return bad("Say what the shot should show.")
	}
	switch req.Task {
	case TaskEdit, TaskExtend:
		if len(req.Videos) != 1 {
			return bad("Choose the one clip to work on.")
		}
		if v := req.Videos[0]; v.Seconds > float64(c.InputVideoMaxSeconds)+0.05 {
			return bad("%s works on clips of up to %d seconds; this one is %.1f seconds. Pick a shorter part.", c.Title, c.InputVideoMaxSeconds, v.Seconds)
		}
		if req.Task == TaskExtend && extendFrom+float64(req.Seconds) > float64(c.ExtendTotalSeconds)+0.05 {
			return bad("%s extends a shot to at most %d seconds in all; this one is already %.0f seconds.", c.Title, c.ExtendTotalSeconds, extendFrom)
		}
	default:
		if len(req.Videos) > c.MaxVideoRefs {
			return bad("A shot can use at most %d short clips of someone; this one has %d.", c.MaxVideoRefs, len(req.Videos))
		}
		for _, v := range req.Videos {
			if v.Seconds > c.VideoRefMaxSeconds+0.05 {
				return bad("Clips that show someone can be at most %g seconds long.", c.VideoRefMaxSeconds)
			}
		}
	}
	if req.Task == TaskInterpolate && len(req.Images) < 2 {
		return bad("A shot between two frames needs both frames.")
	}
	return nil
}

// Describe is a model in a few plain words: "Gemini Omni 1.1 Flash: shots of 3 to 10 seconds, up to 4K, with sound".
func (c *Capabilities) Describe() string {
	top := c.Resolutions[len(c.Resolutions)-1]
	return fmt.Sprintf("%s: shots of %d to %d seconds, up to %s, with sound", c.Title, c.MinSeconds, c.MaxSeconds, ResolutionLabel(top))
}

// ResolutionLabel is a resolution as people write it: 720p, 4K.
func ResolutionLabel(r string) string {
	if strings.EqualFold(r, "4k") {
		return "4K"
	}
	return r
}

func joinOr(xs []int) string {
	var out []string
	for _, x := range xs {
		out = append(out, fmt.Sprint(x))
	}
	if len(out) < 2 {
		return strings.Join(out, "")
	}
	return strings.Join(out[:len(out)-1], ", ") + " or " + out[len(out)-1]
}

// PerSecond is what a second of video costs at a resolution, from a table
// priced by tokens or by the second (false when it has no price for it).
func (pt *PriceTable) PerSecond(resolution string) (float64, bool) {
	if len(pt.PerSecondUSD) > 0 {
		v, ok := pt.PerSecondUSD[resolution]
		return v, ok
	}
	tps, ok := pt.VideoTokensPerSecond[resolution]
	return RoundUSD(float64(tps) * pt.VideoOutputPerMillion / 1e6), ok
}
