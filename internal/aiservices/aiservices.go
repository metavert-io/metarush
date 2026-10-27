// Package aiservices is MetaRush's layer for AI services (D224): one contract
// per kind of work — pictures, voices, voice copies, voice change, sound
// effects, music, dubbing and transcription in the cloud — generalized from
// generated video's lifecycle (internal/videogen): what a model can do, what a
// request costs, sending it, asking how it is going, downloading the result,
// cancelling it and deleting it at the provider afterwards.
//
// Requests are provider-neutral (Request); each provider implements a kind of
// work as an Adapter that mostly maps requests. Models, what they can do, their
// limits, dated prices, retirement dates, licence terms and the providers' data
// terms are data, not code (catalog.json, D225). Generated video keeps its own
// package and records (D123); the catalogue lists its models too, so choosing
// a model reads one place.
//
// Keys never pass through here as configuration: callers read the machine's
// key from internal/providers and hand it to each call; adapters send it only
// in a request header, only to their own API host (or a loopback test server).
// An adapter only talks to its provider: pricing agreement, money, consent,
// records and clean-up live in internal/aiwork (D226), the same for every
// adapter.
package aiservices

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Kinds of work.
const (
	// KindVideo is generated video: internal/videogen and internal/generate
	// (listed in the catalogue; requests go through generate.*).
	KindVideo = "video"
	// KindPicture makes a picture from words, or changes pictures by an instruction.
	KindPicture = "picture"
	// KindVoice says words in a voice (text to speech).
	KindVoice = "voice"
	// KindVoiceCopy makes a copy of a person's voice, with their consent.
	KindVoiceCopy = "voice-copy"
	// KindVoiceChange says a recording again in another voice (speech to speech).
	KindVoiceChange = "voice-change"
	// KindSoundEffect makes a sound effect from words.
	KindSoundEffect = "sound-effect"
	// KindMusic makes music from words.
	KindMusic = "music"
	// KindDubbing says a recording's speech again in another language.
	KindDubbing = "dubbing"
	// KindTranscription writes down what is said, in the cloud (local
	// transcription stays the default, D10).
	KindTranscription = "transcription"
	// KindPresenter has a presenter read lines on camera from sound
	// MetaRush supplies (D273).
	KindPresenter = "presenter"
	// KindPresenterMake makes a person's own presenter at the provider from a
	// training take and their consent (D273).
	KindPresenterMake = "presenter-make"
)

// Tasks: what a request asks of its kind.
const (
	TaskGenerate   = "generate"   // from words (pictures, sound effects, music)
	TaskEdit       = "edit"       // pictures: change pictures by an instruction
	TaskSpeak      = "speak"      // voices
	TaskCopy       = "copy"       // voice copies
	TaskChange     = "change"     // voice change
	TaskDub        = "dub"        // dubbing
	TaskTranscribe = "transcribe" // transcription
	TaskRead       = "read"       // presenters: read supplied sound on camera
	TaskTrain      = "train"      // making a presenter from a training take
)

// Input roles: what a file sent with a request is for.
const (
	RolePicture = "picture" // a picture to change, or one the words refer to
	RoleAudio   = "audio"   // a recording to change the voice of, or to write down
	RoleMedia   = "media"   // a recording or video to dub
	RoleSample  = "sample"  // a sample of the voice to copy
	RoleConsent = "consent" // the speaker reading the provider's consent script
	RoleFootage = "footage" // a presenter's training take (D273)
)

// Outputs: what a kind of work makes.
const (
	OutputPicture    = "picture"
	OutputAudio      = "audio"
	OutputMedia      = "media" // audio or video, as the input was (dubbing)
	OutputTranscript = "transcript"
	OutputVoice      = "voice" // a voice at the provider (voice copies)
	OutputVideo      = "video"
	OutputPresenter  = "presenter" // a presenter at the provider (D273)
)

// KindInfo says what a kind of work is, in plain words, and how MetaRush
// treats it.
type KindInfo struct {
	Kind  string `json:"kind" jsonschema:"enum=video,enum=picture,enum=voice,enum=voice-copy,enum=voice-change,enum=sound-effect,enum=music,enum=dubbing,enum=transcription,enum=presenter,enum=presenter-make,enum=background-removal,enum=upscale,enum=lip-sync"`
	Title string `json:"title" jsonschema:"description=The kind of work as people say it: Pictures\\, Music."`
	// What one result is called ("a picture", "a sound effect").
	What string `json:"what"`
	// Output is what it makes: picture, audio, media (as the input was), transcript or voice.
	Output string `json:"output" jsonschema:"enum=picture,enum=audio,enum=media,enum=transcript,enum=voice,enum=video,enum=presenter"`
	// Tasks it can be asked for; the first is the default.
	Tasks []string `json:"tasks"`
	// Reuse: the same request (same words, files and settings) gives the same
	// result, so one made before is used again for free instead of paying twice.
	Reuse bool `json:"reuse"`
	// Likeness: it can make a real person's likeness (a face or a voice), so
	// consent rules apply (D126, D227).
	Likeness bool `json:"likeness,omitempty"`
	// Edit: it changes a person's own picture without making anything new
	// (a background taken away), so the result isn't marked as AI-generated;
	// it says how it was changed (D279).
	Edit bool `json:"edit,omitempty"`
}

var kinds = []KindInfo{
	{Kind: KindVideo, Title: "Video", What: "a generated shot", Output: "video", Tasks: []string{"text_to_video"}, Likeness: true},
	{Kind: KindPicture, Title: "Pictures", What: "a picture", Output: OutputPicture, Tasks: []string{TaskGenerate, TaskEdit}},
	{Kind: KindVoice, Title: "Voices", What: "AI speech", Output: OutputAudio, Tasks: []string{TaskSpeak}, Reuse: true},
	{Kind: KindVoiceCopy, Title: "Voice copies", What: "a voice copy", Output: OutputVoice, Tasks: []string{TaskCopy}, Likeness: true},
	{Kind: KindVoiceChange, Title: "Voice change", What: "a recording in another voice", Output: OutputAudio, Tasks: []string{TaskChange}, Reuse: true},
	{Kind: KindSoundEffect, Title: "Sound effects", What: "a sound effect", Output: OutputAudio, Tasks: []string{TaskGenerate}},
	{Kind: KindMusic, Title: "Music", What: "a piece of music", Output: OutputAudio, Tasks: []string{TaskGenerate}},
	{Kind: KindDubbing, Title: "Dubbing", What: "a dubbed copy", Output: OutputMedia, Tasks: []string{TaskDub}, Reuse: true, Likeness: true},
	{Kind: KindTranscription, Title: "Transcription", What: "a transcript", Output: OutputTranscript, Tasks: []string{TaskTranscribe}, Reuse: true},
	{Kind: KindPresenter, Title: "Presenters", What: "a presenter clip", Output: OutputVideo, Tasks: []string{TaskRead}, Reuse: true, Likeness: true},
	{Kind: KindPresenterMake, Title: "Making presenters", What: "a presenter", Output: OutputPresenter, Tasks: []string{TaskTrain}, Likeness: true},
}

// Kinds lists every kind of work, video first.
func Kinds() []KindInfo { return slices.Clone(kinds) }

// KindOf returns a kind's description.
func KindOf(kind string) (KindInfo, error) {
	for _, k := range kinds {
		if k.Kind == kind {
			return k, nil
		}
	}
	var names []string
	for _, k := range kinds {
		names = append(names, k.Kind)
	}
	return KindInfo{}, apperr.New(apperr.InvalidInput, "MetaRush has no kind of AI work called “%s”.", kind).WithDetails(map[string]any{"kinds": names})
}

// Media is a file sent with a request: the exact bytes in a local file, with
// their hash.
type Media struct {
	Role string `json:"role" jsonschema:"enum=picture,enum=audio,enum=media,enum=sample,enum=consent,enum=footage"`
	MIME string `json:"mime"`
	Path string `json:"-"`
	// URL is a short-lived link to the same bytes, for providers that
	// download some inputs themselves (D-ID's training take): never stored,
	// logged or hashed.
	URL     string  `json:"-"`
	SHA256  string  `json:"sha256"`
	Size    int64   `json:"size"`
	Seconds float64 `json:"seconds,omitempty"`
	// Name is the file name it is sent under (never a path on this computer).
	Name string `json:"-"`
}

// Options are a request's settings; each kind reads its own (stored on the
// generation record as they were asked for).
type Options = models.GenOptions

// Request is one piece of AI work, provider-neutral.
type Request struct {
	Kind  string `json:"kind"`
	Task  string `json:"task"`
	Model string `json:"model"`
	// Prompt is what to make (pictures, sound effects, music), the words to
	// say (voices) or what to change (a picture's instruction).
	Prompt  string  `json:"prompt,omitempty"`
	Inputs  []Media `json:"inputs,omitempty"`
	Options Options `json:"options"`
	// Tag marks what the request leaves at the provider (a voice copy's name)
	// so MetaRush can find it again when the answer is lost (VoiceFinder).
	// It names the record, not what is asked, so it isn't in the hash.
	Tag string `json:"tag,omitempty"`
}

// Hash is the SHA-256 of the canonical request: what is asked, the files by
// their own hashes (never their bytes, never a key).
func (r *Request) Hash(provider string) string {
	type m struct {
		Role   string `json:"role"`
		SHA256 string `json:"sha256"`
		MIME   string `json:"mime"`
	}
	canon := struct {
		Provider string  `json:"provider"`
		Kind     string  `json:"kind"`
		Task     string  `json:"task"`
		Model    string  `json:"model"`
		Prompt   string  `json:"prompt"`
		Inputs   []m     `json:"inputs"`
		Options  Options `json:"options"`
	}{Provider: provider, Kind: r.Kind, Task: r.Task, Model: r.Model, Prompt: r.Prompt, Inputs: []m{}, Options: r.Options}
	for _, x := range r.Inputs {
		canon.Inputs = append(canon.Inputs, m{x.Role, x.SHA256, x.MIME})
	}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Input is the first input with a role (nil when there is none).
func (r *Request) Input(role string) *Media {
	for i := range r.Inputs {
		if r.Inputs[i].Role == role {
			return &r.Inputs[i]
		}
	}
	return nil
}

// InputsOf are the inputs with a role.
func (r *Request) InputsOf(role string) []Media {
	var out []Media
	for _, x := range r.Inputs {
		if x.Role == role {
			out = append(out, x)
		}
	}
	return out
}

// HeardSeconds is how long the recordings sent to be changed, dubbed or
// written down are, in all.
func (r *Request) HeardSeconds() float64 {
	total := 0.0
	for _, x := range r.Inputs {
		if x.Role == RoleAudio || x.Role == RoleMedia {
			total += x.Seconds
		}
	}
	return total
}

// Ticket is the provider's handle on a sent request.
type Ticket struct {
	// ID is the provider's ID for the request (a dubbing project, an
	// interaction); empty for one-shot calls that answered with the result.
	ID string `json:"id,omitempty"`
	// Files are inputs uploaded for it (deleted by Forget).
	Files []string `json:"files,omitempty"`
	// Extra are other handles the provider gave (a history item, a
	// transcript's ID), deleted by Forget where its API allows.
	Extra       map[string]string `json:"extra,omitempty"`
	SubmittedAt time.Time         `json:"submittedAt"`
	// Ready is the result of a one-shot call: the provider answered with it
	// straight away, so there is nothing to wait for. It is never stored:
	// MetaRush keeps the outputs themselves before it saves the ticket.
	Ready *Status `json:"-"`
}

// Status states.
const (
	StateRunning  = videogen.StateRunning
	StateDone     = videogen.StateDone
	StateFailed   = videogen.StateFailed
	StateCanceled = videogen.StateCanceled
)

// Word is a word heard, with when it is said (seconds) and who says it (the
// provider's label).
type Word struct {
	Text    string  `json:"text"`
	Start   float64 `json:"start"`
	End     float64 `json:"end"`
	Speaker string  `json:"speaker,omitempty"`
}

// Transcript is what transcription in the cloud wrote down.
type Transcript struct {
	Language string `json:"language,omitempty"`
	Text     string `json:"text"`
	Words    []Word `json:"words"`
	// Recognizer and RecognizerModel name the program that wrote it on this
	// computer (D278: "parakeet", "whisper.cpp"), so the clip's transcript
	// says so as a local one does.
	Recognizer      string `json:"recognizer,omitempty"`
	RecognizerModel string `json:"recognizerModel,omitempty"`
}

// Output is one result at the provider: inline bytes, an address to download
// from, a transcript or a voice.
type Output struct {
	MIME   string `json:"mime,omitempty"`
	Data   []byte `json:"-"`
	URI    string `json:"uri,omitempty"`
	FileID string `json:"fileId,omitempty"`
	// Language is a dub's or a transcript's language.
	Language string `json:"language,omitempty"`
	// Transcript is what transcription wrote down.
	Transcript *Transcript `json:"transcript,omitempty"`
	// VoiceID is the provider's ID of a voice copy (or of a voice it made
	// along with a presenter, deleted after).
	VoiceID string `json:"voiceId,omitempty"`
	// PresenterID is the provider's ID of a presenter it made (D273).
	PresenterID string `json:"presenterId,omitempty"`
	// Text is what came with it (a song's words or structure).
	Text string `json:"text,omitempty"`
}

// Delivery is how an output arrives: inline, uri, transcript or voice.
func (o *Output) Delivery() string {
	switch {
	case o.Transcript != nil:
		return "transcript"
	case o.PresenterID != "":
		return "presenter"
	case o.VoiceID != "":
		return "voice"
	case len(o.Data) > 0:
		return "inline"
	}
	return "uri"
}

// Usage is what the provider reported it counted (Known false: it said nothing).
type Usage struct {
	Known        bool    `json:"known"`
	InputTokens  int64   `json:"inputTokens,omitempty"`
	OutputTokens int64   `json:"outputTokens,omitempty"`
	Seconds      float64 `json:"seconds,omitempty"`
	Characters   int64   `json:"characters,omitempty"`
}

// Status is how a request is going.
type Status struct {
	State   string           `json:"state"`
	Outputs []Output         `json:"outputs,omitempty"`
	Usage   Usage            `json:"usage"`
	Safety  *videogen.Safety `json:"safety,omitempty"`
	// Text is what the model said (a refusal reads like this).
	Text string          `json:"text,omitempty"`
	Err  *videogen.Error `json:"error,omitempty"`
	// Progress is how far along it is (0–1) when the provider says.
	Progress float64 `json:"progress,omitempty"`
}

// Adapter is one provider's way of doing one kind of work (D224). Most of
// an adapter maps the provider-neutral request to the provider's API; what a
// model can do and what a request costs come from the catalogue.
type Adapter interface {
	// Provider is the catalogue's provider ID: gemini, elevenlabs.
	Provider() string
	// Kind is the kind of work it does.
	Kind() string
	// Capabilities says what a model can do (its catalogue entry; "" the default).
	Capabilities(model string) (*Model, error)
	// Estimate prices a request with the price table in force at the given time.
	Estimate(req *Request, at time.Time) (*Estimate, error)
	// Submit sends a request and returns the provider's ticket. One-shot
	// calls (most kinds) come back with the result (Ticket.Ready); others
	// return at once and are asked about with Poll.
	Submit(ctx context.Context, key string, req *Request) (*Ticket, error)
	// Poll asks how a request is going.
	Poll(ctx context.Context, key string, t *Ticket) (*Status, error)
	// Fetch downloads one output into dst and returns the bytes written.
	Fetch(ctx context.Context, key string, t *Ticket, out *Output, dst io.Writer) (int64, error)
	// Cancel stops a running request.
	Cancel(ctx context.Context, key string, t *Ticket) error
	// Forget deletes the request, its uploaded inputs and its outputs at the
	// provider, where its API allows (after the result is safely downloaded).
	Forget(ctx context.Context, key string, t *Ticket, outs []Output) error
}

// VoiceRemover is an adapter that can delete a voice it made (a voice copy
// withdrawn by a person, or because the consent it relied on was withdrawn).
type VoiceRemover interface {
	RemoveVoice(ctx context.Context, key, voiceID string, o Options) error
}

// VoiceFinder is an adapter that can look up the voices it made by the tag
// MetaRush put in their names (Request.Tag): a voice copy whose answer was
// lost may have been made all the same, and is found and deleted (D227).
type VoiceFinder interface {
	FindVoices(ctx context.Context, key, tag string, o Options) ([]string, error)
}

// TaggedName is a voice copy's name at the provider: the name a person gave
// and MetaRush's tag, so it can be found again.
func TaggedName(name, tag string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		name = "MetaRush voice"
	}
	if tag == "" {
		return name
	}
	return name + " [" + tag + "]"
}

var (
	regMu    sync.RWMutex
	registry = map[string]Adapter{}
)

func regKey(provider, kind string) string { return provider + "/" + kind }

// Register adds an adapter (from init).
func Register(a Adapter) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[regKey(a.Provider(), a.Kind())] = a
}

// Unregister takes an adapter out (tests).
func Unregister(provider, kind string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(registry, regKey(provider, kind))
}

// Get returns the adapter of a provider for a kind of work.
func Get(provider, kind string) (Adapter, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	a, ok := registry[regKey(strings.ToLower(strings.TrimSpace(provider)), kind)]
	if !ok {
		title := provider
		if p, err := ProviderOf(provider); err == nil {
			title = p.Title
		}
		k, _ := KindOf(kind)
		return nil, apperr.New(apperr.NotFound, "MetaRush can't do %s with %s.", strings.ToLower(firstNonEmpty(k.Title, kind)), title).
			WithDetails(map[string]any{"provider": provider, "kind": kind})
	}
	return a, nil
}

// Adapters lists the adapters for a kind ("" every kind), by provider.
func Adapters(kind string) []Adapter {
	regMu.RLock()
	defer regMu.RUnlock()
	var out []Adapter
	for _, a := range registry {
		if kind == "" || a.Kind() == kind {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Kind() != out[j].Kind() {
			return out[i].Kind() < out[j].Kind()
		}
		return out[i].Provider() < out[j].Provider()
	})
	return out
}

// base is what every adapter shares: its names, and what models can do and
// cost from the catalogue.
type base struct{ provider, kind string }

func (b base) Provider() string { return b.provider }
func (b base) Kind() string     { return b.kind }

// Capabilities reads the model's catalogue entry.
func (b base) Capabilities(model string) (*Model, error) { return ModelFor(b.provider, b.kind, model) }

// Estimate prices a request from the catalogue.
func (b base) Estimate(req *Request, at time.Time) (*Estimate, error) {
	m, err := ModelFor(b.provider, b.kind, req.Model)
	if err != nil {
		return nil, err
	}
	return EstimateFor(b.provider, m, req, at)
}

// oneShot is the polling side of an adapter whose calls answer with the
// result: there is nothing to ask about or stop afterwards.
type oneShot struct{}

// Poll returns the result a one-shot call answered with. After a restart it
// is gone (MetaRush keeps the outputs before it saves the ticket, so it never
// needs to ask).
func (oneShot) Poll(_ context.Context, _ string, t *Ticket) (*Status, error) {
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	return nil, &videogen.Error{Kind: videogen.KindExpired, Message: "The provider answered with the result straight away and keeps nothing to ask about later."}
}

// Fetch writes an output's bytes.
func (oneShot) Fetch(_ context.Context, _ string, _ *Ticket, out *Output, dst io.Writer) (int64, error) {
	if len(out.Data) == 0 {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Message: "The provider's answer had no file to keep."}
	}
	n, err := dst.Write(out.Data)
	return int64(n), err
}

// Cancel has nothing to stop: the call has ended.
func (oneShot) Cancel(context.Context, string, *Ticket) error { return nil }

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

// ErrKept is what Forget answers when the provider offers no way to delete
// what it made (its data terms say how long it keeps it): the record says
// so, in the provider's words, and nothing is retried.
var ErrKept = errors.New("the provider offers no way to delete it")

type keptError struct{ msg string }

func (e *keptError) Error() string        { return e.msg }
func (e *keptError) Is(target error) bool { return target == ErrKept }

// Kept is ErrKept in plain words ("ElevenLabs offers no way to delete music it made").
func Kept(msg string) error { return &keptError{msg: msg} }
