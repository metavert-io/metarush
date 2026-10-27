package api

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// What a person reviews before approving a motion graphic (D68): its contact
// sheet, preview (in each shape it was checked in) and test frames — pictures
// and video only, sandboxed — and plainly nothing for what it doesn't have.
func TestMotionGraphicPicturesForReview(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	frame := checkFrame(t)
	fk, fs := stored(t, d.a, frame, "image/png")
	sheet := []byte("\xff\xd8\xff\xe0 a contact sheet")
	sk, ss := stored(t, d.a, sheet, "image/jpeg")
	video := []byte("\x00\x00\x00\x18ftypmp42 a preview")
	vk, vs := stored(t, d.a, video, "video/mp4")
	hash := strings.Repeat("ef", 32)
	now := time.Now().UTC()
	obj := func(k, s string, n int) *models.MotionObject {
		return &models.MotionObject{Key: k, SHA256: s, Size: int64(n)}
	}
	c := models.MotionComponent{ID: d.a.WorkspaceID() + "/" + hash, WorkspaceID: d.a.WorkspaceID(), Hash: hash, Name: "end-card", Region: "full", State: models.MotionApproved,
		Sheet: obj(sk, ss, len(sheet)), Video: obj(vk, vs, len(video)), Shapes: []models.MotionShape{{Label: "vertical", Width: 1080, Height: 1920, Video: obj(vk, vs, len(video))}},
		Frames: []models.MotionFrame{{Label: "start", At: 0, Object: models.MotionObject{Key: fk, SHA256: fs, Size: int64(len(frame)), Width: 40, Height: 20}}}, CreatedAt: now, UpdatedAt: now}
	if err := d.a.DB.Insert(ctx, store.CMotion, c); err != nil {
		t.Fatal(err)
	}
	base := "/media/motion/" + hash + "/"
	for _, k := range []struct {
		kind, mime string
		size       int
	}{{"sheet", "image/jpeg", len(sheet)}, {"preview", "video/mp4", len(video)}, {"preview-vertical", "video/mp4", len(video)}, {"frame-0", "image/png", len(frame)}} {
		code, h, b := d.send("GET", base+k.kind, nil, nil, false)
		if code != 200 || h.Get("Content-Type") != k.mime || len(b) != k.size || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: %d %v %d bytes", k.kind, code, h, len(b))
		}
	}
	for _, k := range []struct{ kind, says string }{
		{"frame-3", "has no test frame like that"}, {"preview-square", "has no preview"}, {"poster", "has no picture like that"}, {"still-7", "has no test frame like that"},
	} {
		code, _, b := d.send("GET", base+k.kind, nil, nil, false)
		if code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, k.says) {
			t.Errorf("%s: %d %s", k.kind, code, b)
		}
	}
	for _, p := range []string{"/media/motion/not-a-hash/sheet", "/media/motion/" + strings.Repeat("0", 64) + "/sheet"} {
		if code, _, _ := d.send("GET", p, nil, nil, false); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
	// The live preview's still: cached by the browser by its frame (304 when unchanged), its size without a body on HEAD.
	code, h, _ := d.send("GET", base+"still-0", nil, nil, false)
	if code != 200 || h.Get("ETag") == "" {
		t.Fatalf("still: %d %v", code, h)
	}
	if code, _, b := d.send("GET", base+"still-0", nil, map[string]string{"If-None-Match": h.Get("ETag")}, false); code != http.StatusNotModified || len(b) != 0 {
		t.Fatalf("unchanged still: %d %d bytes", code, len(b))
	}
	if code, hh, b := d.send("HEAD", base+"still-0", nil, nil, false); code != 200 || hh.Get("Content-Length") == "" || len(b) != 0 {
		t.Fatalf("HEAD of a still: %d %v %d bytes", code, hh, len(b))
	}
	// Stills kept in memory: the latest few, the oldest out first.
	cache := &stillCache{byKey: map[string][]byte{}}
	for i := 0; i < stillCacheSize+5; i++ {
		cache.put("k"+strconv.Itoa(i), []byte{byte(i)})
	}
	if _, ok := cache.get("k0"); ok {
		t.Fatal("the oldest still is kept past the limit")
	}
	if b, ok := cache.get("k" + strconv.Itoa(stillCacheSize+4)); !ok || b[0] != byte(stillCacheSize+4) {
		t.Fatal("the newest still")
	}
	cache.get("k10")
	cache.put("k10", []byte{1})
	if len(cache.order) != stillCacheSize || len(cache.byKey) != stillCacheSize {
		t.Fatalf("cache size %d %d", len(cache.order), len(cache.byKey))
	}
	if motionKindWords("something") != "picture like that" {
		t.Fatal("kind words")
	}
}

// What a person reviews for a version of a Remotion project (D137): the
// before/after stills of its check and each composition's picture, served as
// sandboxed images; nothing for what the version doesn't have.
func TestRemotionVersionPicturesForReview(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	after := []byte("\xff\xd8\xff\xe0 after")
	ak, as := stored(t, d.a, after, "image/jpeg")
	poster := []byte("\xff\xd8\xff\xe0 poster")
	pk, ps := stored(t, d.a, poster, "image/jpeg")
	hash := strings.Repeat("9a", 32)
	ver := models.RemotionVersion{ID: d.a.WorkspaceID() + "/" + hash, WorkspaceID: d.a.WorkspaceID(), Hash: hash, Number: 2,
		Compositions: []models.RemotionComposition{{ID: "Intro", Width: 1920, Height: 1080, FPS: 30, DurationInFrames: 90, Poster: &models.RemotionObject{Key: pk, SHA256: ps, Size: int64(len(poster))}}},
		Check:        &models.RemotionCheck{OK: true, Summary: "Checked.", Stills: []models.RemotionStill{{Composition: "Intro", After: models.RemotionObject{Key: ak, SHA256: as, Size: int64(len(after))}}}}}
	if err := d.a.DB.Insert(ctx, store.CRemotionVersions, ver); err != nil {
		t.Fatal(err)
	}
	base := "/media/remotion/" + hash + "/"
	for _, p := range []struct {
		path string
		size int
	}{{"still/0/after", len(after)}, {"poster/Intro", len(poster)}} {
		code, h, b := d.send("GET", base+p.path, nil, nil, false)
		if code != 200 || h.Get("Content-Type") != "image/jpeg" || len(b) != p.size || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") {
			t.Errorf("%s: %d %v %d bytes", p.path, code, h, len(b))
		}
	}
	for _, p := range []string{"still/0/before", "still/4/after", "still/x/after", "poster/Outro", "words/1/before", "words/a-b/after", "scene/Intro/x", "nothing/0"} {
		code, _, b := d.send("GET", base+p, nil, nil, false)
		if code != http.StatusNotFound {
			t.Errorf("%s: %d %s", p, code, b)
		}
	}
	for _, p := range []string{"/media/remotion/abc/poster/Intro", "/media/remotion/" + strings.Repeat("0", 64) + "/poster/Intro"} {
		if code, _, b := d.send("GET", p, nil, nil, false); code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "version of a Remotion project") {
			t.Errorf("%s: %d %s", p, code, b)
		}
	}
}
