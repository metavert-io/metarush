package providers

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
)

// HeyGen makes presenter clips of its stock presenters reading the sound
// MetaRush supplies (D273, D275). Its key goes only in the X-Api-Key header,
// only to HeyGen's API host or a loopback test server (HeyGenBaseURL).

// HeyGenName is HeyGen's provider ID; HeyGenTitle how people name it.
const (
	HeyGenName  = "heygen"
	HeyGenTitle = "HeyGen"
)

// EnvHeyGenBaseURL overrides HeyGen's API address: only HeyGen's own host and
// test servers on this computer are accepted.
const EnvHeyGenBaseURL = "METARUSH_HEYGEN_BASE_URL"

const heygenHost = "https://api.heygen.com"

// HeyGenBaseURL is HeyGen's API address (or the override); under go test and
// in browser runs only a test server on this computer.
func HeyGenBaseURL() (string, error) {
	base := heygenHost
	if o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvHeyGenBaseURL)), "/"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.User != nil || u.RawQuery != "" || (o != heygenHost && !localElevenBase(o)) {
			return "", apperr.New(apperr.NotConfigured, "%s must be HeyGen's API host or a local test server", EnvHeyGenBaseURL)
		}
		base = o
	}
	if RealServicesRefused() && !localElevenBase(base) {
		return "", apperr.New(apperr.NotConfigured, "Tests never call HeyGen: set %s to a fake on this computer.", EnvHeyGenBaseURL)
	}
	return base, nil
}

// HeyGen is HeyGen in the integrations registry.
type HeyGen struct{}

func init() { Register(HeyGen{}) }

func (HeyGen) Name() string     { return HeyGenName }
func (HeyGen) Title() string    { return HeyGenTitle }
func (HeyGen) Category() string { return CategoryPresenter }
func (HeyGen) Summary() string {
	return "Stock presenters who read your lines on camera, licensed for commercial use and client work on HeyGen's paid plans. They speak the sound MetaRush gives them. You pay HeyGen for what it makes, and MetaRush shows the price first."
}
func (HeyGen) KeyHelp() string {
	return "Create one in HeyGen's developer platform (platform.heygen.com) under API keys. HeyGen's API is paid from a prepaid balance."
}
func (HeyGen) Capabilities() []string { return []string{"stock-presenters", "presenter-clips"} }

// Test checks a key by reading the account (free).
func (HeyGen) Test(ctx context.Context, key string) (*TestResult, error) {
	base, err := HeyGenBaseURL()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call, start := logbook.ProviderCall{Provider: HeyGenName, Title: HeyGenTitle, What: "checked the key", Method: http.MethodGet, Endpoint: "/v3/users/me"}, time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/v3/users/me", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Api-Key", key)
	req.Header.Set("Accept", "application/json")
	res, err := (&http.Client{}).Do(req)
	call.Duration = time.Since(start)
	if err != nil {
		logbook.ProviderDone(ctx, call, err)
		return nil, apperr.Wrap(apperr.Provider, err, "MetaRush couldn't reach HeyGen. Check the internet connection and try again.")
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	call.Status, call.Bytes = res.StatusCode, len(data)
	logbook.ProviderDone(ctx, call, nil)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return &TestResult{OK: false, Message: "HeyGen did not accept this key. Check that it was copied completely and hasn't been deleted."}, nil
	case res.StatusCode >= 300:
		return nil, apperr.New(apperr.Provider, "HeyGen couldn't check the key just now (it answered %d). Try again in a moment.", res.StatusCode)
	}
	var me struct {
		Data struct {
			BillingType string `json:"billing_type"`
		} `json:"data"`
	}
	_ = json.Unmarshal(data, &me)
	msg := "Connected. HeyGen's stock presenters can read your lines."
	if me.Data.BillingType == "wallet" {
		msg = "Connected, paid from HeyGen's prepaid balance. Its stock presenters can read your lines."
	}
	return &TestResult{OK: true, Message: msg}, nil
}
