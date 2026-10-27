package costs

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// The dashboard says amounts the same way (web/scripts/costs-test.mjs reads
// the same cases).
func TestMoneySharedWithTheDashboard(t *testing.T) {
	var f struct {
		Cases []struct {
			USD   float64 `json:"usd"`
			Money string  `json:"money"`
		} `json:"cases"`
	}
	b, err := os.ReadFile("testdata/money.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &f); err != nil || len(f.Cases) == 0 {
		t.Fatalf("cases: %v", err)
	}
	for _, c := range f.Cases {
		if got := Money(c.USD); got != c.Money {
			t.Errorf("Money(%v) = %q, want %q", c.USD, got, c.Money)
		}
	}
}

func TestMoneyAndAbout(t *testing.T) {
	for _, c := range []struct {
		usd         float64
		money, more string
	}{
		{0, "$0", "nothing"},
		{0.004, "less than a cent", "less than a cent"},
		{0.009, "less than a cent", "less than a cent"},
		{0.01, "$0.01", "about $0.01"},
		{0.399, "$0.40", "about $0.40"},
		{1.8, "$1.80", "about $1.80"},
		{12.5, "$12.50", "about $12.50"},
		{120, "$120", "about $120"},
		{1234.999, "$1,235", "about $1,235"},
		{1234.56, "$1,234.56", "about $1,234.56"},
	} {
		if got := Money(c.usd); got != c.money {
			t.Errorf("Money(%v) = %q, want %q", c.usd, got, c.money)
		}
		if got := About(c.usd); got != c.more {
			t.Errorf("About(%v) = %q, want %q", c.usd, got, c.more)
		}
	}
}

func TestCountAndDay(t *testing.T) {
	for _, c := range []struct {
		n    float64
		want string
	}{{1, "1 credit"}, {0, "0 credits"}, {1240, "1,240 credits"}, {1.5, "1.5 credits"}, {12345.6, "12,346 credits"}} {
		if got := Count(c.n, "credit", "credits"); got != c.want {
			t.Errorf("Count(%v) = %q, want %q", c.n, got, c.want)
		}
	}
	if got := Day("2026-10-23"); got != "23 Oct 2026" {
		t.Errorf("Day = %q", got)
	}
	if got := Day("soon"); got != "soon" {
		t.Errorf("Day keeps what isn't a date: %q", got)
	}
}

// Parts add up to the total, made-before parts cost nothing, and a run adds
// its steps' estimates up under their names.
func TestCostAddsUp(t *testing.T) {
	voice := Of(
		Part{Service: "ElevenLabs", What: "the AI voice", Model: "Eleven Multilingual v2", Source: "Line 1: “Hello”", Amount: 5, Unit: "credits", Units: "5 characters (5 credits)", USD: 0.0004},
		Part{Service: "ElevenLabs", What: "the AI voice", Source: "Line 2: “Again”", Amount: 5, Unit: "credits", USD: 0.0004, Free: true},
	)
	if voice.USD != 0 || voice.About != "less than a cent" || voice.Parts[0].USD != 0.0004 || voice.Parts[1].USD != 0 {
		t.Fatalf("a part under a tenth of a cent is still less than a cent (not nothing), a made-before one is free: %+v", voice)
	}
	shot := Of(
		Part{Service: "Google Gemini", What: "a generated shot", Model: "Gemini Omni 1.1 Flash", Amount: 6, Unit: "seconds of video", USD: 0.6082},
		Part{Service: "Google Gemini", What: "a generated shot", Amount: 1200, Unit: "tokens", USD: 0.0018},
	)
	if shot.USD != 0.61 || shot.About != "about $0.61" {
		t.Fatalf("the total is the parts' sum: %+v", shot)
	}
	run := Of()
	run.Add("Step 2: the AI voice", Of(Part{Service: "ElevenLabs", What: "the AI voice", USD: 0.12}))
	run.Add("Step 4: the opening shot", shot)
	run.Note("Budgets still decide what may be spent.")
	run.Note("Budgets still decide what may be spent.")
	if run.USD != 0.73 || len(run.Parts) != 3 || run.Parts[0].Step != "Step 2: the AI voice" || run.Parts[2].Step != "Step 4: the opening shot" || len(run.Notes) != 1 {
		t.Fatalf("a run's estimate: %+v", run)
	}
	if got := run.Summary(); got != "About $0.73 for the AI voice (ElevenLabs) and a generated shot (Google Gemini)." {
		t.Fatalf("summary: %q", got)
	}
	if got := strings.Join(run.Services(), ", "); got != "ElevenLabs, Google Gemini" {
		t.Fatalf("services: %q", got)
	}
	if c := shot.Total(0.62); c.USD != 0.62 || c.About != "about $0.62" || len(c.Parts) != 2 {
		t.Fatalf("a service's own total: %+v", c)
	}
	free := Of(Part{Service: "ElevenLabs", What: "the AI voice", USD: 0.2, Free: true})
	if !free.AllFree() || free.About != "nothing (made before)" || free.Summary() != "Nothing (made before) for the AI voice (ElevenLabs)." {
		t.Fatalf("all made before: %+v %q", free, free.Summary())
	}
	if Of().Summary() != "Nothing is asked of an outside service." || !Of().Empty() {
		t.Fatal("an empty estimate")
	}
}

// The JSON a card and the dashboard read: parts is always a list.
func TestCostJSON(t *testing.T) {
	b, _ := json.Marshal(Of())
	if string(b) != `{"usd":0,"about":"nothing","parts":[]}` {
		t.Fatalf("empty estimate as JSON: %s", b)
	}
}
