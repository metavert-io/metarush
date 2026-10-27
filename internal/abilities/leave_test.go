package abilities

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/store"
)

// What may leave (D290) for abilities: a client's words kept from abilities
// refuse a run that sends them (the plan says it; starting it is refused and
// recorded); a list of hosts lets the run reach only those (the proxy
// refuses the others, request by request).
func TestRunsFollowWhatMayLeave(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // this terminal is a person's, not Claude Code's
	a := newApp(t)
	needSandbox(t, a)
	testInternet(t, "api.stock.test", "cdn.stock.test")
	approved(t, a, map[string]string{ManifestFile: stockManifest, "fetch.py": stockScript})
	human := ops.Caller{Human: true}
	key := "stock-" + strings.Repeat("7c1d", 6) // secret-scan: allow (made up)
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "stock-photos", Keys: map[string]string{"stock": key}}, human)
	mustCall[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Base"}, ops.Caller{})
	acme := mustCall[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, ops.Caller{}).Project
	mustCall[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: acme.ID}, ops.Caller{})
	mustCall[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Keep: map[string][]string{"abilities": {"words"}}}, human)

	in := AbilityPlanIn{Ref: "stock-photos", Inputs: map[string]any{"words": "ocean"}, ProjectID: acme.ID}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if p.Ready || !strings.Contains(strings.Join(p.Problems, " "), "keep words on this computer") {
		t.Fatalf("the plan says it: %+v", p.Problems)
	}
	if _, err := call[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, human); apperr.From(err).Code != apperr.KeptHere {
		t.Fatalf("starting it: %v", err)
	}
	if n, _ := a.DB.C(store.CAudit).CountDocuments(context.Background(), bson.M{"workspaceId": a.WorkspaceID(), "action": leave.AuditRefused}); n != 1 {
		t.Fatalf("the refusal is recorded once (the plan only says it): %d", n)
	}
	// In another project the same run is fine.
	if p := mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: "stock-photos", Inputs: map[string]any{"words": "ocean"}}, ops.Caller{}); !p.Ready {
		t.Fatalf("elsewhere: %+v", p.Problems)
	}
	// Words may go, but only to api.stock.test: the proxy refuses the CDN.
	mustCall[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Let: map[string][]string{"abilities": {"words"}}, Only: map[string][]string{"abilities": {"api.stock.test"}}}, human)
	p = mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
	if !p.Ready || !strings.Contains(p.Leaves, "MetaRush will refuse its requests to cdn.stock.test") {
		t.Fatalf("the plan names the host it will refuse: %+v", p)
	}
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, human)
	if _, err := runJob(t, a, run.JobID); err != nil {
		t.Fatal(err)
	}
	out := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{})
	var keyed bool
	var cdn string
	for _, q := range out.Requests {
		if q.Host == "api.stock.test" && q.Refused == "" {
			keyed = true
		}
		if q.Host == "cdn.stock.test" {
			cdn = q.Refused
		}
	}
	if !keyed || !strings.Contains(cdn, "allow only api.stock.test among abilities") {
		t.Fatalf("requests: %+v\n%s", out.Requests, out.Log)
	}
}

// An ability's one service on this computer (D284) isn't a place things
// leave to: a project whose rules let nothing leave still runs it, and its
// requests there pass and aren't on the receipt; the ability's other hosts
// are refused, request by request.
func TestLocalServiceStaysHere(t *testing.T) {
	t.Setenv("CLAUDECODE", "") // this terminal is a person's, not Claude Code's
	a := starterApp(t)
	fakeHosts(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/system_stats" {
			writeJSON(w, map[string]any{"system": map[string]any{"comfyui_version": "0.3.0-fake"}, "devices": []any{}})
			return
		}
		writeJSON(w, map[string]any{"got": r.URL.Query().Get("q")})
	}), "http://127.0.0.1:8188", "api.stock.test")
	comfy := models.AbilityLocalProgram{PID: 4242, Name: "python3.12", Path: "/opt/example/python/bin/python3.12", Interpreter: true,
		Cwd: "/opt/example/ComfyUI", Script: "/opt/example/ComfyUI/main.py"}
	listening(t, comfy)
	localRecheck = 0
	t.Cleanup(func() { localRecheck = 3 * time.Second })
	human := ops.Caller{Human: true}
	chk := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: localWordsManifest, "send.py": localWordsScript})}, ops.Caller{})
	if !chk.OK {
		t.Fatalf("the check: %v", problemTexts(chk.Problems))
	}
	mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: chk.Name + "@" + chk.Version, LocalService: true, LocalProgram: &comfy}, human)
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: chk.Name}, human)
	base := mustCall[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Base"}, ops.Caller{}).Project
	acme := mustCall[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, ops.Caller{}).Project
	mustCall[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: acme.ID}, ops.Caller{})
	yes := true
	mustCall[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Nothing: &yes}, human)

	run := func(projectID string) *AbilityRunsOut {
		t.Helper()
		in := AbilityPlanIn{Ref: chk.Name, Inputs: map[string]any{"words": "ocean"}, ProjectID: projectID}
		p := mustCall[AbilityPlanOut](t, a, "ability.plan", in, ops.Caller{})
		if !p.Ready {
			t.Fatalf("the plan: %v", p.Problems)
		}
		if projectID == acme.ID && !strings.Contains(p.Leaves, "MetaRush will refuse its requests to api.stock.test") {
			t.Fatalf("the plan names the host it will refuse: %s", p.Leaves)
		}
		r := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: in, Plan: p.Fingerprint}, human)
		_, err := runJob(t, a, r.JobID)
		out := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: r.ID}, ops.Caller{})
		if err != nil {
			t.Fatalf("the run: %v\n%s\n%+v", err, out.Log, out.Requests)
		}
		return out
	}
	reached := func(out *AbilityRunsOut) (here, there string) {
		here, there = "no request", "no request"
		for _, q := range out.Requests {
			if q.Host == "127.0.0.1" && q.Port == 8188 {
				here = q.Refused
			}
			if q.Host == "api.stock.test" {
				there = q.Refused
			}
		}
		return here, there
	}
	// Nothing may leave: the service here answers; the host elsewhere is refused.
	out := run(acme.ID)
	if here, there := reached(out); here != "" || !strings.Contains(there, "MetaRush didn't send the request") {
		t.Fatalf("requests: %+v\n%s", out.Requests, out.Log)
	}
	l := mustCall[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: acme.ID}, ops.Caller{})
	if !l.Left.Nothing || len(l.Left.Places) != 0 {
		t.Fatalf("nothing of Acme left: %+v", l.Left)
	}
	// No rules: both answer, and the receipt lists only the host elsewhere.
	out = run(base.ID)
	if here, there := reached(out); here != "" || there != "" {
		t.Fatalf("requests: %+v\n%s", out.Requests, out.Log)
	}
	l = mustCall[receipt.ProjectLeavesOut](t, a, "project.leaves", receipt.ProjectLeavesIn{ProjectID: base.ID}, ops.Caller{})
	if len(l.Left.Places) != 1 || l.Left.Places[0].Place != models.PlaceAbilities || !strings.Contains(l.Left.Places[0].To, "api.stock.test") {
		t.Fatalf("what left: %+v", l.Left)
	}
}

const localWordsManifest = `ability: 1
name: local-words
title: Words here and elsewhere
does: Hands a few words to a service on this computer and to one elsewhere.
runs: python3 send.py
inputs:
  words: {kind: words, max: 100}
outputs: [picture]
cost: free
network: metarush
local: {port: 8188, service: ComfyUI, probe: {path: /system_stats, expect: [system.comfyui_version, devices]}}
hosts: [api.stock.test]
sends: [words]
needs: {programs: [python3]}
licence: MIT
test: {inputs: {words: test}}
`

const localWordsScript = `import json, os, sys, urllib.request
if os.environ.get("METARUSH_TEST"):
    print("offline check"); sys.exit(0)
words = json.load(open(os.environ["METARUSH_INPUTS"]))["words"]
for url in [os.environ.get("METARUSH_LOCAL_URL", "http://127.0.0.1:8188") + "/hello?q=" + words, "https://api.stock.test/search?q=" + words]:
    try:
        print("answer", url, urllib.request.urlopen(url, timeout=20).read().decode())
    except Exception as e:
        print("refused", url, e)
open(os.path.join(os.environ["METARUSH_OUTPUT"], "photo.svg"), "w").write('<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"></svg>')
`

// Ability files (D292): exported here, brought in elsewhere as a draft that
// is never approved or set up; checked there, an owner approves it.
func TestAbilityFiles(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	human := ops.Caller{Human: true}
	ok := approved(t, a, map[string]string{ManifestFile: cardManifest, "make.py": cardScript, "notes.md": "Short words read best."})
	dir := t.TempDir()
	file := filepath.Join(dir, "card.ability.zip")
	ex := mustCall[AbilityExportOut](t, a, "ability.export", AbilityExportIn{Ref: "title-card", Out: file}, ops.Caller{})
	if ex.Version != ok.Version || ex.Number != 1 || len(ex.Lineage) != 1 || ex.Bytes == 0 {
		t.Fatalf("export: %+v", ex)
	}
	if _, err := call[AbilityExportOut](t, a, "ability.export", AbilityExportIn{Ref: "title-card", Out: file}, ops.Caller{}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("never written over without force: %v", err)
	}
	// The same version here: nothing new.
	same := mustCall[AbilityImportOut](t, a, "ability.import", AbilityImportIn{File: file}, ops.Caller{})
	if same.New || same.State != models.AbilityApproved {
		t.Fatalf("already here: %+v", same)
	}

	// Another workspace brings it in.
	b := newApp(t)
	dry := mustCall[AbilityImportOut](t, b, "ability.import", AbilityImportIn{File: file, DryRun: true}, ops.Caller{})
	if !dry.New || dry.Number != 0 || dry.From == nil || len(dry.From.Decisions) != 1 || dry.From.Decisions[0].State != models.AbilityApproved {
		t.Fatalf("dry run: %+v", dry)
	}
	if vs, _ := versionsOf(context.Background(), b, "title-card"); len(vs) != 0 {
		t.Fatal("a dry run keeps nothing")
	}
	in := mustCall[AbilityImportOut](t, b, "ability.import", AbilityImportIn{File: file}, ops.Caller{})
	if !in.New || in.Number != 1 || in.State != models.AbilityImported || in.Version != ok.Version || !strings.Contains(in.Next, "ability check --version") {
		t.Fatalf("import: %+v", in)
	}
	// Never approved there by the file: an owner can't approve it unchecked,
	// and it can't run.
	if _, err := call[AbilityDecisionOut](t, b, "ability.approve", AbilityApproveIn{Ref: "title-card"}, human); apperr.From(err).Code != apperr.Validation || !strings.Contains(err.Error(), "hasn't been checked") {
		t.Fatalf("approving it unchecked: %v", err)
	}
	if _, err := call[AbilityPlanOut](t, b, "ability.plan", AbilityPlanIn{Ref: "title-card"}, ops.Caller{}); err == nil {
		t.Fatal("nothing approved here: no plan")
	}
	chk := mustCall[AbilityCheckOut](t, b, "ability.check", AbilityCheckIn{Version: "title-card@" + Short(in.Version)}, ops.Caller{})
	if !chk.OK || chk.State != models.AbilityWaiting || chk.Number != 1 {
		t.Fatalf("checked here: %+v", chk)
	}
	d := mustCall[AbilityDecisionOut](t, b, "ability.approve", AbilityApproveIn{Ref: "title-card@" + Short(in.Version)}, human)
	if d.State != models.AbilityApproved {
		t.Fatalf("approved there by its own owner: %+v", d)
	}

	// An unrelated ability of the same name is refused unless asked.
	other := approvedIn(t, b, strings.Replace(cardManifest, "Makes a plain title card", "Makes another title card", 1))
	file2 := filepath.Join(dir, "other.ability.zip")
	mustCall[AbilityExportOut](t, b, "ability.export", AbilityExportIn{Ref: "title-card@" + Short(other.Version), Out: file2}, ops.Caller{})
	c := newApp(t)
	approvedIn(t, c, strings.Replace(cardManifest, "Makes a plain title card", "Makes a third kind of card", 1))
	if _, err := call[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: file2}, ops.Caller{}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("an unrelated one of the same name: %v", err)
	}
	if got := mustCall[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: file2, AsVersion: true}, ops.Caller{}); got.Number != 2 || !got.Of {
		t.Fatalf("as a new version when asked: %+v", got)
	}
	// A file whose files aren't the version it names, or that holds anything
	// else, is refused.
	for name, change := range map[string]func(name string, b []byte) (string, []byte){
		"changed": func(name string, b []byte) (string, []byte) {
			return name, bytes.Replace(b, []byte("Short words read best."), []byte("Long words read best."), 1)
		},
		"extra": func(name string, b []byte) (string, []byte) {
			if name == NotesFile {
				return "run.sh", b
			}
			return name, b
		},
	} {
		badFile := filepath.Join(dir, name+".ability.zip")
		rewriteZip(t, file, badFile, change)
		if _, err := call[AbilityImportOut](t, c, "ability.import", AbilityImportIn{File: badFile}, ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
			t.Fatalf("%s: %v", name, err)
		}
	}
}

// rewriteZip copies a zip, changing each file's name and bytes.
func rewriteZip(t *testing.T, from, to string, change func(name string, b []byte) (string, []byte)) {
	t.Helper()
	zr, err := zip.OpenReader(from)
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		name, nb := change(f.Name, b)
		w, _ := zw.Create(name)
		_, _ = w.Write(nb)
	}
	_ = zw.Close()
	if err := os.WriteFile(to, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

// approvedIn checks, approves and sets up a version of the title card.
func approvedIn(t *testing.T, a *app.App, manifest string) *AbilityCheckOut {
	t.Helper()
	return approved(t, a, map[string]string{ManifestFile: manifest, "make.py": cardScript})
}
