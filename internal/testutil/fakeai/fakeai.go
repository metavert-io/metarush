// Package fakeai is an in-process fake AI service for every kind of work
// (D224): a provider called "fake" with one model per kind in the catalogue
// and an adapter per kind, so the shared lifecycle (internal/aiwork), the
// conformance suite and the dashboard's checks run without any provider.
// It never talks to the network, accepts only the key it was installed with,
// and makes small real files: PNG pictures, WAV sound, a copy of the input
// for a dub, a timed transcript, a voice ID.
//
// Most kinds answer with the result straight away (one-shot calls); dubbing
// returns a ticket and finishes after ReadyAfter checks, like a real dubbing
// service. Behaviors make it misbehave on purpose: queue them with Script or
// put a marker in the prompt ("[fake:safety]"). Kinds: safety (declined
// when sent), rate (a 429), uncertain (sent, no answer), server (a 500),
// fail (the request fails while it runs), expired (the finished file is gone),
// delay (more checks before it finishes).
package fakeai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the fake provider's ID; Title how it is named.
const (
	Name  = "fake"
	Title = "Fake AI"
)

// Behavior kinds.
const (
	Safety    = "safety"
	Rate      = "rate"
	Uncertain = "uncertain"
	Server    = "server"
	Fail      = "fail"
	Expired   = "expired"
	Delay     = "delay"
	// Lost: the provider does the work, but the answer never comes back.
	Lost = "lost"
	// Hold: the request stays in flight until Release; if the caller hangs
	// up meanwhile, the provider does the work all the same.
	Hold = "hold"
	// ConsentMismatch: the provider didn't hear its consent words in the
	// reading sent with a presenter (D274).
	ConsentMismatch = "consent-mismatch"
)

// Behavior is one scripted misbehavior for the next request.
type Behavior struct {
	Kind string
	N    int // delay: extra checks
}

// Request is one request the fake received.
type Request struct {
	Kind     string
	Model    string
	Prompt   string
	Options  aiservices.Options
	Inputs   []aiservices.Media
	Ticket   string
	Polls    int
	Behavior Behavior
	Forgot   bool
	Canceled bool
}

// Fake is the installed fake provider.
type Fake struct {
	Key string
	// ReadyAfter is how many checks a dub needs before it is finished (default 1).
	ReadyAfter int

	mu      sync.Mutex
	script  []Behavior
	reqs    []*Request
	tickets map[string]*Request
	results map[string]*aiservices.Status
	voices  map[string]bool
	names   map[string]string // a voice copy's name at the provider
	// Presenters (D273): those made (their tagged names; removed ones are
	// gone), the consent requests asked for, the stock presenters.
	presenters map[string]string
	consents   map[string]bool
	videoDir   string
	// Trains: the fake trains on uploads unless opted out (D276).
	Trains   bool
	release  chan struct{}
	released bool
	calls    map[string]int
	problems []string
	nextID   int
}

// Install puts the fake provider into the catalogue and registers its
// adapters until the test ends.
func Install(t testing.TB, key string) *Fake {
	t.Helper()
	return InstallWith(t, key, Options{})
}

// Options change how the fake describes itself.
type Options struct {
	// Trains: its terms say it trains on what it is sent unless opted out (D276).
	Trains bool
	// NoConsentCheck: it has no consent check of its own (consent mode none).
	NoConsentCheck bool
	// StockUnlicensed: its terms don't say what its stock presenters may be used for (D275).
	StockUnlicensed bool
}

// InstallWith installs the fake described as asked.
func InstallWith(t testing.TB, key string, o Options) *Fake {
	t.Helper()
	f, remove, err := NewWith(key, o)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	return f
}

// integration is the fake in the integrations registry, so its key lives
// where every provider key does (METARUSH_FAKE_API_KEY in tests).
type integration struct{}

func (integration) Name() string           { return Name }
func (integration) Title() string          { return Title }
func (integration) Category() string       { return providers.CategoryVoice }
func (integration) Summary() string        { return "A fake AI service for tests." }
func (integration) KeyHelp() string        { return "Tests set it." }
func (integration) Capabilities() []string { return []string{"tests"} }
func (integration) Test(context.Context, string) (*providers.TestResult, error) {
	return &providers.TestResult{OK: true, Message: "Connected to the fake."}, nil
}

var registerOnce sync.Once

// New installs the fake (outside tests: the dashboard's checks); remove takes it out.
func New(key string) (*Fake, func(), error) { return NewWith(key, Options{}) }

// NewWith installs the fake described as asked.
func NewWith(key string, o Options) (*Fake, func(), error) {
	registerOnce.Do(func() { providers.Register(integration{}) })
	f := &Fake{Key: key, ReadyAfter: 1, tickets: map[string]*Request{}, results: map[string]*aiservices.Status{}, voices: map[string]bool{}, names: map[string]string{},
		release: make(chan struct{}), calls: map[string]int{}, presenters: map[string]string{}, consents: map[string]bool{}, Trains: o.Trains}
	if d, err := os.MkdirTemp("", "fakeai-video-"); err == nil {
		f.videoDir = d
	}
	c := Catalog()
	if o.Trains {
		c.Data.TrainsOnUploads, c.Data.OptOut = aiservices.TrainsUnlessOptedOut, "Write to the fake's privacy team."
	}
	if o.NoConsentCheck {
		c.Presenters.ConsentMode = aiservices.ConsentNone
	}
	if o.StockUnlicensed {
		c.Presenters.Stock = aiservices.StockLicence{Summary: "The fake's terms don't say.", Commercial: "unstated", ClientWork: "unstated", Ads: "unstated", Source: "https://example.com/terms", Checked: "2026-10-25"}
	}
	rm, err := aiservices.AddProvider(c)
	if err != nil {
		return nil, nil, err
	}
	for _, k := range aiservices.Kinds() {
		if k.Kind == aiservices.KindVideo {
			continue
		}
		aiservices.Register(&adapter{f: f, kind: k.Kind})
	}
	return f, func() {
		for _, k := range aiservices.Kinds() {
			aiservices.Unregister(Name, k.Kind)
		}
		rm()
		if f.videoDir != "" {
			_ = os.RemoveAll(f.videoDir)
		}
	}, nil
}

// Catalog is the fake provider's catalogue entry: one model per kind, with
// prices in every unit the real catalogue uses.
func Catalog() aiservices.ProviderInfo {
	lic := aiservices.Licence{Summary: "Made by a fake service for tests.", Commercial: "yes", Broadcast: "yes", Source: "https://example.com/terms", Checked: "2026-10-25"}
	m := func(id, kind string, tasks []string, l aiservices.Limits) aiservices.Model {
		return aiservices.Model{ID: id, Title: "Fake " + kind, Kind: kind, Tasks: tasks, Stage: "ga", Rank: 3, Limits: l, Licence: lic, Checked: "2026-10-25"}
	}
	p := func(model, kind, unit string, usd float64) aiservices.Price {
		return aiservices.Price{Model: model, Kind: kind, Effective: "2026-01-01", Checked: "2026-10-25", Source: "https://example.com/prices", Unit: unit, USD: usd}
	}
	pic := p("fake-picture", aiservices.KindPicture, "picture", 0.04)
	pic.ByOption = map[string]float64{"1K": 0.04, "2K": 0.06, "4K": 0.12}
	return aiservices.ProviderInfo{Name: Name, Title: Title, Key: Name, KeyHost: "127.0.0.1", Route: aiservices.RouteDirect,
		Data: aiservices.DataTerms{Summary: "The fake keeps nothing after a test.", Training: "None.", TrainsOnUploads: aiservices.TrainsNo, Retention: "None.", Region: "This computer.",
			Source: "https://example.com/data", Checked: "2026-10-25"},
		ConsentScripts: map[string]string{"en-US": "I am the owner of this voice and I consent to this test."},
		Presenters: &aiservices.PresenterTerms{ConsentMode: aiservices.ConsentAPIUpload, ConsentLanguages: map[string]string{"en": "english", "es": "spanish"}, ConsentMinutes: 30,
			TakeAdvice: "A minute of the person talking to the camera.", Backgrounds: "Any colour.", Deletes: "Everything about the presenter.",
			Erasure: "Ask the fake.", Stock: aiservices.StockLicence{Summary: "Licensed for commercial use and client work; not for ads.", Commercial: "yes", ClientWork: "yes", Ads: "no",
				Source: "https://example.com/terms", Checked: "2026-10-25"}, Source: "https://example.com/presenters", Checked: "2026-10-25"},
		Models: []aiservices.Model{
			m("fake-picture", aiservices.KindPicture, []string{aiservices.TaskGenerate, aiservices.TaskEdit}, aiservices.Limits{Sizes: []string{"1K", "2K", "4K"}, AspectRatios: []string{"1:1", "16:9", "9:16"}, MaxInputs: 4, MaxPromptChars: 4000}),
			m("fake-voice", aiservices.KindVoice, []string{aiservices.TaskSpeak}, aiservices.Limits{MaxChars: 5000}),
			m("fake-voice-copy", aiservices.KindVoiceCopy, []string{aiservices.TaskCopy}, aiservices.Limits{SampleMinSeconds: 1, SampleMaxSeconds: 120, MaxInputs: 4}),
			m("fake-voice-change", aiservices.KindVoiceChange, []string{aiservices.TaskChange}, aiservices.Limits{MinSeconds: 0.5, MaxSeconds: 300}),
			m("fake-sound-effect", aiservices.KindSoundEffect, []string{aiservices.TaskGenerate}, aiservices.Limits{MinSeconds: 0.5, MaxSeconds: 30, MaxPromptChars: 1000}),
			m("fake-music", aiservices.KindMusic, []string{aiservices.TaskGenerate}, aiservices.Limits{MinSeconds: 3, MaxSeconds: 300, MaxPromptChars: 2000}),
			m("fake-dubbing", aiservices.KindDubbing, []string{aiservices.TaskDub}, aiservices.Limits{MinSeconds: 0.5, MaxSeconds: 3600}),
			m("fake-transcription", aiservices.KindTranscription, []string{aiservices.TaskTranscribe}, aiservices.Limits{MaxSeconds: 3600}),
			m("fake-presenter", aiservices.KindPresenter, []string{aiservices.TaskRead}, aiservices.Limits{MaxSeconds: 300, MaxInputs: 1}),
			m("fake-presenter-make", aiservices.KindPresenterMake, []string{aiservices.TaskTrain}, aiservices.Limits{SampleMinSeconds: 5, SampleMaxSeconds: 600}),
			m("fake-background-removal", aiservices.KindBackground, []string{aiservices.TaskCutOut}, aiservices.Limits{MaxInputs: 1}),
			m("fake-upscale", aiservices.KindUpscale, []string{aiservices.TaskUpscale}, aiservices.Limits{MaxInputs: 1, Scales: []int{2, 4}}),
			m("fake-lip-sync", aiservices.KindLipSync, []string{aiservices.TaskLipSync}, aiservices.Limits{MaxSeconds: 300}),
		},
		Prices: []aiservices.Price{pic,
			p("fake-voice", aiservices.KindVoice, "thousand-characters", 0.05),
			p("fake-voice-copy", aiservices.KindVoiceCopy, "request", 0),
			p("fake-voice-change", aiservices.KindVoiceChange, "minute", 0.10),
			p("fake-sound-effect", aiservices.KindSoundEffect, "minute", 0.12),
			p("fake-music", aiservices.KindMusic, "minute", 0.15),
			p("fake-dubbing", aiservices.KindDubbing, "minute", 0.50),
			p("fake-transcription", aiservices.KindTranscription, "hour", 0.30),
			presenterPrice(p("fake-presenter", aiservices.KindPresenter, "minute", 1.20)),
			p("fake-presenter-make", aiservices.KindPresenterMake, "request", 0.50),
			p("fake-background-removal", aiservices.KindBackground, "picture", 0.02),
			p("fake-upscale", aiservices.KindUpscale, "picture", 0.03),
			p("fake-lip-sync", aiservices.KindLipSync, "second", 0.05),
		}}
}

func presenterPrice(pr aiservices.Price) aiservices.Price {
	pr.RoundSeconds = 15
	return pr
}

// Script queues behaviors for the next requests (one each, in order).
func (f *Fake) Script(b ...Behavior) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.script = append(f.script, b...)
}

// Requests lists the requests received, oldest first.
func (f *Fake) Requests() []Request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []Request{}
	for _, r := range f.reqs {
		out = append(out, *r)
	}
	return out
}

// Calls reports how many times a step was called: submit, poll, fetch, cancel, forget, remove-voice.
func (f *Fake) Calls(name string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls[name]
}

// Kept counts requests whose results the fake still keeps (not forgotten).
func (f *Fake) Kept() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.reqs {
		if !r.Forgot {
			n++
		}
	}
	return n
}

// Release lets requests held in flight (Hold) finish.
func (f *Fake) Release() {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.released {
		f.released = true
		close(f.release)
	}
}

// Voices lists the voice copies the fake keeps.
func (f *Fake) Voices() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for v, ok := range f.voices {
		if ok {
			out = append(out, v)
		}
	}
	return out
}

// Problems lists things a careful client never does.
func (f *Fake) Problems() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string{}, f.problems...)
}

var markerRe = regexp.MustCompile(`\[fake:([a-z-]+)(?::(\d+))?\]`)

func (f *Fake) next(prompt string) Behavior {
	if m := markerRe.FindStringSubmatch(prompt); m != nil {
		n := 0
		fmt.Sscan(m[2], &n)
		return Behavior{Kind: m[1], N: n}
	}
	if len(f.script) > 0 {
		b := f.script[0]
		f.script = f.script[1:]
		return b
	}
	return Behavior{}
}

type adapter struct {
	f    *Fake
	kind string
}

func (a *adapter) Provider() string { return Name }
func (a *adapter) Kind() string     { return a.kind }

func (a *adapter) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(Name, a.kind, model)
}

func (a *adapter) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	m, err := aiservices.ModelFor(Name, a.kind, req.Model)
	if err != nil {
		return nil, err
	}
	return aiservices.EstimateFor(Name, m, req, at)
}

func keyErr() error {
	return &videogen.Error{Kind: videogen.KindKey, Provider: Name, HTTPStatus: 401, Message: "Fake AI did not accept the key."}
}

func (a *adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	f := a.f
	f.mu.Lock()
	f.calls["submit"]++
	if key != f.Key {
		f.mu.Unlock()
		return nil, keyErr()
	}
	b := f.next(req.Prompt)
	r := &Request{Kind: a.kind, Model: req.Model, Prompt: req.Prompt, Options: req.Options, Inputs: append([]aiservices.Media{}, req.Inputs...), Behavior: b}
	f.reqs = append(f.reqs, r)
	f.nextID++
	id := fmt.Sprintf("fake-%d", f.nextID)
	f.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	for _, in := range req.Inputs {
		if in.Path == "" {
			continue
		}
		if _, err := os.Stat(in.Path); err != nil {
			return nil, err
		}
	}
	if err := f.checkPresenter(a.kind, req, b); err != nil {
		f.mu.Lock()
		r.Forgot = true // refused: nothing kept
		f.mu.Unlock()
		return nil, err
	}
	switch b.Kind {
	case Safety:
		return nil, &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Message: "Fake AI declined this: its safety filters blocked it.", Detail: "blocked: [fake:safety]",
			Safety: &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: "blocked by the fake"}}
	case Rate:
		return nil, &videogen.Error{Kind: videogen.KindRate, Provider: Name, HTTPStatus: 429, Message: "Fake AI is busy; try again in a minute.", RetryAfter: time.Second}
	case Uncertain:
		r.Forgot = true // nothing kept that MetaRush could find
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached Fake AI, but no answer came back."}
	case Server:
		return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, HTTPStatus: 500, Message: "Fake AI had a problem on its side."}
	}
	st, err := a.result(req, id)
	if err != nil {
		return nil, err
	}
	made := func() { // what the provider keeps of a voice copy or a presenter, answered or not
		switch a.kind {
		case aiservices.KindVoiceCopy:
			f.voices[st.Outputs[0].VoiceID] = true
			f.names[st.Outputs[0].VoiceID] = aiservices.TaggedName(req.Options.Name, req.Tag)
		case aiservices.KindPresenterMake:
			f.presenters[st.Outputs[0].PresenterID] = aiservices.TaggedName(req.Options.Name, req.Tag)
		}
	}
	switch b.Kind {
	case Lost:
		f.mu.Lock()
		made()
		f.mu.Unlock()
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached Fake AI, but no answer came back."}
	case Hold:
		select {
		case <-f.release:
		case <-ctx.Done():
			f.mu.Lock()
			made()
			f.mu.Unlock()
			return nil, ctx.Err() // hung up: the provider made it all the same
		}
	}
	t := &aiservices.Ticket{SubmittedAt: time.Now().UTC()}
	f.mu.Lock()
	defer f.mu.Unlock()
	r.Ticket = id
	made()
	if a.kind == aiservices.KindDubbing || a.kind == aiservices.KindPresenter || a.kind == aiservices.KindPresenterMake || b.Kind == Delay || b.Kind == Fail {
		// A request that runs a while: a ticket to ask about.
		t.ID = id
		t.Files = []string{"files/" + id + "-input"}
		f.tickets[id] = r
		f.results[id] = st
		return t, nil
	}
	t.Extra = map[string]string{"history": id}
	t.Ready = st
	if b.Kind == Expired {
		for i := range st.Outputs {
			st.Outputs[i].Data, st.Outputs[i].URI = nil, "fake://gone/"+id
		}
	}
	f.results[id] = st
	return t, nil
}

// result makes what a request asks for.
func (a *adapter) result(req *aiservices.Request, id string) (*aiservices.Status, error) {
	sum := sha256.Sum256([]byte(req.Hash(Name)))
	h := hex.EncodeToString(sum[:])
	st := &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true}}
	o := aiservices.Output{}
	switch a.kind {
	case aiservices.KindPicture, aiservices.KindBackground, aiservices.KindUpscale:
		o.MIME, o.Data = "image/png", picture(sum[:], req.Options.AspectRatio)
	case aiservices.KindVoice:
		secs := float64(len([]rune(req.Prompt)))/14 + 0.3
		o.MIME, o.Data = "audio/wav", aiservices.ToneWAV(secs, tone(sum[:]))
		st.Usage.Characters = int64(len([]rune(req.Prompt)))
	case aiservices.KindSoundEffect, aiservices.KindMusic:
		secs := req.Options.Seconds
		if secs <= 0 {
			secs = 2
		}
		o.MIME, o.Data = "audio/wav", aiservices.ToneWAV(secs, tone(sum[:]))
		st.Usage.Seconds = secs
		if a.kind == aiservices.KindMusic {
			o.Text = "[instrumental]"
		}
	case aiservices.KindVoiceChange:
		in := req.Input(aiservices.RoleAudio)
		o.MIME, o.Data = "audio/wav", aiservices.ToneWAV(firstPositive(in.Seconds, 1), tone(sum[:]))
		st.Usage.Seconds = in.Seconds
	case aiservices.KindDubbing, aiservices.KindLipSync:
		in := req.Input(aiservices.RoleMedia)
		b, err := os.ReadFile(in.Path)
		if err != nil {
			return nil, err
		}
		o.MIME, o.Data, o.Language = in.MIME, b, req.Options.TargetLanguage
		st.Usage.Seconds = in.Seconds
	case aiservices.KindTranscription:
		in := req.Input(aiservices.RoleAudio)
		o.Transcript = transcript(firstPositive(in.Seconds, 2), req.Options.Language)
		o.Language = o.Transcript.Language
		st.Usage.Seconds = in.Seconds
	case aiservices.KindVoiceCopy:
		o.VoiceID = "fakevoice_" + h[:12]
	case aiservices.KindPresenterMake:
		// The presenter, and the voice the provider copied from the take
		// (deleted after: MetaRush's own sound is what presenters speak).
		o.PresenterID, o.VoiceID = "fakeprs_"+h[:12], "fakevoicefromtake_"+h[:8]
	case aiservices.KindPresenter:
		in := req.Input(aiservices.RoleAudio)
		o.MIME, o.Data = "video/mp4", a.f.video(h, in)
		st.Usage.Seconds = in.Seconds
	}
	st.Outputs = []aiservices.Output{o}
	return st, nil
}

// tone is a pitch made from the request, so different requests make different files.
func tone(seed []byte) float64 { return 180 + float64(seed[0])*2 + float64(seed[1])/128 }

func firstPositive(vs ...float64) float64 {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}

var fakeWords = strings.Fields("this is what the fake service heard in the recording and it says so plainly")

// transcript times words evenly over the recording, two speakers taking turns.
func transcript(seconds float64, lang string) *aiservices.Transcript {
	n := len(fakeWords)
	step := seconds / float64(n)
	t := &aiservices.Transcript{Language: firstNonEmpty(lang, "en")}
	for i, w := range fakeWords {
		sp := "speaker_0"
		if i >= n/2 {
			sp = "speaker_1"
		}
		t.Words = append(t.Words, aiservices.Word{Text: w, Start: float64(i) * step, End: float64(i)*step + step*0.8, Speaker: sp})
	}
	t.Text = strings.Join(fakeWords, " ")
	return t
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

// picture is a small PNG coloured by the request, in its shape.
func picture(seed []byte, aspect string) []byte {
	w, h := 64, 64
	switch aspect {
	case "16:9":
		w, h = 96, 54
	case "9:16":
		w, h = 54, 96
	}
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	c := color.RGBA{seed[0], seed[1], seed[2], 255}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			if (x/8+y/8)%2 == 0 {
				img.Set(x, y, c)
			} else {
				img.Set(x, y, color.RGBA{255 - c.R, 255 - c.G, 255 - c.B, 255})
			}
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

func (a *adapter) Poll(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["poll"]++
	if key != f.Key {
		return nil, keyErr()
	}
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	if t == nil || t.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "Fake AI keeps nothing to ask about for a request it answered at once."}
	}
	r, ok := f.tickets[t.ID]
	if !ok || r.Forgot {
		return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, HTTPStatus: 404, Message: "Fake AI no longer has this request."}
	}
	r.Polls++
	if r.Canceled {
		return &aiservices.Status{State: aiservices.StateCanceled}, nil
	}
	need := max(1, f.ReadyAfter)
	if r.Behavior.Kind == Delay {
		need += max(1, r.Behavior.N)
	}
	if r.Polls < need {
		return &aiservices.Status{State: aiservices.StateRunning, Progress: float64(r.Polls) / float64(need)}, nil
	}
	if r.Behavior.Kind == Fail {
		return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "Fake AI couldn't finish this."}}, nil
	}
	st := *f.results[t.ID]
	st.Outputs = nil
	for i, o := range f.results[t.ID].Outputs {
		o.URI, o.FileID = fmt.Sprintf("fake://%s/%d", t.ID, i), fmt.Sprintf("files/%s-out-%d", t.ID, i)
		o.Data = nil
		st.Outputs = append(st.Outputs, o)
	}
	return &st, nil
}

func (a *adapter) Fetch(ctx context.Context, key string, t *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	f := a.f
	f.mu.Lock()
	f.calls["fetch"]++
	if key != f.Key {
		f.mu.Unlock()
		return 0, keyErr()
	}
	var data []byte
	switch {
	case len(out.Data) > 0:
		data = out.Data
	case strings.HasPrefix(out.URI, "fake://gone/"):
	case t != nil && t.ID != "":
		if r, ok := f.tickets[t.ID]; ok && !r.Forgot && r.Behavior.Kind != Expired {
			var i int
			fmt.Sscanf(out.URI[strings.LastIndex(out.URI, "/")+1:], "%d", &i)
			if res := f.results[t.ID]; res != nil && i < len(res.Outputs) {
				data = res.Outputs[i].Data
			}
		}
	}
	f.mu.Unlock()
	if len(data) == 0 {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "Fake AI no longer has the finished file.", Hint: "Make it again."}
	}
	n, err := dst.Write(data)
	return int64(n), err
}

func (a *adapter) Cancel(ctx context.Context, key string, t *aiservices.Ticket) error {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["cancel"]++
	if key != f.Key {
		return keyErr()
	}
	if t != nil {
		if r, ok := f.tickets[t.ID]; ok {
			r.Canceled = true
		}
	}
	return nil
}

func (a *adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, outs []aiservices.Output) error {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["forget"]++
	if key != f.Key {
		return keyErr()
	}
	id := ""
	if t != nil {
		id = t.ID
		if id == "" {
			id = t.Extra["history"]
		}
	}
	for _, r := range f.reqs {
		if r.Ticket == id && id != "" {
			r.Forgot = true
		}
	}
	return nil
}

// FindVoices lists the voice copies whose name carries a tag (aiservices.VoiceFinder).
func (a *adapter) FindVoices(ctx context.Context, key, tag string, _ aiservices.Options) ([]string, error) {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["find-voices"]++
	if key != f.Key {
		return nil, keyErr()
	}
	var out []string
	for id, ok := range f.voices {
		if ok && tag != "" && strings.Contains(f.names[id], "["+tag+"]") {
			out = append(out, id)
		}
	}
	return out, nil
}

// RemoveVoice deletes a voice copy (aiservices.VoiceRemover).
func (a *adapter) RemoveVoice(ctx context.Context, key, voiceID string, _ aiservices.Options) error {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["remove-voice"]++
	if key != f.Key {
		return keyErr()
	}
	if !f.voices[voiceID] {
		return &videogen.Error{Kind: videogen.KindNotFound, Provider: Name, HTTPStatus: 404, Message: "Fake AI has no such voice."}
	}
	f.voices[voiceID] = false
	return nil
}

// checkPresenter refuses what a careful presenter provider refuses: a
// presenter made without its consent request answered, or a clip of a
// presenter it doesn't have.
func (f *Fake) checkPresenter(kind string, req *aiservices.Request, b Behavior) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch kind {
	case aiservices.KindPresenterMake:
		p, _ := aiservices.ProviderOf(Name)
		if p != nil && p.Presenters != nil && p.Presenters.ConsentMode == aiservices.ConsentAPIUpload {
			if req.Input(aiservices.RoleConsent) == nil || !f.consents[req.Options.ConsentID] {
				f.problems = append(f.problems, "a presenter was asked for without the consent request answered")
				return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, HTTPStatus: 400, Message: "Fake AI wants the person reading its consent words first."}
			}
		}
		if b.Kind == ConsentMismatch {
			return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, HTTPStatus: 400, Message: "Fake AI didn't hear its consent words in the reading.", Detail: "consent audio does not match"}
		}
		if req.Input(aiservices.RoleFootage) == nil {
			return &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, HTTPStatus: 400, Message: "Fake AI needs a training take."}
		}
	case aiservices.KindPresenter:
		id := req.Options.Presenter
		_, made := f.presenters[id]
		if !made && id != "conformance-presenter" && !strings.HasPrefix(id, "fake-stock-") {
			return &videogen.Error{Kind: videogen.KindNotFound, Provider: Name, HTTPStatus: 404, Message: "Fake AI has no such presenter."}
		}
	}
	return nil
}

// video is a small real MP4 of a presenter reading the sound sent (ffmpeg),
// or, without ffmpeg, a few bytes that read as one.
func (f *Fake) video(hash string, in *aiservices.Media) []byte {
	secs := 1.0
	if in != nil && in.Seconds > 0 {
		secs = in.Seconds
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil || f.videoDir == "" || in == nil || in.Path == "" {
		return append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)
	}
	out := filepath.Join(f.videoDir, hash[:16]+".mp4")
	if b, err := os.ReadFile(out); err == nil {
		return b
	}
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x180:rate=25:duration=%.3f", secs), "-i", in.Path,
		"-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-shortest",
		"-movflags", "+faststart", out)
	if err := cmd.Run(); err != nil {
		return append([]byte("\x00\x00\x00\x18ftypmp42\x00\x00\x00\x00mp42isom"), make([]byte, 64)...)
	}
	b, _ := os.ReadFile(out)
	return b
}

// Presenters lists the presenters the fake keeps.
func (f *Fake) Presenters() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.presenters {
		out = append(out, id)
	}
	return out
}

// ConsentRequests lists the consent requests the fake keeps.
func (f *Fake) ConsentRequests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for id := range f.consents {
		out = append(out, id)
	}
	return out
}

// AskConsent makes a consent request (aiservices.PresenterMaker).
func (a *adapter) AskConsent(ctx context.Context, key, language string) (*aiservices.ConsentChallenge, error) {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["ask-consent"]++
	if key != f.Key {
		return nil, keyErr()
	}
	f.nextID++
	id := fmt.Sprintf("fakecst-%d", f.nextID)
	f.consents[id] = true
	exp := time.Now().UTC().Add(30 * time.Minute)
	return &aiservices.ConsentChallenge{ID: id, Text: fmt.Sprintf("I, [user name], agree to this fake presenter. Code %d.", 4800+f.nextID), Language: language, ExpiresAt: &exp}, nil
}

// DropConsent deletes a consent request (aiservices.PresenterMaker).
func (a *adapter) DropConsent(ctx context.Context, key, id string) error {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["drop-consent"]++
	if key != f.Key {
		return keyErr()
	}
	if !f.consents[id] {
		return &videogen.Error{Kind: videogen.KindNotFound, Provider: Name, HTTPStatus: 404, Message: "Fake AI has no such consent request."}
	}
	delete(f.consents, id)
	return nil
}

// RemovePresenter deletes a presenter (aiservices.PresenterMaker).
func (a *adapter) RemovePresenter(ctx context.Context, key, id string) error {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["remove-presenter"]++
	if key != f.Key {
		return keyErr()
	}
	if _, ok := f.presenters[id]; !ok {
		return &videogen.Error{Kind: videogen.KindNotFound, Provider: Name, HTTPStatus: 404, Message: "Fake AI has no such presenter."}
	}
	delete(f.presenters, id)
	return nil
}

// FindPresenters lists the presenters whose name carries a tag (aiservices.PresenterMaker).
func (a *adapter) FindPresenters(ctx context.Context, key, tag string) ([]string, error) {
	f := a.f
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls["find-presenters"]++
	if key != f.Key {
		return nil, keyErr()
	}
	var out []string
	for id, name := range f.presenters {
		if tag != "" && strings.Contains(name, "["+tag+"]") {
			out = append(out, id)
		}
	}
	return out, nil
}

// StockPresenters lists the fake's stock presenters (aiservices.StockLister).
func (a *adapter) StockPresenters(ctx context.Context, key string) ([]aiservices.StockPresenter, error) {
	if key != a.f.Key {
		return nil, keyErr()
	}
	return []aiservices.StockPresenter{{ID: "fake-stock-amber", Name: "Amber", Gender: "female", GreenScreen: true},
		{ID: "fake-stock-adam", Name: "Adam", Gender: "male", GreenScreen: true}}, nil
}
