package e2e

import (
	"context"
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/secretpath"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

const fakeSecret = "sk_test_DO_NOT_LEAK_7f3a9c" // a stand-in credential that must never reach a project folder

// fakeAccessKey looks like a cloud access key ID; built at run time so the
// secret scanner never sees one in the source.
var fakeAccessKey = "AKIA" + strings.Repeat("FAKE", 4)

type cutEnv struct {
	t       *testing.T
	a       *app.App
	cfg     *config.Config
	project string
	talk    string // interview with a word-timed transcript
	broll   string
	rev1    string
	ctx     context.Context
}

func newCutEnv(t *testing.T) *cutEnv {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	wsID := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: wsID, Name: "Cut sheets"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "editing-laptop"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")},
		S3: config.S3{AccessKeyID: fakeAccessKey, SecretAccessKey: fakeSecret}}
	ctx := context.Background()
	a, err := app.OpenWith(ctx, cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	now := time.Now().UTC()
	if err := db.Insert(ctx, store.CWorkspaces, models.Workspace{ID: wsID, WorkspaceID: wsID, Name: "Cut sheets", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	e := &cutEnv{t: t, a: a, cfg: cfg, ctx: ctx, project: ids.New(ids.Project)}
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: e.project, WorkspaceID: wsID, Name: "Builder story", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	asset := func(name string, dur float64, sha string, tx bool) string {
		id := ids.New(ids.Asset)
		as := models.Asset{ID: id, WorkspaceID: wsID, ProjectIDs: []string{e.project}, Kind: "video", Name: name, SHA256: sha, State: models.AssetAvailable,
			Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1080, Height: 1920, FPS: 30}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if tx {
			as.Transcript = &models.TranscriptStatus{State: "available", Provider: "fixture", Model: "fixture", Precision: "word", Segments: 2, Words: 16, Speakers: 2, UpdatedAt: now}
		}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return id
	}
	e.talk = asset("interview-a.mov", 30, strings.Repeat("9f", 32), true)
	e.broll = asset("broll-c.mp4", 10, strings.Repeat("0d", 32), false)
	w := func(text string, from, to float64) models.Word { return models.Word{Text: text, From: from, To: to} }
	segs := []models.TranscriptSegment{
		{ID: ids.New(ids.Segment), WorkspaceID: wsID, AssetID: e.talk, Index: 0, From: 1, To: 4, Text: "We built it twice, because I'm going to fail.", Speaker: "speaker_00", SpeakerName: "Ada",
			Words: []models.Word{w("We", 1, 1.2), w("built", 1.25, 1.6), w("it", 1.65, 1.75), w("twice,", 1.8, 2.3), w("because", 2.8, 3.1), w("I'm", 3.12, 3.3), w("going", 3.32, 3.6), w("to", 3.62, 3.7), w("fail.", 3.72, 4.0)}},
		{ID: ids.New(ids.Segment), WorkspaceID: wsID, AssetID: e.talk, Index: 1, From: 6, To: 8, Text: "And we did it very quickly.", Speaker: "speaker_01",
			Words: []models.Word{w("And", 6, 6.2), w("we", 6.22, 6.4), w("did", 6.42, 6.6), w("it", 6.62, 6.7), w("very", 6.75, 7.0), w("quickly.", 7.02, 7.6)}},
	}
	for _, s := range segs {
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	// Two voices: one a person named, one nobody has named yet.
	for _, v := range []models.AssetSpeaker{
		{Label: "speaker_00", Name: "Ada", Source: models.SpeakerByPerson, Seconds: 3, First: 1, Turns: 1, Words: 9},
		{Label: "speaker_01", Seconds: 2, First: 6, Turns: 1, Words: 6},
	} {
		v.ID, v.WorkspaceID, v.AssetID, v.Run, v.Samples, v.CreatedAt, v.UpdatedAt = ids.New(ids.Speaker), wsID, e.talk, "fixture", []models.TimeSpan{}, now, now
		if err := a.DB.Insert(ctx, store.CSpeakers, v); err != nil {
			t.Fatal(err)
		}
	}
	plan := editspec.Plan{ProjectID: e.project, Instruction: "first cut", Spec: &editspec.Spec{SchemaVersion: 1, Title: "Builder story", Output: editspec.Output{Preset: editspec.PresetVertical},
		Clips: []editspec.Clip{
			{ID: "hook", AssetID: e.talk, In: 0.9, Out: 2.5},
			{ID: "broll", AssetID: e.broll, In: 0, Out: 2, Muted: true},
			{ID: "payoff", AssetID: e.talk, In: 5.8, Out: 7.8},
			{ID: "end", AssetID: e.broll, In: 4, Out: 6},
		}}}
	e.rev1 = e.applyPlan(plan).Revision.ID
	return e
}

func (e *cutEnv) applyPlan(plan editspec.Plan) *ops.EditApplyOut {
	e.t.Helper()
	raw, _ := json.Marshal(plan)
	chk := call[ops.EditCheckOut](e.t, e.a, "edit.check", ops.EditCheckIn{Plan: raw})
	if !chk.Valid {
		e.t.Fatalf("plan invalid: %+v", chk.Errors)
	}
	return call[ops.EditApplyOut](e.t, e.a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: chk.ConfirmToken})
}

func (e *cutEnv) head() *models.Revision {
	e.t.Helper()
	var p models.Project
	if err := e.a.DB.GetByID(e.ctx, store.CProjects, e.a.WorkspaceID(), e.project, &p); err != nil {
		e.t.Fatal(err)
	}
	var r models.Revision
	if err := e.a.DB.GetByID(e.ctx, store.CRevisions, e.a.WorkspaceID(), p.Branches["main"].Head, &r); err != nil {
		e.t.Fatal(err)
	}
	return &r
}

func (e *cutEnv) clip(r *models.Revision, id string) editspec.Clip {
	for _, c := range r.Spec.Clips {
		if c.ID == id {
			return c
		}
	}
	e.t.Fatalf("no clip %s in r%d", id, r.Number)
	return editspec.Clip{}
}

func clipOrder(r *models.Revision) string {
	var ids []string
	for _, c := range r.Spec.Clips {
		ids = append(ids, c.ID)
	}
	return strings.Join(ids, ",")
}

func readFile(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// replaceLine swaps the line that starts with prefix (after indentation).
func replaceLine(t *testing.T, text, prefix, with string) string {
	t.Helper()
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimSpace(l), prefix) {
			lines[i] = with
			return strings.Join(lines, "\n")
		}
	}
	t.Fatalf("no line starting with %q in:\n%s", prefix, text)
	return ""
}

func push(t *testing.T, a *app.App, dir, msg string) (*ops.FolderOut, error) {
	t.Helper()
	dry, err := ops.CallTyped[ops.FolderOut](context.Background(), a, "project.push", ops.ProjectPushIn{Dir: dir, Message: msg, DryRun: true}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	if !dry.Valid || dry.Check == nil || dry.Check.ConfirmToken == "" {
		b, _ := json.MarshalIndent(dry, "", "  ")
		t.Fatalf("push dry run not ready: %s", b)
	}
	return ops.CallTyped[ops.FolderOut](context.Background(), a, "project.push", ops.ProjectPushIn{Dir: dir, Message: msg, ConfirmToken: dry.Check.ConfirmToken}, ops.Caller{})
}

func TestCutSheetFolderWorkflow(t *testing.T) {
	e := newCutEnv(t)
	dir := filepath.Join(t.TempDir(), "builder-story")

	// Checkout writes the sheet, the footage reference and the lock.
	co := call[ops.FolderOut](t, e.a, "project.checkout", ops.ProjectCheckoutIn{ProjectID: "Builder story", Dir: dir})
	if co.Base.Number != 1 || co.ProjectID != e.project {
		t.Fatalf("checkout: %+v", co)
	}
	sheet := readFile(t, filepath.Join(dir, "cut.md"))
	footage := readFile(t, filepath.Join(dir, "footage.md"))
	lock := readFile(t, filepath.Join(dir, ".metarush", "lock.json"))
	for _, want := range []string{"base: " + e.rev1, "interview-a: " + e.talk + " sha256:9f9f9f9f9f9f9f9f", "1. hook: interview-a 0:00.9 → 0:02.5", "> Ada: “We built it twice,”", "# interview-a.mov · 0:30 · Ada, Speaker 2"} {
		if !strings.Contains(sheet, want) {
			t.Fatalf("cut.md lacks %q:\n%s", want, sheet)
		}
	}
	if !strings.Contains(footage, "0:06 → 0:08 Speaker 2: “And we did it very quickly.”") || !strings.Contains(footage, "0:01 → 0:04 Ada: “We built it twice,") ||
		!strings.Contains(footage, "2 voices in “interview-a”: Ada, plus Speaker 2 not named yet.") {
		t.Fatalf("footage.md lacks transcript excerpts:\n%s", footage)
	}
	if !strings.Contains(lock, `"revisionId": "`+e.rev1+`"`) || !strings.Contains(lock, strings.Repeat("9f", 32)) {
		t.Fatalf("lock.json: %s", lock)
	}
	scanForSecrets(t, dir, e)

	// Every guard that keeps shareable data out of project folders recognizes it:
	// log exports and bug reports refuse to write into it, also into a subfolder.
	if got := secretpath.ProjectFolder(filepath.Join(dir, "notes", "x.md")); got != dir {
		t.Fatalf("project folder of a file inside it: %q, want %q", got, dir)
	}
	for _, p := range []string{filepath.Join(dir, "log.jsonl"), filepath.Join(dir, "notes", "bug-report.zip")} {
		if err := logbook.CheckOutput(p); err == nil || !strings.Contains(err.Error(), "project folder") {
			t.Fatalf("log output inside the project folder (%s): %v", p, err)
		}
	}

	// A second checkout into the same folder refuses to overwrite.
	if _, err := ops.CallTyped[ops.FolderOut](e.ctx, e.a, "project.checkout", ops.ProjectCheckoutIn{ProjectID: e.project, Dir: dir}, ops.Caller{}); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("second checkout: %v", err)
	}

	// Edit: trim hook, swap broll and payoff, a note on broll, end payoff after words.
	edited := replaceLine(t, sheet, "1. hook:", "1. hook: interview-a 0:00.9 → 0:02.4")
	edited = replaceLine(t, edited, "2. broll:", "2. payoff: interview-a 0:05.8 → \"did it very quickly\"")
	edited = replaceLine(t, edited, "3. payoff:", "3. broll: broll-c 0:00 → 0:02 · muted\n\n   Keep the workbench in focus.")
	if err := os.WriteFile(filepath.Join(dir, "cut.md"), []byte(edited), 0o644); err != nil {
		t.Fatal(err)
	}
	st := call[ops.FolderOut](t, e.a, "project.status", ops.ProjectDirIn{Dir: dir})
	if st.Files[0].State != "modified" || !st.Valid || len(st.Changes) < 3 {
		b, _ := json.MarshalIndent(st, "", "  ")
		t.Fatalf("status: %s", b)
	}

	// Pushing without a token is refused; with the dry run's token it saves r2.
	if _, err := ops.CallTyped[ops.FolderOut](e.ctx, e.a, "project.push", ops.ProjectPushIn{Dir: dir}, ops.Caller{}); !apperr.Is(err, apperr.ConfirmRequired) {
		t.Fatalf("push without token: %v", err)
	}
	res, err := push(t, e.a, dir, "tighten the hook, payoff before the b-roll")
	if err != nil {
		t.Fatal(err)
	}
	r2 := e.head()
	if res.Revision == nil || res.Revision.ID != r2.ID || r2.Number != 2 || r2.Instruction != "tighten the hook, payoff before the b-roll" || r2.Origin != "cut-sheet:folder" {
		t.Fatalf("push result %+v, head r%d %q %q", res.Revision, r2.Number, r2.Instruction, r2.Origin)
	}
	if clipOrder(r2) != "hook,payoff,broll,end" {
		t.Fatalf("order %s", clipOrder(r2))
	}
	if e.clip(r2, "hook").Out != 2.4 || e.clip(r2, "broll").Notes != "Keep the workbench in focus." {
		t.Fatalf("trim/note lost: %+v %+v", e.clip(r2, "hook"), e.clip(r2, "broll"))
	}
	// "quickly." ends at 7.6 and nothing follows: the out point is 0.15 s after it.
	if out := e.clip(r2, "payoff").Out; out != 7.75 {
		t.Fatalf("word anchor resolved to %v, want 7.75", out)
	}
	if len(r2.Ops) == 0 {
		t.Fatal("a cut sheet change against a base must be stored as ops (so it can rebase)")
	}
	// The folder now holds r2 with exact times.
	sheet = readFile(t, filepath.Join(dir, "cut.md"))
	if !strings.Contains(sheet, "base: "+r2.ID) || !strings.Contains(sheet, "2. payoff: interview-a 0:05.8 → 0:07.75") || strings.Contains(sheet, "did it very quickly\"") {
		t.Fatalf("cut.md after push:\n%s", sheet)
	}
	if st := call[ops.FolderOut](t, e.a, "project.status", ops.ProjectDirIn{Dir: dir}); st.HeadMoved || st.Files[0].State != "unchanged" {
		t.Fatalf("status after push: %+v", st)
	}

	// Someone else trims "end" (a clip we don't touch); we change "hook". Disjoint: rebases.
	e.applyPlan(editspec.Plan{BaseRevisionID: r2.ID, Instruction: "shorter ending", Ops: []editspec.Op{{Op: "updateClip", ID: "end", Set: json.RawMessage(`{"out":5.5}`)}}})
	edited = replaceLine(t, sheet, "1. hook:", "1. hook: interview-a 0:01 → 0:02.4")
	_ = os.WriteFile(filepath.Join(dir, "cut.md"), []byte(edited), 0o644)
	if st := call[ops.FolderOut](t, e.a, "project.status", ops.ProjectDirIn{Dir: dir}); !st.HeadMoved || st.HeadNumber != 3 {
		t.Fatalf("status should see the newer head: %+v", st)
	}
	res, err = push(t, e.a, dir, "start on 'We'")
	if err != nil {
		t.Fatalf("disjoint edits must rebase: %v", err)
	}
	r4 := e.head()
	if r4.Number != 4 || r4.RebasedFrom != r2.ID || e.clip(r4, "hook").In != 1 || e.clip(r4, "end").Out != 5.5 {
		t.Fatalf("rebased revision r%d lost an edit: hook %+v end %+v", r4.Number, e.clip(r4, "hook"), e.clip(r4, "end"))
	}

	// Someone changes hook again; we change hook too: a conflict, nothing overwritten.
	e.applyPlan(editspec.Plan{BaseRevisionID: r4.ID, Instruction: "their hook", Ops: []editspec.Op{{Op: "updateClip", ID: "hook", Set: json.RawMessage(`{"out":2.3}`)}}})
	sheet = readFile(t, filepath.Join(dir, "cut.md"))
	edited = replaceLine(t, sheet, "1. hook:", "1. hook: interview-a 0:01 → 0:02.2")
	_ = os.WriteFile(filepath.Join(dir, "cut.md"), []byte(edited), 0o644)
	_, err = ops.CallTyped[ops.FolderOut](e.ctx, e.a, "project.push", ops.ProjectPushIn{Dir: dir, DryRun: true}, ops.Caller{})
	if !apperr.Is(err, apperr.RevisionConflict) || !strings.Contains(apperr.From(err).Message, "hook") {
		t.Fatalf("overlapping edits must conflict: %v", err)
	}
	if e.head().Number != 5 || readFile(t, filepath.Join(dir, "cut.md")) != edited {
		t.Fatal("a conflict must change nothing")
	}

	// Pull refuses to overwrite the local change and names the file; --force discards it.
	_, err = ops.CallTyped[ops.FolderOut](e.ctx, e.a, "project.pull", ops.ProjectDirIn{Dir: dir}, ops.Caller{})
	if !apperr.Is(err, apperr.Conflict) || !strings.Contains(apperr.From(err).Message, "cut.md") {
		t.Fatalf("pull over local changes: %v", err)
	}
	pulled := call[ops.FolderOut](t, e.a, "project.pull", ops.ProjectDirIn{Dir: dir, Force: true})
	if pulled.Base.Number != 5 || !strings.Contains(readFile(t, filepath.Join(dir, "cut.md")), "1. hook: interview-a 0:01 → 0:02.3") {
		t.Fatalf("forced pull: %+v", pulled)
	}
	scanForSecrets(t, dir, e)
}

func TestCutSheetOps(t *testing.T) {
	e := newCutEnv(t)
	show := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: e.project, Revision: "r1", Format: "json"})
	if show.Revision == nil || show.Revision.Number != 1 || show.ReadOnly || show.Spec == nil {
		t.Fatalf("cut.show r1: %+v", show)
	}
	// An unchanged sheet checks clean with no changes: the round trip is exact.
	chk := call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: show.Sheet})
	if !chk.Valid || !chk.NoChanges || chk.ConfirmToken != "" {
		t.Fatalf("unchanged sheet: %+v", chk)
	}
	// A broken line is reported at its line with a stable code.
	broken := strings.Replace(show.Sheet, "0:00.9 → 0:02.5", "0:00.9 → soon", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: broken})
	if chk.Valid || len(chk.Problems) == 0 || chk.Problems[0].Code != "bad_time" || chk.Problems[0].Line == 0 {
		t.Fatalf("broken sheet: %+v", chk.Problems)
	}
	// Validation findings land on the clip's line in plain words.
	tooLong := strings.Replace(show.Sheet, "0:00.9 → 0:02.5", "0:00.9 → 0:40", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: tooLong})
	var found bool
	for _, p := range chk.Problems {
		if p.Code == "out_of_range" && strings.Contains(p.Message, "only 0:30 long") && p.Line > 0 {
			found = true
		}
	}
	if chk.Valid || !found {
		t.Fatalf("out of range: %+v", chk.Problems)
	}
	// Apply needs the token, and the token is bound to the exact sheet.
	trimmed := strings.Replace(show.Sheet, "0:00.9 → 0:02.5", "0:00.9 → 0:02.4", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: trimmed})
	if !chk.Valid || chk.ConfirmToken == "" || len(chk.Changes) != 2 || !strings.Contains(chk.Changes[1], "hook now ends at 0:02.4 instead of 0:02.5") {
		t.Fatalf("check: %+v", chk)
	}
	if _, err := ops.CallTyped[ops.CutApplyOut](e.ctx, e.a, "cut.apply", ops.CutApplyIn{Sheet: trimmed}, ops.Caller{}); !apperr.Is(err, apperr.ConfirmRequired) {
		t.Fatalf("apply without token: %v", err)
	}
	other := strings.Replace(show.Sheet, "0:00.9 → 0:02.5", "0:00.9 → 0:02.3", 1)
	if _, err := ops.CallTyped[ops.CutApplyOut](e.ctx, e.a, "cut.apply", ops.CutApplyIn{Sheet: other, ConfirmToken: chk.ConfirmToken}, ops.Caller{}); !apperr.Is(err, apperr.ConfirmRequired) {
		t.Fatalf("token for another sheet: %v", err)
	}
	applied := call[ops.CutApplyOut](t, e.a, "cut.apply", ops.CutApplyIn{Sheet: trimmed, ConfirmToken: chk.ConfirmToken})
	if applied.Revision.Number != 2 || !strings.Contains(applied.Sheet, "base: "+applied.Revision.ID) {
		t.Fatalf("apply: %+v", applied.Revision)
	}
	if !strings.Contains(applied.SheetURL, "tab=script") || !strings.Contains(applied.SheetURL, "rev="+applied.Revision.ID) {
		t.Fatalf("apply links to the new revision's cut sheet, got %q", applied.SheetURL)
	}
	// Locks hold: locking via the sheet works, and changing a locked clip is refused.
	locked := strings.Replace(applied.Sheet, "1. hook: interview-a 0:00.9 → 0:02.4", "1. hook: interview-a 0:00.9 → 0:02.4 · locked", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: locked})
	r3 := call[ops.CutApplyOut](t, e.a, "cut.apply", ops.CutApplyIn{Sheet: locked, ConfirmToken: chk.ConfirmToken})
	change := strings.Replace(r3.Sheet, "0:00.9 → 0:02.4 · locked", "0:00.9 → 0:02 · locked", 1)
	if _, err := ops.CallTyped[ops.CutCheckOut](e.ctx, e.a, "cut.check", ops.CutCheckIn{Sheet: change}, ops.Caller{}); !apperr.Is(err, apperr.Locked) {
		t.Fatalf("changing a locked clip: %v", err)
	}
	// An older revision opens read-only; rN works; compare reads as words + lines.
	old := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: "builder story", Revision: "r1"})
	if !old.ReadOnly {
		t.Fatal("r1 is not the latest; it must open read-only")
	}
	cmp := call[ops.CutCompareOut](t, e.a, "cut.compare", ops.CutCompareIn{ProjectID: e.project, Left: "r1", Right: "r3"})
	if !strings.Contains(cmp.Markdown, "# Version 1 → version 3") || !strings.Contains(cmp.Markdown, "hook is now locked.") || !strings.Contains(cmp.Markdown, "+1. hook: interview-a 0:00.9 → 0:02.4 · locked") {
		t.Fatalf("compare markdown:\n%s", cmp.Markdown)
	}
	if !strings.Contains(cmp.SheetURL, "mode=compare") || !strings.Contains(cmp.SheetURL, "left="+cmp.Left.ID) {
		t.Fatalf("compare link: %q", cmp.SheetURL)
	}
	// Footage names follow the titles people give clips; sheets written
	// before a rename keep working (their footage list says what each name is).
	call[ops.AssetLinkOut](t, e.a, "asset.update", ops.AssetUpdateIn{ID: e.talk, Title: "Ada's interview"})
	renamed := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: e.project})
	for _, want := range []string{"adas-interview: " + e.talk, "“Ada's interview” · interview-a.mov", "1. hook: adas-interview 0:00.9 → 0:02.4"} {
		if !strings.Contains(renamed.Sheet, want) {
			t.Fatalf("after renaming the clip, the sheet lacks %q:\n%s", want, renamed.Sheet)
		}
	}
	if again := call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: r3.Sheet}); !again.Valid || !again.NoChanges {
		t.Fatalf("a sheet written before the rename: %+v", again.Problems)
	}
	// cut.diff: a sheet written against r2 while r3 exists shows both comparisons.
	diff := call[ops.CutDiffOut](t, e.a, "cut.diff", ops.CutDiffIn{Sheet: strings.Replace(applied.Sheet, "0:02.4", "0:02.45", 1)})
	if !diff.HeadMoved || diff.Head == nil || len(diff.TheirEdits) == 0 || !strings.Contains(strings.Join(diff.Base.Changes, " "), "hook now ends at 0:02.45") {
		t.Fatalf("diff: %+v", diff)
	}
	// A project with no cut shows a starter sheet listing its footage.
	empty := ids.New(ids.Project)
	_ = e.a.DB.Insert(e.ctx, store.CProjects, models.Project{ID: empty, WorkspaceID: e.a.WorkspaceID(), Name: "Empty", Branches: map[string]models.BranchHead{}})
	_, _ = e.a.DB.C(store.CAssets).UpdateByID(e.ctx, e.broll, map[string]any{"$push": map[string]any{"projectIds": empty}})
	starter := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: empty})
	if !starter.Starter || !strings.Contains(starter.Sheet, "project: "+empty) || !strings.Contains(starter.Sheet, "broll-c: "+e.broll) {
		t.Fatalf("starter sheet:\n%s", starter.Sheet)
	}
	first := strings.Replace(starter.Sheet, "## Clips\n", "## Clips\n\n1. open: broll-c 0:01 → 0:03\n", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: first})
	if !chk.Valid {
		t.Fatalf("first cut from a starter sheet: %+v", chk.Problems)
	}
	if r := call[ops.CutApplyOut](t, e.a, "cut.apply", ops.CutApplyIn{Sheet: first, ConfirmToken: chk.ConfirmToken}); r.Revision.Number != 1 {
		t.Fatalf("first revision: %+v", r.Revision)
	}
}

// scanForSecrets fails if a project folder holds credentials, connection
// strings, confirm tokens or machine-local paths.
func scanForSecrets(t *testing.T, dir string, e *cutEnv) {
	t.Helper()
	key, _ := os.ReadFile(filepath.Join(e.cfg.StateDir(), "confirm.key"))
	needles := []string{fakeSecret, fakeAccessKey, e.cfg.Database.URI, e.cfg.Home(), e.cfg.Media.Filesystem.Root}
	if len(key) > 0 {
		needles = append(needles, string(key))
	}
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`mongodb(\+srv)?://`), regexp.MustCompile(`ct_\d+_[0-9a-f]{8,}`), regexp.MustCompile(`(?i)secret|password|BEGIN [A-Z ]*PRIVATE KEY`),
		regexp.MustCompile(`AKIA[0-9A-Z]{16}`), regexp.MustCompile(`/Users/|/home/`),
	}
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, _ := os.ReadFile(p)
		s := string(b)
		for _, n := range needles {
			if n != "" && strings.Contains(s, n) {
				t.Errorf("%s contains a configuration value it must not (%q…)", p, n[:min(8, len(n))])
			}
		}
		for _, re := range patterns {
			if m := re.FindString(s); m != "" {
				t.Errorf("%s contains %q", p, m)
			}
		}
		if rel, _ := filepath.Rel(dir, p); strings.Contains(rel, "log") || strings.Contains(rel, "cache") {
			t.Errorf("unexpected file in a project folder: %s", rel)
		}
		return nil
	})
}

// Narration and the mix through a cut sheet: lines with start points in words,
// a mix in the settings, where each line lands shown under it, and a reworded
// line saved as a line-level change (so it rebases like any edit).
func TestCutSheetNarration(t *testing.T) {
	e := newCutEnv(t)
	show := call[ops.CutShowOut](t, e.a, "cut.show", ops.CutShowIn{ProjectID: e.project})
	sheet := strings.Replace(show.Sheet, "footage:", "mix:\n  camera sound: off\n  music: -6 dB\nfootage:", 1) +
		"\n## Voiceover\n\n- intro: \"We built it twice.\" · 0.2 s after hook starts\n  Delivery: warm\n- \"Then we did it again.\" · after the previous line\n"
	chk := call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: sheet})
	if !chk.Valid {
		t.Fatalf("narration sheet: %+v", chk.Problems)
	}
	changes := strings.Join(chk.Changes, " | ")
	for _, want := range []string{"Narration added: 2 lines.", "The camera sound is now off.", "Music: now 6 dB quieter (was normal)."} {
		if !strings.Contains(changes, want) {
			t.Fatalf("changes lack %q: %s", want, changes)
		}
	}
	applied := call[ops.CutApplyOut](t, e.a, "cut.apply", ops.CutApplyIn{Sheet: sheet, ConfirmToken: chk.ConfirmToken})
	h := e.head()
	if v := h.Spec.Voiceover; v == nil || len(v.Beats) != 2 || v.Beats[1].ID != "line-2" || v.Beats[0].Anchor.ClipID != "hook" || v.Beats[0].Anchor.Offset != 0.2 || v.Beats[0].Delivery != "warm" {
		t.Fatalf("saved narration: %+v", h.Spec.Voiceover)
	}
	if m := h.Spec.Mix; m == nil || !m.MuteSource || m.Music == nil || *m.Music.GainDB != -6 {
		t.Fatalf("saved mix: %+v", h.Spec.Mix)
	}
	if !strings.Contains(applied.Sheet, "- intro: \"We built it twice.\" · 0.2 s after hook starts\n  > starts at 0:00.2 · ") {
		t.Fatalf("the saved sheet should say where the line lands:\n%s", applied.Sheet)
	}
	// Reword one line: one updateBeat, not a whole new script.
	edited := strings.Replace(applied.Sheet, "\"Then we did it again.\"", "\"Then we did it again, faster.\"", 1)
	chk = call[ops.CutCheckOut](t, e.a, "cut.check", ops.CutCheckIn{Sheet: edited})
	if !chk.Valid || !strings.Contains(string(chk.Plan), `"op":"updateBeat"`) || strings.Contains(string(chk.Plan), "setScript") {
		t.Fatalf("a reworded line should be one updateBeat: %s %+v", chk.Plan, chk.Problems)
	}
	if !strings.Contains(strings.Join(chk.Changes, " | "), "Narration line line-2 now says “Then we did it again, faster.”.") {
		t.Fatalf("changes: %v", chk.Changes)
	}
}
