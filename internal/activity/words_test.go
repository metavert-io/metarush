package activity

import (
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
)

func TestSmallWordHelpers(t *testing.T) {
	now := time.Now()
	if eta(0.02, now.Add(-time.Minute), now) != nil || eta(1, now.Add(-time.Minute), now) != nil || eta(0.5, now.Add(-time.Second), now) != nil {
		t.Fatal("no estimate too early, at the end, or right after starting")
	}
	if e := eta(0.25, now.Add(-30*time.Second), now); e == nil || *e < 89.9 || *e > 90.1 {
		t.Fatalf("eta %v", e)
	}
	if clamp01(-0.5) != 0 || clamp01(1.5) != 1 || clamp01(0.4) != 0.4 {
		t.Fatal("clamp01")
	}
	for in, want := range map[string]string{"": "", "  the disk is full ": "The disk is full.", "Done!": "Done!", "why?": "Why?", "ok.": "Ok."} {
		if got := sentence(in); got != want {
			t.Errorf("sentence(%q) = %q", in, got)
		}
	}
	a, b := now, now.Add(time.Second)
	if !maxTime(a, b).Equal(b) || !maxTime(b, a).Equal(b) {
		t.Fatal("maxTime")
	}
	for _, c := range []struct{ kind, url, want string }{
		{"repo", "https://github.com/acme/widget", "acme/widget from GitHub"},
		{"picture", "https://www.example.com/brand/logo.png", "a picture from example.com"},
		{"page", "https://example.com/pricing/", "a snapshot of example.com/pricing"},
		{"page", "https://example.com/docs/getting-started/install-on-linux", "a snapshot of example.com/docs/getting-started/install-o…"},
		{"page", "not a link", "a link"},
	} {
		if got := fetchWords(c.kind, c.url); got != c.want {
			t.Errorf("fetchWords(%s, %s) = %q, want %q", c.kind, c.url, got, c.want)
		}
	}
	if scenesWord(1) != "1 scene" || scenesWord(4) != "4 scenes" {
		t.Fatal("scenesWord")
	}
	if voicesFound(0) != "No voices found" || voicesFound(1) != "Found 1 voice" || voicesFound(3) != "Found 3 voices" {
		t.Fatal("voicesFound")
	}
	for v, want := range map[any]int{7: 7, int32(8): 8, int64(9): 9, 10.9: 10, "11": 0} {
		if toInt(v) != want {
			t.Errorf("toInt(%#v) = %d", v, toInt(v))
		}
	}
	if andList(nil) != "" || andList([]string{"a"}) != "a" || andList([]string{"a", "b"}) != "a and b" || andList([]string{"a", "b", "c"}) != "a, b and c" {
		t.Fatal("andList")
	}
	if capFirst("") != "" || capFirst("working") != "Working" || orStr("  ", "Done") != "Done" || orStr("x", "Done") != "x" {
		t.Fatal("capFirst / orStr")
	}
	if clip("  a   b  ", 10) != "a b" || clip("ééééé", 3) != "éé…" {
		t.Fatal("clip")
	}
	if firstLine("  first\nsecond") != "first" || len([]rune(firstLine(strings.Repeat("x", 500)))) != 240 {
		t.Fatal("firstLine")
	}
	if minutesWords(30*time.Second) != "30 seconds" || minutesWords(90*time.Second) != "about a minute" || minutesWords(5*time.Minute+10*time.Second) != "5 minutes" {
		t.Fatal("minutesWords")
	}
	if firstNonEmptyStr("", " ", "x", "y") != "x" || firstNonEmptyStr() != "" {
		t.Fatal("firstNonEmptyStr")
	}
	if strOf(3) != "" || strOf("x") != "x" {
		t.Fatal("strOf")
	}
}

// The quality check's verdict in its own words: problems must be fixed,
// things worth checking may be fine.
func TestQAVerdict(t *testing.T) {
	cases := []struct {
		res  map[string]any
		want string
	}{
		{map[string]any{}, "No problems found"},
		{map[string]any{"errors": int32(2)}, "2 problems to fix"},
		{map[string]any{"warnings": int64(1)}, "1 thing worth checking"},
		{map[string]any{"errors": int64(1), "warnings": int32(3)}, "1 problem, 3 things worth checking"},
	}
	for _, c := range cases {
		if got := qaVerdict(c.res); got != c.want {
			t.Errorf("%v: %q, want %q", c.res, got, c.want)
		}
	}
}

func TestGenerationWhat(t *testing.T) {
	cases := []struct {
		g    models.Generation
		want string
	}{
		{models.Generation{Operation: models.GenExtend, Params: models.GenParams{Seconds: 4}}, "Continuing a clip by 4 seconds"},
		{models.Generation{Operation: models.GenEdit}, "Changing a clip"},
		{models.Generation{Operation: models.GenLookTest}, "A test shot of a look"},
		{models.Generation{Operation: "shot", Params: models.GenParams{Seconds: 5}, Take: 1}, "A 5-second shot"},
		{models.Generation{Operation: "again", Params: models.GenParams{Seconds: 6}, Take: 3}, "A 6-second shot (take 3)"},
	}
	for _, c := range cases {
		if got := genWhat(c.g); got != c.want {
			t.Errorf("%+v: %q, want %q", c.g.Operation, got, c.want)
		}
	}
}

// What a group of waiting jobs waits for, short enough for the activity bar.
func TestWaitText(t *testing.T) {
	pct := 12
	cases := []struct {
		count int
		r     jobs.WaitReason
		typ   string
		want  string
	}{
		{1, jobs.WaitReason{Code: jobs.WaitBattery, Machine: "laptop", BatteryPercent: &pct}, "transcribe", "1 transcription paused: battery at 12% on laptop"},
		{2, jobs.WaitReason{Code: jobs.WaitBattery, Machine: "laptop"}, "render", "2 renders paused: laptop is on battery"},
		{1, jobs.WaitReason{Code: jobs.WaitNoWorker, Text: jobs.PinnedWaitText + " studio-mac.", Machine: "studio-mac"}, "import", "1 video from a link waiting for MetaRush to run on studio-mac"},
		{2, jobs.WaitReason{Code: jobs.WaitNoWorker, Text: jobs.PinnedWaitText + " the computer that asked for it."}, "import", "2 videos from links waiting for MetaRush to run on the computer that asked for it"},
		{3, jobs.WaitReason{Code: jobs.WaitNoWorker, Text: "No machine running MetaRush can make previews right now."}, "proxy", "3 preview copies waiting for a machine running MetaRush"},
		{1, jobs.WaitReason{Code: jobs.WaitHeavyDisabled, Machine: "mini"}, "mezzanine", "1 steady copy waiting: big jobs are turned off on mini"},
		{1, jobs.WaitReason{Code: jobs.WaitBusy, Machine: "mini"}, "qa", "1 quality check waiting its turn on mini"},
		{2, jobs.WaitReason{Code: jobs.WaitBusy, Machine: "mini"}, "fetch", "2 web pages or repositories waiting their turn on mini"},
		{1, jobs.WaitReason{Code: jobs.WaitRetry}, "generate", "1 generated clip trying again after a problem"},
		{1, jobs.WaitReason{Code: "something-new"}, "mystery", "1 job waiting"},
		{4, jobs.WaitReason{Code: "something-new"}, "mystery", "4 jobs waiting"},
	}
	for _, c := range cases {
		g := &WaitGroup{Code: c.r.Code, Count: c.count}
		if got := waitText(g, &c.r, c.typ); got != c.want {
			t.Errorf("%s/%s ×%d: %q, want %q", c.r.Code, c.typ, c.count, got, c.want)
		}
	}
}

// The activity bar's one line.
func TestHeadline(t *testing.T) {
	cases := []struct {
		name string
		o    Summary
		want string
	}{
		{"nothing", Summary{}, "Nothing running right now"},
		{"all done", Summary{Counts: Counts{Done: 3}}, "All caught up"},
		{"busy", Summary{Counts: Counts{Uploads: 2, Imports: 1, Processing: 1, Renders: 2, QA: 1, Assistant: 1, Storage: 1, Generating: 2, Failed: 1}},
			"Uploading 2 files, importing 1 video from links, processing 1 file, 2 renders, 1 quality check, Claude Code working, moving media to new storage, making 2 clips with generative video, 1 failure to review"},
		{"one narration", Summary{Counts: Counts{Narration: 1}, Items: []Item{{Category: CatNarration, State: StateDone, StageLabel: "Ready to review"}, {Category: CatNarration, State: StateActive, StageLabel: "Making the AI voice for line 3"}}},
			"Making the AI voice for line 3"},
		{"two narrations", Summary{Counts: Counts{Narration: 2}}, "Making narration for 2 cuts"},
		{"waiting that matters", Summary{Waiting: []WaitGroup{{Code: jobs.WaitBusy, Text: "1 render waiting its turn on mini"}, {Code: jobs.WaitHeavyDisabled, Text: "1 render waiting: big jobs are turned off on mini"}}},
			"1 render waiting: big jobs are turned off on mini"},
	}
	for _, c := range cases {
		if got := headline(&c.o); got != c.want {
			t.Errorf("%s: %q\nwant %q", c.name, got, c.want)
		}
	}
}

func TestOKLabelForSeveralPeople(t *testing.T) {
	cases := []struct {
		ws     []assistant.WaitingCount
		viewer string
		want   string
	}{
		{[]assistant.WaitingCount{{UserID: "usr_a", UserName: "Ada", Count: 0}, {UserID: "usr_b", UserName: "Bo", Count: 2}}, "usr_me", "Needs Bo's OK"},
		{[]assistant.WaitingCount{{UserID: "usr_a", UserName: "Ada", Count: 1}, {UserID: "usr_b", UserName: "Bo", Count: 1}}, "usr_me", "Needs someone's OK"},
		{[]assistant.WaitingCount{{UserID: "usr_a", UserName: " ", Count: 1}}, "usr_me", "Needs someone's OK"},
		{[]assistant.WaitingCount{{UserID: "usr_a", UserName: "Ada", Count: 1}}, "", "Needs your OK"},
		{[]assistant.WaitingCount{{UserID: "", Count: 1}}, "usr_me", "Needs your OK"},
		{nil, "usr_me", "Needs someone's OK"},
	}
	for _, c := range cases {
		if got := okLabel(c.ws, c.viewer); got != c.want {
			t.Errorf("%+v / %q: %q, want %q", c.ws, c.viewer, got, c.want)
		}
	}
}
