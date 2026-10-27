package api

import (
	"bytes"
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A clip's files and a render's are served to the signed-in dashboard: the
// original as a download under its name, a copy it has, plainly nothing (in
// words) for one it hasn't, a clip still being added refused until it's
// ready; a render's video and picture, and nothing for one not finished.
func TestClipAndRenderFilesAreServed(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	c := addClip(t, d.a, "Harbour", 32<<10)
	base := "/media/assets/" + c.asset.ID + "/"
	code, h, b := d.send("GET", base+"original?download=1", nil, nil, false)
	if code != 200 || h.Get("Content-Disposition") != `attachment; filename="Harbour.mp4"` || len(b) != 64<<10 {
		t.Fatalf("the original as a download: %d %v %d bytes", code, h, len(b))
	}
	if code, h, b := d.send("GET", base+"proxy", nil, nil, false); code != 200 || h.Get("Content-Type") != "video/mp4" || !bytes.Equal(b, c.proxy) {
		t.Fatalf("the preview copy: %d %v", code, h)
	}
	if code, h, b := d.send("GET", base+"thumbnail", nil, nil, false); code != 200 || h.Get("Content-Type") != "image/jpeg" || !bytes.Equal(b, c.picture) {
		t.Fatalf("the picture: %d %v", code, h)
	}
	for _, v := range []struct{ variant, says string }{
		{"waveform", "has no sound picture yet"}, {"mezzanine", "has no steady copy yet"}, {"contact_sheet", "has no pictures yet"}, {"other", "has no copy like that yet"},
	} {
		code, _, b := d.send("GET", base+v.variant, nil, nil, false)
		if code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "“Harbour” "+v.says) {
			t.Errorf("%s: %d %s", v.variant, code, b)
		}
	}
	if code, _, b := d.send("GET", base+"original?media="+strings.Repeat("0", 64), nil, nil, false); code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "copy of “Harbour”") {
		t.Fatalf("a copy it doesn't have: %d %s", code, b)
	}
	if _, err := d.a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": c.asset.ID}, bson.M{"$set": bson.M{"state": models.AssetStaging}}); err != nil {
		t.Fatal(err)
	}
	if code, _, b := d.send("GET", base+"original", nil, nil, false); code != http.StatusConflict || !strings.Contains(envOf(t, b).Error.Message, "isn't ready to play yet") {
		t.Fatalf("a clip still being added: %d %s", code, b)
	}

	rd, video := addRender(t, d.a)
	code, h, b = d.send("GET", "/media/renders/"+rd.ID+"?download=1", nil, nil, false)
	if code != 200 || !bytes.Equal(b, video) || !strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="metarush-r3-vertical-1080p-`) {
		t.Fatalf("the render as a download: %d %v", code, h)
	}
	if code, h, _ := d.send("GET", "/media/renders/"+rd.ID+"/thumbnail", nil, nil, false); code != 200 || h.Get("Content-Type") != "image/jpeg" {
		t.Fatalf("the render's picture: %d %v", code, h)
	}
	unfinished := models.Render{ID: ids.New(ids.Render), WorkspaceID: d.a.WorkspaceID(), ProjectID: rd.ProjectID, RevisionID: rd.RevisionID, State: models.RenderRendering, CreatedAt: time.Now().UTC()}
	if err := d.a.DB.Insert(ctx, store.CRenders, unfinished); err != nil {
		t.Fatal(err)
	}
	for _, p := range []struct{ path, says string }{
		{"/media/renders/" + unfinished.ID, "That render isn't ready to watch."},
		{"/media/renders/" + unfinished.ID + "/thumbnail", "That render has no picture yet."},
		{"/media/renders/" + ids.New(ids.Render), "That render isn't ready to watch."},
	} {
		if code, _, b := d.send("GET", p.path, nil, nil, false); code != http.StatusNotFound || envOf(t, b).Error.Message != p.says {
			t.Errorf("%s: %d %s", p.path, code, b)
		}
	}
}
