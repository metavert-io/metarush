package recipes

import (
	"sync"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Two steppers at once never work on one run together. A dashboard request and
// the daemon's loop live in one process (a terminal's recipe run in another):
// each look holds the run's lease under a name of its own, a second look gets
// the run as it stands, a save from a look that doesn't hold the lease is
// refused, and two steppers started together make the cut once.
func TestTwoSteppersTakeTurns(t *testing.T) {
	f := setup(t)
	f.save(RecipeSaveIn{})
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	advanceOnStart = false
	defer func() { advanceOnStart = true }()
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	// One look holds the run: another look of the same process can't take it.
	first, err := claim(f.ctx, f.a, run.ID)
	if err != nil || first == nil || first.Lease == nil {
		t.Fatalf("the first look: %+v %v", first, err)
	}
	if second, err := claim(f.ctx, f.a, run.ID); err != nil || second != nil {
		t.Fatalf("a second look took a run another look holds: %+v %v", second, err)
	}
	// A stepper that comes meanwhile gets the run as it stands, untouched.
	if got := f.advance(run.ID); stepOf(got, stepInputs).State != models.StepPending {
		t.Fatalf("stepped while another look held the run: %+v", got.Steps)
	}
	// Only the look that holds the lease saves.
	other := *first
	other.Lease = &models.RunLease{By: lookName(f.a), Until: time.Now().Add(time.Minute)}
	if err := save(f.ctx, f.a, &other); err != errLost {
		t.Fatalf("a save without the lease: %v", err)
	}
	release(f.ctx, f.a, first)
	// Two steppers at once, to the end: the cut is made once, nothing fails.
	together := func() {
		t.Helper()
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for i := 0; i < 2; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := Advance(f.ctx, f.a, run.ID)
				errs <- err
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("a stepper failed: %v", err)
			}
		}
	}
	together()
	got, _ := getRun(f.ctx, f.a, run.ID)
	if got.State == models.RunFailed || got.Revision == "" || got.Render == "" {
		t.Fatalf("one stepper made the cut and asked for its review copy: %+v %+v", got.State, got.Steps)
	}
	versions := func() int64 {
		n, err := f.a.DB.C(store.CRevisions).CountDocuments(f.ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "origin": "recipe:" + run.ID})
		if err != nil {
			t.Fatal(err)
		}
		return n
	}
	if n := versions(); n != 1 {
		t.Fatalf("the cut is made once: %d versions", n)
	}
	f.finishReview(run.ID)
	together()
	if done, _ := getRun(f.ctx, f.a, run.ID); done.State != models.RunDone || versions() != 1 || done.Lease != nil {
		t.Fatalf("done once, its lease given back: %+v %d %+v", done.State, versions(), done.Lease)
	}
}
