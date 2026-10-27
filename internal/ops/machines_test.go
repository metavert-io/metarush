package ops

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/machines"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// machine.rekey is for people, on this machine only; its dry run
// (machine.rekeyPlan) changes nothing and anyone may ask for it.
func TestMachineRekeyIsForPeople(t *testing.T) {
	plan, _ := Get("machine.rekeyPlan")
	rekey, _ := Get("machine.rekey")
	if plan == nil || rekey == nil || plan.Mutating || plan.HumanOnly || !plan.LocalOnly || !rekey.Mutating || !rekey.HumanOnly || !rekey.LocalOnly {
		t.Fatalf("flags: plan %+v rekey %+v", plan, rekey)
	}
	a := testapp.New(t)
	ctx := context.Background()
	if _, err := CallTyped[machines.MachineRekey](ctx, a, "machine.rekey", MachineRekeyIn{}, Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("not a person: %v", err)
	}
	if _, err := CallTyped[machines.MachineRekey](ctx, a, "machine.rekey", MachineRekeyIn{}, Caller{Human: true, Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("over the network: %v", err)
	}
	// The test installation keeps its footage in a folder: nothing to replace.
	if _, err := CallTyped[machines.MachineRekey](ctx, a, "machine.rekeyPlan", MachineRekeyIn{}, Caller{}); apperr.From(err).Code != apperr.NotConfigured {
		t.Fatalf("plan on a folder store: %v", err)
	}
}
