package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// A motion graphic's card sends every test frame's size and shape, drawn in
// one shape or several (D377), so the panel shows each at its own ratio —
// never cropped into a widescreen box.
func TestMotionCardFramesSayTheirSize(t *testing.T) {
	a := testApp(t, "")
	m := NewTerminal(a, "/usr/local/bin/metarush-test")
	t.Cleanup(m.Close)
	ctx := context.Background()
	save := func(hash string, frames []models.MotionFrame, shapes []models.MotionShape) {
		t.Helper()
		obj := models.MotionObject{Key: "k", SHA256: hash}
		for i := range frames {
			frames[i].Object.Key, frames[i].Object.SHA256 = "k", hash
		}
		if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "route-map", Region: "center", SchemaJSON: `{}`,
			Files: []models.MotionFile{}, Imports: []models.MotionImport{}, Archive: obj, Frames: frames, Shapes: shapes,
			Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1920}}); err != nil {
			t.Fatal(err)
		}
	}
	says := func(hash string) string {
		act := &AssistantAction{}
		m.motionPictures(ctx, act, hash)
		var out []string
		for _, f := range act.Frames {
			out = append(out, fmt.Sprintf("%s %dx%d %s", f.Shape, f.Width, f.Height, f.Label))
		}
		return strings.Join(out, " | ")
	}

	// One shape: sizes all the same, still sent (the panel used to fall back to 16:9 boxes).
	one := strings.Repeat("1a", 32)
	save(one, []models.MotionFrame{{Label: "start", Object: models.MotionObject{Width: 1080, Height: 1920}, Shape: "vertical"}, {Label: "end"}}, nil)
	if got := says(one); got != "vertical 1080x1920 start | vertical 1080x1920 end" {
		t.Fatalf("one shape: %s", got)
	}
	// Frames are linked by their own SHA-256 (D381): a card keeps showing what it showed.
	act := &AssistantAction{}
	m.motionPictures(ctx, act, one)
	if len(act.Frames) == 0 || act.Frames[0].URL != "/media/motion/"+one+"/frame-"+one {
		t.Fatalf("frame links: %+v", act.Frames)
	}
	// Two vertical sizes: each frame keeps its own, labels stay plain.
	two := strings.Repeat("2b", 32)
	save(two, []models.MotionFrame{
		{Label: "start", Object: models.MotionObject{Width: 1080, Height: 1920}, Shape: "vertical 9:16"},
		{Label: "start", Object: models.MotionObject{Width: 1080, Height: 1350}, Shape: "vertical 4:5"},
	}, []models.MotionShape{{Label: "vertical 9:16", Width: 1080, Height: 1920}, {Label: "vertical 4:5", Width: 1080, Height: 1350}})
	if got := says(two); got != "vertical 9:16 1080x1920 start | vertical 4:5 1080x1350 start" {
		t.Fatalf("two vertical sizes: %s", got)
	}
}

// Claude's `motion check --version … --shape …` (the command a cut's note
// gives) is a card (D382): it draws the same version in that shape, starts by
// itself as a safe step and Auto may run it, since it only adds test frames;
// approving it in that shape stays a person's card.
func TestMotionRedrawAndShapeApprovalCards(t *testing.T) {
	a := testApp(t, "")
	ctx := context.Background()
	m := &Manager{App: a, names: map[string]string{}}
	hash := strings.Repeat("3c", 32)
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "route-map", Region: "center", SchemaJSON: `{}`,
		Files: []models.MotionFile{}, Imports: []models.MotionImport{}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1920},
		Frames: []models.MotionFrame{{Label: "middle", Object: models.MotionObject{Key: "k", SHA256: strings.Repeat("0a", 32), Width: 1080, Height: 1920}}}}); err != nil {
		t.Fatal(err)
	}
	card := func(command string) *AssistantAction {
		t.Helper()
		words, err := splitCommand(command)
		if err != nil {
			t.Fatal(err)
		}
		call, ok := parseMetarush(words)
		if !ok || readOnlyCall(call) {
			t.Fatalf("%s should be a card (%+v)", command, call)
		}
		act := &AssistantAction{Kind: call.Path, State: ActionPending}
		m.describeAction(ctx, &conv{id: "cnv_redraw"}, &call, act)
		for _, s := range append([]string{act.Title}, act.Details...) {
			if err := plainwords.CheckPanel(s); err != nil {
				t.Errorf("%s: card text %q: %v", command, s, err)
			}
		}
		return act
	}
	redraw := card("metarush motion check --version route-map@3c3c3c3c --shape 1920x1080 --json")
	var in ops.MotionCheckIn
	_ = json.Unmarshal([]byte(redraw.Input), &in)
	if redraw.Op != "motion.check" || in.Version != "route-map@"+hash || in.Shape != "1920x1080" || in.Dir != "" || !strings.Contains(redraw.Title, "widescreen (16:9)") || redraw.HumanOnly {
		t.Fatalf("the redraw card: %+v (%s)", redraw, redraw.Input)
	}
	if c := (&conv{id: "cnv_redraw"}); !c.safeStep(redraw) || !autoOps["motion.check"] {
		t.Fatal("drawing again only adds test frames: a safe step Auto may run")
	}
	approve := card("metarush motion approve route-map@3c3c3c3c --shape 1920x1080")
	var din ops.MotionDecideIn
	_ = json.Unmarshal([]byte(approve.Input), &din)
	if approve.Op != "motion.approve" || !approve.HumanOnly || fmt.Sprint(din.Shapes) != "[1920x1080]" || !strings.Contains(approve.Title, "in widescreen (16:9)") {
		t.Fatalf("the approve card: %+v (%s)", approve, approve.Input)
	}
}
