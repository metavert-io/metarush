package editspec_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// The dashboard applies the clip ops (and moveBeat, the narration lane's
// gesture) in the browser for instant feedback (web/src/lib/editops.ts)
// before the server's preview answers. This golden
// file is what Go makes of a set of cuts and ops; web/scripts/editops-parity.mjs
// checks that the browser makes the same. Regenerate after changing the ops:
//
//	UPDATE_GOLDEN=1 go test ./internal/editspec -run TestStudioOpsGolden
const goldenPath = "testdata/studio-ops.golden.json"

type goldenCase struct {
	Name  string          `json:"name"`
	Spec  *editspec.Spec  `json:"spec"`
	Ops   []editspec.Op   `json:"ops"`
	Clips []editspec.Clip `json:"clips,omitempty"`
	// Beats are the narration lines after the ops, for cuts that have some.
	Beats []editspec.Beat `json:"beats,omitempty"`
	Error bool            `json:"error,omitempty"`
}

func goldenCases() []goldenCase {
	base := func() *editspec.Spec {
		s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30}, Clips: []editspec.Clip{
			{ID: "hook", AssetID: specgen.Assets[0], In: 1, Out: 5, Label: "Hook", Notes: "keep the laugh"},
			{ID: "broll", AssetID: specgen.Assets[1], In: 0, Out: 3, Speed: 1.5, Transition: &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.8}},
			{ID: "end", AssetID: specgen.Assets[0], In: 10, Out: 14, FadeOut: 1, Transition: &editspec.Transition{Type: editspec.TransitionDip, Duration: 0.6}},
		}}
		editspec.Normalize(s)
		return s
	}
	f := func(v float64) *float64 { return &v }
	named := []goldenCase{
		{Name: "split", Ops: []editspec.Op{{Op: "splitClip", ID: "end", At: f(12.5)}}},
		{Name: "split named", Ops: []editspec.Op{{Op: "splitClip", ID: "hook", At: f(3), NewID: "hook-b"}}},
		{Name: "split twice", Ops: []editspec.Op{{Op: "splitClip", ID: "end", At: f(12)}, {Op: "splitClip", ID: "end-2", At: f(13)}}},
		{Name: "split outside", Ops: []editspec.Op{{Op: "splitClip", ID: "end", At: f(20)}}},
		{Name: "trim end", Ops: []editspec.Op{{Op: "trimClip", ID: "hook", Edge: "end", To: f(4.2)}}},
		{Name: "trim in", Ops: []editspec.Op{{Op: "trimClip", ID: "broll", Edge: "in", To: f(0.75)}}},
		{Name: "roll in with speed", Ops: []editspec.Op{{Op: "trimClip", ID: "broll", Edge: "in", To: f(0.75), Mode: editspec.TrimRoll}}},
		{Name: "roll end", Ops: []editspec.Op{{Op: "trimClip", ID: "broll", Edge: "end", To: f(2.4), Mode: editspec.TrimRoll}}},
		{Name: "roll first", Ops: []editspec.Op{{Op: "trimClip", ID: "hook", Edge: "in", To: f(2), Mode: editspec.TrimRoll}}},
		{Name: "slip", Ops: []editspec.Op{{Op: "slipClip", ID: "end", By: f(-2.25)}}},
		{Name: "slip too far", Ops: []editspec.Op{{Op: "slipClip", ID: "hook", By: f(-3)}}},
		{Name: "words inside", Ops: []editspec.Op{{Op: "removeWords", ID: "hook", From: f(2), To: f(2.6)}}},
		{Name: "words head", Ops: []editspec.Op{{Op: "removeWords", ID: "hook", From: f(0), To: f(1.5)}}},
		{Name: "words tail", Ops: []editspec.Op{{Op: "removeWords", ID: "end", From: f(13), To: f(20)}}},
		{Name: "words all", Ops: []editspec.Op{{Op: "removeWords", ID: "broll", From: f(0), To: f(3)}}},
		{Name: "range inside", Ops: []editspec.Op{{Op: "deleteRange", From: f(0.5), To: f(1.5)}}},
		{Name: "range across", Ops: []editspec.Op{{Op: "deleteRange", From: f(3), To: f(6.2)}}},
		{Name: "range in dissolve", Ops: []editspec.Op{{Op: "deleteRange", From: f(3.5), To: f(3.7)}}},
		{Name: "range past end", Ops: []editspec.Op{{Op: "deleteRange", From: f(7), To: f(30)}}},
		{Name: "range nothing", Ops: []editspec.Op{{Op: "deleteRange", From: f(40), To: f(41)}}},
		{Name: "range everything", Ops: []editspec.Op{{Op: "deleteRange", From: f(0), To: f(40)}}},
		{Name: "move", Ops: []editspec.Op{{Op: "moveClip", ID: "end", Before: "hook"}}},
		{Name: "insert", Ops: []editspec.Op{{Op: "insertClip", After: "hook", Clip: &editspec.Clip{ID: "new", AssetID: specgen.Assets[2], In: 1, Out: 2.5, Speed: 1}}}},
		{Name: "remove", Ops: []editspec.Op{{Op: "removeClip", ID: "broll"}}},
		{Name: "update", Ops: []editspec.Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"muted":true,"label":null}`)}}},
		{Name: "transition", Ops: []editspec.Op{{Op: "setTransition", ID: "end", Transition: &editspec.Transition{Type: editspec.TransitionWipe, Duration: 0.4, Direction: "up"}}}},
		{Name: "transition reset", Ops: []editspec.Op{{Op: "setTransition", ID: "broll", Transition: &editspec.Transition{Type: editspec.TransitionCut}}}},
		{Name: "fade out", Ops: []editspec.Op{{Op: "setTransition", ID: "end", Edge: "end", Transition: &editspec.Transition{Type: editspec.TransitionFade, Duration: 2}}}},
		{Name: "gestures", Ops: []editspec.Op{{Op: "moveClip", ID: "end", After: "hook"}, {Op: "trimClip", ID: "hook", Edge: "end", To: f(3.5)}, {Op: "splitClip", ID: "broll", At: f(1)}, {Op: "removeClip", ID: "broll-2"}}},
	}
	for i := range named {
		named[i].Spec = base()
	}
	// Narration lines moved on the timeline (moveBeat: a new anchor, a new place in the script, or both).
	narrated := func() *editspec.Spec {
		s := base()
		gap, at := 0.4, 6.0
		s.Voiceover = &editspec.Voiceover{Beats: []editspec.Beat{
			{ID: "l1", Text: "It starts in the kitchen.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Offset: 0.3}},
			{ID: "l2", Text: "Then the garden wakes up.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter, Gap: &gap}},
			{ID: "l3", Text: "And by noon, everything is green.", Anchor: editspec.Anchor{Kind: editspec.AnchorTime, At: &at}},
		}}
		editspec.Normalize(s)
		return s
	}
	clock := func(v float64) *editspec.Anchor { return &editspec.Anchor{Kind: editspec.AnchorTime, At: f(v)} }
	lines := []goldenCase{
		{Name: "line to a shot", Ops: []editspec.Op{{Op: "moveBeat", ID: "l2", Anchor: &editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "broll", Offset: 1.2}}}},
		{Name: "line to the clock", Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", Anchor: clock(2.5)}}},
		{Name: "line to a moment", Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", Anchor: &editspec.Anchor{Kind: editspec.AnchorMoment, AssetID: specgen.Assets[0], At: f(3), Offset: 0.2, Quote: "she laughs"}}}},
		{Name: "line to after the one before", Ops: []editspec.Op{{Op: "moveBeat", ID: "l3", Anchor: &editspec.Anchor{Kind: editspec.AnchorAfter}}}},
		{Name: "line reordered", Ops: []editspec.Op{{Op: "moveBeat", ID: "l3", Before: "l1"}}},
		{Name: "line moved and reordered", Ops: []editspec.Op{{Op: "moveBeat", ID: "l2", Anchor: clock(9), After: "l3"}}},
		{Name: "line and a split", Ops: []editspec.Op{{Op: "splitClip", ID: "hook", At: f(3)}, {Op: "moveBeat", ID: "l1", Anchor: &editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook-2", Offset: 0.5}}}},
		{Name: "lines moved twice", Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", Anchor: clock(1)}, {Op: "moveBeat", ID: "l1", Anchor: &editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "end", Edge: "end", Offset: -1}}}},
		{Name: "no such line", Ops: []editspec.Op{{Op: "moveBeat", ID: "l9", Anchor: clock(1)}}},
		{Name: "line without a new place", Ops: []editspec.Op{{Op: "moveBeat", ID: "l1"}}},
		{Name: "line after a missing line", Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", After: "l9"}}},
	}
	for i := range lines {
		lines[i].Spec = narrated()
	}
	lockedLine := narrated()
	lockedLine.Voiceover.Beats[2].Locked = true
	lockedNarration := narrated()
	lockedNarration.Voiceover.Locked = true
	lines = append(lines,
		goldenCase{Name: "locked line", Spec: lockedLine, Ops: []editspec.Op{{Op: "moveBeat", ID: "l3", Anchor: clock(7)}}},
		goldenCase{Name: "another line than the locked one", Spec: lockedLine, Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", Anchor: clock(1)}}},
		goldenCase{Name: "locked narration", Spec: lockedNarration, Ops: []editspec.Op{{Op: "moveBeat", ID: "l1", Anchor: clock(1)}}},
	)
	named = append(named, lines...)
	// A conversation clip keeps its picture through every edit (D99).
	conv := func() *editspec.Spec {
		s := base()
		s.Conversations = map[string]editspec.ConversationPin{"call": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz0", Members: []editspec.ConversationMember{
			{ID: "ada", Name: "Ada", AssetID: specgen.Assets[0]}, {ID: "sam", Name: "Sam", AssetID: specgen.Assets[1], Start: 0.5}}}}
		s.Clips = append(s.Clips, editspec.Clip{ID: "talk", AssetID: editspec.ConvKey("call"), In: 2, Out: 20, Shots: []editspec.Shot{
			{At: 0, Member: "ada", Rationale: "She opens."}, {At: 6, Member: "sam"}, {At: 9.5, Layout: editspec.LayoutSideBySide, Dissolve: 0.3}, {At: 14, Card: "Why twice?", Members: []string{"sam"}}}})
		editspec.Normalize(s)
		return s
	}
	for _, c := range []goldenCase{
		{Name: "conversation split", Ops: []editspec.Op{{Op: "splitClip", ID: "talk", At: f(7.25)}}},
		{Name: "conversation split on a shot", Ops: []editspec.Op{{Op: "splitClip", ID: "talk", At: f(9.5)}}},
		{Name: "conversation words", Ops: []editspec.Op{{Op: "removeWords", ID: "talk", From: f(8), To: f(10)}}},
		{Name: "conversation words head", Ops: []editspec.Op{{Op: "removeWords", ID: "talk", From: f(2), To: f(4)}}},
		{Name: "conversation range", Ops: []editspec.Op{{Op: "deleteRange", From: f(16), To: f(20)}}},
		{Name: "conversation trim", Ops: []editspec.Op{{Op: "trimClip", ID: "talk", Edge: "in", To: f(7)}}},
		{Name: "conversation shots", Ops: []editspec.Op{{Op: "updateClip", ID: "talk", Set: json.RawMessage(`{"shots":[{"at":2,"layout":"grid"}]}`)}}},
	} {
		c.Spec = conv()
		named = append(named, c)
	}
	locked := base()
	locked.Clips[0].Locked = true
	for _, op := range []editspec.Op{{Op: "splitClip", ID: "hook", At: f(3)}, {Op: "trimClip", ID: "hook", Edge: "end", To: f(4)}, {Op: "slipClip", ID: "hook", By: f(0.5)},
		{Op: "removeWords", ID: "hook", From: f(2), To: f(3)}, {Op: "deleteRange", From: f(1), To: f(2)}, {Op: "trimClip", ID: "broll", Edge: "in", To: f(0.5), Mode: editspec.TrimRoll}} {
		named = append(named, goldenCase{Name: "locked " + op.Op, Spec: locked, Ops: []editspec.Op{op}})
	}
	// Random cuts and gestures (like TestStudioOpsOnRandomCuts).
	for seed := int64(1); seed <= 150; seed++ {
		g := specgen.New(seed)
		g.Plain = true
		s := g.Spec()
		editspec.Normalize(s)
		for i := range s.Clips {
			s.Clips[i].Locked = false
		}
		r := g.R
		c := s.Clips[r.Intn(len(s.Clips))]
		// float64(…): no fused multiply-add on arm64, so every computer writes
		// the same golden file (amd64 has none).
		mid := c.In + float64((c.Out-c.In)*r.Float64())
		_, totalF := editspec.Timeline(s)
		total := float64(totalF) / float64(s.Output.FPS)
		a, b := total*r.Float64(), total*r.Float64()
		if a > b {
			a, b = b, a
		}
		var op editspec.Op
		switch r.Intn(5) {
		case 0:
			op = editspec.Op{Op: "splitClip", ID: c.ID, At: f(mid)}
		case 1:
			op = editspec.Op{Op: "trimClip", ID: c.ID, Edge: []string{"in", "end"}[r.Intn(2)], To: f(mid), Mode: []string{"", editspec.TrimRoll}[r.Intn(2)]}
		case 2:
			op = editspec.Op{Op: "slipClip", ID: c.ID, By: f(r.Float64() - 0.5)}
		case 3:
			op = editspec.Op{Op: "removeWords", ID: c.ID, From: f(mid - 0.3), To: f(mid + 0.3)}
		default:
			op = editspec.Op{Op: "deleteRange", From: f(a), To: f(b)}
		}
		named = append(named, goldenCase{Name: fmt.Sprintf("random %d %s", seed, op.Op), Spec: s, Ops: []editspec.Op{op}})
	}
	for i := range named {
		got, _, err := editspec.ApplyOps(named[i].Spec, named[i].Ops, false)
		if err != nil {
			named[i].Error = true
			continue
		}
		named[i].Clips = got.Clips
		if got.Voiceover != nil {
			named[i].Beats = got.Voiceover.Beats
		}
	}
	return named
}

func TestStudioOpsGolden(t *testing.T) {
	cases := goldenCases()
	b, err := goldenFile("go test ./internal/editspec -run TestStudioOpsGolden (UPDATE_GOLDEN=1)", cases)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := os.ReadFile(goldenPath)
	if err == nil && !bytes.Equal(cur, b) {
		err = sameGolden(cur, b)
	}
	if err != nil {
		t.Fatalf("%s is stale; run UPDATE_GOLDEN=1 go test ./internal/editspec -run TestStudioOpsGolden (%v)", goldenPath, err)
	}
	// The browser's working copy makes the same cuts (when node and the web
	// dependencies are installed; CI and the build checks have both).
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if _, err := os.Stat("../../node_modules/esbuild"); err != nil {
		t.Skip("web dependencies not installed (npm install)")
	}
	out, err := exec.Command(node, "../../web/scripts/editops-parity.mjs", goldenPath).CombinedOutput()
	if err != nil {
		t.Fatalf("the dashboard's ops differ from Go's:\n%s", out)
	}
}

func TestGoldenComparisonIgnoresOnlyTheLastBits(t *testing.T) {
	base := `{"cases":[{"name":"a","clips":[{"in":5.692376143363883,"out":7}]}]}`
	for doc, same := range map[string]bool{
		base: true,
		`{"cases":[{"name":"a","clips":[{"in":5.692376143363882,"out":7}]}]}`: true,  // one bit (arm64 vs amd64)
		`{"cases":[{"name":"a","clips":[{"in":5.6924,"out":7}]}]}`:            false, // a different time
		`{"cases":[{"name":"b","clips":[{"in":5.692376143363883,"out":7}]}]}`: false,
		`{"cases":[{"name":"a","clips":[{"in":5.692376143363883}]}]}`:         false,
		`{"cases":[{"name":"a","clips":[]}]}`:                                 false,
	} {
		if err := sameGolden([]byte(base), []byte(doc)); (err == nil) != same {
			t.Errorf("%s: %v, want same=%v", doc, err, same)
		}
	}
}

// sameGolden compares two golden files as JSON, numbers to within a billionth
// (of a second, mostly): the last bits of a float may differ between
// computers (arm64 fuses multiply-adds, amd64 does not), and that is not a
// different cut.
func sameGolden(a, b []byte) error {
	var x, y any
	if err := json.Unmarshal(a, &x); err != nil {
		return err
	}
	if err := json.Unmarshal(b, &y); err != nil {
		return err
	}
	return sameJSON(x, y, "")
}

func sameJSON(x, y any, at string) error {
	switch xv := x.(type) {
	case map[string]any:
		yv, ok := y.(map[string]any)
		if !ok || len(xv) != len(yv) {
			return fmt.Errorf("%s differs", at)
		}
		for k, v := range xv {
			if err := sameJSON(v, yv[k], at+"."+k); err != nil {
				return err
			}
		}
	case []any:
		yv, ok := y.([]any)
		if !ok || len(xv) != len(yv) {
			return fmt.Errorf("%s differs", at)
		}
		for i := range xv {
			if err := sameJSON(xv[i], yv[i], fmt.Sprintf("%s[%d]", at, i)); err != nil {
				return err
			}
		}
	case float64:
		yv, ok := y.(float64)
		if !ok || math.Abs(xv-yv) > 1e-9*math.Max(1, math.Max(math.Abs(xv), math.Abs(yv))) {
			return fmt.Errorf("%s: %v, now %v", at, x, y)
		}
	default:
		if x != y {
			return fmt.Errorf("%s: %v, now %v", at, x, y)
		}
	}
	return nil
}

// goldenFile writes {"generatedBy": …, "cases": […]} with one compact line per case.
func goldenFile[T any](by string, cases []T) ([]byte, error) {
	var buf bytes.Buffer
	head, err := json.Marshal(by)
	if err != nil {
		return nil, err
	}
	buf.WriteString(`{"generatedBy": `)
	buf.Write(head)
	buf.WriteString(",\n\"cases\": [\n")
	for i, c := range cases {
		b, err := json.Marshal(c)
		if err != nil {
			return nil, err
		}
		buf.Write(b)
		if i < len(cases)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]}\n")
	return buf.Bytes(), nil
}
