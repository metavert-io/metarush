package wsarchive

import (
	"context"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// OpRestore names the restore in the activity log and the change history.
const OpRestore = "workspace.restore"

// RecordRestore writes the restore into the copy itself, once it is done:
// an entry in its activity log and one in its change history (who restored
// it, on which computer, from which archive). A restore runs before any
// MetaRush works in the copy, so these are written directly.
func RecordRestore(ctx context.Context, db *store.DB, rep *RestoreReport, archive string, actor models.Actor, machine string) error {
	now := time.Now().UTC()
	facts := map[string]any{"archive": archive, "madeAt": rep.MadeAt, "madeBy": rep.MadeBy.Person, "madeOn": rep.MadeBy.Machine,
		"documents": rep.Documents, "media": rep.Media.Mode, "mediaFiles": rep.Media.Files, "missing": rep.Media.Missing,
		"fromLevel": rep.FromLevel, "toLevel": rep.ToLevel, "migrated": len(rep.Migrated)}
	ev := models.AuditEvent{ID: ids.New(ids.Audit), WorkspaceID: rep.Workspace.ID, At: now, Actor: actor, Action: OpRestore,
		TargetType: "workspace", TargetID: rep.Workspace.ID, Details: facts}
	if _, err := db.C(store.CAudit).InsertOne(ctx, ev); err != nil {
		return err
	}
	ch := logbook.ChannelCLI
	if actor.Via == "dashboard" {
		ch = logbook.ChannelDashboard
	}
	e := logbook.Entry{ID: ids.New(logbook.EntryPrefix), WorkspaceID: rep.Workspace.ID, At: now, Kind: logbook.KindOp, Level: logbook.LevelInfo,
		Channel: ch, UserID: actor.UserID, UserName: actor.UserName, InstallationID: actor.InstallationID, Machine: machine,
		Version: version.Current().Version, Summary: "restored the workspace from an export",
		Op: &logbook.OpInfo{Name: OpRestore, Mutating: true, Human: true, OK: true, Actions: []string{OpRestore},
			Output: &logbook.OutputInfo{Facts: facts, Counts: map[string]int{"collections": len(rep.Collections)}}}}
	_, err := db.C(store.CLogEntries).InsertOne(ctx, e)
	return err
}
