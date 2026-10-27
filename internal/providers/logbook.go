package providers

import (
	"context"
	"net/http"
	"strings"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
)

// The activity log never shows a provider key: every key this process reads or
// saves is reported to it (seen), and long-running processes (the dashboard,
// workers) ask the registry for every configured key when they start and every
// hour, so a key pasted into a prompt is hidden too.
func init() {
	logbook.AddSecretSource(func(cfg *config.Config) []string {
		var out []string
		for _, p := range All() {
			if k, _, err := Key(cfg, p.Name()); err == nil && k != "" {
				out = append(out, k)
			}
		}
		return out
	})
}

// seen tells the activity log about a key this process holds.
func seen(key string) string {
	logbook.RememberSecret(key)
	return key
}

type charsKey struct{}

// withCharacters marks a request as sending n characters of text for speech
// (what ElevenLabs bills); the log records the count, never the text.
func withCharacters(ctx context.Context, n int) context.Context {
	return context.WithValue(ctx, charsKey{}, n)
}

// elevenCall describes an ElevenLabs request for the activity log: what it
// does in plain words and its address without the query (search words stay
// out). The key and the bodies are never part of it.
func elevenCall(ctx context.Context, method, path string) logbook.ProviderCall {
	endpoint, _, _ := strings.Cut(path, "?")
	c := logbook.ProviderCall{Provider: ElevenLabsName, Title: "ElevenLabs", Method: method, Endpoint: endpoint, What: elevenWhat(method, endpoint)}
	c.Characters, _ = ctx.Value(charsKey{}).(int)
	return c
}

func elevenWhat(method, endpoint string) string {
	switch {
	case strings.HasPrefix(endpoint, "/v1/text-to-speech/"):
		return "make speech"
	case endpoint == "/v1/user/subscription":
		return "check the plan"
	case endpoint == "/v2/voices" || endpoint == "/v1/voices":
		return "list voices"
	case endpoint == "/v1/shared-voices":
		return "search the voice library"
	case strings.HasPrefix(endpoint, "/v1/voices/add/"):
		return "add a library voice"
	case endpoint == "/v1/voices/add":
		return "copy a voice"
	case endpoint == "/v1/models":
		return "list models"
	case endpoint == "/v1/audio-isolation":
		return "make a voice clearer"
	case method == http.MethodGet:
		return "read " + strings.Trim(endpoint, "/")
	}
	return "send a request"
}
