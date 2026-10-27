package api

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// checkFrame is a test frame as the motion check draws one: the check's
// backdrop (motion.TestBackground) with a white bar on it.
func checkFrame(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 40, 20))
	for y := 0; y < 20; y++ {
		for x := 0; x < 40; x++ {
			img.Set(x, y, color.RGBA{0x10, 0x10, 0x14, 0xff})
			if y >= 12 && y < 18 && x >= 4 && x < 36 {
				img.Set(x, y, color.RGBA{0xff, 0xff, 0xff, 0xff})
			}
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// The live preview's stills of a motion graphic (D187) are pictures drawn in
// the render jail, served only to a signed-in browser, only for versions
// whose check drew test frames: the frame without the check's backdrop.
func TestMotionStillsAreServedSignedInForCheckedGraphics(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	frame := checkFrame(t)
	key, sha := stored(t, d.a, frame, "image/png")
	now := time.Now().UTC()
	checked := strings.Repeat("ab", 32)
	unchecked := strings.Repeat("cd", 32)
	for _, c := range []models.MotionComponent{
		{ID: d.a.WorkspaceID() + "/" + checked, WorkspaceID: d.a.WorkspaceID(), Hash: checked, Name: "name-bar", Region: "lower", State: models.MotionApproved,
			Frames: []models.MotionFrame{{Label: "middle", At: 2, Object: models.MotionObject{Key: key, SHA256: sha, Size: int64(len(frame)), Width: 40, Height: 20}}}, CreatedAt: now, UpdatedAt: now},
		{ID: d.a.WorkspaceID() + "/" + unchecked, WorkspaceID: d.a.WorkspaceID(), Hash: unchecked, Name: "end-card", Region: "full", State: models.MotionWaiting, CreatedAt: now, UpdatedAt: now},
	} {
		if err := d.a.DB.Insert(ctx, store.CMotion, c); err != nil {
			t.Fatal(err)
		}
	}

	ans := d.get("/media/motion/"+checked+"/still-0", "")
	if ans.code != http.StatusOK {
		t.Fatalf("the checked graphic's still: %d %s", ans.code, ans.message)
	}
	img, err := png.Decode(bytes.NewReader(ans.body))
	if err != nil {
		t.Fatalf("not a picture: %v", err)
	}
	if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c.A != 0 {
		t.Errorf("the backdrop is still there: %+v", c)
	}
	if c := color.NRGBAModel.Convert(img.At(10, 14)).(color.NRGBA); c.A != 255 {
		t.Errorf("what the graphic drew went: %+v", c)
	}
	// Twice: the same picture (kept in memory by the frame's SHA-256).
	if again := d.get("/media/motion/"+checked+"/still-0", ""); again.code != http.StatusOK || !bytes.Equal(again.body, ans.body) {
		t.Errorf("asking again: %d, same bytes %v", again.code, bytes.Equal(again.body, ans.body))
	}
	for _, path := range []string{"/media/motion/" + unchecked + "/still-0", "/media/motion/" + checked + "/still-1", "/media/motion/" + checked + "/still-x"} {
		if ans := d.get(path, ""); ans.code != http.StatusNotFound {
			t.Errorf("%s: %d, want not found", path, ans.code)
		}
	}
	// A browser that isn't signed in gets nothing.
	res, err := http.Get(d.base + "/media/motion/" + checked + "/still-0")
	if err != nil {
		t.Fatal(err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Errorf("without signing in: %d, want 401", res.StatusCode)
	}
}
