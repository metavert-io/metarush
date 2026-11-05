package api

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/mattes"
	"github.com/metavert-io/metarush/internal/matting/mattingtest"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
)

// A clip's cut-out reaches the signed-in dashboard from this computer, by
// its file's SHA-256, as a video it can seek in; a still of a moment with a
// new background comes as a JPEG; nothing else answers (D510, D513).
func TestCutOutsAreServedToTheDashboard(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	fakelocal.Matter(t)
	d, projectID, _ := newDash(t, noClaude)
	ctx := context.Background()
	src := mattingtest.Footage(t, t.TempDir(), 1, 160, 96, 25)
	named := filepath.Join(filepath.Dir(src), "host.mp4")
	if err := os.Rename(src, named); err != nil {
		t.Fatal(err)
	}
	res := ingest.File(ctx, d.a, named, ingest.Options{ProjectID: projectID, Via: "cli"})
	if res.AssetID == "" {
		t.Fatalf("ingest: %+v", res)
	}
	out, err := ops.CallTyped[mattes.CutoutOut](ctx, d.a, "footage.cutout", mattes.CutoutIn{AssetID: res.AssetID}, ops.Caller{Human: true})
	if err != nil || out.JobID == "" {
		t.Fatalf("asked: %+v %v", out, err)
	}
	j, err := d.a.Queue.LeaseByID(ctx, jobs.Worker{ID: d.a.Cfg.Installation.ID, Name: "test", JobTypes: []string{jobs.TypeMatte}, AcceptsHeavy: true}, out.JobID)
	if err != nil || j == nil {
		t.Fatalf("lease: %v %v", j, err)
	}
	made, err := mattes.Run(ctx, d.a, j, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	sha, _ := made["sha256"].(string)
	file := "/media/cutouts/" + sha
	if code, _, _ := d.send("GET", file, nil, nil, false); code != http.StatusUnauthorized {
		t.Fatalf("without signing in: %d", code)
	}
	d.signIn()
	code, h, b := d.send("GET", file, nil, nil, false)
	if code != 200 || h.Get("Content-Type") != "video/webm" || !bytes.HasPrefix(b, []byte{0x1a, 0x45, 0xdf, 0xa3}) || h.Get("Location") != "" {
		t.Fatalf("the cut-out: %d %v", code, h)
	}
	if code, h, part := d.send("GET", file, nil, map[string]string{"Range": "bytes=0-99"}, false); code != http.StatusPartialContent || len(part) != 100 || !strings.HasPrefix(h.Get("Content-Range"), "bytes 0-99/") {
		t.Fatalf("a part of it: %d %v (%d bytes)", code, h, len(part))
	}
	for _, p := range []string{"/media/cutouts/" + strings.Repeat("0", 64), "/media/cutouts/not-a-hash"} {
		if code, _, _ := d.send("GET", p, nil, nil, false); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
	q := url.Values{"assetId": {res.AssetID}, "at": {"0.5"}, "with": {"colour"}, "colour": {"#00b140"}, "width": {"160"}}
	code, h, b = d.send("GET", "/media/cutouts/preview?"+q.Encode(), nil, nil, false)
	if code != 200 || h.Get("Content-Type") != "image/jpeg" || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
		t.Fatalf("the still: %d %v %s", code, h, b[:min(len(b), 200)])
	}
	q.Set("with", "sky")
	if code, _, b := d.send("GET", "/media/cutouts/preview?"+q.Encode(), nil, nil, false); code == 200 || envOf(t, b).Error.Message == "" {
		t.Fatalf("an unknown background: %d %s", code, b)
	}
}
