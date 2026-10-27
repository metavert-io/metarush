package ops

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

// videoDir is this repository's Remotion package, when its dependencies and
// headless browser are installed (the Go CI job has neither; the video
// package's own tests and render self-test cover that side there).
func videoDir(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "..", "..", "video")
	for _, p := range []string{filepath.Join(dir, "..", "node_modules", "esbuild"), filepath.Join(dir, "..", "node_modules", "typescript"), filepath.Join(dir, "node_modules", ".remotion")} {
		if _, err := os.Stat(p); err != nil {
			t.Skipf("the video package isn't installed here (%s); run npm install and npm run browser:ensure -w video", p)
		}
	}
	return dir
}

func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if d.IsDir() {
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		b, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), b, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func code(err error) string { return apperr.From(err).Code }

// The whole loop on one machine: check → refused for Claude and without a
// person → approved by a person → a cut may use it → withdrawn → refused;
// editing one character makes a new version that waits for approval again.
func TestMotionCheckApproveRevoke(t *testing.T) {
	vd := videoDir(t)
	a := testapp.New(t)
	a.Cfg.Tools.VideoDir, a.Cfg.Tools.DevRenderer = vd, true // this checkout (developer setting)
	a.Tools = tools.New(a.Cfg.Tools)
	if !a.Tools.Has(tools.Node) {
		t.Skip("node is not installed")
	}
	ctx := context.Background()
	folder := t.TempDir()
	comp := filepath.Join(folder, "motion", "route-map")
	copyTree(t, filepath.Join(vd, "render", "fixtures", "motion", "route-map"), comp)

	out, err := CallTyped[MotionCheckOut](ctx, a, "motion.check", MotionCheckIn{Dir: comp, Shape: "vertical", NoPreview: true}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if !out.OK || out.State != models.MotionWaiting || len(out.Frames) < 5 || out.Sheet == "" || !strings.HasPrefix(out.Version, "route-map@") {
		t.Fatalf("check: %+v", out)
	}
	if _, err := os.Stat(out.Sheet); err != nil {
		t.Fatalf("contact sheet: %v", err)
	}
	if !strings.Contains(out.Summary, "A person needs to approve this version") {
		t.Fatalf("summary: %q", out.Summary)
	}
	// The archive is in the media store under its own hash: every machine renders the reviewed bytes.
	if _, err := a.Media.Stat(ctx, media.ObjectKey(a.WorkspaceID(), out.Hash)); err != nil {
		t.Fatalf("archive not stored: %v", err)
	}
	ref := out.Version

	// People only: no person (a script, headless Claude Code) …
	if _, err := Call(ctx, a, "motion.approve", mustJSON(MotionDecideIn{Ref: ref}), Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("approve without a person: %v", err)
	}
	// … and never the dashboard assistant, even if something marked it human.
	as := *a
	as.Actor.Via = models.ViaAssistant
	if _, err := Call(ctx, &as, "motion.approve", mustJSON(MotionDecideIn{Ref: ref}), Caller{Human: true}); code(err) != apperr.Unauthorized {
		t.Fatalf("approve on the assistant channel: %v", err)
	}
	if _, err := Call(ctx, &as, "motion.check", mustJSON(MotionCheckIn{Dir: comp}), Caller{}); code(err) != apperr.Unauthorized {
		t.Fatalf("check on the assistant channel: %v", err)
	}
	// From the dashboard, only folders in an assistant conversation are checked.
	if _, err := Call(ctx, a, "motion.check", mustJSON(MotionCheckIn{Dir: comp}), Caller{Human: true, Remote: true}); code(err) != apperr.Unauthorized {
		t.Fatalf("remote check of a local folder: %v", err)
	}

	// A cut can't use it before a person approves it.
	errsFor := func() []string { return checkSpec(t, a, out.Hash) }
	if got := errsFor(); !contains(got, "motion_not_approved") {
		t.Fatalf("unapproved version in a cut: %v", got)
	}
	ap, err := CallTyped[MotionDecideOut](ctx, a, "motion.approve", MotionDecideIn{Ref: ref, Note: "reviewed"}, Caller{Human: true})
	if err != nil || ap.Row.State != models.MotionApproved || !strings.Contains(ap.Summary, "Approved route-map@") {
		t.Fatalf("approve: %v %+v", err, ap)
	}
	if got := errsFor(); contains(got, "motion_not_approved") || contains(got, "motion_unknown") {
		t.Fatalf("approved version in a cut: %v", got)
	}
	show, err := CallTyped[MotionShowOut](ctx, a, "motion.show", MotionShowIn{Ref: ref}, Caller{})
	if err != nil || len(show.Source) != 3 || len(show.Fields) != 3 || show.Fields[0].Name != "from" || show.SheetPath == "" || len(show.Component.Decisions) != 1 {
		t.Fatalf("show: %v %+v", err, show)
	}

	// Revoke works.
	if _, err := CallTyped[MotionDecideOut](ctx, a, "motion.revoke", MotionDecideIn{Ref: ref}, Caller{}); code(err) != apperr.HumanOnly {
		t.Fatalf("revoke without a person: %v", err)
	}
	if _, err := CallTyped[MotionDecideOut](ctx, a, "motion.revoke", MotionDecideIn{Ref: ref}, Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if got := errsFor(); !contains(got, "motion_not_approved") {
		t.Fatalf("revoked version in a cut: %v", got)
	}
	if _, err := CallTyped[MotionDecideOut](ctx, a, "motion.approve", MotionDecideIn{Ref: ref}, Caller{Human: true}); err != nil {
		t.Fatal(err)
	}

	// One character more is another version: it waits for a person again,
	// and the source shows what changed since the approved one.
	idx := filepath.Join(comp, "index.tsx")
	b, _ := os.ReadFile(idx)
	if err := os.WriteFile(idx, []byte(strings.Replace(string(b), "both names.", "both names!", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	next, err := CallTyped[MotionCheckOut](ctx, a, "motion.check", MotionCheckIn{Dir: comp, NoPreview: true}, Caller{})
	if err != nil || !next.OK || next.Hash == out.Hash || next.State != models.MotionWaiting {
		t.Fatalf("edited: %v %+v", err, next)
	}
	ns, err := CallTyped[MotionShowOut](ctx, a, "motion.show", MotionShowIn{Ref: next.Version}, Caller{})
	if err != nil || ns.Previous == nil || ns.Previous.Hash != out.Hash {
		t.Fatalf("previous approved version: %v %+v", err, ns.Previous)
	}
	changed := 0
	for _, f := range ns.Source {
		if f.Change == "changed" && f.Path == "index.tsx" && len(f.Changes) > 0 {
			changed++
		}
	}
	if changed != 1 {
		t.Fatalf("the diff against the approved version: %+v", ns.Source)
	}
	list, err := CallTyped[MotionListOut](ctx, a, "motion.list", MotionListIn{All: true}, Caller{})
	if err != nil || len(list.Components) != 2 || list.Waiting != 1 {
		t.Fatalf("list: %v %+v", err, list)
	}
}

// A component that breaks the rules never gets recorded, with located problems.
func TestMotionCheckRefusesForbiddenImports(t *testing.T) {
	vd := videoDir(t)
	a := testapp.New(t)
	a.Cfg.Tools.VideoDir, a.Cfg.Tools.DevRenderer = vd, true // this checkout (developer setting)
	a.Tools = tools.New(a.Cfg.Tools)
	if !a.Tools.Has(tools.Node) {
		t.Skip("node is not installed")
	}
	comp := filepath.Join(t.TempDir(), "motion", "sneaky")
	if err := os.MkdirAll(comp, 0o755); err != nil {
		t.Fatal(err)
	}
	src := "import { AbsoluteFill } from 'remotion';\nimport { execSync } from 'child_process';\nimport { z } from 'zod';\nexport const schema = z.object({});\nexport const meta = { summary: 'x', region: 'center', minDuration: 1, maxDuration: 2 };\nexport default function S() { return <AbsoluteFill>{String(execSync('id'))}</AbsoluteFill>; }\n"
	if err := os.WriteFile(filepath.Join(comp, "index.tsx"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := CallTyped[MotionCheckOut](context.Background(), a, "motion.check", MotionCheckIn{Dir: comp, NoPreview: true}, Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if out.OK || out.Stage != "scan" || out.State != "" || len(out.Problems) == 0 || out.Problems[0].Line != 2 || !strings.Contains(out.Problems[0].Message, "child_process is part of Node.js") {
		t.Fatalf("forbidden import: %+v", out)
	}
	if !strings.Contains(out.Summary, "index.tsx line 2") {
		t.Fatalf("summary names the line: %q", out.Summary)
	}
}

// checkSpec validates a one-clip cut that uses a motion graphic and returns
// the error codes (the footage doesn't exist here; only the motion codes matter).
func checkSpec(t *testing.T, a *app.App, hash string) []string {
	t.Helper()
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical},
		Clips:    []editspec.Clip{{ID: "a", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz0", In: 0, Out: 2}},
		Overlays: []editspec.Overlay{{ID: "route", Motion: &editspec.MotionRef{Name: "route-map", Hash: hash}, At: 0.2, Duration: 3, Params: json.RawMessage(`{"from":"Lisbon","to":"Porto"}`)}}}
	editspec.Normalize(s)
	facts, err := loadFacts(context.Background(), a, "", s)
	if err != nil {
		t.Fatal(err)
	}
	var codes []string
	for _, is := range editspec.Validate(s, facts, nil, false) {
		if is.Severity == "error" {
			codes = append(codes, is.Code)
		}
	}
	return codes
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}
