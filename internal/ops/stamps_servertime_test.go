package ops_test

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A version saved, and a render asked for, on a computer whose clock is ten
// minutes slow carry the database server's time (D360): the daemons wait for
// a version to be quiet a minute by that clock, and count the review copies of
// the last hour by it, so the version gets no copy at once.
func TestVersionsAndRendersCarryTheServersTime(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Slow laptop")
	c := clip(t, a, "Jetty", p.ID)
	restore := testutil.SkewClock(t, -10*time.Minute)
	v := saveCut(t, a, p.ID, "", c.ID)
	r := do[ops.RenderOut](t, a, "render.submit", ops.RenderSubmitIn{RevisionID: v, Quality: "review"})
	restore()
	now := byServer(t, a)
	near := func(what string, at time.Time) {
		t.Helper()
		if d := now.Sub(at); d < 0 || d > time.Minute {
			t.Fatalf("%s saved on a computer ten minutes slow: %v before the server's now", what, d)
		}
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), v, &rev); err != nil {
		t.Fatal(err)
	}
	near("the version", rev.CreatedAt)
	var proj models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.ID, &proj); err != nil {
		t.Fatal(err)
	}
	near("its cut", proj.Branches["main"].UpdatedAt)
	near("the project", proj.UpdatedAt)
	var rd models.Render
	if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), r.Render.ID, &rd); err != nil {
		t.Fatal(err)
	}
	near("the render", rd.CreatedAt)
}
