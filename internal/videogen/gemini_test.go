package videogen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
)

// fakeKey builds a key-shaped value at run time (no literal for the secret scanner).
func fakeKey() string { return "gk-" + strings.Repeat("Zq9", 9) + "-test" }

func fake(t *testing.T) (*fakevideogen.Server, string) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is needed for the fake's videos")
	}
	key := fakeKey()
	s := fakevideogen.New(key)
	t.Cleanup(s.Close)
	t.Setenv(EnvGeminiBaseURL, s.URL)
	return s, key
}

func waitDone(t *testing.T, key string, tk *Ticket) *Status {
	t.Helper()
	for i := 0; i < 20; i++ {
		st, err := Gemini{}.Poll(context.Background(), key, tk)
		if err != nil {
			t.Fatalf("poll: %v", err)
		}
		if st.State != StateRunning {
			return st
		}
	}
	t.Fatal("never finished")
	return nil
}

func TestBaseURLOnlyGoogleOrLoopback(t *testing.T) {
	for _, bad := range []string{"https://evil.example.com", "http://10.0.0.5:9", "https://generativelanguage.googleapis.com.evil.com", "http://user:pw@127.0.0.1:1"} {
		t.Setenv(EnvGeminiBaseURL, bad)
		if _, err := GeminiBaseURL(); err == nil {
			t.Errorf("%s must be refused (the key would be sent there)", bad)
		}
	}
	t.Setenv(EnvGeminiBaseURL, "http://127.0.0.1:5146")
	if b, err := GeminiBaseURL(); err != nil || b != "http://127.0.0.1:5146" {
		t.Fatalf("loopback test server: %v %v", b, err)
	}
	if !allowedGeminiBase("https://generativelanguage.googleapis.com") {
		t.Fatal("Google's own host is the default outside tests")
	}
}

// A test that forgets its fake can never reach Google: under go test the real
// host is refused, whether it is the default or set on purpose.
func TestTestsNeverReachGoogle(t *testing.T) {
	for _, base := range []string{"", "https://generativelanguage.googleapis.com"} {
		t.Setenv(EnvGeminiBaseURL, base)
		if b, err := GeminiBaseURL(); err == nil || !strings.Contains(err.Error(), "Tests never call Google") {
			t.Fatalf("%q: the real host must be refused under go test, got %q %v", base, b, err)
		}
	}
	t.Setenv(EnvGeminiBaseURL, "")
	if _, err := (Gemini{}).client(fakeKey()); err == nil {
		t.Fatal("a client without a fake must not be built")
	}
}

func TestEstimateUsesTheDatedTable(t *testing.T) {
	g := Gemini{}
	at := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	e, err := g.Estimate(&Request{Model: "gemini-omni-1.1-flash", Task: TaskTextToVideo, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p", AspectRatio: "16:9"}, at)
	if err != nil {
		t.Fatal(err)
	}
	if e.VideoTokens != 3*1931 || e.PriceTable != "2026-10-19" || e.PerSecondUSD != 0.034 {
		t.Fatalf("estimate %+v", e)
	}
	// 3 s of 360p ≈ $0.10, plus the thinking allowance: well under the $0.50 smoke-test cap.
	if e.USD < 0.10 || e.USD > 0.2 {
		t.Fatalf("usd %v", e.USD)
	}
	if _, err := g.Estimate(&Request{Model: "gemini-omni-1.1-flash", Seconds: 3, Resolution: "360p"}, time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Fatal("before the first table there is no price")
	}
	e6, _ := g.Estimate(&Request{Model: "gemini-omni-1.1-flash", Prompt: "x", Seconds: 6, Resolution: "720p"}, at)
	if e6.VideoTokens != 34752 || e6.PerSecondUSD != 0.101 {
		t.Fatalf("720p %+v", e6)
	}
}

func TestCapabilitiesRefusePlainly(t *testing.T) {
	c, err := ModelInfo(GeminiName, "")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	ok := &Request{Model: c.ID, Task: TaskTextToVideo, Prompt: "boat", Seconds: 6, Resolution: "720p", AspectRatio: "16:9"}
	if err := c.Check(ok, 0, now); err != nil {
		t.Fatal(err)
	}
	for name, r := range map[string]Request{
		"too long":     {Model: c.ID, Task: TaskTextToVideo, Prompt: "x", Seconds: 12, Resolution: "720p", AspectRatio: "16:9"},
		"square":       {Model: c.ID, Task: TaskTextToVideo, Prompt: "x", Seconds: 6, Resolution: "720p", AspectRatio: "1:1"},
		"8k":           {Model: c.ID, Task: TaskTextToVideo, Prompt: "x", Seconds: 6, Resolution: "8k", AspectRatio: "16:9"},
		"long ref":     {Model: c.ID, Task: TaskReferenceToVideo, Prompt: "x", Seconds: 6, Resolution: "720p", AspectRatio: "16:9", Videos: []Media{{Seconds: 4}}},
		"one frame":    {Model: c.ID, Task: TaskInterpolate, Prompt: "x", Seconds: 6, Resolution: "720p", AspectRatio: "16:9", Images: []Media{{}}},
		"long extend":  {Model: c.ID, Task: TaskExtend, Prompt: "x", Seconds: 10, Resolution: "720p", AspectRatio: "16:9", Videos: []Media{{Seconds: 10}}},
		"empty prompt": {Model: c.ID, Task: TaskTextToVideo, Prompt: " ", Seconds: 6, Resolution: "720p", AspectRatio: "16:9"},
	} {
		extend := 0.0
		if name == "long extend" {
			extend = 35
		}
		if err := c.Check(&r, extend, now); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	retired := *c
	retired.RetiredOn = "2026-01-01"
	if err := retired.Check(ok, 0, now); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("a retired model: %v", err)
	}
}

// Text to video, inline: sent in the background, read, downloaded, then
// deleted at the provider; the key only ever travels in its header.
func TestGeminiTextToVideoInline(t *testing.T) {
	s, key := fake(t)
	g := Gemini{PollFiles: 10 * time.Millisecond}
	req := &Request{Model: "gemini-omni-1.1-flash", Task: TaskTextToVideo, Prompt: "a red paper boat drifting on a pond", Seconds: 3, Resolution: "360p", AspectRatio: "16:9"}
	tk, err := g.Submit(context.Background(), key, req)
	if err != nil {
		t.Fatal(err)
	}
	st := waitDone(t, key, tk)
	if st.State != StateDone || len(st.Outputs) != 1 || st.Outputs[0].Delivery() != "inline" || !st.Usage.Known || st.Usage.VideoTokens != 3*1931 {
		t.Fatalf("status %+v", st)
	}
	var buf bytes.Buffer
	n, err := g.Fetch(context.Background(), key, tk, &st.Outputs[0], &buf)
	if err != nil || n < 1000 || !bytes.Contains(buf.Bytes()[:64], []byte("ftyp")) {
		t.Fatalf("fetch %d %v", n, err)
	}
	it := s.Interactions()[0]
	if it.Body["background"] != true || it.Body["store"] != true || it.Seconds != 3 || it.Resolution != "360p" || it.Delivery != "inline" || it.Task != "" {
		t.Fatalf("request %+v", it.Body)
	}
	// Inline is Google's default: the field is left out (its guide and reference name it differently).
	if rf, _ := it.Body["response_format"].(map[string]any); rf == nil || rf["duration"] != "3s" || rf["delivery"] != nil {
		t.Fatalf("response_format %+v", it.Body["response_format"])
	}
	if err := g.Forget(context.Background(), key, tk, st.Outputs); err != nil {
		t.Fatal(err)
	}
	if ki, kf := s.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("kept at the provider: %d requests, %d files", ki, kf)
	}
	if p := s.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
	// The same request again is a different take (no seed): different bytes.
	tk2, _ := g.Submit(context.Background(), key, req)
	st2 := waitDone(t, key, tk2)
	var buf2 bytes.Buffer
	_, _ = g.Fetch(context.Background(), key, tk2, &st2.Outputs[0], &buf2)
	if bytes.Equal(buf.Bytes(), buf2.Bytes()) {
		t.Fatal("a second take must not be byte-identical")
	}
}

// A long final-quality shot comes as a file: downloaded once ready, then deleted.
// An extension uploads its clip through the Files API (checked, deleted after).
func TestGeminiFilesBothWays(t *testing.T) {
	s, key := fake(t)
	g := Gemini{PollFiles: 10 * time.Millisecond}
	clip := filepath.Join(t.TempDir(), "in.mp4")
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=24:duration=2", "-pix_fmt", "yuv420p", clip).CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	b, _ := os.ReadFile(clip)
	sum := sha256.Sum256(b)
	req := &Request{Model: "gemini-omni-1.1-flash", Task: TaskExtend, Prompt: "Continue the scene.", Seconds: 8, Resolution: "1080p", AspectRatio: "16:9",
		Videos: []Media{{Role: "input-video", MIME: "video/mp4", Path: clip, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(b)), Seconds: 2}}}
	tk, err := g.Submit(context.Background(), key, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(tk.Files) != 1 {
		t.Fatalf("uploaded %v", tk.Files)
	}
	st := waitDone(t, key, tk)
	if st.State != StateDone || st.Outputs[0].Delivery() != "uri" || st.Outputs[0].FileID == "" {
		t.Fatalf("status %+v", st)
	}
	it := s.Interactions()[0]
	if it.Task != TaskExtend || len(it.Videos) != 1 || it.Delivery != "uri" {
		t.Fatalf("request %+v", it)
	}
	var buf bytes.Buffer
	if _, err := g.Fetch(context.Background(), key, tk, &st.Outputs[0], &buf); err != nil || buf.Len() < 1000 {
		t.Fatalf("download: %v %d", err, buf.Len())
	}
	if err := g.Forget(context.Background(), key, tk, st.Outputs); err != nil {
		t.Fatal(err)
	}
	if ki, kf := s.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("kept: %d %d", ki, kf)
	}
	if p := s.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
}

func kindOf(err error) string {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	return ""
}

// Every scripted misbehavior is understood: blocks on either side with their
// codes, rate against spend limits, server trouble, a retired model, a partial
// result, a failed request and an expired output.
func TestGeminiMisbehaviors(t *testing.T) {
	s, key := fake(t)
	g := Gemini{PollFiles: 10 * time.Millisecond}
	ctx := context.Background()
	req := func(prompt string) *Request {
		return &Request{Model: "gemini-omni-1.1-flash", Task: TaskTextToVideo, Prompt: prompt, Seconds: 4, Resolution: "360p", AspectRatio: "16:9"}
	}
	// Input side: refused when sent.
	_, err := g.Submit(ctx, key, req("[fake:celebrity] a famous actor waves"))
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindSafety || e.Safety == nil || e.Safety.Side != "input" || e.Safety.Category != "person" || len(e.Safety.SupportCodes) != 1 {
		t.Fatalf("celebrity: %+v", err)
	}
	if ae := e.AppError(); ae.Code != "E_SAFETY_BLOCKED" {
		t.Fatal(ae.Code)
	}
	// Output side: finished without a video.
	tk, err := g.Submit(ctx, key, req("[fake:child] kids at a party"))
	if err != nil {
		t.Fatal(err)
	}
	st := waitDone(t, key, tk)
	if st.State != StateFailed || st.Safety == nil || st.Safety.Side != "output" || st.Safety.Category != "child" || st.Err.Kind != KindSafety {
		t.Fatalf("child: %+v %+v", st, st.Safety)
	}
	// The answer's shape is kept (to tell a refusal from a form this version doesn't know), never its values.
	if !strings.Contains(st.Err.Detail, "Google's answer had: ") || !strings.Contains(st.Err.Detail, "status") || strings.Contains(st.Err.Detail, tk.ID) {
		t.Fatalf("shape: %q", st.Err.Detail)
	}
	// Rate limit against spend limit.
	_, err = g.Submit(ctx, key, req("[fake:rate] a boat"))
	if !errors.As(err, &e) || e.Kind != KindRate || e.RetryAfter != time.Second || !e.Retryable() {
		t.Fatalf("rate: %+v", err)
	}
	_, err = g.Submit(ctx, key, req("[fake:spend] a boat"))
	if !errors.As(err, &e) || e.Kind != KindSpend || e.RetryAfter < 10*time.Minute {
		t.Fatalf("spend: %+v", err)
	}
	// Server trouble when sending: retryable, nothing left at the provider.
	_, err = g.Submit(ctx, key, req("[fake:server] a boat"))
	if kindOf(err) != KindServer {
		t.Fatalf("server: %v", err)
	}
	// A retired model.
	s.Retired["gemini-omni-1.1-flash"] = true
	_, err = g.Submit(ctx, key, req("a boat"))
	if kindOf(err) != KindRetired {
		t.Fatalf("retired: %v", err)
	}
	delete(s.Retired, "gemini-omni-1.1-flash")
	// Partial: a shorter video, marked as such.
	tk, _ = g.Submit(ctx, key, req("[fake:partial] a boat"))
	st = waitDone(t, key, tk)
	if st.State != StateDone || !st.Partial || len(st.Outputs) != 1 {
		t.Fatalf("partial %+v", st)
	}
	// Failed at the provider.
	tk, _ = g.Submit(ctx, key, req("[fake:fail] a boat"))
	st = waitDone(t, key, tk)
	if st.State != StateFailed || st.Err == nil || st.Err.Kind != KindServer {
		t.Fatalf("fail %+v", st)
	}
	// A poll error is reported, the next poll goes on.
	tk, _ = g.Submit(ctx, key, req("[fake:poll-error] a boat"))
	if _, err := g.Poll(ctx, key, tk); kindOf(err) != KindServer {
		t.Fatalf("poll error: %v", err)
	}
	if st = waitDone(t, key, tk); st.State != StateDone {
		t.Fatal(st.State)
	}
	// Expired output (a file Google no longer has).
	r := req("[fake:expired] a boat")
	r.Resolution, r.Seconds = "1080p", 10
	tk, _ = g.Submit(ctx, key, r)
	st = waitDone(t, key, tk)
	_, err = g.Fetch(ctx, key, tk, &st.Outputs[0], &bytes.Buffer{})
	if kindOf(err) != KindExpired {
		t.Fatalf("expired: %v", err)
	}
	// Cancel.
	s.ReadyAfter = 5
	tk, _ = g.Submit(ctx, key, req("a slow boat"))
	if err := g.Cancel(ctx, key, tk); err != nil {
		t.Fatal(err)
	}
	if st, _ := g.Poll(ctx, key, tk); st.State != StateCanceled {
		t.Fatal(st.State)
	}
}

func TestKeyCheck(t *testing.T) {
	s, key := fake(t)
	r, err := geminiKey{}.Test(context.Background(), key)
	if err != nil || !r.OK || !strings.Contains(r.Message, "Gemini Omni 1.1 Flash") {
		t.Fatalf("%+v %v", r, err)
	}
	// Listing the models can't tell a free-tier project (it lists the video model too): the check promises no video,
	// it says what Google needs and that the first shot tells (D178).
	if strings.Contains(r.Message, "can make video") || !strings.Contains(r.Message, "billing turned on") || !strings.Contains(r.Message, "first shot") {
		t.Fatalf("the check must not promise video it hasn't seen: %q", r.Message)
	}
	s.SetFreeTier(fakevideogen.FreeTierOnStatus)
	if r, _ := (geminiKey{}).Test(context.Background(), key); !r.OK || strings.Contains(r.Message, "can make video") {
		t.Fatalf("a free-tier key: %+v", r)
	}
	s.SetFreeTier("")
	r, err = geminiKey{}.Test(context.Background(), "gk-wrong-"+strings.Repeat("x", 20))
	if err != nil || r.OK || !strings.Contains(r.Message, "did not accept") {
		t.Fatalf("wrong key: %+v %v", r, err)
	}
	s.Retired["gemini-omni-1.1-flash"] = true
	r, _ = geminiKey{}.Test(context.Background(), key)
	if !r.OK || !strings.Contains(r.Message, "doesn't offer") {
		t.Fatalf("no video model: %+v", r)
	}
}

// seenFreeTier is Google's answer, word for word, to every status check of a request a free-tier project had
// started (2026-10-20): a 429 whose code is a word, with no status and no details.
const seenFreeTier = `{"error":{"code":"too_many_requests","message":"Rate limit exceeded for model gemini-omni-1.1-flash (limit: 0 input tokens per minute on Free Tier). Please upgrade your tier at https://ai.dev/rate-limit."}}`

// The free tier is never a rate limit to wait out (D178): Google's answer of 2026-10-20 and its other wordings
// (quota names, a limit of zero, an error written as text, "billed users") are KindTier on any call, said plainly
// with Google's page behind the hint, and never retried — while real rate and spending limits stay as they were.
func TestFreeTierIsNotARateLimit(t *testing.T) {
	for _, phase := range []string{"submit", "poll"} {
		e := classifyHTTP(429, []byte(seenFreeTier), http.Header{}, phase)
		if e.Kind != KindTier || e.Retryable() || e.RetryAfter != 0 || e.HTTPStatus != 429 ||
			e.Message != "This Google project is on the free tier, which can't make video. Turn on billing for the project in Google AI Studio, then try again." ||
			!strings.Contains(e.Hint, "https://ai.dev/rate-limit") || !strings.Contains(e.Hint, "Nothing was made or charged") ||
			!strings.Contains(e.Detail, "429 too_many_requests Rate limit exceeded") || !strings.Contains(e.Detail, "limit: 0") {
			t.Fatalf("%s: %+v", phase, e)
		}
		ae := e.AppError()
		if d, _ := ae.Details.(map[string]any); ae.Code != apperr.Provider || d["reason"] != KindTier || d["retryAfterSeconds"] != nil {
			t.Fatalf("%s: %+v", phase, ae)
		}
		if f := plainwords.Check(e.Message + " " + e.Hint); len(f) > 0 {
			t.Fatalf("not plain: %v", f)
		}
	}
	free := map[string]struct {
		status int
		body   string
	}{
		// Google's usual quota answer for the free tier: "billing" in it must not make it a spending limit.
		"quota failure":    {429, `{"error":{"code":429,"message":"You exceeded your current quota, please check your plan and billing details.\n* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-omni-1.1-flash","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaMetric":"generativelanguage.googleapis.com/generate_content_free_tier_input_token_count","quotaId":"GenerateContentInputTokensPerModelPerMinute-FreeTier","quotaValue":"0"}]},{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"28s"}]}}`},
		"quota id only":    {429, `{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.QuotaFailure","violations":[{"quotaId":"GenerateRequestsPerDayPerProjectPerModel-FreeTier"}]}]}}`},
		"an error as text": {429, `{"error":"Rate limit exceeded for model gemini-omni-1.1-flash (limit: 0 input tokens per minute on Free Tier)."}`},
		"plain text":       {429, `Too many requests: free tier limit reached`},
		"a word for 429":   {400, `{"error":{"code":"too_many_requests","message":"Free Tier: limit 0 requests per minute for this model."}}`},
		"billed users":     {400, `{"error":{"code":400,"message":"Gemini Omni is only available to billed users at this time.","status":"FAILED_PRECONDITION"}}`},
		"billing disabled": {403, `{"error":{"code":403,"message":"This API method requires billing to be enabled.","status":"PERMISSION_DENIED","details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"BILLING_DISABLED"}]}}`},
	}
	for name, c := range free {
		if e := classifyHTTP(c.status, []byte(c.body), http.Header{}, "poll"); e.Kind != KindTier || e.Retryable() || !strings.Contains(e.Message, "free tier") {
			t.Errorf("%s: %+v", name, e)
		}
	}
	// A limit of zero on a project that isn't said to be on the free tier: the same handling, other words.
	e := classifyHTTP(429, []byte(`{"error":{"code":429,"message":"Quota exceeded for metric: generativelanguage.googleapis.com/generate_requests_per_model, limit: 0","status":"RESOURCE_EXHAUSTED"}}`), http.Header{}, "submit")
	if e.Kind != KindTier || strings.Contains(e.Message, "free tier") || !strings.Contains(e.Message, "limit is zero") || !strings.Contains(e.Hint, TierLink) {
		t.Fatalf("a zero limit: %+v", e)
	}
	// Real limits stay limits: waited out (the rate) or ten minutes (the spending).
	for name, c := range map[string]struct {
		body string
		kind string
	}{
		"rate":       {`{"error":{"code":429,"message":"Resource has been exhausted (e.g. check quota).","status":"RESOURCE_EXHAUSTED","details":[{"@type":"type.googleapis.com/google.rpc.RetryInfo","retryDelay":"1s"}]}}`, KindRate},
		"a limit":    {`{"error":{"code":429,"message":"Quota exceeded for metric: generativelanguage.googleapis.com/generate_requests_per_model, limit: 10","status":"RESOURCE_EXHAUSTED"}}`, KindRate},
		"a fraction": {`{"error":{"code":429,"message":"Request rate limit: 0.5 a second, exceeded.","status":"RESOURCE_EXHAUSTED"}}`, KindRate},
		"spend":      {`{"error":{"code":429,"message":"Your project exceeded its spend limit for the current 10-minute window.","status":"RESOURCE_EXHAUSTED"}}`, KindSpend},
		"a word":     {`{"error":{"code":"too_many_requests","message":"Rate limit exceeded for model gemini-omni-1.1-flash. Please try again later."}}`, KindRate},
	} {
		if e := classifyHTTP(429, []byte(c.body), http.Header{}, "poll"); e.Kind != c.kind || !e.Retryable() || e.RetryAfter <= 0 {
			t.Errorf("%s: %+v", name, e)
		}
	}
	// Google's own trouble is never taken for the free tier.
	if e := classifyHTTP(503, []byte(`{"error":{"code":503,"message":"Free tier capacity is busy.","status":"UNAVAILABLE"}}`), http.Header{}, "poll"); e.Kind != KindServer {
		t.Fatalf("a 503: %+v", e)
	}
}

// The free tier said in a request's own status (failed, with errors whose codes are words or numbers) is the same.
func TestFreeTierInAFailedRequest(t *testing.T) {
	for _, body := range []string{
		`{"id":"v1_x","status":"failed","errors":[{"code":429,"message":"Rate limit exceeded for model gemini-omni-1.1-flash (limit: 0 input tokens per minute on Free Tier)."}]}`,
		`{"id":"v1_x","status":"failed","error":{"code":"too_many_requests","message":"Rate limit exceeded (limit: 0 input tokens per minute on Free Tier)."}}`,
	} {
		var it interaction
		if err := json.Unmarshal([]byte(body), &it); err != nil {
			t.Fatalf("a code as a number or a word must be readable: %v", err)
		}
		st := statusOf(&it)
		if st.State != StateFailed || st.Err == nil || st.Err.Kind != KindTier || !strings.Contains(st.Err.Message, "free tier") || !strings.Contains(st.Err.Detail, "limit: 0") {
			t.Fatalf("%s: %+v", body, st.Err)
		}
	}
	// A failure that isn't about the tier stays what it was.
	var it interaction
	_ = json.Unmarshal([]byte(`{"id":"v1_x","status":"failed","errors":[{"code":13,"message":"The video could not be generated."}]}`), &it)
	if st := statusOf(&it); st.Err == nil || st.Err.Kind != KindServer {
		t.Fatalf("%+v", st.Err)
	}
}

// The fake plays the free tier word for word: on every status check of an accepted request (as Google did), when a
// request is sent, or for one request by its prompt; cancelling and deleting still work.
func TestGeminiFreeTier(t *testing.T) {
	s, key := fake(t)
	g := Gemini{PollFiles: 10 * time.Millisecond}
	ctx := context.Background()
	req := func(prompt string) *Request {
		return &Request{Model: "gemini-omni-1.1-flash", Task: TaskTextToVideo, Prompt: prompt, Seconds: 3, Resolution: "360p", AspectRatio: "16:9"}
	}
	s.SetFreeTier(fakevideogen.FreeTierOnStatus)
	tk, err := g.Submit(ctx, key, req("a red paper boat drifting on a pond"))
	if err != nil {
		t.Fatalf("the request is accepted: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := g.Poll(ctx, key, tk); kindOf(err) != KindTier {
			t.Fatalf("check %d: %v", i+1, err)
		}
	}
	hr, _ := http.NewRequest(http.MethodGet, s.URL+"/v1beta/interactions/"+tk.ID, nil)
	hr.Header.Set("x-goog-api-key", key)
	res, err := http.DefaultClient.Do(hr)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != 429 || strings.TrimSpace(string(raw)) != seenFreeTier {
		t.Fatalf("the fake's answer differs from Google's: %d %s", res.StatusCode, raw)
	}
	if err := g.Cancel(ctx, key, tk); err != nil {
		t.Fatal(err)
	}
	if err := g.Forget(ctx, key, tk, nil); err != nil {
		t.Fatal(err)
	}
	// Refused when sent.
	s.SetFreeTier(fakevideogen.FreeTierOnStart)
	_, err = g.Submit(ctx, key, req("a red paper boat"))
	var e *Error
	if !errors.As(err, &e) || e.Kind != KindTier || e.HTTPStatus != 429 || !strings.Contains(e.Detail, "Free Tier") {
		t.Fatalf("when sent: %+v", err)
	}
	// One request, by its prompt.
	s.SetFreeTier("")
	tk, _ = g.Submit(ctx, key, req("[fake:free-tier] a boat"))
	if _, err := g.Poll(ctx, key, tk); kindOf(err) != KindTier {
		t.Fatalf("by prompt: %v", err)
	}
	if _, err := g.Submit(ctx, key, req("[fake:free-tier-start] a boat")); kindOf(err) != KindTier {
		t.Fatalf("by prompt, when sent: %v", err)
	}
	// Status checks answered with ordinary 429s until they aren't.
	tk, _ = g.Submit(ctx, key, req("[fake:poll-rate:2] a boat"))
	for i := 0; i < 2; i++ {
		if _, err := g.Poll(ctx, key, tk); kindOf(err) != KindRate {
			t.Fatalf("poll-rate %d: %v", i+1, err)
		}
	}
	if st := waitDone(t, key, tk); st.State != StateDone {
		t.Fatal(st.State)
	}
	if p := s.Problems(); len(p) > 0 {
		t.Fatal(p)
	}
}

// Google's answer when it has blocked a key's project (seen live on 2026-10-20) says what only the person can do.
func TestDeniedProjectSaysWhatToDo(t *testing.T) {
	body := []byte(`{"error":{"code":403,"message":"Your project has been denied access. Please contact support.","status":"PERMISSION_DENIED"}}`)
	e := classifyHTTP(403, body, nil, "submit")
	if e.Kind != KindPermission || !strings.Contains(e.Message, "blocked this key's project") || !strings.Contains(e.Detail, "denied access") || e.Retryable() {
		t.Fatalf("%+v", e)
	}
	// Other refusals keep the general advice (billing, the paid tier).
	e = classifyHTTP(403, []byte(`{"error":{"code":403,"message":"Permission denied on resource project.","status":"PERMISSION_DENIED"}}`), nil, "submit")
	if e.Kind != KindPermission || !strings.Contains(e.Message, "billing") {
		t.Fatalf("%+v", e)
	}
}
