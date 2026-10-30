package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// movedForLooks moves the workspace's database from the studio into a new
// test database and opens the studio again on it (as the next command there
// would). The moving App still reads the old database.
func movedForLooks(t *testing.T) (w *moveWorkspace, dst *store.DB, n *app.App, out *ops.DatabaseMoveOut) {
	t.Helper()
	w = newMoveWorkspace(t)
	ctx := context.Background()
	dst = moveDB(t)
	if err := ops.SaveMoveAddress(w.a.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	out, err := ops.CallTyped[ops.DatabaseMoveOut](ctx, w.a, "database.move", ops.DatabaseMoveIn{DatabaseTargetIn: ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: dst.Name}}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	n, err = app.Open(ctx, w.a.Cfg.Home(), "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(n.Close)
	if n.DB.Name != dst.Name {
		t.Fatalf("the studio opens %s", n.DB.Name)
	}
	return w, dst, n, out
}

func moveOf(t *testing.T, a *app.App) string {
	t.Helper()
	m, err := a.DB.ReadMoveMarker(context.Background())
	if err != nil || m == nil {
		t.Fatalf("marker: %v", err)
	}
	return m.Move
}

func lateNotes(t *testing.T, db *store.DB) []models.Notification {
	t.Helper()
	var ns []models.Notification
	cur, err := db.C(store.CNotifications).Find(context.Background(), bson.M{"kind": models.NotifyLateWrites})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(context.Background(), &ns); err != nil {
		t.Fatal(err)
	}
	return ns
}

// oldLogCount is how many activity log entries the old database holds.
func oldLogCount(t *testing.T, db *store.DB) int64 {
	t.Helper()
	n, err := db.C(store.CLogEntries).CountDocuments(context.Background(), bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// The move takes its first look right after it switched (nothing found: no
// bell note) and keeps the old database's address for the later ones. A
// comment changed and one added in the old database after it was marked
// moved — as a computer that slept through the move would — are found by the
// next look an owner asks for, in plain words, with a bell note; the look
// changes nothing in the old database. Carrying the changed one over puts the
// old database's version here (recorded in the change history and the
// activity log); a project changed in both databases is refused by check and
// set and both versions are shown; leaving it decides it.
func TestLateWriteIsFoundCarriedOverOrRefused(t *testing.T) {
	w, dst, n, out := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	if out.Late == nil || out.Late.Waiting != 0 || len(out.Late.Looks) != 1 || out.Late.Looks[0].When != ops.LateSoon || !out.Late.Here || out.Late.Next == nil {
		t.Fatalf("the first look: %+v", out.Late)
	}
	if !ops.KeepsLookAddress(a.Cfg, move) {
		t.Fatal("the old database's address isn't kept for the looks")
	}
	if len(lateNotes(t, dst)) != 0 {
		t.Fatal("a bell note with nothing found")
	}
	if !slices.ContainsFunc(out.Next, func(s string) bool { return strings.Contains(s, "looks at the old database again") }) {
		t.Fatalf("the move doesn't say it looks again: %v", out.Next)
	}

	// Written into the old database after it was marked moved.
	old := a.DB.DB // past the freeze, as a computer that slept through the move would
	var comment bson.M
	if err := old.Collection(store.CComments).FindOne(ctx, bson.M{}).Decode(&comment); err != nil {
		t.Fatal(err)
	}
	late := time.Now().UTC().Truncate(time.Millisecond)
	if _, err := old.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": comment["_id"]}, bson.M{"$set": bson.M{"text": "Lena: tighten the intro, and the end", "updatedAt": late}}); err != nil {
		t.Fatal(err)
	}
	added := ids.New("cmt")
	if _, err := old.Collection(store.CComments).InsertOne(ctx, bson.M{"_id": added, "workspaceId": a.WorkspaceID(), "text": "Sam: the logo is late", "createdAt": late,
		"installationId": w.b.Cfg.Installation.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Spring launch (old)"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := dst.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Spring launch (new)"}}); err != nil {
		t.Fatal(err)
	}
	before, logs := hashBut(t, a.DB), oldLogCount(t, a.DB)

	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if !lw.Looked || lw.Problem != "" || lw.Waiting != 3 || len(lw.Found) != 3 {
		t.Fatalf("the look: %+v", lw)
	}
	if hashBut(t, a.DB) != before || oldLogCount(t, a.DB) != logs {
		t.Fatal("a look changed the old database")
	}
	byRecord := map[string]ops.LateWriteView{}
	for _, f := range lw.Found {
		byRecord[f.Record] = f
	}
	changed, fresh, both := byRecord[comment["_id"].(string)], byRecord[added], byRecord[w.project]
	if changed.Kind != "changed" || changed.What != "a comment" || !changed.CanCarry || changed.CopyChanged || changed.At == nil || !changed.At.Equal(late) {
		t.Fatalf("the changed comment: %+v", changed)
	}
	if fresh.Kind != "new" || fresh.Computer != "Edit laptop" || !fresh.CanCarry || !strings.Contains(fresh.Says, "on Edit laptop") {
		t.Fatalf("the new comment: %+v", fresh)
	}
	if both.Kind != "changed" || both.CanCarry || !both.CopyChanged || !strings.Contains(both.Says, "changed here too") {
		t.Fatalf("the project changed on both sides: %+v", both)
	}
	notes := lateNotes(t, dst)
	if len(notes) != 1 || notes[0].UserID != a.Cfg.User.ID || !strings.Contains(notes[0].Body, "a comment, changed") || notes[0].Link != "/settings/storage#database" {
		t.Fatalf("the bell: %+v", notes)
	}
	// The same look again finds nothing new: no second note, the same numbers.
	lw2, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true}, ops.Caller{})
	if err != nil || len(lw2.Found) != 3 || lw2.Moves[0].Looks[len(lw2.Moves[0].Looks)-1].Found != 0 || len(lateNotes(t, dst)) != 1 {
		t.Fatalf("looking again: %+v %v", lw2, err)
	}

	// Both versions side by side (a person's).
	if _, err := ops.CallTyped[ops.DatabaseLateShowOut](ctx, n, "database.late.show", ops.DatabaseLateShowIn{ID: both.ID}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("both versions shown without a person: %v", err)
	}
	sh, err := ops.CallTyped[ops.DatabaseLateShowOut](ctx, n, "database.late.show", ops.DatabaseLateShowIn{ID: both.ID}, ops.Caller{Human: true})
	if err != nil || !slices.Contains(sh.Differs, "name") || !strings.Contains(string(sh.Old), "(old)") || !strings.Contains(string(sh.Here), "(new)") {
		t.Fatalf("show: %+v %v", sh, err)
	}

	// Carrying over: never Claude, an AI app or without a person.
	ai := *n
	ai.Actor.Via = models.ViaAssistant
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, &ai, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{changed.ID}}, ops.Caller{Human: true}); err == nil {
		t.Fatal("Claude carried a record over")
	}
	agent := *n
	agent.Actor.Agent = "codex"
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, &agent, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{changed.ID}}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("an AI app carried a record over: %v", err)
	}
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{changed.ID}}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("carried over without a person: %v", err)
	}
	// The changed comment and the new one, by their numbers.
	c, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{itoa(changed.N), itoa(fresh.N)}}, ops.Caller{Human: true})
	if err != nil || len(c.Carried) != 2 || len(c.Refused) != 0 {
		t.Fatalf("carry: %+v %v", c, err)
	}
	var here bson.M
	if err := dst.C(store.CComments).FindOne(ctx, bson.M{"_id": comment["_id"]}).Decode(&here); err != nil || here["text"] != "Lena: tighten the intro, and the end" {
		t.Fatalf("the comment here: %v %v", here, err)
	}
	if err := dst.C(store.CComments).FindOne(ctx, bson.M{"_id": added}).Decode(&here); err != nil || here["text"] != "Sam: the logo is late" {
		t.Fatalf("the new comment here: %v %v", here, err)
	}
	if k, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "database.late.carry"}); k != 2 {
		t.Fatalf("%d carry-overs in the change history", k)
	}
	n.Log.Flush(ctx)
	if k, _ := dst.C(store.CLogEntries).CountDocuments(ctx, bson.M{"op.name": "database.late.carry"}); k == 0 {
		t.Fatal("the carry-over isn't in the activity log")
	}
	// Carried already: refused as decided.
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{changed.ID}}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("carried twice: %v", err)
	}
	// Changed here too: refused, nothing changes.
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{both.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "changed_here" || !strings.Contains(apperr.From(err).Details.(map[string]any)["id"].(string), "lwr_") {
		t.Fatalf("a record changed on both sides: %v", err)
	}
	var p bson.M
	if err := dst.C(store.CProjects).FindOne(ctx, bson.M{"_id": w.project}).Decode(&p); err != nil || p["name"] != "Spring launch (new)" {
		t.Fatalf("the project here changed: %v %v", p, err)
	}
	// A change here between the look and the carry is caught by the check and set too.
	lv, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{both.ID}}, ops.Caller{Human: true})
	if err != nil || len(lv.Left) != 1 || lv.Left[0].State != "left" {
		t.Fatalf("leave: %+v %v", lv, err)
	}
	if k, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "database.late.leave"}); k != 1 {
		t.Fatal("leaving isn't in the change history")
	}
	st, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, n, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
	if err != nil || st.Late == nil || st.Late.Waiting != 0 || st.Late.Found != 3 {
		t.Fatalf("the status: %+v %v", st, err)
	}
	if hashBut(t, a.DB) != before {
		t.Fatal("carrying over changed the old database")
	}
	// No contents of a record in what an AI may read.
	b, _ := json.Marshal(lw)
	for _, word := range []string{"tighten the intro", "logo is late", "Spring launch (old)"} {
		if strings.Contains(string(b), word) {
			t.Fatalf("the list quotes a record: %s", word)
		}
	}
}

func itoa(n int) string { return strconv.Itoa(n) }

// A record removed from the old database after the move is reported, never
// removed here: carrying it over is refused, leaving it decides it. And a
// carry-over checks and sets: a record here that changed between the look
// and the carry-over is never overwritten.
func TestLateDeleteIsReportedNeverDeleted(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	var comment bson.M
	if err := a.DB.DB.Collection(store.CComments).FindOneAndDelete(ctx, bson.M{}).Decode(&comment); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.DB.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Spring launch (old)"}}); err != nil {
		t.Fatal(err)
	}
	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true}, ops.Caller{})
	if err != nil || len(lw.Found) != 2 {
		t.Fatalf("the look: %+v %v", lw, err)
	}
	var f, project ops.LateWriteView
	for _, x := range lw.Found {
		if x.Record == w.project {
			project = x
		} else {
			f = x
		}
	}
	if !project.CanCarry {
		t.Fatalf("the project: %+v", project)
	}
	if _, err := dst.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"brief": "changed after the look"}}); err != nil {
		t.Fatal(err)
	}
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{itoa(project.N)}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "changed_here" {
		t.Fatalf("carried over a record changed since the look: %v", err)
	}
	var p bson.M
	if err := dst.C(store.CProjects).FindOne(ctx, bson.M{"_id": w.project}).Decode(&p); err != nil || p["brief"] != "changed after the look" || p["name"] == "Spring launch (old)" {
		t.Fatalf("the project here: %v %v", p, err)
	}
	if f.Kind != "gone" || f.CanCarry || f.Record != comment["_id"] || !strings.Contains(f.Says, "never removes it by itself") {
		t.Fatalf("the removed comment: %+v", f)
	}
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "gone" {
		t.Fatalf("a removal carried over: %v", err)
	}
	if k, _ := dst.C(store.CComments).CountDocuments(ctx, bson.M{"_id": comment["_id"]}); k != 1 {
		t.Fatal("the comment was removed here")
	}
	if len(lateNotes(t, dst)) != 1 {
		t.Fatal("no bell note for a removal")
	}
	if _, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{All: true}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if k, _ := dst.C(store.CComments).CountDocuments(ctx, bson.M{"_id": comment["_id"]}); k != 1 {
		t.Fatal("leaving it removed the comment here")
	}
}

// The looks run on their schedule on the computer that moved (MetaRush in
// the background): none before the first is due, then at 15 minutes, an hour
// and a day. The old database's address and the fingerprints stay on this
// computer until the last look, and are dropped after it. With nothing found,
// there is never a bell note. Another computer can't look: it doesn't keep
// the address.
func TestLateLooksKeepTheAddressUntilTheLast(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	prints := filepath.Join(n.Cfg.StateDir(), "database-move", move+".prints")
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil || len(rec.Due) != 3 {
		t.Fatalf("the looks to come: %+v %v", rec, err)
	}
	looks := func() int {
		var r struct {
			Looks []bson.M `bson:"looks"`
			Ended bool     `bson:"ended"`
		}
		_ = dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&r)
		return len(r.Looks)
	}
	kept := func() bool {
		_, err := os.Stat(prints)
		return ops.KeepsLookAddress(n.Cfg, move) && err == nil
	}
	if said := ops.LookDueLate(ctx, n, rec.Due[0].Add(-time.Minute)); len(said) != 0 || looks() != 1 {
		t.Fatalf("a look before the first was due: %v", said)
	}
	for i, due := range rec.Due {
		said := ops.LookDueLate(ctx, n, due.Add(time.Second))
		if len(said) != 1 || !strings.Contains(said[0], "nothing new") || looks() != i+2 {
			t.Fatalf("look %d: %v (%d looks)", i+1, said, looks())
		}
		if last := i == len(rec.Due)-1; kept() == last {
			t.Fatalf("after look %d the address is kept: %v", i+1, kept())
		}
	}
	st := ops.LateStatus(ctx, n)
	if st == nil || !st.Ended || st.Next != nil || !strings.Contains(st.Summary, "no longer keeps its address") {
		t.Fatalf("after the last look: %+v", st)
	}
	if said := ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Hour)); len(said) != 0 {
		t.Fatalf("a look after the last: %v", said)
	}
	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true}, ops.Caller{})
	if err != nil || lw.Looked || !strings.Contains(lw.Problem, "no longer looks") {
		t.Fatalf("asking after the last look: %+v %v", lw, err)
	}
	if len(lateNotes(t, dst)) != 0 {
		t.Fatal("a bell note with nothing found")
	}
	// The laptop, once switched, never looks: the studio keeps the address.
	if err := ops.SaveMoveAddress(w.b.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[ops.DatabaseSwitchOut](ctx, w.b, "database.switch", ops.DatabaseSwitchIn{}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	b2, err := app.Open(ctx, w.b.Cfg.Home(), "cli")
	if err != nil {
		t.Fatal(err)
	}
	defer b2.Close()
	if said := ops.LookDueLate(ctx, b2, rec.Due[0].Add(time.Second)); len(said) != 0 {
		t.Fatalf("the laptop looked: %v", said)
	}
}

// A look that can't reach the old database is tried again later, and the
// looks end a day after the last one was due: the address is dropped then.
func TestLateLooksEndWhenTheOldDatabaseIsGone(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	ctx := context.Background()
	move := moveOf(t, w.a)
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	// The old database's address now names a server that doesn't answer.
	if err := ops.KeepLookAddressForTest(n.Cfg, move, "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=300&connectTimeoutMS=300"); err != nil {
		t.Fatal(err)
	}
	said := ops.LookDueLate(ctx, n, rec.Due[0].Add(time.Second))
	if len(said) != 1 || !strings.Contains(said[0], "couldn't look") {
		t.Fatalf("a look that couldn't be made: %v", said)
	}
	if said := ops.LookDueLate(ctx, n, rec.Due[0].Add(2*time.Second)); len(said) != 0 {
		t.Fatalf("tried again at once: %v", said)
	}
	st := ops.LateStatus(ctx, n)
	if st == nil || st.Ended || st.Problem == "" {
		t.Fatalf("after a look that couldn't be made: %+v", st)
	}
	said = ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+time.Minute))
	if len(said) != 1 || !strings.Contains(said[0], "stopped looking") || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("the looks' end: %v", said)
	}
	if st := ops.LateStatus(ctx, n); st == nil || !st.Ended || !strings.Contains(st.EndedWhy, "couldn't reach") {
		t.Fatalf("ended: %+v", st)
	}
	// Without the fingerprints (dropped above) no look could ever be made:
	// the looks end at once rather than being tried again for a day.
	if _, err := dst.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": move}, bson.M{"$set": bson.M{"ended": false}}); err != nil {
		t.Fatal(err)
	}
	if err := ops.KeepLookAddressForTest(n.Cfg, move, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	said = ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+15*time.Minute))
	if len(said) != 1 || !strings.Contains(said[0], "stopped looking") || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("without the fingerprints: %v", said)
	}
	if st := ops.LateStatus(ctx, n); st == nil || !st.Ended || !strings.Contains(st.EndedWhy, "no longer keeps") {
		t.Fatalf("ended without the fingerprints: %+v", st)
	}
}
