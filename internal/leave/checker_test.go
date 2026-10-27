package leave

import (
	"context"
	"fmt"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A read that counts every project looks at the Content Settings once, not
// once per project (it was quadratic in the number of projects).
func TestEveryProjectIsOneLook(t *testing.T) {
	t.Setenv("CLAUDECODE", "")
	a := testapp.New(t)
	ctx := context.Background()
	var last string
	for i := 0; i < 40; i++ {
		out, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: fmt.Sprintf("P%02d", i)}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		last = out.Project.ID
	}
	if _, err := ops.CallTyped[ops.ContentShowOut](ctx, a, "content.own", ops.ContentOwnIn{ProjectID: last}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.CallTyped[LeaveOut](ctx, a, "content.leave.set", LeaveSetIn{ProjectID: last, Keep: map[string][]string{"ai": {"voices"}}}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	before := checkerLoads.Load()
	if err := aiReads(ctx, &claude, "log.list", map[string]any{}); err != nil {
		t.Fatalf("nobody keeps words: %v", err)
	}
	if n := checkerLoads.Load() - before; n != 1 {
		t.Fatalf("%d looks for one read over 40 projects", n)
	}
}
