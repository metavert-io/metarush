package resources

import (
	"context"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures fetched from Google Drive folders (D220): each Drive file a run
// used is a picture of its own, tied to the file by its Drive ID
// (Resource.driveFile), so new contents of the same file become a new version
// of the same picture and a file fetched before isn't fetched again while
// Google's checksum says it is the same. Migration 42 keeps one picture per
// Drive file.

// DriveFileResource is the picture a Google Drive file was fetched as (nil none).
func DriveFileResource(ctx context.Context, a *app.App, driveID string) *models.Resource {
	if driveID == "" {
		return nil
	}
	var r models.Resource
	if err := a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "driveFile.id": driveID}).Decode(&r); err != nil {
		return nil
	}
	return &r
}
