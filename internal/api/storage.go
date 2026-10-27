package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
)

// Moving the media from the dashboard (D112, Settings → Storage).
// storage.plan and storage.migrate are LocalOnly (they read a storage
// description file and switch this machine's settings), so the signed-in
// dashboard of this daemon gets three narrow endpoints instead: pick a folder,
// check a storage described in a form (the daemon writes the description; an
// S3 secret goes once to this computer's keychain and never comes back), and
// start the move a person confirmed. The move runs as `metarush storage
// migrate` in a process of its own that takes a one-time grant on its stdin,
// so it keeps going while this daemon restarts with the new storage.

// StorageMovePlanIn describes the new storage.
type StorageMovePlanIn struct {
	Target ops.StorageTarget `json:"target"`
}

// StorageMovePlanOut is what moving would copy, and the move to confirm.
type StorageMovePlanOut struct {
	MoveID string             `json:"moveId"`
	Plan   ops.StoragePlanOut `json:"plan"`
}

// StorageMoveStartIn confirms a planned move.
type StorageMoveStartIn struct {
	MoveID   string `json:"moveId"`
	ReadBack bool   `json:"readBack,omitempty" jsonschema:"description=Also download every file from the new storage again and check it (slower; may cost downloads)."`
}

// StorageMoveStartOut says the move started.
type StorageMoveStartOut struct {
	Started bool   `json:"started"`
	Summary string `json:"summary"`
}

// StorageChooseOut is the folder a person picked for the media.
type StorageChooseOut struct {
	Canceled bool   `json:"canceled"`
	Path     string `json:"path,omitempty"`
}

// storageBody decodes a small JSON body strictly.
func storageBody(r *http.Request, v any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<14))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil && err != io.EOF {
		return apperr.Wrap(apperr.InvalidInput, err, "MetaRush couldn't read the request.")
	}
	return nil
}

// ownerOnly refuses a signed-in person who isn't an owner (D191): the
// endpoints that change this computer's settings outside the ops registry.
func (s *Server) ownerOnly(w http.ResponseWriter, r *http.Request, what string) bool {
	if err := ops.RequireOwner(r.Context(), s.App, what); err != nil {
		writeErr(w, err)
		return true
	}
	return false
}

// storageElsewhere refuses moving the media from a browser on another computer
// (remote access): the move runs here with this computer's keys, so it is
// started at this computer (the page says so; this is the server's half).
func (s *Server) storageElsewhere(w http.ResponseWriter, r *http.Request) bool {
	if !accessOf(r).public {
		return false
	}
	writeErr(w, apperr.New(apperr.NotConfigured, "Moving the media starts on %s itself.", s.App.Cfg.MachineName()).
		WithHint("Open MetaRush there to move it.").WithDetails(map[string]any{"reason": "elsewhere"}))
	return true
}

// storageChoose opens this computer's folder picker for the new storage.
func (s *Server) storageChoose(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "storage.choose") || s.pickerElsewhere(w, r) {
		return
	}
	a := *s.appFor(r, "dashboard")
	home, _ := os.UserHomeDir()
	p, err := a.Tools.ChooseFolder(r.Context(), "Choose the folder for MetaRush's media", home)
	if errors.Is(err, tools.ErrUnsupported) {
		writeErr(w, apperr.New(apperr.NotConfigured, "This computer can't show a folder picker.").WithHint("Type the folder's full place instead.").
			WithDetails(map[string]any{"reason": "unsupported"}))
		return
	}
	if err != nil {
		writeErr(w, apperr.Wrap(apperr.Internal, err, "The folder picker didn't open. Try again."))
		return
	}
	if p == "" {
		writeOK(w, StorageChooseOut{Canceled: true})
		return
	}
	writeOK(w, StorageChooseOut{Path: p})
}

// storagePlan checks a storage described in the form: what moving would copy.
func (s *Server) storagePlan(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "storage.plan") || s.storageElsewhere(w, r) {
		return
	}
	var in StorageMovePlanIn
	if err := storageBody(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	// Listing a large bucket takes a while; the page shows that it is checking.
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Minute)
	defer cancel()
	mv, plan, err := ops.PlanDashboardMove(ctx, &a, in.Target)
	in.Target.SecretAccessKey = ""
	if err != nil {
		writeErr(w, err)
		return
	}
	writeOK(w, StorageMovePlanOut{MoveID: mv.ID, Plan: *plan})
}

// storageMove starts a planned move a person confirmed in a styled dialog.
func (s *Server) storageMove(w http.ResponseWriter, r *http.Request) {
	if s.ownerOnly(w, r, "storage.migrate") || s.storageElsewhere(w, r) {
		return
	}
	var in StorageMoveStartIn
	if err := storageBody(r, &in); err != nil {
		writeErr(w, err)
		return
	}
	a := *s.appFor(r, "dashboard")
	if ops.MoveRunningHere(r.Context(), &a) {
		writeErr(w, apperr.New(apperr.Conflict, "The media is already moving on this computer.").WithHint("The activity bar shows how far it is."))
		return
	}
	logPath, err := ops.DashboardMoveLog(&a, in.MoveID)
	if err != nil {
		writeErr(w, err)
		return
	}
	token, mv, err := ops.GrantDashboardMove(&a, in.MoveID, in.ReadBack)
	if err != nil {
		writeErr(w, err)
		return
	}
	logf, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		ops.FinishDashboardMove(&a, mv.ID, nil, err)
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start moving the media."))
		return
	}
	defer logf.Close()
	st, err := a.Tools.Start(tools.Spec{Tool: tools.Self, Args: []string{"--home", a.Cfg.Home(), "storage", "migrate", "--dashboard-move", mv.ID},
		Stdin: strings.NewReader(token + "\n"), Stdout: logf})
	if err != nil {
		ops.FinishDashboardMove(&a, mv.ID, nil, err)
		writeErr(w, apperr.Wrap(apperr.Internal, err, "MetaRush couldn't start moving the media."))
		return
	}
	go func() { _ = st.Wait() }() // reaped here; after a restart, ops.ReapDashboardMoves
	s.broker.Local(Event{Type: "settings"})
	writeOK(w, StorageMoveStartOut{Started: true, Summary: "Moving the media to " + mv.To.Name + ". The activity bar shows how far it is."})
}
