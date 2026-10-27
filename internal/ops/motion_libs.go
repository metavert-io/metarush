package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

// Shared libraries of motion graphics (D171): approving a new version of a
// library is the one approval a theme change needs — the latest cuts whose
// graphics are pinned to an older version move to it, one new version of
// each cut (older versions of the cuts keep what they had).

// LibraryMove is a cut whose graphics moved to a library's new version.
type LibraryMove struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Revision    string `json:"revision"`
	Number      int    `json:"number"`
	Graphics    int    `json:"graphics"`
}

// moveLibraryPins moves the latest cuts' graphics to a newly approved library version.
func moveLibraryPins(ctx context.Context, a *app.App, lib *models.MotionComponent) ([]LibraryMove, []string) {
	var ps []models.Project
	if a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), nil, &ps) != nil {
		return nil, nil
	}
	var moves []LibraryMove
	var problems []string
	for _, p := range ps {
		head := p.Branches["main"].Head
		if head == "" {
			continue
		}
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r) != nil {
			continue
		}
		var eops []editspec.Op
		for _, o := range r.Spec.Overlays {
			if o.Motion == nil || o.Locked {
				continue
			}
			h, ok := o.Motion.Libs[lib.Name]
			if !ok || h == lib.Hash {
				continue
			}
			set, _ := json.Marshal(map[string]any{"motion": map[string]any{"libs": map[string]string{lib.Name: lib.Hash}}})
			eops = append(eops, editspec.Op{Op: "updateOverlay", ID: o.ID, Set: set})
		}
		if len(eops) == 0 {
			continue
		}
		plan := editspec.Plan{BaseRevisionID: r.ID, Branch: "main", Origin: "motion:library",
			Instruction: fmt.Sprintf("Move the graphics to the newly approved version of the shared library %s", lib.Name)}
		plan.Ops = eops
		raw, _ := json.Marshal(plan)
		chk, err := CallTyped[EditCheckOut](ctx, a, "edit.check", EditCheckIn{Plan: raw}, Caller{Human: true})
		if err != nil || !chk.Valid {
			msg := "the change didn't pass its check"
			if err != nil {
				msg = err.Error()
			} else if len(chk.Errors) > 0 {
				msg = chk.Errors[0].Message
			}
			problems = append(problems, fmt.Sprintf("“%s” stays on the older version: %s", p.Name, strings.TrimSuffix(msg, ".")))
			continue
		}
		out, err := CallTyped[EditApplyOut](ctx, a, "edit.apply", EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken}, Caller{Human: true})
		if err != nil {
			problems = append(problems, fmt.Sprintf("“%s” stays on the older version: %s", p.Name, err.Error()))
			continue
		}
		moves = append(moves, LibraryMove{ProjectID: p.ID, ProjectName: p.Name, Revision: out.Revision.ID, Number: out.Revision.Number, Graphics: len(eops)})
	}
	return moves, problems
}

// libraryMoveWords says what moved, in plain words.
func libraryMoveWords(moves []LibraryMove, problems []string) string {
	var parts []string
	for _, m := range moves {
		parts = append(parts, fmt.Sprintf("“%s” (now version %d)", m.ProjectName, m.Number))
	}
	s := ""
	if len(parts) > 0 {
		s = " The latest cuts that use it moved to it: " + strings.Join(parts, ", ") + "."
	}
	for _, p := range problems {
		s += " " + p + "."
	}
	return s
}

var _ = motion.Short
