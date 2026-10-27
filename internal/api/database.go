package api

import (
	"context"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
)

// Moving the database from the dashboard (D338, Settings → Storage → Move the
// database). database.move and database.switch are LocalOnly (they switch this
// computer's settings), and the new database's address must never pass
// through a logged request, so the signed-in dashboard of this daemon gets
// three narrow endpoints: plan a move (a typed address goes once to this
// computer's keychain and never comes back), start the move a person
// confirmed — `metarush database move --dashboard-move <id>` in a process of
// its own with a one-time grant on its standard input, like the media's move
// (it keeps going while this daemon starts again holding, then switched) — and
// switch this computer after another one moved the workspace. Abandoning and
// the status are ordinary operations (database.abandon, database.move.status).

// DatabaseMovePlanIn describes the new database.
type DatabaseMovePlanIn struct {
	Form ops.DatabaseMoveForm `json:"form"`
}

// DatabaseMovePlanOut is what moving would do, and the move to confirm.
type DatabaseMovePlanOut struct {
	MoveID string              `json:"moveId"`
	Plan   ops.DatabasePlanOut `json:"plan"`
}

// DatabaseMoveStartIn confirms a planned move.
type DatabaseMoveStartIn struct {
	MoveID string `json:"moveId"`
}

// DatabaseMoveStartOut says the move started.
type DatabaseMoveStartOut struct {
	Started bool   `json:"started"`
	Summary string `json:"summary"`
}

// DatabaseSwitchFormIn switches this computer with its own sign-in.
type DatabaseSwitchFormIn struct {
	// Address is sent once and never returned: kept on this computer.
	Address string `json:"address" jsonschema:"maxLength=2048"`
	Name    string `json:"name,omitempty" jsonschema:"maxLength=63"`
}

// databaseElsewhere refuses moving or switching the database from a browser
// on another computer: it runs here, with this computer's sign-ins.
func (s *Server) databaseElsewhere(w http.ResponseWriter, r *http.Request) bool {
	if !accessOf(r).public {
		return false
	}
	writeErr(w, apperr.New(apperr.NotConfigured, "Moving the database starts on %s itself.", s.App.Cfg.MachineName()).
		WithHint("Open MetaRush there to move it or switch it.").WithDetails(map[string]any{"reason": "elsewhere"}))
	return true
}

func (s *Server) databasePlan(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "database.plan") || s.databaseElsewhere(w, r) {
		return
	}
	var in DatabaseMovePlanIn
	if err := storageBody(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	run, plan, err := ops.PlanDashboardDatabaseMove(ctx, &a, in.Form)
	in.Form.Address = ""
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, DatabaseMovePlanOut{MoveID: run.ID, Plan: *plan})
}

func (s *Server) databaseMove(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "database.move") || s.databaseElsewhere(w, r) {
		return
	}
	var in DatabaseMoveStartIn
	if err := storageBody(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	if ops.DatabaseMoveRunsHere(&a) {
		writeErr(w, apperr.New(apperr.Conflict, "The database is already being moved from this computer.").WithHint("This page shows how far it is."))
		return
	}
	logPath, err := ops.DatabaseMoveLog(&a, in.MoveID)
	if err != nil {
		writeErr(w, err)
		return
	}
	token, run, err := ops.GrantDashboardDatabaseMove(&a, in.MoveID)
	if err != nil {
		writeErr(w, err)
		return
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		ops.FailDashboardDatabaseMove(&a, run.ID, err)
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start moving the database."))
		return
	}
	defer logf.Close()
	st, err := a.Tools.Start(tools.Spec{Tool: tools.Self, Args: []string{"--home", a.Cfg.Home(), "database", "move", "--dashboard-move", run.ID},
		Stdin: strings.NewReader(token + "\n"), Stdout: logf})
	if err != nil {
		ops.FailDashboardDatabaseMove(&a, run.ID, err)
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start moving the database."))
		return
	}
	go func() { _ = st.Wait() }() // reaped here; after a restart, ops.ReapDashboardMoves
	s.broker.Local(Event{Type: "settings"})
	writeOK(w, DatabaseMoveStartOut{Started: true, Summary: "Moving the database to " + run.To.Words + ". Changes pause on every computer until it's done."})
}

func (s *Server) databaseSwitch(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "database.switch") || s.databaseElsewhere(w, r) {
		return
	}
	var in DatabaseSwitchFormIn
	if err := storageBody(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	if err := ops.SaveMoveAddress(a.Cfg, in.Address); err != nil {
		in.Address = ""
		writeErr(w, err)
		return
	}
	in.Address = ""
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Minute)
	defer cancel()
	out, err := ops.CallTyped[ops.DatabaseSwitchOut](ctx, &a, "database.switch", ops.DatabaseSwitchIn{Name: in.Name}, ops.Caller{Human: true})
	if err != nil {
		writeErr(w, err)
		return
	}
	s.broker.Local(Event{Type: "settings"})
	writeOK(w, out)
}

// heldGuard: while a move keeps changes out of the workspace's database
// (D335) this MetaRush holds — it reads, and refuses every change that isn't
// an operation (which refuses it itself, plainly) or the move's own.
func (s *Server) heldGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !s.held || r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)
			return
		}
		p := r.URL.Path
		if strings.HasPrefix(p, "/api/ops/") || strings.HasPrefix(p, "/api/database/") || p == "/api/auth/login" || p == "/api/auth/logout" {
			next.ServeHTTP(w, r)
			return
		}
		m, _ := s.App.MoveMarker(r.Context())
		err := ops.FrozenError(m)
		if err == nil {
			err = apperr.New(apperr.Moving, "The workspace is being moved to another database; changes wait until it's done.").
				WithDetails(map[string]any{"reason": "database_moving"})
		}
		writeErr(w, err)
	})
}
