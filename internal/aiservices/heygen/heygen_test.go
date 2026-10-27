package heygen_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/heygen"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
	"github.com/metavert-io/metarush/internal/videogen"
)

// fakeKey is a key-shaped value made at run time (no literal for the secret scanner).
func fakeKey() string { return "hg" + strings.Repeat("Vb5", 11) + "fake" }

func setup(t *testing.T) (*fakeheygen.Server, string) {
	t.Helper()
	key := fakeKey()
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	return fake, key
}

func adapter(t *testing.T) aiservices.Adapter {
	t.Helper()
	a, err := aiservices.Get(heygen.Name, aiservices.KindPresenter)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func kindOf(err error) string {
	var ve *videogen.Error
	if errors.As(err, &ve) {
		return ve.Kind
	}
	return ""
}

// HeyGen's stock presenters pass the conformance suite against the fake:
// listed, a clip of one reading uploaded sound made, downloaded, then the
// video and the sound deleted.
func TestHeyGenConforms(t *testing.T) {
	fake, key := setup(t)
	aiconformance.Run(t, aiconformance.Harness{Adapter: adapter(t), Key: key, Kept: fake.Kept, Problems: fake.Problems, Presenter: "lk_amber_studio", Stock: true})
}

var tags int

// request is a clip of a stock presenter with its own tag (the tag is the
// idempotency key: the same tag asks for the same video).
func request(t *testing.T) *aiservices.Request {
	tags++
	sound := aiconformance.WriteMedia(t, t.TempDir(), "line.wav", "audio/wav", aiservices.RoleAudio, aiservices.ToneWAV(2, 220), 2)
	return &aiservices.Request{Kind: aiservices.KindPresenter, Task: aiservices.TaskRead, Model: "avatar-iv", Tag: fmt.Sprintf("mr-heygen%d", tags),
		Inputs: []aiservices.Media{sound}, Options: aiservices.Options{Presenter: "lk_adam_studio", Stock: true, Background: "#00b140", AspectRatio: "9:16"}}
}

func run(t *testing.T, a aiservices.Adapter, key string, tk *aiservices.Ticket) *aiservices.Status {
	t.Helper()
	for i := 0; i < 6; i++ {
		st, err := a.Poll(context.Background(), key, tk)
		if err != nil {
			t.Fatal(err)
		}
		if st.State != aiservices.StateRunning {
			return st
		}
	}
	t.Fatal("never finished")
	return nil
}

// HeyGen's own answers are said plainly: no balance, a presenter withdrawn,
// moderation, an expired link; a lost answer is asked again with the same
// idempotency key and gets the same video, never a second one; a person's
// own presenter there isn't built.
func TestHeyGenTroubleIsPlain(t *testing.T) {
	fake, key := setup(t)
	a := adapter(t)
	ctx := context.Background()

	fake.Next(fakeheygen.NoCredit)
	if _, err := a.Submit(ctx, key, request(t)); kindOf(err) != videogen.KindPermission || !strings.Contains(err.Error(), "balance") {
		t.Fatalf("no credit: %v", err)
	}
	gone := request(t)
	gone.Options.Presenter = "lk_gone_studio"
	if _, err := a.Submit(ctx, key, gone); kindOf(err) != videogen.KindRetired {
		t.Fatalf("withdrawn presenter: %v", err)
	}
	own := request(t)
	own.Options.Stock = false
	if _, err := a.Submit(ctx, key, own); kindOf(err) != videogen.KindInvalid {
		t.Fatalf("a person's presenter: %v", err)
	}
	fake.Next(fakeheygen.Moderation)
	tk, err := a.Submit(ctx, key, request(t))
	if err != nil {
		t.Fatal(err)
	}
	if st := run(t, a, key, tk); st.State != aiservices.StateFailed || st.Err.Kind != videogen.KindSafety {
		t.Fatalf("moderation: %+v", st)
	}
	fake.Next(fakeheygen.ExpiredResult)
	tk, err = a.Submit(ctx, key, request(t))
	if err != nil {
		t.Fatal(err)
	}
	st := run(t, a, key, tk)
	if len(st.Outputs) == 0 {
		t.Fatalf("expired run: %+v %+v", st, st.Err)
	}
	var buf bytes.Buffer
	if _, err := a.Fetch(ctx, key, tk, &st.Outputs[0], &buf); kindOf(err) != videogen.KindExpired {
		t.Fatalf("expired: %v", err)
	}
	// A lost answer: asked again with the same tag (the idempotency key: Go's
	// client resends such a request by itself on a dropped connection, and
	// MetaRush's own retry would too), the same video comes back, never a second.
	before := fake.Calls("POST /v3/videos")
	fake.Next(fakeheygen.Lost)
	req := request(t)
	req.Tag = "mr-lostonce"
	tk, err = a.Submit(ctx, key, req)
	if err != nil && kindOf(err) != videogen.KindUncertain {
		t.Fatalf("lost: %v", err)
	}
	if err != nil {
		if tk, err = a.Submit(ctx, key, req); err != nil {
			t.Fatal(err)
		}
	}
	n := 0
	for _, v := range fake.Videos() {
		if strings.HasSuffix(v, " mr-lostonce") {
			n++
		}
	}
	if n != 1 || fake.Calls("POST /v3/videos") != before+2 || tk.ID == "" {
		t.Fatalf("the lost video was made %d times (%d calls)", n, fake.Calls("POST /v3/videos")-before)
	}
	if ps := fake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}

// The key: checked by reading the account, never repeated; the base address
// takes only HeyGen's host or this computer, and only this computer under go test.
func TestHeyGenKeyAndAddress(t *testing.T) {
	_, key := setup(t)
	res, err := providers.HeyGen{}.Test(context.Background(), key)
	if err != nil || !res.OK {
		t.Fatalf("test: %+v %v", res, err)
	}
	res, err = providers.HeyGen{}.Test(context.Background(), "wrong-key-for-heygen-0000")
	if err != nil || res.OK || strings.Contains(res.Message, "wrong-key") {
		t.Fatalf("wrong key: %+v %v", res, err)
	}
	t.Setenv(providers.EnvHeyGenBaseURL, "https://evil.example.com")
	if _, err := providers.HeyGenBaseURL(); err == nil {
		t.Error("a foreign host")
	}
	t.Setenv(providers.EnvHeyGenBaseURL, "https://api.heygen.com")
	if _, err := providers.HeyGenBaseURL(); err == nil || !strings.Contains(err.Error(), "Tests never call HeyGen") {
		t.Errorf("the real host under go test: %v", err)
	}
}

// The catalogue: priced by the second, its stock licence offered (commercial
// use and client work on paid plans, ads on paid plans too), a person's own
// presenter not made there, training unless opted out.
func TestHeyGenCatalogue(t *testing.T) {
	m, err := aiservices.ModelFor(heygen.Name, aiservices.KindPresenter, "")
	if err != nil {
		t.Fatal(err)
	}
	req := &aiservices.Request{Kind: aiservices.KindPresenter, Model: m.ID, Inputs: []aiservices.Media{{Role: aiservices.RoleAudio, Seconds: 20}}, Options: aiservices.Options{Presenter: "lk_x", Stock: true}}
	e, err := aiservices.EstimateFor(heygen.Name, m, req, time.Now())
	if err != nil || e.USD < 0.999 || e.USD > 1.001 || e.Units != "20 seconds on camera" {
		t.Fatalf("estimate %+v %v", e, err)
	}
	req.Options.AspectRatio = "3:2"
	if err := m.Check(req, time.Now()); err == nil || !strings.Contains(err.Error(), "clips shaped") {
		t.Errorf("shape: %v", err)
	}
	p, _ := aiservices.ProviderOf(heygen.Name)
	if !p.Presenters.Stock.Offered() || p.Presenters.ConsentMode != aiservices.ConsentHostedPage || p.Data.TrainsOnUploads != aiservices.TrainsUnlessOptedOut {
		t.Errorf("terms %+v %+v", p.Presenters, p.Data)
	}
	if _, err := aiservices.ModelFor(heygen.Name, aiservices.KindPresenterMake, ""); err == nil {
		t.Error("a person's presenter at HeyGen isn't built")
	}
}

// An answer MetaRush can't read after HeyGen was asked for a clip: it may
// have been made, so it is never sent again (uncertain, not a server error to
// retry); the clip is found by its title (MetaRush's tag) and deleted with
// the sound uploaded for it. The sound goes up once per clip: asked again
// with the same tag, HeyGen gives back the same upload (D277).
func TestHeyGenLostClipIsFoundByItsTitle(t *testing.T) {
	fake, key := setup(t)
	a := adapter(t)
	ctx := context.Background()
	req := request(t)
	fake.Next(fakeheygen.Garbled)
	_, err := a.Submit(ctx, key, req)
	var ve *videogen.Error
	if !errors.As(err, &ve) || ve.Kind != videogen.KindUncertain || len(ve.Files) != 1 || !strings.HasPrefix(ve.Files[0], "assets/") {
		t.Fatalf("garbled: %v", err)
	}
	if len(fake.Videos()) != 1 || fake.Assets() != 1 {
		t.Fatalf("videos %v, sound %d", fake.Videos(), fake.Assets())
	}
	n, err := a.(aiservices.TagCleaner).ForgetTagged(ctx, key, req.Tag)
	if err != nil || n != 1 {
		t.Fatalf("found %d by its title: %v", n, err)
	}
	if err := a.Forget(ctx, key, &aiservices.Ticket{Files: ve.Files}, nil); err != nil {
		t.Fatal(err)
	}
	if fake.Kept() != 0 {
		t.Fatalf("left at HeyGen: %d", fake.Kept())
	}
	// The same request twice: one upload, one video.
	again := request(t)
	t1, err := a.Submit(ctx, key, again)
	if err != nil {
		t.Fatal(err)
	}
	t2, err := a.Submit(ctx, key, again)
	if err != nil || t2.ID != t1.ID || fake.Assets() != 1 || strings.Join(t1.Files, ",") != strings.Join(t2.Files, ",") {
		t.Fatalf("asked twice: %+v %+v sound %d %v", t1, t2, fake.Assets(), err)
	}
	if ps := fake.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}
