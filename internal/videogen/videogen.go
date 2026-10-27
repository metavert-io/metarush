// Package videogen is the provider layer for generative video (D123): a small
// interface each provider implements — what a model can do, what a request
// costs, sending it, asking how it is going, downloading the result,
// cancelling it and deleting it at the provider afterwards — plus the model
// catalog and dated price tables, which are data (catalog.json), not code.
//
// Keys never pass through here as configuration: callers read the machine's
// key from internal/providers and hand it to each call; providers send it only
// in a request header, only to their own API host (or a loopback test server).
package videogen

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
)

// Tasks a generation can be.
const (
	TaskTextToVideo      = "text_to_video"      // from words only
	TaskImageToVideo     = "image_to_video"     // starts from a picture (a frame of footage)
	TaskReferenceToVideo = "reference_to_video" // pictures or short clips of who or what should appear
	TaskInterpolate      = "interpolate"        // from a first frame to a last frame
	TaskEdit             = "edit"               // changes a clip by an instruction
	TaskExtend           = "extend"             // continues a clip
)

// Provider is one generative-video service.
type Provider interface {
	Name() string  // stable ID: "gemini"
	Title() string // display name: "Google Gemini"
	// Capabilities says what a model can do (from the catalog).
	Capabilities(model string) (*Capabilities, error)
	// Estimate prices a request with the price table in force at the given time.
	Estimate(req *Request, at time.Time) (*Estimate, error)
	// Submit sends a request and returns the provider's ticket for it. It
	// never waits for the video.
	Submit(ctx context.Context, key string, req *Request) (*Ticket, error)
	// Poll asks how a request is going.
	Poll(ctx context.Context, key string, t *Ticket) (*Status, error)
	// Fetch downloads one output into dst and returns the bytes written.
	Fetch(ctx context.Context, key string, t *Ticket, out *Output, dst io.Writer) (int64, error)
	// Cancel stops a running request.
	Cancel(ctx context.Context, key string, t *Ticket) error
	// Forget deletes the request, its uploaded inputs and its outputs at the
	// provider (after the result is safely downloaded).
	Forget(ctx context.Context, key string, t *Ticket, outs []Output) error
}

// Media is a picture or a clip sent with a request: the exact bytes in a
// local file, with their hash.
type Media struct {
	Role    string  `json:"role"`
	MIME    string  `json:"mime"`
	Path    string  `json:"-"`
	SHA256  string  `json:"sha256"`
	Size    int64   `json:"size"`
	Seconds float64 `json:"seconds,omitempty"`
}

// Request is one generation, provider-neutral.
type Request struct {
	Model       string  `json:"model"`
	Task        string  `json:"task"`
	Prompt      string  `json:"prompt"`
	Seconds     int     `json:"seconds"`
	AspectRatio string  `json:"aspectRatio"`
	Resolution  string  `json:"resolution"`
	Images      []Media `json:"images,omitempty"` // in order: <IMAGE_REF_0>, <IMAGE_REF_1>…; a start frame first; interpolation: first, last
	Videos      []Media `json:"videos,omitempty"` // reference clips, or the one clip an edit or extension works on
}

// Hash is the SHA-256 of the canonical request: what is asked, the pictures
// and clips by their own hashes (never their bytes, never a key).
func (r *Request) Hash(provider string) string {
	type m struct {
		Role   string `json:"role"`
		SHA256 string `json:"sha256"`
		MIME   string `json:"mime"`
	}
	canon := struct {
		Provider    string `json:"provider"`
		Model       string `json:"model"`
		Task        string `json:"task"`
		Prompt      string `json:"prompt"`
		Seconds     int    `json:"seconds"`
		AspectRatio string `json:"aspectRatio"`
		Resolution  string `json:"resolution"`
		Images      []m    `json:"images"`
		Videos      []m    `json:"videos"`
	}{Provider: provider, Model: r.Model, Task: r.Task, Prompt: r.Prompt, Seconds: r.Seconds, AspectRatio: r.AspectRatio, Resolution: r.Resolution,
		Images: []m{}, Videos: []m{}}
	for _, x := range r.Images {
		canon.Images = append(canon.Images, m{x.Role, x.SHA256, x.MIME})
	}
	for _, x := range r.Videos {
		canon.Videos = append(canon.Videos, m{x.Role, x.SHA256, x.MIME})
	}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// Ticket is the provider's handle on a sent request.
type Ticket struct {
	ID          string    `json:"id"`
	Files       []string  `json:"files,omitempty"` // inputs uploaded for it (deleted by Forget)
	SubmittedAt time.Time `json:"submittedAt"`
}

// Status states.
const (
	StateRunning  = "running"
	StateDone     = "done"
	StateFailed   = "failed"
	StateCanceled = "canceled"
)

// Output is a finished video at the provider: inline bytes, or an address to
// download from.
type Output struct {
	MIME   string `json:"mime"`
	Data   []byte `json:"-"`
	URI    string `json:"uri,omitempty"`
	FileID string `json:"fileId,omitempty"` // the provider's file (deleted by Forget)
}

// Delivery is how an output arrives: inline or uri.
func (o *Output) Delivery() string {
	if len(o.Data) > 0 {
		return "inline"
	}
	return "uri"
}

// Usage is what the provider reported it counted (Known false: it said nothing).
type Usage struct {
	Known         bool  `json:"known"`
	InputTokens   int64 `json:"inputTokens"`
	OutputTokens  int64 `json:"outputTokens"`
	VideoTokens   int64 `json:"videoTokens"`
	TextTokens    int64 `json:"textTokens"`
	ThoughtTokens int64 `json:"thoughtTokens"`
}

// Safety is what the provider's safety filters did.
type Safety struct {
	Blocked       bool     `json:"blocked"`
	Side          string   `json:"side,omitempty"` // input | output
	FilteredCount int      `json:"filteredCount,omitempty"`
	SupportCodes  []string `json:"supportCodes,omitempty"`
	Category      string   `json:"category,omitempty"`
	Message       string   `json:"message,omitempty"`
}

// Status is how a request is going.
type Status struct {
	State   string   `json:"state"`
	Outputs []Output `json:"outputs,omitempty"`
	Usage   Usage    `json:"usage"`
	Safety  *Safety  `json:"safety,omitempty"`
	Text    string   `json:"text,omitempty"` // what the model said (a refusal reads like this)
	Err     *Error   `json:"error,omitempty"`
	// Partial: the provider stopped early (it may still have sent a video).
	Partial bool `json:"partial,omitempty"`
}

// Error kinds.
const (
	KindKey        = "key"        // the key was not accepted
	KindPermission = "permission" // the key may not do this (billing, region, model access)
	KindSafety     = "safety"     // the safety filters declined it
	KindRate       = "rate"       // too many requests: wait a little
	KindSpend      = "spend"      // the provider's spending limit for its time window
	KindTier       = "tier"       // the key's project is on a plan that makes no video at all (Gemini: the free tier); waiting can't help (D178)
	KindRetired    = "retired"    // the model is gone
	KindInvalid    = "invalid"    // the request isn't one the provider accepts
	KindServer     = "server"     // a problem on the provider's side
	KindNetwork    = "network"    // the request didn't get through
	KindUncertain  = "uncertain"  // sent, but no answer: it may or may not have been accepted
	KindExpired    = "expired"    // the provider no longer has it
	KindNotFound   = "not-found"
	// KindUnusable: the provider answered (and may have charged) but the
	// answer can't be used — no candidates, no words, no sound. Never sent
	// again: trying again pays again (D226).
	KindUnusable = "unusable"
)

// Error is a provider error in plain words; Detail keeps the provider's own.
type Error struct {
	Kind       string        `json:"kind"`
	HTTPStatus int           `json:"httpStatus,omitempty"`
	Message    string        `json:"message"`
	Hint       string        `json:"hint,omitempty"`
	Detail     string        `json:"detail,omitempty"`
	RetryAfter time.Duration `json:"retryAfter,omitempty"`
	Safety     *Safety       `json:"safety,omitempty"`
	Provider   string        `json:"provider,omitempty"`
	// Files are what the request uploaded that the provider may still keep
	// when its answer was lost (KindUncertain): deleted then, as a ticket's are.
	Files []string `json:"files,omitempty"`
}

func (e *Error) Error() string { return e.Message }

// Retryable reports whether trying again later can help on its own.
func (e *Error) Retryable() bool {
	switch e.Kind {
	case KindRate, KindSpend, KindServer, KindNetwork:
		return true
	}
	return false
}

// AppError maps a provider error to MetaRush's stable codes.
func (e *Error) AppError() *apperr.Error {
	code := apperr.Provider
	switch e.Kind {
	case KindKey:
		code = apperr.NotConfigured
	case KindSafety:
		code = apperr.SafetyBlocked
	case KindInvalid:
		code = apperr.InvalidInput
	}
	ae := apperr.New(code, "%s", e.Message)
	if e.Hint != "" {
		ae = ae.WithHint(e.Hint)
	}
	d := map[string]any{"provider": e.Provider, "reason": e.Kind}
	if e.HTTPStatus != 0 {
		d["httpStatus"] = e.HTTPStatus
	}
	if e.Detail != "" {
		d["providerMessage"] = e.Detail
	}
	if e.RetryAfter > 0 {
		d["retryAfterSeconds"] = int(e.RetryAfter.Seconds())
	}
	if s := e.Safety; s != nil {
		d["safety"] = s
	}
	return ae.WithDetails(d)
}

var (
	regMu    sync.RWMutex
	registry = map[string]Provider{}
)

// Register adds a provider (from init).
func Register(p Provider) {
	regMu.Lock()
	defer regMu.Unlock()
	registry[p.Name()] = p
}

// Get returns a provider by name.
func Get(name string) (Provider, error) {
	regMu.RLock()
	defer regMu.RUnlock()
	p, ok := registry[strings.ToLower(strings.TrimSpace(name))]
	if !ok {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't make video with “%s”.", name).WithDetails(map[string]any{"available": names()})
	}
	return p, nil
}

func names() []string {
	out := make([]string, 0, len(registry))
	for n := range registry {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// All lists providers by name.
func All() []Provider {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Provider, 0, len(registry))
	for _, n := range names() {
		out = append(out, registry[n])
	}
	return out
}
