package aiwork_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/fakeai"
	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

// Computers sharing a database have clocks minutes apart: what AI work
// decides across them goes by the database server's clock (D360). Each test
// plays a computer ten minutes ahead and one ten minutes behind against
// records near a limit by the server's clock, where a wrong clock would
// decide the other way.

const skew = 10 * time.Minute

func serverNow(t *testing.T, a *app.App) time.Time {
	t.Helper()
	now, err := a.DB.ServerNow(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return now
}

// near fails unless at is the server's time now (within a few seconds).
func near(t *testing.T, what string, at, server time.Time) {
	t.Helper()
	if d := at.Sub(server); d < -time.Minute || d > time.Minute {
		t.Fatalf("%s is %v, %v from the server's time", what, at, d)
	}
}

// as runs fn on a computer whose clock is off by off.
func as(t *testing.T, off time.Duration, fn func()) {
	t.Helper()
	restore := testutil.SkewClock(t, off)
	defer restore()
	fn()
}

func (e *env) setGen(t *testing.T, id string, set bson.M) {
	t.Helper()
	if _, err := e.a.DB.C(store.CGenerations).UpdateOne(context.Background(), bson.M{"_id": id}, bson.M{"$set": set}); err != nil {
		t.Fatal(err)
	}
}

func (e *env) presenter(t *testing.T, name string) aiwork.PresenterView {
	t.Helper()
	v, err := call[aiwork.PresenterView](t, e.a, "presenter.show", aiwork.PresenterIDIn{ID: name}, false)
	if err != nil {
		t.Fatal(err)
	}
	return *v
}

// AI work left without an outcome is closed by the sweep once it is a minute
// old by the server's clock; its record and outcome carry the server's time.
func TestAIWorkSweepGoesByTheServersClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	var out *aiwork.MakeOut
	as(t, -skew, func() {
		out = e.ask(t, aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a paper boat on a pond"})
	})
	near(t, "createdAt", e.gen(t, out.Work.ID).CreatedAt, serverNow(t, e.a))
	if _, err := e.a.Queue.RequestCancel(ctx, out.JobID); err != nil {
		t.Fatal(err)
	}
	age := func(d time.Duration) { e.setGen(t, out.Work.ID, bson.M{"createdAt": serverNow(t, e.a).Add(-d)}) }
	age(55 * time.Second)
	as(t, skew, func() { aiwork.Sweep(ctx, e.a) })
	if g := e.gen(t, out.Work.ID); g.Outcome != nil {
		t.Fatalf("a computer ahead closed work 55 s old: %+v", g.Outcome)
	}
	age(65 * time.Second)
	as(t, -skew, func() { aiwork.Sweep(ctx, e.a) })
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Outcome.State != models.GenCanceled {
		t.Fatalf("a computer behind left work 65 s old: %+v", g.Outcome)
	}
	near(t, "the outcome's time", g.Outcome.At, serverNow(t, e.a))
}

// A voice copy cut off while it was being sent is withdrawn at the server's
// time and looked for at the provider for LostVoiceLookFor after it by the
// server's clock, whichever computer runs the deletion.
func TestLostVoiceCopyIsLookedForByTheServersClock(t *testing.T) {
	e := setup(t)
	in, _ := e.copyReady(t, "Odile Brask")
	out, err := call[aiwork.MakeOut](t, e.a, "ai.voices.copy", in, true)
	if err != nil {
		t.Fatal(err)
	}
	if err := generate.AppendTo(context.Background(), e.a, out.Work.ID, "sending", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	as(t, -skew, func() { _, _ = e.run(t, out.JobID) })
	g := e.gen(t, out.Work.ID)
	if g.Withdrawn == nil || g.Removed != nil || g.Withdrawn.JobID == "" {
		t.Fatalf("withdrawn %+v removed %+v", g.Withdrawn, g.Removed)
	}
	near(t, "when it was withdrawn", g.Withdrawn.At, serverNow(t, e.a))
	withdrawn := func(ago time.Duration) { e.setGen(t, g.ID, bson.M{"withdrawn.at": serverNow(t, e.a).Add(-ago)}) }
	var wait *generate.Wait
	withdrawn(aiwork.LostVoiceLookFor - 30*time.Second)
	as(t, skew, func() {
		if _, err := e.run(t, g.Withdrawn.JobID); !errors.As(err, &wait) {
			t.Fatalf("a computer ahead stopped looking 30 s early: %v", err)
		}
	})
	withdrawn(aiwork.LostVoiceLookFor + 30*time.Second)
	as(t, -skew, func() {
		if _, err := e.run(t, g.Withdrawn.JobID); err != nil {
			t.Fatalf("a computer behind looked again after the time was up: %v", err)
		}
	})
	if g = e.gen(t, out.Work.ID); g.Removed == nil || !strings.Contains(g.Removed.Detail, "none was made") {
		t.Fatalf("removed %+v", g.Removed)
	}
}

// A presenter clip whose answer was lost is looked for at the service for
// LostVoiceLookFor after its outcome by the server's clock, and its outcome
// carries the server's time.
func TestLostClipIsLookedForByTheServersClock(t *testing.T) {
	e := setup(t)
	key := "hg" + strings.Repeat("Qp6", 11) + "fake"
	fake := fakeheygen.New(key)
	t.Cleanup(fake.Close)
	t.Setenv(providers.EnvHeyGenBaseURL, fake.URL)
	t.Setenv(providers.EnvVar(providers.HeyGenName), key)
	line := e.sound(t, "a plain line", 3)
	out := e.ask(t, aiwork.MakeIn{Kind: aiservices.KindPresenter, ProjectID: e.project, Presenter: "stock:lk_amber_studio", Model: "heygen/avatar-iv", Clip: line.ID})
	fake.Next(fakeheygen.GarbledUnmade)
	as(t, -skew, func() {
		if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "looks for it again") {
			t.Fatalf("not found yet: %v", err)
		}
	})
	g := e.gen(t, out.Work.ID)
	if g.Outcome == nil || g.Forgotten != nil {
		t.Fatalf("after one look: outcome %+v forgotten %+v", g.Outcome, g.Forgotten)
	}
	near(t, "the outcome's time", g.Outcome.At, serverNow(t, e.a))
	ended := func(ago time.Duration) { e.setGen(t, g.ID, bson.M{"outcome.at": serverNow(t, e.a).Add(-ago)}) }
	ended(aiwork.LostVoiceLookFor - 30*time.Second)
	as(t, skew, func() {
		if _, err := e.run(t, out.JobID); err == nil || !strings.Contains(err.Error(), "looks for it again") {
			t.Fatalf("a computer ahead stopped looking 30 s early: %v", err)
		}
	})
	if g = e.gen(t, out.Work.ID); g.Forgotten != nil {
		t.Fatalf("given up 30 s early: %+v", g.Forgotten)
	}
	ended(aiwork.LostVoiceLookFor + 30*time.Second)
	as(t, -skew, func() { _, _ = e.run(t, out.JobID) })
	if g = e.gen(t, out.Work.ID); g.Forgotten == nil || g.Forgotten.OK || !strings.Contains(g.Forgotten.Detail, "found none") {
		t.Fatalf("a computer behind looked again after the time was up: %+v", g.Forgotten)
	}
}

// A presenter whose making lost its answer is retired at the server's time
// and looked for at the service for LostVoiceLookFor after it by the server's
// clock; when it was deleted is the server's time too (presenterMade compares
// it with the making's outcome, written on another computer).
func TestLostPresenterIsLookedForByTheServersClock(t *testing.T) {
	e := setup(t)
	in, _, _ := e.presenterReady(t, "Odile Brask", "fake/fake-presenter-make")
	in.DryRun = true
	dry, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	in.DryRun, in.MaxUSD = false, usd(dry.Estimate.USD)
	e.fake.Script(fakeai.Behavior{Kind: fakeai.Uncertain})
	out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", in, true)
	if err != nil {
		t.Fatal(err)
	}
	as(t, -skew, func() { _, _ = e.run(t, out.JobID) })
	v := e.presenter(t, in.ID)
	if v.State != models.PresenterRetired || v.Removed != nil || v.RetiredAt == nil || v.RetireJobID == "" {
		t.Fatalf("a presenter whose answer was lost: %+v", v.Presenter)
	}
	near(t, "when it was retired", *v.RetiredAt, serverNow(t, e.a))
	if v.TrainingID == "" {
		t.Fatalf("the making isn't noted: %+v", v.Presenter)
	}
	if g := e.gen(t, v.TrainingID); g.Outcome == nil {
		t.Fatal("the making has no outcome")
	} else {
		near(t, "the making's outcome", g.Outcome.At, serverNow(t, e.a))
	}
	retired := func(ago time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CPresenters).UpdateOne(context.Background(), bson.M{"_id": v.ID}, bson.M{"$set": bson.M{"retiredAt": serverNow(t, e.a).Add(-ago)}}); err != nil {
			t.Fatal(err)
		}
	}
	var wait *generate.Wait
	retired(aiwork.LostVoiceLookFor - 30*time.Second)
	as(t, skew, func() {
		if _, err := e.run(t, v.RetireJobID); !errors.As(err, &wait) {
			t.Fatalf("a computer ahead stopped looking 30 s early: %v", err)
		}
	})
	retired(aiwork.LostVoiceLookFor + 30*time.Second)
	as(t, -skew, func() {
		if _, err := e.run(t, v.RetireJobID); err != nil {
			t.Fatalf("a computer behind looked again after the time was up: %v", err)
		}
	})
	if v = e.presenter(t, in.ID); v.Removed == nil {
		t.Fatalf("not recorded as deleted: %+v", v.Presenter)
	}
	near(t, "when it was deleted", v.Removed.At, serverNow(t, e.a))
}

// Whether a presenter's consent is in force — when a clip is planned, when
// it is sent, when the sweep looks for ended consents — goes by the server's
// clock, and a presenter retired by the sweep says the server's time.
func TestPresenterConsentGoesByTheServersClock(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	in, odile, consent := e.presenterReady(t, "Odile Brask", "fake/fake-presenter-make")
	pr := e.makePresenter(t, in)
	line := e.sound(t, "odile line", 3)
	e.words(t, line, odile, strings.Fields("welcome to the garden"), func(i int) float64 { return float64(i) * 0.5 })
	clip := e.askTest(t, pr, line.ID)
	ends := func(in time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": consent.ID}, bson.M{"$set": bson.M{"expiresAt": serverNow(t, e.a).Add(in)}}); err != nil {
			t.Fatal(err)
		}
	}
	dry := aiwork.PresenterTestIn{ID: pr.Name, Clip: line.ID, ProjectID: e.project, DryRun: true}

	// 30 s left: a computer ahead plans with it, sends with it and keeps the presenter.
	ends(30 * time.Second)
	as(t, skew, func() {
		if est, err := call[aiwork.MakeOut](t, e.a, "presenter.test", dry, true); err != nil || !est.Estimate.Allowed {
			t.Fatalf("a computer ahead took a consent with 30 s left as ended: %+v %v", est, err)
		}
		if _, err := e.run(t, clip.JobID); err != nil {
			t.Fatalf("a computer ahead stopped a clip whose consent has 30 s left: %v", err)
		}
		if n := aiwork.Sweep(ctx, e.a); n != 0 {
			t.Fatalf("a computer ahead swept %d with the consent in force", n)
		}
	})
	if g := e.gen(t, clip.Work.ID); g.Outcome == nil || g.Outcome.State != models.GenDone {
		t.Fatalf("the clip: %+v", g.Outcome)
	}
	if v := e.presenter(t, pr.Name); v.State == models.PresenterRetired {
		t.Fatalf("retired by a computer ahead: %s", v.RetireNote)
	}
	// Ended 30 s ago: a computer behind plans nothing with it and retires the presenter.
	ends(-30 * time.Second)
	as(t, -skew, func() {
		if est, err := call[aiwork.MakeOut](t, e.a, "presenter.test", dry, true); err == nil && est.Estimate.Allowed {
			t.Fatalf("a computer behind took an ended consent as in force: %+v", est.Estimate)
		}
		if n := aiwork.Sweep(ctx, e.a); n == 0 {
			t.Fatal("a computer behind didn't retire a presenter whose consent ended")
		}
	})
	v := e.presenter(t, pr.Name)
	if v.State != models.PresenterRetired || !strings.Contains(v.RetireNote, "has ended") || v.RetiredAt == nil {
		t.Fatalf("after the consent ended: %+v", v.Presenter)
	}
	near(t, "when it was retired", *v.RetiredAt, serverNow(t, e.a))
}

// The service's consent words end by the server's clock: kept in its time
// whichever computer asked for them, too old to make the presenter with
// within two minutes of their end, and shown until then.
func TestConsentWordsEndByTheServersClock(t *testing.T) {
	e := setup(t)
	var in aiwork.PresenterMakeIn
	as(t, -skew, func() { in, _, _ = e.presenterReady(t, "Odile Brask", "fake/fake-presenter-make") })
	v := e.presenter(t, in.ID)
	if v.Challenge == nil || v.Challenge.ExpiresAt == nil {
		t.Fatalf("no consent words: %+v", v.Presenter)
	}
	near(t, "the words' end", *v.Challenge.ExpiresAt, serverNow(t, e.a).Add(30*time.Minute))
	ends := func(in time.Duration) {
		t.Helper()
		if _, err := e.a.DB.C(store.CPresenters).UpdateOne(context.Background(), bson.M{"_id": v.ID}, bson.M{"$set": bson.M{"challenge.expiresAt": serverNow(t, e.a).Add(in)}}); err != nil {
			t.Fatal(err)
		}
	}
	dry := in
	dry.DryRun = true
	ends(2*time.Minute + 30*time.Second)
	as(t, skew, func() {
		if out, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", dry, true); err != nil || !out.Estimate.Allowed {
			t.Fatalf("a computer ahead took words with 2 min 30 s left as too old: %+v %v", out, err)
		}
	})
	ends(time.Minute + 30*time.Second)
	as(t, -skew, func() {
		if _, err := call[aiwork.PresenterOut](t, e.a, "presenter.make", dry, true); codeOf(err) != apperr.Conflict || !strings.Contains(apperr.From(err).Message, "expired") {
			t.Fatalf("a computer behind made a presenter with words 1 min 30 s from their end: %v", err)
		}
	})
	ends(30 * time.Second)
	as(t, skew, func() {
		if v := e.presenter(t, in.ID); v.Words == "" {
			t.Fatalf("a computer ahead hid words with 30 s left: %s", v.Next)
		}
	})
	ends(-30 * time.Second)
	as(t, -skew, func() {
		if v := e.presenter(t, in.ID); v.Words != "" || !strings.Contains(v.Next, "expired") {
			t.Fatalf("a computer behind showed words that ended: %q %s", v.Words, v.Next)
		}
	})
}

// Other AI work's ticket says when it was sent in the server's time too, which
// activity on any computer reads for how long it has been making (D360).
func TestAWorksTicketIsStampedInTheServersTime(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	testutil.ServerOff(t, e.a.DB, -10*time.Minute)
	out, _ := e.make(t, aiwork.MakeIn{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "a paper boat on a pond"})
	if _, err := e.run(t, out.JobID); err != nil {
		t.Fatal(err)
	}
	g := e.gen(t, out.Work.ID)
	now, err := e.a.DB.Now(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if g.Ticket == nil {
		t.Fatal("no ticket")
	}
	near(t, "the ticket", g.Ticket.SubmittedAt, now)
}
