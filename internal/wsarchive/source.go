package wsarchive

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A source database is only ever read here: listCollections, listIndexes,
// find, count, aggregate and buildInfo, all of which MongoDB's read role
// allows. Nothing in this file writes.

// ReadLevel reads a database's migration level without changing it. A
// database with no migrations recorded has level 0.
func ReadLevel(ctx context.Context, db *store.DB) (MigrationLevel, error) {
	cur, err := db.C(store.CMigrations).Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return MigrationLevel{}, err
	}
	var docs []struct {
		ID        any       `bson:"_id"`
		Name      string    `bson:"name"`
		AppliedAt time.Time `bson:"appliedAt"`
	}
	if err := cur.All(ctx, &docs); err != nil {
		return MigrationLevel{}, err
	}
	l := MigrationLevel{Applied: []AppliedMigration{}, ThisMetaRush: store.SchemaVersion()}
	for _, d := range docs {
		v, ok := intOf(d.ID)
		if !ok {
			continue
		}
		l.Applied = append(l.Applied, AppliedMigration{Version: v, Name: d.Name, AppliedAt: d.AppliedAt.UTC()})
		l.Highest = max(l.Highest, v)
	}
	sort.Slice(l.Applied, func(i, j int) bool { return l.Applied[i].Version < l.Applied[j].Version })
	l.Older = len(Missing(l)) > 0
	return l, nil
}

func intOf(v any) (int, bool) {
	switch x := v.(type) {
	case int32:
		return int(x), true
	case int64:
		return int(x), true
	case int:
		return x, true
	case float64:
		return int(x), x == float64(int(x))
	}
	return 0, false
}

// Missing lists this MetaRush's migrations a level hasn't applied: what the
// restore runs forward.
func Missing(l MigrationLevel) []int {
	have := map[int]bool{}
	for _, m := range l.Applied {
		have[m.Version] = true
	}
	var out []int
	for _, m := range store.Migrations {
		if !have[m.Version] {
			out = append(out, m.Version)
		}
	}
	return out
}

// Unknown lists the applied migrations this MetaRush doesn't have: a
// database (or an archive) a newer MetaRush migrated.
func Unknown(l MigrationLevel) []int {
	known := map[int]bool{}
	for _, m := range store.Migrations {
		known[m.Version] = true
	}
	var out []int
	for _, m := range l.Applied {
		if !known[m.Version] {
			out = append(out, m.Version)
		}
	}
	return out
}

// ErrNewer is the plain refusal of a database or archive a newer MetaRush made.
func ErrNewer(what string, versions []int) error {
	return apperr.New(apperr.Conflict, "%s was brought up to date by a newer MetaRush than this one, so this MetaRush can't read it safely.", what).
		WithHint("Update MetaRush on this computer, then try again.").
		WithDetails(map[string]any{"reason": "newer_database", "migrations": versions, "thisMetaRush": store.SchemaVersion()}).
		WithCommand("metarush version")
}

// CheckReadable refuses a level a newer MetaRush made.
func CheckReadable(what string, l MigrationLevel) error {
	if u := Unknown(l); len(u) > 0 {
		return ErrNewer(what, u)
	}
	return nil
}

// Workspaces lists the workspaces a database holds.
func Workspaces(ctx context.Context, db *store.DB) ([]models.Workspace, error) {
	var out []models.Workspace
	cur, err := db.C(store.CWorkspaces).Find(ctx, bson.M{}, options.Find().SetProjection(bson.M{"_id": 1, "name": 1, "mediaStore": 1, "workspaceId": 1}).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	err = cur.All(ctx, &out)
	return out, err
}

// FindWorkspace reads one workspace's record (nil when there is none).
func FindWorkspace(ctx context.Context, db *store.DB, id string) (*models.Workspace, error) {
	var ws models.Workspace
	err := db.C(store.CWorkspaces).FindOne(ctx, bson.M{"_id": id}).Decode(&ws)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &ws, nil
}

// ChooseWorkspace picks the workspace to export: the one asked for, else the
// database's only one.
func ChooseWorkspace(ctx context.Context, db *store.DB, want string) (*models.Workspace, error) {
	if want != "" {
		ws, err := FindWorkspace(ctx, db, want)
		if err != nil {
			return nil, err
		}
		if ws == nil {
			return nil, apperr.New(apperr.NotFound, "That workspace isn't in this database.").WithDetails(map[string]any{"workspace": want, "database": db.Name})
		}
		return ws, nil
	}
	all, err := Workspaces(ctx, db)
	if err != nil {
		return nil, err
	}
	switch len(all) {
	case 0:
		return nil, apperr.New(apperr.NotFound, "This database holds no MetaRush workspace.").WithDetails(map[string]any{"database": db.Name})
	case 1:
		return FindWorkspace(ctx, db, all[0].ID)
	}
	var names []string
	list := []map[string]string{}
	for _, w := range all {
		names = append(names, "“"+w.Name+"”")
		list = append(list, map[string]string{"id": w.ID, "name": w.Name})
	}
	return nil, apperr.New(apperr.InvalidInput, "This database holds several workspaces: %s. Say which one to export.", strings.Join(names, ", ")).
		WithDetails(map[string]any{"workspaces": list}).WithCommand("metarush workspace export --workspace <id> …")
}

// CollectionInfo is a collection a database holds, as listCollections says.
type CollectionInfo struct {
	Name    string
	Type    string
	Options bson.Raw
}

// ListCollections lists a database's collections (system ones left out).
func ListCollections(ctx context.Context, db *store.DB) ([]CollectionInfo, error) {
	cur, err := db.DB.ListCollections(ctx, bson.D{})
	if err != nil {
		return nil, err
	}
	var specs []struct {
		Name    string   `bson:"name"`
		Type    string   `bson:"type"`
		Options bson.Raw `bson:"options"`
	}
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	var out []CollectionInfo
	for _, s := range specs {
		if strings.HasPrefix(s.Name, "system.") {
			continue
		}
		out = append(out, CollectionInfo{Name: s.Name, Type: s.Type, Options: s.Options})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// ListIndexes lists a collection's indexes but _id's, as listIndexes gives them.
func ListIndexes(ctx context.Context, db *store.DB, coll string) ([]bson.Raw, error) {
	cur, err := db.C(coll).Indexes().List(ctx)
	if err != nil {
		return nil, err
	}
	var specs []bson.Raw
	if err := cur.All(ctx, &specs); err != nil {
		return nil, err
	}
	out := specs[:0]
	for _, ix := range specs {
		if name, _ := ix.Lookup("name").StringValueOK(); name == "_id_" {
			continue
		}
		out = append(out, ix)
	}
	return out, nil
}

// workspaceFilter is the documents of one workspace.
func workspaceFilter(ws string) bson.M { return bson.M{"workspaceId": ws} }

// sized counts a workspace's documents in a collection and their size as
// stored (BSON bytes), reading them on the server.
func sized(ctx context.Context, db *store.DB, coll, ws string) (count, bytes int64, err error) {
	cur, err := db.C(coll).Aggregate(ctx, mongo.Pipeline{
		{{Key: "$match", Value: workspaceFilter(ws)}},
		{{Key: "$group", Value: bson.D{{Key: "_id", Value: nil}, {Key: "n", Value: bson.D{{Key: "$sum", Value: 1}}},
			{Key: "b", Value: bson.D{{Key: "$sum", Value: bson.D{{Key: "$bsonSize", Value: "$$ROOT"}}}}}}}},
	})
	if err != nil {
		return 0, 0, err
	}
	defer cur.Close(ctx)
	var res []struct {
		N int64 `bson:"n"`
		B int64 `bson:"b"`
	}
	if err := cur.All(ctx, &res); err != nil {
		return 0, 0, err
	}
	if len(res) == 0 {
		return 0, 0, nil
	}
	return res[0].N, res[0].B, nil
}

// unscoped counts documents that belong to no workspace (no workspaceId):
// they are named in the manifest, never silently left out.
func unscoped(ctx context.Context, db *store.DB, coll string) (int64, error) {
	return db.C(coll).CountDocuments(ctx, bson.M{"workspaceId": bson.M{"$exists": false}})
}

// otherWorkspaces counts documents of other workspaces in a collection.
func otherWorkspaces(ctx context.Context, db *store.DB, coll, ws string) (int64, error) {
	return db.C(coll).CountDocuments(ctx, bson.M{"workspaceId": bson.M{"$exists": true, "$ne": ws}})
}

// runningNow names the workspace's computers that ran MetaRush in the last
// few minutes (an export reads the database as it is: what changes meanwhile
// may be half in the archive).
func runningNow(ctx context.Context, db *store.DB, ws string, now time.Time) []string {
	var insts []models.Installation
	cur, err := db.C(store.CInstallations).Find(ctx, bson.M{"workspaceId": ws, "lastSeenAt": bson.M{"$gte": now.Add(-3 * time.Minute)}, "state": bson.M{"$ne": models.InstallationRevoked}})
	if err != nil || cur.All(ctx, &insts) != nil {
		return nil
	}
	var out []string
	for _, in := range insts {
		out = append(out, firstOf(in.Name, in.Hostname, in.ID))
	}
	sort.Strings(out)
	return out
}

func firstOf(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func plural(n int64, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return fmt.Sprintf("%s %s", thousands(n), many)
}

func thousands(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + thousands(-n)
	}
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
