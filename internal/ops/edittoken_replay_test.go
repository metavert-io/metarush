package ops_test

import (
	"encoding/json"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A checked change saved twice with the same confirm token — a retry, or a
// loop — is never made twice: the plan names its base, so the second save
// rebases onto the version the first one made and its changes overlap there.
func TestAConfirmedEditIsNotMadeTwice(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Garden story")
	c1 := clip(t, a, "Garage interview", p.ID)
	saveCut(t, a, p.ID, "", c1.ID)

	for _, tc := range []struct {
		name string
		op   editspec.Op
	}{
		{"a clip added", editspec.Op{Op: "insertClip", After: "ca", Clip: &editspec.Clip{ID: "cz", AssetID: c1.ID, In: 4, Out: 6}}},
		{"a title added", editspec.Op{Op: "addOverlay", Overlay: &editspec.Overlay{ID: "t1", Template: "title.card", At: 0.5, Duration: 2, Params: json.RawMessage(`{"title":"Spring"}`)}}},
	} {
		base := headOf(t, a, p.ID)
		plan, _ := json.Marshal(editspec.Plan{BaseRevisionID: base, Instruction: tc.name, Ops: []editspec.Op{tc.op}})
		chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
		if !chk.Valid {
			t.Fatalf("%s: not valid: %+v", tc.name, chk.Errors)
		}
		first := do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken})
		err := doErr(a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}, person)
		if code(err) != "E_REVISION_CONFLICT" {
			t.Fatalf("%s: a second save with one check: %v", tc.name, err)
		}
		if h := headOf(t, a, p.ID); h != first.Revision.ID {
			t.Fatalf("%s: the latest version moved past the first save (%s → %s): %v", tc.name, first.Revision.ID, h, err)
		}
	}
}
