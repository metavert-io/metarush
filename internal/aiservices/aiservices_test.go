package aiservices_test

import (
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
)

var day = time.Date(2026, 10, 25, 12, 0, 0, 0, time.UTC)

// The catalogue is data people edit by hand: every model has a kind MetaRush
// knows, a price in force, licence terms with a date, and every default names
// a model of its kind.
func TestCatalogueIsWhole(t *testing.T) {
	c := aiservices.LoadCatalog()
	if len(c.Providers) < 2 {
		t.Fatalf("providers: %d", len(c.Providers))
	}
	for _, p := range c.Providers {
		if p.Title == "" || p.Data.Summary == "" || p.Data.Checked == "" {
			t.Errorf("%s: provider entry incomplete", p.Name)
		}
		switch p.Route {
		case aiservices.RouteDirect:
			if p.Key == "" || p.KeyHost == "" {
				t.Errorf("%s: a provider elsewhere names its key and the only host it goes to", p.Name)
			}
		case aiservices.RouteLocal:
			// Models on this computer: no key, no host, nothing to pay (D278).
			if p.Key != "" || p.KeyHost != "" {
				t.Errorf("%s: a provider on this computer names a key or a host", p.Name)
			}
			for _, pr := range p.Prices {
				if pr.USD != 0 {
					t.Errorf("%s: %s costs something on this computer", p.Name, pr.Model)
				}
			}
		default:
			t.Errorf("%s: only direct and local adapters are built (D224, D278), route %q", p.Name, p.Route)
		}
		seen := map[string]bool{}
		for _, m := range p.Models {
			key := m.Kind + "/" + m.ID
			if seen[key] {
				t.Errorf("%s: %s twice", p.Name, key)
			}
			seen[key] = true
			if _, err := aiservices.KindOf(m.Kind); err != nil {
				t.Errorf("%s/%s: %v", p.Name, m.ID, err)
			}
			if m.Rank < 1 || m.Rank > 5 {
				t.Errorf("%s/%s: rank %d", p.Name, m.ID, m.Rank)
			}
			if m.Licence.Summary == "" || m.Licence.Checked == "" || m.Licence.Source == "" {
				t.Errorf("%s/%s: licence terms incomplete", p.Name, m.ID)
			}
			if _, err := aiservices.PriceAt(p.Name, m.Kind, m.ID, day); err != nil {
				t.Errorf("%s/%s: no price in force: %v", p.Name, m.ID, err)
			}
			if m.Provider != p.Name || m.ProviderTitle != p.Title {
				t.Errorf("%s/%s: provider not filled in", p.Name, m.ID)
			}
		}
		for _, pr := range p.Prices {
			if !seen[pr.Kind+"/"+pr.Model] {
				t.Errorf("%s: a price for an unknown model %s/%s", p.Name, pr.Kind, pr.Model)
			}
			if pr.Checked == "" || pr.Source == "" {
				t.Errorf("%s: the price of %s says neither when nor where it was read", p.Name, pr.Model)
			}
		}
	}
	for _, k := range aiservices.Kinds() {
		if k.Kind == aiservices.KindVideo {
			if len(aiservices.ModelsOf(k.Kind)) == 0 {
				t.Error("video's models aren't listed")
			}
			continue
		}
		ref := aiservices.DefaultRef(k.Kind)
		if ref == "" {
			t.Errorf("no default for %s", k.Kind)
			continue
		}
		m, err := aiservices.FindModel(k.Kind, "")
		if err != nil || m.Ref() != ref {
			t.Errorf("%s: default %s: %v", k.Kind, ref, err)
		}
	}
}

func near(a, b float64) bool { return math.Abs(a-b) < 0.0005 }

func TestEstimatesFromTheCatalogue(t *testing.T) {
	cases := []struct {
		name, provider, kind, model string
		req                         aiservices.Request
		usd                         float64
		units                       string
	}{
		{"music by the minute", "elevenlabs", aiservices.KindMusic, "music_v2", aiservices.Request{Prompt: "calm guitar", Options: aiservices.Options{Seconds: 30}}, 0.075, "30 seconds of sound"},
		{"a song whatever its length", "gemini", aiservices.KindMusic, "lyria-3.5", aiservices.Request{Prompt: "calm guitar", Options: aiservices.Options{Seconds: 120}}, 0.08, "1 song"},
		{"a picture by its size, with the words sent", "gemini", aiservices.KindPicture, "gemini-nano-banana-2.1", aiservices.Request{Prompt: strings.Repeat("a", 400), Options: aiservices.Options{Size: "2K"}}, 0.0504 + 100*1.5/1e6, "1 picture at 2K"},
		{"speech by characters", "elevenlabs", aiservices.KindVoice, "eleven_flash_v2_5", aiservices.Request{Prompt: strings.Repeat("x", 2500)}, 0.1, "2,500 characters"},
		{"dubbing by the minute heard", "elevenlabs", aiservices.KindDubbing, "dubbing_v1", aiservices.Request{Inputs: []aiservices.Media{{Role: aiservices.RoleMedia, Seconds: 90}}}, 0.75, "90 seconds heard"},
		{"transcription by the hour", "elevenlabs", aiservices.KindTranscription, "scribe_v2", aiservices.Request{Inputs: []aiservices.Media{{Role: aiservices.RoleAudio, Seconds: 1800}}}, 0.11, "30 minutes heard"},
		{"a voice copy costs nothing more", "elevenlabs", aiservices.KindVoiceCopy, "instant", aiservices.Request{}, 0, "1 request"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, err := aiservices.ModelFor(c.provider, c.kind, c.model)
			if err != nil {
				t.Fatal(err)
			}
			c.req.Kind, c.req.Model = c.kind, m.ID
			e, err := aiservices.EstimateFor(c.provider, m, &c.req, day)
			if err != nil {
				t.Fatal(err)
			}
			if !near(e.USD, c.usd) {
				t.Errorf("usd %v, want %v (%+v)", e.USD, c.usd, e)
			}
			if c.units != "" && e.Units != c.units {
				t.Errorf("units %q, want %q", e.Units, c.units)
			}
			if e.Rate == "" || e.PriceTable == "" || len(e.Parts("“calm guitar”")) == 0 {
				t.Errorf("rate or parts missing: %+v", e)
			}
		})
	}
}

// A dated price table takes over on its day, and the estimate says it is coming.
func TestPricesChangeOnTheirDay(t *testing.T) {
	m, err := aiservices.ModelFor("gemini", aiservices.KindVoice, "gemini-3.8-flash-tts")
	if err != nil {
		t.Fatal(err)
	}
	req := &aiservices.Request{Kind: aiservices.KindVoice, Model: m.ID, Prompt: strings.Repeat("word ", 28)} // 140 characters: 10 seconds
	before, _ := aiservices.EstimateFor("gemini", m, req, day)
	after, _ := aiservices.EstimateFor("gemini", m, req, time.Date(2027, 1, 2, 0, 0, 0, 0, time.UTC))
	if before.PriceTable == after.PriceTable || after.OutputUSD <= before.OutputUSD {
		t.Fatalf("before %+v after %+v", before, after)
	}
	if !strings.Contains(strings.Join(before.Notes, " "), "price changes on 1 Jan 2027") {
		t.Errorf("notes: %v", before.Notes)
	}
}

// A sound effect without a length is priced at the longest the model makes.
func TestUnsetLengthIsPricedAtTheLongest(t *testing.T) {
	m, _ := aiservices.ModelFor("elevenlabs", aiservices.KindSoundEffect, "")
	e, err := aiservices.EstimateFor("elevenlabs", m, &aiservices.Request{Kind: m.Kind, Model: m.ID, Prompt: "a door"}, day)
	if err != nil {
		t.Fatal(err)
	}
	if !near(e.USD, 0.06) || len(e.Notes) == 0 {
		t.Errorf("%+v", e)
	}
}

func TestChecksSayWhyInPlainWords(t *testing.T) {
	m, _ := aiservices.ModelFor("elevenlabs", aiservices.KindSoundEffect, "")
	for _, c := range []struct {
		req  aiservices.Request
		want string
	}{
		{aiservices.Request{Kind: aiservices.KindSoundEffect}, "Say what to make"},
		{aiservices.Request{Kind: aiservices.KindSoundEffect, Prompt: "door", Options: aiservices.Options{Seconds: 45}}, "0.5 seconds to 30 seconds"},
		{aiservices.Request{Kind: aiservices.KindMusic, Prompt: "x"}, "doesn't do music"},
	} {
		err := m.Check(&c.req, day)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%+v: %v, want %q", c.req, err, c.want)
		}
		if apperr.From(err).Code != apperr.InvalidInput {
			t.Errorf("code %s", apperr.From(err).Code)
		}
	}
	retired := *m
	retired.RetiredOn = "2026-10-01"
	if err := retired.Check(&aiservices.Request{Kind: aiservices.KindSoundEffect, Prompt: "door"}, day); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Errorf("retired: %v", err)
	}
}

// The estimate offers the cheapest model that costs less and the best ranked
// one that ranks higher; a forbidden provider is never offered.
func TestAlternativesCheaperAndBetter(t *testing.T) {
	m, _ := aiservices.ModelFor("elevenlabs", aiservices.KindVoice, "eleven_multilingual_v2")
	req := &aiservices.Request{Kind: aiservices.KindVoice, Model: m.ID, Prompt: strings.Repeat("hello there ", 50)}
	e, _ := aiservices.EstimateFor("elevenlabs", m, req, day)
	alts := aiservices.Alternatives(m, req, e.USD, day, nil)
	var why []string
	for _, a := range alts {
		why = append(why, a.Why+":"+a.Provider+"/"+a.Model)
		if a.Why == "cheaper" && a.USD >= e.USD {
			t.Errorf("cheaper costs more: %+v", a)
		}
		if a.Why == "better" && a.Rank <= m.Rank {
			t.Errorf("better ranks lower: %+v", a)
		}
	}
	if len(alts) != 2 {
		t.Fatalf("alternatives %v", why)
	}
	only := aiservices.Alternatives(m, req, e.USD, day, func(p string) bool { return p == "elevenlabs" })
	for _, a := range only {
		if a.Provider != "elevenlabs" {
			t.Errorf("a forbidden provider was offered: %+v", a)
		}
	}
}

func TestModelLookups(t *testing.T) {
	if _, err := aiservices.ModelFor("gemini", aiservices.KindVoiceCopy, ""); err != nil {
		t.Errorf("gemini voice copy: %v", err)
	}
	if m, err := aiservices.FindModel(aiservices.KindMusic, "elevenlabs/music_v2_5"); err != nil || m.ID != "music_v2_5" {
		t.Errorf("%v %v", m, err)
	}
	if _, err := aiservices.FindModel(aiservices.KindMusic, "no-such-model"); err == nil {
		t.Error("an unknown model was found")
	}
	_, err := aiservices.ModelFor("gemini", aiservices.KindDubbing, "")
	if err == nil || apperr.From(err).Code != apperr.NotFound {
		t.Errorf("gemini has no dubbing: %v", err)
	}
	if _, err := aiservices.Get("gemini", aiservices.KindDubbing); err == nil {
		t.Error("an adapter that doesn't exist")
	}
	if _, err := aiservices.AddProvider(aiservices.ProviderInfo{Name: "gemini"}); err == nil {
		t.Error("a catalogue provider was replaced")
	}
}

func TestMediaHelpers(t *testing.T) {
	w := aiservices.ToneWAV(1.5, 300)
	if aiservices.Sniff(w) != "audio/wav" || math.Abs(aiservices.WAVSeconds(w)-1.5) > 0.01 {
		t.Errorf("wav: %s %v", aiservices.Sniff(w), aiservices.WAVSeconds(w))
	}
	pcm := make([]byte, 48000)
	if s := aiservices.WAVSeconds(aiservices.PCMToWAV(pcm, aiservices.PCMRate("audio/L16;codec=pcm;rate=24000"), 1)); math.Abs(s-1) > 0.001 {
		t.Errorf("pcm: %v", s)
	}
	if !aiservices.IsRawPCM("audio/L16;rate=24000") || aiservices.IsRawPCM("audio/mpeg") {
		t.Error("raw pcm")
	}
	if aiservices.Sniff([]byte("ID3\x04")) != "audio/mpeg" || aiservices.Sniff([]byte("\x00\x00\x00\x18ftypmp42")) != "video/mp4" || aiservices.Sniff([]byte(`{"a":1}`)) != "application/json" {
		t.Error("sniff")
	}
}

// Every kind's fake passes the conformance suite, so the suite itself is
// exercised before any real adapter relies on it.
func TestFakeAdaptersConform(t *testing.T) {
	f := fakeai.Install(t, "fake-key-for-conformance-1234")
	for _, k := range aiservices.Kinds() {
		if k.Kind == aiservices.KindVideo {
			continue
		}
		t.Run(k.Kind, func(t *testing.T) {
			a, err := aiservices.Get(fakeai.Name, k.Kind)
			if err != nil {
				t.Fatal(err)
			}
			aiconformance.Run(t, aiconformance.Harness{Adapter: a, Key: f.Key, Kept: f.Kept, Problems: f.Problems})
		})
	}
	// The voice copy was made, found by its tag and removed.
	if f.Calls("find-voices") < 2 || f.Calls("remove-voice") != 1 || len(f.Voices()) != 0 {
		t.Errorf("voice copy: found %d times, removed %d times, kept %v", f.Calls("find-voices"), f.Calls("remove-voice"), f.Voices())
	}
	// The presenter was made after its consent request, found by its tag and
	// removed, and the consent request deleted (D273, D277).
	if f.Calls("ask-consent") < 1 || f.Calls("find-presenters") < 2 || f.Calls("remove-presenter") != 2 || len(f.Presenters()) != 0 || len(f.ConsentRequests()) != 0 {
		t.Errorf("presenter: asked %d, found %d times, removed %d times, kept %v, consent requests kept %v", f.Calls("ask-consent"), f.Calls("find-presenters"),
			f.Calls("remove-presenter"), f.Presenters(), f.ConsentRequests())
	}
}

// A model reachable only through a gateway is listed, said plainly, and never
// offered, chosen or sent (D282: direct adapters by default, no gateway built).
func TestGatewayOnlyModelsAreListedNotOffered(t *testing.T) {
	lic := aiservices.Licence{Summary: "Test terms.", Commercial: "yes", Broadcast: "yes", Source: "https://example.com/terms", Checked: "2026-10-25"}
	rm, err := aiservices.AddProvider(aiservices.ProviderInfo{Name: "studio-x", Title: "Studio X", Key: "studio-x", KeyHost: "api.example.com", Route: aiservices.RouteDirect,
		Data: aiservices.DataTerms{Summary: "Test data terms.", Checked: "2026-10-25"},
		Models: []aiservices.Model{{ID: "sx-music", Title: "Studio X Music", Kind: aiservices.KindMusic, Tasks: []string{aiservices.TaskGenerate}, Stage: "ga", Rank: 5,
			Licence: lic, OnlyVia: "a gateway"}},
		Prices: []aiservices.Price{{Model: "sx-music", Kind: aiservices.KindMusic, Effective: "2026-01-01", Checked: "2026-10-25", Source: "https://example.com/prices", Unit: "song", USD: 0.01}}})
	if err != nil {
		t.Fatal(err)
	}
	defer rm()
	m, err := aiservices.FindModel(aiservices.KindMusic, "studio-x/sx-music")
	if err != nil || !m.GatewayOnly() {
		t.Fatalf("listed: %+v %v", m, err)
	}
	if _, err := aiservices.ModelFor("studio-x", aiservices.KindMusic, ""); err == nil {
		t.Error("a gateway-only model became its provider's usual one")
	}
	req := &aiservices.Request{Kind: aiservices.KindMusic, Model: m.ID, Prompt: "calm guitar"}
	err = m.Check(req, day)
	if ae := apperr.From(err); err == nil || ae.Code != apperr.NotConfigured || !strings.Contains(ae.Message, "only reachable through a gateway") || !strings.Contains(ae.Message, "uses no gateway") {
		t.Fatalf("check: %v", err)
	}
	chosen, _ := aiservices.ModelFor("elevenlabs", aiservices.KindMusic, "music_v2")
	for _, alt := range aiservices.Alternatives(chosen, &aiservices.Request{Kind: aiservices.KindMusic, Prompt: "calm guitar", Options: aiservices.Options{Seconds: 30}}, 0.075, day, nil) {
		if alt.Provider == "studio-x" {
			t.Errorf("offered: %+v", alt)
		}
	}
	for _, p := range aiservices.LoadCatalog().Providers {
		for _, x := range p.Models {
			if x.GatewayOnly() && aiservices.DefaultRef(x.Kind) == x.Ref() {
				t.Errorf("%s is a default but only reachable through a gateway", x.Ref())
			}
		}
	}
}

// The schema's list of kinds is the kinds MetaRush knows, each once.
func TestKindsSchemaListsEachKindOnce(t *testing.T) {
	f, _ := reflect.TypeOf(aiservices.KindInfo{}).FieldByName("Kind")
	var listed, known []string
	for _, part := range strings.Split(f.Tag.Get("jsonschema"), ",") {
		if k, ok := strings.CutPrefix(part, "enum="); ok {
			listed = append(listed, k)
		}
	}
	for _, k := range aiservices.Kinds() {
		known = append(known, k.Kind)
	}
	slices.Sort(listed)
	slices.Sort(known)
	if !slices.Equal(listed, known) {
		t.Errorf("schema %v, kinds %v", listed, known)
	}
}
