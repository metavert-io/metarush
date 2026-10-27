package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
)

// D-ID is the first provider of presenters (D273): a person's own presenter
// made from a training take and their recorded consent, reading sound
// MetaRush supplies. Its key goes only in the Authorization header, only to
// D-ID's API host or a loopback test server (DIDBaseURL).

// DIDName is D-ID's provider ID; DIDTitle how people name it.
const (
	DIDName  = "d-id"
	DIDTitle = "D-ID"
)

// CategoryPresenter groups the providers that make presenters (Settings → Integrations).
const CategoryPresenter = "presenter"

// EnvDIDBaseURL overrides D-ID's API address: only D-ID's own host and test
// servers on this computer are accepted, so the key never goes elsewhere.
const EnvDIDBaseURL = "METARUSH_D_ID_BASE_URL"

const didHost = "https://api.d-id.com"

// DIDBaseURL is D-ID's API address (or the override). Under go test and in
// browser runs (RealServicesRefused) only a test server on this computer is
// accepted, so a test that forgets its fake never reaches D-ID.
func DIDBaseURL() (string, error) {
	base := didHost
	if o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvDIDBaseURL)), "/"); o != "" {
		u, err := url.Parse(o)
		if err != nil || u.User != nil || u.RawQuery != "" || (o != didHost && !localElevenBase(o)) {
			return "", apperr.New(apperr.NotConfigured, "%s must be D-ID's API host or a local test server", EnvDIDBaseURL)
		}
		base = o
	}
	if RealServicesRefused() && !localElevenBase(base) {
		return "", apperr.New(apperr.NotConfigured, "Tests never call D-ID: set %s to a fake on this computer.", EnvDIDBaseURL)
	}
	return base, nil
}

// LocalURL reports whether an address is a server on this computer.
func LocalURL(raw string) bool { return localElevenBase(raw) }

// DownloadAllowed reports whether a file a provider pointed to may be
// downloaded (without any key): over HTTPS, or from this computer — and only
// from this computer under go test and in browser runs.
func DownloadAllowed(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil {
		return false
	}
	if localElevenBase(raw) {
		return true
	}
	return u.Scheme == "https" && u.Host != "" && !RealServicesRefused()
}

// DID is D-ID in the integrations registry.
type DID struct{}

func init() { Register(DID{}) }

func (DID) Name() string     { return DIDName }
func (DID) Title() string    { return DIDTitle }
func (DID) Category() string { return CategoryPresenter }
func (DID) Summary() string {
	return "A presenter who reads your lines on camera: a person's own, made from their approved look and a short recording of them, with their recorded consent. It speaks the sound MetaRush gives it, such as the person's own voice copy. You pay D-ID for what it makes, and MetaRush shows the price first."
}
func (DID) KeyHelp() string {
	return "Create one in D-ID's studio under Account settings → API key. It is shown once: copy all of it. Clips for commercial use need a paid D-ID plan."
}
func (DID) Capabilities() []string { return []string{"presenters", "consent-check", "presenter-clips"} }

// Test checks a key by reading the account's credits (free).
func (DID) Test(ctx context.Context, key string) (*TestResult, error) {
	base, err := DIDBaseURL()
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	call, start := logbook.ProviderCall{Provider: DIDName, Title: DIDTitle, What: "checked the key", Method: http.MethodGet, Endpoint: "/credits"}, time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/credits", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Basic "+key) // D-ID's key is sent as issued
	req.Header.Set("Accept", "application/json")
	res, err := (&http.Client{}).Do(req)
	call.Duration = time.Since(start)
	if err != nil {
		logbook.ProviderDone(ctx, call, err)
		return nil, apperr.Wrap(apperr.Provider, err, "MetaRush couldn't reach D-ID. Check the internet connection and try again.")
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	call.Status, call.Bytes = res.StatusCode, len(data)
	logbook.ProviderDone(ctx, call, nil)
	switch {
	case res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden:
		return &TestResult{OK: false, Message: "D-ID did not accept this key. Check that it was copied completely and hasn't been deleted."}, nil
	case res.StatusCode >= 300:
		return nil, apperr.New(apperr.Provider, "D-ID couldn't check the key just now (it answered %d). Try again in a moment.", res.StatusCode)
	}
	var c struct {
		Remaining *float64 `json:"remaining"`
		Total     *float64 `json:"total"`
	}
	_ = json.Unmarshal(data, &c)
	msg := "Connected. D-ID makes presenters with this key; clips for commercial use need a paid plan."
	if c.Remaining != nil && c.Total != nil {
		msg = fmt.Sprintf("Connected. %s of %s D-ID credits left this period; clips for commercial use need a paid plan.", commas(int64(*c.Remaining)), commas(int64(*c.Total)))
	}
	r := &TestResult{OK: true, Message: msg}
	if c.Remaining != nil && c.Total != nil {
		r.Usage = &Usage{Used: int64(*c.Total - *c.Remaining), Limit: int64(*c.Total), Unit: "credits"}
	}
	return r, nil
}
