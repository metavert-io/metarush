package store_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// Every time compared across computers is the database server's (D360). This
// test reads the Go code of every package under internal/ (no database, no
// build) and flags what would compare a stored time with this computer's clock,
// or write this computer's clock where another computer judges it:
//
//   - time.Since and time.Until of anything but a time this function took
//     from the clock itself (that is a duration within one process);
//   - x.Before, x.After and x.Sub with this computer's clock on one side only;
//   - this computer's clock under $lt, $lte, $gt or $gte;
//   - this computer's clock passed to what judges a stored time by it: any
//     function with a parameter `now time.Time`, a struct's now field, and
//     jobs.Online, jobs.Why, a consent's Active… (judgesATime);
//   - this computer's clock written to a field other computers judge
//     (serverStamped).
//
// A new one fails. Use the server's time (a query with $$NOW, $currentDate,
// store.DB.Now, store.DB.NowOrLocal). A place that really is this computer's
// own business goes in clockAllowed, by file and function, with how many
// places it has and why; an entry that no longer matches fails too, so the
// list only shrinks.

// serverStamped are fields another computer compares: written in the server's
// time, as they appear in queries (lower case) and in structs.
var serverStamped = map[string]bool{
	"nextRunAt": true, "NextRunAt": true,
	"lastHeartbeat": true, "LastHeartbeat": true,
	"lease.expiresAt": true,
	"syncUntil":       true, "SyncUntil": true,
	"trash.at": true, "trash.claim.at": true,
	"approvingAt": true, "ApprovingAt": true,
	"sessionsValidAfter": true, "SessionsValidAfter": true,
	"lockedUntil": true, "LockedUntil": true,
	"lastSeenAt": true, "LastSeenAt": true,
	"lastUsedAt": true, "LastUsedAt": true,
	"beat": true, "Beat": true,
	"completedAt": true, "CompletedAt": true,
	"pendingSince": true, "PendingSince": true,
	"triggers.next": true, "triggers.pendingSince": true,
	"textFixes.lock.until": true,
	"holds.at":             true,
}

// judgesATime are functions that judge a stored time against the now they are
// given: it must be the server's.
var judgesATime = map[string]bool{"Online": true, "Why": true, "Active": true, "HoldsNewWords": true, "Covers": true, "CoversUseAt": true, "consentStatus": true}

// clockAllowed lists the places that may use this computer's clock:
// "file function" → how many places, and why.
var clockAllowed = map[string]struct {
	N   int
	Why string
}{
	"internal/aiwork/presenter_retire.go linkServer.add":            {1, "the link server's own memory, in one process"},
	"internal/aiwork/presenter_retire.go linkServer.serve":          {1, "the link server's own memory, in one process"},
	"internal/aiwork/presenters.go askWords":                        {1, "turns the adapter's end by this computer's clock into time left; what is stored is the server's now plus that"},
	"internal/abilities/listener.go localChecks.Check":              {1, "an in-memory cache"},
	"internal/aiservices/elevenlabs/kinds.go Adapter.pollDub":       {1, "a progress estimate while this process waits"},
	"internal/cache/quota.go Cache.ReadQuotaStatus":                 {1, "this computer's cache"},
	"internal/cache/repair.go Cache.Repair":                         {1, "local files' times"},
	"internal/intake/folders.go scanner.publish":                    {1, "an in-process throttle"},
	"internal/intake/folders.go scanner.scan":                       {4, "a linked folder's scan on this computer: local files' times and its own memory"},
	"internal/localdb/install.go counting.Read":                     {1, "an in-process throttle"},
	"internal/localdb/upgrade.go Server.Watch":                      {1, "the database on this computer: failures in this process's memory"},
	"internal/logbook/daemon.go Logger.sweepLocalBodies":            {1, "local folders' dates"},
	"internal/logbook/events.go ranFor":                             {1, "only when the job's end isn't known; the attempt ran in this process"},
	"internal/logbook/hookrun.go cleanStaleSessions":                {1, "local files' times"},
	"internal/logbook/logger.go OpRecord.End":                       {1, "how long an operation took, in one process"},
	"internal/logbook/logops/ops.go init":                           {5, "a person's time window over the log, whose entries keep their writer's clock"},
	"internal/logbook/spool.go Logger.FlushSpool":                   {1, "local files' times"},
	"internal/providers/vertex.go VertexAccessToken":                {2, "a token kept in memory, signed for Google's service"},
	"internal/renderer/install.go Env.prune":                        {1, "local files' times"},
	"internal/storagemove/storagemove.go journal.add":               {1, "an in-process throttle"},
	"internal/storagemove/storagemove.go tracker.emit":              {2, "progress rates, in one process"},
	"internal/todos/scores.go Muted":                                {1, "an in-memory cache"},
	"internal/tools/tools.go Set.report":                            {1, "how long a program took, in one process"},
	"internal/uploads/uploads.go Store.List":                        {2, "upload records are files on this computer"},
	"internal/uploads/uploads.go Store.persist":                     {1, "an in-process throttle"},
	"internal/api/access.go Server.signedIn":                        {3, "sessions live in this computer's session file"},
	"internal/activity/activity.go builder.uploadItem":              {1, "an upload's record is a file on this computer, kept by its clock"},
	"internal/aiservices/aiconformance/conformance.go Run":          {1, "a test's check of an adapter, not a stored time"},
	"internal/api/access.go Server.signInTailscale":                 {1, "a session's end and idle time, in this computer's session file"},
	"internal/api/access.go Server.whileSignedIn":                   {1, "sessions live in this computer's session file"},
	"internal/api/extension.go extAPI.createUpload":                 {1, "a record's stamp"},
	"internal/api/extension.go extAPI.pair":                         {1, "pairing codes live in a file on this computer"},
	"internal/api/extension.go extAPI.requireToken":                 {1, "pairing tokens live in a file on this computer"},
	"internal/api/signin.go Server.signInGuard":                     {1, "an in-memory rate limit"},
	"internal/api/signin.go Server.startSession":                    {1, "a session's end and idle time, in this computer's session file"},
	"internal/api/uploads.go uploadAPI.ingest":                      {2, "upload records are files on this computer"},
	"internal/api/uploads.go uploadAPI.patch":                       {2, "upload records are files on this computer"},
	"internal/browserext/ops.go init":                               {1, "pairing codes live in a file on this computer"},
	"internal/browserext/ops.go status":                             {1, "pairing codes live in a file on this computer"},
	"internal/cli/log.go logPurgeCmd":                               {1, "reads a person's words for a date"},
	"internal/cli/logbundle.go logBundleCmd":                        {1, "a file's name"},
	"internal/cli/logbundle.go writeBundle":                         {1, "reads a person's words for a date"},
	"internal/improve/ops.go init":                                  {1, "a date in a printed list"},
	"internal/intake/folders.go folderRow":                          {1, "a linked folder's state file on this computer"},
	"internal/linkimport/job.go Run":                                {1, "a record's stamp"},
	"internal/linkimport/ops.go dryRun":                             {1, "a record's stamp"},
	"internal/recipes/run.go stopRun":                               {1, "a sample's end, a record's stamp"},
	"internal/recipes/steps.go engine.fail":                         {1, "a sample's end, a record's stamp"},
	"internal/recorder/finish.go Finish":                            {1, "which message to show; other computers are refused right after"},
	"internal/recorder/finish.go takeOver":                          {1, "a recording's lease: only its own computer writes and judges it"},
	"internal/recorder/ops.go discard":                              {1, "which message to show; other computers are refused right after"},
	"internal/recorder/sweep.go Sweep":                              {1, "only a recording's own computer sweeps it"},
	"internal/remote/ops.go people":                                 {1, "sessions live in this computer's session file"},
	"internal/speakers/assign.go AssignWords":                       {1, "a record's stamp"},
	"internal/speakers/restore.go RestoreWords":                     {1, "a record's stamp"},
	"internal/api/events.go Broker.pollMark":                        {1, "picks the earlier of this computer's clock and the server's on purpose: changes carry either"},
	"internal/api/extension.go extAPI.limited":                      {1, "an in-memory rate limit"},
	"internal/api/server.go Server.auth":                            {2, "a one-time sign-in nonce in this process's memory; a session's end and idle time, in this computer's session file"},
	"internal/api/uploads.go uploadAPI.changed":                     {1, "an in-memory throttle"},
	"internal/api/uploads.go uploadAPI.sweep":                       {1, "upload records are files on this computer"},
	"internal/assistant/manager.go Manager.finishTurnLocked":        {1, "a turn's length on the computer that ran it"},
	"internal/assistant/manager.go Manager.startReaper":             {1, "idle processes on this computer, in memory"},
	"internal/assistant/models.go Manager.Models":                   {1, "an in-memory cache"},
	"internal/assistant/probe.go Manager.probe":                     {1, "an in-memory cache"},
	"internal/assistant/purge.go Manager.sweepGone":                 {1, "a local folder's age"},
	"internal/assistant/work.go Manager.checkWork":                  {1, "only the conversation's holder follows its work, with a 12 hour margin"},
	"internal/auth/accounts.go Invite":                              {1, "a detail in the audit record"},
	"internal/auth/accounts.go Method":                              {1, "an in-memory cache"},
	"internal/auth/accounts.go userForSession":                      {2, "an in-memory cache"},
	"internal/catchup/catchup.go RunSteps":                          {1, "a catch-up step's record; nothing compares it"},
	"internal/cli/cache.go printCacheStats":                         {3, "this computer's cache, shown to a person"},
	"internal/cli/cache.go progressPrinter.update":                  {1, "an in-process throttle"},
	"internal/cli/log.go dayLabel":                                  {1, "a date's words in this person's zone"},
	"internal/cli/log.go logTailCmd":                                {2, "the log's lines shown as they come; a window, not a decision"},
	"internal/improve/ops.go Add":                                   {2, "an improvement item's lastSeenAt, another field of that name; nothing compares it"},
	"internal/improve/ops.go Signals":                               {4, "a report's period over the log, whose entries keep their writer's clock"},
	"internal/ingest/ops.go init":                                   {1, "an unfinished upload's start is the storage service's time, with a margin of hours"},
	"internal/ingest/watch.go Watcher.publish":                      {1, "an in-process throttle"},
	"internal/ingest/watch.go Watcher.scan":                         {2, "local files' times"},
	"internal/ingest/watchops.go readStatusFile":                    {1, "a local file's age"},
	"internal/linkimport/sweep.go Sweep":                            {1, "local files' times"},
	"internal/linkimport/update.go due":                             {1, "this computer's own state file"},
	"internal/linkimport/ytdlp.go Probe":                            {1, "a local deadline"},
	"internal/machines/machines.go List":                            {1, "a seven day warning's words"},
	"internal/ops/databasemove.go databaseMove":                     {2, "how long this run took, in one process"},
	"internal/ops/databasemove_state.go ClaimDashboardDatabaseMove": {1, "a grant in a file on this computer"},
	"internal/ops/databasemove_state.go DatabaseMoveStatus":         {1, "a grant in a file on this computer"},
	"internal/ops/databasemove_state.go GrantDashboardDatabaseMove": {1, "a plan in a file on this computer"},
	"internal/ops/databasemove_state.go dbMoveRun.save":             {1, "an in-process throttle"},
	"internal/ops/databasemove_state.go moveRunsHere":               {1, "a grant in a file on this computer"},
	"internal/ops/databasemove_state.go pruneDBMoves":               {1, "a plan in a file on this computer"},
	"internal/ops/edit.go checkToken":                               {1, "a confirm token's key is this installation's, so only this computer checks it"},
	"internal/ops/storage.go moveJob.beat":                          {1, "an in-process throttle"},
	"internal/ops/storage_dashboard.go ClaimDashboardMove":          {1, "a grant in a file on this computer"},
	"internal/ops/storage_dashboard.go GrantDashboardMove":          {1, "a plan in a file on this computer"},
	"internal/ops/storage_dashboard.go MoveRunningHere":             {1, "a grant in a file on this computer"},
	"internal/ops/storage_dashboard.go StorageStatus":               {1, "a grant in a file on this computer"},
	"internal/ops/storage_dashboard.go pruneDashboardMoves":         {1, "a plan in a file on this computer"},
	"internal/ops/sync.go syncSummary":                              {1, "this computer's cache"},
	"internal/recorder/finish.go Recover":                           {1, "a recording's lease: only its own computer writes and judges it"},
	"internal/recorder/finish.go claim":                             {1, "a recording's lease: only its own computer writes and judges it"},
	"internal/recorder/finish.go complete":                          {1, "pieces arrive at the recording's own computer"},
	"internal/recorder/ops.go ago":                                  {1, "words shown to a person"},
	"internal/recorder/ops.go throwAway":                            {1, "a recording's lease: only its own computer writes and judges it"},
	"internal/recorder/ops.go view":                                 {1, "words shown to a person"},
	"internal/recorder/recorder.go progress":                        {1, "an in-process throttle"},
	"internal/remotion/work.go pruneWork":                           {1, "local work folders' times"},
	"internal/store/guard.go DB.MoveMarkerForChange":                {1, "the age of this process's last look at the marker"},
	"internal/store/guard.go DB.MoveMarkerNow":                      {2, "the age of this process's last look at the marker"},
	"internal/store/servertime.go DB.Now":                           {2, "the helper's own marks, by the monotonic clock"},
	"internal/voiceover/track.go tracker.beat":                      {1, "an in-process throttle"},
	"internal/worker/worker.go Worker.trackDeferral":                {1, "an in-process throttle"},
}

type clockFinding struct {
	key, line, what string
}

func TestNothingComparesWithThisComputersClock(t *testing.T) {
	found := map[string][]clockFinding{}
	root := filepath.Join("..")
	takesNow := map[string]map[int]bool{}
	if err := eachSource(root, func(_ string, _ *token.FileSet, f *ast.File) error {
		collectNowParams(f, takesNow)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	err := eachSource(root, func(path string, fset *token.FileSet, f *ast.File) error {
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		for _, fi := range scanClock(fset, "internal/"+filepath.ToSlash(rel), f, takesNow) {
			found[fi.key] = append(found[fi.key], fi)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for k := range found {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		allowed, ok := clockAllowed[k]
		if ok && allowed.N == len(found[k]) {
			continue
		}
		var lines []string
		for _, fi := range found[k] {
			lines = append(lines, "    "+fi.line+"  "+fi.what)
		}
		if ok {
			t.Errorf("%s: %d places use this computer's clock, the list says %d (%s):\n%s", k, len(found[k]), allowed.N, allowed.Why, strings.Join(lines, "\n"))
		} else {
			t.Errorf("%s uses this computer's clock where another computer may judge the time:\n%s\nUse the database server's time (a query with $$NOW, $currentDate, store.DB.Now), or, when only this computer ever reads it, add it to clockAllowed in internal/store/clockguard_test.go with why.", k, strings.Join(lines, "\n"))
		}
	}
	for k := range clockAllowed {
		if len(found[k]) == 0 {
			t.Errorf("clockAllowed lists %s, which no longer uses this computer's clock: take it out", k)
		}
	}
}

// TestTheClockGuardSeesWhatItShould keeps the scanner itself honest.
func TestTheClockGuardSeesWhatItShould(t *testing.T) {
	src := `package x
func flagged() {
	_ = time.Since(j.UpdatedAt)
	_ = time.Until(*at)
	_ = j.Lease.ExpiresAt.After(time.Now())
	_ = time.Now().Before(j.NextRunAt)
	now := time.Now().UTC()
	_ = now.Sub(j.UpdatedAt)
	_ = bson.M{"updatedAt": bson.M{"$lt": now.Add(-time.Hour)}}
	_ = bson.D{{Key: "$gte", Value: time.Now()}}
	_ = jobs.Online(w, now)
	_ = jobs.Why(j, ws, time.Now().UTC())
	set := bson.M{"nextRunAt": now, "trash.at": store.Clock()}
	_ = models.Job{NextRunAt: now}
	j.LastHeartbeat = time.Now().UTC()
	set["beat"] = now
	FollowLatest(ctx, a, time.Now().UTC())
	_ = live(l, now)
	_ = b.look(now)
	_ = builder{now: time.Now()}
	b.now = now
}
func FollowLatest(ctx context.Context, a *app.App, now time.Time) {}
func live(l *Link, now time.Time) bool { return true }
func (b *builder) look(now time.Time) bool { return true }
func fine() {
	start := time.Now()
	_ = time.Since(start)
	deadline := time.Now().Add(time.Minute)
	_ = time.Until(deadline)
	_ = time.Now().Before(deadline)
	_ = start.Sub(deadline)
	_ = a.After(b)
	_ = time.After(time.Second)
	_ = bson.M{"createdAt": start, "at": bson.M{"$lt": cutoff}}
	if now, err := db.Now(ctx); err == nil {
		_ = now.Sub(j.UpdatedAt)
		_ = bson.M{"nextRunAt": now}
	}
	now := time.Now()
	f := func(now time.Time) bool { return now.After(j.At) }
	_ = f(now)
}
`
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "x.go", src, 0)
	if err != nil {
		t.Fatal(err)
	}
	var flagged, fine int
	takesNow := map[string]map[int]bool{}
	collectNowParams(f, takesNow)
	for _, fi := range scanClock(fset, "x.go", f, takesNow) {
		switch fi.key {
		case "x.go flagged":
			flagged++
		default:
			t.Errorf("flagged in fine code: %+v", fi)
			fine++
		}
	}
	if flagged != 19 {
		t.Errorf("flagged %d places, want 19", flagged)
	}
}

// eachSource parses every non-test Go file under root (tests' helpers aside).
func eachSource(root string, fn func(path string, fset *token.FileSet, f *ast.File) error) error {
	return filepath.WalkDir(root, func(path string, e os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() {
			if e.Name() == "testutil" || e.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		return fn(path, fset, f)
	})
}

// collectNowParams notes the functions and methods, by name, with a parameter
// `now time.Time`, and where it is: what they are given they judge stored times
// by, or pass on to what does.
func collectNowParams(f *ast.File, takesNow map[string]map[int]bool) {
	for _, decl := range f.Decls {
		d, ok := decl.(*ast.FuncDecl)
		if !ok || d.Type.Params == nil {
			continue
		}
		i := 0
		for _, field := range d.Type.Params.List {
			isTime := false
			if sel, ok := field.Type.(*ast.SelectorExpr); ok {
				if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "time" && sel.Sel.Name == "Time" {
					isTime = true
				}
			}
			names := field.Names
			if len(names) == 0 {
				i++
				continue
			}
			for _, n := range names {
				if isTime && n.Name == "now" {
					if takesNow[d.Name.Name] == nil {
						takesNow[d.Name.Name] = map[int]bool{}
					}
					takesNow[d.Name.Name][i] = true
				}
				i++
			}
		}
	}
}

// scanClock finds the places in one file.
func scanClock(fset *token.FileSet, path string, f *ast.File, takesNow map[string]map[int]bool) []clockFinding {
	var out []clockFinding
	for _, decl := range f.Decls {
		name := ""
		switch d := decl.(type) {
		case *ast.FuncDecl:
			name = d.Name.Name
			if d.Recv != nil && len(d.Recv.List) == 1 {
				name = receiverName(d.Recv.List[0].Type) + "." + name
			}
		case *ast.GenDecl:
			if d.Tok != token.VAR {
				continue
			}
			name = "var"
		default:
			continue
		}
		s := &clockScan{fset: fset, key: path + " " + name, local: map[*ast.Object]bool{}, takesNow: takesNow}
		s.findLocals(decl)
		s.check(decl)
		out = append(out, s.found...)
	}
	return out
}

func receiverName(e ast.Expr) string {
	switch x := e.(type) {
	case *ast.StarExpr:
		return receiverName(x.X)
	case *ast.IndexExpr:
		return receiverName(x.X)
	case *ast.Ident:
		return x.Name
	}
	return "?"
}

type clockScan struct {
	fset  *token.FileSet
	key   string
	local map[*ast.Object]bool // variables that hold this computer's clock
	// takesNow: functions and methods by name, with where a now parameter is.
	takesNow map[string]map[int]bool
	found    []clockFinding
}

func (s *clockScan) add(n ast.Node, what string) {
	var b strings.Builder
	_ = printer.Fprint(&b, token.NewFileSet(), n)
	text := strings.Join(strings.Fields(b.String()), " ")
	if len(text) > 100 {
		text = text[:100] + "…"
	}
	pos := s.fset.Position(n.Pos())
	s.found = append(s.found, clockFinding{key: s.key, line: fmt.Sprintf("line %d", pos.Line), what: what + ": " + text})
}

// findLocals marks the variables that take this computer's clock, until
// nothing new turns up (one may be made from another).
func (s *clockScan) findLocals(decl ast.Node) {
	for changed := true; changed; {
		changed = false
		ast.Inspect(decl, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.AssignStmt:
				if len(x.Lhs) != len(x.Rhs) {
					return true
				}
				for i, l := range x.Lhs {
					if id, ok := l.(*ast.Ident); ok && id.Obj != nil && !s.local[id.Obj] && s.isLocal(x.Rhs[i]) {
						s.local[id.Obj], changed = true, true
					}
				}
			case *ast.ValueSpec:
				if len(x.Names) != len(x.Values) {
					return true
				}
				for i, id := range x.Names {
					if id.Obj != nil && !s.local[id.Obj] && s.isLocal(x.Values[i]) {
						s.local[id.Obj], changed = true, true
					}
				}
			}
			return true
		})
	}
}

// isLocal says whether an expression is this computer's clock: time.Now(),
// store.Clock(), jobs.Clock() or a variable holding it, and what is worked out
// from them with a method that keeps a time (Add, UTC, Round…).
func (s *clockScan) isLocal(e ast.Expr) bool {
	switch x := e.(type) {
	case *ast.ParenExpr:
		return s.isLocal(x.X)
	case *ast.StarExpr:
		return s.isLocal(x.X)
	case *ast.UnaryExpr:
		return x.Op == token.AND && s.isLocal(x.X)
	case *ast.Ident:
		return x.Obj != nil && s.local[x.Obj]
	case *ast.CallExpr:
		switch fn := x.Fun.(type) {
		case *ast.Ident:
			return fn.Name == "Clock"
		case *ast.SelectorExpr:
			if pkg, ok := fn.X.(*ast.Ident); ok && pkg.Obj == nil {
				switch pkg.Name + "." + fn.Sel.Name {
				case "time.Now", "store.Clock", "jobs.Clock":
					return true
				}
			}
			switch fn.Sel.Name {
			case "UTC", "Local", "Round", "Truncate", "Add", "AddDate", "In":
				return s.isLocal(fn.X)
			}
		}
	}
	return false
}

// check flags the places in one declaration.
func (s *clockScan) check(decl ast.Node) {
	ast.Inspect(decl, func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.CallExpr:
			s.checkCall(x)
		case *ast.KeyValueExpr:
			if key := keyName(x.Key); key != "" && s.isLocal(x.Value) {
				switch {
				case key == "$lt" || key == "$lte" || key == "$gt" || key == "$gte":
					s.add(x, "under "+key)
				case serverStamped[key]:
					s.add(x, "writes "+key)
				case key == "now":
					s.add(x, "a now field by this computer's clock")
				}
			}
		case *ast.CompositeLit:
			// bson.D{{Key: "$lt", Value: now}}, bson.E{…}
			var k string
			var v ast.Expr
			for _, el := range x.Elts {
				if kv, ok := el.(*ast.KeyValueExpr); ok {
					switch keyName(kv.Key) {
					case "Key":
						k = keyName(kv.Value)
					case "Value":
						v = kv.Value
					}
				}
			}
			if v != nil && s.isLocal(v) && (strings.HasPrefix(k, "$lt") || strings.HasPrefix(k, "$gt") || serverStamped[k]) {
				s.add(x, "under "+k)
			}
		case *ast.AssignStmt:
			if len(x.Lhs) != len(x.Rhs) {
				return true
			}
			for i, l := range x.Lhs {
				name := ""
				switch t := l.(type) {
				case *ast.SelectorExpr:
					name = t.Sel.Name
				case *ast.IndexExpr:
					name = keyName(t.Index)
				}
				if serverStamped[name] && s.isLocal(x.Rhs[i]) {
					s.add(x, "writes "+name)
				}
				if _, isField := l.(*ast.SelectorExpr); isField && name == "now" && s.isLocal(x.Rhs[i]) {
					s.add(x, "a now field by this computer's clock")
				}
			}
		}
		return true
	})
}

func (s *clockScan) checkCall(c *ast.CallExpr) {
	name := ""
	switch f := c.Fun.(type) {
	case *ast.Ident:
		name = f.Name
	case *ast.SelectorExpr:
		name = f.Sel.Name
	}
	for i := range s.takesNow[name] {
		if i < len(c.Args) && s.isLocal(c.Args[i]) {
			s.add(c, "local time as the now of "+name)
		}
	}
	fn, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		if id, isID := c.Fun.(*ast.Ident); isID && judgesATime[id.Name] {
			s.checkArgs(c, id.Name)
		}
		return
	}
	if judgesATime[fn.Sel.Name] {
		s.checkArgs(c, fn.Sel.Name)
		return
	}
	if pkg, isPkg := fn.X.(*ast.Ident); isPkg && pkg.Obj == nil {
		switch pkg.Name + "." + fn.Sel.Name {
		case "time.Since", "time.Until":
			if len(c.Args) == 1 && !s.isLocal(c.Args[0]) {
				s.add(c, "with a stored time")
			}
			return
		case "time.After":
			return
		}
	}
	switch fn.Sel.Name {
	case "Before", "After", "Sub":
		if len(c.Args) == 1 && s.isLocal(fn.X) != s.isLocal(c.Args[0]) {
			s.add(c, "against a stored time")
		}
	}
}

func (s *clockScan) checkArgs(c *ast.CallExpr, what string) {
	for _, a := range c.Args {
		if s.isLocal(a) {
			s.add(c, "local time passed to "+what)
			return
		}
	}
}

func keyName(e ast.Expr) string {
	switch k := e.(type) {
	case *ast.BasicLit:
		if k.Kind == token.STRING {
			return strings.Trim(k.Value, "`\"")
		}
	case *ast.Ident:
		return k.Name
	}
	return ""
}
