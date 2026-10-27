package generate_test

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// clipIn adds a clip of the given length (picture and sound, or sound only)
// to a project ("" = none, the Inbox), looked at for faces with nobody on
// screen.
func (e *env) clipIn(t *testing.T, name, projectID string, seconds int, picture bool) *models.Asset {
	t.Helper()
	ctx := context.Background()
	dir := t.TempDir()
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	p := filepath.Join(dir, name+".wav")
	if picture {
		p = filepath.Join(dir, name+".mp4")
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x180:rate=24:duration=%d", seconds))
	}
	args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=520:duration=%d", seconds), "-shortest")
	if picture {
		args = append(args, "-pix_fmt", "yuv420p")
	}
	args = append(args, "-metadata", "comment="+name, p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	res := ingest.File(ctx, e.a, p, ingest.Options{ProjectID: projectID, FootageOnly: true})
	if res.AssetID == "" {
		t.Fatalf("ingest %+v", res)
	}
	if picture {
		_ = e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), res.AssetID, bson.M{"$set": bson.M{"analysis.visual": "available"}})
	}
	var as models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), res.AssetID, &as); err != nil {
		t.Fatal(err)
	}
	return &as
}

// waitFor awaits a condition (the fake answers in its own goroutines).
func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	end := time.Now().Add(30 * time.Second)
	for !ok() {
		if time.Now().After(end) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func secs(v float64) *float64 { return &v }

// Continuing a clip and changing it (generate.extend, generate.edit): priced
// first, the part of the clip that goes with the request is exact, the new
// clip is named after the one it continues, a generated clip can be continued
// by its generation, and what can't be done is refused in plain words before
// anything is sent.
func TestExtendAndChangeAClip(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	src := e.clipIn(t, "pond", e.project, 4, true)

	dry, err := call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: src.ID, Seconds: 3, Resolution: "360p", DryRun: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	est := dry.Estimate
	if !dry.DryRun || len(dry.JobIDs) != 0 || len(dry.Generations) != 0 || !est.Allowed || est.Operation != models.GenExtend || est.Task != videogen.TaskExtend ||
		est.Seconds != 3 || est.Resolution != "360p" || len(est.References) != 1 || !strings.HasPrefix(est.References[0], "Continues “") ||
		!strings.Contains(est.References[0], "from 0:04.0") || !strings.HasPrefix(est.Summary, "3 more seconds of a clip (draft, 360p, wide) costs about $0.") {
		t.Fatalf("estimate %+v", est)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("a dry run sends nothing")
	}
	// The estimate says whose budget pays: the clip's own project (Auto checks it, D241).
	if est.ProjectID != e.project {
		t.Fatalf("paid from %q, want the clip's project %q", est.ProjectID, e.project)
	}
	out, err := call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: src.ID, Seconds: 3, Resolution: "360p", Prompt: "the boat drifts out of view",
		MaxUSD: usd(est.USD)}, false)
	if err != nil {
		t.Fatal(err)
	}
	g := out.Generations[0]
	ref := g.References[0]
	if g.Operation != models.GenExtend || g.ProjectID != e.project || len(g.SourceAssets) != 1 || g.SourceAssets[0] != src.ID || len(g.References) != 1 ||
		ref.Role != models.RefInput || ref.AssetID != src.ID || ref.From != 0 || ref.To != src.Media.Duration ||
		g.SentPrompt != "Continue the scene from the end of this clip. the boat drifts out of view" || g.Template == nil ||
		!strings.HasSuffix(g.Template.Vars["title"], " (continued)") {
		t.Fatalf("record %+v %+v", g.Generation, g.Template)
	}
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	if it := e.fake.Interactions()[0]; it.Task != videogen.TaskExtend || len(it.Videos) != 1 || it.Images != 0 {
		t.Fatalf("sent %+v", it)
	}
	v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: g.ID}, false)
	if err != nil {
		t.Fatal(err)
	}
	if v.State != generate.StateDone || len(v.Clips) != 1 || !strings.HasSuffix(v.Clips[0].Title, "(continued)") || v.Clips[0].ProjectID != e.project {
		t.Fatalf("made %+v", v)
	}

	// A generated clip is continued through its generation: the project, the
	// model and its settings come from it, and the new one names it as its parent.
	more, err := call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: g.ID, Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	m := more.Generations[0]
	if m.ParentID != g.ID || m.ProjectID != e.project || len(m.SourceAssets) != 1 || m.SourceAssets[0] != v.Clips[0].AssetID ||
		m.SentPrompt != "Continue the scene from the end of this clip." || m.GroupID == g.GroupID {
		t.Fatalf("continued generation %+v", m.Generation)
	}
	shown, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: m.ID}, false)
	if err != nil || shown.Parent == nil || shown.Parent.ID != g.ID || shown.Parent.Operation != models.GenExtend {
		t.Fatalf("lineage %+v %v", shown.Parent, err)
	}
	// One that isn't made yet has nothing to continue.
	if _, err := call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: m.ID, DryRun: true}, false); codeOf(err) != apperr.Conflict ||
		!strings.Contains(apperr.From(err).Message, "isn't ready") {
		t.Fatalf("a generation still being made: %v", err)
	}

	// Changing a part of a clip: the model's shortest shot at least, the part said in words.
	ed, err := call[generate.GenRunOut](t, e.a, "generate.edit", generate.GenEditIn{ID: src.ID, Instruction: "make it night", From: secs(0.5), To: secs(2.5),
		Resolution: "360p", DryRun: true}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ee := ed.Estimate; !ee.Allowed || ee.Task != videogen.TaskEdit || ee.Operation != models.GenEdit || ee.Seconds != 3 || len(ee.References) != 1 ||
		!strings.HasPrefix(ee.References[0], "Changes 0:00.5 to 0:02.5 of “") || !strings.HasPrefix(ee.Summary, "A changed clip (draft, 360p, wide)") {
		t.Fatalf("edit estimate %+v", ee)
	}
	edOut, err := call[generate.GenRunOut](t, e.a, "generate.edit", generate.GenEditIn{ID: src.ID, Instruction: "make it night", From: secs(0.5), To: secs(2.5),
		Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if eg := edOut.Generations[0]; eg.SentPrompt != "" || eg.Prompt != "make it night" || !strings.Contains(eg.Template.Vars["title"], "(changed: make it night)") ||
		eg.References[0].From != 0.5 || eg.References[0].To != 2.5 {
		t.Fatalf("edit record %+v %+v", eg.Generation, eg.Template)
	}

	// Refused, in plain words, before anything is sent.
	refusals := []struct {
		name string
		op   string
		in   any
		code string
		says string
	}{
		{"a part longer than the model takes", "generate.edit", generate.GenEditIn{ID: src.ID, Instruction: "make it night", From: secs(0), To: secs(12), DryRun: true},
			apperr.InvalidInput, "changes at most 10 seconds at a time"},
		{"no instruction", "generate.edit", generate.GenEditIn{ID: src.ID, Instruction: "   ", DryRun: true}, apperr.InvalidInput, "Say what to change."},
		{"a clip that isn't there", "generate.extend", generate.GenExtendIn{ID: ids.New(ids.Asset), DryRun: true}, apperr.NotFound, "isn't in this workspace"},
	}
	sound := e.clipIn(t, "birdsong", e.project, 3, false)
	refusals = append(refusals, struct {
		name string
		op   string
		in   any
		code string
		says string
	}{"a sound file", "generate.extend", generate.GenExtendIn{ID: sound.ID, DryRun: true}, apperr.UnsupportedMedia, "has no picture"})
	loose := e.clipIn(t, "loose", "", 4, true)
	refusals = append(refusals, struct {
		name string
		op   string
		in   any
		code string
		says string
	}{"a clip in no project", "generate.extend", generate.GenExtendIn{ID: loose.ID, DryRun: true}, apperr.InvalidInput, "Choose the project the new clip goes to."})
	created := e.fake.Calls("create")
	for _, c := range refusals {
		_, err := call[generate.GenRunOut](t, e.a, c.op, c.in, false)
		if codeOf(err) != c.code || !strings.Contains(apperr.From(err).Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if e.fake.Calls("create") != created {
		t.Fatal("a refused request reached the provider")
	}
	// A clip in no project goes where the person says.
	if res, err := call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: loose.ID, ProjectID: e.project, Seconds: 3, Resolution: "360p", DryRun: true}, false); err != nil ||
		!res.Estimate.Allowed {
		t.Fatalf("a loose clip for a project: %+v %v", res, err)
	}
}

// A look's test sheet (looks.test): a few short, cheap draft shots a person
// asks for before approving the look — priced first, people only, kept out of
// the Inbox, on the look's sheet, the bell pointing at the person. Changing
// what a look shows needs approving again; a withdrawn look is done with.
func TestLookTestSheetAndChanges(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ada := e.person(t, "Ada")
	studio := e.clipWithFace(t, "studio", ada, "Ada", true)
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada", Self: true,
		Statement: "I, Ada, agree to MetaRush making videos with my likeness for my own projects.", Adult: true}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "Ada", Person: "Ada", Description: "A woman in her thirties with short dark hair and round glasses.",
		Stills: []generate.LookStillIn{{AssetID: studio.ID, At: 1}, {AssetID: studio.ID, At: 2.5}}}, true); err != nil {
		t.Fatal(err)
	}

	if _, err := call[generate.GenRunOut](t, e.a, "looks.test", generate.LookTestIn{Look: "ada", DryRun: true}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("test shots are a person's spending: %v", err)
	}
	dry, err := call[generate.GenRunOut](t, e.a, "looks.test", generate.LookTestIn{Look: "ada", DryRun: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if !dry.DryRun || dry.Estimate.Takes != 2 || dry.Estimate.USD <= 0 || len(dry.JobIDs) != 0 ||
		!strings.HasPrefix(dry.Summary, "2 test shots for the look “Ada” (draft, 360p) cost about $0.") {
		t.Fatalf("dry run %+v", dry)
	}
	if _, err := call[generate.GenRunOut](t, e.a, "looks.test", generate.LookTestIn{Look: "ada"}, true); codeOf(err) != apperr.ConfirmRequired {
		t.Fatalf("no agreed price: %v", err)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("nothing is sent before the price is agreed")
	}
	out, err := call[generate.GenRunOut](t, e.a, "looks.test", generate.LookTestIn{Look: "ada", MaxUSD: usd(dry.Estimate.USD)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Generations) != 2 || len(out.JobIDs) != 2 || out.Estimate.Takes != 2 || !strings.Contains(out.Summary, "2 test shots of the look “Ada”") {
		t.Fatalf("test sheet %+v", out)
	}
	for _, g := range out.Generations {
		if g.Operation != models.GenLookTest || g.Params.Resolution != "360p" || g.Params.Seconds != 3 || g.Look == nil || g.Look.ConsentID == "" ||
			len(g.References) != 2 || g.ProjectID != "" || !strings.HasPrefix(g.Prompt, "Ada ") {
			t.Fatalf("test shot %+v", g.Generation)
		}
	}
	lv, err := call[generate.LookView](t, e.a, "looks.show", generate.LookRefIn{Look: "ada"}, false)
	if err != nil || len(lv.TestShots) != 2 || lv.Usable || lv.Consent == nil || lv.Consent.Status != "active" {
		t.Fatalf("look sheet %+v %v", lv, err)
	}
	// Made: on the look's test sheet, out of the Inbox; the bell points at the person.
	if _, err := e.run(t, ctx, out.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	made, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: out.Generations[0].ID}, false)
	if err != nil || made.State != generate.StateDone || len(made.Clips) != 1 {
		t.Fatalf("test shot made: %+v %v", made, err)
	}
	var clip models.Asset
	if err := e.a.DB.GetByID(ctx, store.CAssets, e.a.WorkspaceID(), made.Clips[0].AssetID, &clip); err != nil || clip.ArchivedAt == nil {
		t.Fatalf("a test shot stays out of the Inbox: %+v %v", clip.ArchivedAt, err)
	}
	var n models.Notification
	if err := e.a.DB.C(store.CNotifications).FindOne(ctx, bson.M{"groupKey": "generation:" + made.ID}).Decode(&n); err != nil ||
		n.Title != "A test shot of the look “Ada” is ready" || n.Link != "/people?person="+ada || !strings.Contains(n.Body, "test sheet") {
		t.Fatalf("notification %+v %v", n, err)
	}

	// Approved, then changed: a new title keeps the approval, a new description doesn't.
	if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "ada"}, true); err != nil {
		t.Fatal(err)
	}
	up, err := call[generate.LookOut](t, e.a, "looks.update", generate.LookUpdateIn{Look: "ada", Title: "Ada L."}, true)
	if err != nil || up.Look.State != models.LookApproved || up.Look.Title != "Ada L." || strings.Contains(up.Summary, "approving again") {
		t.Fatalf("a new title: %+v %v", up, err)
	}
	before := up.Look.Version
	up, err = call[generate.LookOut](t, e.a, "looks.update", generate.LookUpdateIn{Look: "ada",
		Description: "A woman in her thirties with short dark hair, round glasses and a green scarf."}, true)
	if err != nil || up.Look.State != models.LookDraft || up.Look.Version == before || up.Look.ApprovedAt != nil || !strings.Contains(up.Summary, "needs approving again") {
		t.Fatalf("a new description: %+v %v", up, err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.update", generate.LookUpdateIn{Look: "ada", Style: models.LookStylized}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("a style without saying which: %v", err)
	}
	bob := e.person(t, "Bob")
	porch := e.clipWithFace(t, "porch", bob, "Bob", true)
	_, err = call[generate.LookOut](t, e.a, "looks.update", generate.LookUpdateIn{Look: "ada", Stills: []generate.LookStillIn{{AssetID: porch.ID, At: 1}, {AssetID: studio.ID, At: 1}}}, true)
	if codeOf(err) != apperr.ConsentRequired || !strings.Contains(apperr.From(err).Message, "shows Bob, not Ada") {
		t.Fatalf("someone else's face in Ada's look: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.update", generate.LookUpdateIn{Look: "ada", Stills: []generate.LookStillIn{{AssetID: studio.ID, At: 2}}}, true); err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "ada"}, true); codeOf(err) != apperr.InvalidInput ||
		!strings.Contains(apperr.From(err).Message, "needs 2 or 3 stills") {
		t.Fatalf("approving a look with one still: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "Ada", Person: "Ada", Description: "Another look with the same name.",
		Stills: []generate.LookStillIn{{AssetID: studio.ID, At: 1}}}, true); codeOf(err) != apperr.Conflict {
		t.Fatalf("two looks with one name: %v", err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "!!!", Description: "A look whose name has no letters at all.",
		Stills: []generate.LookStillIn{{AssetID: studio.ID, At: 1}}}, true); codeOf(err) != apperr.InvalidInput {
		t.Fatalf("a name with no letters: %v", err)
	}

	// Listed: one look, not ready (a draft).
	ls, err := call[generate.LookListOut](t, e.a, "looks.list", generate.LookListIn{Person: "Ada"}, false)
	if err != nil || len(ls.Looks) != 1 || ls.Looks[0].Usable || ls.Summary != "1 look, 0 ready to use." {
		t.Fatalf("list %+v %v", ls, err)
	}
	// Withdrawn: no tests, no changes, no approval; withdrawing again says so; listed only with All.
	if _, err := call[generate.LookOut](t, e.a, "looks.revoke", generate.LookDecideIn{Look: "ada", Note: "not her best"}, true); err != nil {
		t.Fatal(err)
	}
	if again, err := call[generate.LookOut](t, e.a, "looks.revoke", generate.LookDecideIn{Look: "ada"}, true); err != nil || again.Summary != "It was withdrawn already." {
		t.Fatalf("withdrawn twice: %+v %v", again, err)
	}
	for _, c := range []struct {
		op string
		in any
	}{{"looks.test", generate.LookTestIn{Look: "ada", DryRun: true}}, {"looks.update", generate.LookUpdateIn{Look: "ada", Title: "Ada again"}},
		{"looks.approve", generate.LookDecideIn{Look: "ada"}}} {
		if _, err := ops.CallTyped[generate.LookOut](ctx, e.a, c.op, c.in, ops.Caller{Human: true}); codeOf(err) != apperr.Conflict ||
			!strings.Contains(apperr.From(err).Message, "was withdrawn") {
			t.Errorf("%s on a withdrawn look: %v", c.op, err)
		}
	}
	if ls, _ := call[generate.LookListOut](t, e.a, "looks.list", generate.LookListIn{}, false); len(ls.Looks) != 0 || ls.Summary != "No looks yet." {
		t.Fatalf("withdrawn looks are listed only on request: %+v", ls)
	}
	if ls, _ := call[generate.LookListOut](t, e.a, "looks.list", generate.LookListIn{All: true}, false); len(ls.Looks) != 1 || ls.Looks[0].State != models.LookRevoked {
		t.Fatalf("all looks: %+v", ls)
	}
	if _, err := call[generate.LookView](t, e.a, "looks.show", generate.LookRefIn{Look: "nobody"}, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("an unknown look: %v", err)
	}
}

// A look's test sheet needs the person's consent in force: one that ran out
// stops it before anything is priced or sent.
func TestLookTestNeedsConsentInForce(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ada := e.person(t, "Ada")
	studio := e.clipWithFace(t, "studio", ada, "Ada", true)
	c, err := call[generate.ConsentOut](t, e.a, "consent.record", generate.ConsentRecordIn{Person: "Ada", Self: true,
		Statement: "I, Ada, agree to MetaRush making videos with my likeness for my own projects.", Adult: true}, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "Ada", Person: "Ada", Description: "A woman in her thirties with short dark hair.",
		Stills: []generate.LookStillIn{{AssetID: studio.ID, At: 1}, {AssetID: studio.ID, At: 2}}}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.a.DB.Update(ctx, store.CConsents, e.a.WorkspaceID(), c.Consent.ID, bson.M{"$set": bson.M{"expiresAt": time.Now().Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	_, err = call[generate.GenRunOut](t, e.a, "looks.test", generate.LookTestIn{Look: "ada", DryRun: true}, true)
	if codeOf(err) != apperr.ConsentRequired || !strings.Contains(apperr.From(err).Message, "Ada's consent isn't in force") {
		t.Fatalf("a look test after the consent ran out: %v", err)
	}
	shown, err := call[generate.ConsentView](t, e.a, "consent.show", generate.ConsentIDIn{ID: c.Consent.ID}, false)
	if err != nil || shown.Status != "expired" || !strings.Contains(shown.Summary, "ended on") || len(shown.Looks) != 1 {
		t.Fatalf("an expired consent: %+v %v", shown, err)
	}
	if _, err := call[generate.LookOut](t, e.a, "looks.approve", generate.LookDecideIn{Look: "ada"}, true); codeOf(err) != apperr.ConsentRequired {
		t.Fatalf("approving without consent in force: %v", err)
	}
}

// Consent records (D126): what one covers and until when, refused when the
// recording can't be someone's consent, listed and shown in plain words,
// withdrawn once — with the looks that relied on it.
func TestConsentRecords(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	if l, err := call[generate.ConsentListOut](t, e.a, "consent.list", generate.ConsentListIn{}, false); err != nil || len(l.Consents) != 0 || l.Summary != "No consent recorded yet." {
		t.Fatalf("empty list %+v %v", l, err)
	}
	jess := e.person(t, "Jess")
	says := e.clipWithFace(t, "jess-says-yes", jess, "Jess", true)
	until := time.Now().UTC().AddDate(1, 0, 0).Format("2006-01-02")
	rec := generate.ConsentRecordIn{Person: "Jess", Statement: "I, Jess, agree to my likeness in generated video for the pond film.", Recording: says.ID, Adult: true,
		Projects: []string{e.project}, ExpiresOn: until}

	bad := []struct {
		name string
		in   func(generate.ConsentRecordIn) generate.ConsentRecordIn
		code string
		says string
	}{
		{"a project that isn't there", func(r generate.ConsentRecordIn) generate.ConsentRecordIn {
			r.Projects = []string{ids.New(ids.Project)}
			return r
		}, apperr.NotFound, "isn't in this workspace"},
		{"a day that isn't one", func(r generate.ConsentRecordIn) generate.ConsentRecordIn { r.ExpiresOn = "2027-02-30"; return r }, apperr.InvalidInput, "as a date like"},
		{"a day that has passed", func(r generate.ConsentRecordIn) generate.ConsentRecordIn { r.ExpiresOn = "2020-01-01"; return r }, apperr.InvalidInput, "That day has passed."},
		{"a recording that isn't there", func(r generate.ConsentRecordIn) generate.ConsentRecordIn { r.Recording = ids.New(ids.Asset); return r }, apperr.ConsentRequired,
			"with the person speaking"},
	}
	for _, c := range bad {
		if _, err := call[generate.ConsentOut](t, e.a, "consent.record", c.in(rec), true); codeOf(err) != c.code || !strings.Contains(apperr.From(err).Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	// A generated clip is never anyone's consent.
	shot, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a pond at dawn", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.run(t, ctx, shot.JobIDs[0]); err != nil {
		t.Fatal(err)
	}
	made, _ := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: shot.Generations[0].ID}, false)
	gen := rec
	gen.Recording = made.Clips[0].AssetID
	if _, err := call[generate.ConsentOut](t, e.a, "consent.record", gen, true); codeOf(err) != apperr.ConsentRequired ||
		!strings.Contains(apperr.From(err).Message, "A generated clip can't be someone's consent") {
		t.Fatalf("a generated clip as consent: %v", err)
	}

	c, err := call[generate.ConsentOut](t, e.a, "consent.record", rec, true)
	if err != nil {
		t.Fatal(err)
	}
	zone, _ := time.LoadLocation(c.Consent.ExpiresZone) // the last day ends where it was recorded (D274)
	if !strings.HasPrefix(c.Summary, "Recorded Jess' consent.") || c.Consent.Status != "active" || c.Consent.Scope.AllProjects || len(c.Consent.Scope.Projects) != 1 ||
		c.Consent.ExpiresAt == nil || zone == nil || c.Consent.ExpiresAt.In(zone).Format("2006-01-02") != until || !strings.Contains(c.Consent.Summary, "covers 1 project until") ||
		c.Consent.Link != "/library/people?person="+jess {
		t.Fatalf("recorded %+v", c)
	}
	// A look relying on it.
	if _, err := call[generate.LookOut](t, e.a, "looks.create", generate.LookCreateIn{Title: "Jess", Person: "Jess", Description: "A tall woman with a red raincoat.",
		Stills: []generate.LookStillIn{{AssetID: says.ID, At: 1}, {AssetID: says.ID, At: 2}}}, true); err != nil {
		t.Fatal(err)
	}
	l, err := call[generate.ConsentListOut](t, e.a, "consent.list", generate.ConsentListIn{Person: "Jess"}, false)
	if err != nil || len(l.Consents) != 1 || l.Summary != "1 consent record, 1 in force." || len(l.Consents[0].Looks) != 1 {
		t.Fatalf("list %+v %v", l, err)
	}
	if _, err := call[generate.ConsentListOut](t, e.a, "consent.list", generate.ConsentListIn{Person: "Zebedee Quorn"}, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("someone nobody added: %v", err)
	}
	if _, err := call[generate.ConsentView](t, e.a, "consent.show", generate.ConsentIDIn{ID: ids.New(ids.Consent)}, false); codeOf(err) != apperr.NotFound {
		t.Fatalf("a consent that isn't there: %v", err)
	}

	// Withdrawn: people only, once; the look goes with it.
	if _, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: c.Consent.ID}, false); codeOf(err) != apperr.HumanOnly {
		t.Fatalf("withdrawing is people only: %v", err)
	}
	r, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: c.Consent.ID, Note: "she changed her mind"}, true)
	if err != nil || r.Consent.Status != "revoked" || len(r.LooksRevoked) != 1 || r.LooksRevoked[0] != "Jess" || !strings.HasSuffix(r.Summary, "1 look withdrawn too.") ||
		r.Consent.RevokeNote != "she changed her mind" || !strings.Contains(r.Consent.Summary, "was withdrawn on") {
		t.Fatalf("revoked %+v %v", r, err)
	}
	if again, err := call[generate.ConsentOut](t, e.a, "consent.revoke", generate.ConsentRevokeIn{ID: c.Consent.ID}, true); err != nil || again.Summary != "It was withdrawn already." {
		t.Fatalf("withdrawn twice: %+v %v", again, err)
	}
	if lv, _ := call[generate.LookView](t, e.a, "looks.show", generate.LookRefIn{Look: "jess"}, false); lv.State != models.LookRevoked || lv.Usable {
		t.Fatalf("the look relying on it: %+v", lv.GenLook)
	}
	if l, _ := call[generate.ConsentListOut](t, e.a, "consent.list", generate.ConsentListIn{}, false); l.Summary != "1 consent record, 0 in force." {
		t.Fatalf("after withdrawing: %q", l.Summary)
	}
}

// Stopping a shot while Google is making it: the worker's context ends, the
// job says the person asked, so MetaRush asks Google to stop and to delete
// it, settles what it cost and rings no bell (the person asked).
func TestCancelWhileItIsBeingMade(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	e.fake.ReadyAfter = 1 << 20 // it never finishes by itself
	out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: "a very slow boat", Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
	if err != nil {
		t.Fatal(err)
	}
	gid := out.Generations[0].ID
	j, err := e.a.Queue.Get(ctx, out.JobIDs[0])
	if err != nil {
		t.Fatal(err)
	}
	rctx, stop := context.WithCancel(ctx)
	defer stop()
	done := make(chan error, 1)
	go func() {
		_, err := generate.Run(rctx, e.a, j, nil)
		done <- err
	}()
	waitFor(t, "the first check on the video", func() bool { return e.fake.Calls("get") >= 1 })
	c, err := call[generate.GenCancelOut](t, e.a, "generate.cancel", generate.GenIDIn{ID: gid}, false)
	if err != nil || c.Summary != "Asked it to stop. MetaRush tells Google Gemini to stop too." {
		t.Fatalf("cancel %+v %v", c, err)
	}
	stop() // the worker's heartbeat sees the request and ends the job's context
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("the run ends with its context: %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("the run never stopped")
	}
	v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: gid}, false)
	if err != nil || v.State != generate.StateCanceled || v.Outcome == nil || v.Outcome.Message != "It was stopped." || v.Settled == nil || v.Forgotten == nil || !v.Forgotten.OK ||
		v.Summary != "A 3-second shot was stopped." {
		t.Fatalf("stopped %+v %v", v, err)
	}
	if e.fake.Calls("cancel") != 1 {
		t.Fatalf("Google is asked to stop it: %d", e.fake.Calls("cancel"))
	}
	if ki, kf := e.fake.Kept(); ki != 0 || kf != 0 {
		t.Fatalf("left at the provider: %d %d", ki, kf)
	}
	if l := ledger(t, e.a, "project", e.project); l.HeldMicros != 0 {
		t.Fatalf("money still held: %+v", l)
	}
	if n, _ := e.a.DB.C(store.CNotifications).CountDocuments(ctx, bson.M{"groupKey": "generation:" + gid}); n != 0 {
		t.Fatalf("a stop the person asked for rings the bell (%d)", n)
	}
	again, err := call[generate.GenCancelOut](t, e.a, "generate.cancel", generate.GenIDIn{ID: gid}, false)
	if err != nil || again.Summary != "It has already ended: a 3-second shot was stopped." {
		t.Fatalf("asking again: %+v %v", again, err)
	}
}

// The sweep (every few minutes in the daemon) closes generations whose job
// ended without an outcome — stopped before it was sent (nothing spent),
// stopped while being made or given up on (the estimate counts, in case it
// was made) — leaves the rest alone, and lets go of money held for a request
// whose record was never saved.
func TestSweepClosesWhatWasLeft(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	ws := e.a.WorkspaceID()
	shot := func(prompt string) generate.GenView {
		t.Helper()
		out, err := call[generate.GenRunOut](t, e.a, "generate.shot", generate.GenShotIn{ProjectID: e.project, Prompt: prompt, Seconds: 3, Resolution: "360p", MaxUSD: usd(1)}, false)
		if err != nil {
			t.Fatal(err)
		}
		return out.Generations[0]
	}
	aged := func(id string) {
		t.Helper()
		if err := e.a.DB.Update(ctx, store.CGenerations, ws, id, bson.M{"$set": bson.M{"createdAt": time.Now().UTC().Add(-2 * time.Minute)}}); err != nil {
			t.Fatal(err)
		}
	}
	early := shot("a boat stopped before it was sent")
	if _, err := e.a.Queue.RequestCancel(ctx, early.JobID); err != nil {
		t.Fatal(err)
	}
	aged(early.ID)
	late := shot("a boat stopped while it was made")
	if err := e.a.DB.Update(ctx, store.CGenerations, ws, late.ID, bson.M{"$set": bson.M{"ticket": models.GenTicket{ID: "interactions/left", SubmittedAt: time.Now().UTC()}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.Queue.RequestCancel(ctx, late.JobID); err != nil {
		t.Fatal(err)
	}
	aged(late.ID)
	failed := shot("a boat nobody could make")
	if err := e.a.DB.Update(ctx, store.CJobs, ws, failed.JobID, bson.M{"$set": bson.M{"state": models.JobFailed, "error": "the worker gave up after 6 tries"}}); err != nil {
		t.Fatal(err)
	}
	aged(failed.ID)
	waiting := shot("a boat still waiting")
	aged(waiting.ID)
	fresh := shot("a boat stopped a moment ago")
	if _, err := e.a.Queue.RequestCancel(ctx, fresh.JobID); err != nil {
		t.Fatal(err)
	}
	// Money held for a request that was never saved (the process stopped in between), an hour ago.
	orphan := ids.New(ids.Generation)
	if _, err := generate.Reserve(ctx, e.a, e.project, orphan, 0.4, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := e.a.DB.C(store.CGenSpend).UpdateMany(ctx, bson.M{"workspaceId": ws, "holds.id": orphan}, bson.M{"$set": bson.M{"holds.$.at": time.Now().UTC().Add(-2 * time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	heldBefore := ledger(t, e.a, "project", e.project).HeldMicros

	if n := generate.Sweep(ctx, e.a); n != 5 {
		t.Fatalf("swept %d: three generations closed and the orphan's two holds let go", n)
	}
	show := func(id string) generate.GenView {
		t.Helper()
		v, err := call[generate.GenView](t, e.a, "generate.show", generate.GenIDIn{ID: id}, false)
		if err != nil {
			t.Fatal(err)
		}
		return *v
	}
	if v := show(early.ID); v.State != generate.StateCanceled || v.Outcome.Message != "It was stopped before it was sent. Nothing was spent." || v.Settled == nil || v.Settled.USD != 0 {
		t.Fatalf("stopped before sending: %+v %+v", v.Outcome, v.Settled)
	}
	if v := show(late.ID); v.State != generate.StateCanceled || v.Outcome.Message != "It was stopped." || v.Settled == nil || v.Settled.USD != v.Estimate.USD ||
		v.Outcome.Actual == nil || v.Outcome.Actual.Basis != "assumed" {
		t.Fatalf("stopped while made: %+v %+v", v.Outcome, v.Settled)
	}
	if v := show(failed.ID); v.State != generate.StateFailed || v.Outcome.Message != "MetaRush gave up on this video: the worker gave up after 6 tries" ||
		v.Settled == nil || v.Settled.USD != v.Estimate.USD || v.Summary != v.Outcome.Message {
		t.Fatalf("given up: %+v %+v", v.Outcome, v.Settled)
	}
	if v := show(waiting.ID); v.Outcome != nil || v.State != generate.StateWaiting || v.Summary != "A 3-second shot: waiting to be sent to Google Gemini." {
		t.Fatalf("still waiting: %+v %q", v.Outcome, v.Summary)
	}
	if v := show(fresh.ID); v.Outcome != nil || v.State != generate.StateCanceled {
		t.Fatalf("a moment ago (left for the next sweep): %+v %q", v.Outcome, v.State)
	}
	l := ledger(t, e.a, "project", e.project)
	if want := heldBefore - generate.Micros(early.Estimate.USD+late.Estimate.USD+failed.Estimate.USD+0.4); l.HeldMicros != want {
		t.Fatalf("held %d, want %d", l.HeldMicros, want)
	}
	if want := generate.Micros(late.Estimate.USD + failed.Estimate.USD); l.SpentMicros != want || l.Count != 2 {
		t.Fatalf("spent %d (count %d), want %d", l.SpentMicros, l.Count, want)
	}
	// Sweeping again changes nothing.
	if n := generate.Sweep(ctx, e.a); n != 0 {
		t.Fatalf("swept again: %d", n)
	}
	// Reserve refuses what goes over the day's cap, in plain words.
	_, err := generate.Reserve(ctx, e.a, e.project, ids.New(ids.Generation), 50, time.Now())
	if codeOf(err) != apperr.Budget || !strings.Contains(apperr.From(err).Message, "today's limit") {
		t.Fatalf("over the cap: %v", err)
	}
}

// generate.configure: the team's model, draft and final quality and daily
// limit, a project's budget (none, or back to the default); what it can't
// take is said plainly; without the key the settings say so and nothing is sent.
func TestConfigureAndSettings(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	for _, c := range []struct {
		name string
		in   generate.GenConfigureIn
		code string
		says string
	}{
		{"nothing to change", generate.GenConfigureIn{}, apperr.InvalidInput, "Say what to change"},
		{"a budget for no project", generate.GenConfigureIn{BudgetUSD: usd(5)}, apperr.InvalidInput, "Budgets are set for each project"},
		{"a project that isn't there", generate.GenConfigureIn{ProjectID: ids.New(ids.Project), BudgetUSD: usd(3)}, apperr.NotFound, "That project isn't in this workspace."},
	} {
		if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", c.in, true); codeOf(err) != c.code || !strings.Contains(apperr.From(err).Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if _, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{Model: "a-model-nobody-makes"}, true); err == nil {
		t.Fatal("an unknown model was taken")
	}
	st, err := call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{Model: "gemini-omni-1.1-flash", DraftResolution: "360p", FinalResolution: "4k",
		DailyCapUSD: usd(25.004)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if st.Model != "gemini-omni-1.1-flash" || st.DraftResolution != "360p" || st.FinalResolution != "4k" || st.DailyCapUSD != 25 || st.DailyCapDefault ||
		len(st.Models) != 1 || !st.Models[0].Default || st.Models[0].PerSecondUSD["360p"] <= 0 || len(st.SpendLimitsPer10Minutes) == 0 || !st.KeyReady {
		t.Fatalf("team settings %+v", st)
	}
	// New shots follow them: a draft is 360p, final quality 4K (upscaled, said so).
	in := generate.GenShotIn{ProjectID: e.project, Prompt: "a heron lands", Seconds: 3}
	est, err := call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || est.Resolution != "360p" || est.Quality != "draft" {
		t.Fatalf("draft %+v %v", est, err)
	}
	in.Quality = "final"
	est, err = call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || est.Resolution != "4k" || !est.Upscaled || len(est.Notes) != 1 || !strings.Contains(est.Notes[0], "4K video is made at a lower resolution and upscaled") ||
		!strings.Contains(est.Summary, "final quality, 4K") {
		t.Fatalf("final %+v %v", est, err)
	}
	// No limit for the project (the daily cap still applies), then back to the default.
	st, err = call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, BudgetUSD: usd(0)}, true)
	if err != nil || st.ProjectBudgetUSD != nil || st.ProjectBudgetDefault || st.Budget.Project == nil || st.Budget.Project.LimitUSD != nil ||
		!strings.Contains(st.Summary, "this project: $0 spent, no project limit") {
		t.Fatalf("no project limit: %+v %v", st, err)
	}
	st, err = call[generate.GenSettingsOut](t, e.a, "generate.configure", generate.GenConfigureIn{ProjectID: e.project, ClearBudget: true}, true)
	if err != nil || st.ProjectBudgetUSD == nil || *st.ProjectBudgetUSD != 10 || !st.ProjectBudgetDefault || !strings.Contains(st.Summary, "this project: $0 of $10.00") {
		t.Fatalf("back to the default: %+v %v", st, err)
	}
	// Without the key: said in the settings and the estimate; a shot is refused before anything is sent.
	if _, err := ops.CallTyped[providers.Status](ctx, e.a, "provider.remove", providers.NameIn{Name: videogen.GeminiName}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	st, err = call[generate.GenSettingsOut](t, e.a, "generate.settings", generate.GenSettingsIn{ProjectID: e.project}, false)
	if err != nil || st.KeyReady || !strings.HasPrefix(st.Summary, "Google Gemini isn't set up on this computer yet. Today: $0 of $25.00 spent") {
		t.Fatalf("no key: %+v %v", st, err)
	}
	in.Quality = ""
	est, err = call[generate.GenEstimate](t, e.a, "generate.estimate", in, false)
	if err != nil || est.Allowed || est.KeyReady || !strings.Contains(strings.Join(est.Problems, " "), "isn't set up on this computer yet") {
		t.Fatalf("estimate without a key: %+v %v", est, err)
	}
	in.MaxUSD = usd(1)
	if _, err := call[generate.GenRunOut](t, e.a, "generate.shot", in, false); codeOf(err) != apperr.NotConfigured {
		t.Fatalf("a shot without a key: %v", err)
	}
	if e.fake.Calls("create") != 0 {
		t.Fatal("sent without a key")
	}
}
