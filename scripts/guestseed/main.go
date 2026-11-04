// Command guestseed gives a version of a throwaway installation a finished
// review copy, for scripts/guests-e2e.sh: the guest browser run needs a video
// to watch, and drawing one with the renderer would make the run long and
// heavy. The video is an ordinary MP4 the script made with ffmpeg, kept in the
// installation's media folder by its content address like a render's. It
// refuses anything but a local metarush-test-* database. A development helper
// (go run ./scripts/guestseed), not part of the metarush binary.
//
//	go run ./scripts/guestseed <mongodb-uri> <metarush-test-…> <media folder> <version's ID> <video.mp4>
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

func main() {
	if len(os.Args) != 6 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/guestseed <mongodb-uri> <metarush-test-name> <media folder> <version id> <video.mp4>")
		os.Exit(2)
	}
	uri, name, root, revID, video := os.Args[1], os.Args[2], os.Args[3], os.Args[4], os.Args[5]
	if !strings.HasPrefix(name, store.TestDBPrefix) || !(strings.Contains(uri, "127.0.0.1") || strings.Contains(uri, "localhost")) {
		fmt.Fprintf(os.Stderr, "refusing %q at %q: only local %s* databases are seeded\n", name, uri, store.TestDBPrefix)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		fail(err)
	}
	defer db.Close(ctx)
	var rev models.Revision
	if err := db.C(store.CRevisions).FindOne(ctx, bson.M{"_id": revID}).Decode(&rev); err != nil {
		fail(fmt.Errorf("no version %s: %w", revID, err))
	}
	fs, err := media.NewFS(root, false)
	if err != nil {
		fail(err)
	}
	sha, err := media.HashFile(video)
	if err != nil {
		fail(err)
	}
	st, err := os.Stat(video)
	if err != nil {
		fail(err)
	}
	key := media.ObjectKey(rev.WorkspaceID, sha)
	if _, err := fs.PutFile(ctx, key, video, media.PutOptions{SHA256: sha, ContentType: "video/mp4"}, media.Resume{}, nil); err != nil {
		fail(err)
	}
	now := time.Now().UTC()
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: rev.WorkspaceID, ProjectID: rev.ProjectID, RevisionID: rev.ID, RevisionNumber: rev.Number,
		SpecHash: rev.SpecHash, Preset: rev.Spec.Output.Preset, Quality: "review", State: models.RenderAvailable,
		Output: &models.RenderOutput{StorageKey: key, SHA256: sha, Size: st.Size(), Duration: rev.Duration, Width: 1280, Height: 720, FPS: 30},
		CreatedBy: models.Actor{UserID: "usr_e2e", UserName: "E2E", Via: "cli"}, CreatedAt: now, CompletedAt: &now}
	if err := db.Insert(ctx, store.CRenders, rd); err != nil {
		fail(err)
	}
	fmt.Printf("seeded: a review copy of version %d (%s, %d bytes)\n", rev.Number, rd.ID, st.Size())
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "guestseed:", err)
	os.Exit(1)
}
