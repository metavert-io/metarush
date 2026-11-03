package assistant

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
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
