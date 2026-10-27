// Package aiconformance is the conformance suite every AI service adapter
// passes (D224): what the catalogue says about its models, pricing before
// anything is sent, the lifecycle from sending to deleting at the provider,
// files people can open, a refused key, and a request hash that never holds a
// key or a path. Each adapter runs it against its fake (internal/testutil:
// fakeai, fakevideogen's Gemini endpoints, fakeeleven) — never a real service.
package aiconformance

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Harness is one adapter and the fake it talks to.
type Harness struct {
	Adapter aiservices.Adapter
	// Key is the only key the fake accepts.
	Key string
	// Request builds a request the model can do (nil: SampleRequest).
	Request func(t *testing.T, m *aiservices.Model, dir string) *aiservices.Request
	// Kept counts what the provider still keeps (nil: not checked).
	Kept func() int
	// Problems lists what the fake saw that a careful client never does (nil: not checked).
	Problems func() []string
	// Polls is how many checks to allow before giving up (default 20).
	Polls int
	// Presenter is a presenter the fake already has, for presenter clips
	// (default "conformance-presenter"); Stock says it is a stock presenter.
	Presenter string
	Stock     bool
	// Link serves a local file to the fake as a link, for adapters whose
	// provider downloads some inputs itself (aiservices.LinkTaker).
	Link func(t *testing.T, path string) string
	// Env is the computer an adapter whose models run here works on (D278):
	// its programs (fakes in tests) and folders.
	Env *aiservices.Env
}

// Run checks the adapter.
func Run(t *testing.T, h Harness) {
	t.Helper()
	a := h.Adapter
	kind := a.Kind()
	k, err := aiservices.KindOf(kind)
	if err != nil {
		t.Fatalf("kind %q: %v", kind, err)
	}
	p, err := aiservices.ProviderOf(a.Provider())
	if err != nil {
		t.Fatalf("provider %q isn't in the catalogue: %v", a.Provider(), err)
	}
	var models []aiservices.Model
	for _, m := range p.Models {
		if m.Kind == kind {
			models = append(models, m)
		}
	}
	if len(models) == 0 {
		t.Fatalf("the catalogue has no %s models for %s", kind, p.Name)
	}
	build := h.Request
	if build == nil {
		build = SampleRequest
	}
	now := time.Now().UTC()

	t.Run("catalogue", func(t *testing.T) {
		for _, m := range models {
			c, err := a.Capabilities(m.ID)
			if err != nil || c.ID != m.ID || c.Kind != kind {
				t.Errorf("%s: capabilities %+v, %v", m.ID, c, err)
				continue
			}
			if c.Title == "" || len(c.Tasks) == 0 || c.Licence.Summary == "" || c.Licence.Checked == "" {
				t.Errorf("%s: the catalogue entry lacks a title, tasks or licence terms", m.ID)
			}
			if m.Retired(now) {
				continue
			}
			req := build(t, &m, t.TempDir())
			if h.Presenter != "" && req.Options.Presenter != "" {
				req.Options.Presenter, req.Options.Stock = h.Presenter, h.Stock
			}
			if err := m.Check(req, now); err != nil {
				t.Errorf("%s: the sample request is refused: %v", m.ID, err)
				continue
			}
			e, err := a.Estimate(req, now)
			if err != nil {
				t.Errorf("%s: estimate: %v", m.ID, err)
				continue
			}
			if e.USD < 0 || e.PriceTable == "" || e.Rate == "" || e.Units == "" {
				t.Errorf("%s: estimate incomplete: %+v", m.ID, e)
			}
			if pr, _ := aiservices.PriceAt(p.Name, kind, m.ID, now); pr != nil && pr.USD > 0 && e.USD <= 0 {
				t.Errorf("%s: a priced model estimated at nothing: %+v", m.ID, e)
			}
		}
		if _, err := a.Capabilities(""); err != nil {
			t.Errorf("no default model: %v", err)
		}
	})

	m := models[0]
	for _, x := range models {
		if !x.Retired(now) {
			m = x
			break
		}
	}
	dir := t.TempDir()
	req := build(t, &m, dir)
	prepare(t, h, p, req)

	t.Run("hash", func(t *testing.T) {
		h1, h2 := req.Hash(p.Name), req.Hash(p.Name)
		if h1 != h2 || len(h1) != 64 {
			t.Fatalf("hash isn't stable: %s %s", h1, h2)
		}
		other := *req
		other.Prompt += " (changed)"
		other.Options.Region = "elsewhere"
		if other.Hash(p.Name) == h1 {
			t.Error("a different request has the same hash")
		}
		moved := *req
		moved.Inputs = append([]aiservices.Media{}, req.Inputs...)
		for i := range moved.Inputs {
			moved.Inputs[i].Path = filepath.Join(dir, "elsewhere", moved.Inputs[i].Name)
		}
		if moved.Hash(p.Name) != h1 {
			t.Error("the hash depends on where the files are on this computer")
		}
	})

	ctxFor := func() context.Context {
		if h.Env != nil {
			return aiservices.WithEnv(context.Background(), *h.Env)
		}
		return context.Background()
	}

	if p.Local() {
		// A model on this computer needs no key, never sends one and says
		// whether it can run here (D278).
		t.Run("on this computer", func(t *testing.T) {
			if p.Key != "" || p.KeyHost != "" {
				t.Errorf("a provider on this computer names a key (%q) or a host (%q)", p.Key, p.KeyHost)
			}
			if _, ok := a.(aiservices.Here); !ok {
				t.Error("an adapter on this computer can't say whether a model can run here (aiservices.Here)")
			}
			if h.Env == nil {
				t.Fatal("the harness gives an adapter on this computer no Env")
			}
			if _, err := a.Submit(context.Background(), "", req); err == nil {
				t.Error("it ran without being told which computer to run on")
			}
			for _, m := range models {
				if pr, err := aiservices.PriceAt(p.Name, kind, m.ID, now); err != nil || pr.USD != 0 {
					t.Errorf("%s: a model on this computer costs something: %+v %v", m.ID, pr, err)
				}
			}
		})
	} else {
		t.Run("wrong key", func(t *testing.T) {
			_, err := a.Submit(context.Background(), "wrong-key-for-conformance-0000", req)
			var ve *videogen.Error
			if !errors.As(err, &ve) || (ve.Kind != videogen.KindKey && ve.Kind != videogen.KindPermission) {
				t.Fatalf("a refused key gave %v, want a key error", err)
			}
			if strings.Contains(ve.Message, "wrong-key") || strings.Contains(ve.Detail, "wrong-key") {
				t.Error("the error repeats the key")
			}
		})
	}

	t.Run("lifecycle", func(t *testing.T) {
		before := 0
		if h.Kept != nil {
			before = h.Kept()
		}
		ctx := ctxFor()
		if kind == aiservices.KindVoiceCopy || kind == aiservices.KindPresenterMake {
			req.Tag = "mr-conform" + strings.ToLower(strings.ReplaceAll(p.Name, "-", ""))
		}
		tk, err := a.Submit(ctx, h.Key, req)
		if err != nil {
			t.Fatalf("submit: %v", err)
		}
		if tk.SubmittedAt.IsZero() {
			t.Error("the ticket has no time")
		}
		st := tk.Ready
		if st == nil {
			if tk.ID == "" {
				t.Fatal("a ticket with neither a result nor an ID")
			}
			polls := h.Polls
			if polls == 0 {
				polls = 20
			}
			for i := 0; i < polls; i++ {
				st, err = a.Poll(ctx, h.Key, tk)
				if err != nil {
					t.Fatalf("poll: %v", err)
				}
				if st.State != aiservices.StateRunning {
					break
				}
			}
		}
		if st == nil || st.State != aiservices.StateDone {
			t.Fatalf("not done: %+v", st)
		}
		if len(st.Outputs) == 0 {
			t.Fatal("done without outputs")
		}
		for i := range st.Outputs {
			o := st.Outputs[i]
			checkOutput(t, a, h.Key, tk, &o, k)
		}
		if kind == aiservices.KindVoiceCopy {
			checkVoiceLookup(t, a, h.Key, req, st.Outputs[0].VoiceID)
		}
		if kind == aiservices.KindPresenterMake {
			defer checkPresenterLookup(t, a, h.Key, req, st.Outputs[0].PresenterID)
		}
		if err := a.Forget(ctx, h.Key, tk, st.Outputs); err != nil && !errors.Is(err, aiservices.ErrKept) {
			t.Errorf("forget: %v", err)
		}
		if h.Kept != nil {
			if after := h.Kept(); after > before {
				t.Errorf("the provider still keeps %d more things after forgetting", after-before)
			}
		}
		if err := a.Cancel(ctx, h.Key, tk); err != nil && tk.ID == "" {
			t.Errorf("cancelling a finished one-shot request: %v", err)
		}
		// (A finished presenter must outlast a late cancel: checkPresenterLookup finds it.)
	})

	if h.Problems != nil {
		if ps := h.Problems(); len(ps) > 0 {
			t.Errorf("the fake saw: %v", ps)
		}
	}
}

// prepare readies a sample request for the adapter: the presenter a clip
// reads with, the provider's consent request a presenter answers (asked for
// here, as MetaRush does before the person records), and links to the
// inputs the provider downloads itself.
func prepare(t *testing.T, h Harness, p *aiservices.ProviderInfo, req *aiservices.Request) {
	t.Helper()
	a := h.Adapter
	switch a.Kind() {
	case aiservices.KindPresenter:
		req.Options.Presenter = firstNonEmpty(h.Presenter, req.Options.Presenter)
		req.Options.Stock = h.Stock
		if sl, ok := a.(aiservices.StockLister); ok {
			list, err := sl.StockPresenters(context.Background(), h.Key)
			if err != nil {
				t.Fatalf("listing stock presenters: %v", err)
			}
			for _, sp := range list {
				if sp.ID == "" || sp.Name == "" {
					t.Errorf("a stock presenter without an ID or a name: %+v", sp)
				}
			}
			if _, err := sl.StockPresenters(context.Background(), "wrong-key-for-conformance-0000"); err == nil {
				t.Error("a refused key listed stock presenters")
			}
		}
	case aiservices.KindPresenterMake:
		pm, ok := a.(aiservices.PresenterMaker)
		if !ok {
			t.Fatal("a presenter adapter can't ask for consent, delete or find presenters (aiservices.PresenterMaker)")
		}
		if p.Presenters == nil {
			t.Fatal("the catalogue says nothing about the provider's presenters (presenters)")
		}
		if p.Presenters.ConsentMode == aiservices.ConsentAPIUpload {
			lang := p.Presenters.ConsentLanguage("en")
			if lang == "" {
				t.Fatal("the provider checks consent itself but the catalogue names no language for it")
			}
			ch, err := pm.AskConsent(context.Background(), h.Key, "en")
			if err != nil || ch.ID == "" || strings.TrimSpace(ch.Text) == "" {
				t.Fatalf("asking for the provider's consent words: %+v %v", ch, err)
			}
			if strings.Contains(ch.Text, "[") {
				if w := ch.Words("Ada Lovelace"); strings.Contains(w, "[") || !strings.Contains(w, "Ada Lovelace") {
					t.Errorf("the consent words keep a place for a name: %q", w)
				}
			}
			req.Options.ConsentID = ch.ID
		} else {
			req.Inputs = slicesDelete(req.Inputs, aiservices.RoleConsent)
		}
		if _, err := pm.AskConsent(context.Background(), "wrong-key-for-conformance-0000", "en"); err == nil && p.Presenters.ConsentMode == aiservices.ConsentAPIUpload {
			t.Error("a refused key was asked for consent words")
		}
	}
	if lt, ok := a.(aiservices.LinkTaker); ok {
		if h.Link == nil {
			t.Fatal("the adapter's provider downloads inputs from links: the harness needs Link")
		}
		for i := range req.Inputs {
			for _, role := range lt.LinkRoles() {
				if req.Inputs[i].Role == role {
					req.Inputs[i].URL = h.Link(t, req.Inputs[i].Path)
				}
			}
		}
	}
}

func slicesDelete(in []aiservices.Media, role string) []aiservices.Media {
	var out []aiservices.Media
	for _, m := range in {
		if m.Role != role {
			out = append(out, m)
		}
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// checkPresenterLookup: a presenter can be found by the tag in its name (an
// answer lost on the way back must not leave a person's double at the
// provider), is gone once removed, and the provider's consent record can be
// deleted too (D277).
func checkPresenterLookup(t *testing.T, a aiservices.Adapter, key string, req *aiservices.Request, presenter string) {
	t.Helper()
	pm := a.(aiservices.PresenterMaker)
	ctx := context.Background()
	if req.Tag == "" {
		t.Fatal("no tag")
	}
	found, err := pm.FindPresenters(ctx, key, req.Tag)
	if err != nil || len(found) != 1 || found[0] != presenter {
		t.Fatalf("looking the presenter up by its tag: %v %v (want %s)", found, err, presenter)
	}
	if other, err := pm.FindPresenters(ctx, key, req.Tag+"x"); err != nil || len(other) != 0 {
		t.Errorf("another tag found %v %v", other, err)
	}
	if err := pm.RemovePresenter(ctx, key, presenter); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if found, err := pm.FindPresenters(ctx, key, req.Tag); err != nil || len(found) != 0 {
		t.Errorf("found after it was removed: %v %v", found, err)
	}
	if err := pm.RemovePresenter(ctx, key, presenter); err == nil {
		t.Error("removing it twice: the provider should say it has no such presenter")
	} else if ve := (*videogen.Error)(nil); !errors.As(err, &ve) || ve.Kind != videogen.KindNotFound {
		t.Errorf("removing it twice: %v, want not found", err)
	}
	if id := req.Options.ConsentID; id != "" {
		if err := pm.DropConsent(ctx, key, id); err != nil {
			t.Errorf("deleting the provider's consent record: %v", err)
		}
	}
}

// checkVoiceLookup: a voice copy can be found by the tag in its name (an
// answer lost on the way back must not leave it at the provider for good),
// and is gone once removed (D227).
func checkVoiceLookup(t *testing.T, a aiservices.Adapter, key string, req *aiservices.Request, voice string) {
	t.Helper()
	f, ok := a.(aiservices.VoiceFinder)
	if !ok {
		t.Error("a voice copy adapter can't look its voices up by MetaRush's tag (aiservices.VoiceFinder)")
		return
	}
	rm, ok := a.(aiservices.VoiceRemover)
	if !ok {
		t.Error("a voice copy adapter can't delete its voices (aiservices.VoiceRemover)")
		return
	}
	ctx := context.Background()
	found, err := f.FindVoices(ctx, key, req.Tag, req.Options)
	if err != nil || len(found) != 1 || found[0] != voice {
		t.Fatalf("looking the voice up by its tag: %v %v (want %s)", found, err, voice)
	}
	if other, err := f.FindVoices(ctx, key, req.Tag+"x", req.Options); err != nil || len(other) != 0 {
		t.Errorf("another tag found %v %v", other, err)
	}
	if err := rm.RemoveVoice(ctx, key, voice, req.Options); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if found, err := f.FindVoices(ctx, key, req.Tag, req.Options); err != nil || len(found) != 0 {
		t.Errorf("found after it was removed: %v %v", found, err)
	}
}

func checkOutput(t *testing.T, a aiservices.Adapter, key string, tk *aiservices.Ticket, o *aiservices.Output, k aiservices.KindInfo) {
	t.Helper()
	switch k.Output {
	case aiservices.OutputTranscript:
		tr := o.Transcript
		if tr == nil || len(tr.Words) == 0 || strings.TrimSpace(tr.Text) == "" {
			t.Fatalf("no transcript: %+v", o)
		}
		last := -1.0
		for _, w := range tr.Words {
			if w.End < w.Start || w.Start < last-0.001 {
				t.Errorf("word times out of order: %+v", w)
			}
			last = w.Start
		}
		return
	case aiservices.OutputVoice:
		if o.VoiceID == "" {
			t.Fatalf("no voice: %+v", o)
		}
		return
	case aiservices.OutputPresenter:
		if o.PresenterID == "" {
			t.Fatalf("no presenter: %+v", o)
		}
		return
	}
	var b bytes.Buffer
	n, err := a.Fetch(context.Background(), key, tk, o, &b)
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if n != int64(b.Len()) || n == 0 {
		t.Fatalf("fetch said %d bytes and wrote %d", n, b.Len())
	}
	got := aiservices.Sniff(b.Bytes())
	ok := false
	switch k.Output {
	case aiservices.OutputPicture:
		ok = strings.HasPrefix(got, "image/")
	case aiservices.OutputAudio:
		ok = strings.HasPrefix(got, "audio/") || got == "video/mp4"
	case aiservices.OutputMedia:
		ok = strings.HasPrefix(got, "audio/") || strings.HasPrefix(got, "video/")
	case aiservices.OutputVideo:
		ok = strings.HasPrefix(got, "video/")
	}
	if !ok {
		t.Errorf("a %s output is %q, not a file people can open", k.Kind, got)
	}
	if o.MIME == "" {
		t.Error("an output without a type")
	}
}

// SampleRequest is a request every model of a kind can do, with sample files
// written into dir: a picture, a few seconds of sound.
func SampleRequest(t *testing.T, m *aiservices.Model, dir string) *aiservices.Request {
	t.Helper()
	k, _ := aiservices.KindOf(m.Kind)
	req := &aiservices.Request{Kind: m.Kind, Task: k.Tasks[0], Model: m.ID}
	sound := func(role string, seconds float64) aiservices.Media {
		return WriteMedia(t, dir, role+".wav", "audio/wav", role, aiservices.ToneWAV(seconds, 200), seconds)
	}
	switch m.Kind {
	case aiservices.KindPicture:
		req.Prompt = "A small red kite over a green hill, in watercolour."
		if len(m.Limits.Sizes) > 0 {
			req.Options.Size = m.Limits.Sizes[0]
		}
		if len(m.Limits.AspectRatios) > 0 {
			req.Options.AspectRatio = m.Limits.AspectRatios[0]
		}
	case aiservices.KindVoice:
		req.Prompt = "Welcome to the garden. Today we plant tomatoes."
		if len(m.Limits.Voices) > 0 {
			req.Options.Voice = m.Limits.Voices[0]
		} else {
			req.Options.Voice = "conformance-voice"
		}
	case aiservices.KindSoundEffect:
		req.Prompt = "A wooden door creaks open slowly."
		req.Options.Seconds = max(2, m.Limits.MinSeconds)
	case aiservices.KindMusic:
		req.Prompt = "A calm acoustic guitar piece for a cooking video."
		req.Options.Seconds, req.Options.Instrumental = max(10, m.Limits.MinSeconds), true
	case aiservices.KindVoiceChange:
		req.Inputs = []aiservices.Media{sound(aiservices.RoleAudio, 2)}
		req.Options.Voice = "conformance-voice"
	case aiservices.KindDubbing:
		req.Inputs = []aiservices.Media{sound(aiservices.RoleMedia, 3)}
		req.Options.Language, req.Options.TargetLanguage = "en", "es"
	case aiservices.KindTranscription:
		req.Inputs = []aiservices.Media{sound(aiservices.RoleAudio, 3)}
		req.Options.WordTimes = true
	case aiservices.KindLipSync:
		req.Inputs = []aiservices.Media{WriteMedia(t, dir, "footage.mp4", "video/mp4", aiservices.RoleMedia, SampleMP4(), 3), sound(aiservices.RoleAudio, 2)}
	case aiservices.KindBackground, aiservices.KindUpscale:
		req.Inputs = []aiservices.Media{WriteMedia(t, dir, "picture.png", "image/png", aiservices.RolePicture, SamplePicture(48, 32), 0)}
		if len(m.Limits.Scales) > 0 {
			req.Options.Scale = m.Limits.Scales[0]
		}
	case aiservices.KindVoiceCopy:
		secs := max(12, m.Limits.SampleMinSeconds)
		req.Inputs = []aiservices.Media{sound(aiservices.RoleSample, secs), sound(aiservices.RoleConsent, 6)}
		req.Options.Name, req.Options.ConsentLocale = "Conformance voice", "en-US"
	case aiservices.KindPresenter:
		req.Inputs = []aiservices.Media{sound(aiservices.RoleAudio, 3)}
		req.Options.Presenter = "conformance-presenter"
	case aiservices.KindPresenterMake:
		// A take's bytes need only be what the fake can check: an MP4 header
		// with the seconds said here.
		secs := max(65, m.Limits.SampleMinSeconds)
		take := WriteMedia(t, dir, "footage.mp4", "video/mp4", aiservices.RoleFootage, SampleMP4(), secs)
		consent := WriteMedia(t, dir, "consent.mp4", "video/mp4", aiservices.RoleConsent, SampleMP4(), 8)
		req.Inputs = []aiservices.Media{consent, take}
		req.Options.Name, req.Options.ConsentLocale = "Conformance presenter", "en-US"
	}
	return req
}

// WriteMedia writes a sample file and describes it as an input.
func WriteMedia(t *testing.T, dir, name, mime, role string, b []byte, seconds float64) aiservices.Media {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return aiservices.Media{Role: role, MIME: mime, Path: p, Name: name, SHA256: aiservices.SHA256(b), Size: int64(len(b)), Seconds: seconds}
}

// SamplePicture is a small PNG: a red disc on a pale background, so a
// subject can be told from its background.
func SamplePicture(w, h int) []byte {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	cx, cy, r := float64(w)/2, float64(h)/2, float64(min(w, h))/3
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := color.NRGBA{235, 235, 225, 255}
			if dx, dy := float64(x)-cx, float64(y)-cy; dx*dx+dy*dy < r*r {
				c = color.NRGBA{200, 30, 40, 255}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	var b bytes.Buffer
	_ = png.Encode(&b, img)
	return b.Bytes()
}

// SampleMP4 is the start of an MP4 file: enough for a fake to hand back and
// for Sniff to know it (fakes don't play it).
func SampleMP4() []byte {
	b := []byte("\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2")
	return append(b, make([]byte, 64)...)
}
