package leave

import (
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
)

func rules(places map[string]models.LeavePlace) *models.LeaveRules {
	return &models.LeaveRules{Places: places}
}

// Rules keep kinds from a place, let only some receivers have the rest, or
// keep everything; nothing set keeps today's behaviour.
func TestRefuses(t *testing.T) {
	ai := Send{Place: models.PlaceAI, To: "gemini", Kinds: []string{models.LeaveWords, models.LeaveVoices}}
	if refuses(nil, ai) != nil || refuses(&models.LeaveRules{}, ai) != nil {
		t.Fatal("no rules: anything may leave")
	}
	r := refuses(rules(map[string]models.LeavePlace{models.PlaceAI: {Keep: []string{models.LeaveVoices}}}), ai)
	if r == nil || r.Kind != models.LeaveVoices {
		t.Fatalf("voices kept from AI services: %+v", r)
	}
	if refuses(rules(map[string]models.LeavePlace{models.PlaceClaude: {Keep: []string{models.LeaveVoices}}}), ai) != nil {
		t.Fatal("a rule for another place doesn't count")
	}
	only := rules(map[string]models.LeavePlace{models.PlaceAI: {Only: []string{"elevenlabs"}}})
	if r := refuses(only, ai); r == nil || r.Kind != "" {
		t.Fatalf("only ElevenLabs: %+v", r)
	}
	ai.To = "elevenlabs"
	if refuses(only, ai) != nil {
		t.Fatal("ElevenLabs may receive them")
	}
	ai.AnyReceiver, ai.To = true, ""
	if refuses(only, ai) != nil {
		t.Fatal("who receives it is decided later")
	}
	if r := refuses(&models.LeaveRules{Nothing: true}, Send{Place: models.PlaceStorage, Kinds: []string{models.LeaveFootage}}); r == nil || r.Kind != models.LeaveFootage {
		t.Fatalf("nothing leaves: %+v", r)
	}
}

func TestReceives(t *testing.T) {
	for _, c := range []struct {
		only      []string
		place, to string
		want      bool
	}{
		{[]string{"api.example.com"}, models.PlaceAbilities, "api.example.com", true},
		{[]string{"*.example.com"}, models.PlaceAbilities, "cdn.example.com", true},
		{[]string{"*.example.com"}, models.PlaceAbilities, "example.com", false},
		{[]string{"*.example.com"}, models.PlaceAbilities, "badexample.com", false},
		{[]string{"*.example.com"}, models.PlaceAI, "x.example.com", false},
		{[]string{"commercial"}, models.PlaceClaude, "commercial", true},
		{[]string{"commercial"}, models.PlaceClaude, "", false},
	} {
		if got := Receives(c.only, c.place, c.to); got != c.want {
			t.Errorf("%v %s %s: %v", c.only, c.place, c.to, got)
		}
	}
}

// Letting more leave is listed (an owner's decision); keeping more is not.
func TestLooser(t *testing.T) {
	strict := rules(map[string]models.LeavePlace{models.PlaceAI: {Keep: []string{models.LeaveVoices}, Only: []string{"elevenlabs"}}})
	if l := Looser(strict, strict); len(l) != 0 {
		t.Fatalf("the same: %v", l)
	}
	if l := Looser(nil, strict); len(l) != 0 {
		t.Fatalf("stricter: %v", l)
	}
	if l := Looser(strict, nil); len(l) != 2 {
		t.Fatalf("none at all: %v", l)
	}
	wider := rules(map[string]models.LeavePlace{models.PlaceAI: {Keep: []string{models.LeaveVoices}, Only: []string{"elevenlabs", "gemini"}}})
	if l := Looser(strict, wider); len(l) != 1 || !strings.Contains(strings.ToLower(l[0]), "gemini") {
		t.Fatalf("another receiver: %v", l)
	}
	if l := Looser(&models.LeaveRules{Nothing: true}, strict); len(l) != 1 {
		t.Fatalf("from nothing: %v", l)
	}
	if l := Looser(strict, &models.LeaveRules{Nothing: true}); len(l) != 0 {
		t.Fatalf("to nothing: %v", l)
	}
}

func TestNormalizeAndProblems(t *testing.T) {
	r := &models.LeaveRules{Places: map[string]models.LeavePlace{
		models.PlaceAI:       {Keep: []string{models.LeavePictures, models.LeaveFootage, models.LeaveFootage}, Only: []string{" Gemini ", "gemini"}},
		models.PlaceDelivery: {},
	}, Note: "  the contract "}
	Normalize(r)
	if p := r.Places[models.PlaceAI]; !slices.Equal(p.Keep, []string{models.LeaveFootage, models.LeavePictures}) || !slices.Equal(p.Only, []string{"gemini"}) {
		t.Fatalf("normalized: %+v", p)
	}
	if _, ok := r.Places[models.PlaceDelivery]; ok || r.Note != "the contract" {
		t.Fatalf("empty places dropped, note trimmed: %+v", r)
	}
	for _, bad := range []*models.LeaveRules{
		{Places: map[string]models.LeavePlace{"moon": {Keep: []string{models.LeaveWords}}}},
		{Places: map[string]models.LeavePlace{models.PlaceAI: {Keep: []string{"smells"}}}},
		{Places: map[string]models.LeavePlace{models.PlaceClaude: {Only: []string{"friends"}}}},
		{Places: map[string]models.LeavePlace{models.PlaceStorage: {Only: []string{"x"}}}},
	} {
		if Problems(bad) == "" {
			t.Errorf("no problem found in %+v", bad)
		}
	}
	if s := Summary(rules(map[string]models.LeavePlace{models.PlaceClaude: {Keep: models.LeaveKinds}})); s != "Nothing to Claude" {
		t.Errorf("summary %q", s)
	}
	if s := Summary(nil); s != "Anything MetaRush offers may leave" {
		t.Errorf("summary %q", s)
	}
}

func TestKinds(t *testing.T) {
	if k := Kinds(true, RoleKinds("start-frame", true), RoleKinds("audio", true)); !slices.Equal(k, []string{models.LeaveVoices, models.LeaveFaces, models.LeaveWords, models.LeavePictures}) {
		t.Fatalf("kinds %v", k)
	}
	if k := RoleKinds("picture", false); !slices.Equal(k, []string{models.LeavePictures}) {
		t.Fatalf("a library picture: %v", k)
	}
	if k := Kinds(false); k == nil || len(k) != 0 {
		t.Fatalf("nothing: %#v", k)
	}
}

// Where a workspace keeps things: a bucket elsewhere or a sync folder is
// cloud storage; this computer and the local network are not.
func TestKept(t *testing.T) {
	for _, c := range []struct {
		m     config.Media
		cloud bool
	}{
		{config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: t.TempDir()}}, false},
		{config.Media{Provider: "s3", S3: config.S3{Endpoint: "http://127.0.0.1:9000", Bucket: "m"}}, false},
		{config.Media{Provider: "s3", S3: config.S3{Endpoint: "http://192.168.1.20:9000", Bucket: "m"}}, false},
		{config.Media{Provider: "s3", S3: config.S3{Endpoint: "https://storage.example.com", Bucket: "m"}}, true},
		{config.Media{Provider: "s3", S3: config.S3{Preset: "aws", Bucket: "m"}}, true},
		{config.Media{Provider: "gcs", GCS: config.GCS{Bucket: "m"}}, true},
	} {
		if k := MediaKept(c.m); k.Cloud != c.cloud || k.Words == "" {
			t.Errorf("%+v: %+v", c.m, k)
		}
	}
	for _, c := range []struct {
		d     config.Database
		cloud bool
	}{
		{config.Database{URI: "mongodb://127.0.0.1:27150/?replicaSet=metarush", Local: &config.LocalDatabase{Port: 27150}}, false},
		{config.Database{URI: "mongodb://localhost:27018"}, false},
		{config.Database{URI: "mongodb://10.0.0.5:27017"}, false},
		{config.Database{URI: "mongodb+srv://cluster0.example.net/"}, true},
		{config.Database{URI: "mongodb://db.example.com:27017"}, true},
	} {
		if k := DatabaseKept(c.d); k.Cloud != c.cloud {
			t.Errorf("%+v: %+v", c.d, k)
		}
	}
}
