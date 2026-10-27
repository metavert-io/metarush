package abilities

import (
	"context"
	"strings"
	"testing"

	_ "github.com/metavert-io/metarush/internal/aiservices/syncso" // the sync. integration, as the binary has it
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakevertex"
)

const keyedManifest = `ability: 1
name: keyed
title: Keyed
does: Calls a service with a key.
runs: python3 run.py
outputs: [picture]
cost: free
keys:
  - {name: KEY, header: Authorization}
hosts: [HOST]
sends: [words]
needs: {programs: [python3]}
licence: MIT
test: {inputs: {}}
`

// noHosts is an integration that doesn't say where its key may go (one
// added later and forgotten): it is never lent to an ability.
type noHosts struct{}

func (noHosts) Name() string           { return "newservice" }
func (noHosts) Title() string          { return "New service" }
func (noHosts) Category() string       { return providers.CategoryVideo }
func (noHosts) Summary() string        { return "A service for tests." }
func (noHosts) KeyHelp() string        { return "Tests set it." }
func (noHosts) Capabilities() []string { return nil }
func (noHosts) Test(context.Context, string) (*providers.TestResult, error) {
	return &providers.TestResult{OK: true}, nil
}

// A key named after one of MetaRush's integrations goes only to that
// integration's own hosts, as the integration declares them; Google Cloud's
// service-account key never goes into a request; an integration that names
// no hosts never lends its key; an ability's own key goes where it says.
func TestIntegrationKeysStayWithTheirService(t *testing.T) {
	providers.Register(noHosts{})
	cases := []struct {
		key, host, want string
	}{
		{"vertex", "api.example.com", "MetaRush's Google Cloud Vertex AI key is a private key"}, // the name keeps its capitals mid-sentence
		{"vertex", "us-central1-aiplatform.googleapis.com", "is a private key"},
		{"sync", "api.example.com", "goes only to its own service, not to api.example.com"},
		{"sync", "api.sync.so", ""},
		{"gemini", "api.example.com", "goes only to its own service"},
		{"elevenlabs", "api.sg.residency.elevenlabs.io", ""},
		{"newservice", "api.example.com", "doesn't know where its New service key may go"},
		{"stock", "api.example.com", ""},
	}
	for _, c := range cases {
		m, err := ParseManifest([]byte(strings.NewReplacer("KEY", c.key, "HOST", c.host).Replace(keyedManifest)))
		if err != nil {
			t.Fatal(err)
		}
		got := strings.Join(problemTexts(m.Validate(map[string]bool{ManifestFile: true, "run.py": true})), "; ")
		if c.want == "" && strings.Contains(got, "keys") {
			t.Errorf("%s to %s: %q", c.key, c.host, got)
		}
		if c.want != "" && !strings.Contains(got, c.want) {
			t.Errorf("%s to %s: %q, want %q", c.key, c.host, got, c.want)
		}
		if strings.Contains(got, "metaRush") {
			t.Errorf("%s to %s: the name lost its capital: %q", c.key, c.host, got)
		}
	}
}

// Checked again when a run starts: a version approved before the rule (or
// whose integration changed since) never gets the private key into a request.
func TestRunRefusesAnIntegrationsKeyElsewhere(t *testing.T) {
	a := newApp(t)
	for _, k := range []models.AbilityKey{{Name: "vertex", Env: "VERTEX_API_KEY", Hosts: []string{"api.example.com"}}, {Name: "sync", Env: "SYNC_API_KEY", Hosts: []string{"api.example.com"}}} {
		f := models.AbilityFacts{Network: NetworkMetaRush, Hosts: k.Hosts, Keys: []models.AbilityKey{k}, Minutes: 1, MB: 1}
		_, msg, _ := sandboxRun(context.Background(), a, &models.AbilityRun{}, &models.AbilityVersion{Facts: f}, nil, nil, nil, nil, nil, func(float64, string) {})
		if !strings.Contains(msg, "private key") && !strings.Contains(msg, "goes only to its own service") {
			t.Errorf("%s: %q", k.Name, msg)
		}
	}
}

// What a computer has shows the service account, never any of its key.
func TestHereShowsTheAccountNotTheKey(t *testing.T) {
	a := newApp(t)
	_, key := fakevertex.Start(t)
	kept, err := providers.NormalizeVertexKey(key)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(providers.EnvVar("vertex"), kept)
	h, _ := hereFor(context.Background(), a, models.AbilityFacts{Keys: []models.AbilityKey{{Name: "vertex"}}}, "keyed", false)
	if len(h.Keys) != 1 || !h.Keys[0].Configured || !strings.Contains(h.Keys[0].Masked, "service account") || strings.Contains(h.Keys[0].Masked, "PRIVATE") {
		t.Fatalf("keys %+v", h.Keys)
	}
}

// What an ability provides says which of MetaRush's services it reaches, by
// their key or their hosts (any, with an open network), so Content Settings
// that rule a service out rule the ability out too (D281).
func TestReachesNamesTheServices(t *testing.T) {
	for _, c := range []struct {
		f    models.AbilityFacts
		want string
	}{
		{models.AbilityFacts{Network: NetworkOff}, ""},
		{models.AbilityFacts{Network: NetworkOpen}, "*"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"api.example.com"}}, ""},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"api.eu.residency.elevenlabs.io"}}, "elevenlabs"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"api.example.com"}, Keys: []models.AbilityKey{{Name: "elevenlabs"}}}, "elevenlabs"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"api.sync.so", "generativelanguage.googleapis.com"}, Keys: []models.AbilityKey{{Name: "sync"}, {Name: "pexels"}}}, "gemini sync"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"us-central1-aiplatform.googleapis.com"}}, "vertex"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"aiplatform.googleapis.com.example.com"}}, ""},
		// A name for many hosts reaches every service among them; a domain
		// above a service's host, and a name under it, reach it too.
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"*.elevenlabs.io"}}, "elevenlabs"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"*.sync.so:443"}}, "sync"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"*.googleapis.com"}}, "gemini vertex"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"googleapis.com"}}, "gemini vertex"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"elevenlabs.io"}}, "elevenlabs"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"eu.residency.elevenlabs.io"}}, "elevenlabs"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"v2.api.sync.so"}}, "sync"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"*.aiplatform.googleapis.com"}}, "vertex"},
		{models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"storage.googleapis.com", "*.example.com", "notelevenlabs.io", "sync.so.example.com"}}, ""},
	} {
		if got := strings.Join(reaches(c.f), " "); got != c.want {
			t.Errorf("%+v: %q, want %q", c.f, got, c.want)
		}
	}
	v := &models.AbilityVersion{Name: "keyed", Title: "Keyed", Facts: models.AbilityFacts{Network: NetworkMetaRush, Hosts: []string{"api.elevenlabs.io"}, Provides: []string{"upscale"}}}
	if p := providerInfo(v); strings.Join(p.Reaches, " ") != "elevenlabs" || p.Stays {
		t.Fatalf("provider %+v", p)
	}
}

// An ability that provides a kind of AI work keeps the starter pack's rules
// (D281, D284, D285): its runs go through the same core, which refuses a
// local service no owner agreed to; a file nobody set up here keeps it from
// being ready; and its data terms name the service on this computer.
func TestProvidingKeepsTheLocalRules(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	l := &models.AbilityLocal{Port: 8188, Service: "ComfyUI"}
	v := &models.AbilityVersion{Name: "comfy", Title: "Comfy", Facts: models.AbilityFacts{Network: NetworkMetaRush, Local: l, Provides: []string{"picture"}, Sends: []string{"words"},
		Minutes: 1, MB: 1}}
	if _, msg, _ := sandboxRun(ctx, a, &models.AbilityRun{}, v, nil, nil, nil, nil, nil, func(float64, string) {}); !strings.Contains(msg, "which no owner agreed to") {
		t.Errorf("a local service nobody agreed to: %q", msg)
	}
	p := providerInfo(v)
	if !strings.Contains(p.Data.Summary, "ComfyUI on this computer (port 8188)") || strings.Contains(p.Data.Summary, "reaches only ,") || p.Stays || len(p.Reaches) != 0 {
		t.Errorf("data terms %+v", p)
	}
	h, _ := hereFor(ctx, a, models.AbilityFacts{Files: []models.AbilityNeededFile{{Name: "model", Says: "The model", Env: FileEnv("model")}}}, "comfy", true)
	if h.Ready {
		t.Error("ready without the file a person sets up here")
	}
}
