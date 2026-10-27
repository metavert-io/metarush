package providers

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
)

// ElevenLabsName is the provider ID.
const ElevenLabsName = "elevenlabs"

// EnvElevenLabsBaseURL overrides the API endpoint. Only ElevenLabs' own hosts
// and loopback test servers are accepted, so a key can never be sent elsewhere.
const EnvElevenLabsBaseURL = "METARUSH_ELEVENLABS_BASE_URL"

// EnvNoPaidServices set to 1 makes this process refuse every paid AI
// service's real address, as it is under go test: browser runs set it
// (scripts/lib/e2e.sh), so a run whose fakes aren't set up fails plainly
// instead of reaching a real service. It can only make MetaRush stricter.
const EnvNoPaidServices = "METARUSH_NO_PAID_SERVICES"

// RealServicesRefused reports whether this process may only talk to fakes on
// this computer: under go test, or in a browser run (EnvNoPaidServices).
func RealServicesRefused() bool {
	return testing.Testing() || noPaidServices()
}

func noPaidServices() bool { return strings.TrimSpace(os.Getenv(EnvNoPaidServices)) == "1" }

var elevenHosts = map[string]string{
	"":   "https://api.elevenlabs.io",
	"us": "https://api.us.elevenlabs.io",
	"eu": "https://api.eu.residency.elevenlabs.io",
	"in": "https://api.in.residency.elevenlabs.io",
	"sg": "https://api.sg.residency.elevenlabs.io",
}

// ElevenLabsBaseURL returns the API base URL for a region (or the override).
// Under go test and in browser runs (RealServicesRefused) only a test server
// on this computer is accepted, so a test that forgets to set up its fake can
// never reach ElevenLabs (and spend money).
func ElevenLabsBaseURL(region string) (string, error) {
	base, err := elevenRegionBase(region)
	if o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvElevenLabsBaseURL)), "/"); o != "" {
		if !allowedElevenBase(o) {
			return "", apperr.New(apperr.NotConfigured, "%s must be an ElevenLabs API host or a local test server", EnvElevenLabsBaseURL)
		}
		base, err = o, nil
	}
	if err != nil {
		return "", err
	}
	if RealServicesRefused() && !localElevenBase(base) {
		return "", apperr.New(apperr.NotConfigured, "Tests never call ElevenLabs: set %s to a fake on this computer.", EnvElevenLabsBaseURL)
	}
	return base, nil
}

// elevenRegionBase is ElevenLabs' own API host for a region.
func elevenRegionBase(region string) (string, error) {
	base, ok := elevenHosts[strings.ToLower(region)]
	if !ok {
		return "", apperr.New(apperr.InvalidInput, "ElevenLabs has no region “%s”. Choose us, eu, in or sg, or leave it empty.", region)
	}
	return base, nil
}

func allowedElevenBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" {
		return false
	}
	for _, h := range elevenHosts {
		if raw == h {
			return true
		}
	}
	return localElevenBase(raw)
}

// localElevenBase reports whether a base URL is a server on this computer.
func localElevenBase(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := u.Hostname()
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// ElevenLabs is the provider registered in the registry.
type ElevenLabs struct{}

func init() { Register(ElevenLabs{}) }

func (ElevenLabs) Name() string     { return ElevenLabsName }
func (ElevenLabs) Title() string    { return "ElevenLabs" }
func (ElevenLabs) Category() string { return CategoryVoice }
func (ElevenLabs) Summary() string {
	return "Natural-sounding AI narration for your videos, with word timing for captions, and clearer voices in the cloud. The same key makes sound effects, music, dubs into other languages and recordings in another voice, and writes down recordings in the cloud when you ask. You pay ElevenLabs for what it makes, and MetaRush shows the price first."
}
func (ElevenLabs) KeyHelp() string {
	return "Create one in your ElevenLabs account under Developers → API keys. If you limit what the key may do, allow text to speech, voices, models, audio isolation and reading your user info; for the other AI services also sound effects, music, dubbing, the voice changer, speech to text and history."
}
func (ElevenLabs) Capabilities() []string {
	return []string{"tts", "word-timing", "voices", "voice-library", "models", "voice-cloning", "voice-isolation", "usage", "sound-effects", "music", "dubbing", "voice-changer", "speech-to-text"}
}

// Test checks a key: it must be accepted, and plan usage is read when the key may.
func (e ElevenLabs) Test(ctx context.Context, key string) (*TestResult, error) {
	c, err := NewElevenClient(key, "")
	if err != nil {
		return nil, err
	}
	sub, err := c.Subscription(ctx)
	if err == nil {
		return &TestResult{OK: true, Message: fmt.Sprintf("Connected. %s plan: %s of %s characters used this period.", titleCase(sub.Tier), commas(sub.CharacterCount), commas(sub.CharacterLimit)),
			Usage: sub.Usage()}, nil
	}
	var pe *ProviderError
	if errors.As(err, &pe) && pe.Status == http.StatusUnauthorized && !pe.MissingPermission {
		return &TestResult{OK: false, Message: "ElevenLabs did not accept this key. Check that it was copied completely and has not been deleted."}, nil
	}
	if errors.As(err, &pe) && (pe.Status == http.StatusForbidden || pe.MissingPermission) {
		if _, err2 := c.Models(ctx); err2 == nil {
			return &TestResult{OK: true, Message: "Connected. This key cannot read your plan usage, so MetaRush cannot show how many characters are left."}, nil
		}
		return &TestResult{OK: false, Message: "This key is missing permissions MetaRush needs (text to speech, voices, models)."}, nil
	}
	return nil, err
}

// ElevenClient talks to the ElevenLabs API. The key is sent only in the
// xi-api-key header, and only to the configured API host.
type ElevenClient struct {
	Base string
	key  string
	HTTP *http.Client
}

// NewElevenClient builds a client for a region (see ElevenLabsBaseURL).
func NewElevenClient(key, region string) (*ElevenClient, error) {
	base, err := ElevenLabsBaseURL(region)
	if err != nil {
		return nil, err
	}
	return &ElevenClient{Base: base, key: key, HTTP: &http.Client{Timeout: 120 * time.Second}}, nil
}

// ProviderError is an API error, in plain words, without the key.
type ProviderError struct {
	Status            int
	Code              string // provider status string (e.g. quota_exceeded)
	Message           string
	MissingPermission bool
}

func (e *ProviderError) Error() string { return e.Message }

// AppError maps provider errors to stable codes.
func (e *ProviderError) AppError() *apperr.Error {
	code := apperr.Provider
	if e.Status == http.StatusUnauthorized && !e.MissingPermission {
		code = apperr.NotConfigured
	}
	return apperr.New(code, "%s", e.Message).WithDetails(map[string]any{"provider": ElevenLabsName, "httpStatus": e.Status, "providerStatus": e.Code})
}

func (c *ElevenClient) do(ctx context.Context, method, path string, body io.Reader, contentType string, out any) (err error) {
	call, start := elevenCall(ctx, method, path), time.Now()
	defer func() { call.Duration = time.Since(start); logbook.ProviderDone(ctx, call, err) }() // the activity log: never the key or the bodies
	req, err := http.NewRequestWithContext(ctx, method, c.Base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("xi-api-key", c.key)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := c.HTTP.Do(req)
	if err != nil {
		return apperr.Wrap(apperr.Provider, err, "MetaRush couldn't reach ElevenLabs. Check the internet connection and try again.")
	}
	defer res.Body.Close()
	call.Status, call.RequestID = res.StatusCode, res.Header.Get("request-id")
	data, err := io.ReadAll(io.LimitReader(res.Body, 256<<20))
	call.Bytes = len(data)
	if err != nil {
		return apperr.Wrap(apperr.Provider, err, "The connection to ElevenLabs dropped. Try again.")
	}
	if res.StatusCode >= 300 {
		return elevenError(res.StatusCode, data)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return apperr.Wrap(apperr.Provider, err, "ElevenLabs sent an answer MetaRush couldn't read. Try again in a moment.")
	}
	return nil
}

func elevenError(status int, data []byte) error {
	var body struct {
		Detail json.RawMessage `json:"detail"`
	}
	_ = json.Unmarshal(data, &body)
	pe := &ProviderError{Status: status}
	var obj struct {
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	var str string
	switch {
	case json.Unmarshal(body.Detail, &obj) == nil && (obj.Status != "" || obj.Message != ""):
		pe.Code, pe.Message = obj.Status, obj.Message
	case json.Unmarshal(body.Detail, &str) == nil:
		pe.Message = str
	}
	if strings.Contains(pe.Code, "permission") || strings.Contains(strings.ToLower(pe.Message), "missing the permission") {
		pe.MissingPermission = true
	}
	plain := ""
	switch {
	case status == http.StatusUnauthorized && !pe.MissingPermission:
		plain = "ElevenLabs did not accept the key"
	case pe.MissingPermission:
		plain = "The ElevenLabs key is missing a permission this needs"
	case status == http.StatusPaymentRequired || strings.Contains(pe.Code, "quota"):
		plain = "Your ElevenLabs plan has no characters left for this"
	case status == http.StatusTooManyRequests:
		plain = "ElevenLabs is busy right now; try again in a minute"
	case status == http.StatusUnprocessableEntity:
		plain = "ElevenLabs couldn't use these settings"
	case status >= 500:
		plain = "ElevenLabs had a problem on its side; try again later"
	default:
		plain = "ElevenLabs refused the request"
	}
	if pe.Message != "" {
		plain += ": " + pe.Message
	}
	pe.Message = plain
	return pe
}

// Subscription is GET /v1/user/subscription (the fields MetaRush uses).
type Subscription struct {
	Tier                       string `json:"tier"`
	CharacterCount             int64  `json:"character_count"`
	CharacterLimit             int64  `json:"character_limit"`
	NextCharacterCountResetUTC int64  `json:"next_character_count_reset_unix"`
	CanUseInstantVoiceCloning  bool   `json:"can_use_instant_voice_cloning"`
	Status                     string `json:"status"`
}

// Usage converts the subscription to the registry's usage shape.
func (s *Subscription) Usage() *Usage {
	u := &Usage{Plan: s.Tier, Used: s.CharacterCount, Limit: s.CharacterLimit, Unit: "characters"}
	if s.NextCharacterCountResetUTC > 0 {
		t := time.Unix(s.NextCharacterCountResetUTC, 0).UTC()
		u.ResetsAt = &t
	}
	return u
}

// Remaining is the characters left this period (-1 when unknown).
func (s *Subscription) Remaining() int64 {
	if s.CharacterLimit <= 0 {
		return -1
	}
	return s.CharacterLimit - s.CharacterCount
}

func (c *ElevenClient) Subscription(ctx context.Context) (*Subscription, error) {
	var s Subscription
	if err := c.do(ctx, http.MethodGet, "/v1/user/subscription", nil, "", &s); err != nil {
		return nil, err
	}
	return &s, nil
}

// Voice is a voice from the account (GET /v2/voices) or the shared library.
type Voice struct {
	VoiceID     string            `json:"voiceId"`
	Name        string            `json:"name"`
	Category    string            `json:"category,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"` // gender, accent, age, use_case, descriptive
	PreviewURL  string            `json:"-"`                // fetched by the daemon, never sent to the browser
	HasPreview  bool              `json:"hasPreview"`
	Library     bool              `json:"library,omitempty"`       // from the shared voice library (added to the account on first use)
	OwnerID     string            `json:"publicOwnerId,omitempty"` // library voices
	Cloned      bool              `json:"cloned,omitempty"`
}

// VoicesPage is one page of voices.
type VoicesPage struct {
	Voices        []Voice `json:"voices"`
	HasMore       bool    `json:"hasMore"`
	NextPageToken string  `json:"nextPageToken,omitempty"`
}

type apiVoice struct {
	VoiceID     string            `json:"voice_id"`
	Name        string            `json:"name"`
	Category    string            `json:"category"`
	Description string            `json:"description"`
	Labels      map[string]string `json:"labels"`
	PreviewURL  string            `json:"preview_url"`
}

func (v apiVoice) voice() Voice {
	return Voice{VoiceID: v.VoiceID, Name: v.Name, Category: v.Category, Description: v.Description, Labels: v.Labels,
		PreviewURL: v.PreviewURL, HasPreview: v.PreviewURL != "", Cloned: v.Category == "cloned"}
}

// Voices lists the account's voices (premade, cloned, generated, library copies).
func (c *ElevenClient) Voices(ctx context.Context, search, pageToken string, pageSize int, ids []string) (*VoicesPage, error) {
	q := url.Values{}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 100
	}
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("include_total_count", "false")
	if search != "" {
		q.Set("search", search)
	}
	if pageToken != "" {
		q.Set("next_page_token", pageToken)
	}
	for _, id := range ids {
		q.Add("voice_ids", id)
	}
	var res struct {
		Voices        []apiVoice `json:"voices"`
		HasMore       bool       `json:"has_more"`
		NextPageToken string     `json:"next_page_token"`
	}
	if err := c.do(ctx, http.MethodGet, "/v2/voices?"+q.Encode(), nil, "", &res); err != nil {
		return nil, err
	}
	out := &VoicesPage{Voices: []Voice{}, HasMore: res.HasMore, NextPageToken: res.NextPageToken}
	for _, v := range res.Voices {
		out.Voices = append(out.Voices, v.voice())
	}
	return out, nil
}

// SharedVoices searches the public voice library.
func (c *ElevenClient) SharedVoices(ctx context.Context, search string, page, pageSize int) (*VoicesPage, error) {
	q := url.Values{}
	if pageSize <= 0 || pageSize > 100 {
		pageSize = 30
	}
	q.Set("page_size", strconv.Itoa(pageSize))
	q.Set("page", strconv.Itoa(max(0, page)))
	if search != "" {
		q.Set("search", search)
	}
	var res struct {
		Voices []struct {
			PublicOwnerID string `json:"public_owner_id"`
			VoiceID       string `json:"voice_id"`
			Name          string `json:"name"`
			Accent        string `json:"accent"`
			Gender        string `json:"gender"`
			Age           string `json:"age"`
			Descriptive   string `json:"descriptive"`
			UseCase       string `json:"use_case"`
			Category      string `json:"category"`
			Language      string `json:"language"`
			Description   string `json:"description"`
			PreviewURL    string `json:"preview_url"`
		} `json:"voices"`
		HasMore bool `json:"has_more"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/shared-voices?"+q.Encode(), nil, "", &res); err != nil {
		return nil, err
	}
	out := &VoicesPage{Voices: []Voice{}, HasMore: res.HasMore}
	if res.HasMore {
		out.NextPageToken = strconv.Itoa(max(0, page) + 1)
	}
	for _, v := range res.Voices {
		labels := map[string]string{}
		for k, val := range map[string]string{"accent": v.Accent, "gender": v.Gender, "age": v.Age, "descriptive": v.Descriptive, "use_case": v.UseCase, "language": v.Language} {
			if val != "" {
				labels[k] = val
			}
		}
		out.Voices = append(out.Voices, Voice{VoiceID: v.VoiceID, Name: v.Name, Category: v.Category, Description: v.Description, Labels: labels,
			PreviewURL: v.PreviewURL, HasPreview: v.PreviewURL != "", Library: true, OwnerID: v.PublicOwnerID})
	}
	return out, nil
}

// AddSharedVoice copies a library voice into the account and returns its voice ID.
func (c *ElevenClient) AddSharedVoice(ctx context.Context, ownerID, voiceID, name string) (string, error) {
	b, _ := json.Marshal(map[string]any{"new_name": name})
	var res struct {
		VoiceID string `json:"voice_id"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/voices/add/"+url.PathEscape(ownerID)+"/"+url.PathEscape(voiceID), bytes.NewReader(b), "application/json", &res); err != nil {
		return "", err
	}
	return res.VoiceID, nil
}

// Model is a model from GET /v1/models.
type Model struct {
	ModelID          string   `json:"modelId"`
	Name             string   `json:"name"`
	Description      string   `json:"description,omitempty"`
	TTS              bool     `json:"tts"`
	CanUseStyle      bool     `json:"canUseStyle"`
	CanUseBoost      bool     `json:"canUseSpeakerBoost"`
	MaxChars         int      `json:"maxCharacters,omitempty"`
	CostMultiplier   float64  `json:"costMultiplier"` // credits per character
	Languages        []string `json:"languages"`
	LanguageCodes    []string `json:"languageCodes"`
	RequiresAlpha    bool     `json:"requiresAlpha,omitempty"`
	Timestamps       bool     `json:"timestamps"` // word timing (alignment) is expected; see TimestampsNote
	TimestampsNote   string   `json:"timestampsNote,omitempty"`
	RecommendedScore int      `json:"-"`
}

func (c *ElevenClient) Models(ctx context.Context) ([]Model, error) {
	var res []struct {
		ModelID             string  `json:"model_id"`
		Name                string  `json:"name"`
		Description         string  `json:"description"`
		CanDoTTS            bool    `json:"can_do_text_to_speech"`
		CanUseStyle         bool    `json:"can_use_style"`
		CanUseSpeakerBoost  bool    `json:"can_use_speaker_boost"`
		RequiresAlphaAccess bool    `json:"requires_alpha_access"`
		MaxTextLength       int     `json:"maximum_text_length_per_request"`
		MaxCharsSubscribed  int     `json:"max_characters_request_subscribed_user"`
		TokenCostFactor     float64 `json:"token_cost_factor"`
		ModelRates          *struct {
			CharacterCostMultiplier float64 `json:"character_cost_multiplier"`
		} `json:"model_rates"`
		Languages []struct {
			LanguageID string `json:"language_id"`
			Name       string `json:"name"`
		} `json:"languages"`
	}
	if err := c.do(ctx, http.MethodGet, "/v1/models", nil, "", &res); err != nil {
		return nil, err
	}
	out := make([]Model, 0, len(res))
	for _, m := range res {
		mm := Model{ModelID: m.ModelID, Name: m.Name, Description: m.Description, TTS: m.CanDoTTS, CanUseStyle: m.CanUseStyle, CanUseBoost: m.CanUseSpeakerBoost,
			MaxChars: m.MaxTextLength, RequiresAlpha: m.RequiresAlphaAccess, CostMultiplier: 1, Timestamps: m.CanDoTTS, Languages: []string{}, LanguageCodes: []string{}}
		if mm.MaxChars == 0 {
			mm.MaxChars = m.MaxCharsSubscribed
		}
		switch {
		case m.ModelRates != nil && m.ModelRates.CharacterCostMultiplier > 0:
			mm.CostMultiplier = m.ModelRates.CharacterCostMultiplier
		case m.TokenCostFactor > 0:
			mm.CostMultiplier = m.TokenCostFactor
		}
		for _, l := range m.Languages {
			mm.Languages = append(mm.Languages, l.Name)
			mm.LanguageCodes = append(mm.LanguageCodes, l.LanguageID)
		}
		if mm.TTS {
			mm.TimestampsNote = "word timing comes with each generation; if a model ever returns none, MetaRush estimates it and says so"
		}
		out = append(out, mm)
	}
	return out, nil
}

// PickDefaultModel prefers a high-quality multilingual text-to-speech model
// that supports word timing, chosen from the live list (never a hard-coded ID):
// multilingual and broad language support first, full-quality (cost
// multiplier ≥ 1) over the cheaper fast models, generally available over alpha.
func PickDefaultModel(models []Model) *Model {
	best, bestScore := -1, -1<<30
	for i, m := range models {
		if !m.TTS || !m.Timestamps {
			continue
		}
		score := 0
		id := strings.ToLower(m.ModelID + " " + m.Name)
		if strings.Contains(id, "multilingual") {
			score += 40
		}
		score += min(len(m.LanguageCodes), 40)
		if m.CostMultiplier >= 1 {
			score += 25
		}
		if strings.Contains(id, "flash") || strings.Contains(id, "turbo") {
			score -= 10
		}
		if m.RequiresAlpha {
			score -= 60
		}
		if score > bestScore || score == bestScore && best >= 0 && m.ModelID < models[best].ModelID {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return nil
	}
	return &models[best]
}

// TTSVoiceSettings is the ElevenLabs voice_settings object.
type TTSVoiceSettings struct {
	Stability       *float64 `json:"stability,omitempty"`
	SimilarityBoost *float64 `json:"similarity_boost,omitempty"`
	Style           *float64 `json:"style,omitempty"`
	UseSpeakerBoost *bool    `json:"use_speaker_boost,omitempty"`
	Speed           *float64 `json:"speed,omitempty"`
}

// TTSRequest is one generation.
type TTSRequest struct {
	VoiceID      string
	ModelID      string
	Text         string
	Settings     TTSVoiceSettings
	OutputFormat string
	PreviousText string
	NextText     string
}

// TimedWord is a word with timing from the provider's alignment.
type TimedWord struct {
	Text     string
	From, To float64
}

// TTSResult is generated audio with word timing.
type TTSResult struct {
	Audio      []byte
	Ext        string // file extension matching the bytes (mp3, wav, …)
	Words      []TimedWord
	Characters int
	Aligned    bool // the provider returned character alignment
}

// TTS generates speech with character timestamps (POST /v1/text-to-speech/{voice}/with-timestamps).
func (c *ElevenClient) TTS(ctx context.Context, r TTSRequest) (*TTSResult, error) {
	body := map[string]any{"text": r.Text, "model_id": r.ModelID}
	if r.Settings != (TTSVoiceSettings{}) {
		body["voice_settings"] = r.Settings
	}
	if r.PreviousText != "" {
		body["previous_text"] = r.PreviousText
	}
	if r.NextText != "" {
		body["next_text"] = r.NextText
	}
	b, _ := json.Marshal(body)
	format := r.OutputFormat
	if format == "" {
		format = DefaultOutputFormat
	}
	var res struct {
		AudioBase64 string `json:"audio_base64"`
		Alignment   *struct {
			Characters []string  `json:"characters"`
			Start      []float64 `json:"character_start_times_seconds"`
			End        []float64 `json:"character_end_times_seconds"`
		} `json:"alignment"`
	}
	path := "/v1/text-to-speech/" + url.PathEscape(r.VoiceID) + "/with-timestamps?output_format=" + url.QueryEscape(format)
	if err := c.do(withCharacters(ctx, len([]rune(r.Text))), http.MethodPost, path, bytes.NewReader(b), "application/json", &res); err != nil {
		return nil, err
	}
	audio, err := base64.StdEncoding.DecodeString(res.AudioBase64)
	if err != nil || len(audio) == 0 {
		return nil, apperr.New(apperr.Provider, "ElevenLabs sent no audio. Try again in a moment.")
	}
	out := &TTSResult{Audio: audio, Ext: audioExt(audio, format), Characters: len([]rune(r.Text))}
	if a := res.Alignment; a != nil && len(a.Characters) > 0 && len(a.Start) == len(a.Characters) && len(a.End) == len(a.Characters) {
		out.Words, out.Aligned = WordsFromAlignment(a.Characters, a.Start, a.End), true
	}
	return out, nil
}

// DefaultOutputFormat is MP3 at 44.1 kHz, 128 kbps (available on every plan).
const DefaultOutputFormat = "mp3_44100_128"

// WordsFromAlignment groups character timings into words: a word starts at its
// first character and ends at its last; punctuation stays attached.
func WordsFromAlignment(chars []string, start, end []float64) []TimedWord {
	var out []TimedWord
	var cur strings.Builder
	from, to := -1.0, 0.0
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, TimedWord{Text: t, From: round3(from), To: round3(to)})
		}
		cur.Reset()
		from = -1
	}
	for i, ch := range chars {
		isSpace := strings.TrimFunc(ch, unicode.IsSpace) == ""
		if isSpace {
			flush()
			continue
		}
		if from < 0 {
			from = start[i]
		}
		cur.WriteString(ch)
		if end[i] > to || cur.Len() == len(ch) {
			to = end[i]
		}
	}
	flush()
	return out
}

func round3(v float64) float64 { return float64(int64(v*1000+0.5)) / 1000 }

// audioExt names audio bytes by their content (RIFF → wav, ID3/MPEG → mp3, OggS → ogg).
func audioExt(b []byte, format string) string {
	switch {
	case len(b) >= 4 && string(b[:4]) == "RIFF":
		return "wav"
	case len(b) >= 4 && string(b[:4]) == "OggS":
		return "ogg"
	case len(b) >= 3 && string(b[:3]) == "ID3", len(b) >= 2 && b[0] == 0xFF && b[1]&0xE0 == 0xE0:
		return "mp3"
	}
	if i := strings.Index(format, "_"); i > 0 {
		return format[:i]
	}
	return "bin"
}

// CloneVoice creates an instant voice clone from sample files (POST /v1/voices/add).
func (c *ElevenClient) CloneVoice(ctx context.Context, name, description string, files []string, removeNoise bool) (string, bool, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	_ = w.WriteField("name", name)
	if description != "" {
		_ = w.WriteField("description", description)
	}
	if removeNoise {
		_ = w.WriteField("remove_background_noise", "true")
	}
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return "", false, err
		}
		part, err := w.CreateFormFile("files", filepath.Base(f))
		if err != nil {
			return "", false, err
		}
		if _, err := part.Write(data); err != nil {
			return "", false, err
		}
	}
	if err := w.Close(); err != nil {
		return "", false, err
	}
	var res struct {
		VoiceID              string `json:"voice_id"`
		RequiresVerification bool   `json:"requires_verification"`
	}
	if err := c.do(ctx, http.MethodPost, "/v1/voices/add", &buf, w.FormDataContentType(), &res); err != nil {
		return "", false, err
	}
	return res.VoiceID, res.RequiresVerification, nil
}

// FetchPreview downloads a voice's public preview sample. The API key is never
// sent: previews are plain HTTPS files (or the local test server).
func FetchPreview(ctx context.Context, previewURL string, maxBytes int64) ([]byte, string, error) {
	u, err := url.Parse(previewURL)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && allowedElevenBase(u.Scheme+"://"+u.Host))) {
		return nil, "", apperr.New(apperr.Provider, "This voice has no sample to play.")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, previewURL, nil)
	if err != nil {
		return nil, "", err
	}
	res, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return nil, "", apperr.Wrap(apperr.Provider, err, "MetaRush couldn't download the voice sample. Check the internet connection.")
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, "", apperr.New(apperr.Provider, "The voice sample isn't available right now.").WithDetails(map[string]any{"httpStatus": res.StatusCode})
	}
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBytes+1))
	if err != nil {
		return nil, "", err
	}
	if int64(len(data)) > maxBytes {
		return nil, "", apperr.New(apperr.Provider, "The voice sample is unexpectedly large, so MetaRush didn't play it.")
	}
	ct := res.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "audio/") {
		ct = map[string]string{"mp3": "audio/mpeg", "wav": "audio/wav", "ogg": "audio/ogg"}[audioExt(data, "")]
		if ct == "" {
			ct = "audio/mpeg"
		}
	}
	return data, ct, nil
}

func titleCase(s string) string {
	if s == "" {
		return "Your"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// commas formats 12345 as 12,345.
// Commas formats a count with thousands separators: 20000 → "20,000".
func Commas(n int64) string { return commas(n) }

func commas(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	parts = append([]string{s}, parts...)
	out := strings.Join(parts, ",")
	if neg {
		out = "-" + out
	}
	return out
}

// SortVoices orders voices: favorites first, then by name.
func SortVoices(vs []Voice, favorites map[string]bool) {
	sort.SliceStable(vs, func(i, j int) bool {
		fi, fj := favorites[vs[i].VoiceID], favorites[vs[j].VoiceID]
		if fi != fj {
			return fi
		}
		return strings.ToLower(vs[i].Name) < strings.ToLower(vs[j].Name)
	})
}
