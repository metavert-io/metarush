package abilities

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The forwarder runs as this test binary inside a Linux sandbox (runs
// through MetaRush's proxy).
func TestMain(m *testing.M) {
	if len(os.Args) > 1 && os.Args[1] == sandbox.ForwardCommand {
		os.Exit(sandbox.ForwardMain(os.Args[2:]))
	}
	os.Exit(m.Run())
}

// newApp opens a test app whose keys live in its own home's .env (never
// this computer's keychain).
func newApp(t *testing.T) *app.App {
	t.Helper()
	a := testapp.New(t)
	a.Cfg.Secrets = "env"
	// Keeping a key in the home's .env also sets it in this process: each
	// test starts without the test keys and leaves none behind.
	for _, k := range []string{"METARUSH_STOCK_API_KEY", "METARUSH_CARDS_API_KEY"} {
		t.Setenv(k, "")
	}
	return a
}

// needSandbox skips (or, with METARUSH_TEST_SANDBOX=require, fails) where
// this computer can't run a program apart, or has no python3.
func needSandbox(t *testing.T, a *app.App) {
	t.Helper()
	iso := sandbox.Here(a.Tools)
	why := ""
	if !iso.OK() {
		why = "no sandbox on this computer: " + iso.Problem
	} else if _, err := exec.LookPath("python3"); err != nil {
		why = "no python3 on this computer"
	}
	if why == "" {
		return
	}
	if os.Getenv("METARUSH_TEST_SANDBOX") == "require" {
		t.Fatal(why)
	}
	t.Skip(why)
}

const pictureSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="64" height="36"><rect width="64" height="36" fill="#264653"/></svg>`

// cardManifest is a local ability: a picture made from words, no network.
const cardManifest = `ability: 1
name: title-card
title: Title card
does: Makes a plain title card picture from a few words.
runs: python3 make.py
inputs:
  words: {kind: words, max: 80, says: The words on the card}
outputs: [picture]
cost: free
needs: {programs: [python3]}
licence: MIT
search: [title, card, picture]
test: {inputs: {words: Hello}}
`

const cardScript = `import json, os, sys
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
out = os.environ["METARUSH_OUTPUT"]
words = inputs["words"]
if os.environ.get("BREAK") or words == "break":
    sys.exit(4)
svg = '` + pictureSVG + `'.replace("</svg>", "<text>" + words + "</text></svg>")
open(os.path.join(out, "card.svg"), "w").write(svg)
json.dump({"files": [{"path": "card.svg", "title": "Card: " + words, "licence": "CC0"}]}, open(os.path.join(out, "result.json"), "w"))
print("made a card for", words)
`

func call[O any](t *testing.T, a *app.App, name string, in any, c ops.Caller) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, c)
}

func mustCall[O any](t *testing.T, a *app.App, name string, in any, c ops.Caller) *O {
	t.Helper()
	out, err := call[O](t, a, name, in, c)
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func todosOf(t *testing.T, a *app.App, kind string) []models.Todo {
	t.Helper()
	var ts []models.Todo
	if err := a.DB.Find(context.Background(), store.CTodos, a.WorkspaceID(), bson.M{"kind": kind}, &ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

func TestCheckApproveRevoke(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	human := ops.Caller{Human: true}
	dir := writeFolder(t, map[string]string{ManifestFile: cardManifest, "make.py": cardScript, "notes.md": "Short words read best."})

	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: dir}, ops.Caller{})
	if !out.OK || !out.Recorded || !out.New || out.Number != 1 || out.State != models.AbilityWaiting || out.Test == nil || !out.Test.OK {
		b, _ := json.MarshalIndent(out, "", "  ")
		t.Fatalf("a good ability passes its check and waits:\n%s", b)
	}
	if len(out.Test.Outputs) != 1 || out.Test.Outputs[0].Kind != OutPicture || !strings.Contains(out.Test.Log, "made a card for Hello") {
		t.Fatalf("the test run's picture and log: %+v", out.Test)
	}
	// The same folder again is the same version.
	again := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: dir}, ops.Caller{})
	if again.New || again.Version != out.Version || again.Number != 1 {
		t.Fatalf("checked again: %+v", again)
	}
	// An owner is asked: a to-do and the bell.
	if ts := todosOf(t, a, models.TodoAbilityApproval); len(ts) != 1 || ts[0].State != models.TodoOpen || !ts[0].HumanOnly || ts[0].Context.Ability.Version != out.Version {
		t.Fatalf("the to-do: %+v", ts)
	}
	var ns []models.Notification
	_ = a.DB.Find(context.Background(), store.CNotifications, a.WorkspaceID(), bson.M{"kind": models.NotifyAbilityWaiting}, &ns)
	if len(ns) != 1 || !strings.Contains(ns[0].Title, "Title card") {
		t.Fatalf("the bell: %+v", ns)
	}

	// Lists, finds and shows.
	list := mustCall[AbilityListOut](t, a, "ability.list", AbilityListIn{}, ops.Caller{})
	if len(list.Abilities) != 1 || list.Waiting != 1 || list.Abilities[0].Decides != DecideSafe || list.Abilities[0].Cost != "free" {
		t.Fatalf("list: %+v", list)
	}
	if f := mustCall[AbilityFindOut](t, a, "ability.find", AbilityFindIn{Words: "title cards"}, ops.Caller{}); len(f.Matches) != 0 {
		t.Fatalf("find leaves out versions nobody approved: %+v", f)
	}
	if f := mustCall[AbilityFindOut](t, a, "ability.find", AbilityFindIn{Words: "title cards", All: true}, ops.Caller{}); len(f.Matches) != 1 || !strings.Contains(f.Matches[0].Line, "Title card") {
		t.Fatalf("find --all: %+v", f)
	}
	show := mustCall[AbilityShowOut](t, a, "ability.show", AbilityRefIn{Ref: "title-card"}, ops.Caller{})
	if !show.CanApprove || show.Page.Leaves != "Nothing leaves this computer." || show.Page.Cost != "Free" || show.Ability.Notes == "" || show.Here.SetUp {
		t.Fatalf("show: %+v", show.Page)
	}

	// Approving: owners, people only, never Claude.
	ref := "title-card@" + Short(out.Version)
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("without a person: %v", err)
	}
	member := ops.OverHTTP(context.Background(), func() bool { return false })
	raw, _ := json.Marshal(AbilityApproveIn{Ref: ref})
	if _, err := ops.Call(member, a, "ability.approve", raw, ops.Caller{Human: true, Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("a member: %v", err)
	}
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	if _, err := call[AbilityDecisionOut](t, &claude, "ability.approve", AbilityApproveIn{Ref: ref}, human); err == nil {
		t.Fatal("Claude can't approve")
	}
	d := mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: ref, Note: "looks fine"}, human)
	if d.State != models.AbilityApproved {
		t.Fatalf("approved: %+v", d)
	}
	if ts := todosOf(t, a, models.TodoAbilityApproval); ts[0].State == models.TodoOpen {
		t.Fatal("deciding closes the to-do")
	}
	if f := mustCall[AbilityFindOut](t, a, "ability.find", AbilityFindIn{Words: "picture"}, ops.Caller{}); len(f.Matches) != 1 {
		t.Fatalf("an approved ability is found: %+v", f)
	}

	// A changed folder is a new version, which waits; the approved one stays.
	changed := writeFolder(t, map[string]string{ManifestFile: strings.Replace(cardManifest, "max: 80", "max: 90", 1), "make.py": cardScript})
	v2 := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: changed}, ops.Caller{})
	if v2.Number != 2 || v2.State != models.AbilityWaiting {
		t.Fatalf("a new version: %+v", v2)
	}
	list = mustCall[AbilityListOut](t, a, "ability.list", AbilityListIn{}, ops.Caller{})
	if r := list.Abilities[0]; r.Approved == nil || r.Approved.Number != 1 || r.WaitingVersion != 2 {
		t.Fatalf("the list shows the approved version and the waiting one: %+v", r)
	}
	if v, err := resolve(context.Background(), a, "title-card", true); err != nil || v.Number != 1 {
		t.Fatalf("a run takes the newest approved version: %+v %v", v, err)
	}

	// Taking it back.
	if _, err := call[AbilityDecisionOut](t, a, "ability.revoke", AbilityRevokeIn{Ref: ref}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("revoke without a person: %v", err)
	}
	r := mustCall[AbilityDecisionOut](t, a, "ability.revoke", AbilityRevokeIn{Ref: ref}, human)
	if r.State != models.AbilityRevoked {
		t.Fatalf("revoked: %+v", r)
	}
	if _, err := resolve(context.Background(), a, "title-card", true); err == nil {
		t.Fatal("nothing approved any more")
	}
}

func TestCheckProblems(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	// A manifest with problems: nothing kept.
	bad := writeFolder(t, map[string]string{ManifestFile: strings.Replace(cardManifest, "licence: MIT\n", "", 1), "make.py": cardScript})
	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: bad}, ops.Caller{})
	if out.OK || out.Recorded || len(out.Problems) == 0 || !strings.Contains(out.Problems[0].String(), "licence") {
		t.Fatalf("problems, nothing kept: %+v", out)
	}
	// A test that fails: kept as failed, never approvable.
	failing := writeFolder(t, map[string]string{ManifestFile: strings.Replace(cardManifest, "words: Hello", "words: break", 1), "make.py": cardScript})
	out = mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: failing}, ops.Caller{})
	if out.OK || !out.Recorded || out.State != models.AbilityFailed || out.Test == nil || out.Test.Exit != 4 {
		t.Fatalf("a failing test: %+v", out)
	}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: "title-card"}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Validation {
		t.Fatalf("a failed version can't be approved: %v", err)
	}
	if ts := todosOf(t, a, models.TodoAbilityApproval); len(ts) != 0 {
		t.Fatalf("nobody is asked about a failed version: %+v", ts)
	}
	// Writing something it doesn't declare fails the check.
	sneaky := strings.Replace(cardScript, `"card.svg"), "w").write(svg)`, `"card.svg"), "w").write(svg); open(os.path.join(out, "notes.txt"), "w").write("x")`, 1)
	out = mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: cardManifest, "make.py": sneaky})}, ops.Caller{})
	if out.OK || !strings.Contains(strings.Join(problemTexts(out.Problems), " "), "notes.txt") {
		t.Fatalf("an output it doesn't declare: %+v", out.Problems)
	}
	// The test runs with the network off and outside the person's home.
	nosy := strings.Replace(cardScript, "words = inputs[\"words\"]", `words = inputs["words"]
import urllib.request
try:
    urllib.request.urlopen("http://example.com", timeout=3); print("REACHED")
except Exception as e:
    print("no network")
try:
    os.listdir(os.path.expanduser("~real")); print("READ-HOME")
except Exception:
    pass`, 1)
	out = mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: strings.Replace(cardManifest, "max: 80", "max: 81", 1), "make.py": nosy})}, ops.Caller{})
	if !out.OK || strings.Contains(out.Test.Log, "REACHED") || !strings.Contains(out.Test.Log, "no network") {
		t.Fatalf("the network is off in the check: %+v\n%s", out.Problems, out.Test.Log)
	}
}

func problemTexts(ps []Problem) []string {
	var out []string
	for _, p := range ps {
		out = append(out, p.String())
	}
	return out
}

func TestSetup(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	keyed := strings.Replace(cardManifest, "cost: free", "cost: free\nkeys: [{name: cards, hosts: [api.example.com]}]\nhosts: [api.example.com]\nsends: [words]", 1)
	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: keyed, "make.py": cardScript})}, ops.Caller{})
	if !out.OK {
		t.Fatalf("check: %+v", out.Problems)
	}
	if _, err := call[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card"}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("setup is for a person: %v", err)
	}
	human := ops.Caller{Human: true}
	s := mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card"}, human)
	if !s.Here.SetUp || len(s.Here.Programs) != 1 || !s.Here.Programs[0].OK || s.Here.Ready || !strings.Contains(strings.Join(s.Here.Problems, " "), "no cards key") {
		t.Fatalf("set up, but no key yet: %+v", s.Here)
	}
	key := "cards-" + strings.Repeat("k3y", 8) // secret-scan: allow (made up)
	member := ops.OverHTTP(context.Background(), func() bool { return false })
	raw, _ := json.Marshal(AbilitySetupIn{Ref: "title-card", Keys: map[string]string{"cards": key}})
	if _, err := ops.Call(member, a, "ability.setup", raw, ops.Caller{Human: true, Remote: true}); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("only an owner adds keys: %v", err)
	}
	s = mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card", Keys: map[string]string{"cards": key}}, human)
	if !s.Here.Keys[0].Configured || strings.Contains(s.Here.Keys[0].Masked, key) {
		t.Fatalf("the key, masked: %+v", s.Here.Keys)
	}
	// Nothing the database keeps holds the key.
	var all []bson.M
	for _, c := range []string{store.CAbilities, store.CAbilitySetups, store.CAudit} {
		_ = a.DB.Find(context.Background(), c, a.WorkspaceID(), bson.M{}, &all)
		b, _ := json.Marshal(all)
		if strings.Contains(string(b), key) {
			t.Fatalf("%s holds the key", c)
		}
	}
	if _, err := call[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card", Programs: map[string]string{"node": "/usr/bin/true"}}, human); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a program it doesn't run: %v", err)
	}
	s = mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card", Forget: true}, human)
	if s.Here.SetUp {
		t.Fatal("forgotten")
	}
}
