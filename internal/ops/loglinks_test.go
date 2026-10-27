package ops

import (
	"context"
	"errors"
	"testing"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// "See log": a problem notification and a job's details point at the log
// entries behind them; good news does not.
func TestSeeLogLinks(t *testing.T) {
	jobID, renderID, assetID := ids.New(ids.Job), ids.New(ids.Render), ids.New(ids.Asset)
	cases := []struct {
		n    models.Notification
		want string
	}{
		{models.Notification{Tone: "error", JobID: jobID, AssetID: assetID}, links.Log("job", jobID)},
		{models.Notification{Tone: "warning", RenderID: renderID}, links.Log("render", renderID)},
		{models.Notification{Tone: "error", AssetID: assetID}, links.Log("asset", assetID)},
		{models.Notification{Tone: "success", JobID: jobID}, ""},
		{models.Notification{Tone: "error"}, ""},
	}
	for i, c := range cases {
		if got := notificationLogLink(c.n); got != c.want {
			t.Errorf("case %d: %q, want %q", i, got, c.want)
		}
	}
	if got := links.Log("job", jobID); got != "/settings/log?job="+jobID {
		t.Fatalf("log link %q", got)
	}

	a := testapp.New(t)
	ctx := context.Background()
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRender, IdempotencyKey: "render:see-log", Payload: map[string]any{}})
	if err != nil {
		t.Fatal(err)
	}
	leased, err := a.Queue.Lease(ctx, jobs.Worker{ID: a.Cfg.Installation.ID, Name: "test-machine", JobTypes: []string{jobs.TypeRender}, AcceptsHeavy: true})
	if err != nil || leased == nil {
		t.Fatalf("lease: %v %v", leased, err)
	}
	if err := a.Queue.Fail(ctx, leased, a.Cfg.Installation.ID, errors.New("the renderer stopped"), false); err != nil {
		t.Fatal(err)
	}
	d := call[JobShowOut](t, a, "job.show", JobIDIn{ID: j.ID})
	if d.State != models.JobFailed || d.LogLink != links.Log("job", j.ID) {
		t.Fatalf("job details: state %s, log link %q", d.State, d.LogLink)
	}
}
