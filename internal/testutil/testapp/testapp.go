// Package testapp opens a throwaway MetaRush app for tests: a metarush-test-*
// database on the local test MongoDB (dropped afterwards) and a filesystem
// media store and home folder in temp directories.
package testapp

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// New opens the app. The config is also saved to the home folder so code that
// edits config.yaml (watch.set, worker.configure) has a file to edit.
func New(t testing.TB) *app.App {
	t.Helper()
	return NewWith(t, Options{})
}

// Options change how NewWith opens the app.
type Options struct {
	// DatabaseURI is how the app reaches its test database (a stand-in a test
	// can cut, internal/testutil/dblink); the database is still made and
	// dropped through the test MongoDB itself.
	DatabaseURI string
	// Media is the media store (a fake bucket, internal/testutil/fakebucket);
	// unset: a folder in the test's temp directory.
	Media *config.Media
}

// NewWith opens the app like New, with options.
func NewWith(t testing.TB, o Options) *app.App {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "tester"}
	uri := testutil.URI()
	if o.DatabaseURI != "" {
		uri = o.DatabaseURI
	}
	cfg.Database = config.Database{URI: uri, Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	if o.Media != nil {
		cfg.Media = *o.Media
	}
	cfg.Dashboard.Port = 4859
	if err := config.Save(h, cfg); err != nil {
		t.Fatal(err)
	}
	a, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// HandedOver makes a's computer one that handed its role to another computer
// of the workspace and stepped down (D346): a settled handover record, as
// receiving and stepping down leave it. It returns the other computer's ID.
func HandedOver(t testing.TB, a *app.App, toName string) string {
	t.Helper()
	ctx := context.Background()
	to := ids.New(ids.Installation)
	now := time.Now().UTC()
	if _, err := a.DB.C(store.CHandovers).InsertOne(ctx, bson.M{"_id": ids.New(ids.Handover), "workspaceId": a.WorkspaceID(), "kind": "handover",
		"from": a.Cfg.Installation.ID, "fromName": a.Cfg.Installation.Name, "to": to, "toName": toName, "state": "done",
		"startedAt": now, "receivedAt": now, "settledAt": now, "steppedDownAt": now, "moved": bson.M{}}); err != nil {
		t.Fatal(err)
	}
	return to
}
