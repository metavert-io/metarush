package api

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// The dashboard plays voice samples through the daemon: signed-in sessions
// only, the provider key never reaches the browser, and errors stay JSON.
func TestVoicePreviewStreamsThroughTheDaemon(t *testing.T) {
	keyring.MockInit()                             // never touch the real keychain
	const key = "sk_test_preview_0123456789abcdef" // secret-scan: allow
	fake := fakeeleven.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	get := func(path string) (int, string, []byte) {
		t.Helper()
		res, err := d.client.Get(d.base + path)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Content-Type"), b
	}
	const ada = "/api/providers/elevenlabs/voices/voice_ada/preview"

	if code, _, _ := get(ada); code != http.StatusUnauthorized {
		t.Fatalf("a sample without a session: %d, want 401", code)
	}
	d.signIn()

	// No key on this machine: an honest "not set up", as JSON.
	code, ct, body := get(ada)
	if code == http.StatusOK || !strings.Contains(ct, "json") || !bytes.Contains(body, []byte("E_NOT_CONFIGURED")) {
		t.Fatalf("without a key: %d %s %s", code, ct, body)
	}

	if _, err := providers.StoreKey(d.a.Cfg, providers.ElevenLabsName, key, false); err != nil {
		t.Fatal(err)
	}
	code, ct, body = get(ada)
	if code != http.StatusOK || ct != "audio/wav" || !bytes.HasPrefix(body, []byte("RIFF")) {
		t.Fatalf("account voice sample: %d %s %d bytes", code, ct, len(body))
	}
	if bytes.Contains(body, []byte(key)) {
		t.Fatal("the sample response contains the key")
	}
	if code, ct, body = get("/api/providers/elevenlabs/voices/lib_dawn/preview?library=1"); code != http.StatusOK || !bytes.HasPrefix(body, []byte("RIFF")) {
		t.Fatalf("library voice sample: %d %s", code, ct)
	}
	if code, _, body = get("/api/providers/elevenlabs/voices/voice_nobody/preview"); code != http.StatusNotFound {
		t.Fatalf("unknown voice: %d %s", code, body)
	}
	if code, _, body = get("/api/providers/acme/voices/voice_ada/preview"); code != http.StatusNotFound || !bytes.Contains(body, []byte("E_NOT_FOUND")) {
		t.Fatalf("unknown provider: %d %s", code, body)
	}
	if fake.Calls("preview") < 2 {
		t.Fatalf("samples should come from the provider's sample files, got %d fetches", fake.Calls("preview"))
	}
}
