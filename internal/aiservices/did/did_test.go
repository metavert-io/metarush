package did_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/did"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakedid"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "ZGlk" + strings.Repeat("Qx7", 9) + ":fake" }

func setup(t *testing.T) (*fakedid.Server, string) {
	t.Helper()
	key := fakeKey()
	fake := fakedid.New(key)
	fake.Polls = 2
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvDIDBaseURL, fake.URL)
	did.ConsentWait = 5 * time.Second
	return fake, key
}

// links serves files on this computer the way a store's short-lived link
// would (a file name ending in .mp4, as D-ID's reference asks).
func links(t *testing.T) func(t *testing.T, path string) string {
	var mu sync.Mutex
	files := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		p := files[r.URL.Path]
		mu.Unlock()
		if p == "" {
			http.NotFound(w, r)
			return
		}
		http.ServeFile(w, r, p)
	}))
	t.Cleanup(srv.Close)
	return func(t *testing.T, path string) string {
		mu.Lock()
		defer mu.Unlock()
		name := "/links/" + filepath.Base(path)
		if !strings.HasSuffix(name, ".mp4") {
			name += ".mp4"
		}
		files[name] = path
		return srv.URL + name
	}
}

func adapter(t *testing.T, kind string) aiservices.Adapter {
	t.Helper()
	a, err := aiservices.Get(did.Name, kind)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// Both kinds pass the conformance suite against the fake: a presenter made
// after D-ID checked the consent reading, found by its tag and deleted; a
// clip of a presenter reading uploaded sound, downloaded, then deleted with
// its sound.
func TestDIDAdaptersConform(t *testing.T) {
	fake, key := setup(t)
	fake.Presenter("avt_conformance", "Conformance presenter")
	link := links(t)
	for _, kind := range []string{aiservices.KindPresenterMake, aiservices.KindPresenter} {
		t.Run(kind, func(t *testing.T) {
			aiconformance.Run(t, aiconformance.Harness{Adapter: adapter(t, kind), Key: key, Kept: fake.Kept, Problems: fake.Problems, Presenter: "avt_conformance", Link: link})
		})
	}
	if got := fake.Fetched(); len(got) < 2 {
		t.Errorf("D-ID fetched %v; want the consent reading and the training take", got)
	}
	if ps := fake.Presenters(); len(ps) != 1 || ps[0] != "avt_conformance" {
		t.Errorf("presenters left at D-ID: %v", ps)
	}
	if cs := fake.Consents(); len(cs) != 0 {
		t.Errorf("consent records left at D-ID: %v", cs)
	}
	// The voice D-ID copied from the take was deleted with the request.
	if n := fake.Calls("DELETE /tts/voices/{id}"); n != 1 {
		t.Errorf("deleted %d copied voices, want 1", n)
	}
}

func kindOf(err error) string {
	var ve *videogen.Error
	if errors.As(err, &ve) {
		return ve.Kind
	}
	return ""
}

var tags int

func trainRequest(t *testing.T, a aiservices.Adapter, key string, link func(*testing.T, string) string) *aiservices.Request {
	t.Helper()
	ch, err := a.(aiservices.PresenterMaker).AskConsent(context.Background(), key, "en-US")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ch.Text, "[user name]") || !strings.Contains(ch.Words("Ada Lovelace"), "I, Ada Lovelace confirm") {
		t.Errorf("consent words: %q → %q", ch.Text, ch.Words("Ada Lovelace"))
	}
	if ch.ExpiresAt == nil || time.Until(*ch.ExpiresAt) < 25*time.Minute {
		t.Errorf("expiry: %v", ch.ExpiresAt)
	}
	dir := t.TempDir()
	reading := aiconformance.WriteMedia(t, dir, "reading.mp4", "video/mp4", aiservices.RoleConsent, aiconformance.SampleMP4(), 8)
	take := aiconformance.WriteMedia(t, dir, "take.mp4", "video/mp4", aiservices.RoleFootage, aiconformance.SampleMP4(), 90)
	reading.URL, take.URL = link(t, reading.Path), link(t, take.Path)
	tags++
	return &aiservices.Request{Kind: aiservices.KindPresenterMake, Task: aiservices.TaskTrain, Model: "express", Tag: fmt.Sprintf("mr-test%d", tags),
		Inputs: []aiservices.Media{reading, take}, Options: aiservices.Options{Name: "Ada Lovelace", ConsentID: ch.ID, GreenScreen: true}}
}

// D-ID's own answers are said plainly: a reading that doesn't match its
// words, no minutes left, a presenter turned down, a lost answer (it may
// exist: found by its tag), a clip declined by moderation, an expired link.
func TestDIDTroubleIsPlain(t *testing.T) {
	fake, key := setup(t)
	link := links(t)
	ctx := context.Background()
	mk := adapter(t, aiservices.KindPresenterMake)

	fake.Next(fakedid.ConsentMismatch)
	_, err := mk.Submit(ctx, key, trainRequest(t, mk, key, link))
	if kindOf(err) != videogen.KindInvalid || !strings.Contains(err.Error(), "consent words") {
		t.Fatalf("mismatch: %v", err)
	}

	fake.Next(fakedid.NoCredits)
	_, err = mk.Submit(ctx, key, trainRequest(t, mk, key, link))
	if kindOf(err) != videogen.KindPermission || !strings.Contains(err.Error(), "no minutes left") {
		t.Fatalf("no credits: %v", err)
	}

	fake.Next(fakedid.RejectAvatar)
	tk, err := mk.Submit(ctx, key, trainRequest(t, mk, key, link))
	if err != nil {
		t.Fatal(err)
	}
	var st *aiservices.Status
	for i := 0; i < 5; i++ {
		if st, err = mk.Poll(ctx, key, tk); err != nil || st.State != aiservices.StateRunning {
			break
		}
	}
	if err != nil || st.State != aiservices.StateFailed || st.Err == nil || st.Err.Kind != videogen.KindSafety {
		t.Fatalf("rejected: %+v %v", st, err)
	}

	fake.Next(fakedid.LostAvatar)
	req := trainRequest(t, mk, key, link)
	_, err = mk.Submit(ctx, key, req)
	if kindOf(err) != videogen.KindUncertain {
		t.Fatalf("lost: %v", err)
	}
	found, err := mk.(aiservices.PresenterMaker).FindPresenters(ctx, key, req.Tag)
	if err != nil || len(found) != 1 {
		t.Fatalf("the lost presenter wasn't found by its tag: %v %v", found, err)
	}

	// No link to the take: refused before anything is sent.
	nolink := trainRequest(t, mk, key, link)
	nolink.Inputs[1].URL = ""
	if _, err := mk.Submit(ctx, key, nolink); kindOf(err) != videogen.KindInvalid {
		t.Fatalf("no link: %v", err)
	}

	// A clip declined by moderation, and one whose link expired.
	fake.Presenter("avt_ready", "Ready")
	clip := adapter(t, aiservices.KindPresenter)
	dir := t.TempDir()
	sound := aiconformance.WriteMedia(t, dir, "line.wav", "audio/wav", aiservices.RoleAudio, aiservices.ToneWAV(2, 220), 2)
	creq := &aiservices.Request{Kind: aiservices.KindPresenter, Task: aiservices.TaskRead, Model: "express", Inputs: []aiservices.Media{sound}, Options: aiservices.Options{Presenter: "avt_ready"}}
	fake.Next(fakedid.Moderation)
	tk, err = clip.Submit(ctx, key, creq)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if st, err = clip.Poll(ctx, key, tk); err != nil || st.State != aiservices.StateRunning {
			break
		}
	}
	if err != nil || st.State != aiservices.StateFailed || st.Err.Kind != videogen.KindSafety {
		t.Fatalf("moderation: %+v %v", st, err)
	}
	fake.Next(fakedid.ExpiredResult)
	tk, err = clip.Submit(ctx, key, creq)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if st, err = clip.Poll(ctx, key, tk); err != nil || st.State != aiservices.StateRunning {
			break
		}
	}
	if err != nil || st.State != aiservices.StateDone {
		t.Fatalf("done: %+v %v", st, err)
	}
	var buf bytes.Buffer
	if _, err := clip.Fetch(ctx, key, tk, &st.Outputs[0], &buf); kindOf(err) != videogen.KindExpired {
		t.Fatalf("expired: %v", err)
	}
	// A background only for a presenter made against a green screen.
	creq.Options.Background = "#00b140"
	if _, err := clip.Submit(ctx, key, creq); kindOf(err) != videogen.KindInvalid {
		t.Fatalf("background without a green screen: %v", err)
	}
	// Stock presenters aren't offered (their licence is unstated, D275).
	creq.Options.Background, creq.Options.Stock = "", true
	if _, err := clip.Submit(ctx, key, creq); kindOf(err) != videogen.KindInvalid {
		t.Fatalf("stock: %v", err)
	}
	if ps := fake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}

// A refused key is said plainly and never repeated; the key goes only in the
// Authorization header.
func TestDIDKey(t *testing.T) {
	_, key := setup(t)
	res, err := providers.DID{}.Test(context.Background(), key)
	if err != nil || !res.OK || !strings.Contains(res.Message, "172 of 180") {
		t.Fatalf("test: %+v %v", res, err)
	}
	res, err = providers.DID{}.Test(context.Background(), "wrong-key-for-did-test-0000")
	if err != nil || res.OK || strings.Contains(res.Message, "wrong-key") {
		t.Fatalf("wrong key: %+v %v", res, err)
	}
	_, err = adapter(t, aiservices.KindPresenterMake).(aiservices.PresenterMaker).AskConsent(context.Background(), "wrong-key-for-did-test-0000", "en")
	if kindOf(err) != videogen.KindKey || strings.Contains(err.Error(), "wrong-key") {
		t.Fatalf("wrong key: %v", err)
	}
}

// The base address takes only D-ID's host or this computer, and only this
// computer under go test; files are downloaded only over HTTPS or from this
// computer, and only from this computer under go test.
func TestDIDBaseURL(t *testing.T) {
	t.Setenv(providers.EnvDIDBaseURL, "https://evil.example.com")
	if _, err := providers.DIDBaseURL(); err == nil {
		t.Error("a foreign host was accepted")
	}
	t.Setenv(providers.EnvDIDBaseURL, "https://api.d-id.com")
	if _, err := providers.DIDBaseURL(); err == nil || !strings.Contains(err.Error(), "Tests never call D-ID") {
		t.Errorf("the real host under go test: %v", err)
	}
	t.Setenv(providers.EnvDIDBaseURL, "")
	if _, err := providers.DIDBaseURL(); err == nil {
		t.Error("the default host under go test")
	}
	t.Setenv(providers.EnvDIDBaseURL, "http://127.0.0.1:9")
	if b, err := providers.DIDBaseURL(); err != nil || b != "http://127.0.0.1:9" {
		t.Errorf("loopback: %s %v", b, err)
	}
	if providers.DownloadAllowed("https://d-id-results.example.com/x.mp4") {
		t.Error("a real download under go test")
	}
	if !providers.DownloadAllowed("http://127.0.0.1:1234/x.mp4") || providers.DownloadAllowed("http://user:pw@127.0.0.1/x") {
		t.Error("loopback downloads")
	}
}

// The catalogue prices a clip by the minute in 15-second steps, says the
// steps, and offers none of D-ID's stock presenters (their licence is unstated).
func TestDIDCatalogue(t *testing.T) {
	m, err := aiservices.ModelFor(did.Name, aiservices.KindPresenter, "")
	if err != nil {
		t.Fatal(err)
	}
	req := &aiservices.Request{Kind: aiservices.KindPresenter, Model: m.ID, Inputs: []aiservices.Media{{Role: aiservices.RoleAudio, Seconds: 20}}, Options: aiservices.Options{Presenter: "avt_x"}}
	if err := m.Check(req, time.Now()); err != nil {
		t.Fatal(err)
	}
	e, err := aiservices.EstimateFor(did.Name, m, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if e.Units != "30 seconds on camera" || e.USD < 0.55 || e.USD > 0.56 || !strings.Contains(strings.Join(e.Notes, " "), "steps of 15 seconds") {
		t.Errorf("estimate: %+v", e)
	}
	req.Inputs[0].Seconds = 400
	if err := m.Check(req, time.Now()); err == nil || !strings.Contains(err.Error(), "at most 5 minutes") {
		t.Errorf("too long: %v", err)
	}
	p, _ := aiservices.ProviderOf(did.Name)
	if p.Presenters == nil || p.Presenters.Stock.Offered() || p.Presenters.ConsentMode != aiservices.ConsentAPIUpload || p.Data.TrainsOnUploads != aiservices.TrainsNo {
		t.Errorf("presenter terms: %+v", p.Presenters)
	}
	if p.Presenters.ConsentLanguage("es-MX") != "spanish" || p.Presenters.ConsentLanguage("ja") != "" {
		t.Error("consent languages")
	}
	mk, _ := aiservices.ModelFor(did.Name, aiservices.KindPresenterMake, "")
	short := &aiservices.Request{Kind: aiservices.KindPresenterMake, Model: mk.ID, Inputs: []aiservices.Media{{Role: aiservices.RoleFootage, Seconds: 30}}}
	if err := mk.Check(short, time.Now()); err == nil || !strings.Contains(err.Error(), "at least 1 minute") {
		t.Errorf("a short take: %v", err)
	}
}

// An answer MetaRush can't read, or none at all, after D-ID was asked to
// make something: it may have been made (and charged), so it is never sent
// again (uncertain, not a server error to retry); the clip is found by the
// tag in its name and deleted with the sound uploaded for it, and a
// presenter is found by its tag (D277).
func TestDIDLostClipIsFoundByItsTag(t *testing.T) {
	fake, key := setup(t)
	ctx := context.Background()
	fake.Presenter("avt_ready", "Ready")
	clip := adapter(t, aiservices.KindPresenter)
	sound := aiconformance.WriteMedia(t, t.TempDir(), "line.wav", "audio/wav", aiservices.RoleAudio, aiservices.ToneWAV(2, 220), 2)
	for _, b := range []string{fakedid.Garbled, fakedid.LostScene} {
		req := &aiservices.Request{Kind: aiservices.KindPresenter, Task: aiservices.TaskRead, Model: "express", Inputs: []aiservices.Media{sound},
			Options: aiservices.Options{Presenter: "avt_ready"}, Tag: "mr-" + b}
		before := fake.Calls("POST /scenes")
		fake.Next(b)
		_, err := clip.Submit(ctx, key, req)
		var ve *videogen.Error
		if !errors.As(err, &ve) || ve.Kind != videogen.KindUncertain || len(ve.Files) != 1 || !strings.HasPrefix(ve.Files[0], "audios/") {
			t.Fatalf("%s: %v", b, err)
		}
		if fake.Calls("POST /scenes") != before+1 || fake.Scenes() != 1 || fake.Audios() != 1 {
			t.Fatalf("%s: asked %d times, scenes %d, sound %d", b, fake.Calls("POST /scenes")-before, fake.Scenes(), fake.Audios())
		}
		n, err := clip.(aiservices.TagCleaner).ForgetTagged(ctx, key, req.Tag)
		if err != nil || n != 1 {
			t.Fatalf("%s: found %d by its tag: %v", b, n, err)
		}
		if err := clip.Forget(ctx, key, &aiservices.Ticket{Files: ve.Files}, nil); err != nil {
			t.Fatal(err)
		}
		if fake.Kept() != 0 {
			t.Fatalf("%s: left at D-ID: %d", b, fake.Kept())
		}
	}
	// A presenter whose answer can't be read: uncertain too, and found by its tag.
	link := links(t)
	mk := adapter(t, aiservices.KindPresenterMake)
	req := trainRequest(t, mk, key, link)
	fake.Next(fakedid.Garbled)
	if _, err := mk.Submit(ctx, key, req); kindOf(err) != videogen.KindUncertain {
		t.Fatalf("garbled presenter: %v", err)
	}
	if found, err := mk.(aiservices.PresenterMaker).FindPresenters(ctx, key, req.Tag); err != nil || len(found) != 1 {
		t.Fatalf("the presenter wasn't found by its tag: %v %v", found, err)
	}
	if ps := fake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}
