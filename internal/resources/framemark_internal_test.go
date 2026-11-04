package resources

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func jpegFile(t *testing.T, dir, name string, shade uint8) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 36))
	for y := 0; y < 36; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{shade, uint8(x * 4), uint8(y * 7), 255})
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// A frame MetaRush takes says where it was taken, inside the file (D457):
// it stays a picture every program opens, Resources keeps where it came from
// when it is added here, a frame taken in another workspace gives nothing
// here, and a picture with no such comment is just a picture.
func TestAFrameSaysWhereItWasTaken(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	film := project(t, a, "Film")
	now := time.Now().UTC()
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "their-hook.mp4", Title: "Their hook", State: models.AssetAvailable,
		ProjectIDs: []string{}, Media: models.MediaInfo{Streams: []models.Stream{}}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	frame := jpegFile(t, dir, "00m02.jpg", 10)
	if err := MarkFrame(frame, a.WorkspaceID(), clip.ID, 2.5); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(frame)
	if err != nil {
		t.Fatal(err)
	}
	_, format, err := image.Decode(f)
	_ = f.Close()
	if err != nil || format != "jpeg" {
		t.Fatalf("the marked frame is still a JPEG: %s %v", format, err)
	}
	if fm := readFrameMark(frame); fm == nil || fm.Asset != clip.ID || fm.At != 2.5 || fm.Workspace != a.WorkspaceID() {
		t.Fatalf("the comment: %+v", fm)
	}
	added, err := AddFile(ctx, a, frame, AddOptions{ProjectID: film.ID})
	if err != nil || added.Resource == nil {
		t.Fatalf("add: %+v %v", added, err)
	}
	if fo := added.Resource.Latest().Source.FrameOf; fo == nil || fo.AssetID != clip.ID || fo.Title != "Their hook" || fo.At != 2.5 {
		t.Fatalf("where it came from: %+v", fo)
	}
	if got := added.Resource.Latest().Source.Provenance(); got != "a frame of “Their hook” at 0:02" {
		t.Fatalf("in words: %q", got)
	}
	if FrameReference(ctx, a, added.Resource, "") != nil || len(added.Notes) != 0 {
		t.Fatalf("a frame of the person's own footage: %+v", added.Notes)
	}
	// Taken in another workspace: nothing here.
	elsewhere := jpegFile(t, dir, "elsewhere.jpg", 90)
	if err := MarkFrame(elsewhere, ids.New(ids.Workspace), clip.ID, 1); err != nil {
		t.Fatal(err)
	}
	other, err := AddFile(ctx, a, elsewhere, AddOptions{ProjectID: film.ID})
	if err != nil || other.Resource == nil || other.Resource.Latest().Source.FrameOf != nil {
		t.Fatalf("a frame from another workspace: %+v %v", other, err)
	}
	// No comment: just a picture.
	plain := jpegFile(t, dir, "plain.jpg", 200)
	if readFrameMark(plain) != nil {
		t.Fatal("a picture with no comment says it's a frame")
	}
	if err := MarkFrame(filepath.Join(dir, "missing.jpg"), a.WorkspaceID(), clip.ID, 1); err == nil {
		t.Fatal("marking a file that isn't there")
	}
}
