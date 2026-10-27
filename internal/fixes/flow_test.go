package fixes_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

type flow struct {
	t       *testing.T
	a       *app.App
	project string
	dir     string
}

func newFlow(t *testing.T) *flow {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Garden film", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	return &flow{t: t, a: a, project: pid, dir: t.TempDir()}
}

func (f *flow) ffmpeg(name string, args ...string) string {
	f.t.Helper()
	out := filepath.Join(f.dir, name)
	if b, err := exec.Command("ffmpeg", append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...), out)...).CombinedOutput(); err != nil {
		f.t.Fatalf("ffmpeg: %v %s", err, b)
	}
	return out
}

func (f *flow) ingest(path string) *models.Asset {
	f.t.Helper()
	r := ingest.File(context.Background(), f.a, path, ingest.Options{ProjectID: f.project, Via: "cli"})
	if r.AssetID == "" {
		f.t.Fatalf("ingest: %+v", r)
	}
	return f.asset(r.AssetID)
}

func (f *flow) asset(id string) *models.Asset {
	f.t.Helper()
	var as models.Asset
	if err := f.a.DB.GetByID(context.Background(), store.CAssets, f.a.WorkspaceID(), id, &as); err != nil {
		f.t.Fatal(err)
	}
	return &as
}

func (f *flow) view(id string) *fixes.View {
	f.t.Helper()
	v, err := ops.CallTyped[fixes.View](context.Background(), f.a, "asset.fixes", fixes.ShowIn{ID: id}, ops.Caller{})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

// measure runs the clip's inspect job here, as a worker would.
func (f *flow) measure(as *models.Asset) {
	f.t.Helper()
	ctx := context.Background()
	j, err := fixes.QueueInspect(ctx, f.a, as, false)
	if err != nil {
		f.t.Fatal(err)
	}
	if _, err := fixes.Inspect(ctx, f.a, j, func(float64, string) {}); err != nil {
		f.t.Fatal(err)
	}
}

// prepare runs a prepare job here, as a worker would.
func (f *flow) prepare(jobID string) map[string]any {
	f.t.Helper()
	ctx := context.Background()
	j, err := f.a.Queue.Get(ctx, jobID)
	if err != nil {
		f.t.Fatal(err)
	}
	j.AttemptCount = 1
	res, err := fixes.Prepare(ctx, f.a, j, func(float64, string) {})
	if err != nil {
		f.t.Fatal(err)
	}
	return res
}

// basicSteadying makes this app steady with ffmpeg's basic method, whatever
// ffmpeg this machine has: its ffmpeg is used through a stand-in that hides
// vid.stab's filters (Linux distributions build ffmpeg with vid.stab,
// Homebrew does not), and no better build is set up (downloads are off in
// tests; the stand-in zip does not exist). TestShakyClipGetsAVidStabCopy
// covers the better method where this machine has it.
func (f *flow) basicSteadying() {
	f.t.Helper()
	f.t.Setenv("METARUSH_STABILIZER_ZIP", filepath.Join(f.dir, "none.zip"))
	ff := f.a.Tools.Path(tools.FFmpeg)
	if ff == "" {
		f.t.Skip("needs ffmpeg")
	}
	q := "'" + strings.ReplaceAll(ff, "'", `'\''`) + "'"
	script := filepath.Join(f.dir, "ffmpeg-without-vidstab")
	body := "#!/bin/sh\n# This machine's ffmpeg with vid.stab's filters hidden.\n" +
		"for a in \"$@\"; do if [ \"$a\" = -filters ]; then " + q + " \"$@\" | grep -v vidstab; exit 0; fi; done\n" +
		"exec " + q + " \"$@\"\n"
	if err := os.WriteFile(script, []byte(body), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if !f.a.Tools.Use(tools.FFmpeg, script) {
		f.t.Fatal("the stand-in ffmpeg was refused")
	}
	if st := (&fixes.Stabilizer{Home: f.a.Cfg.Home(), Tools: f.a.Tools}).Status(context.Background()); st.Ready {
		f.t.Fatalf("vid.stab is still found: %+v", st)
	}
}

func (f *flow) tick(in fixes.SetIn) *fixes.View {
	f.t.Helper()
	v, err := ops.CallTyped[fixes.View](context.Background(), f.a, "asset.fixes.set", in, ops.Caller{Human: true})
	if err != nil {
		f.t.Fatal(err)
	}
	return v
}

func (f *flow) head() *models.Revision {
	f.t.Helper()
	ctx := context.Background()
	var p models.Project
	if err := f.a.DB.GetByID(ctx, store.CProjects, f.a.WorkspaceID(), f.project, &p); err != nil {
		f.t.Fatal(err)
	}
	var r models.Revision
	if err := f.a.DB.GetByID(ctx, store.CRevisions, f.a.WorkspaceID(), p.Branches["main"].Head, &r); err != nil {
		f.t.Fatal(err)
	}
	return &r
}

// A shaky camera clip (D152): MetaRush marks Steady the camera; ticking it
// makes a fixed copy (the original kept), measured before and after, which
// the clip uses from then on; the cut saved before keeps the original, and
// gets the copy only as a new version; unticking goes back to the original,
// and the version that uses the copy stays as it was saved (D73).
func TestShakyClipGetsASteadyCopy(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	f.basicSteadying() // no better steadying here: the basic one, said plainly
	as := f.ingest(f.shakyWalk())
	original, originalVariants := as.StorageKey, as.Variants

	// A cut that plays it, saved before any fix.
	plan := fmt.Sprintf(`{"projectId":%q,"instruction":"first cut","spec":{"schemaVersion":1,"title":"Walk","output":{"preset":"landscape-1080p","fps":30},
	  "clips":[{"id":"walk","assetId":%q,"in":0.5,"out":5.5}]}}`, f.project, as.ID)
	chk, err := ops.CallTyped[ops.EditCheckOut](ctx, f.a, "edit.check", ops.EditCheckIn{Plan: json.RawMessage(plan)}, ops.Caller{Human: true})
	if err != nil || !chk.Valid {
		t.Fatalf("check: %+v %v", chk, err)
	}
	v1, err := ops.CallTyped[ops.EditApplyOut](ctx, f.a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan), ConfirmToken: chk.ConfirmToken}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}

	f.measure(as)
	v := f.view(as.ID)
	if v.Kind != models.SourceCamera || len(v.Fixes) != 3 {
		t.Fatalf("camera fixes: %+v", v)
	}
	steady := v.Fixes[slices.IndexFunc(v.Fixes, func(r fixes.Row) bool { return r.ID == models.FixSteady })]
	if !steady.Fits || !strings.Contains(steady.Why, "shakes") {
		t.Fatalf("steady should fit: %+v", steady)
	}

	// Tick it: a prepare job; the clip plays its original until the copy is made.
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixSteady}})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 || v.Using != "original" {
		t.Fatalf("after ticking: %+v", v)
	}
	res := f.prepare(v.JobIDs[0])
	sha, _ := res["sha256"].(string)
	as = f.asset(as.ID)
	if !as.Fixed() || as.Fixes.Copy.SHA256 != sha || as.StorageKey != original || as.MediaSHA() != sha {
		t.Fatalf("the clip uses its fixed copy, the original is kept: %+v", as.Fixes)
	}
	if _, ok := as.Variants[models.VariantProxy]; !ok {
		t.Fatal("the fixed copy has its own preview copy")
	}
	if len(as.Fixes.OriginalVariants) != len(originalVariants) {
		t.Fatalf("the original's own files are set aside, not lost: %v vs %v", as.Fixes.OriginalVariants, originalVariants)
	}
	v = f.view(as.ID)
	if v.Copy == nil || len(v.Copy.Measures) == 0 || v.Copy.Measures[0].After >= v.Copy.Measures[0].Before ||
		!slices.ContainsFunc(v.Copy.Notes, func(n string) bool { return strings.Contains(n, "basic method") }) {
		t.Fatalf("measured before and after, and said it used the basic steadying: %+v", v.Copy)
	}
	if e := f.steadyEngine(as); e != fixes.EngineDeshake {
		t.Fatalf("steadied with %q, want the basic method", e)
	}
	// What the fix affects is looked at again (the picture: scenes and movement).
	if n, _ := f.a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "assetId": as.ID, "type": jobs.TypeEvidence, "payload.reason": "fixed"}); n != 1 {
		t.Fatalf("evidence again: %d", n)
	}

	// The saved version still plays the original; the offer moves the cut to the copy as a new version.
	if f.head().Spec.FixedSHA(as.ID) != "" || len(v.Cuts) != 1 || v.Cuts[0].Plays != "original" || !strings.Contains(v.Cuts[0].Offer, "steadier picture") {
		t.Fatalf("cuts: %+v", v.Cuts)
	}
	dry, err := ops.CallTyped[ops.CutEditOut](ctx, f.a, "cut.useFixed", ops.CutUseFixedIn{CutEditCommon: ops.CutEditCommon{ProjectID: f.project, DryRun: true}, Footage: as.ID}, ops.Caller{Human: true})
	if err != nil || dry.Check == nil {
		t.Fatalf("dry run: %+v %v", dry, err)
	}
	applied, err := ops.CallTyped[ops.CutEditOut](ctx, f.a, "cut.useFixed", ops.CutUseFixedIn{CutEditCommon: ops.CutEditCommon{ProjectID: f.project, ConfirmToken: dry.Check.ConfirmToken}, Footage: as.ID}, ops.Caller{Human: true})
	if err != nil || applied.Applied == nil {
		t.Fatalf("use fixed: %+v %v", applied, err)
	}
	v2 := f.head()
	if v2.Number != 2 || v2.Spec.FixedSHA(as.ID) != sha {
		t.Fatalf("version 2 plays the copy: %d %+v", v2.Number, v2.Spec.Fixed)
	}
	var first models.Revision
	_ = f.a.DB.GetByID(ctx, store.CRevisions, f.a.WorkspaceID(), v1.Revision.ID, &first)
	if first.Spec.FixedSHA(as.ID) != "" || first.SpecHash != v1.Revision.SpecHash {
		t.Fatal("version 1 must never change")
	}

	// Untick: the clip goes back to its original; version 2 keeps playing the copy it pinned.
	v = f.tick(fixes.SetIn{ID: as.ID, Untick: []string{models.FixSteady}})
	as = f.asset(as.ID)
	if as.Fixed() || as.MediaSHA() != "" || v.Using != "original" {
		t.Fatalf("back to the original: %+v", as.Fixes)
	}
	if p := f.head(); p.ID != v2.ID || p.Spec.FixedSHA(as.ID) != sha {
		t.Fatal("unticking must not change a saved version")
	}
	copies, err := ops.LoadPinnedCopies(ctx, f.a, &v2.Spec)
	if err != nil || copies[as.ID] == nil || copies[as.ID].SHA256 != sha {
		t.Fatalf("version 2 still finds its copy to render: %v %v", copies, err)
	}
	// Ticking it again reuses the copy: nothing is made twice.
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixSteady}})
	if as = f.asset(as.ID); !as.Fixed() || as.Fixes.Copy.SHA256 != sha || len(v.JobIDs) != 0 {
		t.Fatalf("the same fixes reuse their copy: %+v %+v", as.Fixes, v.JobIDs)
	}
}

// shakyWalk is a 6 s camera clip that shakes, with a tone for its sound.
func (f *flow) shakyWalk() string {
	f.t.Helper()
	return f.ffmpeg("garden-walk.mp4", "-f", "lavfi", "-i", "testsrc=size=720x420:rate=30:duration=6", "-f", "lavfi", "-i", "sine=frequency=330:duration=6",
		"-vf", "crop=640:360:x='40+18*sin(n*1.9)*cos(n*0.7)':y='30+14*sin(n*2.3+1)'", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest")
}

// steadyEngine is how a clip's fixed copy was steadied (its fix step).
func (f *flow) steadyEngine(as *models.Asset) string {
	f.t.Helper()
	var c models.SourceCopy
	if err := f.a.DB.GetByID(context.Background(), store.CSourceCopies, f.a.WorkspaceID(), as.Fixes.Copy.ID, &c); err != nil {
		f.t.Fatal(err)
	}
	for _, s := range c.FixSteps.Steps {
		if s.Fix == models.FixSteady {
			return s.Engine
		}
	}
	return ""
}

// The better steadying (vid.stab, D153), where this machine has it: the
// configured ffmpeg when it was built with vid.stab (Linux distributions'
// are), or on a Mac the pinned build from METARUSH_STABILIZER_ZIP. The copy
// is steadier and says nothing about the basic method.
func TestShakyClipGetsAVidStabCopy(t *testing.T) {
	f := newFlow(t)
	ctx := context.Background()
	stab := &fixes.Stabilizer{Home: f.a.Cfg.Home(), Tools: f.a.Tools}
	if st, _ := stab.Ensure(ctx, nil); !st.Ready {
		t.Skip("no vid.stab here: an ffmpeg built with it (Linux), or METARUSH_STABILIZER_ZIP naming the pinned download (Mac)")
	}
	as := f.ingest(f.shakyWalk())
	f.measure(as)
	v := f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixSteady}})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 {
		t.Fatalf("after ticking: %+v", v)
	}
	f.prepare(v.JobIDs[0])
	as = f.asset(as.ID)
	if !as.Fixed() {
		t.Fatalf("the clip uses its steady copy: %+v", as.Fixes)
	}
	if e := f.steadyEngine(as); e != fixes.EngineVidStab {
		t.Fatalf("steadied with %q, want vid.stab", e)
	}
	v = f.view(as.ID)
	if v.Copy == nil || len(v.Copy.Measures) == 0 || v.Copy.Measures[0].After >= v.Copy.Measures[0].Before ||
		slices.ContainsFunc(v.Copy.Notes, func(n string) bool { return strings.Contains(n, "basic method") }) {
		t.Fatalf("steadier, and nothing about the basic method: %+v", v.Copy)
	}
}

// A voice recording with a hum (D152): Remove hum fits and is measured before
// and after; what the sound fix affects (the words) is heard again.
func TestHummingVoiceGetsAQuietCopy(t *testing.T) {
	f := newFlow(t)
	hum := f.ffmpeg("kitchen-voice.wav", "-f", "lavfi", "-i", "aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t))+0.08*sin(2*PI*60*t)+0.05*sin(2*PI*120*t)+0.03*sin(2*PI*180*t):s=48000:d=8", "-c:a", "pcm_s16le")
	as := f.ingest(hum)
	f.measure(as)
	v := f.view(as.ID)
	if v.Kind != models.SourceVoice {
		t.Fatalf("kind %q", v.Kind)
	}
	row := v.Fixes[slices.IndexFunc(v.Fixes, func(r fixes.Row) bool { return r.ID == models.FixHum })]
	if !row.Fits || row.Why != "There's a hum" {
		t.Fatalf("hum should fit: %+v", row)
	}
	v = f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixHum}})
	f.prepare(v.JobIDs[0])
	v = f.view(as.ID)
	if v.Copy == nil || len(v.Copy.Measures) != 1 || v.Copy.Measures[0].After > v.Copy.Measures[0].Before-10 {
		t.Fatalf("the hum is gone from the copy: %+v", v.Copy)
	}
	if after := v.Fixes[slices.IndexFunc(v.Fixes, func(r fixes.Row) bool { return r.ID == models.FixHum })].After; after == "" {
		t.Fatal("the row says the measurement after the fix")
	}
}
