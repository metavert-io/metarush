package publish

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/testutil/fakeyoutube"
)

func testConfig(t *testing.T) *config.Config {
	t.Helper()
	keyring.MockInit() // never the real keychain
	return config.Default(t.TempDir())
}

func consent(_ context.Context, u string) (func(), error) { return func() {}, fakeyoutube.Consent(u) }

// Signing in, posting a video with captions (through a dropped connection),
// and taking the sign-in back — all against the stand-in.
func TestYouTubeSignInAndUpload(t *testing.T) {
	yt := fakeyoutube.New(t)
	t.Setenv(EnvYouTubeBase, yt.Base())
	cfg := testConfig(t)
	ctx := context.Background()

	if _, err := ConnectYouTube(ctx, cfg, "usr_a", consent); !errors.Is(err, ErrNotSetUp) {
		t.Fatalf("no client set up: %v", err)
	}
	if err := SaveClient(cfg, YouTube, Client{ID: yt.ClientID, Secret: yt.Secret}); err != nil {
		t.Fatal(err)
	}
	if SignedIn(cfg, YouTube, "usr_a") != nil {
		t.Fatal("signed in before signing in")
	}
	yt.DenyConsent = true
	if _, err := ConnectYouTube(ctx, cfg, "usr_a", consent); err == nil || !strings.Contains(err.Error(), "didn't allow") {
		t.Fatalf("refused consent: %v", err)
	}
	yt.DenyConsent = false
	acct, err := ConnectYouTube(ctx, cfg, "usr_a", consent)
	if err != nil {
		t.Fatal(err)
	}
	if acct.Channel != "Garden Club" || acct.ChannelID != "UCfake" || len(acct.Scopes) != 2 {
		t.Fatalf("account %+v", acct)
	}
	if a := SignedIn(cfg, YouTube, "usr_a"); a == nil || a.Channel != "Garden Club" {
		t.Fatalf("kept: %+v", a)
	}
	if SignedIn(cfg, YouTube, "usr_b") != nil {
		t.Fatal("another person is signed in as the first")
	}

	// A 40 MB video in 16 MB pieces; the second piece drops halfway.
	video := filepath.Join(t.TempDir(), "cut.mp4")
	data := bytes.Repeat([]byte("0123456789abcdef"), 40<<20/16)
	if err := os.WriteFile(video, data, 0o644); err != nil {
		t.Fatal(err)
	}
	yt.DropAt = 20 << 20
	var reported []int64
	pctx := WithProgress(ctx, func(sent, total int64) { reported = append(reported, sent) })
	posted, err := UploadYouTube(pctx, cfg, "usr_a", Video{Path: video, Title: "The first tomato", Description: "0:00 Intro\n0:42 Harvest", Privacy: Private, Language: "en",
		Captions: []Caption{{Language: "en", Name: "English", Format: "vtt", Content: "WEBVTT\n\n00:00.000 --> 00:01.000\nHello\n"}, {Language: "es", Name: "Spanish", Format: "vtt", Content: "WEBVTT\n"}}})
	if err != nil {
		t.Fatal(err)
	}
	if len(yt.Videos) != 1 || !bytes.Equal(yt.Videos[0].Bytes, data) {
		t.Fatalf("the video arrived damaged: %d videos", len(yt.Videos))
	}
	v := yt.Videos[0]
	if v.Title != "The first tomato" || v.Privacy != Private || !strings.Contains(v.Desc, "0:42 Harvest") || len(v.Captions) != 2 || v.Captions[1].Language != "es" {
		t.Fatalf("video %+v", v)
	}
	if posted.VideoID != v.ID || !strings.HasPrefix(posted.URL, "https://www.youtube.com/watch?v=") || len(posted.Captions) != 2 || len(posted.Notes) != 0 {
		t.Fatalf("posted %+v", posted)
	}
	if len(reported) == 0 || reported[len(reported)-1] != int64(len(data)) {
		t.Fatalf("progress %v", reported)
	}

	// An unreviewed client: YouTube keeps it private, and says so.
	yt.KeepPrivate = true
	posted, err = UploadYouTube(ctx, cfg, "usr_a", Video{Path: video, Title: "Public please", Privacy: Public, Language: "en"})
	if err != nil || len(posted.Notes) != 1 || !strings.Contains(posted.Notes[0], "private") {
		t.Fatalf("kept private: %+v %v", posted, err)
	}
	// The daily allowance used up.
	yt.Quota = true
	_, err = UploadYouTube(ctx, cfg, "usr_a", Video{Path: video, Title: "Again", Privacy: Private})
	var p *Problem
	if !errors.As(err, &p) || !strings.Contains(p.Msg, "daily allowance") {
		t.Fatalf("quota: %v", err)
	}
	// Someone not signed in can't post.
	if _, err := UploadYouTube(ctx, cfg, "usr_b", Video{Path: video, Title: "x", Privacy: Private}); !errors.Is(err, ErrNotSignedIn) {
		t.Fatalf("not signed in: %v", err)
	}
	// Taking it back revokes at Google and forgets it here.
	if _, err := ForgetYouTube(ctx, cfg, "usr_a"); err != nil {
		t.Fatal(err)
	}
	if SignedIn(cfg, YouTube, "usr_a") != nil || len(yt.Revoked) != 1 {
		t.Fatalf("still signed in; revoked %v", yt.Revoked)
	}
}

// The real YouTube is never used in tests, and only a server on this
// computer can stand in for it.
func TestYouTubeRefusedUnderTest(t *testing.T) {
	cfg := testConfig(t)
	t.Setenv(EnvYouTubeBase, "")
	_ = SaveClient(cfg, YouTube, Client{ID: "id", Secret: "secret"})
	if _, err := ConnectYouTube(context.Background(), cfg, "", consent); err == nil || !strings.Contains(err.Error(), "never used in tests") {
		t.Fatalf("real YouTube under go test: %v", err)
	}
	t.Setenv(EnvYouTubeBase, "https://youtube.example.com")
	if _, err := youTubeEndpoints(); err == nil {
		t.Fatal("a stand-in elsewhere was accepted")
	}
}

// An installation that keeps its secrets in its secrets file (no keychain)
// keeps the client and each sign-in there, 0600.
func TestSecretsFile(t *testing.T) {
	cfg := testConfig(t)
	cfg.Secrets = "env"
	if err := SaveClient(cfg, YouTube, Client{ID: "id-1", Secret: "s3cret"}); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(cfg.Home(), ".env"))
	if err != nil || !strings.Contains(string(b), "METARUSH_PUBLISH_YOUTUBE_CLIENT=") {
		t.Fatalf(".env: %q %v", b, err)
	}
	if st, _ := os.Stat(filepath.Join(cfg.Home(), ".env")); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
	if c, err := LoadClient(cfg, YouTube); err != nil || c.ID != "id-1" {
		t.Fatalf("client %+v %v", c, err)
	}
	if err := saveSignIn(cfg, YouTube, "usr_abc", signIn{Refresh: "r-1", Identity: Identity{Channel: "Garden Club"}}); err != nil {
		t.Fatal(err)
	}
	// Read from the file where needed, never put into this process's
	// environment, where Claude Code and other programs would inherit them.
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "METARUSH_PUBLISH_") {
			t.Fatalf("%s is in the environment", strings.SplitN(kv, "=", 2)[0])
		}
	}
	if a := SignedIn(cfg, YouTube, "usr_abc"); a == nil || a.Channel != "Garden Club" {
		t.Fatalf("signed in %+v", a)
	}
	if err := Forget(cfg, YouTube, "usr_abc"); err != nil || SignedIn(cfg, YouTube, "usr_abc") != nil {
		t.Fatalf("forget: %v", err)
	}
}

// An upload cut short (MetaRush stopped) goes on later from the session
// YouTube keeps: only the rest of the file is sent, and the video is whole.
// A session YouTube no longer has says so.
func TestYouTubeUploadGoesOn(t *testing.T) {
	yt := fakeyoutube.New(t)
	t.Setenv(EnvYouTubeBase, yt.Base())
	cfg := testConfig(t)
	ctx := context.Background()
	if err := SaveClient(cfg, YouTube, Client{ID: yt.ClientID, Secret: yt.Secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectYouTube(ctx, cfg, "usr_a", consent); err != nil {
		t.Fatal(err)
	}
	old := UploadChunk
	UploadChunk = 256 << 10
	t.Cleanup(func() { UploadChunk = old })
	data := bytes.Repeat([]byte("0123456789abcdef"), 48<<10) // 768 KiB: three pieces
	p := filepath.Join(t.TempDir(), "v.mp4")
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	session := ""
	stopped, stop := context.WithCancel(ctx)
	sctx := WithProgress(stopped, func(sent, total int64) {
		if sent > 0 && sent < total {
			stop() // MetaRush stops after the first piece
		}
	})
	v := Video{Path: p, Size: int64(len(data)), Title: "Garden", Privacy: Private, Started: func(s string) { session = s }}
	if _, err := UploadYouTube(sctx, cfg, "usr_a", v); err == nil {
		t.Fatal("the stopped upload finished")
	}
	if session == "" || len(yt.Videos) != 0 {
		t.Fatalf("session %q, videos %d", session, len(yt.Videos))
	}
	var from []int64
	v.Started, v.Session = func(string) { t.Fatal("a new session was started") }, session
	posted, err := UploadYouTube(WithProgress(ctx, func(sent, _ int64) { from = append(from, sent) }), cfg, "usr_a", v)
	if err != nil {
		t.Fatal(err)
	}
	if len(yt.Videos) != 1 || !bytes.Equal(yt.Videos[0].Bytes, data) || posted.VideoID != yt.Videos[0].ID {
		t.Fatalf("videos %d, posted %+v", len(yt.Videos), posted)
	}
	if len(from) == 0 || from[0] != 256<<10 {
		t.Fatalf("it went on from %v, not after the first piece", from)
	}
	// Asked again, a finished session gives the same video.
	if again, err := UploadYouTube(ctx, cfg, "usr_a", v); err != nil || again.VideoID != posted.VideoID || len(yt.Videos) != 1 {
		t.Fatalf("again: %+v %v", again, err)
	}
	v.Session = yt.Base() + "/upload/session/gone"
	_, err = UploadYouTube(ctx, cfg, "usr_a", v)
	var pr *Problem
	if !errors.As(err, &pr) || !pr.Gone || !strings.Contains(pr.Msg, "no longer has the unfinished upload") {
		t.Fatalf("a gone session: %v", err)
	}
}

// An answer at the loopback address that isn't this sign-in's (a stale tab,
// another page, someone else on this computer) is turned away and the wait
// goes on for the real one; the state and code challenge never reach the log.
func TestYouTubeSignInWaitsForItsOwnAnswer(t *testing.T) {
	yt := fakeyoutube.New(t)
	t.Setenv(EnvYouTubeBase, yt.Base())
	cfg := testConfig(t)
	ctx := context.Background()
	if err := SaveClient(cfg, YouTube, Client{ID: yt.ClientID, Secret: yt.Secret}); err != nil {
		t.Fatal(err)
	}
	var turnedAway int
	pageGone := false
	open := func(ctx context.Context, raw string) (func(), error) {
		done := func() { pageGone = true }
		u, _ := url.Parse(raw)
		cb := u.Query().Get("redirect_uri")
		for _, bad := range []string{"?state=wrong&code=stolen", "?error=access_denied&state=wrong", "?code=nostate"} {
			res, err := http.Get(cb + bad)
			if err != nil {
				return done, err
			}
			res.Body.Close()
			if res.StatusCode == http.StatusBadRequest {
				turnedAway++
			}
		}
		for _, k := range []string{"state", "code_challenge"} {
			if !logbook.Remembered(u.Query().Get(k)) {
				t.Errorf("the %s isn't kept out of the log", k)
			}
		}
		return done, fakeyoutube.Consent(raw)
	}
	acct, err := ConnectYouTube(ctx, cfg, "usr_b", open)
	if err != nil || acct.Channel != "Garden Club" || turnedAway != 3 {
		t.Fatalf("signed in %+v %v, turned away %d", acct, err, turnedAway)
	}
	// The page that sent the browser on goes when the sign-in is over.
	if !pageGone {
		t.Fatal("the sign-in page outlived the sign-in")
	}
}

// An unfinished upload's address that isn't YouTube's own upload address
// gets nothing — never the person's sign-in.
func TestYouTubeSendsTheSignInOnlyToItsUploadAddress(t *testing.T) {
	yt := fakeyoutube.New(t)
	t.Setenv(EnvYouTubeBase, yt.Base())
	cfg := testConfig(t)
	ctx := context.Background()
	if err := SaveClient(cfg, YouTube, Client{ID: yt.ClientID, Secret: yt.Secret}); err != nil {
		t.Fatal(err)
	}
	if _, err := ConnectYouTube(ctx, cfg, "usr_a", consent); err != nil {
		t.Fatal(err)
	}
	var asked int
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked++
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer other.Close()
	p := filepath.Join(t.TempDir(), "v.mp4")
	_ = os.WriteFile(p, []byte("video bytes"), 0o600)
	_, err := UploadYouTube(ctx, cfg, "usr_a", Video{Path: p, Size: 11, Title: "T", Privacy: Private, Session: other.URL + "/upload/session/x"})
	var pr *Problem
	if !errors.As(err, &pr) || !pr.Gone || asked != 0 {
		t.Fatalf("another address: %v (asked %d times)", err, asked)
	}
	if !logbook.Remembered(other.URL + "/upload/session/x") {
		t.Fatal("the session's address isn't kept out of the log")
	}
}

// A request that doesn't get through is logged in plain words, never with
// Go's error text, which names the address it went to.
func TestYouTubeLogsFailedRequestsPlainly(t *testing.T) {
	for _, err := range []error{
		&url.Error{Op: "Put", URL: "https://www.googleapis.com/upload/youtube/v3/videos?upload_id=SECRET", Err: errors.New("EOF")},
		&url.Error{Op: "Post", URL: "https://oauth2.googleapis.com/token", Err: context.DeadlineExceeded},
	} {
		w := netWords(err)
		if strings.Contains(w, "googleapis") || strings.Contains(w, "SECRET") || w == "" {
			t.Fatalf("%q", w)
		}
	}
	b, _ := os.ReadFile("youtube.go")
	if strings.Contains(string(b), "ProviderDone(ctx, call, err)") {
		t.Fatal("a request's own error is logged as it is")
	}
}
