package generate

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Prices are said the way people read them (D109): never "$0.00" for a cent's
// worth, whole dollars without cents from $100 up.
func TestMoneyIsSaidAsPeopleReadPrices(t *testing.T) {
	for _, c := range []struct {
		usd  float64
		want string
	}{{0, "$0"}, {0.004, "less than a cent"}, {0.01, "$0.01"}, {0.61, "$0.61"}, {9.999, "$10.00"}, {100, "$100"}, {250.5, "$250.50"}, {400, "$400"}} {
		if got := money(c.usd); got != c.want {
			t.Errorf("money(%v) = %q, want %q", c.usd, got, c.want)
		}
	}
}

// The ledgers count micro-dollars; what people see is rounded to a tenth of a cent.
func TestLedgerArithmetic(t *testing.T) {
	if micros(1.5) != 1_500_000 || micros(0) != 0 || micros(0.0000004) != 0 {
		t.Fatalf("micros: %d %d %d", micros(1.5), micros(0), micros(0.0000004))
	}
	if dollars(1_234_567) != 1.235 || dollars(0) != 0 {
		t.Fatalf("dollars: %v", dollars(1_234_567))
	}
	// The day is UTC's, wherever the machine is.
	if d := dayOf(time.Date(2026, 10, 23, 23, 30, 0, 0, time.FixedZone("west", -5*3600))); d != "2026-10-24" {
		t.Fatalf("day %s", d)
	}
	if id := ledgerID("ws_1", "day", "2026-10-23"); id != "ws_1:day:2026-10-23" {
		t.Fatalf("ledger id %s", id)
	}
	if roundCents(12.345) != 12.35 || roundCents(25.004) != 25 || roundMoney(1.2345) != 1.235 {
		t.Fatalf("rounding: %v %v %v", roundCents(12.345), roundCents(25.004), roundMoney(1.2345))
	}
}

// A generation is named in a few words, and its state said in a sentence,
// for the list, the bell and the activity bar.
func TestGenerationsAreNamedAndSaidPlainly(t *testing.T) {
	shot := func(op string, s int) *models.Generation {
		return &models.Generation{Operation: op, Params: models.GenParams{Seconds: s}, Provider: videogen.GeminiName, ProviderTitle: "Google Gemini"}
	}
	for _, c := range []struct {
		g    *models.Generation
		want string
	}{
		{shot(models.GenShot, 6), "a 6-second shot"},
		{shot(models.GenAgain, 4), "a 4-second shot"},
		{shot(models.GenVariation, 10), "a 10-second shot"},
		{shot(models.GenExtend, 6), "6 more seconds of a clip"},
		{shot(models.GenEdit, 4), "a changed clip"},
		{shot(models.GenLookTest, 3), "a test shot of a look"},
	} {
		if got := what(c.g); got != c.want {
			t.Errorf("what(%s) = %q, want %q", c.g.Operation, got, c.want)
		}
	}

	g := shot(models.GenShot, 6)
	clips := []GenClip{{Title: "Pond", Duration: 6}}
	for _, c := range []struct {
		state   string
		clips   []GenClip
		outcome *models.GenOutcome
		want    string
	}{
		{StateWaiting, nil, nil, "A 6-second shot: waiting to be sent to Google Gemini."},
		{StateSending, nil, nil, "A 6-second shot: being sent to Google Gemini."},
		{StateMaking, nil, nil, "A 6-second shot: Google Gemini is making it."},
		{StateDone, nil, &models.GenOutcome{State: StateDone}, "A 6-second shot is ready."},
		{StateDone, clips, &models.GenOutcome{State: StateDone}, "Ready: “Pond” (6.0 seconds)."},
		{StateDone, []GenClip{{Title: "Pond", Duration: 3}}, &models.GenOutcome{State: StateDone, Partial: true}, "Ready: “Pond” (3.0 seconds). It came back shorter than asked."},
		{StateRefused, nil, &models.GenOutcome{State: StateRefused}, "Google Gemini declined a 6-second shot: its safety filters blocked it."},
		{StateCanceled, nil, &models.GenOutcome{State: StateCanceled}, "A 6-second shot was stopped."},
		{StateFailed, nil, &models.GenOutcome{State: StateFailed, Message: "Google Gemini couldn't make this video."}, "Google Gemini couldn't make this video."},
		{StateFailed, nil, &models.GenOutcome{State: StateFailed}, "A 6-second shot couldn't be made."},
	} {
		gg := *g
		gg.Outcome = c.outcome
		if got := summaryOf(&gg, c.state, c.clips); got != c.want {
			t.Errorf("summaryOf(%s) = %q, want %q", c.state, got, c.want)
		}
	}
	// Without a title, the provider's name.
	if providerTitle(&models.Generation{Provider: "gemini"}) != "gemini" {
		t.Fatal("provider title")
	}
}

// The article follows how the number is said: "an 8-second shot", "an
// 11-second shot", "an 18-second shot" (plainwords.AOrAn, the rule every
// surface uses), in the name and in the sentences built from it.
func TestWhatSaysAnBeforeEightAndEleven(t *testing.T) {
	for _, s := range []int{8, 11, 18} {
		g := &models.Generation{Operation: models.GenShot, Params: models.GenParams{Seconds: s}}
		if got, want := what(g), fmt.Sprintf("an %d-second shot", s); got != want {
			t.Errorf("what(%d s) = %q, want %q", s, got, want)
		}
	}
	g := &models.Generation{Operation: models.GenAgain, Params: models.GenParams{Seconds: 8}, ProviderTitle: "Google Gemini"}
	if got := summaryOf(g, StateMaking, nil); got != "An 8-second shot: Google Gemini is making it." {
		t.Errorf("summary: %q", got)
	}
}

// What a person reads about a generation's state follows its record first,
// then its job.
func TestStateFollowsTheRecordThenTheJob(t *testing.T) {
	now := time.Now()
	for _, c := range []struct {
		name string
		g    models.Generation
		j    *models.Job
		want string
	}{
		{"ended", models.Generation{Outcome: &models.GenOutcome{State: models.GenRefused}, Ticket: &models.GenTicket{ID: "x"}}, &models.Job{State: models.JobFailed}, StateRefused},
		{"its job was stopped", models.Generation{}, &models.Job{State: models.JobCanceled}, StateCanceled},
		{"its job failed", models.Generation{Ticket: &models.GenTicket{ID: "x"}}, &models.Job{State: models.JobFailed}, StateFailed},
		{"accepted", models.Generation{Ticket: &models.GenTicket{ID: "x"}}, &models.Job{State: models.JobRunning}, StateMaking},
		{"a send began", models.Generation{Sending: []time.Time{now}}, &models.Job{State: models.JobRunning}, StateSending},
		{"a send answered", models.Generation{Sending: []time.Time{now}, Sends: []models.GenSend{{Result: "rejected"}}}, nil, StateWaiting},
		{"nothing yet", models.Generation{}, nil, StateWaiting},
	} {
		if got := stateOf(&c.g, c.j); got != c.want {
			t.Errorf("%s: %s, want %s", c.name, got, c.want)
		}
	}
}

// Waits and lengths in words.
func TestWaitsAndLengthsInWords(t *testing.T) {
	for _, c := range []struct {
		d    time.Duration
		want string
	}{{30 * time.Second, "a minute"}, {89 * time.Second, "a minute"}, {90 * time.Second, "2 minutes"}, {10 * time.Minute, "10 minutes"}} {
		if got := minutesWords(c.d); got != c.want {
			t.Errorf("minutesWords(%s) = %q, want %q", c.d, got, c.want)
		}
	}
	for _, c := range []struct {
		s    float64
		want string
	}{{3.04, "3.0 seconds"}, {9.94, "9.9 seconds"}, {9.96, "10 seconds"}, {12.4, "12 seconds"}} {
		if got := secondsWords(c.s); got != c.want {
			t.Errorf("secondsWords(%v) = %q, want %q", c.s, got, c.want)
		}
	}
	for _, c := range []struct {
		s    float64
		want string
	}{{0, "0:00.0"}, {4, "0:04.0"}, {65.5, "1:05.5"}, {600, "10:00.0"}} {
		if got := clock(c.s); got != c.want {
			t.Errorf("clock(%v) = %q, want %q", c.s, got, c.want)
		}
	}
}

// A generated clip's file name never carries a path or a line break, whatever
// the prompt says, and is never empty.
func TestGeneratedFileNamesAreSafe(t *testing.T) {
	for _, c := range []struct{ title, want string }{
		{"a red boat", "a red boat.mp4"},
		{"../../etc/passwd", "..-..-etc-passwd.mp4"},
		{`C:\boats\red`, "C--boats-red.mp4"},
		{"line one\nline two\rthree", "line one line two three.mp4"},
		{"nul\x00here", "nulhere.mp4"},
		{"   ", "generated.mp4"},
		{"", "generated.mp4"},
		{strings.Repeat("é", 100), strings.Repeat("é", 80) + ".mp4"},
	} {
		got := fileName(c.title)
		if got != c.want || strings.ContainsAny(got, `/\:`+"\n\r\x00") {
			t.Errorf("fileName(%q) = %q, want %q", c.title, got, c.want)
		}
	}
}

// New clips are titled after the prompt, with the take and the part when there are several.
func TestNewClipsTitles(t *testing.T) {
	r := &runner{g: &models.Generation{Prompt: "a red   paper boat\non a pond", Operation: models.GenShot, Take: 1}}
	if got := r.title(0, 1); got != "a red paper boat on a pond" {
		t.Fatalf("title %q", got)
	}
	r.g.Take = 2
	if got := r.title(1, 2); got != "a red paper boat on a pond (take 2) (2 of 2)" {
		t.Fatalf("title %q", got)
	}
	r.g = &models.Generation{Prompt: "goes on", Operation: models.GenExtend, Take: 1, Template: &models.GenTemplate{Vars: map[string]string{"title": "Pond (continued)"}}}
	if got := r.title(0, 1); got != "Pond (continued)" {
		t.Fatalf("an extension keeps its clip's name: %q", got)
	}
	r.g.Template = nil
	if got := r.title(0, 1); got != "goes on" {
		t.Fatalf("an extension without a recorded title: %q", got)
	}
}

// What a generation cost: the provider's usage priced with the generation's
// table; without usage, the estimate (when it may have been made) or nothing.
func TestCostFallsBackPlainly(t *testing.T) {
	r := &runner{g: &models.Generation{Provider: videogen.GeminiName, Model: "gemini-omni-1.1-flash", CreatedAt: time.Now().UTC(), Estimate: models.GenCost{USD: 0.42}}}
	if c := r.cost(videogen.Usage{}, "assumed"); c.Basis != "assumed" || c.USD != 0.42 {
		t.Fatalf("no usage, maybe made: %+v", c)
	}
	if c := r.cost(videogen.Usage{}, "none"); c.Basis != "none" || c.USD != 0 {
		t.Fatalf("no usage, not made: %+v", c)
	}
	if c := r.cost(videogen.Usage{Known: true, VideoTokens: 3 * 1931}, "none"); c.Basis != "usage" || c.USD <= 0 || c.VideoTokens != 3*1931 {
		t.Fatalf("usage: %+v", c)
	}
	r.g.Model = "a-model-without-prices"
	if c := r.cost(videogen.Usage{Known: true, VideoTokens: 100}, "none"); c.Basis != "assumed" || c.USD != 0.42 {
		t.Fatalf("usage without a price table: %+v", c)
	}
}

// Progress says another provider's name in place of Gemini's.
func TestProgressNamesTheProvider(t *testing.T) {
	var got models.Progress
	r := &runner{g: &models.Generation{ProviderTitle: "Another Video Service"}, report: func(p models.Progress) { got = p }}
	r.say(StageMaking, 0.5)
	if got.Message != "Another Video Service is making it" || got.Stage != StageMaking || got.Pct != 0.5 {
		t.Fatalf("progress %+v", got)
	}
	r.g.ProviderTitle = videogen.GeminiTitle
	r.say(StageDownloading, 0.8)
	if got.Message != "Downloading the video" {
		t.Fatalf("progress %+v", got)
	}
	// Nothing to say without a listener or a ticket.
	(&runner{g: &models.Generation{}}).say(StageSending, 0)
	r.g.Ticket = nil
	got = models.Progress{}
	r.sayLimited(time.Now())
	if got.Message != "" {
		t.Fatalf("limited without a ticket: %+v", got)
	}
	r.g.Ticket = &models.GenTicket{ID: "x", SubmittedAt: time.Now()}
	r.sayLimited(time.Now())
	if !strings.Contains(got.Message, "too busy with this key's requests") {
		t.Fatalf("limited: %+v", got)
	}
}

func TestSmallHelpers(t *testing.T) {
	if (&Wait{After: time.Minute, Reason: "busy"}).Error() != "busy" {
		t.Fatal("wait")
	}
	f := &Final{Err: io.EOF}
	if f.Error() != "EOF" || !errors.Is(f, io.EOF) {
		t.Fatal("final")
	}
	for _, c := range []struct {
		s    string
		want int
	}{{"boat@abc123", 4}, {"boat", -1}, {"a@b@c", 3}, {"", -1}} {
		if got := lastAt(c.s); got != c.want {
			t.Errorf("lastAt(%q) = %d", c.s, got)
		}
	}
	for _, c := range []struct{ in, want string }{{"Ada", "Ada"}, {"Ada Lovelace", "'Ada Lovelace'"}, {"O'Brien", `'O'\''Brien'`}, {`say "hi"`, `'say "hi"'`}} {
		if got := quoteArg(c.in); got != c.want {
			t.Errorf("quoteArg(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	for _, c := range []struct{ in, want string }{{"Captain Blue!", "captain-blue"}, {"  Theo  ", "theo"}, {"!!!", ""},
		{strings.Repeat("abc ", 15), strings.TrimSuffix(strings.Repeat("abc-", 10), "-")}} {
		if got := lookName(c.in); got != c.want {
			t.Errorf("lookName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
	if possessive("Jess", false) != "Jess'" || possessive("Ada", false) != "Ada's" || possessive("Ada", true) != "your" {
		t.Fatal("possessive")
	}
	now := time.Now()
	past, future := now.Add(-time.Hour), now.Add(time.Hour)
	for _, c := range []struct {
		c    models.Consent
		want string
	}{{models.Consent{RevokedAt: &past, ExpiresAt: &future}, "revoked"}, {models.Consent{ExpiresAt: &past}, "expired"}, {models.Consent{ExpiresAt: &now}, "expired"},
		{models.Consent{ExpiresAt: &future}, "active"}, {models.Consent{}, "active"}} {
		if got := consentStatus(&c.c, now); got != c.want {
			t.Errorf("consentStatus(%+v) = %s, want %s", c.c, got, c.want)
		}
	}
	for _, c := range []struct {
		via, origin, want string
	}{{"dashboard", "assistant:cnv_1", "claude-panel"}, {"", "", "cli"}, {"mcp", "", "mcp"}, {"dashboard", "", "dashboard"}} {
		if got := channelOf(models.Actor{Via: c.via}, c.origin); got != c.want {
			t.Errorf("channelOf(%q, %q) = %q, want %q", c.via, c.origin, got, c.want)
		}
	}
	if got := firstLine("  first\r\nsecond"); got != "first" {
		t.Fatalf("firstLine %q", got)
	}
	if got := firstLine(strings.Repeat("x", 700)); got != strings.Repeat("x", 600)+"…" {
		t.Fatalf("firstLine of a long line: %d", len(got))
	}
	if safetyModel(nil) != nil {
		t.Fatal("no safety")
	}
	if s := safetyModel(&videogen.Safety{Blocked: true, Side: "input", SupportCodes: []string{"58061214"}, Category: "celebrity"}); s == nil || !s.Blocked || s.Category != "celebrity" ||
		len(s.SupportCodes) != 1 {
		t.Fatalf("safety %+v", s)
	}
	for _, c := range []struct {
		l    models.GenLookRef
		want string
	}{{models.GenLookRef{Subject: models.LookPerson, PersonID: "per_1"}, "/people?person=per_1"}, {models.GenLookRef{Subject: models.LookCharacter}, "/settings/integrations#looks"},
		{models.GenLookRef{Subject: models.LookPerson}, "/settings/integrations#looks"}} {
		if got := lookLink(&c.l); got != c.want {
			t.Errorf("lookLink(%+v) = %q, want %q", c.l, got, c.want)
		}
	}
	if lookName2(nil) != "" || lookName2(&models.GenLookRef{Name: "ada", Version: "abc"}) != "ada@abc" {
		t.Fatal("look name")
	}
	if projectAspect(&models.Project{Brief: models.Brief{Platforms: []string{"YouTube", "Reels"}}}) != "9:16" || projectAspect(&models.Project{Brief: models.Brief{Platforms: []string{"youtube"}}}) != "" ||
		projectAspect(&models.Project{}) != "" {
		t.Fatal("project aspect")
	}
	if shapeWords("9:16") != "tall" || shapeWords("16:9") != "wide" || shapeWords("") != "wide" {
		t.Fatal("shape words")
	}
	if qualityWords(models.GenParams{Quality: "final", Resolution: "4k"}) != "final quality, 4K" || qualityWords(models.GenParams{Quality: "draft", Resolution: "720p"}) != "draft, 720p" {
		t.Fatalf("quality words %q", qualityWords(models.GenParams{Quality: "final", Resolution: "4k"}))
	}
	tall := &models.Asset{Media: models.MediaInfo{Width: 1080, Height: 1920}}
	wide := &models.Asset{Media: models.MediaInfo{Width: 1920, Height: 1080}}
	if aspectOf(tall, "") != "9:16" || aspectOf(wide, "") != "16:9" || aspectOf(wide, "9:16") != "9:16" {
		t.Fatal("aspect of a clip")
	}
	if firstInt(0, 0, 5) != 5 || firstInt(0, 0) != 0 || firstInt(-1, 3) != 3 {
		t.Fatal("firstInt")
	}
	if firstNonEmpty("", "  ", " x ") != "x" || firstNonEmpty() != "" {
		t.Fatal("firstNonEmpty")
	}
	if upperFirst("") != "" || upperFirst("boat") != "Boat" || plural(1, "take") != "1 take" || plural(3, "take") != "3 takes" {
		t.Fatal("words")
	}
	if clip("a  b\n c", 100) != "a b c" || clip("abcdef", 4) != "abc…" {
		t.Fatalf("clip %q", clip("abcdef", 4))
	}
}
