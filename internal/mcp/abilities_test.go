package mcp

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sandbox"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

const titleCard = `ability: 1
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

const titleCardScript = `import json, os
inputs = json.load(open(os.environ["METARUSH_INPUTS"]))
out = os.environ["METARUSH_OUTPUT"]
open(os.path.join(out, "card.svg"), "w").write('<svg xmlns="http://www.w3.org/2000/svg" width="8" height="8"/>')
`

// An ability is found, described and run through the core (D268): refused
// with a plain line until an owner approved it and a person set it up here,
// a change only run_change makes — confirmed in two steps — and planned
// first.
func TestAbilitiesThroughTheCore(t *testing.T) {
	a := viaMCP(testapp.New(t))
	a.Cfg.Secrets = "env"
	if iso := sandbox.Here(a.Tools); !iso.OK() {
		t.Skip("no sandbox on this computer: " + iso.Problem)
	}
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("no python3")
	}
	ctx := context.Background()
	dir := t.TempDir()
	for name, body := range map[string]string{"ability.yaml": titleCard, "make.py": titleCardScript} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	chk, err := ops.CallTyped[abilities.AbilityCheckOut](ctx, a, "ability.check", abilities.AbilityCheckIn{Path: dir}, ops.Caller{})
	if err != nil || !chk.OK {
		t.Fatalf("check: %+v %v", chk, err)
	}
	c := start(t, a, Options{AllowChanges: true, TokenTTL: time.Minute})
	c.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	line := func() string {
		t.Helper()
		for _, l := range strings.Split(text(c.call("find", map[string]any{"task": "a title card picture"})), "\n") {
			if strings.HasPrefix(l, "title-card — ") {
				return l
			}
		}
		t.Fatal("find doesn't list the ability")
		return ""
	}
	if l := line(); !strings.Contains(l, "needs an owner's approval") || !strings.Contains(l, "changes something") {
		t.Fatalf("waiting: %s", l)
	}
	in := map[string]any{"inputs": map[string]any{"words": "Hi"}, "plan": strings.Repeat("0", 64)}
	if r := c.call("run_change", map[string]any{"name": "title-card", "input": in}); errCode(r) != apperr.NotConfigured || !strings.Contains(text(r), "needs an owner's approval") {
		t.Fatalf("run before approval: %v", r)
	}

	human := ops.Caller{Human: true}
	if _, err := ops.CallTyped[abilities.AbilityDecisionOut](ctx, a, "ability.approve", abilities.AbilityApproveIn{Ref: "title-card@" + chk.Version}, human); err != nil {
		t.Fatal(err)
	}
	if l := line(); !strings.Contains(l, "needs setting up on this computer") {
		t.Fatalf("not set up: %s", l)
	}
	if _, err := ops.CallTyped[abilities.AbilitySetupOut](ctx, a, "ability.setup", abilities.AbilitySetupIn{Ref: "title-card"}, human); err != nil {
		t.Fatal(err)
	}
	if l := line(); strings.Contains(l, "needs") || !strings.Contains(l, "changes something") {
		t.Fatalf("ready: %s", l)
	}
	d := c.call("describe", map[string]any{"name": "title-card"})["structuredContent"].(map[string]any)
	if d["runWith"] != "ability.run" || !strings.Contains(d["note"].(string), "ability.plan") {
		t.Fatalf("describe: %v", d)
	}
	// Planned first, through run (a read).
	p := c.call("run", map[string]any{"name": "ability.plan", "input": map[string]any{"ref": "title-card", "inputs": map[string]any{"words": "Hi"}}})
	fp, _ := p["structuredContent"].(map[string]any)["fingerprint"].(string)
	if p["isError"] != false || len(fp) != 64 {
		t.Fatalf("plan: %v", p)
	}
	in["plan"] = fp
	// run only reads: an ability run is a change.
	if r := c.call("run", map[string]any{"name": "title-card", "input": in}); errCode(r) != apperr.Unauthorized || !strings.Contains(text(r), "only reads") {
		t.Fatalf("run made a change: %v", r)
	}
	first := c.call("run_change", map[string]any{"name": "title-card", "input": in})
	token, _ := first["structuredContent"].(map[string]any)["confirmToken"].(string)
	if errCode(first) != apperr.ConfirmRequired || token == "" {
		t.Fatalf("a run is confirmed like any change: %v", first)
	}
	r := c.call("run_change", map[string]any{"name": "title-card", "input": in, "confirmToken": token})
	if r["isError"] != false || r["structuredContent"].(map[string]any)["status"] != "queued" {
		t.Fatalf("run: %v", r)
	}
	// One that uses a key is a person's to start: run_change refuses it as
	// that, with the command a person runs, before asking for a token.
	keyedDir := t.TempDir()
	keyed := strings.Replace(strings.Replace(titleCard, "name: title-card", "name: key-card", 1), "cost: free",
		"cost: free\nkeys: [{name: cards, hosts: [api.example.com]}]\nhosts: [api.example.com]\nsends: [words]", 1)
	for name, body := range map[string]string{"ability.yaml": keyed, "make.py": titleCardScript} {
		if err := os.WriteFile(filepath.Join(keyedDir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	kc, err := ops.CallTyped[abilities.AbilityCheckOut](ctx, a, "ability.check", abilities.AbilityCheckIn{Path: keyedDir}, ops.Caller{})
	if err != nil || !kc.OK {
		t.Fatalf("check the keyed one: %+v %v", kc, err)
	}
	if _, err := ops.CallTyped[abilities.AbilityDecisionOut](ctx, a, "ability.approve", abilities.AbilityApproveIn{Ref: "key-card@" + kc.Version}, human); err != nil {
		t.Fatal(err)
	}
	key := "cards-" + strings.Repeat("k3y", 8) // secret-scan: allow (made up)
	t.Setenv("METARUSH_CARDS_API_KEY", "")
	if _, err := ops.CallTyped[abilities.AbilitySetupOut](ctx, a, "ability.setup", abilities.AbilitySetupIn{Ref: "key-card", Keys: map[string]string{"cards": key}}, human); err != nil {
		t.Fatal(err)
	}
	kr := c.call("run_change", map[string]any{"name": "key-card", "input": map[string]any{"inputs": map[string]any{"words": "Hi"}, "plan": strings.Repeat("0", 64)}})
	body, _ := kr["structuredContent"].(map[string]any)["error"].(map[string]any)
	details, _ := body["details"].(map[string]any)
	if errCode(kr) != apperr.HumanOnly || details["command"] != "metarush ability run key-card" || !strings.Contains(text(kr), "Only a person starts key-card") {
		t.Fatalf("a person's ability through run_change: %v", kr)
	}
	if strings.Contains(fmt.Sprint(kr), key) {
		t.Fatal("the key showed up in an answer")
	}
	// A read-only connection lists it, marked, and runs nothing.
	ro := start(t, a, Options{})
	ro.request("initialize", map[string]any{"protocolVersion": "2025-11-25", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "test", "version": "1"}})
	if r := ro.call("run", map[string]any{"name": "title-card", "input": in}); errCode(r) != apperr.Unauthorized {
		t.Fatalf("read-only ran an ability: %v", r)
	}
}
