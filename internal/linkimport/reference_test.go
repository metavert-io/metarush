package linkimport_test

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// An import asked to bring a link in as a reference (D398, D444) marks the
// clip it made even when it is tried again after making it (its ingest
// session names the job); a clip it didn't make — the person's own, imported
// before — is never marked.
func TestAnImportTriedAgainStillMarksItsReference(t *testing.T) {
	a, _ := importApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clipFrom := func(videoID string) models.Asset {
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: videoID + ".mp4", SHA256: ids.New("sha"),
			State: models.AssetAvailable, Media: models.MediaInfo{Duration: 2, HasVideo: true, HasAudio: true}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now,
			Source: &models.AssetSource{Platform: models.PlatformYouTube, URL: "https://www.youtube.com/watch?v=" + videoID, VideoID: videoID, Tool: "yt-dlp", Method: models.SourceDownload}}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as
	}
	ask := func(videoID string) string {
		out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://youtu.be/" + videoID}, Reference: true})
		if err != nil || len(out.JobIDs) != 1 {
			t.Fatalf("import %+v %v", out, err)
		}
		return out.JobIDs[0]
	}
	marked := func(id string) bool {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
			t.Fatal(err)
		}
		return as.IsReference()
	}
	// The import made the clip, then stopped before marking it: tried again, it marks it.
	jobID := ask("retryvideo1")
	made := clipFrom("retryvideo1")
	if err := a.DB.Insert(ctx, store.CIngest, models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), SourceName: made.Name, AssetID: made.ID,
		State: "available", JobID: jobID, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	runImports(t, a, []string{jobID})
	if !marked(made.ID) {
		t.Fatal("the clip this import made isn't marked a reference after it was tried again")
	}
	// The person's own clip of that video, imported earlier: never marked.
	own := clipFrom("ownvideo001")
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": own.ID}, bson.M{"$set": bson.M{"title": "Our launch"}}); err != nil {
		t.Fatal(err)
	}
	out, err := call[linkimport.ImportOut](t, a, "ingest.url", linkimport.ImportIn{URLs: []string{"https://youtu.be/ownvideo001"}, Reference: true})
	if err != nil || len(out.Imports) != 1 || out.Imports[0].Status != "already" {
		t.Fatalf("already here: %+v %v", out, err)
	}
	runImports(t, a, out.JobIDs)
	if marked(own.ID) {
		t.Fatal("a clip the import didn't make was marked a reference")
	}
}
