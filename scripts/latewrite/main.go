// Command latewrite plays a computer that slept through a move of the
// database (scripts/database-move-e2e.sh, D352–D358): it writes one project
// straight into the old database after the move marked it moved, past
// MetaRush's own refusal, the way a write sent just before the computer
// slept would land. It only writes into a local metarush-test-* database. It
// is a development helper (go run ./scripts/latewrite), not part of the
// metarush binary.
//
//	go run ./scripts/latewrite <mongodb-uri> <metarush-test-…> "<project name>"
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, `usage: go run ./scripts/latewrite <mongodb-uri> <metarush-test-name> "<project name>"`)
		os.Exit(2)
	}
	uri, name, project := os.Args[1], os.Args[2], os.Args[3]
	if !strings.HasPrefix(name, store.TestDBPrefix) || !store.IsLocalURI(uri) {
		fmt.Fprintf(os.Stderr, "refusing to write into %q: only a local %s* database\n", name, store.TestDBPrefix)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close(ctx)
	m, err := db.ReadMoveMarker(ctx)
	if err != nil || m == nil || m.State != store.MoveMoved {
		fmt.Fprintln(os.Stderr, "the database isn't marked moved: a late write comes only after a move")
		os.Exit(1)
	}
	// A copy of one of its projects, under a new ID and name: the raw
	// collection, not db.C, which refuses every write to a moved database.
	coll := db.DB.Collection(store.CProjects)
	var p bson.M
	if err := coll.FindOne(ctx, bson.M{}).Decode(&p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	now := time.Now().UTC()
	p["_id"], p["name"], p["createdAt"], p["updatedAt"] = ids.New(ids.Project), project, now, now
	if _, err := coll.InsertOne(ctx, p); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(p["_id"])
}
