package api

import (
	"bytes"
	"context"
	"net/http"
	"testing"

	"github.com/metavert-io/metarush/internal/ingest"
)

// A still asked for at the end of a clip, or past it (a sample tile at a
// voice's last moment, a time from before the clip was trimmed), is the clip's
// last picture. The time used to be clamped to just before the end and then
// rounded to its 0.1 s step — for a clip whose length is a whole number of
// tenths, back onto the very end, where there is no frame — and the answer was
// "MetaRush couldn't take a still from the footage."
func TestAStillAtTheEndOfAClipIsItsLastFrame(t *testing.T) {
	needFFmpeg(t)
	d, projectID, _ := newDash(t, noClaude)
	res := ingest.File(context.Background(), d.a, clipFile(t, "three-seconds.mp4", 3, true), ingest.Options{ProjectID: projectID, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	d.signIn()
	for _, at := range []string{"3", "2.99", "99"} {
		code, h, b := d.send("GET", "/media/assets/"+res.AssetID+"/frame?w=160&t="+at, nil, nil, false)
		if code != http.StatusOK || h.Get("Content-Type") != "image/jpeg" || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
			t.Errorf("a still at %s s of a 3 s clip: %d %s", at, code, b)
		}
	}
}
