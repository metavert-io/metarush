package ops_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	"github.com/metavert-io/metarush/internal/dbmove"
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
	return movedForLooksWith(t, nil)
}

// movedForLooksWith is movedForLooks with records added before the move.
func movedForLooksWith(t *testing.T, seed func(w *moveWorkspace)) (w *moveWorkspace, dst *store.DB, n *app.App, out *ops.DatabaseMoveOut) {
	t.Helper()
	w = newMoveWorkspace(t)
	if seed != nil {
		seed(w)
	}
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
	if st == nil || !st.Ended || st.Next != nil || !strings.Contains(st.Summary, "no longer keeps its address") || !strings.Contains(st.Summary, "Nothing was written") ||
		!st.AllClear || st.Checked != ops.LateCheckedEverything || st.CanLook {
		t.Fatalf("after the last look: %+v", st)
	}
	if said := ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Hour)); len(said) != 0 {
		t.Fatalf("a look after the last: %v", said)
	}
	// The move's record no longer promises looks that are over.
	ms, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, n, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
	if err != nil || ms.Run == nil || slices.ContainsFunc(ms.Run.Next, func(s string) bool { return strings.Contains(s, "looks at the old database again") }) {
		t.Fatalf("the status after the last look: %+v %v", ms, err)
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
	if !strings.Contains(st.Summary, "MetaRush couldn't reach") || strings.Contains(st.Summary, "metaRush") {
		t.Fatalf("a name in the middle of a sentence: %s", st.Summary)
	}
	// The first try once the last look was due: a day from here.
	said = ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Second))
	if len(said) != 1 || !strings.Contains(said[0], "couldn't look") || strings.Contains(said[0], "metaRush") || !ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("the last look: %v", said)
	}
	said = ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+time.Minute))
	if len(said) != 1 || !strings.Contains(said[0], "stopped looking") || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("the looks' end: %v", said)
	}
	st = ops.LateStatus(ctx, n)
	if st == nil || !st.Ended || !strings.Contains(st.EndedWhy, "couldn't reach") || st.AllClear || st.Checked != ops.LateCheckedPart || st.CanLook ||
		strings.Contains(st.Summary, "Nothing was written") || !strings.Contains(st.Summary, "couldn't check everything") || strings.Contains(st.Summary, "Look again") {
		t.Fatalf("ended: %+v", st)
	}
	if k := countNotes(t, dst, "late_incomplete:"+move); k != 1 {
		t.Fatalf("%d notes that it couldn't check everything", k)
	}
	var incomplete models.Notification
	if err := dst.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "late_incomplete:" + move}).Decode(&incomplete); err != nil || strings.Contains(incomplete.Body, "metaRush") {
		t.Fatalf("the note: %+v %v", incomplete, err)
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
	st = ops.LateStatus(ctx, n)
	if st == nil || !st.Ended || !strings.Contains(st.EndedWhy, "no longer keeps") || st.AllClear || strings.Contains(st.Summary, "Nothing was written") {
		t.Fatalf("ended without the fingerprints: %+v", st)
	}
}

func countNotes(t *testing.T, db *store.DB, group string) int64 {
	t.Helper()
	n, err := db.C(store.CNotifications).CountDocuments(context.Background(), bson.M{"groupKey": group})
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func lateRaw(t *testing.T, db *store.DB, id string) bson.M {
	t.Helper()
	var d bson.M
	if err := db.C(store.CLateWrites).FindOne(context.Background(), bson.M{"_id": id}).Decode(&d); err != nil {
		t.Fatal(err)
	}
	return d
}

func lookNow(t *testing.T, n *app.App) *ops.DatabaseLateWritesOut {
	t.Helper()
	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](context.Background(), n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true, Limit: 1000}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	return lw
}

func entryOf(t *testing.T, lw *ops.DatabaseLateWritesOut, coll string) ops.LateWriteView {
	t.Helper()
	for _, f := range lw.Found {
		if f.Collection == coll {
			return f
		}
	}
	t.Fatalf("nothing of %s in %+v", coll, lw.Found)
	return ops.LateWriteView{}
}

// Kinds of record that hold sign-in secrets — a password, a sign-in link, an
// upload's address — are only reported: no copy and no ID are kept, nothing is
// shown, nothing is carried over, and the words say what to do. Any other
// record is shown without what MetaRush never shows (a review link's key) and
// with anything shaped like a key taken out; a copy is let go once a person
// decided.
func TestLateSignInSecretsAreOnlyReported(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	ws := a.WorkspaceID()
	old := a.DB.DB
	const (
		lateHash = "$argon2id$v=19$m=65536,t=1,p=4$c2FsdA$bGF0ZQ"
		linkID   = "tok_thelinkitself"
		upload   = "https://storage.example.com/upload/resumable-session-address"
		capKey   = "capability-of-the-review-link"
	)
	address := "mongodb://lena:" + "hunter2" + "word@db.example.com/studio"
	if _, err := old.Collection(store.CUserCredentials).UpdateOne(ctx, bson.M{}, bson.M{"$set": bson.M{"hash": lateHash}}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Collection(store.CAuthTokens).InsertOne(ctx, bson.M{"_id": linkID, "workspaceId": ws, "kind": "reset", "purgeAt": time.Now().UTC().Add(48 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := old.Collection(store.CIngest).InsertOne(ctx, bson.M{"_id": ids.New("ing"), "workspaceId": ws, "sessionUri": upload}); err != nil {
		t.Fatal(err)
	}
	link := ids.New("rvl")
	if _, err := old.Collection(store.CReviewLinks).InsertOne(ctx, bson.M{"_id": link, "workspaceId": ws, "key": capKey, "title": "For Sam", "apiToken": "invented-token-value",
		"settings": bson.M{"clientSecret": "nested-secret-value", "tokenLimit": int32(5), "color": "blue"}}); err != nil {
		t.Fatal(err)
	}
	comment := ids.New("cmt")
	if _, err := old.Collection(store.CComments).InsertOne(ctx, bson.M{"_id": comment, "workspaceId": ws, "text": "Use " + address}); err != nil {
		t.Fatal(err)
	}
	lw := lookNow(t, n)
	if len(lw.Found) != 5 {
		t.Fatalf("found: %+v", lw.Found)
	}
	listed, _ := json.Marshal(lw)
	for _, secret := range []string{lateHash, linkID, upload, capKey, "hunter2"} {
		if strings.Contains(string(listed), secret) {
			t.Fatalf("the list holds %q", secret)
		}
	}
	for coll, words := range map[string]string{store.CUserCredentials: "never carries passwords over", store.CAuthTokens: "send the invitation or the reset again", store.CIngest: "add the file again here"} {
		f := entryOf(t, lw, coll)
		if !f.ReportOnly || f.CanCarry || f.Record != "" || !strings.Contains(f.Says, words) {
			t.Fatalf("%s: %+v", coll, f)
		}
		raw := lateRaw(t, dst, f.ID)
		if raw["old"] != nil || raw["key"] != nil || raw["kept"] != false {
			t.Fatalf("%s kept something: %v", coll, raw)
		}
		sh, err := ops.CallTyped[ops.DatabaseLateShowOut](ctx, n, "database.late.show", ops.DatabaseLateShowIn{ID: f.ID}, ops.Caller{Human: true})
		if err != nil || sh.Old != nil || sh.Here != nil || !strings.Contains(sh.Summary, words) {
			t.Fatalf("%s shown: %+v %v", coll, sh, err)
		}
		_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
		if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "report_only" {
			t.Fatalf("%s carried over: %v", coll, err)
		}
	}
	if k, _ := dst.C(store.CUserCredentials).CountDocuments(ctx, bson.M{"hash": lateHash}); k != 0 {
		t.Fatal("the late password went into this database")
	}
	if k, _ := dst.C(store.CAuthTokens).CountDocuments(ctx, bson.M{"_id": linkID}); k != 0 {
		t.Fatal("the late sign-in link went into this database")
	}
	// Another kind: shown without its capability, and anything shaped like a key taken out.
	rl := entryOf(t, lw, store.CReviewLinks)
	sh, err := ops.CallTyped[ops.DatabaseLateShowOut](ctx, n, "database.late.show", ops.DatabaseLateShowIn{ID: rl.ID}, ops.Caller{Human: true})
	if err != nil || strings.Contains(string(sh.Old), capKey) || !strings.Contains(string(sh.Old), "For Sam") || !slices.Contains(sh.Hidden, "key") {
		t.Fatalf("the review link shown: %s %+v %v", sh.Old, sh, err)
	}
	if strings.Contains(string(sh.Old), "invented-token-value") || strings.Contains(string(sh.Old), "nested-secret-value") || !slices.Contains(sh.Hidden, "apiToken") || !slices.Contains(sh.Hidden, "settings.clientSecret") ||
		!strings.Contains(string(sh.Old), "tokenLimit") || !strings.Contains(string(sh.Old), "blue") {
		t.Fatalf("what looks like a secret, shown: %s, hidden %v", sh.Old, sh.Hidden)
	}
	cm := entryOf(t, lw, store.CComments)
	sh, err = ops.CallTyped[ops.DatabaseLateShowOut](ctx, n, "database.late.show", ops.DatabaseLateShowIn{ID: cm.ID}, ops.Caller{Human: true})
	if err != nil || strings.Contains(string(sh.Old), "hunter2") || !strings.Contains(string(sh.Old), "db.example.com") {
		t.Fatalf("the comment shown: %s %v", sh.Old, err)
	}
	// A decision lets the copy go: carried or left alike.
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{rl.ID}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if raw := lateRaw(t, dst, rl.ID); raw["old"] != nil || raw["kept"] != false || raw["state"] != ops.LateCarried {
		t.Fatalf("carried: %v", raw)
	}
	var carried bson.M
	if err := dst.C(store.CReviewLinks).FindOne(ctx, bson.M{"_id": link}).Decode(&carried); err != nil || carried["key"] != capKey {
		t.Fatalf("the link here: %v %v", carried, err)
	}
	if _, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{cm.ID}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if raw := lateRaw(t, dst, cm.ID); raw["old"] != nil || raw["kept"] != false || raw["state"] != ops.LateLeft {
		t.Fatalf("left: %v", raw)
	}
}

// One late write of a clip's whole transcript is more than a look keeps: it
// says how many more it found, later looks keep going past what is kept, and
// the clip's lines are one entry that goes over whole or not at all — never
// while some aren't listed, never while one changed here too.
func TestLateManyRecordsKeepGoingAndGoOverWhole(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	ws := a.WorkspaceID()
	var lines []any
	for i := 0; i < 120; i++ {
		lines = append(lines, bson.M{"_id": fmt.Sprintf("seg_a%05d", i), "workspaceId": ws, "assetId": "ast_long", "text": "a line"})
	}
	first := "seg_a00000"
	for i := 0; i < 3; i++ {
		lines = append(lines, bson.M{"_id": fmt.Sprintf("seg_b%05d", i), "workspaceId": ws, "assetId": "ast_short", "text": "another"})
	}
	clash := "seg_b00000"
	if _, err := a.DB.DB.Collection(store.CSegments).InsertMany(ctx, lines); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateKeepPerLook(50)
	defer undo()
	lw := lookNow(t, n)
	m := lw.Moves[len(lw.Moves)-1]
	if m.More != 73 || m.Found != 50 || !strings.Contains(m.Summary, "73 more") || len(lw.Found) != 1 || lw.Found[0].GroupSize != 50 {
		t.Fatalf("the first look: %+v %+v", m, lw.Found)
	}
	if !slices.ContainsFunc(lw.Steps, func(s string) bool { return strings.Contains(s, "Not everything") }) {
		t.Fatalf("no word that more is coming: %v", lw.Steps)
	}
	// Not while some of a group aren't listed: nothing goes over in part.
	_, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{"1"}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "not_all_listed" {
		t.Fatalf("carried part of a transcript: %v", err)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_long"}); k != 0 {
		t.Fatalf("%d lines went over", k)
	}
	lw = lookNow(t, n)
	if m := lw.Moves[len(lw.Moves)-1]; m.More != 23 || m.Found != 100 {
		t.Fatalf("the second look: %+v", m)
	}
	lw = lookNow(t, n)
	if m := lw.Moves[len(lw.Moves)-1]; m.More != 0 || m.Found != 123 {
		t.Fatalf("the third look: %+v", m)
	}
	var long, short ops.LateWriteView
	for _, f := range lw.Found {
		if f.GroupSize == 120 {
			long = f
		}
		if f.GroupSize == 3 {
			short = f
		}
	}
	if long.ID == "" || short.ID == "" || !strings.Contains(long.Says, "120 records of the same clip's transcript") || long.GroupWhat == "" {
		t.Fatalf("the groups: %+v", lw.Found)
	}
	// One line of the short clip is here already, as another record: nothing of it goes over.
	if _, err := dst.C(store.CSegments).InsertOne(ctx, bson.M{"_id": clash, "workspaceId": ws, "assetId": "ast_short", "text": "made here"}); err != nil {
		t.Fatal(err)
	}
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{short.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "changed_here" {
		t.Fatalf("part of a group: %v", err)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_short"}); k != 1 {
		t.Fatalf("%d lines of the short clip here", k)
	}
	// The whole transcript, at once, by the one entry.
	c, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{long.ID}}, ops.Caller{Human: true})
	if err != nil || len(c.Carried) != 1 || c.Carried[0].GroupSize != 120 || !strings.Contains(c.Summary, "120 records") {
		t.Fatalf("carry: %+v %v", c, err)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_long"}); k != 120 {
		t.Fatalf("%d of 120 lines went over", k)
	}
	if k, _ := dst.C(store.CLateWrites).CountDocuments(ctx, bson.M{"group": bson.M{"$regex": "ast_long"}, "old": bson.M{"$exists": true}}); k != 0 {
		t.Fatalf("%d copies kept after carrying over", k)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"_id": first}); k != 1 {
		t.Fatal("the first line isn't here")
	}
	if k, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "database.late.carry"}); k != 120 {
		t.Fatalf("%d audit entries for 120 records carried over", k)
	}
	lf, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{short.ID}}, ops.Caller{Human: true})
	if err != nil || !strings.Contains(lf.Summary, "3 records") {
		t.Fatalf("leave: %+v %v", lf, err)
	}
}

// A finding that isn't true any more settles by itself: a record back to the
// version the move copied (its whole kind's hash matching again), a new one
// removed again. Only the version the latest look saw carries over; one found
// again after it settled waits again. A look racing a carry-over wins: the
// older version never goes over. One look runs at a time. Copies nobody
// decided on are let go after their time — never while background deletions
// are paused — and then can't be carried over.
func TestLateFindingsSettleAndCopiesAreLetGo(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	old := a.DB.DB
	var comment bson.M
	if err := old.Collection(store.CComments).FindOne(ctx, bson.M{}).Decode(&comment); err != nil {
		t.Fatal(err)
	}
	moved := comment["text"]
	setComment := func(text any) {
		t.Helper()
		if _, err := old.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": comment["_id"]}, bson.M{"$set": bson.M{"text": text}}); err != nil {
			t.Fatal(err)
		}
	}
	added := ids.New("cmt")
	setComment("first late words")
	if _, err := old.Collection(store.CComments).InsertOne(ctx, bson.M{"_id": added, "workspaceId": a.WorkspaceID(), "text": "new"}); err != nil {
		t.Fatal(err)
	}
	lw := lookNow(t, n)
	if lw.Waiting != 2 {
		t.Fatalf("the look: %+v", lw.Found)
	}
	var f, nw ops.LateWriteView
	for _, x := range lw.Found {
		if x.Record == added {
			nw = x
		} else {
			f = x
		}
	}
	// Back as it was, and removed again: both settle, copies gone; the whole kind's hash matches.
	setComment(moved)
	if _, err := old.Collection(store.CComments).DeleteOne(ctx, bson.M{"_id": added}); err != nil {
		t.Fatal(err)
	}
	lw = lookNow(t, n)
	if lw.Waiting != 0 || lw.Moves[0].Looks[len(lw.Moves[0].Looks)-1].Settled != 2 {
		t.Fatalf("settling: %+v %+v", lw.Moves, lw.Found)
	}
	for _, id := range []string{f.ID, nw.ID} {
		if raw := lateRaw(t, dst, id); raw["state"] != ops.LateSettled || raw["old"] != nil || raw["kept"] != false {
			t.Fatalf("settled: %v", raw)
		}
	}
	// The bell stops asking, without ringing again.
	for _, note := range lateNotes(t, dst) {
		if strings.Contains(note.Body, "carry it over") || !strings.Contains(note.Title, "Nothing from the old database waits") || note.Tone != "info" {
			t.Fatalf("the bell after settling: %+v", note)
		}
	}
	_, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "decided" {
		t.Fatalf("carried what settled: %v", err)
	}
	var here bson.M
	if err := dst.C(store.CComments).FindOne(ctx, bson.M{"_id": comment["_id"]}).Decode(&here); err != nil || here["text"] != moved {
		t.Fatalf("the comment here: %v %v", here, err)
	}
	// Found again: it waits again, and only the newest version goes over.
	setComment("second late words")
	lw = lookNow(t, n)
	if lw.Waiting != 1 {
		t.Fatalf("found again: %+v", lw.Found)
	}
	setComment("third late words")
	lookNow(t, n)
	// A look that finds a newer version while a carry-over reads the one before wins.
	undo := ops.SetLateCarryHook(func(id string) {
		_, _ = dst.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"print": "a-newer-version"}})
	})
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
	undo()
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "changed_again" {
		t.Fatalf("an older version carried over: %v", err)
	}
	if err := dst.C(store.CComments).FindOne(ctx, bson.M{"_id": comment["_id"]}).Decode(&here); err != nil || here["text"] != moved {
		t.Fatalf("the comment here after the race: %v %v", here, err)
	}
	if raw := lateRaw(t, dst, f.ID); raw["state"] != ops.LateWaiting {
		t.Fatalf("after the race: %v", raw)
	}
	// One look at a time.
	if _, err := dst.C(store.CLateLooks).UpdateOne(ctx, bson.M{}, bson.M{"$set": bson.M{"lookingUntil": time.Now().UTC().Add(time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	busy := lookNow(t, n)
	if busy.Looked || !strings.Contains(busy.Problem, "right now") {
		t.Fatalf("two looks at once: %+v", busy)
	}
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	_ = dst.C(store.CLateLooks).FindOne(ctx, bson.M{}).Decode(&rec)
	if said := ops.LookDueLate(ctx, n, rec.Due[0].Add(time.Second)); len(said) != 0 {
		t.Fatalf("a scheduled look beside a running one: %v", said)
	}
	if _, err := dst.C(store.CLateLooks).UpdateOne(ctx, bson.M{}, bson.M{"$unset": bson.M{"lookingUntil": ""}}); err != nil {
		t.Fatal(err)
	}
	// The newest version, once the look has it, goes over.
	lookNow(t, n)
	c, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
	if err != nil || len(c.Carried) != 1 {
		t.Fatalf("carry: %+v %v", c, err)
	}
	if err := dst.C(store.CComments).FindOne(ctx, bson.M{"_id": comment["_id"]}).Decode(&here); err != nil || here["text"] != "third late words" {
		t.Fatalf("the comment here: %v %v", here, err)
	}
	// Copies nobody decided on: kept their time, not while deletions are paused, then let go.
	project := w.project
	if _, err := old.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": project}, bson.M{"$set": bson.M{"name": "Late name"}}); err != nil {
		t.Fatal(err)
	}
	lw = lookNow(t, n)
	p := entryOf(t, lw, store.CProjects)
	if p.CopyUntil == nil || !p.CanCarry {
		t.Fatalf("the project: %+v", p)
	}
	after := p.CopyUntil.Add(time.Hour)
	if k := ops.DropOldLateCopies(ctx, n, p.CopyUntil.Add(-time.Hour)); k != 0 {
		t.Fatalf("%d copies let go early", k)
	}
	if err := dst.PauseDeletions(ctx, a.WorkspaceID(), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if k := ops.DropOldLateCopies(ctx, n, after); k != 0 {
		t.Fatalf("%d copies let go while deletions are paused", k)
	}
	if _, err := dst.C(store.CWorkspaces).UpdateOne(ctx, bson.M{"_id": a.WorkspaceID()}, bson.M{"$unset": bson.M{"deletionsPaused": ""}}); err != nil {
		t.Fatal(err)
	}
	if k := ops.DropOldLateCopies(ctx, n, after); k != 1 {
		t.Fatalf("%d copies let go", k)
	}
	if raw := lateRaw(t, dst, p.ID); raw["old"] != nil || raw["copyDropped"] != true || raw["state"] != ops.LateWaiting {
		t.Fatalf("let go: %v", raw)
	}
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{p.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "no_copy" {
		t.Fatalf("carried without a copy: %v", err)
	}
	lw, err = ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
	if err != nil || !strings.Contains(entryOf(t, lw, store.CProjects).Says, "30 days") || lw.Moves[0].CopyDays != 30 {
		t.Fatalf("the words: %+v %v", lw, err)
	}
}

// ten comments, seeded before a move: a look reading one record at a time can't
// get through them in a few looks.
func tenComments(w *moveWorkspace) {
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if _, err := w.a.DB.C(store.CComments).InsertOne(ctx, bson.M{"_id": fmt.Sprintf("cmt_a%05d", i), "workspaceId": w.a.WorkspaceID(), "text": "seeded"}); err != nil {
			panic(err)
		}
	}
}

// A look in the background keeps to its bounds: it stops, says so, settles
// nothing, isn't tried again; a person's look reads on. A last look that
// stopped part-way keeps the address so a person can look at everything, and
// the looks end when one does, or a day later.
func TestLateLooksKeepToTheirBounds(t *testing.T) {
	w, dst, n, _ := movedForLooksWith(t, tenComments)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	if _, err := a.DB.DB.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Late name"}}); err != nil {
		t.Fatal(err)
	}
	lw := lookNow(t, n)
	if lw.Waiting != 1 {
		t.Fatalf("the look: %+v", lw.Found)
	}
	// Back as it was: only a look that sees everything may say so.
	if _, err := a.DB.DB.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Spring launch"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "another late change"}}); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateBackgroundBytes(1)
	defer undo()
	for i, due := range rec.Due {
		said := ops.LookDueLate(ctx, n, due.Add(time.Second))
		if len(said) != 1 || !strings.Contains(said[0], "part of it") || !strings.Contains(said[0], "after reading") {
			t.Fatalf("look %d: %v", i, said)
		}
	}
	st := ops.LateStatus(ctx, n)
	if st == nil || !st.Partial || !strings.Contains(st.Stopped, "after reading") || !strings.Contains(st.Summary, "stopped part-way") || st.Ended || st.Waiting != 1 {
		t.Fatalf("stopped part-way: %+v", st)
	}
	if !ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatal("the address was dropped after a look that didn't see everything")
	}
	if said := ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Hour)); len(said) != 0 {
		t.Fatalf("tried again by itself: %v", said)
	}
	// A person looks: all of it, within far wider bounds; what went back settles; the looks end.
	lw = lookNow(t, n)
	if !lw.Looked || lw.Waiting != 1 {
		t.Fatalf("a person's look: %+v", lw)
	}
	if m := lw.Moves[0]; m.Partial || !m.Ended || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("after a person's look that saw everything: %+v", m)
	}
	if entryOf(t, lw, store.CComments).State != ops.LateWaiting || entryOf(t, lw, store.CProjects).State != ops.LateSettled {
		t.Fatalf("what it found: %+v", lw.Found)
	}
}

// "Nothing was written" only after a look read everything. A late change in
// records the bounded looks never get to: found stays 0 through every look
// and the extra day, yet the words, the bell and the field a script reads all
// say it couldn't check everything: what it didn't read and what to do, and
// never to look again once the address is gone.
func TestLateIncompleteLooksAreNeverAnAllClear(t *testing.T) {
	w, dst, n, _ := movedForLooksWith(t, tenComments)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	if _, err := a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "out of the looks' reach"}}); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateBackgroundBytes(1)
	defer undo()
	for _, due := range rec.Due[:2] {
		ops.LookDueLate(ctx, n, due.Add(time.Second))
		st := ops.LateStatus(ctx, n)
		if st.Found != 0 || st.AllClear || st.Checked != ops.LateCheckedPart || !st.CanLook || strings.Contains(st.Summary, "Nothing was written") ||
			!strings.Contains(st.Summary, "hasn't been able to check everything") || !strings.Contains(st.Summary, "Look again") || !slices.Contains(st.Unread, store.CComments) {
			t.Fatalf("part of the way: %+v", st)
		}
	}
	if k := countNotes(t, dst, "late_incomplete:"+move); k != 0 {
		t.Fatal("rang before the last look")
	}
	ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Second))
	st := ops.LateStatus(ctx, n)
	if st.Ended || st.AllClear || !st.CanLook || !strings.Contains(st.Summary, "couldn't check everything") || strings.Contains(st.Summary, "Nothing was written") || !strings.Contains(st.Summary, "kind") {
		t.Fatalf("after the last look: %+v", st)
	}
	if st.LookUntil == nil || !strings.Contains(st.Summary, "You have until") {
		t.Fatalf("how long a person has: %+v", st)
	}
	if k := countNotes(t, dst, "late_incomplete:"+move); k != 1 {
		t.Fatalf("%d notes after the last look", k)
	}
	said := ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+time.Minute))
	if len(said) != 1 || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("a day later: %v", said)
	}
	st = ops.LateStatus(ctx, n)
	if !st.Ended || st.AllClear || st.Found != 0 || st.Checked != ops.LateCheckedPart || st.CanLook || st.EndedWhy == "" ||
		!strings.Contains(st.Summary, "couldn't check everything") || strings.Contains(st.Summary, "Nothing was written") || strings.Contains(st.Summary, "Look again") ||
		!strings.Contains(st.Summary, "look in it yourself") {
		t.Fatalf("the final state: %+v", st)
	}
	if k := countNotes(t, dst, "late_incomplete:"+move); k != 1 {
		t.Fatalf("%d notes at the end: one, brought up to date", k)
	}
	var note models.Notification
	if err := dst.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "late_incomplete:" + move}).Decode(&note); err != nil || !strings.Contains(note.Body, "look in it yourself") {
		t.Fatalf("the note: %+v %v", note, err)
	}
	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{Look: true}, ops.Caller{})
	if err != nil || lw.Looked || lw.AllClear || !strings.Contains(lw.Problem, "no longer looks") || strings.Contains(lw.Summary, "Nothing was written") {
		t.Fatalf("asking afterwards: %+v %v", lw, err)
	}
	b, _ := json.Marshal(lw)
	if !strings.Contains(string(b), `"allClear":false`) || !strings.Contains(string(b), `"checked":"part"`) {
		t.Fatalf("the answer for a script: %s", b)
	}
}

// A look that stopped is gone on with: however low the bound, later looks get
// further from where it stopped, not from the start, until the way through is
// done, and only then is everything checked.
func TestLateLooksGoOnFromWhereTheyStopped(t *testing.T) {
	undoAfter := ops.SetLateLookAfter([]time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute, 6 * time.Minute,
		7 * time.Minute, 8 * time.Minute, 9 * time.Minute, 10 * time.Minute, 11 * time.Minute, 12 * time.Minute, 13 * time.Minute, 14 * time.Minute})
	defer undoAfter()
	w, dst, n, _ := movedForLooksWith(t, tenComments)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	if _, err := a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "the last one"}}); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateBackgroundBytes(1)
	defer undo()
	foundAt := -1
	for i, due := range rec.Due {
		ops.LookDueLate(ctx, n, due.Add(time.Second))
		st := ops.LateStatus(ctx, n)
		if st.Found == 1 && foundAt < 0 {
			foundAt = i
		}
		if st.Checked == ops.LateCheckedEverything {
			break
		}
	}
	st := ops.LateStatus(ctx, n)
	if foundAt < 0 || st.Waiting != 1 || st.Checked != ops.LateCheckedEverything {
		t.Fatalf("after going on: found at %d: %+v", foundAt, st)
	}
	if !strings.Contains(st.Summary, "read all of it between") || !strings.Contains(st.Summary, "after it was read wouldn't show") || strings.Contains(st.Summary, "several looks") {
		t.Fatalf("what was read and when: %s", st.Summary)
	}
	lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
	if err != nil || len(lw.Found) != 1 || lw.Found[0].Record != "cmt_a00009" {
		t.Fatalf("the list: %+v %v", lw, err)
	}
}

// Past the move's limit the rest is only counted: the words say so and never
// that a next look lists them; a group that can't be completed any more
// refuses plainly and can be left, saying what stays uncarried; and the looks
// don't read as complete.
func TestLateAtTheLimitOnlyCountsAndGroupsCanEnd(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	var lines []any
	for i := 0; i < 8; i++ {
		lines = append(lines, bson.M{"_id": fmt.Sprintf("seg_a%05d", i), "workspaceId": a.WorkspaceID(), "assetId": "ast_long", "text": "a line"})
	}
	if _, err := a.DB.DB.Collection(store.CSegments).InsertMany(ctx, lines); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateKeepPerMove(3)
	defer undo()
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	for _, due := range rec.Due {
		ops.LookDueLate(ctx, n, due.Add(time.Second))
		st := ops.LateStatus(ctx, n)
		if st.More != 5 || !st.AtLimit || st.Found != 3 || !strings.Contains(st.Summary, "only counted") || strings.Contains(st.Summary, "the next look lists") || st.AllClear || st.Checked != ops.LateCheckedPart {
			t.Fatalf("at the limit: %+v", st)
		}
	}
	if !ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatal("the address went with some only counted")
	}
	// It can't go over whole, and says what to do.
	_, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{"1"}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "cant_be_whole" || !strings.Contains(apperr.From(err).Message, "Leave it") {
		t.Fatalf("a group that can't go over whole: %v", err)
	}
	ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+time.Minute))
	st := ops.LateStatus(ctx, n)
	if !st.Ended || st.AllClear || !strings.Contains(st.Summary, "only counted") || strings.Contains(st.Summary, "Look again") {
		t.Fatalf("ended at the limit: %+v", st)
	}
	lf, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{"1"}}, ops.Caller{Human: true})
	if err != nil || !strings.Contains(lf.Summary, "3 records") || !strings.Contains(lf.Summary, "5 more records") || !strings.Contains(lf.Summary, "only in the old database") {
		t.Fatalf("leaving it: %+v %v", lf, err)
	}
}

// A carry-over that stopped part-way (the process went away): the record it
// held says it is being carried, a list lets go of it after a while, carrying
// again finishes the group, and the history has every record.
func TestLateCrashedCarryOverFinishes(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	var lines []any
	for i := 0; i < 3; i++ {
		lines = append(lines, bson.M{"_id": fmt.Sprintf("seg_a%05d", i), "workspaceId": a.WorkspaceID(), "assetId": "ast_c", "text": "a line"})
	}
	if _, err := a.DB.DB.Collection(store.CSegments).InsertMany(ctx, lines); err != nil {
		t.Fatal(err)
	}
	lw := lookNow(t, n)
	if len(lw.Found) != 1 || lw.Found[0].GroupSize != 3 {
		t.Fatalf("the look: %+v", lw.Found)
	}
	calls := 0
	undo := ops.SetLateCarryHook(func(id string) {
		calls++
		if calls == 2 {
			// The process goes away holding the second record.
			_, _ = dst.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"state": ops.LateCarrying, "carryingAt": time.Now().UTC()}})
			panic("the process went away")
		}
	})
	func() {
		defer func() { _ = recover() }()
		_, _ = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{lw.Found[0].ID}}, ops.Caller{Human: true})
	}()
	undo()
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_c"}); k != 1 {
		t.Fatalf("%d lines went over before it stopped", k)
	}
	var held bson.M
	if err := dst.C(store.CLateWrites).FindOne(ctx, bson.M{"state": ops.LateCarrying}).Decode(&held); err != nil {
		t.Fatal(err)
	}
	// While held: it says so, not that it was decided.
	_, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{held["_id"].(string)}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "being_carried" || strings.Contains(apperr.From(err).Message, "decided") {
		t.Fatalf("leaving what is being carried: %v", err)
	}
	// After a while a list lets go of it; carrying again finishes all three.
	if _, err := dst.C(store.CLateWrites).UpdateOne(ctx, bson.M{"_id": held["_id"]}, bson.M{"$set": bson.M{"carryingAt": time.Now().UTC().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	lw, err = ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var waiting ops.LateWriteView
	for _, f := range lw.Found {
		if f.State == ops.LateWaiting {
			waiting = f
		}
	}
	if waiting.GroupSize != 2 {
		t.Fatalf("what waits again: %+v", lw.Found)
	}
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{waiting.ID}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_c"}); k != 3 {
		t.Fatalf("%d lines here", k)
	}
	if k, _ := dst.C(store.CAudit).CountDocuments(ctx, bson.M{"action": "database.late.carry"}); k != 3 {
		t.Fatalf("%d of 3 records in the change history", k)
	}
}

// A record that changes here while its group goes over stays behind: the rest
// go, MetaRush says what happened, carrying it again is refused plainly (never
// "1 record of the 1 record"), and leaving it ends it.
func TestLateCarryRacingAChangeHere(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	var lines []any
	for i := 0; i < 3; i++ {
		lines = append(lines, bson.M{"_id": fmt.Sprintf("seg_a%05d", i), "workspaceId": a.WorkspaceID(), "assetId": "ast_r", "text": "a line"})
	}
	if _, err := a.DB.DB.Collection(store.CSegments).InsertMany(ctx, lines); err != nil {
		t.Fatal(err)
	}
	lw := lookNow(t, n)
	calls := 0
	undo := ops.SetLateCarryHook(func(string) {
		calls++
		if calls == 2 {
			if _, err := dst.C(store.CSegments).InsertOne(ctx, bson.M{"_id": "seg_a00001", "workspaceId": a.WorkspaceID(), "assetId": "ast_r", "text": "made here meanwhile"}); err != nil {
				panic(err)
			}
		}
	})
	c, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{lw.Found[0].ID}}, ops.Caller{Human: true})
	undo()
	if err != nil || len(c.Carried) != 1 || c.Carried[0].GroupSize != 2 || len(c.Warnings) != 1 || !strings.Contains(c.Warnings[0], "Only 2 of the 3 records") || !strings.Contains(c.Warnings[0], "1 record of them waits") {
		t.Fatalf("the race: %+v %v", c, err)
	}
	if k, _ := dst.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": "ast_r", "text": "a line"}); k != 2 {
		t.Fatalf("%d lines went over", k)
	}
	lw, err = ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	var left ops.LateWriteView
	for _, f := range lw.Found {
		if f.State == ops.LateWaiting {
			left = f
		}
	}
	if left.GroupSize != 1 || !left.CopyChanged {
		t.Fatalf("what stayed behind: %+v", lw.Found)
	}
	_, err = ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{left.ID}}, ops.Caller{Human: true})
	if code, reason := codeAndReason(err); code != apperr.Conflict || reason != "changed_here" || strings.Contains(apperr.From(err).Message, "1 record of the 1 record") {
		t.Fatalf("carrying what stayed behind: %v", err)
	}
	if _, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{left.ID}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if lw, _ := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{}); lw.Waiting != 0 {
		t.Fatalf("still waiting: %+v", lw.Found)
	}
}

// An old database removed or emptied (the docs say you may) is one finding in
// plain words, not one per record: the looks end, what was found before stays
// to decide, and it isn't an all-clear.
func TestLateOldDatabaseRemovedIsOneFinding(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	if _, err := a.DB.DB.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Late name"}}); err != nil {
		t.Fatal(err)
	}
	lookNow(t, n)
	names, err := a.DB.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name != store.CTestRun {
			if err := a.DB.DB.Collection(name).Drop(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	lw := lookNow(t, n)
	if lw.Total != 2 || lw.Waiting != 2 {
		t.Fatalf("the look: %+v", lw.Found)
	}
	gone := lw.Found[1]
	if gone.Kind != "emptied" || !gone.ReportOnly || gone.CanCarry || !strings.Contains(gone.Says, "no records any more") {
		t.Fatalf("the finding: %+v", gone)
	}
	m := lw.Moves[0]
	if !m.Ended || m.Checked != ops.LateCheckedGone || m.AllClear || !strings.Contains(m.Summary, "no records any more") || m.CanLook || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("the looks: %+v", m)
	}
	// What was found before it still waits, and the steps and the bell say so.
	if !strings.Contains(m.Summary, "1 thing it found before that still waits") || m.Waiting != 2 || m.WaitingCopies != 1 {
		t.Fatalf("what waits besides: %+v", m)
	}
	if !slices.ContainsFunc(lw.Steps, func(s string) bool { return strings.HasPrefix(s, "Carry each over") }) {
		t.Fatalf("the steps: %v", lw.Steps)
	}
	var both models.Notification
	if err := dst.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "late_writes:" + move}).Decode(&both); err != nil ||
		both.Title != "The old database has no records any more" || !strings.Contains(both.Body, "If you removed it") || !strings.Contains(both.Body, "carry it over") {
		t.Fatalf("the bell: %+v %v", both, err)
	}
	if entryOf(t, lw, store.CProjects).State != ops.LateWaiting || !entryOf(t, lw, store.CProjects).CanCarry {
		t.Fatalf("what was found before: %+v", lw.Found)
	}
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{gone.ID}}, ops.Caller{Human: true}); err == nil {
		t.Fatal("carried the old database being gone")
	}
	if _, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{gone.ID}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if k := countNotes(t, dst, "late_writes:"+move); k != 1 {
		t.Fatalf("%d notes", k)
	}
}

// The old database with no records has its own words: what it means, what to
// do if you removed it and if you didn't. Not a write, nothing to carry over,
// no copy kept for anyone, and what leaving it says.
func TestLateEmptiedOldDatabaseHasItsOwnWords(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	a := w.a
	ctx := context.Background()
	move := moveOf(t, a)
	names, err := a.DB.DB.ListCollectionNames(ctx, bson.D{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range names {
		if name != store.CTestRun {
			if err := a.DB.DB.Collection(name).Drop(ctx); err != nil {
				t.Fatal(err)
			}
		}
	}
	lw := lookNow(t, n)
	if lw.Waiting != 1 || len(lw.Found) != 1 || lw.AllClear {
		t.Fatalf("the look: %+v", lw)
	}
	f := lw.Found[0]
	for _, want := range []string{"no records any more", "If you removed it, there is nothing to do.", "If you didn't, find out what happened", "from a backup", "Nothing in this database changed"} {
		if !strings.Contains(f.Says, want) {
			t.Fatalf("the finding, without %q: %s", want, f.Says)
		}
	}
	m := lw.Moves[0]
	if m.WaitingCopies != 0 || !strings.Contains(m.Summary, "If you removed it") || strings.Contains(m.Summary, "still waits") {
		t.Fatalf("the looks: %+v", m)
	}
	if !strings.Contains(lw.Summary, "no records any more") || strings.Contains(lw.Summary, "written") || strings.Contains(lw.Summary, "carry") {
		t.Fatalf("the list says: %s", lw.Summary)
	}
	for _, s := range lw.Steps {
		if strings.Contains(s, "Carry") || strings.Contains(s, "copy") || strings.Contains(s, "removed from the old database") {
			t.Fatalf("a step that isn't about this: %v", lw.Steps)
		}
	}
	var note models.Notification
	if err := dst.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "late_writes:" + move}).Decode(&note); err != nil {
		t.Fatal(err)
	}
	if note.Title != "The old database has no records any more" || !strings.Contains(note.Body, "If you removed it") || !strings.Contains(note.Body, "If you didn't") {
		t.Fatalf("the bell: %+v", note)
	}
	for _, s := range []string{"Something was written", "carry it over", "Nobody would see"} {
		if strings.Contains(note.Title+" "+note.Body, s) {
			t.Fatalf("the bell says %q: %+v", s, note)
		}
	}
	if _, err := ops.CallTyped[ops.DatabaseLateCarryOut](ctx, n, "database.late.carry", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true}); err == nil ||
		!strings.Contains(apperr.From(err).Message, "nothing to carry over") {
		t.Fatalf("carrying it: %v", err)
	}
	left, err := ops.CallTyped[ops.DatabaseLateLeaveOut](ctx, n, "database.late.leave", ops.DatabaseLateDecideIn{IDs: []string{f.ID}}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(left.Summary, "Nothing in this database changed") || strings.Contains(left.Summary, "Left ") {
		t.Fatalf("leaving it: %s", left.Summary)
	}
	if st := ops.LateStatus(ctx, n); st.AllClear || st.Checked != ops.LateCheckedGone {
		t.Fatalf("after leaving it, never an all-clear: %+v", st)
	}
}

// looksDueInThePast moves a move's looks back, ten minutes apart, so the last
// was due that long ago and a person's look (on the real clock) comes after it.
func looksDueInThePast(t *testing.T, dst *store.DB, move string, ago time.Duration) []time.Time {
	t.Helper()
	ctx := context.Background()
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC().Truncate(time.Millisecond)
	due := make([]time.Time, len(rec.Due))
	for i := range due {
		due[i] = now.Add(-ago - time.Duration(len(due)-1-i)*10*time.Minute)
	}
	if _, err := dst.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": move}, bson.M{"$set": bson.M{"due": due}}); err != nil {
		t.Fatal(err)
	}
	return due
}

// stopInComments makes the looks stop on the old database's first comment,
// which they have then read and passed, the way a look bounded to almost
// nothing stops; and takes back the write that sent them there, so all a later
// look could find is what is written from here on. It returns that comment.
func stopInComments(t *testing.T, w *moveWorkspace, dst *store.DB, n *app.App, move string, due []time.Time) string {
	t.Helper()
	ctx := context.Background()
	comments := w.a.DB.DB.Collection(store.CComments)
	var original struct {
		Text string `bson:"text"`
	}
	if err := comments.FindOne(ctx, bson.M{"_id": "cmt_a00009"}).Decode(&original); err != nil {
		t.Fatal(err)
	}
	if _, err := comments.UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "for a moment"}}); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateBackgroundBytes(1)
	defer undo()
	var look struct {
		Pass struct {
			Position *dbmove.LookPosition `bson:"position"`
		} `bson:"pass"`
	}
	for i := 0; ; i++ {
		if i >= len(due)-1 {
			t.Fatal("the looks never got to the comments before the last")
		}
		if said := ops.LookDueLate(ctx, n, due[i].Add(time.Second)); len(said) != 1 || !strings.Contains(said[0], "part of it") {
			t.Fatalf("a look: %v", said)
		}
		if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&look); err != nil {
			t.Fatal(err)
		}
		if look.Pass.Position != nil && look.Pass.Position.Kind == store.CComments {
			break
		}
	}
	passed, ok := dbmove.IDOf(look.Pass.Position.Key).StringValueOK()
	if !ok || passed == "cmt_a00009" {
		t.Fatalf("stopped at %+v", look.Pass.Position)
	}
	if _, err := comments.UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": original.Text}}); err != nil {
		t.Fatal(err)
	}
	return passed
}

// An all-clear needs all of it read after the last look was due. The first
// look stopped after one record; a computer asleep until now writes into that
// record; this one wakes 30 hours after the last look was due. Going on from
// where it stopped would pass it by, find nothing, and say nothing was written.
func TestLateAllClearNeedsAPassStartedAfterTheLastLook(t *testing.T) {
	undoAfter := ops.SetLateLookAfter([]time.Duration{time.Minute, 2 * time.Minute, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute, 6 * time.Minute, 7 * time.Minute, 8 * time.Minute})
	defer undoAfter()
	setup := func(t *testing.T) (*moveWorkspace, *store.DB, *app.App, string, string, []time.Time) {
		t.Helper()
		w, dst, n, _ := movedForLooksWith(t, tenComments)
		move := moveOf(t, w.a)
		var rec struct {
			Due []time.Time `bson:"due"`
		}
		if err := dst.C(store.CLateLooks).FindOne(context.Background(), bson.M{"_id": move}).Decode(&rec); err != nil {
			t.Fatal(err)
		}
		passed := stopInComments(t, w, dst, n, move, rec.Due)
		return w, dst, n, move, passed, rec.Due
	}
	ctx := context.Background()
	notClear := func(t *testing.T, dst *store.DB, n *app.App, st *ops.LateLookView, coll, record string) {
		t.Helper()
		if st == nil || !st.Ended || st.AllClear || st.Found != 1 || st.Waiting != 1 || strings.Contains(st.Summary, "Nothing was written") {
			t.Fatalf("the looks: %+v", st)
		}
		lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
		if err != nil || lw.AllClear || len(lw.Found) != 1 || lw.Found[0].Collection != coll || (record != "" && lw.Found[0].Record != record) {
			t.Fatalf("the list: %+v %v", lw, err)
		}
	}

	t.Run("a write into a record already passed", func(t *testing.T) {
		w, dst, n, _, passed, due := setup(t)
		if _, err := w.a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": passed}, bson.M{"$set": bson.M{"text": "written while asleep"}}); err != nil {
			t.Fatal(err)
		}
		ops.LookDueLate(ctx, n, due[len(due)-1].Add(30*time.Hour))
		notClear(t, dst, n, ops.LateStatus(ctx, n), store.CComments, passed)
	})

	t.Run("a kind of record made after the look passed it", func(t *testing.T) {
		w, dst, n, _, _, due := setup(t)
		if _, err := w.a.DB.DB.Collection("aa_notes").InsertOne(ctx, bson.M{"_id": "aa_1", "workspaceId": w.a.WorkspaceID(), "text": "written while asleep"}); err != nil {
			t.Fatal(err)
		}
		ops.LookDueLate(ctx, n, due[len(due)-1].Add(30*time.Hour))
		notClear(t, dst, n, ops.LateStatus(ctx, n), "aa_notes", "")
	})

	// The same, asked for by a person once the last look was due.
	t.Run("a person's look", func(t *testing.T) {
		w, dst, n, _ := movedForLooksWith(t, tenComments)
		move := moveOf(t, w.a)
		passed := stopInComments(t, w, dst, n, move, looksDueInThePast(t, dst, move, time.Hour))
		if _, err := w.a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": passed}, bson.M{"$set": bson.M{"text": "written while asleep"}}); err != nil {
			t.Fatal(err)
		}
		lw := lookNow(t, n)
		if !lw.Looked || lw.AllClear || lw.Waiting != 1 || lw.Found[0].Record != passed || strings.Contains(lw.Summary, "Nothing was written") {
			t.Fatalf("a person's look: %+v", lw)
		}
		notClear(t, dst, n, ops.LateStatus(ctx, n), store.CComments, passed)
	})

	// Nothing written, and all of it read afterwards: an all-clear is still
	// what it says, and from when.
	t.Run("nothing written at all", func(t *testing.T) {
		_, _, n, _, _, due := setup(t)
		wake := due[len(due)-1].Add(30 * time.Hour)
		ops.LookDueLate(ctx, n, wake)
		st := ops.LateStatus(ctx, n)
		if st == nil || !st.Ended || !st.AllClear || st.Found != 0 || !strings.Contains(st.Summary, "Nothing was written") || st.CompleteFrom == nil || st.CompleteFrom.Before(due[len(due)-1]) {
			t.Fatalf("the looks: %+v", st)
		}
	})

	// A way through that began after the last look was due is gone on with: the
	// bound and the rule don't fight, whatever it took.
	t.Run("going on with a pass begun after", func(t *testing.T) {
		w, dst, n, _ := movedForLooksWith(t, tenComments)
		move := moveOf(t, w.a)
		if _, err := w.a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "late"}}); err != nil {
			t.Fatal(err)
		}
		due := looksDueInThePast(t, dst, move, time.Hour)
		undoBytes := ops.SetLateBackgroundBytes(1)
		defer undoBytes()
		ops.LookDueLate(ctx, n, time.Now().UTC())
		if st := ops.LateStatus(ctx, n); st.Checked != ops.LateCheckedPart || st.Ended {
			t.Fatalf("the look that began it: %+v", st)
		}
		lw := lookNow(t, n)
		m := lw.Moves[0]
		if !m.Ended || m.Checked != ops.LateCheckedEverything || m.CompleteFrom == nil || m.CompleteFrom.Before(due[2]) || m.Waiting != 1 {
			t.Fatalf("going on: %+v", m)
		}
	})

	// The rule itself, in what is said about a record that doesn't keep it.
	t.Run("a full read that began earlier is no all-clear", func(t *testing.T) {
		_, dst, n, move, _, due := setup(t)
		last := due[len(due)-1]
		if _, err := dst.C(store.CLateLooks).UpdateOne(ctx, bson.M{"_id": move}, bson.M{"$set": bson.M{
			"ended": true, "complete": true, "partial": false, "stopped": "", "unread": bson.A{}, "more": 0, "completeFrom": last.Add(-time.Hour), "completeAt": last.Add(time.Hour)}}); err != nil {
			t.Fatal(err)
		}
		st := ops.LateStatus(ctx, n)
		if st.AllClear || st.Checked != ops.LateCheckedPart || strings.Contains(st.Summary, "Nothing was written") || !strings.Contains(st.Summary, "began before the last look was due") {
			t.Fatalf("the looks: %+v", st)
		}
	})
}

// A computer that was off for days: the day to act counts from the first look
// it tries once the last one was due, not from when that was due. Neither a
// look that fails nor one that stops part-way ends the looks and lets go of
// the address until that day is over, and a person's look can still finish.
func TestLateWakingLateStillGivesADay(t *testing.T) {
	ctx := context.Background()
	const offFor = 72 * time.Hour

	t.Run("the old database can't be reached", func(t *testing.T) {
		w, dst, n, _ := movedForLooks(t)
		move := moveOf(t, w.a)
		var rec struct {
			Due []time.Time `bson:"due"`
		}
		if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
			t.Fatal(err)
		}
		if err := ops.KeepLookAddressForTest(n.Cfg, move, "mongodb://127.0.0.1:1/?serverSelectionTimeoutMS=300&connectTimeoutMS=300"); err != nil {
			t.Fatal(err)
		}
		wake := rec.Due[2].Add(offFor)
		said := ops.LookDueLate(ctx, n, wake)
		st := ops.LateStatus(ctx, n)
		if len(said) != 1 || !strings.Contains(said[0], "couldn't look") || st.Ended || !ops.KeepsLookAddress(n.Cfg, move) {
			t.Fatalf("waking: %v %+v", said, st)
		}
		if st.LookUntil == nil || st.LookUntil.Sub(wake.Add(ops.LateLookGrace)).Abs() > time.Second || !strings.Contains(st.Summary, "You have until") {
			t.Fatalf("how long a person has: %+v", st)
		}
		var note models.Notification
		if err := dst.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "late_incomplete:" + move}).Decode(&note); err != nil || !strings.Contains(note.Body, "You have until") {
			t.Fatalf("the bell says when: %+v %v", note, err)
		}
		for _, after := range []time.Duration{11 * time.Minute, 23 * time.Hour} {
			said := ops.LookDueLate(ctx, n, wake.Add(after))
			if len(said) != 1 || !strings.Contains(said[0], "couldn't look") || !ops.KeepsLookAddress(n.Cfg, move) || ops.LateStatus(ctx, n).Ended {
				t.Fatalf("%s after waking: %v", after, said)
			}
		}
		said = ops.LookDueLate(ctx, n, wake.Add(ops.LateLookGrace+time.Minute))
		st = ops.LateStatus(ctx, n)
		if len(said) != 1 || !strings.Contains(said[0], "stopped looking") || ops.KeepsLookAddress(n.Cfg, move) || !st.Ended || st.AllClear ||
			strings.Contains(st.Summary, "You have until") || strings.Contains(st.Summary, "Nothing was written") {
			t.Fatalf("a day after waking: %v %+v", said, st)
		}
	})

	t.Run("the look stops part-way", func(t *testing.T) {
		w, dst, n, _ := movedForLooksWith(t, tenComments)
		move := moveOf(t, w.a)
		if _, err := w.a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "late"}}); err != nil {
			t.Fatal(err)
		}
		looksDueInThePast(t, dst, move, offFor)
		undo := ops.SetLateBackgroundBytes(1)
		defer undo()
		wake := time.Now().UTC()
		if said := ops.LookDueLate(ctx, n, wake); len(said) != 1 || !strings.Contains(said[0], "part of it") {
			t.Fatalf("waking: %v", said)
		}
		for _, after := range []time.Duration{time.Minute, 23 * time.Hour} {
			if said := ops.LookDueLate(ctx, n, wake.Add(after)); len(said) != 0 {
				t.Fatalf("%s after waking: %v", after, said)
			}
			if st := ops.LateStatus(ctx, n); st.Ended || !st.CanLook || !strings.Contains(st.Summary, "You have until") || !ops.KeepsLookAddress(n.Cfg, move) {
				t.Fatalf("%s after waking: %+v", after, st)
			}
		}
		// A person can still finish it.
		lw := lookNow(t, n)
		if m := lw.Moves[0]; !lw.Looked || !m.Ended || m.Checked != ops.LateCheckedEverything || m.Waiting != 1 || m.LookUntil != nil {
			t.Fatalf("a person's look: %+v", lw)
		}
	})

	t.Run("nobody finishes the look", func(t *testing.T) {
		w, dst, n, _ := movedForLooksWith(t, tenComments)
		move := moveOf(t, w.a)
		if _, err := w.a.DB.DB.Collection(store.CComments).UpdateOne(ctx, bson.M{"_id": "cmt_a00009"}, bson.M{"$set": bson.M{"text": "late"}}); err != nil {
			t.Fatal(err)
		}
		looksDueInThePast(t, dst, move, offFor)
		undo := ops.SetLateBackgroundBytes(1)
		defer undo()
		wake := time.Now().UTC()
		ops.LookDueLate(ctx, n, wake)
		said := ops.LookDueLate(ctx, n, wake.Add(ops.LateLookGrace+time.Minute))
		if st := ops.LateStatus(ctx, n); len(said) != 1 || !st.Ended || st.AllClear || ops.KeepsLookAddress(n.Cfg, move) {
			t.Fatalf("a day after waking: %v %+v", said, st)
		}
	})
}

// With no looks on record MetaRush can't say nothing was written: after a move
// that couldn't keep what the looks need it says it can't tell and what a
// person can do; a database no move came into has no old database to speak of.
func TestLateNoRecordSaysItCantTell(t *testing.T) {
	ctx := context.Background()
	t.Run("a move that couldn't keep what the looks need", func(t *testing.T) {
		undo := ops.SetLateKeepFails()
		defer undo()
		_, _, n, out := movedForLooks(t)
		if out.Late != nil {
			t.Fatalf("looks after the move: %+v", out.Late)
		}
		lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, n, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		if lw.AllClear || !strings.Contains(lw.Summary, "can't tell whether anything was written") || strings.Contains(strings.ToLower(lw.Summary), "nothing was written") {
			t.Fatalf("the list: %+v", lw)
		}
		if !slices.ContainsFunc(lw.Steps, func(s string) bool { return strings.Contains(s, "look in it yourself") }) {
			t.Fatalf("what a person can do: %v", lw.Steps)
		}
		asked := lookNow(t, n)
		if asked.Looked || asked.AllClear || asked.Problem != asked.Summary || !strings.Contains(asked.Problem, "can't tell") {
			t.Fatalf("asked to look: %+v", asked)
		}
		st, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, n, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
		if err != nil || st.Late != nil || !strings.Contains(st.LateNote, "can't tell") || !strings.Contains(st.LateNote, "look in it yourself") {
			t.Fatalf("the status: %+v %v", st, err)
		}
	})
	t.Run("a database no move came into", func(t *testing.T) {
		w := newMoveWorkspace(t)
		lw, err := ops.CallTyped[ops.DatabaseLateWritesOut](ctx, w.a, "database.late.writes", ops.DatabaseLateWritesIn{}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		if lw.AllClear || !strings.Contains(lw.Summary, "No move into this database") || strings.Contains(strings.ToLower(lw.Summary), "nothing was written") || len(lw.Steps) != 0 {
			t.Fatalf("the list: %+v", lw)
		}
		st, err := ops.CallTyped[ops.DatabaseMoveStatusOut](ctx, w.a, "database.move.status", ops.DatabaseMoveStatusIn{}, ops.Caller{})
		if err != nil || st.LateNote != "" {
			t.Fatalf("the status: %+v %v", st, err)
		}
	})
}

// A last look that stopped part-way, and nobody asked for another: the looks
// end a day later, and the address goes. The plan says beforehand when every
// look will read the old database whole.
func TestLateLooksEndAfterAPartialLastLook(t *testing.T) {
	w, dst, n, _ := movedForLooks(t)
	ctx := context.Background()
	move := moveOf(t, w.a)
	if _, err := w.a.DB.DB.Collection(store.CProjects).UpdateOne(ctx, bson.M{"_id": w.project}, bson.M{"$set": bson.M{"name": "Late name"}}); err != nil {
		t.Fatal(err)
	}
	var rec struct {
		Due []time.Time `bson:"due"`
	}
	if err := dst.C(store.CLateLooks).FindOne(ctx, bson.M{"_id": move}).Decode(&rec); err != nil {
		t.Fatal(err)
	}
	undo := ops.SetLateBackgroundBytes(1)
	defer undo()
	if said := ops.LookDueLate(ctx, n, rec.Due[2].Add(time.Second)); len(said) != 1 {
		t.Fatalf("the last look: %v", said)
	}
	if !ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatal("the address went at once")
	}
	said := ops.LookDueLate(ctx, n, rec.Due[2].Add(ops.LateLookGrace+time.Minute))
	if len(said) != 1 || !strings.Contains(said[0], "didn't see everything") || ops.KeepsLookAddress(n.Cfg, move) {
		t.Fatalf("a day later: %v", said)
	}
	if st := ops.LateStatus(ctx, n); st == nil || !st.Ended {
		t.Fatalf("ended: %+v", st)
	}
}

func TestDatabasePlanSaysWhenLooksReadEverything(t *testing.T) {
	w := newMoveWorkspace(t)
	a := w.a
	ctx := context.Background()
	dst := moveDB(t)
	if err := ops.SaveMoveAddress(a.Cfg, testutil.URI()); err != nil {
		t.Fatal(err)
	}
	target := ops.DatabaseTargetIn{To: ops.MoveToSaved, Name: dst.Name}
	says := func() bool {
		t.Helper()
		p, err := ops.CallTyped[ops.DatabasePlanOut](ctx, a, "database.plan", ops.DatabasePlanIn{DatabaseTargetIn: target}, ops.Caller{Human: true})
		if err != nil {
			t.Fatal(err)
		}
		return slices.ContainsFunc(p.Notes, func(s string) bool { return strings.Contains(s, "reads it whole again") })
	}
	if says() {
		t.Fatal("the plan warns about a server that says its own hashes")
	}
	undo := ops.SetServerHashesWork(false)
	defer undo()
	if !says() {
		t.Fatal("the plan doesn't say every look reads the old database whole")
	}
}
